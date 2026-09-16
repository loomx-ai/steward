package plan_test

import (
	"encoding/json"
	"reflect"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func nativeEffect(controller, managed asset.AssetID) graph.LifecycleBinding {
	b := binding(controller, managed, graph.OwnershipShared, graph.CleanupDelegate, 1)
	b.Evidence = map[string]any{graph.LifecycleEvidenceNativeDeleteEffect: true, graph.LifecycleEvidenceControllerDeleteGuaranteed: true, graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true, "delete_by_default": true, "retention_supported": true}
	return b
}
func TestNativeDeleteEffectsPreserveOwnershipAndChoices(t *testing.T) {
	for _, mode := range []string{"delete", "retain", "default_retain", "legacy_shared", "reference", "protected", "weak", "inferred", "no_guarantee", "no_verification", "unknown_owner", "wrong_policy", "unselected_controller", "both_selected", "other_controller", "competing_actions", "cycle"} {
		t.Run(mode, func(t *testing.T) {
			b := nativeEffect("stack", "resource")
			input := plan.Input{Assets: []asset.Asset{actionable("stack"), actionable("resource"), actionable("other")}, ResolvedAssetIDs: []asset.AssetID{"stack"}}
			switch mode {
			case "retain":
				input.RequestOptions = map[asset.AssetID]map[string]any{"stack": {"retain_all_resources": true}}
			case "default_retain":
				b.Evidence["delete_by_default"] = false
			case "legacy_shared":
				delete(b.Evidence, graph.LifecycleEvidenceNativeDeleteEffect)
			case "reference":
				b.Ownership = graph.OwnershipReferenced
			case "protected":
				input.Protections = []plan.ProtectionPolicy{{AssetID: "resource", Protected: true}}
			case "weak":
				b.Confidence = .5
			case "inferred":
				b.Authority = graph.AuthorityInferred
			case "no_guarantee":
				delete(b.Evidence, graph.LifecycleEvidenceControllerDeleteGuaranteed)
			case "no_verification":
				delete(b.Evidence, graph.LifecycleEvidenceControllerVerifiesManagedAbsence)
			case "unknown_owner":
				b.Ownership = graph.OwnershipUnknown
			case "wrong_policy":
				b.CleanupPolicy = graph.CleanupDirect
			case "unselected_controller":
				input.ResolvedAssetIDs = []asset.AssetID{"resource"}
			case "both_selected":
				input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, "resource")
			}
			input.LifecycleBindings = []graph.LifecycleBinding{b}
			if mode == "other_controller" || mode == "competing_actions" {
				input.LifecycleBindings = append(input.LifecycleBindings, nativeEffect("other", "resource"))
			}
			if mode == "competing_actions" {
				input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, "other")
			}
			if mode == "cycle" {
				input.LifecycleBindings = append(input.LifecycleBindings, nativeEffect("resource", "stack"))
			}
			// Persistence does not promote native effects to exclusive ownership.
			before, _ := json.Marshal(input)
			var restored plan.Input
			if err := json.Unmarshal(before, &restored); err != nil {
				t.Fatal(err)
			}
			result, err := plan.Solve(restored)
			after, _ := json.Marshal(restored)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("input changed", err)
			}
			ownership, err := graph.ResolveAuthority("resource", restored.LifecycleBindings)
			if err != nil || ownership.ControllerAssetID != "resource" || len(ownership.Chain) != 0 {
				t.Fatal("native effect changed global ownership", ownership, err)
			}
			blocked := mode == "protected" || mode == "weak" || mode == "inferred" || mode == "no_guarantee" || mode == "no_verification" || mode == "unknown_owner" || mode == "wrong_policy" || mode == "competing_actions" || mode == "cycle"
			if blocked {
				if len(result.Blockers) == 0 {
					t.Fatal("missing blocker", result)
				}
				return
			}
			if len(result.Blockers) != 0 || len(result.Steps) != 1 {
				t.Fatal(result)
			}
			if mode == "unselected_controller" {
				if result.Steps[0].AssetID != "resource" || len(result.ImpactItems) != 0 {
					t.Fatal(result)
				}
				return
			}
			if len(result.ImpactItems) != 1 || result.Steps[0].AssetID != "stack" {
				t.Fatal(result)
			}
			impact := result.ImpactItems[0]
			expected := plan.ExpectedDelegatedDelete
			switch mode {
			case "retain":
				expected = plan.ExpectedRetainExplicit
			case "default_retain":
				expected = plan.ExpectedProviderDefaultRetain
			case "legacy_shared":
				expected = plan.ExpectedRetainShared
			}
			if impact.Expected != expected || impact.Ownership != b.Ownership || impact.ControllerID != "stack" || impact.DelegatedTo != result.Steps[0].ID || impact.MayContinueBilling != (expected != plan.ExpectedDelegatedDelete) {
				t.Fatal(impact)
			}
			if mode == "other_controller" {
				if len(result.Warnings) != 1 || result.Warnings[0].Code != plan.WarningNativeDeleteAffectsController || result.Warnings[0].ControllerID != "other" {
					t.Fatal(result.Warnings)
				}
			}
		})
	}
}

func TestNativeDeleteEffectsRouteFlatMembersThroughProductController(t *testing.T) {
	parent := nativeEffect("stack", "vm")
	flatChild := nativeEffect("stack", "extension")
	product := binding("vm", "extension", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	product.Evidence = map[string]any{graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
	input := plan.Input{Assets: []asset.Asset{actionable("stack"), actionable("vm"), actionable("extension")}, ResolvedAssetIDs: []asset.AssetID{"stack", "extension"}, LifecycleBindings: []graph.LifecycleBinding{flatChild, product, parent}}
	for _, retain := range []bool{false, true} {
		if retain {
			input.RequestOptions = map[asset.AssetID]map[string]any{"stack": {"retain_all_resources": true}}
		}
		result, err := plan.Solve(input)
		if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 1 || len(result.ImpactItems) != 2 {
			t.Fatal(result, err)
		}
		effects := impactByAsset(result.ImpactItems)
		if effects["vm"].Ownership != graph.OwnershipShared || effects["extension"].Ownership != graph.OwnershipExclusive || effects["extension"].ControllerID != "vm" || effects["extension"].DelegatedTo != result.Steps[0].ID {
			t.Fatal(effects)
		}
		for _, impact := range effects {
			want := plan.ExpectedDelegatedDelete
			if retain {
				want = plan.ExpectedRetainExplicit
			}
			if impact.Expected != want {
				t.Fatal(impact)
			}
		}
		authority, err := graph.ResolveAuthority("extension", input.LifecycleBindings)
		if err != nil || authority.ControllerAssetID != "vm" {
			t.Fatal(authority, err)
		}
	}
}

func TestNativeDeleteEffectsIndependentRetainedParent(t *testing.T) {
	parent := nativeEffect("stack", "group")
	parent.Evidence["resource_type"] = "groups"
	child := nativeEffect("stack", "vm")
	child.Evidence["resource_type"] = "vms"
	product := binding("group", "vm", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	product.Evidence = map[string]any{graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
	for _, explicit := range []bool{false, true} {
		choices := []any{map[string]any{"resource_type": "groups", "delete_mode": "retain"}}
		if explicit {
			choices = append(choices, map[string]any{"resource_type": "vms", "delete_mode": "delete"})
		}
		input := plan.Input{Assets: []asset.Asset{actionable("stack"), actionable("group"), actionable("vm")}, ResolvedAssetIDs: []asset.AssetID{"stack"}, LifecycleBindings: []graph.LifecycleBinding{parent, child, product}, RequestOptions: map[asset.AssetID]map[string]any{"stack": {"delete_options": choices}}}
		result, err := plan.Solve(input)
		if err != nil || len(result.Blockers) != 0 || len(result.ImpactItems) != 2 || len(result.Steps) != 1 {
			t.Fatal(result, err)
		}
		effects := impactByAsset(result.ImpactItems)
		if effects["group"].Expected != plan.ExpectedRetainExplicit {
			t.Fatal(effects)
		}
		expected := plan.ExpectedRetainExplicit
		controller := asset.AssetID("group")
		if explicit {
			expected = plan.ExpectedDelegatedDelete
			controller = "stack"
		}
		if effects["vm"].Expected != expected || effects["vm"].ControllerID != controller {
			t.Fatal(effects)
		}
		verified := plan.VerifiedManagedDeletion(effects["vm"], "stack", result.Steps[0].ID, result.ImpactItems)
		if verified != explicit {
			t.Fatal("native execution verification mismatch", verified, effects)
		}
		if explicit {
			changed := effects["vm"]
			delete(changed.Evidence, graph.LifecycleEvidenceControllerVerifiesManagedAbsence)
			if plan.VerifiedManagedDeletion(changed, "stack", result.Steps[0].ID, result.ImpactItems) {
				t.Fatal("verification accepted missing evidence")
			}
		}
	}
}

func TestNativeDeleteEffectsBindUnselectedControllerEvidence(t *testing.T) {
	input := plan.Input{Assets: []asset.Asset{actionable("stack"), actionable("other"), actionable("resource")}, ResolvedAssetIDs: []asset.AssetID{"stack"}, LifecycleBindings: []graph.LifecycleBinding{nativeEffect("stack", "resource"), nativeEffect("other", "resource")}}
	before, err := plan.Solve(input)
	if err != nil || len(before.Blockers) != 0 {
		t.Fatal(before, err)
	}
	input.LifecycleBindings[1].Evidence["configuration"] = "changed-other-stack"
	after, err := plan.Solve(input)
	if err != nil || before.SnapshotHash == after.SnapshotHash {
		t.Fatal("unselected native controller dropped from approval snapshot", err)
	}
}

func TestNativeDeleteEffectsPreserveIndependentProductPrerequisites(t *testing.T) {
	bindings := []graph.LifecycleBinding{nativeEffect("group", "controller"), nativeEffect("group", "source"), nativeEffect("group", "target"), nativeEffect("group", "gate")}
	for _, id := range []asset.AssetID{"source", "target"} {
		child := binding("controller", id, graph.OwnershipExclusive, graph.CleanupDirect, 1)
		child.DirectCleanupAllowed = true
		bindings = append(bindings, child)
	}
	gate := binding("source", "gate", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	gate.Evidence = map[string]any{graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
	bindings = append(bindings, gate)
	required := requiredDeletion("source", "target")
	required.Evidence[graph.RelationshipEvidenceAutomaticSelection] = false
	input := plan.Input{Assets: []asset.Asset{prerequisiteAsset("group"), prerequisiteAsset("controller"), prerequisiteAsset("source"), prerequisiteAsset("target"), prerequisiteAsset("gate")}, ResolvedAssetIDs: []asset.AssetID{"group"}, LifecycleBindings: bindings, Relationships: []graph.Relationship{required}}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 3 || len(result.ImpactItems) != 2 {
		t.Fatal("native group replaced independent product execution", len(result.Steps), len(result.ImpactItems), result.Blockers, err)
	}
	source, target := stepForAsset(result.Steps, "source"), stepForAsset(result.Steps, "target")
	prerequisites, err := plan.RequiredDeletions(source)
	if err != nil || len(prerequisites) != 1 || prerequisites[0].AssetID != "target" || prerequisites[0].StepID != target.ID || prerequisites[0].ControllerAssetID != "" {
		t.Fatal("independent ordering lost", prerequisites, err)
	}
	effects := impactByAsset(result.ImpactItems)
	if effects["gate"].DelegatedTo != source.ID || effects["controller"].DelegatedTo != stepForAsset(result.Steps, "group").ID {
		t.Fatal("native child controller changed", effects)
	}
}
