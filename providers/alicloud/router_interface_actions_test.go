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
			Identity:   asset.Identity{Provider: asset.ProviderAliCloud, NativeType: "ACS::AliKafka::Topic", NativeID: "alikafka-a/orders"},
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

func TestLogstoreActionReadsBackByNameWithinTheProject(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{
		results: []contracts.InvocationResult{
			{RequestID: "preflight", Data: map[string]any{"logstoreName": "access", "ttl": 30}},
			{RequestID: "delete"},
			{},
		},
		errors: []error{nil, nil, alicloud.NormalizeError(&alicloud.APIError{Code: "LogStoreNotExist", Message: "logstore access does not exist", StatusCode: 404})},
	}
	hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", alicloud.SLSLogStoreNativeType)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			Identity:   asset.Identity{Provider: asset.ProviderAliCloud, NativeType: alicloud.SLSLogStoreNativeType, NativeID: "app-logs/access"},
			Normalized: map[string]any{"project": "app-logs", "logstoreName": "access"},
		},
		Action: "delete", IdempotencyKey: "logstore-step",
	}
	if preflight, err := hook.Preflight(context.Background(), request); err != nil || !preflight.Allowed {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := hook.Wait(context.Background(), request, result); err != nil || !wait.Done {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	deleted := provider.invocations[1]
	if deleted.Parameters["project"] != "app-logs" || deleted.Parameters["logstore"] != "access" {
		t.Fatalf("delete invocation=%+v", deleted)
	}
}

func TestRepositoryActionFinishesWhenTheRegistryReportsItDeleted(t *testing.T) {
	t.Parallel()

	repository := func(requestID, status string) contracts.InvocationResult {
		return contracts.InvocationResult{RequestID: requestID, Data: map[string]any{"Repositories": []any{
			map[string]any{"RepoId": "crr-a", "RepoName": "web", "RepoNamespaceName": "apps", "RepoStatus": status},
		}}}
	}
	provider := &invocationProvider{results: []contracts.InvocationResult{
		repository("preflight", "NORMAL"), {RequestID: "delete"}, repository("deleting", "DELETING"), repository("deleted", "DELETED"),
	}}
	hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", "ACS::CR::Repository")
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			Identity:   asset.Identity{Provider: asset.ProviderAliCloud, NativeType: "ACS::CR::Repository", NativeID: "crr-a"},
			Normalized: map[string]any{"instanceId": "cri-a", "namespaceName": "apps", "repoName": "web"},
		},
		Action: "delete", IdempotencyKey: "repository-step",
	}
	if preflight, err := hook.Preflight(context.Background(), request); err != nil || !preflight.Allowed {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := hook.Wait(context.Background(), request, result); err != nil || wait.Done {
		t.Fatalf("deleting wait=%+v err=%v", wait, err)
	}
	if wait, err := hook.Wait(context.Background(), request, result); err != nil || !wait.Done {
		t.Fatalf("deleted wait=%+v err=%v", wait, err)
	}
	read := provider.invocations[2]
	if read.Parameters["RepoStatus"] != "ALL" || read.Parameters["RepoName"] != "web" || read.Parameters["RepoNamespaceName"] != "apps" {
		t.Fatalf("read invocation=%+v", read)
	}
}

func TestContactGroupReadbackTrustsOnlyACompleteListing(t *testing.T) {
	t.Parallel()

	groups := func(total int, names ...string) contracts.InvocationResult {
		items := make([]any, 0, len(names))
		for _, name := range names {
			items = append(items, map[string]any{"Name": name})
		}
		return contracts.InvocationResult{Data: map[string]any{"Total": total, "ContactGroupList": map[string]any{"ContactGroup": items}}}
	}
	request := contracts.ActionRequest{
		Asset:  asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: "ACS::CMS::AlarmContactGroup", NativeID: "ops"}},
		Action: "delete", IdempotencyKey: "contact-group-step",
	}
	for _, test := range []struct {
		name   string
		result contracts.InvocationResult
		absent bool
		fails  bool
	}{
		{name: "present", result: groups(2, "default", "ops")},
		{name: "complete listing without the group", result: groups(1, "default"), absent: true},
		{name: "one page of a longer listing", result: groups(250, "default"), fails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			provider := &invocationProvider{results: []contracts.InvocationResult{test.result}}
			hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", "ACS::CMS::AlarmContactGroup")
			if err != nil {
				t.Fatal(err)
			}
			readback, err := hook.Readback(context.Background(), request)
			if test.fails {
				if err == nil {
					t.Fatalf("readback = %+v, want an incomplete listing to fail", readback)
				}
				return
			}
			if err != nil || readback.Exists == test.absent {
				t.Fatalf("readback = %+v, err = %v", readback, err)
			}
		})
	}
}
