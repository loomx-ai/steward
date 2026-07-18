package runtime_test

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type fakeProvider struct {
	provider asset.Provider
}

type fakeRegionProvider struct {
	fakeProvider
}

type fakeNetworkProvider struct {
	fakeProvider
}

type fakeResourceKindProvider struct {
	fakeProvider
}

type fakeSiteProvider struct {
	fakeProvider
	sites []contracts.ProviderSite
}

func (p fakeSiteProvider) ConnectionSites() []contracts.ProviderSite {
	return append([]contracts.ProviderSite(nil), p.sites...)
}

func (fakeResourceKindProvider) ResourceKinds() ([]asset.ResourceKind, string) {
	return []asset.ResourceKind{{
		ID:           "alicloud:ACS::ECS::Instance",
		Provider:     asset.ProviderAliCloud,
		NativeType:   "ACS::ECS::Instance",
		ScopeKinds:   []asset.ScopeKind{asset.ScopeRegion},
		DisplayNames: map[string]string{"zh-CN": "云服务器"},
		FieldDisplayNames: map[string]map[string]string{
			"vpc_id": {"zh-CN": "所属专有网络"},
		},
		Icon: "/icons/alicloud/acs-ecs-instance.svg",
	}}, "resource-catalog-a"
}

func (fakeRegionProvider) DiscoverRegions(context.Context, asset.ConnectionID) ([]contracts.DiscoveredRegion, error) {
	return []contracts.DiscoveredRegion{{RegionID: "us-east-1", Name: "US East (N. Virginia)"}}, nil
}

func (fakeNetworkProvider) SearchNetworkTargets(context.Context, contracts.NetworkTargetQuery) (contracts.NetworkTargetPage, error) {
	return contracts.NetworkTargetPage{Items: []contracts.NetworkTargetOption{{Kind: asset.ScanTargetVPC, RegionID: "us-east-1", NativeID: "vpc-123", Name: "main"}}}, nil
}

func TestRegistryCopiesCompiledBundlesAtItsBoundary(t *testing.T) {
	t.Parallel()

	registry := providerruntime.NewRegistry()
	bundle := spec.Bundle{
		Provider: asset.ProviderAWS,
		Hash:     "hash",
		Revision: "revision",
		Specs: []spec.CompiledSpec{{
			ResourceKind: asset.ResourceKind{NativeType: "AWS::EC2::Instance"},
		}},
	}
	if err := registry.RegisterBundle(bundle); err != nil {
		t.Fatalf("register compiled bundle: %v", err)
	}
	bundle.Specs[0].ResourceKind.NativeType = "mutated-input"
	first, err := registry.Bundle(asset.ProviderAWS)
	if err != nil {
		t.Fatalf("resolve compiled bundle: %v", err)
	}
	first.Specs[0].ResourceKind.NativeType = "mutated-output"
	second, err := registry.Bundle(asset.ProviderAWS)
	if err != nil {
		t.Fatalf("resolve compiled bundle again: %v", err)
	}
	if second.Specs[0].ResourceKind.NativeType != "AWS::EC2::Instance" {
		t.Fatalf("registered bundle was mutated: %+v", second)
	}
}

func (p fakeProvider) Provider() asset.Provider { return p.provider }

func (p fakeProvider) Invoke(context.Context, contracts.Invocation) (contracts.InvocationResult, error) {
	return contracts.InvocationResult{}, nil
}

func (p fakeProvider) InventorySources() []contracts.InventorySource {
	return []contracts.InventorySource{{Name: "broad-index", RootScopeKinds: []asset.ScopeKind{asset.ScopeAccount}}}
}

func TestRegistryRejectsDuplicateProvidersAndUnknownLookup(t *testing.T) {
	t.Parallel()

	registry := providerruntime.NewRegistry()
	provider := fakeProvider{provider: asset.ProviderAWS}
	if err := registry.Register(provider); err != nil {
		t.Fatalf("register provider: %v", err)
	}
	descriptors := registry.ProviderDescriptors()
	if len(descriptors) != 1 || descriptors[0].Provider != asset.ProviderAWS || len(descriptors[0].InventorySources) != 1 || descriptors[0].InventorySources[0].Name != "broad-index" {
		t.Fatalf("provider descriptors = %+v", descriptors)
	}
	if err := registry.Register(provider); err == nil {
		t.Fatal("duplicate provider must be rejected")
	}
	if _, err := registry.Resolve(asset.Provider("unsupported")); err == nil {
		t.Fatal("unknown provider lookup must fail")
	}
	resolved, err := registry.Resolve(asset.ProviderAWS)
	if err != nil || resolved.Provider() != asset.ProviderAWS {
		t.Fatalf("resolve provider: resolved=%v err=%v", resolved, err)
	}
}

func TestRegistryCopiesAndValidatesProviderSites(t *testing.T) {
	t.Parallel()

	wantSites := []contracts.ProviderSite{
		{Value: asset.ConnectionSiteCN, LabelKey: "sites.alicloudCN"},
		{Value: asset.ConnectionSiteINTL, LabelKey: "sites.alicloudINTL"},
	}
	registry := providerruntime.NewRegistry()
	if err := registry.Register(fakeSiteProvider{
		fakeProvider: fakeProvider{provider: asset.ProviderAliCloud},
		sites:        wantSites,
	}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(fakeProvider{provider: asset.ProviderAWS}); err != nil {
		t.Fatal(err)
	}

	descriptors := registry.ProviderDescriptors()
	if len(descriptors) != 2 {
		t.Fatalf("ProviderDescriptors() = %#v", descriptors)
	}
	if descriptors[0].Provider != asset.ProviderAliCloud ||
		len(descriptors[0].Sites) != len(wantSites) ||
		descriptors[0].Sites[0] != wantSites[0] ||
		descriptors[0].Sites[1] != wantSites[1] {
		t.Fatalf("Alibaba Cloud descriptor = %#v", descriptors[0])
	}
	if descriptors[1].Provider != asset.ProviderAWS || len(descriptors[1].Sites) != 0 {
		t.Fatalf("AWS descriptor = %#v", descriptors[1])
	}

	for _, test := range []struct {
		provider asset.Provider
		site     asset.ConnectionSite
		wantErr  bool
	}{
		{asset.ProviderAliCloud, asset.ConnectionSiteCN, false},
		{asset.ProviderAliCloud, asset.ConnectionSiteINTL, false},
		{asset.ProviderAliCloud, "", true},
		{asset.ProviderAliCloud, "moon", true},
		{asset.ProviderAWS, "", false},
		{asset.ProviderAWS, asset.ConnectionSiteCN, true},
		{asset.Provider("unknown"), "", true},
	} {
		err := registry.ValidateConnectionSite(test.provider, test.site)
		if (err != nil) != test.wantErr {
			t.Fatalf("ValidateConnectionSite(%q, %q) error = %v, wantErr %t", test.provider, test.site, err, test.wantErr)
		}
	}
}

func TestRegistryResolvesRegionDiscovererCapability(t *testing.T) {
	t.Parallel()

	registry := providerruntime.NewRegistry()
	if err := registry.Register(fakeRegionProvider{fakeProvider{provider: asset.ProviderAWS}}); err != nil {
		t.Fatal(err)
	}
	discoverer, err := registry.ResolveRegionDiscoverer(asset.ProviderAWS)
	if err != nil {
		t.Fatal(err)
	}
	regions, err := discoverer.DiscoverRegions(context.Background(), "connection-a")
	if err != nil || len(regions) != 1 || regions[0].RegionID != "us-east-1" {
		t.Fatalf("regions = %#v, err = %v", regions, err)
	}
	if err := registry.Register(fakeProvider{provider: asset.ProviderAliCloud}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ResolveRegionDiscoverer(asset.ProviderAliCloud); err == nil {
		t.Fatal("provider without region discovery was accepted")
	}
}

func TestRegistryResolvesNetworkTargetDiscovererCapability(t *testing.T) {
	t.Parallel()

	registry := providerruntime.NewRegistry()
	if err := registry.Register(fakeNetworkProvider{fakeProvider{provider: asset.ProviderAWS}}); err != nil {
		t.Fatal(err)
	}
	discoverer, err := registry.ResolveNetworkTargetDiscoverer(asset.ProviderAWS)
	if err != nil {
		t.Fatal(err)
	}
	page, err := discoverer.SearchNetworkTargets(context.Background(), contracts.NetworkTargetQuery{
		ConnectionID: "connection-a", Kind: asset.ScanTargetVPC, RegionID: "us-east-1", Query: "main", Limit: 20,
	})
	if err != nil || len(page.Items) != 1 || page.Items[0].NativeID != "vpc-123" {
		t.Fatalf("page = %#v, err = %v", page, err)
	}
	if err := registry.Register(fakeProvider{provider: asset.ProviderAliCloud}); err != nil {
		t.Fatal(err)
	}
	if _, err := registry.ResolveNetworkTargetDiscoverer(asset.ProviderAliCloud); err == nil {
		t.Fatal("provider without network target discovery was accepted")
	}
}

func TestRegistryCopiesOptionalResourceKindMetadata(t *testing.T) {
	t.Parallel()

	registry := providerruntime.NewRegistry()
	if err := registry.Register(fakeResourceKindProvider{
		fakeProvider{provider: asset.ProviderAliCloud},
	}); err != nil {
		t.Fatal(err)
	}
	kinds, revision, ok := registry.ResourceKinds(asset.ProviderAliCloud)
	if !ok || revision != "resource-catalog-a" || len(kinds) != 1 ||
		kinds[0].Icon != "/icons/alicloud/acs-ecs-instance.svg" {
		t.Fatalf("resource kinds = %+v revision = %q ok = %v", kinds, revision, ok)
	}
	kinds[0].Icon = "mutated"
	kinds[0].ScopeKinds[0] = asset.ScopeGlobal
	kinds[0].DisplayNames["zh-CN"] = "mutated"
	kinds[0].FieldDisplayNames["vpc_id"]["zh-CN"] = "mutated"
	fresh, _, ok := registry.ResourceKinds(asset.ProviderAliCloud)
	if !ok || fresh[0].Icon == "mutated" ||
		fresh[0].ScopeKinds[0] != asset.ScopeRegion ||
		fresh[0].DisplayNames["zh-CN"] != "云服务器" ||
		fresh[0].FieldDisplayNames["vpc_id"]["zh-CN"] != "所属专有网络" {
		t.Fatalf("registered resource kinds were mutated: %+v", fresh)
	}

	if err := registry.Register(fakeProvider{provider: asset.ProviderAWS}); err != nil {
		t.Fatal(err)
	}
	if kinds, revision, ok := registry.ResourceKinds(asset.ProviderAWS); ok || kinds != nil || revision != "" {
		t.Fatalf("provider without metadata returned kinds=%+v revision=%q ok=%v", kinds, revision, ok)
	}
}
