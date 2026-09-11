package azure

import (
	"context"
	"maps"
	"slices"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func communicationMergeIncoming(target, source map[string]any) error {
	for domain, value := range source {
		accounts, ok := value.(map[string]any)
		if !ok || accounts == nil {
			return serviceDenied("invalid_communication_incoming_links")
		}
		if target[domain] == nil {
			target[domain] = map[string]any{}
		}
		for id, configuration := range accounts {
			previous := object(target[domain])[id]
			if text(configuration) == "" || previous != nil && previous != configuration {
				return serviceDenied("communication_incoming_link_conflict")
			}
			object(target[domain])[id] = configuration
		}
	}
	return nil
}

// Domain connections are shared references. Enumerate this subscription's
// account index even when the user selected only an Email Services resource.
// An omitted, previously linked account still needs its own authoritative GET.
func (c *client) communicationIncoming(ctx context.Context, domains map[string]bool, known map[string]any) (map[string]any, map[string]map[string]any, error) {
	result := map[string]any{}
	for domain := range domains {
		if c.communicationIdentity(domain, communicationDomainType) != nil {
			return nil, nil, serviceDenied("invalid_communication_incoming_domain")
		}
		result[domain] = map[string]any{}
	}
	if len(domains) == 0 {
		return result, nil, nil
	}
	accounts, err := c.communicationARMIndex(ctx, communicationType, nil)
	if err != nil {
		return nil, nil, err
	}
	for _, domain := range slices.Sorted(maps.Keys(known)) {
		if c.communicationIdentity(domain, communicationDomainType) != nil {
			return nil, nil, serviceDenied("invalid_communication_recorded_incoming_domain")
		}
		if !domains[domain] {
			continue
		}
		entries, ok := known[domain].(map[string]any)
		if !ok || entries == nil {
			return nil, nil, serviceDenied("invalid_communication_recorded_incoming_accounts")
		}
		for _, id := range slices.Sorted(maps.Keys(entries)) {
			if c.communicationIdentity(id, communicationType) != nil || text(entries[id]) == "" {
				return nil, nil, serviceDenied("invalid_communication_recorded_incoming_account")
			}
			if accounts[id] != nil {
				continue
			}
			raw, err := c.communicationARMRead(ctx, id, communicationType)
			if isNotFound(err) {
				continue
			}
			if err != nil {
				return nil, nil, err
			}
			accounts[id] = raw
		}
	}
	for _, id := range slices.Sorted(maps.Keys(accounts)) {
		raw := accounts[id]
		refs, err := communicationReferences(communicationMember{id: id, kind: communicationType, raw: raw})
		if err != nil {
			return nil, nil, err
		}
		for _, domain := range refs[communicationDomainType] {
			if domains[domain] {
				object(result[domain])[id] = c.privateConfiguration(communicationSnapshot(communicationType, raw))
			}
		}
	}
	return result, accounts, nil
}

func (c *client) communicationAsset(value asset.Asset) error {
	if value.ID == "" || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID == "" || value.Normalized["_communication_connection"] != string(value.Identity.ConnectionID) || value.Identity.Partition != "azure" || value.Location != "global" {
		return serviceDenied("invalid_communication_asset_identity")
	}
	_, err := c.communicationRecorded(value.Identity.NativeID, value.Identity.NativeType, value.Normalized)
	return err
}
