package azure

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func TestKustoNativeSchemasAndProvenance(t *testing.T) {
	payload, err := os.ReadFile("fixtures/kusto/sources.json")
	var manifest []map[string]string
	if err != nil || json.Unmarshal(payload, &manifest) != nil || len(manifest) != 42 {
		t.Fatal("invalid Kusto manifest")
	}
	payload, _ = os.ReadFile("catalog/source/swagger.json")
	var set catalog.RESTDocumentSet
	if json.Unmarshal(payload, &set) != nil {
		t.Fatal("invalid native catalog")
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	var source catalog.RESTSourceDocument
	for _, doc := range set.Documents {
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(doc.Document))
		if err := compiler.AddResource(doc.SourceURI, value); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(doc.SourceURI, "/specification/azure-kusto/") {
			source = doc
		}
	}
	if source.SourceSHA256 != "6c09537668b6efc3a76e6a96f29572aac2457d4188c162f8c74cd8f1690bc59b" {
		t.Fatal("Kusto native source changed")
	}
	var native map[string]any
	json.Unmarshal(source.Document, &native)
	checked := 0
	for _, entry := range manifest {
		payload, err := os.ReadFile("fixtures/kusto/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] || !strings.Contains(entry["source_uri"], "/e45039baa985c442877529906e705982a6e0099d/") {
			t.Fatal("native Kusto example changed", entry["file"])
		}
		for path, methods := range object(native["paths"]) {
			for method, value := range object(methods) {
				op := object(value)
				if op["operationId"] != entry["operation"] || object(object(op["responses"])["200"])["schema"] == nil {
					continue
				}
				schema, err := compiler.Compile(source.SourceURI + "#/paths/" + strings.ReplaceAll(path, "/", "~1") + "/" + method + "/responses/200/schema")
				if err != nil {
					t.Fatal(err)
				}
				example, err := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
				if err != nil {
					t.Fatal(err)
				}
				body := object(object(object(example)["responses"])["200"])["body"]
				if err := schema.Validate(body); err != nil {
					t.Errorf("%s: %v", entry["file"], err)
				}
				checked++
			}
		}
	}
	if checked != 31 {
		t.Fatal("missing Kusto schemas", checked)
	}
	// Swagger's discriminator does not itself validate subtype properties in
	// Draft 4. Validate the selected original bodies against their concrete types.
	for example, definition := range map[string]string{
		"KustoDatabasesGet":                                   "ReadWriteDatabase",
		"KustoDataConnectionsGet":                             "EventHubDataConnection",
		"KustoDataConnectionsEventGridGet":                    "EventGridDataConnection",
		"KustoDataConnectionsCosmosDbGet":                     "CosmosDbDataConnection",
		"KustoDataConnectionsEventHubWithManagedIdentityGet":  "EventHubDataConnectionWithManagedIdentity",
		"KustoDataConnectionsEventGridWithManagedIdentityGet": "EventGridDataConnectionWithManagedIdentity",
	} {
		schema, err := compiler.Compile(source.SourceURI + "#/definitions/" + definition)
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(kustoExampleBody(t, example)); err != nil {
			t.Errorf("concrete %s: %v", example, err)
		}
	}
}
