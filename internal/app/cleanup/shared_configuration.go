package cleanup

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
)

const natMutationScope = "gcp_nat_mutation_scope"

func solveCleanupPlan(input plan.Input) (plan.Result, error) {
	result, err := plan.Solve(input)
	if err != nil {
		return result, err
	}
	scopes := map[asset.AssetID]string{}
	for _, value := range input.Assets {
		if value.Identity.Provider != asset.ProviderGCP || value.Identity.NativeType != "compute.googleapis.com/RouterNat" {
			continue
		}
		parent, _, ok := strings.Cut(value.Identity.NativeID, "/nats/")
		if ok {
			scopes[value.ID] = string(value.Identity.ConnectionID) + "/" + value.Identity.Partition + "/" + parent
		}
	}
	previous := map[string]plan.StepID{}
	// Steps already have topological order. Adding an edge to an earlier sibling
	// keeps that order acyclic and reuses durable worker prerequisite handling.
	for i := range result.Steps {
		step := &result.Steps[i]
		scope := scopes[step.AssetID]
		if scope == "" || step.Action != "delete" {
			continue
		}
		if step.Evidence == nil {
			step.Evidence = map[string]any{}
		}
		step.Evidence[natMutationScope] = scope
		if prior := previous[scope]; prior != "" && !slices.Contains(step.DependsOn, prior) {
			step.DependsOn = append(step.DependsOn, prior)
		}
		previous[scope] = step.ID
	}
	return result, nil
}

// Call under the connection row lock in the execution creation/resume transaction.
// A paused run can retain an outstanding cloud operation, so it still owns scope.
func guardSharedConfiguration(ctx context.Context, repositories persistence.Repositories, aggregate persistence.CleanupTaskAggregate) error {
	scopes := map[string]bool{}
	for _, step := range aggregate.Steps {
		if scope, ok := step.Evidence[natMutationScope].(string); ok && scope != "" {
			scopes[scope] = true
		}
	}
	if len(scopes) == 0 {
		return nil
	}
	options := persistence.ListOptions{ConnectionID: aggregate.Task.ConnectionID, Limit: 500}
	for {
		page, err := repositories.Executions().ListExecutions(ctx, options)
		if err != nil {
			return err
		}
		for _, attempt := range page.Items {
			if attempt.CleanupTaskID == string(aggregate.Task.ID) || attempt.Status == execution.ExecutionSucceeded {
				continue
			}
			other, err := repositories.CleanupTasks().GetTask(ctx, plan.CleanupTaskID(attempt.CleanupTaskID))
			if err != nil {
				return err
			}
			for _, step := range other.Steps {
				scope, _ := step.Evidence[natMutationScope].(string)
				if !scopes[scope] {
					continue
				}
				// Terminal runs may still have an unresolved native receipt. A completed
				// matching action releases the scope even when another action failed.
				actions, err := repositories.Executions().ListActions(ctx, attempt.ID)
				if err != nil {
					return err
				}
				settled := false
				for _, action := range actions {
					if action.CleanupTaskStepID == string(step.ID) && action.Status == execution.ActionSucceeded {
						settled = true
					}
				}
				if !settled {
					return fmt.Errorf("%w: another cleanup execution has an unresolved NAT update on this router", persistence.ErrConflict)
				}
			}
		}
		if page.NextCursor == "" {
			return nil
		}
		options.Cursor = page.NextCursor
	}
}
