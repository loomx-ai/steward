package azure

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func fleetExample(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("fixtures/fleet/" + name)
	var result map[string]any
	if err != nil || json.Unmarshal(raw, &result) != nil {
		t.Fatal("invalid native Fleet example", name, err)
	}
	return result
}

func TestFleetNativeSources(t *testing.T) {
	const pin = "/6005e166d7172cb62fd2894971dbfe1910ac5285/"
	read := func(name, digest string, target any) {
		t.Helper()
		raw, err := os.ReadFile("fixtures/fleet/" + name)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != digest || json.Unmarshal(raw, target) != nil {
			t.Fatal("native Fleet evidence changed", name, err)
		}
	}
	var examples []map[string]string
	read("sources.json", "0461d05aa2cbbdf8f1f242064dd1d6588869f237747b83708fa0ac6ad2540d96", &examples)
	var expected []catalog.RESTSourceDocument
	read("documents.json", "75bd395f77c8b9c3279f73a0dae4d1e29bd6e393bca6cffea0dcbd0049cb5e0f", &expected)
	if len(examples) != 22 || len(expected) != 3 {
		t.Fatal("incomplete Fleet native evidence")
	}
	raw, err := os.ReadFile("catalog/source/swagger.json")
	var set catalog.RESTDocumentSet
	if err != nil || json.Unmarshal(raw, &set) != nil {
		t.Fatal("invalid catalog source", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	sources, roots := map[string]catalog.RESTSourceDocument{}, map[string]bool{}
	for _, doc := range set.Documents {
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc.Document))
		if err != nil || compiler.AddResource(doc.SourceURI, applicationInsightsSwaggerNullability(value)) != nil {
			t.Fatal("invalid native schema", doc.SourceURI, err)
		}
		sources[doc.SourceURI] = doc
	}
	for _, doc := range expected {
		actual, ok := sources[doc.SourceURI]
		if !ok || actual.SourceSHA256 != doc.SourceSHA256 || actual.Dependency != doc.Dependency || !strings.Contains(doc.SourceURI, pin) {
			t.Fatal("Fleet document provenance changed", doc.SourceURI)
		}
		roots[doc.SourceURI] = !doc.Dependency
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	operations := map[string]bool{}
	responses, bodies := 0, 0
	for _, example := range examples {
		t.Run(example["file"], func(t *testing.T) {
			var body map[string]any
			read(example["file"], example["source_sha256"], &body)
			op, ok := metadata.catalog.Operation("Azure.Microsoft.ContainerService." + example["operation"])
			if !ok || !roots[example["source_document"]] || op.SourceURI != example["source_document"] || op.Call.Method != example["method"] || op.Call.Path != example["path"] || !strings.Contains(example["source_uri"], pin) {
				t.Fatal("Fleet example has no native operation")
			}
			operations[op.ID] = true
			if _, err := bindAzureREST(op, object(body["parameters"])); err != nil {
				t.Fatal("native example request does not bind", err)
			}
			for status, raw := range object(body["responses"]) {
				responses++
				value, exists := object(raw)["body"]
				if !exists {
					continue
				}
				pointer := "#/paths/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(example["path"]) + "/" + strings.ToLower(example["method"]) + "/responses/" + status + "/schema"
				schema, err := compiler.Compile(example["source_document"] + pointer)
				if err != nil {
					t.Fatal("invalid native Fleet response schema", err)
				}
				// Two upstream lists use nextLink:null against a string schema.
				// Preserve the original bytes and assert this precise discrepancy.
				if example["file"] == "FleetMembers_ListByFleet.json" || example["file"] == "Gates_ListByFleet.json" {
					if schema.Validate(value) == nil || object(value)["nextLink"] != nil {
						t.Fatal("native null-nextLink discrepancy changed")
					}
					compatible := maps.Clone(object(value))
					delete(compatible, "nextLink")
					value = compatible
				}
				if err := schema.Validate(value); err != nil {
					t.Error("native Fleet response discrepancy", err)
				}
				bodies++
			}
		})
	}
	for _, op := range metadata.catalog.Operations {
		if roots[op.SourceURI] && !operations[op.ID] {
			t.Error("native Fleet operation lacks retained evidence", op.ID)
		}
	}
	if len(operations) != 22 || responses != 33 || bodies != 16 {
		t.Fatal("incomplete Fleet schema validation", len(operations), responses, bodies)
	}
}
