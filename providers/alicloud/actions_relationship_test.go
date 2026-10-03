package alicloud

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

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

func TestWaitBackoffDoublesWithinTheDeletionCheckWindow(t *testing.T) {
	t.Parallel()

	delays := func(timeoutSeconds int) []time.Duration {
		action := &ResourceAction{action: spec.ActionSpec{DeletionCheckTimeoutSeconds: timeoutSeconds}}
		result := contracts.ActionResult{Data: map[string]any{"phase": "delete"}}
		var got []time.Duration
		for range 6 {
			delay, data := action.waitBackoff(result)
			got = append(got, delay)
			// The delay is persisted as JSON between polls.
			encoded, _ := json.Marshal(data)
			result.Data = nil
			if err := json.Unmarshal(encoded, &result.Data); err != nil {
				t.Fatal(err)
			}
			if result.Data["phase"] != "delete" {
				t.Fatalf("provider result lost its data: %+v", result.Data)
			}
		}
		return got
	}
	s := time.Second
	if got := delays(600); fmt.Sprint(got) != fmt.Sprint([]time.Duration{2 * s, 4 * s, 8 * s, 16 * s, 30 * s, 30 * s}) {
		t.Fatalf("600s window delays = %v", got)
	}
	if got := delays(30); fmt.Sprint(got) != fmt.Sprint([]time.Duration{2 * s, 3750 * time.Millisecond, 3750 * time.Millisecond, 3750 * time.Millisecond, 3750 * time.Millisecond, 3750 * time.Millisecond}) {
		t.Fatalf("30s window delays = %v", got)
	}
	// Without a configured window the worker default applies, so polls stay fixed.
	if got := delays(0); fmt.Sprint(got) != fmt.Sprint([]time.Duration{2 * s, 2 * s, 2 * s, 2 * s, 2 * s, 2 * s}) {
		t.Fatalf("default window delays = %v", got)
	}
}
