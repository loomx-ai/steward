package alicloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	VPCGatewayEndpointNativeType = "ACS::VPC::GatewayEndpoint"

	vpcGatewayEndpointListOperation       = "AlibabaCloud.ListVpcGatewayEndpoints"
	vpcGatewayEndpointDissociateOperation = "AlibabaCloud.DissociateRouteTablesFromVpcGatewayEndpoint"
	vpcGatewayEndpointDeleteOperation     = "AlibabaCloud.DeleteVpcGatewayEndpoint"

	vpcGatewayEndpointPhaseAbsent     = "absent"
	vpcGatewayEndpointPhaseDissociate = "dissociate_route_tables"
	vpcGatewayEndpointPhaseDelete     = "delete_gateway_endpoint"
	vpcGatewayEndpointRouteTableBatch = 20
	vpcGatewayEndpointDeletionTimeout = 2 * time.Minute
)

type VPCGatewayEndpointAction struct {
	provider     contracts.Provider
	connectionID asset.ConnectionID
	region       string
}

type vpcGatewayEndpointState struct {
	exists        bool
	status        string
	routeTableIDs []string
	requestID     string
	data          map[string]any
}

func NewVPCGatewayEndpointHook(
	provider contracts.Provider,
	connectionID asset.ConnectionID,
	region string,
) (*VPCGatewayEndpointAction, error) {
	if provider == nil || provider.Provider() != asset.ProviderAliCloud {
		return nil, fmt.Errorf("Alibaba Cloud provider runtime is required")
	}
	if connectionID == "" || strings.TrimSpace(region) == "" {
		return nil, fmt.Errorf("Alibaba Cloud VPC gateway endpoint action requires connection ID and region")
	}
	return &VPCGatewayEndpointAction{
		provider: provider, connectionID: connectionID, region: strings.TrimSpace(region),
	}, nil
}

func (*VPCGatewayEndpointAction) DeletionCheckTimeout() time.Duration {
	return vpcGatewayEndpointDeletionTimeout
}

func (a *VPCGatewayEndpointAction) Preflight(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.PreflightResult, error) {
	if err := validateComplexDelete(request, VPCGatewayEndpointNativeType); err != nil {
		return contracts.PreflightResult{}, err
	}
	endpoint, err := a.readEndpoint(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	evidence := map[string]any{
		"endpoint_id": request.Asset.Identity.NativeID,
		"state":       endpoint.status, "provider_request_id": endpoint.requestID,
		"associated_route_table_ids": append([]string(nil), endpoint.routeTableIDs...),
	}
	if !endpoint.exists {
		return contracts.PreflightResult{
			Absent: true, Reason: "resource no longer exists", Evidence: evidence,
		}, nil
	}
	switch {
	case deletionInProgressState(endpoint.status):
		evidence["next_operation"] = vpcGatewayEndpointListOperation
	case gatewayEndpointTransitionInProgress(endpoint.status):
		evidence["next_operation"] = vpcGatewayEndpointListOperation
	case len(endpoint.routeTableIDs) > 0:
		evidence["next_operation"] = vpcGatewayEndpointDissociateOperation
	default:
		evidence["next_operation"] = vpcGatewayEndpointDeleteOperation
	}
	return contracts.PreflightResult{Allowed: true, Evidence: evidence}, nil
}

func (a *VPCGatewayEndpointAction) Execute(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, error) {
	if err := validateComplexDelete(request, VPCGatewayEndpointNativeType); err != nil {
		return contracts.ActionResult{}, err
	}
	endpoint, err := a.readEndpoint(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return a.advanceDeletion(ctx, request, endpoint, nil)
}

func (a *VPCGatewayEndpointAction) Wait(
	ctx context.Context,
	request contracts.ActionRequest,
	result contracts.ActionResult,
) (contracts.WaitResult, error) {
	if err := validateComplexDelete(request, VPCGatewayEndpointNativeType); err != nil {
		return contracts.WaitResult{}, err
	}
	if strings.TrimSpace(stringValue(result.Data["phase"])) == vpcGatewayEndpointPhaseAbsent {
		return contracts.WaitResult{Done: true, State: vpcGatewayEndpointPhaseAbsent}, nil
	}
	endpoint, err := a.readEndpoint(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !endpoint.exists {
		return contracts.WaitResult{Done: true, State: vpcGatewayEndpointPhaseAbsent}, nil
	}
	next, err := a.advanceDeletion(ctx, request, endpoint, result.Data)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	phase := strings.TrimSpace(stringValue(next.Data["phase"]))
	if phase == vpcGatewayEndpointPhaseAbsent {
		return contracts.WaitResult{Done: true, State: phase, Data: next.Data}, nil
	}
	state := endpoint.status
	if phase == vpcGatewayEndpointPhaseDissociate {
		state = "route_tables_pending"
	} else if phase == vpcGatewayEndpointPhaseDelete {
		state = "gateway_endpoint_delete_requested"
	}
	return contracts.WaitResult{
		Done: false, RetryAfter: actionReadbackInterval, State: state, Data: next.Data,
	}, nil
}

func (a *VPCGatewayEndpointAction) Readback(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ReadbackResult, error) {
	if err := validateComplexDelete(request, VPCGatewayEndpointNativeType); err != nil {
		return contracts.ReadbackResult{}, err
	}
	endpoint, err := a.readEndpoint(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	return contracts.ReadbackResult{
		Exists: endpoint.exists, State: endpoint.status, Data: endpoint.data,
	}, nil
}

func (a *VPCGatewayEndpointAction) advanceDeletion(
	ctx context.Context,
	request contracts.ActionRequest,
	endpoint vpcGatewayEndpointState,
	previous map[string]any,
) (contracts.ActionResult, error) {
	if !endpoint.exists {
		return contracts.ActionResult{
			ProviderRequestID: endpoint.requestID,
			Data:              gatewayEndpointActionData(vpcGatewayEndpointPhaseAbsent, endpoint, previous),
		}, nil
	}
	if deletionInProgressState(endpoint.status) {
		return contracts.ActionResult{
			ProviderRequestID: endpoint.requestID,
			Data:              gatewayEndpointActionData(vpcGatewayEndpointPhaseDelete, endpoint, previous),
			RetryAfter:        actionReadbackInterval,
		}, nil
	}
	if gatewayEndpointTransitionInProgress(endpoint.status) {
		return contracts.ActionResult{
			ProviderRequestID: endpoint.requestID,
			Data:              gatewayEndpointActionData(vpcGatewayEndpointPhaseDissociate, endpoint, previous),
			RetryAfter:        actionReadbackInterval,
		}, nil
	}
	if len(endpoint.routeTableIDs) > 0 {
		return a.dissociateRouteTables(ctx, request, endpoint, previous)
	}
	return a.deleteEndpoint(ctx, request, endpoint, previous)
}

func (a *VPCGatewayEndpointAction) dissociateRouteTables(
	ctx context.Context,
	request contracts.ActionRequest,
	endpoint vpcGatewayEndpointState,
	previous map[string]any,
) (contracts.ActionResult, error) {
	routeTableIDs := append([]string(nil), endpoint.routeTableIDs...)
	if len(routeTableIDs) > vpcGatewayEndpointRouteTableBatch {
		routeTableIDs = routeTableIDs[:vpcGatewayEndpointRouteTableBatch]
	}
	data := gatewayEndpointActionData(vpcGatewayEndpointPhaseDissociate, endpoint, previous)
	data["active_route_table_ids"] = append([]string(nil), routeTableIDs...)
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: a.connectionID,
		Operation:    vpcGatewayEndpointDissociateOperation,
		Scope:        map[string]string{"region": a.region},
		Parameters: map[string]any{
			"RegionId": a.region, "EndpointId": request.Asset.Identity.NativeID,
			"RouteTableIds": routeTableIDs, "DryRun": false,
		},
		IdempotencyKey: vpcGatewayEndpointClientToken(
			request.IdempotencyKey, vpcGatewayEndpointPhaseDissociate, strings.Join(routeTableIDs, ","),
		),
	})
	if isNotFound(err) {
		return contracts.ActionResult{Data: data, RetryAfter: actionReadbackInterval}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
		Data: data, RetryAfter: actionReadbackInterval,
	}, nil
}

func (a *VPCGatewayEndpointAction) deleteEndpoint(
	ctx context.Context,
	request contracts.ActionRequest,
	endpoint vpcGatewayEndpointState,
	previous map[string]any,
) (contracts.ActionResult, error) {
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: a.connectionID,
		Operation:    vpcGatewayEndpointDeleteOperation,
		Scope:        map[string]string{"region": a.region},
		Parameters: map[string]any{
			"RegionId": a.region, "EndpointId": request.Asset.Identity.NativeID, "DryRun": false,
		},
		IdempotencyKey: vpcGatewayEndpointClientToken(
			request.IdempotencyKey, vpcGatewayEndpointPhaseDelete, request.Asset.Identity.NativeID,
		),
	})
	if isNotFound(err) {
		return contracts.ActionResult{
			Data: gatewayEndpointActionData(vpcGatewayEndpointPhaseAbsent, endpoint, previous),
		}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	data := gatewayEndpointActionData(vpcGatewayEndpointPhaseDelete, endpoint, previous)
	return contracts.ActionResult{
		ProviderRequestID: result.RequestID, ProviderOperationID: result.OperationID,
		Data: data, RetryAfter: actionReadbackInterval,
	}, nil
}

func vpcGatewayEndpointClientToken(parts ...string) string {
	digest := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(digest[:])
}

func (a *VPCGatewayEndpointAction) readEndpoint(
	ctx context.Context,
	endpointID string,
) (vpcGatewayEndpointState, error) {
	result, err := a.provider.Invoke(ctx, contracts.Invocation{
		ConnectionID: a.connectionID,
		Operation:    vpcGatewayEndpointListOperation,
		Scope:        map[string]string{"region": a.region},
		Parameters: map[string]any{
			"RegionId": a.region, "EndpointId": endpointID, "MaxResults": 1,
		},
	})
	if isNotFound(err) {
		return vpcGatewayEndpointState{}, nil
	}
	if err != nil {
		return vpcGatewayEndpointState{}, err
	}
	resource, exists, err := readbackResource(
		result.Data, []string{"Endpoints"}, "EndpointId", endpointID,
	)
	if err != nil {
		return vpcGatewayEndpointState{}, err
	}
	if !exists {
		return vpcGatewayEndpointState{requestID: result.RequestID, data: result.Data}, nil
	}
	return vpcGatewayEndpointState{
		exists:        true,
		status:        strings.TrimSpace(stringValue(resource["EndpointStatus"])),
		routeTableIDs: normalizedActionStrings(resource["AssociatedRouteTables"]),
		requestID:     result.RequestID,
		data:          result.Data,
	}, nil
}

func gatewayEndpointTransitionInProgress(state string) bool {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "creating", "modifying", "associating", "dissociating":
		return true
	default:
		return false
	}
}

func gatewayEndpointActionData(
	phase string,
	endpoint vpcGatewayEndpointState,
	previous map[string]any,
) map[string]any {
	data := cloneTopologyMap(previous)
	if data == nil {
		data = make(map[string]any)
	}
	data["phase"] = phase
	data["provider_state"] = endpoint.status
	data["associated_route_table_ids"] = append([]string(nil), endpoint.routeTableIDs...)
	if endpoint.requestID != "" {
		data["readback_request_id"] = endpoint.requestID
	}
	return data
}
