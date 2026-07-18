package alicloud

import (
	"context"
	"reflect"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestPrivateLinkEndpointTopologyEnrichmentDiscoversManagedENIs(t *testing.T) {
	t.Parallel()

	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
	}}
	factory := &topologyRuntimeFactory{invoke: func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		if invocation.Operation != listEndpointZonesOperation {
			t.Fatalf("unexpected PrivateLink topology operation %q", invocation.Operation)
		}
		if invocation.Parameters["RegionId"] != "cn-hangzhou" ||
			invocation.Parameters["EndpointId"] != "ep-2vcr8878a2d95456ad5b" ||
			invocation.Parameters["MaxResults"] != privateLinkEndpointZonePageSize {
			t.Fatalf("PrivateLink zone parameters = %#v", invocation.Parameters)
		}
		return contracts.InvocationResult{
			RequestID: "019FD66C-692D-5A1A-857E-526902E8AB91",
			Data: map[string]any{
				"TotalCount": float64(1),
				"MaxResults": float64(20),
				"Zones": []any{map[string]any{
					"ZoneId": "cn-hangzhou-b", "EniId": "eni-2vcf7po5ka8sz3r6n3ep",
					"ServiceStatus": "Normal", "VSwitchId": "vsw-2vcnq9460iagcgynpskm9",
					"EniIp": "172.32.0.133", "ZoneStatus": "Connected",
					"RegionId": "cn-hangzhou", "ZoneDomain": "",
				}},
			},
		}, nil
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	items := []contracts.InventoryItem{{
		NativeType: endpointNativeType,
		NativeID:   "ep-2vcr8878a2d95456ad5b",
		Normalized: map[string]any{"vpcId": "vpc-a"},
		Raw:        map[string]any{"EndpointId": "ep-2vcr8878a2d95456ad5b"},
	}}
	before := cloneTopologyTestItems(t, items)

	enriched, err := runtime.EnrichInventoryBatch(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Source: "resource-center",
		Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
	}, items)
	if err != nil {
		t.Fatalf("enrich PrivateLink endpoint: %v", err)
	}
	if !reflect.DeepEqual(items, before) {
		t.Fatalf("PrivateLink enrichment mutated input:\n got: %#v\nwant: %#v", items, before)
	}
	if got := enriched[0].Normalized[NormalizedPrivateLinkENIIDsField]; !reflect.DeepEqual(
		got,
		[]string{"eni-2vcf7po5ka8sz3r6n3ep"},
	) {
		t.Fatalf("PrivateLink ENI IDs = %#v", got)
	}
	zones, ok := enriched[0].Normalized[NormalizedPrivateLinkEndpointZonesField].([]any)
	if !ok || len(zones) != 1 {
		t.Fatalf("PrivateLink zones = %#v", enriched[0].Normalized[NormalizedPrivateLinkEndpointZonesField])
	}
	zone, ok := zones[0].(map[string]any)
	if !ok || zone["zone_id"] != "cn-hangzhou-b" ||
		zone["eni_id"] != "eni-2vcf7po5ka8sz3r6n3ep" ||
		zone["vswitch_id"] != "vsw-2vcnq9460iagcgynpskm9" ||
		zone["zone_status"] != "Connected" ||
		zone["service_status"] != "Normal" ||
		zone["request_id"] != "019FD66C-692D-5A1A-857E-526902E8AB91" {
		t.Fatalf("normalized PrivateLink zone = %#v", zone)
	}
	if !reflect.DeepEqual(enriched[0].NetworkReferences, []string{"eni-2vcf7po5ka8sz3r6n3ep"}) {
		t.Fatalf("PrivateLink network references = %#v", enriched[0].NetworkReferences)
	}
}
