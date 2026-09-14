package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestElasticSanOfficialContracts(t *testing.T) {
	data, err := os.ReadFile("fixtures/elastic-san/sources.json")
	var sources []map[string]string
	if err != nil || json.Unmarshal(data, &sources) != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile("catalog/source/swagger.json")
	var documents catalog.RESTDocumentSet
	if err != nil || json.Unmarshal(data, &documents) != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	native := map[string]map[string]any{}
	for _, doc := range documents.Documents {
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc.Document))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource(doc.SourceURI, value); err != nil {
			t.Fatal(err)
		}
		native[doc.SourceURI] = object(value)
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	operations := map[string]bool{}
	responses, bodies, placeholders := 0, 0, 0
	for _, source := range sources {
		t.Run(source["file"], func(t *testing.T) {
			raw, err := os.ReadFile("fixtures/elastic-san/" + source["file"])
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != source["source_sha256"] {
				t.Fatal("original source changed", err)
			}
			var example map[string]any
			if json.Unmarshal(raw, &example) != nil {
				t.Fatal("example")
			}
			op, ok := metadata.catalog.Operation("Azure.Microsoft.ElasticSan." + source["operation"])
			if !ok || op.Call == nil || op.Call.Version != "2026-04-01-preview" || op.Call.Method != source["method"] || op.Call.Path != source["path"] || !strings.Contains(op.SourceURI, "/c20bf553ad64f20c6d5e3f56080380c086cb1fde/") {
				t.Fatal("native contract provenance")
			}
			operations[op.ID] = true
			params := object(example["parameters"])
			if source["file"] == "Volumes_Get_MaximumSet_Gen.json" {
				if params["x-ms-access-soft-deleted-resources"] != "true" {
					t.Fatal("native undeclared header changed")
				}
				if _, err := bindAzureREST(op, params); err == nil {
					t.Fatal("undeclared GET header accepted")
				}
				// Only the request copy changes; the original example remains intact.
				delete(params, "x-ms-access-soft-deleted-resources")
			}
			request, err := bindAzureREST(op, params)
			if err != nil {
				t.Fatal("native request", err)
			}
			u, err := url.Parse(request.URL)
			if err != nil || u.Scheme != "https" || u.Host != "management.azure.com" || request.Method != source["method"] || u.Query().Get("api-version") != "2026-04-01-preview" || len(request.Body) != 0 {
				t.Fatal(request, err)
			}
			for _, name := range []string{"x-ms-access-soft-deleted-resources", "x-ms-delete-snapshots", "x-ms-force-delete"} {
				if text(params[name]) != request.Headers[name] {
					t.Fatal("native header lost", name, request.Headers)
				}
			}
			if text(params["deleteType"]) != u.Query().Get("deleteType") {
				t.Fatal("permanent deletion flag changed")
			}
			params["api-version"] = "2025-09-01"
			if _, err := bindAzureREST(op, params); err == nil {
				t.Fatal("unreviewed API version accepted")
			}
			wire := object(object(object(native[op.SourceURI]["paths"])[op.Call.Path])[strings.ToLower(op.Call.Method)])
			if op.Call.Method == "DELETE" && (wire["x-ms-long-running-operation"] != true || object(wire["x-ms-long-running-operation-options"])["final-state-via"] != "location") {
				t.Fatal("native Location contract changed")
			}
			for code, value := range object(example["responses"]) {
				responses++
				if object(wire["responses"])[code] == nil {
					t.Fatal("undeclared response", code)
				}
				response := object(value)
				if location := text(object(response["headers"])["location"]); location != "" {
					if location != "https://contoso.com/operationstatus" || (&client{subscription: testSubscription}).validateURL(location) == nil {
						t.Fatal("unsafe native placeholder accepted")
					}
					placeholders++
				}
				if body := response["body"]; body != nil {
					pointer := "#/paths/" + strings.ReplaceAll(op.Call.Path, "/", "~1") + "/" + strings.ToLower(op.Call.Method) + "/responses/" + code + "/schema"
					schema, err := compiler.Compile(op.SourceURI + pointer)
					if err != nil {
						t.Fatal(err)
					}
					if err := schema.Validate(body); err != nil {
						t.Error("native schema", err)
					}
					bodies++
				}
			}
		})
	}
	if len(sources) != 34 || len(operations) != 17 || responses != 54 || bodies != 24 || placeholders != 10 {
		t.Fatal("incomplete native evidence", len(sources), len(operations), responses, bodies, placeholders)
	}
}

func TestElasticSanNativeParameterBoundaries(t *testing.T) {
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Volumes_Delete", "Volumes_ListByVolumeGroup", "VolumeGroups_ListByElasticSan", "VolumeSnapshots_Get"} {
		t.Run(name, func(t *testing.T) {
			op, _ := metadata.catalog.Operation("Azure.Microsoft.ElasticSan." + name)
			raw, err := os.ReadFile("fixtures/elastic-san/" + name + "_MinimumSet_Gen.json")
			var example map[string]any
			if err != nil || json.Unmarshal(raw, &example) != nil {
				t.Fatal(err)
			}
			params := object(example["parameters"])
			delete(params, "deleteType")
			request, err := bindAzureREST(op, params)
			if err != nil {
				t.Fatal(err)
			}
			if request.Headers["x-ms-force-delete"] != "" || request.Headers["x-ms-delete-snapshots"] != "" || strings.Contains(request.URL, "deleteType") {
				t.Fatal("implicit destructive option")
			}
			properties := object(op.InputSchema["properties"])
			for _, flag := range []string{"x-ms-access-soft-deleted-resources", "x-ms-delete-snapshots", "x-ms-force-delete", "deleteType"} {
				for _, bad := range []any{true, false, "TRUE", " false", "true\r\nX-Foo: bad", "purge", 1} {
					changed := maps.Clone(params)
					changed[flag] = bad
					if _, err := bindAzureREST(op, changed); err == nil {
						t.Fatal("invalid flag accepted", flag, bad)
					}
				}
				if properties[flag] != nil && flag != "deleteType" {
					for _, valid := range []string{"true", "false"} {
						changed := maps.Clone(params)
						changed[flag] = valid
						bound, err := bindAzureREST(op, changed)
						if err != nil || bound.Headers[flag] != valid {
							t.Fatal("lost explicit flag", flag, valid, err)
						}
					}
				}
			}
			for key := range params {
				if key == "api-version" || key == "subscriptionId" || object(properties[key])["pattern"] == nil {
					continue
				}
				for _, bad := range []string{"", "../other", "name/other", "name?x=y", "name#x", "name\r\n", " name"} {
					changed := maps.Clone(params)
					changed[key] = bad
					if _, err := bindAzureREST(op, changed); err == nil {
						t.Fatal("invalid resource name", key, bad)
					}
				}
			}
		})
	}
}

func TestElasticSanInvocationPrivacyAndHeaderTransport(t *testing.T) {
	logs := []execution.JobLogEntry{}
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { logs = append(logs, e) }))
	calls := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "GET" || req.URL.Query().Get("api-version") != "2026-04-01-preview" || req.Header.Get("x-ms-access-soft-deleted-resources") != "true" || !strings.Contains(req.URL.Path, testSubscription) {
			t.Fatal("native request changed", req.URL, req.Header)
		}
		return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": "/subscriptions/" + testSubscription + "/resourceGroups/group/providers/Microsoft.ElasticSan/elasticSans/san/volumegroups/group", "type": elasticSanGroupType, "name": "group", "properties": map[string]any{"provisioningState": "Succeeded", "deleteRetentionPolicy": map[string]any{"policyState": "Enabled", "retentionPeriodDays": float64(7), "futurePolicy": "private-san-policy"}, "sizeGiB": float64(8), "encryptionProperties": map[string]any{"keyVaultUri": "private-san-keyvault"}, "networkAcls": map[string]any{"source": "private-san-address"}, "storageTarget": map[string]any{"targetIqn": "private-san-target"}, "futureMetadata": "private-san-metadata"}, "identity": map[string]any{"unknown": "private-san-identity"}}}}, http.Header{"X-Ms-Request-Id": {"elastic-san-request"}}), nil
	})
	invocation := contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.ElasticSan.VolumeGroups_ListByElasticSan", Parameters: map[string]any{"resourceGroupName": "group", "elasticSanName": "san", "x-ms-access-soft-deleted-resources": "true"}}
	result, err := r.Invoke(ctx, invocation)
	if err != nil || result.RequestID != "elastic-san-request" {
		t.Fatal(result, err)
	}
	props := object(object(array(result.Data["value"])[0])["properties"])
	if props["provisioningState"] != "Succeeded" || object(props["deleteRetentionPolicy"])["policyState"] != "Enabled" {
		t.Fatal("public state lost", result)
	}
	encoded, _ := json.Marshal(map[string]any{"result": result, "logs": logs})
	if strings.Contains(string(encoded), "private-san-") {
		t.Fatal("private SAN state exposed")
	}
	for _, params := range []map[string]any{{"subscriptionId": testTenant}, {"elasticSanName": "../other"}, {"x-ms-access-soft-deleted-resources": true}, {"api-version": "2025-09-01"}, {"unreviewed": "true"}} {
		changed := invocation
		changed.Parameters = maps.Clone(invocation.Parameters)
		maps.Copy(changed.Parameters, params)
		if _, err := r.Invoke(ctx, changed); err == nil {
			t.Fatal("invalid scope/parameter accepted", params)
		}
	}
	if calls != 1 {
		t.Fatal("invalid invocation reached transport", calls)
	}
}
