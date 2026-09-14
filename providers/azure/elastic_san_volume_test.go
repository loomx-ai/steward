package azure

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

type elasticSanVolumeFixture struct {
	*elasticSanCleanupFixture
	volumeDeletes, snapshotDeletes int
	force, permanent               bool
}

func newElasticSanVolumeFixture(t *testing.T) *elasticSanVolumeFixture {
	f := &elasticSanVolumeFixture{elasticSanCleanupFixture: newElasticSanCleanupFixture(t)}
	f.kind = elasticSanVolumeType
	f.hook = func(r *http.Request) (*http.Response, bool) {
		if r.Method != "DELETE" {
			return nil, false
		}
		id := strings.ToLower(r.URL.Path)
		raw := f.values[id]
		if raw == nil {
			t.Fatal("unexpected DELETE identity", id)
		}
		_, kind, _ := parseID(id)
		if r.ContentLength != 0 || r.URL.Query().Get("api-version") != elasticSanVersion {
			t.Fatal("wrong native deletion")
		}
		switch kind {
		case strings.ToLower(elasticSanVolumeType):
			f.volumeDeletes++
			f.force = r.Header.Get("x-ms-force-delete") == "true"
			f.permanent = r.URL.Query().Get("deleteType") == "permanent"
			if r.Header.Get("x-ms-delete-snapshots") != "false" || r.Header.Get("x-ms-force-delete") != "false" && !f.force || f.permanent != f.retained[id] || len(r.URL.Query()) != 1+btoi(f.permanent) {
				t.Fatal("volume mutation broadened native scope")
			}
		case strings.ToLower(elasticSanSnapshotType):
			f.snapshotDeletes++
			if len(r.URL.Query()) != 1 || r.Header.Get("x-ms-force-delete") != "" || r.Header.Get("x-ms-delete-snapshots") != "" {
				t.Fatal("snapshot got volume options")
			}
		default:
			t.Fatal("deleted a parent or network object", kind)
		}
		object(raw["properties"])["provisioningState"] = "Deleting"
		return jsonResponse(202, raw, http.Header{"Location": {elasticSanTestPollURL()}, "X-Ms-Request-Id": {"native-volume-delete"}}), true
	}
	return f
}
func btoi(value bool) int {
	if value {
		return 1
	}
	return 0
}
func (f *elasticSanVolumeFixture) volumeAction(t *testing.T, id string) (contracts.ActionDriver, contracts.ActionRequest) {
	t.Helper()
	batch, err := f.runtime.List(t.Context(), f.request(elasticSanVolumeType))
	if err != nil {
		t.Fatal(err)
	}
	var value asset.Asset
	for _, item := range batch.Items {
		if item.NativeID == id {
			value = elasticSanTestAsset(item)
		}
	}
	a, err := f.runtime.ResolveAction(t.Context(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	req := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "volume-delete"}
	children := object(object(object(value.Normalized[elasticSanSnapshotCleanup])["volume"])["snapshots"])
	if len(children) > 0 {
		snapshots, err := f.runtime.List(t.Context(), f.request(elasticSanSnapshotType))
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range snapshots.Items {
			if children[item.NativeID] != nil {
				req.PrerequisiteDeletions = append(req.PrerequisiteDeletions, contracts.ActionImpact{Asset: elasticSanTestAsset(item), ControllerID: value.ID, Delete: true})
			}
		}
	}
	return a, req
}
func (f *elasticSanVolumeFixture) softDelete(id string) string {
	raw := f.values[id]
	newID := id + "-1770765061"
	raw = maps.Clone(raw)
	raw["properties"] = maps.Clone(object(raw["properties"]))
	raw["id"], raw["name"] = newID, last(newID)
	object(raw["properties"])["provisioningState"] = "Deleted"
	delete(f.values, id)
	f.values[newID], f.retained[newID] = raw, true
	return newID
}

func TestElasticSanVolumeSoftDeleteAndSeparatePermanentRemoval(t *testing.T) {
	f := newElasticSanVolumeFixture(t)
	id := f.ids[elasticSanVolumeType]
	a, req := f.volumeAction(t, id)
	if _, err := a.Execute(t.Context(), req); err == nil || f.volumeDeletes != 0 {
		t.Fatal("deleted with live snapshots")
	}
	delete(f.values, f.ids[elasticSanSnapshotType])
	result, err := a.Execute(t.Context(), req)
	if err != nil || f.permanent || f.force || f.volumeDeletes != 1 {
		t.Fatal("default deletion", err)
	}
	wait, err := a.Wait(t.Context(), req, result)
	if err != nil || wait.Done {
		t.Fatal("callback completed live volume", err)
	}
	retainedID := f.softDelete(id)
	result.Data = wait.Data
	wait, err = a.Wait(t.Context(), req, result)
	if err != nil || !wait.Done || object(wait.Data["outcome"])["retained_native_id"] != retainedID || object(wait.Data["outcome"])["outcome"] != "soft_deleted" || f.volumeDeletes != 1 {
		t.Fatal("soft deletion outcome lost", wait, err)
	}
	result.Data = wait.Data
	req.ExecutionResult = &result
	if _, err := a.Execute(t.Context(), req); err != nil || f.volumeDeletes != 1 {
		t.Fatal("repeated DELETE or implicit purge", err)
	}
	retained, permanentReq := f.volumeAction(t, retainedID)
	if permanentReq.Asset.Normalized["cleanup_deletion_mode"] != "permanent" {
		t.Fatal("retained purge not explicit")
	}
	result, err = retained.Execute(t.Context(), permanentReq)
	if err != nil || !f.permanent || f.volumeDeletes != 2 {
		t.Fatal("selected retained deletion", err)
	}
	wait, err = retained.Wait(t.Context(), permanentReq, result)
	if err != nil || wait.Done {
		t.Fatal("retained own GET404 hid listed copy", err)
	}
	delete(f.values, retainedID)
	result.Data = wait.Data
	wait, err = retained.Wait(t.Context(), permanentReq, result)
	if err != nil || !wait.Done || object(wait.Data["outcome"])["outcome"] != "absent" {
		t.Fatal("permanent absence", wait, err)
	}
}

// A completed operation callback does not make a transitional retained volume
// terminal. Exercise both native naming forms and recovery without a receipt.
func TestElasticSanVolumeSoftDeleteRequiresTerminalState(t *testing.T) {
	for _, naming := range []string{"renamed", "same-id"} {
		for _, status := range []string{"SoftDeleting", "Deleting", "Restoring", "Succeeded", "", "FutureState"} {
			t.Run(naming+"/"+status, func(t *testing.T) {
				f := newElasticSanVolumeFixture(t)
				id := f.ids[elasticSanVolumeType]
				a, req := f.volumeAction(t, id)
				delete(f.values, f.ids[elasticSanSnapshotType])
				result, err := a.Execute(t.Context(), req)
				if err != nil {
					t.Fatal(err)
				}
				retainedID := f.softDelete(id)
				if naming == "same-id" {
					raw := f.values[retainedID]
					delete(f.values, retainedID)
					delete(f.retained, retainedID)
					retainedID = id
					raw["id"], raw["name"] = id, last(id)
					f.values[id], f.retained[id] = raw, true
				}
				object(f.values[retainedID]["properties"])["provisioningState"] = status
				wait, err := a.Wait(t.Context(), req, result)
				transitional := status == "SoftDeleting" || status == "Deleting"
				if wait.Done || transitional && (err != nil || wait.State != status) || !transitional && err == nil {
					t.Fatal("nonterminal retained state completed deletion", wait, err)
				}
				// An expired callback cannot turn the same observation into completion.
				f.pollStatus = 404
				wait, err = a.Wait(t.Context(), req, result)
				if err == nil || wait.Done {
					t.Fatal("expired callback hid nonterminal volume", wait, err)
				}
				f.pollStatus = 200
				if transitional {
					// Losing the execution receipt must still adopt the in-flight native
					// deletion after restoring the signed plan into a fresh runtime.
					encoded, err := json.Marshal(req)
					if err != nil {
						t.Fatal(err)
					}
					var restored contracts.ActionRequest
					if err := json.Unmarshal(encoded, &restored); err != nil {
						t.Fatal(err)
					}
					fresh, err := NewRuntime(f.runtime.credentials)
					if err != nil {
						t.Fatal(err)
					}
					fresh.transport = f.runtime.transport
					a, err = fresh.ResolveAction(t.Context(), "connection", restored.Asset)
					if err != nil {
						t.Fatal(err)
					}
					result, err = a.Execute(t.Context(), restored)
					if err != nil || f.volumeDeletes != 1 {
						t.Fatal("recovery repeated native deletion", err, f.volumeDeletes)
					}
					req = restored
				}
				object(f.values[retainedID]["properties"])["provisioningState"] = "Deleted"
				wait, err = a.Wait(t.Context(), req, result)
				if err != nil || !wait.Done || object(wait.Data["outcome"])["retained_native_id"] != retainedID || f.volumeDeletes != 1 {
					t.Fatal("terminal retained state not reconciled", wait, err)
				}
			})
		}
	}
}

func TestElasticSanVolumeGuardsAndExplicitForce(t *testing.T) {
	for _, mode := range []string{"snapshot-omitted", "missing-review", "retained-review", "foreign-review", "new-snapshot", "retention", "created", "guid", "size", "managed", "lock", "forbidden", "missing-group", "force", "invalid-force"} {
		t.Run(mode, func(t *testing.T) {
			f := newElasticSanVolumeFixture(t)
			id := f.ids[elasticSanVolumeType]
			a, req := f.volumeAction(t, id)
			raw := f.values[id]
			props := object(raw["properties"])
			if mode != "snapshot-omitted" {
				delete(f.values, f.ids[elasticSanSnapshotType])
			}
			switch mode {
			case "snapshot-omitted":
				f.omitted[f.ids[elasticSanSnapshotType]] = true
			case "missing-review":
				req.PrerequisiteDeletions = nil
			case "retained-review":
				req.PrerequisiteDeletions[0].Delete = false
			case "foreign-review":
				req.PrerequisiteDeletions[0].ControllerID = "other"
			case "new-snapshot":
				child := elasticSanTestRecord(elasticSanSnapshotType)
				childID := f.ids[elasticSanSnapshotType] + "-new"
				child["id"], child["name"] = childID, last(childID)
				object(child["properties"])["creationData"] = map[string]any{"sourceId": id}
				f.values[childID] = child
			case "retention":
				object(f.values[f.ids[elasticSanGroupType]]["properties"])["deleteRetentionPolicy"] = map[string]any{"policyState": "Disabled"}
			case "created":
				object(raw["systemData"])["createdAt"] = "2026-04-01T01:00:00Z"
			case "guid":
				props["volumeId"] = testSubscription
			case "size":
				props["sizeGiB"] = 16
			case "managed":
				props["managedBy"] = map[string]any{"resourceId": resourceID(vmType, "vm")}
			case "lock":
				f.locks = []any{map[string]any{"id": id + "/providers/Microsoft.Authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "forbidden":
				f.hook = func(r *http.Request) (*http.Response, bool) {
					if strings.EqualFold(r.URL.Path, id) && r.Method == "GET" {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					return nil, false
				}
			case "missing-group":
				f.hook = func(r *http.Request) (*http.Response, bool) {
					if strings.EqualFold(r.URL.Path, f.ids[elasticSanGroupType]) {
						return jsonResponse(404, map[string]any{}, nil), true
					}
					return nil, false
				}
			case "force":
				req.Parameters = map[string]any{"force_delete": true}
			case "invalid-force":
				req.Parameters = map[string]any{"force_delete": "true"}
			}
			_, err := a.Execute(t.Context(), req)
			if mode == "force" {
				if err != nil || f.volumeDeletes != 1 || !f.force {
					t.Fatal("explicit force not bound", err)
				}
			} else if err == nil && mode != "missing-group" || f.volumeDeletes != 0 {
				t.Fatal("unsafe deletion", err)
			}
		})
	}
}

func TestElasticSanVolumeReadbackRequiresBothPopulations(t *testing.T) {
	for _, mode := range []string{"retained-denied", "retained-404", "duplicate-guid", "recreated", "restored", "snapshot-remains", "expired-live", "absent"} {
		t.Run(mode, func(t *testing.T) {
			f := newElasticSanVolumeFixture(t)
			id := f.ids[elasticSanVolumeType]
			a, req := f.volumeAction(t, id)
			delete(f.values, f.ids[elasticSanSnapshotType])
			result, err := a.Execute(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "retained-denied", "retained-404":
				status := 403
				if mode == "retained-404" {
					status = 404
				}
				f.hook = func(r *http.Request) (*http.Response, bool) {
					if r.Header.Get("x-ms-access-soft-deleted-resources") == "true" {
						return jsonResponse(status, map[string]any{}, nil), true
					}
					return nil, false
				}
				delete(f.values, id)
			case "duplicate-guid":
				for key, raw := range f.values {
					if f.retained[key] && strings.HasSuffix(key, "-1751081600") && strings.Contains(key, "/volumes/") {
						object(raw["properties"])["volumeId"] = testTenant
					}
				}
			case "recreated":
				object(f.values[id]["properties"])["volumeId"] = testSubscription
			case "restored":
				raw := f.values[id]
				newID := id + "-restored"
				delete(f.values, id)
				raw["id"], raw["name"] = newID, last(newID)
				f.values[newID] = raw
			case "snapshot-remains":
				delete(f.values, id)
				child := elasticSanTestRecord(elasticSanSnapshotType)
				object(child["properties"])["creationData"] = map[string]any{"sourceId": id}
				f.values[f.ids[elasticSanSnapshotType]] = child
			case "expired-live":
				f.pollStatus = 404
			case "absent":
				delete(f.values, id)
			}
			wait, err := a.Wait(t.Context(), req, result)
			if mode == "absent" {
				if err != nil || !wait.Done {
					t.Fatal("absence failed", wait, err)
				}
			} else if err == nil || wait.Done {
				t.Fatal("unproven absence accepted", mode, wait, err)
			}
		})
	}
}

func TestElasticSanVolumeOriginalSoftDeletionIdentity(t *testing.T) {
	var before, after map[string]any
	for file, target := range map[string]*map[string]any{"cli-soft-delete-35.json": &before, "cli-soft-delete-39.json": &after} {
		raw, err := os.ReadFile("fixtures/elastic-san/" + file)
		if err != nil || json.Unmarshal(raw, target) != nil {
			t.Fatal(err)
		}
	}
	live := object(array(before["value"])[0])
	retained := object(array(after["value"])[0])
	c := &client{}
	if text(live["id"]) == text(retained["id"]) || object(live["properties"])["volumeId"] != object(retained["properties"])["volumeId"] || c.privateConfiguration(elasticSanVolumeSnapshot(live)) != c.privateConfiguration(elasticSanVolumeSnapshot(retained)) {
		t.Fatal("native soft deletion lineage changed")
	}
}

func TestElasticSanVolumeSQLitePlanAndRestart(t *testing.T) {
	f := newElasticSanVolumeFixture(t)
	ctx := t.Context()
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	values := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, []string{elasticSanSnapshotType, elasticSanVolumeType, elasticSanGroupType, elasticSanType}, false, true)
	var selected asset.Asset
	for _, value := range values {
		if value.Identity.NativeID == f.ids[elasticSanVolumeType] {
			selected = value
		}
	}
	service := cleanup.NewService(repo, registry)
	task, err := service.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: selected.ID}}, CreatedBy: "operator", RequestOptions: map[asset.AssetID]map[string]any{selected.ID: {"force_delete": true}}})
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 2 {
		t.Fatal("volume/snapshot plan", task.Task.Status, task.Task.Blockers, len(task.Steps), err)
	}
	warned, forceWarned := false, false
	for _, warning := range task.Task.Warnings {
		if warning.Code == plan.WarningElasticSanVolumeForceDelete && warning.AssetID == selected.ID {
			forceWarned = true
		}
		if warning.Code == plan.WarningElasticSanVolumeSoftDelete && warning.AssetID == selected.ID {
			warned = true
		}
	}
	if !warned || !forceWarned {
		t.Fatal("retention effect not reviewed")
	}
	attempt, err := service.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "volume-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := repo.ListJobsByAggregate(ctx, "cleanup_task", string(task.Task.ID))
	if err != nil {
		t.Fatal(err)
	}
	retainedID := ""
	completed := false
	for round := 0; round < 10; round++ {
		repo, err = sqlite.Open(path, "../../migrations")
		if err != nil {
			t.Fatal(err)
		}
		fresh, err := NewRuntime(f.runtime.credentials)
		if err != nil {
			t.Fatal(err)
		}
		fresh.transport = f.runtime.transport
		registered := providerruntime.NewRegistry()
		if err = registered.Register(fresh); err != nil {
			t.Fatal(err)
		}
		if err = registered.RegisterBundle(fresh.Bundle()); err != nil {
			t.Fatal(err)
		}
		resolver := cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
			return registered.ResolveAction(ctx, value.Identity.ConnectionID, value)
		})
		for _, job := range jobs {
			if job.Payload["cleanup_task_step_id"] == nil {
				continue
			}
			bytes, _ := json.Marshal(job)
			var restored execution.Job
			if json.Unmarshal(bytes, &restored) != nil {
				t.Fatal("job restore")
			}
			err = cleanup.NewExecutionHandler(cleanup.NewService(repo, registered), resolver).Handle(ctx, restored)
			var retry *cleanup.RetryError
			if err != nil && !errors.As(err, &retry) {
				t.Fatal("volume worker", err)
			}
			if f.snapshotDeletes > 0 {
				delete(f.values, f.ids[elasticSanSnapshotType])
			}
			if f.volumeDeletes > 0 && retainedID == "" {
				retainedID = f.softDelete(f.ids[elasticSanVolumeType])
			}
		}
		stored, err := repo.GetAsset(ctx, selected.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.ClosedAt != nil {
			completed = true
			break
		}
	}
	if !completed || f.volumeDeletes != 1 || !f.force || f.snapshotDeletes != 1 || retainedID == "" || f.values[retainedID] == nil {
		t.Fatal("volume restart/outcome", completed, f.volumeDeletes, f.snapshotDeletes)
	}
	for _, step := range task.Steps {
		action, err := repo.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(step.ID))
		if step.AssetID == selected.ID && (object(action.ProviderResult["outcome"])["retained_native_id"] != retainedID || object(action.ProviderResult["outcome"])["outcome"] != "soft_deleted") {
			t.Fatal("persisted soft-delete outcome lost", action.ProviderResult)
		}
		if err != nil || action.Status != execution.ActionSucceeded || action.DeletionCheckStartedAt == nil {
			t.Fatal("persisted step", action.Status, err)
		}
	}
	for _, value := range values {
		if value.Identity.NativeType == elasticSanType || value.Identity.NativeType == elasticSanGroupType {
			current, err := repo.GetAsset(ctx, value.ID)
			if err != nil || current.ClosedAt != nil {
				t.Fatal("closed parent", err)
			}
		}
	}
	retained := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, []string{elasticSanVolumeType}, false, true)
	found := false
	for _, value := range retained {
		if value.Identity.NativeID == retainedID && value.Normalized["retained"] == true && value.Normalized["cleanup_deletion_mode"] == "permanent" {
			found = true
		}
	}
	if !found {
		t.Fatal("retained copy not independently discoverable")
	}
}

func TestElasticSanVolumeInventorySharesSnapshotReadsAndRejectsForgedHistory(t *testing.T) {
	f := newElasticSanVolumeFixture(t)
	reads := 0
	original := f.hook
	f.hook = func(r *http.Request) (*http.Response, bool) {
		if r.Method == "GET" && strings.HasSuffix(strings.ToLower(r.URL.Path), "/snapshots") {
			reads++
		}
		return original(r)
	}
	batch, err := f.runtime.List(t.Context(), f.request(elasticSanVolumeType))
	if err != nil || reads != 4 {
		t.Fatal("snapshot indexes repeated per volume", reads, err)
	}
	request := f.request(elasticSanVolumeType)
	request.KnownNativeMetadata = map[string]map[string]any{}
	for _, item := range batch.Items {
		request.KnownNativeIDs = append(request.KnownNativeIDs, item.NativeID)
		request.KnownNativeMetadata[item.NativeID] = item.Normalized
	}
	id := f.ids[elasticSanVolumeType]
	meta := maps.Clone(request.KnownNativeMetadata[id])
	state := maps.Clone(object(meta[elasticSanSnapshotCleanup]))
	volume := maps.Clone(object(state["volume"]))
	volume["policy"] = map[string]any{"policyState": "Disabled"}
	state["volume"] = volume
	meta[elasticSanSnapshotCleanup] = state
	request.KnownNativeMetadata[id] = meta
	reads = 0
	if _, err := f.runtime.List(t.Context(), request); err == nil || reads != 0 {
		t.Fatal("forged volume history reached native snapshot calls", reads, err)
	}
}

func TestElasticSanVolumeAbsentPolicyUsesNativeSemantics(t *testing.T) {
	f := newElasticSanVolumeFixture(t)
	delete(object(f.values[f.ids[elasticSanGroupType]]["properties"]), "deleteRetentionPolicy")
	a, req := f.volumeAction(t, f.ids[elasticSanVolumeType])
	if req.Asset.Normalized["cleanup_deletion_mode"] != "native" {
		t.Fatal("invented retention default")
	}
	delete(f.values, f.ids[elasticSanSnapshotType])
	result, err := a.Execute(t.Context(), req)
	if err != nil || f.permanent {
		t.Fatal("native policy delete", err)
	}
	retainedID := f.softDelete(f.ids[elasticSanVolumeType])
	wait, err := a.Wait(t.Context(), req, result)
	if err != nil || !wait.Done || object(wait.Data["outcome"])["retained_native_id"] != retainedID {
		t.Fatal("observed retention lost", err)
	}
	forged := result
	forged.Data = maps.Clone(wait.Data)
	forged.Data["outcome"] = map[string]any{"outcome": "absent"}
	polls := f.polls
	if _, err := a.Wait(t.Context(), req, forged); err == nil || f.polls != polls {
		t.Fatal("forged persisted outcome accepted")
	}
}

func TestElasticSanVolumeGraphRecoversSnapshotMissingFromOlderVolumeRecord(t *testing.T) {
	f := newElasticSanVolumeFixture(t)
	snapshots, err := f.runtime.List(t.Context(), f.request(elasticSanSnapshotType))
	if err != nil {
		t.Fatal(err)
	}
	child := elasticSanTestAsset(snapshots.Items[0])
	f.omitted[child.Identity.NativeID] = true
	volumes, err := f.runtime.List(t.Context(), f.request(elasticSanVolumeType))
	if err != nil {
		t.Fatal(err)
	}
	var volume asset.Asset
	for _, item := range volumes.Items {
		if item.NativeID == f.ids[elasticSanVolumeType] {
			volume = elasticSanTestAsset(item)
		}
	}
	contribution := governance.Contribution{}
	cascades := serviceCascades{client: f.client, connectionID: "connection"}
	if err := cascades.contributeElasticSanVolumes(t.Context(), []asset.Asset{volume, child}, &contribution); err == nil {
		t.Fatal("older volume record hid a known live snapshot")
	}
}
