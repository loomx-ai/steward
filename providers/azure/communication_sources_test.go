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

func communicationExample(t *testing.T, operation string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/communication/" + operation + ".json")
	var value map[string]any
	if err != nil || json.Unmarshal(payload, &value) != nil {
		t.Fatal("invalid Communication example", err)
	}
	return value
}

func TestCommunicationOfficialContracts(t *testing.T) {
	payload, err := os.ReadFile("fixtures/communication/sources.json")
	var sources []map[string]string
	if err != nil || json.Unmarshal(payload, &sources) != nil || len(sources) != 34 {
		t.Fatal("invalid Communication source manifest", err)
	}
	payload, _ = os.ReadFile("catalog/source/swagger.json")
	var documents catalog.RESTDocumentSet
	if err := json.Unmarshal(payload, &documents); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	for _, document := range documents.Documents {
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(document.Document))
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
		payload, err := os.ReadFile("fixtures/communication/" + source["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != source["source_sha256"] || !strings.Contains(source["source_uri"], "/c20bf553ad64f20c6d5e3f56080380c086cb1fde/") {
			t.Fatal("Communication example lost native provenance", source["file"], err)
		}
		example := communicationExample(t, source["operation"])
		prefix := "Azure.Microsoft.Communication."
		if strings.Contains(source["source_document"], "/data-plane/") {
			prefix += "DataPlane."
		}
		op, ok := metadata.catalog.Operation(prefix + source["operation"])
		if !ok || op.Call.Method != source["method"] || op.Call.Path != source["path"] || op.SourceURI != source["source_document"] {
			t.Fatal("Communication contract differs from native source", source["operation"])
		}
		parameters := object(example["parameters"])
		// These five upstream inputs contain extra parameters absent from the
		// corresponding operations. Assert them; keep original fixtures intact.
		switch source["operation"] {
		case "PhoneNumbers_ListPhoneNumbers":
			if parameters["phoneNumber"] != "+11234567890" {
				t.Fatal("native extra list parameter changed")
			}
			delete(parameters, "phoneNumber")
		case "SmtpUsernames_Delete", "SmtpUsernames_Get", "SuppressionListAddresses_Delete", "SuppressionLists_Delete":
			if extra, ok := parameters["parameters"].(map[string]any); !ok || len(extra) != 0 {
				t.Fatal("native extra empty parameter changed")
			}
			delete(parameters, "parameters")
		}
		if _, err := catalog.BindREST(op, parameters); err != nil {
			t.Fatal("native Communication request failed to bind", source["operation"], err)
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
				t.Fatal("native Communication schema missing", source["operation"], status, err)
			}
			if source["operation"] == "SuppressionLists_ListByDomain" && status == "200" {
				// This native example uses nextLink:null despite a string
				// schema. Keep the source and assert this exact discrepancy.
				next, present := object(body)["nextLink"]
				if !present || next != nil || schema.Validate(body) == nil {
					t.Fatal("native null nextLink discrepancy changed")
				}
				delete(object(body), "nextLink")
			}
			if err := schema.Validate(body); err != nil {
				t.Fatal("native Communication response differs from schema", source["operation"], status, err)
			}
			bodies++
		}
	}
	if responses != 56 || bodies != 36 {
		t.Fatal("native Communication response coverage changed", responses, bodies)
	}
}
