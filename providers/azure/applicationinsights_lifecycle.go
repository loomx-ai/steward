package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const insightsLifecycleSource = "azure:application-insights-native-children"

func insightsComponentChildKinds() []string {
	return []string{insightsAnalyticsType, insightsMyAnalyticsType, insightsExportType, insightsFavoriteType, insightsWorkItemType, insightsAPIKeyType, insightsLinkedStorageType}
}

// These seven collections have complete native indexes (or a fixed singleton
// GET) and independent DELETEs. Annotations have a bounded time-window API and
// are not an authoritative all-history collection here.
func (c *client) insightsComponentChildren(ctx context.Context, parent string) ([]serviceChild, error) {
	read := func() ([]serviceChild, error) {
		var children []serviceChild
		for _, kind := range insightsComponentChildKinds() {
			current, err := c.insightsChildren(ctx, parent, kind)
			if err != nil {
				return nil, err
			}
			children = append(children, current...)
		}
		slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
		return children, nil
	}
	first, err := read()
	if err != nil {
		return nil, err
	}
	second, err := read()
	if err != nil {
		return nil, err
	}
	if !slices.EqualFunc(first, second, func(a, b serviceChild) bool {
		return a.id == b.id && a.kind == b.kind && c.insightsChildConfiguration(a.id, a.kind, a.data) == c.insightsChildConfiguration(b.id, b.kind, b.data)
	}) {
		return nil, serviceDenied("insights_component_children_changed")
	}
	return second, nil
}

func (c *client) insightsComponentIncarnation(parent asset.Asset, raw map[string]any) error {
	id, kind, err := parseID(parent.Identity.NativeID)
	if err != nil || id != parent.Identity.NativeID || parent.ID == "" || !strings.EqualFold(kind, applicationInsightsType) || parent.Identity.NativeType != applicationInsightsType || parent.Identity.Provider != asset.ProviderAzure || parent.Identity.ConnectionID == "" || parent.Identity.Partition == "" || !strings.HasPrefix(id, c.root()+"/") || parent.Location != resourceRegion(raw) {
		return serviceDenied("invalid_insights_component_identity")
	}
	if expected := text(parent.Normalized["_monitor_private_link_target_configuration"]); expected == "" || expected != c.privateConfiguration(monitorPrivateLinkTargetSnapshot(raw)) {
		return serviceDenied("insights_component_configuration_changed")
	}
	return nil
}

// Match legacy native URLs exactly: OpaqueID and opaqueid are distinct API
// selectors. Only the ARM parent and fixed route segments are canonicalized.
func insightsChildAsset(parent asset.Asset, child serviceChild, assets []asset.Asset) (*asset.Asset, error) {
	var target *asset.Asset
	for i := range assets {
		candidate := &assets[i]
		if candidate.Identity.Provider != parent.Identity.Provider || candidate.Identity.ConnectionID != parent.Identity.ConnectionID || candidate.Identity.Partition != parent.Identity.Partition || candidate.Identity.NativeID != child.id {
			continue
		}
		if target != nil || candidate.ID == "" || candidate.ID == parent.ID || candidate.Identity.NativeType != child.kind {
			return nil, serviceDenied("ambiguous_insights_component_child")
		}
		target = candidate
	}
	return target, nil
}

func (c *client) contributeInsightsChildren(ctx context.Context, parent asset.Asset, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	before, err := c.insightsComponent(ctx, parent.Identity.NativeID)
	if err != nil {
		return result, err
	}
	if err := c.insightsComponentIncarnation(parent, before); err != nil {
		return result, err
	}
	children, err := c.insightsComponentChildren(ctx, parent.Identity.NativeID)
	if err != nil {
		return result, err
	}
	indexed := map[string]bool{}
	for _, child := range children {
		indexed[child.id] = true
		evidence := map[string]any{"resource_type": child.kind, "instance_id": child.id, "delete_by_default": true, "retention_supported": false}
		target, err := insightsChildAsset(parent, child, assets)
		if err != nil {
			return result, err
		}
		if target == nil {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: child.kind, NativeID: child.id, ControllerID: parent.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
			continue
		}
		if target.Location != parent.Location || text(target.Normalized["_insights_component"]) != parent.Identity.NativeID || target.Normalized["_insights_component_configuration"] != parent.Normalized["_monitor_private_link_target_configuration"] || text(target.Normalized[insightsChildProofKey(child.kind)]) != c.insightsChildConfiguration(child.id, child.kind, child.data) {
			return result, serviceDenied("insights_component_child_configuration_changed")
		}
		// The component cannot stand in for this child DELETE or its readback.
		// The existing native leaf driver verifies the exact item after deletion.
		directAllowed := target.Normalized["cleanup_protected"] != true && target.Normalized["cleanup_controller_only"] != true
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: parent.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDirect, DirectCleanupAllowed: directAllowed, EvidenceSource: insightsLifecycleSource, Evidence: evidence, Confidence: 1})
		result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: parent.ID, Type: graph.RelationshipAttachedTo, Source: insightsLifecycleSource, Evidence: evidence, Confidence: 1})
	}
	for _, value := range assets {
		if value.Identity.Provider != parent.Identity.Provider || value.Identity.ConnectionID != parent.Identity.ConnectionID || value.Identity.Partition != parent.Identity.Partition || !slices.Contains(insightsComponentChildKinds(), value.Identity.NativeType) {
			continue
		}
		id, owner, kind, _, err := insightsChildIdentity(value.Identity.NativeID)
		if err != nil || id != value.Identity.NativeID || kind != value.Identity.NativeType {
			return result, serviceDenied("invalid_indexed_insights_child_identity")
		}
		if owner != parent.Identity.NativeID || indexed[id] {
			continue
		}
		mapping, _ := findType(kind)
		if _, err := c.insightsChildRead(ctx, mapping, id); !isNotFound(err) {
			if err != nil {
				return result, err
			}
			return result, serviceDenied("insights_child_missing_from_native_index")
		}
	}
	after, err := c.insightsComponent(ctx, parent.Identity.NativeID)
	if err != nil {
		return result, err
	}
	if err := c.insightsComponentIncarnation(parent, after); err != nil {
		return result, err
	}
	return result, nil
}
