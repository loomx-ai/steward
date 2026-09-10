package azure

import (
	"context"
	"net/url"
	"strings"
)

const (
	apimVaultType    = "Microsoft.KeyVault/vaults"
	apimIdentityType = "Microsoft.ManagedIdentity/userAssignedIdentities"
)

// The URL is evidence of a vault dependency, never a destination to fetch.
// Preserve only its origin when no resource in this subscription matches it.
func apimVaultOrigin(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || value != strings.TrimSpace(value) || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Port() != "" || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery || u.RawPath != "" {
		return "", serviceDenied("invalid_apim_key_vault_url")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	if (len(parts) != 2 && len(parts) != 3) || parts[0] != "secrets" {
		return "", serviceDenied("invalid_apim_key_vault_secret")
	}
	for _, part := range parts[1:] {
		if part == "" || strings.TrimSpace(part) != part || strings.ContainsAny(part, "\\%?#\x00\r\n") || part == "." || part == ".." {
			return "", serviceDenied("invalid_apim_key_vault_secret")
		}
	}
	return "https://" + strings.ToLower(u.Host), nil
}

func (c *client) apimExternalReference(ctx context.Context, kind, selector string, indexes map[string][]serviceChild) (string, error) {
	if kind == apimIdentityType && !uuidPattern.MatchString(selector) {
		return "", serviceDenied("invalid_apim_identity_client_id")
	}
	values, loaded := indexes[kind]
	if !loaded {
		rows, err := c.subscriptionReferenceIndex(ctx, kind)
		if err != nil {
			return "", err
		}
		for _, row := range rows {
			values = append(values, serviceChild{id: strings.ToLower(text(row["id"])), kind: kind, data: row})
		}
		indexes[kind] = values
	}
	matches := func(raw map[string]any) bool {
		if kind == apimIdentityType {
			return strings.EqualFold(text(object(raw["properties"])["clientId"]), selector)
		}
		endpoint, err := url.Parse(text(object(raw["properties"])["vaultUri"]))
		return err == nil && endpoint.Scheme == "https" && endpoint.User == nil && endpoint.Port() == "" && endpoint.RawQuery == "" && endpoint.Fragment == "" && (endpoint.Path == "" || endpoint.Path == "/") && strings.EqualFold("https://"+endpoint.Host, selector)
	}
	found := ""
	for _, value := range values {
		if !matches(value.data) {
			continue
		}
		live, err := c.linkedResource(ctx, value.id)
		if err != nil {
			return "", err
		}
		if found != "" || !strings.EqualFold(text(live["name"]), last(value.id)) || !matches(live) || serviceListedIncarnation(value.data, live) != nil {
			return "", serviceDenied("apim_external_reference_changed_or_ambiguous")
		}
		found = value.id
	}
	if found == "" {
		if kind == apimIdentityType {
			return "client-id:" + strings.ToLower(selector), nil
		}
		return selector, nil
	}
	return found, nil
}

func (c *client) apimExternalReferences(ctx context.Context, kind string, raw map[string]any, indexes map[string][]serviceChild, refs map[string][]string) error {
	add := func(kind string, value any) error {
		if value == nil || value == "" {
			return nil // Null identifies the service's system-assigned identity.
		}
		selector, ok := value.(string)
		if !ok {
			return serviceDenied("invalid_apim_external_reference")
		}
		if kind == apimVaultType {
			var err error
			selector, err = apimVaultOrigin(selector)
			if err != nil {
				return err
			}
		}
		ref, err := c.apimExternalReference(ctx, kind, selector, indexes)
		if err == nil {
			addReference(refs, kind, ref)
		}
		return err
	}
	props := object(raw["properties"])
	switch strings.ToLower(last(kind)) {
	case "service":
		hostnames := array(props["hostnameConfigurations"])
		if props["hostnameConfigurations"] != nil && hostnames == nil {
			return serviceDenied("invalid_apim_hostname_references")
		}
		for _, hostname := range hostnames {
			if object(hostname) == nil {
				return serviceDenied("invalid_apim_hostname_reference")
			}
			if err := add(apimVaultType, object(hostname)["keyVaultId"]); err != nil {
				return err
			}
			if err := add(apimIdentityType, object(hostname)["identityClientId"]); err != nil {
				return err
			}
		}
	case "namedvalues", "certificates":
		vault := object(props["keyVault"])
		if props["keyVault"] != nil && vault == nil {
			return serviceDenied("invalid_apim_key_vault_reference")
		}
		if err := add(apimVaultType, vault["secretIdentifier"]); err != nil {
			return err
		}
		return add(apimIdentityType, vault["identityClientId"])
	case "loggers":
		value := object(props["credentials"])["identityClientId"]
		if strings.EqualFold(text(value), "SystemAssigned") {
			return nil
		}
		return add(apimIdentityType, value)
	}
	return nil
}
