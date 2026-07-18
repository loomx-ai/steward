package alicloud

import "testing"

func TestPrivateLinkServiceResourceTypesCoverBothSidesOfSupportedBindings(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		natGatewayNativeType:       "vpcNat",
		slbLoadBalancerNativeType:  "slb",
		albLoadBalancerNativeType:  "alb",
		nlbLoadBalancerNativeType:  "nlb",
		gwlbLoadBalancerNativeType: "gwlb",
	}
	for nativeType, wantResourceType := range tests {
		nativeType, wantResourceType := nativeType, wantResourceType
		t.Run(nativeType, func(t *testing.T) {
			t.Parallel()
			gotResourceType, ok := privateLinkServiceResourceType(nativeType)
			if !ok || gotResourceType != wantResourceType {
				t.Fatalf(
					"PrivateLink service resource type for %q = %q, %v; want %q, true",
					nativeType,
					gotResourceType,
					ok,
					wantResourceType,
				)
			}
		})
	}
	if resourceType, ok := privateLinkServiceResourceType("ACS::ECS::Instance"); ok || resourceType != "" {
		t.Fatalf("unbound native type unexpectedly mapped to PrivateLink: %q, %v", resourceType, ok)
	}
}
