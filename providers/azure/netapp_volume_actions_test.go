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
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

type netappVolumeFixture struct {
	*netappFixture
	id                                   string
	deletes, polls, readFault, pollFault int
	done, failAfterDelete                bool
}

func newNetappVolumeFixture(t *testing.T) *netappVolumeFixture {
	base := newNetappFixture(t)
	f := &netappVolumeFixture{netappFixture: base, id: strings.ToLower(resourceID(netappAccountType, "first")) + "/capacitypools/item/volumes/item"}
	base.override = func(q *http.Request) (*http.Response, bool) {
		path := strings.ToLower(q.URL.Path)
		if q.Method == "DELETE" {
			if path != f.id || q.URL.Query().Get("api-version") != netappVersion || q.URL.Query().Has("forceDelete") || q.ContentLength != 0 {
				t.Fatal("unreviewed native mutation", q.URL)
			}
			f.deletes++
			if f.failAfterDelete {
				f.readFault = 503
			}
			h := http.Header{}
			h.Set("Azure-AsyncOperation", netappTestPollURL("status_url"))
			h.Set("Location", netappTestPollURL("result_url"))
			return &http.Response{StatusCode: 202, Header: h, Body: io.NopCloser(strings.NewReader(""))}, true
		}
		if f.readFault != 0 && strings.HasPrefix(path, f.id) {
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
func (f *netappVolumeFixture) assets(t *testing.T) []asset.Asset {
	t.Helper()
	values := []asset.Asset{}
	for _, kind := range []string{netappVolumeType, netappVolumeType + "/snapshots", netappVolumeType + "/subvolumes", netappVolumeType + "/volumeQuotaRules"} {
		req := netappRequest(f.runtime, kind)
		req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
		page, err := f.runtime.List(t.Context(), req)
		if err != nil || len(page.Items) != 1 {
			t.Fatal("native scope", kind, len(page.Items), err)
		}
		item := page.Items[0]
		values = append(values, asset.Asset{ID: asset.AssetID(fmt.Sprint("netapp-", len(values))), Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeID: item.NativeID, NativeType: kind}, Normalized: item.Normalized, Location: item.Location})
	}
	return values
}
func netappVolumeRequest(values []asset.Asset) contracts.ActionRequest {
	req := contracts.ActionRequest{Asset: values[0], Action: "delete", IdempotencyKey: "netapp-delete"}
	for _, v := range values[1:] {
		req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: v, ControllerID: req.Asset.ID, Delete: true})
	}
	return req
}
func TestNetappVolumeDurableCascadeAndIndependentAbsence(t *testing.T) {
	f := newNetappVolumeFixture(t)
	values := f.assets(t)
	req := netappVolumeRequest(values)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
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
	req.ExecutionResult = &result
	if _, err := driver.Wait(t.Context(), req, result); err == nil {
		t.Fatal("failed read accepted")
	}
	f.readFault = 0
	if _, err := driver.Execute(t.Context(), req); err != nil || f.deletes != 1 {
		t.Fatal("delete replayed", err)
	}
	f.done = true
	for i := 0; i < 2; i++ {
		out, err := driver.Wait(t.Context(), req, result)
		if err != nil || out.Done {
			t.Fatal("operation closed live scope", out, err)
		}
		result.Data = out.Data
		req.ExecutionResult = &result
	}
	if result.Data["operation_done"] != true {
		t.Fatal("terminal native result not saved")
	}
	f.missing[f.id] = true
	if out, err := driver.Wait(t.Context(), req, result); err != nil || out.Done {
		t.Fatal("parent absence erased children", out, err)
	}
	for _, v := range values[1:] {
		f.missing[v.Identity.NativeID] = true
	}
	if out, err := driver.Wait(t.Context(), req, result); err != nil || !out.Done {
		t.Fatal("independent child absence", out, err)
	}
	f.missing[redisParentID(f.id)] = true
	if _, err := driver.Readback(t.Context(), req); err == nil || isNotFound(err) {
		t.Fatal("parent lookup failure became volume absence")
	}
}
func TestNetappVolumeChangedReviewNeverDeletes(t *testing.T) {
	for _, fault := range []string{"volume", "pool", "account", "group", "child", "new child", "root protection", "child protection", "lock", "restoring", "replication", "missing impact", "retention", "impact connection", "impact configuration", "parameters", "receipt", "review proof", "unknown subvolume flag", "clone", "unknown volume type", "replica type"} {
		t.Run(fault, func(t *testing.T) {
			f := newNetappVolumeFixture(t)
			values := f.assets(t)
			req := netappVolumeRequest(values)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
			if err != nil {
				t.Fatal(err)
			}
			old := f.override
			switch fault {
			case "volume":
				object(f.objects[f.id]["properties"])["futurePrivateField"] = "changed"
			case "pool":
				object(f.objects[redisParentID(f.id)]["properties"])["poolId"] = testApplication
			case "account":
				object(f.objects[redisParentID(redisParentID(f.id))]["properties"])["futurePrivateField"] = "changed"
			case "group":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if q.URL.Query().Get("api-version") == resourcesVersion && strings.HasSuffix(q.URL.Path, "/test") {
						return jsonResponse(403, nil, nil), true
					}
					return old(q)
				}
			case "child":
				object(f.objects[values[1].Identity.NativeID]["properties"])["futurePrivateField"] = "changed"
			case "new child":
				raw := batchClone(f.objects[values[1].Identity.NativeID])
				id := f.id + "/snapshots/new"
				raw["id"], raw["name"] = id, "new"
				f.objects[id] = raw
			case "root protection":
				f.objects[f.id]["tags"] = map[string]any{"steward:protected": "true"}
			case "child protection":
				f.objects[values[1].Identity.NativeID]["tags"] = map[string]any{"steward:protected": "true"}
			case "lock":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(strings.ToLower(q.URL.Path), "/providers/microsoft.authorization/locks") {
						return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": f.id + "/providers/Microsoft.Authorization/locks/guard", "properties": map[string]any{"level": "CanNotDelete"}}}}, nil), true
					}
					return old(q)
				}
			case "restoring":
				object(f.objects[f.id]["properties"])["isRestoring"] = true
			case "replication":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(strings.ToLower(q.URL.Path), "/listreplications") {
						return jsonResponse(200, map[string]any{"value": []any{map[string]any{"remoteVolumeResourceId": strings.Replace(f.id, "/first/", "/second/", 1), "remoteVolumeRegion": "westus"}}}, nil), true
					}
					return old(q)
				}
			case "missing impact":
				req.LifecycleImpacts = req.LifecycleImpacts[1:]
			case "retention":
				req.LifecycleImpacts[0].Delete = false
			case "impact connection":
				req.LifecycleImpacts[0].Asset.Identity.ConnectionID = "different"
			case "impact configuration":
				req.LifecycleImpacts[0].Asset.Normalized = batchClone(req.LifecycleImpacts[0].Asset.Normalized)
				req.LifecycleImpacts[0].Asset.Normalized["_netapp_configuration"] = "changed"
			case "parameters":
				req.Parameters = map[string]any{"forceDelete": true}
			case "receipt":
				req.ExecutionResult = &contracts.ActionResult{Data: map[string]any{"binding": "forged"}}
			case "review proof":
				req.Asset.Normalized = batchClone(req.Asset.Normalized)
				req.Asset.Normalized[netappVolumeProof] = "changed"
			case "clone":
				object(f.objects[f.id]["properties"])["cloneProgress"] = 50
			case "unknown volume type":
				object(f.objects[f.id]["properties"])["volumeType"] = "Future"
			case "replica type":
				object(f.objects[f.id]["properties"])["volumeType"] = "DataProtection"
			case "unknown subvolume flag":
				object(f.objects[f.id]["properties"])["enableSubvolumes"] = "Future"
			}
			if _, err := driver.Execute(t.Context(), req); err == nil || f.deletes != 0 {
				t.Fatal("changed boundary deleted", fault, err)
			}
		})
	}
}
func TestNetappVolumeReplicationPagination(t *testing.T) {
	for _, fault := range []string{"", "foreign page", "cycle", "invalid query", "filter", "malformed peer"} {
		t.Run(fault, func(t *testing.T) {
			f := newNetappVolumeFixture(t)
			old := f.override
			calls := 0
			f.override = func(q *http.Request) (*http.Response, bool) {
				if !strings.HasSuffix(strings.ToLower(q.URL.Path), "/listreplications") {
					return old(q)
				}
				calls++
				if calls == 1 {
					body, _ := io.ReadAll(q.Body)
					if q.Method != "POST" || string(body) != `{"exclude":"Deleted"}` {
						t.Fatal("native first page contract", q.Method, string(body))
					}
					next := apiURL(f.id+"/listReplications", netappVersion) + "&$skiptoken=two"
					switch fault {
					case "foreign page":
						next = strings.Replace(next, "management.azure.com", "foreign.example", 1)
					case "cycle":
						next = q.URL.String()
					case "invalid query":
						next += "&bad=%zz"
					case "filter":
						next += "&exclude=None"
					}
					return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": next}, nil), true
				}
				if q.Method != "GET" || q.ContentLength != 0 {
					t.Fatal("native continuation must use GET")
				}
				peer := map[string]any{"remoteVolumeResourceId": strings.Replace(f.id, "/first/", "/second/", 1), "remoteVolumeRegion": "westus"}
				if fault == "malformed peer" {
					peer["remoteVolumeResourceId"] = "not-an-arm-id"
				}
				return jsonResponse(200, map[string]any{"value": []any{peer}}, nil), true
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			rows, err := c.netappActiveReplications(t.Context(), f.id)
			if fault == "" {
				if err != nil || len(rows) != 1 || calls != 2 {
					t.Fatal("lost peer on later page", len(rows), err)
				}
			} else if err == nil {
				t.Fatal("incomplete replication accepted", fault)
			}
		})
	}
}
func TestNetappVolumeLifecycleReview(t *testing.T) {
	f := newNetappVolumeFixture(t)
	values := f.assets(t)
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := c.netappVolumeContribution(values[0], values)
	if err != nil || len(contribution.Bindings) != 3 || len(contribution.Unresolved) != 0 {
		t.Fatal(contribution, err)
	}
	for _, b := range contribution.Bindings {
		if b.ControllerAssetID != values[0].ID || b.CleanupPolicy != graph.CleanupDelegate || b.DirectCleanupAllowed || b.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] != true {
			t.Fatal("invalid native ownership", b)
		}
	}
	missing, err := c.netappVolumeContribution(values[0], values[:2])
	if err != nil || len(missing.Unresolved) != 2 {
		t.Fatal("missing children unreviewed", missing, err)
	}
	for _, u := range missing.Unresolved {
		if !u.BlocksCleanup {
			t.Fatal("missing child allowed deletion")
		}
	}
	if _, err := f.runtime.ResolveAction(t.Context(), "connection", values[1]); err == nil {
		t.Fatal("cascade enabled independent snapshot deletion")
	}
}
func TestNetappVolumeExpiredCallbackRequiresWholeScopeAbsence(t *testing.T) {
	f := newNetappVolumeFixture(t)
	values := f.assets(t)
	req := netappVolumeRequest(values)
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	f.pollFault = 404
	f.missing[f.id] = true
	if out, err := driver.Wait(t.Context(), req, result); err == nil || out.Done {
		t.Fatal("expired callback erased children", out, err)
	}
	for _, v := range values[1:] {
		f.missing[v.Identity.NativeID] = true
	}
	if out, err := driver.Wait(t.Context(), req, result); err != nil || !out.Done || out.Data["operation_done"] == true {
		t.Fatal("own scope absence", out, err)
	}
}
func TestNetappVolumeWorkerRestartAndRetainedBackups(t *testing.T) {
	f := newNetappVolumeFixture(t)
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{}
	for _, k := range netappResources {
		kinds = append(kinds, k.kind)
	}
	values := azureNativeWorkerScan(t, f.runtime, netappSource, repo, registry, kinds, false, true)
	if len(values) != 22 {
		t.Fatal("incomplete native scan", len(values))
	}
	var root asset.Asset
	for _, v := range values {
		if v.Identity.NativeID == f.id {
			root = v
		}
	}
	if root.ID == "" {
		t.Fatal("missing root")
	}
	planner := cleanup.NewService(repo, registry)
	selectors := []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: root.ID}}
	retained, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "operator", RequestOptions: map[asset.AssetID]map[string]any{root.ID: {"retain_all_resources": true}}})
	if err != nil || len(retained.Task.Blockers) == 0 {
		t.Fatal("retention silently bypassed", retained, err)
	}
	task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "operator"})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || len(task.ImpactItems) != 3 {
		t.Fatal("native cleanup plan", task, err)
	}
	warned := false
	for _, w := range task.Task.Warnings {
		if w.Code == plan.WarningNetappVolumeDelete {
			warned = true
			if !strings.Contains(w.Message, "unmount") || !strings.Contains(w.Message, "backup vaults") {
				t.Fatal("incomplete deletion consequence", w)
			}
		}
	}
	if !warned {
		t.Fatal("missing consequence warning")
	}
	attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "netapp-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
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
		t.Fatal("missing controller job")
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
		t.Fatal("accepted receipt not saved", first)
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
		t.Fatal("terminal checkpoint missing", terminal)
	}
	f.missing[f.id] = true
	resume(true)
	for _, v := range values {
		if netappVolumeChild(v.Identity.NativeType) && redisParentID(v.Identity.NativeID) == f.id {
			f.missing[v.Identity.NativeID] = true
		}
	}
	resume(true)
	resume(false)
	if f.deletes != 1 {
		t.Fatal("native delete replayed")
	}
	active, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(active) != 18 {
		t.Fatal("wrong final scope", len(active), err)
	}
	backups := 0
	for _, v := range active {
		if v.Identity.NativeType == netappAccountType+"/backupVaults/backups" {
			backups++
		}
		wire, _ := json.Marshal(v)
		if strings.Contains(string(wire), "netapp-private-canary") {
			t.Fatal("private raw data persisted")
		}
	}
	if backups != 2 {
		t.Fatal("volume cascade erased retained backups")
	}
}

func TestNetappSubvolumeDisabledStillReadsKnownChildren(t *testing.T) {
	f := newNetappVolumeFixture(t)
	id := f.id + "/subvolumes/item"
	object(f.objects[f.id]["properties"])["enableSubvolumes"] = "Disabled"
	old := f.override
	reads := 0
	f.override = func(q *http.Request) (*http.Response, bool) {
		if strings.EqualFold(q.URL.Path, f.id+"/subvolumes") {
			t.Fatal("disabled subvolume collection was queried")
		}
		if strings.EqualFold(q.URL.Path, id) {
			reads++
		}
		return old(q)
	}
	req := netappRequest(f.runtime, netappVolumeType+"/subvolumes")
	req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
	req.KnownNativeIDs = []string{id}
	page, err := f.runtime.List(t.Context(), req)
	if err != nil || len(page.Items) != 1 || reads < 2 || len(page.AbsentNativeIDs) != 0 {
		t.Fatal("known disabled child disappeared", page, err)
	}
	f.missing[id] = true
	page, err = f.runtime.List(t.Context(), req)
	if err != nil || len(page.Items) != 0 || len(page.AbsentNativeIDs) != 1 {
		t.Fatal("known child not independently reconciled", page, err)
	}
}
func TestNetappVolumeKnownReviewAndRecreation(t *testing.T) {
	f := newNetappVolumeFixture(t)
	values := f.assets(t)
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	req := netappRequest(f.runtime, netappVolumeType)
	req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
	req.KnownNativeIDs = []string{f.id}
	req.KnownNativeMetadata = map[string]map[string]any{f.id: values[0].Normalized}
	child := values[1].Identity.NativeID
	f.hidden[child] = true
	page, err := f.runtime.List(t.Context(), req)
	if err != nil || len(object(object(page.Items[0].Normalized[netappVolumeReview])["members"])) != 3 {
		t.Fatal("known omitted member lost", err)
	}
	f.missing[child] = true
	page, err = f.runtime.List(t.Context(), req)
	if err != nil || object(object(object(page.Items[0].Normalized[netappVolumeReview])["members"])[child])["absent"] != true {
		t.Fatal("known absence not tracked", err)
	}
	req.KnownNativeMetadata[f.id] = batchClone(req.KnownNativeMetadata[f.id])
	req.KnownNativeMetadata[f.id][netappVolumeProof] = "tampered"
	refreshed, err := f.runtime.List(t.Context(), req)
	if err != nil || len(refreshed.Items) != 1 || refreshed.Items[0].Normalized[netappVolumeProof] == "tampered" {
		t.Fatal("read-only hints were treated as mutation authority", err)
	}
	foreign := strings.Replace(f.id, testSubscription, testApplication, 1) + "/snapshots/item"
	object(object(req.KnownNativeMetadata[f.id][netappVolumeReview])["members"])[foreign] = map[string]any{"kind": netappVolumeType + "/snapshots", "configuration": "forged"}
	if _, err = f.runtime.List(t.Context(), req); err == nil {
		t.Fatal("foreign read hint accepted")
	}
	actionReq := netappVolumeRequest(values)
	driver, err := newNetappVolumeAction(c, "connection", values[0])
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), actionReq)
	if err != nil {
		t.Fatal(err)
	}
	actionReq.ExecutionResult = &result
	object(f.objects[f.id]["properties"])["fileSystemId"] = testApplication
	if _, err := driver.Readback(t.Context(), actionReq); err == nil {
		t.Fatal("recreated volume matched saved action")
	}
	f.missing[f.id] = true
	for _, v := range values[1:] {
		f.missing[v.Identity.NativeID] = true
	}
	object(f.objects[redisParentID(f.id)]["properties"])["poolId"] = testApplication
	if _, err := driver.Readback(t.Context(), actionReq); err == nil {
		t.Fatal("recreated pool proved old volume absent")
	}
}

func TestNetappVolumeInitialEligibility(t *testing.T) {
	for _, fault := range []string{"restoring", "clone", "replica", "short clone", "unknown type", "missing incarnation", "protected parent", "protected child"} {
		t.Run(fault, func(t *testing.T) {
			f := newNetappVolumeFixture(t)
			props := object(f.objects[f.id]["properties"])
			switch fault {
			case "restoring":
				props["isRestoring"] = true
			case "clone":
				props["cloneProgress"] = 25
			case "replica":
				props["volumeType"] = "DataProtection"
			case "short clone":
				props["volumeType"] = "ShortTermClone"
			case "unknown type":
				props["volumeType"] = "Future"
			case "missing incarnation":
				delete(props, "fileSystemId")
			case "protected parent":
				f.objects[redisParentID(f.id)]["tags"] = map[string]any{"steward:protected": "true"}
			case "protected child":
				f.objects[f.id+"/snapshots/item"]["tags"] = map[string]any{"steward:protected": "true"}
			}
			req := netappRequest(f.runtime, netappVolumeType)
			req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			page, err := f.runtime.List(t.Context(), req)
			if err != nil || len(page.Items) != 1 || page.Items[0].Actionable == nil || *page.Items[0].Actionable || page.Items[0].Normalized["cleanup_protected"] != true {
				t.Fatal("unsafe initial eligibility", fault, len(page.Items), err)
			}
		})
	}
}

func TestNetappVolumeGroupRecheckMustRemainAuthoritative(t *testing.T) {
	f := newNetappVolumeFixture(t)
	old := f.override
	calls := 0
	f.override = func(q *http.Request) (*http.Response, bool) {
		if q.URL.Query().Get("api-version") == resourcesVersion && strings.HasSuffix(q.URL.Path, "/test") {
			calls++
			status := 200
			if calls == 2 {
				status = 202
			}
			return jsonResponse(status, map[string]any{"id": strings.ToLower(q.URL.Path), "name": "test", "type": groupType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}, nil), true
		}
		return old(q)
	}
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.netappVolumeBoundary(t.Context(), f.id, nil); err == nil || calls != 2 {
		t.Fatal("nonterminal group response preserved stale boundary", err, calls)
	}
}
func TestNetappVolumeCredentialRotationRefreshesInventoryNotOldActions(t *testing.T) {
	f := newNetappVolumeFixture(t)
	values := f.assets(t)
	old := values[0]
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	c.fingerprint[0]++
	if _, err := newNetappVolumeAction(c, "connection", old); err == nil {
		t.Fatal("old deletion proof survived credential rotation")
	}
	req := netappRequest(f.runtime, netappVolumeType)
	req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
	req.KnownNativeIDs = []string{f.id}
	req.KnownNativeMetadata = map[string]map[string]any{f.id: old.Normalized}
	page, err := f.runtime.listNetapp(t.Context(), c, req)
	if err != nil || len(page.Items) != 1 || page.Items[0].Normalized[netappVolumeProof] == old.Normalized[netappVolumeProof] {
		t.Fatal("credential rotation stranded inventory", err)
	}
}
