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

// Both image families are VM creation sources, not runtime attachments. Native
// image deletion must work while the deployed VM and its original reference live.
func newLocalImageFixture(t *testing.T, kind string) *localRootFixture {
	f := newLocalRootFixture(t, kind)
	object(object(f.values[f.ids[azureLocalVMType]]["properties"])["storageProfile"])["imageReference"] = map[string]any{"id": f.id}
	return f
}

func TestAzureLocalImagesOwnReadbackRecovery(t *testing.T) {
	for _, kind := range []string{azureLocalImageType, azureLocalMarketplaceType} {
		for _, status := range []int{202, 204, 404} {
			t.Run(kind+http.StatusText(status), func(t *testing.T) {
				f := newLocalImageFixture(t, kind)
				previous := f.override
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.Contains(strings.ToLower(req.URL.Path), "microsoft.hybridcompute") || strings.Contains(strings.ToLower(req.URL.Path), "virtualmachineinstances") {
						t.Fatal("image-only inventory/cleanup read a VM or Arc registration")
					}
					return previous(req)
				}
				request := f.requestAsset(t)
				if len(stringValues(object(request.Asset.Normalized[azureLocalCleanup])["vms"])) != 0 {
					t.Fatal("image acquired VM prerequisites")
				}
				before, _ := json.Marshal(f.values[f.ids[azureLocalVMType]])
				f.status, f.retain = status, true
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				result, err := driver.Execute(t.Context(), request)
				if err != nil || f.rootDeletes != 1 {
					t.Fatal("image DELETE with surviving VM", err)
				}
				for range 3 {
					data, _ := json.Marshal(result)
					_ = json.Unmarshal(data, &result)
					fresh, _ := NewRuntime(f.runtime.credentials)
					fresh.transport = f.runtime.transport
					driver, err = fresh.ResolveAction(t.Context(), "connection", request.Asset)
					if err != nil {
						t.Fatal(err)
					}
					request.ExecutionResult = &result
					if _, err = driver.Execute(t.Context(), request); err != nil || f.rootDeletes != 1 {
						t.Fatal("image DELETE replayed", err)
					}
					wait, err := driver.Wait(t.Context(), request, result)
					if err != nil || wait.Done {
						t.Fatal("live image completed", wait, err)
					}
					result.Data = wait.Data
				}
				delete(f.values, f.id)
				wait, err := driver.Wait(t.Context(), request, result)
				if err != nil || !wait.Done {
					t.Fatal("image own absence not accepted", wait, err)
				}
				after, _ := json.Marshal(f.values[f.ids[azureLocalVMType]])
				if string(before) != string(after) || f.vmDeletes != 0 || f.deleted != 0 || len(f.arc.deleted) != 0 {
					t.Fatal("image removal affected deployed VM")
				}
			})
		}
	}
}

func TestAzureLocalImagesReviewBoundaries(t *testing.T) {
	for _, kind := range []string{azureLocalImageType, azureLocalMarketplaceType} {
		for _, mode := range []string{"configuration", "etag", "location", "tag", "group", "lock", "denied", "parameters", "impact", "prerequisite", "vm-history", "forged", "deleting", "progress"} {
			t.Run(kind+mode, func(t *testing.T) {
				f := newLocalImageFixture(t, kind)
				request := f.requestAsset(t)
				switch mode {
				case "configuration":
					object(f.values[f.id]["properties"])["imagePath"] = "private-local-replacement"
				case "etag":
					f.values[f.id]["etag"] = "replacement"
				case "location":
					f.values[f.id]["location"] = "westus"
				case "tag":
					f.values[f.id]["tags"] = map[string]any{"steward:protected": "true"}
				case "group":
					f.group["tags"] = map[string]any{"steward:protected": "true"}
				case "lock":
					f.locks = []any{map[string]any{"id": f.id + "/providers/microsoft.authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}
				case "denied":
					previous := f.override
					f.override = func(req *http.Request) (*http.Response, bool) {
						if req.Method == "GET" && strings.EqualFold(req.URL.Path, f.id) {
							return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
						}
						return previous(req)
					}
				case "parameters":
					request.Parameters = map[string]any{"force": true}
				case "impact":
					request.LifecycleImpacts = []contracts.ActionImpact{{Asset: f.asset(t, azureLocalVMType), ControllerID: request.Asset.ID, Delete: true}}
				case "prerequisite":
					request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: f.asset(t, azureLocalVMType), ControllerID: request.Asset.ID, Delete: true}}
				case "vm-history":
					state := object(request.Asset.Normalized[azureLocalCleanup])
					state["vms"] = []string{f.ids[azureLocalVMType]}
					request.Asset.Normalized[azureLocalCleanupProof] = f.client.azureLocalRootBinding(f.id, "connection", state)
				case "forged":
					request.Asset.Normalized[azureLocalCleanupProof] = "forged"
				case "deleting":
					object(f.values[f.id]["properties"])["provisioningState"] = "Deleting"
				case "progress":
					object(f.values[f.id]["properties"])["status"] = map[string]any{"progressPercentage": 75}
				}
				driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
				if err == nil {
					_, err = driver.Execute(t.Context(), request)
				}
				allowed := mode == "deleting" || mode == "progress"
				if (err == nil) != allowed || f.rootDeletes != 0 && mode != "progress" {
					t.Fatal("image boundary", mode, err, f.rootDeletes)
				}
				if mode == "progress" && f.rootDeletes != 1 {
					t.Fatal("operational image progress prevented cleanup")
				}
			})
		}
	}
}

func TestAzureLocalImagesRegisteredExecutionRecovery(t *testing.T) {
	for _, kind := range []string{azureLocalImageType, azureLocalMarketplaceType} {
		t.Run(kind, func(t *testing.T) {
			f := newLocalImageFixture(t, kind)
			f.retain = true
			repo, registry, path := azureNativeWorkerRepository(t, f.runtime)
			values := azureNativeWorkerScan(t, f.runtime, azureLocalSource, repo, registry, []string{azureLocalVMType, azureLocalAgentType, azureLocalIdentityType, azureLocalDiskType, azureLocalNICType, azureLocalNetworkType, azureLocalStorageType, azureLocalImageType, azureLocalMarketplaceType}, false, true)
			var image asset.Asset
			for _, value := range values {
				if value.Identity.NativeID == f.id {
					image = value
				}
			}
			if image.ID == "" {
				t.Fatal("image missing from registered scan")
			}
			planner := cleanup.NewService(repo, registry)
			task, err := planner.CreateTask(t.Context(), cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: image.ID}}, CreatedBy: "operator"})
			if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != 1 || len(task.ImpactItems) != 0 || task.Steps[0].AssetID != image.ID {
				t.Fatal("image-only plan", err, task)
			}
			required, err := plan.RequiredDeletions(task.Steps[0])
			if err != nil || len(required) != 0 {
				t.Fatal("image deletion scheduled a VM", required, err)
			}
			attempt, err := planner.CreateExecution(t.Context(), cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "image-worker", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
			if err != nil {
				t.Fatal(err)
			}
			jobs, err := repo.ListJobsByAggregate(t.Context(), "cleanup_task", string(task.Task.ID))
			if err != nil || len(jobs) != 1 {
				t.Fatal("image execution jobs", jobs, err)
			}
			before, _ := json.Marshal(f.values[f.ids[azureLocalVMType]])
			origin := ""
			for round := 0; round < 5; round++ {
				data, _ := json.Marshal(jobs[0])
				var restored execution.Job
				_ = json.Unmarshal(data, &restored)
				fresh, err := NewRuntime(f.runtime.credentials)
				if err != nil {
					t.Fatal(err)
				}
				fresh.transport = f.runtime.transport
				registry := providerruntime.NewRegistry()
				if err := registry.Register(fresh); err != nil {
					t.Fatal(err)
				}
				if err := registry.RegisterBundle(fresh.Bundle()); err != nil {
					t.Fatal(err)
				}
				repo, err := sqlite.Open(path, "../../migrations")
				if err != nil {
					t.Fatal(err)
				}
				resolver := cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
					return registry.ResolveAction(ctx, value.Identity.ConnectionID, value)
				})
				if round == 3 {
					delete(f.values, f.id)
				}
				err = cleanup.NewExecutionHandler(cleanup.NewService(repo, registry), resolver).Handle(t.Context(), restored)
				var retry *cleanup.RetryError
				if err != nil && !errors.As(err, &retry) {
					t.Fatal("image worker recovery", err)
				}
				current, err := repo.Executions().GetActionByExecutionStep(t.Context(), attempt.ID, string(task.Steps[0].ID))
				if err != nil || current.ProviderError != nil {
					t.Fatal("image persisted failure", err, current.ProviderError)
				}
				if round == 0 {
					origin = current.ProviderOperationID
				}
				if origin == "" || current.ProviderOperationID != origin {
					t.Fatal("image operation identity changed")
				}
				stored, err := repo.GetAsset(t.Context(), image.ID)
				if err != nil || (stored.ClosedAt != nil) != (round == 4) || (current.Status == execution.ActionSucceeded) != (round == 4) {
					t.Fatal("image own readback lost", err, current.Status)
				}
			}
			after, _ := json.Marshal(f.values[f.ids[azureLocalVMType]])
			if string(before) != string(after) || f.rootDeletes != 1 || f.rootPolls == 0 || f.vmDeletes != 0 || f.deleted != 0 || len(f.arc.deleted) != 0 {
				t.Fatal("image cleanup affected other resources")
			}
			remaining, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
			if err != nil || len(remaining) != len(values)-1 {
				t.Fatal("image cleanup closed unrelated assets", err, len(remaining))
			}
		})
	}
}

func TestAzureLocalImagesKnownInventory(t *testing.T) {
	for _, kind := range []string{azureLocalImageType, azureLocalMarketplaceType} {
		for _, mode := range []string{"omitted", "absent", "removed-context", "removed-proof", "removed-both"} {
			t.Run(kind+mode, func(t *testing.T) {
				f := newLocalImageFixture(t, kind)
				value := f.requestAsset(t).Asset
				f.omitted[f.id] = true
				request := f.request(kind)
				request.KnownNativeIDs = []string{f.id}
				request.KnownNativeMetadata = map[string]map[string]any{f.id: value.Normalized}
				switch mode {
				case "absent":
					delete(f.values, f.id)
				case "removed-context":
					delete(value.Normalized, azureLocalCleanup)
				case "removed-proof":
					delete(value.Normalized, azureLocalCleanupProof)
				case "removed-both":
					delete(value.Normalized, azureLocalCleanup)
					delete(value.Normalized, azureLocalCleanupProof)
				}
				batch, err := f.runtime.List(t.Context(), request)
				if mode == "absent" {
					if err != nil || len(batch.Items) != 0 || len(batch.AbsentNativeIDs) != 1 || batch.AbsentNativeIDs[0] != f.id {
						t.Fatal("own image absence lost", batch, err)
					}
				} else if mode == "omitted" {
					if err != nil || len(batch.Items) != 1 || batch.Items[0].NativeID != f.id || !*batch.Items[0].Actionable {
						t.Fatal("known image omission not recovered", batch, err)
					}
				} else if err == nil {
					t.Fatal("stripped image context accepted", mode)
				}
			})
		}
	}
}
