package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func identityRegistry(t *testing.T, r *Runtime) *providerruntime.Registry {
	t.Helper()
	registry := providerruntime.NewRegistry()
	if err := registry.Register(r); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterBundle(r.Bundle()); err != nil {
		t.Fatal(err)
	}
	return registry
}
func TestIdentityGroupsCreatorWorkerPreservesDirectoryVisibility(t *testing.T) {
	ctx := context.Background()
	s := newIdentityScenario()
	r := s.runtime(t)
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "identity-scan.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	root := asset.Scope{ID: "project", ConnectionID: connection.ID, Kind: asset.ScopeProject, NativeID: "sample-project", Name: "Project", CreatedAt: now, UpdatedAt: now}
	region := asset.ConnectionRegion{ID: "region", ConnectionID: connection.ID, RegionID: "us-central1", DiscoveredName: "US Central", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}
	for _, write := range []func() error{func() error { return repositories.Connections().PutConnection(ctx, connection) }, func() error { return repositories.Inventory().PutScope(ctx, root) }, func() error { return repositories.Regions().PutRegion(ctx, region) }} {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
	registry := identityRegistry(t, r)
	creator, err := inventory.NewCreator(repositories, registry, inventory.WithCreatorClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	handler := inventory.NewScanHandler(repositories, registry, inventory.NewService(repositories.Inventory(), inventory.WithClock(func() time.Time { return now })))
	kinds := []asset.ResourceKindID{r.resourceKind(identityGroupType).ID, r.resourceKind(identityMemberType).ID}
	for _, run := range []string{"initial", "denied", "scope_removed", "scope_changed", "visibility_filtered"} {
		switch run {
		case "denied":
			s.hook = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Path == "/v1/groups" && req.URL.Query().Get("pageSize") == "500" {
					return dataformResponse(req, 403, map[string]any{"error": map[string]any{"message": "PRIVATE_DIRECTORY_PERMISSION_ERROR"}}), true
				}
				return nil, false
			}
		case "scope_removed":
			s.hook = nil
			s.root = ""
		case "scope_changed":
			s.root = "customers/Cother"
			s.groups = map[string]map[string]any{}
			s.members = map[string]map[string]any{}
		case "visibility_filtered":
			s.root = "customers/C01234567"
		}
		scan, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "test", RegionMode: inventory.RegionModeAllActive, ResourceKindIDs: kinds})
		if err != nil || len(scan.Shards) != 2 {
			t.Fatalf("directory shard routing: %+v %v", scan, err)
		}
		for _, job := range scan.Jobs {
			err := handler.Handle(ctx, job)
			if (run == "denied") != (err != nil) {
				t.Fatalf("scan %s: %v", run, err)
			}
		}
		for _, shard := range scan.Shards {
			finished, err := repositories.Inventory().GetScanShard(ctx, shard.ID)
			if err != nil || finished.Source != identityInventorySource || finished.Authoritative || finished.Coverage.Complete == (run == "denied") {
				t.Fatalf("directory authority/coverage: %+v %v", finished, err)
			}
		}
		page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 20})
		if err != nil || len(page.Items) != 4 {
			t.Fatalf("directory projection: %+v %v", page, err)
		}
		for _, value := range page.Items {
			if value.ClosedAt != nil || value.DeletedAt != nil || !value.Capabilities.Has(asset.CapabilityActionable) || value.Normalized["project_id"] != nil || value.Normalized["project_number"] != nil || text(value.Normalized[identitySnapshot]) == "" {
				t.Fatalf("directory observation lost or falsely owned by project: %+v", value)
			}
		}
		raw, _ := json.Marshal(page)
		if strings.Contains(string(raw), "PRIVATE_DIRECTORY_") {
			t.Fatal("directory permission message escaped redaction")
		}
		now = now.Add(time.Minute)
	}
}

// The real cleanup service/SQLite worker is discarded between invocation, wait
// and readback. The last instance can succeed only using the stored receipt.
func TestIdentityGroupSQLiteExecutionRestartsBeforeReceiptReadback(t *testing.T) {
	for _, variant := range []string{"completed", "receipt_lost", "receipt_corrupt", "surviving_member"} {
		t.Run(variant, func(t *testing.T) {
			ctx := context.Background()
			s := newIdentityScenario()
			r, values, _, _ := identityReviewed(t, s)
			repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "identity-execution.db"), filepath.Join("..", "..", "migrations"))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			scope := asset.Scope{ID: "identity-global", ConnectionID: "connection", Kind: asset.ScopeGlobal, NativeID: "sample-project/global", Name: "Global", CreatedAt: now, UpdatedAt: now}
			if err := repositories.Connections().PutConnection(ctx, asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
				t.Fatal(err)
			}
			for i := range values {
				values[i].ScopeID = scope.ID
				values[i].ResourceKindID = r.resourceKind(values[i].Identity.NativeType).ID
				values[i].CurrentObservationID = asset.ObservationID(fmt.Sprintf("native-observation-%d", i))
				values[i].FirstSeenAt = now
				values[i].LastSeenAt = now
				values[i].Name = last(values[i].Identity.NativeID)
				if err := repositories.Inventory().PutResourceKind(ctx, r.resourceKind(values[i].Identity.NativeType)); err != nil {
					t.Fatal(err)
				}
				if err := repositories.Inventory().PutAsset(ctx, values[i]); err != nil {
					t.Fatal(err)
				}
			}
			contributor, err := r.ServiceLifecycle(ctx, "connection")
			if err != nil {
				t.Fatal(err)
			}
			contribution, err := contributor.Contribute(ctx, scope.ID, values)
			if err != nil {
				t.Fatal(err)
			}
			for i := range contribution.Bindings {
				b := &contribution.Bindings[i]
				b.ID = graph.LifecycleBindingID(fmt.Sprintf("identity-binding-%d", i))
				b.ObservedAt = now
				b.GraphRevision = "identity-native-graph"
			}
			for i := range contribution.Relationships {
				v := &contribution.Relationships[i]
				v.ID = graph.RelationshipID(fmt.Sprintf("identity-relation-%d", i))
				v.ObservedAt = now
				v.GraphRevision = "identity-native-graph"
			}
			if err := repositories.Graph().ReplaceGraph(ctx, scope.ID, "identity-native-graph", contribution.Relationships, contribution.Bindings); err != nil {
				t.Fatal(err)
			}
			parent := batchAsset(values, identityTestGroup)
			registry := identityRegistry(t, r)
			planner := cleanup.NewService(repositories, registry, cleanup.WithClock(func() time.Time { return now }))
			task, err := planner.CreateTask(ctx, cleanup.CreateTaskRequest{ConnectionID: "connection", Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: parent.ID}}, CreatedBy: "test"})
			if err != nil {
				t.Fatal(err)
			}
			attempt, err := planner.CreateExecution(ctx, cleanup.CreateExecutionRequest{CleanupTaskID: task.Task.ID, ConnectionID: "connection", RequestedBy: "test", IdempotencyKey: "sqlite-native-group", Confirmation: cleanup.ExecutionConfirmation{Acknowledged: true}})
			if err != nil {
				t.Fatal(err)
			}
			job, err := repositories.Jobs().ClaimNext(ctx, "identity-worker", now, time.Minute)
			if err != nil {
				t.Fatal(err)
			}
			run := func() error {
				restarted := s.runtime(t)
				handler := cleanup.NewExecutionHandler(planner, cleanup.ActionResolverFunc(func(ctx context.Context, value asset.Asset) (cleanup.ActionDriver, error) {
					return restarted.ResolveAction(ctx, value.Identity.ConnectionID, value)
				}))
				return handler.Handle(ctx, job)
			}
			for _, want := range []execution.ActionStatus{execution.ActionWaiting, execution.ActionReadingBack} {
				var retry *cleanup.RetryError
				if err := run(); !errors.As(err, &retry) {
					t.Fatalf("native worker checkpoint %s: %v", want, err)
				}
				actions, err := repositories.Executions().ListActions(ctx, attempt.ID)
				if err != nil || len(actions) != 1 || actions[0].Status != want || actions[0].ProviderResult["phase"] != "identity_group_delete" {
					t.Fatalf("persisted checkpoint %s: %+v %v", want, actions, err)
				}
				now = now.Add(3 * time.Second)
			}
			actions, err := repositories.Executions().ListActions(ctx, attempt.ID)
			if err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "receipt_lost":
				actions[0].ProviderResult = nil
			case "receipt_corrupt":
				actions[0].ProviderResult["binding"] = "different-reviewed-plan"
			case "surviving_member":
				fresh := newIdentityScenario()
				name := identityTestGroup + "/memberships/m-user"
				s.members[name] = fresh.members[name]
			}
			if variant == "receipt_lost" || variant == "receipt_corrupt" {
				if err := repositories.Executions().UpdateAction(ctx, actions[0]); err != nil {
					t.Fatal(err)
				}
			}
			err = run()
			actions, readErr := repositories.Executions().ListActions(ctx, attempt.ID)
			if readErr != nil || len(actions) != 1 {
				t.Fatalf("native actions: %+v %v", actions, readErr)
			}
			if variant == "completed" {
				if err != nil || actions[0].Status != execution.ActionSucceeded || actions[0].Readback["state"] != "delete_confirmed" {
					t.Fatalf("restarted native receipt: %+v %v", actions, err)
				}
			} else if actions[0].Status == execution.ActionSucceeded {
				t.Fatalf("invalid persisted completion succeeded: %+v %v", actions, err)
			}
			for _, prior := range values {
				persisted, err := repositories.Inventory().GetAsset(ctx, prior.ID)
				if err != nil {
					t.Fatal(err)
				}
				shouldDelete := variant == "completed" && prior.Identity.NativeID != "//"+identityHost+"/"+identityTestNested
				if (persisted.DeletedAt != nil) != shouldDelete {
					t.Fatalf("receipt projected incorrect deletion state: %+v", persisted)
				}
			}
			if len(s.writes) != 1 || s.writes[0] != identityTestGroup || s.groups[identityTestNested] == nil {
				t.Fatalf("restart replayed group delete or deleted nested group: %v", s.writes)
			}
		})
	}
}
