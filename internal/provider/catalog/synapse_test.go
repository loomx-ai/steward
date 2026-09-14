package catalog

import (
	"encoding/json"
	"maps"
	"math"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func synapseCatalogSource(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(`{"swagger":"2.0","info":{"title":"SparkClient","version":"2020-12-01"},"x-ms-parameterized-host":{"hostTemplate":"{endpoint}","useSchemePrefix":false,"parameters":[{"$ref":"#/parameters/Endpoint"}]},"parameters":{"Endpoint":{"name":"endpoint","in":"path","required":true,"type":"string","x-ms-skip-url-encoding":true}},"paths":{"/livyApi/versions/{livyApiVersion}/sparkPools/{sparkPoolName}/batches/{batchId}":{"delete":{"operationId":"SparkBatch_CancelSparkBatchJob","parameters":[{"name":"livyApiVersion","in":"path","type":"string","required":true,"x-ms-skip-url-encoding":true,"x-ms-client-default":"2019-11-01-preview"},{"name":"sparkPoolName","in":"path","type":"string","required":true,"x-ms-skip-url-encoding":true},{"name":"batchId","in":"path","type":"integer","format":"int32","required":true}],"responses":{"200":{}}}}}}`), &document); err != nil {
		t.Fatal(err)
	}
	if change != nil {
		change(document)
	}
	raw, _ := json.Marshal(document)
	source, _ := json.Marshal(RESTDocumentSet{Documents: []RESTSourceDocument{{SourceURI: "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/synapse/data-plane/Microsoft.Synapse/stable/2020-12-01/sparkJob.json", SourceSHA256: strings.Repeat("a", 64), Document: raw}}})
	return source
}

func TestAzureSynapseNativeEndpointBinding(t *testing.T) {
	source := synapseCatalogSource(t, nil)
	c, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", source)
	if err != nil {
		t.Fatal(err)
	}
	op := requireRESTOperation(t, c, "Azure.Microsoft.Synapse.DataPlane.SparkBatch_CancelSparkBatchJob")
	parameters := map[string]any{"endpoint": "https://workspace.dev.azuresynapse.net", "sparkPoolName": "sparkpool", "batchId": 123}
	bound, err := BindREST(op, parameters)
	if err != nil || bound.URL != "https://workspace.dev.azuresynapse.net/livyApi/versions/2020-12-01/sparkPools/sparkpool/batches/123" || bound.Method != "DELETE" || !op.Destructive || op.Call.Style != "azure-synapse-rest" || len(op.Call.RawPathParameters) != 0 {
		t.Fatal(bound, err)
	}
	if _, ok := op.InputSchema["properties"].(map[string]any)["api-version"]; ok {
		t.Fatal("invented Livy query version")
	}
	if _, ok := parameters["livyApiVersion"]; ok {
		t.Fatal("mutated caller parameters")
	}
	assertRESTDeterministic(t, "azure-openapi", asset.ProviderAzure, source)
	for _, id := range []any{0, int32(1), int64(2147483647), float64(123), json.Number("123")} {
		p := maps.Clone(parameters)
		p["batchId"] = id
		if _, err := BindREST(op, p); err != nil {
			t.Fatal(id, err)
		}
	}
	for name, values := range map[string][]any{
		"endpoint":       {"workspace.dev.azuresynapse.net", "http://workspace.dev.azuresynapse.net", "https://dev.azuresynapse.net", "https://other.workspace.dev.azuresynapse.net", "https://workspace.dev.azuresynapse.net.evil.invalid", "https://workspace.dev.azuresynapse.net:443", "https://user@workspace.dev.azuresynapse.net", "https://workspace.dev.azuresynapse.net/", "https://workspace.dev.azuresynapse.net?token=x", "https://workspace.dev.azuresynapse.net#fragment", "https://management.azure.com"},
		"sparkPoolName":  {"../other", "a/b", "a%2fb", "a\\b", "..", "", " pool", 42},
		"batchId":        {-1, 2147483648, 1.2, "123", true, json.Number("1.0"), math.NaN(), math.Inf(1), nil},
		"livyApiVersion": {"2019-11-01-preview", "2020-12-01/other"},
		"api-version":    {"2020-12-01"}, "detailed": {true},
	} {
		for _, v := range values {
			p := maps.Clone(parameters)
			p[name] = v
			if _, err := BindREST(op, p); err == nil {
				t.Errorf("accepted %s=%v", name, v)
			}
		}
	}
}

func TestAzureSynapseRejectsChangedHostContract(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"template": func(d map[string]any) {
			d["x-ms-parameterized-host"].(map[string]any)["hostTemplate"] = "{endpoint}.evil.invalid"
		},
		"scheme":     func(d map[string]any) { d["x-ms-parameterized-host"].(map[string]any)["useSchemePrefix"] = true },
		"service":    func(d map[string]any) { d["info"].(map[string]any)["title"] = "Other" },
		"fixed host": func(d map[string]any) { d["host"] = "management.azure.com" },
		"base path":  func(d map[string]any) { d["basePath"] = "/other" },
		"optional endpoint": func(d map[string]any) {
			d["parameters"].(map[string]any)["Endpoint"].(map[string]any)["required"] = false
		},
		"missing endpoint": func(d map[string]any) { delete(d["parameters"].(map[string]any), "Endpoint") },
		"missing Livy version": func(d map[string]any) {
			for _, item := range d["paths"].(map[string]any) {
				op := item.(map[string]any)["delete"].(map[string]any)
				op["parameters"].([]any)[0].(map[string]any)["in"] = "query"
			}
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", synapseCatalogSource(t, change)); err == nil {
				t.Fatal("accepted altered native host/version contract")
			}
		})
	}
}
