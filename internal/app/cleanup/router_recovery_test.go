package cleanup

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestRouterRecoveryUsesFrozenWorkerImpactOrder(t *testing.T) {
	for _, mode := range []string{"reviewed", "missing-snapshot", "changed-identity", "missing-step", "managed-prerequisite", "unknown-impact"} {
		t.Run(mode, func(t *testing.T) {
			repos, err := sqlite.Open(filepath.Join(t.TempDir(), "router.db"), "../../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			root := routerMutationAsset("root", "Router", "one")
			task := persistence.CleanupTaskAggregate{}
			step := plan.CleanupTaskStep{ID: "root", AssetID: root.ID, Action: "delete", DependsOn: []plan.StepID{"a", "b"}, Evidence: map[string]any{plan.EvidencePlannedAsset: root, plan.EvidenceRequiredDeletions: []plan.RequiredDeletion{{StepID: "b", AssetID: "b"}}}}
			task.Steps = append(task.Steps, step)
			for _, entry := range []struct{ id, kind string }{{"a", "RoutePolicy"}, {"b", "NamedSet"}, {"nat", "RouterNat"}} {
				value := routerMutationAsset(entry.id, entry.kind, "one")
				value.Normalized = map[string]any{"version": "reviewed"}
				evidence := map[string]any{plan.EvidencePlannedAsset: value, "lifecycle_controller": "root", "cleanup_policy": graph.CleanupDirect}
				if entry.id == "nat" {
					task.ImpactItems = append(task.ImpactItems, plan.ImpactItem{ID: "nat", AssetID: value.ID, ControllerID: root.ID, DelegatedTo: step.ID, Expected: plan.ExpectedDelegatedDelete, Evidence: evidence})
				} else {
					task.Steps = append(task.Steps, plan.CleanupTaskStep{ID: plan.StepID(entry.id), AssetID: value.ID, Action: "delete", Evidence: evidence})
				}
				value.Normalized = map[string]any{"version": "later-inventory"}
				if mode == "changed-identity" && entry.id == "a" {
					value.Identity.ConnectionID = "other"
				}
				if err := repos.Inventory().PutAsset(t.Context(), value); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "missing-snapshot":
				delete(task.ImpactItems[0].Evidence, plan.EvidencePlannedAsset)
			case "missing-step":
				task.Steps = task.Steps[:2]
			case "managed-prerequisite":
				step.Evidence[plan.EvidenceRequiredDeletions] = []plan.RequiredDeletion{{StepID: "b", AssetID: "b", ControllerAssetID: "other"}}
			case "unknown-impact":
				task.ImpactItems[0].Expected = plan.ExpectedUnknown
			}
			request := contracts.ActionRequest{Asset: root, Action: "delete"}
			err = routerRecoveryImpacts(t.Context(), repos, task, step, &request)
			if mode != "reviewed" {
				if err == nil {
					t.Fatal("invalid recovery accepted")
				}
				return
			}
			if err != nil || len(request.PrerequisiteDeletions) != 2 || request.PrerequisiteDeletions[0].Asset.ID != "b" || request.PrerequisiteDeletions[1].Asset.ID != "a" || len(request.LifecycleImpacts) != 1 {
				t.Fatal(request, err)
			}
			for _, impact := range append(request.PrerequisiteDeletions, request.LifecycleImpacts...) {
				if impact.Asset.Normalized["version"] != "reviewed" || impact.ControllerID != root.ID || !impact.Delete {
					t.Fatal("worker review changed", impact)
				}
			}
		})
	}
}

func TestRouterSettlementProofBindsAllChildReview(t *testing.T) {
	root := routerMutationAsset("root", "Router", "one")
	child := routerMutationAsset("child", "RouterNat", "one")
	step := plan.CleanupTaskStep{ID: "root", AssetID: root.ID, Evidence: map[string]any{plan.EvidencePlannedAsset: root}}
	task := persistence.CleanupTaskAggregate{Steps: []plan.CleanupTaskStep{step}, ImpactItems: []plan.ImpactItem{{ID: "impact", AssetID: child.ID, DelegatedTo: step.ID, Evidence: map[string]any{plan.EvidencePlannedAsset: child}}}}
	before, err := sharedMutationDigest(execution.ExecutionAttempt{}, step, execution.ActionAttempt{}, task)
	if err != nil {
		t.Fatal(err)
	}
	child.Normalized = map[string]any{"nat-configuration": "changed"}
	task.ImpactItems[0].Evidence[plan.EvidencePlannedAsset] = child
	after, err := sharedMutationDigest(execution.ExecutionAttempt{}, step, execution.ActionAttempt{}, task)
	if err != nil || before == after {
		t.Fatal("child configuration did not invalidate Router proof", err)
	}
	task.Steps = append(task.Steps, plan.CleanupTaskStep{ID: "policy", AssetID: asset.AssetID("policy")})
	final, err := sharedMutationDigest(execution.ExecutionAttempt{}, step, execution.ActionAttempt{}, task)
	if err != nil || after == final {
		t.Fatal("prerequisites did not invalidate Router proof", err)
	}
}

func TestRouterComponentSettlementPreservesExistingProofEncoding(t *testing.T) {
	for _, kind := range []string{"RouterNat", "RoutePolicy", "NamedSet"} {
		value := routerMutationAsset("child", kind, "one")
		step := plan.CleanupTaskStep{ID: "child", AssetID: value.ID, Evidence: map[string]any{plan.EvidencePlannedAsset: value}}
		attempt := execution.ExecutionAttempt{ID: "attempt"}
		action := execution.ActionAttempt{ID: "action"}
		// Pre-Router proof encoding must still validate even if operation history expires.
		encoded, err := json.Marshal(struct {
			Attempt execution.ExecutionAttempt
			Step    plan.CleanupTaskStep
			Action  execution.ActionAttempt
		}{attempt, step, action})
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(encoded)
		actual, err := sharedMutationDigest(attempt, step, action, persistence.CleanupTaskAggregate{Steps: []plan.CleanupTaskStep{{ID: "unrelated"}}})
		if err != nil || actual != hex.EncodeToString(sum[:]) {
			t.Fatal(kind, "invalidated existing component proof", actual, err)
		}
	}
}
