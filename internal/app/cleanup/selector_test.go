package cleanup_test

import (
	"reflect"
	"slices"
	"testing"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/core/topology"
)

func TestExpandConnectionIncludesRegionalAndGlobalAssets(t *testing.T) {
	t.Parallel()

	result, err := cleanup.ExpandSelectors(cleanup.SelectionInput{
		Selectors: []plan.CleanupSelector{{Kind: plan.SelectorConnection, ConnectionID: "connection"}},
		Scopes: []asset.Scope{
			{ID: "account", ConnectionID: "connection", Kind: asset.ScopeAccount},
			{ID: "region", ConnectionID: "connection", ParentID: "account", Kind: asset.ScopeRegion},
			{ID: "global", ConnectionID: "connection", ParentID: "account", Kind: asset.ScopeGlobal},
		},
		Assets: []asset.Asset{
			{ID: "regional", Identity: asset.Identity{ConnectionID: "connection"}, ScopeID: "region"},
			{ID: "global-asset", Identity: asset.Identity{ConnectionID: "connection"}, ScopeID: "global"},
			{ID: "other", Identity: asset.Identity{ConnectionID: "other"}, ScopeID: "other-scope"},
		},
	})
	if err != nil || !reflect.DeepEqual(result.AssetIDs, []asset.AssetID{"global-asset", "regional"}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
}

func TestExpandOverlappingSelectorsDeduplicatesAssets(t *testing.T) {
	t.Parallel()

	result, err := cleanup.ExpandSelectors(cleanup.SelectionInput{
		Selectors: []plan.CleanupSelector{
			{Kind: plan.SelectorConnection, ConnectionID: "connection"},
			{Kind: plan.SelectorScope, ConnectionID: "connection", ScopeID: "region", Descendants: true},
			{Kind: plan.SelectorAsset, AssetID: "instance"},
		},
		Scopes: []asset.Scope{{ID: "region", ConnectionID: "connection", Kind: asset.ScopeRegion}},
		Assets: []asset.Asset{{ID: "instance", Identity: asset.Identity{ConnectionID: "connection"}, ScopeID: "region"}},
	})
	if err != nil || !reflect.DeepEqual(result.AssetIDs, []asset.AssetID{"instance"}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	wantSelectorAssetIDs := [][]asset.AssetID{
		{"instance"},
		{"instance"},
		{"instance"},
	}
	if !reflect.DeepEqual(result.SelectorAssetIDs, wantSelectorAssetIDs) {
		t.Fatalf("selector asset IDs = %v, want %v", result.SelectorAssetIDs, wantSelectorAssetIDs)
	}
}

func TestExpandSelectorsVPCUsesRegionAndNormalizedMembership(t *testing.T) {
	t.Parallel()

	groupKey := topology.VPCFocusKey("cn-hangzhou", "vpc-1")
	result, err := cleanup.ExpandSelectors(cleanup.SelectionInput{
		Selectors: []plan.CleanupSelector{{Kind: plan.SelectorGroup, ConnectionID: "connection", GroupKey: groupKey, Descendants: true}},
		Scopes: []asset.Scope{
			{ID: "region-hangzhou", ConnectionID: "connection", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
			{ID: "region-beijing", ConnectionID: "connection", Kind: asset.ScopeRegion, NativeID: "cn-beijing"},
		},
		Kinds: map[asset.ResourceKindID]asset.ResourceKind{
			"vpc": {ID: "vpc", Class: "network.vpc"},
		},
		Assets: []asset.Asset{
			{ID: "instance", Identity: asset.Identity{ConnectionID: "connection"}, ScopeID: "region-hangzhou", Normalized: map[string]any{topology.NormalizedVPCID: "vpc-1"}},
			{ID: "gateway-endpoint", Identity: asset.Identity{ConnectionID: "connection", NativeID: "vpce-a"}, ScopeID: "region-hangzhou", Normalized: map[string]any{"vpcId": "vpc-1"}},
			{ID: "network", Identity: asset.Identity{ConnectionID: "connection", NativeID: "vpc-1"}, ResourceKindID: "vpc", Name: "Production VPC", ScopeID: "region-hangzhou", Normalized: map[string]any{topology.NormalizedVPCID: "vpc-1"}},
			{ID: "other-vpc", Identity: asset.Identity{ConnectionID: "connection"}, ScopeID: "region-hangzhou", Normalized: map[string]any{topology.NormalizedVPCID: "vpc-2"}},
			{ID: "other-region", Identity: asset.Identity{ConnectionID: "connection"}, ScopeID: "region-beijing", Normalized: map[string]any{topology.NormalizedVPCID: "vpc-1"}},
			{ID: "other-connection", Identity: asset.Identity{ConnectionID: "other"}, ScopeID: "region-hangzhou", Normalized: map[string]any{topology.NormalizedVPCID: "vpc-1"}},
		},
	})
	if err != nil || !reflect.DeepEqual(result.AssetIDs, []asset.AssetID{"gateway-endpoint", "instance", "network"}) {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	if result.Selectors[0].DisplayName != "Production VPC" || result.Selectors[0].GroupKey != groupKey {
		t.Fatalf("selector = %+v", result.Selectors[0])
	}
}

func TestExpandSelectorsVPCMatchesCertainProjectedMembership(t *testing.T) {
	t.Parallel()

	connection := asset.CloudConnection{ID: "connection"}
	scopes := []asset.Scope{{
		ID: "region", ConnectionID: connection.ID, Kind: asset.ScopeRegion,
		NativeID: "cn-hangzhou", Location: "cn-hangzhou",
	}}
	kinds := map[asset.ResourceKindID]asset.ResourceKind{
		"vpc":  {ID: "vpc", Class: "network.vpc"},
		"vsw":  {ID: "vsw", Class: "network.subnet"},
		"ecs":  {ID: "ecs", Class: "compute.instance"},
		"disk": {ID: "disk", Class: "storage.block"},
		"sg":   {ID: "sg", Class: "network.security_group"},
	}
	assets := []asset.Asset{
		{
			ID: "vpc", Identity: asset.Identity{ConnectionID: connection.ID, NativeID: "vpc-a"},
			ScopeID: "region", ResourceKindID: "vpc",
			Normalized: map[string]any{topology.NormalizedVPCID: "vpc-a"},
		},
		{
			ID: "vsw", Identity: asset.Identity{ConnectionID: connection.ID, NativeID: "vsw-a"},
			ScopeID: "region", ResourceKindID: "vsw",
			Normalized: map[string]any{
				topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a",
			},
		},
		{
			ID: "vsw-b", Identity: asset.Identity{ConnectionID: connection.ID, NativeID: "vsw-b"},
			ScopeID: "region", ResourceKindID: "vsw",
			Normalized: map[string]any{
				topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-b",
			},
		},
		{
			ID: "ecs", Identity: asset.Identity{ConnectionID: connection.ID},
			ScopeID: "region", ResourceKindID: "ecs",
			Normalized: map[string]any{
				topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a",
			},
		},
		{
			ID: "ecs-b", Identity: asset.Identity{ConnectionID: connection.ID},
			ScopeID: "region", ResourceKindID: "ecs",
			Normalized: map[string]any{
				topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-b",
			},
		},
		{
			ID: "conflict-anchor-a", Identity: asset.Identity{ConnectionID: connection.ID},
			ScopeID: "region", ResourceKindID: "ecs",
			Normalized: map[string]any{
				topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-a",
			},
		},
		{
			ID: "conflict-anchor-b", Identity: asset.Identity{ConnectionID: connection.ID},
			ScopeID: "region", ResourceKindID: "ecs",
			Normalized: map[string]any{
				topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-b",
			},
		},
		{
			ID: "disk", Identity: asset.Identity{ConnectionID: connection.ID},
			ScopeID: "region", ResourceKindID: "disk",
		},
		{
			ID: "conflicting-disk", Identity: asset.Identity{ConnectionID: connection.ID},
			ScopeID: "region", ResourceKindID: "disk",
		},
		{
			ID: "security-group", Identity: asset.Identity{ConnectionID: connection.ID},
			ScopeID: "region", ResourceKindID: "sg",
		},
		{
			ID: "unknown-vswitch", Identity: asset.Identity{ConnectionID: connection.ID},
			ScopeID: "region", ResourceKindID: "disk",
			Normalized: map[string]any{
				topology.NormalizedVPCID: "vpc-a", topology.NormalizedVSwitchID: "vsw-missing",
			},
		},
	}
	relationships := []graph.Relationship{
		{ID: "disk-attachment", SourceAssetID: "disk", TargetAssetID: "ecs", Type: graph.RelationshipAttachedTo},
		{ID: "conflict-a", SourceAssetID: "conflicting-disk", TargetAssetID: "conflict-anchor-a", Type: graph.RelationshipAttachedTo},
		{ID: "conflict-b", SourceAssetID: "conflicting-disk", TargetAssetID: "conflict-anchor-b", Type: graph.RelationshipAttachedTo},
		{ID: "shared-security-group", SourceAssetID: "ecs", TargetAssetID: "security-group", Type: graph.RelationshipUses},
	}
	projected, err := topology.Project(topology.Input{
		Connection: connection, Scopes: scopes, Assets: assets, Kinds: kinds,
		Relationships: relationships,
		Focus: topology.Focus{
			Kind: topology.FocusVPC, RegionID: "cn-hangzhou", VPCID: "vpc-a",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	view := projected.View.(topology.VPCView)
	if !slices.ContainsFunc(view.Resources, func(value topology.Resource) bool {
		return value.AssetID == "disk" && !value.MembershipUnknown
	}) {
		t.Fatalf("attached disk is not a certain VPC canvas member: %+v", view.Resources)
	}
	if slices.ContainsFunc(view.Resources, func(value topology.Resource) bool {
		return value.AssetID == "security-group"
	}) {
		t.Fatalf("shared security group inherited through uses: %+v", view.Resources)
	}
	if !slices.ContainsFunc(view.Resources, func(value topology.Resource) bool {
		return value.AssetID == "conflicting-disk" && value.MembershipUnknown
	}) {
		t.Fatalf("conflicting disk did not fail closed on the VPC canvas: %+v", view.Resources)
	}

	selection, err := cleanup.ExpandSelectors(cleanup.SelectionInput{
		Selectors: []plan.CleanupSelector{{
			Kind: plan.SelectorGroup, ConnectionID: connection.ID,
			GroupKey: topology.VPCFocusKey("cn-hangzhou", "vpc-a"),
		}},
		Connections: []asset.CloudConnection{connection}, Scopes: scopes,
		Assets: assets, Kinds: kinds, Relationships: relationships,
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := []asset.AssetID{
		"conflict-anchor-a", "conflict-anchor-b", "disk", "ecs", "ecs-b", "vpc", "vsw", "vsw-b",
	}; !reflect.DeepEqual(selection.AssetIDs, want) {
		t.Fatalf("selected IDs = %v, want %v", selection.AssetIDs, want)
	}
}

func TestExpandSelectorsVPCRecognizesOldBoundaryFromKindAndNativeIdentity(t *testing.T) {
	t.Parallel()

	groupKey := topology.VPCFocusKey("cn-hangzhou", "vpc-old")
	result, err := cleanup.ExpandSelectors(cleanup.SelectionInput{
		Selectors: []plan.CleanupSelector{{
			Kind: plan.SelectorGroup, ConnectionID: "connection", GroupKey: groupKey,
		}},
		Scopes: []asset.Scope{{
			ID: "region", ConnectionID: "connection", Kind: asset.ScopeRegion,
			NativeID: "cn-hangzhou", Location: "cn-hangzhou",
		}},
		Kinds: map[asset.ResourceKindID]asset.ResourceKind{
			"kind-vpc":      {ID: "kind-vpc", Class: "network.vpc"},
			"kind-instance": {ID: "kind-instance", Class: "compute.instance"},
			"kind-other":    {ID: "kind-other", Class: "other"},
		},
		Assets: []asset.Asset{
			{
				ID: "old-vpc", Identity: asset.Identity{ConnectionID: "connection", NativeID: "vpc-old"},
				ResourceKindID: "kind-vpc", Name: "Old production VPC", ScopeID: "region",
			},
			{
				ID: "child", Identity: asset.Identity{ConnectionID: "connection", NativeID: "i-a"},
				ResourceKindID: "kind-instance", ScopeID: "region",
				Normalized: map[string]any{topology.NormalizedVPCID: "vpc-old"},
			},
			{
				ID: "same-native-non-vpc", Identity: asset.Identity{ConnectionID: "connection", NativeID: "vpc-old"},
				ResourceKindID: "kind-other", Name: "Wrong display name", ScopeID: "region",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.AssetIDs, []asset.AssetID{"child", "old-vpc"}) {
		t.Fatalf("resolved IDs = %v", result.AssetIDs)
	}
	if result.Selectors[0].DisplayName != "Old production VPC" {
		t.Fatalf("selector = %+v", result.Selectors[0])
	}
}

func TestExpandSelectorsIncludesVPCInterconnectsForEitherEndpointVPC(t *testing.T) {
	t.Parallel()

	connection := asset.CloudConnection{ID: "connection"}
	scopes := []asset.Scope{
		{ID: "hangzhou", ConnectionID: connection.ID, Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		{ID: "shanghai", ConnectionID: connection.ID, Kind: asset.ScopeRegion, NativeID: "cn-shanghai"},
	}
	kinds := map[asset.ResourceKindID]asset.ResourceKind{
		"vpc":              {ID: "vpc", Class: "network.vpc"},
		"peer":             {ID: "peer", Class: "network.peer_connection"},
		"router-interface": {ID: "router-interface", Class: "network.peer_connection"},
	}
	assets := []asset.Asset{
		{ID: "requester", Identity: asset.Identity{ConnectionID: connection.ID, NativeID: "vpc-requester"}, ScopeID: "hangzhou", ResourceKindID: "vpc"},
		{ID: "accepter", Identity: asset.Identity{ConnectionID: connection.ID, NativeID: "vpc-accepter"}, ScopeID: "shanghai", ResourceKindID: "vpc"},
		{
			ID: "peer-connection", Identity: asset.Identity{ConnectionID: connection.ID, NativeID: "pcc-a"},
			ScopeID: "hangzhou", ResourceKindID: "peer",
			Normalized: map[string]any{topology.NormalizedVPCID: "vpc-requester"},
		},
		{
			ID: "router-interface", Identity: asset.Identity{
				ConnectionID: connection.ID, NativeID: "ri-bp1kbigq0y1gw1qerdswm",
			},
			ScopeID: "hangzhou", ResourceKindID: "router-interface",
			Normalized: map[string]any{topology.NormalizedVPCID: "vpc-requester"},
		},
	}
	relationships := []graph.Relationship{
		{ID: "peer-requester", SourceAssetID: "peer-connection", TargetAssetID: "requester", Type: graph.RelationshipMemberOf},
		{ID: "peer-accepter", SourceAssetID: "peer-connection", TargetAssetID: "accepter", Type: graph.RelationshipMemberOf},
		{ID: "router-interface-requester", SourceAssetID: "router-interface", TargetAssetID: "requester", Type: graph.RelationshipMemberOf},
		{ID: "router-interface-accepter", SourceAssetID: "router-interface", TargetAssetID: "accepter", Type: graph.RelationshipMemberOf},
	}

	for _, test := range []struct {
		name     string
		regionID string
		vpcID    string
		want     []asset.AssetID
	}{
		{name: "requester", regionID: "cn-hangzhou", vpcID: "vpc-requester", want: []asset.AssetID{"peer-connection", "requester", "router-interface"}},
		{name: "cross-region accepter", regionID: "cn-shanghai", vpcID: "vpc-accepter", want: []asset.AssetID{"accepter", "peer-connection", "router-interface"}},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := cleanup.ExpandSelectors(cleanup.SelectionInput{
				Selectors: []plan.CleanupSelector{{
					Kind: plan.SelectorGroup, ConnectionID: connection.ID,
					GroupKey: topology.VPCFocusKey(test.regionID, test.vpcID),
				}},
				Connections: []asset.CloudConnection{connection}, Scopes: scopes,
				Assets: assets, Kinds: kinds, Relationships: relationships,
			})
			if err != nil || !reflect.DeepEqual(result.AssetIDs, test.want) {
				t.Fatalf("selection=%+v err=%v, want=%v", result, err, test.want)
			}
		})
	}
}

func TestExpandSelectorsVPCRecognizesOldEmptyBoundary(t *testing.T) {
	t.Parallel()

	result, err := cleanup.ExpandSelectors(cleanup.SelectionInput{
		Selectors: []plan.CleanupSelector{{
			Kind: plan.SelectorGroup, ConnectionID: "connection",
			GroupKey: topology.VPCFocusKey("cn-hangzhou", "vpc-empty"),
		}},
		Scopes: []asset.Scope{{
			ID: "region", ConnectionID: "connection", Kind: asset.ScopeRegion,
			NativeID: "cn-hangzhou", Location: "cn-hangzhou",
		}},
		Kinds: map[asset.ResourceKindID]asset.ResourceKind{
			"kind-vpc": {ID: "kind-vpc", Class: "network.vpc"},
		},
		Assets: []asset.Asset{{
			ID: "old-empty-vpc", Identity: asset.Identity{ConnectionID: "connection", NativeID: "vpc-empty"},
			ResourceKindID: "kind-vpc", Name: "Empty VPC", ScopeID: "region",
		}},
	})
	if err != nil || !reflect.DeepEqual(result.AssetIDs, []asset.AssetID{"old-empty-vpc"}) {
		t.Fatalf("result = %+v, err = %v", result, err)
	}
}

func TestExpandSelectorsVPCRejectsAssetsWithoutAuthoritativeRegionScope(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		scopeID asset.ScopeID
		scopes  []asset.Scope
	}{
		{name: "empty scope"},
		{name: "missing scope", scopeID: "missing"},
		{
			name: "cyclic scope", scopeID: "cycle-a",
			scopes: []asset.Scope{
				{ID: "cycle-a", ConnectionID: "connection", ParentID: "cycle-b", Kind: asset.ScopeZone},
				{ID: "cycle-b", ConnectionID: "connection", ParentID: "cycle-a", Kind: asset.ScopeResourceGroup},
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			result, err := cleanup.ExpandSelectors(cleanup.SelectionInput{
				Selectors: []plan.CleanupSelector{{
					Kind: plan.SelectorGroup, ConnectionID: "connection",
					GroupKey: topology.VPCFocusKey("cn-hangzhou", "vpc-orphan"),
				}},
				Scopes: test.scopes,
				Kinds: map[asset.ResourceKindID]asset.ResourceKind{
					"kind-vpc": {ID: "kind-vpc", Class: "network.vpc"},
				},
				Assets: []asset.Asset{
					{
						ID: "orphan-vpc", Identity: asset.Identity{ConnectionID: "connection", NativeID: "vpc-orphan"},
						ScopeID: test.scopeID, ResourceKindID: "kind-vpc", Location: "cn-hangzhou",
					},
					{
						ID: "orphan-child", Identity: asset.Identity{ConnectionID: "connection", NativeID: "i-orphan"},
						ScopeID: test.scopeID, Location: "cn-hangzhou",
						Normalized: map[string]any{topology.NormalizedVPCID: "vpc-orphan"},
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(result.AssetIDs) != 0 {
				t.Fatalf("orphan assets selected through Location fallback: %+v", result)
			}
		})
	}
}

func TestProjectedScopeSummariesRoundTripWithoutBroadening(t *testing.T) {
	t.Parallel()

	assets := []asset.Asset{
		{ID: "global-only", Identity: asset.Identity{ConnectionID: "connection"}, ScopeID: "global"},
		{ID: "region-root", Identity: asset.Identity{ConnectionID: "connection"}, ScopeID: "region", Location: "cn-hangzhou"},
		{ID: "region-child", Identity: asset.Identity{ConnectionID: "connection"}, ScopeID: "zone", Location: "cn-hangzhou"},
	}
	scopes := []asset.Scope{
		{ID: "account", ConnectionID: "connection", Kind: asset.ScopeAccount},
		{ID: "global", ConnectionID: "connection", ParentID: "account", Kind: asset.ScopeGlobal},
		{ID: "region", ConnectionID: "connection", ParentID: "account", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Location: "cn-hangzhou"},
		{ID: "zone", ConnectionID: "connection", ParentID: "region", Kind: asset.ScopeZone, NativeID: "cn-hangzhou-h", Location: "cn-hangzhou"},
	}
	projected, err := topology.Project(topology.Input{
		Connection: asset.CloudConnection{ID: "connection"},
		Regions: []asset.ConnectionRegion{{
			ID: "region-record", ConnectionID: "connection", RegionID: "cn-hangzhou", Lifecycle: asset.RegionActive,
		}},
		Scopes: scopes, Assets: assets,
	})
	if err != nil {
		t.Fatal(err)
	}
	account := projected.View.(topology.AccountView)
	for _, test := range []struct {
		summary topology.EntrySummary
		want    []asset.AssetID
	}{
		{summary: *account.GlobalResources, want: []asset.AssetID{"global-only"}},
		{summary: account.Regions[0], want: []asset.AssetID{"region-child", "region-root"}},
	} {
		selection, err := cleanup.ExpandSelectors(cleanup.SelectionInput{
			Selectors: []plan.CleanupSelector{{
				Kind:    plan.SelectorKind(test.summary.Cleanup.SelectorKind),
				ScopeID: asset.ScopeID(test.summary.Cleanup.SelectorKey), Descendants: true,
			}},
			Connections: []asset.CloudConnection{{ID: "connection"}}, Scopes: scopes, Assets: assets,
		})
		if err != nil || !reflect.DeepEqual(selection.AssetIDs, test.want) {
			t.Fatalf("summary=%+v selection=%+v err=%v", test.summary, selection, err)
		}
	}
}

func TestExpandSelectorsVPCRejectsMalformedOrWrongConnection(t *testing.T) {
	t.Parallel()

	for _, selector := range []plan.CleanupSelector{
		{Kind: plan.SelectorGroup, ConnectionID: "connection", GroupKey: topology.RegionFocusKey("cn-hangzhou")},
		{Kind: plan.SelectorGroup, ConnectionID: "other", GroupKey: topology.VPCFocusKey("cn-hangzhou", "vpc-1")},
	} {
		_, err := cleanup.ExpandSelectors(cleanup.SelectionInput{
			Selectors:   []plan.CleanupSelector{selector},
			Connections: []asset.CloudConnection{{ID: "connection"}},
			Assets: []asset.Asset{{
				ID: "instance", Identity: asset.Identity{ConnectionID: "connection"}, Location: "cn-hangzhou",
				Normalized: map[string]any{topology.NormalizedVPCID: "vpc-1"},
			}},
		})
		if err == nil {
			t.Fatalf("selector %+v succeeded", selector)
		}
	}
}

func TestExpandSelectorsVPCClearsClientSuppliedDerivedFields(t *testing.T) {
	t.Parallel()

	result, err := cleanup.ExpandSelectors(cleanup.SelectionInput{
		Selectors: []plan.CleanupSelector{{
			Kind: plan.SelectorGroup, ConnectionID: "connection",
			GroupKey: topology.VPCFocusKey("cn-hangzhou", "vpc-a"),
			ScopeID:  "forged", ScopeKind: asset.ScopeAccount, Descendants: true, DisplayName: "forged",
		}},
		Connections: []asset.CloudConnection{{ID: "connection"}},
		Scopes: []asset.Scope{{
			ID: "region", ConnectionID: "connection", Kind: asset.ScopeRegion,
			NativeID: "cn-hangzhou", Location: "cn-hangzhou",
		}},
		Kinds: map[asset.ResourceKindID]asset.ResourceKind{
			"vpc": {ID: "vpc", Class: "network.vpc"},
		},
		Assets: []asset.Asset{{
			ID: "vpc", Identity: asset.Identity{ConnectionID: "connection", NativeID: "vpc-a"},
			ResourceKindID: "vpc", Name: "Production", ScopeID: "region", Location: "cn-hangzhou",
			Normalized: map[string]any{topology.NormalizedVPCID: "vpc-a"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	selector := result.Selectors[0]
	if selector.ScopeID != "region" || selector.ScopeKind != asset.ScopeRegion ||
		selector.Descendants || selector.DisplayName != "Production" {
		t.Fatalf("canonical selector = %+v", selector)
	}
}
