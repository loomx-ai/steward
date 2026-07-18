package hooks

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/providers/alicloud"
)

const (
	cenVPCAttachmentEvidence = "cen:ListTransitRouterVpcAttachments"
	cenVPCAttachmentKind     = "cen_transit_router_vpc_attachment"
	cenTopologyEvidence      = "cen:topology-apis"
	vbrNativeType            = "ACS::ExpressConnect::VirtualBorderRouter"
	vpnConnectionNativeType  = "ACS::VPN::VpnConnection"
	ecrNativeType            = "ACS::ExpressConnectRouter::ExpressConnectRouter"
	prefixListNativeType     = "ACS::ECS::PrefixList"
)

// CENTopology constructs the CEN topology that Resource Center does not expose
// and derives lifecycle ownership for service-managed ENIs from VPC attachment
// zone mappings.
type CENTopology struct{}

// CENVPCAttachments is retained as a source-compatible name for the original,
// narrower contributor.
type CENVPCAttachments = CENTopology

func NewCENTopology() *CENTopology {
	return &CENTopology{}
}

func NewCENVPCAttachments() *CENVPCAttachments {
	return NewCENTopology()
}

func (*CENTopology) Contribute(
	_ context.Context,
	_ asset.ScopeID,
	assets []asset.Asset,
) (governance.Contribution, error) {
	ordered := append([]asset.Asset(nil), assets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	networkInterfaces := make(map[string][]asset.Asset)
	resourcesByIdentity := make(map[string][]asset.Asset)
	for _, value := range ordered {
		if value.Identity.Provider != asset.ProviderAliCloud {
			continue
		}
		key := cenResourceKey(value, value.Identity.NativeType, value.Identity.NativeID)
		resourcesByIdentity[key] = append(resourcesByIdentity[key], value)
		if value.Identity.NativeType == networkInterfaceNativeType {
			networkKey := cenManagedResourceKey(value, value.Identity.NativeID)
			networkInterfaces[networkKey] = append(networkInterfaces[networkKey], value)
		}
	}

	result := governance.Contribution{}
	seen := make(map[string]struct{})
	for _, resource := range ordered {
		if resource.Identity.Provider != asset.ProviderAliCloud ||
			!isCENTopologyNativeType(resource.Identity.NativeType) {
			continue
		}
		if parentNativeType, parentNativeID := cenImmediateParent(resource); parentNativeID != "" {
			contributeCENParentRelationship(
				&result,
				resource,
				parentNativeType,
				parentNativeID,
				resourcesByIdentity,
			)
		}
		contributeCENManagedLifecycle(&result, resource, resourcesByIdentity)
		if err := contributeCENTopologyRelationships(&result, resource, resourcesByIdentity); err != nil {
			return governance.Contribution{}, err
		}
		if resource.Identity.NativeType != alicloud.CENTransitRouterVPCAttachmentNativeType {
			continue
		}
		attachment := resource
		zoneMappings, err := cenZoneMappings(attachment)
		if err != nil {
			return governance.Contribution{}, fmt.Errorf(
				"CEN VPC attachment %q topology evidence: %w",
				attachment.Identity.NativeID,
				err,
			)
		}
		for _, zoneMapping := range zoneMappings {
			networkInterfaceID := strings.TrimSpace(normalizedScalar(zoneMapping["NetworkInterfaceId"]))
			if networkInterfaceID == "" {
				continue
			}
			key := string(attachment.ID) + "\x00" + networkInterfaceID
			if _, duplicate := seen[key]; duplicate {
				continue
			}
			seen[key] = struct{}{}

			evidence := cenVPCAttachmentEvidenceMap(attachment, zoneMapping, networkInterfaceID)
			candidates := networkInterfaces[cenManagedResourceKey(attachment, networkInterfaceID)]
			managed, resolved := resolveCENManagedNetworkInterface(attachment, zoneMapping, candidates)
			if !resolved {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{
					Provider: attachment.Identity.Provider, ConnectionID: attachment.Identity.ConnectionID,
					NativeType: networkInterfaceNativeType, NativeID: networkInterfaceID,
					ControllerID: attachment.ID, Relationship: graph.RelationshipMemberOf,
					Evidence: evidence,
				})
				continue
			}
			result.Relationships = append(result.Relationships, graph.Relationship{
				SourceAssetID: managed.ID, TargetAssetID: attachment.ID,
				Type: graph.RelationshipMemberOf, Source: cenVPCAttachmentEvidence,
				Evidence: evidence, Confidence: 1,
			})
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{
				ControllerAssetID: attachment.ID, ManagedAssetID: managed.ID,
				Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive,
				CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: false,
				EvidenceSource: cenVPCAttachmentEvidence, Evidence: evidence, Confidence: 1,
			})
		}
	}
	return result, nil
}

func contributeCENManagedLifecycle(
	result *governance.Contribution,
	resource asset.Asset,
	resourcesByIdentity map[string][]asset.Asset,
) {
	var controller asset.Asset
	var lifecycleKind, evidenceSource string
	evidence := map[string]any{}
	switch resource.Identity.NativeType {
	case alicloud.CENTransitRouterRouteTableNativeType:
		if !strings.EqualFold(
			normalizedScalar(resource.Normalized["routeTableType"]),
			"System",
		) {
			return
		}
		candidates := resourcesByIdentity[cenResourceKey(
			resource,
			alicloud.CENTransitRouterNativeType,
			normalizedScalar(resource.Normalized[alicloud.NormalizedCENTransitRouterIDField]),
		)]
		if len(candidates) != 1 {
			return
		}
		controller = candidates[0]
		lifecycleKind = "cen_transit_router_system_route_table"
		evidenceSource = "cen:system-route-table"
		evidence["route_table_type"] = "System"
		evidence["route_table_id"] = resource.Identity.NativeID
	case alicloud.CENRouteMapNativeType:
		if normalizedScalar(resource.Normalized["priority"]) != "5000" {
			return
		}
		candidates := resourcesByIdentity[cenResourceKey(
			resource,
			alicloud.CENTransitRouterNativeType,
			normalizedScalar(resource.Normalized[alicloud.NormalizedCENTransitRouterIDField]),
		)]
		if len(candidates) != 1 {
			return
		}
		controller = candidates[0]
		lifecycleKind = "cen_system_route_map"
		evidenceSource = "cen:system-route-map"
		evidence["priority"] = "5000"
		evidence["route_map_id"] = resource.Identity.NativeID
		evidence["route_table_id"] = normalizedScalar(
			resource.Normalized[alicloud.NormalizedCENTransitRouterRouteTableIDField],
		)
		evidence[graph.LifecycleEvidenceUnselectedControllerAction] =
			graph.LifecycleUnselectedControllerSkip
	default:
		return
	}
	evidence["source"] = evidenceSource
	evidence["lifecycle_kind"] = lifecycleKind
	evidence["controller_id"] = controller.Identity.NativeID
	evidence["delete_by_default"] = true
	evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
	if lifecycleKind == "cen_transit_router_system_route_table" {
		evidence[graph.LifecycleEvidenceControllerIntegratedResource] = true
	}
	result.Bindings = append(result.Bindings, graph.LifecycleBinding{
		ControllerAssetID:    controller.ID,
		ManagedAssetID:       resource.ID,
		Authority:            graph.AuthorityAuthoritative,
		Ownership:            graph.OwnershipExclusive,
		CleanupPolicy:        graph.CleanupDelegate,
		DirectCleanupAllowed: false,
		EvidenceSource:       evidenceSource,
		Evidence:             evidence,
		Confidence:           1,
	})
}

func isCENTopologyNativeType(nativeType string) bool {
	switch nativeType {
	case alicloud.CENBandwidthPackageNativeType,
		alicloud.CENTransitRouterNativeType,
		alicloud.CENTransitRouterVPCAttachmentNativeType,
		alicloud.CENTransitRouterVBRAttachmentNativeType,
		alicloud.CENTransitRouterVPNAttachmentNativeType,
		alicloud.CENTransitRouterECRAttachmentNativeType,
		alicloud.CENTransitRouterPeerAttachmentNativeType,
		alicloud.CENTransitRouterRouteTableNativeType,
		alicloud.CENTransitRouterCidrNativeType,
		alicloud.CENFlowLogNativeType,
		alicloud.CENRouteMapNativeType,
		alicloud.CENChildInstanceAttachmentNativeType,
		alicloud.CENTransitRouterMulticastDomainNativeType,
		alicloud.CENTrafficMarkingPolicyNativeType,
		alicloud.CENInterRegionTrafficQosPolicyNativeType:
		return true
	default:
		return false
	}
}

func contributeCENTopologyRelationships(
	result *governance.Contribution,
	resource asset.Asset,
	resourcesByIdentity map[string][]asset.Asset,
) error {
	switch resource.Identity.NativeType {
	case alicloud.CENBandwidthPackageNativeType:
		for _, cenID := range cenBandwidthPackageCENIDs(resource) {
			contributeCENReferenceRelationship(
				result, resource, alicloud.CENInstanceNativeType, cenID,
				graph.RelationshipAttachedTo, resourcesByIdentity, nil,
			)
		}
	case alicloud.CENTransitRouterVPCAttachmentNativeType:
		contributeCENReferenceRelationship(
			result, resource, vpcNativeType,
			normalizedScalar(resource.Normalized[alicloud.NormalizedCENVPCIDField]),
			graph.RelationshipAttachedTo, resourcesByIdentity, nil,
		)
	case alicloud.CENTransitRouterVBRAttachmentNativeType:
		contributeCENReferenceRelationship(
			result, resource, vbrNativeType,
			normalizedScalar(resource.Normalized[alicloud.NormalizedCENVBRIDField]),
			graph.RelationshipAttachedTo, resourcesByIdentity, nil,
		)
	case alicloud.CENTransitRouterVPNAttachmentNativeType:
		contributeCENReferenceRelationship(
			result, resource, vpnConnectionNativeType,
			normalizedScalar(resource.Normalized[alicloud.NormalizedCENVPNIDField]),
			graph.RelationshipAttachedTo, resourcesByIdentity, nil,
		)
	case alicloud.CENTransitRouterECRAttachmentNativeType:
		contributeCENReferenceRelationship(
			result, resource, ecrNativeType,
			normalizedScalar(resource.Normalized[alicloud.NormalizedCENECRIDField]),
			graph.RelationshipAttachedTo, resourcesByIdentity, nil,
		)
	case alicloud.CENTransitRouterPeerAttachmentNativeType:
		contributeCENReferenceRelationship(
			result, resource, alicloud.CENTransitRouterNativeType,
			normalizedScalar(resource.Normalized[alicloud.NormalizedCENPeerTransitRouterIDField]),
			graph.RelationshipAttachedTo, resourcesByIdentity,
			map[string]any{"peer": true},
		)
		contributeCENReferenceRelationship(
			result, resource, alicloud.CENBandwidthPackageNativeType,
			normalizedScalar(resource.Normalized[alicloud.NormalizedCENBandwidthPackageIDField]),
			graph.RelationshipUses, resourcesByIdentity, nil,
		)
	case alicloud.CENTransitRouterRouteTableNativeType:
		return contributeCENRouteTableRelationships(result, resource, resourcesByIdentity)
	case alicloud.CENFlowLogNativeType:
		contributeCENReferenceRelationship(
			result, resource, cenAttachmentNativeType,
			normalizedScalar(resource.Normalized[alicloud.NormalizedCENTransitRouterAttachmentIDField]),
			graph.RelationshipAttachedTo, resourcesByIdentity, nil,
		)
	case alicloud.CENRouteMapNativeType:
		for _, field := range []string{
			"sourceRouteTableIds",
			"destinationRouteTableIds",
			"originalRouteTableIds",
		} {
			for _, routeTableID := range normalizedScalars(resource.Normalized[field]) {
				contributeCENReferenceRelationship(
					result, resource, alicloud.CENTransitRouterRouteTableNativeType,
					routeTableID, graph.RelationshipUses, resourcesByIdentity,
					map[string]any{"route_map_field": field},
				)
			}
		}
		for _, field := range []string{"sourceInstanceIds", "destinationInstanceIds"} {
			for _, instanceID := range normalizedScalars(resource.Normalized[field]) {
				contributeCENAnyNetworkResourceRelationship(
					result, resource, instanceID, resourcesByIdentity,
					map[string]any{"route_map_field": field},
				)
			}
		}
	case alicloud.CENChildInstanceAttachmentNativeType:
		childType := normalizedScalar(resource.Normalized["childInstanceType"])
		childID := normalizedScalar(resource.Normalized["childInstanceId"])
		contributeCENReferenceRelationship(
			result, resource, cenChildInstanceNativeType(childType), childID,
			graph.RelationshipAttachedTo, resourcesByIdentity,
			map[string]any{"child_instance_type": childType},
		)
	case alicloud.CENTransitRouterMulticastDomainNativeType:
		associations, err := normalizedObjects(resource.Normalized["associations"])
		if err != nil {
			return fmt.Errorf("CEN multicast domain %q associations: %w", resource.Identity.NativeID, err)
		}
		for _, association := range associations {
			contributeCENReferenceRelationship(
				result, resource, cenAttachmentNativeType,
				firstNormalizedScalar(association, "TransitRouterAttachmentId"),
				graph.RelationshipAttachedTo, resourcesByIdentity,
				map[string]any{"multicast_association": association},
			)
			contributeCENReferenceRelationship(
				result, resource, vSwitchNativeType,
				firstNormalizedScalar(association, "VSwitchId"),
				graph.RelationshipUses, resourcesByIdentity,
				map[string]any{"multicast_association": association},
			)
		}
	case alicloud.CENInterRegionTrafficQosPolicyNativeType:
		contributeCENReferenceRelationship(
			result, resource, alicloud.CENTransitRouterPeerAttachmentNativeType,
			normalizedScalar(resource.Normalized[alicloud.NormalizedCENTransitRouterAttachmentIDField]),
			graph.RelationshipUses, resourcesByIdentity, nil,
		)
	}
	return nil
}

func cenImmediateParent(resource asset.Asset) (string, string) {
	scalar := func(field string) string {
		return normalizedScalar(resource.Normalized[field])
	}
	switch resource.Identity.NativeType {
	case alicloud.CENBandwidthPackageNativeType:
		return "", ""
	case alicloud.CENTransitRouterNativeType,
		alicloud.CENChildInstanceAttachmentNativeType:
		return alicloud.CENInstanceNativeType, scalar(alicloud.NormalizedCENInstanceIDField)
	case alicloud.CENRouteMapNativeType:
		if routeTableID := scalar(alicloud.NormalizedCENTransitRouterRouteTableIDField); routeTableID != "" {
			return alicloud.CENTransitRouterRouteTableNativeType, routeTableID
		}
	case alicloud.CENFlowLogNativeType:
		if attachmentID := scalar(alicloud.NormalizedCENTransitRouterAttachmentIDField); attachmentID != "" {
			return cenAttachmentNativeType, attachmentID
		}
	case alicloud.CENInterRegionTrafficQosPolicyNativeType:
		if attachmentID := scalar(alicloud.NormalizedCENTransitRouterAttachmentIDField); attachmentID != "" {
			return alicloud.CENTransitRouterPeerAttachmentNativeType, attachmentID
		}
	}
	if transitRouterID := scalar(alicloud.NormalizedCENTransitRouterIDField); transitRouterID != "" {
		return alicloud.CENTransitRouterNativeType, transitRouterID
	}
	return alicloud.CENInstanceNativeType, scalar(alicloud.NormalizedCENInstanceIDField)
}

func cenBandwidthPackageCENIDs(resource asset.Asset) []string {
	if values := normalizedScalars(resource.Normalized["cenIds"]); len(values) > 0 {
		return values
	}
	configuration, ok := resource.Normalized["configuration"].(map[string]any)
	if !ok {
		return nil
	}
	for key, value := range configuration {
		if strings.EqualFold(strings.TrimSpace(key), "CenIds") {
			return normalizedScalars(value)
		}
	}
	return nil
}

const cenAttachmentNativeType = "ACS::CEN::TransitRouterAttachment"

func contributeCENRouteTableRelationships(
	result *governance.Contribution,
	routeTable asset.Asset,
	resourcesByIdentity map[string][]asset.Asset,
) error {
	associations, err := normalizedObjects(
		routeTable.Normalized[alicloud.NormalizedCENRouteTableAssociationsField],
	)
	if err != nil {
		return fmt.Errorf("CEN route table %q associations: %w", routeTable.Identity.NativeID, err)
	}
	for _, association := range associations {
		contributeCENReverseRelationship(
			result, routeTable, cenAttachmentNativeType,
			firstNormalizedScalar(association, "TransitRouterAttachmentId"),
			graph.RelationshipAttachedTo, resourcesByIdentity,
			map[string]any{"association": association},
		)
	}

	propagations, err := normalizedObjects(
		routeTable.Normalized[alicloud.NormalizedCENRouteTablePropagationsField],
	)
	if err != nil {
		return fmt.Errorf("CEN route table %q propagations: %w", routeTable.Identity.NativeID, err)
	}
	for _, propagation := range propagations {
		contributeCENReverseRelationship(
			result, routeTable, cenAttachmentNativeType,
			firstNormalizedScalar(propagation, "TransitRouterAttachmentId"),
			graph.RelationshipRoutesTo, resourcesByIdentity,
			map[string]any{"propagation": propagation},
		)
	}

	routeEntries, err := normalizedObjects(
		routeTable.Normalized[alicloud.NormalizedCENRouteEntriesField],
	)
	if err != nil {
		return fmt.Errorf("CEN route table %q entries: %w", routeTable.Identity.NativeID, err)
	}
	for _, routeEntry := range routeEntries {
		nextHopID := firstNormalizedScalar(
			routeEntry,
			"TransitRouterRouteEntryNextHopId",
			"NextHopId",
		)
		contributeCENReferenceRelationship(
			result, routeTable, cenAttachmentNativeType, nextHopID,
			graph.RelationshipRoutesTo, resourcesByIdentity,
			map[string]any{
				"destination_cidr": firstNormalizedScalar(
					routeEntry,
					"TransitRouterRouteEntryDestinationCidrBlock",
					"DestinationCidrBlock",
				),
				"next_hop_type": firstNormalizedScalar(
					routeEntry,
					"TransitRouterRouteEntryNextHopType",
					"NextHopType",
				),
				"route_entry_type": firstNormalizedScalar(
					routeEntry,
					"TransitRouterRouteEntryType",
					"Type",
				),
			},
		)
	}

	prefixAssociations, err := normalizedObjects(
		routeTable.Normalized[alicloud.NormalizedCENPrefixListAssociationsField],
	)
	if err != nil {
		return fmt.Errorf("CEN route table %q prefix lists: %w", routeTable.Identity.NativeID, err)
	}
	for _, prefixAssociation := range prefixAssociations {
		contributeCENReferenceRelationship(
			result, routeTable, prefixListNativeType,
			firstNormalizedScalar(prefixAssociation, "PrefixListId"),
			graph.RelationshipUses, resourcesByIdentity,
			map[string]any{"prefix_list_association": prefixAssociation},
		)
		contributeCENReferenceRelationship(
			result, routeTable, cenAttachmentNativeType,
			firstNormalizedScalar(prefixAssociation, "NextHop"),
			graph.RelationshipRoutesTo, resourcesByIdentity,
			map[string]any{"prefix_list_association": prefixAssociation},
		)
	}
	return nil
}

func contributeCENParentRelationship(
	result *governance.Contribution,
	resource asset.Asset,
	parentNativeType string,
	parentNativeID string,
	resourcesByIdentity map[string][]asset.Asset,
) {
	contributeCENReferenceRelationship(
		result,
		resource,
		parentNativeType,
		parentNativeID,
		graph.RelationshipMemberOf,
		resourcesByIdentity,
		nil,
	)
}

func contributeCENReferenceRelationship(
	result *governance.Contribution,
	source asset.Asset,
	targetNativeType string,
	targetNativeID string,
	relationship graph.RelationshipType,
	resourcesByIdentity map[string][]asset.Asset,
	extraEvidence map[string]any,
) {
	targetNativeType = strings.TrimSpace(targetNativeType)
	targetNativeID = strings.TrimSpace(targetNativeID)
	if targetNativeType == "" || targetNativeID == "" {
		return
	}
	evidence := cenTopologyBaseEvidence(source)
	for key, value := range extraEvidence {
		evidence[key] = value
	}
	candidates := resourcesByIdentity[cenResourceKey(source, targetNativeType, targetNativeID)]
	if targetNativeType == cenAttachmentNativeType {
		candidates = cenAttachmentCandidates(source, targetNativeID, resourcesByIdentity)
	}
	if len(candidates) != 1 {
		result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{
			Provider: source.Identity.Provider, ConnectionID: source.Identity.ConnectionID,
			NativeType: targetNativeType, NativeID: targetNativeID,
			ControllerID: source.ID, Relationship: relationship,
			Evidence: evidence,
		})
		return
	}
	result.Relationships = append(result.Relationships, graph.Relationship{
		SourceAssetID: source.ID, TargetAssetID: candidates[0].ID,
		Type: relationship, Source: cenEvidenceSource(source.Identity.NativeType),
		Evidence: evidence, Confidence: 1,
	})
}

func contributeCENReverseRelationship(
	result *governance.Contribution,
	target asset.Asset,
	sourceNativeType string,
	sourceNativeID string,
	relationship graph.RelationshipType,
	resourcesByIdentity map[string][]asset.Asset,
	extraEvidence map[string]any,
) {
	sourceNativeID = strings.TrimSpace(sourceNativeID)
	if sourceNativeID == "" {
		return
	}
	candidates := resourcesByIdentity[cenResourceKey(target, sourceNativeType, sourceNativeID)]
	if sourceNativeType == cenAttachmentNativeType {
		candidates = cenAttachmentCandidates(target, sourceNativeID, resourcesByIdentity)
	}
	evidence := cenTopologyBaseEvidence(target)
	for key, value := range extraEvidence {
		evidence[key] = value
	}
	if len(candidates) != 1 {
		result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{
			Provider: target.Identity.Provider, ConnectionID: target.Identity.ConnectionID,
			NativeType: sourceNativeType, NativeID: sourceNativeID,
			ControllerID: target.ID, Relationship: relationship,
			Evidence: evidence,
		})
		return
	}
	result.Relationships = append(result.Relationships, graph.Relationship{
		SourceAssetID: candidates[0].ID, TargetAssetID: target.ID,
		Type: relationship, Source: cenEvidenceSource(target.Identity.NativeType),
		Evidence: evidence, Confidence: 1,
	})
}

func cenAttachmentCandidates(
	resource asset.Asset,
	nativeID string,
	resourcesByIdentity map[string][]asset.Asset,
) []asset.Asset {
	result := []asset.Asset{}
	for _, nativeType := range []string{
		alicloud.CENTransitRouterVPCAttachmentNativeType,
		alicloud.CENTransitRouterVBRAttachmentNativeType,
		alicloud.CENTransitRouterVPNAttachmentNativeType,
		alicloud.CENTransitRouterECRAttachmentNativeType,
		alicloud.CENTransitRouterPeerAttachmentNativeType,
	} {
		result = append(
			result,
			resourcesByIdentity[cenResourceKey(resource, nativeType, nativeID)]...,
		)
	}
	return result
}

func contributeCENAnyNetworkResourceRelationship(
	result *governance.Contribution,
	source asset.Asset,
	nativeID string,
	resourcesByIdentity map[string][]asset.Asset,
	extraEvidence map[string]any,
) {
	nativeID = strings.TrimSpace(nativeID)
	if nativeID == "" {
		return
	}
	for _, nativeType := range []string{
		vpcNativeType,
		vbrNativeType,
		vpnConnectionNativeType,
		ecrNativeType,
	} {
		candidates := resourcesByIdentity[cenResourceKey(source, nativeType, nativeID)]
		if len(candidates) != 1 {
			continue
		}
		evidence := cenTopologyBaseEvidence(source)
		for key, value := range extraEvidence {
			evidence[key] = value
		}
		result.Relationships = append(result.Relationships, graph.Relationship{
			SourceAssetID: source.ID, TargetAssetID: candidates[0].ID,
			Type: graph.RelationshipUses, Source: cenEvidenceSource(source.Identity.NativeType),
			Evidence: evidence, Confidence: 1,
		})
		return
	}
}

func cenEvidenceSource(nativeType string) string {
	switch nativeType {
	case alicloud.CENBandwidthPackageNativeType:
		return "cen:DescribeCenBandwidthPackages"
	case alicloud.CENTransitRouterNativeType:
		return "cen:ListTransitRouters"
	case alicloud.CENTransitRouterVPCAttachmentNativeType:
		return "cen:ListTransitRouterVpcAttachments"
	case alicloud.CENTransitRouterVBRAttachmentNativeType:
		return "cen:ListTransitRouterVbrAttachments"
	case alicloud.CENTransitRouterVPNAttachmentNativeType:
		return "cen:ListTransitRouterVpnAttachments"
	case alicloud.CENTransitRouterECRAttachmentNativeType:
		return "cen:ListTransitRouterEcrAttachments"
	case alicloud.CENTransitRouterPeerAttachmentNativeType:
		return "cen:ListTransitRouterPeerAttachments"
	case alicloud.CENTransitRouterRouteTableNativeType:
		return "cen:transit-router-routing-apis"
	default:
		return cenTopologyEvidence
	}
}

func cenTopologyBaseEvidence(resource asset.Asset) map[string]any {
	return map[string]any{
		"source":                       cenEvidenceSource(resource.Identity.NativeType),
		"cen_id":                       normalizedScalar(resource.Normalized[alicloud.NormalizedCENInstanceIDField]),
		"transit_router_id":            normalizedScalar(resource.Normalized[alicloud.NormalizedCENTransitRouterIDField]),
		"transit_router_attachment_id": normalizedScalar(resource.Normalized[alicloud.NormalizedCENTransitRouterAttachmentIDField]),
		"resource_native_type":         resource.Identity.NativeType,
		"resource_native_id":           resource.Identity.NativeID,
	}
}

func cenChildInstanceNativeType(childType string) string {
	switch strings.ToUpper(strings.TrimSpace(childType)) {
	case "VPC":
		return vpcNativeType
	case "VBR":
		return vbrNativeType
	case "VPN":
		return vpnConnectionNativeType
	case "ECR":
		return ecrNativeType
	default:
		return ""
	}
}

func normalizedObjects(value any) ([]map[string]any, error) {
	if value == nil {
		return nil, nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var objects []map[string]any
	if err := json.Unmarshal(payload, &objects); err != nil {
		return nil, fmt.Errorf("value must be an array")
	}
	sort.Slice(objects, func(i, j int) bool {
		left, _ := json.Marshal(objects[i])
		right, _ := json.Marshal(objects[j])
		return string(left) < string(right)
	})
	return objects, nil
}

func normalizedScalars(value any) []string {
	result := []string{}
	switch typed := value.(type) {
	case string:
		if value := strings.TrimSpace(typed); value != "" {
			result = append(result, value)
		}
	case []string:
		for _, item := range typed {
			result = append(result, normalizedScalars(item)...)
		}
	case []any:
		for _, item := range typed {
			result = append(result, normalizedScalars(item)...)
		}
	case map[string]any:
		for _, item := range typed {
			result = append(result, normalizedScalars(item)...)
		}
	case map[string]string:
		for _, item := range typed {
			result = append(result, normalizedScalars(item)...)
		}
	}
	sort.Strings(result)
	return result
}

func firstNormalizedScalar(object map[string]any, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(normalizedScalar(object[key])); value != "" {
			return value
		}
	}
	return ""
}

func cenZoneMappings(attachment asset.Asset) ([]map[string]any, error) {
	value := attachment.Normalized[alicloud.NormalizedCENZoneMappingsField]
	if value == nil {
		return nil, nil
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var mappings []map[string]any
	if err := json.Unmarshal(payload, &mappings); err != nil {
		return nil, fmt.Errorf("ZoneMappings must be an array")
	}
	sort.Slice(mappings, func(i, j int) bool {
		left := normalizedScalar(mappings[i]["NetworkInterfaceId"])
		right := normalizedScalar(mappings[j]["NetworkInterfaceId"])
		return left < right
	})
	return mappings, nil
}

func cenManagedResourceKey(controller asset.Asset, nativeID string) string {
	return strings.Join([]string{
		string(controller.Identity.Partition),
		string(controller.Identity.ConnectionID),
		strings.TrimSpace(nativeID),
	}, "\x00")
}

func cenResourceKey(resource asset.Asset, nativeType, nativeID string) string {
	return strings.Join([]string{
		string(resource.Identity.Partition),
		string(resource.Identity.ConnectionID),
		strings.TrimSpace(nativeType),
		strings.TrimSpace(nativeID),
	}, "\x00")
}

func resolveCENManagedNetworkInterface(
	attachment asset.Asset,
	zoneMapping map[string]any,
	candidates []asset.Asset,
) (asset.Asset, bool) {
	if len(candidates) == 1 {
		return candidates[0], true
	}
	regionID := strings.TrimSpace(normalizedScalar(
		attachment.Normalized[alicloud.NormalizedCENRegionIDField],
	))
	vpcID := strings.TrimSpace(normalizedScalar(
		attachment.Normalized[alicloud.NormalizedCENVPCIDField],
	))
	vSwitchID := strings.TrimSpace(normalizedScalar(zoneMapping["VSwitchId"]))
	matches := make([]asset.Asset, 0, len(candidates))
	for _, candidate := range candidates {
		if regionID != "" && strings.TrimSpace(candidate.Location) != regionID {
			continue
		}
		if vpcID != "" &&
			strings.TrimSpace(normalizedScalar(candidate.Normalized["vpc_id"])) != vpcID {
			continue
		}
		if vSwitchID != "" &&
			strings.TrimSpace(normalizedScalar(candidate.Normalized["vswitch_id"])) != vSwitchID {
			continue
		}
		matches = append(matches, candidate)
	}
	if len(matches) != 1 {
		return asset.Asset{}, false
	}
	return matches[0], true
}

func cenVPCAttachmentEvidenceMap(
	attachment asset.Asset,
	zoneMapping map[string]any,
	networkInterfaceID string,
) map[string]any {
	evidence := cenVPCAttachmentBaseEvidence(attachment)
	evidence["zone_id"] = normalizedScalar(zoneMapping["ZoneId"])
	evidence["vswitch_id"] = normalizedScalar(zoneMapping["VSwitchId"])
	evidence["network_interface_id"] = networkInterfaceID
	evidence["delete_by_default"] = true
	evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
	evidence[graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents] = true
	evidence[graph.LifecycleEvidenceWaitTimeoutSeconds] = 600
	evidence[graph.LifecycleEvidenceWaitPollSeconds] = 15
	return evidence
}

func cenVPCAttachmentBaseEvidence(attachment asset.Asset) map[string]any {
	return map[string]any{
		"source":                       cenVPCAttachmentEvidence,
		"lifecycle_kind":               cenVPCAttachmentKind,
		"cen_id":                       normalizedScalar(attachment.Normalized[alicloud.NormalizedCENInstanceIDField]),
		"transit_router_id":            normalizedScalar(attachment.Normalized[alicloud.NormalizedCENTransitRouterIDField]),
		"transit_router_attachment_id": attachment.Identity.NativeID,
		"vpc_id":                       normalizedScalar(attachment.Normalized[alicloud.NormalizedCENVPCIDField]),
		"vpc_region_id":                normalizedScalar(attachment.Normalized[alicloud.NormalizedCENRegionIDField]),
	}
}
