package alicloud

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	listEndpointZonesOperation              = "AlibabaCloud.PrivateLink.ListVpcEndpointZones"
	listEndpointServiceResourcesOperation   = "AlibabaCloud.PrivateLink.ListVpcEndpointServiceResources"
	NormalizedPrivateLinkEndpointZonesField = "_privatelink_endpoint_zones"
	NormalizedPrivateLinkENIIDsField        = "network_interface_ids"
	normalizedEndpointServiceResourcesField = "_privatelink_service_resources"
	normalizedEndpointServiceSLBIDs         = "slb_load_balancer_ids"
	normalizedEndpointServiceALBIDs         = "alb_load_balancer_ids"
	normalizedEndpointServiceNLBIDs         = "nlb_load_balancer_ids"
	normalizedEndpointServiceGWLBIDs        = "gwlb_load_balancer_ids"
	normalizedEndpointServiceNATGatewayIDs  = "nat_gateway_ids"
	privateLinkEndpointZonePageSize         = 1000
	endpointServiceResourcePageSize         = 50
)

func (r *Runtime) enrichPrivateLinkEndpointTopology(
	ctx context.Context,
	request contracts.InventoryRequest,
	items []contracts.InventoryItem,
) ([]contracts.InventoryItem, error) {
	indices := make([]int, 0)
	for index := range items {
		if items[index].NativeType == endpointNativeType &&
			strings.TrimSpace(items[index].NativeID) != "" {
			indices = append(indices, index)
		}
	}
	if len(indices) == 0 {
		return items, nil
	}
	region, err := inventoryRegion(request)
	if err != nil {
		return nil, err
	}
	credential, err := r.resolveCredential(ctx, request.ConnectionID)
	if err != nil {
		return nil, err
	}
	operation, ok := r.catalog.Operation(listEndpointZonesOperation)
	if !ok {
		return nil, fmt.Errorf(
			"Alibaba Cloud operation %q is not in the generated catalog",
			listEndpointZonesOperation,
		)
	}

	for _, index := range indices {
		endpointID := strings.TrimSpace(items[index].NativeID)
		zones, eniIDs, err := r.loadPrivateLinkEndpointZones(
			ctx,
			request,
			credential,
			region,
			endpointID,
			operation,
		)
		if err != nil {
			return nil, err
		}
		if items[index].Normalized == nil {
			items[index].Normalized = make(map[string]any)
		}
		items[index].Normalized[NormalizedPrivateLinkEndpointZonesField] = zones
		if len(eniIDs) > 0 {
			items[index].Normalized[NormalizedPrivateLinkENIIDsField] = eniIDs
			items[index].NetworkReferences = appendUniqueReferences(
				items[index].NetworkReferences,
				eniIDs,
			)
		}
	}
	return items, nil
}

func (r *Runtime) loadPrivateLinkEndpointZones(
	ctx context.Context,
	request contracts.InventoryRequest,
	credential contracts.Credential,
	region string,
	endpointID string,
	operation catalog.Operation,
) ([]any, []string, error) {
	zonesByID := make(map[string]map[string]any)
	apiService := cloudAPIService(operation)
	operationName := operation.Name
	nextToken := ""
	for page := 0; page < 1000; page++ {
		parameters := map[string]any{
			"RegionId":   region,
			"EndpointId": endpointID,
			"MaxResults": privateLinkEndpointZonePageSize,
		}
		if nextToken != "" {
			parameters["NextToken"] = nextToken
		}
		execution.LogCloudAPIRequest(ctx, apiService, operationName, rawCloudPayload(parameters))
		result, err := r.factory.Invoke(ctx, credential, region, operation, contracts.Invocation{
			ConnectionID: request.ConnectionID,
			Operation:    operation.Key(),
			Scope:        map[string]string{"region": region},
			Parameters:   parameters,
		})
		if err != nil {
			LogCloudAPIError(ctx, apiService, operationName, err)
			normalized := NormalizeError(err)
			if isNotFound(normalized) {
				return []any{}, nil, nil
			}
			return nil, nil, fmt.Errorf(
				"%s %s failed for endpoint %q: %w",
				apiService,
				operationName,
				endpointID,
				normalized,
			)
		}
		responsePayload := rawCloudPayload(result.Data)
		if result.RequestID != "" {
			responsePayload["RequestId"] = result.RequestID
		}
		execution.LogCloudAPIResponse(ctx, apiService, operationName, responsePayload)

		rawZoneValue := valueAtPath(result.Data, "Zones")
		rawZones, ok := productAPIListValue(rawZoneValue)
		if !ok && nilLikeValue(rawZoneValue) {
			rawZones, ok = []any{}, true
		}
		if !ok {
			return nil, nil, fmt.Errorf(
				"%s %s response for endpoint %q has no zone list",
				apiService,
				operationName,
				endpointID,
			)
		}
		for index, raw := range rawZones {
			zone, ok := raw.(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf(
					"%s %s response zone %d for endpoint %q has type %T",
					apiService,
					operationName,
					index,
					endpointID,
					raw,
				)
			}
			zoneID := strings.TrimSpace(stringValue(zone["ZoneId"]))
			if zoneID == "" {
				return nil, nil, fmt.Errorf(
					"%s %s response zone %d for endpoint %q has no ZoneId",
					apiService,
					operationName,
					index,
					endpointID,
				)
			}
			zonesByID[zoneID] = normalizedPrivateLinkEndpointZone(zone, result.RequestID)
		}
		next := strings.TrimSpace(stringValue(result.Data["NextToken"]))
		if next == "" {
			next = strings.TrimSpace(result.NextToken)
		}
		if next == "" {
			break
		}
		if next == nextToken {
			return nil, nil, fmt.Errorf(
				"%s %s returned repeated token %q for endpoint %q",
				apiService,
				operationName,
				next,
				endpointID,
			)
		}
		nextToken = next
		if page == 999 {
			return nil, nil, fmt.Errorf(
				"%s %s exceeded 1000 pages for endpoint %q",
				apiService,
				operationName,
				endpointID,
			)
		}
	}

	zoneIDs := make([]string, 0, len(zonesByID))
	for zoneID := range zonesByID {
		zoneIDs = append(zoneIDs, zoneID)
	}
	sort.Strings(zoneIDs)
	zones := make([]any, 0, len(zoneIDs))
	eniSet := make(map[string]struct{})
	for _, zoneID := range zoneIDs {
		zone := zonesByID[zoneID]
		zones = append(zones, zone)
		if eniID := strings.TrimSpace(stringValue(zone["eni_id"])); eniID != "" {
			eniSet[eniID] = struct{}{}
		}
	}
	eniIDs := sortedEndpointServiceResourceIDs(eniSet)
	return zones, eniIDs, nil
}

func normalizedPrivateLinkEndpointZone(zone map[string]any, requestID string) map[string]any {
	return map[string]any{
		"zone_id":        strings.TrimSpace(stringValue(zone["ZoneId"])),
		"zone_status":    strings.TrimSpace(stringValue(zone["ZoneStatus"])),
		"service_status": strings.TrimSpace(stringValue(zone["ServiceStatus"])),
		"vswitch_id":     strings.TrimSpace(stringValue(zone["VSwitchId"])),
		"eni_id":         strings.TrimSpace(stringValue(zone["EniId"])),
		"eni_ip":         strings.TrimSpace(stringValue(zone["EniIp"])),
		"request_id":     strings.TrimSpace(requestID),
	}
}

func (r *Runtime) enrichEndpointServiceTopology(
	ctx context.Context,
	request contracts.InventoryRequest,
	items []contracts.InventoryItem,
) ([]contracts.InventoryItem, error) {
	indices := make([]int, 0)
	for index := range items {
		if items[index].NativeType == endpointServiceNativeType &&
			strings.TrimSpace(items[index].NativeID) != "" {
			indices = append(indices, index)
		}
	}
	if len(indices) == 0 {
		return items, nil
	}
	region, err := inventoryRegion(request)
	if err != nil {
		return nil, err
	}
	credential, err := r.resolveCredential(ctx, request.ConnectionID)
	if err != nil {
		return nil, err
	}
	operation, ok := r.catalog.Operation(listEndpointServiceResourcesOperation)
	if !ok {
		return nil, fmt.Errorf(
			"Alibaba Cloud operation %q is not in the generated catalog",
			listEndpointServiceResourcesOperation,
		)
	}

	for _, index := range indices {
		serviceID := strings.TrimSpace(items[index].NativeID)
		resources, err := r.loadEndpointServiceResources(
			ctx,
			request,
			credential,
			region,
			serviceID,
			operation,
		)
		if err != nil {
			return nil, err
		}
		if items[index].Normalized == nil {
			items[index].Normalized = make(map[string]any)
		}
		items[index].Normalized[normalizedEndpointServiceResourcesField] = resources.records
		for resourceType, field := range endpointServiceResourceFields {
			if ids := resources.ids(resourceType); len(ids) > 0 {
				items[index].Normalized[field] = ids
				items[index].NetworkReferences = appendUniqueReferences(
					items[index].NetworkReferences,
					ids,
				)
			}
		}
	}
	return items, nil
}

var endpointServiceResourceFields = map[string]string{
	"slb":    normalizedEndpointServiceSLBIDs,
	"alb":    normalizedEndpointServiceALBIDs,
	"nlb":    normalizedEndpointServiceNLBIDs,
	"gwlb":   normalizedEndpointServiceGWLBIDs,
	"vpcnat": normalizedEndpointServiceNATGatewayIDs,
}

type endpointServiceResources struct {
	byType  map[string]map[string]struct{}
	records []any
}

func (r endpointServiceResources) ids(resourceType string) []string {
	return sortedEndpointServiceResourceIDs(r.byType[resourceType])
}

func (r *Runtime) loadEndpointServiceResources(
	ctx context.Context,
	request contracts.InventoryRequest,
	credential contracts.Credential,
	region string,
	serviceID string,
	operation catalog.Operation,
) (endpointServiceResources, error) {
	resources := endpointServiceResources{byType: make(map[string]map[string]struct{})}
	seenRecords := make(map[string]struct{})
	apiService := cloudAPIService(operation)
	operationName := operation.Name
	nextToken := ""
	for page := 0; page < 1000; page++ {
		parameters := map[string]any{
			"RegionId":   region,
			"ServiceId":  serviceID,
			"MaxResults": endpointServiceResourcePageSize,
		}
		if nextToken != "" {
			parameters["NextToken"] = nextToken
		}
		execution.LogCloudAPIRequest(ctx, apiService, operationName, rawCloudPayload(parameters))
		result, err := r.factory.Invoke(ctx, credential, region, operation, contracts.Invocation{
			ConnectionID: request.ConnectionID,
			Operation:    operation.Key(),
			Scope:        map[string]string{"region": region},
			Parameters:   parameters,
		})
		if err != nil {
			LogCloudAPIError(ctx, apiService, operationName, err)
			normalized := NormalizeError(err)
			if isNotFound(normalized) {
				return resources, nil
			}
			return endpointServiceResources{}, fmt.Errorf(
				"%s %s failed for endpoint service %q: %w",
				apiService,
				operationName,
				serviceID,
				normalized,
			)
		}
		responsePayload := rawCloudPayload(result.Data)
		if result.RequestID != "" {
			responsePayload["RequestId"] = result.RequestID
		}
		execution.LogCloudAPIResponse(ctx, apiService, operationName, responsePayload)

		rawResourceValue := valueAtPath(result.Data, "Resources")
		rawResources, ok := productAPIListValue(rawResourceValue)
		if !ok && nilLikeValue(rawResourceValue) {
			rawResources, ok = []any{}, true
		}
		if !ok {
			return endpointServiceResources{}, fmt.Errorf(
				"%s %s response for endpoint service %q has no resource list",
				apiService,
				operationName,
				serviceID,
			)
		}
		for index, raw := range rawResources {
			resource, ok := raw.(map[string]any)
			if !ok {
				return endpointServiceResources{}, fmt.Errorf(
					"%s %s response resource %d for endpoint service %q has type %T",
					apiService,
					operationName,
					index,
					serviceID,
					raw,
				)
			}
			resourceID := strings.TrimSpace(stringValue(resource["ResourceId"]))
			resourceType := strings.ToLower(strings.TrimSpace(stringValue(resource["ResourceType"])))
			if resourceID == "" || resourceType == "" {
				return endpointServiceResources{}, fmt.Errorf(
					"%s %s response resource %d for endpoint service %q has no resource identity",
					apiService,
					operationName,
					index,
					serviceID,
				)
			}
			if _, supported := endpointServiceResourceFields[resourceType]; supported {
				if resources.byType[resourceType] == nil {
					resources.byType[resourceType] = make(map[string]struct{})
				}
				resources.byType[resourceType][resourceID] = struct{}{}
			}
			recordKey := resourceType + "\x00" + resourceID + "\x00" +
				strings.TrimSpace(stringValue(resource["ZoneId"]))
			if _, exists := seenRecords[recordKey]; !exists {
				seenRecords[recordKey] = struct{}{}
				resources.records = append(resources.records, map[string]any{
					"resource_id":   resourceID,
					"resource_type": resourceType,
					"vpc_id":        strings.TrimSpace(stringValue(resource["VpcId"])),
					"vswitch_id":    strings.TrimSpace(stringValue(resource["VSwitchId"])),
					"zone_id":       strings.TrimSpace(stringValue(resource["ZoneId"])),
					"request_id":    strings.TrimSpace(result.RequestID),
				})
			}
		}
		next := strings.TrimSpace(stringValue(result.Data["NextToken"]))
		if next == "" {
			next = strings.TrimSpace(result.NextToken)
		}
		if next == "" {
			sort.Slice(resources.records, func(i, j int) bool {
				left := resources.records[i].(map[string]any)
				right := resources.records[j].(map[string]any)
				leftKey := stringValue(left["resource_type"]) + "\x00" +
					stringValue(left["resource_id"]) + "\x00" + stringValue(left["zone_id"])
				rightKey := stringValue(right["resource_type"]) + "\x00" +
					stringValue(right["resource_id"]) + "\x00" + stringValue(right["zone_id"])
				return leftKey < rightKey
			})
			return resources, nil
		}
		if next == nextToken {
			return endpointServiceResources{}, fmt.Errorf(
				"%s %s returned repeated token %q for endpoint service %q",
				apiService,
				operationName,
				next,
				serviceID,
			)
		}
		nextToken = next
	}
	return endpointServiceResources{}, fmt.Errorf(
		"%s %s exceeded 1000 pages for endpoint service %q",
		apiService,
		operationName,
		serviceID,
	)
}

func sortedEndpointServiceResourceIDs(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}
