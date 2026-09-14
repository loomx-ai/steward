package azure

import (
	"context"
	"encoding/json"
	"errors"
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

func elasticSanRootFixture(t *testing.T) *elasticSanCleanupFixture {
	f := newElasticSanCleanupFixture(t)
	f.kind = elasticSanType
	for id, retained := range f.retained {
		if retained {
			delete(f.values, id)
			delete(f.retained, id)
		}
	}
	f.values[f.ids[elasticSanType]]["systemData"] = map[string]any{"createdAt": "2026-02-11T09:51:01.7803283Z"}
	object(f.values[f.ids[elasticSanGroupType]]["properties"])["deleteRetentionPolicy"] = map[string]any{"policyState": "Disabled"}
	return f
}

func elasticSanRootActionFixture(t *testing.T) (*elasticSanCleanupFixture, contracts.ActionDriver, contracts.ActionRequest) {
	f := elasticSanRootFixture(t)
	driver, request := f.action(t)
	for _, kind := range []string{elasticSanGroupType, elasticSanEndpointType} {
		batch, err := f.runtime.List(t.Context(), f.request(kind))
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range batch.Items {
			request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, contracts.ActionImpact{Asset: elasticSanTestAsset(item), ControllerID: request.Asset.ID, Delete: true})
		}
	}
	return f, driver, request
}

func elasticSanRootFinishChildren(f *elasticSanCleanupFixture) {
	for id := range f.values {
		if id != f.ids[elasticSanType] {
			delete(f.values, id)
		}
	}
}

func TestElasticSanRootDeleteRequiresChildrenAndOwnAbsence(t *testing.T) {
	f, a, request := elasticSanRootActionFixture(t)
	if _, err := a.Execute(t.Context(), request); err == nil || f.deletes != 0 {
		t.Fatal("SAN deleted live children", err)
	}
	elasticSanRootFinishChildren(f)
	// Native readOnly counters and the etag can change after child deletion.
	props := object(f.values[f.ids[elasticSanType]]["properties"])
	props["volumeGroupCount"], props["totalVolumeSizeGiB"] = 0, 0
	f.values[f.ids[elasticSanType]]["etag"] = "after-children"
	result, err := a.Execute(t.Context(), request)
	if err != nil || f.deletes != 1 {
		t.Fatal("SAN DELETE", err, f.deletes)
	}
	wait, err := a.Wait(t.Context(), request, result)
	if err != nil || wait.Done {
		t.Fatal("live SAN closed", wait, err)
	}
	result.Data = wait.Data
	request.ExecutionResult = &result
	if _, err := a.Execute(t.Context(), request); err != nil || f.deletes != 1 {
		t.Fatal("repeated native SAN DELETE", err)
	}
	delete(f.values, f.ids[elasticSanType])
	wait, err = a.Wait(t.Context(), request, result)
	if err != nil || !wait.Done {
		t.Fatal("SAN own absence", wait, err)
	}
}

func TestElasticSanRootPreflightAndReviewGuards(t *testing.T) {
	for _, mode := range []string{"options", "missing-review", "foreign-review", "changed-member", "root-created", "root-config", "root-protected", "managed", "lock", "denied", "new-child", "retained-child"} {
		t.Run(mode, func(t *testing.T) {
			f, a, request := elasticSanRootActionFixture(t)
			saved := batchClone(f.values[f.ids[elasticSanVolumeType]])
			elasticSanRootFinishChildren(f)
			raw := f.values[f.ids[elasticSanType]]
			switch mode {
			case "options":
				request.Parameters = map[string]any{"force_delete": true}
			case "missing-review":
				request.PrerequisiteDeletions = nil
			case "foreign-review":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "other"
			case "changed-member":
				request.PrerequisiteDeletions[0].Asset.Normalized[elasticSanSnapshotCleanupProof] = "changed"
			case "root-created":
				object(raw["systemData"])["createdAt"] = "2026-02-12T09:51:01Z"
			case "root-config":
				object(raw["properties"])["baseSizeTiB"] = 27
			case "root-protected":
				raw["tags"] = map[string]any{"steward:protected": "true"}
			case "managed":
				raw["managedBy"] = resourceID(vmType, "controller")
			case "lock":
				f.locks = []any{map[string]any{"id": f.ids[elasticSanType] + "/providers/Microsoft.Authorization/locks/protect", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "denied":
				f.hook = func(r *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(strings.ToLower(r.URL.Path), "/volumegroups") {
						return jsonResponse(403, map[string]any{}, nil), true
					}
					return nil, false
				}
			case "new-child", "retained-child":
				id := f.ids[elasticSanVolumeType] + "-unexpected"
				saved["id"], saved["name"] = id, last(id)
				f.values[id] = saved
				if mode == "retained-child" {
					f.retained[id] = true
					object(saved["properties"])["provisioningState"] = "Deleted"
				}
			}
			if _, err := a.Execute(t.Context(), request); err == nil || f.deletes != 0 {
				t.Fatal("unverified SAN deletion", mode, err)
			}
		})
	}
}

func TestElasticSanRootTerminalReadsCannotHideKnownChildren(t *testing.T) {
	for _, mode := range []string{"missing-collections", "known-snapshot", "known-volume", "known-group", "denied-index", "denied-own", "expired-callback", "recreated-root"} {
		t.Run(mode, func(t *testing.T) {
			f, a, request := elasticSanRootActionFixture(t)
			originals := map[string]map[string]any{}
			for id, raw := range f.values {
				originals[id] = batchClone(raw)
			}
			elasticSanRootFinishChildren(f)
			result, err := a.Execute(t.Context(), request)
			if err != nil {
				t.Fatal(err)
			}
			delete(f.values, f.ids[elasticSanType])
			switch mode {
			case "known-snapshot":
				f.values[f.ids[elasticSanSnapshotType]] = originals[f.ids[elasticSanSnapshotType]]
			case "known-volume":
				f.values[f.ids[elasticSanVolumeType]] = originals[f.ids[elasticSanVolumeType]]
			case "known-group":
				f.values[f.ids[elasticSanGroupType]] = originals[f.ids[elasticSanGroupType]]
			case "expired-callback":
				f.pollStatus = 404
			case "recreated-root":
				raw := originals[f.ids[elasticSanType]]
				object(raw["systemData"])["createdAt"] = "2026-02-12T09:51:01Z"
				f.values[f.ids[elasticSanType]] = raw
			}
			f.hook = func(r *http.Request) (*http.Response, bool) {
				path := strings.ToLower(r.URL.Path)
				if mode == "denied-own" && path == f.ids[elasticSanSnapshotType] {
					return jsonResponse(403, map[string]any{}, nil), true
				}
				if strings.HasSuffix(path, "/volumegroups") || strings.HasSuffix(path, "/snapshots") || strings.HasSuffix(path, "/volumes") || strings.HasSuffix(path, "/privateendpointconnections") {
					status := 404
					if mode == "denied-index" {
						status = 403
					}
					return jsonResponse(status, map[string]any{}, nil), true
				}
				return nil, false
			}
			wait, err := a.Wait(t.Context(), request, result)
			success := mode == "missing-collections" || mode == "expired-callback"
			if success && (err != nil || !wait.Done) || !success && (err == nil || wait.Done) {
				t.Fatal("SAN terminal proof", mode, wait.Done, err)
			}
		})
	}
}

func TestElasticSanRootRetentionIsNotImplicitPurge(t *testing.T) {
	for _, mode := range []string{"enabled-group", "retained-volume", "retained-group"} {
		t.Run(mode, func(t *testing.T) {
			f := elasticSanRootFixture(t)
			if mode == "enabled-group" {
				object(f.values[f.ids[elasticSanGroupType]]["properties"])["deleteRetentionPolicy"] = map[string]any{"policyState": "Enabled", "retentionPeriodDays": 7}
			} else {
				kind := elasticSanVolumeType
				if mode == "retained-group" {
					kind = elasticSanGroupType
				}
				id := f.ids[kind]
				f.retained[id] = true
				object(f.values[id]["properties"])["provisioningState"] = "Deleted"
			}
			batch, err := f.runtime.List(t.Context(), f.request(elasticSanType))
			if err != nil {
				t.Fatal(err)
			}
			item := elasticSanRootItem(t, f.elasticSanFixture, batch)
			if item.Actionable == nil || *item.Actionable || item.Normalized["cleanup_protected"] != true {
				t.Fatal("retention silently authorized parent purge")
			}
			if _, err := f.runtime.ResolveAction(t.Context(), "connection", elasticSanTestAsset(item)); err == nil {
				t.Fatal("protected SAN resolved driver")
			}

			if mode == "enabled-group" {
				repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
				values := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType}, false, true)
				request := cleanup.CreateTaskRequest{ConnectionID: "connection", CreatedBy: "operator"}
				for _, value := range values {
					if value.Identity.NativeType == elasticSanType || value.Identity.NativeType == elasticSanEndpointType {
						request.Selectors = append(request.Selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: value.ID})
					}
				}
				service := cleanup.NewService(repo, registry)
				task, err := service.CreateTask(t.Context(), request)
				if err != nil || task.Task.Status == plan.StatusReady {
					t.Fatal("protected parent released child deletion steps", task.Task, err)
				}
				if _, err := service.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "blocked-retained-san", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}}); err == nil {
					t.Fatal("protected SAN plan executed")
				}
			}
		})
	}
}

func TestElasticSanRootSQLitePlanAndRestart(t *testing.T) {
	f := elasticSanRootFixture(t)
	counts := map[string]int{}
	f.hook = func(r *http.Request) (*http.Response, bool) {
		if r.Method != "DELETE" {
			return nil, false
		}
		id := strings.ToLower(r.URL.Path)
		_, kind, _ := parseID(id)
		if f.values[id] == nil || len(r.URL.Query()) != 1 || r.URL.Query().Get("api-version") != elasticSanVersion || r.ContentLength != 0 {
			t.Fatal("unexpected native mutation", id)
		}
		if strings.EqualFold(kind, elasticSanVolumeType) {
			if r.Header.Get("x-ms-force-delete") != "false" || r.Header.Get("x-ms-delete-snapshots") != "false" {
				t.Fatal("unreviewed volume force flags")
			}
		} else if r.Header.Get("x-ms-force-delete") != "" || r.Header.Get("x-ms-delete-snapshots") != "" {
			t.Fatal("parent inherited volume flags")
		}
		counts[id]++
		if id == f.ids[elasticSanType] && len(f.values) != 1 {
			t.Fatal("SAN deleted before its children")
		}
		object(f.values[id]["properties"])["provisioningState"] = "Deleting"
		return jsonResponse(202, f.values[id], http.Header{"Location": {elasticSanTestPollURL()}}), true
	}
	ctx := t.Context()
	repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType}
	values := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
	byID := map[string]asset.Asset{}
	for _, v := range values {
		byID[v.Identity.NativeID] = v
	}
	root := byID[f.ids[elasticSanType]]
	service := cleanup.NewService(repo, registry)
	req := cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: root.ID}}, CreatedBy: "operator"}
	blocked, err := service.CreateTask(ctx, req)
	if err != nil || blocked.Task.Status == plan.StatusReady {
		t.Fatal("PEC silently selected", err)
	}
	req.Selectors = append(req.Selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: byID[f.ids[elasticSanEndpointType]].ID})
	task, err := service.CreateTask(ctx, req)
	if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 5 {
		t.Fatal("SAN prerequisite plan", task.Task.Status, task.Task.Blockers, len(task.Steps), err)
	}
	attempt, err := service.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "root-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
	if err != nil {
		t.Fatal(err)
	}
	jobs, err := repo.ListJobsByAggregate(ctx, "cleanup_task", string(task.Task.ID))
	if err != nil {
		t.Fatal(err)
	}
	completed := false
	for round := 0; round < 24; round++ {
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
				t.Fatal("job roundtrip")
			}
			err = cleanup.NewExecutionHandler(cleanup.NewService(repo, registered), resolver).Handle(ctx, restored)
			var retry *cleanup.RetryError
			if err != nil && !errors.As(err, &retry) {
				t.Fatal("SAN worker", err)
			}
			for id, count := range counts {
				if count > 0 {
					delete(f.values, id)
				}
			}
		}
		stored, err := repo.GetAsset(ctx, root.ID)
		if err != nil {
			t.Fatal(err)
		}
		if stored.ClosedAt != nil {
			completed = true
			break
		}
	}
	if !completed || len(counts) != 5 {
		t.Fatal("SAN restart did not complete", counts)
	}
	for _, step := range task.Steps {
		action, err := repo.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(step.ID))
		if err != nil || action.Status != execution.ActionSucceeded || action.DeletionCheckStartedAt == nil {
			t.Fatal("SAN persisted step", action.Status, err)
		}
		value, err := repo.GetAsset(ctx, step.AssetID)
		if err != nil || value.ClosedAt == nil || counts[value.Identity.NativeID] != 1 {
			t.Fatal("native mutation repeated or asset not closed", value.ID, err)
		}
	}
	if remaining := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true); len(remaining) != 0 {
		t.Fatal("completed SAN family reappeared", len(remaining))
	}

}

func TestElasticSanRootStaleBoundaryBlocksPlanUntilRefresh(t *testing.T) {
	f := elasticSanRootFixture(t)
	repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	kinds := []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType}
	values := azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
	request := cleanup.CreateTaskRequest{ConnectionID: "connection", CreatedBy: "operator"}
	var root asset.AssetID
	for _, value := range values {
		if value.Identity.NativeType == elasticSanType || value.Identity.NativeType == elasticSanEndpointType {
			request.Selectors = append(request.Selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: value.ID})
		}
		if value.Identity.NativeType == elasticSanType {
			root = value.ID
		}
	}
	object(f.values[f.ids[elasticSanVolumeType]]["properties"])["sizeGiB"] = 16
	azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, []string{elasticSanVolumeType}, false, true)
	task, err := cleanup.NewService(repo, registry).CreateTask(t.Context(), request)
	blocked := false
	for _, blocker := range task.Task.Blockers {
		blocked = blocked || blocker.Code == plan.BlockUnresolvedCleanup && blocker.ControllerID == root
	}
	if err != nil || task.Task.Status == plan.StatusReady || !blocked {
		t.Fatal("stale SAN boundary was executable", task.Task.Blockers, err)
	}
	azureNativeWorkerScan(t, f.runtime, elasticSanSource, repo, registry, kinds, false, true)
	task, err = cleanup.NewService(repo, registry).CreateTask(t.Context(), request)
	if err != nil || task.Task.Status != plan.StatusReady {
		t.Fatal("fresh SAN boundary did not restore review", task.Task.Blockers, err)
	}
}
