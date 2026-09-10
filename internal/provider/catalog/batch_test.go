package catalog

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func batchCatalogSource(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	// Minimal protocol fixture. Native Batch examples and schemas are retained
	// and verified by the Azure provider's source tests.
	var document map[string]any
	json.Unmarshal([]byte(`{"swagger":"2.0","info":{"title":"Azure Batch","version":"2025-06-01"},"x-ms-parameterized-host":{"hostTemplate":"{endpoint}","useSchemePrefix":false,"parameters":[{"name":"endpoint","in":"path","required":true,"type":"string","format":"uri","x-ms-skip-url-encoding":true}]},"paths":{"/jobs/{jobId}":{"get":{"operationId":"Jobs_GetJob","parameters":[{"name":"jobId","in":"path","type":"string","required":true}],"responses":{"200":{"schema":{"type":"object"}}}},"delete":{"operationId":"Jobs_DeleteJob","parameters":[{"name":"jobId","in":"path","type":"string","required":true},{"name":"If-Match","in":"header","type":"string"}],"responses":{"202":{}}}},"/jobs":{"get":{"operationId":"Jobs_ListJobs","x-ms-pageable":{"nextLinkName":"odata.nextLink"},"responses":{"200":{"schema":{"type":"object"}}}}},"/pools/{poolId}/removenodes":{"post":{"operationId":"Pools_RemoveNodes","parameters":[{"name":"poolId","in":"path","type":"string","required":true},{"name":"removeOptions","in":"body","required":true,"schema":{"type":"object"}}],"responses":{"202":{}}}}}}`), &document)
	if change != nil {
		change(document)
	}
	raw, _ := json.Marshal(document)
	source, _ := json.Marshal(RESTDocumentSet{Documents: []RESTSourceDocument{{SourceURI: "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/e45039baa985c442877529906e705982a6e0099d/specification/batch/data-plane/Batch/stable/2025-06-01/BatchService.json", SourceSHA256: strings.Repeat("a", 64), Document: raw}}})
	return source
}

func TestAzureBatchParameterizedHostAndNativeOperations(t *testing.T) {
	source := batchCatalogSource(t, nil)
	c, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", source)
	if err != nil {
		t.Fatal(err)
	}
	op := requireRESTOperation(t, c, "Azure.Microsoft.Batch.DataPlane.Jobs_DeleteJob")
	if op.Call.Style != "azure-batch-rest" || op.Call.Endpoint != "{endpoint}" || !op.Destructive {
		t.Fatal("Batch data plane was turned into an ARM call", op)
	}
	request, err := BindREST(op, map[string]any{"endpoint": "https://account.eastus2.batch.azure.com", "jobId": "Schedule:job-1", "If-Match": "0x1234"})
	if err != nil || request.URL != "https://account.eastus2.batch.azure.com/jobs/Schedule:job-1?api-version=2025-06-01" || request.Headers["If-Match"] != "0x1234" {
		t.Fatal(request, err)
	}
	listing := requireRESTOperation(t, c, "Azure.Microsoft.Batch.DataPlane.Jobs_ListJobs")
	if listing.Pagination.OutputTokenPath != "odata.nextLink" {
		t.Fatal("lost dotted native continuation field")
	}
	remove := requireRESTOperation(t, c, "Azure.Microsoft.Batch.DataPlane.Pools_RemoveNodes")
	request, err = BindREST(remove, map[string]any{"endpoint": "https://account.eastus2.batch.azure.com", "poolId": "pool", "removeOptions": map[string]any{"nodeList": []string{"node"}, "nodeDeallocationOption": "requeue"}})
	if err != nil || !remove.Destructive || request.Method != "POST" || !strings.Contains(string(request.Body), `"nodeList":["node"]`) {
		t.Fatal("lost native node removal", request, err)
	}
	assertRESTDeterministic(t, "azure-openapi", asset.ProviderAzure, source)
	for _, endpoint := range []any{nil, "https://batch.azure.com", "http://account.eastus2.batch.azure.com", "https://account.eastus2.batch.azure.com.evil.invalid", "https://account.eastus2.batch.azure.com:443", "https://user@account.eastus2.batch.azure.com", "https://account.eastus2.batch.azure.com/jobs", "https://account.eastus2.batch.azure.com?token=secret", "https://account.eastus2.batch.azure.com#fragment", "https://management.azure.com", "https://a.eastus2.batch.azure.com"} {
		if _, err := BindREST(op, map[string]any{"endpoint": endpoint, "jobId": "job"}); err == nil {
			t.Fatalf("unsafe Batch origin accepted: %v", endpoint)
		}
	}
	for _, params := range []map[string]any{{"endpoint": "https://account.eastus2.batch.azure.com", "jobId": "../other"}, {"endpoint": "https://account.eastus2.batch.azure.com", "jobId": "job", "api-version": "2024-02-01"}, {"endpoint": "https://account.eastus2.batch.azure.com", "jobId": "job", "If-Match": "secret\r\nInjected: true"}} {
		if _, err := BindREST(op, params); err == nil {
			t.Fatal("unsafe Batch parameters accepted")
		}
	}
}

func TestAzureBatchRejectsUnsupportedHostTemplates(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"template": func(d map[string]any) {
			d["x-ms-parameterized-host"].(map[string]any)["hostTemplate"] = "{endpoint}.evil.invalid"
		},
		"scheme":                 func(d map[string]any) { d["x-ms-parameterized-host"].(map[string]any)["useSchemePrefix"] = true },
		"different service":      func(d map[string]any) { d["info"].(map[string]any)["title"] = "Other" },
		"conflicting fixed host": func(d map[string]any) { d["host"] = "management.azure.com" },
		"path prefix":            func(d map[string]any) { d["basePath"] = "/different" },
		"optional endpoint": func(d map[string]any) {
			d["x-ms-parameterized-host"].(map[string]any)["parameters"].([]any)[0].(map[string]any)["required"] = false
		},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", batchCatalogSource(t, change)); err == nil {
				t.Fatal("unsupported host accepted")
			}
		})
	}
}
