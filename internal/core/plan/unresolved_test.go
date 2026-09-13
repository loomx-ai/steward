package plan_test

import (
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func TestUnresolvedCleanupBoundaryFollowsDeletionOwner(t *testing.T) {
	for _, mode := range []string{"selected", "delegated", "direct", "retained", "unrelated", "informational", "foreign-connection", "foreign-provider"} {
		t.Run(mode, func(t *testing.T) {
			root, child := actionable("root"), actionable("child")
			reference := graph.UnresolvedReference{Provider: child.Identity.Provider, ConnectionID: child.Identity.ConnectionID, ControllerID: child.ID, NativeType: "missing.kind", NativeID: "missing-child", Relationship: graph.RelationshipAttachedTo, BlocksCleanup: true, GraphRevision: "revision-one"}
			input := plan.Input{Assets: []asset.Asset{root, child}, ResolvedAssetIDs: []asset.AssetID{child.ID}, Unresolved: []graph.UnresolvedReference{reference}}
			switch mode {
			case "delegated", "direct", "retained":
				policy := graph.CleanupDelegate
				if mode == "direct" {
					policy = graph.CleanupDirect
				}
				if mode == "retained" {
					policy = graph.CleanupRetain
				}
				input.LifecycleBindings = []graph.LifecycleBinding{binding("root", "child", graph.OwnershipExclusive, policy, 1)}
				input.ResolvedAssetIDs = []asset.AssetID{root.ID}
			case "unrelated":
				input.ResolvedAssetIDs = []asset.AssetID{root.ID}
			case "informational":
				input.Unresolved[0].BlocksCleanup = false
			case "foreign-connection":
				input.Unresolved[0].ConnectionID = "another-connection"
			case "foreign-provider":
				input.Unresolved[0].Provider = "another-provider"
			}
			result, err := plan.Solve(input)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, blocker := range result.Blockers {
				if blocker.Code == plan.BlockUnresolvedCleanup {
					found = true
					owner := child.ID
					if mode == "delegated" {
						owner = root.ID
					}
					if blocker.AssetID != owner || blocker.ControllerID != child.ID || blocker.Evidence["native_id"] != reference.NativeID {
						t.Fatal("wrong incomplete boundary", blocker)
					}
				}
			}
			expected := mode == "selected" || mode == "delegated" || mode == "direct"
			if found != expected {
				t.Fatal("incorrect unresolved cleanup barrier", mode, result.Blockers)
			}
		})
	}
}

func TestUnresolvedDiagnosticsAreBoundDeterministically(t *testing.T) {
	value := actionable("controller")
	input := plan.Input{Assets: []asset.Asset{value}, ResolvedAssetIDs: []asset.AssetID{value.ID}}
	before, err := plan.Solve(input)
	if err != nil {
		t.Fatal(err)
	}
	ref := graph.UnresolvedReference{Provider: value.Identity.Provider, ConnectionID: value.Identity.ConnectionID, ControllerID: value.ID, NativeType: "child.kind", NativeID: "one", BlocksCleanup: true}
	other := ref
	other.NativeID = "two"
	input.Unresolved = []graph.UnresolvedReference{ref, other}
	first, err := plan.Solve(input)
	if err != nil || first.SnapshotHash == before.SnapshotHash {
		t.Fatal("new diagnostics did not invalidate frozen review", err)
	}
	input.Unresolved = []graph.UnresolvedReference{other, ref}
	second, err := plan.Solve(input)
	if err != nil || second.SnapshotHash != first.SnapshotHash {
		t.Fatal("diagnostic order changed snapshot", err)
	}
	input.Unresolved[0].BlocksCleanup = false
	changed, err := plan.Solve(input)
	if err != nil || changed.SnapshotHash == first.SnapshotHash {
		t.Fatal("blocking policy not bound", err)
	}
}
