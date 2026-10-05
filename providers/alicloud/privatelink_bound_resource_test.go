package alicloud

import (
	"context"
	"sync"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type boundResourceProvider struct {
	mu       sync.Mutex
	services []string
	calls    []contracts.Invocation
	// resources maps ServiceId to the resource ids bound to it.
	resources map[string][]string
}

func (p *boundResourceProvider) Provider() asset.Provider { return asset.ProviderAliCloud }

func (p *boundResourceProvider) Invoke(_ context.Context, invocation contracts.Invocation) (contracts.InvocationResult, error) {
	p.mu.Lock()
	p.calls = append(p.calls, invocation)
	p.mu.Unlock()
	switch invocation.Operation {
	case privateLinkListServicesOperation:
		items := make([]any, 0, len(p.services))
		for _, id := range p.services {
			items = append(items, map[string]any{"ServiceId": id})
		}
		return contracts.InvocationResult{RequestID: "list-services", Data: map[string]any{"Services": items}}, nil
	case privateLinkListResourcesOperation:
		items := make([]any, 0)
		for _, id := range p.resources[invocation.Parameters["ServiceId"].(string)] {
			items = append(items, map[string]any{"ResourceId": id, "ZoneId": "zone-a"})
		}
		return contracts.InvocationResult{Data: map[string]any{"Resources": items}}, nil
	}
	panic("unexpected operation " + invocation.Operation)
}

func TestPrivateLinkBoundResourceLookupFiltersLoadBalancersServerSide(t *testing.T) {
	t.Parallel()
	// The fake honors the documented ResourceId filter by returning only the
	// matching service.
	provider := &boundResourceProvider{
		services:  []string{"epsrv-b"},
		resources: map[string][]string{"epsrv-b": {"other-lb", "lb-a"}},
	}
	action := &ResourceAction{provider: provider, region: "cn-hangzhou", nativeType: slbLoadBalancerNativeType}
	bindings, _, err := action.findPrivateLinkServiceResourceBindings(context.Background(), "lb-a", "slb")
	if err != nil || len(bindings) != 1 || bindings[0].serviceID != "epsrv-b" || bindings[0].resourceType != "slb" {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	if got := provider.calls[0].Parameters["ResourceId"]; got != "lb-a" {
		t.Fatalf("ListVpcEndpointServices ResourceId = %#v, want lb-a", got)
	}
	if len(provider.calls) != 2 {
		t.Fatalf("calls = %d, want 1 list + 1 resource read", len(provider.calls))
	}
}

func TestPrivateLinkBoundResourceLookupScansEveryServiceForNAT(t *testing.T) {
	t.Parallel()
	services := []string{"epsrv-e", "epsrv-d", "epsrv-c", "epsrv-b", "epsrv-a", "epsrv-f"}
	provider := &boundResourceProvider{services: services, resources: map[string][]string{
		"epsrv-c": {"ngw-a"}, "epsrv-f": {"ngw-a", "ngw-b"},
	}}
	action := &ResourceAction{provider: provider, region: "cn-hangzhou", nativeType: natGatewayNativeType}
	bindings, _, err := action.findPrivateLinkServiceResourceBindings(context.Background(), "ngw-a", "vpcNat")
	if err != nil || len(bindings) != 2 ||
		bindings[0].serviceID != "epsrv-c" || bindings[1].serviceID != "epsrv-f" {
		t.Fatalf("bindings=%+v err=%v", bindings, err)
	}
	if _, filtered := provider.calls[0].Parameters["ResourceId"]; filtered {
		t.Fatalf("NAT lookup used the undocumented ResourceId filter: %+v", provider.calls[0])
	}
	if len(provider.calls) != 1+len(services) {
		t.Fatalf("calls = %d, want every service read", len(provider.calls))
	}
}
