package cleanup

import (
	"context"
	"fmt"
	"slices"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Reconstruct the worker's frozen request in its original list order.
// Notification channels reuse the direct-prerequisite path for alert policies.
// Router policies/sets are direct prerequisites; NATs are delegated impacts.
// Recovery never substitutes current child configuration for a missing snapshot.
func routerRecoveryImpacts(ctx context.Context, repositories persistence.Repositories, task persistence.CleanupTaskAggregate, step plan.CleanupTaskStep, request *contracts.ActionRequest) error {
	frozen := func(id asset.AssetID, evidence map[string]any) (asset.Asset, error) {
		if _, ok := evidence[plan.EvidencePlannedAsset]; !ok {
			return asset.Asset{}, fmt.Errorf("router recovery requires frozen child review")
		}
		live, err := repositories.Inventory().GetAsset(ctx, id)
		if err != nil {
			return asset.Asset{}, err
		}
		value, err := plan.PlannedAsset(evidence, live)
		if err != nil {
			return asset.Asset{}, err
		}
		if value.Identity.Provider != request.Asset.Identity.Provider || value.Identity.ConnectionID != request.Asset.Identity.ConnectionID || value.Identity.Partition != request.Asset.Identity.Partition {
			return asset.Asset{}, fmt.Errorf("router recovery child identity changed")
		}
		return value, nil
	}
	required, err := plan.RequiredDeletions(step)
	if err != nil {
		return err
	}
	seen := map[asset.AssetID]bool{}
	appendPrerequisite := func(candidate plan.CleanupTaskStep) error {
		value, err := frozen(candidate.AssetID, candidate.Evidence)
		if err != nil {
			return err
		}
		request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, contracts.ActionImpact{Asset: value, ControllerID: step.AssetID, Delete: true})
		seen[value.ID] = true
		return nil
	}
	for _, requirement := range required {
		if requirement.ControllerAssetID != "" {
			return fmt.Errorf("router recovery prerequisite must be independently deleted")
		}
		var match *plan.CleanupTaskStep
		for i := range task.Steps {
			candidate := &task.Steps[i]
			if candidate.ID == requirement.StepID && candidate.AssetID == requirement.AssetID && candidate.Action == "delete" {
				if match != nil {
					return fmt.Errorf("router recovery prerequisite is ambiguous")
				}
				match = candidate
			}
		}
		if match == nil {
			return fmt.Errorf("router recovery prerequisite is missing")
		}
		if err := appendPrerequisite(*match); err != nil {
			return err
		}
	}
	for _, candidate := range task.Steps {
		if candidate.Action != "delete" || seen[candidate.AssetID] || fmt.Sprint(candidate.Evidence["lifecycle_controller"]) != string(step.AssetID) || fmt.Sprint(candidate.Evidence["cleanup_policy"]) != string(graph.CleanupDirect) || !slices.Contains(step.DependsOn, candidate.ID) {
			continue
		}
		if err := appendPrerequisite(candidate); err != nil {
			return err
		}
	}
	for _, impact := range task.ImpactItems {
		if impact.DelegatedTo != step.ID {
			continue
		}
		if impact.Expected != plan.ExpectedDelegatedDelete {
			return fmt.Errorf("router recovery requires reviewed child deletion")
		}
		value, err := frozen(impact.AssetID, impact.Evidence)
		if err != nil {
			return err
		}
		request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{Asset: value, ControllerID: impact.ControllerID, Delete: true})
	}
	return nil
}
