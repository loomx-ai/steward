package azure

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Both descriptors use resourceID for identity. resourceType/datasourceType
// can be workload labels (for example OssDB), and resourceUri is not an ARM
// endpoint to follow. Non-Azure sources may have opaque backup-service IDs.
func dataProtectionSourceReferences(raw map[string]any) (map[string][]string, error) {
	refs := map[string][]string{}
	props := object(raw["properties"])
	for _, field := range []string{"dataSourceInfo", "dataSourceSetInfo"} {
		if field == "dataSourceSetInfo" && props[field] == nil {
			continue
		}
		descriptor := object(props[field])
		value, ok := descriptor["resourceID"].(string)
		if !ok || value == "" || value != strings.TrimSpace(value) || strings.ContainsAny(value, "\x00\r\n\t") {
			return nil, serviceDenied("invalid_backup_data_source_identity")
		}
		if !strings.HasPrefix(strings.ToLower(value), "/subscriptions/") {
			continue
		}
		id, kind, err := parseID(value)
		if err != nil {
			return nil, serviceDenied("invalid_backup_data_source_arm_identity")
		}
		if registered, exists := findType(kind); exists {
			kind = registered.NativeType
		}
		addReference(refs, kind, id)
	}
	return refs, nil
}

// A source workload is independently owned. References order explicitly
// selected deletions but never authorize its deletion with a backup instance.
// Retained instances carry historical provenance, not a live source dependency.
func (c *client) contributeDataProtectionSources(ctx context.Context, connection asset.ConnectionID, value asset.Asset, assets []asset.Asset) (result governance.Contribution, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	id, err := c.dataProtectionIdentity(value.Identity.NativeID, dataProtectionInstance)
	if err != nil || id != value.Identity.NativeID || value.ID == "" || value.Identity.Provider != asset.ProviderAzure || value.Identity.Partition != "azure" || value.Identity.ConnectionID != connection || value.Identity.NativeType != dataProtectionInstance {
		return result, serviceDenied("invalid_backup_source_graph_identity")
	}
	own, err := c.dataProtectionRead(ctx, id, dataProtectionInstance)
	if err != nil {
		return result, err
	}
	if value.Normalized["_data_protection_configuration"] != c.privateConfiguration(own.data) {
		return result, serviceDenied("backup_source_graph_requires_refresh")
	}
	refs, err := dataProtectionSourceReferences(own.data)
	if err != nil {
		return result, err
	}
	return c.contributeNativeReferences(value, assets, refs, "azure:backup-source-reference")
}
