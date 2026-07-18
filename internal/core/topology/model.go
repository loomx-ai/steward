package topology

import (
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const MaxResourceLimit = 10_000

type Revision struct {
	Inventory   string    `json:"inventory"`
	Graph       string    `json:"graph"`
	SpecBundle  string    `json:"spec_bundle"`
	ProjectedAt time.Time `json:"projected_at"`
}

type Coverage struct {
	Status             string     `json:"status"`
	LastCompleteScanAt *time.Time `json:"last_complete_scan_at,omitempty"`
	FailedShards       int        `json:"failed_shards"`
}

type Response struct {
	Revision   Revision            `json:"revision"`
	Coverage   Coverage            `json:"coverage"`
	View       View                `json:"view"`
	Warnings   []ProjectionWarning `json:"warnings,omitempty"`
	NextCursor string              `json:"next_cursor,omitempty"`
	Truncated  bool                `json:"truncated"`
}

type View interface {
	topologyView()
}

type ViewKind string

const (
	ViewAccount       ViewKind = "account"
	ViewRegion        ViewKind = "region"
	ViewResourceGraph ViewKind = "resource_graph"
	ViewVPC           ViewKind = "vpc"
)

type CleanupSummary struct {
	Selectable        bool   `json:"selectable"`
	SelectorKind      string `json:"selector_kind,omitempty"`
	SelectorKey       string `json:"selector_key,omitempty"`
	Confirmation      string `json:"confirmation,omitempty"`
	PotentialBlockers int    `json:"potential_blockers"`
}

type EntrySummary struct {
	Key           string         `json:"key"`
	AssetID       asset.AssetID  `json:"asset_id,omitempty"`
	Dirty         bool           `json:"dirty,omitempty"`
	Name          string         `json:"name"`
	NativeID      string         `json:"native_id,omitempty"`
	ResourceCount int            `json:"resource_count"`
	Cleanup       CleanupSummary `json:"cleanup"`
}

type ViewContext struct {
	Key      string `json:"key"`
	Name     string `json:"name"`
	NativeID string `json:"native_id,omitempty"`
}

type AccountView struct {
	Kind            ViewKind       `json:"kind"`
	GlobalResources *EntrySummary  `json:"global_resources,omitempty"`
	Regions         []EntrySummary `json:"regions"`
}

func (AccountView) topologyView() {}

type RegionView struct {
	Kind            ViewKind       `json:"kind"`
	Region          ViewContext    `json:"region"`
	PublicResources EntrySummary   `json:"public_resources"`
	VPCs            []EntrySummary `json:"vpcs"`
}

func (RegionView) topologyView() {}

type ResourceGraphView struct {
	Kind      ViewKind       `json:"kind"`
	Context   ViewContext    `json:"context"`
	Ancestors []ViewContext  `json:"ancestors,omitempty"`
	Resources []Resource     `json:"resources"`
	Edges     []ResourceEdge `json:"edges"`
}

func (ResourceGraphView) topologyView() {}

type VPCView struct {
	Kind               ViewKind       `json:"kind"`
	Region             ViewContext    `json:"region"`
	VPC                ViewContext    `json:"vpc"`
	PublicResourceKeys []string       `json:"public_resource_keys"`
	VSwitches          []VSwitch      `json:"vswitches"`
	Resources          []Resource     `json:"resources"`
	Edges              []ResourceEdge `json:"edges"`
}

func (VPCView) topologyView() {}

type VSwitch struct {
	Key           string   `json:"key"`
	AssetID       string   `json:"asset_id,omitempty"`
	Dirty         bool     `json:"dirty,omitempty"`
	Name          string   `json:"name"`
	NativeID      string   `json:"native_id"`
	Zone          string   `json:"zone,omitempty"`
	ResourceCount int      `json:"resource_count"`
	ResourceKeys  []string `json:"resource_keys"`
}

type Domain string

const (
	DomainNetwork Domain = "network"
	DomainCompute Domain = "compute"
	DomainStorage Domain = "storage"
	DomainUnknown Domain = "unknown"
)

type Resource struct {
	Key               string               `json:"key"`
	AssetID           asset.AssetID        `json:"asset_id"`
	Dirty             bool                 `json:"dirty,omitempty"`
	ResourceKindID    asset.ResourceKindID `json:"resource_kind_id"`
	Name              string               `json:"name"`
	NativeID          string               `json:"native_id"`
	TypeName          string               `json:"type_name"`
	TypeNames         map[string]string    `json:"type_names,omitempty"`
	Icon              string               `json:"icon,omitempty"`
	Class             string               `json:"class,omitempty"`
	ConsoleLinkValues map[string]string    `json:"console_link_values,omitempty"`
	Domain            Domain               `json:"domain"`
	State             string               `json:"state,omitempty"`
	FindingCount      int                  `json:"finding_count"`
	Actionable        bool                 `json:"actionable"`
	MembershipUnknown bool                 `json:"membership_unknown,omitempty"`
	ExternalRelations []ExternalRelation   `json:"external_relations,omitempty"`
	Cleanup           CleanupSummary       `json:"cleanup"`
}

type ProjectedEdgeKind string

const (
	ProjectedRelationship ProjectedEdgeKind = "relationship"
	ProjectedLifecycle    ProjectedEdgeKind = "lifecycle"
)

type ResourceEdge struct {
	Key       string            `json:"key"`
	SourceKey string            `json:"source_key"`
	TargetKey string            `json:"target_key"`
	Kind      ProjectedEdgeKind `json:"kind"`
	Relation  string            `json:"relation"`
	Metadata  map[string]any    `json:"metadata,omitempty"`
}

type ExternalRelation struct {
	Key                  string               `json:"key"`
	Kind                 ProjectedEdgeKind    `json:"kind"`
	Relation             string               `json:"relation"`
	Direction            string               `json:"direction"`
	TargetID             asset.AssetID        `json:"target_id"`
	TargetName           string               `json:"target_name"`
	TargetType           string               `json:"target_type"`
	TargetResourceKindID asset.ResourceKindID `json:"target_resource_kind_id,omitempty"`
	TargetClass          string               `json:"target_class,omitempty"`
}

type ProjectionWarning struct {
	Code           string `json:"code"`
	RelationKey    string `json:"relation_key,omitempty"`
	VisibleAssetID string `json:"visible_asset_id,omitempty"`
	Message        string `json:"message"`
}

type FocusKind string

const (
	FocusAccount       FocusKind = "account"
	FocusAccountGlobal FocusKind = "account_global"
	FocusRegion        FocusKind = "region"
	FocusRegionPublic  FocusKind = "region_public"
	FocusVPC           FocusKind = "vpc"
)

type Focus struct {
	Kind     FocusKind
	RegionID string
	VPCID    string
}

type Input struct {
	Focus                Focus
	Connection           asset.CloudConnection
	Regions              []asset.ConnectionRegion
	Scopes               []asset.Scope
	Assets               []asset.Asset
	AssetCountsByScope   map[asset.ScopeID]int
	Kinds                map[asset.ResourceKindID]asset.ResourceKind
	Relationships        []graph.Relationship
	LifecycleBindings    []graph.LifecycleBinding
	FindingCounts        map[asset.AssetID]int
	ResourceClass        string
	ResourceKindIDs      []asset.ResourceKindID
	ResourceQueryApplied bool
	Risk                 string
	Cursor               string
	Limit                int
	Revision             Revision
	Coverage             Coverage
}
