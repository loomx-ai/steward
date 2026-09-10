package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func cosmosName(value any) (string, error) {
	name, ok := value.(string)
	if !ok || name == "" || name != strings.TrimSpace(name) || name == "." || name == ".." || strings.ContainsAny(name, "/\\%?#\x00\r\n") {
		return "", serviceDenied("invalid_cosmos_reference_name")
	}
	return name, nil
}
func cosmosFleetTarget(raw map[string]any) (string, error) {
	value, ok := object(object(raw["properties"])["globalDatabaseAccountProperties"])["resourceId"].(string)
	_, kind, err := parseID(value)
	if !ok || value != strings.TrimSpace(value) || err != nil || !strings.EqualFold(kind, cosmosType) {
		return "", serviceDenied("invalid_cosmos_fleet_account")
	}
	return value, nil
}

// Assignable scopes may deliberately name resources that do not exist yet.
// Validate the account boundary and preserve them as role configuration; they
// do not establish ownership or deletion prerequisites for hypothetical data.
func cosmosValidateScope(root string, value any) error {
	scope, ok := value.(string)
	if !ok || scope == "" || scope != strings.TrimSpace(scope) || strings.ContainsAny(scope, "%?#\\\x00\r\n") {
		return serviceDenied("invalid_cosmos_role_scope")
	}
	if scope == "/" {
		return nil
	}
	if strings.HasPrefix(strings.ToLower(scope), "/subscriptions/") {
		if !strings.EqualFold(strings.TrimSuffix(scope, "/"), root) && !strings.HasPrefix(strings.ToLower(scope), strings.ToLower(root)+"/") {
			return serviceDenied("cosmos_role_scope_outside_account")
		}
		scope = scope[len(root):]
	}
	for _, part := range strings.Split(strings.Trim(scope, "/"), "/") {
		if part == "." || part == ".." {
			return serviceDenied("invalid_cosmos_role_scope")
		}
	}
	return nil
}
func cosmosReferenceIDs(kind string, raw map[string]any) ([]string, error) {
	kind = cosmosKind(kind)
	wire := responseID(kind, text(raw["id"]))
	root := cosmosRootID(wire)
	props := object(raw["properties"])
	refs := []string{}
	if kind == cosmosFleetAccountType {
		target, err := cosmosFleetTarget(raw)
		if err != nil {
			return nil, err
		}
		return []string{target}, nil
	}
	if strings.HasSuffix(kind, "RoleAssignments") {
		value, ok := props["roleDefinitionId"].(string)
		_, typ, err := parseID(value)
		if !ok || value != strings.TrimSpace(value) || err != nil || !strings.EqualFold(typ, strings.TrimSuffix(kind, "RoleAssignments")+"RoleDefinitions") || !cosmosSameWireID(cosmosRootID(value), root) {
			return nil, serviceDenied("invalid_cosmos_role_reference")
		}
		if err := cosmosValidateScope(root, props["scope"]); err != nil {
			return nil, err
		}
		refs = append(refs, value)
	}
	if strings.HasSuffix(kind, "RoleDefinitions") && kind != cosmosMongoRoleType {
		values, ok := props["assignableScopes"].([]any)
		if !ok || len(values) == 0 {
			return nil, serviceDenied("invalid_cosmos_assignable_scopes")
		}
		for _, value := range values {
			if err := cosmosValidateScope(root, value); err != nil {
				return nil, err
			}
		}
	}
	if kind == cosmosMongoRoleType || kind == cosmosMongoUserType {
		database, err := cosmosName(props["databaseName"])
		if err != nil {
			return nil, err
		}
		field := "roleName"
		if kind == cosmosMongoUserType {
			field = "userName"
		}
		name, err := cosmosName(props[field])
		if err != nil {
			return nil, err
		}
		if last(wire) != database+"."+name {
			return nil, serviceDenied("cosmos_mongo_principal_identity_mismatch")
		}
		refs = append(refs, root+"/mongodbDatabases/"+database)
		if value := props["roles"]; value != nil {
			roles, ok := value.([]any)
			if !ok {
				return nil, serviceDenied("invalid_cosmos_inherited_roles")
			}
			for _, value := range roles {
				role := object(value)
				db, err := cosmosName(role["db"])
				if err != nil {
					return nil, err
				}
				name, err := cosmosName(role["role"])
				if err != nil {
					return nil, err
				}
				// These four built-ins exist in every MongoDB database without
				// a provisioned ARM custom-role resource.
				if slices.Contains([]string{"read", "readWrite", "dbAdmin", "dbOwner"}, name) {
					continue
				}
				refs = append(refs, root+"/mongodbRoleDefinitions/"+db+"."+name)
			}
		}
	}
	if kind == cosmosContainerType {
		policy := object(object(props["resource"])["clientEncryptionPolicy"])
		if value := policy["includedPaths"]; value != nil {
			paths, ok := value.([]any)
			if !ok {
				return nil, serviceDenied("invalid_cosmos_encryption_policy")
			}
			for _, path := range paths {
				key, err := cosmosName(object(path)["clientEncryptionKeyId"])
				if err != nil {
					return nil, err
				}
				refs = append(refs, cosmosParentID(wire)+"/clientEncryptionKeys/"+key)
			}
		}
	}
	slices.SortFunc(refs, func(a, b string) int { return strings.Compare(cosmosWireSignature(a), cosmosWireSignature(b)) })
	return slices.CompactFunc(refs, cosmosSameWireID), nil
}
func cosmosIncomingKinds(kind string) []string {
	kind = cosmosKind(kind)
	if strings.HasSuffix(kind, "RoleDefinitions") && kind != cosmosMongoRoleType {
		return []string{strings.TrimSuffix(kind, "RoleDefinitions") + "RoleAssignments"}
	}
	switch kind {
	case cosmosMongoRoleType:
		return []string{cosmosMongoRoleType, cosmosMongoUserType}
	case cosmosMongoDatabaseType:
		return []string{cosmosMongoRoleType, cosmosMongoUserType}
	case cosmosType:
		return []string{cosmosFleetAccountType}
	}
	return nil
}
func cosmosSharedPrerequisite(parent, child asset.Asset) bool {
	if !isCosmosType(parent.Identity.NativeType) || !isCosmosType(child.Identity.NativeType) || !slices.Contains(cosmosIncomingKinds(parent.Identity.NativeType), cosmosKind(child.Identity.NativeType)) {
		return false
	}
	wire := text(parent.Normalized["_cosmos_wire_id"])
	refs, err := cosmosReferenceIDs(child.Identity.NativeType, map[string]any{"id": text(child.Normalized["_cosmos_wire_id"]), "properties": child.Normalized})
	if err != nil {
		return false
	}
	return slices.ContainsFunc(refs, func(ref string) bool { return cosmosSameWireID(ref, wire) })
}
func (c *client) cosmosIncoming(ctx context.Context, target asset.Identity, raw map[string]any) ([]serviceChild, error) {
	if len(cosmosIncomingKinds(target.NativeType)) == 0 {
		return nil, nil
	}
	wire := responseID(target.NativeType, text(raw["id"]))
	if strings.EqualFold(target.NativeType, cosmosType) {
		return c.cosmosFleetAssociations(ctx, wire)
	}
	rootID := cosmosRootID(wire)
	root, err := c.cosmosResource(ctx, rootID)
	if err != nil {
		return nil, err
	}
	kinds := []string{}
	for _, kind := range cosmosIncomingKinds(target.NativeType) {
		applies, err := cosmosChildApplies(kind, root)
		if err != nil {
			return nil, err
		}
		if applies {
			kinds = append(kinds, kind)
		}
	}
	children, err := c.nativeServiceChildren(ctx, asset.Identity{NativeID: strings.ToLower(rootID), NativeType: cosmosType}, root, kinds)
	if err != nil {
		return nil, err
	}
	result := []serviceChild{}
	for _, child := range children {
		refs, err := cosmosReferenceIDs(child.kind, child.data)
		if err != nil {
			return nil, err
		}
		if child.id != strings.ToLower(wire) && slices.ContainsFunc(refs, func(ref string) bool { return cosmosSameWireID(ref, wire) }) {
			child.direct = true
			result = append(result, child)
		}
	}
	return result, nil
}
func (c *client) cosmosFleetAssociations(ctx context.Context, account string) ([]serviceChild, error) {
	mapping, _ := findType(cosmosFleetType)
	records, err := c.listAll(ctx, c.root()+"/providers/Microsoft.DocumentDB/fleets", mapping.Version)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	result := []serviceChild{}
	for _, value := range records {
		listed := object(value)
		wire := responseID(cosmosFleetType, text(listed["id"]))
		id, kind, err := parseID(wire)
		if err != nil || !strings.EqualFold(kind, cosmosFleetType) || !validResponseType(cosmosFleetType, text(listed["type"])) || !strings.HasPrefix(id, c.root()+"/") || seen[id] {
			return nil, serviceDenied("invalid_cosmos_fleet_list")
		}
		seen[id] = true
		fleet, err := c.cosmosResource(ctx, wire)
		if err != nil {
			return nil, err
		}
		if err := cosmosListedIncarnation(cosmosFleetType, listed, fleet); err != nil {
			return nil, err
		}
		spaces, err := c.nativeServiceChildren(ctx, asset.Identity{NativeID: id, NativeType: cosmosFleetType}, fleet, []string{cosmosFleetspaceType})
		if err != nil {
			return nil, err
		}
		for _, space := range spaces {
			links, err := c.nativeServiceChildren(ctx, asset.Identity{NativeID: space.id, NativeType: space.kind}, space.data, []string{cosmosFleetAccountType})
			if err != nil {
				return nil, err
			}
			for _, link := range links {
				target, err := cosmosFleetTarget(link.data)
				if err != nil {
					return nil, err
				}
				if cosmosSameWireID(target, account) {
					link.direct = true
					result = append(result, link)
				}
			}
		}
		current, err := c.cosmosResource(ctx, wire)
		if err != nil {
			return nil, err
		}
		if c.privateConfiguration(cosmosSnapshot(cosmosFleetType, fleet)) != c.privateConfiguration(cosmosSnapshot(cosmosFleetType, current)) {
			return nil, serviceDenied("cosmos_fleet_changed")
		}
	}
	return result, nil
}
