package azure

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const aksType = "Microsoft.ContainerService/managedClusters"
const aksSource = "azure:aks-node-resource-group"

type aksLifecycle struct{ client *client }

// ClusterLifecycle resolves the same explicit connection used by inventory and
// execution. Resource names and MC_ prefixes are never ownership evidence.
func (r *Runtime) ClusterLifecycle(ctx context.Context, id asset.ConnectionID) (governance.Contributor, error) {
	c, err := r.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	return &aksLifecycle{client: c}, nil
}

func aksNodeGroup(subscription string, properties map[string]any) (string, error) {
	name := text(properties["nodeResourceGroup"])
	if name == "" || strings.ContainsAny(name, "/\\") {
		return "", fmt.Errorf("AKS cluster omitted a valid node resource group")
	}
	id, kind, err := parseID("/subscriptions/" + subscription + "/resourceGroups/" + name)
	if err != nil || !strings.EqualFold(kind, groupType) {
		return "", fmt.Errorf("invalid AKS node resource group")
	}
	return id, nil
}

func inResourceGroup(id, group string) bool {
	return strings.EqualFold(id, group) || strings.HasPrefix(strings.ToLower(id), strings.ToLower(group)+"/")
}

// The native group list establishes membership for all kinds. Known resources
// are read with their product APIs, and documented cascades are expanded until
// every nested or external descendant has been visited.
func (c *client) managedGroupResources(ctx context.Context, clusterID, group string) ([]map[string]any, error) {
	_, controllerType, _ := parseID(clusterID)
	response, err := c.request(ctx, "GET", apiURL(group, resourcesVersion))
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(response, group, groupType) {
		return nil, fmt.Errorf("AKS node resource group identity mismatch")
	}
	if owner := text(response.data["managedBy"]); owner != "" && !strings.EqualFold(owner, clusterID) {
		return nil, fmt.Errorf("AKS node resource group belongs to another controller")
	}
	values, err := c.listAll(ctx, group+"/resources", resourcesVersion)
	if err != nil {
		return nil, err
	}
	response.data["type"] = groupType
	result := []map[string]any{response.data}
	seen := map[string]map[string]any{group: response.data}
	var visit func(map[string]any) error
	visit = func(raw map[string]any) error {
		id, kind, err := parseID(text(raw["id"]))
		if err != nil || !strings.HasPrefix(id, c.root()+"/") || !validResponseType(kind, text(raw["type"])) {
			return fmt.Errorf("invalid AKS descendant identity")
		}
		if previous := seen[id]; previous != nil {
			return serviceListedIncarnation(raw, previous)
		}
		rule, known := findType(kind)
		if known {
			endpoint, err := c.resourceURL(rule, id)
			if err != nil {
				return err
			}
			live, err := c.request(ctx, "GET", endpoint)
			if err != nil {
				return err
			}
			if !validResourceResponse(live, id, kind) {
				return fmt.Errorf("AKS resource detail identity mismatch")
			}
			if err := serviceListedIncarnation(raw, live.data); err != nil {
				return err
			}
			raw = live.data
			raw["type"] = rule.NativeType
		}
		seen[id] = raw
		result = append(result, raw)
		if !known {
			return nil
		}
		children, err := c.children(ctx, rule, raw)
		if err != nil {
			return err
		}
		if rule.NativeType == vnetType {
			links, err := c.virtualNetworkDNSLinks(ctx, id)
			if err != nil {
				return err
			}
			for _, link := range links {
				children = append(children, link.data)
			}
		}
		for _, child := range children {
			if strings.EqualFold(controllerType, monitorWorkspaceType) && !inResourceGroup(text(child["id"]), group) {
				_, childType, _ := parseID(text(child["id"]))
				if strings.EqualFold(childType, dataCollectionAssociationType) {
					continue // The workspace reviews these as separate unlink steps.
				}
			}
			if !inResourceGroup(text(child["id"]), group) && !aksExternalRelation(aksNativeAsset(raw), aksNativeAsset(child)) {
				return fmt.Errorf("AKS descendant is outside its native deletion cascade")
			}
			if err := visit(child); err != nil {
				return err
			}
		}
		return nil
	}
	listed := map[string]bool{group: true}
	for _, value := range values {
		raw := object(value)
		id, kind, err := parseID(text(raw["id"]))
		if err != nil || !inResourceGroup(id, group) || listed[id] || !validResponseType(kind, text(raw["type"])) {
			return nil, fmt.Errorf("invalid or duplicate AKS node resource group member")
		}
		listed[id] = true
		if err := visit(raw); err != nil {
			return nil, err
		}
	}
	current, err := c.request(ctx, "GET", apiURL(group, resourcesVersion))
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(current, group, groupType) || !strings.EqualFold(text(response.data["managedBy"]), text(current.data["managedBy"])) {
		return nil, fmt.Errorf("AKS resource group ownership changed")
	}
	if err := serviceListedIncarnation(response.data, current.data); err != nil {
		return nil, err
	}
	return result, nil
}

// This view is used only for relationship checks; inventory still uses the full
// normalization path and its redaction/protection rules.
func aksNativeAsset(raw map[string]any) asset.Asset {
	id, kind, _ := parseID(text(raw["id"]))
	normalized := map[string]any{}
	for key, value := range object(raw["properties"]) {
		normalized[key] = value
	}
	normalized["managedBy"] = raw["managedBy"]
	return asset.Asset{Identity: asset.Identity{NativeID: id, NativeType: kind}, Normalized: normalized}
}

func aksExternalRelation(parent, child asset.Asset) bool {
	if streamAnalyticsClusterPrerequisite(parent, child) {
		return false // An associated job is not owned by the cluster's group.
	}
	if kustoSharedPrerequisite(parent, child) {
		return false // Group deletion does not detach an external follower.
	}
	if strings.EqualFold(child.Identity.NativeType, dataCollectionAssociationType) {
		return false // Deleting the group does not prove external associations are unlinked.
	}
	if strings.EqualFold(parent.Identity.NativeType, vnetType) && strings.EqualFold(child.Identity.NativeType, privateDNSLinkType) {
		return strings.EqualFold(text(object(child.Normalized["virtualNetwork"])["id"]), parent.Identity.NativeID)
	}
	// Flexible VMs require independent deletion; deleting a scale set in the
	// group does not authorize deletion of a Flexible VM in another group.
	if strings.EqualFold(parent.Identity.NativeType, scaleSetType) && strings.EqualFold(child.Identity.NativeType, vmType) {
		return false
	}
	return slices.ContainsFunc(serviceChildKinds(parent.Identity.NativeType), func(kind string) bool {
		return strings.EqualFold(kind, child.Identity.NativeType)
	}) && serviceChildRelation(parent, child)
}

func managedGroupFrozenMembers(group string, assets []asset.Asset) map[string]bool {
	members := map[string]bool{}
	for _, value := range assets {
		if inResourceGroup(value.Identity.NativeID, group) {
			members[strings.ToLower(value.Identity.NativeID)] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, parent := range assets {
			if !members[strings.ToLower(parent.Identity.NativeID)] {
				continue
			}
			for _, child := range assets {
				id := strings.ToLower(child.Identity.NativeID)
				if !members[id] && aksExternalRelation(parent, child) {
					members[id], changed = true, true
				}
			}
		}
	}
	return members
}

func (h *aksLifecycle) Contribute(ctx context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	for _, cluster := range assets {
		if cluster.Identity.Provider != asset.ProviderAzure || !strings.EqualFold(cluster.Identity.NativeType, aksType) {
			continue
		}
		kind, _ := findType(aksType)
		endpoint, err := h.client.resourceURL(kind, cluster.Identity.NativeID)
		if err != nil {
			return result, err
		}
		response, err := h.client.request(ctx, "GET", endpoint)
		if err != nil {
			return result, err
		}
		if !validResourceResponse(response, cluster.Identity.NativeID, aksType) {
			return result, fmt.Errorf("AKS cluster identity mismatch")
		}
		if err := serviceIncarnation(cluster, response.data); err != nil {
			return result, err
		}
		group, err := aksNodeGroup(h.client.subscription, object(response.data["properties"]))
		if err != nil {
			return result, err
		}
		planned, err := aksNodeGroup(h.client.subscription, cluster.Normalized)
		if err != nil || group != planned || inResourceGroup(cluster.Identity.NativeID, group) {
			return result, fmt.Errorf("AKS node resource group changed; refresh inventory")
		}
		contribution, err := h.client.contributeManagedGroup(ctx, cluster, response, group, aksSource, assets)
		if err != nil {
			return result, err
		}
		result.Bindings = append(result.Bindings, contribution.Bindings...)
		result.Relationships = append(result.Relationships, contribution.Relationships...)
		result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
	}
	return result, nil
}

func (c *client) contributeManagedGroup(ctx context.Context, controller asset.Asset, response response, group, source string, assets []asset.Asset) (governance.Contribution, error) {
	resources, err := c.managedGroupResources(ctx, controller.Identity.NativeID, group)
	if err != nil {
		return governance.Contribution{}, err
	}
	return c.bindManagedGroup(ctx, controller, group, source, response.requestID, resources, assets)
}

func (c *client) bindManagedGroup(ctx context.Context, controller asset.Asset, group, source, requestID string, resources []map[string]any, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	members := map[string]string{}
	liveByID := map[string]map[string]any{}
	for _, raw := range resources {
		id, nativeType, _ := parseID(text(raw["id"]))
		members[id] = nativeType
		liveByID[id] = raw
	}
	byID := map[string]asset.Asset{}
	for _, value := range assets {
		if value.Identity.Provider != controller.Identity.Provider || value.Identity.ConnectionID != controller.Identity.ConnectionID || value.Identity.Partition != controller.Identity.Partition || (!inResourceGroup(value.Identity.NativeID, group) && members[strings.ToLower(value.Identity.NativeID)] == "") {
			continue
		}
		id, nativeType, err := parseID(value.Identity.NativeID)
		if err != nil || !strings.EqualFold(nativeType, value.Identity.NativeType) || byID[id].ID != "" {
			return result, fmt.Errorf("invalid or ambiguous AKS managed asset")
		}
		if live := liveByID[id]; live != nil {
			if err := c.servicePrivateIncarnation(value, live); err != nil {
				return result, err
			}
			if err := c.monitorManagedIncarnation(ctx, value, live, liveByID[group]); err != nil {
				return result, err
			}
			if err := serviceIncarnation(value, live); err != nil {
				return result, err
			}
		}
		byID[id] = value
		members[id] = value.Identity.NativeType
	}
	ids := make([]string, 0, len(members))
	for id := range members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		nativeType := members[id]
		evidence := map[string]any{"resource_type": nativeType, "instance_id": id, "managed_resource_group": group, "delete_by_default": true, "retention_supported": false, "request_id": requestID, graph.LifecycleEvidenceControllerDeleteGuaranteed: true, graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
		value, found := byID[id]
		if !found {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: controller.Identity.Provider, ConnectionID: controller.Identity.ConnectionID, NativeType: nativeType, NativeID: id, ControllerID: controller.ID, Relationship: graph.RelationshipMemberOf, Evidence: evidence})
			continue
		}
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: controller.ID, ManagedAssetID: value.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, EvidenceSource: source, Evidence: evidence, Confidence: 1})
		result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: value.ID, TargetAssetID: controller.ID, Type: graph.RelationshipMemberOf, Source: source, Evidence: evidence, Confidence: 1})
	}
	return result, nil
}

// Group deletion takes precedence over per-resource lifetime policies. Resolve
// the frozen membership once per contribution, including native external trees,
// so an outside DNS zone cannot claim records already owned by the AKS cascade.
type managedGroupMemberKey struct {
	connection    asset.ConnectionID
	partition, id string
}

func managedGroupKey(identity asset.Identity, nativeID string) managedGroupMemberKey {
	return managedGroupMemberKey{identity.ConnectionID, identity.Partition, strings.ToLower(nativeID)}
}

func managedGroupMembers(assets []asset.Asset) map[managedGroupMemberKey]bool {
	result := map[managedGroupMemberKey]bool{}
	for _, cluster := range assets {
		if cluster.Identity.Provider != asset.ProviderAzure || (!strings.EqualFold(cluster.Identity.NativeType, aksType) && !strings.EqualFold(cluster.Identity.NativeType, monitorWorkspaceType) && !strings.EqualFold(cluster.Identity.NativeType, applicationInsightsType)) {
			continue
		}
		parts := strings.Split(cluster.Identity.NativeID, "/")
		if len(parts) < 3 {
			continue
		}
		group, err := controllerResourceGroup(parts[2], cluster.Identity.NativeType, cluster.Normalized)
		if cluster.Identity.NativeType == applicationInsightsType {
			var kind string
			group, kind, err = parseID(text(object(cluster.Normalized["_insights_workspace"])["managed_group"]))
			if !strings.EqualFold(kind, groupType) || !strings.HasPrefix(group, "/subscriptions/"+strings.ToLower(parts[2])+"/") {
				continue
			}
		}
		if err != nil {
			continue
		}
		candidates := []asset.Asset{}
		for _, value := range assets {
			if value.Identity.Provider == cluster.Identity.Provider && value.Identity.ConnectionID == cluster.Identity.ConnectionID && value.Identity.Partition == cluster.Identity.Partition {
				candidates = append(candidates, value)
			}
		}
		for id := range managedGroupFrozenMembers(group, candidates) {
			result[managedGroupKey(cluster.Identity, id)] = true
		}
	}
	return result
}

func (a *action) managedGroupImpacts(request contracts.ActionRequest, group string) (map[string]contracts.ActionImpact, error) {
	root := request.Asset
	if root.Identity.Provider != asset.ProviderAzure || !strings.EqualFold(root.Identity.NativeID, a.id) || !strings.EqualFold(root.Identity.NativeType, a.kind.NativeType) || (a.kind.NativeType != aksType && a.kind.NativeType != monitorWorkspaceType && a.kind.NativeType != applicationInsightsType) {
		return nil, serviceDenied("invalid_aks_controller")
	}
	impacts := map[string]contracts.ActionImpact{}
	assets := []asset.Asset{}
	seen := map[asset.AssetID]bool{root.ID: true}
	for _, impact := range request.LifecycleImpacts {
		identity := impact.Asset.Identity
		id, kind, err := parseID(identity.NativeID)
		if err != nil || impact.Asset.ID == "" || seen[impact.Asset.ID] || !strings.EqualFold(kind, identity.NativeType) || identity.Provider != asset.ProviderAzure || identity.ConnectionID != root.Identity.ConnectionID || identity.Partition != root.Identity.Partition || !strings.HasPrefix(id, a.client.root()+"/") || impacts[id].Asset.ID != "" || impact.ControllerID != root.ID {
			return nil, serviceDenied("invalid_aks_lifecycle_impact")
		}
		if !impact.Delete {
			return nil, serviceDenied("aks_retention_requires_moving_resource")
		}
		seen[impact.Asset.ID] = true
		impacts[id] = impact
		assets = append(assets, impact.Asset)
	}
	members := managedGroupFrozenMembers(group, assets)
	for id := range impacts {
		if !members[id] {
			return nil, serviceDenied("aks_external_resource_scope_changed")
		}
	}
	return impacts, nil
}

func (a *action) managedGroupPreflight(ctx context.Context, request contracts.ActionRequest, raw map[string]any, locks []any) (string, error) {
	group, err := controllerResourceGroup(a.client.subscription, a.kind.NativeType, object(raw["properties"]))
	if err != nil {
		return "", err
	}
	planned, err := controllerResourceGroup(a.client.subscription, a.kind.NativeType, request.Asset.Normalized)
	if err != nil || group != planned || inResourceGroup(a.id, group) {
		return "aks_node_resource_group_changed", nil
	}
	if err := serviceIncarnation(request.Asset, raw); err != nil {
		return "", err
	}
	if _, err := a.managedGroupImpacts(request, group); err != nil {
		return "", err
	}
	resources, err := a.client.managedGroupResources(ctx, a.id, group)
	if err != nil {
		return "", err
	}
	return a.managedGroupResourcesPreflight(ctx, request, group, resources, locks)
}

func (a *action) managedGroupResourcesPreflight(ctx context.Context, request contracts.ActionRequest, group string, resources []map[string]any, locks []any) (string, error) {
	impacts, err := a.managedGroupImpacts(request, group)
	if err != nil {
		return "", err
	}
	visited, externalGroups := map[string]bool{}, map[string]bool{}
	var groupResource map[string]any
	for _, resource := range resources {
		if strings.EqualFold(text(resource["id"]), group) {
			groupResource = resource
		}
	}
	for _, resource := range resources {
		id, kind, _ := parseID(text(resource["id"]))
		impact, found := impacts[id]
		if !found || !strings.EqualFold(impact.Asset.Identity.NativeType, kind) {
			return "aks_resource_missing_from_plan", nil
		}
		visited[id] = true
		if err := a.client.servicePrivateIncarnation(impact.Asset, resource); err != nil {
			return "", err
		}
		if err := a.client.monitorManagedIncarnation(ctx, impact.Asset, resource, groupResource); err != nil {
			return "", err
		}
		if err := a.client.workbookManagedIncarnation(ctx, impact.Asset); err != nil {
			return "", err
		}
		if isStreamAnalyticsType(kind) {
			if err := streamAnalyticsReady(kind, resource); err != nil {
				return "", err
			}
			mapping, _ := findType(kind)
			childAction := action{client: a.client, kind: mapping, id: id}
			if err := childAction.streamAnalyticsTargetPreflight(ctx, impact.Asset, resource); err != nil {
				return "", err
			}
		}
		if isKustoType(kind) {
			if err := kustoReady(kind, resource); err != nil {
				return "", err
			}
			mapping, _ := findType(kind)
			childAction := action{client: a.client, kind: mapping, id: id}
			if err := childAction.kustoTargetPreflight(ctx, impact.Asset, resource); err != nil {
				return "", err
			}
		}
		if err := serviceCreationIdentity(impact.Asset, resource); err != nil {
			return "", err
		}
		if a.kind.NativeType == monitorWorkspaceType && isDataCollectionType(kind) && text(impact.Asset.Normalized["_data_collection_configuration"]) == dataCollectionConfiguration(kind, resource) {
			// Reviewed external unlinks can change the collection target's ETag.
			// Its configuration and immutable creation identity must still match.
			impact.Asset.Normalized = cloneNormalizedWithoutGeneration(impact.Asset.Normalized)
		}
		if a.kind.NativeType == applicationInsightsType {
			state := object(request.Asset.Normalized["_insights_workspace"])
			expected := text(object(object(state["members"])[id])["lifecycle_configuration"])
			if id == group {
				expected = text(state["workspace_group_lifecycle_configuration"])
			}
			if expected == "" || expected != a.client.privateConfiguration(insightsWorkspaceLifecycleSnapshot(resource)) {
				return "insights_workspace_member_configuration_changed", nil
			}
			// The frozen private configuration and creation identity still
			// match after the reviewed AMPLS association removals.
			impact.Asset.Normalized = cloneNormalizedWithoutGeneration(impact.Asset.Normalized)
		}
		if err := serviceIncarnation(impact.Asset, resource); err != nil {
			return "", err
		}
		if locked(id, locks) {
			return "azure_management_lock", nil
		}
		if !inResourceGroup(id, group) {
			external := strings.Join(strings.Split(id, "/")[:5], "/")
			if !externalGroups[external] {
				current, err := a.client.request(ctx, "GET", apiURL(external, resourcesVersion))
				if err != nil {
					return "", err
				}
				if !validResourceResponse(current, external, groupType) {
					return "", fmt.Errorf("AKS external resource group identity mismatch")
				}
				if text(current.data["managedBy"]) != "" {
					return "azure_managed_resource_group", nil
				}
				externalGroups[external] = true
			}
		}
		if rule, known := findType(kind); known {
			if reason := protectionReason(rule, resource); reason != "" && !controllerOnlyReason(reason) {
				return reason, nil
			}
		}
		for tag, value := range object(resource["tags"]) {
			if strings.EqualFold(tag, "steward/protected") || strings.EqualFold(tag, "steward:protected") {
				switch strings.ToLower(text(value)) {
				case "1", "true", "yes", "on", "protected":
					return "aks_managed_resource_protected", nil
				}
			}
		}
	}
	for id, impact := range impacts {
		kind, known := findType(impact.Asset.Identity.NativeType)
		if !known {
			continue // Unknown kinds remain contained by the native group.
		}
		if !visited[id] {
			endpoint, err := a.client.resourceURL(kind, id)
			if err != nil {
				return "", err
			}
			if _, err = a.client.request(ctx, "GET", endpoint); !isNotFound(err) {
				if err != nil {
					return "", err
				}
				return "aks_resource_membership_changed", nil
			}
		}
	}
	// Group deletion also triggers native VM/NIC auto-delete policies. Until
	// external attachment detachment/retention is modeled for AKS, require those
	// attachments to stay within the explicitly reviewed node group.
	for _, resource := range resources {
		_, nativeType, _ := parseID(text(resource["id"]))
		kind, known := findType(nativeType)
		if !known || (kind.NativeType != vmType && kind.NativeType != nicType) {
			continue
		}
		attachments, err := resourceAttachments(a.client.subscription, kind.NativeType, object(resource["properties"]))
		if err != nil {
			return "", err
		}
		for _, attachment := range attachments {
			if attachment.delete && !inResourceGroup(attachment.id, group) {
				return "aks_external_attachment_requires_detach", nil
			}
		}
	}
	return "", nil
}

func (a *action) managedGroupReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	group, err := controllerResourceGroup(a.client.subscription, a.kind.NativeType, request.Asset.Normalized)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	return a.managedGroupResourcesReadback(ctx, request, group)
}

func (a *action) managedGroupResourcesReadback(ctx context.Context, request contracts.ActionRequest, group string) (contracts.ReadbackResult, error) {
	impacts, err := a.managedGroupImpacts(request, group)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	response, err := a.client.request(ctx, "GET", apiURL(group, resourcesVersion))
	if !isNotFound(err) {
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		if !validResourceResponse(response, group, groupType) {
			return contracts.ReadbackResult{}, fmt.Errorf("AKS node resource group readback identity mismatch")
		}
		if a.kind.NativeType == applicationInsightsType {
			owner, err := insightsManagedBy(response.data)
			if err != nil || owner != a.id || !insightsARMReadValid(response, group, groupType) {
				return contracts.ReadbackResult{}, serviceDenied("insights_managed_group_readback_changed")
			}
		}
		return contracts.ReadbackResult{Exists: true, State: "deleting_node_resource_group"}, nil
	}
	ids := make([]string, 0, len(impacts))
	for id := range impacts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		kind, known := findType(impacts[id].Asset.Identity.NativeType)
		if !known || strings.EqualFold(kind.NativeType, groupType) {
			continue // Group absence is the authority for unknown contained kinds.
		}
		endpoint, err := a.client.resourceURL(kind, id)
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		live, err := a.client.request(ctx, "GET", endpoint)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		if !validResourceResponse(live, id, kind.NativeType) {
			return contracts.ReadbackResult{}, fmt.Errorf("AKS child readback identity mismatch")
		}
		if err := a.client.workbookManagedIncarnation(ctx, impacts[id].Asset); err != nil {
			return contracts.ReadbackResult{}, err
		}
		if err := a.client.monitorManagedIncarnation(ctx, impacts[id].Asset, live.data, nil); err != nil {
			return contracts.ReadbackResult{}, err
		}
		if a.kind.NativeType == applicationInsightsType {
			if !insightsARMReadValid(live, id, kind.NativeType) {
				return contracts.ReadbackResult{}, serviceDenied("invalid_insights_managed_member_readback")
			}
			expected := text(object(object(object(request.Asset.Normalized["_insights_workspace"])["members"])[id])["lifecycle_configuration"])
			if expected == "" || expected != a.client.privateConfiguration(insightsWorkspaceLifecycleSnapshot(live.data)) {
				return contracts.ReadbackResult{}, serviceDenied("insights_managed_member_readback_changed")
			}
		}
		return contracts.ReadbackResult{Exists: true, State: "deleting_aks_resources"}, nil
	}
	return contracts.ReadbackResult{Exists: false}, nil
}
