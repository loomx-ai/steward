package alicloud

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func ramTopologyRequest(runtime *Runtime, t *testing.T, nativeType string) contracts.InventoryRequest {
	t.Helper()
	kind, ok := runtime.resourceKindByNativeType[nativeType]
	if !ok {
		t.Fatalf("resource kind %s is missing", nativeType)
	}
	return contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeGlobal, Location: "cn-hangzhou"},
		Source:       "product-api",
		ResourceKind: &kind,
		Limit:        100,
	}
}

func TestRAMInventoryRecordsGroupMembersAndPolicyAttachments(t *testing.T) {
	t.Parallel()

	// Shapes follow the official Ram 2015-05-01 metadata.
	runtime, factory := encryptionKeyRuntime(t, func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		switch invocation.Operation {
		case "AlibabaCloud.RAM.ListGroups":
			return contracts.InvocationResult{Data: map[string]any{"IsTruncated": false, "Groups": map[string]any{"Group": []any{
				map[string]any{"GroupName": "cleaners", "GroupId": "g-1"},
			}}}}, nil
		case "AlibabaCloud.RAM.ListUsersForGroup":
			if invocation.Parameters["Marker"] == nil {
				return contracts.InvocationResult{Data: map[string]any{"IsTruncated": true, "Marker": "m-2", "Users": map[string]any{"User": []any{
					map[string]any{"UserName": "steward", "JoinDate": "2025-01-01T00:00:00Z"},
				}}}}, nil
			}
			return contracts.InvocationResult{Data: map[string]any{"IsTruncated": false, "Users": map[string]any{"User": []any{
				map[string]any{"UserName": "alice"},
			}}}}, nil
		case "AlibabaCloud.RAM.ListPolicies":
			return contracts.InvocationResult{Data: map[string]any{"IsTruncated": false, "Policies": map[string]any{"Policy": []any{
				map[string]any{"PolicyName": "cleanup", "PolicyType": "Custom", "AttachmentCount": 3},
				map[string]any{"PolicyName": "unused", "PolicyType": "Custom", "AttachmentCount": 0},
			}}}}, nil
		case "AlibabaCloud.RAM.ListEntitiesForPolicy":
			if invocation.Parameters["PolicyName"] != "cleanup" || invocation.Parameters["PolicyType"] != "Custom" {
				return contracts.InvocationResult{}, errors.New("unexpected policy lookup")
			}
			return contracts.InvocationResult{Data: map[string]any{
				"Users":  map[string]any{"User": []any{map[string]any{"UserName": "steward"}}},
				"Groups": map[string]any{"Group": []any{map[string]any{"GroupName": "cleaners"}}},
				"Roles":  map[string]any{"Role": []any{map[string]any{"RoleName": "steward-role"}}},
			}}, nil
		}
		return contracts.InvocationResult{}, errors.New("unexpected call " + invocation.Operation)
	})

	groups := ramTopologyRequest(runtime, t, ramGroupNativeType)
	batch, err := runtime.List(context.Background(), groups)
	if err != nil {
		t.Fatalf("list groups: %v", err)
	}
	enriched, err := runtime.EnrichInventoryBatch(context.Background(), groups, batch.Items)
	if err != nil {
		t.Fatalf("enrich groups: %v", err)
	}
	if got := enriched[0].Normalized[NormalizedRAMGroupUsersField]; !reflect.DeepEqual(got, []any{"steward", "alice"}) {
		t.Fatalf("group users = %#v", got)
	}

	policies := ramTopologyRequest(runtime, t, ramPolicyNativeType)
	batch, err = runtime.List(context.Background(), policies)
	if err != nil {
		t.Fatalf("list policies: %v", err)
	}
	enriched, err = runtime.EnrichInventoryBatch(context.Background(), policies, batch.Items)
	if err != nil {
		t.Fatalf("enrich policies: %v", err)
	}
	byName := map[string]map[string]any{}
	for _, item := range enriched {
		byName[item.NativeID] = item.Normalized
	}
	if !reflect.DeepEqual(byName["cleanup"][NormalizedRAMPolicyUsersField], []any{"steward"}) ||
		!reflect.DeepEqual(byName["cleanup"][NormalizedRAMPolicyGroupsField], []any{"cleaners"}) ||
		!reflect.DeepEqual(byName["cleanup"][NormalizedRAMPolicyRolesField], []any{"steward-role"}) {
		t.Fatalf("cleanup policy = %#v", byName["cleanup"])
	}
	if !reflect.DeepEqual(byName["unused"][NormalizedRAMPolicyUsersField], []any{}) {
		t.Fatalf("unused policy = %#v", byName["unused"])
	}
	lookups := 0
	for _, call := range factory.calls {
		if call.Operation == "AlibabaCloud.RAM.ListEntitiesForPolicy" {
			lookups++
		}
	}
	if lookups != 1 {
		t.Fatalf("an unattached policy was looked up: %d lookups", lookups)
	}
}

func TestRAMInventoryMembershipErrors(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		code  string
		fails bool
	}{
		{code: "EntityNotExist.Group"},
		{code: "NoPermission", fails: true},
	} {
		t.Run(test.code, func(t *testing.T) {
			t.Parallel()
			runtime, _ := encryptionKeyRuntime(t, func(contracts.Invocation) (contracts.InvocationResult, error) {
				return contracts.InvocationResult{}, &APIError{Code: test.code, Message: "failed", StatusCode: 403}
			})
			request := ramTopologyRequest(runtime, t, ramGroupNativeType)
			items := []contracts.InventoryItem{{NativeType: ramGroupNativeType, NativeID: "cleaners"}}
			enriched, err := runtime.EnrichInventoryBatch(context.Background(), request, items)
			if test.fails {
				if err == nil || !strings.Contains(err.Error(), "cleaners") {
					t.Fatalf("error = %v, want the batch to fail", err)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(enriched[0].Normalized[NormalizedRAMGroupUsersField], []any{}) {
				t.Fatalf("enriched = %+v, error = %v", enriched, err)
			}
		})
	}
}
