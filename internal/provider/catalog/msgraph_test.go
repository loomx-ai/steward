package catalog

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func msgraphCatalogSource(t *testing.T, uri string, change func(map[string]any)) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(`{"openapi":"3.0.4","info":{"version":"v1.0"},"servers":[{"url":"https://graph.microsoft.com/v1.0"}],"paths":{"/users":{"get":{"operationId":"users.user.ListUser","parameters":[{"$ref":"#/components/parameters/top"},{"name":"$select","in":"query","schema":{"type":"array","items":{"type":"string"}}}],"responses":{"2XX":{"$ref":"#/components/responses/microsoft.graph.userCollectionResponse"}}}},"/users/{user-id}":{"parameters":[{"name":"user-id","in":"path","required":true,"schema":{"type":"string"}}],"get":{"operationId":"users.user.GetUser","responses":{"2XX":{"description":"Retrieved entity"}}},"delete":{"operationId":"users.user.DeleteUser","responses":{"204":{}}}}},"components":{"parameters":{"top":{"name":"$top","in":"query","schema":{"type":"integer"}}}}}`), &document); err != nil {
		t.Fatal(err)
	}
	if change != nil {
		change(document)
	}
	raw, _ := json.Marshal(document)
	source, _ := json.Marshal(RESTDocumentSet{Documents: []RESTSourceDocument{{SourceURI: uri, SourceSHA256: strings.Repeat("a", 64), SourceFormat: msgraphSourceFormat, Document: raw}}})
	return source
}

func TestAzureMicrosoftGraphOperations(t *testing.T) {
	uri := "https://raw.githubusercontent.com/microsoftgraph/msgraph-metadata/28e4d24f3547898328eb52c700f2beb869116cfe/openapi/v1.0/openapi.yaml"
	source := msgraphCatalogSource(t, uri, nil)
	c, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", source)
	if err != nil {
		t.Fatal(err)
	}
	assertRESTDeterministic(t, "azure-openapi", asset.ProviderAzure, source)
	list := requireRESTOperation(t, c, "Azure.Microsoft.Graph.users.user.ListUser")
	if list.Call.Style != "azure-graph-rest" || list.Pagination == nil || list.Pagination.OutputTokenPath != "@odata.nextLink" || list.Destructive {
		t.Fatal(list.Call, list.Pagination)
	}
	bound, err := BindREST(list, map[string]any{"$select": "id,displayName", "$top": 999})
	if err != nil || bound.URL != "https://graph.microsoft.com/v1.0/users?%24select=id%2CdisplayName&%24top=999" {
		t.Fatal(bound, err)
	}
	get := requireRESTOperation(t, c, "Azure.Microsoft.Graph.users.user.GetUser")
	if bound, err = BindREST(get, map[string]any{"userId": "6e7b768e-07e2-4810-8459-485f84f8f204"}); err != nil || bound.URL != "https://graph.microsoft.com/v1.0/users/6e7b768e-07e2-4810-8459-485f84f8f204" || get.Pagination != nil {
		t.Fatal(bound, err)
	}
	if deletion := requireRESTOperation(t, c, "Azure.Microsoft.Graph.users.user.DeleteUser"); !deletion.Destructive {
		t.Fatal("Graph deletion was not marked destructive")
	}
	for _, value := range []any{"../me", "a/b", ""} {
		if _, err := BindREST(get, map[string]any{"userId": value}); err == nil {
			t.Fatal("accepted Graph object path", value)
		}
	}
	for name, source := range map[string][]byte{
		"foreign repository": msgraphCatalogSource(t, "https://raw.githubusercontent.com/example/metadata/main/openapi.yaml", nil),
		"beta service": msgraphCatalogSource(t, uri, func(d map[string]any) {
			d["servers"] = []any{map[string]any{"url": "https://graph.microsoft.com/beta"}}
		}),
		"foreign server": msgraphCatalogSource(t, uri, func(d map[string]any) { d["servers"] = []any{map[string]any{"url": "https://graph.evil.invalid/v1.0"}} }),
		"missing parameter": msgraphCatalogSource(t, uri, func(d map[string]any) {
			delete(d["components"].(map[string]any)["parameters"].(map[string]any), "top")
		}),
	} {
		if _, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", source); err == nil {
			t.Fatal("accepted unsupported Microsoft Graph source", name)
		}
	}
}
