package graph

import (
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
)

type RelationshipID string
type LifecycleBindingID string

type RelationshipType string

const (
	RelationshipDependsOn   RelationshipType = "depends_on"
	RelationshipAttachedTo  RelationshipType = "attached_to"
	RelationshipConnectedTo RelationshipType = "connected_to"
	RelationshipMemberOf    RelationshipType = "member_of"
	RelationshipUses        RelationshipType = "uses"
	RelationshipCreatedFrom RelationshipType = "created_from"
	RelationshipRoutesTo    RelationshipType = "routes_to"
)

type Relationship struct {
	ID            RelationshipID   `json:"id"`
	SourceAssetID asset.AssetID    `json:"source_asset_id"`
	TargetAssetID asset.AssetID    `json:"target_asset_id"`
	Type          RelationshipType `json:"type"`
	Source        string           `json:"source"`
	Evidence      map[string]any   `json:"evidence"`
	Confidence    float64          `json:"confidence"`
	GraphRevision string           `json:"graph_revision"`
	ObservedAt    time.Time        `json:"observed_at"`
	ClosedAt      *time.Time       `json:"closed_at,omitempty"`
}

type Authority string

const (
	AuthorityAuthoritative Authority = "authoritative"
	AuthorityInferred      Authority = "inferred"
)

type Ownership string

const (
	OwnershipExclusive  Ownership = "exclusive"
	OwnershipShared     Ownership = "shared"
	OwnershipReferenced Ownership = "referenced"
	OwnershipUnknown    Ownership = "unknown"
)

type CleanupPolicy string

const (
	CleanupDelegate CleanupPolicy = "delegate"
	CleanupRetain   CleanupPolicy = "retain"
	CleanupDirect   CleanupPolicy = "direct"
	CleanupUnknown  CleanupPolicy = "unknown"
)

const (
	LifecycleEvidenceWaitUntilAbsentBeforeDependents = "wait_until_absent_before_dependents"
	LifecycleEvidenceWaitTimeoutSeconds              = "wait_timeout_seconds"
	LifecycleEvidenceWaitPollSeconds                 = "wait_poll_seconds"
	LifecycleEvidenceControllerDeleteGuaranteed      = "controller_delete_guaranteed"
	LifecycleEvidenceControllerIntegratedResource    = "controller_integrated_resource"
	LifecycleEvidenceUnselectedControllerAction      = "unselected_controller_action"
	LifecycleUnselectedControllerSkip                = "skip"
	RelationshipEvidenceDeletionOrder                = "deletion_order"
	DeletionOrderTargetBeforeSource                  = "target_before_source"
)

type LifecycleBinding struct {
	ID                   LifecycleBindingID `json:"id"`
	ControllerAssetID    asset.AssetID      `json:"controller_asset_id"`
	ManagedAssetID       asset.AssetID      `json:"managed_asset_id"`
	Authority            Authority          `json:"authority"`
	Ownership            Ownership          `json:"ownership"`
	CleanupPolicy        CleanupPolicy      `json:"cleanup_policy"`
	DirectCleanupAllowed bool               `json:"direct_cleanup_allowed,omitempty"`
	EvidenceSource       string             `json:"evidence_source"`
	Evidence             map[string]any     `json:"evidence"`
	Confidence           float64            `json:"confidence"`
	GraphRevision        string             `json:"graph_revision"`
	ObservedAt           time.Time          `json:"observed_at"`
	ClosedAt             *time.Time         `json:"closed_at,omitempty"`
}

type UnresolvedReference struct {
	Provider      asset.Provider     `json:"provider"`
	ConnectionID  asset.ConnectionID `json:"connection_id"`
	NativeType    string             `json:"native_type"`
	NativeID      string             `json:"native_id"`
	ControllerID  asset.AssetID      `json:"controller_id"`
	Relationship  RelationshipType   `json:"relationship"`
	Evidence      map[string]any     `json:"evidence"`
	GraphRevision string             `json:"graph_revision"`
}

type AuthorityResolution struct {
	RequestedAssetID  asset.AssetID      `json:"requested_asset_id"`
	ControllerAssetID asset.AssetID      `json:"controller_asset_id"`
	Chain             []LifecycleBinding `json:"chain"`
}
