package contracts

import (
	"context"

	"github.com/loomx-ai/steward/internal/core/asset"
)

type InventoryRequest struct {
	ConnectionID asset.ConnectionID `json:"connection_id"`
	Scope        asset.Scope        `json:"scope"`
	// Source is the server-selected inventory source for this shard. Providers
	// may keep product API discovery metadata for actions and explicit fallback
	// scans while routing supported inventory through a broader native source.
	Source string `json:"source,omitempty"`
	// ResourceKind is nil for a Provider-wide L0 inventory query. A deep or
	// product-specific shard supplies a kind as an explicit server-side filter.
	ResourceKind  *asset.ResourceKind `json:"resource_kind,omitempty"`
	Cursor        string              `json:"cursor,omitempty"`
	Limit         int                 `json:"limit"`
	Options       map[string]any      `json:"options,omitempty"`
	NetworkTarget *asset.ScanTarget   `json:"network_target,omitempty"`
}

// InventoryScope maps one inventory result into the Provider's hierarchy.
// ID, connection, parent, and timestamps are application-owned so Provider
// adapters cannot forge persistence identities.
type InventoryScope struct {
	Kind     asset.ScopeKind `json:"kind"`
	NativeID string          `json:"native_id"`
	Name     string          `json:"name,omitempty"`
	Location string          `json:"location,omitempty"`
}

type InventoryItem struct {
	NativeType   string             `json:"native_type"`
	NativeID     string             `json:"native_id"`
	ResourceKind asset.ResourceKind `json:"resource_kind"`
	// Actionable can only narrow the resource-kind capability for one
	// discovered resource. A nil value inherits the resource kind.
	Actionable        *bool             `json:"actionable,omitempty"`
	Scope             InventoryScope    `json:"scope,omitempty"`
	Name              string            `json:"name,omitempty"`
	State             string            `json:"state,omitempty"`
	Location          string            `json:"location,omitempty"`
	Tags              map[string]string `json:"tags,omitempty"`
	Normalized        map[string]any    `json:"normalized,omitempty"`
	Raw               map[string]any    `json:"raw"`
	NativeAliases     []string          `json:"native_aliases,omitempty"`
	NetworkReferences []string          `json:"network_references,omitempty"`
}

type InventoryBatch struct {
	Items      []InventoryItem `json:"items"`
	NextCursor string          `json:"next_cursor,omitempty"`
	RequestID  string          `json:"request_id,omitempty"`
	Complete   bool            `json:"complete"`
}

type InventoryAdapter interface {
	List(ctx context.Context, request InventoryRequest) (InventoryBatch, error)
}

type InventoryBatchEnricher interface {
	EnrichInventoryBatch(context.Context, InventoryRequest, []InventoryItem) ([]InventoryItem, error)
}

// NetworkTargetQuery is a live Provider API query used by the scan-task
// creation form. Results must not be served from the local inventory database.
type NetworkTargetQuery struct {
	ConnectionID   asset.ConnectionID   `json:"connection_id"`
	Kind           asset.ScanTargetKind `json:"kind"`
	RegionID       string               `json:"region_id"`
	ParentNativeID string               `json:"parent_native_id,omitempty"`
	Query          string               `json:"query,omitempty"`
	Cursor         string               `json:"cursor,omitempty"`
	Limit          int                  `json:"limit"`
}

type NetworkTargetOption struct {
	Kind           asset.ScanTargetKind `json:"kind"`
	RegionID       string               `json:"region_id"`
	NativeID       string               `json:"native_id"`
	Name           string               `json:"name,omitempty"`
	ParentNativeID string               `json:"parent_native_id,omitempty"`
}

type NetworkTargetPage struct {
	Items      []NetworkTargetOption `json:"items"`
	NextCursor string                `json:"next_cursor,omitempty"`
	RequestID  string                `json:"request_id,omitempty"`
}

type NetworkTargetDiscoverer interface {
	SearchNetworkTargets(context.Context, NetworkTargetQuery) (NetworkTargetPage, error)
}
