package azure

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
)

func TestDataFactoryNativeReadsAndPrivateSnapshots(t *testing.T) {
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	for _, mapping := range metadata.kinds {
		if dataFactoryKind(mapping.NativeType) == "" {
			continue
		}
		t.Run(mapping.NativeType, func(t *testing.T) {
			op, _ := metadata.catalog.Operation(mapping.ReadOperations[0])
			raw := dataFactoryBody(t, op.Name)
			id := strings.ToLower(text(raw["id"]))
			node := ""
			if mapping.NativeType == dataFactoryNodeType {
				node = text(raw["nodeName"])
				id = strings.ToLower(text(dataFactoryBody(t, "IntegrationRuntimes_Get")["id"]) + "/nodes/" + node)
			}
			// The credential example has a copied linked-service name. The
			// private-connection example returns its parent factory's ID.
			// Reject these native identity defects; use an explicitly composed
			// corrected resource only for the subsequent read-path test.
			if mapping.NativeType == dataFactoryCredentialType {
				if raw["name"] != "exampleLinkedService" || dataFactoryMetadata(id, mapping.NativeType, "", raw) == nil {
					t.Fatal("native credential name discrepancy changed")
				}
				raw["name"] = last(id)
			}
			if mapping.NativeType == dataFactoryPECType {
				if !strings.HasSuffix(id, "/factories/examplefactoryname") {
					t.Fatal("native private-connection parent ID discrepancy changed")
				}
				id += "/privateendpointconnections/connection"
				if dataFactoryMetadata(id, mapping.NativeType, "", raw) == nil {
					t.Fatal("collapsed native connection ID was accepted")
				}
				raw["name"] = "connection"
			}
			id = strings.Replace(id, "12345678-1234-1234-1234-123456789012", testSubscription, 1)
			if node == "" {
				raw["id"] = id
			}
			calls := 0
			c := directClient(func(req *http.Request) (*http.Response, error) {
				calls++
				expected := id
				if node != "" {
					expected = dataFactoryParent(id, mapping.NativeType) + "/nodes/" + node
				}
				if req.Method != "GET" || req.URL.Host != "management.azure.com" || !strings.EqualFold(req.URL.Path, expected) || node != "" && last(req.URL.Path) != node || req.URL.Query().Get("api-version") != dataFactoryVersion || len(req.URL.Query()) != 1 {
					t.Fatal("native own read changed", req.Method, req.URL)
				}
				return jsonResponse(200, raw, nil), nil
			})
			live, err := c.dataFactoryRead(t.Context(), id, mapping.NativeType, node)
			if err != nil || calls != 1 || c.privateConfiguration(live) != c.privateConfiguration(raw) {
				t.Fatal("native own read failed", err, calls)
			}
			if _, err := dataFactoryTypedReferences(id, mapping.NativeType, raw); err != nil {
				t.Fatal("native reference fields rejected", err)
			}
			before := c.privateConfiguration(dataFactorySnapshot(mapping.NativeType, raw))
			changed := batchClone(raw)
			changed["futurePrivateSetting"] = "private-value-without-secret-name"
			if c.privateConfiguration(dataFactorySnapshot(mapping.NativeType, changed)) == before {
				t.Fatal("private future configuration disappeared")
			}
			payload, _ := json.Marshal(dataFactorySafeValue(changed))
			if strings.Contains(string(payload), "private-value-without-secret-name") {
				t.Fatal("private future setting became public")
			}
			if mapping.NativeType == dataFactoryNodeType {
				changed = batchClone(raw)
				changed["status"] = "Offline"
				changed["lastConnectTime"] = "2026-08-01T00:00:00Z"
				if c.privateConfiguration(dataFactorySnapshot(mapping.NativeType, changed)) != before {
					t.Fatal("node liveness invalidated configuration")
				}
				changed["registerTime"] = "2026-08-01T00:00:00Z"
				if c.privateConfiguration(dataFactorySnapshot(mapping.NativeType, changed)) == before {
					t.Fatal("node replacement lost its incarnation")
				}
			}
			if mapping.NativeType == dataFactoryType {
				changed = batchClone(raw)
				object(changed["properties"])["createTime"] = "2026-08-01T00:00:00Z"
				if c.privateConfiguration(dataFactorySnapshot(mapping.NativeType, changed)) == before {
					t.Fatal("factory replacement lost its incarnation")
				}
			}
		})
	}
}

func TestDataFactoryReferenceShapesIgnoreUserData(t *testing.T) {
	root := strings.ToLower(resourceID(dataFactoryType, "factory"))
	ref := func(typ, name string) map[string]any { return map[string]any{"type": typ, "referenceName": name} }
	nested := map[string]any{"type": "ExecutePipeline", "name": "child", "typeProperties": map[string]any{"pipeline": ref("PipelineReference", "nested"), "parameters": map[string]any{"value": ref("DatasetReference", "not-a-dataset")}}}
	pipeline := map[string]any{"properties": map[string]any{"parameters": map[string]any{"fake": ref("PipelineReference", "not-a-pipeline")}, "activities": []any{
		map[string]any{"name": "loop", "type": "ForEach", "typeProperties": map[string]any{"items": map[string]any{"type": "Expression", "value": "@pipeline().parameters.private"}, "activities": []any{nested}}},
		map[string]any{"name": "copy", "type": "Copy", "inputs": []any{ref("DatasetReference", "source")}, "outputs": []any{ref("DatasetReference", "destination")}, "typeProperties": map[string]any{"source": map[string]any{"type": "BlobSource"}, "sink": map[string]any{"type": "BlobSink"}}},
		map[string]any{"name": "web", "type": "WebActivity", "typeProperties": map[string]any{"method": "POST", "url": "https://example.invalid", "body": map[string]any{"pipeline": ref("PipelineReference", "user-body"), "activities": []any{nested}}}},
	}}}
	refs, err := dataFactoryTypedReferences(root+"/pipelines/main", dataFactoryPipelineType, pipeline)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(refs[dataFactoryPipelineType], []string{root + "/pipelines/nested"}) || !slices.Equal(refs[dataFactoryDatasetType], []string{root + "/datasets/destination", root + "/datasets/source"}) || len(refs) != 3 {
		t.Fatal("typed references differ", refs)
	}
	for _, mode := range []string{"wrong-type", "non-string-name", "path-name", "dynamic-name", "wrong-array", "unknown-activity"} {
		t.Run(mode, func(t *testing.T) {
			value := batchClone(pipeline)
			activities := array(object(value["properties"])["activities"])
			fields := object(object(array(object(object(activities[0])["typeProperties"])["activities"])[0])["typeProperties"])
			switch mode {
			case "wrong-type":
				object(fields["pipeline"])["type"] = "DatasetReference"
			case "non-string-name":
				object(fields["pipeline"])["referenceName"] = map[string]any{"value": "nested"}
			case "path-name":
				object(fields["pipeline"])["referenceName"] = "../other"
			case "dynamic-name":
				object(fields["pipeline"])["referenceName"] = "@pipeline().parameters.target"
			case "wrong-array":
				object(value["properties"])["activities"] = map[string]any{}
			case "unknown-activity":
				object(activities[0])["type"] = "FutureActivity"
			}
			if _, err := dataFactoryTypedReferences(root+"/pipelines/main", dataFactoryPipelineType, value); err == nil {
				t.Fatal("ambiguous native reference accepted")
			}
		})
	}
}

func TestDataFactoryCDCStringResponseIsNarrow(t *testing.T) {
	root := strings.ToLower(resourceID(dataFactoryType, "factory"))
	endpoint := apiURL(root+"/adfcdcs/cdc/status", dataFactoryVersion)
	for _, mode := range []string{"valid", "ordinary-read", "other-kind", "foreign-provider", "wrong-version", "duplicate-version", "wrong-method", "object", "null", "array", "blank", "padded"} {
		t.Run(mode, func(t *testing.T) {
			target, method, body := endpoint, "GET", any("Stopped")
			switch mode {
			case "ordinary-read":
				target = apiURL(root+"/adfcdcs/cdc", dataFactoryVersion)
			case "other-kind":
				target = apiURL(root+"/triggers/trigger/status", dataFactoryVersion)
			case "foreign-provider":
				target = strings.Replace(endpoint, "microsoft.datafactory", "microsoft.other", 1)
			case "wrong-version":
				target = strings.Replace(endpoint, dataFactoryVersion, "2020-01-01", 1)
			case "duplicate-version":
				target += "&api-version=" + dataFactoryVersion
			case "wrong-method":
				method = "POST"
			case "object":
				body = map[string]any{"status": "Stopped"}
			case "null":
				body = nil
			case "array":
				body = []any{"Stopped"}
			case "blank":
				body = ""
			case "padded":
				body = " Stopped "
			}
			c := directClient(func(req *http.Request) (*http.Response, error) { return jsonResponse(200, body, nil), nil })
			result, err := c.request(t.Context(), method, target)
			if mode == "valid" {
				if err != nil || result.data["status"] != "Stopped" {
					t.Fatal("native CDC string rejected", err)
				}
			} else if err == nil {
				t.Fatal("string exception escaped the native CDC operation", mode)
			}
		})
	}
}

func TestDataFactoryAPIProjectionHidesAuthoredContent(t *testing.T) {
	root := strings.ToLower(resourceID(dataFactoryType, "factory"))
	raw := map[string]any{"status": "InProgress", "properties": map[string]any{"createTime": "2020-01-01T00:00:00Z", "parameters": map[string]any{"private": "sensitive-value"}, "typeProperties": map[string]any{"connectionString": "sensitive-value"}}, "parameters": map[string]any{"ordinary": "sensitive-value"}, "future": "sensitive-value", "message": "sensitive-value"}
	for _, path := range []string{root, root + "/pipelines/pipeline", root + "/queryPipelineRuns", root + "/integrationruntimes/runtime/stop/operationstatuses/00001111222233334444555566667777"} {
		endpoint := apiURL(path, dataFactoryVersion)
		for _, method := range []string{"GET", "POST", "DELETE"} {
			payload, _ := json.Marshal(safeAPIPayload(map[string]any{"method": method, "path": path, "body": raw, "query": map[string]any{"api-version": dataFactoryVersion, "private": "sensitive-value"}}, endpoint))
			if strings.Contains(string(payload), "sensitive-value") || !strings.Contains(string(payload), method) {
				t.Fatal("Data Factory request projection lost privacy or diagnostics")
			}
			response, _ := json.Marshal(safeAPIPayload(map[string]any{"request_id": "request-id", "status_code": 200, "body": raw}, endpoint))
			if strings.Contains(string(response), "sensitive-value") || !strings.Contains(string(response), "request-id") {
				t.Fatal("Data Factory response projection lost privacy or diagnostics")
			}
		}
	}
	u, _ := url.Parse(apiURL(root+"/adfcdcs/cdc/status", dataFactoryVersion))
	if !dataFactoryStringResponse("GET", u) {
		t.Fatal("invalid native status shape")
	}
}
