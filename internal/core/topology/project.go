package topology

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"strconv"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const (
	NormalizedVPCID     = "vpc_id"
	NormalizedVSwitchID = "vswitch_id"
	NormalizedZoneID    = "zone_id"
)

func Project(input Input) (Response, error) {
	if input.Limit == 0 {
		input.Limit = 200
	}
	if input.Limit < 1 || input.Limit > MaxResourceLimit {
		return Response{}, fmt.Errorf("topology resource limit is outside the allowed range")
	}
	if err := validateRisk(input.Risk); err != nil {
		return Response{}, err
	}
	if input.Focus.Kind == "" {
		input.Focus.Kind = FocusAccount
	}
	projector := newProjector(input)
	response := Response{Revision: input.Revision, Coverage: input.Coverage}
	switch input.Focus.Kind {
	case FocusAccount:
		response.View = projector.accountView()
	case FocusAccountGlobal:
		return projector.resourceGraphResponse(response, projector.globalAssets(), ViewContext{
			Key: AccountGlobalFocusKey(), Name: input.Connection.Name,
		})
	case FocusRegion:
		response.View = projector.regionView(input.Focus.RegionID)
	case FocusRegionPublic:
		return projector.resourceGraphResponse(response, projector.regionPublicAssets(input.Focus.RegionID), ViewContext{
			Key: RegionPublicFocusKey(input.Focus.RegionID), Name: projector.regionName(input.Focus.RegionID), NativeID: input.Focus.RegionID,
		}, ViewContext{
			Key: RegionFocusKey(input.Focus.RegionID), Name: projector.regionName(input.Focus.RegionID), NativeID: input.Focus.RegionID,
		})
	case FocusVPC:
		return projector.vpcResponse(response, input.Focus.RegionID, input.Focus.VPCID)
	default:
		return Response{}, fmt.Errorf("unsupported topology focus kind %q", input.Focus.Kind)
	}
	response.Warnings = append(response.Warnings, projector.warnings...)
	return response, nil
}

type placement struct {
	vpcID             string
	vSwitchID         string
	zone              string
	membershipUnknown bool
	conflicting       bool
}

type placementEvidence struct {
	regionID  string
	vpcID     string
	vSwitchID string
}

type projector struct {
	input          Input
	open           []asset.Asset
	allByID        map[asset.AssetID]asset.Asset
	openByID       map[asset.AssetID]asset.Asset
	scopeByID      map[asset.ScopeID]asset.Scope
	knownVPCs      map[string]map[string]asset.Asset
	knownVSwitches map[string]map[string]asset.Asset
	placements     map[asset.AssetID]placement
	warnings       []ProjectionWarning
}

func newProjector(input Input) *projector {
	p := &projector{
		input:          input,
		allByID:        make(map[asset.AssetID]asset.Asset, len(input.Assets)),
		openByID:       make(map[asset.AssetID]asset.Asset, len(input.Assets)),
		scopeByID:      make(map[asset.ScopeID]asset.Scope, len(input.Scopes)),
		knownVPCs:      make(map[string]map[string]asset.Asset),
		knownVSwitches: make(map[string]map[string]asset.Asset),
		placements:     make(map[asset.AssetID]placement),
	}
	for _, scope := range input.Scopes {
		p.scopeByID[scope.ID] = scope
	}
	for _, value := range input.Assets {
		p.allByID[value.ID] = value
		if value.ClosedAt != nil {
			continue
		}
		if input.Connection.ID != "" && value.Identity.ConnectionID != "" && value.Identity.ConnectionID != input.Connection.ID {
			continue
		}
		p.open = append(p.open, value)
		p.openByID[value.ID] = value
		if authoritativeScopeKind(value, p.scopeByID) == "" {
			p.warnings = append(p.warnings, ProjectionWarning{
				Code: "scope_unknown", VisibleAssetID: string(value.ID),
				Message: "resource is not assigned to an authoritative global or Region scope",
			})
		}
		class := input.Kinds[value.ResourceKindID].Class
		switch class {
		case "network.vpc":
			regionID := assetRegion(value, p.scopeByID)
			if regionID == "" {
				continue
			}
			nativeID := normalizedVPCID(value.Normalized)
			if nativeID == "" {
				nativeID = strings.TrimSpace(value.Identity.NativeID)
			}
			if nativeID != "" {
				if p.knownVPCs[regionID] == nil {
					p.knownVPCs[regionID] = make(map[string]asset.Asset)
				}
				p.knownVPCs[regionID][nativeID] = value
			}
		case "network.subnet":
			regionID := assetRegion(value, p.scopeByID)
			if regionID == "" {
				continue
			}
			nativeID := normalizedString(value.Normalized, NormalizedVSwitchID)
			if nativeID == "" {
				nativeID = strings.TrimSpace(value.Identity.NativeID)
			}
			if nativeID != "" {
				if p.knownVSwitches[regionID] == nil {
					p.knownVSwitches[regionID] = make(map[string]asset.Asset)
				}
				p.knownVSwitches[regionID][nativeID] = value
			}
		}
	}
	p.resolvePlacements()
	sort.SliceStable(p.warnings, func(i, j int) bool {
		if p.warnings[i].Code != p.warnings[j].Code {
			return p.warnings[i].Code < p.warnings[j].Code
		}
		if p.warnings[i].VisibleAssetID != p.warnings[j].VisibleAssetID {
			return p.warnings[i].VisibleAssetID < p.warnings[j].VisibleAssetID
		}
		return p.warnings[i].RelationKey < p.warnings[j].RelationKey
	})
	return p
}

func (p *projector) resolvePlacements() {
	explicit := make(map[asset.AssetID]bool, len(p.open))
	for _, value := range p.open {
		if p.isBoundary(value) || !p.participatesInNetworkPlacement(value) {
			continue
		}
		item := placement{
			vpcID:     normalizedVPCID(value.Normalized),
			vSwitchID: normalizedString(value.Normalized, NormalizedVSwitchID),
			zone:      normalizedString(value.Normalized, NormalizedZoneID),
		}
		explicit[value.ID] = item.vpcID != "" || item.vSwitchID != ""
		regionID := assetRegion(value, p.scopeByID)
		if explicit[value.ID] && regionID == "" {
			item.membershipUnknown = true
		}
		if item.vpcID != "" && p.knownVPCs[regionID][item.vpcID].ID == "" {
			item.membershipUnknown = true
		}
		if item.vpcID == "" && item.vSwitchID != "" {
			item.membershipUnknown = true
		}
		if item.vpcID != "" && item.vSwitchID != "" && p.knownVSwitches[regionID][item.vSwitchID].ID == "" {
			item.membershipUnknown = true
		} else if item.vpcID != "" && item.vSwitchID != "" && !p.vSwitchBelongsToVPC(regionID, item.vSwitchID, item.vpcID) {
			item.membershipUnknown = true
			p.warnings = append(p.warnings, ProjectionWarning{
				Code: "vswitch_parent_mismatch", VisibleAssetID: string(value.ID),
				Message: "vSwitch belongs to a different VPC than the resource placement",
			})
		}
		p.placements[value.ID] = item
	}

	adjacency := make(map[asset.AssetID]map[asset.AssetID]struct{})
	for _, value := range p.open {
		if !p.isBoundary(value) && p.participatesInNetworkPlacement(value) {
			adjacency[value.ID] = make(map[asset.AssetID]struct{})
		}
	}
	for _, relationship := range p.input.Relationships {
		if !relationshipSupportsNetworkPlacement(relationship) {
			continue
		}
		if adjacency[relationship.SourceAssetID] == nil || adjacency[relationship.TargetAssetID] == nil {
			continue
		}
		adjacency[relationship.SourceAssetID][relationship.TargetAssetID] = struct{}{}
		adjacency[relationship.TargetAssetID][relationship.SourceAssetID] = struct{}{}
	}
	ids := make([]asset.AssetID, 0, len(adjacency))
	for id := range adjacency {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	visited := make(map[asset.AssetID]struct{}, len(ids))
	for _, root := range ids {
		if _, seen := visited[root]; seen {
			continue
		}
		component := []asset.AssetID{}
		queue := []asset.AssetID{root}
		visited[root] = struct{}{}
		for len(queue) > 0 {
			id := queue[0]
			queue = queue[1:]
			component = append(component, id)
			for neighbor := range adjacency[id] {
				if _, seen := visited[neighbor]; seen {
					continue
				}
				visited[neighbor] = struct{}{}
				queue = append(queue, neighbor)
			}
		}
		p.resolveAttachedComponent(component, explicit)
	}
}

func relationshipSupportsNetworkPlacement(relationship graph.Relationship) bool {
	if relationship.ClosedAt != nil || relationship.Type != graph.RelationshipAttachedTo {
		return false
	}
	// CEN attached_to edges describe control-plane connectivity and routing.
	// In particular, a peer attachment intentionally connects transit routers
	// in different Regions, so it must not be treated as proof that both
	// endpoints share one Region, VPC, or vSwitch.
	return !strings.HasPrefix(
		strings.ToLower(strings.TrimSpace(relationship.Source)),
		"cen:",
	)
}

func (p *projector) resolveAttachedComponent(component []asset.AssetID, explicit map[asset.AssetID]bool) {
	component = append([]asset.AssetID(nil), component...)
	sort.Slice(component, func(i, j int) bool { return component[i] < component[j] })
	evidence := make([]placementEvidence, 0, len(component))
	invalidExplicit := false
	for _, id := range component {
		if !explicit[id] {
			continue
		}
		value := p.openByID[id]
		item := p.placements[id]
		if item.membershipUnknown || item.conflicting {
			invalidExplicit = true
			continue
		}
		regionID := assetRegion(value, p.scopeByID)
		if regionID == "" || item.vpcID == "" || p.knownVPCs[regionID][item.vpcID].ID == "" {
			invalidExplicit = true
			continue
		}
		if item.vSwitchID != "" && !p.vSwitchBelongsToVPC(regionID, item.vSwitchID, item.vpcID) {
			invalidExplicit = true
			continue
		}
		evidence = append(evidence, placementEvidence{
			regionID: regionID, vpcID: item.vpcID, vSwitchID: item.vSwitchID,
		})
	}
	if len(evidence) == 0 && !invalidExplicit {
		return
	}
	placementConflict := invalidExplicit
	var candidate placementEvidence
	if len(evidence) > 0 {
		candidate = evidence[0]
		for _, endpoint := range evidence[1:] {
			if endpoint.regionID != candidate.regionID || endpoint.vpcID != candidate.vpcID {
				placementConflict = true
			}
		}
	}
	var inferred placement
	vSwitchConflict := false
	if !placementConflict && len(evidence) > 0 {
		inferred.vpcID = candidate.vpcID
		allComplete := true
		commonVSwitch := ""
		for _, endpoint := range evidence {
			if endpoint.vSwitchID == "" {
				allComplete = false
				continue
			}
			if commonVSwitch == "" {
				commonVSwitch = endpoint.vSwitchID
			} else if endpoint.vSwitchID != commonVSwitch {
				vSwitchConflict = true
			}
		}
		if !vSwitchConflict && allComplete && commonVSwitch != "" {
			inferred.vSwitchID = commonVSwitch
			inferred.zone = normalizedString(
				p.knownVSwitches[candidate.regionID][commonVSwitch].Normalized,
				NormalizedZoneID,
			)
		}
	}
	for _, id := range component {
		if explicit[id] {
			continue
		}
		value := p.openByID[id]
		regionID := assetRegion(value, p.scopeByID)
		nodeConflict := placementConflict || regionID == "" || regionID != candidate.regionID
		if nodeConflict {
			item := placement{
				membershipUnknown: true,
				conflicting:       true,
			}
			p.placements[id] = item
			p.warnings = append(p.warnings, ProjectionWarning{
				Code: "placement_conflict", VisibleAssetID: string(id),
				Message: "attached resources disagree on network placement",
			})
			continue
		}
		if vSwitchConflict {
			item := inferred
			item.membershipUnknown = true
			item.conflicting = true
			p.placements[id] = item
			p.warnings = append(p.warnings, ProjectionWarning{
				Code: "placement_conflict", VisibleAssetID: string(id),
				Message: "attached resources disagree on network placement",
			})
			continue
		}
		p.placements[id] = inferred
	}
}

func (p *projector) accountView() AccountView {
	regions := append([]asset.ConnectionRegion(nil), p.input.Regions...)
	sort.SliceStable(regions, func(i, j int) bool {
		if regions[i].RegionID != regions[j].RegionID {
			return asset.RegionIDLess(regions[i].RegionID, regions[j].RegionID)
		}
		return regions[i].ID < regions[j].ID
	})
	view := AccountView{Kind: ViewAccount, Regions: make([]EntrySummary, 0, len(regions))}
	globalAssets := p.globalAssets()
	globalScope := p.globalScope()
	globalCount := len(globalAssets)
	globalCleanup := p.exactScopeCleanup(globalScope, globalAssets)
	if p.input.AssetCountsByScope != nil {
		globalCount = p.assetCountInScope(globalScope.ID)
		globalCleanup = p.countedScopeCleanup(globalScope, globalCount)
	}
	if p.hasResourceFilter() {
		globalCleanup = CleanupSummary{}
	}
	if globalCount > 0 {
		view.GlobalResources = &EntrySummary{
			Key: AccountGlobalFocusKey(), Name: "Global resources", ResourceCount: globalCount,
			Cleanup: globalCleanup,
		}
	}
	for _, region := range regions {
		if region.ConnectionID != p.input.Connection.ID || region.Lifecycle != asset.RegionActive {
			continue
		}
		regionScope := p.regionScope(region.RegionID)
		count := 0
		for _, value := range p.open {
			if assetRegion(value, p.scopeByID) == region.RegionID {
				count++
			}
		}
		cleanup := p.exactScopeCleanup(regionScope, p.regionAssets(region.RegionID))
		if p.input.AssetCountsByScope != nil {
			count = p.assetCountInScope(regionScope.ID)
			cleanup = p.countedScopeCleanup(regionScope, count)
		}
		if p.hasResourceFilter() {
			cleanup = CleanupSummary{}
		}
		if p.input.ResourceQueryApplied && count == 0 {
			continue
		}
		view.Regions = append(view.Regions, EntrySummary{
			Key: RegionFocusKey(region.RegionID), Name: region.EffectiveName(), NativeID: region.RegionID, ResourceCount: count,
			Cleanup: cleanup,
		})
	}
	return view
}

func (p *projector) regionView(regionID string) RegionView {
	public := p.filteredResources(p.regionPublicAssets(regionID))
	view := RegionView{
		Kind:   ViewRegion,
		Region: ViewContext{Key: RegionFocusKey(regionID), Name: p.regionName(regionID), NativeID: regionID},
		PublicResources: EntrySummary{
			Key: RegionPublicFocusKey(regionID), Name: "Public resources", ResourceCount: len(public),
			Cleanup: CleanupSummary{},
		},
		VPCs: []EntrySummary{},
	}
	for nativeID, value := range p.knownVPCs[regionID] {
		// The VPC is the container represented by this summary card. Count only
		// the boundary and resource nodes that the user will see after drilling
		// into it, otherwise an empty VPC misleadingly appears to contain itself.
		count := 0
		for vSwitchID, vSwitch := range p.knownVSwitches[regionID] {
			if p.vSwitchBelongsToVPC(regionID, vSwitchID, nativeID) && p.resourceMatchesFilters(vSwitch) {
				count++
			}
		}
		uncertainMembership := false
		for _, candidate := range p.filteredResources(p.open) {
			if p.isBoundary(candidate) || assetRegion(candidate, p.scopeByID) != regionID {
				continue
			}
			placement := p.placements[candidate.ID]
			if placement.vpcID == nativeID {
				count++
				if placement.membershipUnknown || placement.conflicting {
					uncertainMembership = true
				}
			}
		}
		name := strings.TrimSpace(value.Name)
		if name == "" {
			name = nativeID
		}
		cleanup := p.groupCleanup(VPCFocusKey(regionID, nativeID))
		if p.hasResourceFilter() {
			cleanup = CleanupSummary{}
		}
		if uncertainMembership {
			cleanup = CleanupSummary{PotentialBlockers: 1}
		}
		view.VPCs = append(view.VPCs, EntrySummary{
			Key: VPCFocusKey(regionID, nativeID), AssetID: value.ID, Dirty: value.Dirty, Name: name, NativeID: nativeID, ResourceCount: count,
			Cleanup: cleanup,
		})
	}
	sort.SliceStable(view.VPCs, func(i, j int) bool {
		if view.VPCs[i].Name != view.VPCs[j].Name {
			return view.VPCs[i].Name < view.VPCs[j].Name
		}
		return view.VPCs[i].Key < view.VPCs[j].Key
	})
	return view
}

func (p *projector) globalAssets() []asset.Asset {
	included := make(map[asset.AssetID]struct{})
	pending := make([]asset.AssetID, 0)
	for _, value := range p.open {
		if authoritativeScopeKind(value, p.scopeByID) == asset.ScopeGlobal {
			included[value.ID] = struct{}{}
			pending = append(pending, value.ID)
		}
	}
	childrenByParent := make(map[asset.AssetID][]asset.AssetID)
	for _, relationship := range p.input.Relationships {
		if relationship.ClosedAt != nil ||
			relationship.Type != graph.RelationshipMemberOf ||
			relationship.SourceAssetID == "" ||
			relationship.TargetAssetID == "" {
			continue
		}
		childrenByParent[relationship.TargetAssetID] = append(
			childrenByParent[relationship.TargetAssetID],
			relationship.SourceAssetID,
		)
	}
	for len(pending) > 0 {
		parentID := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		for _, childID := range childrenByParent[parentID] {
			if _, exists := included[childID]; exists {
				continue
			}
			child, exists := p.openByID[childID]
			if !exists || authoritativeScopeKind(child, p.scopeByID) == "" {
				continue
			}
			included[childID] = struct{}{}
			pending = append(pending, childID)
		}
	}
	result := make([]asset.Asset, 0, len(included))
	for _, value := range p.open {
		if _, exists := included[value.ID]; exists {
			result = append(result, value)
		}
	}
	return result
}

func (p *projector) regionPublicAssets(regionID string) []asset.Asset {
	result := []asset.Asset{}
	for _, value := range p.open {
		if p.isBoundary(value) || assetRegion(value, p.scopeByID) != regionID {
			continue
		}
		item := p.placements[value.ID]
		if item.vpcID == "" || p.knownVPCs[regionID][item.vpcID].ID == "" {
			result = append(result, value)
		}
	}
	return result
}

func (p *projector) regionAssets(regionID string) []asset.Asset {
	result := []asset.Asset{}
	for _, value := range p.open {
		if assetRegion(value, p.scopeByID) == regionID {
			result = append(result, value)
		}
	}
	return result
}

func (p *projector) resourceGraphResponse(
	base Response,
	values []asset.Asset,
	context ViewContext,
	ancestors ...ViewContext,
) (Response, error) {
	filtered := p.filteredResources(values)
	sortAssets(filtered)
	page, start, end, next, truncated, err := paginateAssets(filtered, p.input.Cursor, p.input.Limit)
	if err != nil {
		return Response{}, err
	}
	resources := p.buildResources(page)
	edges, warnings := p.projectEdges(filtered, start, end, resources)
	base.View = ResourceGraphView{
		Kind:      ViewResourceGraph,
		Context:   context,
		Ancestors: append([]ViewContext(nil), ancestors...),
		Resources: resources,
		Edges:     edges,
	}
	base.Warnings = append(append([]ProjectionWarning(nil), p.warnings...), warnings...)
	base.NextCursor = next
	base.Truncated = truncated
	return base, nil
}

func (p *projector) filteredResources(values []asset.Asset) []asset.Asset {
	result := make([]asset.Asset, 0, len(values))
	for _, value := range values {
		if !p.resourceMatchesFilters(value) {
			continue
		}
		result = append(result, value)
	}
	return result
}

func (p *projector) resourceMatchesFilters(value asset.Asset) bool {
	kind := p.input.Kinds[value.ResourceKindID]
	if p.input.ResourceClass != "" && kind.Class != p.input.ResourceClass {
		return false
	}
	if len(p.input.ResourceKindIDs) > 0 {
		selected := false
		for _, kindID := range p.input.ResourceKindIDs {
			if value.ResourceKindID == kindID {
				selected = true
				break
			}
		}
		if !selected {
			return false
		}
	}
	selectable := value.Capabilities.Has(asset.CapabilityActionable)
	switch p.input.Risk {
	case "actionable":
		return selectable
	case "findings":
		return p.input.FindingCounts[value.ID] > 0
	case "blockers":
		return p.cleanup(value).PotentialBlockers > 0
	default:
		return true
	}
}

func (p *projector) hasResourceFilter() bool {
	return p.input.ResourceClass != "" || len(p.input.ResourceKindIDs) > 0 ||
		p.input.ResourceQueryApplied || p.input.Risk != ""
}

func (p *projector) buildResources(values []asset.Asset) []Resource {
	result := make([]Resource, 0, len(values))
	for _, value := range values {
		kind := p.input.Kinds[value.ResourceKindID]
		name := strings.TrimSpace(value.Name)
		if name == "" {
			name = strings.TrimSpace(value.Identity.NativeID)
		}
		result = append(result, Resource{
			Key: string(value.ID), AssetID: value.ID, Dirty: value.Dirty, ResourceKindID: value.ResourceKindID,
			Name: name, NativeID: strings.TrimSpace(value.Identity.NativeID),
			TypeName: kind.DisplayName, TypeNames: maps.Clone(kind.DisplayNames),
			Icon: kind.Icon, Class: kind.Class,
			ConsoleLinkValues: p.consoleLinkValues(value, kind.ConsoleLinkTemplate),
			Domain:            ResourceDomain(kind), State: value.State, FindingCount: p.input.FindingCounts[value.ID],
			Actionable:        value.Capabilities.Has(asset.CapabilityActionable),
			MembershipUnknown: p.placements[value.ID].membershipUnknown,
			Cleanup:           p.cleanup(value),
		})
	}
	return result
}

func (p *projector) consoleLinkValues(
	resource asset.Asset,
	template string,
) map[string]string {
	values := map[string]string{}
	for field, raw := range resource.Normalized {
		if !strings.Contains(template, "{"+field+"}") &&
			!strings.Contains(template, "{"+field+"|") {
			continue
		}
		var value string
		switch typed := raw.(type) {
		case string:
			value = strings.TrimSpace(typed)
		case json.Number:
			value = typed.String()
		case float64:
			value = strconv.FormatFloat(typed, 'f', -1, 64)
		case float32:
			value = strconv.FormatFloat(float64(typed), 'f', -1, 32)
		case int:
			value = strconv.Itoa(typed)
		case int64:
			value = strconv.FormatInt(typed, 10)
		case int32:
			value = strconv.FormatInt(int64(typed), 10)
		case uint:
			value = strconv.FormatUint(uint64(typed), 10)
		case uint64:
			value = strconv.FormatUint(typed, 10)
		case uint32:
			value = strconv.FormatUint(uint64(typed), 10)
		}
		if value != "" {
			values[field] = value
		}
	}
	if strings.Contains(template, "{regionId}") && values["regionId"] == "" {
		regionID := strings.TrimSpace(resource.Location)
		if !strings.EqualFold(regionID, "global") {
			values["regionId"] = regionID
		}
	}
	if strings.Contains(template, "{parentId}") && values["parentId"] == "" {
		parentIDs := map[string]struct{}{}
		for _, relationship := range p.input.Relationships {
			if relationship.ClosedAt != nil ||
				relationship.Type != graph.RelationshipMemberOf ||
				relationship.SourceAssetID != resource.ID {
				continue
			}
			parent := p.openByID[relationship.TargetAssetID]
			parentID := strings.TrimSpace(parent.Identity.NativeID)
			if parentID != "" {
				parentIDs[parentID] = struct{}{}
			}
		}
		if len(parentIDs) == 1 {
			for parentID := range parentIDs {
				values["parentId"] = parentID
			}
		}
	}
	if len(values) == 0 {
		return nil
	}
	return values
}

func (p *projector) cleanup(value asset.Asset) CleanupSummary {
	selectable := value.Capabilities.Has(asset.CapabilityActionable)
	blockers := 0
	if selectable && p.input.Coverage.Status != "" && p.input.Coverage.Status != "complete" {
		blockers = 1
	}
	return CleanupSummary{
		Selectable: selectable, SelectorKind: "asset", SelectorKey: string(value.ID),
		Confirmation: "confirm", PotentialBlockers: blockers,
	}
}

func (p *projector) groupCleanup(key string) CleanupSummary {
	blockers := 0
	if p.input.Coverage.Status != "" && p.input.Coverage.Status != "complete" {
		blockers = 1
	}
	return CleanupSummary{
		Selectable: true, SelectorKind: "group", SelectorKey: key,
		Confirmation: "confirm", PotentialBlockers: blockers,
	}
}

func (p *projector) exactScopeCleanup(scope asset.Scope, projected []asset.Asset) CleanupSummary {
	if scope.ID == "" || len(projected) == 0 || !sameAssetSet(projected, p.assetsInScope(scope.ID)) {
		return CleanupSummary{}
	}
	return p.scopeCleanup(scope)
}

func (p *projector) countedScopeCleanup(scope asset.Scope, count int) CleanupSummary {
	if scope.ID == "" || count == 0 {
		return CleanupSummary{}
	}
	return p.scopeCleanup(scope)
}

func (p *projector) scopeCleanup(scope asset.Scope) CleanupSummary {
	blockers := 0
	if p.input.Coverage.Status != "" && p.input.Coverage.Status != "complete" {
		blockers = 1
	}
	confirmation := "confirm"
	if scope.Kind == asset.ScopeAccount || scope.Kind == asset.ScopeRegion {
		confirmation = "type_name"
	}
	return CleanupSummary{
		Selectable: true, SelectorKind: "scope", SelectorKey: string(scope.ID),
		Confirmation: confirmation, PotentialBlockers: blockers,
	}
}

func (p *projector) assetCountInScope(scopeID asset.ScopeID) int {
	if scopeID == "" {
		return 0
	}
	allowed := map[asset.ScopeID]bool{scopeID: true}
	for changed := true; changed; {
		changed = false
		for _, scope := range p.input.Scopes {
			if !allowed[scope.ID] && allowed[scope.ParentID] {
				allowed[scope.ID] = true
				changed = true
			}
		}
	}
	count := 0
	for candidate, value := range p.input.AssetCountsByScope {
		if allowed[candidate] {
			count += value
		}
	}
	return count
}

func (p *projector) globalScope() asset.Scope {
	var result asset.Scope
	for _, scope := range p.input.Scopes {
		if scope.ConnectionID != p.input.Connection.ID || scope.Kind != asset.ScopeGlobal {
			continue
		}
		if result.ID != "" {
			return asset.Scope{}
		}
		result = scope
	}
	return result
}

func (p *projector) regionScope(regionID string) asset.Scope {
	var result asset.Scope
	for _, scope := range p.input.Scopes {
		if scope.ConnectionID != p.input.Connection.ID || scope.Kind != asset.ScopeRegion ||
			(scope.NativeID != regionID && scope.Location != regionID) {
			continue
		}
		if result.ID != "" {
			return asset.Scope{}
		}
		result = scope
	}
	return result
}

func (p *projector) assetsInScope(scopeID asset.ScopeID) []asset.Asset {
	allowed := map[asset.ScopeID]bool{scopeID: true}
	for changed := true; changed; {
		changed = false
		for _, scope := range p.input.Scopes {
			if !allowed[scope.ID] && allowed[scope.ParentID] {
				allowed[scope.ID] = true
				changed = true
			}
		}
	}
	result := []asset.Asset{}
	for _, value := range p.open {
		if allowed[value.ScopeID] {
			result = append(result, value)
		}
	}
	return result
}

func sameAssetSet(left, right []asset.Asset) bool {
	if len(left) != len(right) {
		return false
	}
	ids := make(map[asset.AssetID]struct{}, len(left))
	for _, value := range left {
		ids[value.ID] = struct{}{}
	}
	for _, value := range right {
		if _, ok := ids[value.ID]; !ok {
			return false
		}
	}
	return true
}

func (p *projector) vSwitchBelongsToVPC(regionID, vSwitchID, vpcID string) bool {
	value := p.knownVSwitches[regionID][vSwitchID]
	return value.ID != "" && normalizedVPCID(value.Normalized) == vpcID
}

func (p *projector) isBoundary(value asset.Asset) bool {
	class := p.input.Kinds[value.ResourceKindID].Class
	return class == "network.vpc" || class == "network.subnet"
}

func (p *projector) participatesInNetworkPlacement(value asset.Asset) bool {
	return p.input.Kinds[value.ResourceKindID].Class != "orchestration.stack_group"
}

func (p *projector) regionName(regionID string) string {
	for _, region := range p.input.Regions {
		if region.ConnectionID == p.input.Connection.ID && region.RegionID == regionID {
			return region.EffectiveName()
		}
	}
	return regionID
}

func ResourceDomain(kind asset.ResourceKind) Domain {
	switch {
	case strings.HasPrefix(kind.Class, "network."):
		return DomainNetwork
	case strings.HasPrefix(kind.Class, "compute."), strings.HasPrefix(kind.Class, "container."):
		return DomainCompute
	case strings.HasPrefix(kind.Class, "storage."):
		return DomainStorage
	default:
		return DomainUnknown
	}
}

func validateRisk(value string) error {
	switch value {
	case "", "actionable", "findings", "blockers":
		return nil
	default:
		return fmt.Errorf("unsupported topology risk filter %q", value)
	}
}

func normalizedString(values map[string]any, key string) string {
	value, ok := values[key]
	if !ok || value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func normalizedVPCID(values map[string]any) string {
	if value := normalizedString(values, NormalizedVPCID); value != "" {
		return value
	}
	candidates := make(map[string]struct{})
	collectNormalizedVPCIDs(values, candidates)
	if len(candidates) != 1 {
		return ""
	}
	for candidate := range candidates {
		return candidate
	}
	return ""
}

func collectNormalizedVPCIDs(value any, candidates map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if canonicalTopologyField(key) == "vpcid" {
				if candidate, ok := child.(string); ok {
					if candidate = strings.TrimSpace(candidate); candidate != "" {
						candidates[candidate] = struct{}{}
					}
				}
			}
			collectNormalizedVPCIDs(child, candidates)
		}
	case []any:
		for _, child := range typed {
			collectNormalizedVPCIDs(child, candidates)
		}
	}
}

func canonicalTopologyField(value string) string {
	return strings.ToLower(
		strings.NewReplacer("_", "", "-", "").Replace(strings.TrimSpace(value)),
	)
}

func assetRegion(value asset.Asset, scopes map[asset.ScopeID]asset.Scope) string {
	scope, ok := asset.AuthoritativeScope(value.ScopeID, scopes)
	if !ok || scope.Kind != asset.ScopeRegion {
		return ""
	}
	if nativeID := strings.TrimSpace(scope.NativeID); nativeID != "" {
		return nativeID
	}
	return strings.TrimSpace(scope.Location)
}

func authoritativeScopeKind(value asset.Asset, scopes map[asset.ScopeID]asset.Scope) asset.ScopeKind {
	scope, ok := asset.AuthoritativeScope(value.ScopeID, scopes)
	if !ok {
		return ""
	}
	return scope.Kind
}

func sortAssets(values []asset.Asset) {
	sort.SliceStable(values, func(i, j int) bool {
		return values[i].ID < values[j].ID
	})
}

func paginateAssets(values []asset.Asset, cursor string, limit int) ([]asset.Asset, int, int, string, bool, error) {
	start := 0
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return nil, 0, 0, "", false, fmt.Errorf("decode topology cursor: %w", err)
		}
		key := asset.AssetID(decoded)
		index := sort.Search(len(values), func(index int) bool { return values[index].ID >= key })
		if index >= len(values) || values[index].ID != key {
			return nil, 0, 0, "", false, fmt.Errorf("topology cursor key is no longer present")
		}
		start = index + 1
	}
	end := start + limit
	if end > len(values) {
		end = len(values)
	}
	truncated := end < len(values)
	next := ""
	if truncated && end > start {
		next = base64.RawURLEncoding.EncodeToString([]byte(values[end-1].ID))
	}
	return values[start:end], start, end, next, truncated, nil
}
