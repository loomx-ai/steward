package catalog

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func keyVaultCatalogSource(t *testing.T, change func(map[string]any)) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal([]byte(`{"swagger":"2.0","info":{"title":"KeyVaultClient","version":"2025-07-01"},"x-ms-parameterized-host":{"hostTemplate":"{vaultBaseUrl}","useSchemePrefix":false,"parameters":[{"name":"vaultBaseUrl","in":"path","required":true,"type":"string","format":"uri","x-ms-skip-url-encoding":true}]},"paths":{"/certificates":{"get":{"operationId":"GetCertificates","parameters":[{"name":"api-version","in":"query","required":true,"type":"string"},{"name":"maxresults","in":"query","required":false,"type":"integer","format":"int32","minimum":1,"maximum":25}],"responses":{"200":{}}}},"/certificates/{certificate-name}/{certificate-version}":{"get":{"operationId":"GetCertificate","parameters":[{"name":"api-version","in":"query","required":true,"type":"string"},{"name":"certificate-name","in":"path","required":true,"type":"string","x-ms-client-name":"certificateName"},{"name":"certificate-version","in":"path","required":true,"type":"string","x-ms-client-name":"certificateVersion"}],"responses":{"200":{}}}}}}`), &document); err != nil {
		t.Fatal(err)
	}
	if change != nil {
		change(document)
	}
	raw, _ := json.Marshal(document)
	source, _ := json.Marshal(RESTDocumentSet{Documents: []RESTSourceDocument{{SourceURI: "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/7a636fbb260b428a0798320e9fe72ac62acdd1e3/specification/keyvault/data-plane/Certificates/stable/2025-07-01/certificates.json", SourceSHA256: strings.Repeat("a", 64), Document: raw}}})
	return source
}

func TestAzureKeyVaultNativeEndpointBinding(t *testing.T) {
	source := keyVaultCatalogSource(t, nil)
	c, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", source)
	if err != nil {
		t.Fatal(err)
	}
	assertRESTDeterministic(t, "azure-openapi", asset.ProviderAzure, source)
	get := requireRESTOperation(t, c, "Azure.Microsoft.KeyVault.DataPlane.GetCertificate")
	if get.Call.Style != "azure-keyvault-rest" || get.Path != "/certificates/{certificateName}/{certificateVersion}" || get.Destructive {
		t.Fatal(get.Call, get.Path)
	}
	bound, err := BindREST(get, map[string]any{"vaultBaseUrl": "https://myvault.vault.azure.net", "certificateName": "cert-1"})
	if err != nil || bound.URL != "https://myvault.vault.azure.net/certificates/cert-1/?api-version=2025-07-01" || bound.Method != "GET" {
		t.Fatal(bound, err)
	}
	bound, err = BindREST(get, map[string]any{"vaultBaseUrl": "https://myvault.vault.azure.net", "certificateName": "cert-1", "certificateVersion": "f60f2a4f8ae442cfb41ca2090bd4b769"})
	if err != nil || bound.URL != "https://myvault.vault.azure.net/certificates/cert-1/f60f2a4f8ae442cfb41ca2090bd4b769?api-version=2025-07-01" {
		t.Fatal(bound, err)
	}
	list := requireRESTOperation(t, c, "Azure.Microsoft.KeyVault.DataPlane.GetCertificates")
	if bound, err = BindREST(list, map[string]any{"vaultBaseUrl": "https://myvault.vault.azure.net"}); err != nil || bound.URL != "https://myvault.vault.azure.net/certificates?api-version=2025-07-01" {
		t.Fatal(bound, err)
	}
	for _, endpoint := range []any{"myvault.vault.azure.net", "http://myvault.vault.azure.net", "https://vault.azure.net", "https://a.b.vault.azure.net", "https://myvault.vault.azure.net/", "https://myvault.vault.azure.net:443", "https://myvault.vault.azure.net.evil.invalid", "https://user@myvault.vault.azure.net", "https://my_vault.vault.azure.net", "https://management.azure.com", 42} {
		if _, err := BindREST(list, map[string]any{"vaultBaseUrl": endpoint}); err == nil {
			t.Fatal("accepted Key Vault endpoint", endpoint)
		}
	}
	for name, value := range map[string]any{"certificateName": "../other", "api-version": "7.4", "certificate-name": "cert"} {
		parameters := map[string]any{"vaultBaseUrl": "https://myvault.vault.azure.net", "certificateName": "cert"}
		parameters[name] = value
		if _, err := BindREST(get, parameters); err == nil {
			t.Fatal("accepted parameter", name, value)
		}
	}
	for name, change := range map[string]func(map[string]any){
		"host-template": func(d map[string]any) { d["x-ms-parameterized-host"].(map[string]any)["hostTemplate"] = "{endpoint}" },
		"base-path":     func(d map[string]any) { d["basePath"] = "/v1" },
		"collision": func(d map[string]any) {
			op := d["paths"].(map[string]any)["/certificates/{certificate-name}/{certificate-version}"].(map[string]any)["get"].(map[string]any)
			op["parameters"] = append(op["parameters"].([]any), map[string]any{"name": "certificateName", "in": "query", "type": "string"})
		},
	} {
		if _, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", keyVaultCatalogSource(t, change)); err == nil {
			t.Fatal("accepted unsupported Key Vault contract", name)
		}
	}
}
