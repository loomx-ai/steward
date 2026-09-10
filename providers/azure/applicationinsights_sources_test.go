package azure

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

func TestApplicationInsightsNativeOperationCoverage(t *testing.T) {
	payload, err := os.ReadFile("fixtures/applicationinsights/operation-coverage.json")
	var audit struct {
		Commit     string                       `json:"spec_commit"`
		Tree       string                       `json:"application_insights_tree"`
		Documents  []catalog.RESTSourceDocument `json:"documents"`
		Operations []struct {
			Source    string `json:"source_uri"`
			Selected  string `json:"selected_source_uri"`
			Version   string `json:"api_version"`
			Operation string `json:"operation"`
			Method    string `json:"method"`
			Path      string `json:"path"`
			Handling  string `json:"handling"`
		} `json:"operations"`
	}
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != "664b9cb343ac85f626c02efd953250d46490573481164d84ae482c79bbf709f1" || json.Unmarshal(payload, &audit) != nil || len(audit.Documents) != 44 || len(audit.Operations) != 244 || audit.Commit != "e45039baa985c442877529906e705982a6e0099d" || audit.Tree != "e881e65b75862be16c5c59aaa2cb1ecaf8b2cc64" {
		t.Fatal("native operation coverage changed", err)
	}
	documents := map[string]string{}
	for _, doc := range audit.Documents {
		if !strings.Contains(doc.SourceURI, "/"+audit.Commit+"/") || len(doc.SourceSHA256) != 64 || documents[doc.SourceURI] != "" {
			t.Fatal("invalid audit source", doc.SourceURI)
		}
		documents[doc.SourceURI] = doc.SourceSHA256
	}
	payload, err = os.ReadFile("catalog/source/swagger.json")
	var snapshot catalog.RESTDocumentSet
	if err != nil || json.Unmarshal(payload, &snapshot) != nil {
		t.Fatal("invalid native snapshot", err)
	}
	for _, doc := range snapshot.Documents {
		if expected, present := documents[doc.SourceURI]; present && doc.SourceSHA256 != expected {
			t.Fatal("operation audit source disagrees with retained document", doc.SourceURI)
		}
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	seen, selected, deletes, selectedDeletes := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, row := range audit.Operations {
		key := row.Source + " " + row.Method + " " + row.Path
		if seen[key] || documents[row.Source] == "" {
			t.Fatal("duplicate or unbound native operation", key)
		}
		seen[key] = true
		if row.Method == "DELETE" {
			deletes[strings.ToLower(row.Path)] = true
			if row.Handling != "selected-delete" && row.Handling != "alternate-version" {
				t.Fatal("unreviewed native deletion", row.Operation)
			}
		}
		switch row.Handling {
		case "selected-read", "selected-delete":
			namespace := strings.Split(strings.Split(row.Path, "/providers/")[1], "/")[0]
			id := "Azure." + namespace + "." + row.Operation
			op, ok := metadata.catalog.Operation(id)
			if !ok || row.Selected != row.Source || op.SourceURI != row.Source || op.Call.Method != row.Method || op.Call.Path != row.Path || op.Call.Version != row.Version || selected[id] {
				t.Fatal("selected native operation differs from catalog", id)
			}
			selected[id] = true
			if row.Method == "DELETE" {
				selectedDeletes[strings.ToLower(row.Path)] = true
			}
		case "alternate-version":
			if row.Selected == row.Source || documents[row.Selected] == "" {
				t.Fatal("invalid alternate version", row.Operation)
			}
		case "provider-capability", "telemetry-purge", "deleted-resource-view", "subscription-billing-migration", "diagnostic-token", "test-result-artifact", "create-update":
			if row.Selected != "" {
				t.Fatal("unselected operation has a selected binding", row.Operation)
			}
		default:
			t.Fatal("unreviewed native operation", row.Operation)
		}
	}
	if len(selected) != 63 || len(deletes) != 15 || len(selectedDeletes) != len(deletes) {
		t.Fatal("incomplete native operation audit", len(selected), len(deletes), len(selectedDeletes))
	}
	for path := range deletes {
		if !selectedDeletes[path] {
			t.Fatal("native DELETE route is missing from catalog", path)
		}
	}
	for _, operation := range metadata.catalog.Operations {
		if documents[operation.SourceURI] != "" && !selected[operation.ID] {
			t.Fatal("catalog operation is missing from native audit", operation.ID)
		}
	}
}

func TestApplicationInsightsNativeSources(t *testing.T) {
	const directory = "fixtures/applicationinsights/"
	const pin = "/e45039baa985c442877529906e705982a6e0099d/"
	read := func(name, digest string, target any) {
		t.Helper()
		payload, err := os.ReadFile(directory + name)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != digest || json.Unmarshal(payload, target) != nil {
			t.Fatal("Application Insights native evidence changed", name, err)
		}
	}
	var examples []map[string]string
	read("sources.json", "ebb9ec2144a43b62f04b73c0d5082f0fb6879f8843af00159dbe6697d7ad468f", &examples)
	var expected []catalog.RESTSourceDocument
	read("documents.json", "bed867a7de2814c93d276cba6cacef23baa046e0129421f0948c9c9459adaa31", &expected)
	var discrepancies map[string][]string
	read("schema-discrepancies.json", "3aaf0080e94c8b97c3fb793312563cb26a8a267be4c407ab1bb418e5f87497c7", &discrepancies)
	if len(examples) != 69 || len(expected) != 22 {
		t.Fatal("incomplete Application Insights native evidence")
	}
	payload, err := os.ReadFile("catalog/source/swagger.json")
	var set catalog.RESTDocumentSet
	if err != nil || json.Unmarshal(payload, &set) != nil {
		t.Fatal("invalid native catalog", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	sources := map[string]catalog.RESTSourceDocument{}
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
			t.Fatal("native source differs from retained evidence", doc.SourceURI)
		}
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	seenDiscrepancies := map[string]bool{}
	for _, example := range examples {
		var body map[string]any
		read(example["file"], example["source_sha256"], &body)
		namespace := strings.Split(strings.Split(example["path"], "/providers/")[1], "/")[0]
		operation, ok := metadata.catalog.Operation("Azure." + namespace + "." + example["operation"])
		if !ok || operation.SourceURI != example["source_document"] || operation.Call.Method != example["method"] || operation.Call.Path != example["path"] || !strings.Contains(example["source_uri"], pin) {
			t.Fatal("native example has no matching catalog operation", example["file"])
		}
		var document map[string]any
		if json.Unmarshal(sources[example["source_document"]].Document, &document) != nil {
			t.Fatal("invalid native document")
		}
		op := object(object(object(document["paths"])[example["path"]])[strings.ToLower(example["method"])])
		for status, raw := range object(body["responses"]) {
			value, exists := object(raw)["body"]
			if !exists || object(object(op["responses"])[status])["schema"] == nil {
				continue
			}
			pointer := "#/paths/" + strings.ReplaceAll(example["path"], "/", "~1") + "/" + strings.ToLower(example["method"]) + "/responses/" + status + "/schema"
			schema, err := compiler.Compile(example["source_document"] + pointer)
			if err != nil {
				t.Fatal("invalid native schema", example["file"], err)
			}
			var failures []string
			if err := schema.Validate(value); err != nil {
				var collect func(*jsonschema.ValidationError)
				collect = func(err *jsonschema.ValidationError) {
					if len(err.Causes) == 0 {
						failures = append(failures, "/"+strings.Join(err.InstanceLocation, "/")+": "+err.ErrorKind.LocalizedString(message.NewPrinter(language.English)))
					}
					for _, child := range err.Causes {
						collect(child)
					}
				}
				collect(err.(*jsonschema.ValidationError))
				slices.Sort(failures)
			}
			key := example["file"] + " HTTP " + status
			if !slices.Equal(failures, discrepancies[key]) {
				t.Errorf("native response discrepancies changed: %s: %q", key, failures)
			}
			if len(failures) != 0 {
				seenDiscrepancies[key] = true
			}
			checked++
		}
	}
	if checked != 56 || len(discrepancies) != 23 || len(seenDiscrepancies) != len(discrepancies) {
		t.Fatal("incomplete native response validation", checked, len(seenDiscrepancies))
	}
	t.Log("verified native response bodies", checked)
}

// Draft 4 does not understand Azure's explicit Swagger x-nullable extension.
// Interpret it in the in-memory validator without editing the retained source.
func applicationInsightsSwaggerNullability(value any) any {
	switch node := value.(type) {
	case map[string]any:
		for key, child := range node {
			node[key] = applicationInsightsSwaggerNullability(child)
		}
		if node["x-nullable"] == true {
			delete(node, "x-nullable")
			return map[string]any{"anyOf": []any{map[string]any{"type": "null"}, node}}
		}
	case []any:
		for i, child := range node {
			node[i] = applicationInsightsSwaggerNullability(child)
		}
	}
	return value
}
