package azure

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

type protectionPolicyFixture struct {
	*protectionFixture
	deletes, status, readFault int
	headers                    http.Header
	body                       string
	otherPolicy                string
}

func newProtectionPolicyFixture(t *testing.T) *protectionPolicyFixture {
	t.Helper()
	f := &protectionPolicyFixture{protectionFixture: newProtectionFixture(t), status: 200, headers: http.Header{}, otherPolicy: ""}
	f.otherPolicy = f.policy + "-retained"
	other := batchClone(f.objects[f.policy])
	other["id"] = f.otherPolicy
	other["name"] = last(f.otherPolicy)
	f.objects[f.otherPolicy] = other
	for _, id := range []string{f.instance, f.deletedInstance} {
		object(object(f.objects[id]["properties"])["policyInfo"])["policyId"] = f.otherPolicy
	}
	base := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		if q.Method == "DELETE" {
			if strings.ToLower(q.URL.Path) != f.policy || q.URL.Query().Get("api-version") != dataProtectionVersion || len(q.URL.Query()) != 1 || q.Header.Get("If-Match") != "" || !uuidPattern.MatchString(q.Header.Get("x-ms-client-request-id")) {
				t.Fatal("invalid backup policy mutation", q.Method, q.URL)
			}
			if q.Body != nil {
				body, err := io.ReadAll(q.Body)
				if err != nil || len(body) != 0 {
					t.Fatal("policy deletion changed body", err)
				}
			}
			f.deletes++
			return &http.Response{StatusCode: f.status, Header: f.headers, Body: io.NopCloser(strings.NewReader(f.body))}, nil
		}
		if f.deletes > 0 && f.readFault != 0 && strings.EqualFold(q.URL.Path, f.policy) {
			return jsonResponse(f.readFault, map[string]any{}, nil), nil
		}
		if armPathProvider(q.URL.Path) != "microsoft.dataprotection" && !strings.EqualFold(q.URL.Path, "/subscriptions/"+testSubscription+"/resourcegroups/test") {
			if res, ok := fleetGraphEmptyIndexes(t, q); ok {
				return res, nil
			}
		}
		return base.RoundTrip(q)
	})
	return f
}
func (f *protectionPolicyFixture) request(t *testing.T) contracts.ActionRequest {
	t.Helper()
	batch, err := f.runtime.List(t.Context(), protectionRequest(f.protectionFixture, dataProtectionPolicy))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range batch.Items {
		if item.NativeID == f.policy {
			return contracts.ActionRequest{Asset: asset.Asset{ID: "policy", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: dataProtectionPolicy, NativeID: item.NativeID}, Location: item.Location, Normalized: item.Normalized}, Action: "delete", IdempotencyKey: "policy-delete"}
		}
	}
	t.Fatal("missing policy inventory")
	return contracts.ActionRequest{}
}
func TestDataProtectionPolicyNativeDeleteAndRestart(t *testing.T) {
	for _, status := range []int{200, 204} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newProtectionPolicyFixture(t)
			f.status = status
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			f.readFault = 403
			result, err := driver.Execute(t.Context(), req)
			if err != nil || f.deletes != 1 || result.Data["binding"] == nil {
				t.Fatal("lost native acknowledgement", result, err)
			}
			result = fleetSerializedResult(t, result, nil)
			fresh, err := NewRuntime(f.runtime.credentials)
			if err != nil {
				t.Fatal(err)
			}
			fresh.transport = f.runtime.transport
			driver, err = fresh.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if waited, err := driver.Wait(t.Context(), req, result); err == nil || waited.Done {
				t.Fatal("forbidden own read became success", waited, err)
			}
			f.readFault = 0
			req.ExecutionResult = &result
			if _, err = driver.Execute(t.Context(), req); err != nil || f.deletes != 1 {
				t.Fatal("receipt replayed mutation", err)
			}
			waited, err := driver.Wait(t.Context(), req, result)
			if err != nil || waited.Done {
				t.Fatal("live policy erased", waited, err)
			}
			delete(f.objects, f.policy)
			// Ordinary native ETag/state bookkeeping is not authored vault configuration.
			f.objects[f.vault]["etag"] = "after-policy-delete"
			waited, err = driver.Wait(t.Context(), req, result)
			if err != nil || !waited.Done {
				t.Fatal("policy absence not observed", waited, err)
			}
			if f.objects[f.instance] == nil || f.objects[f.deletedInstance] == nil || f.objects[f.otherPolicy] == nil || f.objects[f.vault] == nil {
				t.Fatal("policy cleanup altered retained backups")
			}
			object(f.objects[f.vault]["properties"])["privateFutureSetting"] = "changed"
			if _, err = driver.Readback(t.Context(), req); err == nil {
				t.Fatal("changed parent accepted after absence")
			}
		})
	}
}
func TestDataProtectionPolicyRejectsInvalidAcknowledgement(t *testing.T) {
	for _, mode := range []string{"202", "body", "null", "location", "404"} {
		t.Run(mode, func(t *testing.T) {
			f := newProtectionPolicyFixture(t)
			req := f.request(t)
			switch mode {
			case "202":
				f.status = 202
			case "body":
				f.body = "{}"
			case "null":
				f.body = "null"
			case "location":
				f.headers.Set("Location", apiURL(f.policy+"/operationResults/other", dataProtectionVersion))
			case "404":
				f.status = 404
				f.body = `{"error":{"code":"ResourceGroupNotFound"}}`
			}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), req)
			if err == nil || result.Data["binding"] != nil {
				t.Fatal("undocumented native receipt accepted", mode, result, err)
			}
		})
	}
}
func TestDataProtectionPolicyMutationBoundaries(t *testing.T) {
	for _, mode := range []string{"policy-config", "vault-config", "group-tag", "lock", "new-active", "new-retained", "consumer-forbidden", "policy-forbidden", "parent-missing", "proof", "region", "parameters", "receipt"} {
		t.Run(mode, func(t *testing.T) {
			f := newProtectionPolicyFixture(t)
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "policy-config":
				object(f.objects[f.policy]["properties"])["privateFutureSetting"] = "changed"
			case "vault-config":
				object(f.objects[f.vault]["properties"])["securitySettings"] = map[string]any{"immutabilitySettings": map[string]any{"state": "Locked"}}
			case "new-active", "new-retained":
				id := f.instance
				if mode == "new-retained" {
					id = f.deletedInstance
				}
				object(object(f.objects[id]["properties"])["policyInfo"])["policyId"] = f.policy
			case "proof":
				req.Asset.Normalized[dataProtectionPolicyProof] = "forged"
			case "region":
				req.Asset.Location = "westus"
			case "parameters":
				req.Parameters = map[string]any{"force": true}
			case "receipt":
				req.ExecutionResult = &contracts.ActionResult{Data: map[string]any{"accepted_status": 200, "binding": "forged"}}
			}
			f.override = func(q *http.Request) (*http.Response, bool) {
				path := strings.ToLower(q.URL.Path)
				if mode == "group-tag" && path == "/subscriptions/"+testSubscription+"/resourcegroups/test" {
					return jsonResponse(200, map[string]any{"id": path, "type": groupType, "location": "eastus", "tags": map[string]any{"steward:protected": "true"}}, nil), true
				}
				if mode == "lock" && strings.HasSuffix(path, "/providers/microsoft.authorization/locks") {
					return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": f.vault + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}}, nil), true
				}
				if mode == "consumer-forbidden" && path == f.vault+"/deletedbackupinstances" || mode == "policy-forbidden" && path == f.policy {
					return jsonResponse(403, map[string]any{}, nil), true
				}
				if mode == "parent-missing" && path == f.vault {
					return jsonResponse(404, map[string]any{}, nil), true
				}
				return nil, false
			}
			if _, err = driver.Preflight(t.Context(), req); err == nil {
				t.Fatal("changed boundary passed preflight", mode)
			}
			if _, err = driver.Execute(t.Context(), req); err == nil || f.deletes != 0 {
				t.Fatal("unreviewed mutation", mode, err, f.deletes)
			}
		})
	}
}
func TestDataProtectionPolicyKnownConsumersCannotDisappearFromLists(t *testing.T) {
	f := newProtectionPolicyFixture(t)
	object(object(f.objects[f.deletedInstance]["properties"])["policyInfo"])["policyId"] = f.policy
	req := protectionRequest(f.protectionFixture, dataProtectionPolicy)
	first, err := f.runtime.List(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	req.KnownNativeIDs = []string{f.policy}
	req.KnownNativeMetadata = map[string]map[string]any{}
	for _, item := range first.Items {
		if item.NativeID == f.policy {
			req.KnownNativeMetadata[f.policy] = item.Normalized
			if *item.Actionable || item.Normalized["cleanup_protection_reason"] != "backup_policy_in_use" {
				t.Fatal("retained instance lost protection")
			}
		}
	}
	f.omitted[f.deletedInstance] = true
	later, err := f.runtime.List(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range later.Items {
		if item.NativeID == f.policy && *item.Actionable {
			t.Fatal("missing list row freed policy")
		}
	}
	delete(f.objects, f.deletedInstance)
	later, err = f.runtime.List(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range later.Items {
		if item.NativeID == f.policy && !*item.Actionable {
			t.Fatal("own absence did not release policy")
		}
	}
}
func TestDataProtectionPolicyCleanupWorkerKeepsBackups(t *testing.T) {
	f := newProtectionPolicyFixture(t)
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, dataProtectionSource, repo, registry, []string{dataProtectionVault, dataProtectionPolicy, dataProtectionInstance, dataProtectionDeletedInstance, dataProtectionDeletedVault}, false, false)
	var policy asset.Asset
	for _, v := range values {
		if v.Identity.NativeID == f.policy {
			policy = v
		}
	}
	if policy.ID == "" {
		t.Fatal("native policy not persisted")
	}
	planner := cleanup.NewService(repo, registry)
	task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: policy.ID}}, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || len(task.ImpactItems) != 0 {
		t.Fatal("policy-only plan", task, err)
	}
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "policy-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := repo.ListJobsByAggregate(t.Context(), "cleanup_task", string(task.Task.ID))
	if err != nil {
		t.Fatal(err)
	}
	var job execution.Job
	for _, j := range jobs {
		if text(j.Payload["cleanup_task_step_id"]) == string(task.Steps[0].ID) {
			job = j
		}
	}
	if job.ID == "" {
		t.Fatal("missing cleanup job")
	}
	resume := func(pending bool) execution.ActionAttempt {
		t.Helper()
		repo, err = sqlite.Open(path, "../../migrations")
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := NewRuntime(f.runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		fresh.transport = f.runtime.transport
		registry = providerruntime.NewRegistry()
		if err = registry.Register(fresh); err != nil {
			t.Fatal(err)
		}
		if err = registry.RegisterBundle(fresh.Bundle()); err != nil {
			t.Fatal(err)
		}
		worker := cleanup.NewExecutionHandler(cleanup.NewService(repo, registry), cleanup.ActionResolverFunc(func(ctx context.Context, v asset.Asset) (cleanup.ActionDriver, error) {
			return registry.ResolveAction(ctx, v.Identity.ConnectionID, v)
		}))
		err = worker.Handle(t.Context(), job)
		var retry *cleanup.RetryError
		if pending && !errors.As(err, &retry) || !pending && err != nil {
			t.Fatal("worker recovery", err)
		}
		current, err := repo.Executions().GetActionByExecutionStep(t.Context(), attempt.ID, string(task.Steps[0].ID))
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	// Transient service failures must resume from the durable acknowledgement.
	// Permission failures are covered separately and intentionally terminate jobs.
	f.readFault = 503
	first := resume(true)
	if first.ProviderResult["binding"] == nil || f.deletes != 1 {
		t.Fatal("native acknowledgement not durable", first)
	}
	again := resume(true)
	if first.ProviderResult["binding"] != again.ProviderResult["binding"] {
		t.Fatal("receipt changed across restart")
	}
	f.readFault = 0
	resume(true)
	delete(f.objects, f.policy)
	resume(true)
	resume(false)
	active, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(active) != len(values)-1 || f.deletes != 1 {
		t.Fatal("cleanup scope", len(active), len(values), err, f.deletes)
	}
	for _, v := range active {
		if v.Identity.NativeID == f.policy {
			t.Fatal("deleted policy remains active")
		}
	}
	wire, _ := json.Marshal(first.ProviderResult)
	if strings.Contains(string(wire), "secret-config") {
		t.Fatal("receipt exposed private configuration")
	}
}
