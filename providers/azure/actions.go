package azure

import (
	"context"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type action struct {
	client       *client
	kind         resourceType
	id, endpoint string
	wireID       string
	location     string
	connectionID asset.ConnectionID
	partition    string
	deletion     catalog.RESTRequest
}

func (r *Runtime) ResolveAction(ctx context.Context, id asset.ConnectionID, value asset.Asset) (resolved contracts.ActionDriver, err error) {
	kind, ok := findType(value.Identity.NativeType)
	if !ok || kind.ReadOnly || value.Identity.Provider != asset.ProviderAzure {
		return nil, fmt.Errorf("Azure resource has no action driver")
	}
	if isAPIMType(kind.NativeType) && value.Identity.ConnectionID != id {
		return nil, serviceDenied("apim_action_connection_changed")
	}
	if communicationKind(kind.NativeType) != "" && value.Identity.ConnectionID != id {
		return nil, serviceDenied("communication_action_connection_changed")
	}
	if (monitorPrivateLinkKind(kind.NativeType) != "" || monitorPrivateLinkTarget(kind.NativeType)) && value.Identity.ConnectionID != id {
		return nil, serviceDenied("monitor_private_link_action_connection_changed")
	}
	c, err := r.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err == nil && monitorARMTarget(value) && monitorResourceKind(value.Identity.NativeType) == "" {
			if value.Identity.ConnectionID != id {
				resolved, err = nil, serviceDenied("monitor_target_action_connection_changed")
				return
			}
			resolved = &monitorTargetAction{client: c, inner: resolved, planned: value}
		}
	}()
	if rbacResourceKind(kind.NativeType) != "" {
		return newRBACAction(c, id, value)
	}
	if kind.NativeType == diagnosticSettingsType {
		return newDiagnosticAction(c, id, value)
	}
	if monitorResourceKind(kind.NativeType) != "" {
		return newMonitorAction(c, id, value)
	}
	if insightsWorkbookKind(kind.NativeType) != "" {
		return newInsightsWorkbookAction(c, id, value)
	}
	if fleetKind(kind.NativeType).kind != "" {
		return newFleetAction(c, id, value, kind)
	}
	if isBatchType(kind.NativeType) {
		if value.Identity.ConnectionID != id {
			return nil, serviceDenied("batch_action_connection_changed")
		}
		return newBatchAction(c, value, kind)
	}
	if communicationKind(kind.NativeType) != "" {
		return newCommunicationAction(c, id, value, kind)
	}
	if dataFactoryKind(kind.NativeType) != "" {
		return newDataFactoryAction(c, id, value, kind)
	}
	if kind.NativeType == netappSnapshotPolicyType {
		return newNetappSnapshotPolicyAction(c, id, value)
	}
	if kind.NativeType == netappPoolType {
		return newNetappPoolAction(c, id, value)
	}
	if netappDirectLeaf(kind.NativeType) {
		return newNetappRecoveryAction(c, id, value)
	}
	if kind.NativeType == netappVolumeType {
		return newNetappVolumeAction(c, id, value)
	}
	if kind.NativeType == synapseRestorePointType {
		return newSynapseRestorePointAction(c, id, value)
	}
	if kind.NativeType == synapseSQLType {
		return newSynapseSQLAction(c, id, value)
	}
	if kind.NativeType == synapseType {
		data, err := r.synapseResolvedClient(id, c)
		if err != nil {
			return nil, err
		}
		return newSynapseWorkspaceAction(data, id, value)
	}
	if kind.NativeType == synapseSparkType {
		data, err := r.synapseResolvedClient(id, c)
		if err != nil {
			return nil, err
		}
		return newSynapseSparkAction(data, id, value)
	}
	if dataMigrationKind(kind.NativeType) != "" {
		return newDataMigrationAction(c, id, value, kind)
	}
	if kind.NativeType == azureLocalAgentType || kind.NativeType == azureLocalVMType || azureLocalIndependent(kind.NativeType) {
		return newAzureLocalAction(c, id, value, kind)
	}
	if elasticSanIndependentChild(kind.NativeType) {
		return newElasticSanChildAction(c, id, value, kind)
	}
	if hybridComputeKind(kind.NativeType) != "" {
		return newHybridComputeAction(c, id, value, kind)
	}
	if insightsLegacyKind(kind.NativeType).kind != "" || insightsARMChildKind(kind.NativeType) != "" {
		return newInsightsChildAction(c, id, value, kind)
	}
	wireID, err := c.plannedResourceID(value)
	if err != nil {
		return nil, err
	}
	endpoint, err := c.resourceURL(kind, wireID)
	if err != nil {
		return nil, err
	}
	nativeID, _, _ := parseID(value.Identity.NativeID)
	operation, parameters, err := c.resourceOperation(kind, wireID, "DELETE")
	if err != nil {
		return nil, err
	}
	definition, _ := r.productDefinition(kind.NativeType)
	for key, supplied := range definition.Actions["delete"].Parameters {
		resolved := supplied
		if expression, ok := supplied.(string); ok {
			switch expression {
			case "scope.subscription":
				resolved = c.subscription
			case "scope.subscriptionPath":
				resolved = strings.TrimPrefix(c.root(), "/")
			case "scope.location":
				resolved = value.Location
			case "resource.nativeId":
				resolved = nativeID
			default:
				if strings.HasPrefix(expression, "resource.normalized.") {
					resolved = value.Normalized
					for _, part := range strings.Split(strings.TrimPrefix(expression, "resource.normalized."), ".") {
						resolved = object(resolved)[part]
					}
					if resolved == nil || resolved == "" {
						return nil, fmt.Errorf("Azure action is missing its bound resource parameter %s", key)
					}
				} else if strings.HasPrefix(expression, "scope.") || strings.HasPrefix(expression, "resource.") {
					return nil, fmt.Errorf("unsupported Azure action parameter expression")
				}
			}
		}
		if bound, exists := parameters[key]; exists && bound != resolved {
			return nil, fmt.Errorf("Azure action parameters cannot change the resource identity")
		}
		parameters[key] = resolved
	}
	deletion, err := bindAzureREST(operation, parameters)
	if err != nil {
		return nil, err
	}
	driver := &action{client: c, kind: kind, id: nativeID, wireID: wireID, endpoint: endpoint, deletion: deletion, location: strings.ToLower(value.Location), connectionID: id, partition: value.Identity.Partition}
	if isDomainType(kind.NativeType) {
		return newDomainAction(driver, value)
	}
	if kind.NativeType == applicationInsightsType {
		if value.ID == "" || value.Identity.NativeID != nativeID || value.Identity.ConnectionID != id || value.Identity.Partition == "" || value.Location == "" || value.Location != driver.location {
			return nil, serviceDenied("invalid_insights_component_action_identity")
		}
		if _, err := c.insightsWorkspacePlan(value); err != nil {
			return nil, err
		}
		return &insightsComponentAction{action: *driver, assetID: value.ID, configuration: text(value.Normalized["_monitor_private_link_target_configuration"]), workspaceConfiguration: text(value.Normalized["_insights_workspace_configuration"]), groupConfiguration: text(value.Normalized["_insights_group_configuration"]), settingsConfiguration: text(value.Normalized[insightsSettingsProof])}, nil
	}
	return driver, nil
}
func (*action) DeletionCheckTimeout() time.Duration { return time.Hour }
func (a *action) Preflight(ctx context.Context, request contracts.ActionRequest) (check contracts.PreflightResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.monitorPrivateLinkRequestIdentity(request.Asset); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.apimRequestIdentity(request.Asset); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.streamAnalyticsRequestIdentity(request.Asset); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.kustoRequestIdentity(request.Asset); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.mongoClusterRequestIdentity(request.Asset); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.cosmosRequestIdentity(request.Asset); err != nil {
		return contracts.PreflightResult{}, err
	}
	if request.Action != "delete" {
		return contracts.PreflightResult{Reason: "unsupported_action"}, nil
	}
	res, err := a.client.readResource(ctx, a.endpoint)
	if isNotFound(err) {
		if monitorPrivateLinkTarget(a.kind.NativeType) {
			if err := a.servicePrerequisitesAbsent(ctx, request); err != nil {
				return contracts.PreflightResult{}, err
			}
		}
		if a.kind.NativeType == serviceBusMigrationType {
			if err := a.verifyMigrationTarget(ctx, request.Asset); err != nil {
				return contracts.PreflightResult{}, err
			}
		}
		if a.kind.NativeType == aksType {
			read, err := a.managedGroupReadback(ctx, request)
			return contracts.PreflightResult{Allowed: err == nil, Absent: !read.Exists && err == nil, Evidence: map[string]any{"aks_cluster_absent": true}}, err
		}
		if HasServiceCascade(a.kind.NativeType) {
			read, err := a.serviceCascadeReadback(ctx, request)
			return contracts.PreflightResult{Allowed: err == nil, Absent: !read.Exists && err == nil, Evidence: map[string]any{"service_parent_absent": true}}, err
		}
		return contracts.PreflightResult{Allowed: true, Absent: true}, nil
	}
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if !validResourceResponse(res, a.id, a.kind.NativeType) || (isCosmosType(a.kind.NativeType) && !cosmosSameWireID(responseID(a.kind.NativeType, text(res.data["id"])), a.wireID)) {
		return contracts.PreflightResult{}, fmt.Errorf("Azure preflight identity mismatch")
	}
	if err := serviceCreationIdentity(request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := dataCollectionIncarnation(request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := monitorWorkspaceIncarnation(request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := containerGroupIncarnation(request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.client.servicePrivateIncarnation(request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := grafanaIncarnation(request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.cdnPreflight(ctx, request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.wafPreflight(request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.cosmosPreflight(ctx, request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.apimPreflight(ctx, request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.apimReferencesAbsent(ctx, request); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.streamAnalyticsPreflight(ctx, request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.kustoPreflight(ctx, request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.mongoClusterPreflight(ctx, request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.cognitivePreflight(ctx, request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.searchPreflight(ctx, request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.redisPreflight(ctx, request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.appServicePreflight(ctx, request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.monitorPrivateLinkReferencesAbsent(ctx, request, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.monitorPrivateLinkPreflight(ctx, request.Asset, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if err := a.grafanaParentPreflight(ctx, request.Asset); err != nil {
		return contracts.PreflightResult{}, err
	}
	if a.kind.NativeType == eventHubNamespaceType {
		planned, live := request.Asset.Normalized["clusterArmId"], object(res.data["properties"])["clusterArmId"]
		if !messagingNamespaceValueValid(planned) || !messagingNamespaceValueValid(live) || ((text(planned) != "" || text(live) != "") && (!eventHubClusterReference(live, text(planned)) || !eventHubClusterReference(planned, text(live)))) {
			return contracts.PreflightResult{Reason: "eventhub_cluster_membership_changed"}, nil
		}
	}
	if reason := protectionReason(a.kind, res.data); reason != "" {
		return contracts.PreflightResult{Reason: reason}, nil
	}
	if reason, creation, err := a.client.messagingReplicationContext(ctx, a.kind.NativeType, a.id); reason != "" || err != nil {
		return contracts.PreflightResult{Reason: reason}, err
	} else if expected := text(request.Asset.Normalized["_messaging_namespace_creation"]); expected != "" && expected != creation {
		return contracts.PreflightResult{Reason: "messaging_namespace_recreated"}, nil
	}
	parts := strings.Split(a.id, "/")
	group, err := a.client.request(ctx, "GET", apiURL(strings.Join(parts[:5], "/"), resourcesVersion))
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if !validResourceResponse(group, strings.Join(parts[:5], "/"), groupType) {
		return contracts.PreflightResult{}, fmt.Errorf("Azure resource group identity mismatch")
	}
	if text(group.data["managedBy"]) != "" {
		return contracts.PreflightResult{Reason: "azure_managed_resource_group"}, nil
	}
	if isDomainType(a.kind.NativeType) && (!insightsARMReadValid(group, strings.Join(parts[:5], "/"), groupType) || protectedAzureTags(object(group.data["tags"]))) {
		return contracts.PreflightResult{Reason: "azure_domain_resource_group_protected"}, nil
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if locked(a.id, locks) {
		return contracts.PreflightResult{Reason: "azure_management_lock"}, nil
	}
	if a.kind.NativeType == serviceBusMigrationType {
		if err := a.migrationPreflight(ctx, request.Asset, res.data, locks); err != nil {
			return contracts.PreflightResult{}, err
		}
	}
	if recoveryType(a.kind.NativeType) {
		if err := a.recoveryPreflight(ctx, request.Asset, res.data, locks); err != nil {
			return contracts.PreflightResult{}, err
		}
	}
	if err := a.validateScaleSetVMOwner(ctx, request, res.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if reason := serviceAssociationReason(a.kind.NativeType, res.data); reason != "" {
		return contracts.PreflightResult{Reason: reason}, nil
	}
	if HasServiceCascade(a.kind.NativeType) {
		if err := a.serviceCascadePreflight(ctx, request, res.data, locks); err != nil {
			return contracts.PreflightResult{}, err
		}
	}
	if a.kind.NativeType == vmType || a.kind.NativeType == nicType {
		if _, reason, err := a.evaluateAttachments(ctx, request, res.data, locks); reason != "" || err != nil {
			return contracts.PreflightResult{Reason: reason}, err
		}
	}
	switch a.kind.NativeType {
	case publicDNSZoneType:
		if err := a.client.domainZoneUnused(ctx, a.id); err != nil {
			return contracts.PreflightResult{}, err
		}
	case aksType:
		if reason, err := a.managedGroupPreflight(ctx, request, res.data, locks); reason != "" || err != nil {
			return contracts.PreflightResult{Reason: reason}, err
		}
	case vnetType:
		children, err := a.client.listAll(ctx, a.id+"/subnets", a.kind.Version)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if len(children) > 0 {
			return contracts.PreflightResult{Reason: "virtual_network_has_subnets"}, nil
		}
		linked, err := a.client.virtualNetworkHasDNSLinks(ctx, a.id)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if linked {
			return contracts.PreflightResult{Reason: "virtual_network_has_private_dns_links"}, nil
		}
	case privateDNSZoneType:
		children, err := a.client.privateDNSLinks(ctx, a.id, res.data)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if len(children) > 0 {
			return contracts.PreflightResult{Reason: "private_dns_zone_has_virtual_network_links"}, nil
		}
	case storageType:
		empty, err := a.client.storageAccountEmpty(ctx, a.id, res.data)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if !empty {
			return contracts.PreflightResult{Reason: "storage_account_not_empty"}, nil
		}
	case containerType:
		empty, err := a.client.blobContainerEmpty(ctx, a.id)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if !empty {
			return contracts.PreflightResult{Reason: "blob_container_not_empty"}, nil
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}
func (a *action) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	check, err := a.Preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !check.Allowed {
		return contracts.ActionResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProtected, Code: check.Reason, Message: contracts.SafeProviderValidationMessage}}
	}
	if check.Absent {
		if monitorPrivateLinkKind(a.kind.NativeType) != "" {
			return a.operationResult(response{})
		}
		return contracts.ActionResult{}, nil
	}
	if check.Evidence["aks_cluster_absent"] == true || check.Evidence["service_parent_absent"] == true {
		return contracts.ActionResult{}, nil
	}
	if a.kind.NativeType == vmType || a.kind.NativeType == nicType {
		return a.prepareAttachments(ctx, request)
	}
	if a.kind.NativeType == serviceBusMigrationType {
		return a.prepareMigration(ctx, request)
	}
	if recoveryType(a.kind.NativeType) {
		return a.prepareRecovery(ctx, request)
	}
	return a.delete(ctx, request)
}

func (a *action) delete(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	headers := map[string]string{}
	for name, value := range a.deletion.Headers {
		headers[name] = value
	}
	if isAPIMType(a.kind.NativeType) {
		if err := a.apimDeleteHeaders(ctx, request.Asset, headers); err != nil {
			return contracts.ActionResult{}, err
		}
	}
	if request.IdempotencyKey != "" {
		headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey)
	}
	res, err := a.client.requestBody(ctx, a.deletion.Method, a.deletion.URL, a.deletion.Body, headers)
	if isNotFound(err) {
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if err := a.operationError(res); err != nil {
		return contracts.ActionResult{}, err
	}
	return a.operationResult(res)
}

func (a *action) operationResult(res response) (contracts.ActionResult, error) {
	if fleetKind(a.kind.NativeType).kind != "" {
		return a.fleetOperationResult(res)
	}
	if isAPIMType(a.kind.NativeType) {
		return a.apimOperationResult(res)
	}
	operation := res.header.Get("Azure-AsyncOperation")
	polling := "status"
	if operation == "" {
		operation = res.header.Get("Operation-Location")
	}
	if operation == "" {
		operation = res.header.Get("Location")
		polling = "location"
	}
	if operation != "" {
		if err := a.validateOperationURL(operation); err != nil {
			return contracts.ActionResult{}, err
		}
	}
	if monitorPrivateLinkKind(a.kind.NativeType) != "" {
		polling = "status"
	}
	data := map[string]any{"polling": polling}
	if monitorPrivateLinkKind(a.kind.NativeType) != "" {
		data["monitor_private_link_operation_binding"] = a.monitorPrivateLinkOperationBinding(operation)
	}
	if isStreamAnalyticsType(a.kind.NativeType) && operation != "" {
		data["stream_analytics_operation_binding"] = a.operationBinding(operation)
	}
	if isKustoType(a.kind.NativeType) && operation != "" {
		data["kusto_operation_binding"] = a.operationBinding(operation)
	}
	if isMongoClusterType(a.kind.NativeType) && operation != "" {
		data["mongocluster_operation_binding"] = a.operationBinding(operation)
	}
	if isCosmosType(a.kind.NativeType) && operation != "" {
		data["cosmos_operation_binding"] = a.cosmosOperationBinding(operation)
	}
	if isCognitiveType(a.kind.NativeType) && operation != "" {
		data["cognitive_operation_binding"] = a.operationBinding(operation)
	}
	if isSearchType(a.kind.NativeType) && operation != "" {
		data["search_operation_binding"] = a.operationBinding(operation)
	}
	if isRedisType(a.kind.NativeType) && operation != "" {
		data["redis_operation_binding"] = a.operationBinding(operation)
	}
	if isGrafanaType(a.kind.NativeType) && operation != "" {
		data["grafana_operation_binding"] = a.operationBinding(operation)
	}
	if isCDNType(a.kind.NativeType) && operation != "" {
		data["cdn_operation_binding"] = a.operationBinding(operation)
	}
	if isWAFType(a.kind.NativeType) && operation != "" {
		data["waf_operation_binding"] = a.operationBinding(operation)
	}
	if isAppServiceType(a.kind.NativeType) && operation != "" {
		data["app_service_operation_binding"] = a.operationBinding(operation)
	}
	return contracts.ActionResult{ProviderOperationID: operation, ProviderRequestID: res.requestID, Data: data, RetryAfter: retryAfter(res.header)}, nil
}
func operationError(response response) error {
	data := response.data
	state := strings.ToLower(text(data["status"]))
	if state == "" {
		state = strings.ToLower(text(object(data["properties"])["provisioningState"]))
	}
	if state == "failed" || state == "canceled" || state == "cancelled" || len(object(data["error"])) > 0 {
		return &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProviderFailure, Code: "operation_failed", Message: contracts.SafeProviderValidationMessage, RequestID: response.requestID}}
	}
	return nil
}
func (a *action) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.monitorPrivateLinkRequestIdentity(request.Asset); err != nil {
		return contracts.WaitResult{}, err
	}
	if err := a.apimRequestIdentity(request.Asset); err != nil {
		return contracts.WaitResult{}, err
	}
	if err := a.streamAnalyticsRequestIdentity(request.Asset); err != nil {
		return contracts.WaitResult{}, err
	}
	if err := a.kustoRequestIdentity(request.Asset); err != nil {
		return contracts.WaitResult{}, err
	}
	if err := a.mongoClusterRequestIdentity(request.Asset); err != nil {
		return contracts.WaitResult{}, err
	}
	if err := a.cosmosRequestIdentity(request.Asset); err != nil {
		return contracts.WaitResult{}, err
	}
	phase := text(result.Data["phase"])
	if phase == "prepare_attachments" {
		return a.waitAttachmentPreparation(ctx, request, result)
	}
	if phase == "break_recovery" || phase == "await_recovery" {
		return a.waitRecoveryPreparation(ctx, request, result)
	}
	if phase == "revert_migration" || phase == "await_migration" {
		return a.waitMigrationPreparation(ctx, request, result)
	}
	if phase != "" {
		if phase != "delete" || (a.kind.NativeType != vmType && a.kind.NativeType != nicType && a.kind.NativeType != serviceBusMigrationType && !recoveryType(a.kind.NativeType)) {
			return contracts.WaitResult{}, fmt.Errorf("invalid Azure action phase")
		}
		result.ProviderOperationID = text(result.Data["operation"])
	}
	poll, err := a.poll(ctx, result)
	if err != nil || !poll.Done {
		return poll, err
	}
	read, err := a.Readback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second, State: read.State, Data: poll.Data}, err
}

func (a *action) poll(ctx context.Context, result contracts.ActionResult) (contracts.WaitResult, error) {
	if fleetKind(a.kind.NativeType).kind != "" {
		return a.fleetPoll(ctx, result)
	}
	if err := a.monitorPrivateLinkPollReceipt(result); err != nil {
		return contracts.WaitResult{}, err
	}
	if isAPIMType(a.kind.NativeType) {
		return a.apimPoll(ctx, result)
	}
	if result.ProviderOperationID != "" {
		if err := a.validateOperationURL(result.ProviderOperationID); err != nil {
			return contracts.WaitResult{}, err
		}
		if polling := text(result.Data["polling"]); polling != "status" && polling != "location" {
			return contracts.WaitResult{}, fmt.Errorf("invalid Azure polling protocol")
		}
		if err := a.streamAnalyticsPollReceipt(&result); err != nil {
			return contracts.WaitResult{}, err
		}
		if isKustoType(a.kind.NativeType) && text(result.Data["kusto_operation_binding"]) != a.operationBinding(result.ProviderOperationID) {
			return contracts.WaitResult{}, fmt.Errorf("Kusto polling receipt does not match its resource")
		}
		if isMongoClusterType(a.kind.NativeType) && text(result.Data["mongocluster_operation_binding"]) != a.operationBinding(result.ProviderOperationID) {
			return contracts.WaitResult{}, fmt.Errorf("DocumentDB polling receipt does not match its resource")
		}
		if isCosmosType(a.kind.NativeType) && text(result.Data["cosmos_operation_binding"]) != a.cosmosOperationBinding(result.ProviderOperationID) {
			return contracts.WaitResult{}, fmt.Errorf("Cosmos DB polling receipt does not match its resource")
		}
		if isGrafanaType(a.kind.NativeType) && text(result.Data["grafana_operation_binding"]) != a.operationBinding(result.ProviderOperationID) {
			return contracts.WaitResult{}, fmt.Errorf("Grafana polling receipt does not match its resource")
		}
		if isCDNType(a.kind.NativeType) && text(result.Data["cdn_operation_binding"]) != a.operationBinding(result.ProviderOperationID) {
			return contracts.WaitResult{}, fmt.Errorf("CDN polling receipt does not match its resource")
		}
		if isWAFType(a.kind.NativeType) && text(result.Data["waf_operation_binding"]) != a.operationBinding(result.ProviderOperationID) {
			return contracts.WaitResult{}, fmt.Errorf("WAF polling receipt does not match its resource")
		}
		if isAppServiceType(a.kind.NativeType) && text(result.Data["app_service_operation_binding"]) != a.operationBinding(result.ProviderOperationID) {
			return contracts.WaitResult{}, fmt.Errorf("App Service polling receipt does not match its resource")
		}
		if isCognitiveType(a.kind.NativeType) && text(result.Data["cognitive_operation_binding"]) != a.operationBinding(result.ProviderOperationID) {
			return contracts.WaitResult{}, fmt.Errorf("Cognitive Services polling receipt does not match its resource")
		}
		if isSearchType(a.kind.NativeType) && text(result.Data["search_operation_binding"]) != a.operationBinding(result.ProviderOperationID) {
			return contracts.WaitResult{}, fmt.Errorf("Search polling receipt does not match its resource")
		}
		if isRedisType(a.kind.NativeType) && text(result.Data["redis_operation_binding"]) != a.operationBinding(result.ProviderOperationID) {
			return contracts.WaitResult{}, fmt.Errorf("Redis polling receipt does not match its resource")
		}
		res, err := a.client.requestAt(ctx, "GET", result.ProviderOperationID, nil, nil, a.validateOperationURL)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			if err := a.resourceOperationResponse(result.ProviderOperationID, res); err != nil {
				return contracts.WaitResult{}, err
			}
			if err := a.grafanaOperationResponse(result.ProviderOperationID, res); err != nil {
				return contracts.WaitResult{}, err
			}
			if err := a.operationError(res); err != nil {
				return contracts.WaitResult{}, err
			}
			data, err := a.streamAnalyticsNextPoll(result, res)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			state := text(res.data["status"])
			if state == "" {
				state = text(object(res.data["properties"])["provisioningState"])
			}
			done := strings.EqualFold(state, "Succeeded")
			if text(result.Data["polling"]) == "location" && state == "" {
				done = res.status == 200 || res.status == 204
			}
			if a.streamAnalyticsLocationReadback(result, res) {
				done = true
			}
			if !done {
				return contracts.WaitResult{RetryAfter: retryAfter(res.header), State: state, Data: data}, nil
			}
			return contracts.WaitResult{Done: true, Data: data}, nil
		}
	}
	return contracts.WaitResult{Done: true}, nil
}

func (a *action) validateOperationURL(endpoint string) error {
	if fleetKind(a.kind.NativeType).kind != "" {
		return validateFleetOperationURL(a.client.subscription, a.id, a.location, endpoint)
	}
	if monitorPrivateLinkKind(a.kind.NativeType) != "" {
		normalized, err := monitorPrivateLinkOperationURL(a.client.subscription, a.id, endpoint)
		if err == nil && normalized != endpoint {
			return serviceDenied("monitor_private_link_poll_url_not_canonical")
		}
		return err
	}
	if isAPIMType(a.kind.NativeType) {
		return validateAPIMOperationURL(a.client.subscription, a.id, a.location, a.kind.Version, endpoint)
	}
	if isStreamAnalyticsType(a.kind.NativeType) {
		return validateStreamAnalyticsOperationURL(a.client.subscription, a.id, a.kind.Version, endpoint)
	}
	if isKustoType(a.kind.NativeType) {
		return validateKustoOperationURL(a.client.subscription, a.location, a.kind.Version, endpoint)
	}
	if isMongoClusterType(a.kind.NativeType) {
		return validateMongoClusterOperationURL(a.client.subscription, a.location, a.kind.Version, endpoint)
	}
	if isCosmosType(a.kind.NativeType) {
		return validateCosmosOperationURL(a.client.subscription, a.wireID, a.kind.Version, endpoint)
	}
	if isGrafanaType(a.kind.NativeType) && grafanaGlobalOperation(endpoint) {
		return validateGrafanaGlobalOperation(endpoint, a.location)
	}
	if err := a.client.validateURL(endpoint); err != nil {
		return err
	}
	u, _ := url.Parse(endpoint)
	parts := strings.Split(strings.ToLower(u.Path), "/")
	namespace := strings.ToLower(strings.Split(a.kind.NativeType, "/")[0])
	resourceParts := strings.Split(a.id, "/")
	found := false
	for i, part := range parts {
		if i+1 >= len(parts) {
			continue
		}
		switch part {
		case "providers":
			if parts[i+1] != namespace {
				return fmt.Errorf("Azure operation belongs to another resource provider")
			}
			found = true
		case "resourcegroups":
			if len(resourceParts) < 5 || parts[i+1] != resourceParts[4] {
				return fmt.Errorf("Azure operation belongs to another resource group")
			}
		case "locations":
			if a.location != "" && a.location != "global" && strings.ReplaceAll(parts[i+1], " ", "") != strings.ReplaceAll(strings.ToLower(a.location), " ", "") {
				return fmt.Errorf("Azure operation belongs to another region")
			}
		}
	}
	if !found {
		return fmt.Errorf("Azure operation has no resource provider")
	}
	return nil
}
func (a *action) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.monitorPrivateLinkRequestIdentity(request.Asset); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.apimRequestIdentity(request.Asset); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.streamAnalyticsRequestIdentity(request.Asset); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.kustoRequestIdentity(request.Asset); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.mongoClusterRequestIdentity(request.Asset); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.cosmosRequestIdentity(request.Asset); err != nil {
		return contracts.ReadbackResult{}, err
	}
	res, err := a.client.readResource(ctx, a.endpoint)
	if isNotFound(err) {
		if monitorPrivateLinkTarget(a.kind.NativeType) {
			if err := a.servicePrerequisitesAbsent(ctx, request); err != nil {
				return contracts.ReadbackResult{}, err
			}
		}
		if a.kind.NativeType == serviceBusMigrationType {
			if err := a.verifyMigrationTarget(ctx, request.Asset); err != nil {
				return contracts.ReadbackResult{}, err
			}
		}
		if a.kind.NativeType == aksType {
			return a.managedGroupReadback(ctx, request)
		}
		if HasServiceCascade(a.kind.NativeType) {
			return a.serviceCascadeReadback(ctx, request)
		}
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if !validResourceResponse(res, a.id, a.kind.NativeType) || (isCosmosType(a.kind.NativeType) && !cosmosSameWireID(responseID(a.kind.NativeType, text(res.data["id"])), a.wireID)) {
		return contracts.ReadbackResult{}, fmt.Errorf("Azure readback identity mismatch")
	}
	return contracts.ReadbackResult{Exists: true, State: text(object(res.data["properties"])["provisioningState"])}, nil
}
func protectionReason(kind resourceType, raw map[string]any) string {
	if kind.NativeType == cdnProfileType {
		switch text(object(raw["properties"])["resourceState"]) {
		case "Migrating", "Migrated", "PendingMigrationCommit", "CommittingMigration", "AbortingMigration":
			return "azure_cdn_profile_migration_in_progress"
		}
	}

	if protectedAzureTags(object(raw["tags"])) {
		return "azure_protected_tag"
	}
	if kind.NativeType == fleetGateType {
		return "azure_fleet_gate_requires_update_run"
	}
	if reason := apimProtection(kind.NativeType, raw); reason != "" {
		return reason
	}
	if kind.NativeType == batchPerimeterType {
		return "azure_batch_managed_configuration"
	}
	if reason := streamAnalyticsProtection(kind.NativeType); reason != "" {
		return reason
	}
	if reason := kustoProtection(kind.NativeType, raw); reason != "" {
		return reason
	}
	if reason := cosmosProtection(kind.NativeType, raw); reason != "" {
		return reason
	}
	if reason := cognitiveProtection(kind.NativeType, raw); reason != "" {
		return reason
	}
	if kind.NativeType == searchPerimeterType {
		return "azure_search_managed_configuration"
	}
	if kind.NativeType == redisPolicyType {
		switch object(raw["properties"])["type"] {
		case "BuiltIn":
			return "azure_redis_builtin_policy"
		case "Custom":
		default:
			return "azure_redis_unknown_policy_type"
		}
	}
	if kind.NativeType == redisLinkType && object(raw["properties"])["serverRole"] == "Primary" {
		return "azure_redis_secondary_link"
	}
	if kind.NativeType == eventHubClusterType {
		if reason := eventHubClusterMinimumAge(raw, time.Now()); reason != "" {
			return reason
		}
	}
	if isDNSRecordType(kind.NativeType) {
		if protectedAzureTags(object(object(raw["properties"])["metadata"])) {
			return "azure_protected_tag"
		}
		if object(raw["properties"])["isAutoRegistered"] == true {
			return "azure_dns_auto_registered_record"
		}
		if isDNSSystemRecord(kind.NativeType, text(raw["id"])) {
			return "azure_dns_system_record"
		}
	}
	if kind.NativeType == "Microsoft.Sql/servers/databases" && strings.EqualFold(last(text(raw["id"])), "master") {
		return "azure_system_database"
	}
	if owner := text(raw["managedBy"]); owner != "" {
		_, ownerType, err := parseID(owner)
		// An attached disk's managedBy is its VM. The VM -> disk dependency
		// orders detachment; other provider-managed resources remain protected.
		if kind.NativeType != diskType || err != nil || !strings.EqualFold(ownerType, vmType) {
			return "azure_managed_resource"
		}
	}
	properties := object(raw["properties"])
	if reason := messagingDeletionReason(kind.NativeType, raw); reason != "" {
		return reason
	}
	if messagingManagedConfiguration(kind.NativeType) {
		return "azure_messaging_managed_configuration"
	}
	if kind.NativeType == vpnLinkConnectionType {
		return "azure_vpn_connection_managed_link"
	}
	if kind.NativeType == scaleSetNICType || kind.NativeType == scaleSetIPConfigType || kind.NativeType == scaleSetPublicIPType {
		return "azure_scale_set_managed_network"
	}
	if (kind.NativeType == scaleSetVMType || kind.NativeType == vmType) && (object(properties["protectionPolicy"])["protectFromScaleSetActions"] == true || object(properties["protectionPolicy"])["protectFromScaleIn"] == true) {
		return "azure_scale_set_instance_protected"
	}
	if kind.NativeType == nicType && text(object(properties["privateEndpoint"])["id"]) != "" {
		return "azure_private_endpoint_managed_nic"
	}
	if kind.NativeType == containerType && (properties["hasLegalHold"] == true || properties["hasImmutabilityPolicy"] == true) {
		return "blob_container_retention_policy"
	}
	return ""
}

func controllerOnlyReason(reason string) bool {
	switch reason {
	case "azure_fleet_gate_requires_update_run":
		return true
	case "azure_apim_template_reset_only", "azure_apim_builtin_group", "azure_apim_administrator", "azure_apim_builtin_subscription":
		return true
	case "azure_batch_managed_configuration":
		return true
	case "azure_managed_resource", "azure_managed_resource_group", "azure_scale_set_managed_vm", "azure_scale_set_managed_network", "azure_vpn_connection_managed_link", "azure_private_endpoint_managed_nic", "azure_system_database", "azure_dns_system_record", "azure_dns_auto_registered_record":
		return true
	case "azure_stream_analytics_transformation", "azure_kusto_following_database", "azure_kusto_active_image", "azure_cosmos_builtin_role", "azure_cosmos_managed_encryption_key", "azure_cognitive_managed_configuration", "azure_search_managed_configuration", "azure_redis_builtin_policy", "azure_redis_secondary_link", "azure_messaging_recovery_secondary", "azure_messaging_managed_configuration", "azure_messaging_default_authorization_rule", "azure_messaging_replication_requires_unpairing", "azure_app_service_default_hostname":
		return true
	default:
		return false
	}
}

func protectedAzureTags(tags map[string]any) bool {
	for key, value := range tags {
		if strings.EqualFold(key, "steward/protected") || strings.EqualFold(key, "steward:protected") {
			switch strings.ToLower(text(value)) {
			case "1", "true", "yes", "on", "protected":
				return true
			}
		}
	}
	return false
}
