package gcp

import (
	"embed"
	"fmt"
	"slices"
	"strings"
	"sync"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

const inventorySource = "cloud-asset-inventory"
const actionHook = "gcp.resource"

//go:generate go run ../../cmd/cataloggen -provider gcp -format google-discovery -source catalog/source/discovery.json -output catalog/generated/catalog.json
//go:embed catalog/generated/catalog.json specs/*.yaml
var providerFiles embed.FS

type resourceType struct {
	NativeType       string            `json:"native_type"`
	Collection       string            `json:"collection"`
	Scopes           []asset.ScopeKind `json:"scope_kinds"`
	ReadOperations   []string          `json:"read_operations"`
	DeleteOperations []string          `json:"delete_operations"`
}

var both = []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal}

type providerMetadata struct {
	catalog catalog.Catalog
	bundle  spec.Bundle
	kinds   []resourceType
}

var providerData = sync.OnceValues(loadProviderData)

func loadProviderData() (providerMetadata, error) {
	var result providerMetadata
	payload, err := providerFiles.ReadFile("catalog/generated/catalog.json")
	if err != nil {
		return result, err
	}
	result.catalog, err = catalog.UnmarshalGenerated(payload)
	if err != nil {
		return result, err
	}
	for _, kind := range result.catalog.ResourceTypes {
		if kind.REST == nil {
			return result, fmt.Errorf("GCP resource %q has no REST binding", kind.NativeType)
		}
		result.kinds = append(result.kinds, resourceType{NativeType: kind.NativeType, Scopes: kind.ScopeKinds, Collection: kind.REST.Collection, ReadOperations: kind.REST.ReadOperations, DeleteOperations: kind.REST.DeleteOperations})
	}
	entries, err := providerFiles.ReadDir("specs")
	if err != nil {
		return result, err
	}
	var sources [][]byte
	for _, entry := range entries {
		payload, err := providerFiles.ReadFile("specs/" + entry.Name())
		if err != nil {
			return result, err
		}
		sources = append(sources, payload)
	}
	result.bundle, err = spec.CompileBundle(sources, result.catalog, spec.HookRegistry{actionHook: {spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback}})
	if err != nil {
		return result, err
	}
	if len(result.kinds) != len(result.catalog.ResourceTypes) || len(result.kinds) != len(result.bundle.Specs) {
		return result, fmt.Errorf("GCP resource metadata and spec coverage differ")
	}
	seen := map[string]bool{}
	definitions := map[string]spec.ResourceKindSpec{}
	for _, compiled := range result.bundle.Specs {
		definitions[compiled.ResourceKind.NativeType] = compiled.Definition
	}
	for _, kind := range result.kinds {
		if seen[kind.NativeType] || kind.Collection == "" || len(kind.ReadOperations) == 0 {
			return result, fmt.Errorf("invalid GCP resource mapping %q", kind.NativeType)
		}
		seen[kind.NativeType] = true
		if _, ok := result.catalog.ResourceType(kind.NativeType); !ok {
			return result, fmt.Errorf("GCP resource %q is missing from catalog", kind.NativeType)
		}
		definition, ok := definitions[kind.NativeType]
		if !ok || definition.Discovery.Detail == nil || !slices.Contains(kind.ReadOperations, definition.Discovery.Detail.Operation) {
			return result, fmt.Errorf("GCP resource %q read binding differs from spec", kind.NativeType)
		}
		deletion, actionable := definition.Actions["delete"]
		if actionable != (len(kind.DeleteOperations) > 0) || (actionable && !slices.Contains(kind.DeleteOperations, deletion.Operation)) {
			return result, fmt.Errorf("GCP resource %q delete binding differs from spec", kind.NativeType)
		}
		for _, ids := range [][]string{kind.ReadOperations, kind.DeleteOperations} {
			for _, id := range ids {
				operation, ok := result.catalog.Operation(id)
				if !ok || operation.Call == nil || operation.Call.Style != "google-rest" {
					return result, fmt.Errorf("GCP resource %q references an unknown operation %q", kind.NativeType, id)
				}
				if slices.Contains(kind.ReadOperations, id) && operation.Call.Method != "GET" {
					return result, fmt.Errorf("GCP read binding %q is not a GET", id)
				}
				if slices.Contains(kind.DeleteOperations, id) && (operation.Call.Method != "DELETE" || !operation.Destructive) {
					return result, fmt.Errorf("GCP delete binding %q is not destructive", id)
				}
			}
		}
	}
	return result, nil
}

func allTypes() []resourceType {
	metadata, _ := providerData()
	return metadata.kinds
}
func findType(nativeType string) (resourceType, bool) {
	for _, kind := range allTypes() {
		if kind.NativeType == nativeType {
			return kind, true
		}
	}
	return resourceType{}, false
}
func compileBundle() (spec.Bundle, error) {
	metadata, err := providerData()
	return metadata.bundle, err
}

func referenceKey(nativeType string) string {
	return "refs_" + strings.NewReplacer(".", "_", "/", "_").Replace(nativeType)
}
