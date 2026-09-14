package azure

import (
	"bytes"
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
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

const synapseSourceRoot = "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/"
const synapseARMSource = synapseSourceRoot + "synapse/resource-manager/Microsoft.Synapse/stable/2021-06-01/"

func TestSynapseNativeContracts(t *testing.T) {
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
		"common-types/resource-management/v1/types.json":                                "5727903ec2102686dfbda7228fd22ccad2e1f944d27d5877f6885b83d0d453de",
		"common-types/resource-management/v2/types.json":                                "ccb3b6327aad7d108bdaffca3eefcd15737032ec655ca3f933300e828047d723",
		"synapse/common/v1/privateEndpointConnection.json":                              "a08467b10b1807c2341b80284f8e70b0c8c4813deb3daeda773088ed51fa0eff",
		"synapse/common/v1/types.json":                                                  "492a8ebe396cc1f02ece49dbf55ac27148660abde99dc7a1539e4245bb7925bf",
		"synapse/resource-manager/Microsoft.Synapse/stable/2021-06-01/bigDataPool.json": "2eb41d1fa6529a75ce5eac4ff095c9ad636e49ed9c7cde678ef4b02317ff6c62",
		"synapse/resource-manager/Microsoft.Synapse/stable/2021-06-01/sqlPool.json":     "dec5e9c22e154830020e949e28263675cbae12161fdab9eb4e464646ee6e0dac",
		"synapse/resource-manager/Microsoft.Synapse/stable/2021-06-01/workspace.json":   "ee0a9797f7d91b7f5b71d615459bc8adeeb0ad14fa1e96afbbfd42aed067397d",
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	documents := map[string]map[string]any{}
	for _, doc := range set.Documents {
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc.Document))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource(doc.SourceURI, value); err != nil {
			t.Fatal(err)
		}
		name := strings.TrimPrefix(doc.SourceURI, synapseSourceRoot)
		if expected, ok := fingerprints[name]; ok {
			if doc.SourceSHA256 != expected {
				t.Fatal("native source fingerprint changed", name)
			}
			delete(fingerprints, name)
		}
		if strings.HasPrefix(doc.SourceURI, synapseARMSource) {
			documents[doc.SourceURI] = object(value)
		}
	}
	if len(fingerprints) != 0 || len(documents) != 3 {
		t.Fatal("missing pinned Synapse sources", fingerprints)
	}
	payload, err = os.ReadFile("fixtures/synapse/sources.json")
	var manifest []map[string]string
	if err != nil || json.Unmarshal(payload, &manifest) != nil || len(manifest) != 23 {
		t.Fatal("invalid Synapse manifest")
	}
	// These are upstream inconsistencies, not permission to relax live
	// validation or rewrite the source examples. See fixtures/synapse/README.md.
	requestDiscrepancies := map[string]string{
		"ListSqlPoolRestorePoints.json":          "location",
		"SqlPoolRestorePointsGet.json":           "location",
		"SqlPoolRestorePointsDelete.json":        "location",
		"ListSqlPoolsInWorkspaceWithFilter.json": "$filter",
	}
	responseDiscrepancies := map[string][]string{
		"RestorableDroppedSqlPoolGet.json/200":  {"/properties/elasticPoolName: got null, want string"},
		"RestorableDroppedSqlpoolList.json/200": {"/value/0/properties/elasticPoolName: got null, want string", "/value/1/properties/elasticPoolName: got null, want string"},
	}
	seen, schemas := map[string]bool{}, 0
	requestExceptions, responseExceptions := 0, 0
	for _, entry := range manifest {
		t.Run(entry["file"], func(t *testing.T) {
			payload, err := os.ReadFile("fixtures/synapse/" + entry["file"])
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] || entry["source_uri"] != synapseARMSource+"examples/"+entry["file"] {
				t.Fatal("native example changed")
			}
			example, err := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
			if err != nil {
				t.Fatal(err)
			}
			source := synapseARMSource + entry["document"]
			path := entry["path"]
			method := strings.ToLower(entry["method"])
			native := object(object(object(documents[source]["paths"])[path])[method])
			if native["operationId"] != entry["operation"] {
				t.Fatal("example does not match native operation")
			}
			referenced := false
			for _, reference := range object(native["x-ms-examples"]) {
				referenced = referenced || object(reference)["$ref"] == "./examples/"+entry["file"] || object(reference)["$ref"] == "examples/"+entry["file"]
			}
			if !referenced {
				t.Fatal("unreferenced native example")
			}
			operation, ok := metadata.catalog.Operation("Azure.Microsoft.Synapse." + entry["operation"])
			if !ok || operation.SourceURI != source || operation.Call == nil || operation.Call.Version != "2021-06-01" || operation.Call.Method != entry["method"] || operation.Call.Path != path {
				t.Fatal("missing or altered native binding")
			}
			seen[operation.ID] = true
			parameters := maps.Clone(object(object(example)["parameters"]))
			if extra := requestDiscrepancies[entry["file"]]; extra != "" {
				if _, err := bindAzureREST(operation, parameters); err == nil || err.Error() != fmt.Sprintf("unknown parameter %q for %s", extra, operation.ID) {
					t.Fatal("native example request discrepancy changed", err)
				}
				// Check the actual declared request separately. The fixture and
				// its failed full request remain unchanged and explicitly tested.
				delete(parameters, extra)
				requestExceptions++
			}
			request, err := bindAzureREST(operation, parameters)
			if err != nil {
				t.Fatal(err)
			}
			query := url.Values{}
			for key, value := range parameters {
				if strings.Contains(path, "{"+key+"}") {
					path = strings.ReplaceAll(path, "{"+key+"}", url.PathEscape(text(value)))
				} else {
					query.Set(key, text(value))
				}
			}
			if request.Method != entry["method"] || request.URL != armOrigin+path+"?"+query.Encode() || len(request.Body) != 0 || len(request.Headers) != 0 {
				t.Fatal("native request changed", request)
			}
			// Pinned versions and path boundaries apply to every native call,
			// including pause/resume and operation-status requests.
			for _, invalid := range []map[string]any{{"api-version": "2021-06-01-preview"}, {"workspaceName": "../other"}, {"unexpected": true}} {
				params := maps.Clone(parameters)
				maps.Copy(params, invalid)
				if _, err := bindAzureREST(operation, params); err == nil {
					t.Fatal("invalid native request accepted", invalid)
				}
			}
			for status, response := range object(object(example)["responses"]) {
				definition, exists := object(native["responses"])[status]
				if !exists {
					t.Fatal("undocumented native response", status)
				}
				if object(definition)["schema"] == nil {
					continue
				}
				pointer := strings.NewReplacer("~", "~0", "/", "~1").Replace(entry["path"])
				schema, err := compiler.Compile(source + "#/paths/" + pointer + "/" + method + "/responses/" + status + "/schema")
				if err != nil {
					t.Fatal(err)
				}
				var failures []string
				if err := schema.Validate(object(response)["body"]); err != nil {
					var collect func(*jsonschema.ValidationError)
					collect = func(e *jsonschema.ValidationError) {
						if len(e.Causes) == 0 {
							failures = append(failures, "/"+strings.Join(e.InstanceLocation, "/")+": "+e.ErrorKind.LocalizedString(message.NewPrinter(language.English)))
						}
						for _, child := range e.Causes {
							collect(child)
						}
					}
					collect(err.(*jsonschema.ValidationError))
					slices.Sort(failures)
					responseExceptions++
				}
				if !slices.Equal(failures, responseDiscrepancies[entry["file"]+"/"+status]) {
					t.Errorf("native response discrepancies changed: %s: %q", status, failures)
				}
				schemas++
			}
		})
	}
	if len(seen) != 22 || schemas != 34 || requestExceptions != len(requestDiscrepancies) || responseExceptions != len(responseDiscrepancies) {
		t.Fatal("missing native contract coverage", len(seen), schemas)
	}
	t.Logf("checked %d operations, %d native examples and %d response schemas (4 request and 2 response discrepancies pinned)", len(seen), len(manifest), schemas)
}
