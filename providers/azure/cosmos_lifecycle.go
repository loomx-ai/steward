package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func cosmosOwnedKinds(kind string) []string {
	switch cosmosKind(kind) {
	case cosmosType:
		children := []string{cosmosSQLDatabaseType, cosmosMongoDatabaseType, cosmosKeyspaceType, cosmosGremlinDatabaseType, cosmosTableType, cosmosMongoRoleType, cosmosMongoUserType, cosmosPECType, cosmosServiceType, cosmosNotebookType}
		for _, api := range []string{"sql", "cassandra", "gremlin", "table", "mongoMI"} {
			children = append(children, cosmosType+"/"+api+"RoleAssignments", cosmosType+"/"+api+"RoleDefinitions")
		}
		return children
	case cosmosSQLDatabaseType:
		return []string{cosmosContainerType, cosmosKeyType}
	case cosmosContainerType:
		return []string{cosmosStoredProcedureType, cosmosTriggerType, cosmosFunctionType}
	case cosmosMongoDatabaseType:
		return []string{cosmosCollectionType}
	case cosmosKeyspaceType:
		return []string{cosmosCassandraTableType}
	case cosmosGremlinDatabaseType:
		return []string{cosmosGraphType}
	case cosmosCassandraType:
		return []string{cosmosDataCenterType}
	case cosmosFleetType:
		return []string{cosmosFleetspaceType}
	case cosmosFleetspaceType:
		return []string{cosmosFleetAccountType}
	}
	return nil
}
func cosmosCascadeKinds(kind string) []string {
	return append(cosmosOwnedKinds(kind), cosmosIncomingKinds(kind)...)
}
func cosmosPrerequisiteKind(parent, child string) bool {
	return cosmosKind(child) != cosmosKeyType && slices.Contains(cosmosCascadeKinds(parent), cosmosKind(child))
}
func cosmosIntrinsicChild(parent, child, reason string) bool {
	return (cosmosKind(parent) == cosmosType && strings.HasSuffix(child, "RoleDefinitions") && reason == "azure_cosmos_builtin_role") || (cosmosKind(parent) == cosmosSQLDatabaseType && cosmosKind(child) == cosmosKeyType && reason == "azure_cosmos_managed_encryption_key")
}
func (c *client) cosmosChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	collect := func() ([]serviceChild, error) {
		kinds := []string{}
		for _, kind := range cosmosOwnedKinds(parent.NativeType) {
			applies, err := cosmosChildApplies(kind, raw)
			if err != nil {
				return nil, err
			}
			if applies {
				kinds = append(kinds, kind)
			}
		}
		children, err := c.nativeServiceChildren(ctx, parent, raw, kinds)
		if err != nil {
			return nil, err
		}
		incoming, err := c.cosmosIncoming(ctx, parent, raw)
		if err != nil {
			return nil, err
		}
		children = append(children, incoming...)
		if strings.EqualFold(parent.NativeType, cosmosType) && object(raw["properties"])["privateEndpointConnections"] != nil {
			listed, err := cosmosPECIndexes(cosmosType, raw)
			if err != nil {
				return nil, err
			}
			var actual []string
			for _, child := range children {
				if child.kind == cosmosPECType {
					actual = append(actual, child.id)
				}
			}
			slices.Sort(actual)
			if !slices.Equal(listed, actual) {
				return nil, serviceDenied("cosmos_private_endpoint_indexes_disagree")
			}
		}
		slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
		for i := 1; i < len(children); i++ {
			if children[i-1].id == children[i].id {
				return nil, serviceDenied("cosmos_duplicate_child")
			}
		}
		return children, nil
	}
	first, err := collect()
	if err != nil {
		return nil, err
	}
	second, err := collect()
	if err != nil {
		return nil, err
	}
	if !slices.EqualFunc(first, second, func(a, b serviceChild) bool {
		return a.id == b.id && c.privateConfiguration(cosmosSnapshot(a.kind, a.data)) == c.privateConfiguration(cosmosSnapshot(b.kind, b.data))
	}) {
		return nil, serviceDenied("cosmos_children_changed")
	}
	return second, nil
}
