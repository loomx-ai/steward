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

func rbacExample(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("fixtures/rbac/" + name)
	var result map[string]any
	if err != nil || json.Unmarshal(raw, &result) != nil {
		t.Fatal("invalid native RBAC example", name, err)
	}
	return result
}

func TestRBACNativeSources(t *testing.T) {
	const pin = "/5da82d5c3687ac3cc845330aaf0f13d3a40ce47e/"
	read := func(name, digest string, target any) {
		t.Helper()
		raw, err := os.ReadFile("fixtures/rbac/" + name)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(raw)) != digest || json.Unmarshal(raw, target) != nil {
			t.Fatal("native RBAC evidence changed", name, err)
		}
	}
	var examples []map[string]string
	read("sources.json", "9009da3f9bfd43399cf859c3ea24bdb3d66ebd809d844e3c86155c6cd069114d", &examples)
	var expected []catalog.RESTSourceDocument
	read("documents.json", "8a995f90c403e65b3e2277de98c3c91e5b0ff2c9af43a500324731e29ec4076b", &expected)
	if len(examples) != 11 || len(expected) != 6 {
		t.Fatal("incomplete RBAC native evidence")
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
			t.Fatal("RBAC document provenance changed", doc.SourceURI)
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
			op, ok := metadata.catalog.Operation("Azure.Microsoft.Authorization." + example["operation"])
			if !ok || !roots[example["source_document"]] || op.SourceURI != example["source_document"] || op.Call.Method != example["method"] || op.Call.Path != example["path"] || !strings.Contains(example["source_uri"], pin) {
				t.Fatal("RBAC example has no native operation")
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
					t.Fatal("invalid native RBAC response schema", err)
				}
				if err := schema.Validate(value); err != nil {
					t.Error("native RBAC response discrepancy", err)
				}
				bodies++
			}
		})
	}
	for _, op := range metadata.catalog.Operations {
		if roots[op.SourceURI] && !operations[op.ID] {
			t.Error("native RBAC operation lacks retained evidence", op.ID)
		}
	}
	if len(operations) != 11 || responses != 13 || bodies != 11 {
		t.Fatal("incomplete RBAC schema validation", len(operations), responses, bodies)
	}
}
