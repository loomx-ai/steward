package cleanup

import (
	"fmt"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/core/topology"
)

type SelectionInput struct {
	Selectors     []plan.CleanupSelector
	Connections   []asset.CloudConnection
	Scopes        []asset.Scope
	Assets        []asset.Asset
	Kinds         map[asset.ResourceKindID]asset.ResourceKind
	Relationships []graph.Relationship
}

type SelectionResult struct {
	Selectors        []plan.CleanupSelector
	AssetIDs         []asset.AssetID
	SelectorAssetIDs [][]asset.AssetID
	ConnectionIDs    []asset.ConnectionID
}

func ExpandSelectors(input SelectionInput) (SelectionResult, error) {
	if len(input.Selectors) == 0 {
		return SelectionResult{}, fmt.Errorf("cleanup task requires at least one selector")
	}
	scopes := make(map[asset.ScopeID]asset.Scope, len(input.Scopes))
	for _, scope := range input.Scopes {
		scopes[scope.ID] = scope
	}
	assets := make(map[asset.AssetID]asset.Asset, len(input.Assets))
	for _, value := range input.Assets {
		assets[value.ID] = value
	}
	connectionValues := make(map[asset.ConnectionID]asset.CloudConnection, len(input.Connections))
	for _, connection := range input.Connections {
		connectionValues[connection.ID] = connection
	}
	selected := make(map[asset.AssetID]struct{})
	connections := make(map[asset.ConnectionID]struct{})
	result := SelectionResult{
		Selectors:        make([]plan.CleanupSelector, 0, len(input.Selectors)),
		SelectorAssetIDs: make([][]asset.AssetID, 0, len(input.Selectors)),
	}
	for _, raw := range input.Selectors {
		selector := raw
		selectorAssets := make(map[asset.AssetID]struct{})
		switch selector.Kind {
		case plan.SelectorConnection:
			if selector.ConnectionID == "" {
				return SelectionResult{}, fmt.Errorf("connection selector requires connection_id")
			}
			if connection, ok := connectionValues[selector.ConnectionID]; ok {
				selector.DisplayName = connection.Principal
			} else if len(connectionValues) > 0 {
				return SelectionResult{}, fmt.Errorf("connection selector %q was not found", selector.ConnectionID)
			}
			connections[selector.ConnectionID] = struct{}{}
			selector.Descendants = true
			for _, value := range input.Assets {
				if value.ClosedAt == nil && value.Identity.ConnectionID == selector.ConnectionID {
					selectorAssets[value.ID] = struct{}{}
				}
			}
		case plan.SelectorScope:
			scope, ok := scopes[selector.ScopeID]
			if !ok || selector.ScopeID == "" {
				return SelectionResult{}, fmt.Errorf("scope selector %q was not found", selector.ScopeID)
			}
			if selector.ConnectionID == "" {
				selector.ConnectionID = scope.ConnectionID
			}
			if selector.ConnectionID != scope.ConnectionID {
				return SelectionResult{}, fmt.Errorf("scope %q does not belong to connection %q", selector.ScopeID, selector.ConnectionID)
			}
			selector.ScopeKind = scope.Kind
			selector.DisplayName = scope.Name
			connections[scope.ConnectionID] = struct{}{}
			allowed := map[asset.ScopeID]bool{scope.ID: true}
			if selector.Descendants {
				for changed := true; changed; {
					changed = false
					for _, candidate := range input.Scopes {
						if !allowed[candidate.ID] && allowed[candidate.ParentID] {
							allowed[candidate.ID] = true
							changed = true
						}
					}
				}
			}
			for _, value := range input.Assets {
				if value.ClosedAt == nil && allowed[value.ScopeID] {
					selectorAssets[value.ID] = struct{}{}
				}
			}
		case plan.SelectorGroup:
			focus, err := topology.ParseFocusKey(selector.GroupKey)
			if err != nil || focus.Kind != topology.FocusVPC {
				return SelectionResult{}, fmt.Errorf("group selector key is invalid")
			}
			if selector.ConnectionID == "" {
				return SelectionResult{}, fmt.Errorf("group selector requires connection_id")
			}
			if _, ok := connectionValues[selector.ConnectionID]; !ok && len(connectionValues) > 0 {
				return SelectionResult{}, fmt.Errorf("group connection %q was not found", selector.ConnectionID)
			}
			selector.ScopeID = ""
			selector.ScopeKind = ""
			selector.Descendants = false
			for _, scope := range input.Scopes {
				if scope.ConnectionID == selector.ConnectionID && scope.Kind == asset.ScopeRegion &&
					(scope.NativeID == focus.RegionID || scope.Location == focus.RegionID) {
					selector.ScopeID = scope.ID
					selector.ScopeKind = scope.Kind
					break
				}
			}
			selector.DisplayName = focus.VPCID
			connections[selector.ConnectionID] = struct{}{}
			connection := connectionValues[selector.ConnectionID]
			if connection.ID == "" {
				connection.ID = selector.ConnectionID
			}
			memberIDs := topology.VPCGroupAssetIDs(topology.Input{
				Connection: connection, Scopes: input.Scopes, Assets: input.Assets,
				Kinds: input.Kinds, Relationships: input.Relationships,
			}, focus.RegionID, focus.VPCID)
			members := make(map[asset.AssetID]struct{}, len(memberIDs))
			for _, id := range memberIDs {
				members[id] = struct{}{}
			}
			for _, value := range input.Assets {
				if _, member := members[value.ID]; !member {
					continue
				}
				boundary := input.Kinds[value.ResourceKindID].Class == "network.vpc"
				selectorAssets[value.ID] = struct{}{}
				if boundary && strings.TrimSpace(value.Name) != "" {
					selector.DisplayName = value.Name
				}
			}
		case plan.SelectorAsset:
			if selector.AssetID == "" {
				return SelectionResult{}, fmt.Errorf("asset selector requires asset_id")
			}
			selectorAssets[selector.AssetID] = struct{}{}
			if value, ok := assets[selector.AssetID]; ok {
				selector.ConnectionID = value.Identity.ConnectionID
				selector.ScopeID = value.ScopeID
				selector.DisplayName = value.Name
				connections[value.Identity.ConnectionID] = struct{}{}
			}
		default:
			return SelectionResult{}, fmt.Errorf("unsupported cleanup selector kind %q", selector.Kind)
		}
		resolved := make([]asset.AssetID, 0, len(selectorAssets))
		for id := range selectorAssets {
			selected[id] = struct{}{}
			resolved = append(resolved, id)
		}
		sort.Slice(resolved, func(i, j int) bool { return resolved[i] < resolved[j] })
		result.Selectors = append(result.Selectors, selector)
		result.SelectorAssetIDs = append(result.SelectorAssetIDs, resolved)
	}
	for id := range selected {
		result.AssetIDs = append(result.AssetIDs, id)
	}
	sort.Slice(result.AssetIDs, func(i, j int) bool { return result.AssetIDs[i] < result.AssetIDs[j] })
	for id := range connections {
		if id != "" {
			result.ConnectionIDs = append(result.ConnectionIDs, id)
		}
	}
	sort.Slice(result.ConnectionIDs, func(i, j int) bool { return result.ConnectionIDs[i] < result.ConnectionIDs[j] })
	return result, nil
}
