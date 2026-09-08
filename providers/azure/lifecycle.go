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

const attachmentSource = "azure:resource-delete-options"

type attachedResource struct {
	id, kind, slot string
	delete         bool
}

func resourceAttachments(subscription, nativeType string, properties map[string]any) ([]attachedResource, error) {
	var result []attachedResource
	seen := map[string]bool{}
	add := func(id, kind, slot string, option any) error {
		policy := text(option)
		if option != nil && (!strings.EqualFold(policy, "Delete") && !strings.EqualFold(policy, "Detach")) {
			return fmt.Errorf("invalid Azure attached resource delete option")
		}
		deletes := strings.EqualFold(policy, "Delete")
		if id == "" {
			if deletes {
				return fmt.Errorf("Azure auto-delete attachment has no managed resource identity")
			}
			return nil
		}
		canonical, actualType, err := parseID(id)
		if err != nil || !strings.HasPrefix(canonical, "/subscriptions/"+strings.ToLower(subscription)+"/") || !strings.EqualFold(actualType, kind) || seen[canonical] {
			return fmt.Errorf("invalid or duplicate Azure attachment identity")
		}
		seen[canonical] = true
		result = append(result, attachedResource{id: canonical, kind: kind, slot: slot, delete: deletes})
		return nil
	}
	values := func(value any) ([]any, error) {
		if value == nil {
			return nil, nil
		}
		result, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("invalid Azure attachment array")
		}
		return result, nil
	}
	switch nativeType {
	case vmType:
		storage := object(properties["storageProfile"])
		if properties["storageProfile"] != nil && storage == nil {
			return nil, fmt.Errorf("invalid Azure VM storage profile")
		}
		os := object(storage["osDisk"])
		if storage["osDisk"] != nil && os == nil {
			return nil, fmt.Errorf("invalid Azure VM OS disk")
		}
		if !strings.EqualFold(text(object(os["diffDiskSettings"])["option"]), "Local") {
			if err := add(text(object(os["managedDisk"])["id"]), diskType, "os", os["deleteOption"]); err != nil {
				return nil, err
			}
		}
		disks, err := values(storage["dataDisks"])
		if err != nil {
			return nil, err
		}
		for _, value := range disks {
			disk := object(value)
			if disk == nil || disk["lun"] == nil {
				return nil, fmt.Errorf("Azure data disk attachment has no LUN")
			}
			if err := add(text(object(disk["managedDisk"])["id"]), diskType, "data:"+fmt.Sprint(disk["lun"]), disk["deleteOption"]); err != nil {
				return nil, err
			}
		}
		nics, err := values(object(properties["networkProfile"])["networkInterfaces"])
		if err != nil {
			return nil, err
		}
		for _, value := range nics {
			nic := object(value)
			if text(nic["id"]) == "" {
				return nil, fmt.Errorf("Azure NIC attachment has no identity")
			}
			if err := add(text(nic["id"]), nicType, "nic", object(nic["properties"])["deleteOption"]); err != nil {
				return nil, err
			}
		}
	case nicType:
		configs, err := values(properties["ipConfigurations"])
		if err != nil {
			return nil, err
		}
		for _, value := range configs {
			configuration := object(value)
			if configuration == nil || text(configuration["name"]) == "" {
				return nil, fmt.Errorf("invalid Azure IP configuration")
			}
			ip := object(object(configuration["properties"])["publicIPAddress"])
			if len(ip) == 0 {
				continue
			}
			if text(ip["id"]) == "" {
				return nil, fmt.Errorf("Azure public IP attachment has no identity")
			}
			if err := add(text(ip["id"]), "Microsoft.Network/publicIPAddresses", "ip:"+strings.ToLower(text(configuration["name"])), object(ip["properties"])["deleteOption"]); err != nil {
				return nil, err
			}
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].id < result[j].id })
	return result, nil
}

type ResourceAttachments struct{}

func NewResourceAttachments() *ResourceAttachments { return &ResourceAttachments{} }

func (*ResourceAttachments) Contribute(_ context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	for _, controller := range assets {
		if controller.Identity.Provider != asset.ProviderAzure || (controller.Identity.NativeType != vmType && controller.Identity.NativeType != nicType) {
			continue
		}
		attachments, err := resourceAttachments(text(controller.Normalized["subscription_id"]), controller.Identity.NativeType, controller.Normalized)
		if err != nil {
			return result, err
		}
		for _, attachment := range attachments {
			if sameAKSNodeGroup(assets, controller, attachment.id) {
				continue
			}
			evidence := map[string]any{"resource_type": attachment.kind, "instance_id": attachment.id, "attachment_slot": attachment.slot, "delete_by_default": attachment.delete, "lifecycle_kind": "azure_attached_resource_delete"}
			var managed *asset.Asset
			for i := range assets {
				candidate := &assets[i]
				if candidate.Identity.Provider == controller.Identity.Provider && candidate.Identity.ConnectionID == controller.Identity.ConnectionID && candidate.Identity.Partition == controller.Identity.Partition && strings.EqualFold(candidate.Identity.NativeID, attachment.id) && strings.EqualFold(candidate.Identity.NativeType, attachment.kind) {
					if managed != nil {
						return result, fmt.Errorf("ambiguous Azure attachment identity")
					}
					managed = candidate
				}
			}
			if managed == nil {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: controller.Identity.Provider, ConnectionID: controller.Identity.ConnectionID, NativeType: attachment.kind, NativeID: attachment.id, ControllerID: controller.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
				continue
			}
			if attachment.delete {
				evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
				result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: controller.ID, ManagedAssetID: managed.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, EvidenceSource: attachmentSource, Evidence: evidence, Confidence: 1})
			} else {
				evidence[graph.RelationshipEvidenceDeletionOrder] = graph.DeletionOrderTargetBeforeSource
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: managed.ID, TargetAssetID: controller.ID, Type: graph.RelationshipAttachedTo, Source: attachmentSource, Evidence: evidence, Confidence: 1})
		}
	}
	return result, nil
}

type attachmentUpdate struct {
	asset  asset.Asset
	live   map[string]any
	retain []attachedResource
}

// evaluateAttachments checks the full cascade, including a NIC's public IPs.
// Only reviewed outcomes can authorize deletion or the changes needed to retain
// a child. Native resource IDs and lifecycle controller IDs must both match.
func (a *action) evaluateAttachments(ctx context.Context, request contracts.ActionRequest, raw map[string]any, locks []any) ([]attachmentUpdate, string, error) {
	var updates []attachmentUpdate
	groups := map[string]bool{}
	var walk func(asset.Asset, map[string]any) (string, error)
	walk = func(controller asset.Asset, raw map[string]any) (string, error) {
		nativeType := controller.Identity.NativeType
		if nativeType == "" {
			nativeType = a.kind.NativeType
		}
		current, err := resourceAttachments(a.client.subscription, nativeType, object(raw["properties"]))
		if err != nil {
			return "", err
		}
		planned, err := resourceAttachments(a.client.subscription, nativeType, controller.Normalized)
		if err != nil {
			return "", err
		}
		if len(current) != len(planned) {
			return "attached_resources_changed", nil
		}
		var retain []attachedResource
		for i, child := range current {
			expected := planned[i]
			if child.id != expected.id || child.slot != expected.slot {
				return "attached_resources_changed", nil
			}
			shouldDelete := expected.delete
			var impact *contracts.ActionImpact
			if expected.delete {
				for i := range request.LifecycleImpacts {
					candidate := &request.LifecycleImpacts[i]
					identity := candidate.Asset.Identity
					if candidate.ControllerID == controller.ID && strings.EqualFold(identity.NativeID, child.id) && strings.EqualFold(identity.NativeType, child.kind) && identity.Provider == asset.ProviderAzure && identity.ConnectionID == controller.Identity.ConnectionID && identity.Partition == controller.Identity.Partition {
						if impact != nil {
							return "ambiguous_lifecycle_impact", nil
						}
						impact, shouldDelete = candidate, candidate.Delete
					}
				}
				if impact == nil {
					return "attached_resource_missing_from_plan", nil
				}
			}
			if child.delete != expected.delete && (shouldDelete || child.delete) {
				return "attached_resources_changed", nil
			}
			if !shouldDelete {
				if child.delete {
					retain = append(retain, child)
				}
				continue
			}
			kind, _ := findType(child.kind)
			endpoint, err := a.client.resourceURL(kind, child.id)
			if err != nil {
				return "", err
			}
			response, err := a.client.request(ctx, "GET", endpoint)
			if isNotFound(err) {
				continue
			}
			if err != nil {
				return "", err
			}
			if !validResourceResponse(response, child.id, child.kind) {
				return "", fmt.Errorf("Azure cascade read identity mismatch")
			}
			if reason := protectionReason(kind, response.data); reason != "" {
				return reason, nil
			}
			if locked(child.id, locks) {
				return "azure_management_lock", nil
			}
			parts := strings.Split(child.id, "/")
			groupID := strings.Join(parts[:5], "/")
			if !groups[groupID] {
				group, err := a.client.request(ctx, "GET", apiURL(groupID, resourcesVersion))
				if err != nil {
					return "", err
				}
				if text(group.data["managedBy"]) != "" {
					return "azure_managed_resource_group", nil
				}
				groups[groupID] = true
			}
			if child.kind == nicType {
				if reason, err := walk(impact.Asset, response.data); reason != "" || err != nil {
					return reason, err
				}
			}
		}
		if len(retain) > 0 {
			updates = append(updates, attachmentUpdate{asset: controller, live: raw, retain: retain})
		}
		return "", nil
	}
	reason, err := walk(request.Asset, raw)
	return updates, reason, err
}
