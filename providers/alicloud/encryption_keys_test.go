package alicloud

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func encryptionKeyRuntime(t *testing.T, invoke func(contracts.Invocation) (contracts.InvocationResult, error)) (*Runtime, *topologyRuntimeFactory) {
	t.Helper()
	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Type:   asset.CredentialAliCloudAccessKey,
		Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
	}}
	factory := &topologyRuntimeFactory{invoke: invoke}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	return runtime, factory
}

func encryptionKeyRequest(source string) contracts.InventoryRequest {
	return contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		Source:       source,
	}
}

func TestInventoryReadsDatabaseEncryptionKeys(t *testing.T) {
	t.Parallel()

	// Response shapes follow the official api.aliyun.com metadata of
	// Rds 2014-08-15, polardb 2017-08-01 and R-kvstore 2015-01-01.
	responses := map[string]map[string]map[string]any{
		"rm-tde": {
			"AlibabaCloud.RDS.DescribeDBInstanceTDE":           {"TDEStatus": "Enabled", "TDEMode": "BYOK", "EncryptionKey": "key-tde"},
			"AlibabaCloud.RDS.DescribeDBInstanceEncryptionKey": {"EncryptionKey": "key-disk", "EncryptionKeyStatus": "Enabled"},
		},
		"rm-plain": {
			"AlibabaCloud.RDS.DescribeDBInstanceTDE":           {"TDEStatus": "Disabled", "EncryptionKey": "key-stale"},
			"AlibabaCloud.RDS.DescribeDBInstanceEncryptionKey": {"EncryptionKey": ""},
		},
		"pc-tde":   {"AlibabaCloud.PolarDB.DescribeDBClusterTDE": {"TDEStatus": "Enabled", "EncryptionKey": "key-polar", "TDERegion": "cn-hangzhou"}},
		"pc-plain": {"AlibabaCloud.PolarDB.DescribeDBClusterTDE": {"TDEStatus": "Disabled"}},
		"r-tde": {
			"AlibabaCloud.Redis.DescribeInstanceTDEStatus": {"TDEStatus": "Enabled"},
			"AlibabaCloud.Redis.DescribeEncryptionKey":     {"EncryptionKey": "key-redis", "EncryptionKeyStatus": "Enabled"},
		},
		"r-plain": {"AlibabaCloud.Redis.DescribeInstanceTDEStatus": {"TDEStatus": "Disabled"}},
	}
	runtime, factory := encryptionKeyRuntime(t, func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		for _, parameter := range []string{"DBInstanceId", "DBClusterId", "InstanceId"} {
			if id, ok := invocation.Parameters[parameter].(string); ok {
				if data, ok := responses[id][invocation.Operation]; ok {
					return contracts.InvocationResult{RequestID: "req", Data: data}, nil
				}
			}
		}
		return contracts.InvocationResult{}, errors.New("unexpected call " + invocation.Operation)
	})
	items := []contracts.InventoryItem{
		{NativeType: rdsInstanceNativeType, NativeID: "rm-tde"},
		{NativeType: rdsInstanceNativeType, NativeID: "rm-plain"},
		{NativeType: polarDBClusterNativeType, NativeID: "pc-tde"},
		{NativeType: polarDBClusterNativeType, NativeID: "pc-plain"},
		{NativeType: redisInstanceNativeType, NativeID: "r-tde"},
		{NativeType: redisInstanceNativeType, NativeID: "r-plain"},
		{NativeType: vpcNativeType, NativeID: "vpc-a"},
	}
	enriched, err := runtime.EnrichInventoryBatch(context.Background(), encryptionKeyRequest("product-api"), items)
	if err != nil {
		t.Fatalf("enrich: %v", err)
	}
	want := map[string]map[string]string{
		"rm-tde":   {NormalizedTDEEncryptionKeyField: "key-tde", "EncryptionKey": "key-disk"},
		"rm-plain": {},
		"pc-tde":   {NormalizedTDEEncryptionKeyField: "key-polar"},
		"pc-plain": {},
		"r-tde":    {NormalizedTDEEncryptionKeyField: "key-redis"},
		"r-plain":  {},
	}
	for _, item := range enriched {
		expected, checked := want[item.NativeID]
		if !checked {
			continue
		}
		for _, field := range []string{NormalizedTDEEncryptionKeyField, "EncryptionKey"} {
			got, _ := item.Normalized[field].(string)
			if got != expected[field] {
				t.Errorf("%s %s = %q, want %q", item.NativeID, field, got, expected[field])
			}
		}
	}
	// A disabled Redis instance is not asked for its key.
	for _, call := range factory.calls {
		if call.Operation == "AlibabaCloud.Redis.DescribeEncryptionKey" && call.Parameters["InstanceId"] != "r-tde" {
			t.Errorf("unexpected key lookup %+v", call)
		}
	}
	if len(factory.calls) != 9 {
		t.Errorf("calls = %d, want 9: %+v", len(factory.calls), factory.calls)
	}
}

func TestInventoryEncryptionKeyErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, code string
		fails      bool
	}{
		{name: "documented unsupported engine proves no key", code: "IncorrectEngineVersion"},
		{name: "instance deleted since listing", code: "InvalidDBInstanceId.NotFound"},
		{name: "permission denied leaves the key unknown", code: "Forbidden.RAM", fails: true},
		{name: "throttling leaves the key unknown", code: "Throttling.User", fails: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			runtime, _ := encryptionKeyRuntime(t, func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
				if invocation.Operation == "AlibabaCloud.RDS.DescribeDBInstanceTDE" {
					return contracts.InvocationResult{}, &APIError{Code: test.code, Message: "denied", StatusCode: 400}
				}
				return contracts.InvocationResult{Data: map[string]any{"EncryptionKey": ""}}, nil
			})
			items := []contracts.InventoryItem{{NativeType: rdsInstanceNativeType, NativeID: "rm-a"}}
			enriched, err := runtime.EnrichInventoryBatch(context.Background(), encryptionKeyRequest("product-api"), items)
			if test.fails {
				if err == nil || !strings.Contains(err.Error(), "rm-a") {
					t.Fatalf("error = %v, want a failed batch naming the instance", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("enrich: %v", err)
			}
			if _, found := enriched[0].Normalized[NormalizedTDEEncryptionKeyField]; found {
				t.Fatalf("normalized = %+v", enriched[0].Normalized)
			}
		})
	}
}

func TestInventoryEncryptionKeyRequiresStatus(t *testing.T) {
	t.Parallel()

	runtime, _ := encryptionKeyRuntime(t, func(contracts.Invocation) (contracts.InvocationResult, error) {
		return contracts.InvocationResult{Data: map[string]any{"EncryptionKey": "key-a"}}, nil
	})
	items := []contracts.InventoryItem{{NativeType: polarDBClusterNativeType, NativeID: "pc-a"}}
	if _, err := runtime.EnrichInventoryBatch(context.Background(), encryptionKeyRequest("product-api"), items); err == nil ||
		!strings.Contains(err.Error(), "TDEStatus") {
		t.Fatalf("error = %v, want a missing status to fail the batch", err)
	}
}

func TestResourceCenterInventorySkipsEncryptionKeyLookups(t *testing.T) {
	t.Parallel()

	runtime, factory := encryptionKeyRuntime(t, func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		return contracts.InvocationResult{}, errors.New("unexpected call " + invocation.Operation)
	})
	items := []contracts.InventoryItem{{NativeType: rdsInstanceNativeType, NativeID: "rm-a"}}
	if _, err := runtime.EnrichInventoryBatch(context.Background(), encryptionKeyRequest("resource-center"), items); err != nil {
		t.Fatalf("enrich: %v", err)
	}
	if len(factory.calls) != 0 {
		t.Fatalf("calls = %+v", factory.calls)
	}
}
