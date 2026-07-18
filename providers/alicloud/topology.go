package alicloud

import (
	"context"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

const (
	vpcNativeType              = "ACS::VPC::VPC"
	ecsInstanceNativeType      = "ACS::ECS::Instance"
	vSwitchNativeType          = "ACS::VPC::VSwitch"
	securityGroupNativeType    = "ACS::ECS::SecurityGroup"
	networkInterfaceNativeType = "ACS::ECS::NetworkInterface"
	diskNativeType             = "ACS::ECS::Disk"
	endpointNativeType         = "ACS::PrivateLink::VpcEndpoint"
	endpointServiceNativeType  = "ACS::PrivateLink::VpcEndpointService"
	natGatewayNativeType       = "ACS::NAT::NatGateway"
	slbLoadBalancerNativeType  = "ACS::SLB::LoadBalancer"
	albLoadBalancerNativeType  = "ACS::ALB::LoadBalancer"
	nlbLoadBalancerNativeType  = "ACS::NLB::LoadBalancer"
	gwlbLoadBalancerNativeType = "ACS::GWLB::LoadBalancer"
	armsEnvironmentNativeType  = "ACS::ARMS::Environment"
	dtsInstanceNativeType      = "ACS::DTS::Instance"
	ossBucketNativeType        = "ACS::OSS::Bucket"
)

type topologyDetailDefinition struct {
	nativeType string
	service    string
	api        spec.ProductAPISpec
	enrich     func([]contracts.InventoryItem, map[string]map[string]any) []contracts.InventoryItem
}

var topologyEnrichers = map[string]func(
	[]contracts.InventoryItem,
	map[string]map[string]any,
) []contracts.InventoryItem{
	ecsInstanceNativeType:        enrichInstanceTopology,
	securityGroupNativeType:      enrichSecurityGroupTopology,
	networkInterfaceNativeType:   enrichNetworkInterfaceTopology,
	diskNativeType:               enrichDiskTopology,
	armsEnvironmentNativeType:    enrichARMSEnvironmentTopology,
	dtsInstanceNativeType:        enrichDTSInventoryTopology,
	ossBucketNativeType:          enrichOSSBucketProperties,
	polarDBApplicationNativeType: enrichPolarDBApplicationTopology,
}

func (r *Runtime) topologyDetailDefinitions() []topologyDetailDefinition {
	definitions := make([]topologyDetailDefinition, 0, len(topologyEnrichers))
	for _, compiled := range r.bundle.Specs {
		enrich, supported := topologyEnrichers[compiled.ResourceKind.NativeType]
		api := compiled.Definition.Discovery.Enrich
		if !supported || api == nil {
			continue
		}
		service := "product-api"
		if operation, ok := r.catalog.Operation(api.Operation); ok {
			service = cloudAPIService(operation)
		}
		definitions = append(definitions, topologyDetailDefinition{
			nativeType: compiled.ResourceKind.NativeType,
			service:    service,
			api:        *api,
			enrich:     enrich,
		})
	}
	return definitions
}

func (r *Runtime) EnrichInventoryBatch(
	ctx context.Context,
	request contracts.InventoryRequest,
	items []contracts.InventoryItem,
) ([]contracts.InventoryItem, error) {
	enriched, err := cloneInventoryItems(items)
	if err != nil {
		return nil, err
	}
	enriched, err = r.enrichDataWorksResourceGroups(ctx, request, enriched)
	if err != nil {
		return nil, err
	}
	enriched, err = r.enrichPrivateLinkEndpointTopology(ctx, request, enriched)
	if err != nil {
		return nil, err
	}
	enriched, err = r.enrichEndpointServiceTopology(ctx, request, enriched)
	if err != nil {
		return nil, err
	}
	enriched = enrichNLBInventoryTopology(enriched)
	resourceCenter := strings.TrimSpace(request.Source) == "resource-center"
	if !resourceCenter {
		for index := range enriched {
			if enriched[index].NativeType != vpcNativeType {
				continue
			}
			if enriched[index].Normalized == nil {
				enriched[index].Normalized = make(map[string]any)
			}
			enriched[index].Normalized["vpc_id"] = strings.TrimSpace(enriched[index].NativeID)
		}
	}
	definitions := r.topologyDetailDefinitions()
	if resourceCenter {
		filtered := make([]topologyDetailDefinition, 0, 1)
		for _, definition := range definitions {
			if definition.nativeType == dtsInstanceNativeType {
				filtered = append(filtered, definition)
			}
		}
		definitions = filtered
	}
	supported := make(map[string]struct{}, len(definitions))
	for _, definition := range definitions {
		supported[definition.nativeType] = struct{}{}
	}
	grouped := make(map[string][]string, len(definitions))
	normalizedByType := make(map[string]map[string]map[string]any, len(definitions))
	for _, item := range enriched {
		nativeID := strings.TrimSpace(item.NativeID)
		if _, ok := supported[item.NativeType]; nativeID == "" || !ok {
			continue
		}
		grouped[item.NativeType] = appendUniqueString(grouped[item.NativeType], nativeID)
		if normalizedByType[item.NativeType] == nil {
			normalizedByType[item.NativeType] = make(map[string]map[string]any)
		}
		normalizedByType[item.NativeType][nativeID] = item.Normalized
	}
	if len(grouped) == 0 {
		return enriched, nil
	}
	region, err := inventoryRegion(request)
	if err != nil {
		return nil, err
	}
	credential, err := r.resolveCredential(ctx, request.ConnectionID)
	if err != nil {
		return nil, err
	}
	for _, definition := range definitions {
		ids := grouped[definition.nativeType]
		if len(ids) == 0 {
			continue
		}
		details, err := r.loadTopologyDetails(
			ctx,
			request,
			credential,
			region,
			definition,
			ids,
			normalizedByType[definition.nativeType],
			request.ResourceKind == nil,
		)
		if err != nil {
			return nil, err
		}
		enriched = definition.enrich(enriched, details)
	}
	return enriched, nil
}

func (r *Runtime) loadTopologyDetails(
	ctx context.Context,
	request contracts.InventoryRequest,
	credential contracts.Credential,
	region string,
	definition topologyDetailDefinition,
	ids []string,
	normalizedByID map[string]map[string]any,
	allowMissing bool,
) (map[string]map[string]any, error) {
	details := make(map[string]map[string]any, len(ids))
	batchLimit := definition.api.MaxBatchSize
	if batchLimit <= 0 {
		batchLimit = 100
	}
	for start := 0; start < len(ids); start += batchLimit {
		end := start + batchLimit
		if end > len(ids) {
			end = len(ids)
		}
		batchIDs := ids[start:end]
		var normalized map[string]any
		if len(batchIDs) == 1 {
			normalized = normalizedByID[batchIDs[0]]
		}
		parameters, err := resolveSpecParameters(
			definition.api.Parameters,
			specParameterContext{region: region, nativeIDs: batchIDs, normalized: normalized},
		)
		if err != nil {
			return nil, err
		}
		operation := detailOperationName(definition)
		catalogOperation, ok := r.catalog.Operation(definition.api.Operation)
		if !ok {
			return nil, fmt.Errorf("Alibaba Cloud operation %q is not in the generated catalog", definition.api.Operation)
		}
		execution.LogCloudAPIRequest(ctx, definition.service, operation, rawCloudPayload(parameters))
		result, err := r.factory.Invoke(ctx, credential, region, catalogOperation, contracts.Invocation{
			ConnectionID: request.ConnectionID,
			Operation:    catalogOperation.Key(),
			Scope:        map[string]string{"region": region},
			Parameters:   parameters,
		})
		if err != nil {
			LogCloudAPIError(ctx, definition.service, operation, err)
			normalized := NormalizeError(err)
			return nil, fmt.Errorf("%s failed: %w", detailOperationLabel(definition), normalized)
		}
		responsePayload := rawCloudPayload(result.Data)
		if result.RequestID != "" {
			responsePayload["RequestId"] = result.RequestID
		}
		execution.LogCloudAPIResponse(ctx, definition.service, operation, responsePayload)
		batchDetails, err := validateTopologyDetailCoverage(
			definition,
			batchIDs,
			topologyDetailRecords(result.Data, definition.api.ItemsPath),
			allowMissing,
			result.RequestID,
		)
		if err != nil {
			return nil, err
		}
		for nativeID, record := range batchDetails {
			details[nativeID] = record
		}
	}
	return details, nil
}

func validateTopologyDetailCoverage(
	definition topologyDetailDefinition,
	requestedIDs []string,
	records []any,
	allowMissing bool,
	providerRequestID string,
) (map[string]map[string]any, error) {
	label := detailOperationLabel(definition)
	requested := make(map[string]struct{}, len(requestedIDs))
	for _, nativeID := range requestedIDs {
		nativeID = strings.TrimSpace(nativeID)
		if nativeID == "" {
			return nil, fmt.Errorf("%s detail request contains an empty ID", label)
		}
		if _, exists := requested[nativeID]; exists {
			return nil, fmt.Errorf("%s detail request contains duplicate ID %q", label, nativeID)
		}
		requested[nativeID] = struct{}{}
	}

	details := make(map[string]map[string]any, len(records))
	for _, value := range records {
		record, ok := value.(map[string]any)
		if !ok {
			return nil, topologyDetailResponseError(
				label, providerRequestID,
				"record has no resource identity at %s", definition.api.IdentityPath,
			)
		}
		nativeID, ok := nonEmptyString(valueAtPath(record, definition.api.IdentityPath))
		if !ok {
			return nil, topologyDetailResponseError(
				label, providerRequestID,
				"record has no resource identity at %s", definition.api.IdentityPath,
			)
		}
		if _, expected := requested[nativeID]; !expected {
			return nil, topologyDetailResponseError(
				label, providerRequestID,
				"contains an unexpected resource: resource_id=%q", nativeID,
			)
		}
		if _, exists := details[nativeID]; exists {
			return nil, topologyDetailResponseError(
				label, providerRequestID,
				"contains a duplicate resource: resource_id=%q", nativeID,
			)
		}
		details[nativeID] = record
	}

	missing := make([]string, 0)
	for _, nativeID := range requestedIDs {
		nativeID = strings.TrimSpace(nativeID)
		if _, exists := details[nativeID]; !exists {
			missing = append(missing, nativeID)
		}
	}
	if len(missing) > 0 && !allowMissing {
		return nil, topologyDetailResponseError(
			label, providerRequestID,
			"is missing requested resources: resource_ids=%s", strings.Join(missing, ","),
		)
	}
	return details, nil
}

func topologyDetailResponseError(label, providerRequestID, format string, values ...any) error {
	message := fmt.Sprintf(format, values...)
	if providerRequestID = strings.TrimSpace(providerRequestID); providerRequestID != "" {
		message += "; provider_request_id=" + providerRequestID
	}
	return fmt.Errorf("%s detail response %s", label, message)
}

func detailOperationName(definition topologyDetailDefinition) string {
	return strings.TrimPrefix(definition.api.Operation, "AlibabaCloud.")
}

func detailOperationLabel(definition topologyDetailDefinition) string {
	return strings.TrimSpace(definition.service + " " + detailOperationName(definition))
}

func enrichInstanceTopology(items []contracts.InventoryItem, details map[string]map[string]any) []contracts.InventoryItem {
	return enrichTopologyItems(items, ecsInstanceNativeType, details, func(detail map[string]any) map[string]any {
		normalized := make(map[string]any, 5)
		if vpcAttributes, ok := detail["VpcAttributes"].(map[string]any); ok {
			copyTopologyString(normalized, "vpc_id", vpcAttributes["VpcId"])
			copyTopologyString(normalized, "vswitch_id", vpcAttributes["VSwitchId"])
		}
		copyTopologyString(normalized, "zone_id", detail["ZoneId"])
		if securityGroupIDs, ok := detail["SecurityGroupIds"].(map[string]any); ok {
			copyTopologyStrings(normalized, "security_group_ids", securityGroupIDs["SecurityGroupId"])
		}
		if networkInterfaces, ok := detail["NetworkInterfaces"].(map[string]any); ok {
			ids := make([]any, 0)
			for _, value := range anySlice(networkInterfaces["NetworkInterface"]) {
				record, ok := value.(map[string]any)
				if !ok {
					continue
				}
				if id, ok := nonEmptyString(record["NetworkInterfaceId"]); ok {
					ids = append(ids, id)
				}
			}
			if len(ids) > 0 {
				normalized["network_interface_ids"] = ids
			}
		}
		return normalized
	})
}

func enrichSecurityGroupTopology(items []contracts.InventoryItem, details map[string]map[string]any) []contracts.InventoryItem {
	return enrichTopologyItems(items, securityGroupNativeType, details, func(detail map[string]any) map[string]any {
		normalized := make(map[string]any, 3)
		copyTopologyString(normalized, "vpc_id", detail["VpcId"])
		copyTopologyValue(normalized, NormalizedServiceManagedField, detail["ServiceManaged"])
		copyTopologyValue(normalized, NormalizedServiceIDField, detail["ServiceID"])
		return normalized
	})
}

func enrichNetworkInterfaceTopology(items []contracts.InventoryItem, details map[string]map[string]any) []contracts.InventoryItem {
	return enrichTopologyItems(items, networkInterfaceNativeType, details, func(detail map[string]any) map[string]any {
		normalized := make(map[string]any, 6)
		copyTopologyString(normalized, "vpc_id", detail["VpcId"])
		copyTopologyString(normalized, "vswitch_id", detail["VSwitchId"])
		copyTopologyString(normalized, "attached_instance_id", detail["InstanceId"])
		if securityGroupIDs, ok := detail["SecurityGroupIds"].(map[string]any); ok {
			copyTopologyStrings(normalized, NormalizedSecurityGroupIDsField, securityGroupIDs["SecurityGroupId"])
		}
		copyTopologyValue(normalized, NormalizedServiceManagedField, detail["ServiceManaged"])
		copyTopologyValue(normalized, NormalizedServiceIDField, detail["ServiceID"])
		return normalized
	})
}

func enrichDiskTopology(items []contracts.InventoryItem, details map[string]map[string]any) []contracts.InventoryItem {
	return enrichTopologyItems(items, diskNativeType, details, func(detail map[string]any) map[string]any {
		normalized := make(map[string]any, 4)
		copyTopologyString(normalized, "attached_instance_id", detail["InstanceId"])
		copyTopologyString(normalized, "zone_id", detail["ZoneId"])
		copyTopologyString(normalized, "disk_type", detail["Type"])
		copyTopologyValue(normalized, "delete_with_instance", detail["DeleteWithInstance"])
		return normalized
	})
}

func enrichARMSEnvironmentTopology(items []contracts.InventoryItem, details map[string]map[string]any) []contracts.InventoryItem {
	return enrichTopologyItems(items, armsEnvironmentNativeType, details, func(detail map[string]any) map[string]any {
		normalized := make(map[string]any, 10)
		copyTopologyString(normalized, "name", detail["EnvironmentName"])
		copyTopologyString(normalized, "state", detail["BindResourceStatus"])
		copyTopologyString(normalized, "environmentType", detail["EnvironmentType"])
		copyTopologyString(normalized, "environmentSubType", detail["EnvironmentSubType"])
		copyTopologyString(normalized, "bindResourceType", detail["BindResourceType"])
		copyTopologyString(normalized, "managedType", detail["ManagedType"])
		copyTopologyString(normalized, "resourceGroupId", detail["ResourceGroupId"])
		copyTopologyString(normalized, "vpcId", detail["VpcId"])
		copyTopologyString(normalized, "vSwitchId", detail["VswitchId"])
		copyTopologyString(normalized, "securityGroupId", detail["SecurityGroupId"])
		return normalized
	})
}

func cloneInventoryItems(items []contracts.InventoryItem) ([]contracts.InventoryItem, error) {
	cloned := cloneTopologySlice(items)
	for index := range cloned {
		cloned[index].Tags = cloneTopologyStringMap(items[index].Tags)
		cloned[index].Normalized = cloneTopologyMap(items[index].Normalized)
		cloned[index].Raw = cloneTopologyMap(items[index].Raw)
		cloned[index].NativeAliases = cloneTopologySlice(items[index].NativeAliases)
		cloned[index].NetworkReferences = cloneTopologySlice(items[index].NetworkReferences)
		cloned[index].ResourceKind.ScopeKinds = cloneTopologySlice(items[index].ResourceKind.ScopeKinds)
		cloned[index].ResourceKind.Capabilities = cloneTopologySlice(items[index].ResourceKind.Capabilities)
		cloned[index].ResourceKind.DisplayNames = cloneTopologyStringMap(items[index].ResourceKind.DisplayNames)
		cloned[index].ResourceKind.FieldDisplayNames = cloneLocalizedFields(items[index].ResourceKind.FieldDisplayNames)
		cloned[index].ResourceKind.Properties = asset.CloneResourceProperties(items[index].ResourceKind.Properties)
		cloned[index].ResourceKind.SummaryFields = cloneTopologySlice(items[index].ResourceKind.SummaryFields)
	}
	return cloned, nil
}

func cloneTopologyMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	cloned := make(map[string]any, len(values))
	for key, value := range values {
		cloned[key] = cloneTopologyValue(value)
	}
	return cloned
}

func cloneTopologyStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]string, len(values))
	for key, value := range values {
		cloned[key] = value
	}
	return cloned
}

func cloneLocalizedFields(values map[string]map[string]string) map[string]map[string]string {
	if values == nil {
		return nil
	}
	cloned := make(map[string]map[string]string, len(values))
	for field, labels := range values {
		cloned[field] = cloneTopologyStringMap(labels)
	}
	return cloned
}

func cloneTopologyValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneTopologyMap(typed)
	case map[string]string:
		return cloneTopologyStringMap(typed)
	case []any:
		cloned := make([]any, len(typed))
		for index, item := range typed {
			cloned[index] = cloneTopologyValue(item)
		}
		return cloned
	case []string:
		return cloneTopologySlice(typed)
	default:
		return value
	}
}

func appendUniqueString(values []string, value string) []string {
	for _, existing := range values {
		if existing == value {
			return values
		}
	}
	return append(values, value)
}

func topologyDetailRecords(document map[string]any, path string) []any {
	if strings.TrimSpace(path) == "" || strings.TrimSpace(path) == "$" {
		return []any{document}
	}
	var current any = document
	for _, segment := range strings.Split(path, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		current, ok = object[segment]
		if !ok {
			return nil
		}
	}
	if object, ok := current.(map[string]any); ok {
		return []any{object}
	}
	return anySlice(current)
}

func enrichTopologyItems(
	items []contracts.InventoryItem,
	nativeType string,
	details map[string]map[string]any,
	normalize func(map[string]any) map[string]any,
) []contracts.InventoryItem {
	result := cloneTopologySlice(items)
	for index := range result {
		if result[index].NativeType != nativeType {
			continue
		}
		detail, ok := details[result[index].NativeID]
		if !ok {
			continue
		}
		merged := make(map[string]any, len(result[index].Normalized)+6)
		for key, value := range result[index].Normalized {
			merged[key] = value
		}
		for key, value := range normalize(detail) {
			merged[key] = value
		}
		result[index].Normalized = merged
	}
	return result
}

func cloneTopologySlice[T any](values []T) []T {
	if values == nil {
		return nil
	}
	cloned := make([]T, len(values))
	copy(cloned, values)
	return cloned
}

func copyTopologyString(target map[string]any, key string, value any) {
	if text, ok := nonEmptyString(value); ok {
		target[key] = text
	}
}

func copyTopologyStrings(target map[string]any, key string, value any) {
	values := make([]any, 0)
	for _, item := range anySlice(value) {
		if text, ok := nonEmptyString(item); ok {
			values = append(values, text)
		}
	}
	if len(values) > 0 {
		target[key] = values
	}
}

func copyTopologyValue(target map[string]any, key string, value any) {
	if value != nil {
		target[key] = value
	}
}

func nonEmptyString(value any) (string, bool) {
	text, ok := value.(string)
	text = strings.TrimSpace(text)
	return text, ok && text != ""
}

func anySlice(value any) []any {
	switch values := value.(type) {
	case []any:
		return values
	case []string:
		result := make([]any, len(values))
		for index := range values {
			result[index] = values[index]
		}
		return result
	default:
		return nil
	}
}
