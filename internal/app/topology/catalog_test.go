package topology

import (
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

func TestCatalogProjectionCarriesProviderNeutralKindPresentation(t *testing.T) {
	t.Parallel()

	kind := asset.ResourceKind{
		ID: "alicloud:ACS::ECS::Disk", Provider: asset.ProviderAliCloud,
		NativeType: "ACS::ECS::Disk", Class: "storage.block", Icon: "disk",
	}
	bundle := spec.Bundle{
		Provider: asset.ProviderAliCloud, Revision: "bundle-icon-only",
		Specs: []spec.CompiledSpec{{
			ResourceKind: kind,
			Definition: spec.ResourceKindSpec{
				Presentation: spec.PresentationSpec{Icon: "disk"},
			},
		}},
	}
	kinds, revision := catalogProjection([]spec.Bundle{bundle}, asset.ProviderAliCloud)
	if kinds[kind.ID].Icon != "disk" || kinds[kind.ID].Class != "storage.block" || revision != "bundle-icon-only" {
		t.Fatalf("resource kind = %+v revision=%q", kinds[kind.ID], revision)
	}
}

func TestGraphFiltersRequireBothVisibleInstanceEndpoints(t *testing.T) {
	t.Parallel()

	visible := map[asset.AssetID]struct{}{
		"instance-a": {},
		"instance-b": {},
	}
	relationships := filterRelationshipsByAssets([]graph.Relationship{
		{ID: "kept", SourceAssetID: "instance-a", TargetAssetID: "instance-b"},
		{ID: "child-target", SourceAssetID: "instance-a", TargetAssetID: "listener-a"},
		{ID: "child-source", SourceAssetID: "listener-a", TargetAssetID: "instance-b"},
	}, visible)
	if len(relationships) != 1 || relationships[0].ID != "kept" {
		t.Fatalf("filtered relationships = %+v", relationships)
	}
	bindings := filterBindingsByAssets([]graph.LifecycleBinding{
		{ID: "kept", ControllerAssetID: "instance-a", ManagedAssetID: "instance-b"},
		{ID: "child-controller", ControllerAssetID: "listener-a", ManagedAssetID: "instance-a"},
		{ID: "child-managed", ControllerAssetID: "instance-a", ManagedAssetID: "listener-a"},
	}, visible)
	if len(bindings) != 1 || bindings[0].ID != "kept" {
		t.Fatalf("filtered lifecycle bindings = %+v", bindings)
	}
}
