package alicloud_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/providers/alicloud"
)

type scriptedProvider struct {
	respond     func(contracts.Invocation) (contracts.InvocationResult, error)
	invocations []contracts.Invocation
}

func (p *scriptedProvider) Provider() asset.Provider { return asset.ProviderAliCloud }

func (p *scriptedProvider) Invoke(_ context.Context, invocation contracts.Invocation) (contracts.InvocationResult, error) {
	p.invocations = append(p.invocations, invocation)
	return p.respond(invocation)
}

func (p *scriptedProvider) calls(operation string) []contracts.Invocation {
	var result []contracts.Invocation
	for _, invocation := range p.invocations {
		if invocation.Operation == operation {
			result = append(result, invocation)
		}
	}
	return result
}

func cenActionRequest(nativeType, nativeID string) contracts.ActionRequest {
	return contracts.ActionRequest{
		Asset: asset.Asset{
			ID: asset.AssetID(nativeID),
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, NativeType: nativeType, NativeID: nativeID,
			},
			Normalized: map[string]any{"transitRouterId": "tr-a", "cenId": "cen-a", "regionId": "cn-hangzhou"},
		},
		Action: "delete", IdempotencyKey: "step-" + nativeID,
	}
}

func TestCENQosPolicyDeletesNonDefaultQueuesFirst(t *testing.T) {
	t.Parallel()

	// DeleteCenInterRegionTrafficQosPolicy fails while queues other than the
	// default exist; the provider refuses to delete the default queue.
	queues := []any{
		map[string]any{"QosQueueId": "qos-queue-default"},
		map[string]any{"QosQueueId": "qos-queue-a"},
	}
	provider := &scriptedProvider{}
	provider.respond = func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		switch invocation.Operation {
		case "AlibabaCloud.CEN.ListCenInterRegionTrafficQosPolicies":
			return contracts.InvocationResult{Data: map[string]any{"TrafficQosPolicies": []any{map[string]any{
				"TrafficQosPolicyId": "qos-a", "TrafficQosPolicyStatus": "Active", "TrafficQosQueues": queues,
			}}}}, nil
		case "AlibabaCloud.CEN.DeleteCenInterRegionTrafficQosQueue":
			if invocation.Parameters["QosQueueId"] == "qos-queue-default" {
				return contracts.InvocationResult{}, alicloud.NormalizeError(&alicloud.APIError{
					Code: "OperationFailed.NotSupportDeleteDefaultQueue", StatusCode: 400,
				})
			}
			queues = queues[:1]
			return contracts.InvocationResult{RequestID: "delete-queue"}, nil
		case "AlibabaCloud.CEN.DeleteCenInterRegionTrafficQosPolicy":
			return contracts.InvocationResult{RequestID: "delete-policy"}, nil
		}
		t.Fatalf("unexpected call %s", invocation.Operation)
		return contracts.InvocationResult{}, nil
	}
	hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", alicloud.CENInterRegionTrafficQosPolicyNativeType)
	if err != nil {
		t.Fatal(err)
	}
	request := cenActionRequest(alicloud.CENInterRegionTrafficQosPolicyNativeType, "qos-a")
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.Data["phase"] != "pre_delete_cleanup" || result.Data["deleted_qos_queue_id"] != "qos-queue-a" {
		t.Fatalf("execute result=%+v err=%v", result, err)
	}
	if deletes := provider.calls("AlibabaCloud.CEN.DeleteCenInterRegionTrafficQosPolicy"); len(deletes) != 0 {
		t.Fatalf("policy deleted before its queues: %+v", deletes)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.State != "delete_requested" {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	deletes := provider.calls("AlibabaCloud.CEN.DeleteCenInterRegionTrafficQosPolicy")
	if len(deletes) != 1 || deletes[0].Parameters["TrafficQosPolicyId"] != "qos-a" {
		t.Fatalf("policy deletes = %+v", deletes)
	}
}

func TestCENMulticastDomainRemovesGroupsThenAssociationsBeforeDeletion(t *testing.T) {
	t.Parallel()

	groups := []any{
		map[string]any{"GroupIpAddress": "239.0.0.1", "NetworkInterfaceId": "eni-source", "GroupSource": true, "GroupMember": false, "Status": "Registered"},
		map[string]any{"GroupIpAddress": "239.0.0.1", "NetworkInterfaceId": "eni-member", "GroupSource": false, "GroupMember": true, "Status": "Registered"},
		map[string]any{"GroupIpAddress": "239.0.0.1", "PeerTransitRouterMulticastDomainId": "tr-mcast-domain-peer", "GroupMember": true, "Status": "Registered"},
	}
	associations := []any{
		map[string]any{"TransitRouterAttachmentId": "tr-attach-a", "VSwitchId": "vsw-a", "Status": "Associated"},
		map[string]any{"TransitRouterAttachmentId": "tr-attach-a", "VSwitchId": "vsw-b", "Status": "Associated"},
	}
	provider := &scriptedProvider{}
	provider.respond = func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		switch invocation.Operation {
		case "AlibabaCloud.CEN.ListTransitRouterMulticastDomains":
			return contracts.InvocationResult{Data: map[string]any{"TransitRouterMulticastDomains": []any{map[string]any{
				"TransitRouterMulticastDomainId": "tr-mcast-domain-a", "Status": "Active",
			}}}}, nil
		case "AlibabaCloud.CEN.ListTransitRouterMulticastGroups":
			return contracts.InvocationResult{Data: map[string]any{"TransitRouterMulticastGroups": groups}}, nil
		case "AlibabaCloud.CEN.DeregisterTransitRouterMulticastGroupSources",
			"AlibabaCloud.CEN.DeregisterTransitRouterMulticastGroupMembers":
			groups = nil
			return contracts.InvocationResult{RequestID: "deregister"}, nil
		case "AlibabaCloud.CEN.ListTransitRouterMulticastDomainAssociations":
			return contracts.InvocationResult{Data: map[string]any{"TransitRouterMulticastAssociations": associations}}, nil
		case "AlibabaCloud.CEN.DisassociateTransitRouterMulticastDomain":
			associations = nil
			return contracts.InvocationResult{RequestID: "disassociate"}, nil
		case "AlibabaCloud.CEN.DeleteTransitRouterMulticastDomain":
			return contracts.InvocationResult{RequestID: "delete-domain"}, nil
		}
		t.Fatalf("unexpected call %s", invocation.Operation)
		return contracts.InvocationResult{}, nil
	}
	hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", alicloud.CENTransitRouterMulticastDomainNativeType)
	if err != nil {
		t.Fatal(err)
	}
	request := cenActionRequest(alicloud.CENTransitRouterMulticastDomainNativeType, "tr-mcast-domain-a")
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.Data["phase"] != "pre_delete_cleanup" {
		t.Fatalf("execute result=%+v err=%v", result, err)
	}
	sources := provider.calls("AlibabaCloud.CEN.DeregisterTransitRouterMulticastGroupSources")
	members := provider.calls("AlibabaCloud.CEN.DeregisterTransitRouterMulticastGroupMembers")
	if len(sources) != 1 || !reflect.DeepEqual(sources[0].Parameters["NetworkInterfaceIds"], []string{"eni-source"}) ||
		len(members) != 2 || !reflect.DeepEqual(members[0].Parameters["NetworkInterfaceIds"], []string{"eni-member"}) ||
		!reflect.DeepEqual(members[1].Parameters["PeerTransitRouterMulticastDomains"], []string{"tr-mcast-domain-peer"}) {
		t.Fatalf("deregistrations sources=%+v members=%+v", sources, members)
	}
	if len(provider.calls("AlibabaCloud.CEN.DisassociateTransitRouterMulticastDomain")) != 0 {
		t.Fatal("vSwitches disassociated while group members remain")
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "pre_delete_cleanup" {
		t.Fatalf("disassociation wait=%+v err=%v", wait, err)
	}
	disassociations := provider.calls("AlibabaCloud.CEN.DisassociateTransitRouterMulticastDomain")
	if len(disassociations) != 1 || disassociations[0].Parameters["TransitRouterAttachmentId"] != "tr-attach-a" ||
		!reflect.DeepEqual(disassociations[0].Parameters["VSwitchIds"], []string{"vsw-a", "vsw-b"}) {
		t.Fatalf("disassociations = %+v", disassociations)
	}
	wait, err = hook.Wait(context.Background(), request, contracts.ActionResult{Data: wait.Data})
	if err != nil || !wait.Done || wait.State != "delete_requested" {
		t.Fatalf("delete wait=%+v err=%v", wait, err)
	}
	if deletes := provider.calls("AlibabaCloud.CEN.DeleteTransitRouterMulticastDomain"); len(deletes) != 1 {
		t.Fatalf("domain deletes = %+v", deletes)
	}
}

func TestCENVBRAttachmentManagedByCloudServiceIsNotDeleted(t *testing.T) {
	t.Parallel()

	// ListTransitRouterVbrAttachments returns ManagedService only for a
	// connection a cloud service manages; DeleteTransitRouterVbrAttachment
	// rejects it with OperationNotPermitted.AttachmentManagedByCloudService.
	for managedService, allowed := range map[string]bool{"": true, "ExpressConnectRouter": false} {
		provider := &scriptedProvider{respond: func(contracts.Invocation) (contracts.InvocationResult, error) {
			record := map[string]any{"TransitRouterAttachmentId": "tr-attach-vbr", "Status": "Attached"}
			if managedService != "" {
				record["ManagedService"] = managedService
			}
			return contracts.InvocationResult{Data: map[string]any{"TransitRouterAttachments": []any{record}}}, nil
		}}
		hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", alicloud.CENTransitRouterVBRAttachmentNativeType)
		if err != nil {
			t.Fatal(err)
		}
		preflight, err := hook.Preflight(context.Background(), cenActionRequest(alicloud.CENTransitRouterVBRAttachmentNativeType, "tr-attach-vbr"))
		if err != nil || preflight.Allowed != allowed {
			t.Fatalf("managed service %q preflight=%+v err=%v", managedService, preflight, err)
		}
	}
}

func TestAPIGatewayAPIIsAbolishedInEveryDeployedStageBeforeDeletion(t *testing.T) {
	t.Parallel()

	// DeleteApi refuses an API that still runs in an environment; AbolishApi
	// takes it offline per StageName (CloudAPI 2016-07-14).
	deployed := []any{
		map[string]any{"StageName": "RELEASE", "DeployedStatus": "DEPLOYED"},
		map[string]any{"StageName": "PRE", "DeployedStatus": "NONDEPLOYED"},
		map[string]any{"StageName": "TEST", "DeployedStatus": "DEPLOYED"},
	}
	provider := &scriptedProvider{}
	provider.respond = func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		switch invocation.Operation {
		case "AlibabaCloud.ApiGateway.DescribeApis":
			return contracts.InvocationResult{Data: map[string]any{"TotalCount": 1, "ApiSummarys": map[string]any{"ApiSummary": []any{map[string]any{
				"ApiId": "api-a", "GroupId": "group-a", "DeployedInfos": map[string]any{"DeployedInfo": deployed},
			}}}}}, nil
		case "AlibabaCloud.ApiGateway.AbolishApi":
			return contracts.InvocationResult{RequestID: "abolish"}, nil
		case "AlibabaCloud.ApiGateway.DeleteApi":
			return contracts.InvocationResult{RequestID: "delete-api"}, nil
		}
		t.Fatalf("unexpected call %s", invocation.Operation)
		return contracts.InvocationResult{}, nil
	}
	hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", "ACS::ApiGateway::Api")
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{
			ID:         "api-a",
			Identity:   asset.Identity{Provider: asset.ProviderAliCloud, NativeType: "ACS::ApiGateway::Api", NativeID: "api-a"},
			Normalized: map[string]any{"groupId": "group-a"},
		},
		Action: "delete", IdempotencyKey: "step-api-a",
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.Data["phase"] != "pre_delete_cleanup" {
		t.Fatalf("execute result=%+v err=%v", result, err)
	}
	abolished := provider.calls("AlibabaCloud.ApiGateway.AbolishApi")
	if len(abolished) != 2 || abolished[0].Parameters["StageName"] != "RELEASE" || abolished[1].Parameters["StageName"] != "TEST" ||
		abolished[0].Parameters["GroupId"] != "group-a" || len(provider.calls("AlibabaCloud.ApiGateway.DeleteApi")) != 0 {
		t.Fatalf("abolish calls = %+v", provider.invocations)
	}
	deployed = nil
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.State != "delete_requested" {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	deletes := provider.calls("AlibabaCloud.ApiGateway.DeleteApi")
	if len(deletes) != 1 || deletes[0].Parameters["GroupId"] != "group-a" || deletes[0].Parameters["ApiId"] != "api-a" {
		t.Fatalf("delete calls = %+v", deletes)
	}
}

func TestConfigAggregatorAbsenceIsProvenByACompleteListing(t *testing.T) {
	t.Parallel()

	// GetAggregator answers Invalid.AggregatorId.Value for both a malformed
	// and a deleted aggregator, so readback lists every aggregator instead.
	provider := &scriptedProvider{respond: func(contracts.Invocation) (contracts.InvocationResult, error) {
		return contracts.InvocationResult{Data: map[string]any{"AggregatorsResult": map[string]any{
			"Aggregators": []any{map[string]any{"AggregatorId": "ca-other", "AggregatorStatus": 1}},
		}}}, nil
	}}
	hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-shanghai", "ACS::Config::Aggregator")
	if err != nil {
		t.Fatal(err)
	}
	readback, err := hook.Readback(context.Background(), contracts.ActionRequest{
		Asset: asset.Asset{ID: "ca-a", Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: "ACS::Config::Aggregator", NativeID: "ca-a",
		}},
		Action: "delete", IdempotencyKey: "step-ca-a",
	})
	if err != nil || readback.Exists {
		t.Fatalf("readback=%+v err=%v", readback, err)
	}
}

func TestResolverRuleIsUnboundFromItsVPCsBeforeDeletion(t *testing.T) {
	t.Parallel()

	// DeleteResolverRule fails while the rule is bound to a VPC, and
	// BindResolverRuleVpc replaces the binding list (pvtz 2018-01-01).
	bound := []any{map[string]any{"VpcId": "vpc-a", "RegionId": "cn-hangzhou"}}
	provider := &scriptedProvider{}
	provider.respond = func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		switch invocation.Operation {
		case "AlibabaCloud.PrivateZone.DescribeResolverRules":
			return contracts.InvocationResult{Data: map[string]any{"TotalItems": 1, "Rules": []any{map[string]any{"Id": "hr-rule", "EndpointId": "hre-a", "BindVpcs": bound}}}}, nil
		case "AlibabaCloud.PrivateZone.BindResolverRuleVpc":
			if _, present := invocation.Parameters["Vpc"]; present {
				t.Fatal("unbinding kept VPCs")
			}
			bound = nil
			return contracts.InvocationResult{RequestID: "unbind"}, nil
		case "AlibabaCloud.PrivateZone.DeleteResolverRule":
			return contracts.InvocationResult{RequestID: "delete-rule"}, nil
		}
		t.Fatalf("unexpected call %s", invocation.Operation)
		return contracts.InvocationResult{}, nil
	}
	hook, err := alicloud.NewActionHook(provider, "connection-a", "cn-hangzhou", "ACS::PrivateZone::ResolverRule")
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{
		Asset: asset.Asset{ID: "rule", Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: "ACS::PrivateZone::ResolverRule", NativeID: "hr-rule"},
			Normalized: map[string]any{"endpointId": "hre-a"}},
		Action: "delete", IdempotencyKey: "step-rule",
	}
	result, err := hook.Execute(context.Background(), request)
	if err != nil || result.Data["phase"] != "pre_delete_cleanup" || len(provider.calls("AlibabaCloud.PrivateZone.DeleteResolverRule")) != 0 {
		t.Fatalf("execute result=%+v err=%v calls=%+v", result, err, provider.invocations)
	}
	wait, err := hook.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || len(provider.calls("AlibabaCloud.PrivateZone.DeleteResolverRule")) != 1 {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
}
