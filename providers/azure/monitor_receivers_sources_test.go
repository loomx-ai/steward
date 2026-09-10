package azure

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

func TestMonitorReceiverNativeSources(t *testing.T) {
	const directory = "fixtures/monitorreceivers/"
	payload, err := os.ReadFile(directory + "sources.json")
	var examples []map[string]string
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != "a6c062c105edaf194361fe342ecef34c78cbab99dd15ef8d20df4b1b289cbd3b" || json.Unmarshal(payload, &examples) != nil || len(examples) != 4 {
		t.Fatal("native receiver source manifest changed", err)
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
		if err != nil || compiler.AddResource(doc.SourceURI, applicationInsightsSwaggerNullability(value)) != nil {
			t.Fatal("invalid native schema", doc.SourceURI, err)
		}
		sources[doc.SourceURI] = doc
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	checked, mismatches := 0, 0
	for _, example := range examples {
		t.Run(example["file"], func(t *testing.T) {
			payload, err := os.ReadFile(directory + example["file"])
			var native map[string]any
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != example["source_sha256"] || json.Unmarshal(payload, &native) != nil || !strings.Contains(example["source_uri"], "/e45039baa985c442877529906e705982a6e0099d/") {
				t.Fatal("native receiver example changed", err)
			}
			doc, ok := sources[example["source_document"]]
			if !ok || doc.SourceSHA256 != example["source_document_sha256"] || doc.Dependency {
				t.Fatal("receiver schema provenance changed")
			}
			provider := strings.Split(strings.Split(example["path"], "/providers/")[1], "/")[0]
			opID, method := "Azure."+provider+"."+example["operation"], "get"
			if example["file"] == "actiongroup-create.json" {
				// Retain the original authoring example because native Get/List
				// omit these receivers. Validate its returned ActionGroupResource
				// against the selected Get contract; do not register or replay PUT.
				opID = "Azure.Microsoft.Insights.ActionGroups_Get"
				if _, exposed := metadata.catalog.Operation("Azure.Microsoft.Insights.ActionGroups_CreateOrUpdate"); exposed {
					t.Fatal("receiver evidence unexpectedly registered a write")
				}
			}
			op, ok := metadata.catalog.Operation(opID)
			if !ok || op.Call == nil || op.SourceURI != example["source_document"] || op.Call.Path != example["path"] || op.Call.Method != "GET" {
				t.Fatal("receiver evidence has no native read schema")
			}
			for status, response := range object(native["responses"]) {
				body := object(response)["body"]
				responseCode := status
				if example["file"] == "actiongroup-create.json" {
					responseCode = "200"
				}
				pointer := "#/paths/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(example["path"]) + "/" + method + "/responses/" + responseCode + "/schema"
				schema, err := compiler.Compile(example["source_document"] + pointer)
				if err != nil {
					t.Fatal("invalid native receiver response schema", err)
				}
				var failures []string
				if err := schema.Validate(body); err != nil {
					var collect func(*jsonschema.ValidationError)
					collect = func(err *jsonschema.ValidationError) {
						if len(err.Causes) == 0 {
							failures = append(failures, "/"+strings.Join(err.InstanceLocation, "/")+": "+err.ErrorKind.LocalizedString(message.NewPrinter(language.English)))
						}
						for _, cause := range err.Causes {
							collect(cause)
						}
					}
					collect(err.(*jsonschema.ValidationError))
				}
				if example["file"] == "workspace-get.json" {
					if !slices.Equal(failures, []string{"/: got array, want object"}) {
						t.Fatal("native Workspace GET discrepancy changed", failures)
					}
					f := newMonitorReceiverFixture(t)
					f.fault = func(req *http.Request) (*http.Response, bool) {
						if strings.EqualFold(req.URL.Path, f.workspaceID) {
							return jsonResponse(200, body, nil), true
						}
						return nil, false
					}
					c, _ := f.runtime.resolve(t.Context(), "connection")
					if _, err := c.monitorReceiverIndex(t.Context(), insightsWorkspaceType); err == nil || isNotFound(err) {
						t.Fatal("native malformed GET authorized an empty receiver index", err)
					}
					mismatches++
				} else if len(failures) != 0 {
					t.Fatal("native receiver response differs from schema", failures)
				}
				checked++
			}
		})
	}
	if checked != 5 || mismatches != 1 {
		t.Fatal("incomplete native receiver source validation", checked, mismatches)
	}
}
