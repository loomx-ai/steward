package azure

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

const inventorySource = "azure-resource-manager"
const productInventorySource = "product-api"
const actionHook = "azure.resource"
const vmType = "Microsoft.Compute/virtualMachines"
const diskType = "Microsoft.Compute/disks"
const vnetType = "Microsoft.Network/virtualNetworks"
const subnetType = "Microsoft.Network/virtualNetworks/subnets"
const nicType = "Microsoft.Network/networkInterfaces"
const storageType = "Microsoft.Storage/storageAccounts"
const containerType = "Microsoft.Storage/storageAccounts/blobServices/containers"
const groupType = "Microsoft.Resources/resourceGroups"

//go:generate go run ../../cmd/cataloggen -provider azure -format azure-openapi -source catalog/source/swagger.json -output catalog/generated/catalog.json
//go:embed catalog/generated/catalog.json specs/*.yaml
var providerFiles embed.FS

type resourceType struct {
	Version          string
	ReadOnly         bool
	NativeType       string            `json:"native_type"`
	Collection       string            `json:"collection"`
	Scopes           []asset.ScopeKind `json:"scope_kinds"`
	ReadOperations   []string          `json:"read_operations"`
	DeleteOperations []string          `json:"delete_operations"`
	ListOperations   []string          `json:"list_operations"`
	ResponseTypes    []string          `json:"response_types,omitempty"`
	ResponseIDTypes  []string          `json:"response_id_types,omitempty"`
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
		if kind.REST == nil || len(kind.REST.ReadOperations) == 0 {
			return result, fmt.Errorf("Azure resource %q has no REST binding", kind.NativeType)
		}
		operation, ok := result.catalog.Operation(kind.REST.ReadOperations[0])
		if !ok || operation.Call == nil {
			return result, fmt.Errorf("Azure resource has no valid read operation")
		}
		result.kinds = append(result.kinds, resourceType{Version: operation.Call.Version, ReadOnly: len(kind.REST.DeleteOperations) == 0, NativeType: kind.NativeType, Scopes: kind.ScopeKinds, Collection: kind.REST.Collection, ReadOperations: kind.REST.ReadOperations, DeleteOperations: kind.REST.DeleteOperations, ListOperations: kind.REST.ListOperations, ResponseTypes: kind.REST.ResponseTypes, ResponseIDTypes: kind.REST.ResponseIDTypes})
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
		return result, fmt.Errorf("Azure resource metadata and spec coverage differ")
	}
	seen := map[string]bool{}
	definitions := map[string]spec.ResourceKindSpec{}
	for _, compiled := range result.bundle.Specs {
		definitions[compiled.ResourceKind.NativeType] = compiled.Definition
	}
	for _, kind := range result.kinds {
		if seen[kind.NativeType] || kind.Collection == "" || len(kind.ReadOperations) == 0 {
			return result, fmt.Errorf("invalid Azure resource mapping %q", kind.NativeType)
		}
		seen[kind.NativeType] = true
		for _, alias := range kind.ResponseIDTypes {
			if !validResponseIDType(kind.NativeType, alias) {
				return result, fmt.Errorf("Azure response ID alias changes an ancestor resource")
			}
			for _, other := range result.kinds {
				if strings.EqualFold(alias, other.NativeType) {
					return result, fmt.Errorf("Azure response ID alias shadows a resource type")
				}
			}
		}
		if _, ok := result.catalog.ResourceType(kind.NativeType); !ok {
			return result, fmt.Errorf("Azure resource %q is missing from catalog", kind.NativeType)
		}
		definition, ok := definitions[kind.NativeType]
		if !ok || definition.Discovery.Detail == nil || !slices.Contains(kind.ReadOperations, definition.Discovery.Detail.Operation) {
			return result, fmt.Errorf("Azure resource %q read binding differs from spec", kind.NativeType)
		}
		deletion, actionable := definition.Actions["delete"]
		if actionable != (len(kind.DeleteOperations) > 0) || (actionable && !slices.Contains(kind.DeleteOperations, deletion.Operation)) {
			return result, fmt.Errorf("Azure resource %q delete binding differs from spec", kind.NativeType)
		}
		if definition.Discovery.Source != insightsInventorySource(kind.NativeType) || definition.Discovery.List == nil || !slices.Contains(kind.ListOperations, definition.Discovery.List.Operation) {
			return result, fmt.Errorf("Azure resource %q list binding differs from spec", kind.NativeType)
		}
		for _, ids := range [][]string{kind.ReadOperations, kind.DeleteOperations, kind.ListOperations} {
			for _, id := range ids {
				operation, ok := result.catalog.Operation(id)
				if !ok || operation.Call == nil || (operation.Call.Style != "azure-rest" && operation.Call.Style != "azure-batch-rest" && operation.Call.Style != "azure-communication-rest" && operation.Call.Style != "azure-synapse-rest") {
					return result, fmt.Errorf("Azure resource %q references an unknown operation %q", kind.NativeType, id)
				}
				if (slices.Contains(kind.ReadOperations, id) && operation.Call.Method != resourceReadMethod(kind.NativeType)) || (slices.Contains(kind.ListOperations, id) && operation.Call.Method != resourceListMethod(kind.NativeType)) {
					return result, fmt.Errorf("Azure read binding %q does not use its native read method", id)
				}
				batchNodeRemoval := kind.NativeType == "Microsoft.Batch/batchAccounts/pools/nodes" && operation.ID == "Azure.Microsoft.Batch.DataPlane.Pools_RemoveNodes" && operation.Call.Method == "POST"
				if slices.Contains(kind.DeleteOperations, id) && ((operation.Call.Method != "DELETE" && !batchNodeRemoval) || !operation.Destructive) {
					return result, fmt.Errorf("Azure delete binding %q is not destructive", id)
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
		if strings.EqualFold(kind.NativeType, nativeType) {
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
	return "refs_" + strings.NewReplacer(".", "_", "/", "_").Replace(strings.ToLower(nativeType))
}

func validResponseIDType(nativeType, alias string) bool {
	// ApiGateway_Get/Delete use the singular root collection in their native
	// examples. This exact alias preserves subscription, group and gateway name.
	if strings.EqualFold(nativeType, apimGatewayType) && strings.EqualFold(alias, apimGatewayAlias) {
		return true
	}
	// The native Cosmos DB examples rename both the parent container and the
	// final collection. These exact aliases do not change any resource name.
	for kind, responseKind := range map[string]string{
		cosmosStoredProcedureType: cosmosSQLDatabaseType + "/sqlContainers/sqlStoredProcedures",
		cosmosTriggerType:         cosmosSQLDatabaseType + "/sqlContainers/sqlTriggers",
		cosmosFunctionType:        cosmosSQLDatabaseType + "/sqlContainers/sqlUserDefinedFunctions",
	} {
		if strings.EqualFold(nativeType, kind) && strings.EqualFold(alias, responseKind) {
			return true
		}
	}
	parts, aliases := strings.Split(nativeType, "/"), strings.Split(alias, "/")
	return len(parts) >= 3 && len(parts) == len(aliases) && aliases[len(aliases)-1] != "" &&
		strings.EqualFold(strings.Join(parts[:len(parts)-1], "/"), strings.Join(aliases[:len(aliases)-1], "/")) &&
		!strings.EqualFold(parts[len(parts)-1], aliases[len(aliases)-1])
}

func resourceListMethod(kind string) string {
	if kind == dataFactoryNodeType {
		return "POST"
	}
	return "GET"
}
