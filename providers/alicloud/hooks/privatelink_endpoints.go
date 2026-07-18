package hooks

import (
	"context"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/providers/alicloud"
)

const (
	privateLinkEndpointNativeType        = "ACS::PrivateLink::VpcEndpoint"
	privateLinkEndpointEvidence          = "privatelink:ListVpcEndpointZones"
	privateLinkSystemSecurityGroupPrefix = "privatelink_system_security_group."
	privateLinkSystemSecurityGroupSource = "privatelink:service-managed-security-group"
)

// PrivateLinkEndpoints maps the ENI created for each endpoint zone to the
// endpoint that owns and removes it.
type PrivateLinkEndpoints struct{}

func NewPrivateLinkEndpoints() *PrivateLinkEndpoints {
	return &PrivateLinkEndpoints{}
}

func (*PrivateLinkEndpoints) Contribute(
	_ context.Context,
	_ asset.ScopeID,
	assets []asset.Asset,
) (governance.Contribution, error) {
	ordered := append([]asset.Asset(nil), assets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	result := governance.Contribution{}
	for _, endpoint := range ordered {
		if endpoint.Identity.Provider != asset.ProviderAliCloud ||
			endpoint.Identity.NativeType != privateLinkEndpointNativeType {
			continue
		}
		zonesByENI := privateLinkZonesByENI(endpoint)
		for _, eniID := range normalizedStrings(
			endpoint.Normalized[alicloud.NormalizedPrivateLinkENIIDsField],
		) {
			evidence := map[string]any{
				"source":      privateLinkEndpointEvidence,
				"endpoint_id": endpoint.Identity.NativeID,
				"eni_id":      eniID,
			}
			if zone := zonesByENI[eniID]; zone != nil {
				for _, field := range []string{"zone_id", "zone_status", "service_status", "vswitch_id", "request_id"} {
					if value := strings.TrimSpace(normalizedScalar(zone[field])); value != "" {
						evidence[field] = value
					}
				}
			}
			managed, resolved := resolvePrivateLinkENI(endpoint, eniID, ordered)
			if !resolved {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{
					Provider: endpoint.Identity.Provider, ConnectionID: endpoint.Identity.ConnectionID,
					NativeType: networkInterfaceNativeType, NativeID: eniID,
					ControllerID: endpoint.ID, Relationship: graph.RelationshipConnectedTo,
					Evidence: evidence,
				})
				continue
			}
			evidence["delete_by_default"] = true
			evidence["lifecycle_kind"] = "privatelink_endpoint_eni"
			evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
			evidence[graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents] = true
			result.Relationships = append(result.Relationships, graph.Relationship{
				SourceAssetID: managed.ID, TargetAssetID: endpoint.ID,
				Type: graph.RelationshipConnectedTo, Source: privateLinkEndpointEvidence,
				Evidence: evidence, Confidence: 1,
			})
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{
				ControllerAssetID: endpoint.ID, ManagedAssetID: managed.ID,
				Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive,
				CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: false,
				EvidenceSource: privateLinkEndpointEvidence, Evidence: evidence, Confidence: 1,
			})
		}
		for _, securityGroup := range privateLinkSystemSecurityGroups(endpoint, ordered) {
			evidence := map[string]any{
				"source":            privateLinkSystemSecurityGroupSource,
				"endpoint_id":       endpoint.Identity.NativeID,
				"security_group_id": securityGroup.Identity.NativeID,
				"delete_by_default": true,
				"lifecycle_kind":    "privatelink_system_security_group",
				graph.LifecycleEvidenceControllerDeleteGuaranteed:      true,
				graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents: true,
				graph.LifecycleEvidenceWaitTimeoutSeconds:              30,
				graph.LifecycleEvidenceWaitPollSeconds:                 2,
			}
			if serviceID := strings.TrimSpace(normalizedScalar(
				securityGroup.Normalized[alicloud.NormalizedServiceIDField],
			)); serviceID != "" {
				evidence["service_id"] = serviceID
			}
			result.Relationships = append(result.Relationships, graph.Relationship{
				SourceAssetID: securityGroup.ID, TargetAssetID: endpoint.ID,
				Type: graph.RelationshipConnectedTo, Source: privateLinkSystemSecurityGroupSource,
				Evidence: evidence, Confidence: 1,
			})
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{
				ControllerAssetID: endpoint.ID, ManagedAssetID: securityGroup.ID,
				Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive,
				CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: false,
				EvidenceSource: privateLinkSystemSecurityGroupSource,
				Evidence:       evidence, Confidence: 1,
			})
		}
	}
	return result, nil
}

func privateLinkSystemSecurityGroups(
	endpoint asset.Asset,
	assets []asset.Asset,
) []asset.Asset {
	wantName := privateLinkSystemSecurityGroupPrefix +
		strings.TrimSpace(endpoint.Identity.NativeID)
	result := make([]asset.Asset, 0, 1)
	for _, candidate := range assets {
		if candidate.Identity.Provider != endpoint.Identity.Provider ||
			candidate.Identity.ConnectionID != endpoint.Identity.ConnectionID ||
			candidate.Identity.Partition != endpoint.Identity.Partition ||
			candidate.Identity.NativeType != securityGroupNativeType ||
			!sameLifecycleScope(endpoint, candidate) ||
			!normalizedBool(candidate.Normalized[alicloud.NormalizedServiceManagedField]) {
			continue
		}
		name := strings.TrimSpace(candidate.Name)
		if name == "" {
			name = strings.TrimSpace(normalizedScalar(candidate.Normalized["name"]))
		}
		if name == wantName {
			result = append(result, candidate)
		}
	}
	return result
}

func privateLinkZonesByENI(endpoint asset.Asset) map[string]map[string]any {
	result := make(map[string]map[string]any)
	values, _ := endpoint.Normalized[alicloud.NormalizedPrivateLinkEndpointZonesField].([]any)
	for _, raw := range values {
		zone, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if eniID := strings.TrimSpace(normalizedScalar(zone["eni_id"])); eniID != "" {
			result[eniID] = zone
		}
	}
	return result
}

func resolvePrivateLinkENI(
	endpoint asset.Asset,
	eniID string,
	assets []asset.Asset,
) (asset.Asset, bool) {
	var result asset.Asset
	for _, candidate := range assets {
		if candidate.Identity.Provider != endpoint.Identity.Provider ||
			candidate.Identity.ConnectionID != endpoint.Identity.ConnectionID ||
			candidate.Identity.Partition != endpoint.Identity.Partition ||
			candidate.Identity.NativeType != networkInterfaceNativeType ||
			strings.TrimSpace(candidate.Identity.NativeID) != strings.TrimSpace(eniID) ||
			!sameLifecycleScope(endpoint, candidate) {
			continue
		}
		if result.ID != "" {
			return asset.Asset{}, false
		}
		result = candidate
	}
	return result, result.ID != ""
}
