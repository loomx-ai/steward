package inventory_test

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

func TestCreatorBuildsServerOwnedScanFromAllActiveRegions(t *testing.T) {
	repositories, creator, now := creatorFixture(t)
	created, err := creator.Create(context.Background(), inventory.ScanCreationRequest{
		ConnectionID: "connection-a", RequestedBy: "alice", RegionMode: inventory.RegionModeAllActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.ScanRun.Targets) != 2 || created.ScanRun.Targets[0].Key != "region:cn-hangzhou" || created.ScanRun.Targets[0].Kind != asset.ScanTargetRegion || created.ScanRun.Targets[0].RegionID != "cn-hangzhou" || created.ScanRun.Targets[0].RegionName != "杭州" || created.ScanRun.Targets[1].RegionID != "cn-shanghai" {
		t.Fatalf("targets = %+v", created.ScanRun.Targets)
	}
	if len(created.Shards) != 2 || len(created.Jobs) != 2 {
		t.Fatalf("creation = %+v", created)
	}
	for _, shard := range created.Shards {
		if shard.Provider != asset.ProviderAliCloud || shard.Source != "resource-center" || shard.RegionID == "" || shard.ScopeID == "" || shard.Authoritative || shard.ResourceKindID != "" {
			t.Fatalf("server-owned shard = %+v", shard)
		}
		scope, err := repositories.Inventory().GetScope(context.Background(), shard.ScopeID)
		if err != nil || scope.ParentID != "root-a" || scope.Kind != asset.ScopeRegion || scope.Location != shard.RegionID || scope.UpdatedAt != now {
			t.Fatalf("scope = %+v err=%v", scope, err)
		}
	}
}

func TestCreatorSnapshotsSelectedRegionNamesAndResourceKinds(t *testing.T) {
	repositories, creator, _ := creatorFixture(t)
	created, err := creator.Create(context.Background(), inventory.ScanCreationRequest{
		ConnectionID: "connection-a", RequestedBy: "alice", RegionMode: inventory.RegionModeSelected,
		RegionIDs: []string{"cn-shanghai", "cn-hangzhou"}, ResourceKindIDs: []asset.ResourceKindID{"alicloud:ACS::ECS::Instance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Shards) != 2 || created.ScanRun.Targets[0].RegionID != "cn-hangzhou" || created.Shards[0].ResourceKindID != "alicloud:ACS::ECS::Instance" {
		t.Fatalf("creation = %+v", created)
	}
	region, err := repositories.Regions().GetRegion(context.Background(), "region-hangzhou")
	if err != nil {
		t.Fatal(err)
	}
	region.NameOverride = "新名称"
	if err := repositories.Regions().PutRegion(context.Background(), region); err != nil {
		t.Fatal(err)
	}
	stored, err := repositories.Inventory().GetScanRun(context.Background(), created.ScanRun.ID)
	if err != nil || stored.Targets[0].RegionName != "杭州" {
		t.Fatalf("stored snapshot = %+v err=%v", stored.Targets, err)
	}
}

func TestCreatorOrdersScanRegionsByGeography(t *testing.T) {
	repositories, creator, now := creatorFixture(t)
	regionIDs := []string{
		"af-south-1",
		"me-central-1",
		"us-east-1",
		"ap-southeast-10",
		"cn-hongkong",
		"moon-1",
		"ap-northeast-1",
		"eu-central-1",
		"ap-southeast-2",
	}
	for index, regionID := range regionIDs {
		if err := repositories.Regions().PutRegion(context.Background(), asset.ConnectionRegion{
			ID:             fmt.Sprintf("region-geography-%d", index),
			ConnectionID:   "connection-a",
			RegionID:       regionID,
			DiscoveredName: regionID,
			Origin:         asset.RegionOriginAPI,
			Lifecycle:      asset.RegionActive,
			CreatedAt:      now,
			UpdatedAt:      now,
		}); err != nil {
			t.Fatal(err)
		}
	}

	created, err := creator.Create(context.Background(), inventory.ScanCreationRequest{
		ConnectionID: "connection-a",
		RequestedBy:  "alice",
		RegionMode:   inventory.RegionModeAllActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(created.ScanRun.Targets))
	for _, target := range created.ScanRun.Targets {
		if target.Kind == asset.ScanTargetRegion {
			got = append(got, target.RegionID)
		}
	}
	want := []string{
		"cn-hangzhou",
		"cn-hongkong",
		"cn-shanghai",
		"ap-northeast-1",
		"ap-southeast-2",
		"ap-southeast-10",
		"eu-central-1",
		"us-east-1",
		"me-central-1",
		"af-south-1",
		"moon-1",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("regions = %v, want %v", got, want)
	}
}

func TestCreatorUsesKindSpecificProductSourceForBroadAndSelectedScans(t *testing.T) {
	newCreator := func(t *testing.T) *inventory.Creator {
		t.Helper()
		repositories, _, now := creatorFixture(t)
		kind := asset.ResourceKind{
			ID:             "alicloud:ACS::ECS::Instance",
			Provider:       asset.ProviderAliCloud,
			NativeType:     "ACS::ECS::Instance",
			ScopeKinds:     []asset.ScopeKind{asset.ScopeRegion},
			Capabilities:   asset.CapabilitySet{asset.CapabilityIndexed},
			BundleRevision: "test",
		}
		directory := creatorDirectory{
			sources: []contracts.InventorySource{
				{
					Name:                 "product-api",
					RootScopeKinds:       []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal},
					AuthoritativeDefault: true,
					KindSpecific:         true,
				},
				{
					Name:           "resource-center",
					RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
				},
			},
			bundle: spec.Bundle{
				Provider: asset.ProviderAliCloud,
				Specs: []spec.CompiledSpec{{
					Definition: spec.ResourceKindSpec{
						Discovery: spec.DiscoverySpec{Source: "product-api"},
					},
					ResourceKind: kind,
				}},
			},
		}
		sequence := 0
		creator, err := inventory.NewCreator(
			repositories,
			directory,
			inventory.WithCreatorClock(func() time.Time { return now }),
			inventory.WithCreatorIDGenerator(func() string {
				sequence++
				return fmt.Sprintf("kind-source-%d", sequence)
			}),
		)
		if err != nil {
			t.Fatal(err)
		}
		return creator
	}

	t.Run("broad scan", func(t *testing.T) {
		created, err := newCreator(t).Create(context.Background(), inventory.ScanCreationRequest{
			ConnectionID: "connection-a",
			RequestedBy:  "alice",
			RegionMode:   inventory.RegionModeAllActive,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(created.Shards) != 4 {
			t.Fatalf("broad shards=%+v", created.Shards)
		}
		productShards := 0
		resourceCenterShards := 0
		for _, shard := range created.Shards {
			switch shard.Source {
			case "product-api":
				productShards++
				if shard.ResourceKindID != "alicloud:ACS::ECS::Instance" || !shard.Authoritative {
					t.Fatalf("broad product shard=%+v", shard)
				}
			case "resource-center":
				resourceCenterShards++
				if shard.ResourceKindID != "" || shard.Authoritative {
					t.Fatalf("broad resource center shard=%+v", shard)
				}
			default:
				t.Fatalf("unexpected broad shard=%+v", shard)
			}
		}
		if productShards != 2 || resourceCenterShards != 2 {
			t.Fatalf("broad shard counts: product=%d resource-center=%d", productShards, resourceCenterShards)
		}
	})

	t.Run("selected kind", func(t *testing.T) {
		created, err := newCreator(t).Create(context.Background(), inventory.ScanCreationRequest{
			ConnectionID:    "connection-a",
			RequestedBy:     "alice",
			RegionMode:      inventory.RegionModeSelected,
			RegionIDs:       []string{"cn-hangzhou"},
			ResourceKindIDs: []asset.ResourceKindID{"alicloud:ACS::ECS::Instance"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(created.Shards) != 1 ||
			len(created.ScanRun.Targets) != 1 ||
			created.ScanRun.Targets[0].Kind != asset.ScanTargetRegion ||
			created.Shards[0].Source != "product-api" ||
			created.Shards[0].ResourceKindID != "alicloud:ACS::ECS::Instance" {
			t.Fatalf("selected-kind shards=%+v", created.Shards)
		}
	})
}

func TestCreatorAddsGlobalTargetToAllRegionProgress(t *testing.T) {
	repositories, _, now := creatorFixture(t)
	globalKind := asset.ResourceKind{
		ID: "alicloud:ACS::CEN::CenInstance", Provider: asset.ProviderAliCloud,
		NativeType: "ACS::CEN::CenInstance", ScopeKinds: []asset.ScopeKind{asset.ScopeGlobal},
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "test",
	}
	directory := creatorDirectory{
		sources: []contracts.InventorySource{
			{Name: "resource-center", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion}},
			{
				Name: "product-api", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal},
				AuthoritativeDefault: true, KindSpecific: true,
			},
		},
		bundle: spec.Bundle{
			Provider: asset.ProviderAliCloud,
			Specs: []spec.CompiledSpec{{
				Definition:   spec.ResourceKindSpec{Discovery: spec.DiscoverySpec{Source: "product-api"}},
				ResourceKind: globalKind,
			}},
		},
	}
	creator, err := inventory.NewCreator(
		repositories,
		directory,
		inventory.WithCreatorClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}
	created, err := creator.Create(context.Background(), inventory.ScanCreationRequest{
		ConnectionID: "connection-a", RequestedBy: "alice", RegionMode: inventory.RegionModeAllActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.ScanRun.Targets) != 3 ||
		created.ScanRun.Targets[0].Kind != asset.ScanTargetGlobal ||
		created.ScanRun.Targets[0].Key != "global" ||
		len(created.Shards) != 3 ||
		len(created.Jobs) != 3 {
		t.Fatalf("creation = %+v", created)
	}
	projection := inventory.ProjectScanTask(created.ScanRun, created.Shards)
	if len(projection.TargetProgress) != 3 ||
		projection.TargetProgress[0].Kind != asset.ScanTargetGlobal ||
		projection.TargetProgress[0].RegionID != "global" ||
		projection.TargetProgress[0].Total != 1 {
		t.Fatalf("target progress = %+v", projection.TargetProgress)
	}
}

func TestCreatorBuildsOneJobPerCanonicalNetworkTarget(t *testing.T) {
	_, creator, _ := creatorFixture(t)
	created, err := creator.Create(context.Background(), inventory.ScanCreationRequest{
		ConnectionID: "connection-a", RequestedBy: "alice", ScopeMode: asset.ScanSelectedNetworks,
		NetworkTargets: []inventory.NetworkTargetRequest{
			{Kind: asset.ScanTargetVPC, RegionID: "cn-hangzhou", NativeID: "vpc-a", Name: "伪造名称"},
			{Kind: asset.ScanTargetVSwitch, RegionID: "cn-hangzhou", NativeID: "vsw-covered", ParentNativeID: "伪造父 VPC"},
			{Kind: asset.ScanTargetVSwitch, RegionID: "cn-shanghai", NativeID: "vsw-standalone", ParentNativeID: "vpc-b", Name: "伪造交换机名称"},
			{Kind: asset.ScanTargetVSwitch, RegionID: "cn-shanghai", NativeID: "vsw-standalone", ParentNativeID: "vpc-b"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.ScanRun.ScopeMode != asset.ScanSelectedNetworks || len(created.ScanRun.Targets) != 2 || len(created.Shards) != 2 || len(created.Jobs) != 2 {
		t.Fatalf("network creation = %+v", created)
	}
	if created.ScanRun.Targets[0].Kind != asset.ScanTargetVPC || created.ScanRun.Targets[0].Name != "Provider VPC A" || created.ScanRun.Targets[1].NativeID != "vsw-standalone" || created.ScanRun.Targets[1].Name != "Provider vSwitch" || created.ScanRun.Targets[1].ParentNativeID != "vpc-b" {
		t.Fatalf("canonical targets = %+v", created.ScanRun.Targets)
	}
	for _, job := range created.Jobs {
		if job.Payload["target_key"] == "" || job.Payload["scan_shard_ids"] == nil || job.Payload["scan_shard_id"] != nil {
			t.Fatalf("target job payload = %#v", job.Payload)
		}
	}
}

func TestCreatorAddsDirectNetworkProductShardsForSelectedNetworks(t *testing.T) {
	repositories, _, now := creatorFixture(t)
	resourceCenter := contracts.InventorySource{
		Name: "resource-center", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
	}
	productAPI := contracts.InventorySource{
		Name: "product-api", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
		KindSpecific: true, AuthoritativeDefault: true,
	}
	resourceKind := func(id, nativeType, class, source string) spec.CompiledSpec {
		return spec.CompiledSpec{
			Definition: spec.ResourceKindSpec{Discovery: spec.DiscoverySpec{Source: source}},
			ResourceKind: asset.ResourceKind{
				ID: asset.ResourceKindID(id), Provider: asset.ProviderAliCloud, NativeType: nativeType, Class: class,
				ScopeKinds: []asset.ScopeKind{asset.ScopeRegion}, DisplayName: nativeType,
				Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "test",
			},
		}
	}
	directory := creatorDirectory{
		sources: []contracts.InventorySource{resourceCenter, productAPI},
		bundle: spec.Bundle{Provider: asset.ProviderAliCloud, Specs: []spec.CompiledSpec{
			resourceKind("alicloud:vpc", "ACS::VPC::VPC", "network.vpc", "product-api"),
			resourceKind("alicloud:vswitch", "ACS::VPC::VSwitch", "network.subnet", "product-api"),
			resourceKind("alicloud:vpc-attachment", "ACS::CEN::TransitRouterVpcAttachment", "network.vpc_attachment", "product-api"),
			resourceKind("alicloud:ecs", "ACS::ECS::Instance", "compute.instance", "resource-center"),
		}},
	}
	sequence := 0
	creator, err := inventory.NewCreator(
		repositories,
		directory,
		inventory.WithCreatorClock(func() time.Time { return now }),
		inventory.WithCreatorIDGenerator(func() string {
			sequence++
			return fmt.Sprintf("network-product-%d", sequence)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	created, err := creator.Create(context.Background(), inventory.ScanCreationRequest{
		ConnectionID: "connection-a", RequestedBy: "alice", ScopeMode: asset.ScanSelectedNetworks,
		NetworkTargets: []inventory.NetworkTargetRequest{
			{Kind: asset.ScanTargetVPC, RegionID: "cn-hangzhou", NativeID: "vpc-a"},
			{Kind: asset.ScanTargetVSwitch, RegionID: "cn-shanghai", NativeID: "vsw-standalone", ParentNativeID: "vpc-b"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Jobs) != 2 || len(created.Shards) != 7 {
		t.Fatalf("network product creation=%+v", created)
	}
	kindsByTarget := make(map[string][]asset.ResourceKindID)
	for _, shard := range created.Shards {
		kindsByTarget[shard.TargetKey] = append(kindsByTarget[shard.TargetKey], shard.ResourceKindID)
		if !shard.Authoritative {
			t.Fatalf("selected-network shard is not authoritative: %+v", shard)
		}
	}
	if got := kindsByTarget["vpc:cn-hangzhou:vpc-a"]; !reflect.DeepEqual(
		got,
		[]asset.ResourceKindID{"", "alicloud:vpc", "alicloud:vpc-attachment", "alicloud:vswitch"},
	) {
		t.Fatalf("VPC target shard kinds=%+v", got)
	}
	if got := kindsByTarget["vswitch:cn-shanghai:vsw-standalone"]; !reflect.DeepEqual(
		got,
		[]asset.ResourceKindID{"", "alicloud:vpc-attachment", "alicloud:vswitch"},
	) {
		t.Fatalf("vSwitch target shard kinds=%+v", got)
	}
}

func TestCreatorUsesProviderSelectedResourceCenterForSupportedNetworkClosure(t *testing.T) {
	repositories, _, now := creatorFixture(t)
	resourceKind := func(id, nativeType, class string) spec.CompiledSpec {
		return spec.CompiledSpec{
			Definition: spec.ResourceKindSpec{Discovery: spec.DiscoverySpec{Source: "product-api"}},
			ResourceKind: asset.ResourceKind{
				ID: asset.ResourceKindID(id), Provider: asset.ProviderAliCloud, NativeType: nativeType, Class: class,
				ScopeKinds: []asset.ScopeKind{asset.ScopeRegion}, DisplayName: nativeType,
				Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "test",
			},
		}
	}
	directory := sourceSelectingCreatorDirectory{creatorDirectory: creatorDirectory{
		sources: []contracts.InventorySource{
			{
				Name: "resource-center", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
				KindSpecific: true, AuthoritativeDefault: true, NetworkClosure: true,
			},
			{
				Name: "product-api", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
				KindSpecific: true, AuthoritativeDefault: true,
			},
		},
		bundle: spec.Bundle{Provider: asset.ProviderAliCloud, Specs: []spec.CompiledSpec{
			resourceKind("alicloud:vpc", "ACS::VPC::VPC", "network.vpc"),
			resourceKind("alicloud:vswitch", "ACS::VPC::VSwitch", "network.subnet"),
			resourceKind("alicloud:ecs", "ACS::ECS::Instance", "compute.instance"),
		}},
	}}
	sequence := 0
	creator, err := inventory.NewCreator(
		repositories,
		directory,
		inventory.WithCreatorClock(func() time.Time { return now }),
		inventory.WithCreatorIDGenerator(func() string {
			sequence++
			return fmt.Sprintf("resource-center-network-%d", sequence)
		}),
	)
	if err != nil {
		t.Fatal(err)
	}
	created, err := creator.Create(context.Background(), inventory.ScanCreationRequest{
		ConnectionID: "connection-a", RequestedBy: "alice", ScopeMode: asset.ScanSelectedNetworks,
		NetworkTargets: []inventory.NetworkTargetRequest{{
			Kind: asset.ScanTargetVPC, RegionID: "cn-hangzhou", NativeID: "vpc-a",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Shards) != 3 {
		t.Fatalf("Resource Center network shards = %#v", created.Shards)
	}
	for _, shard := range created.Shards {
		if shard.Source != "resource-center" || !shard.Authoritative || shard.ResourceKindID == "" {
			t.Fatalf("Resource Center shard = %#v", shard)
		}
	}
}

func TestCreatorRejectsNetworkTargetMissingFromProvider(t *testing.T) {
	_, creator, _ := creatorFixture(t)
	_, err := creator.Create(context.Background(), inventory.ScanCreationRequest{
		ConnectionID: "connection-a", RequestedBy: "alice", ScopeMode: asset.ScanSelectedNetworks,
		NetworkTargets: []inventory.NetworkTargetRequest{{Kind: asset.ScanTargetVPC, RegionID: "cn-hangzhou", NativeID: "vpc-missing"}},
	})
	requestError, ok := err.(*inventory.ScanRequestError)
	if !ok || requestError.Code != "scan.network_target_not_found" {
		t.Fatalf("error = %#v", err)
	}
}

func TestCreatorRejectsResourceKindsForNetworkScope(t *testing.T) {
	_, creator, _ := creatorFixture(t)
	_, err := creator.Create(context.Background(), inventory.ScanCreationRequest{
		ConnectionID: "connection-a", RequestedBy: "alice", ScopeMode: asset.ScanSelectedNetworks,
		NetworkTargets:  []inventory.NetworkTargetRequest{{Kind: asset.ScanTargetVPC, RegionID: "cn-hangzhou", NativeID: "vpc-a"}},
		ResourceKindIDs: []asset.ResourceKindID{"alicloud:ACS::ECS::Instance"},
	})
	requestError, ok := err.(*inventory.ScanRequestError)
	if !ok || requestError.Code != "scan.resource_kinds_not_allowed" {
		t.Fatalf("error = %#v", err)
	}
}

func TestCreatorRejectsUnverifiedConnectionBeforeCreatingJobs(t *testing.T) {
	repositories, creator, _ := creatorFixture(t)
	connection, err := repositories.Connections().GetConnection(context.Background(), "connection-a")
	if err != nil {
		t.Fatal(err)
	}
	connection.Status = asset.ConnectionUnverified
	if err := repositories.Connections().PutConnection(context.Background(), connection); err != nil {
		t.Fatal(err)
	}

	_, err = creator.Create(context.Background(), inventory.ScanCreationRequest{
		ConnectionID: connection.ID, RequestedBy: "alice", RegionMode: inventory.RegionModeAllActive,
	})
	requestError, ok := err.(*inventory.ScanRequestError)
	if !ok || requestError.Code != "scan.connection_not_validated" {
		t.Fatalf("Create() error = %#v", err)
	}
	runs, err := repositories.Inventory().ListScanRunsByConnection(context.Background(), connection.ID)
	if err != nil || len(runs) != 0 {
		t.Fatalf("scan runs = %#v, err = %v", runs, err)
	}
}

func TestCreatorRollsBackWhenConnectionChangesBeforeTaskCreation(t *testing.T) {
	repositories, _, now := creatorFixture(t)
	conflicting := connectionConflictRepositories{Repositories: repositories}
	kind := asset.ResourceKind{ID: "alicloud:ACS::ECS::Instance", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance", DisplayName: "ECS", Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "test"}
	directory := creatorDirectory{bundle: spec.Bundle{Provider: asset.ProviderAliCloud, Specs: []spec.CompiledSpec{{Definition: spec.ResourceKindSpec{Discovery: spec.DiscoverySpec{Source: "resource-center"}}, ResourceKind: kind}}}}
	sequence := 0
	creator, err := inventory.NewCreator(conflicting, directory, inventory.WithCreatorClock(func() time.Time { return now }), inventory.WithCreatorIDGenerator(func() string {
		sequence++
		return fmt.Sprintf("conflict-%d", sequence)
	}))
	if err != nil {
		t.Fatal(err)
	}

	_, err = creator.Create(context.Background(), inventory.ScanCreationRequest{
		ConnectionID: "connection-a", RequestedBy: "alice", RegionMode: inventory.RegionModeAllActive,
	})
	requestError, ok := err.(*inventory.ScanRequestError)
	if !ok || requestError.Code != "scan.connection_changed" {
		t.Fatalf("Create() error = %#v", err)
	}
	runs, listErr := repositories.Inventory().ListScanRunsByConnection(context.Background(), "connection-a")
	if listErr != nil {
		t.Fatal(listErr)
	}
	if len(runs) != 0 {
		t.Fatalf("scan runs = %+v", runs)
	}
}

func TestCreatorLazilyAddsAccountRootForLegacyAlibabaCloudConnection(t *testing.T) {
	t.Skip("new-project database rebuild intentionally drops legacy scope migration compatibility")
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "legacy-creator.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 9, 30, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "legacy-a", Name: "legacy", Provider: asset.ProviderAliCloud, Partition: "public", Principal: "acs:ram::1234567890123456:user/alice", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Credentials().PutCredential(ctx, asset.ConnectionCredential{ConnectionID: connection.ID, Provider: connection.Provider, Type: asset.CredentialAliCloudAccessKey, EnvelopeVersion: 1, Nonce: "test", Ciphertext: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScope(ctx, asset.Scope{ID: "legacy-region-root", ConnectionID: connection.ID, Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Name: "cn-hangzhou", Location: "cn-hangzhou", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Regions().PutRegion(ctx, asset.ConnectionRegion{ID: "legacy-region", ConnectionID: connection.ID, RegionID: "cn-hangzhou", DiscoveredName: "杭州", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Graph().ReplaceGraph(ctx, "legacy-region-root", "legacy-root-graph", nil, nil); err != nil {
		t.Fatal(err)
	}
	creator, err := inventory.NewCreator(repositories, creatorDirectory{}, inventory.WithCreatorClock(func() time.Time { return now }))
	if err != nil {
		t.Fatal(err)
	}
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "alice", RegionMode: inventory.RegionModeAllActive})
	if err != nil {
		t.Fatal(err)
	}
	regionScope, err := repositories.Inventory().GetScope(ctx, created.Shards[0].ScopeID)
	if err != nil {
		t.Fatal(err)
	}
	accountScope, err := repositories.Inventory().GetScope(ctx, regionScope.ParentID)
	if err != nil || accountScope.Kind != asset.ScopeAccount || accountScope.NativeID != "1234567890123456" || regionScope.ID != "legacy-region-root" {
		t.Fatalf("account scope = %+v, err = %v", accountScope, err)
	}
	scopes, err := repositories.Inventory().ListScopesByConnection(ctx, connection.ID)
	if err != nil || len(scopes) != 2 {
		t.Fatalf("migrated scopes = %+v, err = %v", scopes, err)
	}
	for _, scope := range scopes {
		if scope.Kind == asset.ScopeRegion && scope.ParentID != accountScope.ID {
			t.Fatalf("legacy region remained parentless: %+v", scope)
		}
	}
	if revision, err := repositories.Graph().GetGraphRevision(ctx, "legacy-region-root"); !errors.Is(err, persistence.ErrNotFound) || revision != "" {
		t.Fatalf("legacy root graph revision = %q, err = %v", revision, err)
	}
	for _, shard := range created.Shards {
		shard.Status = asset.ShardSucceeded
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
	}
	for range created.Jobs {
		claimed, err := repositories.Jobs().ClaimNext(ctx, "legacy-test-worker", now, time.Minute)
		if err != nil {
			t.Fatal(err)
		}
		if err := repositories.Jobs().Complete(ctx, claimed.ID, "legacy-test-worker", execution.JobSucceeded, "", now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	allScopes, err := repositories.Inventory().ListScopesByConnectionIncludingAliases(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	var accountAlias, regionAlias asset.Scope
	for _, scope := range allScopes {
		if scope.SupersededByID == "" {
			continue
		}
		switch scope.Kind {
		case asset.ScopeAccount:
			accountAlias = scope
		case asset.ScopeRegion:
			regionAlias = scope
		}
	}
	if accountAlias.ID == "" || accountAlias.SupersededByID != accountScope.ID || regionAlias.ID == "" || regionAlias.SupersededByID != "legacy-region-root" {
		t.Fatalf("rolling aliases = account %+v, region %+v", accountAlias, regionAlias)
	}
	accountAlias.ParentID = ""
	accountAlias.SupersededByID = ""
	accountAlias.NativeID = connection.Principal
	regionAlias.ParentID = accountAlias.ID
	regionAlias.SupersededByID = ""
	for _, test := range []struct {
		scope asset.Scope
		want  asset.ScopeID
	}{{scope: accountAlias, want: accountScope.ID}, {scope: regionAlias, want: "legacy-region-root"}} {
		if err := repositories.Inventory().PutScope(ctx, test.scope); err != nil {
			t.Fatal(err)
		}
		resolved, err := repositories.Inventory().GetScope(ctx, test.scope.ID)
		if err != nil || resolved.ID != test.want {
			t.Fatalf("old writer scope %s resolved to %+v, err = %v", test.scope.ID, resolved, err)
		}
	}
	accountScope.NativeID = connection.Principal
	accountScope.Name = connection.Principal
	if err := repositories.Inventory().PutScope(ctx, accountScope); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScope(ctx, asset.Scope{ID: "intermediate-region-scope", ConnectionID: connection.ID, ParentID: accountScope.ID, Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Name: "杭州", Location: "cn-hangzhou", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutAsset(ctx, asset.Asset{
		ID: "intermediate-asset", Identity: asset.Identity{Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: connection.ID, NativeType: "ACS::ECS::Instance", NativeID: "i-intermediate"},
		ScopeID: "intermediate-region-scope", ResourceKindID: "alicloud:ACS::ECS::Instance", FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{ID: "intermediate-run", ConnectionID: connection.ID, Status: asset.ScanSucceeded, RequestedBy: "alice", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScanShard(ctx, asset.ScanShard{ID: "intermediate-shard", ScanRunID: "intermediate-run", Provider: asset.ProviderAliCloud, Source: "resource-center", RegionID: "cn-hangzhou", ScopeID: "intermediate-region-scope", Status: asset.ShardSucceeded, Coverage: asset.Coverage{ScopeID: "intermediate-region-scope"}, CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Graph().ReplaceGraph(ctx, "legacy-region-root", "canonical-transition-graph", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Graph().ReplaceGraph(ctx, "intermediate-region-scope", "duplicate-transition-graph", nil, nil); err != nil {
		t.Fatal(err)
	}
	created, err = creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "alice", RegionMode: inventory.RegionModeAllActive})
	if err != nil {
		t.Fatal(err)
	}
	if created.Shards[0].ScopeID != "legacy-region-root" {
		t.Fatalf("transition scan used scope %q, want legacy-region-root", created.Shards[0].ScopeID)
	}
	accountScope, err = repositories.Inventory().GetScope(ctx, accountScope.ID)
	if err != nil || accountScope.NativeID != "1234567890123456" {
		t.Fatalf("transition account scope = %+v, err = %v", accountScope, err)
	}
	if scope, err := repositories.Inventory().GetScope(ctx, "intermediate-region-scope"); err != nil || scope.ID != "legacy-region-root" {
		t.Fatalf("duplicate scope did not resolve to canonical: %+v, err = %v", scope, err)
	}
	migratedAsset, err := repositories.Inventory().GetAsset(ctx, "intermediate-asset")
	if err != nil || migratedAsset.ScopeID != "legacy-region-root" {
		t.Fatalf("migrated asset = %+v, err = %v", migratedAsset, err)
	}
	migratedShard, err := repositories.Inventory().GetScanShard(ctx, "intermediate-shard")
	if err != nil || migratedShard.ScopeID != "legacy-region-root" || migratedShard.Coverage.ScopeID != "legacy-region-root" {
		t.Fatalf("migrated shard = %+v, err = %v", migratedShard, err)
	}
	for _, scopeID := range []asset.ScopeID{"legacy-region-root", "intermediate-region-scope"} {
		if revision, err := repositories.Graph().GetGraphRevision(ctx, scopeID); !errors.Is(err, persistence.ErrNotFound) || revision != "" {
			t.Fatalf("transition graph revision for %s = %q, err = %v", scopeID, revision, err)
		}
	}
	scopes, err = repositories.Inventory().ListScopesByConnection(ctx, connection.ID)
	if err != nil || len(scopes) != 2 {
		t.Fatalf("scopes after merge = %+v, err = %v", scopes, err)
	}
}

func TestCreatorAddsOneAWSGlobalShardAndFiltersKindsByScope(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "aws-creator.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 9, 45, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "aws-a", Name: "aws", Provider: asset.ProviderAWS, Partition: "aws", Principal: "arn:aws:iam::123456789012:user/alice", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Credentials().PutCredential(ctx, asset.ConnectionCredential{ConnectionID: connection.ID, Provider: connection.Provider, Type: asset.CredentialAWSAccessKey, EnvelopeVersion: 1, Nonce: "test", Ciphertext: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScope(ctx, asset.Scope{ID: "aws-root", ConnectionID: connection.ID, Kind: asset.ScopeAccount, NativeID: "123456789012", Name: "123456789012", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	for _, region := range []asset.ConnectionRegion{
		{ID: "aws-east", ConnectionID: connection.ID, RegionID: "us-east-1", DiscoveredName: "US East", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
		{ID: "aws-west", ConnectionID: connection.ID, RegionID: "us-west-2", DiscoveredName: "US West", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Regions().PutRegion(ctx, region); err != nil {
			t.Fatal(err)
		}
	}
	kinds := []asset.ResourceKind{
		{ID: "aws:AWS::EC2::Instance", Provider: asset.ProviderAWS, NativeType: "AWS::EC2::Instance", ScopeKinds: []asset.ScopeKind{asset.ScopeRegion}, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "test"},
		{ID: "aws:AWS::IAM::Role", Provider: asset.ProviderAWS, NativeType: "AWS::IAM::Role", ScopeKinds: []asset.ScopeKind{asset.ScopeGlobal}, Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "test"},
	}
	specs := make([]spec.CompiledSpec, 0, len(kinds))
	for _, kind := range kinds {
		specs = append(specs, spec.CompiledSpec{Definition: spec.ResourceKindSpec{Discovery: spec.DiscoverySpec{Source: "resource-explorer"}}, ResourceKind: kind})
	}
	directory := creatorDirectory{
		provider: asset.ProviderAWS,
		sources:  []contracts.InventorySource{{Name: "resource-explorer", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal}}},
		bundle:   spec.Bundle{Provider: asset.ProviderAWS, Specs: specs},
	}
	sequence := 0
	creator, err := inventory.NewCreator(repositories, directory, inventory.WithCreatorClock(func() time.Time { return now }), inventory.WithCreatorIDGenerator(func() string {
		sequence++
		return fmt.Sprintf("aws-id-%d", sequence)
	}))
	if err != nil {
		t.Fatal(err)
	}
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: connection.ID, RequestedBy: "alice", RegionMode: inventory.RegionModeAllActive})
	if err != nil {
		t.Fatal(err)
	}
	if len(created.Shards) != 3 {
		t.Fatalf("broad AWS shards = %+v", created.Shards)
	}
	globalCount := 0
	for _, shard := range created.Shards {
		if shard.RegionID != "global" {
			continue
		}
		globalCount++
		scope, err := repositories.Inventory().GetScope(ctx, shard.ScopeID)
		if err != nil || scope.Kind != asset.ScopeGlobal || scope.ParentID != "aws-root" || scope.Location != "us-east-1" {
			t.Fatalf("global scope = %+v, err = %v", scope, err)
		}
	}
	if globalCount != 1 {
		t.Fatalf("global shard count = %d", globalCount)
	}
	selected, err := creator.Create(ctx, inventory.ScanCreationRequest{
		ConnectionID: connection.ID, RequestedBy: "alice", RegionMode: inventory.RegionModeSelected,
		RegionIDs: []string{"global"}, ResourceKindIDs: []asset.ResourceKindID{"aws:AWS::IAM::Role"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected.Shards) != 1 || selected.Shards[0].RegionID != "global" || selected.Shards[0].ResourceKindID != "aws:AWS::IAM::Role" {
		t.Fatalf("global-only selection = %+v", selected.Shards)
	}
}

func TestCreatorTreatsGlobalAsOptionalSelectedRegion(t *testing.T) {
	globalKind := asset.ResourceKind{
		ID: "alicloud:ACS::CEN::CenInstance", Provider: asset.ProviderAliCloud,
		NativeType: "ACS::CEN::CenInstance", ScopeKinds: []asset.ScopeKind{asset.ScopeGlobal},
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "test",
	}
	newCreator := func(t *testing.T) (persistence.Repositories, *inventory.Creator) {
		t.Helper()
		repositories, _, now := creatorFixture(t)
		creator, err := inventory.NewCreator(
			repositories,
			creatorDirectory{
				sources: []contracts.InventorySource{
					{Name: "resource-center", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion}},
					{
						Name: "product-api", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal},
						AuthoritativeDefault: true, KindSpecific: true,
					},
				},
				bundle: spec.Bundle{
					Provider: asset.ProviderAliCloud,
					Specs: []spec.CompiledSpec{{
						Definition:   spec.ResourceKindSpec{Discovery: spec.DiscoverySpec{Source: "product-api"}},
						ResourceKind: globalKind,
					}},
				},
			},
			inventory.WithCreatorClock(func() time.Time { return now }),
		)
		if err != nil {
			t.Fatal(err)
		}
		return repositories, creator
	}

	t.Run("excluded", func(t *testing.T) {
		_, creator := newCreator(t)
		created, err := creator.Create(context.Background(), inventory.ScanCreationRequest{
			ConnectionID: "connection-a", RequestedBy: "alice", ScopeMode: asset.ScanSelectedRegions,
			RegionIDs: []string{"cn-hangzhou"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(created.ScanRun.Targets) != 1 ||
			created.ScanRun.Targets[0].Kind != asset.ScanTargetRegion ||
			len(created.Shards) != 1 ||
			created.Shards[0].RegionID != "cn-hangzhou" {
			t.Fatalf("creation without global = %+v", created)
		}
	})

	t.Run("global only", func(t *testing.T) {
		repositories, creator := newCreator(t)
		created, err := creator.Create(context.Background(), inventory.ScanCreationRequest{
			ConnectionID: "connection-a", RequestedBy: "alice", ScopeMode: asset.ScanSelectedRegions,
			RegionIDs: []string{"global"}, ResourceKindIDs: []asset.ResourceKindID{globalKind.ID},
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(created.ScanRun.Targets) != 1 ||
			created.ScanRun.Targets[0].Kind != asset.ScanTargetGlobal ||
			len(created.Shards) != 1 ||
			created.Shards[0].RegionID != "global" {
			t.Fatalf("global-only creation = %+v", created)
		}
		scope, err := repositories.Inventory().GetScope(context.Background(), created.Shards[0].ScopeID)
		if err != nil || scope.Kind != asset.ScopeGlobal || scope.Location != "cn-hangzhou" {
			t.Fatalf("global scope = %+v, err = %v", scope, err)
		}
	})
}

func TestCreatorRejectsInvalidRegionSelection(t *testing.T) {
	for _, test := range []struct {
		name      string
		mode      inventory.RegionMode
		regionIDs []string
		code      string
	}{
		{name: "missing selection", mode: inventory.RegionModeSelected, code: "scan.region_ids_required"},
		{name: "duplicate", mode: inventory.RegionModeSelected, regionIDs: []string{"cn-hangzhou", "cn-hangzhou"}, code: "scan.region_duplicate"},
		{name: "retired", mode: inventory.RegionModeSelected, regionIDs: []string{"cn-qingdao"}, code: "scan.region_inactive"},
		{name: "excluded", mode: inventory.RegionModeSelected, regionIDs: []string{"cn-beijing"}, code: "scan.region_inactive"},
		{name: "foreign or missing", mode: inventory.RegionModeSelected, regionIDs: []string{"us-east-1"}, code: "scan.region_not_found"},
		{name: "ids with all", mode: inventory.RegionModeAllActive, regionIDs: []string{"cn-hangzhou"}, code: "scan.region_ids_not_allowed"},
		{name: "bad mode", mode: "anything", code: "scan.scope_mode_invalid"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, creator, _ := creatorFixture(t)
			_, err := creator.Create(context.Background(), inventory.ScanCreationRequest{ConnectionID: "connection-a", RequestedBy: "alice", RegionMode: test.mode, RegionIDs: test.regionIDs})
			requestError, ok := err.(*inventory.ScanRequestError)
			if !ok || requestError.Code != test.code {
				t.Fatalf("error = %#v, want %s", err, test.code)
			}
		})
	}
}

type creatorDirectory struct {
	bundle         spec.Bundle
	provider       asset.Provider
	sources        []contracts.InventorySource
	networkTargets []contracts.NetworkTargetOption
}

type sourceSelectingCreatorDirectory struct {
	creatorDirectory
}

func (sourceSelectingCreatorDirectory) ResolveInventorySource(
	_ asset.Provider,
	_ asset.ResourceKind,
	_ string,
) (string, error) {
	return "resource-center", nil
}

func (d creatorDirectory) ProviderDescriptors() []contracts.ProviderDescriptor {
	provider := d.provider
	if provider == "" {
		provider = asset.ProviderAliCloud
	}
	sources := d.sources
	if len(sources) == 0 {
		sources = []contracts.InventorySource{{Name: "resource-center", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion}}}
	}
	return []contracts.ProviderDescriptor{{Provider: provider, InventorySources: sources}}
}

func (d creatorDirectory) Bundle(asset.Provider) (spec.Bundle, error) { return d.bundle, nil }

func (d creatorDirectory) ResolveNetworkTargetDiscoverer(asset.Provider) (contracts.NetworkTargetDiscoverer, error) {
	return d, nil
}

func (d creatorDirectory) SearchNetworkTargets(_ context.Context, query contracts.NetworkTargetQuery) (contracts.NetworkTargetPage, error) {
	options := d.networkTargets
	if len(options) == 0 {
		options = []contracts.NetworkTargetOption{
			{Kind: asset.ScanTargetVPC, RegionID: "cn-hangzhou", NativeID: "vpc-a", Name: "Provider VPC A"},
			{Kind: asset.ScanTargetVSwitch, RegionID: "cn-hangzhou", NativeID: "vsw-covered", Name: "Covered vSwitch", ParentNativeID: "vpc-a"},
			{Kind: asset.ScanTargetVSwitch, RegionID: "cn-shanghai", NativeID: "vsw-standalone", Name: "Provider vSwitch", ParentNativeID: "vpc-b"},
		}
	}
	page := contracts.NetworkTargetPage{}
	for _, option := range options {
		if option.Kind == query.Kind && option.RegionID == query.RegionID && (query.Query == "" || option.NativeID == query.Query) {
			page.Items = append(page.Items, option)
		}
	}
	return page, nil
}

func creatorFixture(t *testing.T) (persistence.Repositories, *inventory.Creator, time.Time) {
	t.Helper()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "creator.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 7, 21, 9, 0, 0, 0, time.UTC)
	if err := repositories.Connections().PutConnection(ctx, asset.CloudConnection{ID: "connection-a", Name: "a", Provider: asset.ProviderAliCloud, Partition: "public", Principal: "a", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Credentials().PutCredential(ctx, asset.ConnectionCredential{ConnectionID: "connection-a", Provider: asset.ProviderAliCloud, Type: asset.CredentialAliCloudAccessKey, EnvelopeVersion: 1, Nonce: "test", Ciphertext: "test", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScope(ctx, asset.Scope{ID: "root-a", ConnectionID: "connection-a", Kind: asset.ScopeAccount, NativeID: "account-a", Name: "account-a", CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	regions := []asset.ConnectionRegion{
		{ID: "region-hangzhou", ConnectionID: "connection-a", RegionID: "cn-hangzhou", DiscoveredName: "China East 1", NameOverride: "杭州", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
		{ID: "region-shanghai", ConnectionID: "connection-a", RegionID: "cn-shanghai", DiscoveredName: "上海", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
		{ID: "region-qingdao", ConnectionID: "connection-a", RegionID: "cn-qingdao", Origin: asset.RegionOriginManual, Lifecycle: asset.RegionRetired, CreatedAt: now, UpdatedAt: now},
		{ID: "region-beijing", ConnectionID: "connection-a", RegionID: "cn-beijing", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionExcluded, CreatedAt: now, UpdatedAt: now},
	}
	for _, region := range regions {
		if err := repositories.Regions().PutRegion(ctx, region); err != nil {
			t.Fatal(err)
		}
	}
	kind := asset.ResourceKind{ID: "alicloud:ACS::ECS::Instance", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance", DisplayName: "ECS", Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "test"}
	directory := creatorDirectory{bundle: spec.Bundle{Provider: asset.ProviderAliCloud, Specs: []spec.CompiledSpec{{Definition: spec.ResourceKindSpec{Discovery: spec.DiscoverySpec{Source: "resource-center"}}, ResourceKind: kind}}}}
	sequence := 0
	creator, err := inventory.NewCreator(repositories, directory, inventory.WithCreatorClock(func() time.Time { return now }), inventory.WithCreatorIDGenerator(func() string {
		sequence++
		return fmt.Sprintf("id-%d", sequence)
	}))
	if err != nil {
		t.Fatal(err)
	}
	return repositories, creator, now
}

type connectionConflictRepositories struct {
	persistence.Repositories
}

func (r connectionConflictRepositories) WithTx(ctx context.Context, action func(persistence.Repositories) error) error {
	return r.Repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		return action(connectionConflictRepositories{Repositories: repositories})
	})
}

func (r connectionConflictRepositories) Connections() persistence.ConnectionRepository {
	return connectionConflictRepository{ConnectionRepository: r.Repositories.Connections()}
}

type connectionConflictRepository struct {
	persistence.ConnectionRepository
}

func (connectionConflictRepository) PutConnectionIfCredentialUnchanged(
	context.Context,
	asset.CloudConnection,
	time.Time,
	asset.ConnectionCredential,
) error {
	return persistence.ErrConflict
}

func (connectionConflictRepository) PutConnectionIfUnchanged(context.Context, asset.CloudConnection, time.Time) error {
	return persistence.ErrConflict
}
