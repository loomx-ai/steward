package azure

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func dataMigrationExample(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/datamigration/" + name + ".json")
	var result map[string]any
	if err != nil || json.Unmarshal(payload, &result) != nil {
		t.Fatal("invalid native Data Migration example", name, err)
	}
	return result
}

func dataMigrationBody(t *testing.T, name string) map[string]any {
	t.Helper()
	return object(object(object(dataMigrationExample(t, name)["responses"])["200"])["body"])
}

func TestDataMigrationOfficialContracts(t *testing.T) {
	payload, err := os.ReadFile("fixtures/datamigration/sources.json")
	var sources []map[string]string
	if err != nil || json.Unmarshal(payload, &sources) != nil {
		t.Fatal("invalid Data Migration provenance", err)
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
		t.Run(source["file"], func(t *testing.T) {
			payload, err := os.ReadFile("fixtures/datamigration/" + source["file"])
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != source["source_sha256"] || !strings.Contains(source["source_uri"], "/c20bf553ad64f20c6d5e3f56080380c086cb1fde/") {
				t.Fatal("native source changed", err)
			}
			prefix := dataMigrationPrefix
			if strings.Contains(source["source_document"], "/Microsoft.Sql/") {
				prefix = "Azure.Microsoft.Sql."
			}
			if strings.Contains(source["source_document"], "/Microsoft.SqlVirtualMachine/") {
				prefix = "Azure.Microsoft.SqlVirtualMachine."
			}
			op, ok := metadata.catalog.Operation(prefix + source["operation"])
			if !ok || op.Call == nil || op.Call.Method != source["method"] || op.Call.Path != source["path"] || op.SourceURI != source["source_document"] {
				t.Fatal("lost native operation", source["operation"])
			}
			example := dataMigrationExample(t, strings.TrimSuffix(source["file"], ".json"))
			parameters := object(example["parameters"])
			switch source["file"] {
			case "DatabaseMigrationsMongoToCosmosDbRUMongo_Get-1.json", "DatabaseMigrationsMongoToCosmosDbvCoreMongo_Get-1.json":
				// These unchanged examples specify an unsupported SQL-only query.
				if parameters["$expand"] != "MigrationStatusDetails" || object(op.InputSchema["properties"])["$expand"] != nil {
					t.Fatal("native Mongo query discrepancy changed")
				}
				delete(parameters, "$expand")
			case "DatabaseMigrationsSqlMi_Delete.json", "DatabaseMigrationsSqlVm_Delete.json":
				if object(parameters["parameters"])["migrationOperationId"] != "4124fe90-d1b6-4b50-b4d9-46d02381f59a" || object(op.InputSchema["properties"])["parameters"] != nil {
					t.Fatal("native DELETE body discrepancy changed")
				}
				delete(parameters, "parameters")
			case "SqlVirtualMachines_Get.json":
				if value, present := parameters["parameters"]; !present || object(value) == nil || len(object(value)) != 0 {
					t.Fatal("native SQL VM extra body changed")
				}
				delete(parameters, "parameters")
			case "MigrationServices_ListBySubscription.json", "SqlMigrationServices_ListBySubscription.json":
				if parameters["subscriptionId"] != "subscriptions/00000000-1111-2222-3333-444444444444/resourceGroups/testrg/providers/Microsoft.Sql/managedInstances/managedInstance1" {
					t.Fatal("native invalid subscription example changed")
				}
				if _, err := catalog.BindREST(op, parameters); err == nil {
					t.Fatal("resource path accepted as subscription ID")
				}
				parameters["subscriptionId"] = "00000000-1111-2222-3333-444444444444"
			}
			if _, err := catalog.BindREST(op, parameters); err != nil {
				t.Error("native request binding", err)
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
					t.Error("native response schema", status, err)
					continue
				}
				var props map[string]any
				expectedState := "Creating"
				switch source["file"] {
				case "DatabaseMigrationsMongoToCosmosDbRUMongo_Get.json", "DatabaseMigrationsMongoToCosmosDbvCoreMongo_Get.json":
					props = object(object(body)["properties"])
				case "MigrationServices_listMigrations.json":
					props = object(object(array(object(body)["value"])[0])["properties"])
				case "ManagedInstances_Get-1.json":
					props, expectedState = object(object(body)["properties"]), "Updating"
				}
				if props != nil {
					// The original examples contain a state outside the schema's
					// enum. Assert this discrepancy and validate every other field.
					if props["provisioningState"] != expectedState || schema.Validate(body) == nil {
						t.Fatal("native provisioning-state discrepancy changed")
					}
					delete(props, "provisioningState")
				}
				if err := schema.Validate(body); err != nil {
					t.Error("native response", status, err)
				}
				bodies++
			}
		})
	}
	if len(sources) != 56 || responses != 76 || bodies != 43 {
		t.Fatal("native response coverage changed", len(sources), responses, bodies)
	}
}

func TestDataMigrationOriginalExampleIdentityDiscrepancies(t *testing.T) {
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"DatabaseMigrationsMongoToCosmosDbRUMongo_Get", "DatabaseMigrationsMongoToCosmosDbRUMongo_Get-1", "DatabaseMigrationsSqlMi_Get", "DatabaseMigrationsSqlMi_Get-1", "DatabaseMigrationsSqlMi_Delete"} {
		t.Run(name, func(t *testing.T) {
			example := dataMigrationExample(t, name)
			parameters := object(example["parameters"])
			delete(parameters, "$expand")
			delete(parameters, "parameters")
			op, _ := metadata.catalog.Operation(dataMigrationPrefix + strings.TrimSuffix(name, "-1"))
			request, err := catalog.BindREST(op, parameters)
			if err != nil {
				t.Fatal(err)
			}
			endpoint, err := url.Parse(request.URL)
			if err != nil {
				t.Fatal(err)
			}
			id := strings.ToLower(endpoint.Path)
			raw := object(object(object(example["responses"])["200"])["body"])
			if strings.Contains(name, "RUMongo") {
				if !strings.Contains(id, "/databaseaccounts/") || !strings.Contains(strings.ToLower(text(raw["id"])), "/mongoclusters/") {
					t.Fatal("native RU example no longer copies a vCore identity")
				}
			} else if scope := text(object(raw["properties"])["scope"]); !strings.HasPrefix(scope, "subscriptions/") || !strings.Contains(scope, "/managedInstance1") {
				t.Fatal("native SQL MI scope discrepancy changed")
			}
			if err := dataMigrationMetadata(id, dataMigrationType, raw); err == nil {
				t.Fatal("inconsistent original example authorized native metadata")
			}
		})
	}
}
