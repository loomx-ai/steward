package alicloud_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/providers/alicloud"
)

func TestVPCGatewayEndpointActionDissociatesRouteTablesBeforeDelete(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		vpcGatewayEndpointResult("preflight", "Created", "vtb-b", "vtb-a"),
		vpcGatewayEndpointResult("execute", "Created", "vtb-b", "vtb-a"),
		{RequestID: "dissociate"},
		vpcGatewayEndpointResult("dissociating", "Dissociating", "vtb-b", "vtb-a"),
		vpcGatewayEndpointResult("dissociated", "Created"),
		{RequestID: "delete"},
		vpcGatewayEndpointResult("deleting", "Deleting"),
		vpcGatewayEndpointResult("absent", ""),
	}}
	hook, err := alicloud.NewVPCGatewayEndpointHook(
		provider,
		"connection-a",
		"cn-hangzhou",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: alicloud.VPCGatewayEndpointNativeType,
			NativeID: "vpce-bp1khxwul8setja1pb32z",
		}},
		Action: "delete", IdempotencyKey: "step-gateway-endpoint",
	}

	preflight, err := hook.Preflight(context.Background(), request)
	if err != nil || !preflight.Allowed || preflight.Absent ||
		preflight.Evidence["next_operation"] != "AlibabaCloud.DissociateRouteTablesFromVpcGatewayEndpoint" {
		t.Fatalf("gateway endpoint preflight=%+v err=%v", preflight, err)
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.Data["phase"] != "dissociate_route_tables" {
		t.Fatalf("start gateway endpoint deletion=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "route_tables_pending" {
		t.Fatalf("gateway endpoint dissociation wait=%+v err=%v", wait, err)
	}
	result.Data = wait.Data
	wait, err = hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "gateway_endpoint_delete_requested" ||
		wait.Data["phase"] != "delete_gateway_endpoint" {
		t.Fatalf("gateway endpoint delete request=%+v err=%v", wait, err)
	}
	result.Data = wait.Data
	wait, err = hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "gateway_endpoint_delete_requested" {
		t.Fatalf("gateway endpoint delete wait=%+v err=%v", wait, err)
	}
	result.Data = wait.Data
	wait, err = hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.State != "absent" {
		t.Fatalf("gateway endpoint completion=%+v err=%v", wait, err)
	}

	wantOperations := []string{
		"AlibabaCloud.ListVpcGatewayEndpoints",
		"AlibabaCloud.ListVpcGatewayEndpoints",
		"AlibabaCloud.DissociateRouteTablesFromVpcGatewayEndpoint",
		"AlibabaCloud.ListVpcGatewayEndpoints",
		"AlibabaCloud.ListVpcGatewayEndpoints",
		"AlibabaCloud.DeleteVpcGatewayEndpoint",
		"AlibabaCloud.ListVpcGatewayEndpoints",
		"AlibabaCloud.ListVpcGatewayEndpoints",
	}
	gotOperations := make([]string, len(provider.invocations))
	for index, invocation := range provider.invocations {
		gotOperations[index] = invocation.Operation
	}
	if !reflect.DeepEqual(gotOperations, wantOperations) {
		t.Fatalf("gateway endpoint operations:\n got: %#v\nwant: %#v", gotOperations, wantOperations)
	}
	dissociate := provider.invocations[2]
	if !reflect.DeepEqual(dissociate.Parameters["RouteTableIds"], []string{"vtb-a", "vtb-b"}) ||
		dissociate.Parameters["EndpointId"] != "vpce-bp1khxwul8setja1pb32z" ||
		dissociate.Parameters["RegionId"] != "cn-hangzhou" ||
		dissociate.Parameters["DryRun"] != false || len(dissociate.IdempotencyKey) != 64 {
		t.Fatalf("gateway endpoint dissociation invocation=%+v", dissociate)
	}
	deleted := provider.invocations[5]
	if len(deleted.IdempotencyKey) != 64 || deleted.IdempotencyKey == dissociate.IdempotencyKey ||
		deleted.Parameters["EndpointId"] != "vpce-bp1khxwul8setja1pb32z" {
		t.Fatalf("gateway endpoint delete invocation=%+v", deleted)
	}
	if timeout := hook.DeletionCheckTimeout(); timeout != 2*time.Minute {
		t.Fatalf("gateway endpoint deletion timeout=%s, want 2m", timeout)
	}
}

func vpcGatewayEndpointResult(
	requestID string,
	status string,
	routeTableIDs ...string,
) contracts.InvocationResult {
	endpoints := []any{}
	if status != "" {
		routes := make([]any, len(routeTableIDs))
		for index, routeTableID := range routeTableIDs {
			routes[index] = routeTableID
		}
		endpoints = append(endpoints, map[string]any{
			"EndpointId": "vpce-bp1khxwul8setja1pb32z", "EndpointStatus": status,
			"AssociatedRouteTables": routes,
		})
	}
	return contracts.InvocationResult{
		RequestID: requestID, Data: map[string]any{"Endpoints": endpoints},
	}
}
