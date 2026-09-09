package azure

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

// Only documented same-profile references authorize shared prerequisites.
// Host names and Key Vault URLs do not establish ownership of external assets.
func cdnReferenceIDs(kind string, raw map[string]any) ([]string, error) {
	if canonical, ok := findType(kind); ok {
		kind = canonical.NativeType
	}
	fields := map[string][]string{}
	switch kind {
	case afdRouteType:
		fields = map[string][]string{"customDomains": {afdDomainType}, "originGroup": {afdOriginGroupType}, "ruleSets": {afdRuleSetType}}
	case afdRuleType:
		fields["originGroup"] = []string{afdOriginGroupType}
	case afdDomainType:
		fields["secret"] = []string{afdSecretType}
	case afdSecurityPolicyType:
		fields["domains"] = []string{afdDomainType, afdEndpointType}
	case cdnOriginGroupType:
		fields["origins"] = []string{cdnOriginType}
	case cdnEndpointType:
		fields["defaultOriginGroup"], fields["originGroup"] = []string{cdnOriginGroupType}, []string{cdnOriginGroupType}
	}
	refs := []string{}
	var reference func(any, []string) error
	reference = func(value any, types []string) error {
		if value == nil {
			return nil
		}
		if values, ok := value.([]any); ok {
			for _, value := range values {
				if err := reference(value, types); err != nil {
					return err
				}
			}
			return nil
		}
		id, parsedType, err := parseID(text(object(value)["id"]))
		if err != nil || !slices.ContainsFunc(types, func(kind string) bool { return strings.EqualFold(parsedType, kind) }) || cdnProfileID(id) != cdnProfileID(text(raw["id"])) {
			return serviceDenied("invalid_cdn_reference")
		}
		if (kind == cdnOriginGroupType || kind == cdnEndpointType) && !strings.HasPrefix(id, strings.Join(strings.Split(strings.ToLower(text(raw["id"])), "/")[:11], "/")+"/") {
			return serviceDenied("invalid_cdn_endpoint_reference")
		}
		refs = append(refs, id)
		return nil
	}
	var visit func(any) error
	visit = func(value any) error {
		switch value := value.(type) {
		case map[string]any:
			for key, nested := range value {
				if types := fields[key]; types != nil {
					if err := reference(nested, types); err != nil {
						return err
					}
				} else if err := visit(nested); err != nil {
					return err
				}
			}
		case []any:
			for _, nested := range value {
				if err := visit(nested); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := visit(object(raw["properties"])); err != nil {
		return nil, err
	}
	slices.Sort(refs)
	return slices.Compact(refs), nil
}

func cdnIncomingKinds(kind string) []string {
	switch kind {
	case afdEndpointType:
		return []string{afdSecurityPolicyType}
	case afdDomainType:
		return []string{afdRouteType, afdSecurityPolicyType}
	case afdOriginGroupType:
		return []string{afdRouteType, afdRuleType}
	case afdRuleSetType:
		return []string{afdRouteType}
	case afdSecretType:
		return []string{afdDomainType}
	case cdnOriginType:
		return []string{cdnOriginGroupType}
	case cdnOriginGroupType:
		return []string{cdnEndpointType}
	}
	return nil
}

// Native recursive lists are already shared with inventory and cascade review.
// Read only the branches which can refer to this target, twice for stability.
func (c *client) cdnIncoming(ctx context.Context, target asset.Identity, profile map[string]any) ([]serviceChild, error) {
	index, err := c.cdnIncomingIndex(ctx, target, profile)
	return index[strings.ToLower(target.NativeID)], err
}

func (c *client) cdnIncomingIndex(ctx context.Context, target asset.Identity, profile map[string]any) (map[string][]serviceChild, error) {
	kinds := cdnIncomingKinds(target.NativeType)
	if len(kinds) == 0 {
		return nil, nil
	}
	var collect func(asset.Identity, map[string]any) ([]serviceChild, error)
	collect = func(parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
		branches := []string{}
		for _, childKind := range serviceChildKinds(parent.NativeType) {
			needed := slices.ContainsFunc(kinds, func(kind string) bool { return kind == childKind || strings.HasPrefix(kind, childKind+"/") })
			if needed {
				branches = append(branches, childKind)
			}
		}
		children, err := c.nativeServiceChildren(ctx, parent, raw, branches)
		if err != nil {
			return nil, err
		}
		result := []serviceChild{}
		for _, child := range children {
			if slices.Contains(kinds, child.kind) {
				refs, err := cdnReferenceIDs(child.kind, child.data)
				if err != nil {
					return nil, err
				}
				if len(refs) > 0 {
					result = append(result, child)
				}
			}
			if slices.ContainsFunc(kinds, func(kind string) bool { return strings.HasPrefix(kind, child.kind+"/") }) {
				nested, err := collect(asset.Identity{NativeID: child.id, NativeType: child.kind}, child.data)
				if err != nil {
					return nil, err
				}
				result = append(result, nested...)
			}
		}
		slices.SortFunc(result, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
		return result, nil
	}
	parent := asset.Identity{NativeID: cdnProfileID(target.NativeID), NativeType: cdnProfileType}
	first, err := collect(parent, profile)
	if err != nil {
		return nil, err
	}
	second, err := collect(parent, profile)
	if err != nil {
		return nil, err
	}
	if !slices.EqualFunc(first, second, func(a, b serviceChild) bool {
		return a.id == b.id && c.privateConfiguration(cdnSnapshot(a.kind, a.data)) == c.privateConfiguration(cdnSnapshot(b.kind, b.data))
	}) {
		return nil, serviceDenied("cdn_references_changed")
	}
	index := map[string][]serviceChild{}
	for _, child := range second {
		refs, _ := cdnReferenceIDs(child.kind, child.data)
		for _, id := range refs {
			index[id] = append(index[id], child)
		}
	}
	return index, nil
}

func (s *serviceCascades) contributeCDNReferences(ctx context.Context, assets []asset.Asset, result *governance.Contribution) error {
	// Share each native referring collection within this contribution. Listing
	// every route once per domain would make profile scans quadratic.
	profiles := map[string]map[string]any{}
	indexes := map[string]map[string][]serviceChild{}
	for _, target := range assets {
		if target.Identity.Provider != asset.ProviderAzure || len(cdnIncomingKinds(target.Identity.NativeType)) == 0 {
			continue
		}
		profileID := cdnProfileID(target.Identity.NativeID)
		profile := profiles[profileID]
		if profile == nil {
			var err error
			profile, err = s.client.cdnProfile(ctx, target.Identity.NativeID)
			if err != nil {
				return err
			}
			profiles[profileID] = profile
		}
		if expected := text(target.Normalized["_cdn_profile_configuration"]); expected == "" || expected != cdnConfiguration(cdnProfileType, profile) {
			return serviceDenied("cdn_profile_changed")
		}
		key := profileID + "|" + target.Identity.NativeType
		index, present := indexes[key]
		if !present {
			var err error
			index, err = s.client.cdnIncomingIndex(ctx, target.Identity, profile)
			if err != nil {
				return err
			}
			indexes[key] = index
		}
		incoming := index[strings.ToLower(target.Identity.NativeID)]
		for _, child := range incoming {
			// An endpoint's default origin group cannot be deleted independently
			// while referenced. Endpoint/profile cleanup already owns that group;
			// making the endpoint its prerequisite would create a ownership cycle.
			if child.kind == cdnEndpointType {
				continue
			}
			evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "resource_type": child.kind, "instance_id": child.id}
			controllers := map[string]any{}
			for _, controller := range assets {
				if controller.Identity.Provider != target.Identity.Provider || controller.Identity.ConnectionID != target.Identity.ConnectionID || controller.Identity.Partition != target.Identity.Partition {
					continue
				}
				if controller.Identity.NativeType == cdnProfileType && strings.EqualFold(controller.Identity.NativeID, cdnProfileID(target.Identity.NativeID)) {
					controllers[string(controller.ID)] = true
				}
				if controller.Identity.NativeType == cdnEndpointType && strings.HasPrefix(target.Identity.NativeID, controller.Identity.NativeID+"/") && strings.HasPrefix(child.id, controller.Identity.NativeID+"/") {
					controllers[string(controller.ID)] = true
				}
			}
			evidence[graph.RelationshipEvidenceDeletionCascadeControllers] = controllers
			var referrer *asset.Asset
			for i := range assets {
				candidate := &assets[i]
				if candidate.Identity.Provider == target.Identity.Provider && candidate.Identity.ConnectionID == target.Identity.ConnectionID && candidate.Identity.Partition == target.Identity.Partition && strings.EqualFold(candidate.Identity.NativeType, child.kind) && strings.EqualFold(candidate.Identity.NativeID, child.id) {
					if referrer != nil {
						return fmt.Errorf("ambiguous Azure CDN reference")
					}
					referrer = candidate
				}
			}
			if referrer == nil {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: target.Identity.Provider, ConnectionID: target.Identity.ConnectionID, NativeType: child.kind, NativeID: child.id, ControllerID: target.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
				continue
			}
			if err := serviceIncarnation(*referrer, child.data); err != nil {
				return err
			}
			if err := s.client.servicePrivateIncarnation(*referrer, child.data); err != nil {
				return err
			}
			if !cdnPrerequisite(target, *referrer) {
				return serviceDenied("cdn_references_changed")
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: referrer.ID, Type: graph.RelationshipDependsOn, Source: "azure:cdn-reference", Evidence: evidence, Confidence: 1})
		}
	}
	return nil
}
