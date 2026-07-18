package topology

import (
	"context"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/finding"
	"github.com/loomx-ai/steward/internal/core/graph"
	core "github.com/loomx-ai/steward/internal/core/topology"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

type bundles struct {
	values               []spec.Bundle
	descriptors          []contracts.ProviderDescriptor
	resourceKinds        []asset.ResourceKind
	resourceKindRevision string
}

func (b bundles) Bundles() []spec.Bundle { return b.values }

func (b bundles) ProviderDescriptors() []contracts.ProviderDescriptor {
	if b.descriptors != nil {
		return append([]contracts.ProviderDescriptor(nil), b.descriptors...)
	}
	result := make([]contracts.ProviderDescriptor, 0, len(b.values))
	for _, bundle := range b.values {
		result = append(result, contracts.ProviderDescriptor{
			Provider: bundle.Provider,
			InventorySources: []contracts.InventorySource{{
				Name: "test-index", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
			}},
		})
	}
	return result
}

func (b bundles) ResourceKinds(provider asset.Provider) ([]asset.ResourceKind, string, bool) {
	if b.resourceKinds == nil {
		return nil, "", false
	}
	result := make([]asset.ResourceKind, 0, len(b.resourceKinds))
	for _, kind := range b.resourceKinds {
		if kind.Provider == provider {
			result = append(result, kind)
		}
	}
	return result, b.resourceKindRevision, true
}

func TestCatalogProjectionsCopyLocalizedFieldDisplayNames(t *testing.T) {
	t.Parallel()

	bundleKind := asset.ResourceKind{
		ID: "bundle-kind", Provider: asset.ProviderAliCloud,
		FieldDisplayNames: map[string]map[string]string{
			"vpc_id": {"zh-CN": "所属专有网络"},
		},
	}
	bundleKinds, _ := catalogProjection([]spec.Bundle{{
		Provider: asset.ProviderAliCloud,
		Specs:    []spec.CompiledSpec{{ResourceKind: bundleKind}},
	}}, asset.ProviderAliCloud)
	bundleKinds[bundleKind.ID].FieldDisplayNames["vpc_id"]["zh-CN"] = "mutated"
	if bundleKind.FieldDisplayNames["vpc_id"]["zh-CN"] != "所属专有网络" {
		t.Fatalf("bundle field display names were mutated: %+v", bundleKind.FieldDisplayNames)
	}

	metadataKind := asset.ResourceKind{
		ID: "metadata-kind", Provider: asset.ProviderAliCloud,
		FieldDisplayNames: map[string]map[string]string{
			"vpc_id": {"zh-CN": "所属专有网络"},
		},
	}
	metadataKinds, _, _, constrained := providerCatalogProjection(bundles{
		resourceKinds:        []asset.ResourceKind{metadataKind},
		resourceKindRevision: "metadata-revision",
	}, asset.ProviderAliCloud)
	if !constrained {
		t.Fatal("provider catalog projection was not constrained")
	}
	metadataKinds[metadataKind.ID].FieldDisplayNames["vpc_id"]["zh-CN"] = "mutated"
	if metadataKind.FieldDisplayNames["vpc_id"]["zh-CN"] != "所属专有网络" {
		t.Fatalf("provider field display names were mutated: %+v", metadataKind.FieldDisplayNames)
	}
}

func TestServiceUsesProviderInstanceCatalogForCountsAndIcons(t *testing.T) {
	t.Parallel()

	_, repositories := topologyServiceFixtureWithRepositories(t, 1, false)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 8, 0, 0, 0, time.UTC)
	child := serviceAsset(
		"listener-historical",
		"listener",
		"connection-a",
		"region-scope",
		"cn-hangzhou",
		now,
		map[string]any{core.NormalizedVPCID: "vpc-a"},
	)
	child.Identity.NativeType = "ACS::ALB::Listener"
	if err := repositories.Inventory().PutAsset(ctx, child); err != nil {
		t.Fatal(err)
	}
	catalog := topologyBundles()
	catalog.resourceKinds = []asset.ResourceKind{
		{
			ID: "vpc", Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::VPC",
			Icon: "/icons/alicloud/acs-vpc-vpc.svg",
		},
		{
			ID: "ecs", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance",
			DisplayName:       "Instance",
			DisplayNames:      map[string]string{"zh-CN": "云服务器", "en-US": "Instance"},
			FieldDisplayNames: map[string]map[string]string{"vpc_id": {"zh-CN": "所属专有网络", "en-US": "VPC"}},
			Icon:              "/icons/alicloud/acs-ecs-instance.svg",
		},
		{
			ID: "misc", Provider: asset.ProviderAliCloud, NativeType: "ACS::OOS::Application",
			Icon: "/icons/alicloud/acs-oos-application.svg",
		},
	}
	catalog.resourceKindRevision = "resource-catalog-a"
	service := NewService(repositories, catalog, WithClock(func() time.Time { return now }))

	account, err := service.Query(ctx, Query{ConnectionID: "connection-a"})
	if err != nil {
		t.Fatal(err)
	}
	accountView := account.View.(core.AccountView)
	var hangzhouCount int
	for _, region := range accountView.Regions {
		if region.Key == core.RegionFocusKey("cn-hangzhou") {
			hangzhouCount = region.ResourceCount
		}
	}
	if hangzhouCount != 2 {
		t.Fatalf("Hangzhou instance count = %d, want 2", hangzhouCount)
	}
	if account.Revision.SpecBundle == "bundle-a" {
		t.Fatalf("provider catalog revision missing from topology revision: %+v", account.Revision)
	}

	vpc, err := service.Query(ctx, Query{
		ConnectionID: "connection-a",
		FocusKey:     core.VPCFocusKey("cn-hangzhou", "vpc-a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	view := vpc.View.(core.VPCView)
	if len(view.Resources) != 1 ||
		view.Resources[0].ResourceKindID != "ecs" ||
		view.Resources[0].Icon != "/icons/alicloud/acs-ecs-instance.svg" {
		t.Fatalf("instance-only VPC resources = %+v", view.Resources)
	}
	if view.Resources[0].TypeName != "Instance" ||
		view.Resources[0].TypeNames["zh-CN"] != "云服务器" {
		t.Fatalf("provider presentation metadata missing from VPC resource = %+v", view.Resources[0])
	}
}

func TestServiceRemovesGraphEdgesWhoseEndpointIsHistoricalChild(t *testing.T) {
	t.Parallel()

	_, repositories := topologyServiceFixtureWithRepositories(t, 1, false)
	ctx := context.Background()
	now := time.Date(2026, 7, 27, 8, 30, 0, 0, time.UTC)
	child := serviceAsset(
		"listener-historical",
		"listener",
		"connection-a",
		"region-scope",
		"cn-hangzhou",
		now,
		map[string]any{core.NormalizedVPCID: "vpc-a"},
	)
	child.Identity.NativeType = "ACS::ALB::Listener"
	if err := repositories.Inventory().PutAsset(ctx, child); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Graph().ReplaceGraph(
		ctx,
		"region-scope",
		"graph-with-child",
		[]graph.Relationship{{
			ID: "relationship-child", SourceAssetID: "instance-0000",
			TargetAssetID: "listener-historical", Type: graph.RelationshipUses,
			Source: "test", Confidence: 1, ObservedAt: now,
		}},
		[]graph.LifecycleBinding{{
			ID: "binding-child", ControllerAssetID: "listener-historical",
			ManagedAssetID: "instance-0000", Authority: graph.AuthorityAuthoritative,
			Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate,
			EvidenceSource: "test", Confidence: 1, ObservedAt: now,
		}},
	); err != nil {
		t.Fatal(err)
	}
	catalog := topologyBundles()
	catalog.resourceKinds = []asset.ResourceKind{
		{
			ID: "vpc", Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::VPC",
			Icon: "/icons/alicloud/acs-vpc-vpc.svg",
		},
		{
			ID: "ecs", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance",
			Icon: "/icons/alicloud/acs-ecs-instance.svg",
		},
		{
			ID: "misc", Provider: asset.ProviderAliCloud, NativeType: "ACS::OOS::Application",
			Icon: "/icons/alicloud/acs-oos-application.svg",
		},
	}
	catalog.resourceKindRevision = "resource-catalog-a"
	service := NewService(repositories, catalog, WithClock(func() time.Time { return now }))

	response, err := service.Query(ctx, Query{
		ConnectionID: "connection-a",
		FocusKey:     core.VPCFocusKey("cn-hangzhou", "vpc-a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	view := response.View.(core.VPCView)
	if len(view.Resources) != 1 || view.Resources[0].AssetID != "instance-0000" {
		t.Fatalf("instance-only resources = %+v", view.Resources)
	}
	if len(view.Edges) != 0 || len(view.Resources[0].ExternalRelations) != 0 {
		t.Fatalf("child endpoint leaked through graph: edges=%+v resource=%+v", view.Edges, view.Resources[0])
	}
	if !view.Resources[0].Cleanup.Selectable {
		t.Fatalf("child lifecycle binding changed instance cleanup: %+v", view.Resources[0].Cleanup)
	}
}

func TestServiceLoadsAccountGlobalRegionAndVPCViews(t *testing.T) {
	t.Parallel()

	service := topologyServiceFixture(t, 3, false)
	account, err := service.Query(context.Background(), Query{ConnectionID: "connection-a"})
	if err != nil {
		t.Fatal(err)
	}
	accountView := account.View.(core.AccountView)
	if got := entryKeys(accountView.Regions); !reflect.DeepEqual(got, []string{
		core.RegionFocusKey("cn-beijing"), core.RegionFocusKey("cn-hangzhou"),
	}) {
		t.Fatalf("account regions = %v", got)
	}
	if accountView.Regions[0].ResourceCount != 0 {
		t.Fatalf("zero-resource Region = %+v", accountView.Regions[0])
	}

	global, err := service.Query(context.Background(), Query{ConnectionID: "connection-a", FocusKey: core.AccountGlobalFocusKey()})
	if err != nil {
		t.Fatal(err)
	}
	globalView := global.View.(core.ResourceGraphView)
	if len(globalView.Resources) != 1 || globalView.Resources[0].Key != "global" {
		t.Fatalf("global view = %+v", globalView)
	}

	regionKey := core.RegionFocusKey("cn-hangzhou")
	region, err := service.Query(context.Background(), Query{ConnectionID: "connection-a", FocusKey: regionKey})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := region.View.(core.RegionView); !ok {
		t.Fatalf("region view = %T", region.View)
	}

	vpcKey := core.VPCFocusKey("cn-hangzhou", "vpc-a")
	vpc, err := service.Query(context.Background(), Query{ConnectionID: "connection-a", FocusKey: vpcKey, Limit: 50})
	if err != nil {
		t.Fatal(err)
	}
	vpcView := vpc.View.(core.VPCView)
	if len(vpcView.Resources) != 3 || vpcView.Resources[0].Key != "instance-0000" {
		t.Fatalf("VPC view = %+v", vpcView)
	}
	if vpc.Coverage.Status != "complete" {
		t.Fatalf("coverage = %+v", vpc.Coverage)
	}
}

func TestServiceResourceQueryOmitsUnmatchedAccountRegions(t *testing.T) {
	t.Parallel()

	service := topologyServiceFixture(t, 3, false)
	account, err := service.Query(context.Background(), Query{
		ConnectionID:  "connection-a",
		ResourceQuery: `region = "cn-hangzhou"`,
	})
	if err != nil {
		t.Fatal(err)
	}
	view := account.View.(core.AccountView)
	if got := entryKeys(view.Regions); !reflect.DeepEqual(got, []string{
		core.RegionFocusKey("cn-hangzhou"),
	}) {
		t.Fatalf("resource-query account regions = %v", got)
	}
	if view.Regions[0].ResourceCount != 4 {
		t.Fatalf("resource-query Hangzhou summary = %+v", view.Regions[0])
	}
	if view.Regions[0].Cleanup.Selectable {
		t.Fatalf("resource-query account summary exposes broad cleanup: %+v", view.Regions[0])
	}
}

func TestServiceLoadsNestedRegionalDescendantsForAccountGlobalView(t *testing.T) {
	t.Parallel()

	service, repositories := topologyServiceFixtureWithRepositories(t, 1, false)
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 12, 1, 0, 0, time.UTC)
	for _, id := range []asset.AssetID{
		"transit-router",
		"route-table",
		"route-map",
		"unrelated-region-resource",
	} {
		if err := repositories.Inventory().PutAsset(
			ctx,
			serviceAsset(
				id,
				"misc",
				"connection-a",
				"region-scope",
				"cn-hangzhou",
				now,
				nil,
			),
		); err != nil {
			t.Fatal(err)
		}
	}
	relationships := []graph.Relationship{
		{
			ID: "router-cen", SourceAssetID: "transit-router",
			TargetAssetID: "global", Type: graph.RelationshipMemberOf,
			Source: "test", Confidence: 1, ObservedAt: now,
		},
		{
			ID: "route-table-router", SourceAssetID: "route-table",
			TargetAssetID: "transit-router", Type: graph.RelationshipMemberOf,
			Source: "test", Confidence: 1, ObservedAt: now,
		},
		{
			ID: "route-map-route-table", SourceAssetID: "route-map",
			TargetAssetID: "route-table", Type: graph.RelationshipMemberOf,
			Source: "test", Confidence: 1, ObservedAt: now,
		},
		{
			ID: "unrelated-cen", SourceAssetID: "unrelated-region-resource",
			TargetAssetID: "global", Type: graph.RelationshipUses,
			Source: "test", Confidence: 1, ObservedAt: now,
		},
	}
	if err := repositories.Graph().ReplaceGraph(
		ctx,
		"account",
		"graph-nested-global",
		relationships,
		nil,
	); err != nil {
		t.Fatal(err)
	}

	response, err := service.Query(ctx, Query{
		ConnectionID: "connection-a",
		FocusKey:     core.AccountGlobalFocusKey(),
	})
	if err != nil {
		t.Fatal(err)
	}
	view := response.View.(core.ResourceGraphView)
	got := make([]string, 0, len(view.Resources))
	for _, resource := range view.Resources {
		got = append(got, resource.Key)
	}
	if !reflect.DeepEqual(
		got,
		[]string{"global", "route-map", "route-table", "transit-router"},
	) {
		t.Fatalf("nested account-global resources = %v", got)
	}
	for _, relationshipID := range []string{
		"router-cen",
		"route-table-router",
		"route-map-route-table",
	} {
		found := false
		for _, edge := range view.Edges {
			if edge.Key == relationshipID &&
				edge.Relation == string(graph.RelationshipMemberOf) {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("nested account-global edge %q missing: %+v", relationshipID, view.Edges)
		}
	}
}

func TestServiceLoadsExternalRelationshipEndpointDetailsForFocusedView(t *testing.T) {
	t.Parallel()

	service, repositories := topologyServiceFixtureWithRepositories(t, 1, false)
	now := time.Date(2026, 7, 24, 12, 1, 0, 0, time.UTC)
	if err := repositories.Graph().ReplaceGraph(
		context.Background(),
		"account",
		"graph-external",
		[]graph.Relationship{{
			ID:            "rel-external",
			SourceAssetID: "instance-0000",
			TargetAssetID: "global",
			Type:          graph.RelationshipUses,
			Source:        "test",
			Confidence:    1,
			GraphRevision: "graph-external",
			ObservedAt:    now,
		}},
		nil,
	); err != nil {
		t.Fatal(err)
	}

	response, err := service.Query(context.Background(), Query{
		ConnectionID: "connection-a",
		FocusKey:     core.VPCFocusKey("cn-hangzhou", "vpc-a"),
		Limit:        50,
	})
	if err != nil {
		t.Fatal(err)
	}
	view := response.View.(core.VPCView)
	var instance core.Resource
	for _, resource := range view.Resources {
		if resource.Key == "instance-0000" {
			instance = resource
			break
		}
	}
	if len(instance.ExternalRelations) != 1 {
		t.Fatalf("external relations = %+v", instance.ExternalRelations)
	}
	relation := instance.ExternalRelations[0]
	if relation.TargetID != "global" || relation.TargetName != "global" || relation.TargetType != "Other" ||
		relation.TargetResourceKindID != "misc" || relation.TargetClass != "other" {
		t.Fatalf("external relation = %+v", relation)
	}
}

func TestServiceDefaultsLimitAndSignsCursorToRevisionAndFocus(t *testing.T) {
	t.Parallel()

	service := topologyServiceFixture(t, 201, false)
	focusKey := core.VPCFocusKey("cn-hangzhou", "vpc-a")
	first, err := service.Query(context.Background(), Query{ConnectionID: "connection-a", FocusKey: focusKey})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(first.View.(core.VPCView).Resources); got != 200 || first.NextCursor == "" {
		t.Fatalf("default page resources=%d cursor=%q", got, first.NextCursor)
	}
	second, err := service.Query(context.Background(), Query{
		ConnectionID: "connection-a", FocusKey: focusKey, Cursor: first.NextCursor,
	})
	if err != nil || len(second.View.(core.VPCView).Resources) != 1 {
		t.Fatalf("second page = %+v, err = %v", second, err)
	}
	_, err = service.Query(context.Background(), Query{
		ConnectionID: "connection-a", FocusKey: core.RegionPublicFocusKey("cn-hangzhou"), Cursor: first.NextCursor,
	})
	if !IsQueryError(err, "topology.cursor_stale") {
		t.Fatalf("cross-focus cursor error = %v", err)
	}
}

func TestServiceCursorBindsFiltersAndLimit(t *testing.T) {
	t.Parallel()

	service := topologyServiceFixture(t, 201, false)
	focusKey := core.VPCFocusKey("cn-hangzhou", "vpc-a")
	first, err := service.Query(context.Background(), Query{
		ConnectionID: "connection-a",
		FocusKey:     focusKey,
	})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page = %+v, err = %v", first, err)
	}

	tests := []struct {
		name  string
		query Query
	}{
		{
			name: "resource class",
			query: Query{
				ConnectionID: "connection-a", FocusKey: focusKey,
				ResourceClass: "compute.instance",
			},
		},
		{
			name: "resource kinds",
			query: Query{
				ConnectionID: "connection-a", FocusKey: focusKey,
				ResourceKindIDs: []asset.ResourceKindID{"ecs"},
			},
		},
		{
			name: "risk",
			query: Query{
				ConnectionID: "connection-a", FocusKey: focusKey,
				Risk: "actionable",
			},
		},
		{
			name: "limit",
			query: Query{
				ConnectionID: "connection-a", FocusKey: focusKey,
				Limit: 50,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			test.query.Cursor = first.NextCursor
			_, err := service.Query(context.Background(), test.query)
			if !IsQueryError(err, "topology.cursor_stale") {
				t.Fatalf("changed query cursor error = %v", err)
			}
		})
	}
}

func TestServiceNormalizesResourceKindsForCursorAndFiltersAccountCounts(t *testing.T) {
	t.Parallel()

	service := topologyServiceFixture(t, 201, false)
	account, err := service.Query(context.Background(), Query{
		ConnectionID:    "connection-a",
		ResourceKindIDs: []asset.ResourceKindID{" ecs ", "ecs"},
	})
	if err != nil {
		t.Fatal(err)
	}
	view := account.View.(core.AccountView)
	if view.GlobalResources != nil {
		t.Fatalf("filtered global resources = %+v", view.GlobalResources)
	}
	if len(view.Regions) != 2 || view.Regions[1].ResourceCount != 201 {
		t.Fatalf("filtered account summary = %+v", view.Regions)
	}
	if view.Regions[1].Cleanup.Selectable {
		t.Fatalf("filtered account summary exposes broad cleanup: %+v", view.Regions[1])
	}

	focusKey := core.VPCFocusKey("cn-hangzhou", "vpc-a")
	first, err := service.Query(context.Background(), Query{
		ConnectionID:    "connection-a",
		FocusKey:        focusKey,
		ResourceKindIDs: []asset.ResourceKindID{"misc", "ecs", "ecs"},
	})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first filtered page = %+v, err = %v", first, err)
	}
	second, err := service.Query(context.Background(), Query{
		ConnectionID:    "connection-a",
		FocusKey:        focusKey,
		Cursor:          first.NextCursor,
		ResourceKindIDs: []asset.ResourceKindID{"ecs", "misc"},
	})
	if err != nil || len(second.View.(core.VPCView).Resources) != 1 {
		t.Fatalf("normalized cursor page = %+v, err = %v", second, err)
	}
}

func TestServiceRejectsResourceKindsOutsideConnectionCatalog(t *testing.T) {
	t.Parallel()

	service := topologyServiceFixture(t, 1, false)
	_, err := service.Query(context.Background(), Query{
		ConnectionID:    "connection-a",
		ResourceKindIDs: []asset.ResourceKindID{"unknown-kind"},
	})
	if !IsQueryError(err, "topology.resource_kind_invalid") {
		t.Fatalf("unsupported resource kind error = %v", err)
	}
}

func TestServiceReturnsStableValidationErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		query Query
		code  string
	}{
		{query: Query{ConnectionID: "connection-a", FocusKey: "vpc:not-base64!"}, code: "topology.focus_invalid"},
		{query: Query{ConnectionID: "connection-a", Limit: core.MaxResourceLimit + 1}, code: "topology.limit_invalid"},
		{query: Query{ConnectionID: "connection-a", Risk: "critical"}, code: "topology.risk_invalid"},
	}
	for _, test := range tests {
		service := NewService(nil, bundles{})
		if _, err := service.Query(context.Background(), test.query); !IsQueryError(err, test.code) {
			t.Fatalf("query %+v error = %v, want %s", test.query, err, test.code)
		}
	}
}

func TestServicePreservesIncompleteCoverage(t *testing.T) {
	t.Parallel()

	service := topologyServiceFixture(t, 1, true)
	response, err := service.Query(context.Background(), Query{
		ConnectionID: "connection-a", FocusKey: core.VPCFocusKey("cn-hangzhou", "vpc-a"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Coverage.Status != "incomplete" {
		t.Fatalf("coverage = %+v", response.Coverage)
	}
}

func TestServiceCoverageRequiresProviderDeclaredGlobalScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		provider            asset.Provider
		rootScopeKinds      []asset.ScopeKind
		descriptorPresent   bool
		sourcePresent       bool
		duplicateDescriptor bool
		includeGlobalTarget bool
		want                string
	}{
		{
			name: "AWS old Region-only scan", provider: asset.ProviderAWS,
			rootScopeKinds:    []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal},
			descriptorPresent: true, sourcePresent: true, want: "incomplete",
		},
		{
			name: "AWS Region and Global scan", provider: asset.ProviderAWS,
			rootScopeKinds:      []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal},
			descriptorPresent:   true,
			sourcePresent:       true,
			includeGlobalTarget: true, want: "complete",
		},
		{
			name: "AliCloud Region-only scan", provider: asset.ProviderAliCloud,
			rootScopeKinds:    []asset.ScopeKind{asset.ScopeRegion},
			descriptorPresent: true, sourcePresent: true, want: "complete",
		},
		{name: "provider descriptor missing", provider: "provider-missing", want: "incomplete"},
		{
			name: "provider inventory sources missing", provider: "provider-no-sources",
			descriptorPresent: true, want: "incomplete",
		},
		{
			name: "empty source scope list supports Global", provider: "provider-all-scopes",
			descriptorPresent: true, sourcePresent: true, want: "incomplete",
		},
		{
			name: "duplicate provider descriptors", provider: "provider-duplicate",
			rootScopeKinds:    []asset.ScopeKind{asset.ScopeRegion},
			descriptorPresent: true, sourcePresent: true, duplicateDescriptor: true, want: "incomplete",
		},
	}
	for index, test := range tests {
		index, test := index, test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			service := providerCoverageTopologyFixture(
				t, index, test.provider, test.rootScopeKinds,
				test.descriptorPresent, test.sourcePresent, test.duplicateDescriptor, test.includeGlobalTarget,
			)
			response, err := service.Query(context.Background(), Query{
				ConnectionID: asset.ConnectionID(fmt.Sprintf("topology-provider-%d", index)),
			})
			if err != nil {
				t.Fatal(err)
			}
			if response.Coverage.Status != test.want {
				t.Fatalf("provider topology coverage = %+v, want %s", response.Coverage, test.want)
			}
		})
	}
}

func TestServiceRejectsSelectedNetworkCoverageAsConnectionComplete(t *testing.T) {
	t.Parallel()

	service, repositories := topologyServiceFixtureWithRepositories(t, 1, false)
	run, err := repositories.Inventory().GetScanRun(context.Background(), "scan")
	if err != nil {
		t.Fatal(err)
	}
	run.ScopeMode = asset.ScanSelectedNetworks
	run.Targets = []asset.ScanTarget{{
		Key: core.VPCFocusKey("cn-hangzhou", "vpc-a"), Kind: asset.ScanTargetVPC,
		RegionID: "cn-hangzhou", NativeID: "vpc-a",
	}}
	if err := repositories.Inventory().PutScanRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	response, err := service.Query(context.Background(), Query{ConnectionID: "connection-a"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Coverage.Status != "incomplete" || response.Coverage.LastCompleteScanAt != nil {
		t.Fatalf("selected-network coverage = %+v", response.Coverage)
	}
}

func TestServiceRejectsResourceKindFilteredCoverageAsConnectionComplete(t *testing.T) {
	t.Parallel()

	service, repositories := topologyServiceFixtureWithRepositories(t, 1, false)
	run, err := repositories.Inventory().GetScanRun(context.Background(), "scan")
	if err != nil {
		t.Fatal(err)
	}
	run.ScopeMode = asset.ScanAllActiveRegions
	run.Targets = []asset.ScanTarget{
		{Key: "region:cn-beijing", Kind: asset.ScanTargetRegion, RegionID: "cn-beijing"},
		{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"},
	}
	run.ResourceKindIDs = []asset.ResourceKindID{"ecs"}
	if err := repositories.Inventory().PutScanRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}

	response, err := service.Query(context.Background(), Query{ConnectionID: "connection-a"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Coverage.Status != "incomplete" || response.Coverage.LastCompleteScanAt != nil {
		t.Fatalf("filtered coverage = %+v", response.Coverage)
	}
}

func TestServiceNewActiveRegionInvalidatesOldFullCoverage(t *testing.T) {
	t.Parallel()

	service, repositories := topologyServiceFixtureWithRepositories(t, 1, false)
	run, err := repositories.Inventory().GetScanRun(context.Background(), "scan")
	if err != nil {
		t.Fatal(err)
	}
	run.ScopeMode = asset.ScanAllActiveRegions
	run.Targets = []asset.ScanTarget{
		{Key: "region:cn-beijing", Kind: asset.ScanTargetRegion, RegionID: "cn-beijing"},
		{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"},
	}
	if err := repositories.Inventory().PutScanRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 24, 14, 30, 0, 0, time.UTC)
	if err := repositories.Regions().PutRegion(context.Background(), asset.ConnectionRegion{
		ID: "region-new", ConnectionID: "connection-a", RegionID: "cn-shenzhen",
		Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}

	response, err := service.Query(context.Background(), Query{ConnectionID: "connection-a"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Coverage.Status != "incomplete" || response.Coverage.LastCompleteScanAt != nil {
		t.Fatalf("coverage after Region addition = %+v", response.Coverage)
	}
}

func TestServiceCursorBecomesStaleWhenFindingCountsChange(t *testing.T) {
	t.Parallel()

	service, repositories := topologyServiceFixtureWithRepositories(t, 201, false)
	focusKey := core.VPCFocusKey("cn-hangzhou", "vpc-a")
	first, err := service.Query(context.Background(), Query{ConnectionID: "connection-a", FocusKey: focusKey})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page = %+v err=%v", first, err)
	}
	now := time.Date(2026, 7, 24, 12, 1, 0, 0, time.UTC)
	if err := repositories.Findings().PutFinding(context.Background(), finding.Finding{
		ID: "finding-new", AssetID: "instance-0000", RuleID: "new", Status: finding.StatusOpen,
		Severity: finding.SeverityHigh, FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	_, err = service.Query(context.Background(), Query{
		ConnectionID: "connection-a", FocusKey: focusKey, Cursor: first.NextCursor,
	})
	if !IsQueryError(err, "topology.cursor_stale") {
		t.Fatalf("finding mutation cursor error = %v", err)
	}
}

func TestServiceCursorBecomesStaleWhenCoverageChanges(t *testing.T) {
	t.Parallel()

	service, repositories := topologyServiceFixtureWithRepositories(t, 201, false)
	focusKey := core.VPCFocusKey("cn-hangzhou", "vpc-a")
	first, err := service.Query(context.Background(), Query{ConnectionID: "connection-a", FocusKey: focusKey})
	if err != nil || first.NextCursor == "" {
		t.Fatalf("first page = %+v err=%v", first, err)
	}
	shard, err := repositories.Inventory().GetScanShard(context.Background(), "shard")
	if err != nil {
		t.Fatal(err)
	}
	shard.Status = asset.ShardFailed
	shard.Coverage.Complete = false
	shard.Coverage.FailureReason = "permission denied"
	if err := repositories.Inventory().PutScanShard(context.Background(), shard); err != nil {
		t.Fatal(err)
	}
	_, err = service.Query(context.Background(), Query{
		ConnectionID: "connection-a", FocusKey: focusKey, Cursor: first.NextCursor,
	})
	if !IsQueryError(err, "topology.cursor_stale") {
		t.Fatalf("coverage mutation cursor error = %v", err)
	}
}

func TestServiceCountsFindingsOnlyForFocusedAssetsAndSkipsEmptyInventory(t *testing.T) {
	t.Parallel()

	_, repositories := topologyServiceFixtureWithRepositories(t, 1, false)
	spy := &findingRepositorySpy{FindingRepository: repositories.Findings()}
	wrapped := findingRepositoriesSpy{Repositories: repositories, findings: spy}
	service := NewService(wrapped, topologyBundles())
	query := Query{
		ConnectionID: "connection-a",
		FocusKey:     core.VPCFocusKey("cn-hangzhou", "vpc-a"),
	}
	if _, err := service.Query(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	if len(spy.assetIDBatches) != 1 ||
		!reflect.DeepEqual(spy.assetIDBatches[0], []asset.AssetID{"instance-0000", "vpc"}) {
		t.Fatalf("focused finding asset IDs = %+v", spy.assetIDBatches)
	}
	if len(spy.options) != 0 {
		t.Fatalf("focused query scanned all connection findings: %+v", spy.options)
	}

	active, err := repositories.Inventory().ListActiveAssetsByConnection(context.Background(), "connection-a", "")
	if err != nil {
		t.Fatal(err)
	}
	closedAt := time.Date(2026, 7, 24, 12, 2, 0, 0, time.UTC)
	for _, value := range active {
		value.ClosedAt = &closedAt
		if err := repositories.Inventory().PutAsset(context.Background(), value); err != nil {
			t.Fatal(err)
		}
	}
	spy.assetIDBatches = nil
	if _, err := service.Query(context.Background(), query); err != nil {
		t.Fatal(err)
	}
	if len(spy.assetIDBatches) != 0 {
		t.Fatalf("empty inventory counted findings: %+v", spy.assetIDBatches)
	}
}

func TestServiceSummaryQueriesAvoidConnectionWideInstanceGraphReads(t *testing.T) {
	t.Parallel()

	_, repositories := topologyServiceFixtureWithRepositories(t, 2_000, false)
	inventorySpy := &topologyInventoryRepositorySpy{
		InventoryRepository: repositories.Inventory(),
	}
	graphSpy := &topologyGraphRepositorySpy{
		GraphRepository: repositories.Graph(),
	}
	findingSpy := &findingRepositorySpy{
		FindingRepository: repositories.Findings(),
	}
	wrapped := topologyRepositoriesSpy{
		Repositories: repositories,
		inventory:    inventorySpy,
		graph:        graphSpy,
		findings:     findingSpy,
	}
	service := NewService(wrapped, topologyBundles())

	account, err := service.Query(context.Background(), Query{
		ConnectionID: "connection-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	accountView := account.View.(core.AccountView)
	if got := accountView.Regions[1].ResourceCount; got != 2_001 {
		t.Fatalf("Hangzhou summary count = %d", got)
	}
	if inventorySpy.connectionReads != 0 ||
		graphSpy.connectionRelationshipReads != 0 ||
		graphSpy.connectionLifecycleReads != 0 ||
		len(findingSpy.options) != 0 {
		t.Fatalf(
			"account summary loaded instance graph: assets=%d relationships=%d lifecycle=%d findings=%d",
			inventorySpy.connectionReads,
			graphSpy.connectionRelationshipReads,
			graphSpy.connectionLifecycleReads,
			len(findingSpy.options),
		)
	}

	region, err := service.Query(context.Background(), Query{
		ConnectionID: "connection-a",
		FocusKey:     core.RegionFocusKey("cn-hangzhou"),
	})
	if err != nil {
		t.Fatal(err)
	}
	regionView := region.View.(core.RegionView)
	if len(regionView.VPCs) != 1 || regionView.VPCs[0].ResourceCount != 2_000 {
		t.Fatalf("Region summary = %+v", regionView)
	}
	if inventorySpy.connectionReads != 0 ||
		graphSpy.connectionRelationshipReads != 0 ||
		graphSpy.connectionLifecycleReads != 0 ||
		len(findingSpy.options) != 0 {
		t.Fatalf(
			"Region summary loaded connection graph: assets=%d relationships=%d lifecycle=%d findings=%d",
			inventorySpy.connectionReads,
			graphSpy.connectionRelationshipReads,
			graphSpy.connectionLifecycleReads,
			len(findingSpy.options),
		)
	}
}

func TestServiceLastCompleteScanSkipsIncompleteShardCoverage(t *testing.T) {
	t.Parallel()

	service, repositories := topologyServiceFixtureWithRepositories(t, 1, false)
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	newer := now.Add(time.Minute)
	if err := repositories.Inventory().CreateScanRun(context.Background(), asset.ScanRun{
		ID: "scan-incomplete", ConnectionID: "connection-a", Status: asset.ScanSucceeded,
		ScopeMode: asset.ScanAllActiveRegions,
		Targets: []asset.ScanTarget{
			{Key: "region:cn-beijing", Kind: asset.ScanTargetRegion, RegionID: "cn-beijing"},
			{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"},
		},
		CreatedAt: newer, FinishedAt: &newer,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScanShard(context.Background(), asset.ScanShard{
		ID: "shard-incomplete", ScanRunID: "scan-incomplete", Provider: asset.ProviderAliCloud,
		ScopeID: "account", Status: asset.ShardSkipped,
		Coverage: asset.Coverage{Complete: false, FreshAt: newer}, CreatedAt: newer, FinishedAt: &newer,
	}); err != nil {
		t.Fatal(err)
	}
	response, err := service.Query(context.Background(), Query{ConnectionID: "connection-a"})
	if err != nil {
		t.Fatal(err)
	}
	if response.Coverage.Status != "complete" || response.Coverage.LastCompleteScanAt == nil ||
		!response.Coverage.LastCompleteScanAt.Equal(now) {
		t.Fatalf("coverage = %+v", response.Coverage)
	}
}

func topologyServiceFixture(t *testing.T, resourceCount int, incomplete bool) *Service {
	t.Helper()
	service, _ := topologyServiceFixtureWithRepositories(t, resourceCount, incomplete)
	return service
}

func topologyServiceFixtureWithRepositories(t *testing.T, resourceCount int, incomplete bool) (*Service, persistence.Repositories) {
	t.Helper()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "topology.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-a", Name: "production", Provider: asset.ProviderAliCloud,
		Partition: "public", Principal: "production", Status: asset.ConnectionActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	for _, region := range []asset.ConnectionRegion{
		{ID: "region-b", ConnectionID: connection.ID, RegionID: "cn-beijing", Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
		{ID: "region-h", ConnectionID: connection.ID, RegionID: "cn-hangzhou", Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
		{ID: "region-r", ConnectionID: connection.ID, RegionID: "cn-shanghai", Lifecycle: asset.RegionRetired, CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Regions().PutRegion(ctx, region); err != nil {
			t.Fatal(err)
		}
	}
	for _, scope := range []asset.Scope{
		{ID: "account", ConnectionID: connection.ID, Kind: asset.ScopeAccount, NativeID: "account", Name: "production", CreatedAt: now, UpdatedAt: now},
		{ID: "global-scope", ConnectionID: connection.ID, ParentID: "account", Kind: asset.ScopeGlobal, NativeID: "global", Name: "Global", CreatedAt: now, UpdatedAt: now},
		{ID: "region-scope", ConnectionID: connection.ID, ParentID: "account", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Name: "Hangzhou", Location: "cn-hangzhou", CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	put := func(value asset.Asset) {
		t.Helper()
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	put(serviceAsset("global", "misc", connection.ID, "global-scope", "", now, nil))
	put(serviceAsset("vpc", "vpc", connection.ID, "region-scope", "cn-hangzhou", now, map[string]any{core.NormalizedVPCID: "vpc-a"}))
	for index := 0; index < resourceCount; index++ {
		put(serviceAsset(asset.AssetID(fmt.Sprintf("instance-%04d", index)), "ecs", connection.ID, "region-scope", "cn-hangzhou", now, map[string]any{core.NormalizedVPCID: "vpc-a"}))
	}
	if err := repositories.Graph().ReplaceGraph(ctx, "account", "graph-a", nil, nil); err != nil {
		t.Fatal(err)
	}
	status := asset.ScanSucceeded
	shardStatus := asset.ShardSucceeded
	complete := true
	if incomplete {
		status, shardStatus, complete = asset.ScanPartial, asset.ShardFailed, false
	}
	if err := repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{
		ID: "scan", ConnectionID: connection.ID, Status: status,
		ScopeMode: asset.ScanAllActiveRegions,
		Targets: []asset.ScanTarget{
			{Key: "region:cn-beijing", Kind: asset.ScanTargetRegion, RegionID: "cn-beijing"},
			{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"},
		},
		CreatedAt: now, FinishedAt: &now,
	}); err != nil {
		t.Fatal(err)
	}
	for index, target := range []asset.ScanTarget{
		{Key: "region:cn-beijing", Kind: asset.ScanTargetRegion, RegionID: "cn-beijing"},
		{Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou"},
	} {
		shardID := asset.ScanShardID(fmt.Sprintf("shard-%d", index))
		if index == 0 {
			shardID = "shard"
		}
		if err := repositories.Inventory().PutScanShard(ctx, asset.ScanShard{
			ID: shardID, ScanRunID: "scan", Provider: asset.ProviderAliCloud,
			TargetKey: target.Key, RegionID: target.RegionID, ScopeID: "account",
			Status: shardStatus, Coverage: asset.Coverage{Complete: complete, FreshAt: now},
			CreatedAt: now, FinishedAt: &now,
		}); err != nil {
			t.Fatal(err)
		}
	}
	bundleCatalog := topologyBundles()
	return NewService(repositories, bundleCatalog, WithClock(func() time.Time { return now })), repositories
}

func topologyBundles() bundles {
	bundle := spec.Bundle{
		Provider: asset.ProviderAliCloud, Revision: "bundle-a",
		Specs: []spec.CompiledSpec{
			{ResourceKind: asset.ResourceKind{ID: "vpc", Provider: asset.ProviderAliCloud, Class: "network.vpc", DisplayName: "VPC"}},
			{ResourceKind: asset.ResourceKind{ID: "ecs", Provider: asset.ProviderAliCloud, Class: "compute.instance", DisplayName: "Instance"}},
			{ResourceKind: asset.ResourceKind{ID: "misc", Provider: asset.ProviderAliCloud, Class: "other", DisplayName: "Other"}},
		},
	}
	return bundles{values: []spec.Bundle{bundle}}
}

func providerCoverageTopologyFixture(
	t *testing.T,
	index int,
	provider asset.Provider,
	rootScopeKinds []asset.ScopeKind,
	descriptorPresent bool,
	sourcePresent bool,
	duplicateDescriptor bool,
	includeGlobalTarget bool,
) *Service {
	t.Helper()
	repositories, err := sqlite.Open(
		filepath.Join(t.TempDir(), "provider-topology.db"),
		filepath.Join("..", "..", "..", "migrations"),
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 20, index, 0, 0, time.UTC)
	connectionID := asset.ConnectionID(fmt.Sprintf("topology-provider-%d", index))
	connection := asset.CloudConnection{
		ID: connectionID, Name: fmt.Sprintf("provider %d", index), Provider: provider,
		Principal: fmt.Sprintf("provider %d", index), Status: asset.ConnectionActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Regions().PutRegion(ctx, asset.ConnectionRegion{
		ID: fmt.Sprintf("topology-provider-region-%d", index), ConnectionID: connectionID,
		RegionID: "region-a", Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	accountID := asset.ScopeID(fmt.Sprintf("topology-provider-account-%d", index))
	regionScopeID := asset.ScopeID(fmt.Sprintf("topology-provider-scope-%d", index))
	for _, scope := range []asset.Scope{
		{
			ID: accountID, ConnectionID: connectionID, Kind: asset.ScopeAccount,
			NativeID: "account", CreatedAt: now, UpdatedAt: now,
		},
		{
			ID: regionScopeID, ConnectionID: connectionID, ParentID: accountID,
			Kind: asset.ScopeRegion, NativeID: "region-a", CreatedAt: now, UpdatedAt: now,
		},
	} {
		if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	value := serviceAsset(
		asset.AssetID(fmt.Sprintf("topology-provider-asset-%d", index)), "kind-provider",
		connectionID, regionScopeID, "region-a", now, nil,
	)
	value.Identity.Provider = provider
	if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Graph().ReplaceGraph(ctx, accountID, fmt.Sprintf("topology-provider-graph-%d", index), nil, nil); err != nil {
		t.Fatal(err)
	}
	targets := []asset.ScanTarget{{
		Key: "region:region-a", Kind: asset.ScanTargetRegion, RegionID: "region-a",
	}}
	if includeGlobalTarget {
		targets = append(targets, asset.ScanTarget{
			Key: "global", Kind: asset.ScanTargetGlobal, RegionID: "global",
		})
	}
	finished := now.Add(time.Minute)
	runID := asset.ScanRunID(fmt.Sprintf("topology-provider-scan-%d", index))
	if err := repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{
		ID: runID, ConnectionID: connectionID, Status: asset.ScanSucceeded,
		ScopeMode: asset.ScanAllActiveRegions, Targets: targets,
		CreatedAt: now, FinishedAt: &finished,
	}); err != nil {
		t.Fatal(err)
	}
	for targetIndex, target := range targets {
		if err := repositories.Inventory().PutScanShard(ctx, asset.ScanShard{
			ID:        asset.ScanShardID(fmt.Sprintf("topology-provider-shard-%d-%d", index, targetIndex)),
			ScanRunID: runID, Provider: provider, Source: "provider-index",
			TargetKey: target.Key, RegionID: target.RegionID, ScopeID: accountID,
			Status: asset.ShardSucceeded, Coverage: asset.Coverage{Complete: true, FreshAt: finished},
			CreatedAt: now, FinishedAt: &finished,
		}); err != nil {
			t.Fatal(err)
		}
	}
	descriptors := []contracts.ProviderDescriptor{{
		Provider: "known-other-provider",
		InventorySources: []contracts.InventorySource{{
			Name: "known-other-provider", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
		}},
	}}
	if duplicateDescriptor {
		descriptors = append(descriptors, contracts.ProviderDescriptor{Provider: provider})
	}
	if descriptorPresent {
		var sources []contracts.InventorySource
		if sourcePresent {
			sources = []contracts.InventorySource{{
				Name: "provider-index", RootScopeKinds: rootScopeKinds,
			}}
		}
		descriptors = append(descriptors, contracts.ProviderDescriptor{
			Provider: provider, InventorySources: sources,
		})
	}
	catalog := bundles{
		values: []spec.Bundle{{
			Provider: provider, Revision: "provider-bundle", Hash: "provider-spec",
			Specs: []spec.CompiledSpec{{ResourceKind: asset.ResourceKind{
				ID: "kind-provider", Provider: provider, Class: "compute.instance",
			}}},
		}},
		descriptors: descriptors,
	}
	return NewService(repositories, catalog, WithClock(func() time.Time { return now }))
}

func serviceAsset(id asset.AssetID, kind asset.ResourceKindID, connectionID asset.ConnectionID, scopeID asset.ScopeID, region string, now time.Time, normalized map[string]any) asset.Asset {
	if normalized == nil {
		normalized = map[string]any{}
	}
	return asset.Asset{
		ID: id, Identity: asset.Identity{Provider: asset.ProviderAliCloud, ConnectionID: connectionID, NativeID: string(id)},
		ScopeID: scopeID, ResourceKindID: kind, CurrentObservationID: asset.ObservationID("obs-" + string(id)),
		Name: string(id), Location: region, Normalized: normalized,
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable},
		FirstSeenAt:  now, LastSeenAt: now,
	}
}

func entryKeys(values []core.EntrySummary) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.Key)
	}
	return result
}

type findingRepositorySpy struct {
	persistence.FindingRepository
	options        []persistence.ListOptions
	assetIDBatches [][]asset.AssetID
}

func (s *findingRepositorySpy) ListFindings(ctx context.Context, options persistence.ListOptions) (persistence.Page[finding.Finding], error) {
	s.options = append(s.options, options)
	return s.FindingRepository.ListFindings(ctx, options)
}

func (s *findingRepositorySpy) CountOpenFindingsByAssetIDs(ctx context.Context, assetIDs []asset.AssetID) (map[asset.AssetID]int, error) {
	s.assetIDBatches = append(s.assetIDBatches, append([]asset.AssetID(nil), assetIDs...))
	return s.FindingRepository.CountOpenFindingsByAssetIDs(ctx, assetIDs)
}

type findingRepositoriesSpy struct {
	persistence.Repositories
	findings persistence.FindingRepository
}

func (s findingRepositoriesSpy) Findings() persistence.FindingRepository {
	return s.findings
}

type topologyInventoryRepositorySpy struct {
	persistence.InventoryRepository
	connectionReads int
}

func (s *topologyInventoryRepositorySpy) ListActiveAssetsByConnection(
	ctx context.Context,
	connectionID asset.ConnectionID,
	kindID asset.ResourceKindID,
) ([]asset.Asset, error) {
	s.connectionReads++
	return s.InventoryRepository.ListActiveAssetsByConnection(
		ctx,
		connectionID,
		kindID,
	)
}

type topologyGraphRepositorySpy struct {
	persistence.GraphRepository
	connectionRelationshipReads int
	connectionLifecycleReads    int
}

func (s *topologyGraphRepositorySpy) ListRelationshipsByConnection(
	ctx context.Context,
	connectionID asset.ConnectionID,
) ([]graph.Relationship, error) {
	s.connectionRelationshipReads++
	return s.GraphRepository.ListRelationshipsByConnection(ctx, connectionID)
}

func (s *topologyGraphRepositorySpy) ListLifecycleBindingsByConnection(
	ctx context.Context,
	connectionID asset.ConnectionID,
) ([]graph.LifecycleBinding, error) {
	s.connectionLifecycleReads++
	return s.GraphRepository.ListLifecycleBindingsByConnection(
		ctx,
		connectionID,
	)
}

type topologyRepositoriesSpy struct {
	persistence.Repositories
	inventory persistence.InventoryRepository
	graph     persistence.GraphRepository
	findings  persistence.FindingRepository
}

func (s topologyRepositoriesSpy) Inventory() persistence.InventoryRepository {
	return s.inventory
}

func (s topologyRepositoriesSpy) Graph() persistence.GraphRepository {
	return s.graph
}

func (s topologyRepositoriesSpy) Findings() persistence.FindingRepository {
	return s.findings
}
