package alicloud

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/synctest"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// gatedCENFactory answers CEN reads from cenWideTopologyResponse. With gate
// set, each call parks until the test releases it, so the test chooses the
// order in which concurrent reads complete.
type gatedCENFactory struct {
	*topologyRuntimeFactory
	gate bool

	mu          sync.Mutex
	calls       int
	inFlight    int
	maxInFlight int
	parked      []chan struct{}
}

func (f *gatedCENFactory) Invoke(_ context.Context, _ contracts.Credential, _ string, _ catalog.Operation, invocation contracts.Invocation) (contracts.InvocationResult, error) {
	f.mu.Lock()
	f.calls++
	f.inFlight++
	f.maxInFlight = max(f.maxInFlight, f.inFlight)
	var wait chan struct{}
	if f.gate {
		wait = make(chan struct{})
		f.parked = append(f.parked, wait)
	}
	f.mu.Unlock()
	if wait != nil {
		<-wait
	}
	f.mu.Lock()
	f.inFlight--
	f.mu.Unlock()
	return cenWideTopologyResponse(invocation)
}

// release completes one parked call: the newest when lifo, else the oldest.
func (f *gatedCENFactory) release(lifo bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	index := 0
	if lifo {
		index = len(f.parked) - 1
	}
	close(f.parked[index])
	f.parked = slices.Delete(f.parked, index, index+1)
}

func cenWideTopologyResponse(invocation contracts.Invocation) (contracts.InvocationResult, error) {
	parameters := invocation.Parameters
	str := func(key string) string { return fmt.Sprint(parameters[key]) }
	operation := strings.TrimPrefix(invocation.Operation, "AlibabaCloud.CEN.")
	flat := func(path string, records ...map[string]any) (contracts.InvocationResult, error) {
		items := make([]any, len(records))
		for index := range records {
			items[index] = records[index]
		}
		return contracts.InvocationResult{
			RequestID: "request-" + operation,
			Data:      map[string]any{path: items, "NextToken": "", "TotalCount": len(records)},
		}, nil
	}
	nested := func(outer, inner string, records ...map[string]any) (contracts.InvocationResult, error) {
		items := make([]any, len(records))
		for index := range records {
			items[index] = records[index]
		}
		return contracts.InvocationResult{
			RequestID: "request-" + operation,
			Data:      map[string]any{outer: map[string]any{inner: items}, "TotalCount": len(records)},
		}, nil
	}
	routeTables := map[string]int{"tr-a": 3, "tr-b": 2}
	switch operation {
	case "DescribeCens":
		return nested("Cens", "Cen", map[string]any{"CenId": "cen-a"}, map[string]any{"CenId": "cen-b"})
	case "ListTransitRouters":
		router := "tr-" + strings.TrimPrefix(str("CenId"), "cen-")
		return flat("TransitRouters", map[string]any{
			"CenId": str("CenId"), "TransitRouterId": router, "RegionId": "cn-hangzhou", "Type": "Enterprise",
		})
	case "ListTransitRouterVpcAttachments", "ListTransitRouterVbrAttachments", "ListTransitRouterVpnAttachments",
		"ListTransitRouterEcrAttachments", "ListTransitRouterPeerAttachments":
		router := str("TransitRouterId")
		kind := strings.TrimSuffix(strings.TrimPrefix(operation, "ListTransitRouter"), "Attachments")
		records := make([]map[string]any, 0, 2)
		for index := 1; index <= 2; index++ {
			id := fmt.Sprintf("%s-%s-%d", strings.ToLower(kind), router, index)
			record := map[string]any{
				"TransitRouterAttachmentId": "attach-" + id, "TransitRouterId": router,
				"TransitRouterAttachmentStatus": "Attached", "RegionId": "cn-hangzhou",
			}
			switch kind {
			case "Vpc":
				record["VpcId"] = id
			case "Vbr":
				record["VbrId"] = id
			case "Vpn":
				record["VpnId"] = id
			case "Ecr":
				record["EcrId"] = id
			case "Peer":
				record["PeerTransitRouterRegionId"] = "cn-shanghai"
			}
			records = append(records, record)
		}
		return flat("TransitRouterAttachments", records...)
	case "ListTransitRouterCidr":
		return flat("CidrLists", map[string]any{"Cidr": "100.64.0.0/24", "TransitRouterCidrId": "cidr-" + str("TransitRouterId")})
	case "ListTransitRouterRouteTables":
		router := str("TransitRouterId")
		records := []map[string]any{}
		for index := 1; index <= routeTables[router]; index++ {
			records = append(records, map[string]any{
				"TransitRouterRouteTableId": fmt.Sprintf("vtb-%s-%d", router, index), "TransitRouterRouteTableStatus": "Active",
			})
		}
		return flat("TransitRouterRouteTables", records...)
	case "ListTransitRouterRouteTableAssociations":
		return flat("TransitRouterAssociations", map[string]any{"TransitRouterAttachmentId": str("TransitRouterRouteTableId") + "-association"})
	case "ListTransitRouterRouteTablePropagations":
		return flat("TransitRouterPropagations", map[string]any{"TransitRouterAttachmentId": str("TransitRouterRouteTableId") + "-propagation"})
	case "ListTransitRouterRouteEntries":
		return flat("TransitRouterRouteEntries", map[string]any{"TransitRouterRouteEntryNextHopId": str("TransitRouterRouteTableId") + "-entry"})
	case "ListTransitRouterPrefixListAssociation":
		return flat("PrefixLists", map[string]any{"PrefixListId": str("TransitRouterTableId") + "-prefix"})
	case "DescribeTransitRouteTableAggregation":
		return flat("Data", map[string]any{"TransitRouteTableAggregationCidr": str("TransitRouteTableId") + "-aggregation"})
	case "ListTrafficMarkingPolicies":
		return flat("TrafficMarkingPolicies")
	case "ListCenInterRegionTrafficQosPolicies":
		return flat("TrafficQosPolicies")
	case "DescribeCenAttachedChildInstances":
		router := "tr-" + strings.TrimPrefix(str("CenId"), "cen-")
		return nested("ChildInstances", "ChildInstance",
			map[string]any{"ChildInstanceId": "vpc-" + router + "-1", "ChildInstanceType": "VPC", "ChildInstanceRegionId": "cn-hangzhou"},
			map[string]any{"ChildInstanceId": "vpc-legacy-" + router, "ChildInstanceType": "VPC", "ChildInstanceRegionId": "cn-hangzhou"},
		)
	case "DescribeFlowlogs":
		return nested("FlowLogs", "FlowLog")
	case "DescribeCenRouteMaps":
		router := "tr-" + strings.TrimPrefix(str("CenId"), "cen-")
		return nested("RouteMaps", "RouteMap", map[string]any{
			"RouteMapId": "route-map-" + router, "CenRegionId": "cn-hangzhou", "TransitRouterRouteTableId": "vtb-" + router + "-2",
		})
	}
	return contracts.InvocationResult{}, fmt.Errorf("unexpected operation %s", invocation.Operation)
}

func collectWideCENTopology(t *testing.T, factory *gatedCENFactory, release func()) []contracts.InventoryItem {
	t.Helper()
	runtime, err := newRuntime(&credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
	}}, factory)
	if err != nil {
		t.Fatal(err)
	}
	collector := &cenTopologyCollector{
		runtime: runtime, ctx: context.Background(), region: "cn-hangzhou",
		request: contracts.InventoryRequest{
			ConnectionID: "connection-a",
			Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Location: "cn-hangzhou"},
		},
		credential:                &contracts.Credential{Type: asset.CredentialAliCloudAccessKey},
		detailedChildInstances:    map[string]struct{}{},
		transitRouterByRouteTable: map[string]string{},
		transitRoutersByCEN:       map[string][]string{},
	}
	done := make(chan error, 1)
	go func() { done <- collector.collect() }()
	for {
		if release == nil {
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			return collector.items
		}
		synctest.Wait()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			return collector.items
		default:
			release()
		}
	}
}

// The CEN topology reads attachment lists and route table lists concurrently,
// yet adds items in the serial order, whatever order the reads complete in.
func TestCENTopologyConcurrentReadsKeepTheSerialResult(t *testing.T) {
	t.Parallel()

	serialFactory := &gatedCENFactory{topologyRuntimeFactory: &topologyRuntimeFactory{}}
	serial := collectWideCENTopology(t, serialFactory, nil)

	var keys []string
	for _, item := range serial {
		keys = append(keys, strings.TrimPrefix(item.NativeType, "ACS::CEN::")+"|"+item.NativeID)
	}
	var want []string
	for _, router := range []string{"tr-a", "tr-b"} {
		for _, kind := range []string{"Vpc", "Vbr", "Vpn", "Ecr", "Peer"} {
			nativeType := map[string]string{
				"Vpc": CENTransitRouterVPCAttachmentNativeType, "Vbr": CENTransitRouterVBRAttachmentNativeType,
				"Vpn": CENTransitRouterVPNAttachmentNativeType, "Ecr": CENTransitRouterECRAttachmentNativeType,
				"Peer": CENTransitRouterPeerAttachmentNativeType,
			}[kind]
			for index := 1; index <= 2; index++ {
				want = append(want, fmt.Sprintf("%s|attach-%s-%s-%d", strings.TrimPrefix(nativeType, "ACS::CEN::"), strings.ToLower(kind), router, index))
			}
		}
		want = append(want, strings.TrimPrefix(CENTransitRouterCidrNativeType, "ACS::CEN::")+"|cidr-"+router)
		for index := 1; index <= map[string]int{"tr-a": 3, "tr-b": 2}[router]; index++ {
			want = append(want, fmt.Sprintf("%s|vtb-%s-%d", strings.TrimPrefix(CENTransitRouterRouteTableNativeType, "ACS::CEN::"), router, index))
		}
		cen := "cen-" + strings.TrimPrefix(router, "tr-")
		want = append(want,
			strings.TrimPrefix(CENChildInstanceAttachmentNativeType, "ACS::CEN::")+"|"+cen+"/vpc-legacy-"+router,
			strings.TrimPrefix(CENRouteMapNativeType, "ACS::CEN::")+"|route-map-"+router,
		)
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("serial order:\n got %v\nwant %v", keys, want)
	}
	// Per CEN: DescribeCens is shared; ListTransitRouters, 5 attachment lists,
	// CIDRs, route tables, 5 lists per route table, 2 traffic policy lists,
	// child instances, flow logs and route maps.
	if want := 1 + (1 + 5 + 1 + 1 + 5*3 + 2 + 3) + (1 + 5 + 1 + 1 + 5*2 + 2 + 3); serialFactory.calls != want {
		t.Fatalf("calls = %d, want %d", serialFactory.calls, want)
	}
	serialJSON, err := json.Marshal(serial)
	if err != nil {
		t.Fatal(err)
	}

	for _, lifo := range []bool{false, true} {
		synctest.Test(t, func(t *testing.T) {
			factory := &gatedCENFactory{topologyRuntimeFactory: &topologyRuntimeFactory{}, gate: true}
			items := collectWideCENTopology(t, factory, func() { factory.release(lifo) })
			got, err := json.Marshal(items)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(serialJSON) {
				t.Fatalf("lifo=%v: concurrent result differs from serial:\n got %s\nwant %s", lifo, got, serialJSON)
			}
			if factory.calls != serialFactory.calls || factory.maxInFlight != enrichmentConcurrency {
				t.Fatalf("lifo=%v: calls = %d (want %d), max in flight = %d (want %d)",
					lifo, factory.calls, serialFactory.calls, factory.maxInFlight, enrichmentConcurrency)
			}
		})
	}
}
