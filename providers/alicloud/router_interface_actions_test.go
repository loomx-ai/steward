package alicloud_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/providers/alicloud"
)

func TestRouterInterfaceActionForceDeletesRoutesBeforeVPC(t *testing.T) {
	t.Parallel()

	resource := func(requestID string) contracts.InvocationResult {
		return contracts.InvocationResult{
			RequestID: requestID,
			Data: map[string]any{"RouterInterfaceSet": map[string]any{
				"RouterInterfaceType": []any{map[string]any{
					"RouterInterfaceId": "ri-bp1kbigq0y1gw1qerdswm", "Status": "active",
				}},
			}},
		}
	}
	provider := &invocationProvider{results: []contracts.InvocationResult{
		resource("preflight"), {RequestID: "delete"},
		{RequestID: "absent", Data: map[string]any{
			"RouterInterfaceSet": map[string]any{"RouterInterfaceType": []any{}},
		}},
	}}
	hook, err := alicloud.NewActionHook(
		provider, "connection-a", "cn-hangzhou", "ACS::VPC::RouterInterface",
	)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::RouterInterface",
			NativeID: "ri-bp1kbigq0y1gw1qerdswm",
		}},
		Action: "delete", IdempotencyKey: "router-interface-step",
	}
	preflight, err := hook.Preflight(context.Background(), request)
	if err != nil || !preflight.Allowed || preflight.Absent {
		t.Fatalf("router interface preflight=%+v err=%v", preflight, err)
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "delete" {
		t.Fatalf("router interface delete=%+v err=%v", result, err)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.State != "absent" {
		t.Fatalf("router interface wait=%+v err=%v", wait, err)
	}

	wantOperations := []string{
		"DescribeRouterInterfaces",
		"DeleteRouterInterface",
		"DescribeRouterInterfaces",
	}
	gotOperations := make([]string, len(provider.invocations))
	for index, invocation := range provider.invocations {
		gotOperations[index] = invocation.Operation
	}
	if !reflect.DeepEqual(gotOperations, wantOperations) {
		t.Fatalf("router interface operations=%v, want=%v", gotOperations, wantOperations)
	}
	deleted := provider.invocations[1]
	if deleted.Parameters["RegionId"] != "cn-hangzhou" ||
		deleted.Parameters["RouterInterfaceId"] != "ri-bp1kbigq0y1gw1qerdswm" ||
		deleted.Parameters["Force"] != true {
		t.Fatalf("router interface delete invocation=%+v", deleted)
	}
	read := provider.invocations[2]
	if read.Parameters["Filter.1.Key"] != "RouterInterfaceId" ||
		read.Parameters["Filter.1.Value.1"] != "ri-bp1kbigq0y1gw1qerdswm" ||
		read.Parameters["PageSize"] != 1 {
		t.Fatalf("router interface read invocation=%+v", read)
	}
}
