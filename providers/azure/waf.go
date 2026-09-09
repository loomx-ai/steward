package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const (
	cdnWAFType             = "Microsoft.Cdn/cdnWebApplicationFirewallPolicies"
	frontDoorWAFType       = "Microsoft.Network/frontDoorWebApplicationFirewallPolicies"
	classicFrontendType    = "Microsoft.Network/frontDoors/frontendEndpoints"
	classicRoutingRuleType = "Microsoft.Network/frontDoors/routingRules"
)

func isWAFType(kind string) bool {
	return strings.EqualFold(kind, cdnWAFType) || strings.EqualFold(kind, frontDoorWAFType)
}

func wafLinkFields(kind string) map[string]string {
	if strings.EqualFold(kind, cdnWAFType) {
		return map[string]string{"endpointLinks": cdnEndpointType}
	}
	return map[string]string{"frontendEndpointLinks": classicFrontendType, "routingRuleLinks": classicRoutingRuleType, "securityPolicyLinks": afdSecurityPolicyType}
}

// Native GET supplies the reverse association indexes. routingRuleLinks is
// optional even in Microsoft's latest recordings; the other indexes must be
// present before this response can establish an unassociated policy.
func wafLinks(kind string, raw map[string]any) ([]string, error) {
	refs := []string{}
	seen := map[string]bool{}
	for field, expectedType := range wafLinkFields(kind) {
		value, present := object(raw["properties"])[field]
		if !present && field == "routingRuleLinks" {
			continue
		}
		members, ok := value.([]any)
		if !ok {
			return nil, serviceDenied("incomplete_waf_associations")
		}
		for _, member := range members {
			id, nativeType, err := parseID(text(object(member)["id"]))
			if err != nil || !strings.EqualFold(nativeType, expectedType) || seen[id] {
				return nil, serviceDenied("invalid_waf_association")
			}
			seen[id] = true
			refs = append(refs, id)
		}
	}
	slices.Sort(refs)
	return refs, nil
}

// Unlinking changes reverse indexes and ETag. Preserve all policy configuration,
// including private match values, while allowing reviewed association removal.
func wafSnapshot(kind string, raw map[string]any) map[string]any {
	snapshot := cdnSnapshot(kind, raw)
	if value, present := raw["location"]; present {
		snapshot["location"] = value
	}
	for field := range wafLinkFields(kind) {
		delete(object(snapshot["properties"]), field)
	}
	return snapshot
}

func wafConfiguration(kind string, raw map[string]any) string {
	return serviceParentConfiguration(kind, wafSnapshot(kind, raw))
}

func wafIncarnation(planned asset.Asset, live map[string]any) error {
	if !isWAFType(planned.Identity.NativeType) {
		return nil
	}
	if _, err := wafLinks(planned.Identity.NativeType, live); err != nil {
		return err
	}
	if expected := text(planned.Normalized["_waf_configuration"]); expected == "" || expected != wafConfiguration(planned.Identity.NativeType, live) {
		return serviceDenied("waf_configuration_changed")
	}
	return nil
}

func wafPolicyReference(kind string, raw map[string]any) (string, error) {
	var value any
	expectedType := ""
	switch kind {
	case cdnEndpointType:
		value, expectedType = object(raw["properties"])["webApplicationFirewallPolicyLink"], cdnWAFType
	case afdSecurityPolicyType:
		value, expectedType = object(object(raw["properties"])["parameters"])["wafPolicy"], frontDoorWAFType
	default:
		return "", nil
	}
	if value == nil {
		return "", nil
	}
	id, nativeType, err := parseID(text(object(value)["id"]))
	if err != nil || !strings.EqualFold(nativeType, expectedType) {
		return "", serviceDenied("invalid_waf_policy_reference")
	}
	return id, nil
}

func wafPrerequisite(parent, referrer asset.Asset) bool {
	return isWAFType(parent.Identity.NativeType) && slices.Contains(stringValues(parent.Normalized["_waf_links"]), strings.ToLower(referrer.Identity.NativeID)) &&
		strings.EqualFold(text(referrer.Normalized["_waf_policy"]), parent.Identity.NativeID)
}

func (a *action) wafPreflight(planned asset.Asset, live map[string]any) error {
	if !isWAFType(planned.Identity.NativeType) {
		return nil
	}
	if err := wafIncarnation(planned, live); err != nil {
		return err
	}
	links, err := wafLinks(planned.Identity.NativeType, live)
	if err != nil {
		return err
	}
	if len(links) != 0 {
		return serviceDenied("waf_associations_require_prior_removal")
	}
	return nil
}

// Reverse links establish prerequisites, never ownership of serving endpoints.
// Classic Front Door endpoint/routing configurations have no standalone DELETE;
// keep them unresolved until their own controller lifecycle is supported.
func (s *serviceCascades) contributeWAFReferences(ctx context.Context, assets []asset.Asset, result *governance.Contribution) error {
	for _, parent := range assets {
		if parent.Identity.Provider != asset.ProviderAzure || !isWAFType(parent.Identity.NativeType) {
			continue
		}
		kind, _ := findType(parent.Identity.NativeType)
		endpoint, err := s.client.resourceURL(kind, parent.Identity.NativeID)
		if err != nil {
			return err
		}
		live, err := s.client.request(ctx, "GET", endpoint)
		if err != nil {
			return err
		}
		if !validResourceResponse(live, parent.Identity.NativeID, parent.Identity.NativeType) {
			return serviceDenied("invalid_waf_policy_identity")
		}
		if err := wafIncarnation(parent, live.data); err != nil {
			return err
		}
		if err := s.client.servicePrivateIncarnation(parent, live.data); err != nil {
			return err
		}
		links, _ := wafLinks(parent.Identity.NativeType, live.data)
		if !slices.Equal(links, stringValues(parent.Normalized["_waf_links"])) {
			return serviceDenied("waf_associations_changed")
		}
		for _, id := range links {
			_, parsedType, _ := parseID(id)
			childKind, known := findType(parsedType)
			if known {
				parsedType = childKind.NativeType
			}
			evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "resource_type": parsedType, "instance_id": id}
			var target *asset.Asset
			for i := range assets {
				candidate := &assets[i]
				if candidate.Identity.Provider == parent.Identity.Provider && candidate.Identity.ConnectionID == parent.Identity.ConnectionID && candidate.Identity.Partition == parent.Identity.Partition && strings.EqualFold(candidate.Identity.NativeID, id) && strings.EqualFold(candidate.Identity.NativeType, parsedType) {
					if target != nil {
						return serviceDenied("ambiguous_waf_association")
					}
					target = candidate
				}
			}
			if !known || childKind.ReadOnly || target == nil {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: parsedType, NativeID: id, ControllerID: parent.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
				continue
			}
			childURL, err := s.client.resourceURL(childKind, id)
			if err != nil {
				return err
			}
			child, err := s.client.request(ctx, "GET", childURL)
			if err != nil {
				return err
			}
			if !validResourceResponse(child, id, childKind.NativeType) {
				return serviceDenied("invalid_waf_referrer_identity")
			}
			policyID, err := wafPolicyReference(childKind.NativeType, child.data)
			if err != nil || !strings.EqualFold(policyID, parent.Identity.NativeID) || !wafPrerequisite(parent, *target) {
				return serviceDenied("waf_association_disagrees")
			}
			if err := serviceIncarnation(*target, child.data); err != nil {
				return err
			}
			if err := s.client.servicePrivateIncarnation(*target, child.data); err != nil {
				return err
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: parent.ID, TargetAssetID: target.ID, Type: graph.RelationshipDependsOn, Source: "azure:waf-association", Evidence: evidence, Confidence: 1})
		}
		latest, err := s.client.request(ctx, "GET", endpoint)
		if err != nil {
			return err
		}
		if !validResourceResponse(latest, parent.Identity.NativeID, parent.Identity.NativeType) {
			return serviceDenied("invalid_waf_policy_identity")
		}
		latestLinks, err := wafLinks(parent.Identity.NativeType, latest.data)
		if err != nil || !slices.Equal(links, latestLinks) || s.client.privateConfiguration(wafSnapshot(parent.Identity.NativeType, live.data)) != s.client.privateConfiguration(wafSnapshot(parent.Identity.NativeType, latest.data)) {
			return serviceDenied("waf_associations_changed")
		}
	}
	return nil
}
