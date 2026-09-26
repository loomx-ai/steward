package azure

import (
	"context"
	"fmt"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const serviceCascadeSource = "azure:service-cascade"
const sqlServerType = "Microsoft.Sql/servers"
const sqlDatabaseType = "Microsoft.Sql/servers/databases"
const vmExtensionType = vmType + "/extensions"

const networkWatcherType = "Microsoft.Network/networkWatchers"

const (
	eventGridTopicType            = "Microsoft.EventGrid/topics"
	eventGridSubscriptionType     = eventGridTopicType + "/eventSubscriptions"
	eventGridSystemTopicType      = "Microsoft.EventGrid/systemTopics"
	eventGridSystemSubscription   = eventGridSystemTopicType + "/eventSubscriptions"
	avdHostPoolType               = "Microsoft.DesktopVirtualization/hostPools"
	avdSessionHostType            = avdHostPoolType + "/sessionHosts"
	avdApplicationGroupType       = "Microsoft.DesktopVirtualization/applicationGroups"
	avdWorkspaceType              = "Microsoft.DesktopVirtualization/workspaces"
	hdinsightClusterType          = "Microsoft.HDInsight/clusters"
	logAnalyticsTableType         = "Microsoft.OperationalInsights/workspaces/tables"
	logAnalyticsTableCustomSuffix = "_CL"
	lbType                        = "Microsoft.Network/loadBalancers"
	lbBackendPoolType             = lbType + "/backendAddressPools"
)

// Native deletion semantics, not an inference from ARM path nesting.
// https://learn.microsoft.com/azure/network-watcher/network-watcher-create
var serviceCascadeRules = map[string][]string{
	domainType:                     {domainOwnershipType},
	fleetType:                      fleetDirectKinds,
	fleetRunType:                   {fleetGateType},
	streamAnalyticsJobType:         streamAnalyticsOwnedKinds(streamAnalyticsJobType),
	streamAnalyticsClusterType:     {streamAnalyticsEndpointType, streamAnalyticsJobType},
	kustoType:                      kustoOwnedKinds(kustoType),
	kustoDatabaseType:              append(kustoOwnedKinds(kustoDatabaseType), kustoAttachmentType),
	kustoAttachmentType:            {kustoDatabaseType},
	mongoClusterType:               append(mongoClusterOwnedKinds(), mongoClusterType),
	cognitiveType:                  cognitiveOwnedKinds(cognitiveType),
	cognitiveProjectType:           cognitiveOwnedKinds(cognitiveProjectType),
	cognitiveApplicationType:       cognitiveOwnedKinds(cognitiveApplicationType),
	cognitiveNetworkType:           cognitiveOwnedKinds(cognitiveNetworkType),
	cognitiveBlocklistType:         cognitiveOwnedKinds(cognitiveBlocklistType),
	cognitiveSharedPlanType:        cognitiveOwnedKinds(cognitiveSharedPlanType),
	cognitiveHostType:              {},
	cognitiveProjectHostType:       {},
	cognitiveConnectionType:        {},
	cognitiveProjectConnectionType: {},
	cognitiveDeploymentType:        {},
	cognitivePolicyType:            {},

	searchType:          {searchConnectionType, searchLinkType, searchPerimeterType},
	redisType:           {redisPolicyType, redisAssignmentType, redisFirewallType, redisLinkType, redisPatchType, redisConnectionType},
	redisPolicyType:     {redisAssignmentType},
	redisLinkType:       {redisLinkType},
	redisEnterpriseType: {redisDatabaseType, redisEnterpriseConnectionType},
	redisDatabaseType:   {redisDatabaseAssignmentType},
	appSiteType:         {appSlotType, appFunctionType, appSiteCertificateType, appBindingType},
	appSlotType:         {appSlotFunctionType, appSlotCertificateType, appSlotBindingType},
	cdnWAFType:          {}, frontDoorWAFType: {}, // Associations are shared prerequisites, not owned children.
	// The native Profiles_Delete contract removes every subresource.
	cdnProfileType:     {cdnEndpointType, afdEndpointType, afdDomainType, afdOriginGroupType, afdRuleSetType, afdSecurityPolicyType, afdSecretType},
	cdnEndpointType:    {cdnOriginType, cdnOriginGroupType, cdnDomainType},
	afdEndpointType:    {afdRouteType},
	afdOriginGroupType: {afdOriginType},
	afdRuleSetType:     {afdRuleType},
	cdnOriginType:      {}, cdnOriginGroupType: {}, afdDomainType: {}, afdSecretType: {},
	monitorWorkspaceType:       {}, // Native default-ingestion managed group; discovered separately.
	dataCollectionRuleType:     {dataCollectionAssociationType},
	dataCollectionEndpointType: {dataCollectionAssociationType},
	// These are direct prerequisites; only their own APIs remove each child.
	monitorPrivateLinkType: {monitorScopedResourceType, monitorPrivateConnectionType},
	grafanaType:            {grafanaPrivateEndpointType, grafanaConnectionType, grafanaIntegrationType},
	// The dedicated cluster's native namespace list contains external ARM IDs.
	// Delete each namespace through its own reviewed lifecycle first.
	eventHubClusterType: {eventHubNamespaceType},
	// Native ARM namespace deletion removes associated resources.
	// https://learn.microsoft.com/rest/api/servicebus/controlplane/namespaces/delete
	serviceBusNamespaceType:    {serviceBusQueueType, serviceBusTopicType, serviceBusNamespaceType + "/authorizationRules", serviceBusRecoveryType, serviceBusMigrationType, serviceBusNamespaceType + "/privateEndpointConnections", serviceBusNamespaceType + "/networkRuleSets"},
	serviceBusQueueType:        {serviceBusQueueType + "/authorizationRules"},
	serviceBusTopicType:        {serviceBusSubscriptionType, serviceBusTopicType + "/authorizationRules"},
	serviceBusSubscriptionType: {serviceBusRuleType},
	serviceBusRecoveryType:     {serviceBusRecoveryType + "/authorizationRules"},
	eventHubNamespaceType:      {eventHubType, eventHubNamespaceType + "/authorizationRules", eventHubRecoveryType, eventHubNamespaceType + "/schemagroups", eventHubNamespaceType + "/applicationGroups", eventHubNamespaceType + "/privateEndpointConnections", eventHubNamespaceType + "/networkRuleSets", eventHubNamespaceType + "/networkSecurityPerimeterConfigurations"},
	eventHubType:               {eventHubConsumerGroupType, eventHubType + "/authorizationRules"},
	eventHubRecoveryType:       {eventHubRecoveryType + "/authorizationRules"},
	hostGroupType:              {hostType},
	capacityGroupType:          {capacityType},
	vpnGatewayType:             {vpnConnectionType, vpnNATRuleType},
	vpnConnectionType:          {vpnLinkConnectionType},
	expressGatewayType:         {expressConnectionType},
	vmType:                     {vmExtensionType},
	scaleSetType:               {scaleSetVMType, scaleSetExtensionType, vmType},
	scaleSetVMType:             {scaleSetVMExtensionType, scaleSetNICType, diskType},
	scaleSetNICType:            {scaleSetIPConfigType},
	scaleSetIPConfigType:       {scaleSetPublicIPType},
	privateEndpointType:        {nicType, privateDNSZoneGroupType},
	publicDNSZoneType:          dnsChildTypes(publicDNSZoneType),
	privateDNSZoneType:         dnsChildTypes(privateDNSZoneType),
	privateDNSZoneGroupType:    {privateDNSZoneType + "/A", privateDNSZoneType + "/AAAA"},
	privateDNSLinkType:         {privateDNSZoneType + "/A", privateDNSZoneType + "/AAAA"},
	// https://learn.microsoft.com/azure/azure-sql/database/logical-servers
	sqlServerType: {sqlDatabaseType, "Microsoft.Sql/servers/elasticPools"},
	// Deleting a topic deletes its event subscriptions.
	// https://learn.microsoft.com/rest/api/eventgrid/controlplane/topics/delete
	eventGridTopicType:       {eventGridSubscriptionType},
	eventGridSystemTopicType: {eventGridSystemSubscription},
	// Backend pools are load balancer configuration and go with it.
	lbType: {lbBackendPoolType},
	// Without force, a host pool is deleted only after its session hosts.
	avdHostPoolType: {avdSessionHostType},
	networkWatcherType: {
		"Microsoft.Network/networkWatchers/flowLogs",
		"Microsoft.Network/networkWatchers/connectionMonitors",
		"Microsoft.Network/networkWatchers/packetCaptures",
	},
}

func HasServiceCascade(nativeType string) bool {
	if isAPIMType(nativeType) {
		return len(apimOwnedKinds(nativeType)) != 0
	}
	if isCosmosType(nativeType) {
		return len(cosmosCascadeKinds(nativeType)) != 0
	}
	for kind := range serviceCascadeRules {
		if strings.EqualFold(kind, nativeType) {
			return true
		}
	}
	return false
}

func serviceChildKinds(nativeType string) []string {
	if isAPIMType(nativeType) {
		return apimOwnedKinds(nativeType)
	}
	if isCosmosType(nativeType) {
		return cosmosCascadeKinds(nativeType)
	}
	for kind, children := range serviceCascadeRules {
		if strings.EqualFold(kind, nativeType) {
			return children
		}
	}
	return nil
}

type serviceCascades struct {
	client       *client
	connectionID asset.ConnectionID
}

func (r *Runtime) ServiceLifecycle(ctx context.Context, id asset.ConnectionID) (governance.Contributor, error) {
	c, err := r.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	return &serviceCascades{client: c, connectionID: id}, nil
}

type serviceChild struct {
	kind, id string
	data     map[string]any
	direct   bool
}

// Each collection uses the same explicit native list rule as inventory. Read
// every child and re-read the parent; an unreadable or changing set is not empty.
func (c *client) nativeServiceChildren(ctx context.Context, parent asset.Identity, raw map[string]any, childTypes []string) ([]serviceChild, error) {
	parentKind, _ := findType(parent.NativeType)
	wireParent := parent.NativeID
	if isCosmosType(parent.NativeType) {
		wireParent = responseID(parent.NativeType, text(raw["id"]))
		id, kind, err := parseID(wireParent)
		if err != nil || !strings.EqualFold(id, parent.NativeID) || !strings.EqualFold(kind, parent.NativeType) {
			return nil, fmt.Errorf("Cosmos DB parent identity mismatch")
		}
	}
	_, params, err := c.resourceOperation(parentKind, wireParent, "GET")
	if err != nil {
		return nil, err
	}
	parentID, _, _ := parseID(parent.NativeID)
	item := contracts.InventoryItem{NativeID: parentID, NativeType: parentKind.NativeType, Normalized: map[string]any{
		"arm_parameters": params, "name": last(parentID), "resource_group": strings.Split(parentID, "/")[4],
	}}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{bundle: metadata.bundle}
	var children []serviceChild
	seen := map[string]bool{}
	for _, childType := range childTypes {
		definition, ok := runtime.productDefinition(childType)
		if !ok || definition.Discovery.List == nil || definition.Discovery.Parent == nil || !strings.EqualFold(definition.Discovery.Parent.NativeType, parentKind.NativeType) {
			return nil, fmt.Errorf("Azure cascade child has no explicit parent discovery")
		}
		bound, err := c.bindProductList(definition.Discovery.List, text(raw["location"]), item)
		if err != nil {
			return nil, err
		}
		u, _ := url.Parse(bound.URL)
		var records []any
		if childType == apimIssueType {
			records, _, _, err = c.apimIssuePage(ctx, parentID)
		} else {
			records, err = c.listAllURL(ctx, bound.URL, u.Path)
		}
		if err != nil {
			return nil, err
		}
		kind, _ := findType(childType)
		for _, value := range records {
			record := object(value)
			if isAPIMAssociation(childType) {
				record, err = apimAssociationRow(parentID, childType, record)
				if err != nil {
					return nil, err
				}
			}
			wireID := responseID(childType, text(record["id"]))
			id, parsedType, err := parseID(wireID)
			if err != nil || !strings.EqualFold(parsedType, childType) || !strings.EqualFold(id, u.Path+"/"+last(id)) || seen[id] || !validResponseType(childType, text(record["type"])) {
				return nil, fmt.Errorf("invalid or duplicate Azure cascade child identity")
			}
			seen[id] = true
			if isCosmosType(childType) && !cosmosSameWireID(cosmosParentID(wireID), wireParent) {
				return nil, fmt.Errorf("Cosmos DB child parent name mismatch")
			}
			endpoint, err := c.resourceURL(kind, wireID)
			if err != nil {
				return nil, err
			}
			live, err := c.readResource(ctx, endpoint)
			if err != nil {
				return nil, err
			}
			if !validResourceResponse(live, id, childType) {
				return nil, fmt.Errorf("Azure cascade child read identity mismatch")
			}
			if ctx.Value(domainReadContextKey{}) == true && isAppServiceType(childType) && (!insightsARMReadValid(live, id, childType) || !nativeConfigurationContains(appServiceSnapshot(childType, record), appServiceSnapshot(childType, live.data))) {
				return nil, serviceDenied("domain_binding_index_changed")
			}
			if isDomainType(childType) && (!insightsARMReadValid(live, id, childType) || !nativeConfigurationContains(domainSnapshot(childType, record), domainSnapshot(childType, live.data))) {
				return nil, serviceDenied("domain_child_index_changed")
			}
			if isDomainType(childType) {
				if err := domainMetadata(childType, live.data); err != nil {
					return nil, err
				}
			}
			if isAPIMType(childType) {
				if err := apimListedIncarnation(childType, record, live.data); err != nil {
					return nil, err
				}
			}
			if isStreamAnalyticsType(childType) {
				if err := streamAnalyticsListedIncarnation(childType, record, live.data); err != nil {
					return nil, err
				}
			}
			if isBatchType(childType) && !nativeConfigurationContains(batchSnapshot(childType, record), batchSnapshot(childType, live.data)) {
				return nil, serviceDenied("batch_listed_configuration_changed")
			}
			if communicationKind(childType) != "" && (communicationARMMetadata(childType, live.data) != nil || !nativeConfigurationContains(communicationSnapshot(childType, record), communicationSnapshot(childType, live.data))) {
				return nil, serviceDenied("communication_child_index_changed")
			}
			if isCosmosType(childType) {
				if err := cosmosListedIncarnation(childType, record, live.data); err != nil {
					return nil, err
				}
			}
			if err := monitorPrivateLinkListed(childType, record, live.data); err != nil {
				return nil, err
			}
			// Lists can omit generation fields; compare every field they do expose.
			if err := serviceListedIncarnation(record, live.data); err != nil {
				return nil, err
			}
			liveID := responseID(childType, text(live.data["id"]))
			live.data["id"] = id
			if isCosmosType(childType) {
				if !cosmosSameWireID(liveID, wireID) {
					return nil, fmt.Errorf("Cosmos DB child name changed")
				}
				live.data["id"] = wireID
				live.data["type"] = childType
			}
			children = append(children, serviceChild{kind: childType, id: id, data: live.data})
		}
	}
	if err := apimNotificationRecipients(raw, childTypes, children); err != nil {
		return nil, err
	}
	if err := apimGatewaySourceMembership(parent.NativeType, children); err != nil {
		return nil, err
	}
	if err := c.verifyProductParent(ctx, productTarget{ParentID: parentID, ParentWireID: wireParent, ParentType: parentKind.NativeType, Generation: productGeneration(raw)}); err != nil {
		return nil, err
	}
	sort.Slice(children, func(i, j int) bool { return children[i].id < children[j].id })
	return children, nil
}

func serviceListedIncarnation(listed, live map[string]any) error {
	for _, field := range []string{"resourceGuid", "resourceUid", "uniqueId", "vmId", "creationTime", "timeCreated", "creationDate", "databaseId", "hostId", "createdAt", "createdAtUtc", "eTag", "immutableId", "accountId", "internalId", "dateCreated", "deploymentId", "commitmentPlanGuid", "topicId"} {
		if expected := object(listed["properties"])[field]; expected != nil && !reflect.DeepEqual(expected, object(live["properties"])[field]) {
			return fmt.Errorf("Azure resource incarnation changed")
		}
	}
	for _, field := range []string{"etag"} {
		if expected := listed[field]; expected != nil && !reflect.DeepEqual(expected, live[field]) {
			return fmt.Errorf("Azure resource generation changed")
		}
	}
	if expected := object(listed["systemData"])["createdAt"]; expected != nil && !reflect.DeepEqual(expected, object(live["systemData"])["createdAt"]) {
		return fmt.Errorf("Azure resource creation identity changed")
	}
	return nil
}

func serviceIncarnation(planned asset.Asset, live map[string]any) error {
	if err := apimIncarnation(planned, live); err != nil {
		return err
	}
	if isAPIMType(planned.Identity.NativeType) {
		planned.Normalized = cloneNormalizedWithoutGeneration(planned.Normalized)
	}
	if err := streamAnalyticsIncarnation(planned, live); err != nil {
		return err
	}
	if isStreamAnalyticsType(planned.Identity.NativeType) {
		planned.Normalized = cloneNormalizedWithoutGeneration(planned.Normalized)
	}
	if err := kustoIncarnation(planned, live); err != nil {
		return err
	}
	if isKustoType(planned.Identity.NativeType) {
		planned.Normalized = cloneNormalizedWithoutGeneration(planned.Normalized)
	}
	if isCosmosType(planned.Identity.NativeType) {
		if err := cosmosIncarnation(planned, live); err != nil {
			return err
		}
		return serviceCreationIdentity(planned, live)
	}
	if err := mongoClusterIncarnation(planned, live); err != nil {
		return err
	}
	if isMongoClusterType(planned.Identity.NativeType) {
		planned.Normalized = cloneNormalizedWithoutGeneration(planned.Normalized)
	}
	if err := cognitiveIncarnation(planned, live); err != nil {
		return err
	}
	if isCognitiveType(planned.Identity.NativeType) {
		planned.Normalized = cloneNormalizedWithoutGeneration(planned.Normalized)
	}
	if err := searchIncarnation(planned, live); err != nil {
		return err
	}
	if isSearchType(planned.Identity.NativeType) {
		planned.Normalized = cloneNormalizedWithoutGeneration(planned.Normalized)
		delete(planned.Normalized, "eTag") // Child deletion changes Search's property ETag.
	}
	if err := redisIncarnation(planned, live); err != nil {
		return err
	}
	if isRedisType(planned.Identity.NativeType) {
		planned.Normalized = cloneNormalizedWithoutGeneration(planned.Normalized)
	}
	if err := appServiceIncarnation(planned, live); err != nil {
		return err
	}
	if isAppServiceType(planned.Identity.NativeType) {
		planned.Normalized = cloneNormalizedWithoutGeneration(planned.Normalized)
	}
	if isDomainType(planned.Identity.NativeType) {
		// The private domain snapshot binds creation, renewal and DNS settings;
		// independent prerequisite deletion may change the operational ETag.
		planned.Normalized = cloneNormalizedWithoutGeneration(planned.Normalized)
	}
	if err := wafIncarnation(planned, live); err != nil {
		return err
	}
	if isWAFType(planned.Identity.NativeType) {
		// Native ETags also change when prerequisite associations disappear.
		// wafIncarnation and the keyed digest bind all remaining configuration.
		planned.Normalized = cloneNormalizedWithoutGeneration(planned.Normalized)
	}
	if err := cdnIncarnation(planned, live); err != nil {
		return err
	}
	if err := containerGroupIncarnation(planned, live); err != nil {
		return err
	}
	if strings.EqualFold(planned.Identity.NativeType, groupType) {
		if expected, captured := planned.Normalized["_managed_group_owner"]; captured && !strings.EqualFold(text(expected), text(live["managedBy"])) {
			return serviceDenied("managed_resource_group_owner_changed")
		}
	}
	if err := monitorWorkspaceIncarnation(planned, live); err != nil {
		return err
	}
	if err := dataCollectionIncarnation(planned, live); err != nil {
		return err
	}
	if err := monitorPrivateLinkIncarnation(planned, live); err != nil {
		return err
	}
	if err := grafanaIncarnation(planned, live); err != nil {
		return err
	}
	if recoveryType(planned.Identity.NativeType) && text(planned.Normalized["_recovery_configuration"]) != "" && text(planned.Normalized["_recovery_configuration"]) == recoveryConfiguration(live) {
		planned.Normalized = cloneNormalizedWithoutGeneration(planned.Normalized)
	}
	if err := serviceCreationIdentity(planned, live); err != nil {
		return err
	}
	if strings.EqualFold(planned.Identity.NativeType, scaleSetType) {
		expected, expectedErr := scaleSetMode(planned.Normalized)
		actual, actualErr := scaleSetMode(object(live["properties"]))
		if expectedErr != nil || actualErr != nil || expected != actual {
			return serviceDenied("scale_set_orchestration_changed")
		}
	}
	if expected := text(planned.Normalized["_arm_generation"]); expected != "" && expected != productGeneration(live) {
		return serviceDenied("service_resource_generation_changed")
	}
	return serviceListedIncarnation(map[string]any{"properties": planned.Normalized}, live)
}

func serviceDenied(reason string) error {
	return &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProtected, Code: reason, Message: contracts.SafeProviderValidationMessage}}
}

func (s *serviceCascades) Contribute(ctx context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	if _, err := s.client.fleetHubOwners(assets); err != nil {
		return result, contracts.DependencyReadError(err)
	}
	var monitorTargets, roleTargets []asset.Asset
	for _, value := range assets {
		if value.Identity.Provider == asset.ProviderAzure && value.Identity.NativeType == rbacRoleType {
			roleTargets = append(roleTargets, value)
		}
		if monitorARMTarget(value) && strings.HasPrefix(strings.ToLower(value.Identity.NativeID), s.client.root()+"/") {
			monitorTargets = append(monitorTargets, value)
		}
	}
	incoming, err := s.client.contributeMonitorIncoming(ctx, monitorTargets, assets)
	if err != nil {
		return result, err
	}
	result.Unresolved = append(result.Unresolved, incoming.Unresolved...)
	result.Relationships = append(result.Relationships, incoming.Relationships...)
	rbac, err := s.client.verifiedIncoming(func() (map[string][]monitorIncomingSource, error) {
		return s.client.rbacIncomingObservation(ctx, roleTargets, assets)
	})
	if err != nil {
		return result, contracts.DependencyReadError(err)
	}
	incoming, err = s.client.contributeIncomingSources(roleTargets, assets, rbac)
	if err != nil {
		return result, contracts.DependencyReadError(err)
	}
	result.Unresolved = append(result.Unresolved, incoming.Unresolved...)
	if err := s.contributeBatch(ctx, assets, &result); err != nil {
		return result, err
	}
	if err := s.contributeCommunication(ctx, assets, &result); err != nil {
		return result, contracts.DependencyReadError(err)
	}
	if err := s.contributeElasticSanRoots(ctx, assets, &result); err != nil {
		return result, err
	}
	if err := s.contributeElasticSanGroups(ctx, assets, &result); err != nil {
		return result, contracts.DependencyReadError(err)
	}
	if err := s.contributeElasticSanVolumes(ctx, assets, &result); err != nil {
		return result, contracts.DependencyReadError(err)
	}
	if err := s.contributeDataFactory(ctx, assets, &result); err != nil {
		return result, contracts.DependencyReadError(err)
	}
	if err := s.contributeDataMigration(ctx, assets, &result); err != nil {
		return result, contracts.DependencyReadError(err)
	}
	if err := s.contributeAzureLocalRoots(ctx, assets, &result); err != nil {
		return result, err
	}
	if err := s.contributeAzureLocalVMs(ctx, assets, &result); err != nil {
		return result, contracts.DependencyReadError(err)
	}
	if err := s.contributeHybridComputeMachines(ctx, assets, &result); err != nil {
		return result, contracts.DependencyReadError(err)
	}
	if err := s.contributeHybridComputeLicenses(ctx, assets, &result); err != nil {
		return result, contracts.DependencyReadError(err)
	}
	batchOwners := batchManagedNodes(assets, result)
	if err := s.contributeIncomingMigrations(ctx, assets, &result); err != nil {
		return result, err
	}
	if err := s.contributeCDNReferences(ctx, assets, &result); err != nil {
		return result, err
	}
	if err := s.contributeAPIMReferences(ctx, assets, &result); err != nil {
		return result, err
	}
	if err := s.contributeMonitorPrivateLinkReferences(ctx, assets, &result); err != nil {
		return result, err
	}
	if err := s.contributeWAFReferences(ctx, assets, &result); err != nil {
		return result, err
	}
	contributeDesktopVirtualization(assets, &result)
	aksMembers := managedGroupMembers(assets)
	parents := slices.Clone(assets)
	sort.SliceStable(parents, func(i, j int) bool {
		return dnsExternalController(parents[i].Identity.NativeType) && !dnsExternalController(parents[j].Identity.NativeType)
	})
	dnsOwners := map[string]asset.AssetID{}
	for _, parent := range parents {
		if parent.Identity.Provider == asset.ProviderAzure && (parent.Identity.NativeType == recoveryServicesItem || parent.Identity.NativeType == recoveryServicesContainer) {
			contribution, err := s.client.contributeRecoverySources(ctx, s.connectionID, parent, assets)
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
		}
		if parent.Identity.Provider == asset.ProviderAzure && parent.Identity.NativeType == recoveryServicesItem {
			contribution, err := s.client.contributeRecoveryItemPrerequisites(ctx, s.connectionID, parent, assets)
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}

		if parent.Identity.Provider == asset.ProviderAzure && parent.Identity.NativeType == dataProtectionInstance {
			contribution, err := s.client.contributeDataProtectionSources(ctx, s.connectionID, parent, assets)
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}
		if parent.Identity.Provider == asset.ProviderAzure && strings.EqualFold(parent.Identity.NativeType, deploymentStackType) {
			contribution, err := s.client.deploymentStackContribution(ctx, parent, assets)
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}

		if parent.Identity.Provider == asset.ProviderAzure && netappAssignmentKind(parent.Identity.NativeType) {
			contribution, err := s.client.netappAssignmentContribution(parent, assets)
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			result.Bindings = append(result.Bindings, contribution.Bindings...)
			if parent.Identity.NativeType == netappVaultType {
				vault, err := s.client.netappVaultContribution(parent, assets)
				if err != nil {
					return result, err
				}
				result.Bindings = append(result.Bindings, vault.Bindings...)
				result.Relationships = append(result.Relationships, vault.Relationships...)
				result.Unresolved = append(result.Unresolved, vault.Unresolved...)
			}
			continue
		}
		if parent.Identity.Provider == asset.ProviderAzure && parent.Identity.NativeType == netappGroupType {
			contribution, err := s.client.netappGroupContribution(parent, assets)
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}
		if parent.Identity.Provider == asset.ProviderAzure && parent.Identity.NativeType == netappPoolType {
			contribution, err := s.client.netappPoolContribution(parent, assets)
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Bindings = append(result.Bindings, contribution.Bindings...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}
		if parent.Identity.Provider == asset.ProviderAzure && parent.Identity.NativeType == netappVolumeType {
			contribution, err := s.client.netappVolumeContribution(parent, assets)
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Bindings = append(result.Bindings, contribution.Bindings...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}
		if parent.Identity.Provider == asset.ProviderAzure && parent.Identity.NativeType == synapseType {
			contribution, err := s.client.synapseWorkspaceContribution(parent, assets)
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Bindings = append(result.Bindings, contribution.Bindings...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}
		if parent.Identity.Provider == asset.ProviderAzure && elasticSanKind(parent.Identity.NativeType) != "" {
			refs, err := s.client.elasticSanRecordedReferences(parent)
			if err != nil {
				return result, err
			}
			contribution, err := s.client.contributeNativeReferences(parent, assets, refs, "azure:elastic-san-reference")
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}
		if parent.Identity.Provider == asset.ProviderAzure && azureLocalKind(parent.Identity.NativeType) != "" {
			refs, err := s.client.azureLocalRecordedReferences(parent)
			if err != nil {
				return result, err
			}
			contribution, err := s.client.contributeNativeReferences(parent, assets, refs, "azure:local-reference")
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}
		if parent.Identity.Provider == asset.ProviderAzure && hybridComputeKind(parent.Identity.NativeType) != "" {
			refs, err := s.client.hybridComputeRecordedReferences(parent)
			if err != nil {
				return result, err
			}
			contribution, err := s.client.contributeNativeReferences(parent, assets, refs, "azure:hybrid-compute-reference")
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}
		if parent.Identity.Provider == asset.ProviderAzure && parent.Identity.NativeType == defenderPricingType {
			refs, err := s.client.defenderRecordedReferences(parent)
			if err != nil {
				return result, err
			}
			contribution, err := s.client.contributeNativeReferences(parent, assets, refs, "azure:defender-plan-reference")
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}
		if batchOwners[parent.ID].ID != "" {
			continue // Batch already contributed this VM's complete native tree.
		}
		if parent.Identity.Provider == asset.ProviderAzure && fleetKind(parent.Identity.NativeType).kind != "" {
			if parent.Identity.NativeType == fleetType {
				contribution, err := s.client.contributeFleetHub(ctx, parent, assets)
				if err != nil {
					return result, err
				}
				result.Bindings = append(result.Bindings, contribution.Bindings...)
				result.Relationships = append(result.Relationships, contribution.Relationships...)
				result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			}
			contribution, err := s.client.contributeFleetReferences(ctx, parent, assets)
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
		}
		if parent.Identity.Provider == asset.ProviderAzure && rbacResourceKind(parent.Identity.NativeType) != "" {
			contribution, err := s.client.contributeRBACReferences(ctx, parent, assets)
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}
		if parent.Identity.Provider == asset.ProviderAzure && parent.Identity.NativeType == diagnosticSettingsType {
			contribution, err := s.client.contributeDiagnosticReferences(ctx, parent, assets)
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}
		if parent.Identity.Provider == asset.ProviderAzure && monitorResourceKind(parent.Identity.NativeType) != "" {
			_, registered := findType(parent.Identity.NativeType)
			if registered || parent.Normalized[monitorConfigurationProof] != nil {
				contribution, err := s.client.contributeMonitorReferences(ctx, parent, assets)
				if err != nil {
					return result, err
				}
				result.Relationships = append(result.Relationships, contribution.Relationships...)
				result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
				continue
			}
		}
		if parent.Identity.Provider == asset.ProviderAzure && insightsWorkbookKind(parent.Identity.NativeType) != "" {
			contribution, err := s.client.contributeWorkbookReferences(ctx, parent, assets)
			if err != nil {
				return result, err
			}
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}
		if isBatchType(parent.Identity.NativeType) || communicationKind(parent.Identity.NativeType) != "" || dataFactoryKind(parent.Identity.NativeType) != "" || dataMigrationKind(parent.Identity.NativeType) != "" {
			continue // Native data-plane identities have their own ownership walk.
		}
		if isWAFType(parent.Identity.NativeType) {
			continue // Already contributed through the native reverse indexes.
		}
		if parent.Identity.Provider == asset.ProviderAzure && parent.Identity.NativeType == applicationInsightsType {
			contribution, err := s.client.contributeInsightsChildren(ctx, parent, assets)
			if err != nil {
				return result, err
			}
			result.Bindings = append(result.Bindings, contribution.Bindings...)
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			contribution, err = s.client.contributeInsightsWorkspace(ctx, parent, assets)
			if err != nil {
				return result, err
			}
			result.Bindings = append(result.Bindings, contribution.Bindings...)
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}
		if parent.Identity.Provider != asset.ProviderAzure || !HasServiceCascade(parent.Identity.NativeType) || aksMembers[managedGroupKey(parent.Identity, parent.Identity.NativeID)] {
			continue
		}
		if strings.EqualFold(parent.Identity.NativeType, monitorWorkspaceType) {
			contribution, err := s.client.contributeMonitorWorkspace(ctx, parent, assets)
			if err != nil {
				return result, err
			}
			result.Bindings = append(result.Bindings, contribution.Bindings...)
			result.Relationships = append(result.Relationships, contribution.Relationships...)
			result.Unresolved = append(result.Unresolved, contribution.Unresolved...)
			continue
		}
		endpoint, err := s.client.plannedResourceURL(parent)
		if err != nil {
			return result, err
		}
		live, err := s.client.readResource(ctx, endpoint)
		if err != nil {
			return result, err
		}
		if !validResourceResponse(live, parent.Identity.NativeID, parent.Identity.NativeType) {
			return result, fmt.Errorf("Azure service parent identity mismatch")
		}
		if err := s.client.servicePrivateIncarnation(parent, live.data); err != nil {
			return result, err
		}
		if err := serviceIncarnation(parent, live.data); err != nil {
			return result, err
		}
		if parent.Identity.NativeType == domainType {
			zone := text(parent.Normalized["_domain_dns_zone"])
			for _, target := range assets {
				if target.Identity.Provider != parent.Identity.Provider || target.Identity.ConnectionID != parent.Identity.ConnectionID || target.Identity.Partition != parent.Identity.Partition || target.Identity.NativeType != publicDNSZoneType || target.Identity.NativeID != zone {
					continue
				}
				// DNS hosting does not own the registration. Deleting a zone
				// requires explicit selection of its registered domain or repointing it.
				evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource}
				result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: parent.ID, Type: graph.RelationshipDependsOn, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
			}
		}
		if parent.Identity.NativeType == eventHubClusterType {
			if err := s.client.verifyEventHubClusterSettings(ctx, parent); err != nil {
				return result, err
			}
		}
		children, err := s.client.plannedServiceChildren(ctx, parent, live.data, assets...)
		if err != nil {
			return result, err
		}
		for _, child := range children {
			if recoveryType(child.kind) && parent.Identity.NativeType+"/disasterRecoveryConfigs" == child.kind && strings.EqualFold(text(object(child.data["properties"])["role"]), "Secondary") {
				if err := s.contributeRecoveryPrerequisite(ctx, parent, child, assets, &result); err != nil {
					return result, err
				}
				continue
			}
			ownerKey := string(parent.Identity.ConnectionID) + "|" + parent.Identity.Partition + "|" + child.id
			if dnsExternalController(parent.Identity.NativeType) {
				if owner, exists := dnsOwners[ownerKey]; exists && owner != parent.ID {
					return result, fmt.Errorf("ambiguous Azure DNS controller ownership")
				}
				dnsOwners[ownerKey] = parent.ID
			} else if isDNSZoneType(parent.Identity.NativeType) && dnsOwners[ownerKey] != "" {
				continue
			}
			evidence := map[string]any{"resource_type": child.kind, "instance_id": child.id, "delete_by_default": true, "retention_supported": false, graph.LifecycleEvidenceControllerDeleteGuaranteed: true, graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
			policy := graph.CleanupDelegate
			if child.direct {
				policy = graph.CleanupDirect
				delete(evidence, graph.LifecycleEvidenceControllerDeleteGuaranteed)
				delete(evidence, graph.LifecycleEvidenceControllerVerifiesManagedAbsence)
			}
			var target *asset.Asset
			for i := range assets {
				candidate := &assets[i]
				if candidate.Identity.Provider == parent.Identity.Provider && candidate.Identity.ConnectionID == parent.Identity.ConnectionID && candidate.Identity.Partition == parent.Identity.Partition && strings.EqualFold(candidate.Identity.NativeType, child.kind) && strings.EqualFold(candidate.Identity.NativeID, child.id) {
					if target != nil {
						return result, fmt.Errorf("ambiguous Azure service child")
					}
					target = candidate
				}
			}
			if target == nil {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: child.kind, NativeID: child.id, ControllerID: parent.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
				continue
			}
			if node := batchOwners[target.ID]; node.ID != "" {
				if parent.Identity.NativeType != scaleSetType || child.kind != scaleSetVMType {
					return result, serviceDenied("ambiguous_batch_vm_controller")
				}
				evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource}
				result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: parent.ID, TargetAssetID: node.ID, Type: graph.RelationshipDependsOn, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
				continue
			}
			if aksMembers[managedGroupKey(target.Identity, child.id)] {
				continue
			}
			if recoveryType(parent.Identity.NativeType) && parent.Identity.NativeType == child.kind && !recoveryPeerRelation(parent, *target) {
				return result, serviceDenied("recovery_pair_changed")
			}
			if parent.Identity.NativeType == eventHubClusterType && !serviceChildRelation(parent, *target) {
				return result, serviceDenied("eventhub_cluster_membership_changed")
			}
			if err := s.client.servicePrivateIncarnation(*target, child.data); err != nil {
				return result, err
			}
			if fleetKind(child.kind).kind != "" {
				if err := s.client.fleetContext(ctx, *target, child.data); err != nil {
					return result, err
				}
			}
			if err := serviceIncarnation(*target, child.data); err != nil {
				return result, err
			}
			if streamAnalyticsClusterPrerequisite(parent, *target) {
				// Cluster deletion only requires stopped jobs at the service. It
				// does not require deleting those independent configurations.
				// Preserve their choice: a retained job must first be moved out
				// of the cluster; an explicitly selected job precedes the cluster.
				evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource}
				result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: parent.ID, TargetAssetID: target.ID, Type: graph.RelationshipDependsOn, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
				continue
			}
			if child.kind == dataCollectionAssociationType || domainPrerequisite(parent, *target) || redisSharedPrerequisite(parent, *target) || cognitiveSharedPrerequisite(parent, *target) || cosmosSharedPrerequisite(parent, *target) || mongoClusterReplicaPrerequisite(parent, *target) || kustoSharedPrerequisite(parent, *target) {
				// Reverse indexes establish an unlink prerequisite, not ownership
				// of the monitored resource or a potentially shared association.
				evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "resource_type": child.kind, "instance_id": child.id}
				result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: parent.ID, TargetAssetID: target.ID, Type: graph.RelationshipDependsOn, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
				continue
			}
			childKind, known := findType(child.kind)
			// Fixed APIM notification containers permit independent recipient
			// removal. Their non-actionable catalog entry still prevents a
			// DELETE of the container itself; it must not veto child unlinking.
			directAllowed := target.Normalized["_kusto_active_image"] != true && known && (!childKind.ReadOnly || isAPIMNotification(child.kind)) && !dnsExternalController(parent.Identity.NativeType) && protectionReason(childKind, child.data) == ""
			if text(target.Normalized["cleanup_protection_reason"]) == "azure_messaging_replication_requires_unpairing" {
				directAllowed = false
			}
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: parent.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: policy, DirectCleanupAllowed: directAllowed, EvidenceSource: serviceCascadeSource, Evidence: evidence, Confidence: 1})
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: parent.ID, Type: graph.RelationshipAttachedTo, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
		}
	}
	groups, err := s.client.contributeResourceGroups(ctx, s.connectionID, assets)
	if err != nil {
		return governance.Contribution{}, err
	}
	result.Bindings = append(result.Bindings, groups.Bindings...)
	result.Relationships = append(result.Relationships, groups.Relationships...)
	result.Unresolved = append(result.Unresolved, groups.Unresolved...)
	return result, nil
}

func (a *action) serviceImpacts(request contracts.ActionRequest) (map[string]contracts.ActionImpact, error) {
	root := request.Asset
	if root.Identity.Provider != asset.ProviderAzure || !strings.EqualFold(root.Identity.NativeID, a.id) || !strings.EqualFold(root.Identity.NativeType, a.kind.NativeType) {
		return nil, serviceDenied("service_parent_identity_changed")
	}
	if fleetKind(root.Identity.NativeType).kind != "" {
		if _, err := a.client.fleetRecordedReferences(root); err != nil {
			return nil, err
		}
	}
	impacts := map[string]contracts.ActionImpact{}
	assets := map[asset.AssetID]asset.Asset{root.ID: root}
	for _, impact := range request.LifecycleImpacts {
		identity := impact.Asset.Identity
		if fleetKind(identity.NativeType).kind != "" {
			if _, err := a.client.fleetRecordedReferences(impact.Asset); err != nil {
				return nil, err
			}
		}
		id, kind, err := parseID(identity.NativeID)
		if err != nil || impact.Asset.ID == "" || assets[impact.Asset.ID].Identity.NativeID != "" || impacts[id].Asset.Identity.NativeID != "" || identity.Provider != asset.ProviderAzure || identity.ConnectionID != root.Identity.ConnectionID || identity.Partition != root.Identity.Partition || !strings.EqualFold(kind, identity.NativeType) || !strings.HasPrefix(id, a.client.root()+"/") {
			return nil, serviceDenied("invalid_service_lifecycle_impact")
		}
		impacts[id] = impact
		assets[impact.Asset.ID] = impact.Asset
	}
	if recoveryType(root.Identity.NativeType) && text(root.Normalized["_recovery_peer_alias"]) != "" {
		peer, exists := impacts[text(root.Normalized["_recovery_peer_alias"])]
		if !exists || peer.ControllerID != root.ID || !recoveryPeerRelation(root, peer.Asset) {
			return nil, serviceDenied("recovery_peer_missing_from_plan")
		}
	}
	if a.kind.NativeType == vmType {
		attachments, err := plannedAttachmentImpacts(a.client.subscription, root, impacts, assets)
		if err != nil {
			return nil, err
		}
		for id := range attachments {
			delete(impacts, id)
		}
	}
	for _, impact := range impacts {
		if !impact.Delete {
			return nil, serviceDenied("service_child_retention_not_supported")
		}
		current := impact
		seen := map[asset.AssetID]bool{}
		for {
			if seen[current.Asset.ID] {
				return nil, serviceDenied("service_child_scope_changed")
			}
			seen[current.Asset.ID] = true
			parent, ok := assets[current.ControllerID]
			kinds := serviceChildKinds(parent.Identity.NativeType)
			if !ok || ((!slices.ContainsFunc(kinds, func(kind string) bool { return strings.EqualFold(kind, current.Asset.Identity.NativeType) }) || !serviceChildRelation(parent, current.Asset)) && !recoveryPeerRelation(parent, current.Asset)) {
				return nil, serviceDenied("service_child_scope_changed")
			}
			if parent.ID == root.ID {
				break
			}
			current = impacts[strings.ToLower(parent.Identity.NativeID)]
		}
	}
	return impacts, nil
}

func (a *action) serviceCascadePreflight(ctx context.Context, request contracts.ActionRequest, live map[string]any, locks []any) error {
	return a.serviceCascadePreflightWithPending(ctx, request, live, locks, nil)
}

func (a *action) serviceCascadePreflightWithPending(ctx context.Context, request contracts.ActionRequest, live map[string]any, locks []any, pending map[asset.AssetID][]asset.AssetID) error {
	return a.serviceCascadePreflightWithManagedGroup(ctx, request, live, locks, pending, nil)
}

func (a *action) serviceCascadePreflightWithManagedGroup(ctx context.Context, request contracts.ActionRequest, live map[string]any, locks []any, pending map[asset.AssetID][]asset.AssetID, managed *resourceGroupManagedPreflight) error {
	if a.kind.NativeType == monitorWorkspaceType {
		return a.monitorWorkspacePreflight(ctx, request, live, locks)
	}
	if a.kind.NativeType == eventHubClusterType {
		if err := a.client.verifyEventHubClusterSettings(ctx, request.Asset); err != nil {
			return err
		}
	}
	if a.kind.NativeType == serviceBusNamespaceType {
		incoming, err := a.client.incomingMigrations(ctx)
		if err != nil {
			return err
		}
		if len(incoming[a.id]) > 0 {
			return serviceDenied("incoming_migration_requires_prior_deletion")
		}
	}
	impacts, err := a.serviceImpacts(request)
	if err != nil {
		return err
	}
	planned := request.Asset
	if err := a.servicePrerequisitesAbsent(ctx, request); err != nil {
		return err
	}
	if len(request.PrerequisiteDeletions) > 0 && (serviceParentConfigurationMatches(planned, live) || batchScaleSetConfigurationMatches(request, live)) {
		planned.Normalized = cloneNormalizedWithoutGeneration(planned.Normalized)
	}
	if a.kind.NativeType == vmType {
		retained, err := vmRetentionApplied(a.client.subscription, request, object(live["properties"]))
		if err != nil {
			return err
		}
		if retained {
			// Our reviewed Delete -> Detach update changes the VM ETag. Keep
			// checking its stable VM ID and every live child/attachment policy.
			planned.Normalized = map[string]any{}
			for key, value := range request.Asset.Normalized {
				if key != "_arm_generation" {
					planned.Normalized[key] = value
				}
			}
		}
	}
	if err := serviceIncarnation(planned, live); err != nil {
		return err
	}
	visited := map[string]bool{}
	verifiedGroups := map[string]map[string]any{}
	var verify func(asset.Asset, map[string]any) error
	verify = func(parent asset.Asset, raw map[string]any) error {
		var known []asset.Asset
		if fleetKind(parent.Identity.NativeType).kind != "" {
			for _, impact := range impacts {
				known = append(known, impact.Asset)
			}
		}
		children, err := a.client.plannedServiceChildren(ctx, parent, raw, known...)
		if err != nil {
			return err
		}
		for _, child := range children {
			impact, ok := impacts[child.id]
			if child.direct && !slices.Contains(pending[parent.ID], impact.Asset.ID) {
				return serviceDenied("service_child_requires_prior_deletion")
			}
			if !ok || impact.ControllerID != parent.ID || !strings.EqualFold(impact.Asset.Identity.NativeType, child.kind) {
				return serviceDenied("service_child_missing_from_plan")
			}
			visited[child.id] = true
			if err := a.client.servicePrivateIncarnation(impact.Asset, child.data); err != nil {
				return err
			}
			if fleetKind(child.kind).kind != "" {
				if err := a.client.fleetContext(ctx, impact.Asset, child.data); err != nil {
					return err
				}
			}
			if err := serviceIncarnation(impact.Asset, child.data); err != nil {
				return err
			}
			if err := a.client.apimVerifyTarget(ctx, impact.Asset, child.data, locks); err != nil {
				return err
			}
			kind, _ := findType(child.kind)
			if child.kind == cognitiveOutboundType {
				childAction := action{client: a.client, kind: kind, id: child.id}
				if err := childAction.cognitiveResourcePreflight(ctx, impact.Asset, child.data, false); err != nil {
					return err
				}
			}
			groupID := strings.Join(strings.Split(child.id, "/")[:5], "/")
			group := verifiedGroups[groupID]
			if group == nil {
				current, err := a.client.request(ctx, "GET", apiURL(groupID, resourcesVersion))
				if err != nil {
					return err
				}
				if !validResourceResponse(current, groupID, groupType) {
					return fmt.Errorf("Azure cascade child group identity mismatch")
				}
				group = current.data
				verifiedGroups[groupID] = group
			}
			if text(group["managedBy"]) != "" && !managed.permits(impact.Asset, group) {
				return serviceDenied("azure_managed_resource_group")
			}
			if locked(child.id, locks) {
				return serviceDenied("azure_management_lock")
			}
			if reason := protectionReason(kind, child.data); reason != "" && !serviceIntrinsicChild(parent.Identity.NativeType, child.kind, reason) {
				return serviceDenied(reason)
			}
			for key, value := range object(child.data["tags"]) {
				if strings.EqualFold(key, "steward/protected") || strings.EqualFold(key, "steward:protected") {
					switch strings.ToLower(text(value)) {
					case "1", "true", "yes", "on", "protected":
						return serviceDenied("service_child_protected")
					}
				}
			}
			if err := verify(impact.Asset, child.data); err != nil {
				return err
			}
		}
		return nil
	}
	if err := verify(request.Asset, live); err != nil {
		return err
	}
	for id, impact := range impacts {
		if visited[id] {
			continue
		}
		endpoint, err := a.client.plannedResourceURL(impact.Asset)
		if err != nil {
			return err
		}
		if _, err = a.client.readResource(ctx, endpoint); !isNotFound(err) {
			if err != nil {
				return err
			}
			return serviceDenied("service_child_membership_changed")
		}
	}
	return nil
}

func (a *action) serviceCascadeReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if a.kind.NativeType == redisLinkType || a.kind.NativeType == redisDatabaseType {
		done, err := a.redisReplicationReadback(ctx, request.Asset)
		if err != nil || !done {
			return contracts.ReadbackResult{Exists: true, State: "redis_replication_unlinking"}, err
		}
	}
	if a.kind.NativeType == monitorWorkspaceType {
		return a.monitorWorkspaceReadback(ctx, request)
	}
	impacts, err := a.serviceImpacts(request)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.servicePrerequisitesAbsent(ctx, request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	ids := make([]string, 0, len(impacts))
	for id := range impacts {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		impact := impacts[id]
		kind, _ := findType(impact.Asset.Identity.NativeType)
		endpoint, err := a.client.plannedResourceURL(impact.Asset)
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		current, err := a.client.readResource(ctx, endpoint)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		if !validResourceResponse(current, id, kind.NativeType) {
			return contracts.ReadbackResult{}, fmt.Errorf("Azure service child readback identity mismatch")
		}
		return contracts.ReadbackResult{Exists: true, State: "service_children_deleting"}, nil
	}
	return contracts.ReadbackResult{Exists: false}, nil
}

// The master database is part of the server's native lifetime. Its restriction
// prohibits direct DELETE, while a reviewed server deletion can remove it.
func serviceIntrinsicChild(parent, child, reason string) bool {
	if parent == fleetRunType && child == fleetGateType && reason == "azure_fleet_gate_requires_update_run" {
		return true
	}
	if isAPIMType(parent) && isAPIMType(child) && strings.HasPrefix(reason, "azure_apim_") && controllerOnlyReason(reason) && strings.EqualFold(parent, child[:strings.LastIndex(child, "/")]) {
		return true
	}
	if parent == streamAnalyticsJobType && child == streamAnalyticsTransformationType && reason == "azure_stream_analytics_transformation" {
		return true
	}
	if parent == kustoAttachmentType && child == kustoDatabaseType && reason == "azure_kusto_following_database" {
		return true
	}
	return cosmosIntrinsicChild(parent, child, reason) || (isCognitiveType(parent) && isCognitiveType(child) && reason == "azure_cognitive_managed_configuration") || (parent == searchType && child == searchPerimeterType && reason == "azure_search_managed_configuration") || (parent == redisType && child == redisPolicyType && reason == "azure_redis_builtin_policy") ||
		(parent == redisLinkType && child == redisLinkType && reason == "azure_redis_secondary_link") ||
		(recoveryType(parent) && parent == child && reason == "azure_messaging_recovery_secondary") ||
		((parent == serviceBusNamespaceType || parent == eventHubNamespaceType) && child == parent+"/authorizationRules" && reason == "azure_messaging_default_authorization_rule") ||
		(messagingManagedConfiguration(child) && strings.EqualFold(parent, child[:strings.LastIndex(child, "/")]) && reason == "azure_messaging_managed_configuration") ||
		(strings.EqualFold(parent, vpnConnectionType) && strings.EqualFold(child, vpnLinkConnectionType) && reason == "azure_vpn_connection_managed_link") ||
		(strings.EqualFold(parent, scaleSetVMType) && strings.EqualFold(child, diskType) && reason == "azure_managed_resource") ||
		((strings.EqualFold(child, scaleSetNICType) || strings.EqualFold(child, scaleSetIPConfigType) || strings.EqualFold(child, scaleSetPublicIPType)) && strings.EqualFold(parent, child[:strings.LastIndex(child, "/")]) && reason == "azure_scale_set_managed_network") ||
		(strings.EqualFold(parent, privateEndpointType) && strings.EqualFold(child, nicType) && reason == "azure_private_endpoint_managed_nic") ||
		(strings.EqualFold(parent, sqlServerType) && strings.EqualFold(child, sqlDatabaseType) && reason == "azure_system_database") ||
		(isDNSZoneType(parent) && isDNSRecordType(child) && reason == "azure_dns_system_record") ||
		((strings.EqualFold(parent, privateDNSLinkType) || strings.EqualFold(parent, privateDNSZoneType)) && strings.HasPrefix(strings.ToLower(child), strings.ToLower(privateDNSZoneType)+"/") && reason == "azure_dns_auto_registered_record")
}

func (c *client) serviceChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	if !HasServiceCascade(parent.NativeType) {
		return nil, nil
	}
	var children []serviceChild
	var err error
	native := false
	switch {
	case parent.NativeType == domainType:
		native = true
		children, err = c.domainChildren(ctx, parent, raw, nil)
	case fleetKind(parent.NativeType).kind != "":
		native = true
		children, err = c.fleetChildren(ctx, parent, raw)
	case isAPIMType(parent.NativeType):
		native = true
		children, err = c.apimChildren(ctx, parent, raw)
	case isCosmosType(parent.NativeType):
		native = true // The Cosmos walk validates both complete native reads.
		children, err = c.cosmosChildren(ctx, parent, raw)
	case isStreamAnalyticsType(parent.NativeType):
		native = true
		children, err = c.streamAnalyticsChildren(ctx, parent, raw)
	case isKustoType(parent.NativeType):
		native = true
		children, err = c.kustoChildren(ctx, parent, raw)
	case parent.NativeType == mongoClusterType:
		native = true
		children, err = c.mongoClusterChildren(ctx, parent, raw)
	case isCognitiveType(parent.NativeType):
		children, err = c.cognitiveChildren(ctx, parent, raw)
	case parent.NativeType == searchType:
		children, err = c.searchChildren(ctx, parent, raw)
	case isRedisType(parent.NativeType):
		children, err = c.redisChildren(ctx, parent, raw)
	case parent.NativeType == appSiteType || parent.NativeType == appSlotType:
		children, err = c.appServiceChildren(ctx, parent, raw)
	case isCDNType(parent.NativeType):
		children, err = c.cdnChildren(ctx, parent, raw)
	case strings.EqualFold(parent.NativeType, monitorWorkspaceType):
		resources, links, failure := c.monitorWorkspaceResources(ctx, asset.Asset{Identity: parent}, raw)
		if failure != nil {
			return nil, failure
		}
		for _, resource := range resources {
			id, kind, _ := parseID(text(resource["id"]))
			children = append(children, serviceChild{kind: kind, id: id, data: resource})
		}
		children = append(children, links...)
	case isDataCollectionType(parent.NativeType):
		children, err = c.dataCollectionAssociations(ctx, parent)
	case strings.EqualFold(parent.NativeType, monitorPrivateLinkType):
		children, err = c.monitorPrivateLinkChildren(ctx, parent, raw)
	case strings.EqualFold(parent.NativeType, grafanaType):
		children, err = c.grafanaChildren(ctx, parent, raw)
	case strings.EqualFold(parent.NativeType, eventHubClusterType):
		children, err = c.eventHubClusterNamespaces(ctx, parent)
	case strings.EqualFold(parent.NativeType, scaleSetType):
		children, err = c.scaleSetChildren(ctx, parent, raw)
	case strings.EqualFold(parent.NativeType, scaleSetVMType):
		children, err = c.uniformVMChildren(ctx, parent, raw)
	case strings.EqualFold(parent.NativeType, privateEndpointType):
		children, err = c.privateEndpointChildren(ctx, parent, raw)
	case strings.EqualFold(parent.NativeType, privateDNSZoneGroupType):
		children, err = c.privateDNSGroupChildren(ctx, parent, raw)
	case strings.EqualFold(parent.NativeType, privateDNSLinkType):
		children, err = c.privateDNSRegistrationChildren(ctx, parent, raw)
	default:
		native = true
		children, err = c.nativeServiceChildren(ctx, parent, raw, serviceChildKinds(parent.NativeType))
	}
	if err != nil {
		return nil, err
	}
	for i := range children {
		children[i].direct = children[i].direct || servicePrerequisiteKind(parent.NativeType, children[i].kind)
		if parent.NativeType == kustoType && children[i].kind == kustoImageType {
			active, err := kustoActiveImage(raw, last(children[i].id))
			if err != nil {
				return nil, err
			}
			if active {
				children[i].direct = false
			}
		}
		if isCosmosType(parent.NativeType) && cosmosIntrinsicChild(parent.NativeType, children[i].kind, cosmosProtection(children[i].kind, children[i].data)) {
			children[i].direct = false
		}
		if isCognitiveType(parent.NativeType) && cognitiveProtection(children[i].kind, children[i].data) == "azure_cognitive_managed_configuration" {
			children[i].direct = false
		}
		if parent.NativeType == redisType && children[i].kind == redisPolicyType && object(children[i].data["properties"])["type"] == "BuiltIn" {
			children[i].direct = false
		}
		if recoveryType(children[i].kind) {
			children[i].direct = !recoveryUnpaired(object(children[i].data["properties"]))
		}
		if children[i].kind == serviceBusMigrationType {
			// A paired migration must first be aborted by its own persisted
			// action. Namespace DELETE cannot stand in for native Revert.
			children[i].direct = text(object(children[i].data["properties"])["targetNamespace"]) != ""
		}
	}
	if native {
		return children, nil // nativeServiceChildren already re-read the parent.
	}
	if err := c.verifyProductParent(ctx, productTarget{ParentID: parent.NativeID, ParentType: parent.NativeType, Generation: productGeneration(raw)}); err != nil {
		return nil, err
	}
	sort.Slice(children, func(i, j int) bool { return children[i].id < children[j].id })
	return children, nil
}

func serviceChildRelation(parent, child asset.Asset) bool {
	if domainPrerequisite(parent, child) {
		return true
	}
	if fleetKind(parent.Identity.NativeType).kind != "" {
		return fleetChildRelation(parent, child)
	}
	if isAPIMType(parent.Identity.NativeType) {
		return slices.Contains(apimOwnedKinds(parent.Identity.NativeType), child.Identity.NativeType) && strings.EqualFold(redisParentID(child.Identity.NativeID), parent.Identity.NativeID)
	}
	if isStreamAnalyticsType(parent.Identity.NativeType) {
		return streamAnalyticsClusterPrerequisite(parent, child) || slices.Contains(streamAnalyticsOwnedKinds(parent.Identity.NativeType), child.Identity.NativeType) && strings.EqualFold(redisParentID(child.Identity.NativeID), parent.Identity.NativeID)
	}
	if isKustoType(parent.Identity.NativeType) {
		return kustoSharedPrerequisite(parent, child) || kustoControlledDatabase(parent, child) || slices.Contains(kustoOwnedKinds(parent.Identity.NativeType), child.Identity.NativeType) && strings.EqualFold(redisParentID(child.Identity.NativeID), parent.Identity.NativeID)
	}
	if parent.Identity.NativeType == mongoClusterType {
		return mongoClusterReplicaPrerequisite(parent, child) || (slices.Contains(mongoClusterOwnedKinds(), child.Identity.NativeType) && strings.EqualFold(redisParentID(child.Identity.NativeID), parent.Identity.NativeID))
	}
	if isCosmosType(parent.Identity.NativeType) {
		if cosmosSharedPrerequisite(parent, child) {
			return true
		}
		return slices.Contains(cosmosOwnedKinds(parent.Identity.NativeType), cosmosKind(child.Identity.NativeType)) && cosmosSameWireID(text(parent.Normalized["_cosmos_wire_id"]), cosmosParentID(text(child.Normalized["_cosmos_wire_id"])))
	}
	if cognitiveSharedPrerequisite(parent, child) {
		return true
	}
	if redisSharedPrerequisite(parent, child) || redisLinkPeerRelation(parent, child) {
		return true
	}
	if parent.Identity.NativeType == redisLinkType {
		return false
	}
	if monitorPrivateLinkPrerequisite(parent, child) {
		return true
	}
	if monitorWorkspaceAssociation(parent, child) {
		return true
	}
	if recoveryPeerRelation(parent, child) {
		return true
	}
	switch {
	case isDataCollectionType(parent.Identity.NativeType) && strings.EqualFold(child.Identity.NativeType, dataCollectionAssociationType):
		return dataCollectionAssociationMembership(map[string]any{"id": child.Identity.NativeID, "properties": child.Normalized}, parent.Identity.NativeID) == nil
	case strings.EqualFold(parent.Identity.NativeType, eventHubClusterType):
		return strings.EqualFold(child.Identity.NativeType, eventHubNamespaceType) && eventHubClusterReference(child.Normalized["clusterArmId"], parent.Identity.NativeID)
	case strings.EqualFold(parent.Identity.NativeType, scaleSetVMType) && strings.EqualFold(child.Identity.NativeType, diskType):
		return uniformVMDiskRelation(parent, child)
	case strings.EqualFold(parent.Identity.NativeType, scaleSetType) && strings.EqualFold(child.Identity.NativeType, vmType):
		mode, err := scaleSetMode(parent.Normalized)
		return err == nil && mode == "Flexible" && strings.EqualFold(text(object(child.Normalized["virtualMachineScaleSet"])["id"]), parent.Identity.NativeID)
	case strings.EqualFold(parent.Identity.NativeType, privateEndpointType) && strings.EqualFold(child.Identity.NativeType, nicType):
		return privateEndpointNICRelation(parent, child)
	case strings.EqualFold(parent.Identity.NativeType, privateDNSZoneGroupType):
		records, err := privateDNSGroupRecords(parent.Normalized)
		if err != nil {
			return false
		}
		return slices.ContainsFunc(records, func(record dnsGroupRecord) bool {
			return strings.EqualFold(record.id, child.Identity.NativeID) && strings.EqualFold(record.kind, child.Identity.NativeType)
		})
	case strings.EqualFold(parent.Identity.NativeType, privateDNSLinkType):
		zone := strings.Join(strings.Split(strings.ToLower(parent.Identity.NativeID), "/")[:9], "/")
		return parent.Normalized["registrationEnabled"] == true && child.Normalized["isAutoRegistered"] == true && strings.HasPrefix(strings.ToLower(child.Identity.NativeID), zone+"/")
	default:
		return strings.HasPrefix(strings.ToLower(child.Identity.NativeID), strings.ToLower(parent.Identity.NativeID)+"/")
	}
}
