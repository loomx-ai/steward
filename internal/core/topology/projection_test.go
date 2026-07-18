package topology_test

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/topology"
)

func TestProjectAccountIncludesActiveZeroResourceRegions(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	view, ok := response.View.(topology.AccountView)
	if !ok {
		t.Fatalf("view = %T", response.View)
	}
	if view.GlobalResources == nil || view.GlobalResources.ResourceCount != 1 {
		t.Fatalf("global resources = %+v", view.GlobalResources)
	}
	if cleanup := view.GlobalResources.Cleanup; !cleanup.Selectable || cleanup.SelectorKind != "scope" ||
		cleanup.SelectorKey != "global" || cleanup.Confirmation != "confirm" {
		t.Fatalf("global cleanup = %+v", cleanup)
	}
	if got := summaryPairs(view.Regions); !reflect.DeepEqual(got, []string{"Beijing=0", "Hangzhou=14"}) {
		t.Fatalf("regions = %v", got)
	}
	if got := []string{view.Regions[0].NativeID, view.Regions[1].NativeID}; !reflect.DeepEqual(got, []string{"cn-beijing", "cn-hangzhou"}) {
		t.Fatalf("Region native IDs = %v", got)
	}
	if view.Regions[0].Cleanup.Selectable {
		t.Fatalf("Region without an authoritative scope is selectable: %+v", view.Regions[0])
	}
	if cleanup := view.Regions[1].Cleanup; !cleanup.Selectable || cleanup.SelectorKind != "scope" ||
		cleanup.SelectorKey != "region" || cleanup.Confirmation != "type_name" {
		t.Fatalf("Region cleanup = %+v", cleanup)
	}

	input.Assets = slices.DeleteFunc(input.Assets, func(value asset.Asset) bool { return value.Location == "" })
	response, err = topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	if response.View.(topology.AccountView).GlobalResources != nil {
		t.Fatalf("zero global summary must be omitted: %+v", response.View)
	}
}

func TestProjectAccountResourceQueryOmitsZeroResourceRegions(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	input.ResourceQueryApplied = true
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	view := response.View.(topology.AccountView)
	if got := summaryPairs(view.Regions); !reflect.DeepEqual(got, []string{"Hangzhou=14"}) {
		t.Fatalf("resource-query regions = %v", got)
	}
	if view.Regions[0].Cleanup.Selectable {
		t.Fatalf("resource-query summary exposes broad cleanup: %+v", view.Regions[0])
	}
}

func TestProjectIncludesOnlyConsoleLinkValuesReferencedByTemplate(t *testing.T) {
	t.Parallel()

	input := topology.Input{
		Focus:      topology.Focus{Kind: topology.FocusAccountGlobal},
		Connection: asset.CloudConnection{ID: "connection-a"},
		Scopes: []asset.Scope{{
			ID: "global", ConnectionID: "connection-a",
			Kind: asset.ScopeGlobal, NativeID: "global",
		}},
		Kinds: map[asset.ResourceKindID]asset.ResourceKind{
			"transit-router": {
				ID: "transit-router", DisplayName: "Transit Router",
				ConsoleLinkTemplate: "https://cen.console.aliyun.com/cen/attachment/{regionId}/{parentId}/{nativeId}",
			},
			"cen": {ID: "cen", DisplayName: "CEN"},
		},
		Assets: []asset.Asset{
			{
				ID: "transit-router-a",
				Identity: asset.Identity{
					Provider:   asset.ProviderAliCloud,
					NativeType: "ACS::CEN::TransitRouter",
					NativeID:   "tr-a",
				},
				ScopeID:        "global",
				ResourceKindID: "transit-router",
				Location:       "cn-hangzhou",
				Dirty:          true,
				Normalized:     map[string]any{"secret": "must-not-be-projected"},
			},
			{
				ID: "cen-a",
				Identity: asset.Identity{
					Provider:   asset.ProviderAliCloud,
					NativeType: "ACS::CEN::CenInstance",
					NativeID:   "cen-a",
				},
				ScopeID:        "global",
				ResourceKindID: "cen",
			},
		},
		Relationships: []graph.Relationship{
			testRelationship(
				"member-of",
				"transit-router-a",
				"cen-a",
				graph.RelationshipMemberOf,
			),
		},
	}

	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	resources := response.View.(topology.ResourceGraphView).Resources
	router := findResource(t, resources, "transit-router-a")
	if !router.Dirty {
		t.Fatal("dirty resource annotation was not projected")
	}
	if !reflect.DeepEqual(
		router.ConsoleLinkValues,
		map[string]string{"parentId": "cen-a", "regionId": "cn-hangzhou"},
	) {
		t.Fatalf("console link values = %+v", router.ConsoleLinkValues)
	}
}

func TestProjectAccountGlobalIncludesRegionalContainmentDescendants(t *testing.T) {
	t.Parallel()

	input := topology.Input{
		Focus:      topology.Focus{Kind: topology.FocusAccountGlobal},
		Connection: asset.CloudConnection{ID: "connection-a"},
		Scopes: []asset.Scope{
			{
				ID: "global", ConnectionID: "connection-a",
				Kind: asset.ScopeGlobal, NativeID: "global",
			},
			{
				ID: "region", ConnectionID: "connection-a",
				Kind: asset.ScopeRegion, NativeID: "cn-hangzhou",
				Location: "cn-hangzhou",
			},
		},
		Kinds: map[asset.ResourceKindID]asset.ResourceKind{
			"cen":         {ID: "cen", DisplayName: "CEN"},
			"router":      {ID: "router", DisplayName: "Transit Router"},
			"route-table": {ID: "route-table", DisplayName: "Route Table"},
			"route-map":   {ID: "route-map", DisplayName: "Route Map"},
			"unrelated":   {ID: "unrelated", DisplayName: "Unrelated"},
		},
		Assets: []asset.Asset{
			testScopedAsset("cen", "cen", "global", ""),
			testScopedAsset("router", "router", "region", "cn-hangzhou"),
			testScopedAsset("route-table", "route-table", "region", "cn-hangzhou"),
			testScopedAsset("route-map", "route-map", "region", "cn-hangzhou"),
			testScopedAsset("unrelated", "unrelated", "region", "cn-hangzhou"),
		},
		Relationships: []graph.Relationship{
			testRelationship("router-cen", "router", "cen", graph.RelationshipMemberOf),
			testRelationship("route-table-router", "route-table", "router", graph.RelationshipMemberOf),
			testRelationship("route-map-route-table", "route-map", "route-table", graph.RelationshipMemberOf),
			testRelationship("unrelated-cen", "unrelated", "cen", graph.RelationshipUses),
		},
		Limit: 200,
	}

	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	view := response.View.(topology.ResourceGraphView)
	if got := resourceKeys(view.Resources); !reflect.DeepEqual(
		got,
		[]string{"cen", "route-map", "route-table", "router"},
	) {
		t.Fatalf("account-global containment resources = %v", got)
	}
	for _, relationshipID := range []string{
		"router-cen",
		"route-table-router",
		"route-map-route-table",
	} {
		if !slices.ContainsFunc(view.Edges, func(edge topology.ResourceEdge) bool {
			return edge.Key == relationshipID &&
				edge.Relation == string(graph.RelationshipMemberOf)
		}) {
			t.Fatalf("account-global containment edge %q missing: %+v", relationshipID, view.Edges)
		}
	}
	if slices.ContainsFunc(view.Resources, func(resource topology.Resource) bool {
		return resource.Key == "unrelated"
	}) {
		t.Fatalf("non-containment regional resource leaked into account-global view: %+v", view.Resources)
	}
}

func TestProjectAccountSortsRegionsByGeography(t *testing.T) {
	t.Parallel()

	regionIDs := []string{
		"af-south-1",
		"me-central-1",
		"us-east-1",
		"na-south-1",
		"ap-southeast-10",
		"cn-hongkong",
		"moon-1",
		"il-central-1",
		"sa-east-1",
		"ap-northeast-1",
		"cn-beijing",
		"eu-central-1",
		"ap-southeast-2",
	}
	input := topology.Input{
		Connection: asset.CloudConnection{ID: "connection-a", Name: "Production"},
		Limit:      200,
	}
	for index, regionID := range regionIDs {
		input.Regions = append(input.Regions, asset.ConnectionRegion{
			ID:             fmt.Sprintf("region-%02d", index),
			ConnectionID:   input.Connection.ID,
			RegionID:       regionID,
			DiscoveredName: regionID,
			Lifecycle:      asset.RegionActive,
		})
	}

	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	view := response.View.(topology.AccountView)
	got := make([]string, 0, len(view.Regions))
	for _, region := range view.Regions {
		got = append(got, region.NativeID)
	}
	want := []string{
		"cn-beijing",
		"cn-hongkong",
		"ap-northeast-1",
		"ap-southeast-2",
		"ap-southeast-10",
		"eu-central-1",
		"na-south-1",
		"sa-east-1",
		"us-east-1",
		"il-central-1",
		"me-central-1",
		"af-south-1",
		"moon-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("regions = %v, want %v", got, want)
	}
}

func TestProjectRegionAndVPCSummaryCountsMatchAppliedFilters(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	input.Focus = topology.Focus{Kind: topology.FocusRegion, RegionID: "cn-hangzhou"}
	input.ResourceClass = "compute.instance"
	input.Risk = "findings"
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	view := response.View.(topology.RegionView)
	if view.Region.NativeID != "cn-hangzhou" || view.PublicResources.ResourceCount != 0 {
		t.Fatalf("region view = %+v", view)
	}
	if view.PublicResources.Cleanup.Selectable || view.PublicResources.Cleanup.SelectorKey != "" {
		t.Fatalf("Region-public must not advertise a broad scope selector: %+v", view.PublicResources.Cleanup)
	}
	if got := summaryPairs(view.VPCs); !reflect.DeepEqual(got, []string{"Empty network=0", "Production network=1"}) {
		t.Fatalf("VPCs = %v", got)
	}
	productionIndex := slices.IndexFunc(view.VPCs, func(summary topology.EntrySummary) bool {
		return summary.Key == topology.VPCFocusKey("cn-hangzhou", "vpc-a")
	})
	if productionIndex < 0 {
		t.Fatalf("Production VPC missing: %+v", view.VPCs)
	}
	payload, err := json.Marshal(view.VPCs[productionIndex])
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["native_id"] != "vpc-a" {
		t.Fatalf("VPC native_id = %#v, payload = %s", fields["native_id"], payload)
	}
	if fields["asset_id"] != "vpc" {
		t.Fatalf("VPC asset_id = %#v, payload = %s", fields["asset_id"], payload)
	}
}

func TestProjectScopeCleanupIsNonSelectableWhenScopeIsAmbiguousOrInexact(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	input.Scopes = append(input.Scopes, asset.Scope{
		ID: "region-duplicate", ConnectionID: "connection-a", Kind: asset.ScopeRegion,
		NativeID: "cn-hangzhou", Location: "cn-hangzhou", Name: "Duplicate",
	})
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	account := response.View.(topology.AccountView)
	if account.Regions[1].Cleanup.Selectable {
		t.Fatalf("ambiguous Region scope is selectable: %+v", account.Regions[1])
	}

	input = topologyFixture()
	input.Assets[0].ScopeID = ""
	response, err = topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	global := response.View.(topology.AccountView).GlobalResources
	if global != nil && global.Cleanup.Selectable {
		t.Fatalf("inexact global scope is selectable: %+v", response.View)
	}
}

func TestProjectZeroResourceAuthoritativeRegionIsNonSelectable(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	input.Scopes = append(input.Scopes, asset.Scope{
		ID: "region-beijing", ConnectionID: "connection-a", Kind: asset.ScopeRegion,
		NativeID: "cn-beijing", Name: "Beijing", Location: "cn-beijing",
	})
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	account := response.View.(topology.AccountView)
	if account.Regions[0].ResourceCount != 0 || account.Regions[0].Cleanup.Selectable {
		t.Fatalf("zero-resource Region = %+v", account.Regions[0])
	}
}

func TestProjectOmitsScopeLessAssetWithWarning(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	input.Assets = []asset.Asset{testAsset("scope-less", "misc", "", nil)}
	input.Assets[0].ScopeID = ""
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	account := response.View.(topology.AccountView)
	if account.GlobalResources != nil {
		t.Fatalf("scope-less asset inferred global: %+v", account.GlobalResources)
	}
	if slices.ContainsFunc(account.Regions, func(summary topology.EntrySummary) bool { return summary.ResourceCount != 0 }) {
		t.Fatalf("scope-less asset inferred Region: %+v", account.Regions)
	}
	if !slices.ContainsFunc(response.Warnings, func(warning topology.ProjectionWarning) bool {
		return warning.Code == "scope_unknown" && warning.VisibleAssetID == "scope-less"
	}) {
		t.Fatalf("scope-less warning missing: %+v", response.Warnings)
	}
}

func TestProjectAWSGlobalEndpointLocationDoesNotDuplicateIntoRegion(t *testing.T) {
	t.Parallel()

	input := topology.Input{
		Connection: asset.CloudConnection{ID: "connection-aws", Provider: asset.ProviderAWS},
		Regions: []asset.ConnectionRegion{{
			ID: "region-us-east-1", ConnectionID: "connection-aws", RegionID: "us-east-1", Lifecycle: asset.RegionActive,
		}},
		Scopes: []asset.Scope{
			{ID: "global-aws", ConnectionID: "connection-aws", Kind: asset.ScopeGlobal, NativeID: "global", Location: "us-east-1"},
			{ID: "global-child-aws", ConnectionID: "connection-aws", ParentID: "global-aws", Kind: asset.ScopeResourceGroup, NativeID: "iam"},
			{ID: "region-aws", ConnectionID: "connection-aws", Kind: asset.ScopeRegion, NativeID: "us-east-1", Location: "us-east-1"},
		},
		Assets: []asset.Asset{{
			ID: "iam-role", Identity: asset.Identity{Provider: asset.ProviderAWS, ConnectionID: "connection-aws"},
			ScopeID: "global-child-aws", ResourceKindID: "iam", Location: "us-east-1",
		}},
		Kinds: map[asset.ResourceKindID]asset.ResourceKind{"iam": {ID: "iam", Class: "identity.role"}},
	}
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	account := response.View.(topology.AccountView)
	if account.GlobalResources == nil || account.GlobalResources.ResourceCount != 1 ||
		len(account.Regions) != 1 || account.Regions[0].ResourceCount != 0 {
		t.Fatalf("AWS global/Region summaries = %+v", account)
	}
}

func TestProjectResourceGraphKeepsUnknownVPCResourcesAndRealEdgesOnly(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	input.Focus = topology.Focus{Kind: topology.FocusRegionPublic, RegionID: "cn-hangzhou"}
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	view := response.View.(topology.ResourceGraphView)
	if !reflect.DeepEqual(view.Ancestors, []topology.ViewContext{{
		Key:      topology.RegionFocusKey("cn-hangzhou"),
		Name:     "Hangzhou",
		NativeID: "cn-hangzhou",
	}}) {
		t.Fatalf("ancestors = %+v", view.Ancestors)
	}
	got := resourceKeys(view.Resources)
	if !reflect.DeepEqual(got, []string{"region-public", "unknown-vpc"}) {
		t.Fatalf("resources = %v", got)
	}
	if !view.Resources[1].MembershipUnknown {
		t.Fatalf("unknown VPC resource = %+v", view.Resources[1])
	}
	for _, edge := range view.Edges {
		if !slices.Contains(got, edge.SourceKey) || !slices.Contains(got, edge.TargetKey) {
			t.Fatalf("edge has ghost endpoint: %+v", edge)
		}
	}
}

func TestProjectResourcesExposeLocalizedTypeNames(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	kind := input.Kinds["misc"]
	kind.DisplayNames = map[string]string{
		"zh-CN": "其他服务",
		"en-US": "Other Service",
	}
	input.Kinds["misc"] = kind
	input.Focus = topology.Focus{
		Kind:     topology.FocusRegionPublic,
		RegionID: "cn-hangzhou",
	}

	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	resource := findResource(
		t,
		response.View.(topology.ResourceGraphView).Resources,
		"region-public",
	)
	payload, err := json.Marshal(resource)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(payload, &fields); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fields["type_names"], map[string]any{
		"zh-CN": "其他服务",
		"en-US": "Other Service",
	}) {
		t.Fatalf("type_names = %#v, payload = %s", fields["type_names"], payload)
	}
}

func TestProjectVPCUsesPlacementWithoutContainmentEdges(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	for index := range input.Assets {
		if input.Assets[index].ID == "ecs-a" {
			input.Assets[index].Identity.NativeID = "i-production-api"
			break
		}
	}
	input.Focus = topology.Focus{Kind: topology.FocusVPC, RegionID: "cn-hangzhou", VPCID: "vpc-a"}
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	view := response.View.(topology.VPCView)
	if view.VPC.Key != "vpc" || view.VPC.NativeID != "vpc-a" {
		t.Fatalf("VPC context = %+v", view.VPC)
	}
	if slices.Contains(resourceKeys(view.Resources), "vpc") || slices.Contains(resourceKeys(view.Resources), "vsw-a") {
		t.Fatalf("boundary assets leaked into resources: %v", resourceKeys(view.Resources))
	}
	if got := vswitchPairs(view.VSwitches); !reflect.DeepEqual(got, []string{"vsw-a=4", "vsw-b=1", "vsw-empty=0"}) {
		t.Fatalf("vSwitches = %v", got)
	}
	if got := view.PublicResourceKeys; !reflect.DeepEqual(got, []string{"sg", "unknown-vswitch"}) {
		t.Fatalf("public resources = %v", got)
	}
	disk := findResource(t, view.Resources, "disk")
	if !slices.Contains(view.VSwitches[0].ResourceKeys, disk.Key) {
		t.Fatalf("disk did not inherit attached ECS placement: %+v", view.VSwitches)
	}
	ecsPayload, err := json.Marshal(findResource(t, view.Resources, "ecs-a"))
	if err != nil {
		t.Fatal(err)
	}
	var ecsFields map[string]any
	if err := json.Unmarshal(ecsPayload, &ecsFields); err != nil {
		t.Fatal(err)
	}
	if ecsFields["native_id"] != "i-production-api" {
		t.Fatalf("ECS native_id = %#v, payload = %s", ecsFields["native_id"], ecsPayload)
	}
	if !findResource(t, view.Resources, "unknown-vswitch").MembershipUnknown {
		t.Fatalf("unknown vSwitch resource was not marked")
	}
	for _, edge := range view.Edges {
		if edge.Relation == string(graph.RelationshipMemberOf) {
			t.Fatalf("containment relationship leaked: %+v", edge)
		}
		if edge.SourceKey == "vpc" || edge.TargetKey == "vpc" || edge.SourceKey == "vsw-a" || edge.TargetKey == "vsw-a" {
			t.Fatalf("edge uses boundary endpoint: %+v", edge)
		}
	}
	relationship := findEdge(t, view.Edges, "eni", "ecs-a", topology.ProjectedRelationship)
	evidence, _ := relationship.Metadata["evidence"].(map[string]any)
	if relationship.Relation != string(graph.RelationshipAttachedTo) || evidence["fixture"] != "eni-ecs" {
		t.Fatalf("relationship = %+v", relationship)
	}
	lifecycle := findEdge(t, view.Edges, "ecs-a", "disk", topology.ProjectedLifecycle)
	lifecycleEvidence, _ := lifecycle.Metadata["evidence"].(map[string]any)
	if lifecycle.Relation != string(graph.CleanupDelegate) ||
		lifecycle.Metadata["ownership"] != string(graph.OwnershipExclusive) ||
		lifecycleEvidence["fixture"] != "lifecycle" {
		t.Fatalf("lifecycle = %+v", lifecycle)
	}
	ecs := findResource(t, view.Resources, "ecs-a")
	if len(ecs.ExternalRelations) != 1 || ecs.ExternalRelations[0].Direction != "outgoing" || ecs.ExternalRelations[0].TargetID != "region-public" {
		t.Fatalf("external relations = %+v", ecs.ExternalRelations)
	}
	if len(response.Warnings) == 0 {
		t.Fatal("closed/missing endpoint warning is absent")
	}
	allKeys := resourceKeys(view.Resources)
	for _, edge := range view.Edges {
		if !slices.Contains(allKeys, edge.SourceKey) || !slices.Contains(allKeys, edge.TargetKey) {
			t.Fatalf("edge created a ghost endpoint: %+v", edge)
		}
	}
}

func TestProjectPlacesEveryVPCIDAliasUnderTheVPC(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	input.Kinds["route-table"] = asset.ResourceKind{
		ID: "route-table", Class: "network.route_table", DisplayName: "Route table",
	}
	aliases := []map[string]any{
		{"vpcId": "vpc-a"},
		{"VpcId": "vpc-a"},
		{"VPC_ID": "vpc-a"},
		{"configuration": map[string]any{"VpcId": "vpc-a"}},
	}
	for index, normalized := range aliases {
		value := testAsset(
			asset.AssetID(fmt.Sprintf("route-table-%d", index)),
			"route-table",
			"cn-hangzhou",
			normalized,
		)
		value.ScopeID = "region"
		input.Assets = append(input.Assets, value)
	}

	input.Focus = topology.Focus{
		Kind: topology.FocusVPC, RegionID: "cn-hangzhou", VPCID: "vpc-a",
	}
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	vpcKeys := resourceKeys(response.View.(topology.VPCView).Resources)
	for index := range aliases {
		key := fmt.Sprintf("route-table-%d", index)
		if !slices.Contains(vpcKeys, key) {
			t.Fatalf("%s was not placed under the VPC: %v", key, vpcKeys)
		}
	}

	input.Focus = topology.Focus{
		Kind: topology.FocusRegionPublic, RegionID: "cn-hangzhou",
	}
	response, err = topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	publicKeys := resourceKeys(response.View.(topology.ResourceGraphView).Resources)
	for index := range aliases {
		key := fmt.Sprintf("route-table-%d", index)
		if slices.Contains(publicKeys, key) {
			t.Fatalf("%s leaked into global resources: %v", key, publicKeys)
		}
	}
}

func TestProjectVPCConflictingAttachedPlacementStaysPublic(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	input.Assets = append(input.Assets, testAsset("conflict", "disk", "cn-hangzhou", nil))
	input.Assets[len(input.Assets)-1].ScopeID = "region"
	input.Relationships = append(input.Relationships,
		testRelationship("conflict-a", "conflict", "ecs-a", graph.RelationshipAttachedTo),
		testRelationship("conflict-b", "conflict", "ecs-b", graph.RelationshipAttachedTo),
	)
	input.Focus = topology.Focus{Kind: topology.FocusVPC, RegionID: "cn-hangzhou", VPCID: "vpc-a"}
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	view := response.View.(topology.VPCView)
	conflict := findResource(t, view.Resources, "conflict")
	if !conflict.MembershipUnknown || !slices.Contains(view.PublicResourceKeys, "conflict") {
		t.Fatalf("conflicting placement = %+v public=%v", conflict, view.PublicResourceKeys)
	}
	if len(response.Warnings) == 0 {
		t.Fatal("conflicting placement warning is absent")
	}
}

func TestProjectRegionDisablesVPCCleanupWhenMembershipIsUncertain(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	input.Assets = append(input.Assets, testAsset("conflict", "disk", "cn-hangzhou", nil))
	input.Assets[len(input.Assets)-1].ScopeID = "region"
	input.Relationships = append(input.Relationships,
		testRelationship("conflict-a", "conflict", "ecs-a", graph.RelationshipAttachedTo),
		testRelationship("conflict-b", "conflict", "ecs-b", graph.RelationshipAttachedTo),
	)
	input.Focus = topology.Focus{Kind: topology.FocusRegion, RegionID: "cn-hangzhou"}
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	view := response.View.(topology.RegionView)
	production := slices.IndexFunc(view.VPCs, func(summary topology.EntrySummary) bool {
		return summary.Key == topology.VPCFocusKey("cn-hangzhou", "vpc-a")
	})
	if production < 0 {
		t.Fatalf("VPC summaries = %+v", view.VPCs)
	}
	cleanup := view.VPCs[production].Cleanup
	if cleanup.Selectable || cleanup.PotentialBlockers == 0 {
		t.Fatalf("uncertain VPC cleanup = %+v", cleanup)
	}
}

func TestProjectVPCUsesNeverInheritsPlacement(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	input.Assets = append(input.Assets, testAsset("shared-service", "misc", "cn-hangzhou", nil))
	input.Assets[len(input.Assets)-1].ScopeID = "region"
	input.Relationships = append(input.Relationships,
		testRelationship("shared-use-a", "ecs-a", "shared-service", graph.RelationshipUses),
		testRelationship("shared-use-b", "ecs-b", "shared-service", graph.RelationshipUses),
	)
	input.Focus = topology.Focus{Kind: topology.FocusVPC, RegionID: "cn-hangzhou", VPCID: "vpc-a"}
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(resourceKeys(response.View.(topology.VPCView).Resources), "shared-service") {
		t.Fatalf("uses relationship inherited VPC placement: %+v", response.View)
	}
}

func TestProjectCENAttachmentsDoNotInferVPCPlacementOrWarn(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	input.Kinds["cen-vpc-attachment"] = asset.ResourceKind{
		ID: "cen-vpc-attachment", Class: "network.vpc_attachment",
		DisplayName: "CEN VPC Attachment",
	}
	input.Kinds["cen-route-table"] = asset.ResourceKind{
		ID: "cen-route-table", Class: "network.route_table",
		DisplayName: "CEN Transit Router Route Table",
	}
	input.Assets = append(input.Assets,
		testAsset("vpc-b", "vpc", "cn-hangzhou", map[string]any{
			topology.NormalizedVPCID: "vpc-b",
		}),
		testAsset(
			"cen-vpc-attachment-a",
			"cen-vpc-attachment",
			"cn-hangzhou",
			map[string]any{topology.NormalizedVPCID: "vpc-a"},
		),
		testAsset(
			"cen-vpc-attachment-b",
			"cen-vpc-attachment",
			"cn-hangzhou",
			map[string]any{topology.NormalizedVPCID: "vpc-b"},
		),
		testAsset("cen-route-table", "cen-route-table", "cn-hangzhou", nil),
	)
	for index := len(input.Assets) - 4; index < len(input.Assets); index++ {
		input.Assets[index].ScopeID = "region"
	}
	for _, relationship := range []graph.Relationship{
		testRelationship(
			"cen-route-association-a",
			"cen-vpc-attachment-a",
			"cen-route-table",
			graph.RelationshipAttachedTo,
		),
		testRelationship(
			"cen-route-association-b",
			"cen-vpc-attachment-b",
			"cen-route-table",
			graph.RelationshipAttachedTo,
		),
	} {
		relationship.Source = "cen:transit-router-routing-apis"
		input.Relationships = append(input.Relationships, relationship)
	}
	input.Focus = topology.Focus{
		Kind: topology.FocusRegionPublic, RegionID: "cn-hangzhou",
	}

	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	routeTable := findResource(
		t,
		response.View.(topology.ResourceGraphView).Resources,
		"cen-route-table",
	)
	if routeTable.MembershipUnknown {
		t.Fatalf("CEN routing relationship inferred network placement: %+v", routeTable)
	}
	if slices.ContainsFunc(response.Warnings, func(warning topology.ProjectionWarning) bool {
		return warning.Code == "placement_conflict" &&
			warning.VisibleAssetID == "cen-route-table"
	}) {
		t.Fatalf("CEN routing relationship emitted a placement warning: %+v", response.Warnings)
	}
	if len(routeTable.ExternalRelations) != 2 {
		t.Fatalf("CEN routing relationships were not preserved: %+v", routeTable.ExternalRelations)
	}
	for _, relationship := range routeTable.ExternalRelations {
		if relationship.Relation != string(graph.RelationshipAttachedTo) {
			t.Fatalf("CEN routing relationship = %+v", relationship)
		}
	}
}

func TestProjectStackGroupDoesNotParticipateInNetworkPlacement(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	input.Kinds["stack-group"] = asset.ResourceKind{
		ID: "stack-group", Class: "orchestration.stack_group",
		DisplayName: "ROS Stack Group",
	}
	stackGroup := testAsset("stack-group-a", "stack-group", "cn-hangzhou", nil)
	stackGroup.ScopeID = "region"
	input.Assets = append(input.Assets, stackGroup)
	input.Relationships = append(input.Relationships, testRelationship(
		"stack-group-attachment",
		"stack-group-a",
		"ecs-a",
		graph.RelationshipAttachedTo,
	))
	input.Focus = topology.Focus{
		Kind: topology.FocusRegionPublic, RegionID: "cn-hangzhou",
	}

	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	stackGroupResource := findResource(
		t,
		response.View.(topology.ResourceGraphView).Resources,
		"stack-group-a",
	)
	if stackGroupResource.MembershipUnknown {
		t.Fatalf("stack group has network membership status: %+v", stackGroupResource)
	}
	if slices.ContainsFunc(response.Warnings, func(warning topology.ProjectionWarning) bool {
		return warning.Code == "placement_conflict" &&
			warning.VisibleAssetID == "stack-group-a"
	}) {
		t.Fatalf("stack group emitted a placement warning: %+v", response.Warnings)
	}
}

func TestProjectAttachedPlacementIsIndependentOfAssetOrder(t *testing.T) {
	t.Parallel()

	input := attachedConflictFixture()
	first, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	reversed := input
	reversed.Assets = append([]asset.Asset(nil), input.Assets...)
	slices.Reverse(reversed.Assets)
	second, err := topology.Project(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := chainProjection(first), chainProjection(second); !reflect.DeepEqual(got, want) {
		t.Fatalf("attached placement depends on asset order:\nfirst=%+v\nsecond=%+v", got, want)
	}
}

func TestProjectAttachedChainConflictMarksEveryInferredNodeUnknown(t *testing.T) {
	t.Parallel()

	response, err := topology.Project(attachedConflictFixture())
	if err != nil {
		t.Fatal(err)
	}
	view := response.View.(topology.ResourceGraphView)
	for _, id := range []asset.AssetID{"chain-a", "chain-b"} {
		resource := findResource(t, view.Resources, string(id))
		if !resource.MembershipUnknown {
			t.Fatalf("%s placement = %+v", id, resource)
		}
		if !slices.ContainsFunc(response.Warnings, func(warning topology.ProjectionWarning) bool {
			return warning.Code == "placement_conflict" && warning.VisibleAssetID == string(id)
		}) {
			t.Fatalf("%s conflict warning missing: %+v", id, response.Warnings)
		}
	}
}

func TestProjectAttachedComponentRejectsUnknownOrInvalidExplicitPlacement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		normalized map[string]any
		addForeign bool
	}{
		{
			name:       "unknown explicit VPC",
			normalized: map[string]any{topology.NormalizedVPCID: "vpc-missing"},
		},
		{
			name: "missing explicit vSwitch",
			normalized: map[string]any{
				topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-missing",
			},
		},
		{
			name: "invalid explicit vSwitch parent",
			normalized: map[string]any{
				topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-foreign",
			},
			addForeign: true,
		},
	}
	for _, test := range tests {
		test := test
		for _, reversed := range []bool{false, true} {
			name := test.name
			if reversed {
				name += " reversed"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				input := topologyFixture()
				input.Relationships = []graph.Relationship{
					testRelationship("attach-valid", "ecs-a", "inferred", graph.RelationshipAttachedTo),
					testRelationship("attach-invalid", "invalid-explicit", "inferred", graph.RelationshipAttachedTo),
				}
				input.Assets = append(input.Assets,
					testAsset("invalid-explicit", "misc", "cn-hangzhou", test.normalized),
					testAsset("inferred", "misc", "cn-hangzhou", nil),
				)
				for index := len(input.Assets) - 2; index < len(input.Assets); index++ {
					input.Assets[index].ScopeID = "region"
				}
				if test.addForeign {
					input.Assets = append(input.Assets,
						testAsset("vpc-b-invalid", "vpc", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-b"}),
						testAsset("vsw-foreign-invalid", "vsw", "cn-hangzhou", map[string]any{
							topology.NormalizedVPCID: "vpc-b", topology.NormalizedVSwitchID: "vsw-foreign",
						}),
					)
					for index := len(input.Assets) - 2; index < len(input.Assets); index++ {
						input.Assets[index].ScopeID = "region"
					}
				}
				if reversed {
					slices.Reverse(input.Assets)
				}
				input.Focus = topology.Focus{Kind: topology.FocusRegionPublic, RegionID: "cn-hangzhou"}

				response, err := topology.Project(input)
				if err != nil {
					t.Fatal(err)
				}
				inferred := findResource(t, response.View.(topology.ResourceGraphView).Resources, "inferred")
				if !inferred.MembershipUnknown {
					t.Fatalf("inferred resource inherited despite invalid explicit endpoint: %+v", inferred)
				}
				if !slices.ContainsFunc(response.Warnings, func(warning topology.ProjectionWarning) bool {
					return warning.Code == "placement_conflict" && warning.VisibleAssetID == "inferred"
				}) {
					t.Fatalf("inferred conflict warning missing: %+v", response.Warnings)
				}
			})
		}
	}
}

func TestProjectAttachedPartialEndpointDoesNotInferVSwitch(t *testing.T) {
	t.Parallel()

	for _, reversed := range []bool{false, true} {
		reversed := reversed
		name := "forward"
		if reversed {
			name = "reversed"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := topologyFixture()
			input.Assets = append(input.Assets,
				testAsset("endpoint-vpc-only", "misc", "cn-hangzhou", map[string]any{
					topology.NormalizedVPCID: "vpc-a",
				}),
				testAsset("endpoint-full", "misc", "cn-hangzhou", map[string]any{
					topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a",
				}),
				testAsset("inferred-partial", "misc", "cn-hangzhou", nil),
			)
			for index := len(input.Assets) - 3; index < len(input.Assets); index++ {
				input.Assets[index].ScopeID = "region"
			}
			input.Relationships = append(input.Relationships,
				testRelationship("partial-vpc", "endpoint-vpc-only", "inferred-partial", graph.RelationshipAttachedTo),
				testRelationship("partial-full", "endpoint-full", "inferred-partial", graph.RelationshipAttachedTo),
			)
			if reversed {
				slices.Reverse(input.Assets)
				slices.Reverse(input.Relationships)
			}
			input.Focus = topology.Focus{Kind: topology.FocusVPC, RegionID: "cn-hangzhou", VPCID: "vpc-a"}

			response, err := topology.Project(input)
			if err != nil {
				t.Fatal(err)
			}
			view := response.View.(topology.VPCView)
			inferred := findResource(t, view.Resources, "inferred-partial")
			if inferred.MembershipUnknown || !slices.Contains(view.PublicResourceKeys, inferred.Key) {
				t.Fatalf("partial endpoints produced non-public inferred placement: resource=%+v public=%v", inferred, view.PublicResourceKeys)
			}
			for _, vSwitch := range view.VSwitches {
				if slices.Contains(vSwitch.ResourceKeys, inferred.Key) {
					t.Fatalf("partial endpoints inferred vSwitch %s: %+v", vSwitch.NativeID, view.VSwitches)
				}
			}
		})
	}
}

func TestProjectAttachedPlacementRequiresAuthoritativeEndpointRegionEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		setup  func(*topology.Input)
		region string
	}{
		{
			name: "cross-Region identical network IDs",
			setup: func(input *topology.Input) {
				input.Scopes = append(input.Scopes, asset.Scope{
					ID: "region-b", ConnectionID: "connection-a", Kind: asset.ScopeRegion,
					NativeID: "cn-beijing", Location: "cn-beijing",
				})
				input.Assets = append(input.Assets,
					testAsset("vpc-region-b", "vpc", "cn-beijing", map[string]any{
						topology.NormalizedVPCID: "vpc-a",
					}),
					testAsset("vsw-region-b", "vsw", "cn-beijing", map[string]any{
						topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a",
					}),
					testAsset("endpoint-region-a", "misc", "cn-hangzhou", map[string]any{
						topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a",
					}),
					testAsset("endpoint-region-b", "misc", "cn-beijing", map[string]any{
						topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a",
					}),
					testAsset("inferred-evidence", "misc", "cn-hangzhou", nil),
				)
				for _, id := range []asset.AssetID{"vpc-region-b", "vsw-region-b", "endpoint-region-b"} {
					for index := range input.Assets {
						if input.Assets[index].ID == id {
							input.Assets[index].ScopeID = "region-b"
						}
					}
				}
				for _, id := range []asset.AssetID{"endpoint-region-a", "inferred-evidence"} {
					for index := range input.Assets {
						if input.Assets[index].ID == id {
							input.Assets[index].ScopeID = "region"
						}
					}
				}
				input.Relationships = append(input.Relationships,
					testRelationship("cross-region-a", "endpoint-region-a", "inferred-evidence", graph.RelationshipAttachedTo),
					testRelationship("cross-region-b", "endpoint-region-b", "inferred-evidence", graph.RelationshipAttachedTo),
				)
			},
			region: "cn-hangzhou",
		},
		{
			name: "boundary and endpoint lack authoritative Scope",
			setup: func(input *topology.Input) {
				for index := range input.Assets {
					if input.Assets[index].ID == "vpc" || input.Assets[index].ID == "vsw-a" {
						input.Assets[index].ScopeID = ""
					}
				}
				input.Assets = append(input.Assets,
					testAsset("endpoint-no-boundary-scope", "misc", "", map[string]any{
						topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a",
					}),
					testAsset("inferred-evidence", "misc", "cn-hangzhou", nil),
				)
				input.Assets[len(input.Assets)-2].ScopeID = ""
				input.Assets[len(input.Assets)-1].ScopeID = "region"
				input.Relationships = append(input.Relationships,
					testRelationship("missing-boundary-scope", "endpoint-no-boundary-scope", "inferred-evidence", graph.RelationshipAttachedTo),
				)
			},
			region: "cn-hangzhou",
		},
		{
			name: "endpoint lacks authoritative Scope",
			setup: func(input *topology.Input) {
				input.Assets = append(input.Assets,
					testAsset("endpoint-no-scope", "misc", "", map[string]any{
						topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a",
					}),
					testAsset("inferred-evidence", "misc", "cn-hangzhou", nil),
				)
				input.Assets[len(input.Assets)-2].ScopeID = ""
				input.Assets[len(input.Assets)-1].ScopeID = "region"
				input.Relationships = append(input.Relationships,
					testRelationship("missing-endpoint-scope", "endpoint-no-scope", "inferred-evidence", graph.RelationshipAttachedTo),
				)
			},
			region: "cn-hangzhou",
		},
		{
			name: "inferred node Region disagrees",
			setup: func(input *topology.Input) {
				input.Scopes = append(input.Scopes, asset.Scope{
					ID: "region-b", ConnectionID: "connection-a", Kind: asset.ScopeRegion,
					NativeID: "cn-beijing", Location: "cn-beijing",
				})
				input.Assets = append(input.Assets,
					testAsset("endpoint-region-a", "misc", "cn-hangzhou", map[string]any{
						topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a",
					}),
					testAsset("inferred-evidence", "misc", "cn-beijing", nil),
				)
				input.Assets[len(input.Assets)-2].ScopeID = "region"
				input.Assets[len(input.Assets)-1].ScopeID = "region-b"
				input.Relationships = append(input.Relationships,
					testRelationship("inferred-region-mismatch", "endpoint-region-a", "inferred-evidence", graph.RelationshipAttachedTo),
				)
			},
			region: "cn-beijing",
		},
	}
	for _, test := range tests {
		test := test
		for _, reversed := range []bool{false, true} {
			reversed := reversed
			name := test.name + " forward"
			if reversed {
				name = test.name + " reversed"
			}
			t.Run(name, func(t *testing.T) {
				t.Parallel()
				input := topologyFixture()
				test.setup(&input)
				if reversed {
					slices.Reverse(input.Assets)
					slices.Reverse(input.Relationships)
				}
				input.Focus = topology.Focus{Kind: topology.FocusRegionPublic, RegionID: test.region}

				response, err := topology.Project(input)
				if err != nil {
					t.Fatal(err)
				}
				inferred := findResource(t, response.View.(topology.ResourceGraphView).Resources, "inferred-evidence")
				if !inferred.MembershipUnknown {
					t.Fatalf("inferred placement did not fail closed: %+v", inferred)
				}
				if !slices.ContainsFunc(response.Warnings, func(warning topology.ProjectionWarning) bool {
					return warning.Code == "placement_conflict" && warning.VisibleAssetID == "inferred-evidence"
				}) {
					t.Fatalf("inferred conflict warning missing: %+v", response.Warnings)
				}
			})
		}
	}
}

func TestProjectAttachedPlacementAcceptsSameRegionCompleteAgreement(t *testing.T) {
	t.Parallel()

	for _, reversed := range []bool{false, true} {
		reversed := reversed
		name := "forward"
		if reversed {
			name = "reversed"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			input := topologyFixture()
			input.Assets = append(input.Assets,
				testAsset("endpoint-complete-a", "misc", "cn-hangzhou", map[string]any{
					topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a",
				}),
				testAsset("endpoint-complete-b", "misc", "cn-hangzhou", map[string]any{
					topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a",
				}),
				testAsset("inferred-complete", "misc", "cn-hangzhou", nil),
			)
			for index := len(input.Assets) - 3; index < len(input.Assets); index++ {
				input.Assets[index].ScopeID = "region"
			}
			input.Relationships = append(input.Relationships,
				testRelationship("complete-a", "endpoint-complete-a", "inferred-complete", graph.RelationshipAttachedTo),
				testRelationship("complete-b", "endpoint-complete-b", "inferred-complete", graph.RelationshipAttachedTo),
			)
			if reversed {
				slices.Reverse(input.Assets)
				slices.Reverse(input.Relationships)
			}
			input.Focus = topology.Focus{Kind: topology.FocusVPC, RegionID: "cn-hangzhou", VPCID: "vpc-a"}

			response, err := topology.Project(input)
			if err != nil {
				t.Fatal(err)
			}
			view := response.View.(topology.VPCView)
			inferred := findResource(t, view.Resources, "inferred-complete")
			if inferred.MembershipUnknown || slices.Contains(view.PublicResourceKeys, inferred.Key) {
				t.Fatalf("complete agreement did not infer placement: resource=%+v public=%v", inferred, view.PublicResourceKeys)
			}
			if !slices.ContainsFunc(view.VSwitches, func(vSwitch topology.VSwitch) bool {
				return vSwitch.NativeID == "vsw-a" && slices.Contains(vSwitch.ResourceKeys, inferred.Key)
			}) {
				t.Fatalf("complete agreement did not infer vSwitch: %+v", view.VSwitches)
			}
		})
	}
}

func TestProjectVPCMismatchedVSwitchParentStaysPublicWithWarning(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	input.Assets = append(input.Assets,
		testAsset("vpc-b", "vpc", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-b"}),
		testAsset("vsw-foreign", "vsw", "cn-hangzhou", map[string]any{
			topology.NormalizedVPCID: "vpc-b", topology.NormalizedVSwitchID: "vsw-foreign",
		}),
		testAsset("wrong-parent", "misc", "cn-hangzhou", map[string]any{
			topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-foreign",
		}),
	)
	for index := len(input.Assets) - 3; index < len(input.Assets); index++ {
		input.Assets[index].ScopeID = "region"
	}
	input.Focus = topology.Focus{Kind: topology.FocusVPC, RegionID: "cn-hangzhou", VPCID: "vpc-a"}
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	view := response.View.(topology.VPCView)
	resource := findResource(t, view.Resources, "wrong-parent")
	if !resource.MembershipUnknown || !slices.Contains(view.PublicResourceKeys, resource.Key) {
		t.Fatalf("mismatched vSwitch parent placement = %+v public=%v", resource, view.PublicResourceKeys)
	}
	if !slices.ContainsFunc(response.Warnings, func(warning topology.ProjectionWarning) bool {
		return warning.Code == "vswitch_parent_mismatch" && warning.VisibleAssetID == "wrong-parent"
	}) {
		t.Fatalf("mismatched vSwitch warning missing: %+v", response.Warnings)
	}
}

func TestProjectFiltersGraphResourcesAndVSwitchCounts(t *testing.T) {
	t.Parallel()

	input := topologyFixture()
	input.Focus = topology.Focus{Kind: topology.FocusVPC, RegionID: "cn-hangzhou", VPCID: "vpc-a"}
	input.ResourceClass = "compute.instance"
	input.Risk = "findings"
	response, err := topology.Project(input)
	if err != nil {
		t.Fatal(err)
	}
	view := response.View.(topology.VPCView)
	if got := resourceKeys(view.Resources); !reflect.DeepEqual(got, []string{"ecs-a"}) {
		t.Fatalf("filtered resources = %v", got)
	}
	if got := vswitchPairs(view.VSwitches); !reflect.DeepEqual(got, []string{"vsw-a=1", "vsw-b=0", "vsw-empty=0"}) {
		t.Fatalf("filtered vSwitch counts = %v", got)
	}
}

func TestProjectPaginatesVPCDeterministicallyAtScaleAndOwnsEdgesByLaterEndpoint(t *testing.T) {
	t.Parallel()

	const total = 2000
	input := topology.Input{
		Focus:      topology.Focus{Kind: topology.FocusVPC, RegionID: "cn-scale", VPCID: "vpc-scale"},
		Connection: asset.CloudConnection{ID: "connection-a"},
		Regions:    []asset.ConnectionRegion{{ID: "region-scale", ConnectionID: "connection-a", RegionID: "cn-scale", Lifecycle: asset.RegionActive}},
		Scopes:     []asset.Scope{{ID: "scope-scale", ConnectionID: "connection-a", Kind: asset.ScopeRegion, NativeID: "cn-scale", Location: "cn-scale"}},
		Kinds: map[asset.ResourceKindID]asset.ResourceKind{
			"vpc": {ID: "vpc", Class: "network.vpc"},
			"vsw": {ID: "vsw", Class: "network.subnet"},
			"ecs": {ID: "ecs", Class: "compute.instance", DisplayName: "Instance"},
		},
		Limit: 137,
	}
	input.Assets = append(input.Assets, testAsset("vpc-scale-asset", "vpc", "cn-scale", map[string]any{topology.NormalizedVPCID: "vpc-scale"}))
	for index := 0; index < 5; index++ {
		input.Assets = append(input.Assets, testAsset(asset.AssetID(fmt.Sprintf("vsw-%d", index)), "vsw", "cn-scale", map[string]any{
			topology.NormalizedVPCID: "vpc-scale", topology.NormalizedVSwitchID: fmt.Sprintf("vsw-%d", index),
		}))
	}
	for index := total - 1; index >= 0; index-- {
		input.Assets = append(input.Assets, testAsset(asset.AssetID(fmt.Sprintf("resource-%04d", index)), "ecs", "cn-scale", map[string]any{
			topology.NormalizedVPCID: "vpc-scale", topology.NormalizedVSwitchID: fmt.Sprintf("vsw-%d", index%5),
		}))
	}
	for index := range input.Assets {
		input.Assets[index].ScopeID = "scope-scale"
	}
	input.Relationships = []graph.Relationship{testRelationship("cross-page", "resource-0000", "resource-0137", graph.RelationshipDependsOn)}

	var all []string
	var edgePages int
	seenCursors := map[string]bool{}
	for {
		response, err := topology.Project(input)
		if err != nil {
			t.Fatal(err)
		}
		repeated, err := topology.Project(input)
		if err != nil {
			t.Fatal(err)
		}
		left, _ := json.Marshal(response)
		right, _ := json.Marshal(repeated)
		if string(left) != string(right) {
			t.Fatal("identical input was not deterministic")
		}
		view := response.View.(topology.VPCView)
		if len(view.Resources) > input.Limit || len(view.VSwitches) != 5 {
			t.Fatalf("bounded page resources=%d switches=%d", len(view.Resources), len(view.VSwitches))
		}
		for _, item := range view.VSwitches {
			if item.ResourceCount != 400 {
				t.Fatalf("complete vSwitch count = %+v", item)
			}
		}
		all = append(all, resourceKeys(view.Resources)...)
		edgePages += len(view.Edges)
		if response.NextCursor == "" {
			if response.Truncated {
				t.Fatal("last page is truncated")
			}
			break
		}
		if seenCursors[response.NextCursor] {
			t.Fatalf("repeating cursor %q", response.NextCursor)
		}
		seenCursors[response.NextCursor] = true
		input.Cursor = response.NextCursor
	}
	if len(all) != total || all[0] != "resource-0000" || all[total-1] != "resource-1999" {
		t.Fatalf("merged resources len=%d first=%q last=%q", len(all), all[0], all[len(all)-1])
	}
	if edgePages != 1 {
		t.Fatalf("cross-page edge count = %d", edgePages)
	}
}

func topologyFixture() topology.Input {
	closed := time.Date(2026, 7, 24, 0, 0, 0, 0, time.UTC)
	input := topology.Input{
		Connection: asset.CloudConnection{ID: "connection-a", Name: "Production"},
		Regions: []asset.ConnectionRegion{
			{ID: "region-2", ConnectionID: "connection-a", RegionID: "cn-beijing", DiscoveredName: "Beijing", Lifecycle: asset.RegionActive},
			{ID: "region-1", ConnectionID: "connection-a", RegionID: "cn-hangzhou", DiscoveredName: "Hangzhou", Lifecycle: asset.RegionActive},
			{ID: "region-3", ConnectionID: "connection-a", RegionID: "cn-shanghai", DiscoveredName: "Shanghai", Lifecycle: asset.RegionRetired},
		},
		Scopes: []asset.Scope{
			{ID: "global", ConnectionID: "connection-a", Kind: asset.ScopeGlobal, NativeID: "global", Name: "Global"},
			{ID: "region", ConnectionID: "connection-a", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Name: "Hangzhou", Location: "cn-hangzhou"},
		},
		Kinds: map[asset.ResourceKindID]asset.ResourceKind{
			"vpc":  {ID: "vpc", Class: "network.vpc", DisplayName: "VPC", Icon: "network"},
			"vsw":  {ID: "vsw", Class: "network.subnet", DisplayName: "vSwitch", Icon: "network"},
			"ecs":  {ID: "ecs", Class: "compute.instance", DisplayName: "Instance", Icon: "server"},
			"disk": {ID: "disk", Class: "storage.block", DisplayName: "Disk", Icon: "disk"},
			"eni":  {ID: "eni", Class: "network.interface", DisplayName: "ENI", Icon: "network"},
			"sg":   {ID: "sg", Class: "network.security_group", DisplayName: "Security Group", Icon: "shield"},
			"misc": {ID: "misc", Class: "other.service", DisplayName: "Other"},
		},
		FindingCounts: map[asset.AssetID]int{"ecs-a": 2},
		Limit:         200,
	}
	input.Assets = []asset.Asset{
		testAsset("global-asset", "misc", "", nil),
		testAsset("vpc", "vpc", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-a"}),
		testAsset("vpc-empty", "vpc", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-empty"}),
		testNamedAsset("vsw-a", "Primary switch", "vsw", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a", topology.NormalizedZoneID: "cn-hangzhou-h"}),
		testNamedAsset("vsw-b", "Secondary switch", "vsw", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-b"}),
		testNamedAsset("vsw-empty", "Empty switch", "vsw", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-empty"}),
		testAsset("ecs-a", "ecs", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a"}),
		testAsset("ecs-a2", "ecs", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a"}),
		testAsset("ecs-b", "ecs", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-b"}),
		testAsset("disk", "disk", "cn-hangzhou", nil),
		testAsset("eni", "eni", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a"}),
		testAsset("sg", "sg", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-a"}),
		testAsset("unknown-vswitch", "misc", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-missing"}),
		testAsset("region-public", "misc", "cn-hangzhou", nil),
		testAsset("unknown-vpc", "misc", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-missing"}),
		testAsset("closed-external", "misc", "cn-hangzhou", nil),
	}
	input.Assets[0].ScopeID = "global"
	for index := 1; index < len(input.Assets); index++ {
		input.Assets[index].ScopeID = "region"
	}
	input.Assets[15].ClosedAt = &closed
	input.Assets[1].Name = "Production network"
	input.Assets[1].Identity.NativeID = "vpc-a"
	input.Assets[2].Name = "Empty network"
	input.Assets[2].Identity.NativeID = "vpc-empty"
	input.Relationships = []graph.Relationship{
		testRelationship("disk-ecs", "disk", "ecs-a", graph.RelationshipAttachedTo),
		testRelationship("eni-ecs", "eni", "ecs-a", graph.RelationshipAttachedTo),
		testRelationship("ecs-vpc", "ecs-a", "vpc", graph.RelationshipMemberOf),
		testRelationship("ecs-vsw", "ecs-a", "vsw-a", graph.RelationshipMemberOf),
		testRelationship("ecs-public", "ecs-a", "region-public", graph.RelationshipDependsOn),
		testRelationship("ecs-closed", "ecs-a", "closed-external", graph.RelationshipDependsOn),
	}
	input.LifecycleBindings = []graph.LifecycleBinding{{
		ID: "lifecycle", ControllerAssetID: "ecs-a", ManagedAssetID: "disk",
		Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive,
		CleanupPolicy: graph.CleanupDelegate, Confidence: 1,
		EvidenceSource: "fixture", Evidence: map[string]any{"fixture": "lifecycle"},
	}}
	return input
}

func attachedConflictFixture() topology.Input {
	input := topologyFixture()
	input.Assets = append(input.Assets,
		testAsset("vpc-b", "vpc", "cn-hangzhou", map[string]any{topology.NormalizedVPCID: "vpc-b"}),
		testAsset("vsw-c", "vsw", "cn-hangzhou", map[string]any{
			topology.NormalizedVPCID: "vpc-b", topology.NormalizedVSwitchID: "vsw-c",
		}),
		testAsset("endpoint-a", "misc", "cn-hangzhou", map[string]any{
			topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a",
		}),
		testAsset("chain-a", "misc", "cn-hangzhou", nil),
		testAsset("chain-b", "misc", "cn-hangzhou", nil),
		testAsset("endpoint-b", "misc", "cn-hangzhou", map[string]any{
			topology.NormalizedVPCID: "vpc-b", topology.NormalizedVSwitchID: "vsw-c",
		}),
	)
	for index := len(input.Assets) - 6; index < len(input.Assets); index++ {
		input.Assets[index].ScopeID = "region"
	}
	input.Relationships = append(input.Relationships,
		testRelationship("attach-endpoint-a", "endpoint-a", "chain-a", graph.RelationshipAttachedTo),
		testRelationship("attach-chain", "chain-a", "chain-b", graph.RelationshipAttachedTo),
		testRelationship("attach-endpoint-b", "chain-b", "endpoint-b", graph.RelationshipAttachedTo),
	)
	input.Focus = topology.Focus{Kind: topology.FocusRegionPublic, RegionID: "cn-hangzhou"}
	return input
}

func chainProjection(response topology.Response) map[string]any {
	view := response.View.(topology.ResourceGraphView)
	result := map[string]any{}
	for _, value := range view.Resources {
		if value.AssetID == "chain-a" || value.AssetID == "chain-b" {
			result[string(value.AssetID)] = value.MembershipUnknown
		}
	}
	for _, warning := range response.Warnings {
		if warning.VisibleAssetID == "chain-a" || warning.VisibleAssetID == "chain-b" {
			result[warning.VisibleAssetID+":warning"] = warning.Code
		}
	}
	return result
}

func testAsset(id asset.AssetID, kind asset.ResourceKindID, region string, normalized map[string]any) asset.Asset {
	return testNamedAsset(id, string(id), kind, region, normalized)
}

func testNamedAsset(id asset.AssetID, name string, kind asset.ResourceKindID, region string, normalized map[string]any) asset.Asset {
	if normalized == nil {
		normalized = map[string]any{}
	}
	return asset.Asset{
		ID: id, Identity: asset.Identity{ConnectionID: "connection-a", NativeID: string(id)},
		ResourceKindID: kind, Name: name, Location: region, Normalized: normalized,
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable},
	}
}

func testScopedAsset(
	id asset.AssetID,
	kind asset.ResourceKindID,
	scopeID asset.ScopeID,
	region string,
) asset.Asset {
	value := testAsset(id, kind, region, nil)
	value.ScopeID = scopeID
	return value
}

func testRelationship(id graph.RelationshipID, source, target asset.AssetID, kind graph.RelationshipType) graph.Relationship {
	return graph.Relationship{
		ID: id, SourceAssetID: source, TargetAssetID: target, Type: kind, Confidence: 1,
		Evidence: map[string]any{"fixture": string(id)},
	}
}

func summaryPairs(values []topology.EntrySummary) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, fmt.Sprintf("%s=%d", value.Name, value.ResourceCount))
	}
	return result
}

func vswitchPairs(values []topology.VSwitch) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, fmt.Sprintf("%s=%d", value.NativeID, value.ResourceCount))
	}
	return result
}

func resourceKeys(values []topology.Resource) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		result = append(result, value.Key)
	}
	return result
}

func findResource(t *testing.T, values []topology.Resource, key string) topology.Resource {
	t.Helper()
	for _, value := range values {
		if value.Key == key {
			return value
		}
	}
	t.Fatalf("resource %q missing: %+v", key, values)
	return topology.Resource{}
}

func findEdge(t *testing.T, values []topology.ResourceEdge, source, target string, kind topology.ProjectedEdgeKind) topology.ResourceEdge {
	t.Helper()
	for _, value := range values {
		if value.SourceKey == source && value.TargetKey == target && value.Kind == kind {
			return value
		}
	}
	t.Fatalf("edge %q -> %q (%s) missing: %+v", source, target, kind, values)
	return topology.ResourceEdge{}
}
