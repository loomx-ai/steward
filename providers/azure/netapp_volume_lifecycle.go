package azure

import (
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const netappVolumeLifecycleSource = "azure:netapp-volume-cascade"

func (c *client) netappVolumeRecorded(value asset.Asset) (map[string]any, error) {
	id, kind, err := parseID(value.Identity.NativeID)
	boundary := object(value.Normalized[netappVolumeReview])
	if err != nil || id != value.Identity.NativeID || kind != strings.ToLower(netappVolumeType) || value.Identity.NativeType != netappVolumeType || len(strings.Split(id, "/")) != 13 || value.Identity.Provider != asset.ProviderAzure || value.Identity.Partition != "azure" || value.Identity.ConnectionID == "" || !strings.HasPrefix(id, c.root()+"/") || value.ID == "" || value.Location != boundary["region"] || len(boundary) != 11 || object(boundary["members"]) == nil || boundary["volume"] != value.Normalized["_netapp_configuration"] {
		return nil, serviceDenied("invalid_netapp_volume_boundary")
	}
	if value.Normalized[netappVolumeProof] != c.netappVolumeProofFor(id, value.Identity.ConnectionID, boundary) {
		return nil, serviceDenied("netapp_volume_boundary_proof_changed")
	}
	return boundary, nil
}
func (c *client) netappVolumeContribution(parent asset.Asset, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	boundary, err := c.netappVolumeRecorded(parent)
	if err != nil {
		return result, err
	}
	for id, value := range object(boundary["members"]) {
		member := object(value)
		kind := text(member["kind"])
		if !netappVolumeChild(kind) || c.netappIdentity(id, kind) != nil || redisParentID(id) != parent.Identity.NativeID {
			return result, serviceDenied("invalid_netapp_volume_member")
		}
		var target *asset.Asset
		for i := range assets {
			candidate := &assets[i]
			if candidate.Identity.Provider != parent.Identity.Provider || candidate.Identity.ConnectionID != parent.Identity.ConnectionID || candidate.Identity.Partition != parent.Identity.Partition || candidate.Identity.NativeID != id || candidate.Identity.NativeType != kind {
				continue
			}
			if target != nil || candidate.ID == "" || candidate.ID == parent.ID {
				return result, serviceDenied("ambiguous_netapp_volume_member")
			}
			target = candidate
		}
		evidence := map[string]any{"resource_type": kind, "instance_id": id, "delete_by_default": true, "retention_supported": false, graph.LifecycleEvidenceControllerDeleteGuaranteed: true, graph.LifecycleEvidenceUnselectedControllerAction: graph.LifecycleUnselectedControllerSkip}
		evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] = true
		if netappDirectLeaf(kind) {
			delete(evidence, graph.LifecycleEvidenceUnselectedControllerAction)
		}
		if target == nil {
			if member["absent"] == true {
				continue
			}
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: kind, NativeID: id, ControllerID: parent.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
			continue
		}
		if target.Location != parent.Location || target.Normalized["_netapp_configuration"] != member["configuration"] {
			return result, serviceDenied("netapp_volume_member_configuration_changed")
		}
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: parent.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: c.netappLeafDirectAllowed(*target), EvidenceSource: netappVolumeLifecycleSource, Evidence: evidence, Confidence: 1})
		result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: parent.ID, Type: graph.RelationshipAttachedTo, Source: netappVolumeLifecycleSource, Evidence: evidence, Confidence: 1})
	}
	return result, nil
}
