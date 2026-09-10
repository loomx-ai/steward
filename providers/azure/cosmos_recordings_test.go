package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func cosmosRecordings(t *testing.T, file string) []redisRecordedResponse {
	t.Helper()
	payload, err := os.ReadFile("fixtures/cosmos/cli-recordings.json")
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	var sources []struct {
		File    string                  `json:"file"`
		Source  string                  `json:"source_uri"`
		SHA     string                  `json:"source_sha256"`
		Records []redisRecordedResponse `json:"recordings"`
	}
	if err != nil || json.Unmarshal(payload, &sources) != nil || len(sources) != 17 {
		t.Fatal("invalid Cosmos CLI recordings", err)
	}
	count := 0
	var result []redisRecordedResponse
	for _, source := range sources {
		if len(source.SHA) != 64 || !strings.Contains(source.Source, "/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/") {
			t.Fatal("unpinned Cosmos recording")
		}
		count += len(source.Records)
		if source.File == file {
			result = source.Records
		}
	}
	if count != 386 || len(result) == 0 {
		t.Fatal("missing Cosmos native responses", count)
	}
	return result
}
func TestCosmosRecordedNativeDeletionAndResumedReadback(t *testing.T) {
	for _, test := range []struct {
		file         string
		read, delete int
	}{
		{"test_delete_database_account.yaml", 5, 6},
		{"test_cosmosdb_sql_database.yaml", 13, 14},
		{"test_cosmosdb_sql_container.yaml", 22, 23},
		{"test_cosmosdb_sql_stored_procedure.yaml", 22, 24},
		{"test_cosmosdb_sql_trigger.yaml", 24, 26},
		{"test_cosmosdb_sql_user_defined_function.yaml", 22, 24},
		{"test_cosmosdb_mongodb_database.yaml", 13, 14},
		{"test_cosmosdb_mongodb_collection.yaml", 17, 18},
		{"test_cosmosdb_mongodb_collection.yaml", 32, 33},
		{"test_cosmosdb_mongodb_role.yaml", 44, 46},
		{"test_cosmosdb_mongodb_role.yaml", 23, 50},
		{"test_cosmosdb_mongodb_role.yaml", 29, 54},
		{"test_cosmosdb_cassandra_keyspace.yaml", 13, 14},
		{"test_cosmosdb_cassandra_table.yaml", 25, 26},
		{"test_cosmosdb_gremlin_database.yaml", 14, 15},
		{"test_cosmosdb_gremlin_graph.yaml", 25, 26},
		{"test_cosmosdb_table.yaml", 16, 17},
		{"test_cosmosdb_table.yaml", 43, 45},
		{"test_cosmosdb_table.yaml", 74, 75},
		{"test_cosmosdb_private_endpoint.yaml", 40, 41},
		{"test_cosmosdb_service.yaml", 23, 25},
		{"test_cosmosdb_fleet_fleetspace_fleetspaceAccount.yaml", 37, 39},
		{"test_cosmosdb_fleet_fleetspace_fleetspaceAccount.yaml", 21, 43},
		{"test_cosmosdb_fleet_fleetspace_fleetspaceAccount.yaml", 1, 48},
	} {
		t.Run(test.file+"/"+strconv.Itoa(test.delete), func(t *testing.T) {
			rows := cosmosRecordings(t, test.file)
			byIndex := map[int]redisRecordedResponse{}
			s := newDNSScenario()
			for _, row := range rows {
				byIndex[row.Index] = row
				if row.Index > test.delete || row.Method != "GET" || row.Status != 200 {
					continue
				}
				if id := text(row.Body["id"]); id != "" {
					if _, kind, err := parseID(id); err == nil && (isCosmosType(kind) || strings.HasSuffix(kind, "/throughputsettings")) {
						s.add(row.Body, "2026-03-15")
					}
				}
				// Some recording setup paths expose an ancestor only in LIST.
				// Its unchanged native body supplies a synthetic supporting GET.
				for _, item := range array(row.Body["value"]) {
					raw := object(item)
					id := strings.ToLower(text(raw["id"]))
					if _, kind, err := parseID(id); err == nil && isCosmosType(kind) && s.records[id] == nil {
						s.add(raw, "2026-03-15")
					}
				}
			}
			raw := byIndex[test.read].Body
			wire := text(raw["id"])
			id, _, err := parseID(wire)
			if err != nil {
				t.Fatal(err)
			}
			s.add(raw, "2026-03-15")
			group := strings.Join(strings.Split(id, "/")[:5], "/")
			s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
			// These support collections are explicitly synthetic. The original
			// CLI scenario does not provide a full authoritative cleanup scan.
			for _, resource := range s.records {
				parentID, parentKind, err := parseID(text(resource["id"]))
				if err != nil || !isCosmosType(parentKind) {
					continue
				}
				for _, child := range cosmosOwnedKinds(parentKind) {
					s.lists[parentID+"/"+strings.ToLower(last(child))] = []any{}
				}
				if parentKind == strings.ToLower(cosmosType) {
					for _, child := range []string{cosmosMongoRoleType, cosmosMongoUserType} {
						s.lists[parentID+"/"+strings.ToLower(last(child))] = []any{}
					}
				}
			}
			s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.documentdb/fleets"] = []any{}
			for _, ancestor := range append([]string{wire}, cosmosAncestorIDs(wire)...) {
				if s.records[strings.ToLower(ancestor)] == nil {
					t.Fatal("recording lacks ancestor", ancestor)
				}
				settingsID := strings.ToLower(ancestor + "/throughputSettings/default")
				if s.records[settingsID] == nil {
					s.status[settingsID] = 404
				}
			}
			deletion := byIndex[test.delete]
			header := http.Header(deletion.Headers)
			operation := header.Get("Azure-AsyncOperation")
			if operation == "" { // Go Header.Get requires canonicalized keys.
				for key, values := range deletion.Headers {
					if strings.EqualFold(key, "Azure-AsyncOperation") && len(values) > 0 {
						operation = values[0]
					}
				}
			}
			var polls []redisRecordedResponse
			for _, row := range rows {
				if row.Index > test.delete && row.Method == "GET" && row.URI == operation {
					polls = append(polls, row)
				}
			}
			sort.Slice(polls, func(i, j int) bool { return polls[i].Index < polls[j].Index })
			if len(polls) == 0 || text(polls[len(polls)-1].Body["status"]) != "Succeeded" {
				t.Fatal("missing native completion", test.delete)
			}
			deleted, absent := false, false
			pollIndex := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					if !cosmosSameWireID(req.URL.Path, wire) || req.URL.Query().Get("api-version") != "2026-03-15" {
						t.Fatal("wrong native DELETE", req.URL.Path)
					}
					deleted = true
					h := http.Header{}
					for key, values := range deletion.Headers {
						for _, value := range values {
							h.Add(key, value)
						}
					}
					return jsonResponse(deletion.Status, deletion.Body, h), true
				}
				if req.URL.String() == operation {
					row := polls[pollIndex]
					if pollIndex < len(polls)-1 {
						pollIndex++
					}
					return jsonResponse(row.Status, row.Body, nil), true
				}
				if cosmosSameWireID(req.URL.Path, wire) && absent {
					return jsonResponse(404, map[string]any{"error": map[string]any{"code": "NotFound"}}, nil), true
				}
				if strings.Contains(strings.ToLower(req.URL.Path), "/microsoft.documentdb/") {
					if value := s.records[strings.ToLower(req.URL.Path)]; value != nil && !cosmosSameWireID(req.URL.Path, text(value["id"])) {
						t.Fatal("request lost native case", req.URL.Path)
					}
				}
				return nil, false
			}
			r := s.runtime(t)
			target := dnsAsset(t, r, raw)
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			var logs []execution.JobLogEntry
			ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
			result, err := driver.Execute(ctx, request)
			if err != nil || !deleted {
				t.Fatal("native deletion", err)
			}
			payload, _ := json.Marshal(request)
			json.Unmarshal(payload, &request)
			payload, _ = json.Marshal(result)
			json.Unmarshal(payload, &result)
			driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			for range len(polls) + 1 {
				waited, err := driver.Wait(ctx, request, result)
				if err != nil || waited.Done {
					t.Fatal("operation completed while native resource exists", waited, err)
				}
			}
			// Microsoft stopped recording at LRO success. Final GET 404 and
			// idempotent resume are synthetic contract checks, not source claims.
			absent = true
			waited, err := driver.Wait(ctx, request, result)
			if err != nil || !waited.Done {
				t.Fatal("final resource absence", waited, err)
			}
			deleted = false
			if _, err := driver.Execute(context.Background(), request); err != nil || deleted {
				t.Fatal("repeated deletion after confirmed absence", err)
			}
			if result.ProviderOperationID != operation {
				t.Fatal("lost native operation URI")
			}
			payload, _ = json.Marshal(logs)
			u, _ := url.Parse(operation)
			for _, key := range []string{"t", "c", "s", "h"} {
				if value := u.Query().Get(key); value != "" && bytes.Contains(payload, []byte(value)) {
					t.Fatal("Cosmos signed polling parameter leaked", key)
				}
			}
		})
	}
}

func TestCosmosOperationEndpointsAndReceipts(t *testing.T) {
	wire := "/subscriptions/" + testSubscription + "/resourceGroups/Test/providers/Microsoft.DocumentDB/databaseAccounts/account/sqlDatabases/Sales/containers/Orders"
	operation := "https://management.azure.com/subscriptions/" + testSubscription + "/providers/Microsoft.DocumentDB/locations/centraluseuap/operationsStatus/11111111-2222-3333-4444-555555555555?api-version=2026-03-15"
	for _, valid := range []string{operation, strings.Replace(operation, "https://management.azure.com", "https://centraluseuap.management.azure.com", 1), "https://management.azure.com" + wire + "/operationResults/11111111-2222-3333-4444-555555555555?api-version=2026-03-15"} {
		if err := validateCosmosOperationURL(testSubscription, wire, "2026-03-15", valid); err != nil {
			t.Fatal(err)
		}
	}
	for _, bad := range []string{strings.Replace(operation, testSubscription, testTenant, 1), strings.Replace(operation, "Microsoft.DocumentDB", "Microsoft.Compute", 1), strings.Replace(operation, "2026-03-15", "2025-10-15", 1), strings.Replace(operation, "management.azure.com", "evil.invalid", 1), strings.Replace(operation, "management.azure.com", "centraluseuap.management.azure.com.evil.invalid", 1), strings.Replace(operation, "management.azure.com", "westus.management.azure.com", 1), operation + "&sig=not-a-native-parameter", operation + "#fragment", strings.Replace(operation, "/operationsStatus/", "/sqlDatabases/", 1), strings.Replace(operation, "/centraluseuap/", "/../", 1), "https://management.azure.com" + strings.Replace(wire, "/Orders", "/orders", 1) + "/operationResults/11111111-2222-3333-4444-555555555555?api-version=2026-03-15"} {
		if err := validateCosmosOperationURL(testSubscription, wire, "2026-03-15", bad); err == nil {
			t.Fatal("accepted invalid operation", bad)
		}
	}
	calls := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		calls++
		return jsonResponse(200, map[string]any{"status": "Succeeded"}, nil), nil
	})
	c, _ := r.resolve(context.Background(), "connection")
	mapping, _ := findType(cosmosContainerType)
	a := &action{client: c, kind: mapping, id: strings.ToLower(wire), wireID: wire}
	result := contracts.ActionResult{ProviderOperationID: operation, Data: map[string]any{"polling": "status", "cosmos_operation_binding": a.cosmosOperationBinding(operation)}}
	copy := *a
	copy.wireID = strings.Replace(wire, "/Orders", "/orders", 1)
	if _, err := copy.poll(context.Background(), result); err == nil || calls != 0 {
		t.Fatal("reused operation for case-only sibling", err, calls)
	}
	result.ProviderOperationID = strings.Replace(operation, "centraluseuap", "westus", 1)
	if _, err := a.poll(context.Background(), result); err == nil || calls != 0 {
		t.Fatal("retargeted operation receipt", err, calls)
	}
}
