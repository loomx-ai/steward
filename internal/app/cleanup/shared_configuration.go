package cleanup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
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
func (s *Service) guardSharedConfiguration(ctx context.Context, repositories persistence.Repositories, aggregate persistence.CleanupTaskAggregate) error {
	registry, _ := s.bundles.(ProviderActionRegistry)
	return guardSharedConfiguration(ctx, repositories, aggregate, registry)
}

func guardSharedConfiguration(ctx context.Context, repositories persistence.Repositories, aggregate persistence.CleanupTaskAggregate, registries ...ProviderActionRegistry) error {
	scopes := map[string]bool{}
	for _, step := range aggregate.Steps {
		if scope, ok := step.Evidence[natMutationScope].(string); ok && scope != "" {
			scopes[scope] = true
		}
	}
	if len(scopes) == 0 {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
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
				var registry ProviderActionRegistry
				if len(registries) > 0 {
					registry = registries[0]
				}
				settled, err := settleSharedConfiguration(ctx, repositories, other, attempt, step, registry)
				if err != nil {
					return fmt.Errorf("%w: cannot verify previous NAT update: %w", persistence.ErrConflict, err)
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

// The connection lock serializes new work; the execution lock prevents a fresh
// intent racing this terminal-state inspection. A still-runnable worker job
// prevents recovery, even when its lease has expired.
func settleSharedConfiguration(ctx context.Context, repositories persistence.Repositories, task persistence.CleanupTaskAggregate, attempt execution.ExecutionAttempt, step plan.CleanupTaskStep, registry ProviderActionRegistry) (bool, error) {
	if err := repositories.Executions().LockExecution(ctx, attempt.ID); err != nil {
		return false, err
	}
	attempt, err := repositories.Executions().GetExecution(ctx, attempt.ID)
	if err != nil {
		return false, err
	}
	actions, err := repositories.Executions().ListActions(ctx, attempt.ID)
	if err != nil {
		return false, err
	}
	var selected *execution.ActionAttempt
	for i := range actions {
		if actions[i].CleanupTaskStepID == string(step.ID) {
			if selected != nil {
				return false, fmt.Errorf("ambiguous action history")
			}
			selected = &actions[i]
		}
	}
	if selected != nil && selected.Status == execution.ActionSucceeded {
		return true, nil
	}
	if attempt.Status != execution.ExecutionFailed && attempt.Status != execution.ExecutionCanceled {
		return false, nil
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "cleanup_task", string(task.Task.ID))
	if err != nil {
		return false, err
	}
	for _, job := range jobs {
		if payloadString(job.Payload, "execution_id") != string(attempt.ID) {
			continue
		}
		if job.Status != execution.JobSucceeded && job.Status != execution.JobFailed && job.Status != execution.JobCanceled {
			return false, nil
		}
	}
	if selected == nil {
		return true, nil
	} // A worker must persist intent before invoking.
	action := *selected
	if action.Status == execution.ActionIntentPersisted && action.ResumeStatus == "" && action.FailedFrom == "" && action.ProviderRequestID == "" && len(action.PreflightEvidence) == 0 && action.ProviderOperationID == "" && len(action.ProviderResult) == 0 {
		return true, nil
	}
	digest, err := sharedMutationDigest(attempt, step, action)
	if err != nil {
		return false, err
	}
	if proof := action.MutationSettlement; proof != nil && proof.Digest == digest && proof.Operation != "" {
		return true, nil
	}
	if registry == nil {
		return false, nil
	}
	if _, ok := step.Evidence[plan.EvidencePlannedAsset]; !ok {
		return false, fmt.Errorf("mutation recovery requires the reviewed asset snapshot")
	}
	live, err := repositories.Inventory().GetAsset(ctx, step.AssetID)
	if err != nil {
		return false, err
	}
	reviewed, err := plan.PlannedAsset(step.Evidence, live)
	if err != nil {
		return false, err
	}
	if reviewed.Identity.ConnectionID != attempt.ConnectionID || reviewed.Identity.Provider != asset.ProviderGCP || reviewed.Identity.NativeType != "compute.googleapis.com/RouterNat" {
		return false, fmt.Errorf("mutation recovery identity changed")
	}
	// ponytail: retain the database locks during rare recovery reads so a second
	// execution cannot race settlement; cap lock duration rather than add a queue.
	driver, err := registry.ResolveAction(ctx, attempt.ConnectionID, reviewed)
	if err != nil {
		return false, err
	}
	reader, ok := driver.(contracts.MutationSettlementReader)
	if !ok {
		return false, nil
	}
	request := contracts.ActionRequest{Asset: reviewed, Action: step.Action, Parameters: cloneRequest(step.RequestOptions), IdempotencyKey: resumedProviderIdempotencyKey(attempt, action)}
	result := contracts.ActionResult{ProviderRequestID: action.ProviderRequestID, ProviderOperationID: action.ProviderOperationID, Data: cloneRequest(action.ProviderResult)}
	settlement, err := reader.MutationSettled(ctx, request, result)
	if err != nil {
		return false, err
	}
	if !settlement.Settled || settlement.Operation == "" {
		return false, nil
	}
	action.MutationSettlement = &execution.MutationSettlementProof{Digest: digest, Operation: settlement.Operation, VerifiedAt: time.Now().UTC()}
	// Keep the original action status, receipt, timestamps and deletion outcome.
	// Any subsequent worker update changes the digest and invalidates this proof.
	if err := repositories.Executions().UpdateAction(ctx, action); err != nil {
		return false, err
	}
	return true, nil
}

func sharedMutationDigest(attempt execution.ExecutionAttempt, step plan.CleanupTaskStep, action execution.ActionAttempt) (string, error) {
	action.MutationSettlement = nil
	encoded, err := json.Marshal(struct {
		Attempt execution.ExecutionAttempt
		Step    plan.CleanupTaskStep
		Action  execution.ActionAttempt
	}{attempt, step, action})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:]), nil
}
