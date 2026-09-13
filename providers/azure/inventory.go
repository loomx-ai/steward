package azure

import (
	"context"
	"encoding/base64"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (r *Runtime) List(ctx context.Context, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	if request.Source != "" && request.Source != inventorySource && request.Source != productInventorySource && request.Source != insightsAnnotationSource && request.Source != insightsWorkbookSource && request.Source != diagnosticInventorySource && request.Source != fleetInventorySource && request.Source != communicationInventorySource && request.Source != dataFactoryInventorySource && request.Source != dataMigrationInventorySource && request.Source != defenderInventorySource && request.Source != hybridComputeSource {
		return contracts.InventoryBatch{}, fmt.Errorf("unsupported Azure inventory source")
	}
	hybrid := request.ResourceKind != nil && hybridComputeKind(request.ResourceKind.NativeType) != ""
	if request.Source == hybridComputeSource && !hybrid || hybrid && request.Source != "" && request.Source != inventorySource && request.Source != hybridComputeSource {
		return contracts.InventoryBatch{}, serviceDenied("invalid_hybrid_compute_inventory_source")
	}
	defender := request.ResourceKind != nil && strings.EqualFold(request.ResourceKind.NativeType, defenderPricingType)
	if request.Source == defenderInventorySource && !defender || defender && request.Source != "" && request.Source != inventorySource && request.Source != defenderInventorySource {
		return contracts.InventoryBatch{}, serviceDenied("invalid_defender_inventory_source")
	}
	datamigration := request.ResourceKind != nil && dataMigrationKind(request.ResourceKind.NativeType) != ""
	if request.Source == dataMigrationInventorySource && !datamigration || datamigration && request.Source != "" && request.Source != inventorySource && request.Source != dataMigrationInventorySource {
		return contracts.InventoryBatch{}, serviceDenied("invalid_datamigration_inventory_source")
	}
	datafactory := request.ResourceKind != nil && dataFactoryKind(request.ResourceKind.NativeType) != ""
	if request.Source == dataFactoryInventorySource && !datafactory || datafactory && request.Source != "" && request.Source != inventorySource && request.Source != dataFactoryInventorySource {
		return contracts.InventoryBatch{}, serviceDenied("invalid_datafactory_inventory_source")
	}
	communication := request.ResourceKind != nil && communicationKind(request.ResourceKind.NativeType) != ""
	if request.Source == communicationInventorySource && !communication || communication && request.Source != "" && request.Source != inventorySource && request.Source != communicationInventorySource {
		return contracts.InventoryBatch{}, serviceDenied("invalid_communication_inventory_source")
	}
	fleet := request.ResourceKind != nil && fleetKind(request.ResourceKind.NativeType).kind != ""
	if request.Source == fleetInventorySource && !fleet || fleet && request.Source != "" && request.Source != inventorySource && request.Source != fleetInventorySource {
		return contracts.InventoryBatch{}, serviceDenied("invalid_fleet_inventory_source")
	}
	diagnostic := request.ResourceKind != nil && strings.EqualFold(request.ResourceKind.NativeType, diagnosticSettingsType)
	if request.Source == diagnosticInventorySource && !diagnostic || diagnostic && request.Source != "" && request.Source != inventorySource && request.Source != diagnosticInventorySource {
		return contracts.InventoryBatch{}, serviceDenied("invalid_diagnostic_inventory_source")
	}
	annotation := request.ResourceKind != nil && strings.EqualFold(request.ResourceKind.NativeType, insightsAnnotationType)
	if request.Source == insightsAnnotationSource && !annotation || annotation && request.Source == productInventorySource {
		return contracts.InventoryBatch{}, serviceDenied("invalid_insights_annotation_inventory_source")
	}
	workbook := request.ResourceKind != nil && insightsWorkbookKind(request.ResourceKind.NativeType) != ""
	if request.Source == insightsWorkbookSource && (!workbook || insightsInventorySource(request.ResourceKind.NativeType) != request.Source) {
		return contracts.InventoryBatch{}, serviceDenied("invalid_insights_workbook_source")
	}
	c, err := r.resolve(ctx, request.ConnectionID)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	if request.Scope.Kind == asset.ScopeSubscription && !strings.EqualFold(request.Scope.NativeID, c.subscription) {
		return contracts.InventoryBatch{}, fmt.Errorf("Azure inventory scope belongs to another subscription")
	}
	if hybrid {
		if request.Source == inventorySource {
			return contracts.InventoryBatch{Complete: true}, nil
		}
		request.Source = hybridComputeSource
		return r.listHybridCompute(ctx, c, request)
	}
	if datamigration {
		if request.Source == inventorySource {
			return contracts.InventoryBatch{Complete: true}, nil
		}
		request.Source = dataMigrationInventorySource
		return r.listDataMigration(ctx, c, request)
	}
	if defender {
		if request.Source == inventorySource {
			return contracts.InventoryBatch{Complete: true}, nil
		}
		request.Source = defenderInventorySource
		return r.listDefender(ctx, c, request)
	}
	if datafactory {
		if request.Source == inventorySource {
			return contracts.InventoryBatch{Complete: true}, nil
		}
		request.Source = dataFactoryInventorySource
		return r.listDataFactory(ctx, c, request)
	}
	if communication {
		if request.Source == inventorySource {
			return contracts.InventoryBatch{Complete: true}, nil
		}
		request.Source = communicationInventorySource
		return r.listCommunication(ctx, c, request)
	}
	if fleet {
		if request.Source == inventorySource {
			return contracts.InventoryBatch{Complete: true}, nil
		}
		request.Source = fleetInventorySource
		return r.listFleet(ctx, c, request)
	}
	if workbook {
		if request.Source == inventorySource {
			return contracts.InventoryBatch{Complete: true}, nil
		}
		if request.Source == "" {
			request.Source = insightsInventorySource(request.ResourceKind.NativeType)
		}
		return r.listInsightsWorkbooks(ctx, c, request)
	}
	if diagnostic {
		if request.Source == inventorySource {
			return contracts.InventoryBatch{Complete: true}, nil
		}
		request.Source = diagnosticInventorySource
		return r.listDiagnosticSettings(ctx, c, request)
	}
	if request.ResourceKind != nil && rbacResourceKind(request.ResourceKind.NativeType) != "" {
		if request.Source == inventorySource {
			return contracts.InventoryBatch{Complete: true}, nil
		}
		if request.Source != "" && request.Source != productInventorySource {
			return contracts.InventoryBatch{}, serviceDenied("invalid_rbac_inventory_source")
		}
		request.Source = productInventorySource
		return r.listRBAC(ctx, c, request)
	}
	if request.ResourceKind != nil && monitorResourceKind(request.ResourceKind.NativeType) != "" {
		if request.Source == inventorySource {
			return contracts.InventoryBatch{Complete: true}, nil
		}
		if request.Source == "" {
			request.Source = productInventorySource
		}
		return r.listMonitorResources(ctx, c, request)
	}
	if request.ResourceKind != nil && (strings.EqualFold(request.ResourceKind.NativeType, applicationInsightsType) || insightsLegacyKind(request.ResourceKind.NativeType).kind != "" || insightsARMChildKind(request.ResourceKind.NativeType) != "") {
		if request.Source == inventorySource {
			return contracts.InventoryBatch{Complete: true}, nil
		}
		request.Source = insightsInventorySource(request.ResourceKind.NativeType)
		return r.listInsights(ctx, c, request)
	}
	if request.ResourceKind != nil && isBatchDataType(request.ResourceKind.NativeType) {
		if request.Source == inventorySource {
			return contracts.InventoryBatch{Complete: true}, nil
		}
		request.Source = productInventorySource
		return r.listBatchData(ctx, c, request)
	}
	if request.Source == productInventorySource {
		return r.listProduct(ctx, c, request, nil)
	}
	path := c.root() + "/resources"
	endpoint := apiURL(path, resourcesVersion)
	if request.Cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || len(decoded) > 16<<10 {
			return contracts.InventoryBatch{}, fmt.Errorf("invalid Azure inventory cursor")
		}
		endpoint = string(decoded)
	}
	u, parseErr := url.Parse(endpoint)
	if parseErr != nil || u.Query().Get("api-version") != resourcesVersion {
		return contracts.InventoryBatch{}, fmt.Errorf("Azure inventory cursor changed API version")
	}
	values, next, provenance, err := c.listPageResult(ctx, endpoint, path)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	groups, err := c.listAll(ctx, c.root()+"/resourcegroups", resourcesVersion)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	groupOwners := map[string]string{}
	for _, value := range groups {
		group := object(value)
		id, _, err := parseID(text(group["id"]))
		if err != nil || !strings.HasPrefix(id, c.root()+"/") {
			return contracts.InventoryBatch{}, fmt.Errorf("invalid Azure resource group")
		}
		groupOwners[id] = text(group["managedBy"])
	}
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{}, Complete: next == "", RequestID: provenance.requestID}
	if next != "" {
		batch.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(next))
	}
	region := strings.ToLower(request.Scope.NativeID)
	if request.Scope.Kind == asset.ScopeGlobal {
		region = "global"
	}
	if request.Scope.Kind == asset.ScopeSubscription {
		if !strings.EqualFold(request.Scope.NativeID, c.subscription) {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure inventory scope belongs to another subscription")
		}
		region = ""
	}
	seen := map[string]bool{}
	appendItem := func(raw map[string]any) error {
		if request.Source == inventorySource && r.usesProductSource(text(raw["type"])) {
			return nil
		}
		item, err := r.inventoryItem(ctx, c, raw, groupOwners, locks)
		if err != nil {
			return err
		}
		if request.ResourceKind != nil && !strings.EqualFold(item.NativeType, request.ResourceKind.NativeType) {
			return nil
		}
		if !seen[item.NativeID] {
			batch.Items = append(batch.Items, item)
			seen[item.NativeID] = true
		}
		return nil
	}
	if (region == "global" || region == "") && request.Cursor == "" {
		for _, value := range groups {
			raw := object(value)
			raw["type"] = groupType
			if err := appendItem(raw); err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
	}
	for _, value := range values {
		raw := object(value)
		if raw == nil {
			return contracts.InventoryBatch{}, fmt.Errorf("invalid Azure resource list item")
		}
		if region != "" && resourceRegion(raw) != region && !(request.NetworkTarget != nil && resourceRegion(raw) == "global") {
			continue
		}
		kind, known := findType(text(raw["type"]))
		if request.Source == inventorySource && known {
			continue
		}
		if known {
			resourceURL, err := c.resourceURL(kind, text(raw["id"]))
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			detail, err := c.request(ctx, "GET", resourceURL)
			if isNotFound(err) {
				continue
			} // Resource removed after the list snapshot.
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			if !validResourceResponse(detail, text(raw["id"]), kind.NativeType) {
				return contracts.InventoryBatch{}, fmt.Errorf("Azure resource detail identity mismatch")
			}
			raw = detail.data
			raw["id"] = responseID(kind.NativeType, text(raw["id"]))
			raw["type"] = kind.NativeType
			if text(raw["location"]) == "" && !isCosmosType(kind.NativeType) {
				raw["location"] = resourceRegion(object(value))
			}
		}
		if err := appendItem(raw); err != nil {
			return contracts.InventoryBatch{}, err
		}
		// ARM's subscription list omits some child resources. Enumerate the
		// children whose lifecycle and dependencies Steward models explicitly.
		children, err := c.children(ctx, kind, raw)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		for _, child := range children {
			if err := appendItem(child); err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
	}
	return batch, nil
}
func resourceRegion(raw map[string]any) string {
	if kind := cosmosKind(text(raw["type"])); kind != "" {
		return cosmosRegion(kind, raw)
	}
	if strings.EqualFold(text(raw["type"]), groupType) {
		return "global"
	}
	// ARM documents can return either the canonical location or its spaced
	// display form (for example the official VM Get example uses "West US").
	location := strings.ReplaceAll(strings.ToLower(text(raw["location"])), " ", "")
	if location == "" {
		return "global"
	}
	return location
}
func (c *client) children(ctx context.Context, kind resourceType, raw map[string]any) ([]map[string]any, error) {
	id, _, err := parseID(text(raw["id"]))
	if err != nil {
		return nil, err
	}
	var values []any
	verifiedChildren := map[string]bool{}
	if HasServiceCascade(kind.NativeType) {
		endpoint, err := c.resourceURL(kind, responseID(kind.NativeType, text(raw["id"])))
		if err != nil {
			return nil, err
		}
		current, err := c.request(ctx, "GET", endpoint)
		if err != nil {
			return nil, err
		}
		if !validResourceResponse(current, id, kind.NativeType) {
			return nil, fmt.Errorf("Azure service parent identity mismatch")
		}
		if err := serviceListedIncarnation(raw, current.data); err != nil {
			return nil, err
		}
		children, err := c.serviceChildren(ctx, asset.Identity{NativeType: kind.NativeType, NativeID: id}, current.data)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			values = append(values, child.data)
			verifiedChildren[child.id] = true
		}
	}
	switch kind.NativeType {
	case privateDNSZoneType:
		links, err := c.privateDNSLinks(ctx, id, raw)
		if err != nil {
			return nil, err
		}
		for _, link := range links {
			values = append(values, link.data)
		}
	case vnetType:
		values, err = c.listAll(ctx, id+"/subnets", kind.Version)
	case storageType:
		values, err = c.listAll(ctx, id+"/blobServices/default/containers", kind.Version)

	}
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for _, value := range values {
		child := object(value)
		childID, childType, err := parseID(text(child["id"]))
		if err != nil || (!strings.HasPrefix(childID, id+"/") && !verifiedChildren[childID]) {
			return nil, fmt.Errorf("Azure child belongs to another parent")
		}
		known, ok := findType(childType)
		if !ok {
			return nil, fmt.Errorf("unexpected Azure child resource type")
		}
		child["type"] = known.NativeType
		if text(child["location"]) == "" && !isCosmosType(known.NativeType) {
			child["location"] = resourceRegion(raw)
		}
		result = append(result, child)
	}
	return result, nil
}
func (r *Runtime) inventoryItem(ctx context.Context, c *client, raw map[string]any, groupOwners map[string]string, locks []any) (contracts.InventoryItem, error) {
	if fleetKind(text(raw["type"])).kind != "" {
		return r.fleetObservedInventoryItem(ctx, c, raw, groupOwners, locks)
	}
	if mapping, known := findType(text(raw["type"])); known && monitorResourceKind(mapping.NativeType) != "" {
		return r.monitorInventoryItem(ctx, c, raw, groupOwners, locks)
	}
	id, parsedType, err := parseID(text(raw["id"]))
	if err != nil || !strings.HasPrefix(id, c.root()+"/") || !strings.EqualFold(parsedType, text(raw["type"])) {
		return contracts.InventoryItem{}, fmt.Errorf("Azure inventory identity mismatch")
	}
	nativeType := parsedType
	kind, known := findType(nativeType)
	if known {
		nativeType = kind.NativeType
	}
	region := resourceRegion(raw)
	scope := contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: region, Name: region, Location: region}
	if region == "global" {
		scope = contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: c.subscription + "/global", Name: "Global", Location: "global"}
	}
	safe := safePayload(raw)
	normalized := map[string]any{}
	for key, value := range object(safe["properties"]) {
		normalized[key] = value
	}
	for _, key := range []string{"sku", "kind", "zones", "managedBy", "instanceId"} {
		if value, ok := safe[key]; ok {
			normalized[key] = value
		}
	}
	parts := strings.Split(id, "/")
	groupID := strings.Join(parts[:5], "/")
	normalized["subscription_id"] = c.subscription
	normalized["name"] = safe["name"]
	if isDNSRecordType(nativeType) {
		tags := map[string]any{}
		for key, value := range object(object(safe["properties"])["metadata"]) {
			tags[key] = value
		}
		for key, value := range object(safe["tags"]) {
			tags[key] = value
		}
		safe["tags"] = tags
	}
	normalized["tags"] = safe["tags"]
	normalized["resource_group"] = parts[4]
	normalized["_inventory_source"] = inventorySource
	normalized["_arm_generation"] = productGeneration(raw)
	if creation := creationGeneration(raw); creation != "" {
		normalized["_arm_creation_generation"] = creation
	}
	normalized["arm_etag"] = text(raw["etag"])
	if err := c.workbookInventory(ctx, id, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	if err := c.apimInventory(ctx, id, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	if location := text(normalized["_apim_location"]); location != "" {
		region = location
		scope = contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: region, Name: region, Location: region}
	}
	if isCosmosType(nativeType) {
		wire := responseID(nativeType, text(raw["id"]))
		normalized["_cosmos_wire_id"] = wire
		normalized["_cosmos_wire_binding"] = c.cosmosWireBinding(nativeType, wire)
	}
	if err := c.cosmosInventory(ctx, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	if err := c.batchInventory(ctx, id, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	if location := text(normalized["_batch_location"]); location != "" {
		region = location
		scope = contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: region, Name: region, Location: region}
	}
	if err := c.streamAnalyticsInventory(ctx, id, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	if location := text(normalized["_stream_analytics_location"]); location != "" {
		region = location
		scope = contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: region, Name: region, Location: region}
	}
	if err := c.kustoInventory(ctx, id, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	if location := text(normalized["_kusto_location"]); location != "" {
		region = location
		scope = contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: region, Name: region, Location: region}
	}
	if err := c.mongoClusterInventory(ctx, id, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	if err := c.cognitiveInventory(ctx, id, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	if err := c.searchInventory(ctx, id, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	if err := c.redisInventory(ctx, id, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	if err := c.appServiceInventory(ctx, id, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	if err := c.domainInventory(ctx, id, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	if err := c.cdnInventory(ctx, id, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, err
	}
	if isWAFType(nativeType) {
		links, err := wafLinks(nativeType, raw)
		if err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized["_waf_links"] = links
		normalized["_waf_configuration"] = wafConfiguration(nativeType, raw)
		normalized["_waf_private_configuration"] = c.privateConfiguration(wafSnapshot(nativeType, raw))
	}
	if reference, err := wafPolicyReference(nativeType, raw); err != nil {
		return contracts.InventoryItem{}, err
	} else if reference != "" {
		normalized["_waf_policy"] = reference
	}
	if nativeType == containerGroupType {
		if err := validateContainerGroup(raw); err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized["_container_group_configuration"] = containerGroupConfiguration(raw)
		normalized["_container_group_private_configuration"] = c.containerGroupPrivateConfiguration(raw)
		if value := safe["identity"]; value != nil {
			normalized["identity"] = value
		}
	}
	if nativeType == groupType {
		normalized["_managed_group_owner"] = strings.ToLower(text(raw["managedBy"]))
	}
	if nativeType == monitorWorkspaceType {
		if err := validateMonitorConnections(id, object(raw["properties"])); err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized["_monitor_workspace_configuration"] = monitorWorkspaceConfiguration(raw)
		if value := safe["identity"]; value != nil {
			normalized["identity"] = value
		}
	}
	if isDataCollectionType(nativeType) {
		normalized["_data_collection_configuration"] = dataCollectionConfiguration(nativeType, raw)
		if value := safe["identity"]; value != nil {
			normalized["identity"] = value
		}
		if nativeType == dataCollectionAssociationType {
			parent, err := dataCollectionMonitoredResource(id)
			if err != nil || len(dataCollectionReferences(raw)) == 0 {
				return contracts.InventoryItem{}, serviceDenied("invalid_data_collection_association")
			}
			normalized["monitoredResourceId"] = parent
		}
	}
	if monitorPrivateLinkTarget(nativeType) {
		if _, _, err := monitorPrivateLinkReverse(nativeType, raw); err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized["_monitor_private_link_target_configuration"] = c.privateConfiguration(monitorPrivateLinkTargetSnapshot(raw))
		if nativeType == insightsWorkspaceType {
			if err := monitorRuleFields(object(raw["properties"]), "customerId"); err != nil {
				return contracts.InventoryItem{}, err
			}
			customer := strings.ToLower(text(object(raw["properties"])["customerId"]))
			if !uuidPattern.MatchString(customer) {
				return contracts.InventoryItem{}, serviceDenied("monitor_receiver_workspace_identity_missing")
			}
			normalized["customerId"] = customer
			normalized[monitorReceiverTargetProof] = c.monitorReceiverTargetBinding(id, nativeType, region, customer, text(normalized["_monitor_private_link_target_configuration"]))
		}
	}
	if err := c.monitorPrivateLinkInventory(ctx, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, contracts.DependencyReadError(err)
	}
	if isGrafanaType(nativeType) {
		normalized["_grafana_configuration"] = grafanaConfiguration(nativeType, raw)
		if nativeType != grafanaType {
			parent, err := c.grafanaParent(ctx, id)
			if err != nil {
				return contracts.InventoryItem{}, contracts.DependencyReadError(err)
			}
			normalized["_grafana_parent_generation"] = productGeneration(parent)
		}
		if value := safe["identity"]; value != nil {
			normalized["identity"] = value
		}
	}
	if hasServicePrerequisites(nativeType) {
		normalized["_arm_parent_configuration"] = serviceParentConfiguration(nativeType, raw)
	}
	if known {
		_, parameters, err := c.resourceOperation(kind, responseID(nativeType, text(raw["id"])), resourceReadMethod(nativeType))
		if err != nil {
			return contracts.InventoryItem{}, err
		}
		// Native child List operations reuse their parent's path parameters,
		// including multiple ancestor names and provider-specific spelling.
		normalized["arm_parameters"] = parameters
	}
	if nativeType == eventHubClusterType {
		settings, err := c.eventHubClusterSettings(ctx, id)
		if err != nil {
			return contracts.InventoryItem{}, err
		}
		if err := c.verifyProductParent(ctx, productTarget{ParentID: id, ParentType: nativeType, Generation: productGeneration(raw)}); err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized["quotaSettings"] = settings
	}
	if zones := array(raw["zones"]); len(zones) > 0 {
		normalized["zone_id"] = fmt.Sprint(zones[0])
	}
	reason := protectionReason(kind, raw)
	if reason == "" && nativeType == kustoImageType && normalized["_kusto_active_image"] == true {
		reason = "azure_kusto_active_image"
	}
	if reason == "" && isCosmosType(nativeType) {
		reason = cosmosThroughputProtection(object(normalized["_cosmos_throughput"]))
	}
	if replicationReason, creation, err := c.messagingReplicationContext(ctx, nativeType, id); err != nil {
		return contracts.InventoryItem{}, err
	} else {
		if creation != "" {
			normalized["_messaging_namespace_creation"] = creation
		}
		if replicationReason != "" && (reason == "" || controllerOnlyReason(reason)) {
			reason = replicationReason
		}
	}
	if groupOwners[groupID] != "" && (reason == "" || controllerOnlyReason(reason)) {
		reason = "azure_managed_resource_group"
	}
	if locked(id, locks) {
		reason = "azure_management_lock"
	}
	if reason != "" {
		// Provider ownership restricts direct deletion. It must not prohibit
		// deletion through the reviewed owning controller. Locks and retention
		// policies still protect both direct and delegated cleanup.
		if controllerOnlyReason(reason) {
			normalized["cleanup_controller_only"] = true
		} else {
			normalized["cleanup_protected"] = true
		}
		normalized["cleanup_protection_reason"] = reason
	}
	refs := references(nativeType, id, raw)
	if isAPIMType(nativeType) {
		for typ, values := range normalized["_apim_external_references"].(map[string][]string) {
			for _, value := range stringValues(values) {
				addReference(refs, typ, value)
			}
		}
		for _, value := range stringValues(normalized["_apim_references"]) {
			if ref, kind, err := parseID(value); err == nil {
				if mapping, known := findType(kind); known {
					addReference(refs, mapping.NativeType, ref)
				}
			}
		}
	}
	if isBatchType(nativeType) {
		batchAddReferences(normalized, refs)
	}
	if isStreamAnalyticsType(nativeType) {
		for _, value := range stringValues(normalized["_stream_analytics_references"]) {
			if ref, refKind, err := parseID(value); err == nil {
				if mapping, known := findType(refKind); known {
					addReference(refs, mapping.NativeType, ref)
				}
			}
		}
	}
	if isCosmosType(nativeType) {
		for _, value := range stringValues(normalized["_cosmos_references"]) {
			if id, typ, err := parseID(value); err == nil {
				if mapping, ok := findType(typ); ok {
					addReference(refs, mapping.NativeType, id)
				}
			}
		}
	}
	if isCognitiveType(nativeType) {
		for _, value := range stringValues(normalized["_cognitive_references"]) {
			if ref, refKind, err := parseID(value); err == nil {
				if mapping, known := findType(refKind); known {
					addReference(refs, mapping.NativeType, ref)
				}
			}
		}
	}
	if recoveryType(nativeType) {
		if err := c.recoveryInventory(ctx, nativeType, id, raw, normalized, refs); err != nil {
			return contracts.InventoryItem{}, err
		}
	}
	if nativeType == serviceBusMigrationType {
		if err := c.migrationInventory(ctx, id, raw, normalized, refs); err != nil {
			return contracts.InventoryItem{}, err
		}
	}
	if err := c.serviceBusForwardReferences(ctx, nativeType, id, raw, refs); err != nil {
		return contracts.InventoryItem{}, err
	}
	if nativeType == vmType {
		// VM placement is carried by its NICs, not by the VM ARM document.
		nicKind, _ := findType(nicType)
		for _, nicID := range refs[nicType] {
			endpoint, err := c.resourceURL(nicKind, nicID)
			if err != nil {
				return contracts.InventoryItem{}, err
			}
			nic, err := c.request(ctx, "GET", endpoint)
			if err != nil {
				return contracts.InventoryItem{}, err
			}
			if !strings.EqualFold(text(nic.data["id"]), nicID) {
				return contracts.InventoryItem{}, fmt.Errorf("Azure NIC identity mismatch")
			}
			for target, ids := range references(nicType, nicID, nic.data) {
				if target == vnetType || target == subnetType {
					for _, ref := range ids {
						addReference(refs, target, ref)
					}
				}
			}
		}
	}
	networkRefs := []string{}
	if nativeType == redisAssignmentType {
		policyID, err := redisPolicyReference(id, raw)
		if err != nil {
			return contracts.InventoryItem{}, err
		}
		addReference(refs, redisPolicyType, policyID)
	}
	for target, ids := range refs {
		sort.Strings(ids)
		normalized[referenceKey(target)] = ids
		networkRefs = append(networkRefs, ids...)
	}
	sort.Strings(networkRefs)
	if ids := refs[vnetType]; len(ids) == 1 {
		normalized["vpc_id"] = ids[0]
	}
	if ids := refs[subnetType]; len(ids) > 0 {
		normalized["subnet_ids"] = ids
		if len(ids) == 1 {
			normalized["vswitch_id"] = ids[0]
		}
	}
	if nativeType == vnetType {
		normalized["vpc_id"] = id
	}
	if nativeType == subnetType {
		normalized["vswitch_id"] = id
	}
	tags := map[string]string{}
	for key, value := range object(safe["tags"]) {
		if s, ok := value.(string); ok {
			tags[key] = s
		}
	}
	actionable := known && !kind.ReadOnly
	state := text(object(raw["properties"])["provisioningState"])
	if isBatchType(nativeType) {
		state = batchState(nativeType, raw)
	}
	if state == "" {
		state = text(object(raw["properties"])["status"])
	}
	if err := c.rbacIdentityInventory(id, nativeType, raw, normalized); err != nil {
		return contracts.InventoryItem{}, err
	}
	return contracts.InventoryItem{NativeID: id, NativeType: nativeType, ResourceKind: r.resourceKind(nativeType), Actionable: &actionable, Scope: scope,
		Name: text(raw["name"]), Location: region, State: state, Tags: tags, Normalized: normalized, Raw: safe,
		NativeAliases: []string{text(raw["id"]), id}, NetworkReferences: networkRefs}, nil
}

func addReference(refs map[string][]string, target, id string) {
	for _, existing := range refs[target] {
		if existing == id {
			return
		}
	}
	refs[target] = append(refs[target], id)
}
func references(nativeType, self string, raw map[string]any) map[string][]string {
	if insightsWorkbookKind(nativeType) != "" {
		return workbookReferences(nativeType, self, raw)
	}
	result := map[string][]string{}
	add := func(value string) {
		id, target, err := parseID(value)
		if err != nil || id == self {
			return
		}
		// Backend pools and similar embedded subresources resolve to their
		// modeled ARM parent. Reverse child lists are excluded below.
		for {
			if kind, known := findType(target); known {
				addReference(result, kind.NativeType, id)
				break
			}
			parts := strings.Split(id, "/")
			if len(parts) <= 9 {
				break
			}
			id, target, err = parseID(strings.Join(parts[:len(parts)-2], "/"))
			if err != nil {
				break
			}
		}
	}
	if isAPIMType(nativeType) {
		refs, _ := apimReferences(nativeType, self, raw) // Inventory validates these before normalization.
		for _, ref := range refs {
			add(ref)
		}
		return result
	}
	fields := map[string]bool{"subnet": true, "virtualnetwork": true, "networksecuritygroup": true, "routetable": true, "natgateway": true,
		"publicipaddress": true, "publicipaddresses": true, "publicipprefix": true, "publicipprefixes": true,
		"networkinterfaces": true, "manageddisk": true, "availabilityset": true, "diskencryptionset": true,
		"loadbalancerbackendaddresspools": true, "applicationgatewaybackendaddresspools": true, "loadbalancerfrontendipconfigurations": true,
		"serverfarmid": true, "virtualnetworksubnetid": true, "subnetresourceid": true, "managedenvironmentid": true, "environmentid": true,
		"elasticpoolid": true, "vnetsubnetid": true, "delegatedsubnetresourceid": true, "keyvaultid": true}
	for _, field := range []string{"virtualmachinescaleset", "virtualnetworkgateway1", "virtualnetworkgateway2", "localnetworkgateway2", "peer", "expressroutecircuit", "expressroutecircuitpeering", "virtualhub", "virtualwan", "remotenetwork", "remotevirtualnetwork", "firewallpolicy", "basepolicy", "ddosprotectionplan", "host", "hostgroup", "capacityreservationgroup", "targetresourceid", "targetresource", "privatednszoneid", "privateendpoint", "storageid", "workspaceResourceId", "associatedroutetable", "routemap", "outboundroutemap", "inboundroutemap"} {
		fields[strings.ToLower(field)] = true
	}
	if strings.EqualFold(nativeType, "Microsoft.Network/networkWatchers/packetCaptures") {
		fields["target"] = true
	}
	if nativeType == vpnConnectionType || nativeType == vpnLinkConnectionType {
		fields["ingressnatrules"], fields["egressnatrules"] = true, true
	}
	if nativeType == eventHubType {
		fields["storageaccountresourceid"] = true
		destination := object(object(object(object(raw["properties"])["captureDescription"])["destination"])["properties"])
		storageID, storageKind, err := parseID(text(destination["storageAccountResourceId"]))
		container := text(destination["blobContainer"])
		if err == nil && strings.EqualFold(storageKind, storageType) && container != "" && container != "." && container != ".." && !strings.ContainsAny(container, "/\\?#%\x00\r\n ") {
			add(storageID + "/blobServices/default/containers/" + container)
		}
	}
	if nativeType == eventHubNamespaceType {
		fields["clusterarmid"] = true
	}
	if nativeType == monitorScopedResourceType {
		fields["linkedresourceid"] = true
	}
	if isGrafanaType(nativeType) {
		fields["privatelinkresourceid"], fields["datasourceresourceid"], fields["azuremonitorworkspaceresourceid"] = true, true, true
	}
	if isCosmosType(nativeType) {
		for _, key := range []string{"virtualnetworkrules", "delegatedmanagementsubnetid", "delegatedsubnetid", "privatelinkresourceid", "networkaclbypassresourceids"} {
			fields[key] = true
		}
	}
	if isStreamAnalyticsType(nativeType) {
		add(streamAnalyticsParentID(self, nativeType))
		fields["privatelinkserviceid"], fields["cluster"] = true, true
	}
	if isKustoType(nativeType) {
		for _, key := range []string{"clusterresourceid", "leaderclusterresourceid", "privatelinkresourceid", "managedidentityresourceid", "cosmosdbaccountresourceid", "eventhubresourceid", "eventhubresourceidformanagedidentity", "storageaccountresourceid", "storageaccountresourceidformanagedidentity", "eventgridresourceid", "iothubresourceid", "useridentity", "enginepublicipid", "datamanagementpublicipid"} {
			fields[key] = true
		}
		if nativeType == kustoDatabaseType {
			if attachment, err := kustoFollowingAttachment(raw); err == nil {
				add(attachment)
			}
		}
		if nativeType == kustoType {
			add(text(object(object(raw["properties"])["migrationCluster"])["id"]))
			for _, value := range array(object(object(raw["properties"])["languageExtensions"])["value"]) {
				if name, err := kustoName(object(value)["languageExtensionCustomImageName"]); err == nil {
					add(self + "/sandboxCustomImages/" + name)
				}
			}
		}
		if nativeType == kustoDataConnectionType {
			props := object(raw["properties"])
			hub := text(props["eventHubResourceId"])
			if hub == "" {
				hub = text(props["eventHubResourceIdForManagedIdentity"])
			}
			if name, err := kustoName(props["consumerGroup"]); err == nil && hub != "" {
				add(hub + "/consumerGroups/" + name)
			}
		}
	}
	if nativeType == mongoClusterType {
		if source, err := mongoClusterSource(raw); err == nil && source != "" {
			add(source)
		}
		// The CMK access identity is current configuration, not restore history.
		key := object(object(object(raw["properties"])["encryption"])["customerManagedKeyEncryption"])
		add(text(object(key["keyEncryptionKeyIdentity"])["userAssignedIdentityResourceId"]))
	}
	if isCognitiveType(nativeType) {
		for _, key := range []string{"resourceid", "subnetarmid", "customersubnet", "serviceresourceid", "accountid", "commitmentplanid"} {
			fields[key] = true
		}
	}
	if isSearchType(nativeType) {
		fields["privatelinkresourceid"], fields["networksecurityperimeter"] = true, true
	}
	if isDataCollectionType(nativeType) {
		for _, key := range []string{"datacollectionruleid", "datacollectionendpointid", "eventhubresourceid", "storageaccountresourceid", "accountresourceid", "resourceid"} {
			fields[key] = true
		}
		if nativeType == dataCollectionAssociationType {
			parent, _ := dataCollectionMonitoredResource(self)
			add(parent)
		}
	}
	if strings.EqualFold(nativeType, monitorWorkspaceType) {
		fields["datacollectionruleresourceid"], fields["datacollectionendpointresourceid"] = true, true
	}
	if isCDNType(nativeType) {
		for _, key := range []string{"originGroup", "ruleSets", "secret", "secretSource", "azureDnsZone", "azureOrigin", "privateLink", "privateLinkResourceId", "webApplicationFirewallPolicyLink", "wafPolicy", "preValidatedCustomDomainResourceId"} {
			fields[strings.ToLower(key)] = true
		}
		if nativeType == afdRouteType {
			fields["customdomains"] = true
		}
		if nativeType == afdSecurityPolicyType {
			fields["domains"] = true
		}
		if nativeType == cdnOriginGroupType {
			fields["origins"] = true
		}
	}
	if strings.EqualFold(nativeType, containerGroupType) {
		fields["subnetids"], fields["identity"] = true, true
	}
	if isAppServiceType(nativeType) {
		fields["function_app_id"], fields["domainid"], fields["certificateresourceid"], fields["privateendpointarmresourceid"] = true, true, true, true
	}
	if strings.EqualFold(nativeType, "Microsoft.Network/networkWatchers/connectionMonitors") {
		fields["resourceid"] = true
	}
	var visit func(any, string)
	visit = func(value any, parent string) {
		switch typed := value.(type) {
		case map[string]any:
			if fields[strings.ToLower(parent)] {
				add(text(typed["id"]))
			}
			for key, value := range typed {
				// Propagated route tables use an ids array inside a named object.
				if strings.EqualFold(parent, "propagatedRouteTables") && strings.EqualFold(key, "ids") {
					for _, id := range array(value) {
						add(text(id))
					}
					continue
				}
				switch key {
				case "subnets", "virtualMachines", "backendIPConfigurations", "privateEndpointConnections", "creationData", "imageReference":
					continue
				case "source":
					if !strings.EqualFold(nativeType, "Microsoft.Network/networkWatchers/connectionMonitors") {
						continue
					}
				case "ipConfigurations":
					if strings.EqualFold(nativeType, subnetType) {
						continue
					}
				}
				visit(value, key)
			}
		case []any:
			for _, value := range typed {
				visit(value, parent)
			}
		case string:
			if fields[strings.ToLower(parent)] {
				add(typed)
			}
		}
	}
	visit(object(raw["properties"]), "")
	for identity := range object(object(raw["identity"])["userAssignedIdentities"]) {
		add(identity)
	}
	// Explicit child -> parent edges order child deletion before its parent.
	parts := strings.Split(self, "/")
	if len(parts) > 9 {
		parent := strings.Join(parts[:len(parts)-2], "/")
		if strings.EqualFold(nativeType, containerType) {
			parent = strings.Join(parts[:9], "/")
		}
		add(parent)
	}
	if strings.EqualFold(nativeType, subnetType) {
		add(strings.Join(parts[:len(parts)-2], "/"))
	}
	for _, id := range result[subnetType] {
		parts := strings.Split(id, "/")
		addReference(result, vnetType, strings.Join(parts[:len(parts)-2], "/"))
	}
	return result
}

func locked(id string, locks []any) bool {
	id = strings.ToLower(id)
	for _, value := range locks {
		lock := object(value)
		level := strings.ToLower(text(object(lock["properties"])["level"]))
		if level != "cannotdelete" && level != "readonly" {
			continue
		}
		lockID := strings.ToLower(text(lock["id"]))
		index := strings.LastIndex(lockID, "/providers/microsoft.authorization/locks/")
		if index < 0 {
			continue
		}
		scope := lockID[:index]
		if id == scope || strings.HasPrefix(id, scope+"/") || strings.HasPrefix(scope, id+"/") {
			return true
		}
	}
	return false
}

// Environment and configuration values can hold application secrets even when
// their keys are innocuous. Do not persist them in inventory or diagnostics.
func safeResource(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		if domainPath(text(typed["id"])) || domainPath("/providers/"+text(typed["type"])) {
			typed = object(domainSafeValue(typed))
		}
		if fleetPath(text(typed["id"])) || fleetPath("/providers/"+text(typed["type"])) {
			typed = object(fleetSafeValue(typed))
		}
		if applicationInsightsRaw(typed) {
			typed = object(applicationInsightsSafeValue(typed))
		}
		if monitorAlertPath(text(typed["id"])) || monitorAlertPath("/providers/"+text(typed["type"])) {
			typed = object(monitorAlertSafeValue(typed))
		}
		if monitorBudgetPath(text(typed["id"])) || monitorBudgetPath("/providers/"+text(typed["type"])) {
			typed = object(monitorBudgetSafeValue(typed))
		}
		if diagnosticSettingsPath(text(typed["id"])) || diagnosticSettingsPath("/providers/"+text(typed["type"])) || rbacPath(text(typed["id"])) || rbacPath("/providers/"+text(typed["type"])) {
			typed = object(diagnosticSettingsSafeValue(typed))
		}
		if apimRaw(typed) {
			typed = apimSafeRaw(typed)
		}
		if batchRaw(typed) {
			typed = object(batchSafeValue(typed))
		}
		result := map[string]any{}
		for key, value := range typed {
			if key == "properties" && (strings.HasPrefix(strings.ToLower(text(typed["type"])), "microsoft.streamanalytics/") || strings.Contains(strings.ToLower(text(typed["id"])), "/providers/microsoft.streamanalytics/")) {
				result[key] = safeResource(streamAnalyticsSafeProperties(value))
				continue
			}
			if key == "properties" && (strings.HasPrefix(strings.ToLower(text(typed["type"])), "microsoft.documentdb/") || strings.Contains(strings.ToLower(text(typed["id"])), "/providers/microsoft.documentdb/")) {
				properties := map[string]any{}
				for name, item := range object(value) {
					if name == "initialCassandraAdminPassword" || name == "base64EncodedCassandraYamlFragment" {
						continue
					}
					if name == "resource" {
						resource := map[string]any{}
						for field, entry := range object(item) {
							if field != "body" && field != "wrappedDataEncryptionKey" {
								resource[field] = entry
							}
						}
						item = resource
					}
					properties[name] = item
				}
				result[key] = safeResource(properties)
				continue
			}
			if typed["matchVariable"] != nil && (key == "matchValue" || key == "matchValues") {
				continue
			}
			if strings.HasSuffix(text(typed["typeName"]), "ConditionParameters") && key == "matchValues" {
				continue
			}
			if strings.HasSuffix(text(typed["typeName"]), "ActionParameters") && (key == "value" || key == "customQueryString" || key == "destination" || key == "customPath" || key == "customFragment") {
				continue
			}
			if strings.EqualFold(key, "settings") && (typed["typeHandlerVersion"] != nil || typed["extensionType"] != nil) {
				continue
			}
			if strings.Contains(strings.ToLower(strings.ReplaceAll(strings.ReplaceAll(key, "-", ""), "_", "")), "connectionstring") {
				continue
			}
			switch strings.ToLower(strings.ReplaceAll(key, "_", "")) {
			case "password", "adminpassword", "secret", "secrets", "clientsecret", "accesskey", "connectionstring", "connectionstrings",
				"servicekey", "authorizationkey", "sharedkey", "presharedkey", "peeringsharedkey", "radiusserversecret", "authenticationkey", "saskey", "sastoken", "primarykey", "secondarykey", "accountkey",
				"requestheaders", "httpheaders", "appsettings", "env", "environmentvariables", "customdata", "userdata", "protectedsettings", "protectedsettingsfromkeyvault", "error", "publishingpassword", "publishingprofile", "privatekey", "administratorloginpassword",
				"command", "configmap", "workspacekey", "storageaccountkey", "securevalue", "keyvalue", "validationtoken", "validationdata", "customblockresponsebody", "defaultcustomblockresponsebody", "pfxblob", "files", "config", "testdata", "secretsfilehref", "customdomainverificationid", "appcommandline", "migrationtoken", "qnaazuresearchendpointkey", "scriptcontent", "scripturlsastoken", "requirementsfilecontent":
				continue
			}
			if strings.EqualFold(key, "scriptUrl") || strings.EqualFold(key, "sampleBlobUrl") || strings.EqualFold(key, "target") || strings.EqualFold(key, "storagePath") || strings.EqualFold(key, "blobUrl") || strings.EqualFold(key, "repository") || strings.EqualFold(key, "vaultBaseUrl") || strings.EqualFold(key, "secretReferenceUri") || strings.HasSuffix(strings.ToLower(key), "href") || key == "invoke_url_template" {
				if endpoint, err := url.Parse(text(value)); err == nil && endpoint.Scheme != "" {
					endpoint.RawQuery, endpoint.Fragment, endpoint.User = "", "", nil
					result[key] = endpoint.String()
					continue
				}
			}
			result[key] = safeResource(value)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = safeResource(item)
		}
		return result
	default:
		return value
	}
}
func (r *Runtime) SearchNetworkTargets(ctx context.Context, query contracts.NetworkTargetQuery) (contracts.NetworkTargetPage, error) {
	nativeType := vnetType
	if query.Kind == asset.ScanTargetVSwitch {
		nativeType = subnetType
	} else if query.Kind != asset.ScanTargetVPC {
		return contracts.NetworkTargetPage{}, fmt.Errorf("unsupported Azure network target")
	}
	kind := r.resourceKind(nativeType)
	batch, err := r.List(ctx, contracts.InventoryRequest{ConnectionID: query.ConnectionID, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: query.RegionID},
		Source: productInventorySource, ResourceKind: &kind, Cursor: query.Cursor, Limit: query.Limit})
	if err != nil {
		return contracts.NetworkTargetPage{}, err
	}
	page := contracts.NetworkTargetPage{Items: []contracts.NetworkTargetOption{}, NextCursor: batch.NextCursor}
	for _, item := range batch.Items {
		parent := text(item.Normalized["vpc_id"])
		if query.ParentNativeID != "" && !strings.EqualFold(parent, query.ParentNativeID) {
			continue
		}
		if query.Query != "" && !strings.Contains(strings.ToLower(item.Name+" "+item.NativeID), strings.ToLower(query.Query)) {
			continue
		}
		page.Items = append(page.Items, contracts.NetworkTargetOption{Kind: query.Kind, RegionID: query.RegionID, NativeID: item.NativeID, Name: item.Name, ParentNativeID: parent})
	}
	return page, nil
}

// A malformed lock entry cannot establish an unlocked resource. Subscription
// and nested ARM locks share the same native extension suffix.
func (c *client) managementLocks(ctx context.Context) ([]any, error) {
	locks, err := c.listAll(ctx, c.root()+"/providers/Microsoft.Authorization/locks", locksVersion)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, value := range locks {
		raw := object(value)
		id := strings.ToLower(text(raw["id"]))
		marker := "/providers/microsoft.authorization/locks/"
		index := strings.LastIndex(id, marker)
		level := strings.ToLower(text(object(raw["properties"])["level"]))
		if index < len(c.root()) || !strings.HasPrefix(id, c.root()+"/") || strings.ContainsAny(id, "%?#\\\x00\r\n") || seen[id] || (level != "readonly" && level != "cannotdelete") || (text(raw["type"]) != "" && !strings.EqualFold(text(raw["type"]), "Microsoft.Authorization/locks")) {
			return nil, fmt.Errorf("invalid Azure management lock")
		}
		name := strings.TrimPrefix(id[index:], marker)
		if name == "" || strings.Contains(name, "/") {
			return nil, fmt.Errorf("invalid Azure management lock name")
		}
		for _, part := range strings.Split(strings.TrimPrefix(id, "/"), "/") {
			if part == "" || part == "." || part == ".." {
				return nil, fmt.Errorf("invalid Azure management lock scope")
			}
		}
		if id[:index] != c.root() {
			if _, _, err := parseID(id[:index]); err != nil {
				if _, _, _, budgetErr := monitorBudgetID(id[:index]); budgetErr != nil {
					return nil, fmt.Errorf("invalid Azure management lock scope")
				}
			}
		}
		seen[id] = true
	}
	return locks, nil
}
