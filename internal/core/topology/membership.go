package topology

import (
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

// VPCGroupAssetIDs returns the open assets whose VPC membership is certain,
// together with the VPC and vSwitch boundary assets required to clean the group.
func VPCGroupAssetIDs(input Input, regionID, vpcID string) []asset.AssetID {
	regionID = strings.TrimSpace(regionID)
	vpcID = strings.TrimSpace(vpcID)
	if regionID == "" || vpcID == "" {
		return nil
	}

	projector := newProjector(input)
	selectedSet := make(map[asset.AssetID]struct{})
	for _, value := range projector.open {
		if assetRegion(value, projector.scopeByID) != regionID {
			continue
		}
		switch input.Kinds[value.ResourceKindID].Class {
		case "network.vpc":
			if projector.knownVPCs[regionID][vpcID].ID == value.ID {
				selectedSet[value.ID] = struct{}{}
			}
		case "network.subnet":
			vSwitchID := normalizedString(value.Normalized, NormalizedVSwitchID)
			if vSwitchID == "" {
				vSwitchID = strings.TrimSpace(value.Identity.NativeID)
			}
			if projector.knownVSwitches[regionID][vSwitchID].ID == value.ID &&
				projector.vSwitchBelongsToVPC(regionID, vSwitchID, vpcID) {
				selectedSet[value.ID] = struct{}{}
			}
		default:
			placement := projector.placements[value.ID]
			if placement.vpcID == vpcID && !placement.membershipUnknown && !placement.conflicting {
				selectedSet[value.ID] = struct{}{}
			}
		}
	}
	// A peer connection is a dependency of both endpoint VPCs. Its primary
	// placement can only name one VPC, so include it from authoritative
	// member_of relationships as well. This also covers cross-Region peers,
	// whose resource scope differs from the accepter VPC scope.
	vpcAssetID := projector.knownVPCs[regionID][vpcID].ID
	if vpcAssetID != "" {
		for _, relationship := range input.Relationships {
			if relationship.ClosedAt != nil ||
				relationship.Type != graph.RelationshipMemberOf ||
				relationship.TargetAssetID != vpcAssetID {
				continue
			}
			source, open := projector.openByID[relationship.SourceAssetID]
			if !open || input.Kinds[source.ResourceKindID].Class != "network.peer_connection" {
				continue
			}
			selectedSet[source.ID] = struct{}{}
		}
	}
	selected := make([]asset.AssetID, 0, len(selectedSet))
	for id := range selectedSet {
		selected = append(selected, id)
	}
	sort.Slice(selected, func(i, j int) bool { return selected[i] < selected[j] })
	return selected
}
