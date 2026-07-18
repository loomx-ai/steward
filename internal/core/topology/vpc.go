package topology

import (
	"fmt"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

func (p *projector) vpcResponse(base Response, regionID, vpcID string) (Response, error) {
	vpcAsset := p.knownVPCs[regionID][vpcID]
	vpcName := vpcID
	vpcKey := VPCFocusKey(regionID, vpcID)
	if vpcAsset.ID != "" {
		vpcKey = string(vpcAsset.ID)
		if strings.TrimSpace(vpcAsset.Name) != "" {
			vpcName = vpcAsset.Name
		}
	}

	allValues := []asset.Asset{}
	for _, value := range p.open {
		if p.isBoundary(value) || assetRegion(value, p.scopeByID) != regionID {
			continue
		}
		if p.placements[value.ID].vpcID == vpcID {
			allValues = append(allValues, value)
		}
	}
	filtered := p.filteredResources(allValues)
	sortAssets(filtered)
	page, start, end, next, truncated, err := paginateAssets(filtered, p.input.Cursor, p.input.Limit)
	if err != nil {
		return Response{}, err
	}
	resources := p.buildResources(page)
	edges, warnings := p.projectEdges(filtered, start, end, resources)

	view := VPCView{
		Kind: ViewVPC,
		Region: ViewContext{
			Key: RegionFocusKey(regionID), Name: p.regionName(regionID), NativeID: regionID,
		},
		VPC:                ViewContext{Key: vpcKey, Name: vpcName, NativeID: vpcID},
		PublicResourceKeys: []string{},
		VSwitches:          p.vSwitches(regionID, vpcID, filtered, page),
		Resources:          resources,
		Edges:              edges,
	}
	for _, value := range page {
		item := p.placements[value.ID]
		if item.vSwitchID == "" || !p.vSwitchBelongsToVPC(regionID, item.vSwitchID, vpcID) {
			view.PublicResourceKeys = append(view.PublicResourceKeys, string(value.ID))
		}
	}
	base.View = view
	base.Warnings = append(append([]ProjectionWarning(nil), p.warnings...), warnings...)
	base.NextCursor = next
	base.Truncated = truncated
	return base, nil
}

func (p *projector) vSwitches(regionID, vpcID string, allValues, page []asset.Asset) []VSwitch {
	result := []VSwitch{}
	for nativeID, value := range p.knownVSwitches[regionID] {
		if normalizedVPCID(value.Normalized) != vpcID {
			continue
		}
		key := string(value.ID)
		if key == "" {
			key = vSwitchProjectionKey(string(p.input.Connection.ID), regionID, nativeID)
		}
		name := strings.TrimSpace(value.Name)
		if name == "" {
			name = nativeID
		}
		item := VSwitch{
			Key: key, AssetID: string(value.ID), Dirty: value.Dirty, Name: name, NativeID: nativeID,
			Zone: normalizedString(value.Normalized, NormalizedZoneID), ResourceCount: 0, ResourceKeys: []string{},
		}
		for _, candidate := range allValues {
			if p.placements[candidate.ID].vSwitchID == nativeID {
				item.ResourceCount++
			}
		}
		for _, candidate := range page {
			if p.placements[candidate.ID].vSwitchID == nativeID {
				item.ResourceKeys = append(item.ResourceKeys, string(candidate.ID))
			}
		}
		result = append(result, item)
	}
	sort.SliceStable(result, func(i, j int) bool {
		return result[i].Key < result[j].Key
	})
	return result
}

func (p *projector) projectEdges(universe []asset.Asset, start, end int, resources []Resource) ([]ResourceEdge, []ProjectionWarning) {
	index := make(map[asset.AssetID]int, len(universe))
	for position, value := range universe {
		index[value.ID] = position
	}
	pageResources := make(map[asset.AssetID]*Resource, len(resources))
	for position := range resources {
		pageResources[resources[position].AssetID] = &resources[position]
	}
	edges := []ResourceEdge{}
	warnings := []ProjectionWarning{}
	for _, relationship := range p.input.Relationships {
		if relationship.ClosedAt != nil {
			continue
		}
		if relationship.Type == graph.RelationshipMemberOf && (p.boundaryID(relationship.SourceAssetID) || p.boundaryID(relationship.TargetAssetID)) {
			continue
		}
		edge := ResourceEdge{
			Key: string(relationship.ID), SourceKey: string(relationship.SourceAssetID), TargetKey: string(relationship.TargetAssetID),
			Kind: ProjectedRelationship, Relation: string(relationship.Type),
			Metadata: map[string]any{
				"source": relationship.Source, "confidence": relationship.Confidence,
				"evidence": relationship.Evidence, "graph_revision": relationship.GraphRevision,
			},
		}
		edges, warnings = p.placeEdge(edges, warnings, edge, relationship.SourceAssetID, relationship.TargetAssetID, index, start, end, pageResources)
	}
	for _, binding := range p.input.LifecycleBindings {
		if binding.ClosedAt != nil {
			continue
		}
		edge := ResourceEdge{
			Key: string(binding.ID), SourceKey: string(binding.ControllerAssetID), TargetKey: string(binding.ManagedAssetID),
			Kind: ProjectedLifecycle, Relation: string(binding.CleanupPolicy),
			Metadata: map[string]any{
				"authority": string(binding.Authority), "ownership": string(binding.Ownership),
				"cleanup_policy": string(binding.CleanupPolicy), "confidence": binding.Confidence,
				"direct_cleanup_allowed": binding.DirectCleanupAllowed,
				"evidence_source":        binding.EvidenceSource, "evidence": binding.Evidence,
				"graph_revision": binding.GraphRevision,
			},
		}
		edges, warnings = p.placeEdge(edges, warnings, edge, binding.ControllerAssetID, binding.ManagedAssetID, index, start, end, pageResources)
	}
	sort.SliceStable(edges, func(i, j int) bool { return edges[i].Key < edges[j].Key })
	sort.SliceStable(warnings, func(i, j int) bool {
		if warnings[i].RelationKey != warnings[j].RelationKey {
			return warnings[i].RelationKey < warnings[j].RelationKey
		}
		return warnings[i].VisibleAssetID < warnings[j].VisibleAssetID
	})
	return edges, warnings
}

func (p *projector) placeEdge(
	edges []ResourceEdge,
	warnings []ProjectionWarning,
	edge ResourceEdge,
	sourceID, targetID asset.AssetID,
	index map[asset.AssetID]int,
	start, end int,
	page map[asset.AssetID]*Resource,
) ([]ResourceEdge, []ProjectionWarning) {
	sourceIndex, sourceVisible := index[sourceID]
	targetIndex, targetVisible := index[targetID]
	if sourceVisible && targetVisible {
		owner := sourceIndex
		if targetIndex > owner {
			owner = targetIndex
		}
		if owner >= start && owner < end {
			edges = append(edges, edge)
		}
		return edges, warnings
	}
	if sourceVisible == targetVisible {
		return edges, warnings
	}
	visibleID, otherID, direction := sourceID, targetID, "outgoing"
	if targetVisible {
		visibleID, otherID, direction = targetID, sourceID, "incoming"
	}
	resource := page[visibleID]
	if resource == nil {
		return edges, warnings
	}
	other, exists := p.openByID[otherID]
	if !exists {
		warnings = append(warnings, ProjectionWarning{
			Code: "relation_endpoint_unavailable", RelationKey: edge.Key, VisibleAssetID: string(visibleID),
			Message: "related resource is unavailable or closed",
		})
		return edges, warnings
	}
	kind := p.input.Kinds[other.ResourceKindID]
	resource.ExternalRelations = append(resource.ExternalRelations, ExternalRelation{
		Key: edge.Key, Kind: edge.Kind, Relation: edge.Relation, Direction: direction,
		TargetID: other.ID, TargetName: other.Name, TargetType: kind.DisplayName,
		TargetResourceKindID: other.ResourceKindID, TargetClass: kind.Class,
	})
	sort.SliceStable(resource.ExternalRelations, func(i, j int) bool {
		return resource.ExternalRelations[i].Key < resource.ExternalRelations[j].Key
	})
	return edges, warnings
}

func (p *projector) boundaryID(id asset.AssetID) bool {
	value, ok := p.openByID[id]
	return ok && p.isBoundary(value)
}

func (p *projector) debugPlacement(value asset.Asset) string {
	item := p.placements[value.ID]
	return fmt.Sprintf("%s/%s", item.vpcID, item.vSwitchID)
}
