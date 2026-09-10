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

func TestStreamAnalyticsNativeSchemasAndProvenance(t *testing.T) {
	payload, err := os.ReadFile("fixtures/streamanalytics/sources.json")
	var manifest []map[string]string
	if err != nil || json.Unmarshal(payload, &manifest) != nil || len(manifest) != 41 {
		t.Fatal("invalid Stream Analytics manifest", err)
	}
	payload, _ = os.ReadFile("catalog/source/swagger.json")
	var set catalog.RESTDocumentSet
	if json.Unmarshal(payload, &set) != nil {
		t.Fatal("invalid native catalog")
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	sources := map[string]catalog.RESTSourceDocument{}
	hashes := map[string]string{
		"clusters.json":              "852308bfe4e8c8f88eaf7b23fcb2ec0d9ecacbbe8bd851f4fce5d6a7032be48c",
		"common/v1/definitions.json": "45e42539d13ca5eb167613fa3203a5e143554680feae5af8c53b8c7d701ea253",
		"functions.json":             "af51174d4c5d7ba922d6ce45cb4b3bfbce4b6de3f732bd1c6f94814225ebd086",
		"inputs.json":                "b4fb358e5fc51aa5a4d9d0e4bf0b03c03b7c8670a18e77dd8367c2045db2b310",
		"outputs.json":               "5b5119f8d62d3840286eab709beb9987fc047e97baa8edda8851705c8389b2aa",
		"privateEndpoints.json":      "9c6d751553b01e7add13712254575fa6bc22ad4a2a340f563c22eb27be0bc0d7",
		"streamingjobs.json":         "d46874af8d68bbf3ad3aff3e0328ceba491505b892edeba3cfce982606dd0d50",
		"transformations.json":       "971ff6ad9d1b6b1fbbd330b6fd92ec6a2af5d950c6b25cb8ba628026f4c40f3c",
	}
	for _, doc := range set.Documents {
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(doc.Document))
		if err := compiler.AddResource(doc.SourceURI, value); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(doc.SourceURI, "/specification/streamanalytics/") {
			name := last(doc.SourceURI)
			if strings.Contains(doc.SourceURI, "/common/v1/") {
				name = "common/v1/" + name
			}
			if doc.SourceSHA256 != hashes[name] || !strings.Contains(doc.SourceURI, "/e45039baa985c442877529906e705982a6e0099d/") {
				t.Fatal("Stream Analytics native source changed", name)
			}
			sources[name] = doc
		}
	}
	if len(sources) != 8 {
		t.Fatal("missing source documents", len(sources))
	}
	checked := 0
	for _, entry := range manifest {
		payload, err := os.ReadFile("fixtures/streamanalytics/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] || !strings.Contains(entry["source_uri"], "/e45039baa985c442877529906e705982a6e0099d/") {
			t.Fatal("native example changed", entry["file"])
		}
		source := sources[entry["source_document"]]
		var native map[string]any
		json.Unmarshal(source.Document, &native)
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
				// Four original list examples use nextLink:null although their
				// Swagger declares string. Keep the original artifact and assert
				// this exact discrepancy, then validate every remaining field.
				switch entry["file"] {
				case "Cluster_ListByResourceGroup.json", "Cluster_ListBySubscription.json", "Cluster_ListStreamingJobs.json", "PrivateEndpoint_ListByCluster.json":
					if schema.Validate(body) == nil || object(body)["nextLink"] != nil {
						t.Fatal("native null-nextLink discrepancy changed", entry["file"])
					}
					compatible := maps.Clone(object(body))
					delete(compatible, "nextLink")
					body = compatible
				}
				if err := schema.Validate(body); err != nil {
					t.Errorf("%s: %v", entry["file"], err)
				}
				checked++
			}
		}
	}
	if checked != 34 {
		t.Fatal("missing Stream Analytics response schemas", checked)
	}
	// Swagger discriminators are not enforced by Draft 4. Validate concrete
	// authored input, connector and function variants against their native types.
	for _, entry := range []struct{ file, source, field, definition string }{
		{"Input_Get_Stream_Blob_CSV", "inputs.json", "", "StreamInputProperties"},
		{"Input_Get_Reference_Blob_CSV", "inputs.json", "", "ReferenceInputProperties"},
		{"Input_Get_Stream_Blob_CSV", "inputs.json", "datasource", "BlobStreamInputDataSource"},
		{"Input_Get_Reference_Blob_CSV", "inputs.json", "datasource", "BlobReferenceInputDataSource"},
		{"Input_Get_Stream_EventHub_JSON", "inputs.json", "datasource", "EventHubStreamInputDataSource"},
		{"Input_Get_Stream_IoTHub_Avro", "inputs.json", "datasource", "IoTHubStreamInputDataSource"},
		{"Output_Get_Blob_CSV", "outputs.json", "datasource", "BlobOutputDataSource"},
		{"Output_Get_AzureSQL", "outputs.json", "datasource", "AzureSqlDatabaseOutputDataSource"},
		{"Output_Get_DataWarehouse", "outputs.json", "datasource", "AzureSynapseOutputDataSource"},
		{"Output_Get_DocumentDB", "outputs.json", "datasource", "DocumentDbOutputDataSource"},
		{"Output_Get_AzureTable", "outputs.json", "datasource", "AzureTableOutputDataSource"},
		{"Output_Get_AzureFunction", "outputs.json", "datasource", "AzureFunctionOutputDataSource"},
		{"Output_Get_ServiceBusQueue_Avro", "outputs.json", "datasource", "ServiceBusQueueOutputDataSource"},
		{"Output_Get_ServiceBusTopic_CSV", "outputs.json", "datasource", "ServiceBusTopicOutputDataSource"},
		{"Output_Get_EventHub_JSON", "outputs.json", "datasource", "EventHubOutputDataSource"},
		{"Output_Get_PowerBI", "outputs.json", "datasource", "PowerBIOutputDataSource"},
		{"Output_Get_AzureDataLakeStore_JSON", "outputs.json", "datasource", "AzureDataLakeStoreOutputDataSource"},
		{"Function_Get_JavaScript", "functions.json", "", "ScalarFunctionProperties"},
		{"Function_Get_JavaScript", "functions.json", "binding", "JavaScriptFunctionBinding"},
		{"Function_Get_AzureML", "functions.json", "binding", "AzureMachineLearningWebServiceFunctionBinding"},
	} {
		schema, err := compiler.Compile(sources[entry.source].SourceURI + "#/definitions/" + entry.definition)
		if err != nil {
			t.Fatal(err)
		}
		props := object(streamAnalyticsExampleBody(t, entry.file)["properties"])
		var value any = props
		if entry.field != "" {
			value = props[entry.field]
		}
		if entry.field == "binding" {
			value = object(props["properties"])["binding"]
		}
		if entry.file == "Output_Get_AzureFunction" {
			if schema.Validate(value) == nil || object(object(value)["properties"])["apiKey"] != nil {
				t.Fatal("native null API-key discrepancy changed")
			}
			compatible := maps.Clone(object(value))
			settings := maps.Clone(object(compatible["properties"]))
			delete(settings, "apiKey")
			compatible["properties"] = settings
			value = compatible
		}
		if err := schema.Validate(value); err != nil {
			t.Errorf("concrete %s %s: %v", entry.file, entry.definition, err)
		}
	}
}
