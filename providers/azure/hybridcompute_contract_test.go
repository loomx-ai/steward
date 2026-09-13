package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/santhosh-tekuri/jsonschema/v6/kind"
)

func TestHybridComputeOfficialContracts(t *testing.T) {
	raw, err := os.ReadFile("fixtures/hybridcompute/sources.json")
	var sources []map[string]string
	if err != nil || json.Unmarshal(raw, &sources) != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("fixtures/hybridcompute/nonnullable-nulls.json")
	var nulls map[string][]string
	if err != nil || json.Unmarshal(raw, &nulls) != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("catalog/source/swagger.json")
	var documents catalog.RESTDocumentSet
	if err != nil || json.Unmarshal(raw, &documents) != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	nativeDocuments := map[string]map[string]any{}
	for _, doc := range documents.Documents {
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc.Document))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource(doc.SourceURI, value); err != nil {
			t.Fatal(err)
		}
		nativeDocuments[doc.SourceURI] = object(value)
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	operations := map[string]bool{}
	responses, bodies := 0, 0
	nullFields := 0
	for _, source := range sources {
		t.Run(source["file"], func(t *testing.T) {
			payload, err := os.ReadFile("fixtures/hybridcompute/" + source["file"])
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != source["source_sha256"] {
				t.Fatal("official example changed", err)
			}
			op, ok := metadata.catalog.Operation("Azure.Microsoft.HybridCompute." + source["operation"])
			if !ok || op.Call == nil || op.Call.Version != "2025-01-13" || !strings.Contains(op.SourceURI, "/c20bf553ad64f20c6d5e3f56080380c086cb1fde/") || op.Call.Method != source["method"] || op.Call.Path != source["path"] {
				t.Fatal("native operation provenance changed")
			}
			operations[op.ID] = true
			var example map[string]any
			if err := json.Unmarshal(payload, &example); err != nil {
				t.Fatal(err)
			}
			parameters := object(example["parameters"])
			// These two native List examples accidentally include a member name.
			// Assert the discrepancy before adapting this in-memory request only.
			unused := map[string]string{"LicenseProfile_List.json": "licenseProfileName", "License_ListBySubscription.json": "licenseName"}[source["file"]]
			if unused != "" {
				if parameters[unused] == nil {
					t.Fatal("native extra parameter changed")
				}
				if _, err := catalog.BindREST(op, parameters); err == nil {
					t.Fatal("undeclared native List parameter accepted")
				}
				delete(parameters, unused)
			}
			request, err := catalog.BindREST(op, parameters)
			if err != nil {
				t.Error("native request", err)
			} else {
				u, err := url.Parse(request.URL)
				if err != nil || u.Scheme != "https" || u.Host != "management.azure.com" || request.Method != source["method"] || u.Query().Get("api-version") != "2025-01-13" || len(request.Body) != 0 {
					t.Error("native request binding changed", request, err)
				}
			}
			parameters["api-version"] = "2023-06-20-preview"
			if _, err := catalog.BindREST(op, parameters); err == nil {
				t.Error("different API version accepted")
			}
			native := object(object(object(nativeDocuments[op.SourceURI]["paths"])[op.Call.Path])[strings.ToLower(op.Call.Method)])
			if source["method"] == "DELETE" {
				if native["x-ms-long-running-operation"] != true || !op.Destructive {
					t.Fatal("native asynchronous delete lost")
				}
				if source["operation"] == "MachineRunCommands_Delete" && object(native["x-ms-long-running-operation-options"])["final-state-via"] != "location" {
					t.Fatal("native Run Command final-state contract lost")
				}
			}
			for status, response := range object(example["responses"]) {
				responses++
				if _, ok := object(native["responses"])[status]; !ok {
					t.Fatal("undeclared native response", status)
				}
				if status == "202" {
					headers := object(object(response)["headers"])
					if headers["Location"] != "{callbackUrl}" || headers["Azure-AsyncOperation"] != "{callbackUri}" || headers["Retry-After"] != float64(200) {
						t.Fatal("native delete header placeholders changed")
					}
				}
				body := object(response)["body"]
				if body == nil {
					continue
				}
				pointer := "#/paths/" + strings.ReplaceAll(op.Call.Path, "/", "~1") + "/" + strings.ToLower(op.Call.Method) + "/responses/" + status + "/schema"
				schema, err := compiler.Compile(op.SourceURI + pointer)
				if err != nil {
					t.Fatal(err)
				}
				if paths := nulls[source["file"]]; len(paths) != 0 {
					// Exact leaf errors are frozen separately from the unchanged wire
					// examples. Never recursively drop unknown nulls to make a test pass.
					err := schema.Validate(body)
					validation, ok := err.(*jsonschema.ValidationError)
					if !ok {
						t.Fatal("native null discrepancy changed", err)
					}
					var actual []string
					var visit func(*jsonschema.ValidationError)
					visit = func(e *jsonschema.ValidationError) {
						if len(e.Causes) == 0 {
							if problem, ok := e.ErrorKind.(*kind.Type); !ok || problem.Got != "null" {
								t.Fatal("unexpected native schema discrepancy", e)
							}
							actual = append(actual, "/"+strings.Join(e.InstanceLocation, "/"))
						}
						for _, cause := range e.Causes {
							visit(cause)
						}
					}
					visit(validation)
					slices.Sort(actual)
					if !slices.Equal(actual, paths) {
						t.Fatal("native null paths changed", actual)
					}
					for _, path := range paths {
						parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
						parent := body
						for _, part := range parts[:len(parts)-1] {
							if entries, ok := parent.([]any); ok {
								index, err := strconv.Atoi(part)
								if err != nil || index < 0 || index >= len(entries) {
									t.Fatal("invalid frozen null path", path)
								}
								parent = entries[index]
							} else {
								parent = object(parent)[part]
							}
						}
						key := parts[len(parts)-1]
						if v, ok := object(parent)[key]; !ok || v != nil {
							t.Fatal("native null changed", path)
						}
						delete(object(parent), key)
						nullFields++
					}
				}
				if source["operation"] == "MachineExtensions_Get" || source["operation"] == "MachineExtensions_List" {
					if schema.Validate(body) == nil {
						t.Fatal("native extension level discrepancy changed")
					}
					entry := body
					if source["operation"] == "MachineExtensions_List" {
						entry = array(object(body)["value"])[0]
					}
					state := object(object(object(object(entry)["properties"])["instanceView"])["status"])
					if state["level"] != "Information" {
						t.Fatal("native extension level changed")
					}
					state["level"] = "Info"
				}
				if err := schema.Validate(body); err != nil {
					t.Error("native response", err)
				}
				bodies++
			}
		})
	}
	if len(sources) != 19 || len(operations) != 18 || responses != 24 || bodies != 15 || nullFields != 138 {
		t.Fatal("incomplete native evidence", len(sources), len(operations), responses, bodies, nullFields)
	}
}

// These newer CLI recordings exercise transport only, retaining 2026-07-15.
// They do not establish 2025-01-13 lifecycle parity or resource-own absence.
func TestHybridComputeRecordedTransport(t *testing.T) {
	payload, err := os.ReadFile("fixtures/hybridcompute/cli-recordings.json")
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != "8661a18c19364d01ff1719ebe280d5caccdd4d9590dd6abd75030982b7bb5eb5" {
		t.Fatal("native recording extract changed", err)
	}
	// Only the public recording's subscription placeholder is replaced in memory.
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	var sources []struct {
		File    string                  `json:"file"`
		URI     string                  `json:"source_uri"`
		SHA     string                  `json:"source_sha256"`
		Records []redisRecordedResponse `json:"recordings"`
	}
	if json.Unmarshal(payload, &sources) != nil || len(sources) != 3 {
		t.Fatal("invalid native recording evidence")
	}
	fingerprints := map[string]string{
		"test_machine_and_extension.yaml": "8e517363532959f318627e393fd3652661f28cea333c97f8a2c28d21e1b4fbd0",
		"test_esu_license.yaml":           "ddecf934e4335742f1354eb65c9be44a8085698af2edfa3487689055525aef11",
		"test_run_command.yaml":           "bcd2c4e04312a9c0a73d6d0cbec8d4dcac43bea61e726b99a8794acfa773e452",
	}
	records, accepted, throttled, emptyResults, rotated := 0, 0, 0, 0, 0
	for _, source := range sources {
		t.Run(source.File, func(t *testing.T) {
			if source.SHA != fingerprints[source.File] || !strings.Contains(source.URI, "/2aa1d8fc6417d0d5055acd88e9491e29734ad62b/") {
				t.Fatal("native recording provenance changed")
			}
			var current redisRecordedResponse
			var logs []execution.JobLogEntry
			ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
			runtime := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != current.Method || req.URL.String() != current.URI {
					return nil, fmt.Errorf("recorded method or signed URL changed")
				}
				return streamAnalyticsRecordedHTTP(current), nil
			})
			client, err := runtime.resolve(ctx, "connection")
			if err != nil {
				t.Fatal(err)
			}
			initialStatus, initialResult, lastState := "", "", ""
			for _, row := range source.Records {
				records++
				current = row
				u, err := url.Parse(row.URI)
				if err != nil || u.Query().Get("api-version") != "2026-07-15" {
					t.Fatal("recorded API version changed")
				}
				res, err := client.request(ctx, row.Method, row.URI)
				switch {
				case row.Status == 429:
					var call *contracts.ProviderCallError
					if !errors.As(err, &call) || call.Provider.Category != execution.ErrorThrottled {
						t.Fatal("recorded throttling lost", err)
					}
					throttled++
				case row.Method == "DELETE":
					if err != nil || res.status != 202 || len(res.data) != 0 {
						t.Fatal("recorded accepted delete changed", err)
					}
					initialStatus, initialResult = res.header.Get("Azure-AsyncOperation"), res.header.Get("Location")
					lastState = ""
					accepted++
				case strings.Contains(u.Path, "/operationstatus/"):
					if err != nil || row.URI != initialStatus || res.data["name"] != last(u.Path) || operationError(res) != nil {
						t.Fatal("recorded status identity changed", row.Index, err)
					}
					lastState = text(res.data["status"])
					if !slices.Contains([]string{"Queued", "InProgress", "Succeeded"}, lastState) {
						t.Fatal("recorded native state changed", lastState)
					}
					if next := res.header.Get("Azure-AsyncOperation"); next != "" && next != initialStatus {
						previous, _ := url.Parse(initialStatus)
						updated, _ := url.Parse(next)
						if previous.Path != updated.Path {
							t.Fatal("recorded rotation changed operation")
						}
						rotated++
					}
				case strings.Contains(u.Path, "/operationresults/"):
					// An ordinary GET must still reject an empty 200. The result
					// transport needs explicit opt-in after a caller validates and
					// authenticates its receipt; this is not resource-404 evidence.
					if err == nil || row.URI != initialResult || lastState != "Succeeded" || row.Body != nil {
						t.Fatal("recorded empty result handling changed", row.Index, err)
					}
					res, err = client.requestUsing(ctx, row.Method, row.URI, nil, nil, func(endpoint string) error {
						if endpoint != initialResult {
							return fmt.Errorf("not the recorded result URL")
						}
						return client.validateURL(endpoint)
					}, client.http, true)
					if err != nil || res.status != 200 || len(res.data) != 0 {
						t.Fatal("explicit native empty result rejected", err)
					}
					emptyResults++
				default:
					t.Fatal("unexpected recorded request", row.Index)
				}
			}
			encoded, _ := json.Marshal(logs)
			if len(logs) == 0 {
				t.Fatal("missing transport logs")
			}
			for _, row := range source.Records {
				u, _ := url.Parse(row.URI)
				for _, key := range []string{"c", "s", "h"} {
					if value := u.Query().Get(key); value != "" && strings.Contains(string(encoded), value) {
						t.Fatal("native polling signature exposed")
					}
				}
			}
		})
	}
	if records != 25 || accepted != 5 || throttled != 1 || emptyResults != 5 || rotated == 0 {
		t.Fatal("incomplete native transport evidence", records, accepted, throttled, emptyResults, rotated)
	}
}
