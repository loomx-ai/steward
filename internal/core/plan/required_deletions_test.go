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
