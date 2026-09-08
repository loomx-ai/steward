package azure

import (
	"context"
	"fmt"
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

// Resource Manager's group list is the authority for all top-level resources,
// including kinds without a direct delete driver. Known nested resources are
// included from inventory as well, so they remain visible in the impact plan.
func (c *client) aksResources(ctx context.Context, clusterID, group string) ([]map[string]any, error) {
	response, err := c.request(ctx, "GET", apiURL(group, resourcesVersion))
	if isNotFound(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(text(response.data["id"]), group) {
		return nil, fmt.Errorf("AKS node resource group identity mismatch")
	}
	if owner := text(response.data["managedBy"]); owner != "" && !strings.EqualFold(owner, clusterID) {
		return nil, fmt.Errorf("AKS node resource group belongs to another controller")
	}
	values, err := c.listAll(ctx, group+"/resources", resourcesVersion)
	if err != nil {
		return nil, err
	}
	result := []map[string]any{response.data}
	response.data["type"] = groupType
	seen := map[string]bool{group: true}
	listed := map[string]bool{group: true}
	for _, value := range values {
		raw := object(value)
		id, kind, err := parseID(text(raw["id"]))
		if err != nil || !inResourceGroup(id, group) || listed[id] || !strings.EqualFold(kind, text(raw["type"])) {
			return nil, fmt.Errorf("invalid or duplicate AKS node resource group member")
		}
		listed[id] = true
		if !seen[id] {
			seen[id] = true
			result = append(result, raw)
		}
		if rule, known := findType(kind); known {
			children, err := c.children(ctx, rule, raw)
			if err != nil {
				return nil, err
			}
			for _, child := range children {
				childID, _, _ := parseID(text(child["id"]))
				if seen[childID] {
					continue
				}
				seen[childID] = true
				result = append(result, child)
			}
		}
	}
	return result, nil
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
		if !strings.EqualFold(text(response.data["id"]), cluster.Identity.NativeID) || !strings.EqualFold(text(response.data["type"]), aksType) {
			return result, fmt.Errorf("AKS cluster identity mismatch")
		}
		group, err := aksNodeGroup(h.client.subscription, object(response.data["properties"]))
		if err != nil {
			return result, err
		}
		planned, err := aksNodeGroup(h.client.subscription, cluster.Normalized)
		if err != nil || group != planned || inResourceGroup(cluster.Identity.NativeID, group) {
			return result, fmt.Errorf("AKS node resource group changed; refresh inventory")
		}
		resources, err := h.client.aksResources(ctx, cluster.Identity.NativeID, group)
		if err != nil {
			return result, err
		}
		members := map[string]string{}
		for _, raw := range resources {
			id, nativeType, _ := parseID(text(raw["id"]))
			members[id] = nativeType
		}
		byID := map[string]asset.Asset{}
		for _, value := range assets {
			if value.Identity.Provider != cluster.Identity.Provider || value.Identity.ConnectionID != cluster.Identity.ConnectionID || value.Identity.Partition != cluster.Identity.Partition || !inResourceGroup(value.Identity.NativeID, group) {
				continue
			}
			id, nativeType, err := parseID(value.Identity.NativeID)
			if err != nil || !strings.EqualFold(nativeType, value.Identity.NativeType) || byID[id].ID != "" {
				return result, fmt.Errorf("invalid or ambiguous AKS managed asset")
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
			evidence := map[string]any{"resource_type": nativeType, "instance_id": id, "node_resource_group": group, "delete_by_default": true, "retention_supported": false, "request_id": response.requestID, graph.LifecycleEvidenceControllerDeleteGuaranteed: true, graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
			value, found := byID[id]
			if !found {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: cluster.Identity.Provider, ConnectionID: cluster.Identity.ConnectionID, NativeType: nativeType, NativeID: id, ControllerID: cluster.ID, Relationship: graph.RelationshipMemberOf, Evidence: evidence})
				continue
			}
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: cluster.ID, ManagedAssetID: value.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, EvidenceSource: aksSource, Evidence: evidence, Confidence: 1})
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: value.ID, TargetAssetID: cluster.ID, Type: graph.RelationshipMemberOf, Source: aksSource, Evidence: evidence, Confidence: 1})
		}
	}
	return result, nil
}

// Group deletion takes precedence over a VM's Detach option. Avoid assigning a
// second exclusive controller to an attachment deleted with the same AKS group.
func sameAKSNodeGroup(assets []asset.Asset, controller asset.Asset, attachedID string) bool {
	for _, cluster := range assets {
		if cluster.Identity.Provider != asset.ProviderAzure || !strings.EqualFold(cluster.Identity.NativeType, aksType) || cluster.Identity.ConnectionID != controller.Identity.ConnectionID || cluster.Identity.Partition != controller.Identity.Partition {
			continue
		}
		parts := strings.Split(cluster.Identity.NativeID, "/")
		if len(parts) < 3 {
			continue
		}
		group, err := aksNodeGroup(parts[2], cluster.Normalized)
		if err == nil && inResourceGroup(controller.Identity.NativeID, group) && inResourceGroup(attachedID, group) {
			return true
		}
	}
	return false
}

func (a *action) aksPreflight(ctx context.Context, request contracts.ActionRequest, raw map[string]any, locks []any) (string, error) {
	group, err := aksNodeGroup(a.client.subscription, object(raw["properties"]))
	if err != nil {
		return "", err
	}
	planned, err := aksNodeGroup(a.client.subscription, request.Asset.Normalized)
	if err != nil || group != planned || inResourceGroup(a.id, group) {
		return "aks_node_resource_group_changed", nil
	}
	resources, err := a.client.aksResources(ctx, a.id, group)
	if err != nil {
		return "", err
	}
	impacts := map[string]contracts.ActionImpact{}
	for _, impact := range request.LifecycleImpacts {
		identity := impact.Asset.Identity
		if impact.ControllerID != request.Asset.ID {
			continue
		}
		id, nativeType, err := parseID(identity.NativeID)
		if err != nil || !strings.EqualFold(nativeType, identity.NativeType) || identity.Provider != asset.ProviderAzure || identity.ConnectionID != request.Asset.Identity.ConnectionID || identity.Partition != request.Asset.Identity.Partition || !inResourceGroup(id, group) || impacts[id].Asset.ID != "" {
			return "invalid_aks_lifecycle_impact", nil
		}
		if !impact.Delete {
			return "aks_retention_requires_moving_resource", nil
		}
		if locked(id, locks) {
			return "azure_management_lock", nil
		}
		impacts[id] = impact
	}
	for _, resource := range resources {
		id, kind, _ := parseID(text(resource["id"]))
		impact, found := impacts[id]
		if !found || !strings.EqualFold(impact.Asset.Identity.NativeType, kind) {
			return "aks_resource_missing_from_plan", nil
		}
		if locked(id, locks) {
			return "azure_management_lock", nil
		}
		if rule, known := findType(kind); known {
			if reason := protectionReason(rule, resource); reason != "" && !controllerOnlyReason(reason) {
				return reason, nil
			}
		}
		for _, tag := range []string{"steward/protected", "steward:protected"} {
			switch strings.ToLower(text(object(resource["tags"])[tag])) {
			case "1", "true", "yes", "on", "protected":
				return "aks_managed_resource_protected", nil
			}
		}
	}
	// An ARM group deletion can also trigger VM/NIC attachment deletion outside
	// that group. Check the current attachment policy rather than assuming all
	// resources live in the node resource group.
	for _, impact := range impacts {
		kind, ok := findType(impact.Asset.Identity.NativeType)
		if !ok || (kind.NativeType != vmType && kind.NativeType != nicType) {
			continue
		}
		endpoint, err := a.client.resourceURL(kind, impact.Asset.Identity.NativeID)
		if err != nil {
			return "", err
		}
		current, err := a.client.request(ctx, "GET", endpoint)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return "", err
		}
		if !strings.EqualFold(text(current.data["id"]), impact.Asset.Identity.NativeID) || !strings.EqualFold(text(current.data["type"]), kind.NativeType) {
			return "", fmt.Errorf("AKS attachment owner identity mismatch")
		}
		attachments, err := resourceAttachments(a.client.subscription, kind.NativeType, object(current.data["properties"]))
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

func (a *action) aksGroupReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	group, err := aksNodeGroup(a.client.subscription, request.Asset.Normalized)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	response, err := a.client.request(ctx, "GET", apiURL(group, resourcesVersion))
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if !strings.EqualFold(text(response.data["id"]), group) {
		return contracts.ReadbackResult{}, fmt.Errorf("AKS node resource group readback identity mismatch")
	}
	return contracts.ReadbackResult{Exists: true, State: "deleting_node_resource_group"}, nil
}
