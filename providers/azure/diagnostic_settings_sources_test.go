package azure

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

func diagnosticExample(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/diagnostic-settings/" + name)
	var result map[string]any
	if err != nil || json.Unmarshal(payload, &result) != nil {
		t.Fatal("invalid native diagnostic example", name, err)
	}
	return result
}

func TestDiagnosticSettingsNativeSources(t *testing.T) {
	const directory = "fixtures/diagnostic-settings/"
	const pin = "/6005e166d7172cb62fd2894971dbfe1910ac5285/"
	read := func(name, digest string, target any) {
		t.Helper()
		payload, err := os.ReadFile(directory + name)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != digest || json.Unmarshal(payload, target) != nil {
			t.Fatal("native diagnostic evidence changed", name, err)
		}
	}
	var examples []map[string]string
	read("sources.json", "6b8df88da766b3ca8b32e33812ca55b21badf7c6e99eaa2bcb6dfc79a19a26a8", &examples)
	var expected []catalog.RESTSourceDocument
	read("documents.json", "47707b749ff9d0a8cbf01a8dccad0b35899c1ad7eb762f551cbff0161e127c8d", &expected)
	if len(examples) != 12 || len(expected) != 4 {
		t.Fatal("incomplete diagnostic evidence")
	}
	payload, err := os.ReadFile("catalog/source/swagger.json")
	var set catalog.RESTDocumentSet
	if err != nil || json.Unmarshal(payload, &set) != nil {
		t.Fatal("invalid native catalog", err)
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
			t.Fatal("diagnostic document provenance changed", doc.SourceURI)
		}
		roots[doc.SourceURI] = !doc.Dependency
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	// The subscription examples explicitly return a null resource type, while
	// their retained schema declares a string. Preserve this exact discrepancy.
	discrepancies := map[string][]string{
		"getSubscriptionDiagnosticSetting.json":           {"/type: got null, want string"},
		"getSubscriptionDiagnosticSettingCategory.json":   {"/type: got null, want string"},
		"listSubscriptionDiagnosticSettings.json":         {"/value/0/type: got null, want string"},
		"listSubscriptionDiagnosticSettingsCategory.json": {"/value/0/type: got null, want string"},
	}
	checked, responses, deletes := 0, 0, 0
	operations := map[string]bool{}
	for _, example := range examples {
		t.Run(example["file"], func(t *testing.T) {
			var body map[string]any
			read(example["file"], example["source_sha256"], &body)
			operation, ok := metadata.catalog.Operation("Azure.Microsoft.Insights." + example["operation"])
			if !ok || !roots[example["source_document"]] || operation.SourceURI != example["source_document"] || operation.Call.Method != example["method"] || operation.Call.Path != example["path"] || !strings.Contains(example["source_uri"], pin) {
				t.Fatal("diagnostic example has no native operation")
			}
			operations[operation.ID] = true
			request, err := bindAzureREST(operation, object(body["parameters"]))
			if err != nil {
				t.Fatal("native request does not bind", err)
			}
			u, _ := url.Parse(request.URL)
			for status, raw := range object(body["responses"]) {
				native := object(raw)
				value, exists := native["body"]
				code, err := strconv.Atoi(status)
				if err != nil {
					t.Fatal("invalid native response status")
				}
				headers := http.Header{"X-Ms-Request-Id": {"native-diagnostic-response"}}
				c := directClient(func(r *http.Request) (*http.Response, error) {
					if r.Method != request.Method || r.URL.String() != request.URL {
						t.Fatal("native request changed on the wire")
					}
					if !exists {
						return &http.Response{StatusCode: code, Header: headers, Body: http.NoBody}, nil
					}
					return jsonResponse(code, value, headers), nil
				})
				c.subscription = strings.ToLower(strings.Split(u.Path, "/")[2])
				result, err := c.requestBody(t.Context(), request.Method, request.URL, request.Body, request.Headers)
				if err != nil || result.status != code || result.requestID != "native-diagnostic-response" {
					t.Fatal("native diagnostic transport failed", result.status, err)
				}
				responses++
				if request.Method == "DELETE" {
					if exists || code != 200 && code != 204 || operationLocation(result.header) != "" {
						t.Fatal("native synchronous deletion changed")
					}
					deletes++
				}
				if !exists {
					continue
				}
				before, _ := json.Marshal(value)
				after, _ := json.Marshal(result.data)
				if !bytes.Equal(before, after) {
					t.Fatal("native private response body changed")
				}
				pointer := "#/paths/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(example["path"]) + "/" + strings.ToLower(example["method"]) + "/responses/" + status + "/schema"
				schema, err := compiler.Compile(example["source_document"] + pointer)
				if err != nil {
					t.Fatal("invalid diagnostic response schema", err)
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
				if !slices.Equal(failures, discrepancies[example["file"]]) {
					t.Errorf("native schema discrepancy: %s: %q", example["file"], failures)
				}
				checked++
			}
		})
	}
	for _, operation := range metadata.catalog.Operations {
		if roots[operation.SourceURI] && !operations[operation.ID] {
			t.Error("diagnostic operation has no retained example", operation.ID)
		}
	}
	if checked != 10 || responses != 14 || deletes != 4 || len(operations) != 8 {
		t.Fatal("incomplete diagnostic validation", checked, responses, deletes, len(operations))
	}
}
