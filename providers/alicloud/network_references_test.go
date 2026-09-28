package alicloud

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestPrefixListAssociationsAreReadAtScanTime(t *testing.T) {
	t.Parallel()

	// Shapes follow the official Ecs 2014-05-26 DescribePrefixListAssociations
	// and Vpc 2016-04-28 GetVpcPrefixListAssociations metadata.
	runtime, factory := encryptionKeyRuntime(t, func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		switch invocation.Operation {
		case "AlibabaCloud.DescribePrefixListAssociations":
			if invocation.Parameters["NextToken"] == "page-2" {
				return contracts.InvocationResult{Data: map[string]any{"PrefixListAssociations": map[string]any{"PrefixListAssociation": []any{
					map[string]any{"ResourceId": "sg-b", "ResourceType": "securitygroup"},
				}}}}, nil
			}
			return contracts.InvocationResult{Data: map[string]any{"NextToken": "page-2", "PrefixListAssociations": map[string]any{"PrefixListAssociation": []any{
				map[string]any{"ResourceId": "sg-a", "ResourceType": "securitygroup"},
			}}}}, nil
		case "AlibabaCloud.GetVpcPrefixListAssociations":
			return contracts.InvocationResult{Data: map[string]any{"PrefixListAssociation": []any{
				map[string]any{"PrefixListId": "pl-vpc", "ResourceId": "vtb-a", "ResourceType": "vpcRouteTable"},
			}}}, nil
		}
		return contracts.InvocationResult{}, errors.New("unexpected call " + invocation.Operation)
	})
	items, err := runtime.enrichPrefixListAssociations(context.Background(), encryptionKeyRequest("product-api"), []contracts.InventoryItem{
		{NativeType: ECSPrefixListNativeType, NativeID: "pl-ecs", Normalized: map[string]any{}},
		{NativeType: VPCPrefixListNativeType, NativeID: "pl-vpc", Normalized: map[string]any{}},
		{NativeType: "ACS::VPC::VPC", NativeID: "vpc-a", Normalized: map[string]any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{map[string]any{"resourceId": "sg-a", "resourceType": "securitygroup"}, map[string]any{"resourceId": "sg-b", "resourceType": "securitygroup"}}
	if !reflect.DeepEqual(items[0].Normalized[NormalizedPrefixListAssociationsField], want) ||
		len(items[1].Normalized[NormalizedPrefixListAssociationsField].([]any)) != 1 ||
		items[2].Normalized[NormalizedPrefixListAssociationsField] != nil || len(factory.calls) != 3 {
		t.Fatalf("items = %+v, calls = %+v", items, factory.calls)
	}
	flowLogs := enrichVPCFlowLogs([]contracts.InventoryItem{{NativeType: vpcFlowLogNativeType, Normalized: map[string]any{"projectName": "flow", "logStoreName": "vpc"}}})
	if flowLogs[0].Normalized["logStoreRef"] != "flow/vpc" {
		t.Fatalf("flow log = %+v", flowLogs[0].Normalized)
	}
}

func TestNetworkPreconditionsKeepManagedFlowLogsAndSharedPrefixLists(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		nativeType, operation, itemsPath, identity, field, value string
		allowed                                                bool
	}{
		// DeleteFlowLog: Forbidden.OperateManagedFlowLog for sls-managed logs.
		{vpcFlowLogNativeType, "DescribeFlowLogs", "FlowLogs.FlowLog", "FlowLogId", "ServiceType", "sls", false},
		{vpcFlowLogNativeType, "DescribeFlowLogs", "FlowLogs.FlowLog", "FlowLogId", "ServiceType", "", true},
		// DeleteVpcPrefixList: OperationDenied.DeleteShareResource.
		{VPCPrefixListNativeType, "ListPrefixLists", "PrefixLists", "PrefixListId", "ShareType", "Shared", false},
		{VPCPrefixListNativeType, "ListPrefixLists", "PrefixLists", "PrefixListId", "ShareType", "", true},
	} {
		record := map[string]any{test.identity: "native-a", "Status": "Active"}
		if test.value != "" {
			record[test.field] = test.value
		}
		data := map[string]any{"TotalCount": 1}
		if test.itemsPath == "FlowLogs.FlowLog" {
			data["FlowLogs"] = map[string]any{"FlowLog": []any{record}}
		} else {
			data[test.itemsPath] = []any{record}
		}
		provider := &runtimeInvocationProvider{result: contracts.InvocationResult{Data: data}}
		hook, err := NewActionHook(provider, "connection-a", "cn-hangzhou", test.nativeType)
		if err != nil {
			t.Fatal(err)
		}
		preflight, err := hook.Preflight(context.Background(), contracts.ActionRequest{
			Asset:  asset.Asset{ID: "a", Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: test.nativeType, NativeID: "native-a"}},
			Action: "delete", IdempotencyKey: "step-a",
		})
		if err != nil || preflight.Allowed != test.allowed || provider.invocation.Operation != test.operation {
			t.Fatalf("%s %s=%q preflight=%+v err=%v", test.nativeType, test.field, test.value, preflight, err)
		}
	}
}

type runtimeInvocationProvider struct {
	result     contracts.InvocationResult
	invocation contracts.Invocation
}

func (*runtimeInvocationProvider) Provider() asset.Provider { return asset.ProviderAliCloud }

func (p *runtimeInvocationProvider) Invoke(_ context.Context, invocation contracts.Invocation) (contracts.InvocationResult, error) {
	p.invocation = invocation
	return p.result, nil
}
