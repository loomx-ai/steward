package alicloud_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/providers/alicloud"
)

func TestRouterInterfaceActionDeletesAndConfirmsAbsence(t *testing.T) {
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
		deleted.Parameters["Force"] != nil {
		t.Fatalf("router interface delete invocation=%+v", deleted)
	}
	read := provider.invocations[2]
	if read.Parameters["Filter.1.Key"] != "RouterInterfaceId" ||
		read.Parameters["Filter.1.Value.1"] != "ri-bp1kbigq0y1gw1qerdswm" ||
		read.Parameters["PageSize"] != 1 {
		t.Fatalf("router interface read invocation=%+v", read)
	}
}

func TestKafkaTopicActionDeletesByInstanceAndName(t *testing.T) {
	t.Parallel()

	topic := func(requestID string, items ...any) contracts.InvocationResult {
		return contracts.InvocationResult{RequestID: requestID, Data: map[string]any{
			"Total": len(items), "TopicList": map[string]any{"TopicVO": items},
		}}
	}
	orders := map[string]any{"InstanceId": "alikafka-a", "Topic": "orders", "StatusName": "服务中"}
	provider := &invocationProvider{results: []contracts.InvocationResult{
		topic("preflight", orders), {RequestID: "delete"}, topic("absent"),
	}}
	hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", "ACS::AliKafka::Topic")
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: "ACS::AliKafka::Topic", NativeID: "alikafka-a/orders"},
			Normalized: map[string]any{"instanceId": "alikafka-a", "topic": "orders"},
		},
		Action: "delete", IdempotencyKey: "kafka-topic-step",
	}
	if preflight, err := hook.Preflight(context.Background(), request); err != nil || !preflight.Allowed {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := hook.Wait(context.Background(), request, result); err != nil || !wait.Done || wait.State != "absent" {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	deleted := provider.invocations[1]
	if deleted.Operation != "AlibabaCloud.AliKafka.DeleteTopic" || deleted.Parameters["InstanceId"] != "alikafka-a" ||
		deleted.Parameters["Topic"] != "orders" || deleted.Parameters["RegionId"] != "cn-hangzhou" {
		t.Fatalf("delete invocation=%+v", deleted)
	}
}
