package azure

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestCosmosNativeRequestNamesAndAliases(t *testing.T) {
	c := &client{subscription: testSubscription}
	root := "/subscriptions/" + testSubscription + "/resourceGroups/GroupOne/providers/Microsoft.DocumentDB/databaseAccounts/account-one"
	for _, test := range []struct{ kind, tail, alias string }{
		{cosmosSQLDatabaseType, "/sqlDatabases/Sales", ""},
		{cosmosContainerType, "/sqlDatabases/Sales/containers/Orders", "/sqlDatabases/Sales/sqlContainers/Orders"},
		{cosmosStoredProcedureType, "/sqlDatabases/Sales/containers/Orders/storedProcedures/CreateOrder", "/sqlDatabases/Sales/sqlContainers/Orders/sqlStoredProcedures/CreateOrder"},
		{cosmosTriggerType, "/sqlDatabases/Sales/containers/Orders/triggers/ValidateOrder", "/sqlDatabases/Sales/sqlContainers/Orders/sqlTriggers/ValidateOrder"},
		{cosmosFunctionType, "/sqlDatabases/Sales/containers/Orders/userDefinedFunctions/TaxRate", "/sqlDatabases/Sales/sqlContainers/Orders/sqlUserDefinedFunctions/TaxRate"},
		{cosmosMongoUserType, "/mongodbUserDefinitions/Sales.testUser", ""},
		{cosmosCollectionType, "/mongodbDatabases/Sales/collections/Orders", "/mongodbDatabases/Sales/mongodbCollections/Orders"},
		{cosmosCassandraTableType, "/cassandraKeyspaces/Sales/tables/Orders", "/cassandraKeyspaces/Sales/cassandraTables/Orders"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			kind, ok := findType(test.kind)
			if !ok {
				t.Fatal("missing kind")
			}
			wire := root + test.tail
			if test.alias != "" && responseID(test.kind, root+test.alias) != wire {
				t.Fatal("alias changed native names", responseID(test.kind, root+test.alias))
			}
			for _, method := range []string{"GET", "DELETE"} {
				op, params, err := c.resourceOperation(kind, wire, method)
				if err != nil {
					t.Fatal(err)
				}
				req, err := catalog.BindREST(op, params)
				if err != nil {
					t.Fatal(err)
				}
				u, _ := url.Parse(req.URL)
				if u.Path != wire || u.Query().Get("api-version") != "2026-03-15" {
					t.Fatal("lost native spelling", req.URL)
				}
			}
			value := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", NativeType: test.kind, NativeID: strings.ToLower(wire)}, Normalized: map[string]any{"_cosmos_wire_id": wire, "_cosmos_wire_binding": c.cosmosWireBinding(test.kind, wire)}}
			if id, err := c.plannedResourceID(value); err != nil || id != wire {
				t.Fatal(id, err)
			}
			value.Normalized["_cosmos_wire_id"] = strings.ToLower(wire)
			if _, err := c.plannedResourceID(value); err == nil {
				t.Fatal("accepted altered native name")
			}
		})
	}
	if validResponseIDType(cosmosStoredProcedureType, cosmosType+"/gremlinDatabases/sqlContainers/sqlStoredProcedures") {
		t.Fatal("accepted unrelated ancestor alias")
	}
}

func TestCosmosReadbackUsesBoundNativeName(t *testing.T) {
	wire := "/subscriptions/" + testSubscription + "/resourceGroups/Test/providers/Microsoft.DocumentDB/databaseAccounts/account-one/sqlDatabases/Sales/containers/Orders"
	reads := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if response, handled := emptyMonitorIndexResponse(t, req); handled {
			return response, nil
		}
		if response, handled := emptyDiagnosticSourceIndexResponse(t, req); handled {
			return response, nil
		}
		if req.Method != "GET" || !cosmosSameWireID(req.URL.Path, wire) {
			t.Fatalf("readback used another resource: %s %s", req.Method, req.URL)
		}
		reads++
		return jsonResponse(200, map[string]any{"id": wire, "type": cosmosContainerType}, nil), nil
	})
	c, err := r.resolve(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	value := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", NativeType: cosmosContainerType, NativeID: strings.ToLower(wire)}, Normalized: map[string]any{"_cosmos_wire_id": wire, "_cosmos_wire_binding": c.cosmosWireBinding(cosmosContainerType, wire)}}
	driver, err := r.ResolveAction(context.Background(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	read, err := driver.Readback(context.Background(), contracts.ActionRequest{Action: "delete", Asset: value})
	if err != nil || !read.Exists || reads != 1 {
		t.Fatal(read, err, reads)
	}
	value.Normalized["_cosmos_wire_id"] = strings.ToLower(wire)
	if _, err := r.ResolveAction(context.Background(), "connection", value); err == nil {
		t.Fatal("resolved tampered native selector")
	}
	if _, err := driver.Readback(context.Background(), contracts.ActionRequest{Action: "delete", Asset: value}); err == nil || reads != 1 {
		t.Fatal("tampered selector reached readback", err, reads)
	}
}
