package azure

import (
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const (
	cosmosType                = "Microsoft.DocumentDB/databaseAccounts"
	cosmosSQLDatabaseType     = cosmosType + "/sqlDatabases"
	cosmosContainerType       = cosmosSQLDatabaseType + "/containers"
	cosmosKeyType             = cosmosSQLDatabaseType + "/clientEncryptionKeys"
	cosmosStoredProcedureType = cosmosContainerType + "/storedProcedures"
	cosmosTriggerType         = cosmosContainerType + "/triggers"
	cosmosFunctionType        = cosmosContainerType + "/userDefinedFunctions"
	cosmosMongoDatabaseType   = cosmosType + "/mongodbDatabases"
	cosmosCollectionType      = cosmosMongoDatabaseType + "/collections"
	cosmosMongoRoleType       = cosmosType + "/mongodbRoleDefinitions"
	cosmosMongoUserType       = cosmosType + "/mongodbUserDefinitions"
	cosmosKeyspaceType        = cosmosType + "/cassandraKeyspaces"
	cosmosCassandraTableType  = cosmosKeyspaceType + "/tables"
	cosmosGremlinDatabaseType = cosmosType + "/gremlinDatabases"
	cosmosGraphType           = cosmosGremlinDatabaseType + "/graphs"
	cosmosTableType           = cosmosType + "/tables"
	cosmosPECType             = cosmosType + "/privateEndpointConnections"
	cosmosServiceType         = cosmosType + "/services"
	cosmosNotebookType        = cosmosType + "/notebookWorkspaces"
	cosmosCassandraType       = "Microsoft.DocumentDB/cassandraClusters"
	cosmosDataCenterType      = cosmosCassandraType + "/dataCenters"
	cosmosFleetType           = "Microsoft.DocumentDB/fleets"
	cosmosFleetspaceType      = cosmosFleetType + "/fleetspaces"
	cosmosFleetAccountType    = cosmosFleetspaceType + "/fleetspaceAccounts"
)

func cosmosKind(kind string) string {
	for _, candidate := range []string{cosmosType, cosmosSQLDatabaseType, cosmosContainerType, cosmosKeyType, cosmosStoredProcedureType, cosmosTriggerType, cosmosFunctionType, cosmosMongoDatabaseType, cosmosCollectionType, cosmosMongoRoleType, cosmosMongoUserType, cosmosKeyspaceType, cosmosCassandraTableType, cosmosGremlinDatabaseType, cosmosGraphType, cosmosTableType, cosmosPECType, cosmosServiceType, cosmosNotebookType, cosmosCassandraType, cosmosDataCenterType, cosmosFleetType, cosmosFleetspaceType, cosmosFleetAccountType} {
		if strings.EqualFold(kind, candidate) {
			return candidate
		}
	}
	for _, api := range []string{"sql", "cassandra", "gremlin", "table", "mongoMI"} {
		for _, suffix := range []string{"RoleAssignments", "RoleDefinitions"} {
			candidate := cosmosType + "/" + api + suffix
			if strings.EqualFold(kind, candidate) {
				return candidate
			}
		}
	}
	return ""
}
func isCosmosType(kind string) bool { return cosmosKind(kind) != "" }

// Cosmos DB data-resource names are case sensitive. ARM graph identities remain
// canonical, while native requests retain each name from the service response.
// https://learn.microsoft.com/azure/cosmos-db/troubleshoot-not-found
func cosmosWireSignature(id string) string {
	parts := strings.Split(id, "/")
	for i := range parts {
		if i < 10 || i%2 == 1 {
			parts[i] = strings.ToLower(parts[i])
		}
	}
	return strings.Join(parts, "/")
}
func cosmosSameWireID(left, right string) bool {
	_, lt, le := parseID(left)
	_, rt, re := parseID(right)
	return le == nil && re == nil && strings.EqualFold(lt, rt) && cosmosWireSignature(left) == cosmosWireSignature(right)
}

// Bind the selector before the first request, including the absence path. A
// modified normalized name must not redirect a readback to a case-only sibling.
func (c *client) cosmosWireBinding(kind, id string) string {
	return c.privateConfiguration(map[string]any{"kind": strings.ToLower(kind), "wire_id": cosmosWireSignature(id)})
}
func (c *client) plannedResourceID(value asset.Asset) (string, error) {
	if !isCosmosType(value.Identity.NativeType) {
		return value.Identity.NativeID, nil
	}
	wire, ok := value.Normalized["_cosmos_wire_id"].(string)
	id, kind, err := parseID(wire)
	if !ok || wire != strings.TrimSpace(wire) || err != nil || !strings.EqualFold(id, value.Identity.NativeID) || !strings.EqualFold(kind, value.Identity.NativeType) || !strings.HasPrefix(id, c.root()+"/") || text(value.Normalized["_cosmos_wire_binding"]) != c.cosmosWireBinding(kind, wire) {
		return "", serviceDenied("cosmos_request_identity_changed")
	}
	return wire, nil
}
func (c *client) plannedResourceURL(value asset.Asset) (string, error) {
	id, err := c.plannedResourceID(value)
	if err != nil {
		return "", err
	}
	kind, ok := findType(value.Identity.NativeType)
	if !ok {
		return "", fmt.Errorf("unknown Azure resource type")
	}
	return c.resourceURL(kind, id)
}

func (a *action) cosmosRequestIdentity(value asset.Asset) error {
	if !isCosmosType(a.kind.NativeType) {
		return nil
	}
	wire, err := a.client.plannedResourceID(value)
	if err != nil {
		return err
	}
	if !cosmosSameWireID(wire, a.wireID) {
		return serviceDenied("cosmos_action_identity_changed")
	}
	return nil
}
