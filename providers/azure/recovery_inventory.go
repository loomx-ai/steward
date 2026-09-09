package azure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func recoveryType(kind string) bool {
	return kind == serviceBusRecoveryType || kind == eventHubRecoveryType
}

func recoveryConfiguration(raw map[string]any) string {
	properties := object(safePayload(raw)["properties"])
	for _, field := range []string{"partnerNamespace", "role", "provisioningState", "pendingReplicationOperationsCount"} {
		delete(properties, field)
	}
	payload, _ := json.Marshal(properties)
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

// Older native responses contain only a namespace name. Resolve it with the
// product API across the subscription; never assume the peer's resource group.
func (c *client) recoveryNamespace(ctx context.Context, namespaceType, value string) (out response, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	id := value
	if !strings.HasPrefix(strings.ToLower(value), "/subscriptions/") {
		if value == "" || strings.ContainsAny(value, "/\\.:@%?# \t\r\n\x00") {
			return response{}, serviceDenied("invalid_recovery_namespace")
		}
		metadata, err := providerData()
		if err != nil {
			return response{}, err
		}
		runtime := &Runtime{bundle: metadata.bundle}
		definition, ok := runtime.productDefinition(namespaceType)
		if !ok || definition.Discovery.List == nil {
			return response{}, fmt.Errorf("Azure recovery namespace discovery is unavailable")
		}
		bound, err := c.bindProductList(definition.Discovery.List, "", contracts.InventoryItem{})
		if err != nil {
			return response{}, err
		}
		u, _ := url.Parse(bound.URL)
		records, err := c.listAllURL(ctx, bound.URL, u.Path)
		if err != nil {
			return response{}, err
		}
		id = ""
		seen := map[string]bool{}
		for _, record := range records {
			raw := object(record)
			candidate, kind, err := parseID(text(raw["id"]))
			if err != nil || !strings.EqualFold(kind, namespaceType) || !strings.HasPrefix(candidate, c.root()+"/") || !validResponseType(namespaceType, text(raw["type"])) || seen[candidate] {
				return response{}, serviceDenied("invalid_recovery_namespace")
			}
			seen[candidate] = true
			if strings.EqualFold(last(candidate), value) {
				if id != "" {
					return response{}, serviceDenied("ambiguous_recovery_namespace")
				}
				id = candidate
			}
		}
		if id == "" {
			return response{}, serviceDenied("recovery_namespace_not_found")
		}
	}
	id, kind, err := parseID(id)
	if err != nil || !strings.EqualFold(kind, namespaceType) || !strings.HasPrefix(id, c.root()+"/") {
		return response{}, serviceDenied("invalid_recovery_namespace")
	}
	definition, ok := findType(namespaceType)
	if !ok || (namespaceType != serviceBusNamespaceType && namespaceType != eventHubNamespaceType) {
		return response{}, serviceDenied("invalid_recovery_namespace")
	}
	endpoint, err := c.resourceURL(definition, id)
	if err != nil {
		return response{}, err
	}
	live, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return response{}, err
	}
	if !validResourceResponse(live, id, namespaceType) {
		return response{}, fmt.Errorf("Azure recovery namespace identity mismatch")
	}
	live.data["id"] = id
	return live, nil
}

func (c *client) recoveryInventory(ctx context.Context, kind, id string, raw, normalized map[string]any, refs map[string][]string) error {
	namespaceType := strings.TrimSuffix(kind, "/disasterRecoveryConfigs")
	namespaceID := strings.Join(strings.Split(id, "/")[:9], "/")
	own, err := c.recoveryNamespace(ctx, namespaceType, namespaceID)
	if err != nil {
		return err
	}
	normalized["_recovery_namespace_creation"] = creationGeneration(own.data)
	normalized["_recovery_configuration"] = recoveryConfiguration(raw)
	properties := object(raw["properties"])
	partner := properties["partnerNamespace"]
	if partner == nil || partner == "" {
		return nil
	}
	peer, err := c.recoveryNamespace(ctx, namespaceType, text(partner))
	if err != nil {
		return err
	}
	peerID := text(peer.data["id"])
	if namespaceID == peerID {
		return serviceDenied("invalid_recovery_namespace")
	}
	peerAliasID := peerID + "/disasterrecoveryconfigs/" + last(id)
	definition, _ := findType(kind)
	endpoint, err := c.resourceURL(definition, peerAliasID)
	if err != nil {
		return err
	}
	peerAlias, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return contracts.DependencyReadError(err)
	}
	if !validResourceResponse(peerAlias, peerAliasID, kind) {
		return serviceDenied("recovery_peer_alias_changed")
	}
	peerProperties := object(peerAlias.data["properties"])
	backlink, err := c.recoveryNamespace(ctx, namespaceType, text(peerProperties["partnerNamespace"]))
	if err != nil {
		return err
	}
	role, peerRole := text(properties["role"]), text(peerProperties["role"])
	if text(backlink.data["id"]) != namespaceID || creationGeneration(backlink.data) != creationGeneration(own.data) || !((strings.EqualFold(role, "Primary") && strings.EqualFold(peerRole, "Secondary")) || (strings.EqualFold(role, "Secondary") && strings.EqualFold(peerRole, "Primary"))) {
		return serviceDenied("recovery_pair_changed")
	}
	if err := c.verifyProductParent(ctx, productTarget{ParentID: peerID, ParentType: namespaceType, Generation: productGeneration(peer.data)}); err != nil {
		return err
	}
	endpoint, err = c.resourceURL(definition, id)
	if err != nil {
		return err
	}
	current, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return contracts.DependencyReadError(err)
	}
	currentProperties := object(current.data["properties"])
	if !validResourceResponse(current, id, kind) || recoveryConfiguration(current.data) != recoveryConfiguration(raw) || !strings.EqualFold(text(currentProperties["role"]), role) || !strings.EqualFold(text(currentProperties["partnerNamespace"]), text(partner)) {
		return serviceDenied("recovery_pair_changed")
	}
	normalized["_recovery_partner_namespace"] = peerID
	normalized["_recovery_peer_alias"] = peerAliasID
	normalized["_recovery_peer_namespace_creation"] = creationGeneration(peer.data)
	normalized["_recovery_peer_configuration"] = recoveryConfiguration(peerAlias.data)
	normalized["_recovery_peer_role"] = peerRole
	addReference(refs, namespaceType, peerID)
	return nil
}
