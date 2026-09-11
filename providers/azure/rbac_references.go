package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) rbacReferences(kind, id string, raw map[string]any) (map[string][]string, error) {
	actual, err := c.rbacValidate(kind, raw)
	if err != nil || actual != id {
		return nil, serviceDenied("invalid_rbac_reference_source")
	}
	refs := map[string][]string{}
	addScope := func(wire string) error {
		scope, err := rbacScope(wire)
		if err != nil {
			return err
		}
		if scope == "/" || len(strings.Split(scope, "/")) == 3 || strings.HasPrefix(scope, "/providers/microsoft.management/managementgroups/") {
			return nil
		}
		parts := strings.Split(scope, "/")
		for end := len(parts); end >= 5; end -= 2 {
			if end < len(parts) && parts[end-2] == "providers" {
				continue
			}
			candidate, typ, err := parseID(strings.Join(parts[:end], "/"))
			if err != nil {
				return serviceDenied("invalid_rbac_reference_scope")
			}
			if mapping, ok := findType(typ); ok {
				typ = mapping.NativeType
			}
			if candidate == id {
				return serviceDenied("rbac_reference_cycle")
			}
			addReference(refs, typ, candidate)
		}
		return nil
	}
	scopes, err := c.rbacScopes(kind, raw)
	if err != nil {
		return nil, err
	}
	for _, scope := range scopes {
		if err := addScope(scope); err != nil {
			return nil, err
		}
	}
	if kind == rbacAssignmentType {
		props := object(raw["properties"])
		if props["principalType"] == "ServicePrincipal" {
			addReference(refs, rbacPrincipalType, rbacPrincipalSelector(c.tenant, text(props["principalId"])))
		}
		role, err := c.rbacRoleID(text(props["roleDefinitionId"]))
		if err != nil {
			return nil, err
		}
		addReference(refs, rbacRoleType, role)
		if delegated := text(props["delegatedManagedIdentityResourceId"]); delegated != "" {
			if err := addScope(delegated); err != nil {
				return nil, err
			}
		}
	}
	return refs, nil
}

func (c *client) rbacRecordedReferences(value asset.Asset) (map[string]any, error) {
	id, _, kind, err := rbacResourceID(value.Identity.NativeID)
	refs, ok := value.Normalized["_rbac_references"].(map[string]any)
	wire := text(value.Normalized[rbacWireSelector])
	selector, wireErr := c.rbacWireID(wire)
	configuration, context := text(value.Normalized[rbacConfigurationProof]), text(value.Normalized[rbacContextProof])
	if err != nil || wireErr != nil || id != value.Identity.NativeID || selector != wire || strings.ToLower(wire) != id || rbacResourceKind(kind) != value.Identity.NativeType || !strings.HasPrefix(id, c.root()+"/") || !ok || configuration == "" || context == "" {
		return nil, serviceDenied("rbac_reference_proof_missing")
	}
	for kind, value := range refs {
		ids := stringValues(value)
		switch typed := value.(type) {
		case []string:
		case []any:
			if len(typed) != len(ids) {
				return nil, serviceDenied("invalid_rbac_recorded_references")
			}
		default:
			return nil, serviceDenied("invalid_rbac_recorded_references")
		}
		if len(ids) == 0 {
			return nil, serviceDenied("empty_rbac_recorded_reference")
		}
		seen := map[string]bool{}
		for _, wire := range ids {
			if kind == rbacPrincipalType {
				if !validRBACPrincipalSelector(kind, wire) || seen[wire] {
					return nil, serviceDenied("invalid_rbac_recorded_principal_reference")
				}
				seen[wire] = true
				continue
			}
			canonical, typ, err := parseID(wire)
			if kind == rbacRoleType {
				canonical, _, typ, err = rbacResourceID(wire)
			}
			if err != nil || wire != canonical || !strings.EqualFold(typ, kind) || seen[wire] {
				return nil, serviceDenied("invalid_rbac_recorded_reference_identity")
			}
			seen[wire] = true
		}
	}
	if text(value.Normalized[rbacReferencesProof]) != c.rbacReferenceBinding(id, kind, wire, configuration, context, refs) {
		return nil, serviceDenied("rbac_recorded_reference_changed")
	}
	return refs, nil
}

func (c *client) contributeRBACReferences(ctx context.Context, parent asset.Asset, assets []asset.Asset) (contribution governance.Contribution, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	driver, err := newRBACAction(c, parent.Identity.ConnectionID, parent)
	if err != nil {
		return contribution, err
	}
	var current response
	for range 2 {
		current, _, err = driver.current(ctx)
		if err != nil {
			return contribution, err
		}
	}
	refs, err := c.rbacReferences(parent.Identity.NativeType, parent.Identity.NativeID, current.data)
	if err != nil {
		return contribution, err
	}
	refs, err = c.rbacResolvePrincipals(ctx, parent, assets, refs)
	if err != nil {
		return contribution, err
	}
	contribution, err = c.contributeNativeReferences(parent, assets, refs, "azure:rbac-reference")
	if err != nil {
		return contribution, err
	}
	for _, reference := range slices.Clone(contribution.Relationships) {
		contribution.Relationships = append(contribution.Relationships, graph.Relationship{SourceAssetID: reference.TargetAssetID, TargetAssetID: parent.ID, Type: graph.RelationshipDependsOn, Source: "azure:rbac-required-cleanup", Confidence: 1, Evidence: map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "resource_type": parent.Identity.NativeType, "instance_id": parent.Identity.NativeID}})
	}
	return contribution, nil
}
