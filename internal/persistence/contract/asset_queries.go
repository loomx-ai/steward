package contract

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/resourcequery"
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

	t.Run("resource queries select the same assets in SQL as in memory", func(t *testing.T) {
		repositories := factory(t)
		inventory := repositories.Inventory()
		ctx := context.Background()
		now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
		for index, value := range []asset.Asset{
			{
				ID: "q-1", Name: "web-1", State: "Running", Location: "cn-hangzhou",
				Identity: asset.Identity{NativeType: "ACS::ECS::Instance", NativeID: "i-Alpha"}, ResourceKindID: "kind-ecs",
				Tags: map[string]string{"aws:cloudformation:stack-name": "Shop", "env": "prod", "a.b": "dotted", "team/owner": "ops"},
				Normalized: map[string]any{
					"cpu": 4, "memory": "8", "public": true, "flag": "true", "port": "80", "zero": 0, "ratio": 1.5,
					"rules": []any{map[string]any{"cidr": "0.0.0.0/0", "port": 22}}, "labels": map[string]any{"Tier": "Front"},
					"created": "2024-05-01", "nothing": nil,
				},
			},
			{
				ID: "q-2", State: "Stopped", Dirty: true,
				Identity: asset.Identity{NativeType: "ACS::ECS::Instance", NativeID: "i-beta"}, ResourceKindID: "kind-ecs",
				Tags:       map[string]string{},
				Normalized: map[string]any{"cpu": "16", "memory": 32, "public": "False", "flag": false, "labels": map[string]any{}},
			},
			{
				ID: "q-3", Name: "db", State: "Running", Location: "us-west-1",
				Identity: asset.Identity{NativeType: "ACS::RDS::DBInstance", NativeID: "rm-gamma"}, ResourceKindID: "kind-rds",
				Tags:       map[string]string{"env": "dev"},
				Normalized: map[string]any{"cpu": "abc", "memory": " 7", "public": "yes", "rules": []any{}, "nested": map[string]any{"deep": map[string]any{"value": 3}}},
			},
			{
				ID: "q-4", Name: "x", Identity: asset.Identity{NativeType: "ACS::OSS::Bucket", NativeID: "bucket"}, ResourceKindID: "kind-oss",
			},
		} {
			value.Identity.Provider, value.Identity.Partition, value.Identity.ConnectionID = asset.ProviderAliCloud, "public", "conn-query"
			value.FirstSeenAt, value.LastSeenAt = now.Add(time.Duration(index)*time.Second), now
			if err := inventory.PutAsset(ctx, value); err != nil {
				t.Fatal(err)
			}
			if value.Dirty {
				if _, err := inventory.SetAssetDirty(ctx, value.ID, true); err != nil {
					t.Fatal(err)
				}
			}
		}
		stored, err := inventory.ListAssets(ctx, persistence.ListOptions{ConnectionID: "conn-query", Limit: 50})
		if err != nil || len(stored.Items) != 4 {
			t.Fatalf("stored assets = %#v, err = %v", stored.Items, err)
		}
		pinned := map[string][]asset.AssetID{
			`region IS NULL`:                                    {"q-2", "q-4"},
			`NOT region = "cn-hangzhou"`:                        {"q-2", "q-3", "q-4"},
			`state != "Running"`:                                {"q-2"},
			`NOT tags.env = "prod"`:                             {"q-2", "q-3", "q-4"},
			`tags.env NOT IN ("prod")`:                          {"q-3"},
			`tags.aws:cloudformation:stack-name = "Shop"`:       {"q-1"},
			`tags.a.b = "dotted"`:                               {"q-1"},
			`tags.team/owner contains "OP"`:                     {"q-1"},
			`properties.cpu >= 8`:                               {"q-2"},
			`NOT properties.cpu > 1`:                            {"q-3", "q-4"},
			`properties.memory >= 7`:                            {"q-1", "q-2"},
			`properties.public = false`:                         {"q-2"},
			`properties.rules contains "0.0.0.0"`:               {"q-1"},
			`properties.labels contains "tier"`:                 {"q-1"},
			`properties.nested.deep.value = 3`:                  {"q-3"},
			`properties.nothing IS NULL`:                        {"q-1", "q-2", "q-3", "q-4"},
			`properties.labels NOT IN ("x")`:                    {"q-1", "q-2"},
			`dirty = true`:                                      {"q-2"},
			`properties.ratio = "1.5" AND properties.cpu = "4"`: {"q-1"},
		}
		queries := []string{
			`region IS NOT NULL`, `NOT name = "web-1"`, `name IS NULL`, `NOT state != "Running"`, `name > "a"`, `name < "x"`,
			`tags.env IN ("prod", "dev")`, `tags.missing IS NULL`, `NOT tags.missing = "x"`, `NOT tags.env contains "o"`,
			`properties.cpu < 8`, `properties.memory = 8`, `properties.memory = "8"`, `properties.memory > 7`,
			`properties.public = true`, `properties.public != true`, `properties.flag = true`, `NOT properties.flag != false`,
			`properties.port = 80`, `properties.port IN (80, 443)`, `properties.port NOT IN (443)`, `properties.zero = 0`,
			`properties.ratio > 1.25`, `properties.cpu > -1`, `properties.rules contains "cidr"`, `properties.rules contains "22"`,
			`properties.labels contains "front"`, `properties.nested contains "deep"`, `properties.public contains "TRU"`,
			`properties.nothing IS NOT NULL`, `NOT properties.nothing = "x"`, `properties.labels IS NOT NULL`, `properties.labels = "x"`,
			`properties.created > "2024-01-01"`, `properties.created < "2024-12-31"`, `NOT properties.created < "2024-12-31"`,
			`dirty != true`, `NOT dirty = false`, `dirty = "true"`, `dirty contains "ru"`,
			`type = "ACS::ECS::Instance" AND NOT properties.cpu >= 8`, `(region IS NULL OR tags.env = "dev") AND NOT dirty = true`,
			`resourceId contains "A"`, `id IN ("q-1", "q-3")`, `provider = "alicloud"`, `resourceKindId != "kind-ecs"`,
			`NOT (properties.cpu >= 8 OR properties.memory < 10)`,
		}
		for query := range pinned {
			queries = append(queries, query)
		}
		for _, query := range queries {
			expression, err := resourcequery.Parse(query)
			if err != nil {
				t.Fatalf("parse %q: %v", query, err)
			}
			var inMemory []asset.AssetID
			for _, value := range stored.Items {
				if expression.Match(value) {
					inMemory = append(inMemory, value.ID)
				}
			}
			page, err := inventory.ListAssets(ctx, persistence.ListOptions{ConnectionID: "conn-query", ResourceQuery: expression, Limit: 50})
			if err != nil {
				t.Fatalf("SQL %q: %v", query, err)
			}
			var inSQL []asset.AssetID
			for _, value := range page.Items {
				inSQL = append(inSQL, value.ID)
			}
			if !slices.Equal(inSQL, inMemory) {
				t.Errorf("%s: SQL selects %v, memory %v", query, inSQL, inMemory)
			}
			if want, ok := pinned[query]; ok && !slices.Equal(inMemory, want) {
				t.Errorf("%s: selects %v, want %v", query, inMemory, want)
			}
		}
	})

	t.Run("assets upsert together and a repeated asset keeps its last value", func(t *testing.T) {
		repositories := factory(t)
		inventory := repositories.Inventory()
		ctx := context.Background()
		now := time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC)
		value := func(id, name string) asset.Asset {
			return asset.Asset{
				ID: asset.AssetID(id), Name: name, ResourceKindID: "kind-ecs", FirstSeenAt: now, LastSeenAt: now,
				Identity: asset.Identity{Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "conn-batch", NativeType: "ACS::ECS::Instance", NativeID: id},
			}
		}
		if err := inventory.PutAssets(ctx, []asset.Asset{value("a", "first"), value("b", "only"), value("a", "last")}); err != nil {
			t.Fatal(err)
		}
		if err := inventory.PutAssets(ctx, []asset.Asset{value("b", "updated")}); err != nil {
			t.Fatal(err)
		}
		for id, want := range map[asset.AssetID]string{"a": "last", "b": "updated"} {
			if stored, err := inventory.GetAsset(ctx, id); err != nil || stored.Name != want {
				t.Fatalf("asset %s = %#v, err = %v", id, stored, err)
			}
		}
		page, err := inventory.ListAssets(ctx, persistence.ListOptions{ConnectionID: "conn-batch", Query: "updated", Limit: 10})
		if err != nil || len(page.Items) != 1 || page.Items[0].ID != "b" {
			t.Fatalf("search after a batch update = %#v, err = %v", page.Items, err)
		}
	})
}
