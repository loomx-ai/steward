package azure

import (
	"context"
	"maps"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const rbacPrincipalType = "Microsoft.Graph/servicePrincipals"
const rbacIdentityMetadata = "_rbac_identity"
const rbacIdentityProof = "_rbac_identity_binding"
const rbacUserIdentityType = "Microsoft.ManagedIdentity/userAssignedIdentities"

func rbacPrincipalSelector(tenant, principal string) string {
	return "principal-id:" + strings.ToLower(tenant) + "/" + strings.ToLower(principal)
}

func validRBACPrincipalSelector(kind, reference string) bool {
	parts := strings.Split(strings.TrimPrefix(reference, "principal-id:"), "/")
	return kind == rbacPrincipalType && strings.HasPrefix(reference, "principal-id:") && reference == strings.ToLower(reference) && len(parts) == 2 && uuidPattern.MatchString(parts[0]) && uuidPattern.MatchString(parts[1])
}

func rbacIdentityTarget(value asset.Asset) bool {
	budget, _ := monitorBudgetKind(value.Identity.NativeType)
	return monitorARMTarget(value) && value.Identity.NativeType != groupType && value.Identity.NativeType != diagnosticSettingsType && insightsARMChildKind(value.Identity.NativeType) == "" && budget == "" && resourceReadMethod(value.Identity.NativeType) == "GET"
}

// An attached user-assigned identity has its own lifetime. Only the identity
// resource's properties or a resource's root system identity identify a
// principal that disappears with this resource. clientId is an application ID.
func rbacNativePrincipal(kind string, raw map[string]any) (string, map[string]any, error) {
	if err := monitorRuleFields(raw, "identity", "properties"); err != nil {
		return "", nil, err
	}
	identity := raw["identity"]
	props := object(identity)
	if kind == rbacUserIdentityType {
		props = object(raw["properties"])
	} else if identity != nil {
		if _, ok := identity.(map[string]any); !ok || monitorRuleFields(props, "type", "principalId", "tenantId") != nil {
			return "", nil, serviceDenied("invalid_rbac_native_identity")
		}
		if mode := props["type"]; mode != nil && mode != text(mode) {
			return "", nil, serviceDenied("invalid_rbac_native_identity_type")
		}
		system := false
		modes, seen := strings.Split(text(props["type"]), ","), map[string]bool{}
		for _, mode := range modes {
			mode = strings.TrimSpace(mode)
			if seen[mode] || (mode == "None" || mode == "") && len(modes) != 1 {
				return "", nil, serviceDenied("ambiguous_rbac_native_identity_type")
			}
			seen[mode] = true
			switch mode {
			case "SystemAssigned":
				system = true
			case "UserAssigned", "None", "":
			default:
				return "", nil, serviceDenied("unknown_rbac_native_identity_type")
			}
		}
		if !system {
			if text(props["type"]) == "" && (props["principalId"] != nil || props["tenantId"] != nil) {
				return "", nil, serviceDenied("rbac_principal_identity_type_missing")
			}
			// Some native GETs retain principal fields with UserAssigned/None.
			// The declared identity type determines whether the resource owns it.
			return "", map[string]any{"principalId": "", "tenantId": ""}, nil
		}
	}
	if err := monitorRuleFields(props, "type", "principalId", "tenantId", "clientId"); err != nil {
		return "", nil, err
	}
	for _, field := range []string{"principalId", "tenantId"} {
		if value := props[field]; value != nil && (value != text(value) || text(value) == "") {
			return "", nil, serviceDenied("invalid_rbac_native_principal_field")
		}
	}
	principal, tenant := text(props["principalId"]), text(props["tenantId"])
	snapshot := map[string]any{"principalId": strings.ToLower(principal), "tenantId": strings.ToLower(tenant)}
	if kind == rbacUserIdentityType {
		clientID := text(props["clientId"])
		if !uuidPattern.MatchString(clientID) || props["clientId"] != clientID {
			return "", nil, serviceDenied("invalid_rbac_native_client_id")
		}
		snapshot["clientId"] = strings.ToLower(clientID)
	}
	if kind != rbacUserIdentityType && principal == "" && tenant == "" {
		return "", snapshot, nil // A pending system identity may not yet have a principal.
	}
	if !uuidPattern.MatchString(principal) || !uuidPattern.MatchString(tenant) || props["principalId"] != principal || props["tenantId"] != tenant {
		return "", nil, serviceDenied("invalid_rbac_native_principal")
	}
	return rbacPrincipalSelector(tenant, principal), snapshot, nil
}

func (c *client) rbacIdentityBinding(id, kind string, metadata map[string]any) string {
	return c.privateConfiguration(map[string]any{"id": id, "kind": kind, "tenant": strings.ToLower(c.tenant), "identity": metadata})
}

func (c *client) rbacIdentityInventory(id, kind string, raw, normalized map[string]any) error {
	value := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, NativeID: id, NativeType: kind}}
	if !rbacIdentityTarget(value) {
		return nil
	}
	principal, snapshot, err := rbacNativePrincipal(kind, raw)
	if err != nil {
		return err
	}
	metadata := map[string]any{"principal": principal, "wire_id": responseID(kind, text(raw["id"])), "configuration": c.privateConfiguration(snapshot)}
	normalized[rbacIdentityMetadata] = metadata
	normalized[rbacIdentityProof] = c.rbacIdentityBinding(id, kind, metadata)
	return nil
}

// The authenticated empty identity is significant too: enabling/replacing a
// system identity after review must not escape the incoming-assignment guard.
func (c *client) rbacRecordedIdentity(value asset.Asset) (map[string]any, error) {
	metadata, ok := value.Normalized[rbacIdentityMetadata].(map[string]any)
	wire, wireOK := metadata["wire_id"].(string)
	principal, principalOK := metadata["principal"].(string)
	id, kind, err := parseID(wire)
	if !rbacIdentityTarget(value) || !ok || len(metadata) != 3 || !wireOK || !principalOK || err != nil || id != value.Identity.NativeID || !strings.EqualFold(kind, value.Identity.NativeType) || !strings.HasPrefix(id, c.root()+"/") || wire != responseID(value.Identity.NativeType, wire) || text(metadata["configuration"]) == "" || principal != "" && !validRBACPrincipalSelector(rbacPrincipalType, principal) || text(value.Normalized[rbacIdentityProof]) != c.rbacIdentityBinding(id, value.Identity.NativeType, metadata) {
		return nil, serviceDenied("rbac_identity_proof_missing_or_changed")
	}
	return metadata, nil
}

func (c *client) rbacIdentityRead(ctx context.Context, value asset.Asset) error {
	metadata, err := c.rbacRecordedIdentity(value)
	if err != nil {
		return err
	}
	kind, known := findType(value.Identity.NativeType)
	if !known {
		return serviceDenied("rbac_identity_reader_missing")
	}
	endpoint, err := c.resourceURL(kind, text(metadata["wire_id"]))
	if err != nil {
		return err
	}
	current, err := c.request(ctx, resourceReadMethod(kind.NativeType), endpoint)
	if isNotFound(err) {
		return nil // The saved binding still identifies assignments left behind.
	}
	if err != nil {
		return err
	}
	if !insightsARMReadValid(current, value.Identity.NativeID, kind.NativeType) || responseID(kind.NativeType, text(current.data["id"])) != metadata["wire_id"] {
		return serviceDenied("rbac_native_identity_resource_changed")
	}
	principal, snapshot, err := rbacNativePrincipal(kind.NativeType, current.data)
	if err != nil {
		return err
	}
	if principal != metadata["principal"] || c.privateConfiguration(snapshot) != text(metadata["configuration"]) {
		return serviceDenied("rbac_native_identity_changed")
	}
	return nil
}

func (c *client) rbacPrincipalMatches(target asset.Asset, reference string) (bool, error) {
	if !validRBACPrincipalSelector(rbacPrincipalType, reference) {
		return false, serviceDenied("invalid_rbac_principal_reference")
	}
	if !rbacIdentityTarget(target) {
		return false, nil
	}
	metadata, err := c.rbacRecordedIdentity(target)
	return reference == text(metadata["principal"]), err
}

func (c *client) rbacResolvePrincipals(ctx context.Context, parent asset.Asset, assets []asset.Asset, refs map[string][]string) (map[string][]string, error) {
	resolved := maps.Clone(refs)
	delete(resolved, rbacPrincipalType)
	for _, reference := range refs[rbacPrincipalType] {
		var target *asset.Asset
		for i := range assets {
			candidate := &assets[i]
			if candidate.Identity.Provider != parent.Identity.Provider || candidate.Identity.ConnectionID != parent.Identity.ConnectionID || candidate.Identity.Partition != parent.Identity.Partition || !rbacIdentityTarget(*candidate) {
				continue
			}
			matches, err := c.rbacPrincipalMatches(*candidate, reference)
			if err != nil {
				return nil, err
			}
			if matches {
				if err := c.rbacIdentityRead(ctx, *candidate); err != nil {
					return nil, err
				}
				if target != nil || candidate.ID == "" || candidate.ID == parent.ID {
					return nil, serviceDenied("ambiguous_rbac_principal_resource")
				}
				target = candidate
			}
		}
		if target == nil {
			addReference(resolved, rbacPrincipalType, reference)
		} else {
			addReference(resolved, target.Identity.NativeType, target.Identity.NativeID)
		}
	}
	return resolved, nil
}
