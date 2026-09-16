package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const resourceGroupCascadeSource = "azure:resource-group-native-delete"

// Group effects are activated only by selecting the group. They do not replace
// exclusive product ownership or turn independent member selection into group deletion.
func (c *client) contributeResourceGroups(ctx context.Context, connection asset.ConnectionID, assets []asset.Asset) (out governance.Contribution, err error) {
	defer func() {
		if err != nil {
			out = governance.Contribution{}
			err = contracts.DependencyReadError(err)
		}
	}()
	// The service/cluster contributors validate these native controller hints;
	// managed groups must keep their owning product's complete lifecycle.
	managed := managedGroupMembers(assets)
	for _, group := range assets {
		if group.Identity.ConnectionID != connection || group.Identity.Provider != asset.ProviderAzure || group.Identity.NativeType != groupType {
			continue
		}
		if managed[managedGroupKey(group.Identity, group.Identity.NativeID)] || text(group.Normalized["_resource_group_configuration"]) == "" || text(group.Normalized["_managed_group_owner"]) != "" {
			continue
		}
		known := map[string]asset.Asset{}
		for _, member := range assets {
			if member.Identity.Provider != group.Identity.Provider || member.Identity.ConnectionID != group.Identity.ConnectionID || member.Identity.Partition != group.Identity.Partition || !inResourceGroup(member.Identity.NativeID, group.Identity.NativeID) || member.ID == group.ID {
				continue
			}
			id := strings.ToLower(member.Identity.NativeID)
			if _, exists := known[id]; exists {
				return out, serviceDenied("resource_group_graph_duplicate_asset")
			}
			known[id] = member
		}
		var previous map[string]map[string]any
		for pass := 0; pass < 2; pass++ {
			current, err := c.resourceGroupGraphMembers(ctx, group, known)
			if err != nil {
				return out, err
			}
			if pass == 1 && c.privateConfiguration(map[string]any{"members": previous}) != c.privateConfiguration(map[string]any{"members": current}) {
				return out, serviceDenied("resource_group_graph_members_changed")
			}
			previous = current
		}
		for _, id := range slices.Sorted(maps.Keys(previous)) {
			raw := previous[id]
			_, kind, _ := deploymentStackMemberID(id)
			if mapping, ok := findType(kind); ok {
				kind = mapping.NativeType
			}
			evidence := map[string]any{"resource_type": kind, "instance_id": id, "delete_by_default": true, "retention_supported": false, graph.LifecycleEvidenceNativeDeleteEffect: true, graph.LifecycleEvidenceControllerDeleteGuaranteed: true, graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
			member, found := known[id]
			_, registered := findType(kind)
			if !found || !registered || !strings.EqualFold(member.Identity.NativeType, kind) {
				out.Unresolved = append(out.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: group.Identity.Provider, ConnectionID: group.Identity.ConnectionID, NativeID: id, NativeType: kind, ControllerID: group.ID, Relationship: graph.RelationshipMemberOf, Evidence: evidence})
				continue
			}
			if err := c.deploymentStackPreparedMember(member, raw, nil); err != nil {
				return out, err
			}
			// Independent extension prerequisites must retain their own product execution.
			if kind == diagnosticSettingsType || rbacResourceKind(kind) != "" {
				out.Relationships = append(out.Relationships, graph.Relationship{SourceAssetID: group.ID, TargetAssetID: member.ID, Type: graph.RelationshipDependsOn, Source: resourceGroupCascadeSource, Confidence: 1, Evidence: map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: true, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource}})
				continue
			}
			out.Bindings = append(out.Bindings, graph.LifecycleBinding{ControllerAssetID: group.ID, ManagedAssetID: member.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipReferenced, CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: true, EvidenceSource: resourceGroupCascadeSource, Evidence: evidence, Confidence: 1})
			out.Relationships = append(out.Relationships, graph.Relationship{SourceAssetID: member.ID, TargetAssetID: group.ID, Type: graph.RelationshipMemberOf, Source: resourceGroupCascadeSource, Evidence: evidence, Confidence: 1})
		}
	}
	return out, nil
}

// Preserve unknown members as unresolved graph references. Known resources use
// their own native reads, and Monitor's product indexes supplement ARM omissions.
func (c *client) resourceGroupGraphMembers(ctx context.Context, group asset.Asset, known map[string]asset.Asset) (map[string]map[string]any, error) {
	readGroup := func() error {
		live, err := c.deploymentStackMemberRead(ctx, group)
		if err != nil {
			return err
		}
		if text(group.Normalized["_resource_group_configuration"]) != c.privateConfiguration(live.data) || text(live.data["managedBy"]) != "" {
			return serviceDenied("resource_group_graph_review_changed")
		}
		return nil
	}
	if err := readGroup(); err != nil {
		return nil, err
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	operation, ok := metadata.catalog.Operation("Azure.ResourceManagementClient.Resources_ListByResourceGroup")
	if !ok {
		return nil, serviceDenied("resource_group_graph_operation_missing")
	}
	bound, err := catalog.BindREST(operation, map[string]any{"subscriptionId": c.subscription, "resourceGroupName": last(group.Identity.NativeID)})
	if err != nil {
		return nil, err
	}
	collection, _ := url.Parse(bound.URL)
	values := map[string]map[string]any{}
	pages := map[string]bool{}
	for next := bound.URL; next != ""; {
		if err := rbacListQuery(next, bound.URL); err != nil {
			return nil, err
		}
		page, _ := url.Parse(next)
		key := strings.ToLower(page.Scheme+"://"+page.Host+page.Path) + "?" + page.Query().Encode()
		if pages[key] || len(pages) >= 10000 {
			return nil, serviceDenied("resource_group_graph_repeated_page")
		}
		pages[key] = true
		rows, following, res, err := c.listPageResult(ctx, next, collection.Path)
		if err != nil {
			return nil, err
		}
		if operationLocation(res.header) != "" {
			return nil, serviceDenied("resource_group_graph_incomplete_index")
		}
		for _, value := range rows {
			raw := object(value)
			id, kind, err := deploymentStackMemberID(text(raw["id"]))
			if err != nil || !inResourceGroup(id, group.Identity.NativeID) || id == strings.ToLower(group.Identity.NativeID) || !validResponseType(kind, text(raw["type"])) || values[id] != nil {
				return nil, serviceDenied("resource_group_graph_invalid_member")
			}
			_, registered := findType(kind)
			if member, found := known[id]; found && registered {
				if !strings.EqualFold(member.Identity.NativeType, kind) {
					return nil, serviceDenied("resource_group_graph_kind_changed")
				}
				live, err := c.deploymentStackMemberRead(ctx, member)
				if err != nil {
					return nil, err
				}
				if err := serviceListedIncarnation(raw, live.data); err != nil {
					return nil, err
				}
				raw = live.data
			} else if monitorResourceKind(kind) != "" {
				live, err := c.monitorResourceRead(ctx, monitorResourceKind(kind), id)
				if err != nil {
					return nil, err
				}
				raw = live.data
			}
			values[id] = raw
		}
		next = following
	}
	extra, err := c.monitorManagedGroupMembers(ctx, strings.ToLower(group.Identity.NativeID), values)
	if err != nil {
		return nil, err
	}
	for _, raw := range extra {
		id := strings.ToLower(text(raw["id"]))
		_, _, kind, err := monitorResourceID(id)
		if err != nil {
			return nil, err
		}
		live, err := c.monitorResourceRead(ctx, kind, id)
		if err != nil {
			return nil, err
		}
		if c.privateConfiguration(monitorResourceSnapshot(kind, raw)) != c.privateConfiguration(monitorResourceSnapshot(kind, live.data)) {
			return nil, serviceDenied("resource_group_graph_monitor_changed")
		}
		values[id] = live.data
	}
	for id := range known {
		if len(strings.Split(strings.Trim(id, "/"), "/")) == 8 && values[id] == nil {
			return nil, serviceDenied("resource_group_graph_index_omitted_member")
		}
	}
	if err := readGroup(); err != nil {
		return nil, err
	}
	return values, nil
}
