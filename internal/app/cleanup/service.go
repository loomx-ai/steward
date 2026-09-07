package cleanup

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	connectionapp "github.com/loomx-ai/steward/internal/app/connection"
	"github.com/loomx-ai/steward/internal/app/scancoverage"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/core/requestmeta"
	coretopology "github.com/loomx-ai/steward/internal/core/topology"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type BundleResolver interface {
	Bundle(asset.Provider) (spec.Bundle, error)
	ProviderDescriptors() []contracts.ProviderDescriptor
}

type CreateTaskRequest struct {
	ConnectionID   asset.ConnectionID               `json:"connection_id"`
	Selectors      []plan.CleanupSelector           `json:"selectors"`
	CreatedBy      string                           `json:"created_by"`
	RequestOptions map[asset.AssetID]map[string]any `json:"request_options,omitempty"`
}

type AddTaskAssetsRequest struct {
	ConnectionID  asset.ConnectionID `json:"connection_id"`
	CleanupTaskID plan.CleanupTaskID `json:"cleanup_task_id"`
	AssetIDs      []asset.AssetID    `json:"asset_ids"`
	UpdatedBy     string             `json:"updated_by"`
}

type ProtectionEvaluator func(asset.Asset) plan.ProtectionPolicy

type ExecutionAuthorizer interface {
	AuthorizeExecution(context.Context, string, []asset.AssetID) error
}

type ExecutionAuthorizerFunc func(context.Context, string, []asset.AssetID) error

func (f ExecutionAuthorizerFunc) AuthorizeExecution(ctx context.Context, actor string, assets []asset.AssetID) error {
	return f(ctx, actor, assets)
}

type CreateExecutionRequest struct {
	ConnectionID   asset.ConnectionID    `json:"connection_id"`
	CleanupTaskID  plan.CleanupTaskID    `json:"cleanup_task_id"`
	RequestedBy    string                `json:"requested_by"`
	IdempotencyKey string                `json:"idempotency_key"`
	Concurrency    *int                  `json:"concurrency,omitempty"`
	Confirmation   ExecutionConfirmation `json:"confirmation"`
}

type ContinueExecutionRequest struct {
	ConnectionID   asset.ConnectionID `json:"connection_id"`
	CleanupTaskID  plan.CleanupTaskID `json:"cleanup_task_id"`
	RequestedBy    string             `json:"requested_by"`
	IdempotencyKey string             `json:"idempotency_key"`
	Concurrency    *int               `json:"concurrency,omitempty"`
}

type ExecutionConfirmation struct {
	TypedNames   map[string]string `json:"typed_names,omitempty"`
	Acknowledged bool              `json:"acknowledged,omitempty"`
}

var (
	ErrExecutionConfirmation          = errors.New("cleanup execution confirmation is invalid")
	ErrExecutionConcurrency           = errors.New("cleanup execution concurrency is invalid")
	ErrExecutionNotContinuable        = errors.New("cleanup execution cannot continue")
	ErrInventoryReconciliationPending = errors.New("inventory relationship reconciliation is pending")
	ErrTaskNotEditable                = errors.New("cleanup task cannot be updated in its current state")
)

const (
	DefaultExecutionConcurrency = 20
	MinExecutionConcurrency     = 1
	MaxExecutionConcurrency     = 100
)

func requestedExecutionConcurrency(value *int) (int, error) {
	if value == nil {
		return DefaultExecutionConcurrency, nil
	}
	if *value < MinExecutionConcurrency || *value > MaxExecutionConcurrency {
		return 0, fmt.Errorf(
			"%w: concurrency must be between %d and %d",
			ErrExecutionConcurrency,
			MinExecutionConcurrency,
			MaxExecutionConcurrency,
		)
	}
	return *value, nil
}

func effectiveExecutionConcurrency(attempt execution.ExecutionAttempt) int {
	if attempt.Concurrency >= MinExecutionConcurrency && attempt.Concurrency <= MaxExecutionConcurrency {
		return attempt.Concurrency
	}
	return DefaultExecutionConcurrency
}

// GetTask returns the stored cleanup snapshot with compatibility-only safety
// dependencies inferred from the task's bound graph revision. The stored rows
// are updated only when execution is continued.
func (s *Service) GetTask(
	ctx context.Context,
	id plan.CleanupTaskID,
	connectionID asset.ConnectionID,
) (persistence.CleanupTaskAggregate, error) {
	if s == nil || s.repositories == nil {
		return persistence.CleanupTaskAggregate{}, fmt.Errorf("planning repositories are required")
	}
	aggregate, err := s.repositories.CleanupTasks().GetTask(ctx, id)
	if err != nil {
		return persistence.CleanupTaskAggregate{}, err
	}
	if connectionID != "" && aggregate.Task.ConnectionID != connectionID {
		return persistence.CleanupTaskAggregate{}, persistence.ErrNotFound
	}
	relationships, err := s.repositories.Graph().ListRelationshipsByAssetIDs(
		ctx,
		aggregate.Task.ResolvedAssetIDs,
	)
	if err != nil {
		return persistence.CleanupTaskAggregate{}, err
	}
	aggregate.Steps, _ = appendCreatedFromTaskDependencies(
		aggregate.Steps,
		relationships,
	)
	return aggregate, nil
}

func ValidateExecutionConfirmation(selectors []plan.CleanupSelector, confirmation ExecutionConfirmation) error {
	needsAcknowledgement := false
	for _, selector := range selectors {
		if requiresTypedName(selector) {
			expected := selectorConfirmationName(selector)
			if expected == "" || confirmation.TypedNames[expected] != expected {
				return fmt.Errorf("%w: exact typed name is required for %s selector", ErrExecutionConfirmation, selector.Kind)
			}
			continue
		}
		needsAcknowledgement = true
	}
	if needsAcknowledgement && !confirmation.Acknowledged {
		return fmt.Errorf("%w: explicit acknowledgement is required", ErrExecutionConfirmation)
	}
	return nil
}

func requiresTypedName(selector plan.CleanupSelector) bool {
	return selector.Kind == plan.SelectorConnection ||
		(selector.Kind == plan.SelectorScope && (selector.ScopeKind == asset.ScopeAccount || selector.ScopeKind == asset.ScopeProject || selector.ScopeKind == asset.ScopeRegion))
}

func selectorConfirmationName(selector plan.CleanupSelector) string {
	if displayName := strings.TrimSpace(selector.DisplayName); displayName != "" {
		return displayName
	}
	switch selector.Kind {
	case plan.SelectorConnection:
		return strings.TrimSpace(string(selector.ConnectionID))
	case plan.SelectorScope:
		return strings.TrimSpace(string(selector.ScopeID))
	case plan.SelectorGroup:
		return strings.TrimSpace(selector.GroupKey)
	case plan.SelectorAsset:
		return strings.TrimSpace(string(selector.AssetID))
	default:
		return ""
	}
}

func confirmationRequirements(selectors []plan.CleanupSelector) (map[string]struct{}, bool) {
	typedNames := map[string]struct{}{}
	needsAcknowledgement := false
	for _, selector := range selectors {
		if requiresTypedName(selector) {
			typedNames[selectorConfirmationName(selector)] = struct{}{}
		} else {
			needsAcknowledgement = true
		}
	}
	return typedNames, needsAcknowledgement
}

func confirmationRequirementEvidence(selectors []plan.CleanupSelector) map[string]any {
	typedNames, needsAcknowledgement := confirmationRequirements(selectors)
	methods := make([]string, 0, 2)
	if len(typedNames) > 0 {
		methods = append(methods, "type_name")
	}
	if needsAcknowledgement {
		methods = append(methods, "acknowledged")
	}
	return map[string]any{
		"methods":                  methods,
		"typed_name_count":         len(typedNames),
		"acknowledgement_required": needsAcknowledgement,
	}
}

func confirmationEvidence(selectors []plan.CleanupSelector, confirmation ExecutionConfirmation) map[string]any {
	evidence := confirmationRequirementEvidence(selectors)
	_, needsAcknowledgement := confirmationRequirements(selectors)
	evidence["acknowledged"] = needsAcknowledgement && confirmation.Acknowledged
	return evidence
}

type ServiceOption func(*Service)

type Service struct {
	repositories         persistence.Repositories
	bundles              BundleResolver
	clock                func() time.Time
	taskIDGenerator      func() string
	executionIDGenerator func() string
	protectionEvaluator  ProtectionEvaluator
	executionAuthorizer  ExecutionAuthorizer
}

func NewService(repositories persistence.Repositories, bundles BundleResolver, options ...ServiceOption) *Service {
	service := &Service{
		repositories:         repositories,
		bundles:              bundles,
		clock:                func() time.Time { return time.Now().UTC() },
		taskIDGenerator:      func() string { return idgen.MustNew("cln") },
		executionIDGenerator: func() string { return idgen.MustNew("exe") },
		protectionEvaluator:  defaultProtectionPolicy,
		executionAuthorizer: ExecutionAuthorizerFunc(func(_ context.Context, actor string, _ []asset.AssetID) error {
			if strings.TrimSpace(actor) == "" {
				return fmt.Errorf("authenticated execution actor is required")
			}
			return nil
		}),
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func WithClock(clock func() time.Time) ServiceOption {
	return func(service *Service) {
		if clock != nil {
			service.clock = clock
		}
	}
}

func WithTaskIDGenerator(generator func() string) ServiceOption {
	return func(service *Service) {
		if generator != nil {
			service.taskIDGenerator = generator
		}
	}
}

func WithExecutionIDGenerator(generator func() string) ServiceOption {
	return func(service *Service) {
		if generator != nil {
			service.executionIDGenerator = generator
		}
	}
}

func WithExecutionAuthorizer(authorizer ExecutionAuthorizer) ServiceOption {
	return func(service *Service) {
		if authorizer != nil {
			service.executionAuthorizer = authorizer
		}
	}
}

func WithProtectionEvaluator(evaluator ProtectionEvaluator) ServiceOption {
	return func(service *Service) {
		if evaluator != nil {
			service.protectionEvaluator = evaluator
		}
	}
}

func (s *Service) CreateTask(ctx context.Context, request CreateTaskRequest) (persistence.CleanupTaskAggregate, error) {
	if s == nil || s.repositories == nil || s.bundles == nil {
		return persistence.CleanupTaskAggregate{}, fmt.Errorf("planning repositories and bundle resolver are required")
	}
	if len(request.Selectors) == 0 || strings.TrimSpace(request.CreatedBy) == "" {
		return persistence.CleanupTaskAggregate{}, fmt.Errorf("cleanup task requires selectors and creator")
	}
	cleanupTaskID := plan.CleanupTaskID(strings.TrimSpace(s.taskIDGenerator()))
	if cleanupTaskID == "" {
		return persistence.CleanupTaskAggregate{}, fmt.Errorf("cleanup task ID generator returned an empty ID")
	}
	requestOptions, err := copyRequestOptions(request.RequestOptions)
	if err != nil {
		return persistence.CleanupTaskAggregate{}, err
	}

	var aggregate persistence.CleanupTaskAggregate
	err = s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		selection, err := s.resolveSelection(ctx, repositories, request.Selectors)
		if err != nil {
			return err
		}
		if len(selection.AssetIDs) == 0 {
			return fmt.Errorf("cleanup selectors resolved no assets")
		}
		if len(selection.ConnectionIDs) != 1 {
			return fmt.Errorf("cleanup task must belong to exactly one cloud connection")
		}
		if err := ensureInventoryReconciled(ctx, repositories.Inventory(), selection.ConnectionIDs); err != nil {
			return err
		}
		connectionID := selection.ConnectionIDs[0]
		if request.ConnectionID != "" && request.ConnectionID != connectionID {
			return fmt.Errorf("cleanup selectors do not belong to the selected connection")
		}
		coverage, err := s.resolveSelectionCoverage(ctx, repositories, selection)
		if err != nil {
			return err
		}
		input, err := s.loadPlanningInput(ctx, repositories, cleanupTaskID, selection, coverage, requestOptions)
		if err != nil {
			return err
		}
		result, err := plan.Solve(input)
		if err != nil {
			return err
		}
		result.Blockers = appendSelectionBlockers(result.Blockers, input, result)
		result.Warnings = appendSelectionWarnings(result.Warnings, input, result)
		status := plan.StatusReady
		if len(result.Blockers) > 0 {
			status = plan.StatusDraft
		}
		value := plan.CleanupTask{
			ID: cleanupTaskID, ConnectionID: connectionID, Status: status, Selectors: selection.Selectors, ResolvedAssetIDs: selection.AssetIDs, SelectorAssetIDs: selection.SelectorAssetIDs, RequestOptions: requestOptions,
			Revision: input.Revision, Coverage: coverage, SnapshotHash: result.SnapshotHash, Blockers: result.Blockers, Warnings: result.Warnings,
			CreatedBy: strings.TrimSpace(request.CreatedBy), CreatedAt: s.clock(),
		}
		if err := repositories.CleanupTasks().CreateTask(ctx, value, result.Steps, result.ImpactItems); err != nil {
			return err
		}
		if err := repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: connectionID, Actor: value.CreatedBy,
			Action: "cleanup.task.create", TargetType: "cleanup_task", TargetID: string(value.ID), Result: string(value.Status),
			Evidence: map[string]any{"selectors": value.Selectors, "resolved_asset_ids": value.ResolvedAssetIDs, "scan_coverage": value.Coverage, "blockers": value.Blockers, "warnings": value.Warnings, "confirmation_requirement": confirmationRequirementEvidence(value.Selectors)}, CreatedAt: value.CreatedAt,
		}); err != nil {
			return err
		}
		aggregate = persistence.CleanupTaskAggregate{Task: value, Steps: result.Steps, ImpactItems: result.ImpactItems}
		return nil
	})
	return aggregate, err
}

// AddTaskAssets extends an editable task with explicit asset selectors and
// atomically replaces its planning snapshot while retaining the task identity.
func (s *Service) AddTaskAssets(ctx context.Context, request AddTaskAssetsRequest) (persistence.CleanupTaskAggregate, error) {
	if s == nil || s.repositories == nil || s.bundles == nil {
		return persistence.CleanupTaskAggregate{}, fmt.Errorf("planning repositories and bundle resolver are required")
	}
	if strings.TrimSpace(string(request.CleanupTaskID)) == "" || strings.TrimSpace(request.UpdatedBy) == "" {
		return persistence.CleanupTaskAggregate{}, fmt.Errorf("cleanup task and updater are required")
	}
	assetIDs := uniqueAssetIDs(request.AssetIDs)
	if len(assetIDs) == 0 {
		return persistence.CleanupTaskAggregate{}, fmt.Errorf("at least one asset is required")
	}

	var aggregate persistence.CleanupTaskAggregate
	err := s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		stored, err := repositories.CleanupTasks().GetTask(ctx, request.CleanupTaskID)
		if err != nil {
			return err
		}
		if request.ConnectionID != "" && stored.Task.ConnectionID != request.ConnectionID {
			return persistence.ErrNotFound
		}
		if stored.Task.Status != plan.StatusDraft && stored.Task.Status != plan.StatusReady {
			return fmt.Errorf("%w: task %q is %s", ErrTaskNotEditable, stored.Task.ID, stored.Task.Status)
		}
		if err := ensureInventoryReconciled(ctx, repositories.Inventory(), []asset.ConnectionID{stored.Task.ConnectionID}); err != nil {
			return err
		}

		selectors := append([]plan.CleanupSelector(nil), stored.Task.Selectors...)
		selectedAssets := make(map[asset.AssetID]struct{})
		for _, selector := range selectors {
			if selector.Kind == plan.SelectorAsset {
				selectedAssets[selector.AssetID] = struct{}{}
			}
		}
		addedAssetIDs := make([]asset.AssetID, 0, len(assetIDs))
		for _, assetID := range assetIDs {
			if _, exists := selectedAssets[assetID]; exists {
				continue
			}
			selectors = append(selectors, plan.CleanupSelector{Kind: plan.SelectorAsset, AssetID: assetID})
			selectedAssets[assetID] = struct{}{}
			addedAssetIDs = append(addedAssetIDs, assetID)
		}
		selection, err := s.resolveSelection(ctx, repositories, selectors)
		if err != nil {
			return err
		}
		if len(selection.AssetIDs) == 0 {
			return fmt.Errorf("cleanup selectors resolved no assets")
		}
		if len(selection.ConnectionIDs) != 1 || selection.ConnectionIDs[0] != stored.Task.ConnectionID {
			return fmt.Errorf("cleanup task must remain within its cloud connection")
		}
		coverage, err := s.resolveSelectionCoverage(ctx, repositories, selection)
		if err != nil {
			return err
		}
		requestOptions, err := copyRequestOptions(stored.Task.RequestOptions)
		if err != nil {
			return err
		}
		input, err := s.loadPlanningInput(ctx, repositories, stored.Task.ID, selection, coverage, requestOptions)
		if err != nil {
			return err
		}
		result, err := plan.Solve(input)
		if err != nil {
			return err
		}
		result.Blockers = appendSelectionBlockers(result.Blockers, input, result)
		result.Warnings = appendSelectionWarnings(result.Warnings, input, result)
		status := plan.StatusReady
		if len(result.Blockers) > 0 {
			status = plan.StatusDraft
		}
		updatedAt := s.clock()
		value := stored.Task
		value.Status = status
		value.Selectors = selection.Selectors
		value.ResolvedAssetIDs = selection.AssetIDs
		value.SelectorAssetIDs = selection.SelectorAssetIDs
		value.RequestOptions = requestOptions
		value.Revision = input.Revision
		value.Coverage = coverage
		value.SnapshotHash = result.SnapshotHash
		value.Blockers = result.Blockers
		value.Warnings = result.Warnings
		value.InvalidationReason = ""
		value.UpdatedAt = &updatedAt
		if err := repositories.CleanupTasks().ReplaceTask(ctx, value, result.Steps, result.ImpactItems); err != nil {
			return err
		}
		if err := repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: value.ConnectionID, Actor: strings.TrimSpace(request.UpdatedBy),
			Action: "cleanup.task.update", TargetType: "cleanup_task", TargetID: string(value.ID), Result: string(value.Status),
			Evidence: map[string]any{"added_asset_ids": addedAssetIDs, "selectors": value.Selectors, "resolved_asset_ids": value.ResolvedAssetIDs, "scan_coverage": value.Coverage, "blockers": value.Blockers, "warnings": value.Warnings, "confirmation_requirement": confirmationRequirementEvidence(value.Selectors)}, CreatedAt: updatedAt,
		}); err != nil {
			return err
		}
		aggregate = persistence.CleanupTaskAggregate{Task: value, Steps: result.Steps, ImpactItems: result.ImpactItems}
		return nil
	})
	return aggregate, err
}

func uniqueAssetIDs(values []asset.AssetID) []asset.AssetID {
	seen := make(map[asset.AssetID]struct{}, len(values))
	result := make([]asset.AssetID, 0, len(values))
	for _, value := range values {
		value = asset.AssetID(strings.TrimSpace(string(value)))
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

// ValidateTask re-reads the authoritative planning inputs and atomically
// invalidates a stored cleanup task if any inventory, graph, bundle, policy, option, or
// lifecycle-impact fact changed. It never rewrites the immutable steps or
// impact items that explain the original decision.
func (s *Service) ValidateTask(ctx context.Context, id plan.CleanupTaskID) (persistence.CleanupTaskAggregate, error) {
	if s == nil || s.repositories == nil || s.bundles == nil {
		return persistence.CleanupTaskAggregate{}, fmt.Errorf("planning repositories and bundle resolver are required")
	}
	if strings.TrimSpace(string(id)) == "" {
		return persistence.CleanupTaskAggregate{}, fmt.Errorf("cleanup task ID is required")
	}
	var aggregate persistence.CleanupTaskAggregate
	var validationErr error
	err := s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		stored, err := repositories.CleanupTasks().GetTask(ctx, id)
		if err != nil {
			return err
		}
		aggregate = stored
		if stored.Task.Status == plan.StatusInvalidated {
			validationErr = fmt.Errorf("%w: %s", plan.ErrCleanupTaskInvalidated, stored.Task.InvalidationReason)
			return nil
		}
		if err := ensureInventoryReconciled(ctx, repositories.Inventory(), []asset.ConnectionID{stored.Task.ConnectionID}); err != nil {
			return err
		}
		selection, err := s.resolveSelection(ctx, repositories, stored.Task.Selectors)
		if err != nil {
			return err
		}
		coverage, err := s.resolveSelectionCoverage(ctx, repositories, selection)
		if err != nil {
			return err
		}
		input, err := s.loadPlanningInput(ctx, repositories, stored.Task.ID, selection, coverage, stored.Task.RequestOptions)
		if err != nil {
			return err
		}
		if len(selection.AssetIDs) == 0 {
			freshnessErr := plan.CheckFresh(stored.Task, input.Revision, "")
			if freshnessErr == nil {
				freshnessErr = fmt.Errorf("%w: cleanup selectors resolved no assets", plan.ErrCleanupTaskInvalidated)
			}
			return invalidateStoredTask(ctx, repositories.CleanupTasks(), stored, freshnessErr, &aggregate, &validationErr)
		}
		current, err := plan.Solve(input)
		if err != nil {
			return err
		}
		current.Blockers = appendSelectionBlockers(current.Blockers, input, current)
		current.Warnings = appendSelectionWarnings(current.Warnings, input, current)
		if stored.Task.Status == plan.StatusReady && len(current.Blockers) > 0 {
			freshnessErr := fmt.Errorf("%w: cleanup planning rules now report %d blocker(s)", plan.ErrCleanupTaskInvalidated, len(current.Blockers))
			return invalidateStoredTask(ctx, repositories.CleanupTasks(), stored, freshnessErr, &aggregate, &validationErr)
		}
		if err := plan.CheckFresh(stored.Task, input.Revision, current.SnapshotHash); err != nil {
			return invalidateStoredTask(ctx, repositories.CleanupTasks(), stored, err, &aggregate, &validationErr)
		}
		return nil
	})
	if err != nil {
		return persistence.CleanupTaskAggregate{}, err
	}
	return aggregate, validationErr
}

func invalidateStoredTask(
	ctx context.Context,
	repository persistence.CleanupTaskRepository,
	stored persistence.CleanupTaskAggregate,
	reason error,
	aggregate *persistence.CleanupTaskAggregate,
	validationErr *error,
) error {
	invalidated := stored.Task
	invalidated.Status = plan.StatusInvalidated
	invalidated.InvalidationReason = reason.Error()
	if err := repository.UpdateTask(ctx, invalidated); err != nil {
		return err
	}
	aggregate.Task = invalidated
	*validationErr = reason
	return nil
}

func (s *Service) CreateExecution(ctx context.Context, request CreateExecutionRequest) (execution.ExecutionAttempt, error) {
	if s == nil || s.repositories == nil || s.bundles == nil {
		return execution.ExecutionAttempt{}, fmt.Errorf("planning repositories and bundle resolver are required")
	}
	actor := strings.TrimSpace(request.RequestedBy)
	idempotencyKey := strings.TrimSpace(request.IdempotencyKey)
	if request.CleanupTaskID == "" || actor == "" || idempotencyKey == "" {
		return execution.ExecutionAttempt{}, fmt.Errorf("cleanup task, actor, and idempotency key are required")
	}
	concurrency, err := requestedExecutionConcurrency(request.Concurrency)
	if err != nil {
		return execution.ExecutionAttempt{}, err
	}
	var created execution.ExecutionAttempt
	var semanticErr error
	err = s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		existing, err := repositories.Executions().GetExecutionByIdempotencyKey(ctx, idempotencyKey)
		if err == nil {
			if existing.CleanupTaskID != string(request.CleanupTaskID) ||
				existing.RequestedBy != actor ||
				effectiveExecutionConcurrency(existing) != concurrency {
				return persistence.ErrConflict
			}
			created = existing
			return nil
		}
		if !errors.Is(err, persistence.ErrNotFound) {
			return err
		}
		aggregate, err := repositories.CleanupTasks().GetTask(ctx, request.CleanupTaskID)
		if err != nil {
			return err
		}
		if aggregate.Task.Status == plan.StatusInvalidated {
			reason := strings.TrimSpace(aggregate.Task.InvalidationReason)
			if reason == "" {
				reason = "cleanup task snapshot is no longer current"
			}
			return fmt.Errorf(
				"%w: cleanup task %q is invalidated: %s",
				plan.ErrCleanupTaskInvalidated,
				request.CleanupTaskID,
				reason,
			)
		}
		if aggregate.Task.Status != plan.StatusReady && !legacyCoverageAdvisoryTask(aggregate.Task) {
			return fmt.Errorf("cleanup task %q is not ready for execution: %s", request.CleanupTaskID, aggregate.Task.Status)
		}
		if request.ConnectionID != "" && aggregate.Task.ConnectionID != request.ConnectionID {
			return persistence.ErrNotFound
		}
		if len(aggregate.Steps) == 0 {
			return fmt.Errorf("cleanup task %q has no executable steps", request.CleanupTaskID)
		}
		if err := ValidateExecutionConfirmation(aggregate.Task.Selectors, request.Confirmation); err != nil {
			return err
		}
		if err := s.executionAuthorizer.AuthorizeExecution(ctx, actor, aggregate.Task.ResolvedAssetIDs); err != nil {
			return err
		}
		if err := ensureInventoryReconciled(ctx, repositories.Inventory(), []asset.ConnectionID{aggregate.Task.ConnectionID}); err != nil {
			return err
		}
		selection, err := s.resolveSelection(ctx, repositories, aggregate.Task.Selectors)
		if err != nil {
			return err
		}
		coverage, err := s.resolveSelectionCoverage(ctx, repositories, selection)
		if err != nil {
			return err
		}
		input, err := s.loadPlanningInput(ctx, repositories, aggregate.Task.ID, selection, coverage, aggregate.Task.RequestOptions)
		if err != nil {
			return err
		}
		if len(selection.AssetIDs) == 0 {
			freshnessErr := plan.CheckFresh(aggregate.Task, input.Revision, "")
			if freshnessErr == nil {
				freshnessErr = fmt.Errorf("%w: cleanup selectors resolved no assets", plan.ErrCleanupTaskInvalidated)
			}
			return invalidateStoredTask(ctx, repositories.CleanupTasks(), aggregate, freshnessErr, &aggregate, &semanticErr)
		}
		current, err := plan.Solve(input)
		if err != nil {
			return err
		}
		current.Blockers = appendSelectionBlockers(current.Blockers, input, current)
		current.Warnings = appendSelectionWarnings(current.Warnings, input, current)
		if len(current.Blockers) > 0 {
			freshnessErr := fmt.Errorf("%w: cleanup planning rules now report %d blocker(s)", plan.ErrCleanupTaskInvalidated, len(current.Blockers))
			return invalidateStoredTask(ctx, repositories.CleanupTasks(), aggregate, freshnessErr, &aggregate, &semanticErr)
		}
		if err := plan.CheckFresh(aggregate.Task, input.Revision, current.SnapshotHash); err != nil {
			return invalidateStoredTask(ctx, repositories.CleanupTasks(), aggregate, err, &aggregate, &semanticErr)
		}
		executionID := execution.ExecutionID(strings.TrimSpace(s.executionIDGenerator()))
		if executionID == "" {
			return fmt.Errorf("execution ID generator returned an empty ID")
		}
		now := s.clock()
		if err := connectionapp.GuardActiveWork(ctx, repositories, aggregate.Task.ConnectionID, now); err != nil {
			return err
		}
		created = execution.ExecutionAttempt{
			ID: executionID, ConnectionID: aggregate.Task.ConnectionID, CleanupTaskID: string(request.CleanupTaskID), Status: execution.ExecutionPending,
			Concurrency: concurrency, RequestedBy: actor, IdempotencyKey: idempotencyKey, CreatedAt: now,
		}
		if err := repositories.Executions().CreateExecution(ctx, created); err != nil {
			return err
		}
		for _, step := range aggregate.Steps {
			job := cleanupExecutionJob(ctx, aggregate.Task, created, step, "", 0, now)
			if err := repositories.Jobs().Enqueue(ctx, job); err != nil {
				return err
			}
		}
		aggregate.Task.Status = plan.StatusExecuting
		if err := repositories.CleanupTasks().UpdateTask(ctx, aggregate.Task); err != nil {
			return err
		}
		if err := repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: aggregate.Task.ConnectionID, Actor: actor,
			Action: "cleanup.execution.create", TargetType: "execution_attempt", TargetID: string(executionID), Result: "accepted",
			Evidence: map[string]any{"cleanup_task_id": request.CleanupTaskID, "idempotency_key": idempotencyKey, "concurrency": concurrency, "confirmation": confirmationEvidence(aggregate.Task.Selectors, request.Confirmation)}, CreatedAt: now,
		}); err != nil {
			return err
		}
		return repositories.Executions().AppendOutbox(ctx, execution.OutboxEvent{
			ID: execution.OutboxEventID(idgen.MustNew("evt")), Topic: "execution.created",
			AggregateID: string(executionID), Payload: map[string]any{"cleanup_task_id": request.CleanupTaskID, "concurrency": concurrency}, CreatedAt: now,
		})
	})
	if err != nil {
		if errors.Is(err, persistence.ErrConflict) {
			existing, lookupErr := s.repositories.Executions().GetExecutionByIdempotencyKey(ctx, idempotencyKey)
			if lookupErr == nil && existing.CleanupTaskID == string(request.CleanupTaskID) && existing.RequestedBy == actor {
				return existing, nil
			}
		}
		return execution.ExecutionAttempt{}, err
	}
	return created, semanticErr
}

func ensureInventoryReconciled(
	ctx context.Context,
	repository persistence.InventoryRepository,
	connectionIDs []asset.ConnectionID,
) error {
	seen := make(map[asset.ConnectionID]struct{}, len(connectionIDs))
	for _, connectionID := range connectionIDs {
		if connectionID == "" {
			continue
		}
		if _, exists := seen[connectionID]; exists {
			continue
		}
		seen[connectionID] = struct{}{}
		runs, err := repository.ListScanRunsByConnection(ctx, connectionID)
		if err != nil {
			return err
		}
		for _, run := range runs {
			if run.Status == asset.ScanReconciling || run.CompletionStatus != "" {
				return fmt.Errorf("%w: scan task %q has not finished updating resource relationships", ErrInventoryReconciliationPending, run.ID)
			}
		}
	}
	return nil
}

func (s *Service) ContinueExecution(ctx context.Context, request ContinueExecutionRequest) (execution.ExecutionAttempt, error) {
	if s == nil || s.repositories == nil {
		return execution.ExecutionAttempt{}, fmt.Errorf("planning repositories are required")
	}
	actor := strings.TrimSpace(request.RequestedBy)
	idempotencyKey := strings.TrimSpace(request.IdempotencyKey)
	if request.CleanupTaskID == "" || actor == "" || idempotencyKey == "" {
		return execution.ExecutionAttempt{}, fmt.Errorf("cleanup task, actor, and idempotency key are required")
	}
	concurrency, err := requestedExecutionConcurrency(request.Concurrency)
	if err != nil {
		return execution.ExecutionAttempt{}, err
	}

	var continued execution.ExecutionAttempt
	err = s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		aggregate, err := repositories.CleanupTasks().GetTask(ctx, request.CleanupTaskID)
		if err != nil {
			return err
		}
		if request.ConnectionID != "" && aggregate.Task.ConnectionID != request.ConnectionID {
			return persistence.ErrNotFound
		}
		executions, err := repositories.Executions().ListCleanupTaskExecutions(
			ctx,
			aggregate.Task.ConnectionID,
			string(aggregate.Task.ID),
			persistence.ListOptions{Limit: 500},
		)
		if err != nil {
			return err
		}
		if len(executions.Items) == 0 {
			return fmt.Errorf("%w: cleanup task %q has no execution", ErrExecutionNotContinuable, request.CleanupTaskID)
		}
		attempt := executions.Items[len(executions.Items)-1]
		if attempt.ContinueIdempotencyKey == idempotencyKey {
			if effectiveExecutionConcurrency(attempt) != concurrency {
				return persistence.ErrConflict
			}
			continued = attempt
			return nil
		}
		if err := ensureInventoryReconciled(ctx, repositories.Inventory(), []asset.ConnectionID{aggregate.Task.ConnectionID}); err != nil {
			return err
		}
		if err := ensureContinuationGraphFresh(ctx, repositories, aggregate.Task); err != nil {
			return err
		}
		relationships, err := repositories.Graph().ListRelationshipsByAssetIDs(
			ctx,
			aggregate.Task.ResolvedAssetIDs,
		)
		if err != nil {
			return err
		}
		// Tasks created before created-from relationships participated in
		// deletion ordering have immutable step IDs but may be missing the
		// safety edge. Preserve those IDs while backfilling the edge so both
		// execution and the UI represent the dependent resource as waiting.
		var inferredDependencyCount int
		aggregate.Steps, inferredDependencyCount = appendCreatedFromTaskDependencies(
			aggregate.Steps,
			relationships,
		)
		actions, err := repositories.Executions().ListActions(ctx, attempt.ID)
		if err != nil {
			return err
		}
		actionByStep := make(map[string]execution.ActionAttempt, len(actions))
		for _, action := range actions {
			actionByStep[action.CleanupTaskStepID] = action
		}
		recoveredBlockedExecution := false
		if aggregate.Task.Status != plan.StatusFailed || attempt.Status != execution.ExecutionFailed {
			if aggregate.Task.Status == plan.StatusExecuting &&
				attempt.Status == execution.ExecutionRunning {
				settled, failed := cleanupStepsSettled(aggregate.Steps, actionByStep)
				recoveredBlockedExecution = settled && failed
			}
			if !recoveredBlockedExecution {
				return fmt.Errorf("%w: cleanup task %q is not failed: task=%s execution=%s", ErrExecutionNotContinuable, request.CleanupTaskID, aggregate.Task.Status, attempt.Status)
			}
		}
		if err := s.executionAuthorizer.AuthorizeExecution(ctx, actor, aggregate.Task.ResolvedAssetIDs); err != nil {
			return err
		}
		currentAssets, err := repositories.Inventory().ListAssetsByIDs(
			ctx,
			aggregate.Task.ResolvedAssetIDs,
		)
		if err != nil {
			return err
		}
		aggregate.Task.Warnings = appendSelectionWarnings(
			aggregate.Task.Warnings,
			plan.Input{Assets: currentAssets, Coverage: aggregate.Task.Coverage},
			plan.Result{Steps: aggregate.Steps},
		)
		now := s.clock()
		if err := connectionapp.GuardActiveWork(ctx, repositories, aggregate.Task.ConnectionID, now); err != nil {
			return err
		}

		jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "cleanup_task", string(aggregate.Task.ID))
		if err != nil {
			return err
		}
		activeJobsByStep := make(map[string][]execution.Job, len(jobs))
		for _, job := range jobs {
			if payloadString(job.Payload, "execution_id") != string(attempt.ID) {
				continue
			}
			if job.Status != execution.JobPending && job.Status != execution.JobRunning {
				continue
			}
			stepID := payloadString(job.Payload, "cleanup_task_step_id")
			activeJobsByStep[stepID] = append(activeJobsByStep[stepID], job)
		}

		attempt.Status = execution.ExecutionRunning
		attempt.Concurrency = concurrency
		attempt.ContinueIdempotencyKey = idempotencyKey
		attempt.ContinueCount++
		attempt.FailureReason = ""
		attempt.FinishedAt = nil
		if attempt.StartedAt == nil {
			attempt.StartedAt = &now
		}
		if err := repositories.Executions().UpdateExecution(ctx, attempt); err != nil {
			return err
		}
		aggregate.Task.Status = plan.StatusExecuting
		if inferredDependencyCount > 0 {
			if err := repositories.CleanupTasks().ReplaceTask(
				ctx,
				aggregate.Task,
				aggregate.Steps,
				aggregate.ImpactItems,
			); err != nil {
				return err
			}
		} else if err := repositories.CleanupTasks().UpdateTask(ctx, aggregate.Task); err != nil {
			return err
		}

		resumedAssetIDs := make([]asset.AssetID, 0, len(aggregate.Steps))
		impactItems := append([]plan.ImpactItem(nil), aggregate.ImpactItems...)
		impactItemsChanged := false
		for _, step := range aggregate.Steps {
			action, exists := actionByStep[string(step.ID)]
			retryableSkip := exists && retryableProviderSkip(action)
			managedVerification := exists &&
				(step.Action == plan.ActionVerifyManagedAbsent ||
					managedResourceProviderRejection(action))
			if exists &&
				(action.Status == execution.ActionSucceeded ||
					action.Status == execution.ActionSkipped &&
						!retryableSkip &&
						!managedVerification) {
				continue
			}
			failedAction := exists && action.Status == execution.ActionFailed
			resumedTerminalAction := failedAction || retryableSkip ||
				managedVerification && action.Status == execution.ActionSkipped
			if resumedTerminalAction {
				resumeStatus := execution.ActionInvoking
				if managedVerification {
					resumeStatus = execution.ActionReadingBack
					if action.Request == nil {
						action.Request = make(map[string]any)
					}
					action.Request[plan.ManagedVerificationOnlyRequestKey] = true
					action.Request[plan.ManagedVerificationRetryOnceRequestKey] = true
					if action.DeletionCheckStartedAt == nil {
						startedAt := action.UpdatedAt
						if action.FinishedAt != nil {
							startedAt = *action.FinishedAt
						}
						if startedAt.IsZero() || startedAt.After(now) {
							startedAt = now
						}
						action.DeletionCheckStartedAt = &startedAt
					}
				} else {
					resumeStatus = actionResumeStatus(action)
					action.DeletionCheckStartedAt = nil
				}
				action.Status = execution.ActionPending
				action.ResumeStatus = resumeStatus
				action.ProviderError = nil
				action.SkipReason = ""
				action.FailedFrom = ""
				action.FinishedAt = nil
				action.UpdatedAt = now
				if err := repositories.Executions().UpdateAction(ctx, action); err != nil {
					return err
				}
				for index := range impactItems {
					if !managedVerification &&
						impactItems[index].DelegatedTo == step.ID &&
						(impactItems[index].Result == plan.ImpactCleanupFailed ||
							retryableSkip &&
								impactItems[index].Result == plan.ImpactStillPresent) {
						impactItems[index].Result = ""
						impactItemsChanged = true
					}
				}
			}
			resumedAssetIDs = append(resumedAssetIDs, step.AssetID)
			// The job that persisted a terminal action failure can still be
			// running until its worker records completion. It will not process
			// the reset action again, so a resumed terminal action always needs
			// a new job.
			if !resumedTerminalAction {
				activeJobs := activeJobsByStep[string(step.ID)]
				running := false
				for _, activeJob := range activeJobs {
					if activeJob.Status == execution.JobRunning {
						running = true
						break
					}
				}
				if running {
					continue
				}
				if len(activeJobs) > 0 {
					for _, activeJob := range activeJobs {
						activeJob.RunAt = now
						activeJob.LastError = ""
						activeJob.UpdatedAt = now
						if err := repositories.Jobs().UpdateJob(ctx, activeJob); err != nil {
							return err
						}
					}
					continue
				}
			}
			jobKey := "cleanup-continue-" + deterministicID(idempotencyKey, string(attempt.ID), string(step.ID))
			job := cleanupExecutionJob(ctx, aggregate.Task, attempt, step, jobKey, attempt.ContinueCount, now)
			if err := repositories.Jobs().Enqueue(ctx, job); err != nil {
				return err
			}
		}
		if len(resumedAssetIDs) == 0 {
			return fmt.Errorf("%w: cleanup task %q has no unfinished resources", ErrExecutionNotContinuable, request.CleanupTaskID)
		}
		if impactItemsChanged {
			if err := repositories.CleanupTasks().UpdateImpactItems(ctx, aggregate.Task.ID, impactItems); err != nil {
				return err
			}
		}
		if err := repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: aggregate.Task.ConnectionID, Actor: actor,
			Action: "cleanup.execution.continue", TargetType: "execution_attempt", TargetID: string(attempt.ID), Result: "accepted",
			Evidence: map[string]any{
				"cleanup_task_id": request.CleanupTaskID, "idempotency_key": idempotencyKey,
				"continue_count": attempt.ContinueCount, "concurrency": concurrency, "resumed_asset_ids": resumedAssetIDs,
				"inferred_dependency_count":   inferredDependencyCount,
				"recovered_blocked_execution": recoveredBlockedExecution,
			}, CreatedAt: now,
		}); err != nil {
			return err
		}
		if err := repositories.Executions().AppendOutbox(ctx, execution.OutboxEvent{
			ID: execution.OutboxEventID(idgen.MustNew("evt")), Topic: "execution.continued",
			AggregateID: fmt.Sprintf("%s:continue:%d", attempt.ID, attempt.ContinueCount), Payload: map[string]any{
				"cleanup_task_id": aggregate.Task.ID, "continue_count": attempt.ContinueCount, "concurrency": concurrency,
			}, CreatedAt: now,
		}); err != nil {
			return err
		}
		continued = attempt
		return nil
	})
	return continued, err
}

func ensureContinuationGraphFresh(
	ctx context.Context,
	repositories persistence.Repositories,
	task plan.CleanupTask,
) error {
	assets, err := repositories.Inventory().ListAssetsByIDs(ctx, task.ResolvedAssetIDs)
	if err != nil {
		return err
	}
	if len(assets) != len(task.ResolvedAssetIDs) {
		return fmt.Errorf(
			"%w: cleanup task %q no longer has a complete dependency snapshot; create a new cleanup task",
			ErrExecutionNotContinuable,
			task.ID,
		)
	}
	scopes := make(map[asset.ScopeID]asset.ConnectionID)
	for _, value := range assets {
		if value.ScopeID == "" {
			continue
		}
		if connectionID, exists := scopes[value.ScopeID]; exists && connectionID != value.Identity.ConnectionID {
			return fmt.Errorf(
				"%w: cleanup task %q has inconsistent dependency scopes",
				ErrExecutionNotContinuable,
				task.ID,
			)
		}
		scopes[value.ScopeID] = value.Identity.ConnectionID
	}
	rootScopes, err := resolveRootScopes(ctx, repositories.Inventory(), scopes)
	if err != nil {
		return err
	}
	currentRevision, err := resolveGraphRevision(ctx, repositories.Graph(), rootScopes)
	if err != nil {
		return err
	}
	if currentRevision != task.Revision.GraphRevision {
		return fmt.Errorf(
			"%w: cleanup task %q dependency graph changed from %q to %q; create a new cleanup task",
			ErrExecutionNotContinuable,
			task.ID,
			task.Revision.GraphRevision,
			currentRevision,
		)
	}
	return nil
}

func cleanupExecutionJob(
	ctx context.Context,
	task plan.CleanupTask,
	attempt execution.ExecutionAttempt,
	step plan.CleanupTaskStep,
	idempotencyKey string,
	retryGeneration int,
	now time.Time,
) execution.Job {
	payload := map[string]any{
		"execution_id": string(attempt.ID), "cleanup_task_id": string(task.ID),
		"cleanup_task_step_id": string(step.ID),
	}
	if requestID := requestmeta.RequestID(ctx); requestID != "" {
		payload["request_id"] = requestID
	}
	return execution.Job{
		ID: execution.JobID(idgen.MustNew("job")), ConnectionID: task.ConnectionID,
		IdempotencyKey: idempotencyKey, AggregateType: "cleanup_task", AggregateID: string(task.ID),
		TargetKey: string(step.AssetID), RetryGeneration: retryGeneration,
		Type: execution.JobExecute, Status: execution.JobPending, Payload: payload,
		RunAt: now, CreatedAt: now, UpdatedAt: now,
	}
}

func actionResumeStatus(action execution.ActionAttempt) execution.ActionStatus {
	if retryableProviderSkipNeedsReinvoke(action) {
		// This skip came from a provider call that never reached the delete
		// operation. A request ID belongs to that failed prerequisite query, so
		// it must not be mistaken for an accepted asynchronous deletion.
		return execution.ActionInvoking
	}
	if terminalROSStackInstanceOperationNeedsReinvoke(action) {
		// A terminal StackInstance operation cannot make progress through more
		// polling. Re-enter the action so it reads the StackGroup and its current
		// StackInstances before deciding what remains to delete.
		return execution.ActionInvoking
	}
	if deletionNeverStartedBeforeTimeout(action) {
		// The provider stayed in its ordinary steady state for the entire
		// confirmation window. Re-run the idempotent delete instead of waiting
		// forever on a request that was not accepted as a deletion.
		return execution.ActionInvoking
	}
	switch action.FailedFrom {
	case execution.ActionInvoking, execution.ActionWaiting, execution.ActionReadingBack, execution.ActionReconciling:
		return action.FailedFrom
	}
	if action.ProviderRequestID != "" || action.ProviderOperationID != "" {
		return execution.ActionWaiting
	}
	return execution.ActionInvoking
}

func terminalROSStackInstanceOperationNeedsReinvoke(action execution.ActionAttempt) bool {
	if action.Status != execution.ActionFailed ||
		action.FailedFrom != execution.ActionWaiting ||
		action.ProviderError == nil ||
		strings.TrimSpace(fmt.Sprint(action.ProviderResult["phase"])) != "delete_stack_instances" {
		return false
	}
	switch strings.ToUpper(strings.TrimSpace(action.ProviderError.Code)) {
	case "FAILED", "STOPPED":
		return true
	default:
		return false
	}
}

func resumedProviderIdempotencyKey(
	attempt execution.ExecutionAttempt,
	action execution.ActionAttempt,
) string {
	key := action.IdempotencyKey
	if attempt.ContinueCount <= 0 || action.Status != execution.ActionInvoking ||
		strings.TrimSpace(fmt.Sprint(action.ProviderResult["phase"])) != "delete_stack_instances" {
		return key
	}
	return fmt.Sprintf("%s:continue:%d", key, attempt.ContinueCount)
}

func retryableProviderSkipNeedsReinvoke(action execution.ActionAttempt) bool {
	return action.Status == execution.ActionSkipped &&
		action.SkipReason == string(asset.SkipProductUnsupported) &&
		action.ProviderError != nil &&
		action.ProviderError.Code == "UnsupportedHTTPMethod" &&
		strings.TrimSpace(fmt.Sprint(action.ProviderError.Summary["operation"])) ==
			"AlibabaCloud.NAS.DescribeLifecyclePolicies"
}

func deletionNeverStartedBeforeTimeout(action execution.ActionAttempt) bool {
	if action.Status != execution.ActionFailed || action.ProviderError == nil ||
		action.ProviderError.Code != "DeletionCheckTimeout" {
		return false
	}
	state := strings.ToLower(strings.TrimSpace(
		fmt.Sprint(action.ProviderError.Summary["last_state"]),
	))
	switch state {
	case "active", "available", "normal", "ready", "running":
		return true
	default:
		return false
	}
}

func retryableProviderSkip(action execution.ActionAttempt) bool {
	if action.Status != execution.ActionSkipped || action.ProviderError == nil {
		return false
	}
	if retryableProviderSkipNeedsReinvoke(action) {
		// Compatibility for attempts made by the short-lived catalog revision
		// that incorrectly sent this GET-only NAS operation as POST.
		return true
	}
	if action.SkipReason != string(asset.SkipProviderRegionUnavailable) {
		return false
	}
	message := strings.ToLower(strings.TrimSpace(action.ProviderError.Message))
	return strings.Contains(message, "dial tcp") &&
		strings.Contains(message, "lookup") &&
		strings.Contains(message, "no such host")
}

type resolvedSelection struct {
	SelectionResult
	Connections       []asset.CloudConnection
	Scopes            []asset.Scope
	Assets            []asset.Asset
	Kinds             map[asset.ResourceKindID]asset.ResourceKind
	Bundles           map[asset.Provider]spec.Bundle
	Relationships     []graph.Relationship
	LifecycleBindings []graph.LifecycleBinding
}

func (s *Service) resolveSelection(ctx context.Context, repositories persistence.Repositories, selectors []plan.CleanupSelector) (resolvedSelection, error) {
	connectionIDs := make(map[asset.ConnectionID]struct{})
	groupConnectionIDs := make(map[asset.ConnectionID]struct{})
	seedAssets := make(map[asset.AssetID]asset.Asset)
	canonicalSelectors := append([]plan.CleanupSelector(nil), selectors...)
	for index, selector := range canonicalSelectors {
		switch selector.Kind {
		case plan.SelectorConnection:
			if selector.ConnectionID != "" {
				connectionIDs[selector.ConnectionID] = struct{}{}
			}
		case plan.SelectorScope:
			scope, err := repositories.Inventory().GetScope(ctx, selector.ScopeID)
			if err != nil {
				return resolvedSelection{}, err
			}
			canonicalSelectors[index].ScopeID = scope.ID
			connectionIDs[scope.ConnectionID] = struct{}{}
		case plan.SelectorGroup:
			focus, err := coretopology.ParseFocusKey(selector.GroupKey)
			if err != nil || focus.Kind != coretopology.FocusVPC || selector.ConnectionID == "" {
				return resolvedSelection{}, fmt.Errorf("group selector key is invalid")
			}
			connectionIDs[selector.ConnectionID] = struct{}{}
			groupConnectionIDs[selector.ConnectionID] = struct{}{}
		case plan.SelectorAsset:
			value, err := repositories.Inventory().GetAsset(ctx, selector.AssetID)
			if errors.Is(err, persistence.ErrNotFound) {
				continue
			}
			if err != nil {
				return resolvedSelection{}, err
			}
			seedAssets[value.ID] = value
			connectionIDs[value.Identity.ConnectionID] = struct{}{}
		}
	}

	connectionList := make([]asset.ConnectionID, 0, len(connectionIDs))
	for id := range connectionIDs {
		connectionList = append(connectionList, id)
	}
	sort.Slice(connectionList, func(i, j int) bool { return connectionList[i] < connectionList[j] })
	result := resolvedSelection{
		Kinds:   make(map[asset.ResourceKindID]asset.ResourceKind),
		Bundles: make(map[asset.Provider]spec.Bundle),
	}
	assetValues := seedAssets
	relationships := make(map[string]graph.Relationship)
	bindings := make(map[string]graph.LifecycleBinding)
	for _, connectionID := range connectionList {
		connection, err := repositories.Connections().GetConnection(ctx, connectionID)
		if err != nil {
			return resolvedSelection{}, err
		}
		result.Connections = append(result.Connections, connection)
		scopes, err := repositories.Inventory().ListScopesByConnection(ctx, connectionID)
		if err != nil {
			return resolvedSelection{}, err
		}
		result.Scopes = append(result.Scopes, scopes...)
		values, err := repositories.Inventory().ListActiveAssetsByConnection(ctx, connectionID, "")
		if err != nil {
			return resolvedSelection{}, err
		}
		for _, value := range values {
			assetValues[value.ID] = value
		}
		connectionRelationships, err := repositories.Graph().ListRelationshipsByConnection(ctx, connectionID)
		if err != nil {
			return resolvedSelection{}, err
		}
		for _, relationship := range connectionRelationships {
			relationships[relationshipIdentity(relationship)] = relationship
		}
		connectionBindings, err := repositories.Graph().ListLifecycleBindingsByConnection(ctx, connectionID)
		if err != nil {
			return resolvedSelection{}, err
		}
		for _, binding := range connectionBindings {
			bindings[lifecycleBindingIdentity(binding)] = binding
		}
	}
	for _, value := range assetValues {
		result.Assets = append(result.Assets, value)
	}
	sort.Slice(result.Assets, func(i, j int) bool { return result.Assets[i].ID < result.Assets[j].ID })
	connectionByID := make(map[asset.ConnectionID]asset.CloudConnection, len(result.Connections))
	for _, connection := range result.Connections {
		connectionByID[connection.ID] = connection
	}
	for connectionID := range groupConnectionIDs {
		connection, exists := connectionByID[connectionID]
		if !exists {
			continue
		}
		bundle, loaded := result.Bundles[connection.Provider]
		if !loaded {
			var err error
			bundle, err = s.bundles.Bundle(connection.Provider)
			if err != nil {
				return resolvedSelection{}, err
			}
			result.Bundles[connection.Provider] = bundle
		}
		for _, compiled := range bundle.Specs {
			result.Kinds[compiled.ResourceKind.ID] = compiled.ResourceKind
		}
	}
	for _, relationship := range relationships {
		result.Relationships = append(result.Relationships, relationship)
	}
	sort.Slice(result.Relationships, func(i, j int) bool {
		return relationshipIdentity(result.Relationships[i]) < relationshipIdentity(result.Relationships[j])
	})
	for _, binding := range bindings {
		result.LifecycleBindings = append(result.LifecycleBindings, binding)
	}
	sort.Slice(result.LifecycleBindings, func(i, j int) bool {
		return lifecycleBindingIdentity(result.LifecycleBindings[i]) < lifecycleBindingIdentity(result.LifecycleBindings[j])
	})
	selection, err := ExpandSelectors(SelectionInput{
		Selectors: canonicalSelectors, Connections: result.Connections,
		Scopes: result.Scopes, Assets: result.Assets, Kinds: result.Kinds,
		Relationships: result.Relationships,
	})
	if err != nil {
		return resolvedSelection{}, err
	}
	result.SelectionResult = selection
	return result, nil
}

func (s *Service) loadPlanningInput(ctx context.Context, repositories persistence.Repositories, cleanupTaskID plan.CleanupTaskID, selection resolvedSelection, coverage plan.ScanCoverage, requestOptions map[asset.AssetID]map[string]any) (plan.Input, error) {
	assets, relationships, bindings := lifecycleComponentFromSnapshot(selection.AssetIDs, selection.Assets, selection.Relationships, selection.LifecycleBindings)
	planningBundles, err := s.loadPlanningBundles(assets, selection.Bundles)
	if err != nil {
		return plan.Input{}, err
	}
	assets = refreshPlanningActionability(assets, planningBundles)
	protections := make([]plan.ProtectionPolicy, 0, len(assets))
	for _, value := range assets {
		policy := s.protectionEvaluator(value)
		if policy.AssetID == "" {
			policy.AssetID = value.ID
		}
		if policy.Protected || policy.Source != "" {
			protections = append(protections, policy)
		}
	}
	revision, err := s.resolveRevisions(ctx, repositories, assets, planningBundles)
	if err != nil {
		return plan.Input{}, err
	}
	return plan.Input{
		CleanupTaskID: cleanupTaskID, Selectors: selection.Selectors, ResolvedAssetIDs: selection.AssetIDs, Assets: assets, Relationships: relationships,
		LifecycleBindings: bindings, Protections: protections, Revision: revision, Coverage: coverage, RequestOptions: requestOptions,
	}, nil
}

func (s *Service) loadPlanningBundles(
	assets []asset.Asset,
	preloaded map[asset.Provider]spec.Bundle,
) (map[asset.Provider]spec.Bundle, error) {
	result := make(map[asset.Provider]spec.Bundle, len(preloaded))
	for provider, bundle := range preloaded {
		result[provider] = bundle
	}
	for _, value := range assets {
		provider := value.Identity.Provider
		if provider == "" {
			continue
		}
		if _, exists := result[provider]; exists {
			continue
		}
		bundle, err := s.bundles.Bundle(provider)
		if err != nil {
			return nil, err
		}
		result[provider] = bundle
	}
	return result, nil
}

// Actionability is declared by the current provider spec but persisted on the
// asset projection. Refresh that single static capability while planning so a
// newly supported cleanup action can repair an existing draft task without
// waiting for another inventory scan. Per-resource service-managed suppression
// remains authoritative.
func refreshPlanningActionability(
	values []asset.Asset,
	bundles map[asset.Provider]spec.Bundle,
) []asset.Asset {
	actionableTypes := make(map[asset.Provider]map[string]struct{}, len(bundles))
	for provider, bundle := range bundles {
		for _, compiled := range bundle.Specs {
			if !compiled.ResourceKind.Capabilities.Has(asset.CapabilityActionable) {
				continue
			}
			if actionableTypes[provider] == nil {
				actionableTypes[provider] = make(map[string]struct{})
			}
			actionableTypes[provider][compiled.ResourceKind.NativeType] = struct{}{}
		}
	}
	result := append([]asset.Asset(nil), values...)
	for index := range result {
		value := &result[index]
		if value.Capabilities.Has(asset.CapabilityActionable) ||
			cleanupNormalizedBool(value.Normalized, "_service_managed") {
			continue
		}
		if _, actionable := actionableTypes[value.Identity.Provider][value.Identity.NativeType]; !actionable {
			continue
		}
		value.Capabilities = append(
			append(asset.CapabilitySet(nil), value.Capabilities...),
			asset.CapabilityActionable,
		)
		sort.Slice(value.Capabilities, func(i, j int) bool {
			return value.Capabilities[i] < value.Capabilities[j]
		})
	}
	return result
}

func lifecycleComponentFromSnapshot(selected []asset.AssetID, allAssets []asset.Asset, allRelationships []graph.Relationship, allBindings []graph.LifecycleBinding) ([]asset.Asset, []graph.Relationship, []graph.LifecycleBinding) {
	queue := append([]asset.AssetID(nil), selected...)
	queued := make(map[asset.AssetID]struct{}, len(queue))
	for _, id := range queue {
		queued[id] = struct{}{}
	}
	available := make(map[asset.AssetID]asset.Asset, len(allAssets))
	for _, value := range allAssets {
		available[value.ID] = value
	}
	assetsByID := make(map[asset.AssetID]asset.Asset)
	relationshipsByKey := make(map[string]graph.Relationship)
	bindingsByKey := make(map[string]graph.LifecycleBinding)
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		value, exists := available[id]
		if !exists {
			continue
		}
		assetsByID[id] = value
		for _, relationship := range allRelationships {
			if relationship.SourceAssetID == id || relationship.TargetAssetID == id {
				relationshipsByKey[relationshipIdentity(relationship)] = relationship
			}
		}
		for _, binding := range allBindings {
			if binding.ControllerAssetID != id && binding.ManagedAssetID != id {
				continue
			}
			bindingsByKey[lifecycleBindingIdentity(binding)] = binding
			for _, endpoint := range []asset.AssetID{binding.ControllerAssetID, binding.ManagedAssetID} {
				if endpoint == "" {
					continue
				}
				if _, seen := queued[endpoint]; !seen {
					queued[endpoint] = struct{}{}
					queue = append(queue, endpoint)
				}
			}
		}
	}
	assets := make([]asset.Asset, 0, len(assetsByID))
	for _, value := range assetsByID {
		assets = append(assets, value)
	}
	sort.Slice(assets, func(i, j int) bool { return assets[i].ID < assets[j].ID })
	relationships := make([]graph.Relationship, 0, len(relationshipsByKey))
	for _, value := range relationshipsByKey {
		relationships = append(relationships, value)
	}
	sort.Slice(relationships, func(i, j int) bool {
		return relationshipIdentity(relationships[i]) < relationshipIdentity(relationships[j])
	})
	bindings := make([]graph.LifecycleBinding, 0, len(bindingsByKey))
	for _, value := range bindingsByKey {
		bindings = append(bindings, value)
	}
	sort.Slice(bindings, func(i, j int) bool {
		return lifecycleBindingIdentity(bindings[i]) < lifecycleBindingIdentity(bindings[j])
	})
	return assets, relationships, bindings
}

func (s *Service) resolveSelectionCoverage(
	ctx context.Context,
	repositories persistence.Repositories,
	selection resolvedSelection,
) (plan.ScanCoverage, error) {
	scopeByID := make(map[asset.ScopeID]asset.Scope, len(selection.Scopes))
	for _, scope := range selection.Scopes {
		scopeByID[scope.ID] = scope
	}
	connectionByID := make(map[asset.ConnectionID]asset.CloudConnection, len(selection.Connections))
	for _, connection := range selection.Connections {
		connectionByID[connection.ID] = connection
	}
	var providerDescriptors []contracts.ProviderDescriptor
	providerDescriptorsLoaded := false
	supportsGlobal := func(provider asset.Provider) (bool, bool) {
		if !providerDescriptorsLoaded {
			providerDescriptors = s.bundles.ProviderDescriptors()
			providerDescriptorsLoaded = true
		}
		return contracts.ProviderSupportsRootScope(providerDescriptors, provider, asset.ScopeGlobal)
	}
	requirements := make(map[asset.ConnectionID]*scancoverage.Requirement)
	requirementFor := func(connectionID asset.ConnectionID) *scancoverage.Requirement {
		requirement := requirements[connectionID]
		if requirement == nil {
			requirement = &scancoverage.Requirement{}
			requirements[connectionID] = requirement
		}
		return requirement
	}
	addAuthoritativeScope := func(requirement *scancoverage.Requirement, scopeID asset.ScopeID, failIfUnknown bool) {
		scope, ok := asset.AuthoritativeScope(scopeID, scopeByID)
		if !ok {
			if failIfUnknown {
				requirement.Unprovable = true
			}
			return
		}
		switch scope.Kind {
		case asset.ScopeRegion:
			regionID := strings.TrimSpace(scope.NativeID)
			if regionID == "" {
				regionID = strings.TrimSpace(scope.Location)
			}
			if regionID == "" {
				requirement.Unprovable = true
				return
			}
			requirement.RegionIDs = append(requirement.RegionIDs, regionID)
		case asset.ScopeGlobal:
			requirement.Global = true
		}
	}
	for _, selector := range selection.Selectors {
		if selector.Kind == plan.SelectorAsset {
			continue
		}
		requirement := requirementFor(selector.ConnectionID)
		switch selector.Kind {
		case plan.SelectorConnection:
			connection, exists := connectionByID[selector.ConnectionID]
			if !exists {
				requirement.Unprovable = true
				continue
			}
			global, provable := supportsGlobal(connection.Provider)
			requirement.Global = global
			if !provable {
				requirement.Unprovable = true
			}
			for _, scope := range selection.Scopes {
				if scope.ConnectionID == selector.ConnectionID {
					addAuthoritativeScope(requirement, scope.ID, false)
				}
			}
			for _, value := range selection.Assets {
				if value.ClosedAt == nil && value.Identity.ConnectionID == selector.ConnectionID {
					addAuthoritativeScope(requirement, value.ScopeID, true)
				}
			}
		case plan.SelectorScope:
			allowed := map[asset.ScopeID]bool{selector.ScopeID: true}
			if selector.Descendants {
				for changed := true; changed; {
					changed = false
					for _, scope := range selection.Scopes {
						if !allowed[scope.ID] && allowed[scope.ParentID] {
							allowed[scope.ID] = true
							changed = true
						}
					}
				}
			}
			for scopeID := range allowed {
				addAuthoritativeScope(requirement, scopeID, false)
			}
			for _, value := range selection.Assets {
				if value.ClosedAt == nil && allowed[value.ScopeID] {
					addAuthoritativeScope(requirement, value.ScopeID, true)
				}
			}
		case plan.SelectorGroup:
			focus, err := coretopology.ParseFocusKey(selector.GroupKey)
			if err != nil || focus.Kind != coretopology.FocusVPC || strings.TrimSpace(focus.RegionID) == "" {
				requirement.Unprovable = true
				continue
			}
			requirement.RegionIDs = append(requirement.RegionIDs, strings.TrimSpace(focus.RegionID))
		default:
			requirement.Unprovable = true
		}
	}
	if len(requirements) == 0 {
		return plan.ScanCoverage{Status: "not_required"}, nil
	}
	connectionIDs := make([]asset.ConnectionID, 0, len(requirements))
	for connectionID := range requirements {
		connectionIDs = append(connectionIDs, connectionID)
	}
	sort.Slice(connectionIDs, func(i, j int) bool { return connectionIDs[i] < connectionIDs[j] })
	result := plan.ScanCoverage{Status: "complete"}
	for _, connectionID := range connectionIDs {
		regions, err := repositories.Regions().ListRegionsByConnection(ctx, connectionID)
		if err != nil {
			return plan.ScanCoverage{}, err
		}
		active := scancoverage.ActiveRegionRequirement(regions, connectionID)
		requirement := *requirements[connectionID]
		requirement.RegionIDs = append(requirement.RegionIDs, active.RegionIDs...)
		summary, err := scancoverage.EvaluateConnection(
			ctx, repositories.Inventory(), connectionID, requirement,
		)
		if err != nil {
			return plan.ScanCoverage{}, err
		}
		value := plan.ConnectionCoverage{
			ConnectionID: connectionID, Status: summary.Status,
			FailedShards: summary.FailedShards, LastCompleteScanAt: summary.LastCompleteScanAt,
		}
		if value.Status != "complete" {
			result.Status = "incomplete"
		}
		result.Connections = append(result.Connections, value)
	}
	return result, nil
}

func appendSelectionBlockers(values []plan.Blocker, input plan.Input, solved plan.Result) []plan.Blocker {
	result := append([]plan.Blocker(nil), values...)
	seen := make(map[string]struct{}, len(result))
	for _, blocker := range result {
		seen[selectionBlockerKey(blocker)] = struct{}{}
	}
	add := func(blocker plan.Blocker) {
		key := selectionBlockerKey(blocker)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		result = append(result, blocker)
	}
	rangeSelection := false
	for _, selector := range input.Selectors {
		if selector.Kind != plan.SelectorAsset {
			rangeSelection = true
			break
		}
	}
	selected := make(map[asset.AssetID]struct{}, len(input.ResolvedAssetIDs))
	for _, id := range input.ResolvedAssetIDs {
		selected[id] = struct{}{}
	}
	plannedDeletion := make(map[asset.AssetID]struct{}, len(solved.Steps)+len(solved.ImpactItems))
	for _, step := range solved.Steps {
		plannedDeletion[step.AssetID] = struct{}{}
	}
	for _, impact := range solved.ImpactItems {
		if impact.Expected == plan.ExpectedDelegatedDelete {
			plannedDeletion[impact.AssetID] = struct{}{}
		}
	}
	assetsByID := make(map[asset.AssetID]asset.Asset, len(input.Assets))
	for _, value := range input.Assets {
		assetsByID[value.ID] = value
	}
	for _, relationship := range input.Relationships {
		if relationship.ClosedAt != nil || !ordersCleanupDependency(relationship.Type) {
			continue
		}
		_, targetPlanned := plannedDeletion[relationship.TargetAssetID]
		_, sourcePlanned := plannedDeletion[relationship.SourceAssetID]
		if !targetPlanned || sourcePlanned {
			continue
		}
		_, targetSelected := selected[relationship.TargetAssetID]
		target := assetsByID[relationship.TargetAssetID]
		if !(rangeSelection && targetSelected) && !isNetworkFoundation(target) {
			continue
		}
		add(plan.Blocker{
			Code: plan.BlockCrossScopeDependency, AssetID: relationship.TargetAssetID,
			Message: "resource is required by another asset outside the planned cleanup",
			Evidence: map[string]any{
				"dependent_asset_id":  relationship.SourceAssetID,
				"suggested_asset_ids": []asset.AssetID{relationship.SourceAssetID},
				"relationship_id":     relationship.ID, "relationship_type": relationship.Type,
				"target_native_type": target.Identity.NativeType,
			},
		})
	}
	sort.Slice(result, func(i, j int) bool { return selectionBlockerKey(result[i]) < selectionBlockerKey(result[j]) })
	return result
}

func appendSelectionWarnings(values []plan.Warning, input plan.Input, solved plan.Result) []plan.Warning {
	result := append([]plan.Warning(nil), values...)
	plannedAssets := make(map[asset.AssetID]struct{}, len(solved.Steps))
	for _, step := range solved.Steps {
		plannedAssets[step.AssetID] = struct{}{}
	}
	for _, value := range input.Assets {
		if _, planned := plannedAssets[value.ID]; !planned {
			continue
		}
		switch value.Identity.NativeType {
		case "ACS::ECS::Image":
			if !cleanupNormalizedBool(value.Normalized, "IsPublic") ||
				selectionWarningExists(result, plan.WarningPublicImageMadePrivate, value.ID) {
				continue
			}
			result = append(result, plan.Warning{
				Code:    plan.WarningPublicImageMadePrivate,
				AssetID: value.ID,
				Message: "public custom image will be changed to private before deletion",
				Evidence: map[string]any{
					"operation": "make_image_private",
					"from":      "public",
					"to":        "private",
				},
			})
		case "ACS::ESS::ScalingGroup":
			instanceCount := cleanupNormalizedNumber(value.Normalized, "TotalInstanceCount")
			if instanceCount <= 0 ||
				selectionWarningExists(result, plan.WarningScalingGroupForceDelete, value.ID) {
				continue
			}
			result = append(result, plan.Warning{
				Code:    plan.WarningScalingGroupForceDelete,
				AssetID: value.ID,
				Message: "scaling group instances will be released by force deletion",
				Evidence: map[string]any{
					"operation":      "force_delete_scaling_group",
					"instance_count": instanceCount,
				},
			})
		}
	}
	if input.Coverage.Status == "incomplete" &&
		!selectionWarningExists(result, plan.WarningScanCoverageIncomplete, "") {
		result = append(result, plan.Warning{
			Code:     plan.WarningScanCoverageIncomplete,
			Message:  "cleanup will use the currently discovered resources because scan coverage is incomplete",
			Evidence: map[string]any{"coverage": input.Coverage},
		})
	}
	sort.Slice(result, func(i, j int) bool {
		left := strings.Join([]string{string(result[i].Code), string(result[i].AssetID), string(result[i].ControllerID)}, "\x00")
		right := strings.Join([]string{string(result[j].Code), string(result[j].AssetID), string(result[j].ControllerID)}, "\x00")
		return left < right
	})
	return result
}

func selectionWarningExists(values []plan.Warning, code plan.WarningCode, assetID asset.AssetID) bool {
	for _, warning := range values {
		if warning.Code == code && warning.AssetID == assetID {
			return true
		}
	}
	return false
}

func cleanupNormalizedBool(normalized map[string]any, field string) bool {
	value := any(normalized)
	for _, segment := range []string{"configuration", field} {
		object, ok := value.(map[string]any)
		if !ok {
			value = nil
			break
		}
		value = object[segment]
	}
	if value == nil {
		value = normalized[field]
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true")
	default:
		return false
	}
}

func cleanupNormalizedNumber(normalized map[string]any, field string) float64 {
	value := any(normalized)
	for _, segment := range []string{"configuration", field} {
		object, ok := value.(map[string]any)
		if !ok {
			value = nil
			break
		}
		value = object[segment]
	}
	if value == nil {
		value = normalized[field]
	}
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case json.Number:
		parsed, _ := typed.Float64()
		return parsed
	default:
		return 0
	}
}

func legacyCoverageAdvisoryTask(task plan.CleanupTask) bool {
	if task.Status != plan.StatusDraft || len(task.Blockers) == 0 {
		return false
	}
	for _, blocker := range task.Blockers {
		if blocker.Code != plan.BlockScanCoverageIncomplete {
			return false
		}
	}
	return true
}

func isNetworkFoundation(value asset.Asset) bool {
	switch value.Identity.NativeType {
	case "ACS::VPC::VPC",
		"ACS::VPC::VSwitch",
		"ACS::ALB::LoadBalancer",
		"ACS::NLB::LoadBalancer",
		"ACS::VPN::CustomerGateway",
		"ACS::VPN::VpnGateway",
		"ACS::CEN::TransitRouter",
		"AWS::EC2::VPC",
		"AWS::EC2::Subnet":
		return true
	default:
		return false
	}
}

func ordersCleanupDependency(value graph.RelationshipType) bool {
	switch value {
	case graph.RelationshipDependsOn, graph.RelationshipAttachedTo, graph.RelationshipMemberOf, graph.RelationshipUses, graph.RelationshipRoutesTo:
		return true
	default:
		return false
	}
}

func selectionBlockerKey(value plan.Blocker) string {
	dependentID, _ := value.Evidence["dependent_asset_id"].(asset.AssetID)
	if dependentID == "" {
		if text, ok := value.Evidence["dependent_asset_id"].(string); ok {
			dependentID = asset.AssetID(text)
		}
	}
	return strings.Join([]string{string(value.Code), string(value.AssetID), string(value.ControllerID), string(dependentID), value.Message}, "\x00")
}

func (s *Service) resolveRevisions(
	ctx context.Context,
	repositories persistence.Repositories,
	assets []asset.Asset,
	preloadedBundles map[asset.Provider]spec.Bundle,
) (plan.RevisionBinding, error) {
	revisionAssets := append([]asset.Asset(nil), assets...)
	for index := range revisionAssets {
		// Dirty is a mutable operator annotation evaluated by the execution
		// guard, not a cloud inventory fact that invalidates a cleanup plan.
		revisionAssets[index].Dirty = false
	}
	inventoryRevision, err := digestValue(revisionAssets)
	if err != nil {
		return plan.RevisionBinding{}, fmt.Errorf("build inventory revision: %w", err)
	}
	scopes := make(map[asset.ScopeID]asset.ConnectionID)
	providers := make(map[asset.Provider]struct{})
	for _, value := range assets {
		if value.ScopeID != "" {
			if connectionID, exists := scopes[value.ScopeID]; exists && connectionID != value.Identity.ConnectionID {
				return plan.RevisionBinding{}, fmt.Errorf("scope %s is associated with multiple connections", value.ScopeID)
			}
			scopes[value.ScopeID] = value.Identity.ConnectionID
		}
		if value.Identity.Provider != "" {
			providers[value.Identity.Provider] = struct{}{}
		}
	}
	rootScopes, err := resolveRootScopes(ctx, repositories.Inventory(), scopes)
	if err != nil {
		return plan.RevisionBinding{}, err
	}
	graphRevision, err := resolveGraphRevision(ctx, repositories.Graph(), rootScopes)
	if err != nil {
		return plan.RevisionBinding{}, err
	}
	bundleRevision, specHash, err := resolveBundleRevision(s.bundles, providers, preloadedBundles)
	if err != nil {
		return plan.RevisionBinding{}, err
	}
	return plan.RevisionBinding{
		InventoryRevision: inventoryRevision, GraphRevision: graphRevision,
		SpecBundleRevision: bundleRevision, SpecHash: specHash,
	}, nil
}

func resolveRootScopes(ctx context.Context, repository persistence.InventoryRepository, scopes map[asset.ScopeID]asset.ConnectionID) (map[asset.ScopeID]struct{}, error) {
	roots := make(map[asset.ScopeID]struct{})
	for scopeID, connectionID := range scopes {
		currentID := scopeID
		visited := make(map[asset.ScopeID]struct{})
		for {
			if _, exists := visited[currentID]; exists {
				return nil, fmt.Errorf("scope hierarchy contains a cycle at %s", currentID)
			}
			visited[currentID] = struct{}{}
			current, err := repository.GetScope(ctx, currentID)
			if err != nil {
				return nil, fmt.Errorf("resolve root scope for %s: %w", scopeID, err)
			}
			if current.ConnectionID != connectionID {
				return nil, fmt.Errorf("scope %s belongs to connection %s, expected %s", current.ID, current.ConnectionID, connectionID)
			}
			if current.ParentID == "" {
				roots[current.ID] = struct{}{}
				break
			}
			currentID = current.ParentID
		}
	}
	return roots, nil
}

func resolveGraphRevision(ctx context.Context, repository persistence.GraphRepository, scopes map[asset.ScopeID]struct{}) (string, error) {
	type scopedRevision struct {
		ScopeID  asset.ScopeID `json:"scope_id"`
		Revision string        `json:"revision"`
	}
	values := make([]scopedRevision, 0, len(scopes))
	for scopeID := range scopes {
		revision, err := repository.GetGraphRevision(ctx, scopeID)
		if errors.Is(err, persistence.ErrNotFound) {
			revision = "absent"
		} else if err != nil {
			return "", err
		}
		values = append(values, scopedRevision{ScopeID: scopeID, Revision: revision})
	}
	sort.Slice(values, func(i, j int) bool { return values[i].ScopeID < values[j].ScopeID })
	if len(values) == 1 {
		return values[0].Revision, nil
	}
	return digestValue(values)
}

func resolveBundleRevision(
	resolver BundleResolver,
	providers map[asset.Provider]struct{},
	preloaded map[asset.Provider]spec.Bundle,
) (string, string, error) {
	type providerRevision struct {
		Provider asset.Provider `json:"provider"`
		Revision string         `json:"revision"`
		Hash     string         `json:"hash"`
	}
	values := make([]providerRevision, 0, len(providers))
	for provider := range providers {
		bundle, ok := preloaded[provider]
		if !ok {
			var err error
			bundle, err = resolver.Bundle(provider)
			if err != nil {
				return "", "", err
			}
		}
		if bundle.Provider != provider || strings.TrimSpace(bundle.Revision) == "" || strings.TrimSpace(bundle.Hash) == "" {
			return "", "", fmt.Errorf("compiled bundle for provider %q has invalid identity or revision", provider)
		}
		values = append(values, providerRevision{Provider: provider, Revision: bundle.Revision, Hash: bundle.Hash})
	}
	sort.Slice(values, func(i, j int) bool { return values[i].Provider < values[j].Provider })
	if len(values) == 0 {
		return "absent", "absent", nil
	}
	if len(values) == 1 {
		return values[0].Revision, values[0].Hash, nil
	}
	revision, err := digestValue(values)
	if err != nil {
		return "", "", err
	}
	hashes := make([]struct {
		Provider asset.Provider `json:"provider"`
		Hash     string         `json:"hash"`
	}, 0, len(values))
	for _, value := range values {
		hashes = append(hashes, struct {
			Provider asset.Provider `json:"provider"`
			Hash     string         `json:"hash"`
		}{Provider: value.Provider, Hash: value.Hash})
	}
	hash, err := digestValue(hashes)
	return revision, hash, err
}

func defaultProtectionPolicy(value asset.Asset) plan.ProtectionPolicy {
	protected, reason := normalizedProtection(value.Normalized)
	if !protected {
		for _, key := range []string{"steward/protected", "steward:protected"} {
			if truthy(value.Tags[key]) {
				protected = true
				reason = "asset tag " + key
				break
			}
		}
	}
	if !protected {
		return plan.ProtectionPolicy{AssetID: value.ID}
	}
	return plan.ProtectionPolicy{AssetID: value.ID, Protected: true, Reason: reason, Source: "asset_projection"}
}

func normalizedProtection(normalized map[string]any) (bool, string) {
	for _, key := range []string{"protected", "cleanup_protected"} {
		if value, ok := normalized[key]; ok {
			if protected, ok := value.(bool); ok && protected {
				return true, "normalized field " + key
			}
			if text, ok := value.(string); ok && truthy(text) {
				return true, "normalized field " + key
			}
		}
	}
	return false, ""
}

func truthy(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on", "protected":
		return true
	default:
		return false
	}
}

func normalizedAssetIDs(values []asset.AssetID) []asset.AssetID {
	seen := make(map[asset.AssetID]struct{}, len(values))
	result := make([]asset.AssetID, 0, len(values))
	for _, id := range values {
		id = asset.AssetID(strings.TrimSpace(string(id)))
		if id == "" {
			continue
		}
		if _, duplicate := seen[id]; duplicate {
			continue
		}
		seen[id] = struct{}{}
		result = append(result, id)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func relationshipIdentity(value graph.Relationship) string {
	if value.ID != "" {
		return string(value.ID)
	}
	return strings.Join([]string{string(value.SourceAssetID), string(value.TargetAssetID), string(value.Type), value.Source, value.GraphRevision}, "\x00")
}

func lifecycleBindingIdentity(value graph.LifecycleBinding) string {
	if value.ID != "" {
		return string(value.ID)
	}
	return strings.Join([]string{string(value.ControllerAssetID), string(value.ManagedAssetID), string(value.Authority), string(value.Ownership), string(value.CleanupPolicy), value.EvidenceSource, value.GraphRevision}, "\x00")
}

func copyRequestOptions(value map[asset.AssetID]map[string]any) (map[asset.AssetID]map[string]any, error) {
	if len(value) == 0 {
		return nil, nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("copy cleanup request options: %w", err)
	}
	var copied map[asset.AssetID]map[string]any
	if err := json.Unmarshal(payload, &copied); err != nil {
		return nil, fmt.Errorf("copy cleanup request options: %w", err)
	}
	return copied, nil
}

func digestValue(value any) (string, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func deterministicID(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])
}
