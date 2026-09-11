package azure

import (
	"context"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

// The two ARM alias paths expose one pairing. Only the primary performs
// BreakPairing and DELETE; selecting the secondary namespace requires that
// same action without taking ownership of the primary namespace.
func recoveryPeerRelation(primary, secondary asset.Asset) bool {
	if !recoveryType(primary.Identity.NativeType) || primary.Identity.NativeType != secondary.Identity.NativeType || !strings.EqualFold(text(primary.Normalized["role"]), "Primary") || !strings.EqualFold(text(secondary.Normalized["role"]), "Secondary") {
		return false
	}
	for _, pair := range [][2]asset.Asset{{primary, secondary}, {secondary, primary}} {
		own, peer := pair[0], pair[1]
		if text(own.Normalized["_recovery_peer_alias"]) != strings.ToLower(peer.Identity.NativeID) || text(own.Normalized["_recovery_partner_namespace"])+"/disasterrecoveryconfigs/"+last(own.Identity.NativeID) != strings.ToLower(peer.Identity.NativeID) || text(own.Normalized["_recovery_peer_namespace_creation"]) == "" || own.Normalized["_recovery_peer_namespace_creation"] != peer.Normalized["_recovery_namespace_creation"] || text(own.Normalized["_recovery_peer_configuration"]) == "" || own.Normalized["_recovery_peer_configuration"] != peer.Normalized["_recovery_configuration"] || !strings.EqualFold(text(own.Normalized["_recovery_peer_role"]), text(peer.Normalized["role"])) {
			return false
		}
	}
	return true
}

func recoveryPrerequisite(parent, primary asset.Asset) bool {
	return recoveryType(primary.Identity.NativeType) && primary.Identity.NativeType == parent.Identity.NativeType+"/disasterRecoveryConfigs" && strings.EqualFold(text(primary.Normalized["role"]), "Primary") && strings.EqualFold(text(primary.Normalized["_recovery_partner_namespace"]), parent.Identity.NativeID) && strings.EqualFold(text(primary.Normalized["_recovery_peer_alias"]), parent.Identity.NativeID+"/disasterrecoveryconfigs/"+last(primary.Identity.NativeID)) && strings.EqualFold(text(primary.Normalized["_recovery_peer_role"]), "Secondary") && text(primary.Normalized["_recovery_peer_configuration"]) != "" && text(primary.Normalized["_recovery_peer_namespace_creation"]) != "" && primary.Normalized["_recovery_peer_namespace_creation"] == parent.Normalized["_arm_creation_generation"] && text(primary.Normalized["_recovery_configuration"]) != "" && text(primary.Normalized["_recovery_namespace_creation"]) != ""
}

func (s *serviceCascades) contributeRecoveryPrerequisite(ctx context.Context, parent asset.Asset, child serviceChild, assets []asset.Asset, result *governance.Contribution) error {
	normalized := map[string]any{}
	for key, value := range object(child.data["properties"]) {
		normalized[key] = value
	}
	if err := s.client.recoveryInventory(ctx, child.kind, child.id, child.data, normalized, map[string][]string{}); err != nil {
		return err
	}
	primaryID := text(normalized["_recovery_peer_alias"])
	evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "resource_type": child.kind, "instance_id": primaryID}
	var primary *asset.Asset
	for i := range assets {
		candidate := &assets[i]
		if candidate.Identity.Provider == parent.Identity.Provider && candidate.Identity.ConnectionID == parent.Identity.ConnectionID && candidate.Identity.Partition == parent.Identity.Partition && candidate.Identity.NativeType == child.kind && strings.EqualFold(candidate.Identity.NativeID, primaryID) {
			if primary != nil {
				return fmt.Errorf("ambiguous Azure recovery primary")
			}
			primary = candidate
		}
	}
	if primary == nil {
		result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: child.kind, NativeID: primaryID, ControllerID: parent.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
		return nil
	}
	secondary := asset.Asset{Identity: asset.Identity{NativeType: child.kind, NativeID: child.id}, Normalized: normalized}
	if !recoveryPrerequisite(parent, *primary) || !recoveryPeerRelation(*primary, secondary) {
		return serviceDenied("recovery_pair_changed")
	}
	result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: parent.ID, TargetAssetID: primary.ID, Type: graph.RelationshipDependsOn, Source: "azure:recovery-pair", Evidence: evidence, Confidence: 1})
	return nil
}

// Preserve the reviewed peer view after BreakPairing clears partnerNamespace.
// The peer may disappear during preparation or remain until primary DELETE.
func (c *client) plannedServiceChildren(ctx context.Context, parent asset.Asset, raw map[string]any, known ...asset.Asset) ([]serviceChild, error) {
	if parent.Identity.NativeType == domainType {
		dependencies, err := c.domainPlan(parent)
		if err != nil {
			return nil, err
		}
		return c.domainChildren(ctx, parent.Identity, raw, dependencies)
	}
	if fleetKind(parent.Identity.NativeType).kind != "" {
		return c.fleetChildren(ctx, parent.Identity, raw, known...)
	}
	if isCosmosType(parent.Identity.NativeType) {
		wire, err := c.plannedResourceID(parent)
		if err != nil {
			return nil, err
		}
		if err := c.cosmosAncestors(ctx, wire, object(parent.Normalized["_cosmos_ancestors"]), false); err != nil {
			return nil, err
		}
		settings, err := c.cosmosThroughput(ctx, parent.Identity.NativeType, wire, raw)
		if err != nil {
			return nil, err
		}
		if text(parent.Normalized["_cosmos_throughput_binding"]) != c.privateConfiguration(settings) {
			return nil, serviceDenied("cosmos_throughput_changed")
		}
	}
	children, err := c.serviceChildren(ctx, parent.Identity, raw)
	if err != nil || !recoveryType(parent.Identity.NativeType) || !strings.EqualFold(text(parent.Normalized["role"]), "Primary") {
		return children, err
	}
	peer, err := c.recoveryPeer(ctx, parent, raw)
	if err != nil {
		return nil, err
	}
	if peer != nil {
		children = append(children, *peer)
	}
	return children, nil
}
