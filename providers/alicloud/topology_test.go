package alicloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type topologyRuntimeFactory struct {
	responses      map[string]contracts.InvocationResult
	calls          []contracts.Invocation
	invoke         func(contracts.Invocation) (contracts.InvocationResult, error)
	resourceCenter ResourceCenterClient
}

func (*topologyRuntimeFactory) CallerIdentity(context.Context, contracts.Credential) (string, string, error) {
	return "", "", errors.New("not used")
}

func (*topologyRuntimeFactory) DiscoverRegions(context.Context, contracts.Credential) ([]providerRegion, error) {
	return nil, errors.New("not used")
}

func (f *topologyRuntimeFactory) ResourceCenter(context.Context, contracts.Credential, string) (ResourceCenterClient, error) {
	if f.resourceCenter == nil {
		return nil, errors.New("not used")
	}
	return f.resourceCenter, nil
}

func (f *topologyRuntimeFactory) Invoke(_ context.Context, _ contracts.Credential, region string, _ catalog.Operation, invocation contracts.Invocation) (contracts.InvocationResult, error) {
	if region != "cn-hangzhou" {
		return contracts.InvocationResult{}, errors.New("unexpected region")
	}
	f.calls = append(f.calls, invocation)
	if f.invoke != nil {
		return f.invoke(invocation)
	}
	result, ok := f.responses[invocation.Operation]
	if !ok {
		return contracts.InvocationResult{}, errors.New("unexpected operation")
	}
	return result, nil
}

func (*topologyRuntimeFactory) ACK(context.Context, contracts.Credential, string) (ACKClient, error) {
	return nil, errors.New("not used")
}

func TestTopologyBatchEnrichment(t *testing.T) {
	t.Parallel()

	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
	}}
	factory := &topologyRuntimeFactory{responses: map[string]contracts.InvocationResult{
		"AlibabaCloud.DescribeInstances": {RequestID: "req-instance", Data: map[string]any{
			"Instances": map[string]any{"Instance": []any{map[string]any{
				"InstanceId": "i-a",
				"VpcAttributes": map[string]any{
					"VpcId": "vpc-a", "VSwitchId": "vsw-a",
				},
				"ZoneId":           "cn-hangzhou-h",
				"SecurityGroupIds": map[string]any{"SecurityGroupId": []any{"sg-a", "sg-b"}},
				"NetworkInterfaces": map[string]any{"NetworkInterface": []any{
					map[string]any{"NetworkInterfaceId": "eni-a"},
				}},
			}}},
		}},
		"AlibabaCloud.DescribeSecurityGroups": {RequestID: "req-security-group", Data: map[string]any{
			"SecurityGroups": map[string]any{"SecurityGroup": []any{
				map[string]any{"SecurityGroupId": "sg-a", "VpcId": "vpc-a"},
			}},
		}},
		"AlibabaCloud.DescribeNetworkInterfaces": {RequestID: "req-network-interface", Data: map[string]any{
			"NetworkInterfaceSets": map[string]any{"NetworkInterfaceSet": []any{
				map[string]any{"NetworkInterfaceId": "eni-a", "VpcId": "vpc-a", "VSwitchId": "vsw-a", "InstanceId": "i-a"},
			}},
		}},
		"AlibabaCloud.DescribeDisks": {RequestID: "req-disk", Data: map[string]any{
			"Disks": map[string]any{"Disk": []any{
				map[string]any{"DiskId": "d-a", "InstanceId": "i-a", "Type": "system", "DeleteWithInstance": true},
			}},
		}},
		"AlibabaCloud.ARMS.DescribeEnvironment": {RequestID: "req-arms-environment", Data: map[string]any{
			"Data": map[string]any{
				"EnvironmentId": "env-a", "EnvironmentName": "VPC-a",
				"EnvironmentType": "ECS", "EnvironmentSubType": "ECS",
				"BindResourceType": "VPC", "ManagedType": "agent-exporter",
				"BindResourceStatus": "Available", "VpcId": "vpc-a",
				"VswitchId": "vsw-a", "SecurityGroupId": "sg-a",
			},
		}},
		"AlibabaCloud.OSS.GetBucketInfo": {RequestID: "req-oss-bucket", Data: map[string]any{
			"BucketInfo": map[string]any{"Bucket": map[string]any{
				"Name": "bucket-a", "AccessControlList": map[string]any{"Grant": "public-read"},
			}},
		}},
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	enricher, ok := any(runtime).(contracts.InventoryBatchEnricher)
	if !ok {
		t.Fatal("Alibaba Cloud runtime does not implement InventoryBatchEnricher")
	}
	items := []contracts.InventoryItem{
		{NativeType: "ACS::VPC::VPC", NativeID: "vpc-a", Normalized: map[string]any{"existing": "vpc"}, Raw: map[string]any{"VpcId": "vpc-a"}},
		{NativeType: "ACS::ECS::Instance", NativeID: "i-a", Normalized: map[string]any{"existing": "instance"}, Raw: map[string]any{"InstanceId": "i-a"}},
		{
			NativeType: "ACS::VPC::VSwitch", NativeID: "vsw-a",
			Normalized: map[string]any{"existing": "vswitch", "vpc_id": "vpc-a", "zone_id": "cn-hangzhou-h"},
			Raw:        map[string]any{"VSwitchId": "vsw-a", "VpcId": "vpc-a", "ZoneId": "cn-hangzhou-h"},
		},
		{NativeType: "ACS::ECS::SecurityGroup", NativeID: "sg-a", Normalized: map[string]any{"existing": "security-group"}, Raw: map[string]any{"SecurityGroupId": "sg-a"}},
		{NativeType: "ACS::ECS::NetworkInterface", NativeID: "eni-a", Normalized: map[string]any{"existing": "network-interface"}, Raw: map[string]any{"NetworkInterfaceId": "eni-a"}},
		{NativeType: "ACS::ECS::Disk", NativeID: "d-a", Normalized: map[string]any{"existing": "disk"}, Raw: map[string]any{"DiskId": "d-a"}},
		{NativeType: armsEnvironmentNativeType, NativeID: "env-a", Normalized: map[string]any{"existing": "environment"}, Raw: map[string]any{"EnvironmentId": "env-a"}},
		{
			NativeType: "ACS::OSS::Bucket", NativeID: "bucket-a",
			Normalized:    map[string]any{"existing": "bucket", "object_count": 1},
			Raw:           map[string]any{"BucketName": "bucket-a", "ObjectCount": 1},
			NativeAliases: []string{},
			ResourceKind: asset.ResourceKind{
				ScopeKinds:    []asset.ScopeKind{},
				Capabilities:  asset.CapabilitySet{},
				SummaryFields: []string{},
			},
		},
	}
	before := cloneTopologyTestItems(t, items)
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) {
		logs = append(logs, entry)
	}))

	enriched, err := enricher.EnrichInventoryBatch(ctx, contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
	}, items)
	if err != nil {
		t.Fatalf("enrich inventory batch: %v", err)
	}
	if !reflect.DeepEqual(items, before) {
		t.Fatalf("input items mutated:\n got: %#v\nwant: %#v", items, before)
	}
	if len(enriched) != len(items) {
		t.Fatalf("enriched items = %d, want %d", len(enriched), len(items))
	}
	if enriched[0].Normalized["vpc_id"] != "vpc-a" {
		t.Fatalf("VPC topology = %#v", enriched[0].Normalized)
	}
	instance := enriched[1]
	if instance.Normalized["vpc_id"] != "vpc-a" || instance.Normalized["vswitch_id"] != "vsw-a" || instance.Normalized["zone_id"] != "cn-hangzhou-h" {
		t.Fatalf("instance topology = %#v", instance.Normalized)
	}
	if !reflect.DeepEqual(instance.Normalized["security_group_ids"], []any{"sg-a", "sg-b"}) {
		t.Fatalf("security_group_ids = %#v", instance.Normalized["security_group_ids"])
	}
	if !reflect.DeepEqual(instance.Normalized["network_interface_ids"], []any{"eni-a"}) {
		t.Fatalf("network_interface_ids = %#v", instance.Normalized["network_interface_ids"])
	}
	if enriched[2].Normalized["vpc_id"] != "vpc-a" ||
		enriched[2].Normalized["zone_id"] != "cn-hangzhou-h" {
		t.Fatalf("vSwitch topology = %#v", enriched[2].Normalized)
	}
	if enriched[3].Normalized["vpc_id"] != "vpc-a" {
		t.Fatalf("security-group topology = %#v", enriched[3].Normalized)
	}
	if enriched[4].Normalized["vswitch_id"] != "vsw-a" {
		t.Fatalf("network-interface topology = %#v", enriched[4].Normalized)
	}
	if enriched[5].Normalized["attached_instance_id"] != "i-a" ||
		enriched[5].Normalized["disk_type"] != "system" ||
		enriched[5].Normalized["delete_with_instance"] != true {
		t.Fatalf("disk topology = %#v", enriched[5].Normalized)
	}
	if enriched[6].Normalized["vpcId"] != "vpc-a" ||
		enriched[6].Normalized["vSwitchId"] != "vsw-a" ||
		enriched[6].Normalized["securityGroupId"] != "sg-a" ||
		enriched[6].Normalized["bindResourceType"] != "VPC" ||
		enriched[6].Normalized["managedType"] != "agent-exporter" {
		t.Fatalf("ARMS environment topology = %#v", enriched[6].Normalized)
	}
	if enriched[7].Normalized["acl"] != "public-read" {
		t.Fatalf("OSS bucket properties = %#v", enriched[7].Normalized)
	}
	if len(factory.calls) != 6 {
		t.Fatalf("detail calls = %d, want 6: %#v", len(factory.calls), factory.calls)
	}
	callsByOperation := make(map[string]contracts.Invocation, len(factory.calls))
	for _, call := range factory.calls {
		callsByOperation[call.Operation] = call
	}
	if callsByOperation["AlibabaCloud.DescribeInstances"].Parameters["InstanceIds"] != `["i-a"]` ||
		callsByOperation["AlibabaCloud.DescribeInstances"].Parameters["MaxResults"] != 1 {
		t.Fatalf("instance detail parameters = %#v", callsByOperation["AlibabaCloud.DescribeInstances"].Parameters)
	}
	if _, called := callsByOperation["AlibabaCloud.DescribeVSwitches"]; called {
		t.Fatalf("vSwitch list result triggered redundant detail call: %#v", factory.calls)
	}
	if callsByOperation["AlibabaCloud.DescribeSecurityGroups"].Parameters["SecurityGroupIds"] != `["sg-a"]` ||
		callsByOperation["AlibabaCloud.DescribeSecurityGroups"].Parameters["MaxResults"] != 1 {
		t.Fatalf("security-group detail parameters = %#v", callsByOperation["AlibabaCloud.DescribeSecurityGroups"].Parameters)
	}
	if !reflect.DeepEqual(callsByOperation["AlibabaCloud.DescribeNetworkInterfaces"].Parameters["NetworkInterfaceId"], []string{"eni-a"}) ||
		callsByOperation["AlibabaCloud.DescribeNetworkInterfaces"].Parameters["MaxResults"] != 1 {
		t.Fatalf("network-interface detail parameters = %#v", callsByOperation["AlibabaCloud.DescribeNetworkInterfaces"].Parameters)
	}
	if callsByOperation["AlibabaCloud.DescribeDisks"].Parameters["DiskIds"] != `["d-a"]` ||
		callsByOperation["AlibabaCloud.DescribeDisks"].Parameters["MaxResults"] != 1 {
		t.Fatalf("disk detail parameters = %#v", callsByOperation["AlibabaCloud.DescribeDisks"].Parameters)
	}
	if callsByOperation["AlibabaCloud.ARMS.DescribeEnvironment"].Parameters["EnvironmentId"] != "env-a" ||
		callsByOperation["AlibabaCloud.ARMS.DescribeEnvironment"].Parameters["RegionId"] != "cn-hangzhou" {
		t.Fatalf("ARMS environment detail parameters = %#v", callsByOperation["AlibabaCloud.ARMS.DescribeEnvironment"].Parameters)
	}
	if callsByOperation["AlibabaCloud.OSS.GetBucketInfo"].Parameters["bucket"] != "bucket-a" ||
		callsByOperation["AlibabaCloud.OSS.GetBucketInfo"].Parameters["location"] != "cn-hangzhou" {
		t.Fatalf("OSS bucket detail parameters = %#v", callsByOperation["AlibabaCloud.OSS.GetBucketInfo"].Parameters)
	}
	responseRequestIDs := make(map[string]string)
	for _, entry := range logs {
		if entry.Kind != execution.JobLogCloudAPIResponse || entry.Level != "info" {
			continue
		}
		requestID, _ := entry.Payload["RequestId"].(string)
		responseRequestIDs[entry.Message] = requestID
	}
	if len(responseRequestIDs) != 6 {
		t.Fatalf("detail response logs = %#v", logs)
	}
	for message, requestID := range responseRequestIDs {
		if requestID == "" {
			t.Fatalf("%s omitted provider request ID: %#v", message, logs)
		}
	}
}

func TestTopologyBatchEnrichmentDiscoversDataWorksOwnershipFromProductAPIs(t *testing.T) {
	t.Parallel()

	const resourceGroupID = "Serverless_res_group_210724245979777_717305919406626"
	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
	}}
	factory := &topologyRuntimeFactory{invoke: func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		switch invocation.Operation {
		case "AlibabaCloud.DataWorks.ListResourceGroupAssociateProjects":
			return contracts.InvocationResult{
				RequestID: "request-projects",
				Data:      map[string]any{"ProjectIdList": []any{float64(3)}},
			}, nil
		case "AlibabaCloud.DataWorks.ListNetworks":
			return contracts.InvocationResult{
				RequestID: "request-networks",
				Data: map[string]any{"PagingInfo": map[string]any{
					"NetworkList": []any{map[string]any{
						"Id": float64(1000), "ResourceGroupId": resourceGroupID,
						"VpcId": "vpc-london", "VswitchId": "vsw-london",
						"SecurityGroupId": "sg-d7ob1fsemhw32ptq0jav", "Status": "Running",
					}},
					"PageNumber": float64(1), "PageSize": float64(100), "TotalCount": float64(1),
				}},
			}, nil
		default:
			return contracts.InvocationResult{}, fmt.Errorf("unexpected operation %s", invocation.Operation)
		}
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	items := []contracts.InventoryItem{{
		NativeType: DataWorksResourceGroupNativeType,
		NativeID:   resourceGroupID,
		Normalized: map[string]any{"existing": "value"},
		Raw:        map[string]any{"ResourceId": resourceGroupID},
	}}
	enriched, err := runtime.EnrichInventoryBatch(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Source:       "resource-center",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
	}, items)
	if err != nil {
		t.Fatalf("enrich DataWorks resource group: %v", err)
	}
	if len(enriched) != 1 ||
		enriched[0].Normalized["existing"] != "value" ||
		enriched[0].Normalized["vpc_id"] != "vpc-london" ||
		enriched[0].Normalized["vswitch_id"] != "vsw-london" ||
		enriched[0].Normalized[NormalizedDataWorksProjectRequestIDField] != "request-projects" {
		t.Fatalf("DataWorks enrichment = %#v", enriched)
	}
	if !reflect.DeepEqual(
		enriched[0].Normalized[NormalizedDataWorksProjectIDsField],
		[]any{"3"},
	) {
		t.Fatalf(
			"DataWorks project IDs = %#v",
			enriched[0].Normalized[NormalizedDataWorksProjectIDsField],
		)
	}
	networks, ok := enriched[0].Normalized[NormalizedDataWorksNetworksField].([]any)
	if !ok || len(networks) != 1 {
		t.Fatalf("DataWorks networks = %#v", enriched[0].Normalized[NormalizedDataWorksNetworksField])
	}
	network, ok := networks[0].(map[string]any)
	if !ok ||
		network["security_group_id"] != "sg-d7ob1fsemhw32ptq0jav" ||
		network["request_id"] != "request-networks" {
		t.Fatalf("DataWorks network = %#v", networks[0])
	}
	if len(factory.calls) != 2 {
		t.Fatalf("DataWorks topology calls = %#v", factory.calls)
	}
	for _, call := range factory.calls {
		if call.Parameters["ResourceGroupId"] != resourceGroupID {
			t.Fatalf("DataWorks topology call = %#v", call)
		}
	}
}

func TestCloneInventoryItemsCopiesLocalizedFieldDisplayNames(t *testing.T) {
	t.Parallel()

	items := []contracts.InventoryItem{{
		ResourceKind: asset.ResourceKind{FieldDisplayNames: map[string]map[string]string{
			"vpc_id": {"zh-CN": "所属专有网络"},
		}},
	}}
	cloned, err := cloneInventoryItems(items)
	if err != nil {
		t.Fatalf("clone inventory items: %v", err)
	}
	cloned[0].ResourceKind.FieldDisplayNames["vpc_id"]["zh-CN"] = "mutated"
	if items[0].ResourceKind.FieldDisplayNames["vpc_id"]["zh-CN"] != "所属专有网络" {
		t.Fatalf("input resource kind field display names were mutated: %+v", items[0].ResourceKind.FieldDisplayNames)
	}
}

func TestTopologyBatchEnrichmentNormalizesDetailFailure(t *testing.T) {
	t.Parallel()

	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
	}}
	factory := &topologyFailingRuntimeFactory{topologyRuntimeFactory: topologyRuntimeFactory{}, err: &APIError{
		Code: "Throttling.User", Message: "slow down", RequestID: "req-throttled", StatusCode: 429,
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	_, err = runtime.EnrichInventoryBatch(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
	}, []contracts.InventoryItem{{NativeType: ecsInstanceNativeType, NativeID: "i-a", Raw: map[string]any{}}})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) || providerError.Provider.Category != execution.ErrorThrottled || providerError.Provider.RequestID != "req-throttled" {
		t.Fatalf("normalized detail error = %#v", err)
	}
	if !strings.Contains(err.Error(), "ecs DescribeInstances") || strings.Contains(err.Error(), "AlibabaCloud.") {
		t.Fatalf("normalized detail error label = %q", err)
	}
}

func TestTopologyBatchEnrichmentLogsTransportFailureWithoutResponsePayload(t *testing.T) {
	t.Parallel()

	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
	}}
	const original = `Post "https://ecs.us-east-1.aliyuncs.com/?RegionId=us-east-1&InstanceIds=%5B%22i-a%22%5D": EOF`
	factory := &topologyFailingRuntimeFactory{
		topologyRuntimeFactory: topologyRuntimeFactory{},
		err:                    errors.New(original),
	}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, entry)
		},
	))

	_, err = runtime.EnrichInventoryBatch(ctx, contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1"},
	}, []contracts.InventoryItem{{
		NativeType: ecsInstanceNativeType,
		NativeID:   "i-a",
		Raw:        map[string]any{},
	}})
	if err == nil {
		t.Fatal("expected provider error")
	}
	if len(logs) != 2 ||
		logs[0].Message != "call ecs DescribeInstances" ||
		logs[1].Kind != execution.JobLogCloudAPIResponse ||
		logs[1].Level != "info" ||
		logs[1].Message != "ecs DescribeInstances failed: "+original ||
		logs[1].Payload != nil {
		t.Fatalf("logs=%#v", logs)
	}
}

func TestTopologyBatchEnrichmentRejectsInvalidDetailCoverage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		items      []contracts.InventoryItem
		records    []any
		nextToken  string
		wantReason string
		wantID     string
	}{
		{
			name: "missing requested record",
			items: []contracts.InventoryItem{
				topologyInventoryItem(ecsInstanceNativeType, "i-a"),
				topologyInventoryItem(ecsInstanceNativeType, "i-b"),
			},
			records:    []any{map[string]any{"InstanceId": "i-a"}},
			wantReason: "missing",
			wantID:     "i-b",
		},
		{
			name: "missing requested record with next token",
			items: []contracts.InventoryItem{
				topologyInventoryItem(ecsInstanceNativeType, "i-a"),
				topologyInventoryItem(ecsInstanceNativeType, "i-b"),
			},
			records:    []any{map[string]any{"InstanceId": "i-a"}},
			nextToken:  "token-2",
			wantReason: "missing",
			wantID:     "i-b",
		},
		{
			name:       "duplicate response record",
			items:      []contracts.InventoryItem{topologyInventoryItem(ecsInstanceNativeType, "i-a")},
			records:    []any{map[string]any{"InstanceId": "i-a"}, map[string]any{"InstanceId": "i-a"}},
			wantReason: "duplicate",
			wantID:     "i-a",
		},
		{
			name:       "unexpected response record",
			items:      []contracts.InventoryItem{topologyInventoryItem(ecsInstanceNativeType, "i-a")},
			records:    []any{map[string]any{"InstanceId": "i-b"}},
			wantReason: "unexpected",
			wantID:     "i-b",
		},
		{
			name:       "malformed response record",
			items:      []contracts.InventoryItem{topologyInventoryItem(ecsInstanceNativeType, "i-a")},
			records:    []any{map[string]any{"InstanceId": "i-a"}, "not-a-record"},
			wantReason: "has no",
			wantID:     "InstanceId",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
				Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
			}}
			data := map[string]any{
				"Instances": map[string]any{"Instance": test.records},
			}
			factory := &topologyRuntimeFactory{responses: map[string]contracts.InvocationResult{
				"AlibabaCloud.DescribeInstances": {
					RequestID: "req-instance",
					Data:      data,
					NextToken: test.nextToken,
				},
			}}
			runtime, err := newRuntime(source, factory)
			if err != nil {
				t.Fatalf("create runtime: %v", err)
			}
			kind := runtime.resourceKindByNativeType[ecsInstanceNativeType]

			_, err = runtime.EnrichInventoryBatch(context.Background(), contracts.InventoryRequest{
				ConnectionID: "connection-a",
				Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
				ResourceKind: &kind,
			}, test.items)
			if err == nil ||
				!strings.Contains(err.Error(), "ecs DescribeInstances") ||
				strings.Contains(err.Error(), "AlibabaCloud.") ||
				!strings.Contains(err.Error(), test.wantReason) ||
				!strings.Contains(err.Error(), test.wantID) ||
				!strings.Contains(err.Error(), "provider_request_id=req-instance") {
				t.Fatalf("detail coverage error = %v, want operation, %q, and %q", err, test.wantReason, test.wantID)
			}
			if test.wantReason == "missing" && !strings.Contains(err.Error(), "resource_ids=") {
				t.Fatalf("missing detail error does not label resource IDs: %v", err)
			}
		})
	}
}

func TestTopologyBatchEnrichmentToleratesConcurrentDeletionInBroadInventory(t *testing.T) {
	t.Parallel()

	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
	}}
	factory := &topologyRuntimeFactory{responses: map[string]contracts.InvocationResult{
		"AlibabaCloud.DescribeNetworkInterfaces": {
			RequestID: "req-network-interface",
			Data: map[string]any{"NetworkInterfaceSets": map[string]any{
				"NetworkInterfaceSet": []any{map[string]any{
					"NetworkInterfaceId": "eni-a", "VpcId": "vpc-a", "VSwitchId": "vsw-a",
				}},
			}},
		},
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}

	enriched, err := runtime.EnrichInventoryBatch(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
	}, []contracts.InventoryItem{
		topologyInventoryItem(networkInterfaceNativeType, "eni-a"),
		topologyInventoryItem(networkInterfaceNativeType, "eni-deleted"),
	})
	if err != nil {
		t.Fatalf("enrich broad inventory with concurrently deleted ENI: %v", err)
	}
	if len(enriched) != 2 || enriched[0].Normalized["vpc_id"] != "vpc-a" {
		t.Fatalf("enriched items=%+v", enriched)
	}
	if _, exists := enriched[1].Normalized["vpc_id"]; exists {
		t.Fatalf("missing ENI detail unexpectedly enriched: %+v", enriched[1])
	}
}

func TestTopologyBatchEnrichmentAcceptsCompleteDetailCoverageWithNextToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		nextToken string
		dataToken string
	}{
		{name: "invocation token", nextToken: "token-2"},
		{name: "response data token", dataToken: "token-data"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
				Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
			}}
			data := map[string]any{
				"Instances": map[string]any{"Instance": []any{
					map[string]any{
						"InstanceId": "i-a",
						"VpcAttributes": map[string]any{
							"VpcId": "vpc-a", "VSwitchId": "vsw-a",
						},
					},
				}},
			}
			if test.dataToken != "" {
				data["NextToken"] = test.dataToken
			}
			factory := &topologyRuntimeFactory{responses: map[string]contracts.InvocationResult{
				"AlibabaCloud.DescribeInstances": {
					RequestID: "req-instance",
					Data:      data,
					NextToken: test.nextToken,
				},
			}}
			runtime, err := newRuntime(source, factory)
			if err != nil {
				t.Fatalf("create runtime: %v", err)
			}

			enriched, err := runtime.EnrichInventoryBatch(context.Background(), contracts.InventoryRequest{
				ConnectionID: "connection-a",
				Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
			}, []contracts.InventoryItem{topologyInventoryItem(ecsInstanceNativeType, "i-a")})
			if err != nil {
				t.Fatalf("enrich complete detail coverage: %v", err)
			}
			if got := enriched[0].Normalized["vpc_id"]; got != "vpc-a" {
				t.Fatalf("vpc_id = %#v, want vpc-a", got)
			}
		})
	}
}

func TestTopologyBatchEnrichmentSkipsEmptyInput(t *testing.T) {
	t.Parallel()

	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
	}}
	factory := &topologyRuntimeFactory{}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}

	enriched, err := runtime.EnrichInventoryBatch(context.Background(), contracts.InventoryRequest{}, nil)
	if err != nil {
		t.Fatalf("enrich empty inventory batch: %v", err)
	}
	if enriched != nil || len(factory.calls) != 0 {
		t.Fatalf("empty enrichment = %#v, calls = %#v", enriched, factory.calls)
	}
}

func TestEndpointServiceTopologyEnrichmentDiscoversLoadBalancerDependencies(t *testing.T) {
	t.Parallel()

	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
	}}
	factory := &topologyRuntimeFactory{invoke: func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		if invocation.Operation != listEndpointServiceResourcesOperation {
			t.Fatalf("unexpected endpoint-service topology operation %q", invocation.Operation)
		}
		if invocation.Parameters["RegionId"] != "cn-hangzhou" ||
			invocation.Parameters["ServiceId"] != "epsrv-a" ||
			invocation.Parameters["MaxResults"] != endpointServiceResourcePageSize {
			t.Fatalf("endpoint-service topology parameters = %#v", invocation.Parameters)
		}
		switch invocation.Parameters["NextToken"] {
		case nil:
			return contracts.InvocationResult{
				RequestID: "req-endpoint-resources-1",
				Data: map[string]any{
					"Resources": []any{
						map[string]any{
							"ResourceType": "alb", "ResourceId": "alb-b",
							"VpcId": "vpc-a", "VSwitchId": "vsw-a", "ZoneId": "cn-hangzhou-h",
						},
						map[string]any{"ResourceType": "NLB", "ResourceId": "nlb-a"},
						map[string]any{"ResourceType": "slb", "ResourceId": "lb-a"},
						map[string]any{"ResourceType": "gwlb", "ResourceId": "gwlb-a"},
						map[string]any{"ResourceType": "vpcNat", "ResourceId": "ngw-a"},
					},
					"NextToken": "page-2",
				},
			}, nil
		case "page-2":
			return contracts.InvocationResult{
				RequestID: "req-endpoint-resources-2",
				Data: map[string]any{"Resources": []any{
					map[string]any{"ResourceType": "alb", "ResourceId": "alb-a"},
					map[string]any{"ResourceType": "alb", "ResourceId": "alb-b"},
				}},
			}, nil
		default:
			t.Fatalf("unexpected endpoint-service resource token %#v", invocation.Parameters["NextToken"])
			return contracts.InvocationResult{}, nil
		}
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	items := []contracts.InventoryItem{{
		NativeType: endpointServiceNativeType,
		NativeID:   "epsrv-a",
		Normalized: map[string]any{"existing": "value"},
		Raw:        map[string]any{"ServiceId": "epsrv-a"},
	}}
	before := cloneTopologyTestItems(t, items)

	enriched, err := runtime.EnrichInventoryBatch(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Source:       "resource-center",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
	}, items)
	if err != nil {
		t.Fatalf("enrich endpoint-service topology: %v", err)
	}
	if !reflect.DeepEqual(items, before) {
		t.Fatalf("endpoint-service enrichment mutated input:\n got: %#v\nwant: %#v", items, before)
	}
	if !reflect.DeepEqual(
		enriched[0].Normalized[normalizedEndpointServiceSLBIDs],
		[]string{"lb-a"},
	) {
		t.Fatalf("endpoint-service SLB IDs = %#v", enriched[0].Normalized[normalizedEndpointServiceSLBIDs])
	}
	if !reflect.DeepEqual(
		enriched[0].Normalized[normalizedEndpointServiceALBIDs],
		[]string{"alb-a", "alb-b"},
	) {
		t.Fatalf("endpoint-service ALB IDs = %#v", enriched[0].Normalized[normalizedEndpointServiceALBIDs])
	}
	if !reflect.DeepEqual(
		enriched[0].Normalized[normalizedEndpointServiceNLBIDs],
		[]string{"nlb-a"},
	) {
		t.Fatalf("endpoint-service NLB IDs = %#v", enriched[0].Normalized[normalizedEndpointServiceNLBIDs])
	}
	if !reflect.DeepEqual(
		enriched[0].Normalized[normalizedEndpointServiceGWLBIDs],
		[]string{"gwlb-a"},
	) {
		t.Fatalf("endpoint-service GWLB IDs = %#v", enriched[0].Normalized[normalizedEndpointServiceGWLBIDs])
	}
	if !reflect.DeepEqual(
		enriched[0].Normalized[normalizedEndpointServiceNATGatewayIDs],
		[]string{"ngw-a"},
	) {
		t.Fatalf("endpoint-service NAT gateway IDs = %#v", enriched[0].Normalized[normalizedEndpointServiceNATGatewayIDs])
	}
	if records, ok := enriched[0].Normalized[normalizedEndpointServiceResourcesField].([]any); !ok || len(records) != 7 {
		t.Fatalf("endpoint-service resource evidence = %#v", enriched[0].Normalized[normalizedEndpointServiceResourcesField])
	}
	if enriched[0].Normalized["existing"] != "value" || len(factory.calls) != 2 {
		t.Fatalf("endpoint-service enrichment = %#v, calls = %#v", enriched, factory.calls)
	}
}

func TestTopologyBatchEnrichmentBatchesAndDeduplicatesDetailIDs(t *testing.T) {
	t.Parallel()

	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
	}}
	factory := &topologyRuntimeFactory{invoke: func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		return completeTopologyDetailResponse(t, invocation), nil
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	items := make([]contracts.InventoryItem, 0, 101*4+4)
	for index := 0; index < 101; index++ {
		items = append(items,
			topologyInventoryItem(ecsInstanceNativeType, fmt.Sprintf("i-%03d", index)),
			topologyInventoryItem(securityGroupNativeType, fmt.Sprintf("sg-%03d", index)),
			topologyInventoryItem(networkInterfaceNativeType, fmt.Sprintf("eni-%03d", index)),
			topologyInventoryItem(diskNativeType, fmt.Sprintf("d-%03d", index)),
		)
	}
	items = append(items,
		topologyInventoryItem(ecsInstanceNativeType, "i-000"),
		topologyInventoryItem(securityGroupNativeType, "sg-000"),
		topologyInventoryItem(networkInterfaceNativeType, "eni-000"),
		topologyInventoryItem(diskNativeType, "d-000"),
	)

	enriched, err := runtime.EnrichInventoryBatch(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
	}, items)
	if err != nil {
		t.Fatalf("enrich inventory batch: %v", err)
	}
	if len(enriched) != len(items) {
		t.Fatalf("enriched items = %d, want %d", len(enriched), len(items))
	}
	callsByOperation := make(map[string][]contracts.Invocation)
	for _, call := range factory.calls {
		callsByOperation[call.Operation] = append(callsByOperation[call.Operation], call)
	}
	assertTopologyJSONBatches(t, callsByOperation["AlibabaCloud.DescribeInstances"], "InstanceIds", "i")
	assertTopologyJSONBatches(t, callsByOperation["AlibabaCloud.DescribeSecurityGroups"], "SecurityGroupIds", "sg")
	assertTopologyArrayBatches(t, callsByOperation["AlibabaCloud.DescribeNetworkInterfaces"], "NetworkInterfaceId", "eni")
	assertTopologyJSONBatches(t, callsByOperation["AlibabaCloud.DescribeDisks"], "DiskIds", "d")
	if len(factory.calls) != 8 {
		t.Fatalf("detail invocation count = %d, want 8: %#v", len(factory.calls), factory.calls)
	}
}

type topologyFailingRuntimeFactory struct {
	topologyRuntimeFactory
	err error
}

func (f *topologyFailingRuntimeFactory) Invoke(context.Context, contracts.Credential, string, catalog.Operation, contracts.Invocation) (contracts.InvocationResult, error) {
	return contracts.InvocationResult{}, f.err
}

func topologyInventoryItem(nativeType, nativeID string) contracts.InventoryItem {
	return contracts.InventoryItem{NativeType: nativeType, NativeID: nativeID, Raw: map[string]any{}}
}

func completeTopologyDetailResponse(t *testing.T, invocation contracts.Invocation) contracts.InvocationResult {
	t.Helper()

	type responseFixture struct {
		parameter    string
		itemsKey     string
		itemKey      string
		identityPath string
		arrayString  bool
		singleID     bool
	}
	fixtures := map[string]responseFixture{
		"AlibabaCloud.DescribeInstances": {
			parameter: "InstanceIds", itemsKey: "Instances", itemKey: "Instance",
			identityPath: "InstanceId", arrayString: true,
		},
		"AlibabaCloud.DescribeSecurityGroups": {
			parameter: "SecurityGroupIds", itemsKey: "SecurityGroups", itemKey: "SecurityGroup",
			identityPath: "SecurityGroupId", arrayString: true,
		},
		"AlibabaCloud.DescribeNetworkInterfaces": {
			parameter: "NetworkInterfaceId", itemsKey: "NetworkInterfaceSets", itemKey: "NetworkInterfaceSet",
			identityPath: "NetworkInterfaceId",
		},
		"AlibabaCloud.DescribeDisks": {
			parameter: "DiskIds", itemsKey: "Disks", itemKey: "Disk",
			identityPath: "DiskId", arrayString: true,
		},
	}
	fixture, ok := fixtures[invocation.Operation]
	if !ok {
		t.Fatalf("unexpected topology detail operation %q", invocation.Operation)
	}
	var ids []string
	switch {
	case fixture.singleID:
		nativeID, ok := invocation.Parameters[fixture.parameter].(string)
		if !ok {
			t.Fatalf("%s parameter = %#v", fixture.parameter, invocation.Parameters)
		}
		ids = []string{nativeID}
	case fixture.arrayString:
		payload, ok := invocation.Parameters[fixture.parameter].(string)
		if !ok || json.Unmarshal([]byte(payload), &ids) != nil {
			t.Fatalf("%s parameter = %#v", fixture.parameter, invocation.Parameters)
		}
	default:
		var ok bool
		ids, ok = invocation.Parameters[fixture.parameter].([]string)
		if !ok {
			t.Fatalf("%s parameter = %#v", fixture.parameter, invocation.Parameters)
		}
	}
	records := make([]any, 0, len(ids))
	for _, nativeID := range ids {
		records = append(records, map[string]any{fixture.identityPath: nativeID})
	}
	return contracts.InvocationResult{Data: map[string]any{
		fixture.itemsKey: map[string]any{fixture.itemKey: records},
	}}
}

func assertTopologyJSONBatches(t *testing.T, calls []contracts.Invocation, parameter, prefix string) {
	t.Helper()
	batches := make([][]string, 0, len(calls))
	for _, call := range calls {
		var ids []string
		payload, ok := call.Parameters[parameter].(string)
		if !ok || json.Unmarshal([]byte(payload), &ids) != nil {
			t.Fatalf("%s JSON batch = %#v", parameter, call.Parameters)
		}
		batches = append(batches, ids)
	}
	assertTopologyBatches(t, calls, batches, prefix)
}

func assertTopologyArrayBatches(t *testing.T, calls []contracts.Invocation, parameter, prefix string) {
	t.Helper()
	batches := make([][]string, 0, len(calls))
	for _, call := range calls {
		ids, ok := call.Parameters[parameter].([]string)
		if !ok {
			t.Fatalf("%s array batch = %#v", parameter, call.Parameters)
		}
		batches = append(batches, ids)
	}
	assertTopologyBatches(t, calls, batches, prefix)
}

func assertTopologyBatches(t *testing.T, calls []contracts.Invocation, batches [][]string, prefix string) {
	t.Helper()
	if len(calls) != 2 || len(batches) != 2 || len(batches[0]) != 100 || len(batches[1]) != 1 {
		t.Fatalf("%s batches = %#v calls=%#v", prefix, batches, calls)
	}
	if calls[0].Parameters["MaxResults"] != 100 || calls[1].Parameters["MaxResults"] != 1 {
		t.Fatalf("%s MaxResults = %#v, %#v", prefix, calls[0].Parameters, calls[1].Parameters)
	}
	seen := make(map[string]struct{}, 101)
	for _, batch := range batches {
		for _, id := range batch {
			if _, exists := seen[id]; exists {
				t.Fatalf("%s duplicate detail ID %q in batches %#v", prefix, id, batches)
			}
			seen[id] = struct{}{}
		}
	}
	if len(seen) != 101 {
		t.Fatalf("%s unique detail IDs = %d, want 101", prefix, len(seen))
	}
}

type topologyGraphRepository struct {
	assets        []asset.Asset
	relationships []graph.Relationship
}

func (r *topologyGraphRepository) ListActiveAssetsByConnection(_ context.Context, connectionID asset.ConnectionID, _ asset.ResourceKindID) ([]asset.Asset, error) {
	result := make([]asset.Asset, 0, len(r.assets))
	for _, value := range r.assets {
		if value.Identity.ConnectionID == connectionID && value.ClosedAt == nil {
			result = append(result, value)
		}
	}
	return result, nil
}

func (r *topologyGraphRepository) ReplaceGraph(_ context.Context, _ asset.ScopeID, _ string, relationships []graph.Relationship, _ []graph.LifecycleBinding) error {
	r.relationships = append([]graph.Relationship(nil), relationships...)
	return nil
}

func TestAlibabaTopologyRelationships(t *testing.T) {
	t.Parallel()

	bundle, err := LoadBundle()
	if err != nil {
		t.Fatalf("compile Alibaba Cloud bundle: %v", err)
	}
	const connectionID = asset.ConnectionID("connection-topology")
	repository := &topologyGraphRepository{assets: []asset.Asset{
		alibabaTopologyAsset("asset-vpc", connectionID, "ACS::VPC::VPC", "vpc-a", nil),
		alibabaTopologyAsset("asset-vpc-peer", connectionID, "ACS::VPC::VPC", "vpc-b", nil),
		alibabaTopologyAsset("asset-peer-connection", connectionID, "ACS::VPC::PeerConnection", "pcc-a", map[string]any{
			"vpcId": "vpc-a", "acceptingVpcId": "vpc-b",
		}),
		alibabaTopologyAsset("asset-router-interface", connectionID, "ACS::VPC::RouterInterface", "ri-a", map[string]any{
			"vpcId": "vpc-a", "acceptingVpcId": "vpc-b",
		}),
		alibabaTopologyAsset("asset-route-table", connectionID, "ACS::VPC::RouteTable", "vtb-a", map[string]any{
			"vpcId": "vpc-a",
		}),
		alibabaTopologyAsset("asset-gateway-endpoint", connectionID, VPCGatewayEndpointNativeType, "vpce-a", map[string]any{
			"vpcId": "vpc-a", "associatedRouteTableIds": []any{"vtb-a"},
		}),
		alibabaTopologyAsset("asset-vswitch", connectionID, "ACS::VPC::VSwitch", "vsw-a", map[string]any{"vpc_id": "vpc-a"}),
		alibabaTopologyAsset("asset-slb", connectionID, "ACS::SLB::LoadBalancer", "lb-a", map[string]any{"vpcId": "vpc-a"}),
		alibabaTopologyAsset("asset-alb", connectionID, albLoadBalancerNativeType, "alb-a", map[string]any{"vpcId": "vpc-a"}),
		alibabaTopologyAsset("asset-nlb", connectionID, nlbLoadBalancerNativeType, "nlb-a", map[string]any{"vpcId": "vpc-a"}),
		alibabaTopologyAsset("asset-gwlb", connectionID, "ACS::GWLB::LoadBalancer", "gwlb-a", map[string]any{"vpcId": "vpc-a"}),
		alibabaTopologyAsset("asset-nat", connectionID, "ACS::NAT::NatGateway", "ngw-a", map[string]any{"vpcId": "vpc-a"}),
		alibabaTopologyAsset("asset-endpoint-service", connectionID, endpointServiceNativeType, "epsrv-a", map[string]any{
			normalizedEndpointServiceSLBIDs:        []any{"lb-a"},
			normalizedEndpointServiceALBIDs:        []any{"alb-a"},
			normalizedEndpointServiceNLBIDs:        []any{"nlb-a"},
			normalizedEndpointServiceGWLBIDs:       []any{"gwlb-a"},
			normalizedEndpointServiceNATGatewayIDs: []any{"ngw-a"},
		}),
		alibabaTopologyAsset("asset-endpoint", connectionID, endpointNativeType, "ep-a", map[string]any{
			"serviceId": "epsrv-a",
			"vpcId":     "vpc-a",
		}),
		alibabaTopologyAsset("asset-ecs", connectionID, "ACS::ECS::Instance", "i-a", map[string]any{
			"vpc_id": "vpc-a", "vswitch_id": "vsw-a",
			"security_group_ids":    []any{"sg-a"},
			"network_interface_ids": []any{"eni-a"},
		}),
		alibabaTopologyAsset("asset-security-group", connectionID, "ACS::ECS::SecurityGroup", "sg-a", map[string]any{"vpc_id": "vpc-a"}),
		alibabaTopologyAsset("asset-disk", connectionID, "ACS::ECS::Disk", "d-a", map[string]any{"attached_instance_id": "i-a"}),
		alibabaTopologyAsset("asset-nas-file-system", connectionID, "ACS::NAS::FileSystem", "nas-a", nil),
		alibabaTopologyAsset("asset-nas-mount-target", connectionID, "ACS::NAS::MountTarget", "nas-a-w.cn-hangzhou.nas.aliyuncs.com", map[string]any{
			"fileSystemId": "nas-a", "vpcId": "vpc-a", "vSwitchId": "vsw-a",
		}),
		alibabaTopologyAsset("asset-vpn-gateway", connectionID, "ACS::VPN::VpnGateway", "vpn-a", map[string]any{"vpcId": "vpc-a"}),
		alibabaTopologyAsset("asset-customer-gateway", connectionID, "ACS::VPN::CustomerGateway", "cgw-a", nil),
		alibabaTopologyAsset("asset-vpn-connection", connectionID, "ACS::VPN::VpnConnection", "vco-a", map[string]any{
			"vpnGatewayId": "vpn-a", "customerGatewayId": "cgw-a",
		}),
		alibabaTopologyAsset("asset-network-interface", connectionID, "ACS::ECS::NetworkInterface", "eni-a", map[string]any{
			"vpc_id": "vpc-a", "vswitch_id": "vsw-a", "security_group_ids": []any{"sg-a"},
		}),
		alibabaTopologyAsset("asset-arms-environment", connectionID, ARMSEnvironmentNativeType, "env-a", map[string]any{
			"vpcId": "vpc-a", "vSwitchId": "vsw-a", "securityGroupId": "sg-a",
		}),
	}}
	result, err := governance.NewService(repository, repository).RebuildGraph(
		context.Background(), "scope-topology", connectionID, "graph-topology", bundle, nil,
	)
	if err != nil {
		t.Fatalf("rebuild Alibaba Cloud graph: %v", err)
	}
	if len(result.Unresolved) != 0 {
		t.Fatalf("unresolved topology references = %#v", result.Unresolved)
	}
	want := map[string]struct{}{
		"asset-peer-connection|member_of|asset-vpc":              {},
		"asset-peer-connection|member_of|asset-vpc-peer":         {},
		"asset-router-interface|member_of|asset-vpc":             {},
		"asset-router-interface|member_of|asset-vpc-peer":        {},
		"asset-route-table|member_of|asset-vpc":                  {},
		"asset-gateway-endpoint|member_of|asset-vpc":             {},
		"asset-gateway-endpoint|uses|asset-route-table":          {},
		"asset-slb|member_of|asset-vpc":                          {},
		"asset-alb|member_of|asset-vpc":                          {},
		"asset-nlb|member_of|asset-vpc":                          {},
		"asset-nat|member_of|asset-vpc":                          {},
		"asset-endpoint-service|depends_on|asset-slb":            {},
		"asset-endpoint-service|depends_on|asset-alb":            {},
		"asset-endpoint-service|depends_on|asset-nlb":            {},
		"asset-endpoint-service|depends_on|asset-gwlb":           {},
		"asset-endpoint-service|depends_on|asset-nat":            {},
		"asset-endpoint|uses|asset-endpoint-service":             {},
		"asset-endpoint|member_of|asset-vpc":                     {},
		"asset-ecs|member_of|asset-vpc":                          {},
		"asset-ecs|member_of|asset-vswitch":                      {},
		"asset-ecs|uses|asset-security-group":                    {},
		"asset-ecs|uses|asset-network-interface":                 {},
		"asset-disk|attached_to|asset-ecs":                       {},
		"asset-nas-mount-target|member_of|asset-nas-file-system": {},
		"asset-nas-mount-target|member_of|asset-vpc":             {},
		"asset-nas-mount-target|member_of|asset-vswitch":         {},
		"asset-vpn-connection|uses|asset-vpn-gateway":            {},
		"asset-vpn-connection|member_of|asset-customer-gateway":  {},
		"asset-vpn-gateway|member_of|asset-vpc":                  {},
		"asset-vswitch|member_of|asset-vpc":                      {},
		"asset-security-group|member_of|asset-vpc":               {},
		"asset-network-interface|member_of|asset-vpc":            {},
		"asset-network-interface|member_of|asset-vswitch":        {},
		"asset-network-interface|uses|asset-security-group":      {},
		"asset-arms-environment|member_of|asset-vpc":             {},
		"asset-arms-environment|member_of|asset-vswitch":         {},
		"asset-arms-environment|uses|asset-security-group":       {},
	}
	got := make(map[string]struct{}, len(result.Relationships))
	stableAssetIDs := make(map[asset.AssetID]struct{}, len(repository.assets))
	for _, value := range repository.assets {
		stableAssetIDs[value.ID] = struct{}{}
		if value.Identity.NativeType == "SecurityGroupMembership" {
			t.Fatalf("intermediate membership asset created: %#v", value)
		}
	}
	for _, relationship := range result.Relationships {
		if _, ok := stableAssetIDs[relationship.SourceAssetID]; !ok {
			t.Fatalf("relationship uses unstable source asset ID: %#v", relationship)
		}
		if _, ok := stableAssetIDs[relationship.TargetAssetID]; !ok {
			t.Fatalf("relationship uses unstable target asset ID: %#v", relationship)
		}
		got[string(relationship.SourceAssetID)+"|"+string(relationship.Type)+"|"+string(relationship.TargetAssetID)] = struct{}{}
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Alibaba Cloud topology facts:\n got: %#v\nwant: %#v", got, want)
	}
}

func TestAlibabaTopologyResourceSpecsCarryIconsWithoutGroupingContracts(t *testing.T) {
	t.Parallel()

	bundle, err := LoadBundle()
	if err != nil {
		t.Fatalf("compile Alibaba Cloud bundle: %v", err)
	}
	want := map[string]struct {
		class             string
		icon              string
		relationshipCount int
	}{
		vpcNativeType:              {class: "network.vpc", icon: "network", relationshipCount: 1},
		ACKClusterNativeType:       {class: "container.cluster", icon: "cluster", relationshipCount: 3},
		vSwitchNativeType:          {class: "network.subnet", icon: "network", relationshipCount: 1},
		securityGroupNativeType:    {class: "network.security_group", icon: "shield", relationshipCount: 1},
		diskNativeType:             {class: "storage.block", icon: "disk", relationshipCount: 1},
		networkInterfaceNativeType: {class: "network.interface", icon: "network", relationshipCount: 3},
		armsEnvironmentNativeType:  {class: "observability.monitoring_environment", icon: "application", relationshipCount: 3},
		endpointNativeType:         {class: "network.endpoint", icon: "network", relationshipCount: 4},
		endpointServiceNativeType:  {class: "network.endpoint_service", icon: "network", relationshipCount: 5},
		"ACS::NAT::NatIp":          {class: "network.nat_ip", icon: "network", relationshipCount: 1},
		"ACS::NAS::MountTarget":    {class: "storage.mount_target", icon: "disk", relationshipCount: 3},
	}
	seen := make(map[string]struct{}, len(want))
	for _, compiled := range bundle.Specs {
		expected, ok := want[compiled.ResourceKind.NativeType]
		if !ok {
			continue
		}
		if compiled.ResourceKind.Class != expected.class || compiled.ResourceKind.Icon != expected.icon ||
			compiled.Definition.Presentation.Icon != expected.icon {
			t.Fatalf("%s grouping-free presentation = kind:%#v presentation:%#v", compiled.ResourceKind.NativeType, compiled.ResourceKind, compiled.Definition.Presentation)
		}
		if len(compiled.Definition.Relationships) != expected.relationshipCount {
			t.Fatalf("%s relationships = %#v", compiled.ResourceKind.NativeType, compiled.Definition.Relationships)
		}
		seen[compiled.ResourceKind.NativeType] = struct{}{}
	}
	if len(seen) != len(want) {
		t.Fatalf("verified resource specs = %#v, want %#v", seen, want)
	}
}

func alibabaTopologyAsset(id asset.AssetID, connectionID asset.ConnectionID, nativeType, nativeID string, normalized map[string]any) asset.Asset {
	return asset.Asset{
		ID: id,
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: connectionID,
			NativeType: nativeType, NativeID: nativeID,
		},
		ScopeID: "scope-topology", Normalized: normalized,
	}
}

func cloneTopologyTestItems(t *testing.T, items []contracts.InventoryItem) []contracts.InventoryItem {
	t.Helper()
	cloned := append([]contracts.InventoryItem(nil), items...)
	for index := range cloned {
		cloned[index].Normalized = make(map[string]any, len(items[index].Normalized))
		for key, value := range items[index].Normalized {
			cloned[index].Normalized[key] = value
		}
		cloned[index].Raw = make(map[string]any, len(items[index].Raw))
		for key, value := range items[index].Raw {
			cloned[index].Raw[key] = value
		}
	}
	return cloned
}
