package azure

import (
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const synapseWorkspaceLifecycleSource = "azure:synapse-workspace-cascade"

func (c *client) synapseWorkspaceRecorded(value asset.Asset) (map[string]any, error) {
	id, kind, err := parseID(value.Identity.NativeID)
	boundary := object(value.Normalized[synapseWorkspaceBoundaryKey])
	if err != nil || id != value.Identity.NativeID || kind != strings.ToLower(synapseType) || value.Identity.NativeType != synapseType || len(strings.Split(id, "/")) != 9 || value.Identity.Provider != asset.ProviderAzure || value.Identity.Partition != "azure" || value.Identity.ConnectionID == "" || !strings.HasPrefix(id, c.root()+"/") || value.ID == "" || len(boundary) != 6 || object(boundary["members"]) == nil || boundary["workspace"] != value.Normalized["_synapse_private_configuration"] {
		return nil, serviceDenied("invalid_synapse_workspace_boundary")
	}
	data := &synapseDataClient{arm: c}
	if value.Normalized[synapseWorkspaceBoundaryProof] != data.workspaceBoundaryProof(id, value.Identity.ConnectionID, boundary) {
		return nil, serviceDenied("synapse_workspace_boundary_proof_changed")
	}
	return boundary, nil
}
func (c *client) synapseWorkspaceContribution(parent asset.Asset, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	boundary, err := c.synapseWorkspaceRecorded(parent)
	if err != nil {
		return result, err
	}
	for id, value := range object(boundary["members"]) {
		member := object(value)
		kind := text(member["kind"])
		var target *asset.Asset
		for i := range assets {
			candidate := &assets[i]
			if candidate.Identity.Provider != parent.Identity.Provider || candidate.Identity.ConnectionID != parent.Identity.ConnectionID || candidate.Identity.Partition != parent.Identity.Partition || candidate.Identity.NativeID != id || candidate.Identity.NativeType != kind {
				continue
			}
			if target != nil || candidate.ID == "" || candidate.ID == parent.ID {
				return result, serviceDenied("ambiguous_synapse_workspace_member")
			}
			target = candidate
		}
		direct := kind == synapseSparkType || kind == synapseSQLType
		evidence := map[string]any{"resource_type": kind, "instance_id": id, "delete_by_default": true, "retention_supported": false, graph.LifecycleEvidenceControllerDeleteGuaranteed: true}
		if !direct {
			evidence[graph.LifecycleEvidenceUnselectedControllerAction] = graph.LifecycleUnselectedControllerSkip
		}
		metadata := synapseDataKind(kind).kind != ""
		if metadata {
			// These native data-plane objects are metadata within the workspace,
			// not independently retained storage. Workspace DELETE removes metadata.
			evidence[graph.LifecycleEvidenceControllerIntegratedResource] = true
			if synapseDataKind(kind).spark {
				evidence[graph.LifecycleEvidenceControllerMetadata] = true
			}
		} else {
			evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] = true
		}
		if target == nil {
			if member["absent"] == true {
				continue
			}
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: kind, NativeID: id, ControllerID: parent.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
			continue
		}
		if target.Location != parent.Location || target.Normalized["_synapse_private_configuration"] != member["configuration"] {
			return result, serviceDenied("synapse_workspace_member_configuration_changed")
		}
		if metadata && target.Normalized["_synapse_workspace"] != parent.Identity.NativeID {
			return result, serviceDenied("synapse_workspace_member_owner_changed")
		}
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: parent.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: direct && target.Normalized["cleanup_protected"] != true && target.Normalized["cleanup_controller_only"] != true, EvidenceSource: synapseWorkspaceLifecycleSource, Evidence: evidence, Confidence: 1})
		result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: parent.ID, Type: graph.RelationshipAttachedTo, Source: synapseWorkspaceLifecycleSource, Evidence: evidence, Confidence: 1})
	}
	return result, nil
}
