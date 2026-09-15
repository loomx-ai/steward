package azure

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type restorePointFixture struct {
	*synapseBackupFixture
	id                         string
	deletes, status, readFault int
	body                       string
	headers                    http.Header
}

func newRestorePointFixture(t *testing.T) *restorePointFixture {
	f := &restorePointFixture{synapseBackupFixture: newSynapseBackupFixture(t), status: 200, headers: http.Header{}}
	base := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		if q.Method == "DELETE" {
			if strings.ToLower(q.URL.Path) != f.id || q.URL.Query().Get("api-version") != synapseVersion {
				t.Fatal("unexpected mutation", q.Method, q.URL)
			}
			if q.Body != nil {
				body, err := io.ReadAll(q.Body)
				if err != nil || len(body) != 0 {
					t.Fatal("nonempty delete body", err)
				}
			}
			f.deletes++
			return &http.Response{StatusCode: f.status, Header: f.headers, Body: io.NopCloser(strings.NewReader(f.body))}, nil
		}
		if f.deletes > 0 && f.readFault != 0 && strings.Contains(strings.ToLower(q.URL.Path), "/microsoft.synapse/") {
			return jsonResponse(f.readFault, nil, nil), nil
		}
		return base.RoundTrip(q)
	})
	return f
}
func (f *restorePointFixture) request(t *testing.T) contracts.ActionRequest {
	t.Helper()
	req := backupRequest(f.runtime, synapseRestorePointType)
	req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
	batch, err := f.runtime.List(t.Context(), req)
	if err != nil || len(batch.Items) != 1 {
		t.Fatal(batch, err)
	}
	item := batch.Items[0]
	f.id = item.NativeID
	return contracts.ActionRequest{Asset: asset.Asset{ID: "point", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeType: synapseRestorePointType, NativeID: item.NativeID}, Normalized: item.Normalized, Location: item.Location}, Action: "delete", IdempotencyKey: "restore-point-delete"}
}
func TestSynapseRestorePointDurableReceipt(t *testing.T) {
	for _, status := range []int{200, 204} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			f := newRestorePointFixture(t)
			req := f.request(t)
			f.status = status
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			f.readFault = 503
			result, err := driver.Execute(t.Context(), req)
			if err != nil || f.deletes != 1 || result.Data["binding"] == nil {
				t.Fatal("ack lost", result, err)
			}
			wire, _ := json.Marshal(result)
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
			req.ExecutionResult = &result
			if _, err = driver.Wait(t.Context(), req, result); err == nil {
				t.Fatal("read failure hidden")
			}
			f.readFault = 0
			if _, err = driver.Execute(t.Context(), req); err != nil || f.deletes != 1 {
				t.Fatal("receipt replay", err)
			}
			f.readFault = 0
			wait, err := driver.Wait(t.Context(), req, result)
			if err != nil || wait.Done {
				t.Fatal("live point erased", wait, err)
			}
			f.missing[f.id] = true
			wait, err = driver.Wait(t.Context(), req, result)
			if err != nil || !wait.Done {
				t.Fatal("own absence", wait, err)
			}
			object(f.objects[redisParentID(f.id)]["properties"])["creationDate"] = "2026-09-15T00:00:00Z"
			if _, err = driver.Readback(t.Context(), req); err == nil {
				t.Fatal("recreated lookup scope accepted")
			}
		})
	}
}
func TestSynapseRestorePointInvalidAcknowledgement(t *testing.T) {
	for _, mode := range []string{"202", "body", "null", "async", "system"} {
		t.Run(mode, func(t *testing.T) {
			f := newRestorePointFixture(t)
			req := f.request(t)
			switch mode {
			case "202":
				f.status = 202
			case "body":
				f.body = "{}"
			case "null":
				f.body = "null"
			case "async":
				f.headers.Set("Location", apiURL(redisParentID(f.id)+"/operationResults/operation", synapseVersion))
			case "system":
				f.status = 400
				f.body = `{"error":{"code":"RestorePointAttemptToDeleteSystemBackup","message":"Cannot delete system backup"}}`
			}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = driver.Execute(t.Context(), req); err == nil || f.deletes != 1 {
				t.Fatal("invalid ack accepted", err)
			}
		})
	}
}
func TestSynapseRestorePointChangedReview(t *testing.T) {
	for _, mode := range []string{"point", "pool", "workspace", "protected", "state", "parent missing", "parent error code", "proof", "receipt", "parameter"} {
		t.Run(mode, func(t *testing.T) {
			f := newRestorePointFixture(t)
			req := f.request(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "point":
				object(f.backups[f.id]["properties"])["restorePointCreationDate"] = "2026-09-15T00:00:00Z"
			case "pool":
				object(f.objects[redisParentID(f.id)]["properties"])["creationDate"] = "2026-09-15T00:00:00Z"
			case "workspace":
				object(f.objects[strings.Join(strings.Split(f.id, "/")[:9], "/")]["properties"])["workspaceUID"] = "replacement"
			case "protected":
				f.backups[f.id]["tags"] = map[string]any{"steward:protected": "true"}
			case "state":
				object(f.objects[redisParentID(f.id)]["properties"])["status"] = "Deleting"
				f.missing[f.id] = true
			case "parent missing":
				f.missing[redisParentID(f.id)] = true
				f.missing[f.id] = true
			case "parent error code":
				f.intercepted = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, f.id) {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "DatabaseDoesNotExist"}}, nil), true
					}
					return nil, false
				}
			case "proof":
				req.Asset.Normalized[synapseRestoreProof] = "forged"
			case "receipt":
				req.ExecutionResult = &contracts.ActionResult{Data: map[string]any{"binding": "forged"}}
			case "parameter":
				req.Parameters = map[string]any{"force": true}
			}
			if _, err = driver.Preflight(t.Context(), req); err == nil {
				t.Fatal("unsafe review accepted", mode)
			}
			if _, err = driver.Execute(t.Context(), req); err == nil || f.deletes != 0 {
				t.Fatal("unsafe mutation", err, f.deletes)
			}
		})
	}
}
func TestSynapseRestorePointEligibility(t *testing.T) {
	for _, mode := range []string{"user", "paused", "automatic", "continuous", "undated", "protected"} {
		t.Run(mode, func(t *testing.T) {
			f := newRestorePointFixture(t)
			for id, raw := range f.backups {
				if text(raw["type"]) != synapseRestorePointType && !strings.EqualFold(text(raw["type"]), synapseRestorePointType) {
					continue
				}
				p := object(raw["properties"])
				switch mode {
				case "paused":
					object(f.objects[redisParentID(id)]["properties"])["status"] = "Paused"
				case "automatic":
					delete(p, "restorePointLabel")
				case "continuous":
					p["restorePointType"] = "CONTINUOUS"
				case "undated":
					delete(p, "restorePointCreationDate")
				case "protected":
					raw["tags"] = map[string]any{"steward:protected": "true"}
				}
			}
			req := f.request(t)
			enabled := req.Asset.Normalized["cleanup_protected"] == false
			if enabled != (mode == "user" || mode == "paused") {
				t.Fatal("eligibility", mode, enabled)
			}
			_, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if (err == nil) != enabled {
				t.Fatal("driver eligibility", err)
			}
		})
	}
}
func TestSynapseRestorePointOfficialRecordings(t *testing.T) {
	wire, err := os.ReadFile("fixtures/synapse/powershell-restorepoint-recordings.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		SourceSHA string `json:"source_sha256"`
		Rows      []struct {
			Method, URI, Body string
			Status            int
			Headers           http.Header
		} `json:"recordings"`
	}
	if err = json.Unmarshal(wire, &fixture); err != nil || fixture.SourceSHA != "18f6d43cffd4ec109039078f94d36da9909182536e66ceb6d5b6ef9375d67c42" || len(fixture.Rows) != 8 {
		t.Fatal("source", err)
	}
	for i := 2; i < len(fixture.Rows); i += 2 {
		own, del := fixture.Rows[i], fixture.Rows[i+1]
		var raw map[string]any
		if err = json.Unmarshal([]byte(own.Body), &raw); err != nil || !synapseUserRestorePoint(raw) {
			t.Fatal("native user point", err)
		}
		if own.Method != "GET" || own.Status != 200 || del.Method != "DELETE" || own.URI != del.URI || del.Status != 200 || del.Body != "" || operationLocation(del.Headers) != "" {
			t.Fatal("native synchronous delete", del)
		}
		f := newRestorePointFixture(t)
		req := f.request(t)
		f.status, f.body = del.Status, del.Body
		for key, values := range del.Headers {
			for _, value := range values {
				f.headers.Add(key, value)
			}
		}
		driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
		if err != nil {
			t.Fatal(err)
		}
		result, err := driver.Execute(t.Context(), req)
		if err != nil || result.ProviderRequestID == "" {
			t.Fatal("recorded acknowledgement", result, err)
		}
	}
}

func TestSynapseRestorePointCleanupWorkerKeepsParents(t *testing.T) {
	f := newRestorePointFixture(t)
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	azureNativeWorkerScan(t, f.runtime, synapseSource, repo, registry, []string{synapseType, synapseSparkType, synapseSQLType}, false, false)
	azureNativeWorkerScan(t, f.runtime, synapseBackupSource, repo, registry, []string{synapseDroppedType, synapseRestorePointType}, false, false)
	// A selected restore point must leave all live parents and other backups intact.
	values, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(values) != 10 {
		t.Fatal(err, len(values))
	}
	var sql asset.Asset
	for _, v := range values {
		if v.Identity.NativeType == synapseRestorePointType && v.Location == "eastus" {
			sql = v
		}
	}
	planner := cleanup.NewService(repo, registry)
	task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: sql.ID}}, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || task.Steps[0].AssetID != sql.ID || len(task.ImpactItems) != 0 {
		t.Fatal("point-only plan", task, err)
	}
	warning := false
	for _, v := range task.Task.Warnings {
		if v.Code == plan.WarningSynapseRestorePointDelete {
			warning = strings.Contains(v.Message, "recovery option") && strings.Contains(v.Message, "other backups are retained")
		}
	}
	if !warning {
		t.Fatal("restore point scope warning missing")
	}
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "sql-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
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
		t.Fatal("missing job", jobs)
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
			t.Fatal("restart", err)
		}
		current, err := repo.Executions().GetActionByExecutionStep(t.Context(), attempt.ID, string(task.Steps[0].ID))
		if err != nil {
			t.Fatal(err)
		}
		return current
	}
	f.id = sql.Identity.NativeID
	f.readFault = 503
	first := resume(true)
	if first.ProviderResult["binding"] == nil || f.deletes != 1 {
		t.Fatal("ack not saved", first)
	}
	again := resume(true)
	if first.ProviderResult["binding"] != again.ProviderResult["binding"] {
		t.Fatal("receipt lost")
	}
	f.readFault = 0
	resume(true)
	f.missing[f.id] = true
	resume(true)
	resume(false)
	active, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(active) != 9 || f.deletes != 1 {
		t.Fatal("scope", active, err, f.deletes)
	}
	for _, v := range active {
		if v.Identity.NativeType == synapseRestorePointType && v.Location == "eastus" {
			t.Fatal("selected restore point still active")
		}
	}
}
