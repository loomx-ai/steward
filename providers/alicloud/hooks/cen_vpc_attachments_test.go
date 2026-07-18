package hooks_test

import (
	"context"
	"reflect"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/providers/alicloud"
	"github.com/loomx-ai/steward/providers/alicloud/hooks"
)

func TestCENVPCAttachmentsOwnManagedENIsFromStructuredZoneMappings(t *testing.T) {
	t.Parallel()

	attachment := asset.Asset{
		ID: "attachment", ScopeID: "scope-global", Location: "ap-northeast-2",
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-a",
			NativeType: alicloud.CENTransitRouterVPCAttachmentNativeType,
			NativeID:   "tr-attach-b9owgteq5t2u1izyqx", ScopeKey: "region:ap-northeast-2",
		},
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable},
		Normalized: map[string]any{
			alicloud.NormalizedCENInstanceIDField:      "cen-b46rksvi4fcjop46b6",
			alicloud.NormalizedCENTransitRouterIDField: "tr-mj7f5z1btac2s1x4jusp5",
			alicloud.NormalizedCENVPCIDField:           "vpc-mj7lo1vl92lfmqi1t7ooo",
			alicloud.NormalizedCENRegionIDField:        "ap-northeast-2",
			alicloud.NormalizedCENZoneMappingsField: []any{
				map[string]any{
					"ZoneId": "ap-northeast-2a", "VSwitchId": "vsw-mj7im2qllrgh20cirb8rh",
					"NetworkInterfaceId": "eni-mj7bdaysvwp3epwuh1bh",
				},
			},
		},
	}
	cen := asset.Asset{
		ID: "cen", ScopeID: "scope-global",
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-a",
			NativeType: alicloud.CENInstanceNativeType,
			NativeID:   "cen-b46rksvi4fcjop46b6", ScopeKey: "account/global",
		},
	}
	transitRouter := asset.Asset{
		ID: "transit-router", ScopeID: "scope-global", Location: "ap-northeast-2",
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-a",
			NativeType: alicloud.CENTransitRouterNativeType,
			NativeID:   "tr-mj7f5z1btac2s1x4jusp5", ScopeKey: "account/global",
		},
	}
	managedENI := dataWorksAsset(
		"managed-eni",
		"ACS::ECS::NetworkInterface",
		"eni-mj7bdaysvwp3epwuh1bh",
		"Interface for ntr-ap-northeast-2 tr-attach-b9owgteq5t2u1izyqx",
		map[string]any{
			"vpc_id": "vpc-mj7lo1vl92lfmqi1t7ooo", "vswitch_id": "vsw-mj7im2qllrgh20cirb8rh",
		},
	)
	nameOnlyENI := dataWorksAsset(
		"name-only-eni",
		"ACS::ECS::NetworkInterface",
		"eni-name-only",
		"Interface for ntr-ap-northeast-2 tr-attach-b9owgteq5t2u1izyqx",
		nil,
	)
	vpc := dataWorksAsset(
		"vpc",
		"ACS::VPC::VPC",
		"vpc-mj7lo1vl92lfmqi1t7ooo",
		"seoul-vpc",
		nil,
	)
	assets := []asset.Asset{managedENI, nameOnlyENI, attachment, cen, transitRouter, vpc}

	contribution, err := hooks.NewCENVPCAttachments().Contribute(
		context.Background(),
		"scope-account",
		assets,
	)
	if err != nil {
		t.Fatalf("build CEN VPC attachment topology: %v", err)
	}
	if len(contribution.Unresolved) != 0 ||
		len(contribution.Relationships) != 3 ||
		len(contribution.Bindings) != 1 {
		t.Fatalf("CEN VPC attachment contribution = %+v", contribution)
	}
	relationships := map[string]graph.Relationship{}
	for _, relationship := range contribution.Relationships {
		key := string(relationship.SourceAssetID) + "|" + string(relationship.TargetAssetID)
		relationships[key] = relationship
	}
	for _, key := range []string{
		"attachment|transit-router",
		"managed-eni|attachment",
	} {
		relationship, found := relationships[key]
		if !found ||
			relationship.Type != graph.RelationshipMemberOf ||
			relationship.Source != "cen:ListTransitRouterVpcAttachments" {
			t.Fatalf("CEN VPC attachment relationship %q = %+v found=%t", key, relationship, found)
		}
	}
	if _, found := relationships["attachment|cen"]; found {
		t.Fatalf("CEN VPC attachment has a redundant direct CEN parent")
	}
	if relationship := relationships["attachment|vpc"]; relationship.Type != graph.RelationshipAttachedTo ||
		relationship.Source != "cen:ListTransitRouterVpcAttachments" {
		t.Fatalf("CEN VPC attachment to VPC relationship = %+v", relationship)
	}
	binding := contribution.Bindings[0]
	if binding.ControllerAssetID != attachment.ID ||
		binding.ManagedAssetID != managedENI.ID ||
		binding.Authority != graph.AuthorityAuthoritative ||
		binding.Ownership != graph.OwnershipExclusive ||
		binding.CleanupPolicy != graph.CleanupDelegate ||
		binding.DirectCleanupAllowed ||
		binding.Confidence != 1 ||
		binding.Evidence["lifecycle_kind"] != "cen_transit_router_vpc_attachment" ||
		binding.Evidence["transit_router_attachment_id"] != "tr-attach-b9owgteq5t2u1izyqx" ||
		binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true {
		t.Fatalf("CEN VPC attachment binding = %+v", binding)
	}

	direct, err := plan.Solve(plan.Input{
		ResolvedAssetIDs:  []asset.AssetID{managedENI.ID},
		Assets:            assets,
		Relationships:     contribution.Relationships,
		LifecycleBindings: contribution.Bindings,
	})
	if err != nil {
		t.Fatalf("plan direct CEN-managed ENI cleanup: %v", err)
	}
	if len(direct.Steps) != 0 ||
		len(direct.Blockers) != 1 ||
		direct.Blockers[0].Code != plan.BlockManagedByController ||
		direct.Blockers[0].ControllerID != attachment.ID ||
		!reflect.DeepEqual(
			direct.Blockers[0].Evidence["suggested_asset_ids"],
			[]asset.AssetID{attachment.ID},
		) {
		t.Fatalf("direct CEN-managed ENI cleanup plan = %+v", direct)
	}

	withSource, err := plan.Solve(plan.Input{
		ResolvedAssetIDs:  []asset.AssetID{managedENI.ID, attachment.ID},
		Assets:            assets,
		Relationships:     contribution.Relationships,
		LifecycleBindings: contribution.Bindings,
	})
	if err != nil {
		t.Fatalf("plan CEN VPC attachment cleanup: %v", err)
	}
	if len(withSource.Blockers) != 0 ||
		len(withSource.Steps) != 2 ||
		len(withSource.ImpactItems) != 1 ||
		withSource.ImpactItems[0].AssetID != managedENI.ID {
		t.Fatalf("CEN VPC attachment cleanup plan = %+v", withSource)
	}
	controller := requireCleanupStepForAsset(t, withSource.Steps, attachment.ID)
	verification := requireCleanupStepForAsset(t, withSource.Steps, managedENI.ID)
	if controller.Kind != plan.StepController ||
		verification.Kind != plan.StepVerification ||
		verification.Action != plan.ActionVerifyManagedAbsent ||
		len(verification.DependsOn) != 1 ||
		verification.DependsOn[0] != controller.ID {
		t.Fatalf(
			"CEN VPC attachment steps: controller=%+v verification=%+v",
			controller,
			verification,
		)
	}
}

func TestCENVPCAttachmentsReportMissingInventoryENI(t *testing.T) {
	t.Parallel()

	attachment := asset.Asset{
		ID: "attachment",
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-a",
			NativeType: alicloud.CENTransitRouterVPCAttachmentNativeType,
			NativeID:   "tr-attach-a",
		},
		Normalized: map[string]any{
			alicloud.NormalizedCENZoneMappingsField: []any{
				map[string]any{"NetworkInterfaceId": "eni-missing"},
			},
		},
	}
	contribution, err := hooks.NewCENVPCAttachments().Contribute(
		context.Background(),
		"scope-account",
		[]asset.Asset{attachment},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Relationships) != 0 ||
		len(contribution.Bindings) != 0 ||
		len(contribution.Unresolved) != 1 ||
		contribution.Unresolved[0].NativeID != "eni-missing" ||
		contribution.Unresolved[0].ControllerID != attachment.ID {
		t.Fatalf("missing CEN ENI contribution = %+v", contribution)
	}
}

func TestCENTopologyConnectsRouteTablesAcrossResourceCenterScopes(t *testing.T) {
	t.Parallel()

	identity := func(nativeType, nativeID string) asset.Identity {
		return asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-a",
			NativeType: nativeType, NativeID: nativeID, ScopeKey: "region:cn-hangzhou",
		}
	}
	cen := asset.Asset{
		ID: "cen",
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-a",
			NativeType: alicloud.CENInstanceNativeType, NativeID: "cen-a",
			ScopeKey: "account/global",
		},
	}
	transitRouter := asset.Asset{
		ID: "transit-router",
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-a",
			NativeType: alicloud.CENTransitRouterNativeType, NativeID: "tr-a",
			ScopeKey: "account/global",
		},
	}
	vpc := asset.Asset{ID: "vpc", Identity: identity("ACS::VPC::VPC", "vpc-a")}
	prefixList := asset.Asset{
		ID: "prefix-list", Identity: identity("ACS::ECS::PrefixList", "pl-a"),
	}
	bandwidthPackage := asset.Asset{
		ID: "bandwidth-package",
		Identity: identity(
			alicloud.CENBandwidthPackageNativeType,
			"cenbwp-8hfrdbedeom4q3sei4",
		),
		Normalized: map[string]any{
			"configuration": map[string]any{
				"CenIds": []any{"cen-a"},
			},
		},
	}
	attachment := asset.Asset{
		ID: "attachment",
		Identity: identity(
			alicloud.CENTransitRouterVPCAttachmentNativeType,
			"tr-attach-a",
		),
		Normalized: map[string]any{
			alicloud.NormalizedCENInstanceIDField:                "cen-a",
			alicloud.NormalizedCENTransitRouterIDField:           "tr-a",
			alicloud.NormalizedCENTransitRouterAttachmentIDField: "tr-attach-a",
			alicloud.NormalizedCENVPCIDField:                     "vpc-a",
		},
	}
	routeTable := asset.Asset{
		ID:       "route-table",
		Identity: identity(alicloud.CENTransitRouterRouteTableNativeType, "vtb-a"),
		Normalized: map[string]any{
			alicloud.NormalizedCENInstanceIDField:      "cen-a",
			alicloud.NormalizedCENTransitRouterIDField: "tr-a",
			alicloud.NormalizedCENRouteTableAssociationsField: []any{map[string]any{
				"TransitRouterAttachmentId": "tr-attach-a", "ResourceType": "VPC",
			}},
			alicloud.NormalizedCENRouteTablePropagationsField: []any{map[string]any{
				"TransitRouterAttachmentId": "tr-attach-a", "ResourceType": "VPC",
			}},
			alicloud.NormalizedCENRouteEntriesField: []any{map[string]any{
				"TransitRouterRouteEntryDestinationCidrBlock": "10.0.0.0/8",
				"TransitRouterRouteEntryNextHopId":            "tr-attach-a",
			}},
			alicloud.NormalizedCENPrefixListAssociationsField: []any{map[string]any{
				"PrefixListId": "pl-a", "NextHop": "tr-attach-a",
			}},
		},
	}

	contribution, err := hooks.NewCENVPCAttachments().Contribute(
		context.Background(),
		"scope-account",
		[]asset.Asset{
			cen, transitRouter, vpc, prefixList, bandwidthPackage, attachment, routeTable,
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Unresolved) != 0 {
		t.Fatalf("CEN route topology unresolved = %+v", contribution.Unresolved)
	}
	got := map[string]bool{}
	for _, relationship := range contribution.Relationships {
		key := string(relationship.SourceAssetID) + "|" +
			string(relationship.TargetAssetID) + "|" +
			string(relationship.Type)
		got[key] = true
	}
	for _, key := range []string{
		"attachment|transit-router|member_of",
		"attachment|vpc|attached_to",
		"route-table|transit-router|member_of",
		"attachment|route-table|attached_to",
		"attachment|route-table|routes_to",
		"route-table|attachment|routes_to",
		"route-table|prefix-list|uses",
		"bandwidth-package|cen|attached_to",
	} {
		if !got[key] {
			t.Errorf("CEN relationship %q is missing: %+v", key, contribution.Relationships)
		}
	}
	for _, key := range []string{
		"attachment|cen|member_of",
		"route-table|cen|member_of",
	} {
		if got[key] {
			t.Errorf("CEN relationship %q bypasses the immediate parent", key)
		}
	}
}

func TestCENTopologyConnectsResourceCenterBandwidthPackageCENIDs(t *testing.T) {
	t.Parallel()

	identity := func(nativeType, nativeID string) asset.Identity {
		return asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public",
			ConnectionID: "connection-a", NativeType: nativeType, NativeID: nativeID,
		}
	}
	bandwidthPackage := asset.Asset{
		ID: "bandwidth-package",
		Identity: identity(
			alicloud.CENBandwidthPackageNativeType,
			"cenbwp-8hfrdbedeom4q3sei4",
		),
		Normalized: map[string]any{
			"configuration": map[string]any{
				"CenIds": []any{"cen-v3amwa41xf5k3shiwg"},
			},
		},
	}
	cen := asset.Asset{
		ID: "cen",
		Identity: identity(
			alicloud.CENInstanceNativeType,
			"cen-v3amwa41xf5k3shiwg",
		),
	}

	contribution, err := hooks.NewCENTopology().Contribute(
		context.Background(),
		"scope-account",
		[]asset.Asset{bandwidthPackage, cen},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Unresolved) != 0 {
		t.Fatalf("CEN bandwidth package topology unresolved = %+v", contribution.Unresolved)
	}
	if len(contribution.Relationships) != 1 {
		t.Fatalf("CEN bandwidth package relationships = %+v", contribution.Relationships)
	}
	relationship := contribution.Relationships[0]
	if relationship.SourceAssetID != bandwidthPackage.ID ||
		relationship.TargetAssetID != cen.ID ||
		relationship.Type != graph.RelationshipAttachedTo {
		t.Fatalf("CEN bandwidth package relationship = %+v", relationship)
	}
}

func TestCENTopologyUsesOnlyTheImmediateContainmentParent(t *testing.T) {
	t.Parallel()

	identity := func(nativeType, nativeID string) asset.Identity {
		return asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public",
			ConnectionID: "connection-a", NativeType: nativeType, NativeID: nativeID,
		}
	}
	cen := asset.Asset{
		ID: "cen",
		Identity: identity(
			alicloud.CENInstanceNativeType,
			"cen-a",
		),
	}
	transitRouter := asset.Asset{
		ID:       "transit-router",
		Identity: identity(alicloud.CENTransitRouterNativeType, "tr-a"),
		Normalized: map[string]any{
			alicloud.NormalizedCENInstanceIDField: "cen-a",
		},
	}
	routeTable := asset.Asset{
		ID:       "route-table",
		Identity: identity(alicloud.CENTransitRouterRouteTableNativeType, "vtb-a"),
		Normalized: map[string]any{
			alicloud.NormalizedCENInstanceIDField:      "cen-a",
			alicloud.NormalizedCENTransitRouterIDField: "tr-a",
		},
	}
	routeMap := asset.Asset{
		ID:       "route-map",
		Identity: identity(alicloud.CENRouteMapNativeType, "cenrmap-a"),
		Normalized: map[string]any{
			alicloud.NormalizedCENInstanceIDField:                "cen-a",
			alicloud.NormalizedCENTransitRouterIDField:           "tr-a",
			alicloud.NormalizedCENTransitRouterRouteTableIDField: "vtb-a",
		},
	}

	contribution, err := hooks.NewCENTopology().Contribute(
		context.Background(),
		"scope-account",
		[]asset.Asset{cen, transitRouter, routeTable, routeMap},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Unresolved) != 0 {
		t.Fatalf("CEN containment unresolved = %+v", contribution.Unresolved)
	}
	got := map[string]graph.RelationshipType{}
	for _, relationship := range contribution.Relationships {
		got[string(relationship.SourceAssetID)+"|"+string(relationship.TargetAssetID)] =
			relationship.Type
	}
	want := map[string]graph.RelationshipType{
		"transit-router|cen":         graph.RelationshipMemberOf,
		"route-table|transit-router": graph.RelationshipMemberOf,
		"route-map|route-table":      graph.RelationshipMemberOf,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("CEN immediate containment = %+v, want %+v", got, want)
	}
}

func TestCENTopologyDelegatesSystemRouteTableAndRouteMapToTransitRouter(t *testing.T) {
	t.Parallel()

	identity := func(nativeType, nativeID string) asset.Identity {
		return asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-a",
			NativeType: nativeType, NativeID: nativeID,
		}
	}
	transitRouter := asset.Asset{
		ID: "transit-router", Identity: identity(alicloud.CENTransitRouterNativeType, "tr-a"),
		Capabilities: asset.CapabilitySet{asset.CapabilityActionable},
	}
	systemRouteTable := asset.Asset{
		ID:       "system-route-table",
		Identity: identity(alicloud.CENTransitRouterRouteTableNativeType, "vtb-system"),
		Normalized: map[string]any{
			alicloud.NormalizedCENTransitRouterIDField: "tr-a",
			"routeTableType": "System",
		},
	}
	systemRouteMap := asset.Asset{
		ID:       "system-route-map",
		Identity: identity(alicloud.CENRouteMapNativeType, "cenrmap-system"),
		Normalized: map[string]any{
			alicloud.NormalizedCENTransitRouterIDField:           "tr-a",
			alicloud.NormalizedCENTransitRouterRouteTableIDField: "vtb-system",
			"priority": 5000,
		},
	}
	customRouteTable := asset.Asset{
		ID:       "custom-route-table",
		Identity: identity(alicloud.CENTransitRouterRouteTableNativeType, "vtb-custom"),
		Normalized: map[string]any{
			alicloud.NormalizedCENTransitRouterIDField: "tr-a",
			"routeTableType": "Custom",
		},
	}

	assets := []asset.Asset{
		transitRouter,
		systemRouteTable,
		systemRouteMap,
		customRouteTable,
	}
	contribution, err := hooks.NewCENTopology().Contribute(
		context.Background(),
		"scope-account",
		assets,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 2 {
		t.Fatalf("bindings=%+v", contribution.Bindings)
	}
	bindings := map[asset.AssetID]graph.LifecycleBinding{}
	for _, binding := range contribution.Bindings {
		bindings[binding.ManagedAssetID] = binding
	}
	routeTableBinding := bindings[systemRouteTable.ID]
	if routeTableBinding.ControllerAssetID != transitRouter.ID ||
		routeTableBinding.Evidence["lifecycle_kind"] != "cen_transit_router_system_route_table" ||
		routeTableBinding.Evidence[graph.LifecycleEvidenceControllerIntegratedResource] != true ||
		routeTableBinding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true {
		t.Fatalf("system route table binding=%+v", routeTableBinding)
	}
	routeMapBinding := bindings[systemRouteMap.ID]
	if routeMapBinding.ControllerAssetID != transitRouter.ID ||
		routeMapBinding.Evidence["lifecycle_kind"] != "cen_system_route_map" ||
		routeMapBinding.Evidence[graph.LifecycleEvidenceUnselectedControllerAction] !=
			graph.LifecycleUnselectedControllerSkip ||
		routeMapBinding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true {
		t.Fatalf("system route map binding=%+v", routeMapBinding)
	}

	solved, err := plan.Solve(plan.Input{
		ResolvedAssetIDs:  []asset.AssetID{transitRouter.ID},
		Assets:            assets,
		Relationships:     contribution.Relationships,
		LifecycleBindings: contribution.Bindings,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(solved.Blockers) != 0 ||
		len(solved.Steps) != 2 ||
		len(solved.ImpactItems) != 2 {
		t.Fatalf("transit router cleanup plan=%+v", solved)
	}
	controller := requireCleanupStepForAsset(t, solved.Steps, transitRouter.ID)
	impacts := map[asset.AssetID]plan.ImpactItem{}
	for _, impact := range solved.ImpactItems {
		impacts[impact.AssetID] = impact
	}
	for _, managedID := range []asset.AssetID{systemRouteTable.ID, systemRouteMap.ID} {
		if impacts[managedID].Expected != plan.ExpectedDelegatedDelete {
			t.Fatalf("impact %s=%+v", managedID, impacts[managedID])
		}
		if managedID == systemRouteTable.ID {
			if verification := cleanupStepForAssetIfPresent(
				solved.Steps,
				managedID,
			); verification.ID != "" {
				t.Fatalf("system route table must not have a verification step: %+v", verification)
			}
			continue
		}
		verification := requireCleanupStepForAsset(t, solved.Steps, managedID)
		if verification.Kind != plan.StepVerification ||
			verification.Action != plan.ActionVerifyManagedAbsent ||
			len(verification.DependsOn) != 1 ||
			verification.DependsOn[0] != controller.ID {
			t.Fatalf(
				"impact %s=%+v controller=%+v verification=%+v",
				managedID,
				impacts[managedID],
				controller,
				verification,
			)
		}
	}
}

func TestCENTopologyDelegatesSystemRouteMapWithoutRouteTableAsset(t *testing.T) {
	t.Parallel()

	identity := func(nativeType, nativeID string) asset.Identity {
		return asset.Identity{
			Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "connection-a",
			NativeType: nativeType, NativeID: nativeID,
		}
	}
	transitRouter := asset.Asset{
		ID: "transit-router", Identity: identity(alicloud.CENTransitRouterNativeType, "tr-a"),
		Capabilities: asset.CapabilitySet{asset.CapabilityActionable},
	}
	systemRouteMap := asset.Asset{
		ID:       "system-route-map",
		Identity: identity(alicloud.CENRouteMapNativeType, "cenrmap-system"),
		Normalized: map[string]any{
			alicloud.NormalizedCENTransitRouterIDField:           "tr-a",
			alicloud.NormalizedCENTransitRouterRouteTableIDField: "vtb-system",
			"priority": 5000,
		},
	}

	assets := []asset.Asset{transitRouter, systemRouteMap}
	contribution, err := hooks.NewCENTopology().Contribute(
		context.Background(),
		"scope-account",
		assets,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Bindings) != 1 {
		t.Fatalf("bindings=%+v", contribution.Bindings)
	}
	binding := contribution.Bindings[0]
	if binding.ControllerAssetID != transitRouter.ID ||
		binding.ManagedAssetID != systemRouteMap.ID ||
		binding.Evidence["route_table_id"] != "vtb-system" {
		t.Fatalf("system route map binding=%+v", binding)
	}

	solved, err := plan.Solve(plan.Input{
		ResolvedAssetIDs:  []asset.AssetID{transitRouter.ID, systemRouteMap.ID},
		Assets:            assets,
		LifecycleBindings: contribution.Bindings,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(solved.Warnings) != 0 ||
		len(solved.Blockers) != 0 ||
		len(solved.Steps) != 2 ||
		len(solved.ImpactItems) != 1 ||
		solved.ImpactItems[0].AssetID != systemRouteMap.ID ||
		solved.ImpactItems[0].Expected != plan.ExpectedDelegatedDelete {
		t.Fatalf("transit router cleanup plan=%+v", solved)
	}
	controller := requireCleanupStepForAsset(t, solved.Steps, transitRouter.ID)
	verification := requireCleanupStepForAsset(t, solved.Steps, systemRouteMap.ID)
	if verification.Kind != plan.StepVerification ||
		verification.Action != plan.ActionVerifyManagedAbsent ||
		len(verification.DependsOn) != 1 ||
		verification.DependsOn[0] != controller.ID {
		t.Fatalf(
			"transit router steps: controller=%+v verification=%+v",
			controller,
			verification,
		)
	}
}
