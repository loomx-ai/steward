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

func TestMongoClusterNativeSchemasAndProvenance(t *testing.T) {
	payload, err := os.ReadFile("fixtures/mongocluster/sources.json")
	var manifest []map[string]string
	if err != nil || json.Unmarshal(payload, &manifest) != nil || len(manifest) != 14 {
		t.Fatal("invalid DocumentDB source manifest")
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
		payload, err := os.ReadFile("fixtures/mongocluster/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] || !strings.Contains(entry["source_uri"], "/e45039baa985c442877529906e705982a6e0099d/") {
			t.Fatal("official DocumentDB example changed")
		}
		for _, doc := range set.Documents {
			if !strings.Contains(doc.SourceURI, "/specification/mongocluster/") {
				continue
			}
			if doc.SourceSHA256 != "2d8659f23e60118c9ed2313146d93bfe5b91142c29e38c33c4937bfb5c9b7154" {
				t.Fatal("DocumentDB API source changed")
			}
			var native map[string]any
			json.Unmarshal(doc.Document, &native)
			for path, item := range object(native["paths"]) {
				op := object(object(item)["get"])
				if op["operationId"] != entry["operation"] {
					continue
				}
				schema, err := compiler.Compile(doc.SourceURI + "#/paths/" + strings.ReplaceAll(path, "/", "~1") + "/get/responses/200/schema")
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
	if checked != 10 {
		t.Fatal("missing native DocumentDB schema checks", checked)
	}
	// The service records principalType "User" while this Swagger enum says
	// "user". Preserve both native sources and require that exact discrepancy.
	for _, doc := range set.Documents {
		if !strings.Contains(doc.SourceURI, "/specification/mongocluster/") {
			continue
		}
		schema, err := compiler.Compile(doc.SourceURI + "#/definitions/EntraIdentityProviderProperties")
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, row := range mongoClusterRecordings(t, "test_documentdb_mongocluster_user.yaml") {
			if row.Index != 27 {
				continue
			}
			found = true
			props := object(object(object(row.Body["properties"])["identityProvider"])["properties"])
			if props["principalType"] != "User" || schema.Validate(props) == nil {
				t.Fatal("native DocumentDB principal-type discrepancy changed")
			}
			copy := maps.Clone(props)
			copy["principalType"] = "user"
			if err := schema.Validate(copy); err != nil {
				t.Fatal("unexpected native principal schema discrepancy", err)
			}
		}
		if !found {
			t.Fatal("missing native DocumentDB principal response")
		}
	}
}

func TestMongoClusterCatalogSeparatesCosmosOperations(t *testing.T) {
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range append(mongoClusterOwnedKinds(), mongoClusterType) {
		mapping, known := findType(kind)
		if !known || mapping.ReadOnly || mapping.Version != mongoClusterVersion || isCosmosType(kind) {
			t.Fatal("DocumentDB used the Cosmos RU catalog", kind)
		}
		for _, id := range append(append([]string{}, mapping.ReadOperations...), mapping.DeleteOperations...) {
			op, ok := metadata.catalog.Operation(id)
			if !ok || !strings.Contains(op.SourceURI, "/specification/mongocluster/") || op.Call.Version != mongoClusterVersion {
				t.Fatal("DocumentDB catalog transport mismatch", id)
			}
		}
	}
	for _, tc := range []struct{ title, kind, version string }{
		{"Cosmos_DB", cosmosPECType, "2026-03-15"},
		{"MongoClusterManagementClient", mongoClusterPECType, mongoClusterVersion},
	} {
		mapping, _ := findType(tc.kind)
		for _, method := range []string{"Get", "Delete"} {
			id := "Azure.Microsoft.DocumentDB." + tc.title + ".PrivateEndpointConnections_" + method
			op, ok := metadata.catalog.Operation(id)
			if !ok || op.Name != "PrivateEndpointConnections_"+method || op.Call.Version != tc.version || !strings.Contains(op.Call.Path, strings.Split(tc.kind, "/")[1]) {
				t.Fatal("ambiguous native operation", id)
			}
			if method == "Get" && mapping.ReadOperations[0] != id || method == "Delete" && mapping.DeleteOperations[0] != id {
				t.Fatal("resource type bound to wrong native operation", tc.kind)
			}
		}
	}
}
