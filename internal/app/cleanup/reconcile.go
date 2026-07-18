package cleanup

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/finding"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
)

const ResidualFindingRule = "cleanup.residual_resource"

// Outcome helpers return only rows owned by the completed step. Persisting
// partial updates prevents independent concurrent steps from overwriting each
// other's impact results with stale snapshots.
func initialImpactResults(values []plan.ImpactItem, stepID plan.StepID) []plan.ImpactItem {
	result := make([]plan.ImpactItem, 0)
	for _, value := range values {
		if value.DelegatedTo != stepID {
			continue
		}
		switch value.Expected {
		case plan.ExpectedDelegatedDelete:
			if plan.ControllerDeletionImpliesAbsence(value.Evidence) {
				value.Result = plan.ImpactDeletedByController
			} else {
				value.Result = plan.ImpactDelegated
			}
		case plan.ExpectedRetainShared:
			value.Result = plan.ImpactRetainedShared
		case plan.ExpectedRetainExplicit, plan.ExpectedProviderDefaultRetain:
			value.Result = plan.ImpactRetainedByPolicy
		default:
			value.Result = plan.ImpactUnknown
		}
		result = append(result, value)
	}
	return result
}

func failedImpactResults(values []plan.ImpactItem, stepID plan.StepID) []plan.ImpactItem {
	result := make([]plan.ImpactItem, 0)
	for _, value := range values {
		if value.DelegatedTo == stepID && value.Expected == plan.ExpectedDelegatedDelete {
			value.Result = plan.ImpactCleanupFailed
			result = append(result, value)
		}
	}
	return result
}

func ignoredDirtyImpactResults(values []plan.ImpactItem, stepID plan.StepID) []plan.ImpactItem {
	result := make([]plan.ImpactItem, 0)
	for _, value := range values {
		if value.DelegatedTo == stepID {
			value.Result = plan.ImpactIgnoredDirty
			result = append(result, value)
		}
	}
	return result
}

func unsupportedImpactResults(values []plan.ImpactItem, stepID plan.StepID) []plan.ImpactItem {
	result := make([]plan.ImpactItem, 0)
	for _, value := range values {
		if value.DelegatedTo == stepID {
			value.Result = plan.ImpactStillPresent
			result = append(result, value)
		}
	}
	return result
}

func reconcileImpactResults(ctx context.Context, repositories persistence.Repositories, aggregate persistence.CleanupTaskAggregate, attempt execution.ExecutionAttempt, stepID plan.StepID, observedAt time.Time) ([]plan.ImpactItem, error) {
	result := append([]plan.ImpactItem(nil), aggregate.ImpactItems...)
	for index := range result {
		impact := &result[index]
		if impact.DelegatedTo != stepID {
			continue
		}
		value, err := repositories.Inventory().GetAsset(ctx, impact.AssetID)
		if errors.Is(err, persistence.ErrNotFound) {
			return nil, fmt.Errorf("reconcile impact asset %q: %w", impact.AssetID, err)
		}
		if err != nil {
			return nil, err
		}
		if value.ClosedAt != nil {
			impact.Result = plan.ImpactDeletedByController
			if err := closeResidualFinding(ctx, repositories.Findings(), impact.AssetID, observedAt); err != nil {
				return nil, err
			}
			continue
		}
		switch impact.Expected {
		case plan.ExpectedRetainShared:
			impact.Result = plan.ImpactRetainedShared
		case plan.ExpectedRetainExplicit, plan.ExpectedProviderDefaultRetain:
			impact.Result = plan.ImpactRetainedByPolicy
		case plan.ExpectedDelegatedDelete:
			impact.Result = plan.ImpactStillPresent
			if err := putResidualFinding(ctx, repositories.Findings(), aggregate.Task, attempt, *impact, value, observedAt); err != nil {
				return nil, err
			}
		default:
			impact.Result = plan.ImpactUnknown
		}
		if impact.Result != plan.ImpactStillPresent {
			if err := closeResidualFinding(ctx, repositories.Findings(), impact.AssetID, observedAt); err != nil {
				return nil, err
			}
		}
	}
	return result, nil
}

func putResidualFinding(ctx context.Context, repository persistence.FindingRepository, cleanupTask plan.CleanupTask, attempt execution.ExecutionAttempt, impact plan.ImpactItem, value asset.Asset, observedAt time.Time) error {
	existing, err := repository.ListFindingsByAsset(ctx, value.ID)
	if err != nil {
		return err
	}
	id := finding.ID(idgen.MustNew("fnd"))
	result := finding.Finding{
		ID: id, AssetID: value.ID, RuleID: ResidualFindingRule, Status: finding.StatusOpen, Severity: finding.SeverityHigh,
		Title:       "Resource remains after delegated cleanup",
		Description: "An authoritative inventory scan still observes a resource that its lifecycle controller was expected to delete.",
		Evidence: map[string]any{
			"engine": "cleanup_reconciliation", "cleanup_task_id": cleanupTask.ID, "execution_id": attempt.ID,
			"controller_id": impact.ControllerID, "expected": impact.Expected, "last_seen_at": value.LastSeenAt,
		},
		SpecBundleRevision: cleanupTask.Revision.SpecBundleRevision, FirstSeenAt: observedAt, LastSeenAt: observedAt,
	}
	for _, current := range existing {
		if current.RuleID == ResidualFindingRule {
			result.ID = current.ID
			result.FirstSeenAt = current.FirstSeenAt
			break
		}
	}
	return repository.PutFinding(ctx, result)
}

func closeResidualFinding(ctx context.Context, repository persistence.FindingRepository, assetID asset.AssetID, observedAt time.Time) error {
	existing, err := repository.ListFindingsByAsset(ctx, assetID)
	if err != nil {
		return err
	}
	for _, current := range existing {
		if current.RuleID != ResidualFindingRule || current.Status != finding.StatusOpen {
			continue
		}
		current.Status = finding.StatusClosed
		current.LastSeenAt = observedAt
		current.ClosedAt = &observedAt
		if err := repository.PutFinding(ctx, current); err != nil {
			return err
		}
	}
	return nil
}
