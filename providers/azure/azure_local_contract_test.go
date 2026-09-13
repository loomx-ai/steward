package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

func TestAzureLocalOfficialContracts(t *testing.T) {
	raw, err := os.ReadFile("fixtures/azure-local/sources.json")
	var sources []map[string]string
	if err != nil || json.Unmarshal(raw, &sources) != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("catalog/source/swagger.json")
	var documents catalog.RESTDocumentSet
	if err != nil || json.Unmarshal(raw, &documents) != nil {
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
	responses, bodies, badParents, badCallbacks, badParameters := 0, 0, 0, 0, 0
	for _, source := range sources {
		t.Run(source["file"], func(t *testing.T) {
			payload, err := os.ReadFile("fixtures/azure-local/" + source["file"])
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != source["source_sha256"] {
				t.Fatal("native Azure Local example changed", err)
			}
			version := "2024-01-01"
			if strings.HasPrefix(source["operation"], "LogicalNetworks_") {
				version = "2025-06-01-preview"
			}
			op, ok := metadata.catalog.Operation("Azure.Microsoft.AzureStackHCI." + source["operation"])
			if !ok || op.Call == nil || op.Call.Version != version || op.Call.Method != source["method"] || op.Call.Path != source["path"] || !strings.Contains(op.SourceURI, "/c20bf553ad64f20c6d5e3f56080380c086cb1fde/") {
				t.Fatal("Azure Local provenance changed")
			}
			operations[op.ID] = true
			var example map[string]any
			if json.Unmarshal(payload, &example) != nil {
				t.Fatal("example")
			}
			params := object(example["parameters"])
			if source["operation"] == "NetworkInterfaces_List" || source["operation"] == "NetworkInterfaces_ListAll" {
				if params["networkInterfaceName"] != "test-nic" || params["resourceGroupName"] != "test-rg" {
					t.Fatal("native extra list parameters changed")
				}
				if _, err := bindAzureREST(op, params); err == nil {
					t.Fatal("undeclared native parameters accepted")
				}
				delete(params, "networkInterfaceName")
				if source["operation"] == "NetworkInterfaces_ListAll" {
					delete(params, "resourceGroupName")
				}
				badParameters++
			}
			if parent, ok := params["resourceUri"].(string); ok {
				if !strings.Contains(parent, "/testrg/Microsoft.HybridCompute/machines/") {
					t.Fatal("native missing-providers discrepancy changed")
				}
				if _, err := bindAzureREST(op, params); err == nil {
					t.Fatal("invalid native parent accepted")
				}
				parent = strings.Replace(parent, "/testrg/Microsoft.HybridCompute/", "/testrg/providers/Microsoft.HybridCompute/", 1)
				suffix := "/providers/Microsoft.AzureStackHCI/virtualMachineInstances/default"
				if source["operation"] == "VirtualMachineInstances_Stop" {
					if !strings.HasSuffix(parent, suffix) {
						t.Fatal("native stop parent discrepancy changed")
					}
					parent = strings.TrimSuffix(parent, suffix)
				}
				params["resourceUri"] = parent
				badParents++
			}
			request, err := bindAzureREST(op, params)
			if err != nil {
				t.Fatal("canonical native request", err)
			}
			u, err := url.Parse(request.URL)
			if err != nil || u.Host != "management.azure.com" || u.Scheme != "https" || u.Query().Get("api-version") != version || len(request.Body) != 0 {
				t.Fatal("native request binding", err)
			}
			params["api-version"] = "2023-09-01-preview"
			if _, err := bindAzureREST(op, params); err == nil {
				t.Fatal("unreviewed API version accepted")
			}
			wire := object(object(object(native[op.SourceURI]["paths"])[op.Call.Path])[strings.ToLower(op.Call.Method)])
			if (source["method"] == "DELETE" || source["method"] == "POST") && wire["x-ms-long-running-operation"] != true {
				t.Fatal("native LRO metadata lost")
			}
			for code, value := range object(example["responses"]) {
				responses++
				response := object(value)
				if object(wire["responses"])[code] == nil {
					t.Fatal("undeclared native response", code)
				}
				if endpoint := text(object(response["headers"])["azure-asyncoperation"]); endpoint != "" {
					if endpoint != "http://azure.async.operation/status" {
						t.Fatal("native callback placeholder changed")
					}
					c := &client{subscription: testSubscription}
					if c.validateURL(endpoint) == nil {
						t.Fatal("unsafe example callback accepted")
					}
					badCallbacks++
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
	if len(sources) != 33 || len(operations) != 33 || responses != 42 || bodies != 25 || badParents != 9 || badCallbacks != 18 || badParameters != 2 {
		t.Fatal("incomplete Azure Local evidence", len(sources), len(operations), responses, bodies, badParents, badCallbacks, badParameters)
	}
}

func TestAzureLocalInvocationScopeAndPrivateMetadata(t *testing.T) {
	machine := strings.TrimPrefix(resourceID(hybridMachineType, "local-vm"), "/")
	instance := "/" + machine + "/providers/Microsoft.AzureStackHCI/virtualMachineInstances/default"
	logs := []execution.JobLogEntry{}
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	calls := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		calls++
		if req.Method != "GET" || req.URL.Path != instance || req.URL.Query().Get("api-version") != "2024-01-01" {
			t.Fatal("wrong native extension request", req.URL.Path)
		}
		return jsonResponse(200, map[string]any{"id": instance, "name": "default", "type": "Microsoft.AzureStackHCI/virtualMachineInstances", "properties": map[string]any{"provisioningState": "Succeeded", "osProfile": map[string]any{"adminPassword": "private-local-password", "ssh": map[string]any{"publicKeys": []any{"private-local-key"}}}, "httpProxyConfig": map[string]any{"httpsProxy": "https://private-local-proxy"}, "futurePrivateConfiguration": "private-local-future"}}, http.Header{"X-Ms-Request-Id": {"local-request"}}), nil
	})
	invoke := contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.AzureStackHCI.VirtualMachineInstances_Get", Parameters: map[string]any{"resourceUri": machine}}
	result, err := r.Invoke(ctx, invoke)
	if err != nil || result.RequestID != "local-request" || result.Data["id"] != instance || object(result.Data["properties"])["provisioningState"] != "Succeeded" {
		t.Fatal("native extension invocation", err)
	}
	encoded, _ := json.Marshal(map[string]any{"data": result.Data, "logs": logs})
	for _, private := range []string{"private-local-password", "private-local-key", "private-local-proxy", "private-local-future"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("private local configuration exposed")
		}
	}
	for _, parent := range []string{"/" + machine, machine + "/", machine + "/providers/Microsoft.AzureStackHCI/virtualMachineInstances/default", strings.Replace(machine, "providers/", "", 1), strings.TrimPrefix(resourceID(vmType, "other"), "/"), strings.Replace(machine, testSubscription, testTenant, 1), machine + "?query=true", machine + "/../other"} {
		invoke.Parameters = map[string]any{"resourceUri": parent}
		if _, err := r.Invoke(ctx, invoke); err == nil {
			t.Error("foreign or malformed local parent accepted", parent)
		}
	}
	if calls != 1 {
		t.Fatal("invalid scope reached transport", calls)
	}
}
