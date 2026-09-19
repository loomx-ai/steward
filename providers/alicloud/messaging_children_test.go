package alicloud

import (
	"context"
	"errors"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestKafkaTopicsAreIdentifiedWithinTheirInstance(t *testing.T) {
	t.Parallel()

	// Two instances hold a topic with the same name; shapes follow the
	// official alikafka 2019-09-16 metadata.
	runtime, factory := encryptionKeyRuntime(t, func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		switch invocation.Operation {
		case "AlibabaCloud.AliKafka.GetInstanceList":
			return contracts.InvocationResult{Data: map[string]any{"InstanceList": map[string]any{"InstanceVO": []any{
				map[string]any{"InstanceId": "alikafka-a"}, map[string]any{"InstanceId": "alikafka-b"},
			}}}}, nil
		case "AlibabaCloud.AliKafka.GetTopicList":
			instance := invocation.Parameters["InstanceId"]
			return contracts.InvocationResult{Data: map[string]any{"Total": 1, "TopicList": map[string]any{"TopicVO": []any{
				map[string]any{"InstanceId": instance, "Topic": "orders", "StatusName": "服务中", "PartitionNum": 12},
			}}}}, nil
		}
		return contracts.InvocationResult{}, errors.New("unexpected call " + invocation.Operation)
	})
	kind := runtime.resourceKindByNativeType["ACS::AliKafka::Topic"]
	request := contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		Source:       "product-api", ResourceKind: &kind, Limit: 100,
	}
	var ids []string
	for {
		batch, err := runtime.List(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range batch.Items {
			ids = append(ids, item.NativeID)
			if item.Normalized["topic"] != "orders" || item.Normalized["instanceId"] == nil {
				t.Fatalf("normalized = %+v", item.Normalized)
			}
		}
		if batch.NextCursor == "" {
			break
		}
		request.Cursor = batch.NextCursor
	}
	if len(ids) != 2 || ids[0] != "alikafka-a/orders" || ids[1] != "alikafka-b/orders" {
		t.Fatalf("native IDs = %v, calls = %+v", ids, factory.calls)
	}
}

func TestLogstoresAreListedByNameWithinTheirProject(t *testing.T) {
	t.Parallel()

	// ListLogStores returns bare names, per the official Sls 2020-12-30 metadata.
	runtime, _ := encryptionKeyRuntime(t, func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		switch invocation.Operation {
		case "AlibabaCloud.SLS.ListProject":
			return contracts.InvocationResult{Data: map[string]any{"total": 1, "count": 1, "projects": []any{
				map[string]any{"projectName": "app-logs", "status": "Normal"},
			}}}, nil
		case "AlibabaCloud.SLS.ListLogStores":
			if invocation.Parameters["project"] != "app-logs" {
				return contracts.InvocationResult{}, errors.New("unexpected project")
			}
			return contracts.InvocationResult{Data: map[string]any{"total": 2, "count": 2, "logstores": []any{"access", "internal-operation_log"}}}, nil
		}
		return contracts.InvocationResult{}, errors.New("unexpected call " + invocation.Operation)
	})
	kind := runtime.resourceKindByNativeType[SLSLogStoreNativeType]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		Source: "product-api", ResourceKind: &kind, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 2 || batch.Items[0].NativeID != "app-logs/access" ||
		batch.Items[0].Normalized["logstoreName"] != "access" || batch.Items[0].Normalized["project"] != "app-logs" {
		t.Fatalf("logstores = %+v", batch.Items)
	}
}

func TestHTTPAPIsAreListedFromEveryVersionGroup(t *testing.T) {
	t.Parallel()

	// ListHttpApis nests versioned APIs under each API, per the official
	// APIG 2024-03-27 metadata; a group without versions must not end paging.
	runtime, factory := encryptionKeyRuntime(t, func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		if invocation.Operation != "AlibabaCloud.APIG.ListHttpApis" {
			return contracts.InvocationResult{}, errors.New("unexpected call " + invocation.Operation)
		}
		if invocation.Parameters["pageNumber"] == 2 || invocation.Parameters["pageNumber"] == "2" {
			return contracts.InvocationResult{Data: map[string]any{"data": map[string]any{"totalSize": 3, "items": []any{
				map[string]any{"name": "c", "versionedHttpApis": []any{map[string]any{"httpApiId": "api-3", "name": "c", "gatewayId": "gw-a"}}},
			}}}}, nil
		}
		return contracts.InvocationResult{Data: map[string]any{"data": map[string]any{"totalSize": 3, "items": []any{
			map[string]any{"name": "a", "versionedHttpApis": []any{map[string]any{"httpApiId": "api-1", "name": "a", "gatewayId": "gw-a"}}},
			map[string]any{"name": "b"},
		}}}}, nil
	})
	kind := runtime.resourceKindByNativeType["ACS::APIG::HttpApi"]
	request := contracts.InventoryRequest{
		ConnectionID: "connection-a", Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		Source: "product-api", ResourceKind: &kind, Limit: 2,
	}
	var ids []string
	for page := 0; page < 5; page++ {
		batch, err := runtime.List(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range batch.Items {
			ids = append(ids, item.NativeID)
		}
		if batch.NextCursor == "" {
			break
		}
		request.Cursor = batch.NextCursor
	}
	if len(ids) != 2 || ids[0] != "api-1" || ids[1] != "api-3" {
		t.Fatalf("http APIs = %v, calls = %+v", ids, factory.calls)
	}
}
