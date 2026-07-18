package catalog_test

import (
	"bytes"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
)

func TestCatalogOnlyResourceKindRemainsIndexed(t *testing.T) {
	t.Parallel()

	c := catalog.Catalog{
		Provider: asset.ProviderAliCloud,
		ResourceTypes: []catalog.ResourceType{{
			NativeType:  "ACS::ECS::Instance",
			DisplayName: "ECS Instance",
			ScopeKinds:  []asset.ScopeKind{asset.ScopeRegion},
		}},
	}

	kind, err := c.ResourceKind("ACS::ECS::Instance", "catalog-checksum", nil)
	if err != nil {
		t.Fatalf("project catalog resource kind: %v", err)
	}
	if len(kind.Capabilities) != 1 || !kind.Capabilities.Has(asset.CapabilityIndexed) {
		t.Fatalf("catalog-only kind capabilities = %v, want indexed only", kind.Capabilities)
	}
	if kind.BundleRevision != "catalog-checksum" {
		t.Fatalf("bundle revision = %q", kind.BundleRevision)
	}
}

func TestRegistryRejectsDuplicateNativeTypes(t *testing.T) {
	t.Parallel()

	registry := catalog.NewRegistry()
	first := catalog.Catalog{
		Provider:      asset.ProviderAWS,
		ResourceTypes: []catalog.ResourceType{{NativeType: "AWS::EC2::Instance"}},
	}
	second := catalog.Catalog{
		Provider:      asset.ProviderAWS,
		ResourceTypes: []catalog.ResourceType{{NativeType: "AWS::EC2::Instance"}},
	}

	if err := registry.Register(first); err != nil {
		t.Fatalf("register first catalog: %v", err)
	}
	if err := registry.Register(second); err == nil {
		t.Fatal("duplicate native type must be rejected")
	}
}

func TestRegistryMergesIndependentCatalogFragmentsForOneProvider(t *testing.T) {
	t.Parallel()

	registry := catalog.NewRegistry()
	fragments := []catalog.Catalog{
		{
			Provider:      asset.ProviderAliCloud,
			Source:        catalog.Source{URI: "ecs.json", Checksum: "ecs-sha"},
			Operations:    []catalog.Operation{{Name: "DescribeInstances"}},
			ResourceTypes: []catalog.ResourceType{{NativeType: "ACS::ECS::Instance"}},
		},
		{
			Provider:      asset.ProviderAliCloud,
			Source:        catalog.Source{URI: "vpc.json", Checksum: "vpc-sha"},
			Operations:    []catalog.Operation{{Name: "DescribeVpcs"}},
			ResourceTypes: []catalog.ResourceType{{NativeType: "ACS::VPC::VPC"}},
		},
	}
	for _, fragment := range fragments {
		if err := registry.Register(fragment); err != nil {
			t.Fatalf("register independent catalog fragment: %v", err)
		}
	}
	merged, err := registry.Resolve(asset.ProviderAliCloud)
	if err != nil {
		t.Fatalf("resolve merged catalog: %v", err)
	}
	if len(merged.ResourceTypes) != 2 || len(merged.Operations) != 2 {
		t.Fatalf("merged catalog = %+v", merged)
	}
	if merged.Source.Checksum == "" || merged.Source.URI != "catalog://alicloud" {
		t.Fatalf("merged catalog provenance = %+v", merged.Source)
	}
}

func TestRegistryNamespacesSameOperationNameAcrossServices(t *testing.T) {
	t.Parallel()

	registry := catalog.NewRegistry()
	for _, fragment := range []catalog.Catalog{
		{
			Provider:   asset.ProviderAliCloud,
			Source:     catalog.Source{URI: "ecs.json", Checksum: "ecs-sha"},
			Operations: []catalog.Operation{{ID: "ecs.ListTags", Name: "ListTags", Service: "ecs"}},
		},
		{
			Provider:   asset.ProviderAliCloud,
			Source:     catalog.Source{URI: "vpc.json", Checksum: "vpc-sha"},
			Operations: []catalog.Operation{{ID: "vpc.ListTags", Name: "ListTags", Service: "vpc"}},
		},
	} {
		if err := registry.Register(fragment); err != nil {
			t.Fatalf("register namespaced operation: %v", err)
		}
	}
	merged, err := registry.Resolve(asset.ProviderAliCloud)
	if err != nil {
		t.Fatalf("resolve merged catalog: %v", err)
	}
	if _, ok := merged.Operation("ecs.ListTags"); !ok {
		t.Fatal("qualified ECS operation was not resolved")
	}
	if _, ok := merged.Operation("vpc.ListTags"); !ok {
		t.Fatal("qualified VPC operation was not resolved")
	}
	if _, ok := merged.Operation("ListTags"); ok {
		t.Fatal("ambiguous unqualified operation must not resolve")
	}
}

func TestRegistryCopiesCatalogFragmentsAtItsBoundary(t *testing.T) {
	t.Parallel()

	registry := catalog.NewRegistry()
	fragment := catalog.Catalog{
		Provider:      asset.ProviderAWS,
		Source:        catalog.Source{URI: "compute.json", Checksum: "compute-sha"},
		Operations:    []catalog.Operation{{ID: "compute.delete", Name: "delete", InputSchema: map[string]any{"type": "object"}}},
		ResourceTypes: []catalog.ResourceType{{NativeType: "AWS::EC2::Instance"}},
	}
	if err := registry.Register(fragment); err != nil {
		t.Fatalf("register catalog fragment: %v", err)
	}
	fragment.ResourceTypes[0].NativeType = "mutated-input"
	fragment.Operations[0].InputSchema["type"] = "mutated-input"
	first, err := registry.Resolve(asset.ProviderAWS)
	if err != nil {
		t.Fatalf("resolve catalog: %v", err)
	}
	first.ResourceTypes[0].NativeType = "mutated-output"
	first.Operations[0].InputSchema["type"] = "mutated-output"
	second, err := registry.Resolve(asset.ProviderAWS)
	if err != nil {
		t.Fatalf("resolve catalog again: %v", err)
	}
	if second.ResourceTypes[0].NativeType != "AWS::EC2::Instance" || second.Operations[0].InputSchema["type"] != "object" {
		t.Fatalf("registered catalog was mutated: %+v", second)
	}
}

func TestOpenAPIImporterIsDeterministicAndCarriesProvenance(t *testing.T) {
	t.Parallel()

	source := []byte(`{
  "info": {"title": "ECS"},
  "paths": {
    "/instances/{id}": {"delete": {"operationId": "DeleteInstance"}},
    "/instances": {"get": {"operationId": "ListInstances"}}
  },
  "x-resource-types": [
    {"nativeType": "ACS::ECS::Instance", "displayName": "ECS Instance", "scopeKinds": ["region"]}
  ]
}`)
	importer := catalog.OpenAPIImporter{}

	first, err := importer.Import(asset.ProviderAliCloud, "official/ecs.json", source)
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	second, err := importer.Import(asset.ProviderAliCloud, "official/ecs.json", source)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	firstBytes, err := catalog.MarshalGenerated(first)
	if err != nil {
		t.Fatalf("marshal first catalog: %v", err)
	}
	secondBytes, err := catalog.MarshalGenerated(second)
	if err != nil {
		t.Fatalf("marshal second catalog: %v", err)
	}
	if !bytes.Equal(firstBytes, secondBytes) {
		t.Fatalf("generated catalog is not deterministic:\n%s\n---\n%s", firstBytes, secondBytes)
	}
	if !bytes.HasPrefix(firstBytes, []byte(catalog.GeneratedHeader)) {
		t.Fatalf("generated catalog lacks do-not-edit header: %s", firstBytes)
	}
	if _, err := catalog.UnmarshalGenerated(bytes.TrimPrefix(firstBytes, []byte(catalog.GeneratedHeader))); err == nil {
		t.Fatal("generated catalog without generator header must be rejected")
	}
	tampered := bytes.Replace(firstBytes, []byte("DeleteInstance"), []byte("TamperInstance"), 1)
	if _, err := catalog.UnmarshalGenerated(tampered); err == nil {
		t.Fatal("manually edited generated catalog must fail content checksum validation")
	}
	if _, err := catalog.UnmarshalGenerated(firstBytes); err != nil {
		t.Fatalf("load verified generated catalog: %v", err)
	}
	if first.Source.URI != "official/ecs.json" || first.Source.Checksum == "" || first.Source.Generator == "" {
		t.Fatalf("incomplete provenance: %+v", first.Source)
	}
	deleteOperation, ok := first.Operation("DeleteInstance")
	if !ok || !deleteOperation.Destructive {
		t.Fatalf("delete operation not classified destructive: %+v, %v", deleteOperation, ok)
	}
}
