package catalog

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestGoogleDiscoveryImportsOfficialComputeOperations(t *testing.T) {
	source := restFixture(t, "google-compute")
	c, err := ImportOfficial("google-discovery", asset.ProviderGCP, "testdata/google-compute.json", source)
	if err != nil {
		t.Fatal(err)
	}
	get := requireRESTOperation(t, c, "compute.instances.get")
	if get.Method != "GET" || get.Path != "/compute/v1/projects/{project}/zones/{zone}/instances/{instance}" || get.Call.Endpoint != "https://compute.googleapis.com" || get.Destructive {
		t.Fatalf("unexpected instance read transport: %+v", get)
	}
	remove := requireRESTOperation(t, c, "compute.instances.delete")
	if !remove.Destructive || remove.Call.IdempotencyParameter != "requestId" || remove.SourceURI != "https://www.googleapis.com/discovery/v1/apis/compute/v1/rest" {
		t.Fatalf("missing deletion semantics/provenance: %+v", remove)
	}
	list := requireRESTOperation(t, c, "compute.instances.list")
	if list.Pagination == nil || list.Pagination.ItemsPath != "items" || list.Pagination.OutputTokenPath != "nextPageToken" {
		t.Fatalf("lost native pagination: %+v", list.Pagination)
	}
	assertRESTDeterministic(t, "google-discovery", asset.ProviderGCP, source)
}

func TestAzureOpenAPIImportsOfficialPublicIPOperations(t *testing.T) {
	source := restFixture(t, "azure-public-ip")
	c, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "testdata/azure-public-ip.json", source)
	if err != nil {
		t.Fatal(err)
	}
	get := requireRESTOperation(t, c, "Azure.Microsoft.Network.PublicIPAddresses_Get")
	if get.Call.Version != "2024-05-01" || get.Call.Endpoint != "https://management.azure.com" || !strings.HasSuffix(get.Path, "/publicIPAddresses/{publicIpAddressName}") || get.Destructive {
		t.Fatalf("unexpected ARM read transport: %+v", get)
	}
	remove := requireRESTOperation(t, c, "Azure.Microsoft.Network.PublicIPAddresses_Delete")
	if remove.Method != "DELETE" || !remove.Destructive {
		t.Fatalf("lost delete semantics: %+v", remove)
	}
	list := requireRESTOperation(t, c, "Azure.Microsoft.Network.PublicIPAddresses_List")
	if list.Pagination == nil || list.Pagination.OutputTokenPath != "nextLink" || list.Pagination.ItemsPath != "value" {
		t.Fatalf("lost ARM pagination: %+v", list)
	}
	properties := get.InputSchema["properties"].(map[string]any)
	if properties["subscriptionId"] == nil || properties["api-version"] == nil {
		t.Fatalf("missing path/version parameters: %+v", properties)
	}
	assertRESTDeterministic(t, "azure-openapi", asset.ProviderAzure, source)
}

func TestRESTImportRejectsWrongProviderAndInvalidSources(t *testing.T) {
	for _, test := range []struct {
		name, format string
		provider     asset.Provider
	}{
		{"google-compute", "google-discovery", asset.ProviderGCP},
		{"azure-public-ip", "azure-openapi", asset.ProviderAzure},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := restFixture(t, test.name)
			if _, err := ImportOfficial(test.format, asset.ProviderAWS, "fixture", source); err == nil {
				t.Fatal("cross-provider source accepted")
			}
			var set RESTDocumentSet
			if err := json.Unmarshal(source, &set); err != nil {
				t.Fatal(err)
			}
			set.Documents = append(set.Documents, set.Documents[0])
			duplicate, _ := json.Marshal(set)
			if _, err := ImportOfficial(test.format, test.provider, "fixture", duplicate); err == nil || !strings.Contains(err.Error(), "duplicate") {
				t.Fatalf("duplicate operations accepted: %v", err)
			}
			set.Documents = set.Documents[:1]
			set.Documents[0].SourceSHA256 = strings.Repeat("z", 64)
			invalidHash, _ := json.Marshal(set)
			if _, err := ImportOfficial(test.format, test.provider, "fixture", invalidHash); err == nil {
				t.Fatal("invalid upstream fingerprint accepted")
			}
			set.Documents[0].SourceSHA256 = strings.Repeat("1", 64)
			set.Documents[0].Document = bytes.ReplaceAll(set.Documents[0].Document, []byte("management.azure.com"), []byte("untrusted.example"))
			set.Documents[0].Document = bytes.ReplaceAll(set.Documents[0].Document, []byte("compute.googleapis.com"), []byte("untrusted.example"))
			invalidHost, _ := json.Marshal(set)
			if _, err := ImportOfficial(test.format, test.provider, "fixture", invalidHost); err == nil {
				t.Fatal("untrusted API endpoint accepted")
			}
		})
	}
}

func TestDiscoveryPreservesExpandedResourceNameParameters(t *testing.T) {
	set := RESTDocumentSet{Documents: []RESTSourceDocument{{SourceURI: "https://run.googleapis.com/$discovery/rest?version=v2", SourceSHA256: strings.Repeat("a", 64), Document: json.RawMessage(`{"name":"run","version":"v2","rootUrl":"https://run.googleapis.com/","servicePath":"","resources":{"projects":{"resources":{"locations":{"resources":{"services":{"methods":{"delete":{"id":"run.projects.locations.services.delete","httpMethod":"DELETE","path":"v2/{+name}","parameters":{"name":{"type":"string","location":"path"}}}}}}}}}}}`)}}}
	source, _ := json.Marshal(set)
	c, err := ImportOfficial("google-discovery", asset.ProviderGCP, "fixture", source)
	if err != nil {
		t.Fatal(err)
	}
	op := requireRESTOperation(t, c, "run.projects.locations.services.delete")
	if op.Path != "/v2/{name}" || len(op.Call.RawPathParameters) != 1 || op.Call.RawPathParameters[0] != "name" {
		t.Fatalf("expanded name lost: %+v", op)
	}
}

func restFixture(t *testing.T, name string) []byte {
	t.Helper()
	source, err := os.ReadFile("testdata/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	return source
}

func requireRESTOperation(t *testing.T, c Catalog, id string) Operation {
	t.Helper()
	operation, ok := c.Operation(id)
	if !ok || operation.Call == nil {
		t.Fatalf("native operation %s missing", id)
	}
	return operation
}

func assertRESTDeterministic(t *testing.T, format string, provider asset.Provider, source []byte) {
	t.Helper()
	var first []byte
	for i := 0; i < 5; i++ {
		c, err := ImportOfficial(format, provider, "fixture", source)
		if err != nil {
			t.Fatal(err)
		}
		encoded, err := MarshalGenerated(c)
		if err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			first = encoded
		} else if !bytes.Equal(first, encoded) {
			t.Fatal("catalog generation depends on map iteration order")
		}
		if _, err := UnmarshalGenerated(encoded); err != nil {
			t.Fatalf("generated catalog fails checksum/readback: %v", err)
		}
	}
}

func TestAzureReferencesCannotDisappearSilently(t *testing.T) {
	var set RESTDocumentSet
	if err := json.Unmarshal(restFixture(t, "azure-public-ip"), &set); err != nil {
		t.Fatal(err)
	}
	set.Documents = set.Documents[:1]
	raw, _ := json.Marshal(set)
	if _, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", raw); err == nil || !strings.Contains(err.Error(), "unresolved Azure reference") {
		t.Fatalf("missing source parameter dependency accepted: %v", err)
	}
}

func TestAzureDNSNativeOperationNameCollisions(t *testing.T) {
	source := restFixture(t, "azure-dns-records")
	c, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", source)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ title, collection, version string }{
		{"DnsManagementClient", "dnsZones", "2018-05-01"},
		{"PrivateDnsManagementClient", "privateDnsZones", "2024-06-01"},
	} {
		for _, name := range []string{"RecordSets_Get", "RecordSets_Delete", "RecordSets_ListByType"} {
			op := requireRESTOperation(t, c, "Azure.Microsoft.Network."+tc.title+"."+name)
			if op.Name != name || op.Call.Version != tc.version || !strings.Contains(op.Path, "/"+tc.collection+"/") || !strings.Contains(op.SourceURI, "/"+tc.version+"/") {
				t.Fatalf("native DNS identity/transport changed: %+v", op)
			}
		}
	}
	var set RESTDocumentSet
	if err := json.Unmarshal(source, &set); err != nil {
		t.Fatal(err)
	}
	slices.Reverse(set.Documents)
	reordered, _ := json.Marshal(set)
	again, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", reordered)
	if err != nil || !reflect.DeepEqual(c.Operations, again.Operations) {
		t.Fatalf("DNS qualification depends on source order: %v", err)
	}
	set.Documents = append(set.Documents, set.Documents[0])
	duplicate, _ := json.Marshal(set)
	if _, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", duplicate); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("same-document collision accepted: %v", err)
	}
	assertRESTDeterministic(t, "azure-openapi", asset.ProviderAzure, source)
}

func TestAzureNativeOperationTitlesWithSpaces(t *testing.T) {
	source := bytes.ReplaceAll(restFixture(t, "azure-dns-records"), []byte(`"title": "DnsManagementClient"`), []byte(`"title": "Cosmos DB"`))
	c, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", source)
	if err != nil {
		t.Fatal(err)
	}
	op := requireRESTOperation(t, c, "Azure.Microsoft.Network.Cosmos_DB.RecordSets_Get")
	if op.Name != "RecordSets_Get" || !strings.Contains(op.Path, "/dnsZones/") {
		t.Fatal("document-title qualification changed native operation metadata")
	}
	// Normalization cannot silently combine two different API documents.
	duplicate := bytes.ReplaceAll(source, []byte(`"title": "PrivateDnsManagementClient"`), []byte(`"title": "Cosmos_DB"`))
	if _, err := ImportOfficial("azure-openapi", asset.ProviderAzure, "fixture", duplicate); err == nil {
		t.Fatal("normalized document-title collision was accepted")
	}
	assertRESTDeterministic(t, "azure-openapi", asset.ProviderAzure, source)
}
