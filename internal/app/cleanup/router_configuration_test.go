package cleanup

import (
	"errors"
	"path/filepath"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
)

func routerMutationAsset(id, kind, router string) asset.Asset {
	suffix := map[string]string{"RouterNat": "nats", "RoutePolicy": "routePolicies", "NamedSet": "namedSets"}[kind]
	native := "//compute.googleapis.com/projects/project/regions/region/routers/" + router
	if suffix != "" {
		native += "/" + suffix + "/" + id
	}
	return asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{ConnectionID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", NativeType: "compute.googleapis.com/" + kind, NativeID: native}, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable}}
}

func TestRouterScopePlanPreservesDependenciesAndIndependentRouters(t *testing.T) {
	input := plan.Input{CleanupTaskID: "router-plan"}
	for i, kind := range []string{"NamedSet", "RoutePolicy", "RouterNat", "Router", "RoutePolicy"} {
		id := string(rune('a' + i))
		router := "one"
		if i == 4 {
			router = "two"
		}
		value := routerMutationAsset(id, kind, router)
		if i == 1 {
			value.Identity.Partition = "google-cloud"
		}
		input.Assets = append(input.Assets, value)
		input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, value.ID)
	}
	result, err := solveCleanupPlan(input)
	if err != nil || len(result.Steps) != 5 {
		t.Fatal(result, err)
	}
	byAsset := map[asset.AssetID]plan.CleanupTaskStep{}
	for _, step := range result.Steps {
		byAsset[step.AssetID] = step
	}
	for _, pair := range [][2]asset.AssetID{{"b", "a"}, {"c", "b"}, {"d", "c"}} {
		if !slices.Contains(byAsset[pair[0]].DependsOn, byAsset[pair[1]].ID) {
			t.Fatal(result.Steps)
		}
	}
	if len(byAsset["e"].DependsOn) != 0 {
		t.Fatal("other router serialized", result.Steps)
	}
	// Persisted order can differ from the DAG. Reordering must retain step IDs,
	// existing prerequisites and evidence rather than add a reverse cycle.
	steps := []plan.CleanupTaskStep{{ID: "set", AssetID: "set", Action: "delete", DependsOn: []plan.StepID{"policy"}, Evidence: map[string]any{"review": "set"}}, {ID: "policy", AssetID: "policy", Action: "delete", Evidence: map[string]any{"review": "policy"}}, {ID: "nat", AssetID: "nat", Action: "delete"}}
	scopes := map[asset.AssetID]string{"set": "same", "policy": "same", "nat": "same"}
	ordered, err := serializeRouterSteps(steps, scopes)
	if err != nil {
		t.Fatal(ordered, err)
	}
	positions := map[plan.StepID]int{}
	for i, step := range ordered {
		positions[step.ID] = i
	}
	if positions["policy"] >= positions["set"] || ordered[positions["set"]].Evidence["review"] != "set" {
		t.Fatal(ordered)
	}
	if len(steps[0].Evidence) != 1 || len(steps[1].DependsOn) != 0 {
		t.Fatal("mutated frozen input", steps)
	}
	again, err := serializeRouterSteps(ordered, scopes)
	if err != nil || !reflect.DeepEqual(again, ordered) {
		t.Fatal("non-idempotent", again, err)
	}
}

func TestRouterScopeGuardsAllNativeFamilies(t *testing.T) {
	for _, kind := range []string{"Router", "RouterNat", "RoutePolicy", "NamedSet"} {
		for _, mode := range []string{"frozen", "missing-snapshot", "wrong-annotation", "partition-alias", "malformed", "wrong-connection", "different-router", "succeeded", "failed-detach"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				ctx := t.Context()
				db := filepath.Join(t.TempDir(), "scope.db")
				repos, err := sqlite.Open(db, "../../../migrations")
				if err != nil {
					t.Fatal(err)
				}
				now := time.Now().UTC()
				value := routerMutationAsset("old", kind, "one")
				if mode == "partition-alias" {
					value.Identity.Partition = "google-cloud"
				}
				if mode == "wrong-connection" {
					value.Identity.ConnectionID = "other"
				}
				if err := repos.Inventory().PutAsset(ctx, value); err != nil {
					t.Fatal(err)
				}
				evidence := map[string]any{plan.EvidencePlannedAsset: value}
				if mode == "missing-snapshot" {
					evidence = map[string]any{}
				}
				if mode == "wrong-annotation" {
					evidence[routerMutationScope] = "unrelated"
				}
				if mode == "malformed" {
					evidence[plan.EvidencePlannedAsset] = nil
				}
				old := persistence.CleanupTaskAggregate{Task: plan.CleanupTask{ID: "old", ConnectionID: "connection", Status: plan.StatusExecuting, CreatedAt: now}, Steps: []plan.CleanupTaskStep{{ID: "step", AssetID: value.ID, Action: "delete", Evidence: evidence}}}
				if err := repos.CleanupTasks().CreateTask(ctx, old.Task, old.Steps, nil); err != nil {
					t.Fatal(err)
				}
				attempt := execution.ExecutionAttempt{ID: "run", ConnectionID: "connection", CleanupTaskID: "old", Status: execution.ExecutionRunning, IdempotencyKey: "run", CreatedAt: now}
				if mode == "failed-detach" {
					attempt.Status = execution.ExecutionFailed
				}
				if err := repos.Executions().CreateExecution(ctx, attempt); err != nil {
					t.Fatal(err)
				}
				action := execution.ActionAttempt{ID: "action", ExecutionID: "run", CleanupTaskStepID: "step", AssetID: value.ID, Action: "delete", Status: execution.ActionWaiting, IdempotencyKey: "action", CreatedAt: now, UpdatedAt: now}
				if mode == "succeeded" {
					action.Status = execution.ActionSucceeded
				}
				if mode == "failed-detach" {
					action.ProviderOperationID = "detach-done"
					action.ProviderResult = map[string]any{"phase": "route_policy_detach"}
				}
				if err := repos.Executions().AppendAction(ctx, action); err != nil {
					t.Fatal(err)
				}
				router := "one"
				if mode == "different-router" {
					router = "two"
				}
				selected := routerMutationAsset("new", "RoutePolicy", router)
				current := persistence.CleanupTaskAggregate{Task: plan.CleanupTask{ID: "new", ConnectionID: "connection"}, Steps: []plan.CleanupTaskStep{{ID: "new", AssetID: selected.ID, Action: "delete", Evidence: map[string]any{plan.EvidencePlannedAsset: selected}}}}
				repos, err = sqlite.Open(db, "../../../migrations")
				if err != nil {
					t.Fatal(err)
				}
				registry := &mutationProofRegistry{}
				err = guardSharedConfiguration(ctx, repos, current, registry, "")
				if mode == "failed-detach" {
					expected := 1 // Component providers must verify every possible phase.
					if registry.calls != expected {
						t.Fatal("incorrect settlement capability", kind, registry.calls)
					}
				}
				allowed := mode == "different-router" || mode == "succeeded"
				if allowed && err != nil || !allowed && !errors.Is(err, persistence.ErrConflict) {
					t.Fatal("incorrect router reservation", mode, err)
				}
			})
		}
	}
}
