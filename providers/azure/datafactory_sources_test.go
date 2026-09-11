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

func dataFactoryExample(t *testing.T, operation string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/datafactory/" + operation + ".json")
	var result map[string]any
	if err != nil || json.Unmarshal(payload, &result) != nil {
		t.Fatal("invalid native Data Factory example", operation, err)
	}
	return result
}

func dataFactoryBody(t *testing.T, operation string) map[string]any {
	t.Helper()
	return object(object(object(dataFactoryExample(t, operation)["responses"])["200"])["body"])
}

func TestDataFactoryOfficialContracts(t *testing.T) {
	payload, err := os.ReadFile("fixtures/datafactory/sources.json")
	var sources []map[string]string
	if err != nil || json.Unmarshal(payload, &sources) != nil || len(sources) != 54 {
		t.Fatal("invalid Data Factory provenance", err)
	}
	payload, err = os.ReadFile("catalog/source/swagger.json")
	var documents catalog.RESTDocumentSet
	if err != nil || json.Unmarshal(payload, &documents) != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	for _, document := range documents.Documents {
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(document.Document))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource(document.SourceURI, value); err != nil {
			t.Fatal(err)
		}
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	responses, bodies := 0, 0
	for _, source := range sources {
		t.Run(source["operation"], func(t *testing.T) {
			payload, err := os.ReadFile("fixtures/datafactory/" + source["file"])
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != source["source_sha256"] || !strings.Contains(source["source_uri"], "/c20bf553ad64f20c6d5e3f56080380c086cb1fde/") {
				t.Fatal("native Data Factory source changed", err)
			}
			op, ok := metadata.catalog.Operation(dataFactoryPrefix + source["operation"])
			if !ok || op.Call == nil || op.Call.Method != source["method"] || op.Call.Path != source["path"] || op.SourceURI != source["source_document"] || op.Call.Version != dataFactoryVersion {
				t.Fatal("lost native Data Factory operation", source["operation"])
			}
			example := dataFactoryExample(t, source["operation"])
			parameters := object(example["parameters"])
			if value, present := parameters["ifNoneMatch"]; present {
				// These native lists contain an extra null SDK parameter.
				// GET examples use the SDK alias for the real wire header.
				switch source["operation"] {
				case "IntegrationRuntimes_ListByFactory", "ManagedPrivateEndpoints_ListByFactory", "ManagedVirtualNetworks_ListByFactory":
					if value != nil || object(op.InputSchema["properties"])["if-none-match"] != nil {
						t.Fatal("native extra null list parameter changed")
					}
				default:
					if text(value) == "" || object(op.InputSchema["properties"])["if-none-match"] == nil {
						t.Fatal("invalid native header alias")
					}
					parameters["if-none-match"] = value
				}
				delete(parameters, "ifNoneMatch")
			}
			if _, err := catalog.BindREST(op, parameters); err != nil {
				t.Fatal("native Data Factory request binding", err)
			}
			for status, response := range object(example["responses"]) {
				responses++
				body := object(response)["body"]
				if body == nil {
					continue
				}
				pointer := "#/paths/" + strings.ReplaceAll(source["path"], "/", "~1") + "/" + strings.ToLower(source["method"]) + "/responses/" + status + "/schema"
				schema, err := compiler.Compile(source["source_document"] + pointer)
				if err != nil {
					t.Fatal("native Data Factory response schema", status, err)
				}
				if source["operation"] == "PipelineRuns_QueryByFactory" && status == "200" {
					// The unchanged recording has null completion fields,
					// despite non-nullable schema properties. Assert exactly
					// these native discrepancies before validating the rest.
					rows := array(object(body)["value"])
					if len(rows) != 2 || schema.Validate(body) == nil {
						t.Fatal("native run null discrepancy changed")
					}
					for _, key := range []string{"runEnd", "durationInMs"} {
						value, present := object(rows[1])[key]
						if !present || value != nil {
							t.Fatal("native unfinished run changed")
						}
						delete(object(rows[1]), key)
					}
				}
				if err := schema.Validate(body); err != nil {
					t.Fatal("native Data Factory response", status, err)
				}
				bodies++
			}
		})
	}
	if responses != 76 || bodies != 35 {
		t.Fatal("native response coverage changed", responses, bodies)
	}
	kinds, cleanup := 0, 0
	for _, kind := range metadata.kinds {
		if dataFactoryKind(kind.NativeType) == "" {
			continue
		}
		kinds++
		if !kind.ReadOnly {
			cleanup++
		}
		if kind.Version != dataFactoryVersion || len(kind.ReadOperations) != 1 || len(kind.ListOperations) == 0 {
			t.Fatal("incomplete Data Factory kind")
		}
		if kind.ReadOnly != (kind.NativeType == dataFactoryNetworkType) {
			t.Fatal("invented or lost native DELETE", kind.NativeType)
		}
		if kind.NativeType == dataFactoryNodeType {
			list, _ := metadata.catalog.Operation(kind.ListOperations[0])
			if list.Call.Method != "POST" || list.Name != "IntegrationRuntimes_GetStatus" || list.Pagination != nil {
				t.Fatal("node discovery invented a native GET list")
			}
		}
	}
	if kinds != 14 || cleanup != 13 {
		t.Fatal("Data Factory coverage changed", kinds, cleanup)
	}
}
