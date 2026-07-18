package cleanup

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/core/topology"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

func TestResolveSelectionSkipsKindLookupForNonGroupSelectors(t *testing.T) {
	t.Parallel()

	for _, selector := range []plan.CleanupSelector{
		{Kind: plan.SelectorAsset, AssetID: "instance-kind-count"},
		{Kind: plan.SelectorScope, ConnectionID: "connection-kind-count", ScopeID: "region-kind-count", Descendants: true},
		{Kind: plan.SelectorConnection, ConnectionID: "connection-kind-count"},
	} {
		selector := selector
		t.Run(string(selector.Kind), func(t *testing.T) {
			t.Parallel()
			repositories, inventory := kindLookupFixture(t)
			resolver := &countingBundleResolver{bundle: kindLookupBundle()}
			service := NewService(repositories, resolver)

			if _, err := service.resolveSelection(context.Background(), repositories, []plan.CleanupSelector{selector}); err != nil {
				t.Fatal(err)
			}
			if inventory.getResourceKindCalls != 0 || resolver.calls != 0 {
				t.Fatalf("selector %s kind reads: repository=%d bundle=%d", selector.Kind, inventory.getResourceKindCalls, resolver.calls)
			}
		})
	}
}

func TestResolveSelectionLoadsOneKindBatchForGroupSelector(t *testing.T) {
	t.Parallel()

	repositories, inventory := kindLookupFixture(t)
	resolver := &countingBundleResolver{bundle: kindLookupBundle()}
	service := NewService(repositories, resolver)
	selection, err := service.resolveSelection(context.Background(), repositories, []plan.CleanupSelector{{
		Kind: plan.SelectorGroup, ConnectionID: "connection-kind-count",
		GroupKey: topology.VPCFocusKey("cn-hangzhou", "vpc-a"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if inventory.getResourceKindCalls != 0 || resolver.calls != 1 {
		t.Fatalf("group kind reads: repository=%d bundle=%d", inventory.getResourceKindCalls, resolver.calls)
	}
	if len(selection.AssetIDs) != 2 {
		t.Fatalf("group selection = %+v", selection.SelectionResult)
	}
}

type kindCountingRepositories struct {
	persistence.Repositories
	inventory *kindCountingInventory
}

func (r *kindCountingRepositories) Inventory() persistence.InventoryRepository {
	return r.inventory
}

type kindCountingInventory struct {
	persistence.InventoryRepository
	getResourceKindCalls int
}

func (r *kindCountingInventory) GetResourceKind(ctx context.Context, id asset.ResourceKindID) (asset.ResourceKind, error) {
	r.getResourceKindCalls++
	return r.InventoryRepository.GetResourceKind(ctx, id)
}

type countingBundleResolver struct {
	bundle spec.Bundle
	calls  int
}

func (r *countingBundleResolver) Bundle(provider asset.Provider) (spec.Bundle, error) {
	r.calls++
	if r.bundle.Provider != provider {
		return spec.Bundle{}, fmt.Errorf("bundle for %s not found", provider)
	}
	return r.bundle, nil
}

func (r *countingBundleResolver) ProviderDescriptors() []contracts.ProviderDescriptor {
	return []contracts.ProviderDescriptor{{
		Provider: r.bundle.Provider,
		InventorySources: []contracts.InventorySource{{
			Name: "test-index", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
		}},
	}}
}

func kindLookupFixture(t *testing.T) (*kindCountingRepositories, *kindCountingInventory) {
	t.Helper()
	underlying, err := sqlite.Open(filepath.Join(t.TempDir(), "kind-lookup.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, 7, 24, 18, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-kind-count", Provider: asset.ProviderAliCloud, Name: "kind count",
		Principal: "kind count", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := underlying.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []asset.Scope{
		{ID: "account-kind-count", ConnectionID: connection.ID, Kind: asset.ScopeAccount, NativeID: "account", CreatedAt: now, UpdatedAt: now},
		{ID: "region-kind-count", ConnectionID: connection.ID, ParentID: "account-kind-count", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", CreatedAt: now, UpdatedAt: now},
	} {
		if err := underlying.Inventory().PutScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []asset.ResourceKind{
		{ID: "kind-vpc", Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::VPC", Class: "network.vpc"},
		{ID: "kind-instance", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance", Class: "compute.instance"},
	} {
		if err := underlying.Inventory().PutResourceKind(ctx, kind); err != nil {
			t.Fatal(err)
		}
	}
	vpc := asset.Asset{
		ID: "vpc-kind-count", Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, ConnectionID: connection.ID, NativeType: "ACS::VPC::VPC", NativeID: "vpc-a",
		},
		ScopeID: "region-kind-count", ResourceKindID: "kind-vpc", Name: "VPC",
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable},
		Normalized:   map[string]any{}, FirstSeenAt: now, LastSeenAt: now,
	}
	instance := asset.Asset{
		ID: "instance-kind-count", Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, ConnectionID: connection.ID, NativeType: "ACS::ECS::Instance", NativeID: "i-a",
		},
		ScopeID: "region-kind-count", ResourceKindID: "kind-instance", Name: "instance",
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityActionable},
		Normalized:   map[string]any{topology.NormalizedVPCID: "vpc-a"},
		FirstSeenAt:  now, LastSeenAt: now,
	}
	for _, value := range []asset.Asset{vpc, instance} {
		if err := underlying.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	inventory := &kindCountingInventory{InventoryRepository: underlying.Inventory()}
	return &kindCountingRepositories{Repositories: underlying, inventory: inventory}, inventory
}

func kindLookupBundle() spec.Bundle {
	return spec.Bundle{
		Provider: asset.ProviderAliCloud, Revision: "bundle-kind-count", Hash: "spec-kind-count",
		Specs: []spec.CompiledSpec{
			{ResourceKind: asset.ResourceKind{ID: "kind-vpc", Provider: asset.ProviderAliCloud, NativeType: "ACS::VPC::VPC", Class: "network.vpc"}},
			{ResourceKind: asset.ResourceKind{ID: "kind-instance", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance", Class: "compute.instance"}},
		},
	}
}
