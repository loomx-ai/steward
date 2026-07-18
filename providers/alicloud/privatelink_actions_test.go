package alicloud_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/providers/alicloud"
)

func TestPrivateLinkEndpointActionRemovesZonesSeriallyBeforeEndpoint(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		privateLinkEndpointResult("endpoint-active", "Active"),
		privateLinkZonesResult("zones-two",
			map[string]any{
				"ZoneId": "cn-chengdu-a", "EniId": "eni-2vcctvm3a93n1th0rpp6",
				"VSwitchId": "vsw-a", "ZoneStatus": "Connected",
			},
			map[string]any{
				"ZoneId": "cn-chengdu-b", "EniId": "eni-2vcf7po5ka8sz3r6n3ep",
				"ServiceStatus": "Normal", "VSwitchId": "vsw-b", "ZoneStatus": "Connected",
			},
		),
		{RequestID: "remove-zone-a"},
		privateLinkZonesResult("zone-a-deleting",
			map[string]any{
				"ZoneId": "cn-chengdu-a", "EniId": "eni-2vcctvm3a93n1th0rpp6",
				"VSwitchId": "vsw-a", "ZoneStatus": "Deleting",
			},
			map[string]any{
				"ZoneId": "cn-chengdu-b", "EniId": "eni-2vcf7po5ka8sz3r6n3ep",
				"ServiceStatus": "Normal", "VSwitchId": "vsw-b", "ZoneStatus": "Connected",
			},
		),
		privateLinkZonesResult("zones-one", map[string]any{
			"ZoneId": "cn-chengdu-b", "EniId": "eni-2vcf7po5ka8sz3r6n3ep",
			"ServiceStatus": "Normal", "VSwitchId": "vsw-b", "ZoneStatus": "Connected",
		}),
		emptyNetworkInterfacesResult("eni-a-absent"),
		{RequestID: "remove-zone-b"},
		privateLinkZonesResult("zones-empty"),
		emptyNetworkInterfacesResult("eni-b-absent"),
		emptyNetworkInterfacesResult("all-enis-absent-before-endpoint"),
		{RequestID: "delete-endpoint"},
		privateLinkEndpointResult("endpoint-absent", ""),
		emptyNetworkInterfacesResult("all-enis-absent-after-endpoint"),
	}}
	hook, err := alicloud.NewPrivateLinkEndpointHook(provider, "connection-a", "cn-chengdu")
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID: "endpoint",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: "ACS::PrivateLink::VpcEndpoint",
				NativeID: "ep-2vcr8878a2d95456ad5b",
			},
			Normalized: map[string]any{
				alicloud.NormalizedPrivateLinkENIIDsField: []any{
					"eni-2vcctvm3a93n1th0rpp6", "eni-2vcf7po5ka8sz3r6n3ep",
				},
			},
		},
		Action: "delete", IdempotencyKey: "step-endpoint",
	}

	result, err := hook.Execute(context.Background(), request)
	if err != nil {
		t.Fatalf("start PrivateLink endpoint deletion: %v", err)
	}
	if result.Data["phase"] != "remove_zone" ||
		result.Data["active_zone_id"] != "cn-chengdu-a" ||
		result.Data["active_eni_id"] != "eni-2vcctvm3a93n1th0rpp6" {
		t.Fatalf("first PrivateLink delete phase = %#v", result.Data)
	}

	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "removing_zone:cn-chengdu-a" ||
		wait.Data["active_zone_id"] != "cn-chengdu-a" {
		t.Fatalf("active PrivateLink zone phase = %+v, err=%v", wait, err)
	}
	result.Data = wait.Data
	wait, err = hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "removing_zone:cn-chengdu-b" ||
		wait.Data["active_zone_id"] != "cn-chengdu-b" {
		t.Fatalf("second PrivateLink zone phase = %+v, err=%v", wait, err)
	}
	result.Data = wait.Data
	wait, err = hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "endpoint_delete_requested" ||
		wait.Data["phase"] != "delete_endpoint" {
		t.Fatalf("PrivateLink endpoint delete phase = %+v, err=%v", wait, err)
	}
	result.Data = wait.Data
	wait, err = hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.State != "absent" {
		t.Fatalf("PrivateLink endpoint completion = %+v, err=%v", wait, err)
	}

	wantOperations := []string{
		"AlibabaCloud.PrivateLink.ListVpcEndpoints",
		"AlibabaCloud.PrivateLink.ListVpcEndpointZones",
		"AlibabaCloud.PrivateLink.RemoveZoneFromVpcEndpoint",
		"AlibabaCloud.PrivateLink.ListVpcEndpointZones",
		"AlibabaCloud.PrivateLink.ListVpcEndpointZones",
		"AlibabaCloud.DescribeNetworkInterfaces",
		"AlibabaCloud.PrivateLink.RemoveZoneFromVpcEndpoint",
		"AlibabaCloud.PrivateLink.ListVpcEndpointZones",
		"AlibabaCloud.DescribeNetworkInterfaces",
		"AlibabaCloud.DescribeNetworkInterfaces",
		"AlibabaCloud.PrivateLink.DeleteVpcEndpoint",
		"AlibabaCloud.PrivateLink.ListVpcEndpoints",
		"AlibabaCloud.DescribeNetworkInterfaces",
	}
	gotOperations := make([]string, len(provider.invocations))
	for index, invocation := range provider.invocations {
		gotOperations[index] = invocation.Operation
	}
	if !reflect.DeepEqual(gotOperations, wantOperations) {
		t.Fatalf("PrivateLink deletion operations:\n got: %#v\nwant: %#v", gotOperations, wantOperations)
	}
	for _, index := range []int{2, 6} {
		invocation := provider.invocations[index]
		zoneID := invocation.Parameters["ZoneId"].(string)
		if invocation.IdempotencyKey != "step-endpoint:zone:"+zoneID {
			t.Fatalf("zone deletion invocation %d = %+v", index, invocation)
		}
	}
	if provider.invocations[10].IdempotencyKey != "step-endpoint" {
		t.Fatalf("endpoint deletion invocation = %+v", provider.invocations[10])
	}
}

func TestPrivateLinkEndpointActionUsesTwoMinuteDeletionCheckTimeout(t *testing.T) {
	t.Parallel()

	hook, err := alicloud.NewPrivateLinkEndpointHook(
		&invocationProvider{},
		"connection-a",
		"cn-chengdu",
	)
	if err != nil {
		t.Fatal(err)
	}
	if timeout := hook.DeletionCheckTimeout(); timeout != 2*time.Minute {
		t.Fatalf("PrivateLink endpoint deletion check timeout = %s, want 2m", timeout)
	}
}

func TestPrivateLinkEndpointActionResumesAnExistingZoneDeletion(t *testing.T) {
	t.Parallel()

	provider := &invocationProvider{results: []contracts.InvocationResult{
		privateLinkEndpointResult("endpoint-active", "Pending"),
		privateLinkZonesResult("zone-deleting", map[string]any{
			"ZoneId": "cn-chengdu-b", "EniId": "eni-2vcf7po5ka8sz3r6n3ep",
			"ZoneStatus": "Deleting",
		}),
	}}
	hook, err := alicloud.NewPrivateLinkEndpointHook(provider, "connection-a", "cn-chengdu")
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: "ACS::PrivateLink::VpcEndpoint",
			NativeID: "ep-2vcr8878a2d95456ad5b",
		}},
		Action: "delete", IdempotencyKey: "step-endpoint",
	}

	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.Data["phase"] != "remove_zone" ||
		result.Data["active_zone_id"] != "cn-chengdu-b" || len(provider.invocations) != 2 {
		t.Fatalf("resume PrivateLink zone deletion = %+v, calls=%+v, err=%v", result, provider.invocations, err)
	}
	for _, invocation := range provider.invocations {
		if invocation.Operation == "AlibabaCloud.PrivateLink.RemoveZoneFromVpcEndpoint" {
			t.Fatalf("existing zone deletion was repeated: %+v", provider.invocations)
		}
	}
}

func privateLinkEndpointResult(requestID string, status string) contracts.InvocationResult {
	endpoints := []any{}
	if status != "" {
		endpoints = append(endpoints, map[string]any{
			"EndpointId": "ep-2vcr8878a2d95456ad5b", "EndpointStatus": status,
		})
	}
	return contracts.InvocationResult{
		RequestID: requestID, Data: map[string]any{"Endpoints": endpoints},
	}
}

func privateLinkZonesResult(requestID string, zones ...map[string]any) contracts.InvocationResult {
	values := make([]any, len(zones))
	for index := range zones {
		values[index] = zones[index]
	}
	return contracts.InvocationResult{
		RequestID: requestID, Data: map[string]any{"Zones": values},
	}
}

func emptyNetworkInterfacesResult(requestID string) contracts.InvocationResult {
	return contracts.InvocationResult{
		RequestID: requestID,
		Data: map[string]any{
			"NetworkInterfaceSets": map[string]any{"NetworkInterfaceSet": []any{}},
		},
	}
}
