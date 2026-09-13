package plan_test

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func prerequisiteAsset(id asset.AssetID) asset.Asset {
	value := actionable(id)
	value.Identity = asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: "Example/resources", NativeID: string(id) + "-native"}
	value.Normalized = map[string]any{"creation": string(id) + "-creation"}
	return value
}

func requiredDeletion(source, target asset.AssetID) graph.Relationship {
	return graph.Relationship{ID: graph.RelationshipID(string(source) + ":requires:" + string(target)), SourceAssetID: source, TargetAssetID: target, Type: graph.RelationshipDependsOn, Source: "provider:native-lifecycle", Confidence: 1, Evidence: map[string]any{
		graph.RelationshipEvidenceRequiredDeletion: true,
		graph.RelationshipEvidenceAuthority:        graph.AuthorityAuthoritative,
		graph.RelationshipEvidenceDeletionOrder:    graph.DeletionOrderTargetBeforeSource,
	}}
}

func TestRequiredDeletionCanPreserveIndependentSelection(t *testing.T) {
	for _, mode := range []string{"unselected", "selected", "explicitly-retained", "protected", "malformed", "default-automatic"} {
		t.Run(mode, func(t *testing.T) {
			relationship := requiredDeletion("source", "target")
			relationship.Evidence[graph.RelationshipEvidenceAutomaticSelection] = false
			input := plan.Input{Assets: []asset.Asset{prerequisiteAsset("source"), prerequisiteAsset("target")}, ResolvedAssetIDs: []asset.AssetID{"source"}, Relationships: []graph.Relationship{relationship}}
			if mode != "unselected" && mode != "default-automatic" {
				input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, "target")
			}
			switch mode {
			case "explicitly-retained":
				input.RequestOptions = map[asset.AssetID]map[string]any{"source": {"retain_all_resources": true}}
			case "protected":
				input.Protections = []plan.ProtectionPolicy{{AssetID: "target", Protected: true}}
			case "malformed":
				relationship.Evidence[graph.RelationshipEvidenceAutomaticSelection] = "false"
			case "default-automatic":
				delete(relationship.Evidence, graph.RelationshipEvidenceAutomaticSelection)
			}
			// The review decision must survive stored JSON, not Go-only types.
			payload, _ := json.Marshal(input)
			if err := json.Unmarshal(payload, &input); err != nil {
				t.Fatal(err)
			}
			result, err := plan.Solve(input)
			if err != nil {
				t.Fatal(err)
			}
			allowed := mode == "selected" || mode == "default-automatic"
			if allowed != (len(result.Blockers) == 0) {
				t.Fatal("independent prerequisite selection", result.Blockers)
			}
			if mode == "unselected" && stepForAsset(result.Steps, "target").Action != "" {
				t.Fatal("an independent resource was selected automatically")
			}
			if allowed {
				step := stepForAsset(result.Steps, "source")
				prerequisites, err := plan.RequiredDeletions(step)
				if err != nil || len(prerequisites) != 1 || prerequisites[0].AssetID != "target" || !slices.Contains(step.DependsOn, stepForAsset(result.Steps, "target").ID) {
					t.Fatal("selected prerequisite lost its frozen action", prerequisites, err)
				}
			}
		})
	}
}

func TestSharedRequiredDeletionDoesNotClaimMultipleOwners(t *testing.T) {
	owner := binding("retained-owner", "configuration", graph.OwnershipExclusive, graph.CleanupDirect, 1)
	owner.DirectCleanupAllowed = true
	view := binding("configuration", "view", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	view.Evidence = map[string]any{graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
	input := plan.Input{Assets: []asset.Asset{prerequisiteAsset("first"), prerequisiteAsset("second"), prerequisiteAsset("configuration"), prerequisiteAsset("retained-owner"), prerequisiteAsset("view")}, ResolvedAssetIDs: []asset.AssetID{"first", "second"}, LifecycleBindings: []graph.LifecycleBinding{owner, view}, Relationships: []graph.Relationship{requiredDeletion("first", "configuration"), requiredDeletion("second", "configuration")}}
	for _, selectedOwner := range []bool{false, true} {
		if selectedOwner {
			input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, "retained-owner")
		}
		result, err := plan.Solve(input)
		if err != nil || len(result.Blockers) != 0 || len(result.ImpactItems) != 1 {
			t.Fatalf("shared prerequisite failed %+v %v", result, err)
		}
		configuration := stepForAsset(result.Steps, "configuration")
		if configuration.Action != "delete" || result.ImpactItems[0].DelegatedTo != configuration.ID || result.ImpactItems[0].Expected != plan.ExpectedDelegatedDelete {
			t.Fatal("prerequisite lost independent action or owned view")
		}
		if selectedOwner != (stepForAsset(result.Steps, "retained-owner").Action == "delete") {
			t.Fatal("prerequisite changed owner's selection")
		}
		for _, id := range []asset.AssetID{"first", "second"} {
			step := stepForAsset(result.Steps, id)
			required, err := plan.RequiredDeletions(step)
			if err != nil || len(required) != 1 || required[0].AssetID != "configuration" || required[0].StepID != configuration.ID || !slices.Contains(step.DependsOn, configuration.ID) {
				t.Fatalf("missing shared prerequisite %+v %v", required, err)
			}
		}
		deletes := 0
		for _, step := range result.Steps {
			if step.AssetID == "configuration" && step.Action == "delete" {
				deletes++
			}
		}
		if deletes != 1 {
			t.Fatal("shared prerequisite duplicated")
		}
	}
}

func TestRequiredDeletionExpandsChainsAndBindsSnapshot(t *testing.T) {
	input := plan.Input{Assets: []asset.Asset{prerequisiteAsset("source"), prerequisiteAsset("first"), prerequisiteAsset("second")}, ResolvedAssetIDs: []asset.AssetID{"source"}, Relationships: []graph.Relationship{requiredDeletion("source", "first"), requiredDeletion("first", "second")}}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 3 {
		t.Fatalf("chain %+v %v", result, err)
	}
	if !slices.Equal(input.ResolvedAssetIDs, []asset.AssetID{"source"}) {
		t.Fatal("solver mutated caller selection")
	}
	for i, id := range []asset.AssetID{"second", "first", "source"} {
		if result.Steps[i].AssetID != id {
			t.Fatal("prerequisite chain ordered incorrectly")
		}
	}
	payload, _ := json.Marshal(result.Steps)
	var restored []plan.CleanupTaskStep
	if err := json.Unmarshal(payload, &restored); err != nil {
		t.Fatal(err)
	}
	for _, step := range restored {
		required, err := plan.RequiredDeletions(step)
		if err != nil {
			t.Fatal(err)
		}
		for _, prerequisite := range required {
			current := prerequisiteAsset(prerequisite.AssetID)
			closed := time.Now()
			current.ClosedAt, current.Normalized = &closed, map[string]any{"creation": "changed"}
			frozen, err := plan.PlannedAsset(stepForAsset(restored, prerequisite.AssetID).Evidence, current)
			if err != nil || frozen.ClosedAt != nil || frozen.Normalized["creation"] == "changed" {
				t.Fatal("serialized prerequisite lost frozen configuration")
			}
		}
	}
	for i := 0; i < 3; i++ {
		repeated, err := plan.Solve(input)
		if err != nil || repeated.SnapshotHash != result.SnapshotHash {
			t.Fatal("prerequisite snapshot nondeterministic")
		}
	}
	input.Assets[2].Normalized["creation"] = "recreated"
	changed, err := plan.Solve(input)
	if err != nil || changed.SnapshotHash == result.SnapshotHash {
		t.Fatal("required resource configuration omitted from snapshot")
	}
}

func TestRequiredDeletionPrecedesDelegatingActionAndRespectsRetention(t *testing.T) {
	member := binding("namespace", "entity", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
	member.Evidence = map[string]any{graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents: true}
	input := plan.Input{Assets: []asset.Asset{prerequisiteAsset("namespace"), prerequisiteAsset("entity"), prerequisiteAsset("configuration")}, ResolvedAssetIDs: []asset.AssetID{"namespace"}, LifecycleBindings: []graph.LifecycleBinding{member}, Relationships: []graph.Relationship{requiredDeletion("entity", "configuration")}}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 {
		t.Fatalf("delegated prerequisite %+v %v", result, err)
	}
	if result.Steps[0].AssetID != "configuration" || !slices.Contains(stepForAsset(result.Steps, "namespace").DependsOn, stepForAsset(result.Steps, "configuration").ID) {
		t.Fatal("required deletion waited only for post-deletion verification")
	}
	required, err := plan.RequiredDeletions(stepForAsset(result.Steps, "namespace"))
	if err != nil || len(required) != 1 {
		t.Fatal("delegating action did not receive prerequisite")
	}
	input.RequestOptions = map[asset.AssetID]map[string]any{"namespace": {"retain_resources": []string{"entity"}}}
	result, err = plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || stepForAsset(result.Steps, "configuration").Action != "" {
		t.Fatalf("retained source caused prerequisite deletion %+v %v", result, err)
	}
}

func TestRequiredDeletionRejectsUnsafeEvidenceAndTargets(t *testing.T) {
	for _, mode := range []string{"missing", "closed", "not-actionable", "protected", "provider", "connection", "partition", "empty-connection", "inferred", "low-confidence", "invalid-confidence", "missing-source", "wrong-order", "wrong-type", "malformed-flag", "self", "cycle", "unselected-owner", "delegated-to-source"} {
		t.Run(mode, func(t *testing.T) {
			input := plan.Input{Assets: []asset.Asset{prerequisiteAsset("source"), prerequisiteAsset("target"), prerequisiteAsset("owner")}, ResolvedAssetIDs: []asset.AssetID{"source"}, Relationships: []graph.Relationship{requiredDeletion("source", "target")}}
			relationship := &input.Relationships[0]
			switch mode {
			case "missing":
				input.Assets = input.Assets[:1]
			case "closed":
				closed := time.Now()
				input.Assets[1].ClosedAt = &closed
			case "not-actionable":
				input.Assets[1].Capabilities = nil
			case "protected":
				input.Protections = []plan.ProtectionPolicy{{AssetID: "target", Protected: true}}
			case "provider":
				input.Assets[1].Identity.Provider = asset.ProviderGCP
			case "connection":
				input.Assets[1].Identity.ConnectionID = "other"
			case "partition":
				input.Assets[1].Identity.Partition = "other"
			case "empty-connection":
				input.Assets[0].Identity.ConnectionID, input.Assets[1].Identity.ConnectionID = "", ""
			case "inferred":
				relationship.Evidence[graph.RelationshipEvidenceAuthority] = graph.AuthorityInferred
			case "low-confidence":
				relationship.Confidence = 0.5
			case "invalid-confidence":
				relationship.Confidence = 2
			case "missing-source":
				relationship.Source = ""
			case "wrong-order":
				delete(relationship.Evidence, graph.RelationshipEvidenceDeletionOrder)
			case "wrong-type":
				relationship.Type = graph.RelationshipConnectedTo
			case "malformed-flag":
				relationship.Evidence[graph.RelationshipEvidenceRequiredDeletion] = "true"
			case "self":
				relationship.TargetAssetID = "source"
			case "cycle":
				input.Relationships = append(input.Relationships, requiredDeletion("target", "source"))
			case "unselected-owner":
				input.LifecycleBindings = []graph.LifecycleBinding{binding("owner", "target", graph.OwnershipExclusive, graph.CleanupDelegate, 1)}
			case "delegated-to-source":
				input.LifecycleBindings = []graph.LifecycleBinding{binding("source", "target", graph.OwnershipExclusive, graph.CleanupDelegate, 1)}
			}
			result, err := plan.Solve(input)
			if err != nil || len(result.Blockers) == 0 {
				t.Fatalf("unsafe prerequisite accepted %+v %v", result, err)
			}
			if stepForAsset(result.Steps, "owner").Action != "" {
				t.Fatal("required deletion silently promoted to unselected owner")
			}
		})
	}
}

func TestRequiredDeletionHonorsAllRetentionOptions(t *testing.T) {
	for _, options := range []map[string]any{{"retain_all_resources": true}, {"retain_resources": []string{"target"}}, {"retain_resources": []any{"target-native"}}, {"delete_options": []any{map[string]any{"resource_type": "Example/resources", "delete_mode": "retain"}}}} {
		result, err := plan.Solve(plan.Input{Assets: []asset.Asset{prerequisiteAsset("source"), prerequisiteAsset("target")}, ResolvedAssetIDs: []asset.AssetID{"source"}, Relationships: []graph.Relationship{requiredDeletion("source", "target")}, RequestOptions: map[asset.AssetID]map[string]any{"source": options}})
		if err != nil || len(result.Blockers) == 0 || stepForAsset(result.Steps, "target").Action != "" {
			t.Fatalf("retained prerequisite deleted %+v %v", result, err)
		}
	}
}

func TestRequiredDeletionMetadataRejectsMalformedSnapshot(t *testing.T) {
	for _, raw := range []any{nil, []any{}, "bad", []any{map[string]any{"asset_id": "source", "step_id": "target-step"}}, []any{map[string]any{"asset_id": "target", "step_id": "other-step"}}, []any{map[string]any{"asset_id": "target", "step_id": "target-step"}, map[string]any{"asset_id": "target", "step_id": "target-step"}}} {
		step := plan.CleanupTaskStep{AssetID: "source", ID: "source-step", DependsOn: []plan.StepID{"target-step"}, Evidence: map[string]any{plan.EvidenceRequiredDeletions: raw}}
		if _, err := plan.RequiredDeletions(step); err == nil {
			t.Fatalf("malformed prerequisites accepted %+v", raw)
		}
	}
}

func TestRequiredDeletionRetainsResourcesRequestedByAncestorOfDirectStep(t *testing.T) {
	input := plan.Input{Assets: []asset.Asset{prerequisiteAsset("root"), prerequisiteAsset("child"), prerequisiteAsset("target")}, ResolvedAssetIDs: []asset.AssetID{"root"}, LifecycleBindings: []graph.LifecycleBinding{binding("root", "child", graph.OwnershipExclusive, graph.CleanupDirect, 1)}, Relationships: []graph.Relationship{requiredDeletion("child", "target")}, RequestOptions: map[asset.AssetID]map[string]any{"root": {"retain_resources": []string{"target"}}}}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) == 0 || stepForAsset(result.Steps, "target").Action != "" {
		t.Fatalf("ancestor retention lost through direct step %+v %v", result, err)
	}
}

func TestRequiredDeletionCanBeCoveredOnlyByDeclaredCommonCascade(t *testing.T) {
	for _, mode := range []string{"cascade", "direct", "undeclared", "wrong-controller", "retained", "protected", "inferred", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			outer := binding("profile", "endpoint", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
			source := binding("profile", "domain", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
			target := binding("endpoint", "route", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
			for _, value := range []*graph.LifecycleBinding{&outer, &source, &target} {
				value.DirectCleanupAllowed = true
				value.Evidence = map[string]any{graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true, graph.LifecycleEvidenceControllerDeleteGuaranteed: true}
			}
			relation := requiredDeletion("domain", "route")
			relation.Evidence[graph.RelationshipEvidenceDeletionCascadeControllers] = map[string]any{"profile": true}
			input := plan.Input{Assets: []asset.Asset{prerequisiteAsset("profile"), prerequisiteAsset("endpoint"), prerequisiteAsset("domain"), prerequisiteAsset("route")}, ResolvedAssetIDs: []asset.AssetID{"profile"}, LifecycleBindings: []graph.LifecycleBinding{outer, source, target}, Relationships: []graph.Relationship{relation}}
			switch mode {
			case "direct":
				input.ResolvedAssetIDs = []asset.AssetID{"domain"}
			case "undeclared":
				delete(relation.Evidence, graph.RelationshipEvidenceDeletionCascadeControllers)
			case "wrong-controller":
				relation.Evidence[graph.RelationshipEvidenceDeletionCascadeControllers] = map[string]any{"endpoint": true}
			case "retained":
				input.RequestOptions = map[asset.AssetID]map[string]any{"profile": {"retain_resources": []string{"route"}}}
			case "protected":
				input.Protections = []plan.ProtectionPolicy{{AssetID: "route", Protected: true}}
			case "inferred":
				relation.Evidence[graph.RelationshipEvidenceAuthority] = graph.AuthorityInferred
			case "malformed":
				relation.Evidence[graph.RelationshipEvidenceDeletionCascadeControllers] = map[string]any{"profile": "true"}
			}
			result, err := plan.Solve(input)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "cascade" {
				if len(result.Blockers) != 0 || len(result.Steps) != 1 || len(result.ImpactItems) != 3 {
					t.Fatalf("common cascade not preserved: steps=%d impacts=%d blockers=%+v", len(result.Steps), len(result.ImpactItems), result.Blockers)
				}
				if required, err := plan.RequiredDeletions(result.Steps[0]); err != nil || len(required) != 0 {
					t.Fatal("common cascade gained a self prerequisite")
				}
			} else if mode == "direct" {
				if len(result.Blockers) != 0 || len(result.Steps) != 2 || result.Steps[0].AssetID != "route" || result.Steps[1].AssetID != "domain" {
					t.Fatalf("independent prerequisite lost: %+v", result.Blockers)
				}
				if stepForAsset(result.Steps, "profile").Action != "" {
					t.Fatal("unselected profile promoted")
				}
			} else if len(result.Blockers) == 0 {
				t.Fatal("unsafe common cascade accepted")
			}
		})
	}
}

func TestRequiredDeletionControllerCoversOnlyReviewedPrerequisite(t *testing.T) {
	for _, mode := range []string{"cascade", "undeclared", "wrong-controller", "retained", "protected", "inferred", "malformed", "unowned", "foreign", "direct", "controller-unselected"} {
		t.Run(mode, func(t *testing.T) {
			member := binding("controller", "rule", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
			member.DirectCleanupAllowed = true
			member.Evidence = map[string]any{graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true, graph.LifecycleEvidenceControllerDeleteGuaranteed: true}
			relation := requiredDeletion("controller", "rule")
			relation.Evidence[graph.RelationshipEvidenceAutomaticSelection] = false
			relation.Evidence[graph.RelationshipEvidenceDeletionCascadeControllers] = map[string]any{"controller": true}
			input := plan.Input{Assets: []asset.Asset{prerequisiteAsset("controller"), prerequisiteAsset("rule")}, ResolvedAssetIDs: []asset.AssetID{"controller"}, LifecycleBindings: []graph.LifecycleBinding{member}, Relationships: []graph.Relationship{relation}}
			switch mode {
			case "undeclared":
				delete(relation.Evidence, graph.RelationshipEvidenceDeletionCascadeControllers)
			case "wrong-controller":
				relation.Evidence[graph.RelationshipEvidenceDeletionCascadeControllers] = map[string]any{"another": true}
			case "retained":
				input.RequestOptions = map[asset.AssetID]map[string]any{"controller": {"retain_resources": []string{"rule"}}}
			case "protected":
				input.Protections = []plan.ProtectionPolicy{{AssetID: "rule", Protected: true}}
			case "inferred":
				input.LifecycleBindings[0].Authority = graph.AuthorityInferred
			case "malformed":
				relation.Evidence[graph.RelationshipEvidenceDeletionCascadeControllers] = map[string]any{"controller": "true"}
			case "unowned":
				input.LifecycleBindings = nil
			case "foreign":
				input.Assets[1].Identity.ConnectionID = "another"
			case "direct":
				input.LifecycleBindings[0].CleanupPolicy = graph.CleanupDirect
			case "controller-unselected":
				input.ResolvedAssetIDs = []asset.AssetID{"rule"}
			}
			// The declaration and ownership must retain their meaning in a stored
			// graph as well as in memory; no inferred bool or typed-map shortcut.
			encoded, err := json.Marshal(input)
			if err != nil || json.Unmarshal(encoded, &input) != nil {
				t.Fatal("invalid recovered graph", err)
			}
			result, err := plan.Solve(input)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "cascade":
				if len(result.Blockers) != 0 || len(result.Steps) != 1 || result.Steps[0].AssetID != "controller" || len(result.ImpactItems) != 1 || result.ImpactItems[0].Expected != plan.ExpectedDelegatedDelete {
					t.Fatalf("controller cascade lost its reviewed prerequisite: %+v", result)
				}
				if required, err := plan.RequiredDeletions(result.Steps[0]); err != nil || len(required) != 0 {
					t.Fatal("controller cascade gained a self prerequisite", required, err)
				}
			case "direct":
				if len(result.Blockers) != 0 || len(result.Steps) != 2 || result.Steps[0].AssetID != "rule" || result.Steps[1].AssetID != "controller" {
					t.Fatalf("independent prerequisite lost its order: %+v", result)
				}
			case "controller-unselected":
				if len(result.Blockers) != 0 || len(result.Steps) != 1 || result.Steps[0].AssetID != "rule" {
					t.Fatalf("unselected controller acquired a delete: %+v", result)
				}
			default:
				if len(result.Blockers) == 0 {
					t.Fatal("declaration bypassed prerequisite selection or authority")
				}
			}
		})
	}
}

func TestRequiredDeletionCanUseDeclaredVerifiedManagedImpact(t *testing.T) {
	for _, mode := range []string{"verified", "undeclared", "wrong-controller", "unverified", "unselected-controller", "retained", "protected", "inferred", "foreign", "direct"} {
		t.Run(mode, func(t *testing.T) {
			member := binding("controller", "disk", graph.OwnershipExclusive, graph.CleanupDelegate, 1)
			member.Evidence = map[string]any{graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
			relation := requiredDeletion("storage", "disk")
			relation.Evidence[graph.RelationshipEvidenceAutomaticSelection] = false
			relation.Evidence[graph.RelationshipEvidenceDeletionCascadeControllers] = map[string]any{"controller": true}
			input := plan.Input{Assets: []asset.Asset{prerequisiteAsset("storage"), prerequisiteAsset("disk"), prerequisiteAsset("controller")}, ResolvedAssetIDs: []asset.AssetID{"storage", "controller"}, LifecycleBindings: []graph.LifecycleBinding{member}, Relationships: []graph.Relationship{relation}}
			switch mode {
			case "undeclared":
				delete(relation.Evidence, graph.RelationshipEvidenceDeletionCascadeControllers)
			case "wrong-controller":
				relation.Evidence[graph.RelationshipEvidenceDeletionCascadeControllers] = map[string]any{"other": true}
			case "unverified":
				delete(member.Evidence, graph.LifecycleEvidenceControllerVerifiesManagedAbsence)
			case "unselected-controller":
				input.ResolvedAssetIDs = []asset.AssetID{"storage"}
			case "retained":
				input.RequestOptions = map[asset.AssetID]map[string]any{"storage": {"retain_resources": []string{"disk"}}}
			case "protected":
				input.Protections = []plan.ProtectionPolicy{{AssetID: "disk", Protected: true}}
			case "inferred":
				input.LifecycleBindings[0].Authority = graph.AuthorityInferred
			case "foreign":
				input.Assets[1].Identity.ConnectionID = "other"
			case "direct":
				input.LifecycleBindings[0].CleanupPolicy = graph.CleanupDirect
			}
			data, _ := json.Marshal(input)
			if err := json.Unmarshal(data, &input); err != nil {
				t.Fatal(err)
			}
			result, err := plan.Solve(input)
			allowed := mode == "verified" || mode == "direct"
			if err != nil || (len(result.Blockers) == 0) != allowed {
				t.Fatal("managed prerequisite authority", mode, err, result.Blockers)
			}
			if mode == "unselected-controller" && stepForAsset(result.Steps, "controller").ID != "" {
				t.Fatal("requirement selected its owning controller")
			}
			if !allowed {
				return
			}
			required, err := plan.RequiredDeletions(stepForAsset(result.Steps, "storage"))
			if err != nil || len(required) != 1 || required[0].AssetID != "disk" {
				t.Fatal("missing frozen managed prerequisite", required, err)
			}
			if mode == "verified" {
				if len(result.Steps) != 2 || len(result.ImpactItems) != 1 || result.Steps[0].AssetID != "controller" || required[0].ControllerAssetID != "controller" || required[0].StepID != result.Steps[0].ID {
					t.Fatal("managed prerequisite became native disk DELETE", result)
				}
			} else if required[0].ControllerAssetID != "" || len(result.Steps) != 3 {
				t.Fatal("direct requirement became a delegated impact", result)
			}
		})
	}
	for _, controller := range []asset.AssetID{"source", "target"} {
		step := plan.CleanupTaskStep{ID: "source-step", AssetID: "source", DependsOn: []plan.StepID{"owner-step"}, Evidence: map[string]any{plan.EvidenceRequiredDeletions: []plan.RequiredDeletion{{AssetID: "target", StepID: "owner-step", ControllerAssetID: controller}}}}
		if _, err := plan.RequiredDeletions(step); err == nil {
			t.Fatal("self/target controller accepted", controller)
		}
	}
}
