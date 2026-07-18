package alicloud

import (
	"reflect"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestEnrichNLBInventoryTopologyExtractsProductAPIAndResourceCenterEIPs(t *testing.T) {
	t.Parallel()

	items := []contracts.InventoryItem{
		{
			NativeType: nlbLoadBalancerNativeType,
			NativeID:   "nlb-product",
			Normalized: map[string]any{"existing": "product"},
			Raw: map[string]any{"ZoneMappings": []any{
				map[string]any{"LoadBalancerAddresses": []any{
					map[string]any{"AllocationId": "eip-b"},
					map[string]any{"AllocationId": "eip-a"},
				}},
			}},
		},
		{
			NativeType: nlbLoadBalancerNativeType,
			NativeID:   "nlb-resource-center",
			Raw:        map[string]any{},
			Normalized: map[string]any{
				"existing": "resource-center",
				"configuration": map[string]any{"ZoneMappings": []any{
					map[string]any{"AllocationId": []any{"eip-d"}},
					map[string]any{"AllocationId": []any{"eip-c"}},
				}},
			},
		},
		{
			NativeType: "ACS::EIP::EipAddress",
			NativeID:   "eip-a",
			Raw:        map[string]any{},
			Normalized: map[string]any{"existing": "eip"},
		},
	}
	before := cloneTopologyTestItems(t, items)

	enriched := enrichNLBInventoryTopology(items)

	if !reflect.DeepEqual(items, before) {
		t.Fatalf("input items mutated:\n got: %#v\nwant: %#v", items, before)
	}
	if got := enriched[0].Normalized[NormalizedNLBEIPIDsField]; !reflect.DeepEqual(
		got,
		[]any{"eip-a", "eip-b"},
	) {
		t.Fatalf("product API NLB EIPs = %#v", got)
	}
	if got := enriched[1].Normalized[NormalizedNLBEIPIDsField]; !reflect.DeepEqual(
		got,
		[]any{"eip-c", "eip-d"},
	) {
		t.Fatalf("Resource Center NLB EIPs = %#v", got)
	}
	if !reflect.DeepEqual(enriched[0].NetworkReferences, []string{"eip-a", "eip-b"}) ||
		!reflect.DeepEqual(enriched[1].NetworkReferences, []string{"eip-c", "eip-d"}) {
		t.Fatalf("NLB network references = %#v / %#v", enriched[0].NetworkReferences, enriched[1].NetworkReferences)
	}
	if !reflect.DeepEqual(enriched[2], items[2]) {
		t.Fatalf("non-NLB item changed: got %#v want %#v", enriched[2], items[2])
	}
}

func TestIsNLBManagedEIPRequiresServiceManagedAndExactControllerMarker(t *testing.T) {
	t.Parallel()

	value := nlbManagedEIPAsset(
		"eip-a",
		"CREATE_BY_NLB.nlb-a",
		float64(1),
	)
	if !IsNLBManagedEIP(value, "nlb-a") {
		t.Fatal("provider-managed EIP with exact NLB marker was not classified")
	}
	if IsNLBManagedEIP(value, "nlb-b") {
		t.Fatal("EIP was assigned to the wrong NLB controller")
	}
	value.Normalized["configuration"].(map[string]any)["ServiceManaged"] = float64(0)
	if IsNLBManagedEIP(value, "nlb-a") {
		t.Fatal("manually managed EIP was delegated to NLB cleanup")
	}
}

func nlbManagedEIPAsset(nativeID, name string, serviceManaged any) asset.Asset {
	return asset.Asset{
		Name: name,
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: eipAddressNativeType, NativeID: nativeID,
		},
		Normalized: map[string]any{"configuration": map[string]any{
			"Name": name, "ServiceManaged": serviceManaged,
		}},
	}
}
