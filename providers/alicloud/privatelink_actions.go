package alicloud

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	removeEndpointZoneOperation = "AlibabaCloud.PrivateLink.RemoveZoneFromVpcEndpoint"
	deleteEndpointOperation     = "AlibabaCloud.PrivateLink.DeleteVpcEndpoint"

	privateLinkPhaseAbsent       = "absent"
	privateLinkPhaseRemoveZone   = "remove_zone"
	privateLinkPhaseWaitENIs     = "wait_managed_enis"
	privateLinkPhaseDelete       = "delete_endpoint"
	privateLinkActionMaxENIBatch = 100
	privateLinkDeletionTimeout   = 2 * time.Minute
)

type PrivateLinkEndpointAction struct {
	provider     contracts.Provider
	connectionID asset.ConnectionID
	region       string
}

type privateLinkEndpointState struct {
	exists    bool
	status    string
	requestID string
	data      map[string]any
}

type privateLinkEndpointZone struct {
	zoneID     string
	eniID      string
	zoneStatus string
}

func NewPrivateLinkEndpointHook(
	provider contracts.Provider,
	connectionID asset.ConnectionID,
	region string,
) (*PrivateLinkEndpointAction, error) {
	if provider == nil || provider.Provider() != asset.ProviderAliCloud {
		return nil, fmt.Errorf("Alibaba Cloud provider runtime is required")
	}
	if connectionID == "" || strings.TrimSpace(region) == "" {
		return nil, fmt.Errorf("Alibaba Cloud PrivateLink endpoint action requires connection ID and region")
	}
	return &PrivateLinkEndpointAction{
		provider: provider, connectionID: connectionID, region: strings.TrimSpace(region),
	}, nil
}

// DeletionCheckTimeout allows the endpoint action to remove zones and wait for
// its service-managed ENIs without inheriting the generic short polling budget.
func (*PrivateLinkEndpointAction) DeletionCheckTimeout() time.Duration {
	return privateLinkDeletionTimeout
}

func (a *PrivateLinkEndpointAction) Preflight(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.PreflightResult, error) {
	if err := validateComplexDelete(request, endpointNativeType); err != nil {
		return contracts.PreflightResult{}, err
	}
	endpoint, err := a.readEndpoint(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	evidence := map[string]any{
		"endpoint_id": request.Asset.Identity.NativeID,
		"state":       endpoint.status, "provider_request_id": endpoint.requestID,
	}
	if !endpoint.exists {
		return contracts.PreflightResult{
			Absent: true, Reason: "resource no longer exists", Evidence: evidence,
		}, nil
	}
	if deletionInProgressState(endpoint.status) {
		return contracts.PreflightResult{
			Absent: true, Reason: "resource deletion is already in progress", Evidence: evidence,
		}, nil
	}
	zones, requestID, endpointExists, err := a.listEndpointZones(
		ctx,
		request.Asset.Identity.NativeID,
	)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if !endpointExists {
		return contracts.PreflightResult{
			Absent: true, Reason: "resource no longer exists", Evidence: evidence,
		}, nil
	}
	evidence["zone_request_id"] = requestID
	evidence["zone_ids"] = privateLinkZoneIDs(zones)
	evidence["network_interface_ids"] = mergePrivateLinkENIIDs(
		normalizedActionStrings(request.Asset.Normalized[NormalizedPrivateLinkENIIDsField]),
		privateLinkZoneENIIDs(zones),
	)
	if deleting, exists := privateLinkDeletingZone(zones); exists {
		evidence["active_zone_id"] = deleting.zoneID
		evidence["next_operation"] = listEndpointZonesOperation
	} else if len(zones) > 0 {
		evidence["next_operation"] = removeEndpointZoneOperation
	} else {
		evidence["next_operation"] = deleteEndpointOperation
	}
	return contracts.PreflightResult{Allowed: true, Evidence: evidence}, nil
}

func (a *PrivateLinkEndpointAction) Execute(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	if err := validateComplexDelete(request, endpointNativeType); err != nil {
		return contracts.ActionResult{}, err
	}
	endpoint, err := a.readEndpoint(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !endpoint.exists {
		return contracts.ActionResult{
			ProviderRequestID: endpoint.requestID,
			Data:              map[string]any{"phase": privateLinkPhaseAbsent},
		}, nil
	}
	zones, _, endpointExists, err := a.listEndpointZones(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !endpointExists {
		return contracts.ActionResult{
			Data: map[string]any{"phase": privateLinkPhaseAbsent},
		}, nil
	}
	eniIDs := mergePrivateLinkENIIDs(
		normalizedActionStrings(request.Asset.Normalized[NormalizedPrivateLinkENIIDsField]),
		privateLinkZoneENIIDs(zones),
	)
	if deleting, exists := privateLinkDeletingZone(zones); exists {
		return contracts.ActionResult{
			Data:       privateLinkRemovalData(deleting, eniIDs, nil),
			RetryAfter: actionReadbackInterval,
		}, nil
	}
	if len(zones) > 0 {
		result, data, err := a.removeEndpointZone(ctx, request, zones[0], eniIDs, nil)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		return contracts.ActionResult{
			ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
			Data: data, RetryAfter: actionReadbackInterval,
		}, nil
	}
	pending, requestIDs, err := a.pendingNetworkInterfaces(ctx, eniIDs)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if len(pending) > 0 {
		return contracts.ActionResult{
			Data: map[string]any{
				"phase": privateLinkPhaseWaitENIs, "network_interface_ids": eniIDs,
				"pending_network_interface_ids": pending, "eni_request_ids": requestIDs,
			},
			RetryAfter: actionReadbackInterval,
		}, nil
	}
	return a.deleteEndpoint(ctx, request, eniIDs, nil)
}

func (a *PrivateLinkEndpointAction) Wait(
	ctx context.Context,
	request contracts.ActionRequest,
	result contracts.ActionResult,
) (contracts.WaitResult, error) {
	if err := validateComplexDelete(request, endpointNativeType); err != nil {
		return contracts.WaitResult{}, err
	}
	phase := strings.TrimSpace(stringValue(result.Data["phase"]))
	if phase == privateLinkPhaseAbsent {
		return contracts.WaitResult{Done: true, State: privateLinkPhaseAbsent}, nil
	}
	eniIDs := mergePrivateLinkENIIDs(
		normalizedActionStrings(request.Asset.Normalized[NormalizedPrivateLinkENIIDsField]),
		normalizedActionStrings(result.Data["network_interface_ids"]),
	)
	if phase == privateLinkPhaseDelete {
		endpoint, err := a.readEndpoint(ctx, request.Asset.Identity.NativeID)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if endpoint.exists {
			return contracts.WaitResult{
				Done: false, RetryAfter: actionReadbackInterval, State: endpoint.status,
				Data: result.Data,
			}, nil
		}
		pending, requestIDs, err := a.pendingNetworkInterfaces(ctx, eniIDs)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if len(pending) > 0 {
			data := cloneTopologyMap(result.Data)
			data["pending_network_interface_ids"] = pending
			data["eni_request_ids"] = requestIDs
			return contracts.WaitResult{
				Done: false, RetryAfter: actionReadbackInterval,
				State: "managed_enis_pending", Data: data,
			}, nil
		}
		return contracts.WaitResult{Done: true, State: privateLinkPhaseAbsent, Data: result.Data}, nil
	}

	zones, _, endpointExists, err := a.listEndpointZones(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !endpointExists {
		pending, requestIDs, err := a.pendingNetworkInterfaces(ctx, eniIDs)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if len(pending) == 0 {
			return contracts.WaitResult{Done: true, State: privateLinkPhaseAbsent, Data: result.Data}, nil
		}
		data := cloneTopologyMap(result.Data)
		data["pending_network_interface_ids"] = pending
		data["eni_request_ids"] = requestIDs
		return contracts.WaitResult{
			Done: false, RetryAfter: actionReadbackInterval,
			State: "managed_enis_pending", Data: data,
		}, nil
	}
	eniIDs = mergePrivateLinkENIIDs(eniIDs, privateLinkZoneENIIDs(zones))
	activeZoneID := strings.TrimSpace(stringValue(result.Data["active_zone_id"]))
	activeENIID := strings.TrimSpace(stringValue(result.Data["active_eni_id"]))
	if activeZoneID != "" && privateLinkZoneExists(zones, activeZoneID) {
		return contracts.WaitResult{
			Done: false, RetryAfter: actionReadbackInterval,
			State: "removing_zone:" + activeZoneID, Data: result.Data,
		}, nil
	}
	if activeENIID != "" {
		pending, requestIDs, err := a.pendingNetworkInterfaces(ctx, []string{activeENIID})
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if len(pending) > 0 {
			data := cloneTopologyMap(result.Data)
			data["eni_request_ids"] = requestIDs
			return contracts.WaitResult{
				Done: false, RetryAfter: actionReadbackInterval,
				State: "managed_eni_pending:" + activeENIID, Data: data,
			}, nil
		}
	}
	if deleting, exists := privateLinkDeletingZone(zones); exists {
		data := privateLinkRemovalData(deleting, eniIDs, result.Data)
		return contracts.WaitResult{
			Done: false, RetryAfter: actionReadbackInterval,
			State: "removing_zone:" + deleting.zoneID, Data: data,
		}, nil
	}
	if len(zones) > 0 {
		invocation, data, err := a.removeEndpointZone(
			ctx,
			request,
			zones[0],
			eniIDs,
			result.Data,
		)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		data["last_provider_request_id"] = invocation.RequestID
		return contracts.WaitResult{
			Done: false, RetryAfter: actionReadbackInterval,
			State: "removing_zone:" + zones[0].zoneID, Data: data,
		}, nil
	}
	pending, requestIDs, err := a.pendingNetworkInterfaces(ctx, eniIDs)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if len(pending) > 0 {
		data := cloneTopologyMap(result.Data)
		data["phase"] = privateLinkPhaseWaitENIs
		data["network_interface_ids"] = eniIDs
		data["pending_network_interface_ids"] = pending
		data["eni_request_ids"] = requestIDs
		delete(data, "active_zone_id")
		delete(data, "active_eni_id")
		return contracts.WaitResult{
			Done: false, RetryAfter: actionReadbackInterval,
			State: "managed_enis_pending", Data: data,
		}, nil
	}
	deleted, err := a.deleteEndpoint(ctx, request, eniIDs, result.Data)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	return contracts.WaitResult{
		Done: false, RetryAfter: deleted.RetryAfter,
		State: "endpoint_delete_requested", Data: deleted.Data,
	}, nil
}

func (a *PrivateLinkEndpointAction) Readback(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ReadbackResult, error) {
	if err := validateComplexDelete(request, endpointNativeType); err != nil {
		return contracts.ReadbackResult{}, err
	}
	endpoint, err := a.readEndpoint(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if endpoint.exists {
		return contracts.ReadbackResult{
			Exists: true, State: endpoint.status, Data: endpoint.data,
		}, nil
	}
	eniIDs := normalizedActionStrings(
		request.Asset.Normalized[NormalizedPrivateLinkENIIDsField],
	)
	pending, requestIDs, err := a.pendingNetworkInterfaces(ctx, eniIDs)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if len(pending) > 0 {
		return contracts.ReadbackResult{
			Exists: true, State: "managed_enis_pending",
			Data: map[string]any{
				"pending_network_interface_ids": pending, "eni_request_ids": requestIDs,
			},
		}, nil
	}
	return contracts.ReadbackResult{Exists: false, State: privateLinkPhaseAbsent}, nil
}

func (a *PrivateLinkEndpointAction) readEndpoint(
	ctx context.Context,
	endpointID string,
) (privateLinkEndpointState, error) {
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: a.connectionID,
		Operation:    "AlibabaCloud.PrivateLink.ListVpcEndpoints",
		Scope:        map[string]string{"region": a.region},
		Parameters: map[string]any{
			"RegionId": a.region, "EndpointId": endpointID, "MaxResults": 1,
		},
	})
	if isNotFound(err) {
		return privateLinkEndpointState{}, nil
	}
	if err != nil {
		return privateLinkEndpointState{}, err
	}
	resource, exists, err := readbackResource(
		result.Data,
		[]string{"Endpoints"},
		"EndpointId",
		endpointID,
	)
	if err != nil {
		return privateLinkEndpointState{}, err
	}
	if !exists {
		return privateLinkEndpointState{requestID: result.RequestID, data: result.Data}, nil
	}
	return privateLinkEndpointState{
		exists: true, status: strings.TrimSpace(stringValue(resource["EndpointStatus"])),
		requestID: result.RequestID, data: result.Data,
	}, nil
}

func (a *PrivateLinkEndpointAction) listEndpointZones(
	ctx context.Context,
	endpointID string,
) ([]privateLinkEndpointZone, string, bool, error) {
	zonesByID := make(map[string]privateLinkEndpointZone)
	nextToken := ""
	lastRequestID := ""
	for page := 0; page < 1000; page++ {
		parameters := map[string]any{
			"RegionId": a.region, "EndpointId": endpointID,
			"MaxResults": privateLinkEndpointZonePageSize,
		}
		if nextToken != "" {
			parameters["NextToken"] = nextToken
		}
		result, err := a.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: a.connectionID, Operation: listEndpointZonesOperation,
			Scope: map[string]string{"region": a.region}, Parameters: parameters,
		})
		if isNotFound(err) {
			return nil, lastRequestID, false, nil
		}
		if err != nil {
			return nil, "", false, err
		}
		lastRequestID = result.RequestID
		rawValue := valueAtPath(result.Data, "Zones")
		rawZones, ok := productAPIListValue(rawValue)
		if !ok && nilLikeValue(rawValue) {
			rawZones, ok = []any{}, true
		}
		if !ok {
			return nil, "", false, fmt.Errorf(
				"Alibaba Cloud ListVpcEndpointZones response for endpoint %q has no zone list",
				endpointID,
			)
		}
		for index, raw := range rawZones {
			record, ok := raw.(map[string]any)
			if !ok {
				return nil, "", false, fmt.Errorf(
					"Alibaba Cloud ListVpcEndpointZones response zone %d for endpoint %q has type %T",
					index,
					endpointID,
					raw,
				)
			}
			zoneID := strings.TrimSpace(stringValue(record["ZoneId"]))
			if zoneID == "" {
				return nil, "", false, fmt.Errorf(
					"Alibaba Cloud ListVpcEndpointZones response zone %d for endpoint %q has no ZoneId",
					index,
					endpointID,
				)
			}
			zonesByID[zoneID] = privateLinkEndpointZone{
				zoneID: zoneID, eniID: strings.TrimSpace(stringValue(record["EniId"])),
				zoneStatus: strings.TrimSpace(stringValue(record["ZoneStatus"])),
			}
		}
		next := strings.TrimSpace(stringValue(result.Data["NextToken"]))
		if next == "" {
			next = strings.TrimSpace(result.NextToken)
		}
		if next == "" {
			break
		}
		if next == nextToken {
			return nil, "", false, fmt.Errorf(
				"Alibaba Cloud ListVpcEndpointZones returned repeated token %q for endpoint %q",
				next,
				endpointID,
			)
		}
		nextToken = next
		if page == 999 {
			return nil, "", false, fmt.Errorf(
				"Alibaba Cloud ListVpcEndpointZones exceeded 1000 pages for endpoint %q",
				endpointID,
			)
		}
	}
	zones := make([]privateLinkEndpointZone, 0, len(zonesByID))
	for _, zone := range zonesByID {
		zones = append(zones, zone)
	}
	sort.Slice(zones, func(i, j int) bool { return zones[i].zoneID < zones[j].zoneID })
	return zones, lastRequestID, true, nil
}

func (a *PrivateLinkEndpointAction) removeEndpointZone(
	ctx context.Context,
	request contracts.ActionRequest,
	zone privateLinkEndpointZone,
	eniIDs []string,
	previous map[string]any,
) (contracts.InvocationResult, map[string]any, error) {
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: a.connectionID, Operation: removeEndpointZoneOperation,
		Scope: map[string]string{"region": a.region},
		Parameters: map[string]any{
			"RegionId": a.region, "EndpointId": request.Asset.Identity.NativeID,
			"ZoneId": zone.zoneID,
		},
		IdempotencyKey: request.IdempotencyKey + ":zone:" + zone.zoneID,
	})
	if err != nil {
		return contracts.InvocationResult{}, nil, err
	}
	data := privateLinkRemovalData(zone, eniIDs, previous)
	requests, _ := data["zone_request_ids"].(map[string]any)
	requests = cloneTopologyMap(requests)
	if requests == nil {
		requests = make(map[string]any)
	}
	requests[zone.zoneID] = result.RequestID
	data["zone_request_ids"] = requests
	return result, data, nil
}

func (a *PrivateLinkEndpointAction) deleteEndpoint(
	ctx context.Context,
	request contracts.ActionRequest,
	eniIDs []string,
	previous map[string]any,
) (contracts.ActionResult, error) {
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: a.connectionID, Operation: deleteEndpointOperation,
		Scope: map[string]string{"region": a.region},
		Parameters: map[string]any{
			"RegionId": a.region, "EndpointId": request.Asset.Identity.NativeID,
		},
		IdempotencyKey: request.IdempotencyKey,
	})
	if isNotFound(err) {
		return contracts.ActionResult{
			Data: map[string]any{"phase": privateLinkPhaseAbsent},
		}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := cloneTopologyMap(previous)
	if data == nil {
		data = make(map[string]any)
	}
	data["phase"] = privateLinkPhaseDelete
	data["network_interface_ids"] = eniIDs
	data["delete_request_id"] = result.RequestID
	delete(data, "active_zone_id")
	delete(data, "active_eni_id")
	delete(data, "pending_network_interface_ids")
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
		Data: data, RetryAfter: actionReadbackInterval,
	}, nil
}

func (a *PrivateLinkEndpointAction) pendingNetworkInterfaces(
	ctx context.Context,
	ids []string,
) ([]string, map[string]string, error) {
	ids = normalizedActionStrings(ids)
	pending := make(map[string]struct{})
	requestIDs := make(map[string]string)
	for start := 0; start < len(ids); start += privateLinkActionMaxENIBatch {
		end := start + privateLinkActionMaxENIBatch
		if end > len(ids) {
			end = len(ids)
		}
		batch := ids[start:end]
		result, err := a.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: a.connectionID, Operation: "AlibabaCloud.DescribeNetworkInterfaces",
			Scope: map[string]string{"region": a.region},
			Parameters: map[string]any{
				"RegionId": a.region, "NetworkInterfaceId": batch, "MaxResults": len(batch),
			},
		})
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		rawValue := valueAtPath(result.Data, "NetworkInterfaceSets.NetworkInterfaceSet")
		records, ok := productAPIListValue(rawValue)
		if !ok && nilLikeValue(rawValue) {
			records, ok = []any{}, true
		}
		if !ok {
			return nil, nil, fmt.Errorf("Alibaba Cloud DescribeNetworkInterfaces response has no network interface list")
		}
		requested := make(map[string]struct{}, len(batch))
		for _, id := range batch {
			requested[id] = struct{}{}
		}
		for index, raw := range records {
			record, ok := raw.(map[string]any)
			if !ok {
				return nil, nil, fmt.Errorf(
					"Alibaba Cloud DescribeNetworkInterfaces response item %d has type %T",
					index,
					raw,
				)
			}
			id := strings.TrimSpace(stringValue(record["NetworkInterfaceId"]))
			if _, wanted := requested[id]; !wanted {
				continue
			}
			pending[id] = struct{}{}
			requestIDs[id] = result.RequestID
		}
	}
	result := make([]string, 0, len(pending))
	for id := range pending {
		result = append(result, id)
	}
	sort.Strings(result)
	return result, requestIDs, nil
}

func privateLinkZoneIDs(zones []privateLinkEndpointZone) []string {
	result := make([]string, 0, len(zones))
	for _, zone := range zones {
		result = append(result, zone.zoneID)
	}
	return result
}

func privateLinkZoneENIIDs(zones []privateLinkEndpointZone) []string {
	result := make([]string, 0, len(zones))
	for _, zone := range zones {
		if zone.eniID != "" {
			result = append(result, zone.eniID)
		}
	}
	return normalizedActionStrings(result)
}

func privateLinkZoneExists(zones []privateLinkEndpointZone, zoneID string) bool {
	for _, zone := range zones {
		if zone.zoneID == zoneID {
			return true
		}
	}
	return false
}

func privateLinkDeletingZone(zones []privateLinkEndpointZone) (privateLinkEndpointZone, bool) {
	for _, zone := range zones {
		if strings.EqualFold(zone.zoneStatus, "Deleting") {
			return zone, true
		}
	}
	return privateLinkEndpointZone{}, false
}

func privateLinkRemovalData(
	zone privateLinkEndpointZone,
	eniIDs []string,
	previous map[string]any,
) map[string]any {
	data := cloneTopologyMap(previous)
	if data == nil {
		data = make(map[string]any)
	}
	data["phase"] = privateLinkPhaseRemoveZone
	data["active_zone_id"] = zone.zoneID
	data["active_eni_id"] = zone.eniID
	data["network_interface_ids"] = eniIDs
	return data
}

func mergePrivateLinkENIIDs(groups ...[]string) []string {
	values := make([]string, 0)
	for _, group := range groups {
		values = append(values, group...)
	}
	return normalizedActionStrings(values)
}
