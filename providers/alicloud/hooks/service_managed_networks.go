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
	nlbNativeType          = "ACS::NLB::LoadBalancer"
	natGatewayNativeType   = "ACS::NAT::NatGateway"
	eciContainerNativeType = "ACS::ECI::ContainerGroup"

	nlbManagedENIEvidence  = "nlb:service-managed-eni"
	natManagedENIEvidence  = "nat:service-managed-eni"
	natManagedSGEvidence   = "nat:service-managed-security-group"
	eciManagedENIEvidence  = "eci:primary-eni"
	eciManagedDiskEvidence = "eci:disk-attachment"
)

type natManagedSecurityGroupCandidate struct {
	controller    asset.Asset
	securityGroup asset.Asset
}

// ServiceManagedNetworks derives lifecycle ownership for provider-created
// network interfaces and ECI disks from exact inventory configuration IDs.
type ServiceManagedNetworks struct{}

func NewServiceManagedNetworks() *ServiceManagedNetworks {
	return &ServiceManagedNetworks{}
}

func (*ServiceManagedNetworks) Contribute(
	_ context.Context,
	_ asset.ScopeID,
	assets []asset.Asset,
) (governance.Contribution, error) {
	ordered := append([]asset.Asset(nil), assets...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].ID < ordered[j].ID })

	result := governance.Contribution{}
	natSecurityGroupCandidates := make(
		map[asset.AssetID]map[asset.AssetID]natManagedSecurityGroupCandidate,
	)
	for _, controller := range ordered {
		var (
			eniIDs         []string
			evidenceSource string
			lifecycleKind  string
		)
		switch controller.Identity.NativeType {
		case nlbNativeType:
			eniIDs = configurationStringsByKey(controller, "EniId")
			evidenceSource = nlbManagedENIEvidence
			lifecycleKind = "nlb_service_managed_eni"
		case natGatewayNativeType:
			eniIDs = configurationStringsByKey(controller, "EniInstanceId")
			evidenceSource = natManagedENIEvidence
			lifecycleKind = "nat_service_managed_eni"
		case eciContainerNativeType:
			eniIDs = configurationStringsByKey(controller, "EniInstanceId")
			evidenceSource = eciManagedENIEvidence
			lifecycleKind = "eci_primary_eni"
		default:
			continue
		}
		for _, eniID := range eniIDs {
			networkInterface, found := resolveScopedAsset(
				controller,
				networkInterfaceNativeType,
				eniID,
				ordered,
			)
			evidence := map[string]any{
				"source": evidenceSource, "controller_id": controller.Identity.NativeID,
				"network_interface_id": eniID,
			}
			if !found {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{
					Provider: controller.Identity.Provider, ConnectionID: controller.Identity.ConnectionID,
					NativeType: networkInterfaceNativeType, NativeID: eniID,
					ControllerID: controller.ID, Relationship: graph.RelationshipConnectedTo,
					Evidence: evidence,
				})
				continue
			}
			if !managedNetworkInterfaceEvidence(controller, networkInterface) {
				continue
			}
			evidence["delete_by_default"] = true
			evidence["lifecycle_kind"] = lifecycleKind
			evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
			evidence[graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents] = true
			evidence[graph.LifecycleEvidenceWaitTimeoutSeconds] = 300
			evidence[graph.LifecycleEvidenceWaitPollSeconds] = 15
			result.Relationships = append(result.Relationships, graph.Relationship{
				SourceAssetID: networkInterface.ID, TargetAssetID: controller.ID,
				Type: graph.RelationshipConnectedTo, Source: evidenceSource,
				Evidence: evidence, Confidence: 1,
			})
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{
				ControllerAssetID: controller.ID, ManagedAssetID: networkInterface.ID,
				Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive,
				CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: false,
				EvidenceSource: evidenceSource, Evidence: evidence, Confidence: 1,
			})
			if controller.Identity.NativeType == natGatewayNativeType {
				collectNATManagedSecurityGroups(
					controller,
					networkInterface,
					ordered,
					natSecurityGroupCandidates,
					&result,
				)
			}
		}
	}
	appendNATManagedSecurityGroupLifecycle(
		natSecurityGroupCandidates,
		&result,
	)

	for _, disk := range ordered {
		if disk.Identity.Provider != asset.ProviderAliCloud ||
			disk.Identity.NativeType != ecsDiskNativeType {
			continue
		}
		controllerID := diskStringValue(
			disk,
			"attached_instance_id",
			"instanceId",
			"InstanceId",
		)
		deleteWithInstance, known := diskBoolValue(
			disk,
			"delete_with_instance",
			"deleteWithInstance",
			"DeleteWithInstance",
		)
		if !known || !deleteWithInstance || !strings.HasPrefix(controllerID, "eci-") {
			continue
		}
		controller, found := resolveScopedAsset(
			disk,
			eciContainerNativeType,
			controllerID,
			ordered,
		)
		if !found {
			continue
		}
		evidence := map[string]any{
			"source": eciManagedDiskEvidence, "controller_id": controllerID,
			"disk_id": disk.Identity.NativeID, "delete_with_instance": true,
			"delete_by_default": true, "lifecycle_kind": "eci_delete_with_instance_disk",
			graph.LifecycleEvidenceControllerDeleteGuaranteed: true,
		}
		result.Relationships = append(result.Relationships, graph.Relationship{
			SourceAssetID: disk.ID, TargetAssetID: controller.ID,
			Type: graph.RelationshipAttachedTo, Source: eciManagedDiskEvidence,
			Evidence: evidence, Confidence: 1,
		})
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{
			ControllerAssetID: controller.ID, ManagedAssetID: disk.ID,
			Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive,
			CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: false,
			EvidenceSource: eciManagedDiskEvidence, Evidence: evidence, Confidence: 1,
		})
	}
	return result, nil
}

func collectNATManagedSecurityGroups(
	controller asset.Asset,
	networkInterface asset.Asset,
	assets []asset.Asset,
	candidates map[asset.AssetID]map[asset.AssetID]natManagedSecurityGroupCandidate,
	result *governance.Contribution,
) {
	for _, securityGroupID := range normalizedStrings(
		networkInterface.Normalized[alicloud.NormalizedSecurityGroupIDsField],
	) {
		evidence := map[string]any{
			"source":               natManagedSGEvidence,
			"controller_id":        controller.Identity.NativeID,
			"network_interface_id": networkInterface.Identity.NativeID,
			"security_group_id":    securityGroupID,
		}
		securityGroup, found := resolveScopedAsset(
			controller,
			securityGroupNativeType,
			securityGroupID,
			assets,
		)
		if !found {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{
				Provider: controller.Identity.Provider, ConnectionID: controller.Identity.ConnectionID,
				NativeType: securityGroupNativeType, NativeID: securityGroupID,
				ControllerID: controller.ID, Relationship: graph.RelationshipConnectedTo,
				Evidence: evidence,
			})
			continue
		}
		if !natManagedSecurityGroupEvidence(networkInterface, securityGroup) {
			continue
		}
		controllers := candidates[securityGroup.ID]
		if controllers == nil {
			controllers = make(
				map[asset.AssetID]natManagedSecurityGroupCandidate,
			)
			candidates[securityGroup.ID] = controllers
		}
		controllers[controller.ID] = natManagedSecurityGroupCandidate{
			controller: controller, securityGroup: securityGroup,
		}
	}
}

func appendNATManagedSecurityGroupLifecycle(
	candidates map[asset.AssetID]map[asset.AssetID]natManagedSecurityGroupCandidate,
	result *governance.Contribution,
) {
	securityGroupIDs := make([]string, 0, len(candidates))
	for securityGroupID := range candidates {
		securityGroupIDs = append(securityGroupIDs, string(securityGroupID))
	}
	sort.Strings(securityGroupIDs)
	for _, securityGroupID := range securityGroupIDs {
		controllers := candidates[asset.AssetID(securityGroupID)]
		// Exclusive lifecycle ownership is authoritative only when the
		// service-managed security group maps to one NAT gateway.
		if len(controllers) != 1 {
			continue
		}
		var candidate natManagedSecurityGroupCandidate
		for _, value := range controllers {
			candidate = value
		}
		evidence := map[string]any{
			"source":            natManagedSGEvidence,
			"controller_id":     candidate.controller.Identity.NativeID,
			"security_group_id": candidate.securityGroup.Identity.NativeID,
			"delete_by_default": true,
			"lifecycle_kind":    "nat_service_managed_security_group",
			graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents: true,
			graph.LifecycleEvidenceWaitTimeoutSeconds:              120,
			graph.LifecycleEvidenceWaitPollSeconds:                 15,
		}
		if serviceID := strings.TrimSpace(normalizedScalar(
			candidate.securityGroup.Normalized[alicloud.NormalizedServiceIDField],
		)); serviceID != "" {
			evidence["service_id"] = serviceID
		}
		result.Relationships = append(result.Relationships, graph.Relationship{
			SourceAssetID: candidate.securityGroup.ID,
			TargetAssetID: candidate.controller.ID,
			Type:          graph.RelationshipConnectedTo,
			Source:        natManagedSGEvidence,
			Evidence:      evidence,
			Confidence:    1,
		})
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{
			ControllerAssetID:    candidate.controller.ID,
			ManagedAssetID:       candidate.securityGroup.ID,
			Authority:            graph.AuthorityAuthoritative,
			Ownership:            graph.OwnershipExclusive,
			CleanupPolicy:        graph.CleanupDelegate,
			DirectCleanupAllowed: false,
			EvidenceSource:       natManagedSGEvidence,
			Evidence:             evidence,
			Confidence:           1,
		})
	}
}

func natManagedSecurityGroupEvidence(
	networkInterface asset.Asset,
	securityGroup asset.Asset,
) bool {
	if !normalizedBool(
		securityGroup.Normalized[alicloud.NormalizedServiceManagedField],
	) {
		return false
	}
	networkServiceID := strings.TrimSpace(normalizedScalar(
		networkInterface.Normalized[alicloud.NormalizedServiceIDField],
	))
	securityGroupServiceID := strings.TrimSpace(normalizedScalar(
		securityGroup.Normalized[alicloud.NormalizedServiceIDField],
	))
	return networkServiceID == "" ||
		securityGroupServiceID == "" ||
		networkServiceID == securityGroupServiceID
}

func managedNetworkInterfaceEvidence(
	controller asset.Asset,
	networkInterface asset.Asset,
) bool {
	switch controller.Identity.NativeType {
	case nlbNativeType, natGatewayNativeType:
		if normalizedBool(
			networkInterface.Normalized[alicloud.NormalizedServiceManagedField],
		) {
			return true
		}
		managed, known := diskBoolValue(
			networkInterface,
			"serviceManaged",
			"ServiceManaged",
		)
		return known && managed
	case eciContainerNativeType:
		instanceID := diskStringValue(networkInterface, "instanceId", "InstanceId")
		deleteOnRelease, known := diskBoolValue(
			networkInterface,
			"deleteOnRelease",
			"DeleteOnRelease",
		)
		return instanceID == controller.Identity.NativeID && known && deleteOnRelease
	default:
		return false
	}
}

func resolveScopedAsset(
	source asset.Asset,
	nativeType string,
	nativeID string,
	assets []asset.Asset,
) (asset.Asset, bool) {
	var result asset.Asset
	for _, candidate := range assets {
		if candidate.Identity.Provider != source.Identity.Provider ||
			candidate.Identity.ConnectionID != source.Identity.ConnectionID ||
			candidate.Identity.Partition != source.Identity.Partition ||
			candidate.Identity.NativeType != nativeType ||
			strings.TrimSpace(candidate.Identity.NativeID) != strings.TrimSpace(nativeID) ||
			!sameLifecycleScope(source, candidate) {
			continue
		}
		if result.ID != "" {
			return asset.Asset{}, false
		}
		result = candidate
	}
	return result, result.ID != ""
}

func configurationStringsByKey(value asset.Asset, key string) []string {
	configuration, _ := value.Normalized["configuration"].(map[string]any)
	seen := make(map[string]struct{})
	canonicalKey := canonicalHookKey(key)
	collectConfigurationStrings(configuration, canonicalKey, seen)
	// Product API inventory already projects reviewed fields onto the
	// normalized object, while Resource Center keeps the provider field names
	// in configuration. Read both representations so cross-scope topology rules
	// behave consistently for either inventory source.
	for candidateKey, child := range value.Normalized {
		if canonicalHookKey(candidateKey) != canonicalKey {
			continue
		}
		for _, item := range normalizedStrings(child) {
			seen[item] = struct{}{}
		}
	}
	result := make([]string, 0, len(seen))
	for item := range seen {
		result = append(result, item)
	}
	sort.Strings(result)
	return result
}

func collectConfigurationStrings(
	value any,
	key string,
	result map[string]struct{},
) {
	switch typed := value.(type) {
	case map[string]any:
		for candidateKey, child := range typed {
			if canonicalHookKey(candidateKey) == key {
				for _, item := range normalizedStrings(child) {
					result[item] = struct{}{}
				}
			}
			collectConfigurationStrings(child, key, result)
		}
	case []any:
		for _, child := range typed {
			collectConfigurationStrings(child, key, result)
		}
	}
}

func canonicalHookKey(value string) string {
	var result strings.Builder
	for _, character := range strings.TrimSpace(value) {
		if character >= 'A' && character <= 'Z' {
			result.WriteByte(byte(character - 'A' + 'a'))
		} else if character >= 'a' && character <= 'z' ||
			character >= '0' && character <= '9' {
			result.WriteRune(character)
		}
	}
	return result.String()
}
