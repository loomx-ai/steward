package persistence

import (
	"context"
	"errors"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/finding"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/core/resourcequery"
)

var (
	ErrNotFound      = errors.New("repository object not found")
	ErrConflict      = errors.New("repository object conflicts with existing state")
	ErrInvalidCursor = errors.New("repository cursor is invalid")
)

type ListOptions struct {
	Limit           int
	Cursor          string
	ConnectionID    asset.ConnectionID
	CleanupTaskID   string
	Provider        string
	Query           string
	ResourceQuery   *resourcequery.Expression
	Capability      string
	ResourceKindID  asset.ResourceKindID
	ResourceKindIDs []asset.ResourceKindID
	AssetIDs        []asset.AssetID
	NativeIDs       []string
	AssetCanvas     AssetCanvas
	RegionID        string
	VPCID           string
	SearchOrder     bool
	IncludeClosed   bool
}

type CleanupLogFilter struct {
	ResourceID      string
	ResourceKindIDs []asset.ResourceKindID
}

type AssetCanvas string

const (
	AssetCanvasAccount      AssetCanvas = "account"
	AssetCanvasGlobal       AssetCanvas = "global"
	AssetCanvasRegion       AssetCanvas = "region"
	AssetCanvasRegionPublic AssetCanvas = "region-public"
	AssetCanvasVPC          AssetCanvas = "vpc"
)

type RegionListOptions struct {
	Limit        int
	Cursor       string
	ConnectionID asset.ConnectionID
	Lifecycle    asset.RegionLifecycle
	Query        string
}

type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"`
}

type CleanupTaskAggregate struct {
	Task        plan.CleanupTask       `json:"task"`
	Steps       []plan.CleanupTaskStep `json:"steps"`
	ImpactItems []plan.ImpactItem      `json:"impact_items"`
}

// ScanRunListItem is the persisted read model for the scan collection route.
// It is intentionally complete without consulting scan_shards or jobs.
type ScanRunListItem struct {
	ScanRun              asset.ScanRun
	ResourceCount        int
	DurationMS           int64
	DurationRecorded     bool
	DurationActive       bool
	DurationCalculatedAt *time.Time
	UpdatedAt            time.Time
}

type ConnectionListAggregate struct {
	Connection          asset.CloudConnection
	Credential          asset.ConnectionCredential
	ActiveRegionCount   int
	RetiredRegionCount  int
	ExcludedRegionCount int
	LatestRegionRefresh *execution.Job
}

type ConnectionRepository interface {
	PutConnection(context.Context, asset.CloudConnection) error
	PutConnectionIfUnchanged(context.Context, asset.CloudConnection, time.Time) error
	PutConnectionIfCredentialUnchanged(context.Context, asset.CloudConnection, time.Time, asset.ConnectionCredential) error
	GetConnection(context.Context, asset.ConnectionID) (asset.CloudConnection, error)
	ListConnections(context.Context, ListOptions) (Page[asset.CloudConnection], error)
	ListConnectionAggregates(context.Context, ListOptions) (Page[ConnectionListAggregate], error)
}

type CredentialRepository interface {
	PutCredential(context.Context, asset.ConnectionCredential) error
	PutCredentialIfConnectionUnchanged(context.Context, asset.ConnectionCredential, time.Time, asset.ConnectionCredential) error
	GetCredential(context.Context, asset.ConnectionID) (asset.ConnectionCredential, error)
	DeleteCredential(context.Context, asset.ConnectionID) error
}

type RegionRepository interface {
	PutRegion(context.Context, asset.ConnectionRegion) error
	PutRegionIfUnchanged(context.Context, asset.ConnectionRegion, uint64) error
	GetRegion(context.Context, string) (asset.ConnectionRegion, error)
	ListRegions(context.Context, RegionListOptions) (Page[asset.ConnectionRegion], error)
	ListActiveConnectionRegions(context.Context, RegionListOptions) (Page[asset.ConnectionRegion], error)
	ListRegionsByConnection(context.Context, asset.ConnectionID) ([]asset.ConnectionRegion, error)
	CountRegionsByLifecycle(context.Context, asset.ConnectionID) (map[asset.RegionLifecycle]int, error)
}

type InventoryRepository interface {
	WithinInventoryTx(context.Context, func(InventoryRepository) error) error
	PutScope(context.Context, asset.Scope) error
	GetScope(context.Context, asset.ScopeID) (asset.Scope, error)
	GetScopeByNaturalKey(context.Context, asset.ConnectionID, asset.ScopeKind, string) (asset.Scope, error)
	ConsolidateScopes(context.Context, asset.ScopeID, []asset.ScopeID, asset.ScopeID) error
	ListScopes(context.Context, ListOptions) (Page[asset.Scope], error)
	ListScopesByConnection(context.Context, asset.ConnectionID) ([]asset.Scope, error)
	ListScopesByConnectionIncludingAliases(context.Context, asset.ConnectionID) ([]asset.Scope, error)
	PutResourceKind(context.Context, asset.ResourceKind) error
	GetResourceKind(context.Context, asset.ResourceKindID) (asset.ResourceKind, error)
	CreateScanRun(context.Context, asset.ScanRun) error
	PutScanRun(context.Context, asset.ScanRun) error
	PutScanRunIfControlVersion(context.Context, asset.ScanRun, uint64) error
	GetScanRun(context.Context, asset.ScanRunID) (asset.ScanRun, error)
	ListScanRuns(context.Context, ListOptions) (Page[asset.ScanRun], error)
	ListScanRunListItems(context.Context, ListOptions) (Page[ScanRunListItem], error)
	ListScanRunsByConnection(context.Context, asset.ConnectionID) ([]asset.ScanRun, error)
	PutScanShard(context.Context, asset.ScanShard) error
	GetScanShard(context.Context, asset.ScanShardID) (asset.ScanShard, error)
	ListScanShards(context.Context, ListOptions) (Page[asset.ScanShard], error)
	ListScanShardsByRun(context.Context, asset.ScanRunID) ([]asset.ScanShard, error)
	PutAsset(context.Context, asset.Asset) error
	SetAssetDirty(context.Context, asset.AssetID, bool) (asset.Asset, error)
	GetAsset(context.Context, asset.AssetID) (asset.Asset, error)
	GetAssetByIdentity(context.Context, asset.Identity) (asset.Asset, error)
	ListAssetsByIDs(context.Context, []asset.AssetID) ([]asset.Asset, error)
	ListAssets(context.Context, ListOptions) (Page[asset.Asset], error)
	AppendObservation(context.Context, asset.Observation) error
	ListObservations(context.Context, asset.AssetID) ([]asset.Observation, error)
	ListActiveAssets(context.Context, asset.ScopeID, asset.ResourceKindID) ([]asset.Asset, error)
	ListActiveAssetsByScopes(context.Context, asset.ConnectionID, []asset.ScopeID, asset.ResourceKindID) ([]asset.Asset, error)
	ListActiveAssetsByConnection(context.Context, asset.ConnectionID, asset.ResourceKindID) ([]asset.Asset, error)
	CountActiveAssetsByScope(context.Context, asset.ConnectionID, []asset.ResourceKindID) (map[asset.ScopeID]int, error)
	ListAssetIDsObservedByShard(context.Context, asset.ScanShardID) ([]asset.AssetID, error)
	ListAssetIDsObservedByTarget(context.Context, asset.ConnectionID, string, string, asset.ScopeID, asset.ResourceKindID) ([]asset.AssetID, error)
}

type GraphRepository interface {
	ReplaceGraph(context.Context, asset.ScopeID, string, []graph.Relationship, []graph.LifecycleBinding) error
	CloseAssetTopology(context.Context, asset.AssetID, time.Time) error
	GetGraphRevision(context.Context, asset.ScopeID) (string, error)
	ListRelationships(context.Context, asset.AssetID) ([]graph.Relationship, error)
	ListRelationshipsForAsset(context.Context, asset.ConnectionID, asset.AssetID) ([]graph.Relationship, error)
	ListLifecycleBindings(context.Context, asset.AssetID) ([]graph.LifecycleBinding, error)
	ListLifecycleBindingsForAsset(context.Context, asset.ConnectionID, asset.AssetID) ([]graph.LifecycleBinding, error)
	ListRelationshipsByScope(context.Context, asset.ScopeID) ([]graph.Relationship, error)
	ListLifecycleBindingsByScope(context.Context, asset.ScopeID) ([]graph.LifecycleBinding, error)
	ListRelationshipsByAssetIDs(context.Context, []asset.AssetID) ([]graph.Relationship, error)
	ListLifecycleBindingsByAssetIDs(context.Context, []asset.AssetID) ([]graph.LifecycleBinding, error)
	ListRelationshipsByConnection(context.Context, asset.ConnectionID) ([]graph.Relationship, error)
	ListLifecycleBindingsByConnection(context.Context, asset.ConnectionID) ([]graph.LifecycleBinding, error)
	ListGraphRevisionsByConnection(context.Context, asset.ConnectionID) (map[asset.ScopeID]string, error)
}

type FindingRepository interface {
	WithinFindingTx(context.Context, func(FindingRepository) error) error
	PutFinding(context.Context, finding.Finding) error
	ListFindingsByAsset(context.Context, asset.AssetID) ([]finding.Finding, error)
	ListFindingsForAsset(context.Context, asset.ConnectionID, asset.AssetID) ([]finding.Finding, error)
	CountOpenFindingsByAssetIDs(context.Context, []asset.AssetID) (map[asset.AssetID]int, error)
	ListFindings(context.Context, ListOptions) (Page[finding.Finding], error)
}

type CleanupTaskRepository interface {
	CreateTask(context.Context, plan.CleanupTask, []plan.CleanupTaskStep, []plan.ImpactItem) error
	GetTask(context.Context, plan.CleanupTaskID) (CleanupTaskAggregate, error)
	ListTasks(context.Context, ListOptions) (Page[plan.CleanupTask], error)
	ReplaceTask(context.Context, plan.CleanupTask, []plan.CleanupTaskStep, []plan.ImpactItem) error
	UpdateTask(context.Context, plan.CleanupTask) error
	UpdateImpactItems(context.Context, plan.CleanupTaskID, []plan.ImpactItem) error
}

type ExecutionRepository interface {
	CreateExecution(context.Context, execution.ExecutionAttempt) error
	GetExecution(context.Context, execution.ExecutionID) (execution.ExecutionAttempt, error)
	GetExecutionByIdempotencyKey(context.Context, string) (execution.ExecutionAttempt, error)
	ListExecutions(context.Context, ListOptions) (Page[execution.ExecutionAttempt], error)
	ListCleanupTaskExecutions(context.Context, asset.ConnectionID, string, ListOptions) (Page[execution.ExecutionAttempt], error)
	LockExecution(context.Context, execution.ExecutionID) error
	UpdateExecution(context.Context, execution.ExecutionAttempt) error
	AppendAction(context.Context, execution.ActionAttempt) error
	GetAction(context.Context, execution.ActionAttemptID) (execution.ActionAttempt, error)
	GetActionByExecutionStep(context.Context, execution.ExecutionID, string) (execution.ActionAttempt, error)
	ListActions(context.Context, execution.ExecutionID) ([]execution.ActionAttempt, error)
	ListExecutionActions(context.Context, asset.ConnectionID, execution.ExecutionID) ([]execution.ActionAttempt, error)
	CountInFlightActions(context.Context, execution.ExecutionID) (int, error)
	UpdateAction(context.Context, execution.ActionAttempt) error
	AppendOutbox(context.Context, execution.OutboxEvent) error
	ListPendingOutbox(context.Context, int) ([]execution.OutboxEvent, error)
	MarkOutboxPublished(context.Context, execution.OutboxEventID, time.Time) error
}

type AuditRepository interface {
	AppendAuditEvent(context.Context, execution.AuditEvent) error
	ListAuditEvents(context.Context, ListOptions) (Page[execution.AuditEvent], error)
}

type JobRepository interface {
	Enqueue(context.Context, execution.Job) error
	GetJob(context.Context, execution.JobID) (execution.Job, error)
	GetJobByIdempotencyKey(context.Context, string) (execution.Job, error)
	ListJobsByAggregate(context.Context, string, string) ([]execution.Job, error)
	ListJobsByAggregates(context.Context, string, []string) (map[string][]execution.Job, error)
	UpdateJob(context.Context, execution.Job) error
	FindActiveByType(context.Context, asset.ConnectionID, execution.JobType) (execution.Job, error)
	FindLatestByType(context.Context, asset.ConnectionID, execution.JobType) (execution.Job, error)
	ClaimNext(context.Context, string, time.Time, time.Duration, ...execution.JobType) (execution.Job, error)
	RenewLease(context.Context, execution.JobID, string, time.Time) error
	Reschedule(context.Context, execution.JobID, string, time.Time, string, time.Time) error
	Complete(context.Context, execution.JobID, string, execution.JobStatus, string, time.Time) error
	AppendLog(context.Context, execution.JobLog) error
	ListLogs(context.Context, execution.JobID, int64, int) ([]execution.JobLog, error)
	ListLogsByAggregate(context.Context, string, string, string, time.Time, string, int) ([]execution.JobLog, error)
	ListLogsByAggregateBefore(context.Context, string, string, string, time.Time, string, int) ([]execution.JobLog, error)
	ListScanLogsBefore(context.Context, asset.ConnectionID, asset.ScanTaskID, string, time.Time, string, int) (asset.ScanTask, []execution.JobLog, error)
	ListCleanupLogsAfter(context.Context, asset.ConnectionID, plan.CleanupTaskID, CleanupLogFilter, time.Time, string, int) (plan.CleanupTask, []execution.JobLog, error)
	ListCleanupLogsBefore(context.Context, asset.ConnectionID, plan.CleanupTaskID, CleanupLogFilter, time.Time, string, int) (plan.CleanupTask, []execution.JobLog, error)
}

type Repositories interface {
	Connections() ConnectionRepository
	Credentials() CredentialRepository
	Regions() RegionRepository
	Inventory() InventoryRepository
	Graph() GraphRepository
	Findings() FindingRepository
	CleanupTasks() CleanupTaskRepository
	Executions() ExecutionRepository
	Audits() AuditRepository
	Jobs() JobRepository
	WithTx(context.Context, func(Repositories) error) error
}
