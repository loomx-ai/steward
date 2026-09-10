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
)

func TestAPIMNativeOperationCoverage(t *testing.T) {
	payload, err := os.ReadFile("fixtures/apimanagement/operation-coverage.json")
	var audit struct {
		Version    string                       `json:"api_version"`
		Documents  []catalog.RESTSourceDocument `json:"documents"`
		Operations []struct {
			Document  string `json:"source_document"`
			Method    string `json:"method"`
			Operation string `json:"operation"`
			Path      string `json:"path"`
			Handling  string `json:"handling"`
		} `json:"operations"`
	}
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != "554bea4eeca37f4d900f372816f7926bed4cdebea88db2ae2014448e899d55c1" || json.Unmarshal(payload, &audit) != nil || len(audit.Documents) != 55 || len(audit.Operations) != 413 || audit.Version != apimVersion {
		t.Fatal("APIM native operation audit changed", err)
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	base := "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/e45039baa985c442877529906e705982a6e0099d/specification/apimanagement/resource-manager/Microsoft.ApiManagement/ApiManagement/stable/2024-05-01/"
	documents := map[string]string{}
	for _, doc := range audit.Documents {
		if !strings.HasPrefix(doc.SourceURI, base) || len(doc.SourceSHA256) != 64 {
			t.Fatal("invalid APIM coverage provenance", doc.SourceURI)
		}
		documents[doc.SourceURI] = doc.SourceSHA256
	}
	payload, err = os.ReadFile("catalog/source/swagger.json")
	var snapshot catalog.RESTDocumentSet
	if err != nil || json.Unmarshal(payload, &snapshot) != nil {
		t.Fatal(err)
	}
	for _, doc := range snapshot.Documents {
		if expected, exists := documents[doc.SourceURI]; exists && expected != doc.SourceSHA256 {
			t.Fatal("coverage source differs from native catalog", doc.SourceURI)
		}
	}
	cleanup := map[string]bool{}
	for _, kind := range metadata.kinds {
		if isAPIMType(kind.NativeType) {
			for _, id := range kind.DeleteOperations {
				cleanup[id] = true
			}
		}
	}
	seen := map[string]bool{}
	deletes, selected := 0, 0
	for _, row := range audit.Operations {
		id := "Azure.Microsoft.ApiManagement." + row.Operation
		if seen[id] || documents[base+row.Document] == "" {
			t.Fatal("invalid coverage entry", row)
		}
		seen[id] = true
		op, exists := metadata.catalog.Operation(id)
		if slices.Contains([]string{"cleanup", "catalog-read", "reset-only"}, row.Handling) {
			if !exists || op.SourceURI != base+row.Document || op.Call.Method != row.Method || op.Call.Path != row.Path {
				t.Fatal("native operation coverage differs from actual binding", row.Operation)
			}
			selected++
		} else if exists {
			t.Fatal("native operation audit omitted selected binding", row.Operation)
		}
		if row.Method != "DELETE" {
			continue
		}
		deletes++
		switch row.Handling {
		case "cleanup":
			if !cleanup[id] {
				t.Fatal("native deletion has no executable resource binding", row.Operation)
			}
			delete(cleanup, id)
		case "reset-only":
			kind, _ := findType(apimServiceType + "/templates")
			if row.Operation != "EmailTemplate_Delete" || !kind.ReadOnly || len(kind.DeleteOperations) != 0 {
				t.Fatal("template reset became removal")
			}
		case "soft-delete-retention":
			if row.Operation != "DeletedServices_Purge" || exists {
				t.Fatal("purge became default deletion")
			}
		default:
			t.Fatal("unreviewed native DELETE", row.Operation)
		}
	}
	if len(cleanup) != 0 || deletes != 94 || selected != 309 {
		t.Fatal("incomplete APIM native operation coverage", cleanup, deletes, selected)
	}
}

func TestAPIMNativeSchemasAndProvenance(t *testing.T) {
	payload, err := os.ReadFile("fixtures/apimanagement/sources.json")
	var manifest []map[string]string
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != "06c8e3ed77f9f0638edb2fcbbbd1bce87aed19f6db1a029682941b6bf84eb4a7" || json.Unmarshal(payload, &manifest) != nil || len(manifest) != 324 {
		t.Fatal("invalid APIM example manifest", err)
	}
	payload, err = os.ReadFile("fixtures/apimanagement/documents.json")
	var expected []catalog.RESTSourceDocument
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != "6c0c8453662b54b7199dee81493c33ee3ce1f7968f16ee6d31146ccc450b7115" || json.Unmarshal(payload, &expected) != nil || len(expected) != 45 {
		t.Fatal("invalid APIM source manifest", err)
	}
	payload, err = os.ReadFile("catalog/source/swagger.json")
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
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource(doc.SourceURI, value); err != nil {
			t.Fatal(err)
		}
		sources[doc.SourceURI] = doc
	}
	for _, doc := range expected {
		actual, ok := sources[doc.SourceURI]
		if !ok || actual.SourceSHA256 != doc.SourceSHA256 || actual.Dependency != doc.Dependency || !strings.Contains(doc.SourceURI, "/e45039baa985c442877529906e705982a6e0099d/") {
			t.Fatal("APIM native source changed", doc.SourceURI)
		}
	}
	checked := 0
	for _, entry := range manifest {
		payload, err := os.ReadFile("fixtures/apimanagement/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] || !strings.Contains(entry["source_uri"], "/e45039baa985c442877529906e705982a6e0099d/") {
			t.Fatal("native example changed", entry["file"], err)
		}
		sourceURI := entry["source_uri"][:strings.Index(entry["source_uri"], "/examples/")+1] + entry["source_document"]
		source, ok := sources[sourceURI]
		if !ok {
			t.Fatal("missing native source", sourceURI)
		}
		var doc, example map[string]any
		if json.Unmarshal(source.Document, &doc) != nil || json.Unmarshal(payload, &example) != nil {
			t.Fatal("invalid native example")
		}
		found := false
		for path, methods := range object(doc["paths"]) {
			for method, value := range object(methods) {
				op := object(value)
				if op["operationId"] != entry["operation"] {
					continue
				}
				found = true
				for status, response := range object(example["responses"]) {
					nativeResponse := object(object(op["responses"])[status])
					body, present := object(response)["body"]
					if !present || nativeResponse["schema"] == nil {
						continue
					}
					schema, err := compiler.Compile(sourceURI + "#/paths/" + strings.ReplaceAll(path, "/", "~1") + "/" + method + "/responses/" + status + "/schema")
					if err != nil {
						t.Errorf("%s: %v", entry["file"], err)
						continue
					}
					// Preserve native examples unchanged. Assert their exact
					// published discrepancies before checking every remaining field.
					discrepancies := map[string][][]string{
						"ApiManagementGetApiTagDescription.json":           {{"properties", "description"}},
						"ApiManagementListAuthorizationProviders.json":     {{"value", "3", "properties", "oauth2", "grantTypes", "authorizationCode", "scopes"}},
						"ApiManagementGetPrivateEndpointConnection.json":   {{"properties", "provisioningState"}},
						"ApiManagementListPrivateEndpointConnections.json": {{"value", "0", "properties", "provisioningState"}, {"value", "1", "properties", "provisioningState"}},
						"ApiManagementListPortalConfig.json":               {{"value", "0", "properties", "delegation", "delegationUrl"}, {"value", "0", "properties", "delegation", "validationKey"}},
						"ApiManagementPortalConfig.json":                   {{"properties", "delegation", "delegationUrl"}, {"properties", "delegation", "validationKey"}},
						"ApiManagementListPortalRevisions.json":            {{"value", "0", "properties", "statusDetails"}, {"value", "1", "properties", "statusDetails"}},
						"ApiManagementGetPortalRevision.json":              {{"properties", "statusDetails"}},
						"ApiManagementListTenantSettings.json":             {{"value", "0", "properties", "settings", "CustomPortalSettings.UserRegistrationTerms"}},
						"ApiManagementGetTenantSettings.json":              {{"properties", "settings", "CustomPortalSettings.UserRegistrationTerms"}},
					}
					if paths := discrepancies[entry["file"]]; len(paths) != 0 {
						if schema.Validate(body) == nil {
							t.Fatal("native discrepancy changed", entry["file"])
						}
						for _, path := range paths {
							parent := body
							for _, key := range path[:len(path)-1] {
								if values, ok := parent.([]any); ok {
									parent = values[int(key[0]-'0')]
								} else {
									parent = object(parent)[key]
								}
							}
							field := path[len(path)-1]
							value, exists := object(parent)[field]
							if !exists || field == "provisioningState" && value != "Pending" || field != "provisioningState" && value != nil {
								t.Fatal("native discrepancy changed", entry["file"], path)
							}
							delete(object(parent), field)
						}
					}
					if err := schema.Validate(body); err != nil {
						t.Errorf("%s HTTP %s: %v", entry["file"], status, err)
					}
					checked++
				}
			}
		}
		if !found {
			t.Fatal("manifest references unselected operation", entry["operation"])
		}
	}
	if checked != 223 {
		t.Fatal("incomplete APIM schema verification", checked)
	}
	t.Log("verified native response bodies", checked)
}
