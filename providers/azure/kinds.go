package azure

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/spec"
	"gopkg.in/yaml.v3"
)

const inventorySource = "azure-resource-manager"
const actionHook = "azure.resource"
const vmType = "Microsoft.Compute/virtualMachines"
const diskType = "Microsoft.Compute/disks"
const vnetType = "Microsoft.Network/virtualNetworks"
const subnetType = "Microsoft.Network/virtualNetworks/subnets"
const nicType = "Microsoft.Network/networkInterfaces"
const storageType = "Microsoft.Storage/storageAccounts"
const containerType = "Microsoft.Storage/storageAccounts/blobServices/containers"
const groupType = "Microsoft.Resources/resourceGroups"

type resourceType struct {
	NativeType, Version, Class, Name string
	ReadOnly                         bool
}

var both = []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal}
var resourceTypes = []resourceType{
	{vmType, "2024-07-01", "compute.instance", "Azure virtual machine", false},
	{diskType, "2024-03-02", "storage.disk", "Managed disk", false},
	{"Microsoft.Compute/snapshots", "2024-03-02", "storage.snapshot", "Disk snapshot", false},
	{"Microsoft.Compute/images", "2024-07-01", "compute.image", "Managed image", false},
	{"Microsoft.Compute/availabilitySets", "2024-07-01", "compute.availability_set", "Availability set", false},
	{"Microsoft.Compute/virtualMachineScaleSets", "2024-07-01", "compute.scale_group", "Virtual machine scale set", true},
	{vnetType, "2024-05-01", "network.vpc", "Virtual network", false},
	{subnetType, "2024-05-01", "network.subnet", "Virtual network subnet", false},
	{nicType, "2024-05-01", "network.interface", "Network interface", false},
	{"Microsoft.Network/networkSecurityGroups", "2024-05-01", "network.security_group", "Network security group", false},
	{"Microsoft.Network/routeTables", "2024-05-01", "network.route_table", "Route table", false},
	{"Microsoft.Network/publicIPAddresses", "2024-05-01", "network.public_ip", "Public IP address", false},
	{"Microsoft.Network/publicIPPrefixes", "2024-05-01", "network.public_ip_prefix", "Public IP prefix", false},
	{"Microsoft.Network/natGateways", "2024-05-01", "network.nat_gateway", "NAT gateway", false},
	{"Microsoft.Network/loadBalancers", "2024-05-01", "network.load_balancer", "Load balancer", false},
	{"Microsoft.Network/applicationGateways", "2024-05-01", "network.load_balancer", "Application gateway", false},
	{"Microsoft.Network/privateEndpoints", "2024-05-01", "network.endpoint", "Private endpoint", true},
	{storageType, "2023-05-01", "storage.account", "Storage account", false},
	{containerType, "2023-05-01", "storage.bucket", "Blob container", false},
	{"Microsoft.Sql/servers", "2023-08-01", "database.server", "SQL logical server", true},
	{"Microsoft.Sql/servers/databases", "2023-08-01", "database.instance", "SQL database", false},
	{"Microsoft.Sql/servers/elasticPools", "2023-08-01", "database.pool", "SQL elastic pool", false},
	{"Microsoft.DBforPostgreSQL/flexibleServers", "2024-08-01", "database.instance", "PostgreSQL flexible server", false},
	{"Microsoft.DBforMySQL/flexibleServers", "2023-12-30", "database.instance", "MySQL flexible server", false},
	{"Microsoft.Web/sites", "2023-12-01", "compute.service", "App Service or Function App", false},
	{"Microsoft.Web/serverfarms", "2023-12-01", "compute.service_plan", "App Service plan", false},
	{"Microsoft.ContainerRegistry/registries", "2023-07-01", "storage.registry", "Container registry", false},
	{"Microsoft.ContainerService/managedClusters", "2024-02-01", "container.cluster", "AKS cluster", true},
	{"Microsoft.KeyVault/vaults", "2023-07-01", "security.vault", "Key Vault", true},
	{"Microsoft.OperationalInsights/workspaces", "2022-10-01", "observability.workspace", "Log Analytics workspace", false},
	{"Microsoft.ManagedIdentity/userAssignedIdentities", "2023-01-31", "security.identity", "User-assigned managed identity", false},
	{"Microsoft.App/containerApps", "2024-03-01", "compute.service", "Container App", false},
	{"Microsoft.App/managedEnvironments", "2024-03-01", "compute.environment", "Container Apps environment", true},
	{groupType, resourcesVersion, "organization.resource_group", "Resource group", true},
}

func findType(nativeType string) (resourceType, bool) {
	for _, kind := range resourceTypes {
		if strings.EqualFold(kind.NativeType, nativeType) {
			return kind, true
		}
	}
	return resourceType{}, false
}
func referenceKey(nativeType string) string {
	return "refs_" + strings.NewReplacer(".", "_", "/", "_").Replace(strings.ToLower(nativeType))
}
func compileBundle() (spec.Bundle, error) {
	payload, _ := json.Marshal(resourceTypes)
	digest := sha256.Sum256(payload)
	c := catalog.Catalog{Provider: asset.ProviderAzure, Source: catalog.Source{Format: "azure-rest-api-specs", URI: "https://github.com/Azure/azure-rest-api-specs", Checksum: hex.EncodeToString(digest[:]), Generator: "steward/azure"}}
	c.Operations = []catalog.Operation{{ID: "azure.resources.get", Name: "GetResource"}, {ID: "azure.resources.delete", Name: "DeleteResource", Destructive: true}}
	sources := make([][]byte, 0, len(resourceTypes))
	for _, kind := range resourceTypes {
		scopes := both
		if kind.NativeType == groupType {
			scopes = []asset.ScopeKind{asset.ScopeGlobal}
		}
		c.ResourceTypes = append(c.ResourceTypes, catalog.ResourceType{NativeType: kind.NativeType, Class: kind.Class, DisplayName: kind.Name, ScopeKinds: scopes})
		definition := spec.ResourceKindSpec{Schema: spec.SchemaIdentifier, Kind: "ResourceKind",
			Metadata:     spec.Metadata{Provider: asset.ProviderAzure, NativeType: kind.NativeType, Class: kind.Class},
			Scope:        spec.ScopeSpec{Kind: scopes[0]},
			Presentation: spec.PresentationSpec{DisplayNames: map[string]string{"en-US": kind.Name}},
			Discovery:    spec.DiscoverySpec{Source: inventorySource, Detail: &spec.DetailSpec{Operation: "azure.resources.get", ItemsPath: "resource", IdentityPath: "id"}},
			Extensions:   spec.Extensions{Hook: actionHook}}
		definition.Fields = map[string]spec.FieldSpec{"subscriptionId": {Path: "subscription_id", Type: spec.PropertyString}, "resourceGroup": {Path: "resource_group", Type: spec.PropertyString}, "zoneId": {Path: "zone_id", Type: spec.PropertyString}}
		if !kind.ReadOnly {
			definition.Actions = map[string]spec.ActionSpec{"delete": {Operation: "azure.resources.delete", Idempotency: "readback", Waiter: "azure_operation", Readback: "azure_absent"}}
		}
		for _, target := range resourceTypes {
			if target.NativeType == kind.NativeType {
				continue
			}
			relation := "uses"
			if kind.NativeType == vmType && target.NativeType == diskType {
				relation = "attached_to"
			}
			if target.Class == "network.vpc" || target.Class == "network.subnet" {
				relation = "member_of"
			}
			definition.Relationships = append(definition.Relationships, spec.RelationshipSpec{Type: relation, TargetType: target.NativeType, TargetIDPath: referenceKey(target.NativeType)})
		}
		source, err := yaml.Marshal(definition)
		if err != nil {
			return spec.Bundle{}, err
		}
		sources = append(sources, source)
	}
	return spec.CompileBundle(sources, c, spec.HookRegistry{actionHook: {spec.HookPreflight, spec.HookAction, spec.HookWaiter, spec.HookReadback}})
}
