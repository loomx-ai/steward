package alicloud

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestCENTopologyInventoryBuildsNestedRegionalModel(t *testing.T) {
	t.Parallel()

	factory := &topologyRuntimeFactory{responses: cenTopologyResponses()}
	runtime, err := newRuntime(
		&credentialSource{
			wantConnection: "connection-a",
			value: contracts.Credential{
				Type: asset.CredentialAliCloudAccessKey,
				Values: map[string]string{
					"access_key_id": "id", "access_key_secret": "secret",
				},
			},
		},
		factory,
	)
	if err != nil {
		t.Fatal(err)
	}

	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope: asset.Scope{
			Kind: asset.ScopeRegion, NativeID: "cn-hangzhou",
			Name: "Hangzhou", Location: "cn-hangzhou",
		},
		Source: "cen-topology",
		Limit:  500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Complete || batch.NextCursor != "" || len(batch.Items) != 7 {
		t.Fatalf("CEN topology batch = %+v", batch)
	}

	items := make(map[string]contracts.InventoryItem, len(batch.Items))
	for _, item := range batch.Items {
		items[item.NativeType+"|"+item.NativeID] = item
		if item.Location != "cn-hangzhou" ||
			item.Scope.Kind != asset.ScopeRegion ||
			item.Normalized[inventorySourceField] != cenTopologySource {
			t.Fatalf("CEN topology item = %+v", item)
		}
	}
	attachment := items[CENTransitRouterVPCAttachmentNativeType+"|tr-attach-a"]
	if attachment.Normalized[NormalizedCENInstanceIDField] != "cen-a" ||
		attachment.Normalized[NormalizedCENTransitRouterIDField] != "tr-a" ||
		attachment.Normalized[NormalizedCENVPCIDField] != "vpc-a" ||
		!containsCENTestReference(attachment.NetworkReferences, "eni-a") {
		t.Fatalf("CEN VPC attachment = %+v", attachment)
	}
	routeTable := items[CENTransitRouterRouteTableNativeType+"|vtb-a"]
	if routeTable.Normalized[NormalizedCENInstanceIDField] != "cen-a" ||
		routeTable.Normalized[NormalizedCENTransitRouterIDField] != "tr-a" ||
		routeTable.Normalized[NormalizedCENTransitRouterRouteTableIDField] != "vtb-a" ||
		len(routeTable.Normalized[NormalizedCENRouteTableAssociationsField].([]map[string]any)) != 1 ||
		len(routeTable.Normalized[NormalizedCENRouteTablePropagationsField].([]map[string]any)) != 1 ||
		len(routeTable.Normalized[NormalizedCENRouteEntriesField].([]map[string]any)) != 1 ||
		len(routeTable.Normalized[NormalizedCENPrefixListAssociationsField].([]map[string]any)) != 1 ||
		len(routeTable.Normalized[NormalizedCENRouteTableAggregationsField].([]map[string]any)) != 1 {
		t.Fatalf("CEN route table = %+v", routeTable)
	}
	cidr := items[CENTransitRouterCidrNativeType+"|cidr-a"]
	if cidr.Normalized["cidr"] != "100.64.0.0/24" {
		t.Fatalf("CEN transit router CIDR = %+v", cidr)
	}
	routeMap := items[CENRouteMapNativeType+"|route-map-a"]
	if routeMap.Normalized[NormalizedCENInstanceIDField] != "cen-a" ||
		routeMap.Normalized[NormalizedCENTransitRouterIDField] != "tr-a" ||
		routeMap.Normalized[NormalizedCENTransitRouterRouteTableIDField] != "vtb-a" {
		t.Fatalf("CEN route map = %+v", routeMap)
	}
	if _, duplicated := items[CENChildInstanceAttachmentNativeType+"|cen-a/vpc-a"]; duplicated {
		t.Fatal("legacy child instance duplicated an Enterprise Edition VPC attachment")
	}

	called := map[string]contracts.Invocation{}
	for _, call := range factory.calls {
		called[call.Operation] = call
	}
	for _, operation := range []string{
		"AlibabaCloud.CEN.DescribeCens",
		"AlibabaCloud.CEN.ListTransitRouters",
		"AlibabaCloud.CEN.ListTransitRouterVpcAttachments",
		"AlibabaCloud.CEN.ListTransitRouterVbrAttachments",
		"AlibabaCloud.CEN.ListTransitRouterVpnAttachments",
		"AlibabaCloud.CEN.ListTransitRouterEcrAttachments",
		"AlibabaCloud.CEN.ListTransitRouterPeerAttachments",
		"AlibabaCloud.CEN.ListTransitRouterCidr",
		"AlibabaCloud.CEN.ListTransitRouterRouteTables",
		"AlibabaCloud.CEN.ListTransitRouterRouteTableAssociations",
		"AlibabaCloud.CEN.ListTransitRouterRouteTablePropagations",
		"AlibabaCloud.CEN.ListTransitRouterRouteEntries",
		"AlibabaCloud.CEN.ListTransitRouterPrefixListAssociation",
		"AlibabaCloud.CEN.DescribeTransitRouteTableAggregation",
		"AlibabaCloud.CEN.ListTrafficMarkingPolicies",
		"AlibabaCloud.CEN.ListCenInterRegionTrafficQosPolicies",
		"AlibabaCloud.CEN.DescribeCenAttachedChildInstances",
		"AlibabaCloud.CEN.DescribeFlowlogs",
		"AlibabaCloud.CEN.DescribeCenRouteMaps",
	} {
		if _, exists := called[operation]; !exists {
			t.Errorf("CEN topology did not call %s", operation)
		}
	}
	if got := called["AlibabaCloud.CEN.ListTransitRouters"].Parameters["RegionId"]; got != "cn-hangzhou" {
		t.Fatalf("ListTransitRouters RegionId = %#v", got)
	}
	if got := called["AlibabaCloud.CEN.ListTransitRouterPrefixListAssociation"].Parameters["TransitRouterTableId"]; got != "vtb-a" {
		t.Fatalf("prefix list route table parameter = %#v", got)
	}
	if _, exists := called["AlibabaCloud.CEN.ListTransitRouterCidr"].Parameters["MaxResults"]; exists {
		t.Fatal("ListTransitRouterCidr does not support MaxResults")
	}
}

func TestCanonicalCENPeerAttachmentRegion(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		region string
		peer   string
		want   string
	}{
		{region: "cn-qingdao", peer: "cn-beijing", want: "cn-beijing"},
		{region: "cn-beijing", peer: "cn-qingdao", want: "cn-beijing"},
		{region: "cn-hangzhou", want: "cn-hangzhou"},
		{peer: "cn-shanghai", want: "cn-shanghai"},
	} {
		if got := canonicalCENPeerAttachmentRegion(test.region, test.peer); got != test.want {
			t.Fatalf("canonical region for %q/%q = %q, want %q", test.region, test.peer, got, test.want)
		}
	}
}

func TestCENTopologyInventoryFiltersExplicitKindAfterCompleteCollection(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(
		&credentialSource{
			wantConnection: "connection-a",
			value: contracts.Credential{
				Type: asset.CredentialAliCloudAccessKey,
				Values: map[string]string{
					"access_key_id": "id", "access_key_secret": "secret",
				},
			},
		},
		&topologyRuntimeFactory{responses: cenTopologyResponses()},
	)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType[CENTransitRouterVPCAttachmentNativeType]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope: asset.Scope{
			Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Location: "cn-hangzhou",
		},
		Source:       "cen-topology",
		ResourceKind: &kind,
		Limit:        1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Complete || len(batch.Items) != 1 ||
		batch.Items[0].NativeType != CENTransitRouterVPCAttachmentNativeType {
		t.Fatalf("kind-filtered CEN topology batch = %+v", batch)
	}
}

func TestCENTopologyInventoryUsesLegacyChildInstancesForBasicTransitRouter(t *testing.T) {
	t.Parallel()

	responses := map[string]contracts.InvocationResult{
		"AlibabaCloud.CEN.DescribeCens": {
			Data: map[string]any{
				"Cens":       map[string]any{"Cen": []any{map[string]any{"CenId": "cen-basic"}}},
				"TotalCount": 1,
			},
		},
		"AlibabaCloud.CEN.ListTransitRouters": {
			Data: map[string]any{
				"TransitRouters": []any{map[string]any{
					"CenId": "cen-basic", "TransitRouterId": "tr-basic",
					"RegionId": "cn-hangzhou", "Type": "Basic",
				}},
				"TotalCount": 1,
			},
		},
		"AlibabaCloud.CEN.DescribeCenAttachedChildInstances": {
			Data: map[string]any{
				"ChildInstances": map[string]any{"ChildInstance": []any{map[string]any{
					"ChildInstanceId": "vpc-basic", "ChildInstanceType": "VPC",
					"ChildInstanceRegionId": "cn-hangzhou", "Status": "Attached",
				}}},
				"TotalCount": 1,
			},
		},
		"AlibabaCloud.CEN.DescribeFlowlogs": {
			Data: map[string]any{
				"FlowLogs": map[string]any{"FlowLog": []any{}}, "TotalCount": 0,
			},
		},
		"AlibabaCloud.CEN.DescribeCenRouteMaps": {
			Data: map[string]any{
				"RouteMaps": map[string]any{"RouteMap": []any{map[string]any{
					"RouteMapId": "route-map-basic", "CenRegionId": "cn-hangzhou",
					"TransitRouterRouteTableId": "vtb-basic", "Priority": 5000,
					"Status": "Active",
				}}},
				"TotalCount": 1,
			},
		},
	}
	factory := &topologyRuntimeFactory{responses: responses}
	runtime, err := newRuntime(
		&credentialSource{
			wantConnection: "connection-a",
			value: contracts.Credential{
				Type: asset.CredentialAliCloudAccessKey,
				Values: map[string]string{
					"access_key_id": "id", "access_key_secret": "secret",
				},
			},
		},
		factory,
	)
	if err != nil {
		t.Fatal(err)
	}
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope: asset.Scope{
			Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Location: "cn-hangzhou",
		},
		Source: "cen-topology",
	})
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Complete || len(batch.Items) != 2 {
		t.Fatalf("basic transit router topology = %+v", batch)
	}
	items := make(map[string]contracts.InventoryItem, len(batch.Items))
	for _, item := range batch.Items {
		items[item.NativeType+"|"+item.NativeID] = item
	}
	child := items[CENChildInstanceAttachmentNativeType+"|cen-basic/vpc-basic"]
	if child.Normalized["childInstanceId"] != "vpc-basic" {
		t.Fatalf("basic transit router child = %+v", child)
	}
	routeMap := items[CENRouteMapNativeType+"|route-map-basic"]
	if routeMap.Normalized[NormalizedCENTransitRouterIDField] != "tr-basic" ||
		routeMap.Normalized[NormalizedCENTransitRouterRouteTableIDField] != "vtb-basic" ||
		routeMap.Normalized["priority"] != 5000 {
		t.Fatalf("basic transit router route map = %+v", routeMap)
	}
	for _, call := range factory.calls {
		if call.Operation == "AlibabaCloud.CEN.ListTransitRouterVpcAttachments" ||
			call.Operation == "AlibabaCloud.CEN.ListTransitRouterRouteTables" {
			t.Fatalf("enterprise-only API called for Basic transit router: %+v", call)
		}
	}
}

func cenTopologyResponses() map[string]contracts.InvocationResult {
	emptyToken := func(path string) contracts.InvocationResult {
		return contracts.InvocationResult{
			RequestID: "request-empty",
			Data: map[string]any{
				path: []any{}, "NextToken": "", "TotalCount": 0,
			},
		}
	}
	return map[string]contracts.InvocationResult{
		"AlibabaCloud.CEN.DescribeCens": {
			RequestID: "request-cens",
			Data: map[string]any{
				"Cens":       map[string]any{"Cen": []any{map[string]any{"CenId": "cen-a"}}},
				"TotalCount": 1,
			},
		},
		"AlibabaCloud.CEN.ListTransitRouters": {
			RequestID: "request-transit-router",
			Data: map[string]any{
				"TransitRouters": []any{map[string]any{
					"CenId": "cen-a", "TransitRouterId": "tr-a", "RegionId": "cn-hangzhou",
					"Type": "Enterprise", "SupportMulticast": false,
				}},
				"TotalCount": 1,
			},
		},
		"AlibabaCloud.CEN.ListTransitRouterVpcAttachments": {
			RequestID: "request-vpc-attachment",
			Data: map[string]any{
				"TransitRouterAttachments": []any{map[string]any{
					"TransitRouterAttachmentId":     "tr-attach-a",
					"TransitRouterId":               "tr-a",
					"CenId":                         "cen-a",
					"VpcId":                         "vpc-a",
					"VpcRegionId":                   "cn-hangzhou",
					"TransitRouterAttachmentStatus": "Attached",
					"ZoneMappings": []any{map[string]any{
						"ZoneId": "cn-hangzhou-h", "VSwitchId": "vsw-a",
						"NetworkInterfaceId": "eni-a",
					}},
				}},
				"NextToken": "",
			},
		},
		"AlibabaCloud.CEN.ListTransitRouterVbrAttachments":  emptyToken("TransitRouterAttachments"),
		"AlibabaCloud.CEN.ListTransitRouterVpnAttachments":  emptyToken("TransitRouterAttachments"),
		"AlibabaCloud.CEN.ListTransitRouterEcrAttachments":  emptyToken("TransitRouterAttachments"),
		"AlibabaCloud.CEN.ListTransitRouterPeerAttachments": emptyToken("TransitRouterAttachments"),
		"AlibabaCloud.CEN.ListTransitRouterCidr": {
			RequestID: "request-cidr",
			Data: map[string]any{"CidrLists": []any{map[string]any{
				"TransitRouterCidrId": "cidr-a", "TransitRouterId": "tr-a",
				"Cidr": "100.64.0.0/24", "Name": "vpn-cidr",
			}}},
		},
		"AlibabaCloud.CEN.ListTransitRouterRouteTables": {
			RequestID: "request-route-table",
			Data: map[string]any{
				"TransitRouterRouteTables": []any{map[string]any{
					"TransitRouterRouteTableId":     "vtb-a",
					"TransitRouterRouteTableName":   "default",
					"TransitRouterRouteTableStatus": "Active",
				}},
				"NextToken": "",
			},
		},
		"AlibabaCloud.CEN.ListTransitRouterRouteTableAssociations": {
			RequestID: "request-association",
			Data: map[string]any{
				"TransitRouterAssociations": []any{map[string]any{
					"TransitRouterAttachmentId": "tr-attach-a", "ResourceType": "VPC",
				}},
				"NextToken": "",
			},
		},
		"AlibabaCloud.CEN.ListTransitRouterRouteTablePropagations": {
			RequestID: "request-propagation",
			Data: map[string]any{
				"TransitRouterPropagations": []any{map[string]any{
					"TransitRouterAttachmentId": "tr-attach-a", "ResourceType": "VPC",
				}},
				"NextToken": "",
			},
		},
		"AlibabaCloud.CEN.ListTransitRouterRouteEntries": {
			RequestID: "request-route-entry",
			Data: map[string]any{
				"TransitRouterRouteEntries": []any{map[string]any{
					"TransitRouterRouteEntryDestinationCidrBlock": "10.0.0.0/8",
					"TransitRouterRouteEntryNextHopId":            "tr-attach-a",
					"TransitRouterRouteEntryNextHopType":          "Attachment",
				}},
				"NextToken": "",
			},
		},
		"AlibabaCloud.CEN.ListTransitRouterPrefixListAssociation": {
			RequestID: "request-prefix-list",
			Data: map[string]any{
				"PrefixLists": []any{map[string]any{
					"PrefixListId": "pl-a", "NextHop": "tr-attach-a", "NextHopType": "VPC",
				}},
				"TotalCount": 1,
			},
		},
		"AlibabaCloud.CEN.DescribeTransitRouteTableAggregation": {
			RequestID: "request-aggregation",
			Data: map[string]any{
				"Data": []any{map[string]any{
					"TransitRouteTableAggregationCidr": "10.0.0.0/8", "Status": "AllConfigured",
				}},
				"NextToken": "",
			},
		},
		"AlibabaCloud.CEN.ListTrafficMarkingPolicies": {
			RequestID: "request-marking",
			Data: map[string]any{
				"TrafficMarkingPolicies": []any{map[string]any{
					"TrafficMarkingPolicyId": "tm-a", "TrafficMarkingPolicyName": "mark",
				}},
				"NextToken": "",
			},
		},
		"AlibabaCloud.CEN.ListCenInterRegionTrafficQosPolicies": {
			RequestID: "request-qos",
			Data: map[string]any{
				"TrafficQosPolicies": []any{map[string]any{
					"TrafficQosPolicyId": "qos-a", "TrafficQosPolicyName": "qos",
					"TransitRouterAttachmentId": "tr-peer-a",
				}},
				"NextToken": "",
			},
		},
		"AlibabaCloud.CEN.DescribeCenAttachedChildInstances": {
			RequestID: "request-child",
			Data: map[string]any{
				"ChildInstances": map[string]any{"ChildInstance": []any{map[string]any{
					"ChildInstanceId": "vpc-a", "ChildInstanceType": "VPC",
					"ChildInstanceRegionId": "cn-hangzhou", "Status": "Attached",
				}}},
				"TotalCount": 1,
			},
		},
		"AlibabaCloud.CEN.DescribeFlowlogs": {
			RequestID: "request-flowlog",
			Data: map[string]any{
				"FlowLogs": map[string]any{"FlowLog": []any{map[string]any{
					"FlowLogId": "flowlog-a", "RegionId": "cn-hangzhou",
					"TransitRouterAttachmentId": "tr-attach-a", "Status": "Active",
				}}},
				"TotalCount": 1,
			},
		},
		"AlibabaCloud.CEN.DescribeCenRouteMaps": {
			RequestID: "request-route-map",
			Data: map[string]any{
				"RouteMaps": map[string]any{"RouteMap": []any{map[string]any{
					"RouteMapId": "route-map-a", "CenRegionId": "cn-hangzhou",
					"TransitRouterRouteTableId": "vtb-a", "Status": "Active",
				}}},
				"TotalCount": 1,
			},
		},
	}
}

func containsCENTestReference(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
