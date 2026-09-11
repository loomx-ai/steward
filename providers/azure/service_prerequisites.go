package azure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	hostGroupType         = "Microsoft.Compute/hostGroups"
	hostType              = hostGroupType + "/hosts"
	capacityGroupType     = "Microsoft.Compute/capacityReservationGroups"
	capacityType          = capacityGroupType + "/capacityReservations"
	vpnGatewayType        = "Microsoft.Network/vpnGateways"
	vpnConnectionType     = vpnGatewayType + "/vpnConnections"
	vpnNATRuleType        = vpnGatewayType + "/natRules"
	vpnLinkConnectionType = vpnConnectionType + "/vpnLinkConnections"
	expressGatewayType    = "Microsoft.Network/expressRouteGateways"
	expressConnectionType = expressGatewayType + "/expressRouteConnections"
)

// These resources expose independent DELETEs that precede parent deletion.
// https://learn.microsoft.com/azure/virtual-wan/virtual-wan-faq
// https://learn.microsoft.com/troubleshoot/azure/virtual-machines/windows/capacity-reservation-cant-delete-group
var servicePrerequisiteRules = map[string][]string{
	publicDNSZoneType:              {domainType},
	domainType:                     {domainOwnershipType, appBindingType, appSlotBindingType},
	fleetType:                      fleetDirectKinds,
	streamAnalyticsClusterType:     {streamAnalyticsEndpointType, streamAnalyticsJobType},
	kustoType:                      kustoOwnedKinds(kustoType),
	kustoDatabaseType:              append(kustoOwnedKinds(kustoDatabaseType), kustoAttachmentType),
	mongoClusterType:               append(mongoClusterOwnedKinds(), mongoClusterType),
	cognitiveType:                  {cognitiveHostType, cognitivePlanType, cognitiveConnectionType, cognitiveDeploymentType, cognitiveEncryptionType, cognitiveNetworkType, cognitivePECType, cognitiveProjectType, cognitiveBlocklistType, cognitivePolicyType, cognitiveToolType, cognitiveTopicType, cognitiveAssociationType},
	cognitiveProjectType:           {cognitiveApplicationType, cognitiveProjectHostType, cognitiveProjectConnectionType, cognitiveToolType},
	cognitiveApplicationType:       {cognitiveAgentType},
	cognitiveNetworkType:           {cognitiveHostType, cognitiveProjectType},
	cognitiveBlocklistType:         {cognitiveBlockitemType, cognitivePolicyType},
	cognitiveSharedPlanType:        {cognitiveAssociationType},
	cognitiveHostType:              {cognitiveProjectType},
	cognitiveProjectHostType:       {cognitiveApplicationType},
	cognitiveConnectionType:        {cognitiveHostType, cognitiveProjectHostType, cognitiveConnectionType, cognitiveProjectConnectionType},
	cognitiveProjectConnectionType: {cognitiveProjectHostType, cognitiveConnectionType, cognitiveProjectConnectionType},
	cognitiveDeploymentType:        {cognitiveDeploymentType},
	cognitivePolicyType:            {cognitiveDeploymentType},

	searchType:                 {searchConnectionType, searchLinkType},
	redisType:                  {redisPolicyType, redisAssignmentType, redisFirewallType, redisLinkType, redisPatchType, redisConnectionType},
	redisPolicyType:            {redisAssignmentType},
	redisEnterpriseType:        {redisDatabaseType, redisEnterpriseConnectionType},
	redisDatabaseType:          {redisDatabaseAssignmentType},
	cdnWAFType:                 {cdnEndpointType},
	frontDoorWAFType:           {afdSecurityPolicyType},
	afdEndpointType:            {afdSecurityPolicyType},
	afdDomainType:              {afdRouteType, afdSecurityPolicyType},
	afdOriginGroupType:         {afdRouteType, afdRuleSetType, afdRuleType},
	afdRuleSetType:             {afdRouteType},
	afdSecretType:              {afdDomainType},
	cdnOriginType:              {cdnOriginGroupType},
	monitorWorkspaceType:       {dataCollectionAssociationType, monitorScopedResourceType},
	dataCollectionRuleType:     {dataCollectionAssociationType},
	dataCollectionEndpointType: {dataCollectionAssociationType, monitorScopedResourceType},
	monitorPrivateLinkType:     {monitorScopedResourceType, monitorPrivateConnectionType},
	// Native child DELETEs are available in the pinned Grafana REST API.
	grafanaType:             {grafanaPrivateEndpointType, grafanaConnectionType, grafanaIntegrationType},
	eventHubClusterType:     {eventHubNamespaceType},
	serviceBusNamespaceType: {serviceBusMigrationType, serviceBusRecoveryType},
	eventHubNamespaceType:   {eventHubRecoveryType},
	hostGroupType:           {hostType},
	capacityGroupType:       {capacityType},
	vpnGatewayType:          {vpnConnectionType, vpnNATRuleType},
	expressGatewayType:      {expressConnectionType},
}

func servicePrerequisiteKind(parent, child string) bool {
	if monitorPrivateLinkTarget(parent) && strings.EqualFold(child, monitorScopedResourceType) {
		return true
	}
	if isAPIMType(parent) && slices.Contains(apimIncomingKinds(parent), child) {
		return true
	}
	if isCosmosType(parent) {
		return cosmosPrerequisiteKind(parent, child)
	}
	if strings.EqualFold(parent, scaleSetType) && strings.EqualFold(child, vmType) {
		return true // serviceChildRelation separately requires Flexible mode.
	}
	for kind, children := range servicePrerequisiteRules {
		if strings.EqualFold(kind, parent) {
			return slices.ContainsFunc(children, func(kind string) bool { return strings.EqualFold(kind, child) })
		}
	}
	return false
}

func hasServicePrerequisites(kind string) bool {
	return (isCosmosType(kind) && len(cosmosCascadeKinds(kind)) != 0) || kind == scaleSetType || servicePrerequisiteRules[kind] != nil
}

// Keep the entire sanitized native configuration, excluding only the parent's
// ETag, modification audit fields and these documented child collections. The
// executor supplies the reviewed child steps; native GETs must prove each gone.
// This permits the ETag change caused by those deletes, not arbitrary drift.
func serviceParentConfiguration(kind string, raw map[string]any) string {
	safe := safePayload(raw)
	safe["id"] = strings.ToLower(text(raw["id"]))
	safe["name"] = last(text(safe["id"]))
	delete(safe, "type") // A bound response may omit this redundant field.
	delete(safe, "etag")
	for _, field := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(safe["systemData"]), field)
	}
	fields := map[string][]string{
		domainType:                 {"managedHostNames", "registrationStatus", "readyForDnsRecordManagement", "domainNotRenewableReasons"},
		dataCollectionEndpointType: {"privateLinkScopedResources"},
		monitorPrivateLinkType:     {"privateEndpointConnections"},
		grafanaType:                {"privateEndpointConnections"},
		serviceBusNamespaceType:    {"updatedAt"},
		eventHubNamespaceType:      {"updatedAt"},
		hostGroupType:              {"hosts"},
		capacityGroupType:          {"capacityReservations"},
		vpnGatewayType:             {"connections", "natRules"},
		expressGatewayType:         {"expressRouteConnections"},
	}
	for _, field := range fields[kind] {
		delete(object(safe["properties"]), field)
	}
	payload, _ := json.Marshal(safe)
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

func serviceParentConfigurationMatches(planned asset.Asset, live map[string]any) bool {
	return hasServicePrerequisites(planned.Identity.NativeType) && text(planned.Normalized["_arm_parent_configuration"]) != "" && text(planned.Normalized["_arm_parent_configuration"]) == serviceParentConfiguration(planned.Identity.NativeType, live)
}

func cloneNormalizedWithoutGeneration(normalized map[string]any) map[string]any {
	copy := map[string]any{}
	for key, value := range normalized {
		if key != "_arm_generation" {
			copy[key] = value
		}
	}
	return copy
}

func (a *action) servicePrerequisitesAbsent(ctx context.Context, request contracts.ActionRequest) error {
	seen := map[string]bool{}
	assetIDs := map[asset.AssetID]bool{request.Asset.ID: true}
	for _, prerequisite := range request.PrerequisiteDeletions {
		identity := prerequisite.Asset.Identity
		if identity.NativeType == batchNodeType {
			id := strings.ToLower(identity.NativeID)
			if !prerequisite.Delete || prerequisite.Asset.ID == "" || assetIDs[prerequisite.Asset.ID] || prerequisite.ControllerID != request.Asset.ID || seen[id] || identity.Provider != asset.ProviderAzure || identity.ConnectionID != request.Asset.Identity.ConnectionID || identity.Partition != request.Asset.Identity.Partition {
				return serviceDenied("invalid_service_prerequisite")
			}
			seen[id], assetIDs[prerequisite.Asset.ID] = true, true
			if err := a.batchNodePrerequisiteAbsent(ctx, request.Asset, prerequisite.Asset); err != nil {
				return err
			}
			continue
		}
		id, nativeType, err := parseID(identity.NativeID)
		if err != nil || !prerequisite.Delete || prerequisite.Asset.ID == "" || assetIDs[prerequisite.Asset.ID] || prerequisite.ControllerID != request.Asset.ID || seen[id] || identity.Provider != asset.ProviderAzure || identity.ConnectionID != request.Asset.Identity.ConnectionID || identity.Partition != request.Asset.Identity.Partition || !strings.HasPrefix(id, a.client.root()+"/") || !strings.EqualFold(nativeType, identity.NativeType) || !servicePrerequisiteKind(a.kind.NativeType, identity.NativeType) || (!serviceChildRelation(request.Asset, prerequisite.Asset) && !incomingMigrationPrerequisite(request.Asset, prerequisite.Asset) && !recoveryPrerequisite(request.Asset, prerequisite.Asset) && !cdnPrerequisite(request.Asset, prerequisite.Asset) && !apimPrerequisite(request.Asset, prerequisite.Asset) && !wafPrerequisite(request.Asset, prerequisite.Asset) && !redisSharedPrerequisite(request.Asset, prerequisite.Asset)) {
			return serviceDenied("invalid_service_prerequisite")
		}
		seen[id], assetIDs[prerequisite.Asset.ID] = true, true
		if isDomainType(identity.NativeType) {
			if _, err := a.client.domainPlan(prerequisite.Asset); err != nil {
				return err
			}
		}
		kind, known := findType(identity.NativeType)
		if !known || kind.ReadOnly {
			return serviceDenied("invalid_service_prerequisite")
		}
		endpoint, err := a.client.plannedResourceURL(prerequisite.Asset)
		if err != nil {
			return err
		}
		if _, err := a.client.readResource(ctx, endpoint); !isNotFound(err) {
			if err != nil {
				return err
			}
			return serviceDenied("service_prerequisite_still_exists")
		}
	}
	return nil
}

func serviceAssociationReason(kind string, raw map[string]any) string {
	var fields []string
	switch kind {
	case hostType:
		fields = []string{"virtualMachines"}
	case capacityGroupType, capacityType:
		fields = []string{"virtualMachinesAssociated"}
	case vpnNATRuleType:
		fields = []string{"ingressVpnSiteLinkConnections", "egressVpnSiteLinkConnections"}
	}
	for _, field := range fields {
		if value := object(raw["properties"])[field]; value != nil {
			members, ok := value.([]any)
			if !ok || len(members) != 0 {
				if kind == vpnNATRuleType {
					return "azure_vpn_nat_rule_has_link_connections"
				}
				return "azure_compute_resource_has_associated_vms"
			}
		}
	}
	return ""
}
