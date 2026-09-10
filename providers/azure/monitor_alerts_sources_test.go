package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"golang.org/x/text/language"
	"golang.org/x/text/message"
)

func TestMonitorAlertNativeSources(t *testing.T) {
	const directory = "fixtures/monitoralerts/"
	const pin = "/e45039baa985c442877529906e705982a6e0099d/"
	read := func(name, digest string, target any) {
		t.Helper()
		payload, err := os.ReadFile(directory + name)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != digest || json.Unmarshal(payload, target) != nil {
			t.Fatal("Monitor alert native evidence changed", name, err)
		}
	}
	var examples []map[string]string
	read("sources.json", "3eef438a305f9167200baed51c49eb1193c7e5b0a324724c2fb61d80118f43d4", &examples)
	var expected []catalog.RESTSourceDocument
	read("documents.json", "ef11a69f5d7f2c63ab45b8d0450b3b7dd10056126de77267288cc80348e34c5e", &expected)
	// Published list examples use null although the Swagger requires a string.
	// Preserve and enumerate the mismatch instead of repairing native evidence.
	discrepancies := map[string][]string{
		"smart-2021-04-01/SmartDetectorAlertRule_List.json HTTP 200":                {"/nextLink: got null, want string"},
		"smart-2021-04-01/SmartDetectorAlertRule_ListByResourceGroup.json HTTP 200": {"/nextLink: got null, want string"},
	}
	if len(examples) != 37 || len(expected) != 11 {
		t.Fatal("incomplete native alert evidence")
	}
	payload, err := os.ReadFile("catalog/source/swagger.json")
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
	roots := map[string]bool{}
	for _, doc := range expected {
		actual, ok := sources[doc.SourceURI]
		if !ok || actual.SourceSHA256 != doc.SourceSHA256 || actual.Dependency != doc.Dependency || !strings.Contains(doc.SourceURI, pin) {
			t.Fatal("native source differs from retained evidence", doc.SourceURI)
		}
		if !doc.Dependency {
			roots[doc.SourceURI] = true
		}
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	checked, responses, deletes := 0, 0, 0
	seenDiscrepancies, operations := map[string]bool{}, map[string]bool{}
	for _, example := range examples {
		t.Run(example["operation"]+"/"+example["file"], func(t *testing.T) {
			var body map[string]any
			read(example["file"], example["source_sha256"], &body)
			namespace := strings.Split(strings.Split(example["path"], "/providers/")[1], "/")[0]
			operation, ok := metadata.catalog.Operation("Azure." + namespace + "." + example["operation"])
			if !ok || !roots[example["source_document"]] || operation.SourceURI != example["source_document"] || operation.Call.Method != example["method"] || operation.Call.Path != example["path"] || !strings.Contains(example["source_uri"], pin) {
				t.Fatal("native example has no matching catalog operation")
			}
			operations[operation.ID] = true
			parameters := object(body["parameters"])
			if parameters["subscriptionId"] == "subid" {
				parameters["subscriptionId"] = testSubscription
			}
			// These subscription examples include an undeclared group selector.
			// Adapt only the request; keep the native response bytes untouched.
			switch example["operation"] {
			case "ActionGroups_ListBySubscriptionId", "MetricAlerts_ListBySubscription", "PrometheusRuleGroups_ListBySubscription", "SmartDetectorAlertRules_List":
				if _, declared := object(operation.InputSchema["properties"])["resourceGroupName"]; declared {
					t.Fatal("native request discrepancy changed")
				}
				delete(parameters, "resourceGroupName")
			}
			request, err := bindAzureREST(operation, parameters)
			if err != nil {
				t.Fatal("native request does not bind", err)
			}
			var document map[string]any
			if json.Unmarshal(sources[example["source_document"]].Document, &document) != nil {
				t.Fatal("invalid native document")
			}
			op := object(object(object(document["paths"])[example["path"]])[strings.ToLower(example["method"])])
			for status, raw := range object(body["responses"]) {
				native := object(raw)
				value, exists := native["body"]
				code, err := strconv.Atoi(status)
				if err != nil || object(op["responses"])[status] == nil {
					t.Fatal("undeclared native response", status)
				}
				headers := http.Header{}
				for key, value := range object(native["headers"]) {
					headers.Set(key, text(value))
				}
				c := directClient(func(r *http.Request) (*http.Response, error) {
					if r.Method != request.Method || r.URL.String() != request.URL {
						t.Fatal("native wire request changed")
					}
					if !exists {
						return &http.Response{StatusCode: code, Header: headers, Body: http.NoBody}, nil
					}
					return jsonResponse(code, value, headers), nil
				})
				c.subscription = strings.ToLower(text(parameters["subscriptionId"]))
				result, err := c.requestBody(context.Background(), request.Method, request.URL, request.Body, request.Headers)
				if err != nil || result.status != code {
					t.Fatalf("HTTP %d: %v", code, err)
				}
				if exists {
					original, _ := json.Marshal(value)
					actual, _ := json.Marshal(result.data)
					if !bytes.Equal(original, actual) {
						t.Fatal("native body changed during private transport")
					}
				}
				responses++
				if request.Method == http.MethodDelete {
					if exists || code != 200 && code != 204 || result.header.Get("Azure-AsyncOperation") != "" || result.header.Get("Location") != "" {
						t.Fatal("native synchronous DELETE contract changed")
					}
					deletes++
				}
				if !exists || object(object(op["responses"])[status])["schema"] == nil {
					continue
				}
				pointer := "#/paths/" + strings.ReplaceAll(example["path"], "/", "~1") + "/" + strings.ToLower(example["method"]) + "/responses/" + status + "/schema"
				schema, err := compiler.Compile(example["source_document"] + pointer)
				if err != nil {
					t.Fatal("invalid native schema", err)
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
				key := example["file"] + " HTTP " + status
				if !slices.Equal(failures, discrepancies[key]) {
					t.Errorf("native response discrepancies changed: %s: %q", key, failures)
				}
				if len(failures) != 0 {
					seenDiscrepancies[key] = true
				}
				checked++
			}
		})
	}
	for _, operation := range metadata.catalog.Operations {
		if roots[operation.SourceURI] && !operations[operation.ID] {
			t.Error("native operation lacks retained transport example", operation.ID)
		}
	}
	if checked != 30 || responses != 44 || deletes != 14 || len(operations) != 30 || len(seenDiscrepancies) != len(discrepancies) {
		t.Fatal("incomplete native alert validation", checked, responses, deletes, len(operations), len(seenDiscrepancies))
	}
}

func TestMonitorAlertPrivateContentAndInvoke(t *testing.T) {
	private := map[string]any{
		"criteria":  map[string]any{"allOf": []any{map[string]any{"query": "private-query", "ordinary": "private-condition"}}},
		"condition": map[string]any{"allOf": []any{"private-activity"}}, "conditions": []any{map[string]any{"values": []any{"private-processing"}}},
		"customProperties": map[string]any{"ordinary": "private-custom"}, "actionProperties": map[string]any{"ordinary": "private-action"},
		"webHookProperties": map[string]any{"ordinary": "private-webhook"}, "dimensions": map[string]any{"ordinary": "private-status"},
		"customEmailSubject": "private-email-subject", "customWebhookPayload": "private-webhook-payload",
		"detector": map[string]any{"id": "detector-id", "parameters": map[string]any{"ordinary": "private-parameter"}, "parameterDefinitions": []any{"private-definition"}},
		"rules":    []any{map[string]any{"alert": "alert-name", "expression": "private-promql", "labels": map[string]any{"ordinary": "private-label"}, "annotations": map[string]any{"ordinary": "private-annotation"}, "actions": []any{map[string]any{"actionGroupId": resourceID("Microsoft.Insights/actionGroups", "shared"), "actionProperties": map[string]any{"ordinary": "private-action"}}}}},
		"enabled":  true,
	}
	for _, receiver := range []string{"email", "sms", "webhook", "itsm", "azureAppPush", "automationRunbook", "voice", "logicApp", "azureFunction", "armRole", "eventHub"} {
		private[receiver+"Receivers"] = []any{map[string]any{"name": "private-receiver", "ordinary": "private-destination"}}
	}
	before, _ := json.Marshal(private)
	for _, test := range []struct{ kind, operation string }{
		{"Microsoft.Insights/metricAlerts", "MetricAlerts_ListBySubscription"},
		{"Microsoft.Insights/actionGroups", "ActionGroups_ListBySubscriptionId"},
		{"Microsoft.Insights/activityLogAlerts", "ActivityLogAlerts_ListBySubscriptionId"},
		{"Microsoft.Insights/scheduledQueryRules", "ScheduledQueryRules_ListBySubscription"},
		{"microsoft.alertsManagement/smartDetectorAlertRules", "SmartDetectorAlertRules_List"},
		{"Microsoft.AlertsManagement/prometheusRuleGroups", "PrometheusRuleGroups_ListBySubscription"},
		{"Microsoft.AlertsManagement/actionRules", "AlertProcessingRules_ListBySubscription"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			entries := []execution.JobLogEntry{}
			ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { entries = append(entries, entry) }))
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || !strings.EqualFold(req.URL.Path, "/subscriptions/"+testSubscription+"/providers/"+test.kind) {
					t.Fatal("incorrect native invocation", req.URL)
				}
				// Endpoint context must redact list rows with no type or id.
				return jsonResponse(200, map[string]any{"value": []any{map[string]any{"name": "selected", "properties": private}}}, nil), nil
			})
			namespace := strings.Split(test.kind, "/")[0]
			result, err := r.Invoke(ctx, contracts.Invocation{ConnectionID: "connection", Operation: "Azure." + namespace + "." + test.operation})
			if err != nil || len(array(result.Data["value"])) != 1 || object(array(result.Data["value"])[0])["name"] != "selected" {
				t.Fatal("native alert invocation failed", err, result)
			}
			raw := nativeResource(test.kind, "selected", "global", private)
			for _, value := range []any{entries, result, safePayload(raw), safeAPIPayload(map[string]any{"body": private}, apiURL(text(raw["id"]), "2026-01-01")), safePayload(map[string]any{"type": strings.ToUpper(test.kind), "properties": private}), safePayload(map[string]any{"id": strings.ToUpper(text(raw["id"])), "properties": private})} {
				encoded, _ := json.Marshal(value)
				if strings.Contains(string(encoded), "private-") {
					t.Fatal("private alert content escaped", string(encoded))
				}
			}
			props := object(object(array(result.Data["value"])[0])["properties"])
			if props["enabled"] != true || object(props["detector"])["id"] != "detector-id" || object(array(props["rules"])[0])["alert"] != "alert-name" {
				t.Fatal("redaction removed public alert metadata", props)
			}
		})
	}
	after, _ := json.Marshal(private)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("redaction mutated live alert configuration")
	}
	for _, kind := range []string{vmType, "Microsoft.Custom/actionGroups", "Microsoft.Insights/metricAlertsUnrelated"} {
		raw := nativeResource(kind, "unrelated", "eastus", private)
		if monitorAlertPath(text(raw["id"])) || !reflect.DeepEqual(safeAPIPayload(private, apiURL(text(raw["id"]), "2026-01-01")), safePayload(private)) {
			t.Fatal("alert redaction changed another resource family", kind)
		}
	}
}
