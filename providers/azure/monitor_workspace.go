package azure

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const monitorWorkspaceType = "Microsoft.Monitor/accounts"
const monitorWorkspaceSource = "azure:monitor-workspace-managed-resource-group"

func controllerResourceGroup(subscription, kind string, properties map[string]any) (string, error) {
	if strings.EqualFold(kind, aksType) {
		return aksNodeGroup(subscription, properties)
	}
	if !strings.EqualFold(kind, monitorWorkspaceType) {
		return "", serviceDenied("unknown_managed_group_controller")
	}
	defaults := object(properties["defaultIngestionSettings"])
	group := ""
	for _, target := range []struct{ field, kind string }{{"dataCollectionRuleResourceId", dataCollectionRuleType}, {"dataCollectionEndpointResourceId", dataCollectionEndpointType}} {
		id, actual, err := parseID(text(defaults[target.field]))
		if err != nil || !strings.EqualFold(actual, target.kind) || !strings.HasPrefix(id, "/subscriptions/"+strings.ToLower(subscription)+"/") {
			return "", serviceDenied("invalid_monitor_workspace_ingestion_scope")
		}
		current := strings.Join(strings.Split(id, "/")[:5], "/")
		if group != "" && group != current {
			return "", serviceDenied("monitor_workspace_ingestion_groups_disagree")
		}
		group = current
	}
	return group, nil
}

func monitorWorkspaceConfiguration(raw map[string]any) string {
	safe := safePayload(raw)
	delete(object(safe["properties"]), "provisioningState")
	return serviceParentConfiguration(monitorWorkspaceType, safe)
}

func monitorWorkspaceIncarnation(planned asset.Asset, live map[string]any) error {
	if !strings.EqualFold(planned.Identity.NativeType, monitorWorkspaceType) {
		return nil
	}
	if expected := text(planned.Normalized["_monitor_workspace_configuration"]); expected == "" || expected != monitorWorkspaceConfiguration(live) {
		return serviceDenied("monitor_workspace_configuration_changed")
	}
	return nil
}

// Read-only native default-ingestion IDs locate the group; any managedBy value
// must agree. The documented workspace DELETE removes this entire resource group.
func (c *client) monitorWorkspaceResources(ctx context.Context, parent asset.Asset, raw map[string]any) ([]map[string]any, []serviceChild, error) {
	group, err := controllerResourceGroup(c.subscription, monitorWorkspaceType, object(raw["properties"]))
	if err != nil || inResourceGroup(parent.Identity.NativeID, group) {
		return nil, nil, serviceDenied("invalid_monitor_workspace_managed_group")
	}
	first, err := c.managedGroupResources(ctx, parent.Identity.NativeID, group)
	if err != nil {
		return nil, nil, err
	}
	second, err := c.managedGroupResources(ctx, parent.Identity.NativeID, group)
	if err != nil {
		return nil, nil, err
	}
	order := func(a, b map[string]any) int {
		return strings.Compare(strings.ToLower(text(a["id"])), strings.ToLower(text(b["id"])))
	}
	slices.SortFunc(first, order)
	slices.SortFunc(second, order)
	if !slices.EqualFunc(first, second, func(a, b map[string]any) bool {
		return order(a, b) == 0 && productGeneration(a) == productGeneration(b)
	}) {
		return nil, nil, serviceDenied("monitor_workspace_group_members_changed")
	}
	for _, field := range []string{"dataCollectionRuleResourceId", "dataCollectionEndpointResourceId"} {
		id := strings.ToLower(text(object(object(raw["properties"])["defaultIngestionSettings"])[field]))
		if slices.ContainsFunc(second, func(value map[string]any) bool { return strings.EqualFold(text(value["id"]), id) }) {
			continue
		}
		_, nativeType, _ := parseID(id)
		kind, _ := findType(nativeType)
		endpoint, err := c.resourceURL(kind, id)
		if err != nil {
			return nil, nil, err
		}
		if _, err := c.request(ctx, "GET", endpoint); !isNotFound(err) {
			if err != nil {
				return nil, nil, err
			}
			return nil, nil, serviceDenied("monitor_workspace_default_missing_from_group")
		}
	}
	links := []serviceChild{}
	for _, resource := range second {
		id, kind, _ := parseID(text(resource["id"]))
		if !strings.EqualFold(kind, dataCollectionRuleType) && !strings.EqualFold(kind, dataCollectionEndpointType) {
			continue
		}
		children, err := c.dataCollectionAssociations(ctx, asset.Identity{NativeID: id, NativeType: kind})
		if err != nil {
			return nil, nil, err
		}
		for _, child := range children {
			if inResourceGroup(child.id, group) {
				continue
			}
			previous := slices.IndexFunc(links, func(value serviceChild) bool { return value.id == child.id })
			if previous >= 0 {
				if productGeneration(links[previous].data) != productGeneration(child.data) {
					return nil, nil, serviceDenied("monitor_workspace_shared_association_changed")
				}
				continue
			}
			links = append(links, child)
		}
	}
	if err := c.verifyProductParent(ctx, productTarget{ParentID: parent.Identity.NativeID, ParentType: monitorWorkspaceType, Generation: productGeneration(raw)}); err != nil {
		return nil, nil, err
	}
	return second, links, nil
}

func (c *client) contributeMonitorWorkspace(ctx context.Context, parent asset.Asset, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	kind, _ := findType(monitorWorkspaceType)
	endpoint, err := c.resourceURL(kind, parent.Identity.NativeID)
	if err != nil {
		return result, err
	}
	live, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return result, err
	}
	if !validResourceResponse(live, parent.Identity.NativeID, monitorWorkspaceType) {
		return result, serviceDenied("monitor_workspace_identity_changed")
	}
	if err := serviceIncarnation(parent, live.data); err != nil {
		return result, err
	}
	_, links, err := c.monitorWorkspaceResources(ctx, parent, live.data)
	if err != nil {
		return result, err
	}
	group, _ := controllerResourceGroup(c.subscription, monitorWorkspaceType, object(live.data["properties"]))
	result, err = c.contributeManagedGroup(ctx, parent, live, group, monitorWorkspaceSource, assets)
	if err != nil {
		return result, err
	}
	for _, child := range links {
		evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "resource_type": child.kind, "instance_id": child.id}
		var target *asset.Asset
		for i := range assets {
			candidate := &assets[i]
			if candidate.Identity.Provider == parent.Identity.Provider && candidate.Identity.ConnectionID == parent.Identity.ConnectionID && candidate.Identity.Partition == parent.Identity.Partition && strings.EqualFold(candidate.Identity.NativeID, child.id) && strings.EqualFold(candidate.Identity.NativeType, child.kind) {
				if target != nil {
					return result, serviceDenied("ambiguous_monitor_workspace_association")
				}
				target = candidate
			}
		}
		if target == nil {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: child.kind, NativeID: child.id, ControllerID: parent.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
			continue
		}
		if err := serviceIncarnation(*target, child.data); err != nil {
			return result, err
		}
		result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: parent.ID, TargetAssetID: target.ID, Type: graph.RelationshipDependsOn, Source: monitorWorkspaceSource, Evidence: evidence, Confidence: 1})
	}
	return result, nil
}

func monitorWorkspaceAssociation(parent, child asset.Asset) bool {
	if !strings.EqualFold(parent.Identity.NativeType, monitorWorkspaceType) || !strings.EqualFold(child.Identity.NativeType, dataCollectionAssociationType) {
		return false
	}
	parts := strings.Split(parent.Identity.NativeID, "/")
	if len(parts) < 3 {
		return false
	}
	group, err := controllerResourceGroup(parts[2], monitorWorkspaceType, parent.Normalized)
	return err == nil && !inResourceGroup(child.Identity.NativeID, group) && slices.ContainsFunc(dataCollectionReferences(map[string]any{"properties": child.Normalized}), func(id string) bool { return inResourceGroup(id, group) })
}

func (a *action) monitorWorkspacePreflight(ctx context.Context, request contracts.ActionRequest, raw map[string]any, locks []any) error {
	if err := a.servicePrerequisitesAbsent(ctx, request); err != nil {
		return err
	}
	if err := monitorWorkspaceIncarnation(request.Asset, raw); err != nil {
		return err
	}
	_, links, err := a.client.monitorWorkspaceResources(ctx, request.Asset, raw)
	if err != nil {
		return err
	}
	if len(links) > 0 {
		return serviceDenied("monitor_workspace_requires_association_unlink")
	}
	if len(request.PrerequisiteDeletions) > 0 {
		request.Asset.Normalized = cloneNormalizedWithoutGeneration(request.Asset.Normalized)
	}
	reason, err := a.managedGroupPreflight(ctx, request, raw, locks)
	if err != nil {
		return err
	}
	if reason != "" {
		return serviceDenied(strings.ReplaceAll(reason, "aks_", "monitor_workspace_"))
	}
	return nil
}

func (a *action) monitorWorkspaceReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.servicePrerequisitesAbsent(ctx, request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	result, err := a.managedGroupReadback(ctx, request)
	if result.Exists {
		result.State = "deleting_monitor_workspace_resources"
	}
	return result, err
}

func validateMonitorConnections(id string, properties map[string]any) error {
	if properties["privateEndpointConnections"] == nil {
		return nil
	}
	values, ok := properties["privateEndpointConnections"].([]any)
	if !ok {
		return fmt.Errorf("invalid Monitor workspace private connection configuration")
	}
	seen := map[string]bool{}
	for _, value := range values {
		connection := object(value)
		child, kind, err := parseID(text(connection["id"]))
		if err != nil || !strings.EqualFold(kind, monitorWorkspaceType+"/privateEndpointConnections") || !strings.EqualFold(child, id+"/privateEndpointConnections/"+last(child)) || seen[child] {
			return fmt.Errorf("invalid Monitor workspace private connection identity")
		}
		seen[child] = true
	}
	return nil
}
