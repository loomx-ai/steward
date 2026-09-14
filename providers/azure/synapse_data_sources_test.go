package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

const synapseDataSource = synapseSourceRoot + "synapse/data-plane/Microsoft.Synapse/stable/2020-12-01/"
const synapseDataPrefix = "Azure.Microsoft.Synapse.DataPlane."

func TestSynapseDataNativeContracts(t *testing.T) {
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile("catalog/source/swagger.json")
	var set catalog.RESTDocumentSet
	if err != nil || json.Unmarshal(payload, &set) != nil {
		t.Fatal("invalid native catalog")
	}
	fingerprints := map[string]string{
		"artifacts.json":                      "2275c21e0d42c917744079880d69ef85608471ae4b8eda759de2a3fc15f0f5dc",
		"entityTypes/Notebook.json":           "908d3925c1f68874b1792f83b5e361c8bd02e0d3fb2c2f472e08b5856446d006",
		"entityTypes/SparkJobDefinition.json": "c29274688550bb0e5e00919c50473af666bb5c5612e4ec8ab8d416248fca6e96",
		"notebooks.json":                      "bd469f4f1c671bb50299f0a36561b3343b7ca3851ae05193e523b1f1981ffdc9",
		"sparkJob.json":                       "471f584d7699d138fa70fbc9a65482f31be9c44ff105b6b5f9235d6de9abbca4",
		"sparkJobDefinitions.json":            "a79bbafcd2636f584a838ade6069896bdbcb1a69e575a74bbfc454b2b3333d4f",
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	documents := map[string]map[string]any{}
	for _, doc := range set.Documents {
		v, e := jsonschema.UnmarshalJSON(bytes.NewReader(doc.Document))
		if e != nil {
			t.Fatal(e)
		}
		if e = compiler.AddResource(doc.SourceURI, v); e != nil {
			t.Fatal(e)
		}
		if strings.HasPrefix(doc.SourceURI, synapseDataSource) {
			name := strings.TrimPrefix(doc.SourceURI, synapseDataSource)
			if fingerprints[name] != doc.SourceSHA256 {
				t.Fatal("native source changed", name)
			}
			delete(fingerprints, name)
			documents[doc.SourceURI] = object(v)
		}
	}
	if len(fingerprints) != 0 || len(documents) != 6 {
		t.Fatal("missing sources", fingerprints)
	}
	payload, err = os.ReadFile("fixtures/synapse/data-plane/sources.json")
	var manifest []map[string]string
	if err != nil || json.Unmarshal(payload, &manifest) != nil || len(manifest) != 10 {
		t.Fatal("invalid data-plane manifest")
	}
	expectedFailures := map[string][]string{}
	sparkFailures := []string{
		"/appInfo: got null, want object", "/livyInfo: got null, want object", "/pluginInfo: got null, want object", "/schedulerInfo: got null, want object",
		"/state: value must be one of 'not_started', 'starting', 'idle', 'busy', 'shutting_down', 'error', 'dead', 'killed', 'success', 'running', 'recovering'", "/tags: got null, want object",
	}
	expectedFailures["SparkFrontend_SparkBatch_Get.json"] = sparkFailures
	expectedFailures["SparkFrontend_SparkSession_Get.json"] = sparkFailures
	expectedFailures["SparkJobDefinitions_ListByWorkspace.json"] = []string{"/value/0/properties/jobProperties/pyFiles: got array, want object"}
	expectedFailures["SparkJobDefinitions_Get.json"] = []string{"/properties/jobProperties/pyFiles: got array, want object"}
	schemas := 0
	seen := map[string]bool{}
	for _, entry := range manifest {
		t.Run(entry["file"], func(t *testing.T) {
			payload, err := os.ReadFile("fixtures/synapse/data-plane/" + entry["file"])
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] || entry["source_uri"] != synapseDataSource+"examples/"+entry["file"] {
				t.Fatal("native example changed")
			}
			ex, e := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
			if e != nil {
				t.Fatal(e)
			}
			source := synapseDataSource + entry["document"]
			path := entry["path"]
			method := strings.ToLower(entry["method"])
			native := object(object(object(documents[source]["paths"])[path])[method])
			if native["operationId"] != entry["operation"] {
				t.Fatal("native operation mismatch")
			}
			referenced := false
			for _, ref := range object(native["x-ms-examples"]) {
				u, e := url.Parse(source)
				if e != nil {
					t.Fatal(e)
				}
				r, e := url.Parse(text(object(ref)["$ref"]))
				if e == nil && u.ResolveReference(r).String() == entry["source_uri"] {
					referenced = true
				}
			}
			if !referenced {
				t.Fatal("unreferenced fixture")
			}
			op, ok := metadata.catalog.Operation(synapseDataPrefix + entry["operation"])
			if !ok || op.Call == nil || op.SourceURI != source || op.Call.Style != "azure-synapse-rest" || op.Call.Path != path || op.Call.Version != "2020-12-01" || op.Call.Method != entry["method"] {
				t.Fatal("missing native binding")
			}
			if seen[op.ID] {
				t.Fatal("duplicate fixture")
			}
			seen[op.ID] = true
			params := maps.Clone(object(object(ex)["parameters"]))
			// All original examples omit the HTTPS scheme. Keep them unchanged and
			// reject their raw URLs; separately test an explicit HTTPS projection.
			if _, err := bindAzureREST(op, params); err == nil || err.Error() != "invalid Azure Synapse endpoint" {
				t.Fatal("unqualified native endpoint accepted", err)
			}
			params["endpoint"] = "https://" + strings.ToLower(text(params["endpoint"]))
			if method == "delete" {
				if params["livyApiVersion"] != "2019-11-01-preview" || params["detailed"] != true {
					t.Fatal("native cancellation discrepancy changed")
				}
				if _, err := bindAzureREST(op, params); err == nil || !strings.Contains(err.Error(), `unknown parameter "detailed"`) {
					t.Fatal("undeclared cancellation parameter accepted", err)
				}
				delete(params, "detailed")
				if _, err := bindAzureREST(op, params); err == nil || err.Error() != "Synapse API version differs from catalog" {
					t.Fatal("preview cancellation version accepted", err)
				}
				params["livyApiVersion"] = "2020-12-01"
			}
			if v, present := params["ifNoneMatch"]; present {
				if _, err := bindAzureREST(op, params); err == nil || !strings.Contains(err.Error(), `unknown parameter "ifNoneMatch"`) {
					t.Fatal("SDK-style header alias accepted", err)
				}
				delete(params, "ifNoneMatch")
				params["If-None-Match"] = v
			}
			bound, err := bindAzureREST(op, params)
			if err != nil {
				t.Fatal(err)
			}
			if header, present := params["If-None-Match"]; present && bound.Headers["If-None-Match"] != header {
				t.Fatal("conditional header changed", bound.Headers)
			}
			for _, invalid := range []map[string]any{{"endpoint": "https://other.invalid"}, {"unexpected": true}} {
				p := maps.Clone(params)
				maps.Copy(p, invalid)
				if _, err := bindAzureREST(op, p); err == nil {
					t.Fatal("invalid data-plane request accepted", invalid)
				}
			}
			versionParameter := "api-version"
			if entry["document"] == "sparkJob.json" {
				versionParameter = "livyApiVersion"
			}
			changedVersion := maps.Clone(params)
			changedVersion[versionParameter] = "2021-06-01"
			if _, err := bindAzureREST(op, changedVersion); err == nil {
				t.Fatal("different API version accepted")
			}
			u, err := url.Parse(bound.URL)
			if err != nil {
				t.Fatal(err)
			}
			if u.Scheme+"://"+u.Host != params["endpoint"] || bound.Method != entry["method"] || len(bound.Body) != 0 {
				t.Fatal("native request changed", bound)
			}
			expectedPath := path
			for name, value := range params {
				expectedPath = strings.ReplaceAll(expectedPath, "{"+name+"}", fmt.Sprint(value))
			}
			if u.Path != expectedPath {
				t.Fatal("native path changed", bound)
			}
			if entry["document"] == "sparkJob.json" {
				if u.Query().Has("api-version") || op.Pagination != nil {
					t.Fatal("invented Livy paging or query version")
				}
			} else if u.Query().Get("api-version") != "2020-12-01" {
				t.Fatal("missing artifact query version")
			}
			if strings.Contains(entry["operation"], "ByWorkspace") && (op.Pagination == nil || op.Pagination.OutputTokenPath != "nextLink" || op.Pagination.ItemsPath != "value") {
				t.Fatal("artifact paging changed")
			}
			for status, response := range object(object(ex)["responses"]) {
				definition, ok := object(native["responses"])[status]
				if !ok {
					t.Fatal("undocumented response", status)
				}
				if object(definition)["schema"] == nil {
					continue
				}
				pointer := strings.NewReplacer("~", "~0", "/", "~1").Replace(path)
				schema, err := compiler.Compile(source + "#/paths/" + pointer + "/" + method + "/responses/" + status + "/schema")
				if err != nil {
					t.Fatal(err)
				}
				if entry["file"] == "SparkFrontend_SparkBatch_Get.json" || entry["file"] == "SparkFrontend_SparkSession_Get.json" {
					if fmt.Sprint(object(object(response)["body"])["id"]) != "1" {
						t.Fatal("native mismatched-ID discrepancy changed")
					}
					idParameter := "batchId"
					if strings.Contains(entry["file"], "SparkSession") {
						idParameter = "sessionId"
					}
					if fmt.Sprint(params[idParameter]) != "123" {
						t.Fatal("native request-ID discrepancy changed")
					}
				}
				var failures []string
				if err = schema.Validate(object(response)["body"]); err != nil {
					var collect func(*jsonschema.ValidationError)
					collect = func(e *jsonschema.ValidationError) {
						if len(e.Causes) == 0 {
							failures = append(failures, "/"+strings.Join(e.InstanceLocation, "/")+": "+e.ErrorKind.LocalizedString(message.NewPrinter(language.English)))
						}
						for _, cause := range e.Causes {
							collect(cause)
						}
					}
					collect(err.(*jsonschema.ValidationError))
					slices.Sort(failures)
				}
				if !slices.Equal(failures, expectedFailures[entry["file"]]) {
					t.Fatal("native response discrepancies changed", failures)
				}
				if entry["file"] == "SparkFrontend_SparkBatch_List.json" || entry["file"] == "SparkFrontend_SparkSession_List.json" {
					body := object(object(response)["body"])
					if fmt.Sprint(body["from"]) != "0" || fmt.Sprint(body["total"]) != "2" || len(body["sessions"].([]any)) != 0 {
						t.Fatal("native incomplete-list discrepancy changed")
					}
				}
				schemas++
			}
			// Cancellation still requires reviewed lifecycle support. Its catalog
			// contract must not make the mutation independently executable yet.
			if method == "delete" {
				var runtime Runtime
				if _, err = runtime.Invoke(context.Background(), contracts.Invocation{Operation: op.ID, Parameters: params}); err == nil || !strings.Contains(err.Error(), "synapse_data_mutation_not_implemented") {
					t.Fatal("mutation escaped lifecycle gate", err)
				}
			}

		})
	}
	if len(seen) != 10 || schemas != 8 {
		t.Fatal("incomplete native coverage", len(seen), schemas)
	}
}

func TestSynapseSparkPaginationRequestConstraints(t *testing.T) {
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"SparkBatch_GetSparkBatchJobs", "SparkSession_GetSparkSessions"} {
		op, _ := metadata.catalog.Operation(synapseDataPrefix + name)
		params := map[string]any{"endpoint": "https://workspace.dev.azuresynapse.net", "sparkPoolName": "pool", "from": 0, "size": 20, "detailed": true}
		bound, err := bindAzureREST(op, params)
		if err != nil || !strings.HasSuffix(bound.URL, "?detailed=true&from=0&size=20") {
			t.Fatal(bound, err)
		}
		for key, values := range map[string][]any{"from": {-1, 0.5, 2147483648, "0", true}, "size": {-1, 0, 21, 1.5, "20"}, "detailed": {"true", 1, nil}, "api-version": {"2020-12-01"}} {
			for _, v := range values {
				p := maps.Clone(params)
				p[key] = v
				if _, err := bindAzureREST(op, p); err == nil {
					t.Fatal("invalid pagination request", key, v)
				}
			}
		}
	}
}
