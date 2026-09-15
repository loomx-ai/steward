package azure

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// The generic group index is one part of closure, not a replacement for native
// child/service/attachment discovery or product-specific deletion preflight.
func (c *client) deploymentStackGroupIndex(ctx context.Context, req contracts.ActionRequest, group asset.Asset, configurations map[string]any) (map[string]any, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	operation, found := metadata.catalog.Operation("Azure.ResourceManagementClient.Resources_ListByResourceGroup")
	if !found {
		return nil, serviceDenied("missing_deployment_stack_group_list_operation")
	}
	bound, err := catalog.BindREST(operation, map[string]any{"subscriptionId": c.subscription, "resourceGroupName": last(group.Identity.NativeID)})
	if err != nil {
		return nil, err
	}
	collection, _ := url.Parse(bound.URL)
	impacts := map[string]contracts.ActionImpact{}
	for _, impact := range req.LifecycleImpacts {
		impacts[strings.ToLower(impact.Asset.Identity.NativeID)] = impact
	}
	current, err := c.deploymentStackMemberRead(ctx, group)
	if err != nil {
		return nil, err
	}
	if err := c.deploymentStackPreparedMember(group, current.data, nil); err != nil {
		return nil, err
	}
	if protectedAzureTags(object(current.data["tags"])) {
		return nil, serviceDenied("azure_protected_tag")
	}
	if owner := text(current.data["managedBy"]); owner != "" {
		return nil, serviceDenied("deployment_stack_group_has_native_manager")
	}
	snapshot := map[string]any{strings.ToLower(group.Identity.NativeID): current.data}
	seen := map[string]bool{}
	for next := bound.URL; next != ""; {
		if err := rbacListQuery(next, bound.URL); err != nil {
			return nil, err
		}
		page, _ := url.Parse(next)
		pageKey := strings.ToLower(page.Scheme+"://"+page.Host+page.Path) + "?" + page.Query().Encode()
		if seen[pageKey] || len(seen) >= 10000 {
			return nil, serviceDenied("deployment_stack_group_pages_repeated_or_excessive")
		}
		seen[pageKey] = true
		rows, following, response, err := c.listPageResult(ctx, next, collection.Path)
		if err != nil {
			return nil, err
		}
		if operationLocation(response.header) != "" {
			return nil, serviceDenied("incomplete_deployment_stack_group_index")
		}
		for _, row := range rows {
			raw := object(row)
			wire, valid := raw["id"].(string)
			id, kind, err := deploymentStackMemberID(wire)
			if err != nil || !valid || !inResourceGroup(id, group.Identity.NativeID) || !validResponseType(kind, text(raw["type"])) || snapshot[id] != nil {
				return nil, serviceDenied("invalid_deployment_stack_group_resource")
			}
			member := req.Asset
			if !strings.EqualFold(id, req.Asset.Identity.NativeID) {
				impact, found := impacts[id]
				if !found || !impact.Delete || !strings.EqualFold(kind, impact.Asset.Identity.NativeType) {
					return nil, serviceDenied("deployment_stack_group_resource_not_reviewed")
				}
				member = impact.Asset
			}
			live, err := c.deploymentStackMemberRead(ctx, member)
			if err != nil {
				return nil, err
			}
			if err := serviceListedIncarnation(raw, live.data); err != nil {
				return nil, err
			}
			if err := c.deploymentStackPreparedMember(member, live.data, object(configurations[id])); err != nil {
				return nil, err
			}
			if protectedAzureTags(object(live.data["tags"])) {
				return nil, serviceDenied("azure_protected_tag")
			}
			snapshot[id] = live.data
		}
		next = following
	}
	// Known top-level resources still need to be in the generic index. Subresource
	// membership is established by the separate native service child enumerations.
	for id := range impacts {
		if inResourceGroup(id, group.Identity.NativeID) && len(strings.Split(strings.Trim(id, "/"), "/")) == 8 && snapshot[id] == nil {
			return nil, serviceDenied("deployment_stack_group_index_omitted_reviewed_resource")
		}
	}
	final, err := c.deploymentStackMemberRead(ctx, group)
	if err != nil {
		return nil, err
	}
	if c.privateConfiguration(current.data) != c.privateConfiguration(final.data) {
		return nil, serviceDenied("deployment_stack_group_changed_during_read")
	}
	return snapshot, nil
}

// Check every deleted, explicitly managed group twice. Do not silently exclude
// RBAC, diagnostics, other stacks or unknown resources from the reviewed scope.
func (c *client) deploymentStackObserveGroupClosure(ctx context.Context, req contracts.ActionRequest, preparations ...map[string]any) (groups []asset.AssetID, err error) {
	defer func() {
		if err != nil {
			groups = nil
			err = contracts.DependencyReadError(err)
		}
	}()
	configurations, err := c.deploymentStackPreparedConfigurations(req, preparations)
	if err != nil {
		return nil, err
	}
	members := make([]asset.Asset, 0, len(req.LifecycleImpacts))
	for _, impact := range req.LifecycleImpacts {
		members = append(members, impact.Asset)
	}
	if err := c.deploymentStackObserveMemberConfigurations(ctx, req.Asset, members, configurations); err != nil {
		return nil, err
	}
	native := object(object(req.Asset.Normalized[deploymentStackReviewKey])["members"])
	for _, impact := range req.LifecycleImpacts {
		if !impact.Delete || !strings.EqualFold(impact.Asset.Identity.NativeType, groupType) {
			continue
		}
		if native[strings.ToLower(impact.Asset.Identity.NativeID)] == nil {
			return nil, serviceDenied("deployment_stack_group_not_native_member")
		}
		first, err := c.deploymentStackGroupIndex(ctx, req, impact.Asset, configurations)
		if err != nil {
			return nil, err
		}
		second, err := c.deploymentStackGroupIndex(ctx, req, impact.Asset, configurations)
		if err != nil {
			return nil, err
		}
		if c.privateConfiguration(first) != c.privateConfiguration(second) {
			return nil, serviceDenied("deployment_stack_group_members_changed")
		}
		groups = append(groups, impact.Asset.ID)
	}
	if err := c.deploymentStackObserveMemberConfigurations(ctx, req.Asset, members, configurations); err != nil {
		return nil, err
	}
	slices.Sort(groups)
	return groups, nil
}
