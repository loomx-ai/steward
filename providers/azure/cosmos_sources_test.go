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

func TestCosmosNativeSchemasAndProvenance(t *testing.T) {
	payload, _ := os.ReadFile("fixtures/cosmos/sources.json")
	var manifest []map[string]string
	if json.Unmarshal(payload, &manifest) != nil || len(manifest) != 117 {
		t.Fatal("invalid Cosmos source manifest")
	}
	payload, _ = os.ReadFile("catalog/source/swagger.json")
	var set catalog.RESTDocumentSet
	if json.Unmarshal(payload, &set) != nil {
		t.Fatal("invalid native catalog")
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	for _, doc := range set.Documents {
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(doc.Document))
		if err := compiler.AddResource(doc.SourceURI, value); err != nil {
			t.Fatal(err)
		}
	}
	checked := 0
	for _, entry := range manifest {
		payload, err := os.ReadFile("fixtures/cosmos/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] || !strings.Contains(entry["source_uri"], "/e45039baa985c442877529906e705982a6e0099d/") {
			t.Fatal("official Cosmos example changed")
		}
		var example map[string]any
		json.Unmarshal(payload, &example)
		for _, doc := range set.Documents {
			if !strings.Contains(doc.SourceURI, "/specification/cosmos-db/") {
				continue
			}
			var native map[string]any
			json.Unmarshal(doc.Document, &native)
			for path, item := range object(native["paths"]) {
				op := object(object(item)["get"])
				if text(op["operationId"]) != entry["operation"] {
					continue
				}
				schema, err := compiler.Compile(doc.SourceURI + "#/paths/" + strings.ReplaceAll(path, "/", "~1") + "/get/responses/200/schema")
				if err != nil {
					t.Fatal(err)
				}
				body, _ := json.Marshal(object(object(example["responses"])["200"])["body"])
				value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(body))
				err = schema.Validate(value)
				if entry["file"] == "CosmosDBSqlTriggerGet.json" || entry["file"] == "CosmosDBSqlTriggerList.json" {
					// The original files contain literal triggerType/triggerOperation
					// placeholders, outside their own enums. Require that exact defect,
					// then validate a test-only copy with the documented native values.
					if err == nil {
						t.Fatal("native trigger placeholder defect changed")
					}
					var patched map[string]any
					json.Unmarshal(body, &patched)
					resources := []any{patched}
					if values, ok := patched["value"].([]any); ok {
						resources = values
					}
					for _, item := range resources {
						resource := object(object(object(item)["properties"])["resource"])
						if resource["triggerType"] != "triggerType" || resource["triggerOperation"] != "triggerOperation" {
							t.Fatal("unexpected native trigger defect")
						}
						resource["triggerType"], resource["triggerOperation"] = "Pre", "All"
					}
					body, _ := json.Marshal(patched)
					value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(body))
					err = schema.Validate(value)
				}
				if err != nil {
					t.Errorf("%s: %v", entry["file"], err)
				}
				checked++
			}
		}
	}
	if checked != 81 {
		t.Fatal("missing Cosmos schema checks", checked)
	}
}
