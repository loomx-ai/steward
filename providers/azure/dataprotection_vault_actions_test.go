package azure

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
	"io"
	"net/http"
	"strings"
	"testing"
)

type protectionVaultFixture struct {
	*protectionFixture
	deletes, status, readFault int
	statusURL, resultURL       string
}

func newProtectionVaultFixture(t *testing.T) *protectionVaultFixture {
	t.Helper()
	f := &protectionVaultFixture{protectionFixture: newProtectionFixture(t), status: 200}
	f.statusURL = "https://management.azure.com" + f.vault + "/operationstatus/Operation-AbC==?api-version=" + dataProtectionVaultVersion
	f.resultURL = "https://management.azure.com/subscriptions/" + testSubscription + "/providers/microsoft.dataprotection/locations/eastus/operationresults/Operation-AbC==?api-version=" + dataProtectionVaultVersion
	delete(f.objects, f.instance)
	delete(f.objects, f.policy)
	base := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		if q.Method == "DELETE" {
			if !strings.EqualFold(q.URL.Path, f.vault) || q.URL.Query().Get("api-version") != dataProtectionVaultVersion || len(q.URL.Query()) != 1 || q.Header.Get("If-Match") != "" || !uuidPattern.MatchString(q.Header.Get("x-ms-client-request-id")) {
				t.Fatal("unexpected vault mutation", q.Method, q.URL)
			}
			if q.Body != nil {
				body, err := io.ReadAll(q.Body)
				if err != nil || len(body) > 0 {
					t.Fatal("unexpected vault delete body", err)
				}
			}
			f.deletes++
			h := http.Header{}
			if f.status == 202 {
				h.Set("Azure-AsyncOperation", f.statusURL)
				h.Set("Location", f.resultURL)
			}
			return &http.Response{StatusCode: f.status, Header: h, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if q.URL.String() == f.statusURL {
			return jsonResponse(200, map[string]any{"id": q.URL.Path, "name": "Operation-AbC==", "status": "Succeeded"}, nil), nil
		}
		if q.URL.String() == f.resultURL {
			return jsonResponse(200, map[string]any{"objectType": "OperationJobExtendedInfo"}, nil), nil
		}
		if f.deletes > 0 && f.readFault != 0 && strings.EqualFold(q.URL.Path, f.vault) {
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
func (f *protectionVaultFixture) request(t *testing.T) contracts.ActionRequest {
	t.Helper()
	batch, err := f.runtime.List(t.Context(), protectionRequest(f.protectionFixture, dataProtectionVault))
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range batch.Items {
		if item.NativeID == f.vault {
			return contracts.ActionRequest{Asset: asset.Asset{ID: "vault", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: dataProtectionVault, NativeID: item.NativeID}, Location: item.Location, Normalized: item.Normalized}, Action: "delete", IdempotencyKey: "delete-instance"}
		}
	}
	t.Fatal("missing instance")
	return contracts.ActionRequest{}
}

func TestDataProtectionVaultCleanupWorkerKeepsRetainedHistory(t *testing.T) {
	f := newProtectionVaultFixture(t)
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, dataProtectionSource, repo, registry, []string{dataProtectionVault, dataProtectionPolicy, dataProtectionInstance, dataProtectionDeletedInstance, dataProtectionDeletedVault}, false, false)
	var instance asset.Asset
	for _, v := range values {
		if v.Identity.NativeID == f.vault {
			instance = v
		}
	}
	if instance.ID == "" {
		t.Fatal("native vault not persisted")
	}
	planner := cleanup.NewService(repo, registry)
	task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: instance.ID}}, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || len(task.ImpactItems) != 0 {
		t.Fatal("vault-only plan", task, err)
	}
	warned := false
	for _, warning := range task.Task.Warnings {
		if warning.Code == plan.WarningDataProtectionVaultDelete {
			warned = strings.Contains(warning.Message, "does not mean permanent purge") && strings.Contains(warning.Message, "Source workloads")
		}
	}
	if !warned {
		t.Fatal("backup retention warning missing")
	}
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "instance-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
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
	if again.ProviderResult["operation_done"] != true || object(first.ProviderResult["operation"])["binding"] != object(again.ProviderResult["operation"])["binding"] || f.deletes != 1 {
		t.Fatal("native acknowledgement changed across restart")
	}
	failing := resume(true)
	if failing.ProviderResult["binding"] != again.ProviderResult["binding"] {
		t.Fatal("read failure changed receipt")
	}
	f.readFault = 0
	resume(true)
	delete(f.objects, f.vault)
	resume(true)
	final := resume(false)
	if final.Readback["state"] != "active_absent_with_retained_history" || object(final.Readback["data"])["permanent_purge_verified"] != false {
		t.Fatal("retention result not durable", final.Readback)
	}
	active, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(active) != len(values)-1 || f.deletes != 1 {
		t.Fatal("cleanup scope", len(active), len(values), err, f.deletes)
	}
	for _, v := range active {
		if v.Identity.NativeID == f.vault {
			t.Fatal("deleted vault remains active")
		}
	}
	wire, _ := json.Marshal(first.ProviderResult)
	if strings.Contains(string(wire), "secret-config") {
		t.Fatal("receipt exposed private configuration")
	}
}

func TestDataProtectionVaultNativeDeleteAndRetainedHistory(t *testing.T) {
	for _, status := range []int{200, 202, 204} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newProtectionVaultFixture(t)
			f.status = status
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			oldRetained, _ := json.Marshal(f.objects[f.deletedVault])
			oldInstance, _ := json.Marshal(f.objects[f.deletedInstance])
			result, err := driver.Execute(t.Context(), req)
			if err != nil || f.deletes != 1 {
				t.Fatal("native vault receipt", err, f.deletes)
			}
			for i := 0; i < 4; i++ {
				wire, _ := json.Marshal(result)
				result = contracts.ActionResult{}
				if err = json.Unmarshal(wire, &result); err != nil {
					t.Fatal(err)
				}
				fresh, err := NewRuntime(f.runtime.credentials)
				if err != nil {
					t.Fatal(err)
				}
				fresh.transport = f.runtime.transport
				driver, err = fresh.ResolveAction(t.Context(), "connection", req.Asset)
				if err != nil {
					t.Fatal(err)
				}
				poll, err := driver.Wait(t.Context(), req, result)
				if err != nil || poll.Done {
					t.Fatal("live vault disappeared", i, poll, err)
				}
				result.Data = poll.Data
			}
			f.readFault = 403
			if _, err = driver.Wait(t.Context(), req, result); err == nil {
				t.Fatal("forbidden own read accepted")
			}
			req.ExecutionResult = &result
			if _, err = driver.Execute(t.Context(), req); err != nil || f.deletes != 1 {
				t.Fatal("reissued vault delete", err, f.deletes)
			}
			req.ExecutionResult = nil
			f.readFault = 0
			newID := strings.Replace(f.deletedVault, "deleted-one", "deleted-new", 1)
			retained := batchClone(f.objects[f.deletedVault])
			retained["id"], retained["name"] = newID, "deleted-new"
			f.objects[newID] = retained
			delete(f.objects, f.vault)
			poll, err := driver.Wait(t.Context(), req, result)
			if err != nil || !poll.Done || poll.State != "active_absent_with_retained_history" {
				t.Fatal("retained vault readback", poll, err)
			}
			read, err := driver.Readback(t.Context(), req)
			if err != nil || read.Exists || read.Data["permanent_purge_verified"] != false || len(read.Data["retained_vault_ids"].([]string)) != 2 {
				t.Fatal("retained histories collapsed", read, err)
			}
			after, _ := json.Marshal(f.objects[f.deletedVault])
			instanceAfter, _ := json.Marshal(f.objects[f.deletedInstance])
			if string(after) != string(oldRetained) || string(instanceAfter) != string(oldInstance) || f.deletes != 1 {
				t.Fatal("retained data mutated")
			}
		})
	}
}
func TestDataProtectionVaultRejectsChangedReview(t *testing.T) {
	for _, mode := range []string{"new-policy", "new-instance", "new-retained", "vault-config", "group-tag", "guard", "retained-forbidden", "proof", "parameters"} {
		t.Run(mode, func(t *testing.T) {
			f := newProtectionVaultFixture(t)
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "new-policy":
				f.objects[f.policy] = map[string]any{"id": f.policy, "name": last(f.policy), "type": dataProtectionPolicy, "properties": map[string]any{"objectType": "BackupPolicy", "policyRules": []any{}}}
			case "new-instance":
				raw := batchClone(f.objects[f.deletedInstance])
				raw["id"], raw["type"] = f.instance, dataProtectionInstance
				f.objects[f.instance] = raw
			case "new-retained":
				object(f.objects[f.deletedInstance]["properties"])["privateFutureSetting"] = "changed"
			case "vault-config":
				object(f.objects[f.vault]["properties"])["securitySettings"] = map[string]any{"softDeleteSettings": map[string]any{"state": "Off"}}
			case "proof":
				req.Asset.Normalized[dataProtectionVaultProof] = "forged"
			case "parameters":
				req.Parameters = map[string]any{"force": true}
			}
			f.override = func(q *http.Request) (*http.Response, bool) {
				path := strings.ToLower(q.URL.Path)
				if mode == "group-tag" && path == "/subscriptions/"+testSubscription+"/resourcegroups/test" {
					return jsonResponse(200, map[string]any{"id": path, "type": groupType, "location": "eastus", "tags": map[string]any{"steward:protected": "true"}}, nil), true
				}
				if mode == "retained-forbidden" && path == f.vault+"/deletedbackupinstances" {
					return jsonResponse(403, map[string]any{}, nil), true
				}
				if mode == "guard" && strings.Contains(path, "/backupresourceguardproxies") {
					id := f.vault + "/backupresourceguardproxies/proxy"
					raw := map[string]any{"id": id, "name": "proxy", "type": dataProtectionGuardProxy, "properties": map[string]any{"resourceGuardResourceId": "/subscriptions/" + testSubscription + "/resourceGroups/security/providers/Microsoft.DataProtection/resourceGuards/guard"}}
					if path == id {
						return jsonResponse(200, raw, nil), true
					}
					return jsonResponse(200, map[string]any{"value": []any{raw}}, nil), true
				}
				return nil, false
			}
			if _, err := driver.Preflight(t.Context(), req); err == nil {
				t.Fatal("changed vault passed preflight", mode)
			}
			if _, err := driver.Execute(t.Context(), req); err == nil || f.deletes != 0 {
				t.Fatal("unreviewed vault mutation", mode, err, f.deletes)
			}
		})
	}
}

func TestDataProtectionVaultPreservesResourceGuardAndRejectsUnreadableRetention(t *testing.T) {
	f := newProtectionVaultFixture(t)
	guardID := f.vault + "/backupresourceguardproxies/proxy"
	guard := map[string]any{"id": guardID, "name": "proxy", "type": dataProtectionGuardProxy, "properties": map[string]any{"resourceGuardResourceId": "/subscriptions/00000000-0000-0000-0000-000000000001/resourceGroups/security/providers/Microsoft.DataProtection/resourceGuards/guard"}}
	f.objects[guardID] = guard
	object(f.objects[f.vault]["properties"])["isVaultProtectedByResourceGuard"] = true
	req := f.request(t)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), req)
	if err != nil || f.deletes != 1 {
		t.Fatal("native vault delete", err, f.deletes)
	}
	before, _ := json.Marshal(guard)
	delete(f.objects, f.vault)
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.HasSuffix(strings.ToLower(q.URL.Path), "/deletedvaults") {
			return jsonResponse(403, map[string]any{}, nil), true
		}
		return nil, false
	}
	if _, err = driver.Readback(t.Context(), req); err == nil {
		t.Fatal("unreadable retention treated as completed cleanup")
	}
	f.override = nil
	if read, err := driver.Readback(t.Context(), req); err != nil || read.Exists || read.Data["permanent_purge_verified"] != false {
		t.Fatal("retained vault absence", read, err)
	}
	after, _ := json.Marshal(f.objects[guardID])
	if string(before) != string(after) {
		t.Fatal("guard configuration changed")
	}
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.EqualFold(q.URL.Path, "/subscriptions/"+testSubscription+"/resourcegroups/test") {
			return jsonResponse(404, map[string]any{}, nil), true
		}
		return nil, false
	}
	req.ExecutionResult = &result
	if _, err = driver.Readback(t.Context(), req); err == nil {
		t.Fatal("missing resource group treated as own absence")
	}
}
