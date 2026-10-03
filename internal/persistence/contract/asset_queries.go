package contract

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
)

func runAssetQueries(t *testing.T, factory Factory) {
	t.Run("keyword asset search uses the maintained search document", func(t *testing.T) {
		repositories := factory(t)
		inventory := repositories.Inventory()
		ctx := context.Background()
		now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
		identity := func(nativeType, nativeID string) asset.Identity {
			return asset.Identity{Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "conn-search", NativeType: nativeType, NativeID: nativeID}
		}
		web := asset.Asset{
			ID: "ast-web", Identity: identity("ACS::ECS::Instance", "i-0abc123"), ResourceKindID: "kind-ecs",
			Name: "Web_Server*1", State: "Running", Location: "cn-hangzhou",
			Tags:        map[string]string{"aws:cloudformation:stack-name": "Checkout-Stack"},
			Normalized:  map[string]any{"network": map[string]any{"private_ips": []any{"10.0.1.5"}, "port": 8080.0}, "billing": "100%Prepaid"},
			FirstSeenAt: now, LastSeenAt: now,
		}
		bucket := asset.Asset{
			ID: "ast-bucket", Identity: identity("ACS::OSS::Bucket", "logs-web"), ResourceKindID: "kind-oss",
			Name: "web", FirstSeenAt: now.Add(time.Second), LastSeenAt: now,
		}
		twin := asset.Asset{
			ID: "ast-twin", Identity: identity("ACS::ECS::Instance", "i-twin"), ResourceKindID: "kind-ecs",
			Name: "web", FirstSeenAt: now.Add(3 * time.Second), LastSeenAt: now,
		}
		closedAt := now.Add(time.Minute)
		closed := asset.Asset{
			ID: "ast-closed", Identity: identity("ACS::ECS::Instance", "i-gone"), ResourceKindID: "kind-ecs",
			Name: "gone web", FirstSeenAt: now.Add(2 * time.Second), LastSeenAt: now, ClosedAt: &closedAt,
		}
		for _, value := range []asset.Asset{web, bucket, closed, twin} {
			if err := inventory.PutAsset(ctx, value); err != nil {
				t.Fatal(err)
			}
		}
		search := func(term string, options persistence.ListOptions) []asset.AssetID {
			t.Helper()
			options.ConnectionID, options.Query, options.Limit = "conn-search", term, 50
			page, err := inventory.ListAssets(ctx, options)
			if err != nil {
				t.Fatalf("search %q: %v", term, err)
			}
			ids := make([]asset.AssetID, len(page.Items))
			for index, value := range page.Items {
				ids[index] = value.ID
			}
			return ids
		}
		for term, want := range map[string][]asset.AssetID{
			"10.0.1.5":       {"ast-web"},
			"0.1":            {"ast-web"},
			"I-0ABC":         {"ast-web"},
			"checkout-stack": {"ast-web"},
			"stack-name":     {"ast-web"},
			"8080":           {"ast-web"},
			"100%prepaid":    {"ast-web"},
			"r*1":            {"ast-web"},
			"b_s":            {"ast-web"},
			"web":            {"ast-web", "ast-bucket", "ast-twin"},
			"we":             {"ast-web", "ast-bucket", "ast-twin"},
			"oss::bucket":    {"ast-bucket"},
			"ast-bucket":     {"ast-bucket"},
			"running":        {"ast-web"},
			"cn-hangzhou":    {"ast-web"},
			"[":              nil,
			"network":        nil,
			"10.0.1.6":       nil,
		} {
			if got := search(term, persistence.ListOptions{}); !slices.Equal(got, want) {
				t.Fatalf("search %q = %v, want %v", term, got, want)
			}
		}
		if got := search("web", persistence.ListOptions{IncludeClosed: true}); !slices.Equal(got, []asset.AssetID{"ast-web", "ast-bucket", "ast-closed", "ast-twin"}) {
			t.Fatalf("search including closed = %v", got)
		}
		// Instances rank before buckets, and within them a name equal to the
		// term before one that only starts with it.
		if got := search("web", persistence.ListOptions{SearchOrder: true}); !slices.Equal(got, []asset.AssetID{"ast-twin", "ast-web", "ast-bucket"}) {
			t.Fatalf("ranked search = %v", got)
		}

		web.Normalized = map[string]any{"network": map[string]any{"private_ips": []any{"10.0.9.9"}}}
		web.ClosedAt = &closedAt
		if err := inventory.PutAsset(ctx, web); err != nil {
			t.Fatal(err)
		}
		if got := search("10.0.1.5", persistence.ListOptions{IncludeClosed: true}); len(got) != 0 {
			t.Fatalf("replaced value still matches: %v", got)
		}
		if got := search("10.0.9.9", persistence.ListOptions{IncludeClosed: true}); !slices.Equal(got, []asset.AssetID{"ast-web"}) {
			t.Fatalf("rewritten value = %v", got)
		}
		if got := search("10.0.9.9", persistence.ListOptions{}); len(got) != 0 {
			t.Fatalf("closed asset matched an open-only search: %v", got)
		}
	})

	t.Run("asset canvases and capabilities read fields inside the payload", func(t *testing.T) {
		repositories := factory(t)
		inventory := repositories.Inventory()
		ctx := context.Background()
		now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
		if err := inventory.PutScope(ctx, asset.Scope{ID: "scope-hz", ConnectionID: "conn-canvas", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		for _, kind := range []asset.ResourceKind{
			{ID: "kind-vpc", Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::VPC", Class: "network.vpc"},
			{ID: "kind-ecs", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance", Class: "compute.instance"},
		} {
			if err := inventory.PutResourceKind(ctx, kind); err != nil {
				t.Fatal(err)
			}
		}
		put := func(id, nativeType, kindID string, normalized map[string]any, tags map[string]string, capabilities asset.CapabilitySet) {
			t.Helper()
			value := asset.Asset{
				ID: asset.AssetID(id), ScopeID: "scope-hz", ResourceKindID: asset.ResourceKindID(kindID),
				Identity:   asset.Identity{Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "conn-canvas", NativeType: nativeType, NativeID: id},
				Normalized: normalized, Tags: tags, Capabilities: capabilities, FirstSeenAt: now, LastSeenAt: now,
			}
			if err := inventory.PutAsset(ctx, value); err != nil {
				t.Fatal(err)
			}
		}
		put("VPC-1", "ACS::VPC::VPC", "kind-vpc", nil, nil, asset.CapabilitySet{asset.CapabilityIndexed})
		put("in-vpc", "ACS::ECS::Instance", "kind-ecs", map[string]any{"network": map[string]any{"vpc_id": "vpc-1"}}, nil, asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable})
		put("other-vpc", "ACS::ECS::Instance", "kind-ecs", map[string]any{"vpc_id": "vpc-2"}, nil, nil)
		put("public", "ACS::ECS::Instance", "kind-ecs", map[string]any{"vpc_id": ""}, map[string]string{"role": "actionable"}, nil)
		list := func(options persistence.ListOptions) []asset.AssetID {
			t.Helper()
			options.ConnectionID, options.Limit = "conn-canvas", 50
			page, err := inventory.ListAssets(ctx, options)
			if err != nil {
				t.Fatalf("list %+v: %v", options, err)
			}
			ids := make([]asset.AssetID, len(page.Items))
			for index, value := range page.Items {
				ids[index] = value.ID
			}
			slices.Sort(ids)
			return ids
		}
		for name, test := range map[string]struct {
			options persistence.ListOptions
			want    []asset.AssetID
		}{
			"vpc":           {persistence.ListOptions{AssetCanvas: persistence.AssetCanvasVPC, RegionID: "cn-hangzhou", VPCID: "VPC-1"}, []asset.AssetID{"VPC-1", "in-vpc"}},
			"region public": {persistence.ListOptions{AssetCanvas: persistence.AssetCanvasRegionPublic, RegionID: "cn-hangzhou"}, []asset.AssetID{"public"}},
			"capability":    {persistence.ListOptions{Capability: "Actionable"}, []asset.AssetID{"in-vpc"}},
		} {
			if got := list(test.options); !slices.Equal(got, test.want) {
				t.Fatalf("%s = %v, want %v", name, got, test.want)
			}
		}
		// jsonb cannot store U+0000, so assets are stored without it.
		put("nul", "ACS::ECS::Instance", "kind-ecs", map[string]any{"note": "a\x00b"}, nil, nil)
		if stored, err := inventory.GetAsset(ctx, "nul"); err != nil || stored.Normalized["note"] != "ab" {
			t.Fatalf("asset with U+0000 = %#v, err = %v", stored.Normalized, err)
		}
		page, err := inventory.ListAssets(ctx, persistence.ListOptions{ConnectionID: "conn-canvas", Query: "vpc", SearchOrder: true, Limit: 50})
		if err != nil || len(page.Items) != 3 || page.Items[0].ID != "VPC-1" {
			t.Fatalf("VPC kinds rank first = %#v, err = %v", page.Items, err)
		}
	})
}
