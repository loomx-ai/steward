package azure

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func dataFactoryWorkerRepository(t *testing.T, f *dataFactoryFixture) (*sqlite.Repositories, *providerruntime.Registry, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "datafactory.db")
	repository, err := sqlite.Open(path, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	ctx, now := t.Context(), time.Now().UTC()
	if err := repository.PutConnection(ctx, asset.CloudConnection{ID: "connection", Provider: asset.ProviderAzure, Partition: "azure", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutCredential(ctx, asset.ConnectionCredential{ConnectionID: "connection", Provider: asset.ProviderAzure, Type: asset.CredentialAzureServicePrincipal, EnvelopeVersion: 1, Nonce: "test", Ciphertext: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repository.PutScope(ctx, asset.Scope{ID: "root", ConnectionID: "connection", Kind: asset.ScopeSubscription, NativeID: testSubscription, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, region := range []string{"eastus", "westus"} {
		if err := repository.PutRegion(ctx, asset.ConnectionRegion{ID: region, ConnectionID: "connection", RegionID: region, Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	registry := providerruntime.NewRegistry()
	if err := registry.Register(f.runtime); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterBundle(f.runtime.Bundle()); err != nil {
		t.Fatal(err)
	}
	previous := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		if previous != nil {
			if res, ok := previous(req); ok {
				return res, true
			}
		}
		return fleetGraphEmptyIndexes(t, req)
	}
	return repository, registry, path
}

func dataFactoryWorkerScan(t *testing.T, f *dataFactoryFixture, repository *sqlite.Repositories, registry *providerruntime.Registry, kinds []string, failure, nativeGraph bool) []asset.Asset {
	t.Helper()
	ctx := t.Context()
	creator, err := inventory.NewCreator(repository, registry)
	if err != nil {
		t.Fatal(err)
	}
	ids := []asset.ResourceKindID{}
	for _, kind := range kinds {
		ids = append(ids, f.runtime.resourceKind(kind).ID)
	}
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: "connection", RequestedBy: "datafactory-worker", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"eastus", "westus"}, ResourceKindIDs: ids})
	if err != nil || len(created.Shards) != 2*len(kinds) {
		t.Fatal("registered Data Factory regional shards", len(created.Shards), err)
	}
	for _, shard := range created.Shards {
		if shard.Source != dataFactoryInventorySource || shard.Authoritative {
			t.Fatal("native source gained list-absence authority", shard)
		}
		shard.Authoritative = true // Saved legacy flags must not override source authority.
		if err := repository.PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
	}
	handler := inventory.NewScanHandler(repository, registry, inventory.NewService(repository))
	for _, job := range created.Jobs {
		if err := handler.Handle(ctx, job); err != nil && !failure {
			t.Fatal("registered native Data Factory inventory", err)
		}
	}
	failed := false
	for _, shard := range created.Shards {
		stored, err := repository.GetScanShard(ctx, shard.ID)
		if err != nil || stored.Authoritative || stored.Coverage.Authoritative || !failure && stored.Status != asset.ShardSucceeded {
			t.Fatal("Data Factory scan widened absence or failed", stored.Status, err)
		}
		failed = failed || stored.Status != asset.ShardSucceeded
	}
	if failure && !failed {
		t.Fatal("uncertain native scan succeeded")
	}
	if !failure {
		jobs, err := repository.ListJobsByAggregate(ctx, "scan_task", string(created.ScanRun.ID))
		if err != nil {
			t.Fatal(err)
		}
		graphs := 0
		for _, job := range jobs {
			if job.Type != execution.JobGraph {
				continue
			}
			graphs++
			var contributors governance.ContributorResolver
			if nativeGraph {
				contributors = fleetHubGraphContributors{f.runtime}
			}
			if err := governance.NewGraphHandler(repository, registry, contributors).Handle(ctx, job); err != nil {
				t.Fatal("registered native Data Factory graph", err)
			}
		}
		if graphs != 1 {
			t.Fatal("scan did not schedule graph reconciliation", graphs)
		}
	}
	values, err := repository.ListActiveAssetsByConnection(ctx, "connection", "")
	if err != nil {
		t.Fatal(err)
	}
	return values
}

func TestDataFactoryRegisteredInventoryAndKnownAbsence(t *testing.T) {
	f := newDataFactoryFixture(t)
	repository, registry, _ := dataFactoryWorkerRepository(t, f)
	values := dataFactoryWorkerScan(t, f, repository, registry, slices.Sorted(maps.Keys(f.ids)), false, true)
	if len(values) != 14 {
		t.Fatal("registered native inventory lost a kind", len(values))
	}
	for _, value := range values {
		if string(value.ID) == value.Identity.NativeID || value.Location != "eastus" || value.Normalized["_inventory_source"] != dataFactoryInventorySource || value.Capabilities.Has(asset.CapabilityActionable) != (value.Identity.NativeType != dataFactoryNetworkType) {
			t.Fatal("inventory lost generated identity, region or native capabilities", value.Identity.NativeType)
		}
	}
	root, child := f.ids[dataFactoryType], f.ids[dataFactoryDatasetType]
	f.omitted[root], f.omitted[child] = true, true
	scan := func(failure bool) []asset.Asset {
		return dataFactoryWorkerScan(t, f, repository, registry, []string{dataFactoryDatasetType}, failure, false)
	}
	if len(scan(false)) != 14 {
		t.Fatal("LIST omission erased a known live dataset")
	}
	previous := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, child) {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden"}}, nil), true
		}
		return previous(req)
	}
	if len(scan(true)) != 14 {
		t.Fatal("failed native read erased active inventory")
	}
	f.override = previous
	f.deleteResource(root)
	if len(scan(true)) != 14 {
		t.Fatal("parent 404 erased a surviving child")
	}
	f.deleteResource(child)
	if len(scan(true)) != 14 {
		t.Fatal("inconsistent surviving family reconciled as complete")
	}
	for _, id := range slices.Sorted(maps.Keys(f.resources)) {
		f.deleteResource(id)
	}
	if len(scan(false)) != 13 {
		t.Fatal("independent child 404 failed to reconcile absence")
	}
}

func TestDataFactoryRegisteredExecutionAndPhaseRecovery(t *testing.T) {
	for _, mode := range []string{"ssis", "shared-work"} {
		t.Run(mode, func(t *testing.T) {
			f := newDataFactoryFixture(t)
			root, runtime := f.ids[dataFactoryType], f.ids[dataFactoryIRType]
			consumerRoot := ""
			var run, debug map[string]any
			debugPresent, cancelAccepted := true, false
			if mode == "ssis" {
				props := object(f.resources[runtime]["properties"])
				props["type"], props["typeProperties"] = "Managed", map[string]any{"ssisProperties": map[string]any{"edition": "Standard"}}
				object(f.statuses[runtime]["properties"])["type"], object(f.statuses[runtime]["properties"])["state"] = "Managed", "Started"
				f.deleteResource(f.ids[dataFactoryNodeType])
				object(f.resources[f.ids[dataFactoryLinkedType]]["properties"])["connectVia"] = map[string]any{"type": "IntegrationRuntimeReference", "referenceName": last(runtime)}
			} else {
				consumerRoot, _ = dataFactorySharingFixture(t, f, "Key")
				run, debug = dataFactoryWorkFixture(t, f)
				previous := f.override
				f.override = func(req *http.Request) (*http.Response, bool) {
					switch strings.ToLower(req.URL.Path) {
					case root + "/querypipelineruns":
						rows := []any{}
						if !cancelAccepted {
							rows = append(rows, run)
						}
						return jsonResponse(200, map[string]any{"value": rows}, nil), true
					case root + "/pipelineruns/" + text(run["runId"]):
						return jsonResponse(200, run, nil), true
					case root + "/querydataflowdebugsessions":
						rows := []any{}
						if debugPresent {
							rows = append(rows, debug)
						}
						return jsonResponse(200, map[string]any{"value": rows}, nil), true
					}
					return previous(req)
				}
			}
			repository, registry, path := dataFactoryWorkerRepository(t, f)
			values := dataFactoryWorkerScan(t, f, repository, registry, slices.Sorted(maps.Keys(f.ids)), false, true)
			selectors := []plan.CleanupSelector{}
			var consumerFactoryID asset.AssetID
			for _, value := range values {
				if value.Identity.NativeID == root || value.Identity.NativeID == consumerRoot {
					selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: value.ID})
				}
				if value.Identity.NativeID == consumerRoot {
					consumerFactoryID = value.ID
				}
			}
			ctx := t.Context()
			var logs []execution.JobLogEntry
			ctx = execution.WithJobLogSink(ctx, execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
			planner := cleanup.NewService(repository, registry)
			task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: selectors, CreatedBy: "operator"})
			expectedSteps := 7
			if mode == "shared-work" {
				expectedSteps = 5
			}
			if err != nil || task.Task.Status != plan.StatusReady || len(task.Steps) != expectedSteps {
				t.Fatal("registered Data Factory plan", err, task.Task.Status, task.Task.Blockers, len(task.Steps))
			}
			attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{ConnectionID: "connection", CleanupTaskID: task.Task.ID, RequestedBy: "operator", IdempotencyKey: "datafactory-worker-" + mode, Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
			if err != nil {
				t.Fatal(err)
			}
			previous := f.override
			mutations, polls := map[string]int{}, 0
			stopSucceeded := false
			var active plan.CleanupTaskStep
			f.override = func(req *http.Request) (*http.Response, bool) {
				id := strings.ToLower(req.URL.Path)
				if req.Method == "GET" && strings.Contains(id, "/stop/operation") {
					polls++
					if strings.Contains(id, "/operationresults/") {
						res := jsonResponse(200, nil, nil)
						res.Body = http.NoBody
						return res, true
					}
					state := "InProgress"
					if stopSucceeded {
						state = "Succeeded"
					}
					return jsonResponse(200, map[string]any{"status": state}, nil), true
				}
				phase, target, item := "", id, ""
				switch {
				case req.Method == "DELETE":
					phase = "delete"
				case req.Method == "POST" && id == runtime+"/stop":
					phase, target = "stop-runtime", runtime
				case req.Method == "POST" && strings.HasSuffix(id, "/cancel"):
					phase, target, item = "cancel-run", root, text(run["runId"])
					if req.URL.Query().Get("isRecursive") != "false" {
						t.Fatal("worker canceled unreviewed recursive work")
					}
				case req.Method == "POST" && id == root+"/deletedataflowdebugsession":
					phase, target, item = "delete-debug", root, text(debug["sessionId"])
				case req.Method == "POST" && id == runtime+"/removelinks":
					phase, target, item = "remove-links", runtime, last(consumerRoot)
				default:
					return previous(req)
				}
				current, err := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(active.ID))
				key := phase + "/" + target + "/" + item
				if err != nil || current.IdempotencyKey == "" || req.Header.Get("x-ms-client-request-id") != azureRequestID(current.IdempotencyKey+":"+phase+":"+target+":"+item) || mutations[key] != 0 || req.Header.Get("If-Match") != "" || req.URL.Query().Get("api-version") != dataFactoryVersion {
					t.Fatal("mutation lost durable intent, native binding or was replayed", key, err)
				}
				mutations[key]++
				switch phase {
				case "stop-runtime":
					object(f.statuses[runtime]["properties"])["state"] = "Stopping"
					base := runtime + "/stop/"
					op := "/00001111222233334444555566667777"
					return jsonResponse(202, nil, http.Header{"Azure-Asyncoperation": {apiURL(base+"operationstatuses"+op, dataFactoryVersion)}, "Location": {apiURL(base+"operationresults"+op, dataFactoryVersion)}}), true
				case "cancel-run":
					cancelAccepted, run["status"] = true, "Canceling"
					return jsonResponse(200, "", nil), true
				case "delete-debug", "remove-links":
					var body map[string]any
					field := "sessionId"
					if phase == "remove-links" {
						field = "factoryName"
						if f.resources[consumerRoot] != nil || f.resources[consumerRoot+"/integrationruntimes/shared-runtime"] != nil {
							t.Fatal("worker removed links before own consumer absence")
						}
					}
					if json.NewDecoder(req.Body).Decode(&body) != nil || len(body) != 1 || body[field] != item {
						t.Fatal("native preparation body changed", phase)
					}
					return jsonResponse(200, nil, nil), true
				default:
					return jsonResponse(200, nil, nil), true
				}
			}
			completed := map[plan.StepID]bool{}
			for range len(task.Steps) {
				active = plan.CleanupTaskStep{}
				for _, candidate := range task.Steps {
					if !completed[candidate.ID] && !slices.ContainsFunc(candidate.DependsOn, func(id plan.StepID) bool { return !completed[id] }) {
						// Exercise stale host links after the consumer factory is
						// gone. Either factory may legally run once its IR is gone.
						if active.ID == "" || candidate.AssetID == consumerFactoryID {
							active = candidate
						}
						if candidate.AssetID == consumerFactoryID {
							break
						}
					}
				}
				if active.ID == "" {
					t.Fatal("no executable dependency order")
				}
				value, err := repository.GetAsset(ctx, active.AssetID)
				if err != nil {
					t.Fatal(err)
				}
				jobs, err := repository.ListJobsByAggregate(ctx, "cleanup_task", string(task.Task.ID))
				if err != nil {
					t.Fatal(err)
				}
				var job execution.Job
				for _, candidate := range jobs {
					if text(candidate.Payload["cleanup_task_step_id"]) == string(active.ID) {
						job = candidate
					}
				}
				if job.ID == "" {
					t.Fatal("missing persisted step job")
				}
				resume := func(pending bool) execution.ActionAttempt {
					t.Helper()
					wire, _ := json.Marshal(job)
					var restored execution.Job
					if json.Unmarshal(wire, &restored) != nil {
						t.Fatal("job serialization failed")
					}
					repository, err = sqlite.Open(path, "../../migrations")
					if err != nil {
						t.Fatal(err)
					}
					fresh, err := NewRuntime(f.runtime.credentials)
					if err != nil {
						t.Fatal(err)
					}
					fresh.transport = f.runtime.transport
					restarted := providerruntime.NewRegistry()
					if err := restarted.Register(fresh); err != nil {
						t.Fatal(err)
					}
					if err := restarted.RegisterBundle(fresh.Bundle()); err != nil {
						t.Fatal(err)
					}
					resolver := cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
						return restarted.ResolveAction(ctx, value.Identity.ConnectionID, value)
					})
					worker := cleanup.NewExecutionHandler(cleanup.NewService(repository, restarted), resolver)
					var retry *cleanup.RetryError
					err = worker.Handle(ctx, restored)
					if pending && !errors.As(err, &retry) || !pending && err != nil {
						failed, _ := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(active.ID))
						t.Fatal("Data Factory worker lost durable phase", value.Identity.NativeType, pending, err, failed.Status, failed.ProviderError)
					}
					current, err := repository.Executions().GetActionByExecutionStep(ctx, attempt.ID, string(active.ID))
					if err != nil {
						t.Fatal(err)
					}
					return current
				}
				current := resume(true)
				origin := current.ProviderOperationID
				for range 5 {
					current = resume(true)
					phase := text(current.ProviderResult["phase"])
					if phase == "delete" {
						break
					}
					before := batchClone(current.ProviderResult)
					current = resume(true)
					if current.ProviderResult["phase"] != phase || current.ProviderOperationID != origin || current.ProviderResult["binding"] == "" || len(object(current.ProviderResult["accepted"])) != len(object(before["accepted"])) {
						t.Fatal("restarted worker changed accepted phase")
					}
					switch phase {
					case "stop-runtime":
						stopSucceeded = true
						current = resume(true)
						if current.ProviderResult["operation_done"] != true || current.ProviderResult["phase"] != phase {
							t.Fatal("completed Stop operation did not persist before own Stopped state")
						}
						beforePolls := polls
						resume(true)
						if polls != beforePolls {
							t.Fatal("restarted worker repolled completed Stop result")
						}
						object(f.statuses[runtime]["properties"])["state"] = "Stopped"
					case "cancel-run":
						run["status"], run["runEnd"] = "Cancelled", "2026-09-01T00:05:00Z"
					case "delete-debug":
						debugPresent = false
					case "remove-links":
						object(object(f.statuses[runtime]["properties"])["typeProperties"])["links"] = []any{}
					default:
						t.Fatal("unexpected durable preparation", phase)
					}
				}
				if current.ProviderResult["phase"] != "delete" || current.ProviderOperationID != origin {
					t.Fatal("preparation did not reach durable delete", current.ProviderResult["phase"])
				}
				stored, err := repository.GetAsset(ctx, value.ID)
				if err != nil || stored.ClosedAt != nil {
					t.Fatal("accepted DELETE closed a live resource", err)
				}
				f.deleteResource(value.Identity.NativeID)
				if value.Identity.NativeType == dataFactoryType && slices.ContainsFunc(slices.Sorted(maps.Keys(f.resources)), func(id string) bool { return strings.HasPrefix(id, value.Identity.NativeID+"/") }) {
					resume(true)
					stored, err := repository.GetAsset(ctx, value.ID)
					if err != nil || stored.ClosedAt != nil {
						t.Fatal("factory 404 concealed live delegated children", err)
					}
					for _, id := range slices.Sorted(maps.Keys(f.resources)) {
						if strings.HasPrefix(id, value.Identity.NativeID+"/") {
							f.deleteResource(id)
						}
					}
				}
				resume(true)
				resume(false)
				completed[active.ID] = true
			}
			remaining, err := repository.ListActiveAssetsByConnection(ctx, "connection", "")
			if err != nil || len(remaining) != 0 {
				t.Fatal("registered cleanup left active Data Factory assets", len(remaining), err)
			}
			if len(mutations) != 8 {
				t.Fatal("registered cleanup lost native preparation or deletion", mutations)
			}
			journal, err := repository.Executions().ListActions(ctx, attempt.ID)
			if err != nil || len(journal) != expectedSteps || len(logs) == 0 {
				t.Fatal("Data Factory journal missing", len(journal), err)
			}
			payload, _ := json.Marshal(map[string]any{"assets": values, "task": task, "journal": journal, "logs": logs})
			for _, secret := range []string{"private-native-datafactory-config", "private-shared-runtime-key", "sensitive-work-definition", "sensitive-debug-definition"} {
				if strings.Contains(string(payload), secret) {
					t.Fatal("private native Data Factory content reached persisted public evidence", secret)
				}
			}
		})
	}
}
