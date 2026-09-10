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

func TestBatchNativeSchemasAndProvenance(t *testing.T) {
	payload, err := os.ReadFile("fixtures/batch/sources.json")
	var manifest []map[string]string
	if err != nil || json.Unmarshal(payload, &manifest) != nil || len(manifest) != 54 {
		t.Fatal("invalid Batch source manifest", err)
	}
	payload, err = os.ReadFile("catalog/source/swagger.json")
	var set catalog.RESTDocumentSet
	if err != nil || json.Unmarshal(payload, &set) != nil {
		t.Fatal("invalid native catalog")
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	sources := map[string]catalog.RESTSourceDocument{}
	hashes := map[string]string{"openapi.json": "11f295a8e406d24ab90e8b623ed476b8000e7e37ac8e72e0ec8aab7d03c8832a", "BatchService.json": "8427acd8fd774462806e3a48ae1c4e85ff186b62ff02b9e831f698838a852717"}
	for _, doc := range set.Documents {
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(doc.Document))
		if err := compiler.AddResource(doc.SourceURI, value); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(doc.SourceURI, "/specification/batch/") {
			name := last(doc.SourceURI)
			if doc.SourceSHA256 != hashes[name] || !strings.Contains(doc.SourceURI, "/e45039baa985c442877529906e705982a6e0099d/") {
				t.Fatal("Batch native source changed", name)
			}
			sources[name] = doc
		}
	}
	if len(sources) != 2 {
		t.Fatal("missing Batch source documents", len(sources))
	}
	checked := 0
	knownExceptions := 0
	for _, entry := range manifest {
		payload, err := os.ReadFile("fixtures/batch/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] || !strings.Contains(entry["source_uri"], "/e45039baa985c442877529906e705982a6e0099d/") {
			t.Fatal("native Batch example changed", entry["file"], err)
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
				// Preserve and assert native inconsistencies before checking a
				// copy: Azure's fractional TimeSpan duration exceeds the validator's
				// RFC duration format; ARM GET omits a password required by its
				// shared create/read schema. No fixture or source schema is edited.
				known := map[string]string{
					"JobSchedules_GetJobSchedule.json":   "not valid duration",
					"JobSchedules_ListJobSchedules.json": "not valid duration",
					"Jobs_GetJob.json":                   "not valid duration",
					"Jobs_ListJobs.json":                 "not valid duration",
					"Jobs_ListJobsFromSchedule.json":     "not valid duration",
					"Tasks_GetTask.json":                 "not valid duration",
					"Tasks_ListTasks.json":               "not valid duration",
					"PoolGet.json":                       "missing property 'password'",
					"PoolList.json":                      "missing property 'password'",
				}
				if reason := known[entry["file"]]; reason != "" {
					if err := schema.Validate(body); err == nil || !strings.Contains(err.Error(), reason) {
						t.Fatal("known native Batch schema inconsistency changed", entry["file"], err)
					}
					knownExceptions++
					body = batchSchemaExampleCopy(object(body))
				}
				if err := schema.Validate(body); err != nil {
					t.Errorf("%s: %v", entry["file"], err)
				}
				checked++
			}
		}
	}
	if checked != 42 || knownExceptions != 9 {
		t.Fatal("missing Batch example schemas or documented exceptions", checked, knownExceptions)
	}
}

func batchSchemaExampleCopy(raw map[string]any) map[string]any {
	copy := batchClone(raw)
	var walk func(any)
	walk = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for key, entry := range value {
				if (key == "maxWallClockTime" || key == "retentionTime") && entry == "P10675199DT2H48M5.4775807S" {
					value[key] = "P10675199DT2H48M5S"
				} else if key == "userAccounts" {
					for _, user := range array(entry) {
						if object(user)["password"] == nil {
							object(user)["password"] = "SCHEMA_TEST_ONLY"
						}
					}
				} else {
					walk(entry)
				}
			}
		case []any:
			for _, entry := range value {
				walk(entry)
			}
		}
	}
	walk(copy)
	return copy
}
