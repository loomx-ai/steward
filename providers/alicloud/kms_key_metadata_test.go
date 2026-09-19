package alicloud

import (
	"context"
	"errors"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestKMSKeyInventoryRecordsTheKeyCreator(t *testing.T) {
	t.Parallel()

	// ListKeys returns only IDs; DescribeKey reports the creator. Shapes follow
	// the official Kms 2016-01-20 metadata.
	runtime, factory := encryptionKeyRuntime(t, func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		switch invocation.Operation {
		case "AlibabaCloud.KMS.ListKeys":
			return contracts.InvocationResult{Data: map[string]any{"TotalCount": 2, "PageNumber": 1, "PageSize": 100, "Keys": map[string]any{"Key": []any{
				map[string]any{"KeyId": "key-own", "KeyArn": "acs:kms:cn-hangzhou:1234567890123456:key/key-own"},
				map[string]any{"KeyId": "key-rds", "KeyArn": "acs:kms:cn-hangzhou:1234567890123456:key/key-rds"},
			}}}}, nil
		case "AlibabaCloud.KMS.DescribeKey":
			creator := map[any]string{"key-own": "1234567890123456", "key-rds": "Rds"}[invocation.Parameters["KeyId"]]
			return contracts.InvocationResult{Data: map[string]any{"KeyMetadata": map[string]any{
				"KeyId": invocation.Parameters["KeyId"], "Creator": creator, "KeyState": "Enabled",
				"DeletionProtection": "Disabled", "CreationDate": "2025-01-01T00:00:00Z",
			}}}, nil
		}
		return contracts.InvocationResult{}, errors.New("unexpected call " + invocation.Operation)
	})
	kind := runtime.resourceKindByNativeType[KMSKeyNativeType]
	request := contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		Source:       "product-api",
		ResourceKind: &kind,
		Limit:        100,
	}
	batch, err := runtime.List(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	enriched, err := runtime.EnrichInventoryBatch(context.Background(), request, batch.Items)
	if err != nil {
		t.Fatal(err)
	}
	creators := map[string]any{}
	for _, item := range enriched {
		creators[item.NativeID] = item.Normalized["creator"]
	}
	if creators["key-own"] != "1234567890123456" || creators["key-rds"] != "Rds" {
		t.Fatalf("creators = %v, calls = %+v", creators, factory.calls)
	}
	if _, managed := serviceManagedKMSCreator(map[string]any{"creator": creators["key-rds"]}); !managed {
		t.Fatal("a service-created key is not recognized as managed")
	}
}
