package plan

import (
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const EvidenceRequiredDeletions = "required_deletions"

// RequiredDeletion refers to a frozen direct asset or a reviewed managed impact.
// ControllerAssetID is set only when that prerequisite step verifies its absence.
type RequiredDeletion struct {
	AssetID           asset.AssetID `json:"asset_id"`
	StepID            StepID        `json:"step_id"`
	ControllerAssetID asset.AssetID `json:"controller_asset_id,omitempty"`
}

func RequiredDeletions(step CleanupTaskStep) ([]RequiredDeletion, error) {
	raw, present := step.Evidence[EvidenceRequiredDeletions]
	if !present {
		return nil, nil
	}
	payload, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var prerequisites []RequiredDeletion
	if err := json.Unmarshal(payload, &prerequisites); err != nil {
		return nil, err
	}
	if len(prerequisites) == 0 {
		return nil, fmt.Errorf("reviewed cleanup prerequisites are empty")
	}
	seen := map[asset.AssetID]bool{}
	for _, prerequisite := range prerequisites {
		if prerequisite.AssetID == "" || prerequisite.AssetID == step.AssetID || prerequisite.StepID == "" || prerequisite.StepID == step.ID || seen[prerequisite.AssetID] || !slices.Contains(step.DependsOn, prerequisite.StepID) {
			return nil, fmt.Errorf("invalid reviewed cleanup prerequisite")
		}
		if prerequisite.ControllerAssetID != "" && (prerequisite.ControllerAssetID == prerequisite.AssetID || prerequisite.ControllerAssetID == step.AssetID) {
			return nil, fmt.Errorf("invalid managed cleanup prerequisite controller")
		}
		seen[prerequisite.AssetID] = true
	}
	return prerequisites, nil
}

func Solve(input Input) (Result, error) {
	var requirements []graph.Relationship
	for _, relationship := range activeRelationships(input.Relationships) {
		if value, present := relationship.Evidence[graph.RelationshipEvidenceRequiredDeletion]; present && value != false {
			requirements = append(requirements, relationship)
		}
	}
	if len(requirements) == 0 {
		return solveOnce(input)
	}
	assets, err := indexAssets(input.Assets)
	if err != nil {
		return Result{}, err
	}
	input.ResolvedAssetIDs = slices.Clone(input.ResolvedAssetIDs)
	selected := map[asset.AssetID]bool{}
	for _, id := range input.ResolvedAssetIDs {
		selected[id] = true
	}
	// Reusing the normal solver preserves retention and lifecycle authority
	// semantics. Each pass adds at least one previously unselected asset.
	for pass := 0; pass <= len(assets); pass++ {
		result, err := solveOnce(input)
		if err != nil {
			return Result{}, err
		}
		steps := map[StepID]CleanupTaskStep{}
		actionSteps := map[asset.AssetID]CleanupTaskStep{}
		deleting := map[asset.AssetID]StepID{}
		retained := map[asset.AssetID]bool{}
		for _, step := range result.Steps {
			steps[step.ID] = step
			if step.Action == "delete" {
				deleting[step.AssetID] = step.ID
				actionSteps[step.AssetID] = step
			}
		}
		for _, impact := range result.ImpactItems {
			if impact.Expected == ExpectedDelegatedDelete && steps[impact.DelegatedTo].Action == "delete" {
				deleting[impact.AssetID] = impact.DelegatedTo
			} else if impact.Expected != ExpectedDelegatedDelete {
				retained[impact.AssetID] = true
			}
		}
		blockers := newBlockerSet()
		for _, blocker := range result.Blockers {
			blockers.add(blocker)
		}
		byStep := map[StepID]map[asset.AssetID]RequiredDeletion{}
		added := false
		for _, relationship := range requirements {
			sourceStepID := deleting[relationship.SourceAssetID]
			if sourceStepID == "" {
				continue // A retained or skipped resource requires no mutation.
			}
			source := assets[relationship.SourceAssetID]
			target, present := assets[relationship.TargetAssetID]
			blocked := func(code BlockCode, message string) {
				blockers.add(Blocker{Code: code, AssetID: relationship.TargetAssetID, ControllerID: steps[sourceStepID].AssetID, Message: message, Evidence: map[string]any{"relationship_id": relationship.ID, "required_by": relationship.SourceAssetID}})
			}
			if relationship.Evidence[graph.RelationshipEvidenceRequiredDeletion] != true || fmt.Sprint(relationship.Evidence[graph.RelationshipEvidenceAuthority]) != string(graph.AuthorityAuthoritative) || relationship.Type != graph.RelationshipDependsOn || relationship.Evidence[graph.RelationshipEvidenceDeletionOrder] != graph.DeletionOrderTargetBeforeSource || strings.TrimSpace(relationship.Source) == "" || !(relationship.Confidence >= graph.ExecutableConfidence && relationship.Confidence <= 1) {
				blocked(BlockLifecycleAuthority, "required cleanup lacks authoritative provider evidence")
				continue
			}
			automaticSelection := true
			if value, present := relationship.Evidence[graph.RelationshipEvidenceAutomaticSelection]; present {
				var valid bool
				automaticSelection, valid = value.(bool)
				if !valid {
					blocked(BlockLifecycleAuthority, "required cleanup has invalid selection evidence")
					continue
				}
			}
			if !present {
				blocked(BlockAssetMissing, "required cleanup resource is missing from the planning snapshot")
				continue
			}
			if target.ClosedAt != nil {
				blocked(BlockAssetClosed, "required cleanup resource is already closed")
				continue
			}
			if source.ID == target.ID || source.Identity.Provider == "" || source.Identity.ConnectionID == "" || source.Identity.Provider != target.Identity.Provider || source.Identity.ConnectionID != target.Identity.ConnectionID || source.Identity.Partition != target.Identity.Partition {
				blocked(BlockCrossScopeDependency, "required cleanup resource is outside the source's provider connection or partition")
				continue
			}
			retentionBinding := graph.LifecycleBinding{Evidence: map[string]any{"resource_type": target.Identity.NativeType}}
			retainRequested := retained[target.ID] || explicitlyRetained(target, retentionBinding, input.RequestOptions[source.ID])
			seenOwners := map[asset.AssetID]bool{}
			for owner := steps[sourceStepID].AssetID; owner != "" && !seenOwners[owner]; {
				seenOwners[owner] = true
				retainRequested = retainRequested || explicitlyRetained(target, retentionBinding, input.RequestOptions[owner])
				parent, present := actionSteps[owner].Evidence["lifecycle_controller"]
				if !present {
					break
				}
				owner = asset.AssetID(fmt.Sprint(parent))
			}
			if retainRequested {
				blocked(BlockLifecycleAuthority, "required cleanup resource was explicitly retained")
				continue
			}
			targetStepID := deleting[target.ID]
			controllers, _ := relationship.Evidence[graph.RelationshipEvidenceDeletionCascadeControllers].(map[string]any)
			if targetStepID == sourceStepID && controllers[string(steps[sourceStepID].AssetID)] == true && steps[sourceStepID].AssetID != target.ID {
				// The provider explicitly declares this native cascade sufficient.
				// The source can be the controller itself, but its prerequisite
				// must be a reviewed delegated impact. All protections still apply.
				continue
			}
			if targetStepID == "" && !selected[target.ID] {
				if !automaticSelection {
					blocked(BlockLifecycleAuthority, "required cleanup resource must be selected explicitly")
					continue
				}
				selected[target.ID] = true
				input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, target.ID)
				added = true
				continue
			}
			// A provider can declare an already planned controller sufficient for
			// this prerequisite, but only if it verifies the managed asset's own
			// absence. Never promote a requirement into selecting its controller.
			var controller asset.AssetID
			owner := steps[targetStepID].AssetID
			if targetStepID != "" && targetStepID != sourceStepID && owner != target.ID && controllers[string(owner)] == true {
				for _, impact := range result.ImpactItems {
					if impact.AssetID == target.ID && VerifiedManagedDeletion(impact, owner, targetStepID, result.ImpactItems) {
						controller = owner
					}
				}
			}
			if targetStepID == "" || owner != target.ID && controller == "" || targetStepID == sourceStepID {
				blocked(BlockDirectCleanupInvalid, "required cleanup has no independent executable action")
				continue
			}
			if byStep[sourceStepID] == nil {
				byStep[sourceStepID] = map[asset.AssetID]RequiredDeletion{}
			}
			byStep[sourceStepID][target.ID] = RequiredDeletion{AssetID: target.ID, StepID: targetStepID, ControllerAssetID: controller}
		}
		if added {
			continue
		}
		for i := range result.Steps {
			step := &result.Steps[i]
			var prerequisites []RequiredDeletion
			for _, prerequisite := range byStep[step.ID] {
				prerequisites = append(prerequisites, prerequisite)
				if !slices.Contains(step.DependsOn, prerequisite.StepID) {
					step.DependsOn = append(step.DependsOn, prerequisite.StepID)
				}
			}
			if len(prerequisites) != 0 {
				sort.Slice(prerequisites, func(i, j int) bool { return prerequisites[i].AssetID < prerequisites[j].AssetID })
				sort.Slice(step.DependsOn, func(i, j int) bool { return step.DependsOn[i] < step.DependsOn[j] })
				step.Evidence[EvidenceRequiredDeletions] = prerequisites
				step.Evidence = cloneMap(step.Evidence)
			}
		}
		// Required deletion precedes the action that causes a delegated
		// effect, even if that effect has a separate verification step.
		stepAssets := map[asset.AssetID]CleanupTaskStep{}
		stepOwners := map[StepID]asset.AssetID{}
		dependencies := map[asset.AssetID]map[asset.AssetID]struct{}{}
		for _, step := range result.Steps {
			stepAssets[step.AssetID], stepOwners[step.ID] = step, step.AssetID
		}
		for _, step := range result.Steps {
			for _, dependency := range step.DependsOn {
				addDependency(dependencies, step.AssetID, stepOwners[dependency])
			}
		}
		ordered, acyclic := topologicalSteps(stepAssets, dependencies)
		if !acyclic {
			blockers.add(Blocker{Code: BlockDependencyCycle, Message: "resource dependency graph cannot be reduced to a cleanup DAG"})
			ordered = stableSteps(stepAssets)
		}
		result.Steps, result.Blockers = ordered, blockers.values()
		return result, nil
	}
	return Result{}, fmt.Errorf("cleanup prerequisite expansion did not converge")
}

// VerifiedManagedDeletion follows the reviewed execution chain to the action
// that verifies it. An effective-controller label alone cannot authorize a
// nested prerequisite or disguise a changed immediate owner.
func VerifiedManagedDeletion(impact ImpactItem, controller asset.AssetID, step StepID, impacts []ImpactItem) bool {
	for depth := 0; depth <= len(impacts); depth++ {
		if impact.DelegatedTo != step || impact.Expected != ExpectedDelegatedDelete || (impact.Ownership != graph.OwnershipExclusive && !nativeImpactEffect(impact)) || impact.CleanupPolicy != graph.CleanupDelegate || impact.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] != true {
			return false
		}
		if impact.ControllerID == controller {
			return true
		}
		if fmt.Sprint(impact.Evidence["effective_controller"]) != string(controller) {
			return false
		}
		var parent *ImpactItem
		for i := range impacts {
			if impacts[i].AssetID == impact.ControllerID && impacts[i].DelegatedTo == step {
				if parent != nil {
					return false
				}
				parent = &impacts[i]
			}
		}
		if parent == nil {
			return false
		}
		impact = *parent
	}
	return false
}

func nativeImpactEffect(impact ImpactItem) bool {
	confidence, _ := impact.Evidence["confidence"].(float64)
	source, _ := impact.Evidence["evidence_source"].(string)
	return graph.NativeDeleteEffect(graph.LifecycleBinding{
		ControllerAssetID: impact.ControllerID, ManagedAssetID: impact.AssetID,
		Ownership: impact.Ownership, CleanupPolicy: impact.CleanupPolicy,
		Authority: graph.Authority(fmt.Sprint(impact.Evidence["authority"])), Confidence: confidence,
		EvidenceSource: source, Evidence: impact.Evidence,
	})
}
