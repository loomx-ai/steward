package plan

import (
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

type CleanupTaskID string
type StepID string
type ImpactItemID string

type Status string

const (
	StatusDraft       Status = "draft"
	StatusReady       Status = "ready"
	StatusInvalidated Status = "invalidated"
	StatusExecuting   Status = "executing"
	StatusPausing     Status = "pausing"
	StatusPaused      Status = "paused"
	StatusCompleted   Status = "completed"
	StatusFailed      Status = "failed"
	StatusCanceled    Status = "canceled"
)

type StepKind string

const (
	StepDirect       StepKind = "direct"
	StepController   StepKind = "controller"
	StepVerification StepKind = "verification"
)

const (
	ActionVerifyManagedAbsent              = "verify_managed_absent"
	ManagedVerificationOnlyRequestKey      = "_managed_verification_only"
	ManagedVerificationRetryOnceRequestKey = "_managed_verification_retry_once"
)

// ControllerDeletionImpliesAbsence identifies resources that are an inseparable
// part of their controller rather than independently managed cloud resources.
// The lifecycle-kind fallback keeps cleanup tasks created before the explicit
// evidence flag compatible with the current execution semantics.
func ControllerDeletionImpliesAbsence(evidence map[string]any) bool {
	if integrated, _ := evidence[graph.LifecycleEvidenceControllerIntegratedResource].(bool); integrated {
		return true
	}
	lifecycleKind, _ := evidence["lifecycle_kind"].(string)
	switch lifecycleKind {
	case "vpc_system_route_table", "cen_transit_router_system_route_table":
		return true
	default:
		return false
	}
}

type ExpectedOutcome string

const (
	ExpectedDelegatedDelete       ExpectedOutcome = "delegated_delete"
	ExpectedRetainShared          ExpectedOutcome = "retain_shared"
	ExpectedRetainExplicit        ExpectedOutcome = "retain_explicit"
	ExpectedProviderDefaultRetain ExpectedOutcome = "provider_default_retain"
	ExpectedUnknown               ExpectedOutcome = "unknown"
)

type BlockCode string

const (
	BlockAssetMissing           BlockCode = "asset_missing"
	BlockAssetClosed            BlockCode = "asset_closed"
	BlockNotActionable          BlockCode = "not_actionable"
	BlockManagedByController    BlockCode = "managed_by_controller"
	BlockLifecycleConflict      BlockCode = "lifecycle_conflict"
	BlockLifecycleCycle         BlockCode = "lifecycle_cycle"
	BlockLifecycleConfidence    BlockCode = "lifecycle_confidence"
	BlockLifecycleAuthority     BlockCode = "lifecycle_authority"
	BlockProtected              BlockCode = "protected"
	BlockDependencyCycle        BlockCode = "dependency_cycle"
	BlockDirectCleanupInvalid   BlockCode = "direct_cleanup_invalid"
	BlockControllerUnavailable  BlockCode = "controller_unavailable"
	BlockScanCoverageIncomplete BlockCode = "scan_coverage_incomplete"
	BlockCrossScopeDependency   BlockCode = "cross_scope_dependency"
)

type Blocker struct {
	Code         BlockCode      `json:"code"`
	AssetID      asset.AssetID  `json:"asset_id,omitempty"`
	ControllerID asset.AssetID  `json:"controller_id,omitempty"`
	Message      string         `json:"message"`
	Evidence     map[string]any `json:"evidence,omitempty"`
}

type WarningCode string

const (
	WarningManagedResourceDirectCleanup WarningCode = "managed_resource_direct_cleanup"
	WarningManagedByControllerSkipped   WarningCode = "managed_by_controller"
	WarningNotActionableSkipped         WarningCode = "not_actionable"
	WarningScanCoverageIncomplete       WarningCode = "scan_coverage_incomplete"
	WarningPublicImageMadePrivate       WarningCode = "pre_delete_image_visibility_change"
	WarningScalingGroupForceDelete      WarningCode = "scaling_group_force_delete"
)

type Warning struct {
	Code         WarningCode    `json:"code"`
	AssetID      asset.AssetID  `json:"asset_id,omitempty"`
	ControllerID asset.AssetID  `json:"controller_id,omitempty"`
	Message      string         `json:"message"`
	Evidence     map[string]any `json:"evidence,omitempty"`
}

type ProtectionPolicy struct {
	AssetID   asset.AssetID  `json:"asset_id"`
	Protected bool           `json:"protected"`
	Reason    string         `json:"reason,omitempty"`
	Source    string         `json:"source"`
	Evidence  map[string]any `json:"evidence,omitempty"`
}

type ImpactResult string

const (
	ImpactDelegated           ImpactResult = "delegated"
	ImpactDeletedByController ImpactResult = "deleted_by_controller"
	ImpactRetainedByPolicy    ImpactResult = "retained_by_policy"
	ImpactRetainedShared      ImpactResult = "retained_shared"
	ImpactStillPresent        ImpactResult = "still_present"
	ImpactCleanupFailed       ImpactResult = "cleanup_failed"
	ImpactIgnoredDirty        ImpactResult = "ignored_dirty"
	ImpactUnknown             ImpactResult = "unknown"
)

type RevisionBinding struct {
	InventoryRevision  string `json:"inventory_revision"`
	GraphRevision      string `json:"graph_revision"`
	SpecBundleRevision string `json:"spec_bundle_revision"`
	SpecHash           string `json:"spec_hash"`
}

type SelectorKind string

const (
	SelectorConnection SelectorKind = "connection"
	SelectorScope      SelectorKind = "scope"
	SelectorGroup      SelectorKind = "group"
	SelectorAsset      SelectorKind = "asset"
)

type CleanupSelector struct {
	Kind         SelectorKind       `json:"kind"`
	ConnectionID asset.ConnectionID `json:"connection_id,omitempty"`
	ScopeID      asset.ScopeID      `json:"scope_id,omitempty"`
	ScopeKind    asset.ScopeKind    `json:"scope_kind,omitempty"`
	GroupKey     string             `json:"group_key,omitempty"`
	AssetID      asset.AssetID      `json:"asset_id,omitempty"`
	Descendants  bool               `json:"descendants,omitempty"`
	DisplayName  string             `json:"display_name,omitempty"`
}

type ConnectionCoverage struct {
	ConnectionID       asset.ConnectionID `json:"connection_id"`
	Status             string             `json:"status"`
	FailedShards       int                `json:"failed_shards"`
	LastCompleteScanAt *time.Time         `json:"last_complete_scan_at,omitempty"`
}

type ScanCoverage struct {
	Status      string               `json:"status"`
	Connections []ConnectionCoverage `json:"connections,omitempty"`
}

type CleanupTask struct {
	ID                 CleanupTaskID                    `json:"id"`
	ConnectionID       asset.ConnectionID               `json:"connection_id"`
	Status             Status                           `json:"status"`
	Selectors          []CleanupSelector                `json:"selectors"`
	ResolvedAssetIDs   []asset.AssetID                  `json:"resolved_asset_ids"`
	SelectorAssetIDs   [][]asset.AssetID                `json:"selector_asset_ids,omitempty"`
	RequestOptions     map[asset.AssetID]map[string]any `json:"request_options,omitempty"`
	Revision           RevisionBinding                  `json:"revision"`
	Coverage           ScanCoverage                     `json:"scan_coverage"`
	SnapshotHash       string                           `json:"snapshot_hash"`
	Blockers           []Blocker                        `json:"blockers,omitempty"`
	Warnings           []Warning                        `json:"warnings,omitempty"`
	CreatedBy          string                           `json:"created_by"`
	CreatedAt          time.Time                        `json:"created_at"`
	UpdatedAt          *time.Time                       `json:"updated_at,omitempty"`
	InvalidationReason string                           `json:"invalidation_reason,omitempty"`
}

type CleanupTaskStep struct {
	ID             StepID         `json:"id"`
	CleanupTaskID  CleanupTaskID  `json:"cleanup_task_id"`
	AssetID        asset.AssetID  `json:"asset_id"`
	Kind           StepKind       `json:"kind"`
	Action         string         `json:"action"`
	RequestOptions map[string]any `json:"request_options"`
	DependsOn      []StepID       `json:"depends_on"`
	Evidence       map[string]any `json:"evidence"`
}

type ImpactItem struct {
	ID                 ImpactItemID        `json:"id"`
	CleanupTaskID      CleanupTaskID       `json:"cleanup_task_id"`
	AssetID            asset.AssetID       `json:"asset_id"`
	ControllerID       asset.AssetID       `json:"controller_id"`
	DelegatedTo        StepID              `json:"delegated_to,omitempty"`
	Ownership          graph.Ownership     `json:"ownership"`
	CleanupPolicy      graph.CleanupPolicy `json:"cleanup_policy"`
	Expected           ExpectedOutcome     `json:"expected"`
	Result             ImpactResult        `json:"result,omitempty"`
	Evidence           map[string]any      `json:"evidence"`
	MayContinueBilling bool                `json:"may_continue_billing"`
}
