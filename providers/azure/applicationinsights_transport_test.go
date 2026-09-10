package azure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestApplicationInsightsNativeTransport(t *testing.T) {
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile("fixtures/applicationinsights/sources.json")
	var sources []map[string]string
	if err != nil || json.Unmarshal(payload, &sources) != nil {
		t.Fatal("invalid native source manifest", err)
	}
	arrays, responses := 0, 0
	for _, source := range sources {
		t.Run(source["operation"]+"/"+source["file"], func(t *testing.T) {
			payload, err := os.ReadFile("fixtures/applicationinsights/" + source["file"])
			var example struct {
				Parameters map[string]any `json:"parameters"`
				Responses  map[string]struct {
					Body    any               `json:"body"`
					Headers map[string]string `json:"headers"`
				} `json:"responses"`
			}
			if err != nil || json.Unmarshal(payload, &example) != nil {
				t.Fatal("invalid native example", err)
			}
			namespace := strings.Split(strings.Split(source["path"], "/providers/")[1], "/")[0]
			op, ok := metadata.catalog.Operation("Azure." + namespace + "." + source["operation"])
			if !ok {
				t.Fatal("missing native operation")
			}
			// Some native examples use a literal "subid" placeholder. Adapt only
			// the request credential scope; retained response bodies are unchanged.
			if example.Parameters["subscriptionId"] == "subid" {
				example.Parameters["subscriptionId"] = testSubscription
			}
			// Published examples include these undeclared request parameters.
			// Keep each correction explicit; BindREST still rejects every other
			// undeclared parameter and the original files remain byte-for-byte.
			for _, parameter := range map[string][]string{
				"AnalyticsItems_Get": {"scope"}, "AnalyticsItems_Delete": {"scope"},
				"MyWorkbooks_ListBySubscription": {"resourceGroupName"},
				"WebTests_List":                  {"resourceGroupName"},
				"Workbooks_ListBySubscription":   {"resourceGroupName", "sourceId"},
			}[source["operation"]] {
				if _, declared := object(op.InputSchema["properties"])[parameter]; declared {
					t.Fatal("native request discrepancy changed", parameter)
				}
				delete(example.Parameters, parameter)
			}
			request, err := bindAzureREST(op, example.Parameters)
			if err != nil {
				t.Fatal("native example request does not bind", err)
			}
			for status, native := range example.Responses {
				status, _ := strconv.Atoi(status)
				headers := http.Header{}
				for key, value := range native.Headers {
					// The three Monitor DELETE examples contain a URI template,
					// not a recorded operation. Materialize only its placeholders.
					value = strings.NewReplacer("{subscriptionId}", text(example.Parameters["subscriptionId"]), "{resourceGroupName}", text(example.Parameters["resourceGroupName"]), "{asyncOperationId}", "713192d7-503f-477a-9cfe-4efc3ee2bd11").Replace(value)
					if strings.ContainsAny(value, "{}") {
						t.Fatal("unhandled native response header template")
					}
					headers.Set(key, value)
				}
				c := directClient(func(r *http.Request) (*http.Response, error) {
					if r.Method != request.Method || r.URL.String() != request.URL {
						t.Fatal("native request changed")
					}
					if native.Body == nil {
						return &http.Response{StatusCode: status, Header: headers, Body: http.NoBody}, nil
					}
					return jsonResponse(status, native.Body, headers), nil
				})
				c.subscription = strings.ToLower(text(example.Parameters["subscriptionId"]))
				result, err := c.requestBody(context.Background(), request.Method, request.URL, request.Body, request.Headers)
				if err != nil || result.status != status {
					t.Fatalf("HTTP %d: %v", status, err)
				}
				if rows, ok := native.Body.([]any); ok {
					if len(array(result.data["value"])) != len(rows) {
						t.Fatal("native array changed")
					}
					arrays++
				}
				responses++
			}
		})
	}
	if arrays != 9 {
		t.Fatal("incomplete native array coverage", arrays)
	}
	t.Log("native responses", responses)
}

func TestApplicationInsightsRecordedTransport(t *testing.T) {
	payload, err := os.ReadFile("fixtures/applicationinsights/cli-recordings.json")
	var files []struct {
		File    string `json:"file"`
		Records []struct {
			Method  string            `json:"method"`
			URI     string            `json:"uri"`
			Status  int               `json:"status"`
			Headers map[string]string `json:"headers"`
			Body    any               `json:"body"`
		} `json:"recordings"`
	}
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != "a212ab38fdca5f2612a1c81202b1f2a22767b142374dda822bec99304c4ac6bf" || json.Unmarshal(payload, &files) != nil {
		t.Fatal("native CLI recording changed", err)
	}
	count, missing := 0, 0
	for _, file := range files {
		for i, record := range file.Records {
			t.Run(file.File+"/"+strconv.Itoa(i), func(t *testing.T) {
				c := directClient(func(r *http.Request) (*http.Response, error) {
					if r.Method != record.Method || r.URL.String() != record.URI {
						t.Fatal("recorded wire selector changed")
					}
					headers := http.Header{}
					for key, value := range record.Headers {
						headers.Set(key, value)
					}
					if record.Body == nil {
						return &http.Response{StatusCode: record.Status, Header: headers, Body: http.NoBody}, nil
					}
					return jsonResponse(record.Status, record.Body, headers), nil
				})
				u, _ := url.Parse(record.URI)
				c.subscription = strings.Split(u.Path, "/")[2]
				result, err := c.request(context.Background(), record.Method, record.URI)
				if result.status != record.Status || record.Status == 200 && err != nil || record.Status == 404 && !isNotFound(err) {
					t.Fatalf("recorded HTTP %d: %v", record.Status, err)
				}
				if record.Status == 404 {
					missing++
				}
				count++
			})
		}
	}
	if count != 42 || missing != 2 {
		t.Fatal("incomplete recorded transport coverage", count, missing)
	}
}

func TestApplicationInsightsArrayResponseBoundaries(t *testing.T) {
	parent := resourceID(applicationInsightsType, "App")
	for _, suffix := range []string{"analyticsItems", "myanalyticsItems", "exportconfiguration", "favorites", "ProactiveDetectionConfigs", "Annotations/id"} {
		for _, body := range []string{`[]`, `[{"Id":"A","Count":9007199254740993}]`, `null`, `{}`, `{"value":[]}`, `[null]`, `[1]`, `[[]]`, `[] {}`, ``, `not-json`} {
			t.Run(suffix+"/"+body, func(t *testing.T) {
				c := directClient(func(*http.Request) (*http.Response, error) {
					return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
				})
				result, err := c.request(context.Background(), "GET", apiURL(parent+"/"+suffix, "2015-05-01"))
				valid := body == `[]` || strings.Contains(body, `"Count"`)
				if valid != (err == nil) {
					t.Fatalf("array contract: %v", err)
				}
				if strings.Contains(body, `"Count"`) && object(array(result.data["value"])[0])["Count"] != json.Number("9007199254740993") {
					t.Fatal("native number lost precision")
				}
			})
		}
	}
	for _, endpoint := range []string{apiURL(parent+"/favorites/item", "2015-05-01"), apiURL(parent+"/Annotations", "2015-05-01"), apiURL(parent+"/APIKeys", "2015-05-01"), apiURL(parent+"/favorites", "2020-02-02"), apiURL(parent+"/favorites", "2015-05-01") + "&api-version=2015-05-01", apiURL(resourceID(vmType, "vm")+"/favorites", "2015-05-01")} {
		c := directClient(func(*http.Request) (*http.Response, error) { return jsonResponse(200, []any{}, nil), nil })
		if _, err := c.request(context.Background(), "GET", endpoint); err == nil {
			t.Fatal("array accepted outside native contract", endpoint)
		}
	}
	for _, method := range []string{"DELETE", "POST", "HEAD"} {
		c := directClient(func(*http.Request) (*http.Response, error) { return jsonResponse(200, []any{}, nil), nil })
		if _, err := c.request(context.Background(), method, apiURL(parent+"/favorites", "2015-05-01")); err == nil {
			t.Fatal("array accepted for another method", method)
		}
	}
}

func TestApplicationInsightsPrivateContentAndInvoke(t *testing.T) {
	private := map[string]any{"Id": "selected", "Name": "saved", "Content": "private-content", "ConfigProperties": "private-connector", "Properties": "private-annotation", "apiKey": "private-api-key", "InstrumentationKey": "private-instrumentation", "HockeyAppToken": "private-hockey", "serializedData": "private-workbook", "templateData": map[string]any{"ordinary": "private-template"}, "customEmails": []any{"private-email"}, "Configuration": map[string]any{"WebTest": "private-webtest"}, "Request": map[string]any{"RequestBody": "private-body"}, "ContentMatch": "private-match"}
	private["storageUri"] = "https://reader:private-password@storage.example/workbooks?sig=private-sas#private-fragment"
	private["sourceId"] = "https://reader:private-password@context.example/workbooks?token=private-token#private-fragment"
	before, _ := json.Marshal(private)
	entries := []execution.JobLogEntry{}
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { entries = append(entries, entry) }))
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || !strings.HasSuffix(req.URL.Path, "/analyticsItems") || req.URL.Query().Get("api-version") != "2015-05-01" {
			t.Fatal("incorrect native invocation", req.URL)
		}
		return jsonResponse(200, []any{private}, nil), nil
	})
	result, err := r.Invoke(ctx, contracts.Invocation{ConnectionID: "connection", Operation: "Azure.microsoft.insights.AnalyticsItems_List", Parameters: map[string]any{"resourceGroupName": "test", "resourceName": "App", "scopePath": "analyticsItems"}})
	if err != nil || len(array(result.Data["value"])) != 1 || object(array(result.Data["value"])[0])["Id"] != "selected" {
		t.Fatal("native array invocation failed", err, result)
	}
	raw := nativeResource(applicationInsightsType, "App", "eastus", private)
	for _, value := range []any{entries, result, safePayload(raw), safeAPIPayload(map[string]any{"body": private}, apiURL(text(raw["id"]), "2020-02-02"))} {
		encoded, _ := json.Marshal(value)
		if strings.Contains(string(encoded), "private-") {
			t.Fatal("private Application Insights content escaped", string(encoded))
		}
	}
	after, _ := json.Marshal(private)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("redaction mutated live configuration")
	}
	unrelated := map[string]any{"Content": "public-content", "properties": map[string]any{"ordinary": "retained"}}
	if !reflect.DeepEqual(safeAPIPayload(unrelated, apiURL(resourceID(vmType, "vm"), "2024-07-01")), safePayload(unrelated)) {
		t.Fatal("Application Insights redaction changed another provider")
	}
}
