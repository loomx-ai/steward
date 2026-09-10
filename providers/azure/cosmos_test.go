package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func cosmosExample(t *testing.T, operation string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/cosmos/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest []map[string]string
	if json.Unmarshal(payload, &manifest) != nil {
		t.Fatal("invalid source manifest")
	}
	for _, source := range manifest {
		if source["operation"] != operation {
			continue
		}
		payload, err := os.ReadFile("fixtures/cosmos/" + source["file"])
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		if json.Unmarshal(payload, &data) != nil {
			t.Fatal("invalid native example")
		}
		return data
	}
	t.Fatal("missing native example", operation)
	return nil
}
func cosmosExampleResource(t *testing.T, operation string) map[string]any {
	t.Helper()
	return object(object(object(cosmosExample(t, operation)["responses"])["200"])["body"])
}
func cosmosTestAPI(kind string) string {
	suffix := strings.TrimPrefix(kind, cosmosType+"/")
	for prefix, api := range map[string]string{"cassandra": "cassandra", "gremlin": "gremlin", "table": "table", "mongo": "mongo"} {
		if strings.HasPrefix(suffix, prefix) {
			return api
		}
	}
	return "sql"
}

// Compose native example bodies into five separate API accounts. The original
// files retain their placeholder subscriptions, aliases and unrelated names.
// This transport rejects wrong-case requests even when the canonical ARM ID
// happens to match, unlike the shared case-insensitive ARM test server.
func cosmosScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	root := "/subscriptions/" + testSubscription
	group := root + "/resourceGroups/TestGroup"
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
	var raws []map[string]any
	accounts := map[string]map[string]any{}
	for _, api := range []string{"sql", "mongo", "cassandra", "gremlin", "table"} {
		raw := cosmosExampleResource(t, "DatabaseAccounts_Get")
		id := group + "/providers/Microsoft.DocumentDB/databaseAccounts/account-" + api
		raw["id"], raw["name"], raw["type"] = id, last(id), cosmosType
		delete(raw, "identity")
		props := object(raw["properties"])
		props["instanceId"] = api + "-incarnation"
		props["privateEndpointConnections"] = []any{}
		delete(object(props["backupPolicy"]), "migrationState")
		props["capabilities"] = []any{}
		if api == "mongo" {
			raw["kind"] = "MongoDB"
			props["capabilities"] = []any{map[string]any{"name": "EnableMongo"}, map[string]any{"name": "EnableMongoRoleBasedAccessControl"}}
		}
		for name, marker := range map[string]string{"cassandra": "EnableCassandra", "gremlin": "EnableGremlin", "table": "EnableTable"} {
			if api == name {
				props["capabilities"] = []any{map[string]any{"name": marker}}
			}
		}
		accounts[api] = raw
		raws = append(raws, raw)
	}
	pattern := regexp.MustCompile(`\{([^}]+)\}`)
	for _, mapping := range metadata.kinds {
		if !isCosmosType(mapping.NativeType) || mapping.NativeType == cosmosType {
			continue
		}
		op, _ := metadata.catalog.Operation(mapping.ReadOperations[0])
		raw := cosmosExampleResource(t, strings.TrimPrefix(op.ID, "Azure.Microsoft.DocumentDB."))
		params := map[string]string{"subscriptionId": testSubscription, "resourceGroupName": "TestGroup", "accountName": "account-" + cosmosTestAPI(mapping.NativeType), "databaseName": "SalesDB", "containerName": "Orders", "keyspaceName": "sales", "tableName": "Orders", "graphName": "GraphOne", "collectionName": "Orders", "storedProcedureName": "CreateOrder", "triggerName": "ValidateOrder", "userDefinedFunctionName": "ComputeTax", "clientEncryptionKeyName": "EncryptionKey", "clusterName": "cassandra-one", "dataCenterName": "data-center-one", "fleetName": "fleet-one", "fleetspaceName": "fleetspace-one", "fleetspaceAccountName": "account-sql", "mongoRoleDefinitionId": "SalesDB.CustomReader", "mongoUserDefinitionId": "SalesDB.testUser", "roleDefinitionId": "11111111-2222-3333-4444-555555555555", "roleAssignmentId": "22222222-2222-3333-4444-555555555555"}
		id := pattern.ReplaceAllStringFunc(op.Call.Path, func(value string) string {
			if p := params[value[1:len(value)-1]]; p != "" {
				return p
			}
			return "default"
		})
		raw["id"], raw["type"], raw["name"] = id, mapping.NativeType, last(id)
		props := object(raw["properties"])
		if resource := object(props["resource"]); resource != nil {
			resource["id"] = last(id)
		}
		accountID := text(accounts[cosmosTestAPI(mapping.NativeType)]["id"])
		if strings.HasSuffix(mapping.NativeType, "RoleAssignments") {
			props["roleDefinitionId"] = accountID + "/" + strings.TrimSuffix(last(mapping.NativeType), "RoleAssignments") + "RoleDefinitions/" + params["roleDefinitionId"]
			props["scope"] = accountID
		}
		if strings.HasSuffix(mapping.NativeType, "RoleDefinitions") {
			props["assignableScopes"] = []any{accountID}
			props["type"] = "CustomRole"
		}
		if props["id"] != nil {
			props["id"] = id
		}
		switch mapping.NativeType {
		case cosmosTriggerType:
			// The original GET example contains out-of-enum placeholders.
			object(props["resource"])["triggerType"] = "Pre"
			object(props["resource"])["triggerOperation"] = "All"
		case cosmosMongoRoleType:
			props["databaseName"], props["roleName"], props["roles"] = "SalesDB", "CustomReader", []any{}
			props["privileges"] = []any{map[string]any{"resource": map[string]any{"db": "SalesDB", "collection": "Orders"}, "actions": []any{"find"}}}
		case cosmosMongoUserType:
			props["databaseName"], props["userName"] = "SalesDB", "testUser"
			props["roles"] = []any{map[string]any{"db": "SalesDB", "role": "CustomReader"}, map[string]any{"db": "SalesDB", "role": "read"}}
		case cosmosContainerType:
			object(object(props["resource"])["clientEncryptionPolicy"])["includedPaths"] = []any{map[string]any{"path": "/value", "clientEncryptionKeyId": "EncryptionKey", "encryptionAlgorithm": "AEAD_AES_256_CBC_HMAC_SHA256", "encryptionType": "Deterministic"}}
		case cosmosFleetAccountType:
			object(props["globalDatabaseAccountProperties"])["resourceId"] = accounts["sql"]["id"]
		case cosmosPECType:
			object(accounts["sql"]["properties"])["privateEndpointConnections"] = []any{map[string]any{"id": id}}
		}
		raws = append(raws, raw)
	}
	for _, raw := range raws {
		id, kind := text(raw["id"]), text(raw["type"])
		s.add(raw, "2026-03-15")
		parent := cosmosParentID(id)
		path := parent + "/" + last(kind)
		if parent == "" {
			path = root + "/providers/" + kind
		}
		path = strings.ToLower(path)
		s.lists[path] = append(s.lists[path], raw)
		s.version[path] = "2026-03-15"
		for _, child := range cosmosOwnedKinds(kind) {
			applies, err := cosmosChildApplies(child, raw)
			if err != nil {
				t.Fatal(err)
			}
			if !applies {
				continue
			}
			list := strings.ToLower(id + "/" + last(child))
			if _, ok := s.lists[list]; !ok {
				s.lists[list] = []any{}
			}
			s.version[list] = "2026-03-15"
		}
		mapping, _ := findType(kind)
		for _, opID := range mapping.ReadOperations {
			op, _ := metadata.catalog.Operation(opID)
			if strings.HasSuffix(op.Call.Path, "/throughputSettings/default") {
				settingsID := id + "/throughputSettings/default"
				if throughput := object(object(raw["properties"])["options"])["throughput"]; throughput != nil {
					settings := cosmosExampleResource(t, strings.TrimPrefix(op.ID, "Azure.Microsoft.DocumentDB."))
					settings["id"], settings["type"] = settingsID, kind+"/throughputSettings"
					resource := object(object(settings["properties"])["resource"])
					resource["throughput"], resource["offerReplacePending"] = throughput, "false"
					s.add(settings, "2026-03-15")
				} else {
					s.status[strings.ToLower(settingsID)] = 404
				}
				s.version[strings.ToLower(settingsID)] = "2026-03-15"
			}
		}
	}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if !strings.Contains(strings.ToLower(req.URL.Path), "/providers/microsoft.documentdb/") {
			return nil, false
		}
		path := req.URL.Path
		if strings.HasSuffix(strings.ToLower(path), "/throughputsettings/default") {
			path = path[:len(path)-len("/throughputSettings/default")]
		}
		if raw := s.records[strings.ToLower(path)]; raw != nil {
			if !cosmosSameWireID(path, text(raw["id"])) {
				return jsonResponse(404, nil, nil), true
			}
		} else if _, isList := s.lists[strings.ToLower(path)]; isList {
			parent := path[:strings.LastIndex(path, "/")]
			if raw := s.records[strings.ToLower(parent)]; raw != nil && !cosmosSameWireID(parent, text(raw["id"])) {
				return jsonResponse(404, nil, nil), true
			}
		}
		return nil, false
	}
	r := s.runtime(t)
	var assets []asset.Asset
	for _, raw := range raws {
		value := dnsAsset(t, r, raw)
		mapping, _ := findType(value.Identity.NativeType)
		if mapping.ReadOnly || value.Normalized["cleanup_controller_only"] == true {
			value.Capabilities = nil
		}
		assets = append(assets, value)
	}
	return s, r, assets
}
func TestCosmosNativeInventory(t *testing.T) {
	_, r, assets := cosmosScenario(t)
	if len(assets) != 38 {
		t.Fatal("missing Cosmos resources", len(assets))
	}
	seen := map[string]bool{}
	for _, value := range assets {
		kind := value.Identity.NativeType
		if seen[kind] {
			continue
		}
		seen[kind] = true
		t.Run(kind, func(t *testing.T) {
			request := productRequest(r, kind)
			var items []contracts.InventoryItem
			for {
				batch, err := r.List(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				items = append(items, batch.Items...)
				if batch.Complete {
					break
				}
				request.Cursor = batch.NextCursor
			}
			count := 1
			if kind == cosmosType {
				count = 5
			}
			if len(items) != count {
				t.Fatal("wrong API collection membership", len(items), count)
			}
			for _, item := range items {
				if cosmosRegion(kind, item.Raw) != item.Location {
					t.Fatal("wrong deployment scope", item.Location)
				}
				if text(item.Normalized["_cosmos_wire_id"]) == "" || len(object(item.Normalized["_cosmos_ancestors"])) != len(cosmosAncestorIDs(text(item.Normalized["_cosmos_wire_id"]))) {
					t.Fatal("missing native identity or ancestors")
				}
			}
		})
	}
}
func TestCosmosReviewedCleanup(t *testing.T) {
	for _, suffix := range []string{"account-sql", "account-mongo", "account-cassandra", "account-gremlin", "account-table", "cassandra-one", "fleet-one", "SalesDB", "Orders"} {
		t.Run(suffix, func(t *testing.T) {
			s, r, assets := cosmosScenario(t)
			var target asset.Asset
			for _, value := range assets {
				if last(value.Identity.NativeID) == strings.ToLower(suffix) {
					target = value
					break
				}
			}
			request, input := dnsRequest(t, r, assets, target)
			solved, err := plan.Solve(input)
			if err != nil || len(solved.Blockers) != 0 {
				t.Fatal("Cosmos plan", err, solved.Blockers)
			}
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			if len(solved.Steps) > 1 {
				if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
					t.Fatal("parent bypassed reviewed prerequisite deletion")
				}
			}
			for _, step := range solved.Steps {
				index := slices.IndexFunc(assets, func(a asset.Asset) bool { return a.ID == step.AssetID })
				if index < 0 {
					t.Fatal("unknown step")
				}
				value := assets[index]
				driver, err := r.ResolveAction(context.Background(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				request := servicePlanRequest(solved, assets, value)
				_, err = driver.Execute(context.Background(), request)
				if err != nil {
					t.Fatalf("delete %s: %v", value.Identity.NativeType, err)
				}
				// Only controller-owned encryption keys lack a native DELETE.
				for _, impact := range request.LifecycleImpacts {
					s.gone[strings.ToLower(impact.Asset.Identity.NativeID)] = true
				}
				if value.Identity.NativeType == cosmosPECType {
					account := s.records[strings.ToLower(cosmosRootID(value.Identity.NativeID))]
					object(account["properties"])["privateEndpointConnections"] = []any{}
				}
				read, err := driver.Readback(context.Background(), request)
				if err != nil || read.Exists {
					t.Fatalf("readback %s: %+v %v", value.Identity.NativeType, read, err)
				}
			}
			if !s.gone[strings.ToLower(target.Identity.NativeID)] {
				t.Fatal("target survived")
			}
			if suffix == "fleet-one" {
				for _, account := range assets {
					if account.Identity.NativeType == cosmosType && s.gone[account.Identity.NativeID] {
						t.Fatal("fleet removed account")
					}
				}
			}
		})
	}
}
func TestCosmosAPISelectionRejectsConflictingAccounts(t *testing.T) {
	for _, raw := range []map[string]any{
		{"kind": "Parse"},
		{"kind": 123},
		{"kind": "MongoDB", "properties": map[string]any{"capabilities": []any{map[string]any{"name": "EnableCassandra"}}}},
		{"properties": map[string]any{"capabilities": []any{map[string]any{"name": "EnableTable"}, map[string]any{"name": "EnableGremlin"}}}},
		{"properties": map[string]any{"capabilities": []any{map[string]any{"name": "EnableMongoRoleBasedAccessControl"}}}},
		{"properties": map[string]any{"capabilities": "EnableCassandra"}},
		{"properties": map[string]any{"capabilities": []any{map[string]any{"name": "EnableMongo"}, map[string]any{"name": "enablemongo"}}}},
	} {
		if _, _, err := cosmosAPI(raw); err == nil {
			t.Fatal("accepted invalid API markers", raw)
		}
	}
	api, _, err := cosmosAPI(map[string]any{"properties": map[string]any{"capabilities": []any{map[string]any{"name": "EnableServerless"}, map[string]any{"name": "NewNonAPIStorageFeature"}}}})
	if err != nil || api != "sql" {
		t.Fatal("feature selected wrong API", api, err)
	}
}
func TestCosmosInventoryKeepsDataPayloadPrivate(t *testing.T) {
	s, r, assets := cosmosScenario(t)
	for _, kind := range []string{cosmosStoredProcedureType, cosmosKeyType, cosmosDataCenterType, cosmosCassandraType} {
		value := cdnAsset(t, assets, kind)
		raw := s.records[strings.ToLower(value.Identity.NativeID)]
		props := object(raw["properties"])
		secret := "cosmos-private-content-" + fmt.Sprint(len(kind))
		switch kind {
		case cosmosStoredProcedureType:
			object(props["resource"])["body"] = secret
		case cosmosKeyType:
			object(props["resource"])["wrappedDataEncryptionKey"] = secret
		case cosmosDataCenterType:
			props["base64EncodedCassandraYamlFragment"] = secret
		case cosmosCassandraType:
			props["initialCassandraAdminPassword"] = secret
		}
		value = dnsAsset(t, r, raw)
		payload, _ := json.Marshal(value)
		if strings.Contains(string(payload), secret) {
			t.Fatal("persisted private Cosmos content", kind)
		}
		c, _ := r.resolve(context.Background(), "connection")
		if err := c.servicePrivateIncarnation(value, raw); err != nil {
			t.Fatal(err)
		}
		switch kind {
		case cosmosStoredProcedureType:
			object(props["resource"])["body"] = secret + "-changed"
		case cosmosKeyType:
			object(props["resource"])["wrappedDataEncryptionKey"] = secret + "-changed"
		case cosmosDataCenterType:
			props["base64EncodedCassandraYamlFragment"] = secret + "-changed"
		case cosmosCassandraType:
			props["initialCassandraAdminPassword"] = secret + "-changed"
		}
		if err := c.servicePrivateIncarnation(value, raw); err == nil {
			t.Fatal("ignored private Cosmos change", kind)
		}
	}
}
