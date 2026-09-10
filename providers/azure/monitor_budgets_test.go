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

func TestMonitorBudgetNativeSources(t *testing.T) {
	const directory = "fixtures/monitorbudgets/"
	const pin = "/e45039baa985c442877529906e705982a6e0099d/"
	read := func(name, digest string, target any) {
		t.Helper()
		payload, err := os.ReadFile(directory + name)
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != digest || json.Unmarshal(payload, target) != nil {
			t.Fatal("Monitor budget native evidence changed", name, err)
		}
	}
	var examples []map[string]string
	read("sources.json", "6631819040faaa5eddd715832e603236dcdbad5bd5ceaad6e8f65381a95f4810", &examples)
	var expected []catalog.RESTSourceDocument
	read("documents.json", "4bb432cfb59b58dee6ae588a5ea8556aba0a468314c4c86d0dd4b61d7b7a6920", &expected)
	// Every retained example has extra scope selectors absent from its API.
	requestExtras := map[string][]string{
		"consumption-2024-08-01/Budget.json":                                                                      {"resourceGroupName", "subscriptionId"},
		"consumption-2024-08-01/BudgetsList.json":                                                                 {"resourceGroupName", "subscriptionId"},
		"consumption-2024-08-01/DeleteBudget.json":                                                                {"resourceGroupName", "subscriptionId"},
		"cost-management-2025-03-01/Budgets/Delete/DeleteBudget.json":                                             {"resourceGroupName", "subscriptionId"},
		"cost-management-2025-03-01/Budgets/Get/Cost/Get-Cost-Budget.json":                                        {"resourceGroupName", "subscriptionId"},
		"cost-management-2025-03-01/Budgets/Get/ReservationUtilization/Get-ReservationUtilization-AlertRule.json": {"billingAccountId", "billingProfileId"},
		"cost-management-2025-03-01/Budgets/List/EA/BillingAccountBudgetsList-EA-CategoryTypeFilter.json":         {"billingAccountId"},
		"cost-management-2025-03-01/Budgets/List/EA/BillingAccountBudgetsList-EA.json":                            {"billingAccountId"},
		"cost-management-2025-03-01/Budgets/List/EA/DepartmentBudgetsList.json":                                   {"billingAccountId", "departmentId"},
		"cost-management-2025-03-01/Budgets/List/EA/EnrollmentAccountBudgetsList.json":                            {"billingAccountId", "enrollmentAccountId"},
		"cost-management-2025-03-01/Budgets/List/MCA/BillingAccountBudgetsList-MCA-CategoryTypeFilter.json":       {"billingAccountId"},
		"cost-management-2025-03-01/Budgets/List/MCA/BillingAccountBudgetsList-MCA.json":                          {"billingAccountId"},
		"cost-management-2025-03-01/Budgets/List/MCA/BillingProfileBudgetsList-CategoryTypeFilter.json":           {"billingAccountId", "billingProfileId"},
		"cost-management-2025-03-01/Budgets/List/MCA/BillingProfileBudgetsList.json":                              {"billingAccountId", "billingProfileId"},
		"cost-management-2025-03-01/Budgets/List/MCA/CustomerBudgetsList-CategoryTypeFilter.json":                 {"billingAccountId", "customerId"},
		"cost-management-2025-03-01/Budgets/List/MCA/CustomerBudgetsList.json":                                    {"billingAccountId", "customerId"},
		"cost-management-2025-03-01/Budgets/List/MCA/InvoiceSectionBudgetsList.json":                              {"billingAccountId", "billingProfileId", "invoiceSectionId"},
		"cost-management-2025-03-01/Budgets/List/RBAC/ManagementGroupBudgetsList.json":                            {"subscriptionId"},
		"cost-management-2025-03-01/Budgets/List/RBAC/ResourceGroupBudgetsList.json":                              {"resourceGroupName", "subscriptionId"},
		"cost-management-2025-03-01/Budgets/List/RBAC/SubscriptionBudgetsList.json":                               {"subscriptionId"},
	}
	discrepancies := map[string][]string{}
	if len(examples) != 20 || len(expected) != 4 {
		t.Fatal("incomplete native budget evidence")
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
	checked, responses, deletes, denied := 0, 0, 0, 0
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
			if _, err := bindAzureREST(operation, parameters); err == nil {
				t.Fatal("native extra-selector discrepancy changed")
			}
			for _, key := range requestExtras[example["file"]] {
				if _, exists := parameters[key]; !exists || object(operation.InputSchema["properties"])[key] != nil {
					t.Fatal("native scope selector discrepancy changed", key)
				}
				delete(parameters, key)
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
				called := false
				c := directClient(func(r *http.Request) (*http.Response, error) {
					called = true
					if r.Method != request.Method || r.URL.String() != request.URL {
						t.Fatal("native budget wire request changed")
					}
					if !exists {
						return &http.Response{StatusCode: code, Header: headers, Body: http.NoBody}, nil
					}
					return jsonResponse(code, value, headers), nil
				})
				c.subscription = "00000000-0000-0000-0000-000000000000"
				result, err := c.requestBody(context.Background(), request.Method, request.URL, request.Body, request.Headers)
				if !strings.HasPrefix(text(parameters["scope"]), "subscriptions/"+c.subscription) {
					// Billing and management-group examples remain native evidence,
					// but cannot cross this connection's subscription boundary.
					if err == nil || called {
						t.Fatal("foreign budget scope reached transport")
					}
					denied++
				} else {
					if err != nil || result.status != code || !called {
						t.Fatalf("HTTP %d: %v", code, err)
					}
					if exists {
						original, _ := json.Marshal(value)
						actual, _ := json.Marshal(result.data)
						if !bytes.Equal(original, actual) {
							t.Fatal("native budget private response changed")
						}
					}
					responses++
					if request.Method == http.MethodDelete {
						if exists || code != 200 || operationLocation(result.header) != "" {
							t.Fatal("native synchronous budget deletion changed")
						}
						deletes++
					}
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
	if checked != 18 || responses != 7 || deletes != 2 || denied != 13 || len(operations) != 6 || len(seenDiscrepancies) != len(discrepancies) {
		t.Fatal("incomplete native budget validation", checked, responses, deletes, len(operations), len(seenDiscrepancies))
	}
}

func TestMonitorBudgetPrivacyAndInvoke(t *testing.T) {
	private := map[string]any{
		"notifications": map[string]any{"private-notification": map[string]any{"enabled": true, "contactGroups": []any{resourceID(monitorActionGroupType, "private-contact-group")}, "contactEmails": []any{"private-email@example.test"}, "contactRoles": []any{"private-role"}}},
		"filter":        map[string]any{"dimensions": map[string]any{"name": "private-dimension", "values": []any{"private-value"}}},
		"amount":        100.65, "category": "Cost", "currentSpend": map[string]any{"amount": 80.89, "unit": "USD"},
	}
	before, _ := json.Marshal(private)
	for _, namespace := range []string{"Microsoft.Consumption", "Microsoft.CostManagement"} {
		t.Run(namespace, func(t *testing.T) {
			kind := namespace + "/budgets"
			entries := []execution.JobLogEntry{}
			ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { entries = append(entries, entry) }))
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || !strings.EqualFold(req.URL.Path, "/subscriptions/"+testSubscription+"/providers/"+kind) {
					t.Fatal("incorrect native budget invocation", req.URL)
				}
				return jsonResponse(200, map[string]any{"value": []any{map[string]any{"name": "selected", "properties": private}}}, nil), nil
			})
			result, err := r.Invoke(ctx, contracts.Invocation{ConnectionID: "connection", Operation: "Azure." + namespace + ".Budgets_List", Parameters: map[string]any{"scope": "subscriptions/" + testSubscription}})
			if err != nil || len(array(result.Data["value"])) != 1 {
				t.Fatal("native budget invocation failed", err)
			}
			raw := nativeResource(kind, "selected", "global", private)
			for _, value := range []any{entries, result, safePayload(raw), safePayload(map[string]any{"type": strings.ToUpper(kind), "properties": private}), safePayload(map[string]any{"id": strings.ToUpper(text(raw["id"])), "properties": private}), safeAPIPayload(map[string]any{"body": private}, apiURL(text(raw["id"]), "2025-03-01")), safePayload(map[string]any{"type": kind, "properties": map[string]any{"NOTIFICATIONS": private["notifications"], "Filter": private["filter"]}})} {
				encoded, _ := json.Marshal(value)
				if strings.Contains(string(encoded), "private-") {
					t.Fatal("private budget content escaped", string(encoded))
				}
			}
			props := object(object(array(result.Data["value"])[0])["properties"])
			if props["category"] != "Cost" || props["amount"] != 100.65 || object(props["currentSpend"])["unit"] != "USD" {
				t.Fatal("public budget metadata changed")
			}
		})
	}
	after, _ := json.Marshal(private)
	if !bytes.Equal(before, after) {
		t.Fatal("budget sanitization mutated private configuration")
	}
	for _, kind := range []string{vmType, "Microsoft.Custom/budgets", "Microsoft.Consumption/budgetsUnrelated"} {
		raw := nativeResource(kind, "unrelated", "eastus", private)
		if monitorBudgetPath(text(raw["id"])) || !reflect.DeepEqual(safeAPIPayload(private, apiURL(text(raw["id"]), "2024-08-01")), safePayload(private)) {
			t.Fatal("budget redaction changed another family", kind)
		}
	}
}
