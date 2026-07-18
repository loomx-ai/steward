package connection

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/credential"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

var (
	ErrIdentityMismatch     = errors.New("replacement credential belongs to a different cloud identity")
	ErrConnectionBusy       = errors.New("connection has running work")
	ErrConfirmationMismatch = errors.New("connection name confirmation does not match")
	ErrInvalidInput         = errors.New("connection input is invalid")
	ErrInvalidSite          = errors.New("connection site is invalid")
	ErrValidationStale      = errors.New("connection credential changed during validation")
	ErrConnectionChanged    = errors.New("connection changed during operation")
)

type Validator interface {
	ValidateConnection(context.Context, asset.Provider, contracts.Credential) (contracts.ConnectionIdentity, error)
	ValidateConnectionSite(asset.Provider, asset.ConnectionSite) error
}

type RegionRefreshQueue interface {
	Enqueue(context.Context, asset.ConnectionID) (execution.Job, error)
}

type Service struct {
	repositories persistence.Repositories
	vault        *credential.Vault
	validator    Validator
	regionQueue  RegionRefreshQueue
	now          func() time.Time
}

type CredentialSummary struct {
	Type      asset.CredentialType `json:"type"`
	ExpiresAt *time.Time           `json:"expires_at,omitempty"`
	UpdatedAt time.Time            `json:"updated_at"`
}

type View struct {
	asset.CloudConnection
	Credential              CredentialSummary        `json:"credential"`
	ActiveRegionCount       int                      `json:"active_region_count"`
	RetiredRegionCount      int                      `json:"retired_region_count"`
	ExcludedRegionCount     int                      `json:"excluded_region_count"`
	LastRegionRefreshAt     *time.Time               `json:"last_region_refresh_at,omitempty"`
	LastRegionRefreshStatus execution.JobStatus      `json:"last_region_refresh_status,omitempty"`
	RegionRefresh           *RegionRefreshJobSummary `json:"region_refresh,omitempty"`
}

type RegionRefreshJobSummary struct {
	JobID     execution.JobID     `json:"job_id,omitempty"`
	Status    execution.JobStatus `json:"status"`
	ErrorCode string              `json:"error_code,omitempty"`
}

type CreateRequest struct {
	Name       string
	Provider   asset.Provider
	Site       asset.ConnectionSite
	Credential contracts.Credential
	Actor      string
}

func NewService(repositories persistence.Repositories, vault *credential.Vault, validator Validator, regionQueue RegionRefreshQueue) (*Service, error) {
	if repositories == nil || vault == nil || validator == nil || regionQueue == nil {
		return nil, fmt.Errorf("connection repositories, credential vault, provider validator, and region refresh queue are required")
	}
	return &Service{repositories: repositories, vault: vault, validator: validator, regionQueue: regionQueue, now: func() time.Time { return time.Now().UTC() }}, nil
}

func (s *Service) List(ctx context.Context, options persistence.ListOptions) (persistence.Page[View], error) {
	aggregates, err := s.repositories.Connections().ListConnectionAggregates(ctx, options)
	if err != nil {
		return persistence.Page[View]{}, err
	}
	result := persistence.Page[View]{Items: make([]View, 0, len(aggregates.Items)), NextCursor: aggregates.NextCursor}
	for _, aggregate := range aggregates.Items {
		item := view(aggregate.Connection, aggregate.Credential)
		item.ActiveRegionCount = aggregate.ActiveRegionCount
		item.RetiredRegionCount = aggregate.RetiredRegionCount
		item.ExcludedRegionCount = aggregate.ExcludedRegionCount
		if aggregate.LatestRegionRefresh != nil {
			latest := aggregate.LatestRegionRefresh
			item.LastRegionRefreshStatus = latest.Status
			updatedAt := latest.UpdatedAt
			item.LastRegionRefreshAt = &updatedAt
		}
		result.Items = append(result.Items, item)
	}
	return result, nil
}

func (s *Service) Create(ctx context.Context, request CreateRequest) (View, error) {
	request.Name = strings.TrimSpace(request.Name)
	if request.Name == "" || request.Provider == "" || request.Credential.Type == "" || len(request.Credential.Values) == 0 {
		return View{}, fmt.Errorf("%w: connection name, provider, credential type, and credential values are required", ErrInvalidInput)
	}
	if err := s.validator.ValidateConnectionSite(request.Provider, request.Site); err != nil {
		return View{}, fmt.Errorf("%w: %v", ErrInvalidSite, err)
	}
	now := s.now()
	value := asset.CloudConnection{
		ID: asset.ConnectionID(idgen.MustNew("con")), Name: request.Name, Provider: request.Provider,
		Site: request.Site, Status: asset.ConnectionUnverified, CreatedAt: now, UpdatedAt: now,
	}
	sealed, err := s.vault.Seal(value.ID, value.Provider, request.Credential, now)
	if err != nil {
		return View{}, err
	}
	err = s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Connections().PutConnection(ctx, value); err != nil {
			return err
		}
		if err := repositories.Credentials().PutCredential(ctx, sealed); err != nil {
			return err
		}
		return repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: value.ID, Actor: request.Actor, Action: "connection.create",
			TargetType: "cloud_connection", TargetID: string(value.ID), Result: "created",
			Evidence: map[string]any{"provider": value.Provider, "credential_type": sealed.Type}, CreatedAt: now,
		})
	})
	if err != nil {
		return View{}, err
	}
	return view(value, sealed), nil
}

func (s *Service) Rename(ctx context.Context, id asset.ConnectionID, name, actor string) (View, error) {
	name = strings.TrimSpace(name)
	if id == "" || name == "" {
		return View{}, fmt.Errorf("connection ID and name are required")
	}
	value, err := s.repositories.Connections().GetConnection(ctx, id)
	if err != nil {
		return View{}, err
	}
	if value.Status == asset.ConnectionDeleted {
		return View{}, persistence.ErrNotFound
	}
	now := s.now()
	expectedUpdatedAt := value.UpdatedAt
	value.Name = name
	value.UpdatedAt = nextConnectionUpdatedAt(now, expectedUpdatedAt)
	sealed, err := s.repositories.Credentials().GetCredential(ctx, id)
	if err != nil {
		return View{}, err
	}
	err = s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Connections().PutConnectionIfCredentialUnchanged(ctx, value, expectedUpdatedAt, sealed); err != nil {
			return err
		}
		return repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: id, Actor: actor, Action: "connection.rename",
			TargetType: "cloud_connection", TargetID: string(id), Result: "updated", CreatedAt: now,
		})
	})
	if err != nil {
		if errors.Is(err, persistence.ErrConflict) {
			return View{}, ErrConnectionChanged
		}
		return View{}, err
	}
	return view(value, sealed), nil
}

func (s *Service) enqueueRegionRefresh(ctx context.Context, connectionID asset.ConnectionID) *RegionRefreshJobSummary {
	job, err := s.regionQueue.Enqueue(ctx, connectionID)
	if err != nil {
		return &RegionRefreshJobSummary{Status: execution.JobFailed, ErrorCode: "region_refresh.enqueue_failed"}
	}
	return &RegionRefreshJobSummary{JobID: job.ID, Status: job.Status}
}

func (s *Service) ReplaceCredential(ctx context.Context, id asset.ConnectionID, replacement contracts.Credential, actor string) (View, error) {
	if replacement.Type == "" || len(replacement.Values) == 0 {
		return View{}, fmt.Errorf("%w: credential type and values are required", ErrInvalidInput)
	}
	value, err := s.repositories.Connections().GetConnection(ctx, id)
	if err != nil {
		return View{}, err
	}
	if value.Status == asset.ConnectionDeleted {
		return View{}, persistence.ErrNotFound
	}
	expectedUpdatedAt := value.UpdatedAt
	busy, err := s.busy(ctx, id)
	if err != nil {
		return View{}, err
	}
	if busy {
		return View{}, ErrConnectionBusy
	}
	now := s.now()
	sealed, err := s.vault.Seal(id, value.Provider, replacement, now)
	if err != nil {
		return View{}, err
	}
	old, err := s.repositories.Credentials().GetCredential(ctx, id)
	if err != nil {
		return View{}, err
	}
	sealed.CreatedAt = old.CreatedAt
	value.Status = asset.ConnectionUnverified
	value.UpdatedAt = nextConnectionUpdatedAt(now, expectedUpdatedAt)
	err = s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Connections().PutConnectionIfCredentialUnchanged(ctx, value, expectedUpdatedAt, old); err != nil {
			return err
		}
		busy, err := connectionBusy(ctx, repositories, id)
		if err != nil {
			return err
		}
		if busy {
			return ErrConnectionBusy
		}
		if err := repositories.Credentials().PutCredential(ctx, sealed); err != nil {
			return err
		}
		return repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: id, Actor: actor, Action: "connection.credential.replace",
			TargetType: "cloud_connection", TargetID: string(id), Result: "updated", Evidence: map[string]any{"credential_type": sealed.Type}, CreatedAt: now,
		})
	})
	if err != nil {
		if errors.Is(err, persistence.ErrConflict) {
			return View{}, ErrConnectionChanged
		}
		return View{}, err
	}
	return view(value, sealed), nil
}

func (s *Service) Validate(ctx context.Context, id asset.ConnectionID, actor string) (View, error) {
	value, err := s.repositories.Connections().GetConnection(ctx, id)
	if err != nil {
		return View{}, err
	}
	if value.Status == asset.ConnectionDeleted {
		return View{}, persistence.ErrNotFound
	}
	wasActive := value.Status == asset.ConnectionActive
	if wasActive {
		busy, err := s.busy(ctx, id)
		if err != nil {
			return View{}, err
		}
		if busy {
			return View{}, ErrConnectionBusy
		}
	}
	expectedUpdatedAt := value.UpdatedAt
	expectedCredential, err := s.repositories.Credentials().GetCredential(ctx, id)
	if err != nil {
		return View{}, err
	}
	resolved, err := s.vault.Resolve(ctx, id)
	if err != nil {
		return View{}, s.recordValidationFailure(ctx, value, expectedUpdatedAt, expectedCredential, actor, err)
	}
	initialCredentialVersion := credential.SnapshotVersion(value, expectedCredential)
	resolved.ConnectionID = value.ID
	resolved.Site = value.Site
	resolved.Version = initialCredentialVersion
	identity, err := s.validator.ValidateConnection(ctx, value.Provider, resolved)
	if err != nil {
		failureCredential, credentialErr := s.credentialAtValidationVersion(
			ctx,
			value,
			expectedCredential,
			identity.CredentialVersion,
		)
		if credentialErr != nil {
			return View{}, credentialErr
		}
		return View{}, s.recordValidationFailure(ctx, value, expectedUpdatedAt, failureCredential, actor, err)
	}
	currentCredential, err := s.repositories.Credentials().GetCredential(ctx, id)
	if err != nil {
		if errors.Is(err, persistence.ErrNotFound) {
			return View{}, ErrValidationStale
		}
		return View{}, err
	}
	validatedCredentialVersion := identity.CredentialVersion
	if validatedCredentialVersion == "" {
		validatedCredentialVersion = initialCredentialVersion
	}
	if credential.SnapshotVersion(value, currentCredential) != validatedCredentialVersion {
		return View{}, ErrValidationStale
	}
	identity.Partition = strings.TrimSpace(identity.Partition)
	identity.TenantID = strings.TrimSpace(identity.TenantID)
	identity.Principal = strings.TrimSpace(identity.Principal)
	if identity.Partition == "" || identity.TenantID == "" || identity.Principal == "" || len(identity.RootScopes) == 0 {
		return View{}, s.recordValidationFailure(
			ctx,
			value,
			expectedUpdatedAt,
			currentCredential,
			actor,
			contracts.NewCredentialValidationError(
				"provider_identity_incomplete",
				"The cloud provider did not return a usable account identity.",
				nil,
			),
		)
	}
	now := s.now()
	scopes, err := rootScopes(value.ID, identity.RootScopes, now)
	if err != nil {
		return View{}, s.recordValidationFailure(ctx, value, expectedUpdatedAt, currentCredential, actor, err)
	}
	matches, err := s.matchesEstablishedIdentity(ctx, value, identity)
	if err != nil {
		return View{}, err
	}
	if !matches {
		return View{}, s.recordValidationFailure(ctx, value, expectedUpdatedAt, currentCredential, actor, ErrIdentityMismatch)
	}
	value.Partition = identity.Partition
	value.TenantID = identity.TenantID
	value.Principal = identity.Principal
	value.Status = asset.ConnectionActive
	value.UpdatedAt = nextConnectionUpdatedAt(now, expectedUpdatedAt)
	err = s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Connections().PutConnectionIfCredentialUnchanged(ctx, value, expectedUpdatedAt, currentCredential); err != nil {
			return err
		}
		if wasActive {
			busy, err := connectionBusy(ctx, repositories, id)
			if err != nil {
				return err
			}
			if busy {
				return ErrConnectionBusy
			}
		}
		for _, scope := range scopes {
			existing, err := repositories.Inventory().GetScopeByNaturalKey(ctx, value.ID, scope.Kind, scope.NativeID)
			switch {
			case err == nil:
				existing.Name = scope.Name
				existing.Location = scope.Location
				existing.UpdatedAt = now
				if err := repositories.Inventory().PutScope(ctx, existing); err != nil {
					return err
				}
			case errors.Is(err, persistence.ErrNotFound):
				if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
					return err
				}
			default:
				return err
			}
		}
		return repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: value.ID, Actor: actor, Action: "connection.validate",
			TargetType: "cloud_connection", TargetID: string(value.ID), Result: "validated",
			Evidence: map[string]any{"provider": value.Provider, "root_scope_count": len(scopes)}, CreatedAt: now,
		})
	})
	if err != nil {
		if errors.Is(err, persistence.ErrConflict) {
			return View{}, ErrValidationStale
		}
		return View{}, err
	}
	current, err := s.repositories.Connections().GetConnection(ctx, id)
	if err != nil {
		return View{}, err
	}
	sealed, err := s.repositories.Credentials().GetCredential(ctx, id)
	if err != nil {
		return View{}, err
	}
	result := view(current, sealed)
	if current.Status == asset.ConnectionActive {
		result.RegionRefresh = s.enqueueRegionRefresh(ctx, current.ID)
	}
	return result, nil
}

func (s *Service) credentialAtValidationVersion(
	ctx context.Context,
	connection asset.CloudConnection,
	initial asset.ConnectionCredential,
	version string,
) (asset.ConnectionCredential, error) {
	if version == "" || version == credential.SnapshotVersion(connection, initial) {
		return initial, nil
	}
	current, err := s.repositories.Credentials().GetCredential(ctx, connection.ID)
	if err != nil {
		if errors.Is(err, persistence.ErrNotFound) {
			return asset.ConnectionCredential{}, ErrValidationStale
		}
		return asset.ConnectionCredential{}, err
	}
	if credential.SnapshotVersion(connection, current) != version {
		return asset.ConnectionCredential{}, ErrValidationStale
	}
	return current, nil
}

func (s *Service) recordValidationFailure(
	ctx context.Context,
	value asset.CloudConnection,
	expectedUpdatedAt time.Time,
	expectedCredential asset.ConnectionCredential,
	actor string,
	validationErr error,
) error {
	now := s.now()
	wasActive := value.Status == asset.ConnectionActive
	value.Status = asset.ConnectionInvalid
	value.UpdatedAt = nextConnectionUpdatedAt(now, expectedUpdatedAt)
	evidence := map[string]any{"provider": value.Provider}
	var providerErr *contracts.ProviderCallError
	var credentialErr *contracts.CredentialValidationError
	switch {
	case errors.As(validationErr, &providerErr):
		safeProvider := contracts.SanitizeProviderError(providerErr.Provider)
		evidence["error_category"] = safeProvider.Category
		evidence["error_code"] = safeProvider.Code
		evidence["error_message"] = safeProvider.Message
		evidence["provider_request_id"] = safeProvider.RequestID
	case errors.As(validationErr, &credentialErr):
		evidence["error_code"] = credentialErr.Code
	case errors.Is(validationErr, ErrIdentityMismatch):
		evidence["error_code"] = "credential_identity_mismatch"
	default:
		evidence["error_code"] = "credential_validation_failed"
	}
	if err := s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Connections().PutConnectionIfCredentialUnchanged(ctx, value, expectedUpdatedAt, expectedCredential); err != nil {
			return err
		}
		if wasActive {
			busy, err := connectionBusy(ctx, repositories, value.ID)
			if err != nil {
				return err
			}
			if busy {
				return ErrConnectionBusy
			}
		}
		return repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: value.ID, Actor: actor, Action: "connection.validate",
			TargetType: "cloud_connection", TargetID: string(value.ID), Result: "failed", Evidence: evidence, CreatedAt: now,
		})
	}); err != nil {
		if errors.Is(err, persistence.ErrConflict) {
			return ErrValidationStale
		}
		return fmt.Errorf("record connection validation failure: %w", err)
	}
	return validationErr
}

func (s *Service) matchesEstablishedIdentity(
	ctx context.Context,
	value asset.CloudConnection,
	identity contracts.ConnectionIdentity,
) (bool, error) {
	if !hasEstablishedIdentity(value) {
		return true, nil
	}
	if value.Partition != "" && value.Partition != identity.Partition {
		return false, nil
	}
	if value.TenantID != "" {
		return value.TenantID == identity.TenantID, nil
	}
	_, err := s.repositories.Inventory().GetScopeByNaturalKey(ctx, value.ID, asset.ScopeAccount, identity.TenantID)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, persistence.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

func hasEstablishedIdentity(value asset.CloudConnection) bool {
	return strings.TrimSpace(value.Partition) != "" ||
		strings.TrimSpace(value.TenantID) != "" ||
		strings.TrimSpace(value.Principal) != ""
}

func (s *Service) Delete(ctx context.Context, id asset.ConnectionID, confirmation, actor string) error {
	value, err := s.repositories.Connections().GetConnection(ctx, id)
	if err != nil {
		return err
	}
	if value.Status == asset.ConnectionDeleted {
		return persistence.ErrNotFound
	}
	if confirmation != value.Name {
		return ErrConfirmationMismatch
	}
	expectedUpdatedAt := value.UpdatedAt
	busy, err := s.busy(ctx, id)
	if err != nil {
		return err
	}
	if busy {
		return ErrConnectionBusy
	}
	sealed, err := s.repositories.Credentials().GetCredential(ctx, id)
	if err != nil {
		return err
	}
	now := s.now()
	value.Status = asset.ConnectionDeleted
	value.DeletedAt = &now
	value.UpdatedAt = nextConnectionUpdatedAt(now, expectedUpdatedAt)
	err = s.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if err := repositories.Connections().PutConnectionIfCredentialUnchanged(ctx, value, expectedUpdatedAt, sealed); err != nil {
			return err
		}
		busy, err := connectionBusy(ctx, repositories, id)
		if err != nil {
			return err
		}
		if busy {
			return ErrConnectionBusy
		}
		if err := repositories.Credentials().DeleteCredential(ctx, id); err != nil {
			return err
		}
		return repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{
			ID: execution.AuditEventID(idgen.MustNew("aud")), ConnectionID: id, Actor: actor, Action: "connection.delete",
			TargetType: "cloud_connection", TargetID: string(id), Result: "deleted", Evidence: map[string]any{"provider": value.Provider}, CreatedAt: now,
		})
	})
	if errors.Is(err, persistence.ErrConflict) {
		return ErrConnectionChanged
	}
	return err
}

func (s *Service) busy(ctx context.Context, id asset.ConnectionID) (bool, error) {
	return connectionBusy(ctx, s.repositories, id)
}

func connectionBusy(ctx context.Context, repositories persistence.Repositories, id asset.ConnectionID) (bool, error) {
	if jobs, ok := repositories.Jobs().(interface {
		HasActiveJobs(context.Context, asset.ConnectionID) (bool, error)
	}); ok {
		active, err := jobs.HasActiveJobs(ctx, id)
		if err != nil || active {
			return active, err
		}
	}
	runs, err := repositories.Inventory().ListScanRunsByConnection(ctx, id)
	if err != nil {
		return false, err
	}
	for _, run := range runs {
		if run.Status == asset.ScanPending || run.Status == asset.ScanRunning {
			return true, nil
		}
	}
	executions, err := repositories.Executions().ListExecutions(ctx, persistence.ListOptions{Limit: 500, ConnectionID: id})
	if err != nil {
		return false, err
	}
	for _, attempt := range executions.Items {
		if executionKeepsConnectionBusy(attempt.Status) {
			return true, nil
		}
	}
	return false, nil
}

func executionKeepsConnectionBusy(status execution.ExecutionStatus) bool {
	switch status {
	case execution.ExecutionPending,
		execution.ExecutionRunning,
		execution.ExecutionWaiting,
		execution.ExecutionReconciling,
		execution.ExecutionPausing,
		execution.ExecutionPaused:
		return true
	default:
		return false
	}
}

func rootScopes(connectionID asset.ConnectionID, candidates []contracts.RootScopeCandidate, now time.Time) ([]asset.Scope, error) {
	values := make([]asset.Scope, 0, len(candidates))
	for _, candidate := range candidates {
		candidate.NativeID = strings.TrimSpace(candidate.NativeID)
		if candidate.Kind == "" || candidate.NativeID == "" {
			return nil, fmt.Errorf("provider returned an invalid root scope")
		}
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			name = candidate.NativeID
		}
		values = append(values, asset.Scope{
			ID: asset.ScopeID(idgen.MustNew("scp")), ConnectionID: connectionID, Kind: candidate.Kind, NativeID: candidate.NativeID,
			Name: name, Location: strings.TrimSpace(candidate.Location), CreatedAt: now, UpdatedAt: now,
		})
	}
	return values, nil
}

func view(connection asset.CloudConnection, sealed asset.ConnectionCredential) View {
	return View{CloudConnection: connection, Credential: CredentialSummary{Type: sealed.Type, ExpiresAt: sealed.ExpiresAt, UpdatedAt: sealed.UpdatedAt}}
}
