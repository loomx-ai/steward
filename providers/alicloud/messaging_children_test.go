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
