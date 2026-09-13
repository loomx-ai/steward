package azure

import (
	"slices"
	"strings"
)

// Native ARM references only. A Key Vault URI is a data-plane address and
// cannot identify the vault's resource group. Controllers are not ownership.
func elasticSanReferences(id, kind string, raw map[string]any) (map[string][]string, error) {
	refs := map[string][]string{}
	add := func(value any, expected string) error {
		if value == nil || value == "" {
			return nil
		}
		wire, ok := value.(string)
		target, typ, err := parseID(wire)
		if !ok || wire != strings.TrimSpace(wire) || err != nil || target == id || expected != "" && !strings.EqualFold(typ, expected) {
			return serviceDenied("invalid_elastic_san_reference")
		}
		if mapping, ok := findType(typ); ok {
			typ = mapping.NativeType
		}
		refs[typ] = append(refs[typ], target)
		return nil
	}
	if parent := elasticSanParent(id, kind); parent != "" {
		if err := add(parent, ""); err != nil {
			return nil, err
		}
	}
	if err := add(raw["managedBy"], ""); err != nil {
		return nil, err
	}
	props := object(raw["properties"])
	for _, entry := range []struct{ field, key, kind string }{
		{"managedBy", "resourceId", ""},
		{"creationData", "sourceId", ""},
		{"privateEndpoint", "id", "Microsoft.Network/privateEndpoints"},
	} {
		if value := props[entry.field]; value != nil {
			obj := object(value)
			if obj == nil {
				return nil, serviceDenied("invalid_elastic_san_reference_object")
			}
			expected := entry.kind
			if entry.field == "creationData" && kind == elasticSanSnapshotType {
				expected = elasticSanVolumeType
			}
			if err := add(obj[entry.key], expected); err != nil {
				return nil, err
			}
		}
	}
	if kind == elasticSanEndpointType {
		for _, value := range array(props["groupIds"]) {
			// Preserve opaque labels as display data. Only actual native IDs create
			// graph edges; cleanup separately requires a fully resolved group mapping.
			if strings.HasPrefix(text(value), "/") {
				target, typ, err := parseID(text(value))
				if err != nil || !strings.EqualFold(typ, elasticSanGroupType) || elasticSanRoot(target) != elasticSanRoot(id) {
					return nil, serviceDenied("invalid_elastic_san_endpoint_group_reference")
				}
				if err := add(value, elasticSanGroupType); err != nil {
					return nil, err
				}
			}
		}
	}

	if value := props["networkAcls"]; value != nil {
		acl := object(value)
		if acl == nil {
			return nil, serviceDenied("invalid_elastic_san_network_acl")
		}
		if value := acl["virtualNetworkRules"]; value != nil {
			rules, ok := value.([]any)
			if !ok {
				return nil, serviceDenied("invalid_elastic_san_network_rules")
			}
			for _, value := range rules {
				rule := object(value)
				if rule == nil || text(rule["id"]) == "" {
					return nil, serviceDenied("invalid_elastic_san_network_rule")
				}
				if err := add(rule["id"], subnetType); err != nil {
					return nil, err
				}
			}
		}
	}
	if value := props["privateEndpointConnections"]; value != nil {
		connections, ok := value.([]any)
		if !ok {
			return nil, serviceDenied("invalid_elastic_san_endpoint_connections")
		}
		for _, value := range connections {
			connection := object(value)
			if connection == nil {
				return nil, serviceDenied("invalid_elastic_san_endpoint_connection")
			}
			if err := add(connection["id"], elasticSanEndpointType); err != nil {
				return nil, err
			}
			if value := object(connection["properties"])["privateEndpoint"]; value != nil {
				if object(value) == nil {
					return nil, serviceDenied("invalid_elastic_san_private_endpoint")
				}
				if err := add(object(value)["id"], "Microsoft.Network/privateEndpoints"); err != nil {
					return nil, err
				}
			}
		}
	}
	if value := raw["identity"]; value != nil {
		identity := object(value)
		if identity == nil {
			return nil, serviceDenied("invalid_elastic_san_identity_object")
		}
		if value := identity["userAssignedIdentities"]; value != nil {
			identities := object(value)
			if identities == nil {
				return nil, serviceDenied("invalid_elastic_san_user_identities")
			}
			for id := range identities {
				if err := add(id, "Microsoft.ManagedIdentity/userAssignedIdentities"); err != nil {
					return nil, err
				}
			}
		}
	}
	if value := props["encryptionProperties"]; value != nil {
		encryption := object(value)
		if encryption == nil {
			return nil, serviceDenied("invalid_elastic_san_encryption_object")
		}
		if value := encryption["identity"]; value != nil {
			if object(value) == nil {
				return nil, serviceDenied("invalid_elastic_san_encryption_identity")
			}
			if err := add(object(value)["userAssignedIdentity"], "Microsoft.ManagedIdentity/userAssignedIdentities"); err != nil {
				return nil, err
			}
		}
	}
	for kind, ids := range refs {
		slices.Sort(ids)
		refs[kind] = slices.Compact(ids)
	}
	return refs, nil
}
