package azure

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type netappRecoveryFixture struct {
	*netappFixture
	id, kind, source                                  string
	deletes, polls, readFault, pollFault, deleteFault int
	done, failAfterDelete                             bool
}

func newNetappRecoveryFixture(t *testing.T, kind string) *netappRecoveryFixture {
	base := newNetappFixture(t)
	account := strings.ToLower(resourceID(netappAccountType, "first"))
	source := account + "/capacitypools/item/volumes/item"
	id := source + "/snapshots/item"
	if kind == netappBackupType {
		id = account + "/backupvaults/item/backups/item"
	}
	f := &netappRecoveryFixture{netappFixture: base, id: id, kind: kind, source: source}
	base.override = func(q *http.Request) (*http.Response, bool) {
		path := strings.ToLower(q.URL.Path)
		if q.Method == "DELETE" {
			if path != f.id || q.URL.Query().Get("api-version") != netappVersion || len(q.URL.Query()) != 1 || q.ContentLength != 0 {
				t.Fatal("independent recovery deletion escaped target", q.Method, q.URL)
			}
			f.deletes++
			if f.deleteFault != 0 {
				return jsonResponse(f.deleteFault, map[string]any{"error": map[string]any{"code": "SnapshotInUse", "message": "native guard"}}, nil), true
			}
			if f.failAfterDelete {
				f.readFault = 503
			}
			h := http.Header{}
			h.Set("Azure-AsyncOperation", netappTestPollURL("status_url"))
			h.Set("Location", netappTestPollURL("result_url"))
			return &http.Response{StatusCode: 202, Header: h, Body: io.NopCloser(strings.NewReader(""))}, true
		}
		if path == f.id && f.readFault != 0 {
			return jsonResponse(f.readFault, nil, nil), true
		}
		if strings.Contains(path, "/operationresults/") {
			f.polls++
			if f.pollFault != 0 {
				return jsonResponse(f.pollFault, nil, nil), true
			}
			if q.URL.Query().Get("operationResultResponseType") == "Location" {
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, true
			}
			state := "Deleting"
			if f.done {
				state = "Succeeded"
			}
			return jsonResponse(200, netappTestStatus(f.id, state), nil), true
		}
		return fleetGraphEmptyIndexes(t, q)
	}
	return f
}
func (f *netappRecoveryFixture) asset(t *testing.T) asset.Asset {
	t.Helper()
	req := netappRequest(f.runtime, f.kind)
	req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
	page, err := f.runtime.List(t.Context(), req)
	if err != nil || len(page.Items) != 1 {
		t.Fatal("recovery inventory", f.kind, len(page.Items), err)
	}
	item := page.Items[0]
	return asset.Asset{ID: "recovery", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: f.kind, NativeID: item.NativeID}, Normalized: item.Normalized, Location: item.Location}
}
func TestNetappRecoveryNativeDurability(t *testing.T) {
	for _, kind := range []string{netappSnapshotType, netappBackupType} {
		t.Run(kind, func(t *testing.T) {
			f := newNetappRecoveryFixture(t, kind)
			value := f.asset(t)
			req := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "recovery"}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			if pre, err := driver.Preflight(t.Context(), req); err != nil || !pre.Allowed {
				t.Fatal(pre, err)
			}
			f.failAfterDelete = true
			result, err := driver.Execute(t.Context(), req)
			if err != nil || result.Data["binding"] == nil || f.deletes != 1 || f.polls != 0 {
				t.Fatal("acknowledgement not isolated", result, err)
			}
			wire, _ := json.Marshal(result)
			var restored contracts.ActionResult
			if json.Unmarshal(wire, &restored) != nil {
				t.Fatal("receipt serialization")
			}
			result = restored
			req.ExecutionResult = &result
			if out, err := driver.Wait(t.Context(), req, result); out.Done {
				t.Fatal("unreadable live resource closed", out, err)
			}
			if _, err := driver.Readback(t.Context(), req); err == nil {
				t.Fatal("failed own read was ignored")
			}
			f.readFault = 0
			if _, err := driver.Execute(t.Context(), req); err != nil || f.deletes != 1 {
				t.Fatal("delete replayed", err)
			}
			f.done = true
			for i := 0; i < 2; i++ {
				out, err := driver.Wait(t.Context(), req, result)
				if err != nil || out.Done {
					t.Fatal("callback closed a live recovery point", out, err)
				}
				result.Data = out.Data
				req.ExecutionResult = &result
			}
			if result.Data["operation_done"] != true {
				t.Fatal("terminal callback not checkpointed")
			}
			f.missing[f.id] = true
			if out, err := driver.Wait(t.Context(), req, result); err != nil || !out.Done {
				t.Fatal("own recovery absence", out, err)
			}
			f.missing[redisParentID(f.id)] = true
			if _, err := driver.Readback(t.Context(), req); err == nil || isNotFound(err) {
				t.Fatal("parent outage proved absence")
			}
		})
	}
}
func (f *netappRecoveryFixture) peer(name, date string) string {
	id := redisParentID(f.id) + "/backups/" + name
	raw := batchClone(f.objects[f.id])
	raw["id"], raw["name"] = id, name
	props := object(raw["properties"])
	props["snapshotCreationDate"] = date
	props["backupId"] = "d241067a-ae04-cbd2-cc5d-f8bdf9a147d3"
	f.objects[id] = raw
	return id
}
func TestNetappBackupLatestPolicyRules(t *testing.T) {
	for _, scenario := range []string{"no current policy", "historical policy only", "latest assigned", "tied latest", "older", "source absent", "policy disabled but assigned", "unknown snapshot date", "older in another vault", "newer incomplete", "older with unknown sibling", "same backup alias"} {
		t.Run(scenario, func(t *testing.T) {
			f := newNetappRecoveryFixture(t, netappBackupType)
			p := object(f.objects[f.source]["properties"])
			account := strings.Join(strings.Split(f.id, "/")[:9], "/")
			allowed := false
			p["dataProtection"] = map[string]any{"backup": map[string]any{"backupPolicyId": account + "/backuppolicies/item", "policyEnforced": true}}
			switch scenario {
			case "no current policy", "historical policy only":
				delete(p, "dataProtection")
				allowed = true
			case "latest assigned":
			case "tied latest":
				id := f.peer("tie", "2017-08-15T13:23:33Z")
				object(f.objects[id]["properties"])["creationDate"] = "2019-08-15T13:23:33Z"
			case "older":
				f.peer("newer", "2018-08-15T13:23:33Z")
				allowed = true
			case "source absent":
				f.missing[f.source] = true
				allowed = true
			case "policy disabled but assigned":
				object(object(p["dataProtection"])["backup"])["policyEnforced"] = false
			case "unknown snapshot date":
				delete(object(f.objects[f.id]["properties"]), "snapshotCreationDate")
				allowed = true
			case "same backup alias":
				id := f.peer("alias", "2018-08-15T13:23:33Z")
				object(f.objects[id]["properties"])["backupId"] = object(f.objects[f.id]["properties"])["backupId"]
			case "newer incomplete":
				id := f.peer("pending", "2018-08-15T13:23:33Z")
				object(f.objects[id]["properties"])["provisioningState"] = "Creating"
			case "older with unknown sibling":
				f.peer("newer", "2018-08-15T13:23:33Z")
				id := f.peer("unknown", "2016-08-15T13:23:33Z")
				delete(object(f.objects[id]["properties"]), "snapshotCreationDate")
				allowed = true
			case "older in another vault":
				vault := account + "/backupvaults/other"
				raw := batchClone(f.objects[redisParentID(f.id)])
				raw["id"], raw["name"] = vault, "other"
				f.objects[vault] = raw
				peer := batchClone(f.objects[f.id])
				peer["id"], peer["name"] = vault+"/backups/newer", "newer"
				object(peer["properties"])["snapshotCreationDate"] = "2018-08-15T13:23:33Z"
				object(peer["properties"])["backupId"] = "d241067a-ae04-cbd2-cc5d-f8bdf9a147d3"
				f.objects[vault+"/backups/newer"] = peer
				allowed = true
			}
			// More than one backup can now be returned; inspect the reviewed target.
			req := netappRequest(f.runtime, f.kind)
			req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			page, err := f.runtime.List(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, v := range page.Items {
				if v.NativeID == f.id {
					found = true
					if v.Actionable == nil || *v.Actionable != allowed || v.Normalized["cleanup_protected"] == allowed {
						t.Fatal("incorrect latest-backup eligibility", scenario)
					}
				}
			}
			if !found {
				t.Fatal("target vanished")
			}
		})
	}
}
func TestNetappRecoveryChangedBoundaryAndNativeGuards(t *testing.T) {
	for _, kind := range []string{netappSnapshotType, netappBackupType} {
		for _, fault := range []string{"own", "parent", "protected parent", "missing parent", "source permission", "new backup", "receipt", "impacts", "parameter", "proof", "native conflict"} {
			t.Run(kind+"/"+fault, func(t *testing.T) {
				f := newNetappRecoveryFixture(t, kind)
				value := f.asset(t)
				req := contracts.ActionRequest{Asset: value, Action: "delete"}
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				old := f.override
				switch fault {
				case "own":
					object(f.objects[f.id]["properties"])["futurePrivateField"] = "changed"
				case "parent":
					object(f.objects[redisParentID(f.id)]["properties"])["futurePrivateField"] = "changed"
				case "protected parent":
					f.objects[redisParentID(f.id)]["tags"] = map[string]any{"steward:protected": "true"}
				case "missing parent":
					f.missing[redisParentID(f.id)] = true
				case "source permission":
					f.override = func(q *http.Request) (*http.Response, bool) {
						if strings.EqualFold(q.URL.Path, f.source) {
							return jsonResponse(403, nil, nil), true
						}
						return old(q)
					}
				case "new backup":
					if kind == netappBackupType {
						f.peer("newer", "2018-08-15T13:23:33Z")
					} else {
						object(f.objects[f.source]["properties"])["isRestoring"] = true
					}
				case "receipt":
					req.ExecutionResult = &contracts.ActionResult{Data: map[string]any{"binding": "forged"}}
				case "impacts":
					req.LifecycleImpacts = []contracts.ActionImpact{{Asset: value, Delete: true}}
				case "parameter":
					req.Parameters = map[string]any{"forceDelete": true}
				case "proof":
					req.Asset.Normalized = batchClone(req.Asset.Normalized)
					req.Asset.Normalized[netappRecoveryProof] = "forged"
				case "native conflict":
					f.deleteFault = 409
				}
				_, err = driver.Execute(t.Context(), req)
				expectedDeletes := 0
				if fault == "native conflict" {
					expectedDeletes = 1
				}
				if err == nil || f.deletes != expectedDeletes {
					t.Fatal("unsafe independent delete", fault, err, f.deletes)
				}
			})
		}
	}
}
func TestNetappRecoveryRecreationAndExpiredCallbacks(t *testing.T) {
	for _, kind := range []string{netappSnapshotType, netappBackupType} {
		t.Run(kind, func(t *testing.T) {
			f := newNetappRecoveryFixture(t, kind)
			value := f.asset(t)
			req := contracts.ActionRequest{Asset: value, Action: "delete"}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			f.pollFault = 404
			if out, err := driver.Wait(t.Context(), req, result); err == nil || out.Done {
				t.Fatal("expired callback erased live resource")
			}
			f.missing[f.id] = true
			if out, err := driver.Wait(t.Context(), req, result); err != nil || !out.Done {
				t.Fatal("own absence was not independent", out, err)
			}
			f.missing[f.id] = false
			key := "snapshotId"
			if kind == netappBackupType {
				key = "backupId"
			}
			object(f.objects[f.id]["properties"])[key] = testApplication
			req.ExecutionResult = &result
			if _, err := driver.Readback(t.Context(), req); err == nil {
				t.Fatal("recreated recovery point matched old request")
			}
		})
	}
}
func TestNetappRecoverySQLiteWorker(t *testing.T) {
	for _, kind := range []string{netappSnapshotType, netappBackupType} {
		t.Run(kind, func(t *testing.T) {
			f := newNetappRecoveryFixture(t, kind)
			kinds := []string{}
			expected := 22
			if kind == netappBackupType {
				f.missing[f.source] = true
				kinds = []string{netappAccountType, netappAccountType + "/backupVaults", netappBackupType}
				expected = 6
			} else {
				for _, k := range netappResources {
					kinds = append(kinds, k.kind)
				}
			}
			repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
			values := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, kinds, false, true)
			if len(values) != expected {
				t.Fatal("native scope count", len(values), expected)
			}
			var value asset.Asset
			for _, v := range values {
				if v.Identity.NativeID == f.id {
					value = v
				}
			}
			if value.ID == "" {
				t.Fatal("missing recovery point")
			}
			planner := cleanup.NewService(repo, registry)
			task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", CreatedBy: "operator", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: value.ID}}})
			if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || len(task.ImpactItems) != 0 {
				t.Fatal("independent recovery plan", task, err)
			}
			warningCode := plan.WarningNetappSnapshotDelete
			if kind == netappBackupType {
				warningCode = plan.WarningNetappBackupDelete
			}
			warned := false
			for _, w := range task.Task.Warnings {
				if w.Code == warningCode && strings.Contains(w.Message, "permanently removes") {
					warned = true
				}
			}
			if !warned {
				t.Fatal("irreversible recovery warning missing")
			}
			attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "recovery-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
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
					t.Fatal("resume", err)
				}
				current, err := repo.Executions().GetActionByExecutionStep(t.Context(), attempt.ID, string(task.Steps[0].ID))
				if err != nil {
					t.Fatal(err)
				}
				return current
			}
			f.failAfterDelete = true
			first := resume(true)
			if first.ProviderResult["binding"] == nil || f.deletes != 1 {
				t.Fatal("accepted receipt not saved")
			}
			again := resume(true)
			if again.ProviderResult["binding"] != first.ProviderResult["binding"] {
				t.Fatal("restart lost receipt")
			}
			f.readFault = 0
			f.done = true
			resume(true)
			terminal := resume(true)
			if terminal.ProviderResult["operation_done"] != true {
				t.Fatal("terminal checkpoint missing")
			}
			f.missing[f.id] = true
			resume(true)
			resume(false)
			active, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
			if err != nil || len(active) != expected-1 || f.deletes != 1 {
				t.Fatal("wrong deletion scope", len(active), f.deletes, err)
			}
		})
	}
}

func TestNetappBackupMalformedAssignmentPreservesInventory(t *testing.T) {
	for _, field := range []string{"dataProtection", "backup", "backupPolicyId"} {
		t.Run(field, func(t *testing.T) {
			f := newNetappRecoveryFixture(t, netappBackupType)
			value := f.asset(t)
			p := object(f.objects[f.source]["properties"])
			p["dataProtection"] = map[string]any{"backup": map[string]any{}}
			switch field {
			case "dataProtection":
				p["dataProtection"] = []any{}
			case "backup":
				object(p["dataProtection"])["backup"] = []any{}
			case "backupPolicyId":
				object(object(p["dataProtection"])["backup"])["backupPolicyId"] = true
			}
			req := netappRequest(f.runtime, f.kind)
			req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			req.KnownNativeIDs = []string{value.Identity.NativeID}
			req.KnownNativeMetadata = map[string]map[string]any{f.id: value.Normalized}
			page, err := f.runtime.List(t.Context(), req)
			if err == nil || page.Complete || len(page.AbsentNativeIDs) != 0 {
				t.Fatal("malformed policy interpreted as unassigned", field, err)
			}
		})
	}
}

func TestNetappBackupUnknownChronologyKeepsNativeProtection(t *testing.T) {
	f := newNetappRecoveryFixture(t, netappBackupType)
	account := strings.Join(strings.Split(f.id, "/")[:9], "/")
	object(f.objects[f.source]["properties"])["dataProtection"] = map[string]any{"backup": map[string]any{"backupPolicyId": account + "/backuppolicies/item"}}
	delete(object(f.objects[f.id]["properties"]), "snapshotCreationDate")
	value := f.asset(t)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", value)
	if err != nil {
		t.Fatal("nullable chronology disabled a native action", err)
	}
	req := contracts.ActionRequest{Asset: value, Action: "delete"}
	f.deleteFault = 409
	result, err := driver.Execute(t.Context(), req)
	if err == nil || result.Data["binding"] != nil || f.deletes != 1 {
		t.Fatal("native latest-backup guard bypassed", result, err)
	}
	if read, err := driver.Readback(t.Context(), req); err != nil || !read.Exists {
		t.Fatal("guarded backup was closed", read, err)
	}
}

func TestNetappBackupInventoryReusesVerifiedCollection(t *testing.T) {
	f := newNetappRecoveryFixture(t, netappBackupType)
	ids := []string{f.id}
	for i := 0; i < 8; i++ {
		id := f.peer(fmt.Sprint("peer-", i), fmt.Sprintf("2018-01-%02dT00:00:00Z", i+1))
		object(f.objects[id]["properties"])["backupId"] = fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		ids = append(ids, id)
	}
	f.hidden[ids[3]] = true
	f.paged = true
	old := f.override
	reads, sourceReads := 0, 0
	f.override = func(q *http.Request) (*http.Response, bool) {
		if q.Method == "GET" && strings.HasPrefix(strings.ToLower(q.URL.Path), redisParentID(f.id)+"/backups/") {
			reads++
		}
		if q.Method == "GET" && strings.EqualFold(q.URL.Path, f.source) {
			sourceReads++
		}
		return old(q)
	}
	req := netappRequest(f.runtime, f.kind)
	req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
	req.KnownNativeIDs = ids
	page, err := f.runtime.List(t.Context(), req)
	if err != nil || len(page.Items) != len(ids) || reads > 6*len(ids) || sourceReads != 2 {
		t.Fatal("backup inventory repeated complete peer reads per item", len(page.Items), reads, sourceReads, err)
	}
}
