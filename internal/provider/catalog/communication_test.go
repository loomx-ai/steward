package catalog

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func communicationCatalogSource(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	// A small importer boundary fixture; provider tests retain the unchanged
	// native Swagger examples and verify their complete response schemas.
	var document map[string]any
	json.Unmarshal([]byte(`{"swagger":"2.0","info":{"title":"PhoneNumbersClient","version":"2025-06-01"},"x-ms-parameterized-host":{"hostTemplate":"{endpoint}","useSchemePrefix":false,"parameters":[{"$ref":"#/parameters/Endpoint"}]},"parameters":{"Endpoint":{"name":"endpoint","in":"path","required":true,"type":"string","format":"url","x-ms-skip-url-encoding":true}},"paths":{"/phoneNumbers/{phoneNumber}":{"delete":{"operationId":"PhoneNumbers_ReleasePhoneNumber","parameters":[{"name":"phoneNumber","in":"path","type":"string","required":true}],"responses":{"202":{}}}},"/phoneNumbers":{"get":{"operationId":"PhoneNumbers_ListPhoneNumbers","x-ms-pageable":{"nextLinkName":"nextLink","itemName":"phoneNumbers"},"responses":{"200":{"schema":{"type":"object"}}}}}}}`), &document)
	if change != nil {
		change(document)
	}
	raw, _ := json.Marshal(document)
	source, _ := json.Marshal(RESTDocumentSet{Documents: []RESTSourceDocument{{SourceURI: "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/communication/data-plane/PhoneNumbers/stable/2025-06-01/phonenumbers.json", SourceSHA256: strings.Repeat("a", 64), Document: raw}}})
	return source
}

func TestAzureCommunicationNativeEndpointBinding(t *testing.T) {
	source := communicationCatalogSource(t, nil)
	c, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", source)
	if err != nil {
		t.Fatal(err)
	}
	op := requireRESTOperation(t, c, "Azure.Microsoft.Communication.DataPlane.PhoneNumbers_ReleasePhoneNumber")
	bound, err := BindREST(op, map[string]any{"endpoint": "https://account.communication.azure.com", "phoneNumber": "+12065551234"})
	if err != nil || bound.Method != "DELETE" || bound.URL != "https://account.communication.azure.com/phoneNumbers/+12065551234?api-version=2025-06-01" || !op.Destructive || op.Call.Style != "azure-communication-rest" {
		t.Fatal("lost native Communication request binding", bound, err)
	}
	list := requireRESTOperation(t, c, "Azure.Microsoft.Communication.DataPlane.PhoneNumbers_ListPhoneNumbers")
	if list.Pagination.OutputTokenPath != "nextLink" || list.Pagination.ItemsPath != "phoneNumbers" {
		t.Fatal("lost native phone-number pagination")
	}
	assertRESTDeterministic(t, "azure-openapi", asset.ProviderAzure, source)
	for _, endpoint := range []string{"https://communication.azure.com", "http://account.communication.azure.com", "https://account.communication.azure.com.evil.invalid", "https://account.communication.azure.com:443", "https://user@account.communication.azure.com", "https://account.communication.azure.com/phoneNumbers", "https://account.communication.azure.com?token=secret", "https://account.communication.azure.com#fragment", "https://management.azure.com", "https://account..communication.azure.com"} {
		if _, err := BindREST(op, map[string]any{"endpoint": endpoint, "phoneNumber": "+12065551234"}); err == nil {
			t.Fatal("accepted a foreign Communication origin", endpoint)
		}
	}
	if _, err := BindREST(op, map[string]any{"endpoint": "https://account.communication.azure.com", "phoneNumber": "+12065551234", "api-version": "2021-03-07"}); err == nil {
		t.Fatal("accepted a different native API version")
	}
}

func TestAzureCommunicationRejectsChangedHostContract(t *testing.T) {
	for name, change := range map[string]func(map[string]any){
		"template": func(d map[string]any) {
			d["x-ms-parameterized-host"].(map[string]any)["hostTemplate"] = "{endpoint}.evil.invalid"
		},
		"scheme":                 func(d map[string]any) { d["x-ms-parameterized-host"].(map[string]any)["useSchemePrefix"] = true },
		"different service":      func(d map[string]any) { d["info"].(map[string]any)["title"] = "Other" },
		"conflicting fixed host": func(d map[string]any) { d["host"] = "management.azure.com" },
		"path prefix":            func(d map[string]any) { d["basePath"] = "/different" },
		"optional endpoint": func(d map[string]any) {
			d["parameters"].(map[string]any)["Endpoint"].(map[string]any)["required"] = false
		},
		"missing reference": func(d map[string]any) { delete(d["parameters"].(map[string]any), "Endpoint") },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", communicationCatalogSource(t, change)); err == nil {
				t.Fatal("accepted an unsupported endpoint contract")
			}
		})
	}
}
