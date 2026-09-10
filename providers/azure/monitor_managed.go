package azure

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// A managed-group member must retain the same private configuration and native
// receiver resolution as its scan. A generic ARM GET does not establish the
// product's complete read contract. A nil group is used only for residual reads
// after the caller has established native resource-group absence.
func (c *client) monitorManagedIncarnation(ctx context.Context, planned asset.Asset, observed, group map[string]any) (err error) {
	if monitorResourceKind(planned.Identity.NativeType) == "" {
		return nil
	}
	defer func() { err = contracts.DependencyReadError(err) }()
	id, scope, kind, err := monitorResourceID(planned.Identity.NativeID)
	if err != nil || id != planned.Identity.NativeID || kind != planned.Identity.NativeType || planned.Identity.Provider != asset.ProviderAzure || !strings.HasPrefix(scope, c.root()+"/resourcegroups/") {
		return serviceDenied("invalid_managed_monitor_identity")
	}
	if err := c.servicePrivateIncarnation(planned, observed); err != nil {
		return err
	}
	if group != nil && (!strings.EqualFold(text(group["id"]), scope) || text(planned.Normalized[monitorGroupProof]) != c.privateConfiguration(insightsWorkspaceResourceSnapshot(group))) {
		return serviceDenied("managed_monitor_resource_group_changed")
	}
	current, err := c.monitorResourceRead(ctx, kind, id)
	if err != nil {
		return err
	}
	if monitorResourceRegion(kind, current.data) != planned.Location {
		return serviceDenied("managed_monitor_location_changed")
	}
	if err := c.servicePrivateIncarnation(planned, current.data); err != nil {
		return err
	}
	refs, err := c.monitorReferences(ctx, kind, id, current.data)
	if err != nil {
		return err
	}
	return c.monitorReferencesUnchanged(planned, refs)
}
