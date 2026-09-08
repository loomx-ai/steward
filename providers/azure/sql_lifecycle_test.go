package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestSQLServerReviewedDatabaseAndPoolCascade(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	id := root + "/resourceGroups/test/providers/Microsoft.Sql/servers/sql"
	operation := apiURL(root+"/resourceGroups/test/providers/Microsoft.Sql/locations/eastus/serverOperationResults/deletion", "2023-05-01")
	server := map[string]any{"id": id, "type": sqlServerType, "name": "sql", "location": "eastus", "properties": map[string]any{"state": "Ready"}, "systemData": map[string]any{"createdAt": "2026-09-01T00:00:00Z"}}
	master := map[string]any{"id": id + "/databases/master", "type": sqlDatabaseType, "name": "master", "properties": map[string]any{"databaseId": "master-uid", "creationDate": "2026-09-01T00:00:00Z"}}
	database := map[string]any{"id": id + "/databases/app", "type": sqlDatabaseType, "name": "app", "properties": map[string]any{"databaseId": "app-uid", "elasticPoolId": id + "/elasticPools/pool"}}
	pool := map[string]any{"id": id + "/elasticPools/pool", "type": "Microsoft.Sql/servers/elasticPools", "name": "pool", "properties": map[string]any{"state": "Ready"}}
	records := []map[string]any{server, master, database, pool}
	deleted, childrenGone, denyList := false, false, false
	deletes := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == "DELETE" {
			if !strings.EqualFold(req.URL.Path, id) || req.URL.Query().Get("api-version") != "2023-08-01" || req.Header.Get("x-ms-client-request-id") != azureRequestID("delete-sql") {
				t.Fatalf("unexpected SQL write %s", req.URL)
			}
			deletes++
			deleted = true
			return jsonResponse(202, map[string]any{}, http.Header{"Location": {operation}, "X-Ms-Request-Id": {"sql-delete"}}), nil
		}
		if req.Method != "GET" {
			t.Fatalf("unexpected SQL mutation %s", req.URL)
		}
		if strings.EqualFold(req.URL.String(), operation) {
			return jsonResponse(204, nil, nil), nil
		}
		for i, raw := range records {
			if strings.EqualFold(req.URL.Path, text(raw["id"])) {
				if (i == 0 && deleted) || (i > 0 && childrenGone) {
					return jsonResponse(404, map[string]any{}, nil), nil
				}
				if req.URL.Query().Get("api-version") != "2023-08-01" {
					t.Fatalf("incorrect SQL get API version %s", req.URL)
				}
				return jsonResponse(200, raw, nil), nil
			}
		}
		if strings.EqualFold(req.URL.Path, id+"/databases") {
			if denyList {
				return jsonResponse(403, map[string]any{}, nil), nil
			}
			if req.URL.Query().Get("$skiptoken") == "two" {
				return jsonResponse(200, map[string]any{"value": []any{database}}, nil), nil
			}
			return jsonResponse(200, map[string]any{"value": []any{master}, "nextLink": apiURL(id+"/databases", "2023-08-01") + "&%24skiptoken=two"}, nil), nil
		}
		if strings.EqualFold(req.URL.Path, id+"/elasticPools") {
			return jsonResponse(200, map[string]any{"value": []any{pool}}, nil), nil
		}
		if strings.EqualFold(req.URL.Path, root+"/providers/Microsoft.Authorization/locks") {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		if strings.EqualFold(req.URL.Path, root+"/resourceGroups/test") {
			return jsonResponse(200, map[string]any{"id": req.URL.Path}, nil), nil
		}
		t.Fatalf("unexpected SQL request %s", req.URL)
		return nil, nil
	})
	c, err := r.resolve(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	var assets []asset.Asset
	for _, raw := range records {
		item, err := r.inventoryItem(context.Background(), c, raw, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		assets = append(assets, asset.Asset{ID: asset.AssetID(last(item.NativeID)), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: item.NativeType, NativeID: item.NativeID}, Location: "eastus", Capabilities: asset.CapabilitySet{asset.CapabilityActionable}, Normalized: item.Normalized})
	}
	if assets[1].Normalized["cleanup_controller_only"] != true || assets[1].Normalized["cleanup_protected"] == true {
		t.Fatalf("master lifetime policy wrong: %v", assets[1].Normalized)
	}
	contributor, err := r.ServiceLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := contributor.Contribute(context.Background(), "scope", assets)
	if err != nil || len(contribution.Bindings) != 3 || len(contribution.Unresolved) != 0 {
		t.Fatalf("SQL contribution=%+v %v", contribution, err)
	}
	input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{"sql"}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 1 || len(result.ImpactItems) != 3 {
		t.Fatalf("SQL plan=%+v %v", result, err)
	}
	request := contracts.ActionRequest{Asset: assets[0], Action: "delete", IdempotencyKey: "delete-sql"}
	for _, value := range assets[1:] {
		request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{Asset: value, ControllerID: assets[0].ID, Delete: true})
	}
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	// Database creation identities are not ordinary mutable configuration.
	object(database["properties"])["databaseId"] = "replacement-uid"
	if _, err := driver.Execute(context.Background(), request); err == nil || deletes != 0 {
		t.Fatal("recreated database was deleted")
	}
	object(database["properties"])["databaseId"] = "app-uid"
	denyList = true
	if _, err := driver.Execute(context.Background(), request); err == nil || deletes != 0 {
		t.Fatal("unreadable SQL child collection accepted")
	}
	denyList = false
	input.RequestOptions = map[asset.AssetID]map[string]any{"sql": {"retain_resources": []string{"app"}}}
	if result, err := plan.Solve(input); err != nil || len(result.Blockers) == 0 {
		t.Fatalf("SQL retention silently ignored: %+v %v", result, err)
	}
	masterDriver, err := r.ResolveAction(context.Background(), "connection", assets[1])
	if err != nil {
		t.Fatal(err)
	}
	check, err := masterDriver.Preflight(context.Background(), contracts.ActionRequest{Asset: assets[1], Action: "delete"})
	if err != nil || check.Allowed || check.Reason != "azure_system_database" {
		t.Fatalf("master allowed independent deletion: %+v %v", check, err)
	}
	operationResult, err := driver.Execute(context.Background(), request)
	if err != nil || deletes != 1 || operationResult.ProviderRequestID != "sql-delete" {
		t.Fatalf("SQL delete=%+v %v", operationResult, err)
	}
	encoded, _ := json.Marshal(request)
	json.Unmarshal(encoded, &request)
	encoded, _ = json.Marshal(operationResult)
	json.Unmarshal(encoded, &operationResult)
	driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(context.Background(), request, operationResult)
	if err != nil || wait.Done {
		t.Fatalf("lost SQL child absence readback: %+v %v", wait, err)
	}
	childrenGone = true
	wait, err = driver.Wait(context.Background(), request, operationResult)
	if err != nil || !wait.Done || deletes != 1 {
		t.Fatalf("SQL final state: %+v %v", wait, err)
	}
}
