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
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func elasticSanGroupActionFixture(t *testing.T) (*elasticSanCleanupFixture, contracts.ActionDriver, contracts.ActionRequest) {
	t.Helper()
	f := newElasticSanCleanupFixture(t)
	f.kind = elasticSanGroupType
	batch, err := f.runtime.List(t.Context(), f.request(elasticSanGroupType))
	if err != nil {
		t.Fatal(err)
	}
	var selected asset.Asset
	for _, item := range batch.Items {
		if item.NativeID == f.ids[elasticSanGroupType] {
			selected = elasticSanTestAsset(item)
		}
	}
	a, err := f.runtime.ResolveAction(t.Context(), "connection", selected)
	if err != nil {
		t.Fatal(err)
	}
	req := contracts.ActionRequest{Asset: selected, Action: "delete", IdempotencyKey: "group-delete"}
	state := object(selected.Normalized[elasticSanGroupContext])
	for _, kind := range []string{elasticSanVolumeType, elasticSanEndpointType} {
		batch, err := f.runtime.List(t.Context(), f.request(kind))
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range batch.Items {
			if object(state["members"])[item.NativeID] == nil && object(state["connections"])[item.NativeID] == nil {
				continue
			}
			impact := contracts.ActionImpact{Asset: elasticSanTestAsset(item), ControllerID: selected.ID, Delete: item.Normalized["retained"] != true}
			if impact.Delete {
				req.PrerequisiteDeletions = append(req.PrerequisiteDeletions, impact)
			} else {
				req.LifecycleImpacts = append(req.LifecycleImpacts, impact)
			}
		}
	}
	return f, a, req
}

func elasticSanGroupFinishPrerequisites(f *elasticSanCleanupFixture) {
	delete(f.values, f.ids[elasticSanSnapshotType])
	delete(f.values, f.ids[elasticSanEndpointType])
	id := f.ids[elasticSanVolumeType]
	raw := maps.Clone(f.values[id])
	raw["properties"] = maps.Clone(object(raw["properties"]))
	newID := id + "-1770765061"
	raw["id"], raw["name"] = newID, last(newID)
	object(raw["properties"])["provisioningState"] = "Deleted"
	delete(f.values, id)
	f.values[newID], f.retained[newID] = raw, true
}

func TestElasticSanGroupDeleteSoftOutcome(t *testing.T) {
	f, a, req := elasticSanGroupActionFixture(t)
	if _, err := a.Execute(t.Context(), req); err == nil || f.deletes != 0 {
		t.Fatal("group deleted live prerequisites")
	}
	elasticSanGroupFinishPrerequisites(f)
	result, err := a.Execute(t.Context(), req)
	if err != nil || f.deletes != 1 {
		t.Fatal("group DELETE", err, f.deletes)
	}
	wait, err := a.Wait(t.Context(), req, result)
	if err != nil || wait.Done {
		t.Fatal("live group closed", err)
	}
	id := f.ids[elasticSanGroupType]
	object(f.values[id]["properties"])["provisioningState"] = "Deleted"
	f.retained[id] = true
	result.Data = wait.Data
	wait, err = a.Wait(t.Context(), req, result)
	if err != nil || !wait.Done || object(wait.Data["outcome"])["outcome"] != "soft_deleted" || object(wait.Data["outcome"])["retained_native_id"] != id {
		t.Fatal("same-ID retained group outcome", wait, err)
	}
	result.Data = wait.Data
	req.ExecutionResult = &result
	if _, err = a.Execute(t.Context(), req); err != nil || f.deletes != 1 {
		t.Fatal("replayed group DELETE", err)
	}
	// Retained own GET is allowed, but must agree with the selected retained index.
	f.hook = func(r *http.Request) (*http.Response, bool) {
		if r.Method == "GET" && strings.EqualFold(r.URL.Path, id) {
			return jsonResponse(200, f.values[id], nil), true
		}
		return nil, false
	}
	if out, err := a.Readback(t.Context(), req); err != nil || out.Exists {
		t.Fatal("addressable retained own GET", out, err)
	}
	object(f.values[id]["systemData"])["createdAt"] = "2026-02-12T09:51:01Z"
	if _, err := a.Readback(t.Context(), req); err == nil {
		t.Fatal("same-name group replacement closed")
	}
}

func TestElasticSanGroupReviewAndBoundaryFailures(t *testing.T) {
	for _, mode := range []string{"unreviewed", "new-volume", "new-retained", "new-snapshot", "new-connection", "denied-index", "missing-index", "policy-change", "retained-missing", "forged-context", "retained-protected", "retained-locked"} {
		t.Run(mode, func(t *testing.T) {
			f, a, req := elasticSanGroupActionFixture(t)
			elasticSanGroupFinishPrerequisites(f)
			group := f.ids[elasticSanGroupType]
			switch mode {
			case "unreviewed":
				req.PrerequisiteDeletions = nil
			case "new-volume", "new-retained":
				raw := maps.Clone(f.values[f.ids[elasticSanVolumeType]+"-1770765061"])
				raw["properties"] = maps.Clone(object(raw["properties"]))
				id := group + "/volumes/new-volume"
				raw["id"], raw["name"] = id, last(id)
				object(raw["properties"])["volumeId"] = azureRequestID(id)
				object(raw["properties"])["provisioningState"] = "Succeeded"
				f.values[id] = raw
				if mode == "new-retained" {
					object(raw["properties"])["provisioningState"] = "Deleted"
					f.retained[id] = true
				}
			case "new-snapshot":
				raw := elasticSanTestRecord(elasticSanSnapshotType)
				f.values[text(raw["id"])] = raw
			case "new-connection":
				raw := elasticSanTestRecord(elasticSanEndpointType)
				f.values[text(raw["id"])] = raw
			case "denied-index", "missing-index":
				f.hook = func(r *http.Request) (*http.Response, bool) {
					if strings.EqualFold(r.URL.Path, group+"/volumes") {
						status := 403
						if mode == "missing-index" {
							status = 404
						}
						return jsonResponse(status, map[string]any{}, nil), true
					}
					return nil, false
				}
			case "policy-change":
				object(f.values[group]["properties"])["deleteRetentionPolicy"] = map[string]any{"policyState": "Disabled"}
			case "retained-missing":
				delete(f.values, f.ids[elasticSanVolumeType]+"-1751081600")
			case "retained-protected":
				f.values[f.ids[elasticSanVolumeType]+"-1751081600"]["tags"] = map[string]any{"steward:protected": "true"}
			case "retained-locked":
				f.locks = []any{map[string]any{"id": f.ids[elasticSanVolumeType] + "-1751081600/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "forged-context":
				req.Asset.Normalized = maps.Clone(req.Asset.Normalized)
				req.Asset.Normalized[elasticSanGroupContext] = map[string]any{}
			}
			if _, err := a.Execute(t.Context(), req); err == nil || f.deletes != 0 {
				t.Fatal("unverified group boundary mutated", mode, err)
			}
		})
	}
}

func TestElasticSanGroupSQLitePlanAndRestart(t *testing.T) {
	f := newElasticSanVolumeFixture(t)
	groupDeletes, connectionDeletes := 0, 0
	original := f.hook
	f.hook = func(r *http.Request) (*http.Response, bool) {
		id := strings.ToLower(r.URL.Path)
		if r.Method == "DELETE" && (id == f.ids[elasticSanGroupType] || id == f.ids[elasticSanEndpointType]) {
			if len(r.URL.Query()) != 1 || r.Header.Get("x-ms-force-delete") != "" || r.Header.Get("x-ms-delete-snapshots") != "" {
				t.Fatal("group/PEC inherited volume flags")
			}
			if id == f.ids[elasticSanGroupType] {
				groupDeletes++
			} else {
				connectionDeletes++
			}
			object(f.values[id]["properties"])["provisioningState"] = "Deleting"
			return jsonResponse(202, f.values[id], http.Header{"Location": {elasticSanTestPollURL()}}), true
		}
		return original(r)
	}
	ctx := t.Context()
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType}
	values := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
	var selected, connection asset.Asset
	for _, value := range values {
		if value.Identity.NativeID == f.ids[elasticSanGroupType] {
			selected = value
		}
		if value.Identity.NativeID == f.ids[elasticSanEndpointType] {
			connection = value
		}
	}
	service := cleanup.NewService(repo, registry)
	request := cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: selected.ID}}, CreatedBy: "operator"}
	blocked, err := service.CreateTask(ctx, request)
	if err != nil || blocked.Task.Status == plan.StatusReady {
		t.Fatal("incoming connection was silently selected", blocked.Task.Status, err)
	}
	request.Selectors = append(request.Selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: connection.ID})
	task, err := service.CreateTask(ctx, request)
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 4 {
		t.Fatal("group prerequisite plan", task.Task.Status, task.Task.Blockers, len(task.Steps), err)
	}
	warned := false
	for _, warning := range task.Task.Warnings {
		warned = warned || warning.Code == plan.WarningElasticSanGroupDelete && warning.AssetID == selected.ID
	}
	if !warned {
		t.Fatal("group retention consequence missing from review")
	}
	if len(task.ImpactItems) != 1 || task.ImpactItems[0].Expected != plan.ExpectedProviderDefaultRetain {
		t.Fatal("retained volume not reviewed", task.ImpactItems)
	}
	attempt, err := service.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "group-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := repo.ListJobsByAggregate(ctx, "cleanup_task", string(task.Task.ID))
	if err != nil {
		t.Fatal(err)
	}
	retainedID := ""
	completed := false
	for round := 0; round < 18; round++ {
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
			raw, _ := json.Marshal(job)
			var restored execution.Job
			if json.Unmarshal(raw, &restored) != nil {
				t.Fatal("job restore")
			}
			err = cleanup.NewExecutionHandler(cleanup.NewService(repo, registered), resolver).Handle(ctx, restored)
			var retry *cleanup.RetryError
			if err != nil && !errors.As(err, &retry) {
				t.Fatal("group worker", err)
			}
			if f.snapshotDeletes > 0 {
				delete(f.values, f.ids[elasticSanSnapshotType])
			}
			if connectionDeletes > 0 {
				delete(f.values, f.ids[elasticSanEndpointType])
			}
			if f.volumeDeletes > 0 && retainedID == "" {
				retainedID = f.softDelete(f.ids[elasticSanVolumeType])
			}
			if groupDeletes > 0 {
				id := f.ids[elasticSanGroupType]
				object(f.values[id]["properties"])["provisioningState"] = "Deleted"
				f.retained[id] = true
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
	if !completed || groupDeletes != 1 || connectionDeletes != 1 || f.volumeDeletes != 1 || f.snapshotDeletes != 1 || f.permanent || f.force {
		t.Fatal("group restart completion", completed, groupDeletes, connectionDeletes, f.volumeDeletes, f.snapshotDeletes)
	}
	for _, step := range task.Steps {
		action, err := repo.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(step.ID))
		if err != nil || action.Status != execution.ActionSucceeded || action.DeletionCheckStartedAt == nil {
			t.Fatal("persisted group step", action.Status, err)
		}
		if step.AssetID == selected.ID && object(action.ProviderResult["outcome"])["retained_native_id"] != selected.Identity.NativeID {
			t.Fatal("same-ID group outcome lost")
		}
	}
	retained, err := repo.GetAsset(ctx, task.ImpactItems[0].AssetID)
	if err != nil || retained.ClosedAt != nil {
		t.Fatal("closed retained member", err)
	}
	rediscovered := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
	found := false
	for _, value := range rediscovered {
		if value.ID == selected.ID {
			found = value.ClosedAt == nil && value.Normalized["retained"] == true
		}
	}
	if !found {
		t.Fatal("same-ID retained group was not reopened")
	}
}

func TestElasticSanGroupTerminalIndexesAndKnownSnapshots(t *testing.T) {
	for _, mode := range []string{"snapshot-404", "snapshot-403", "known-snapshot-live", "volume-404", "group-index-403", "callback-expired-live", "callback-expired-deleted"} {
		t.Run(mode, func(t *testing.T) {
			f, a, req := elasticSanGroupActionFixture(t)
			snapshot := maps.Clone(f.values[f.ids[elasticSanSnapshotType]])
			elasticSanGroupFinishPrerequisites(f)
			result, err := a.Execute(t.Context(), req)
			if err != nil {
				t.Fatal(err)
			}
			id := f.ids[elasticSanGroupType]
			if mode != "callback-expired-live" {
				object(f.values[id]["properties"])["provisioningState"] = "Deleted"
				f.retained[id] = true
			}
			if mode == "known-snapshot-live" {
				f.values[f.ids[elasticSanSnapshotType]] = snapshot
			}
			if strings.HasPrefix(mode, "callback-expired") {
				f.pollStatus = 404
			}
			f.hook = func(r *http.Request) (*http.Response, bool) {
				path := strings.ToLower(r.URL.Path)
				if path == id+"/snapshots" && (strings.HasPrefix(mode, "snapshot-") || mode == "known-snapshot-live") {
					status := 404
					if mode == "snapshot-403" {
						status = 403
					}
					return jsonResponse(status, map[string]any{}, nil), true
				}
				if mode == "volume-404" && path == id+"/volumes" {
					return jsonResponse(404, map[string]any{}, nil), true
				}
				if mode == "group-index-403" && path == elasticSanRoot(id)+"/volumegroups" {
					return jsonResponse(403, map[string]any{}, nil), true
				}
				return nil, false
			}
			wait, err := a.Wait(t.Context(), req, result)
			success := mode == "snapshot-404" || mode == "callback-expired-deleted"
			if success && (err != nil || !wait.Done) || !success && (err == nil || wait.Done) {
				t.Fatal("unverified group completion", mode, wait.Done, err)
			}
		})
	}
}

func TestElasticSanGroupDisabledRetentionAndNoImplicitPurge(t *testing.T) {
	f := newElasticSanCleanupFixture(t)
	f.kind = elasticSanGroupType
	group := f.ids[elasticSanGroupType]
	object(f.values[group]["properties"])["deleteRetentionPolicy"] = map[string]any{"policyState": "Disabled"}
	batch, err := f.runtime.List(t.Context(), f.request(elasticSanGroupType))
	if err != nil {
		t.Fatal(err)
	}
	item := elasticSanGroupItem(t, f.elasticSanFixture, batch)
	if _, err := f.runtime.ResolveAction(t.Context(), "connection", elasticSanTestAsset(item)); err == nil {
		t.Fatal("disabled policy implicitly purged retained members")
	}
	for id := range f.values {
		if strings.HasPrefix(id, group+"/") || id == f.ids[elasticSanEndpointType] {
			delete(f.values, id)
		}
	}
	batch, err = f.runtime.List(t.Context(), f.request(elasticSanGroupType))
	if err != nil {
		t.Fatal(err)
	}
	value := elasticSanTestAsset(elasticSanGroupItem(t, f.elasticSanFixture, batch))
	a, err := f.runtime.ResolveAction(t.Context(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	req := contracts.ActionRequest{Asset: value, Action: "delete"}
	result, err := a.Execute(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	delete(f.values, group)
	wait, err := a.Wait(t.Context(), req, result)
	if err != nil || !wait.Done || object(wait.Data["outcome"])["outcome"] != "absent" {
		t.Fatal("permanent group absence", wait, err)
	}
}

func TestElasticSanGroupStaleMembershipRequiresGroupRefresh(t *testing.T) {
	f, driver, frozen := elasticSanGroupActionFixture(t)
	repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType}
	values := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
	var selected asset.AssetID
	for _, value := range values {
		if value.Identity.NativeID == f.ids[elasticSanGroupType] {
			selected = value.ID
		}
	}
	object(f.values[f.ids[elasticSanVolumeType]]["properties"])["sizeGiB"] = 16
	// Child reconciliation remains available while its group record is older.
	azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, []string{elasticSanVolumeType}, false, true)
	elasticSanGroupFinishPrerequisites(f)
	// A task's readiness is not native mutation authority. Even after its child
	// steps finish, a changed incarnation/configuration must stop the group.
	if _, err := driver.Execute(t.Context(), frozen); err == nil || !strings.Contains(err.Error(), "elastic_san_group_retained_member_changed") || f.deletes != 0 {
		t.Fatal("stale group mutated", err)
	}
	azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
	request := cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: selected}}, CreatedBy: "operator"}
	task, err := cleanup.NewService(repo, registry).CreateTask(t.Context(), request)
	if err != nil || task.Task.Status != plan.StatusReady {
		t.Fatal("refreshed group review", task.Task.Status, task.Task.Blockers, err)
	}
}
