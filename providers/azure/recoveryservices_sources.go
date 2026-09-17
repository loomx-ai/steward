package azure

import (
	"context"
	"net/url"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// These schema fields identify independently owned ARM resources. Workload names
// and HANA host/instance labels do not identify an ARM resource and are not used.
func recoveryServicesSourceReferences(raw map[string]any) (map[string][]string, error) {
	refs := map[string][]string{}
	props := object(raw["properties"])
	primary := ""
	for _, field := range []string{"sourceResourceId", "virtualMachineId"} {
		value, exists := props[field]
		if !exists || value == nil || value == "" {
			continue
		}
		id, ok := value.(string)
		if !ok || id != strings.TrimSpace(id) || strings.ContainsAny(id, "\x00\r\n\t") {
			return nil, serviceDenied("invalid_recovery_source_identity")
		}
		if strings.HasPrefix(strings.ToLower(id), "https:") {
			u, err := url.Parse(id)
			if err != nil || u.Scheme != "https" || !strings.EqualFold(u.Host, "management.azure.com") || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawFragment != "" || u.RawPath != "" {
				return nil, serviceDenied("invalid_recovery_source_url")
			}
			id = u.Path
		}
		canonical, kind, err := parseID(id)
		if err != nil || field == "virtualMachineId" && !strings.EqualFold(kind, "Microsoft.Compute/virtualMachines") {
			return nil, serviceDenied("invalid_recovery_source_arm_identity")
		}
		if field == "virtualMachineId" && primary != "" && primary != canonical {
			return nil, serviceDenied("ambiguous_recovery_source_identity")
		}
		primary = canonical
		if registered, ok := findType(kind); ok {
			kind = registered.NativeType
		}
		addReference(refs, kind, canonical)
	}
	return refs, nil
}

func (c *client) contributeRecoverySources(ctx context.Context, connection asset.ConnectionID, value asset.Asset, assets []asset.Asset) (result governance.Contribution, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	kind := value.Identity.NativeType
	if value.Identity.Provider != asset.ProviderAzure || value.Identity.Partition != "azure" || value.Identity.ConnectionID != connection || value.ID == "" || kind != recoveryServicesItem && kind != recoveryServicesContainer {
		return result, serviceDenied("invalid_recovery_source_graph_identity")
	}
	id, err := c.recoveryServicesIdentity(value.Identity.NativeID, kind)
	if err != nil || id != value.Identity.NativeID {
		return result, serviceDenied("invalid_recovery_source_graph_owner")
	}
	own, err := c.recoveryServicesRead(ctx, id, kind)
	if err != nil {
		return result, err
	}
	if value.Normalized["_recovery_services_configuration"] != c.privateConfiguration(own.data) {
		return result, serviceDenied("recovery_source_graph_requires_refresh")
	}
	retained := object(own.data["properties"])["isScheduledForDeferredDelete"] == true
	plannedRetained := value.Normalized["retained"] == true
	if kind == recoveryServicesContainer {
		retained = object(own.data["properties"])["registrationStatus"] == "SoftDeleted"
		plannedRetained = value.Normalized["state"] == "SoftDeleted"
	}
	if plannedRetained != retained {
		return result, serviceDenied("recovery_source_retention_changed")
	}
	// Retained backup provenance does not imply that its source remains live.
	if retained {
		return result, nil
	}
	refs, err := recoveryServicesSourceReferences(own.data)
	if err != nil {
		return result, err
	}
	return c.contributeNativeReferences(value, assets, refs, "azure:recovery-source-reference")
}
