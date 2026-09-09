package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func wafExample(t *testing.T, product, operation string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/waf/" + product + "/" + operation + ".json")
	var value map[string]any
	if err != nil || json.Unmarshal(payload, &value) != nil {
		t.Fatal("invalid native WAF example", err)
	}
	return value
}

func TestWAFNativeExamplesAndSchemas(t *testing.T) {
	payload, err := os.ReadFile("fixtures/waf/sources.json")
	var manifest []map[string]string
	if err != nil || json.Unmarshal(payload, &manifest) != nil || len(manifest) != 6 {
		t.Fatal("invalid WAF source manifest")
	}
	for _, entry := range manifest {
		payload, err := os.ReadFile("fixtures/waf/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] {
			t.Fatal("native WAF example bytes changed")
		}
	}
	payload, _ = os.ReadFile("catalog/source/swagger.json")
	var set catalog.RESTDocumentSet
	json.Unmarshal(payload, &set)
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
	for _, doc := range set.Documents {
		product := ""
		if strings.Contains(doc.SourceURI, "/Cdn/stable/2025-12-01/") {
			product = "cdn"
		} else if strings.Contains(doc.SourceURI, "/FrontDoor/stable/2025-11-01/") {
			product = "frontdoor"
		}
		if product == "" {
			continue
		}
		var native map[string]any
		json.Unmarshal(doc.Document, &native)
		for path, item := range object(native["paths"]) {
			for method, item := range object(item) {
				op := object(item)
				id := text(op["operationId"])
				if !strings.HasPrefix(id, "Policies_") || method != "get" {
					continue
				}
				schema, err := compiler.Compile(doc.SourceURI + "#/paths/" + strings.ReplaceAll(path, "/", "~1") + "/get/responses/200/schema")
				if err != nil {
					t.Fatal(err)
				}
				body := object(object(object(wafExample(t, product, id)["responses"])["200"])["body"])
				payload, _ := json.Marshal(body)
				value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
				if err := schema.Validate(value); err == nil {
					t.Fatal("documented native null inconsistency changed", product, id)
				}
				// Both official examples return null optional selectors; Front Door
				// also returns null logScrubbing. Omit only these fields in a copy.
				rows := []any{body}
				if listed, ok := body["value"].([]any); ok {
					rows = listed
				}
				for _, row := range rows {
					props := object(object(row)["properties"])
					settings := object(props["policySettings"])
					if settings["logScrubbing"] == nil {
						delete(settings, "logScrubbing")
					}
					for _, field := range []string{"customRules", "rateLimitRules"} {
						for _, rule := range array(object(props[field])["rules"]) {
							for _, match := range array(object(rule)["matchConditions"]) {
								if object(match)["selector"] == nil {
									delete(object(match), "selector")
								}
							}
						}
					}
				}
				payload, _ = json.Marshal(body)
				value, _ = jsonschema.UnmarshalJSON(bytes.NewReader(payload))
				if err := schema.Validate(value); err != nil {
					t.Fatalf("unexpected WAF source inconsistency %s/%s: %v", product, id, err)
				}
				checked++
			}
		}
	}
	if checked != 4 {
		t.Fatal("missing native WAF response checks", checked)
	}
}

func wafScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s, r, assets := cdnScenario(t)
	root := "/subscriptions/" + testSubscription
	for _, tc := range []struct{ product, kind, version, source, link string }{
		{"cdn", cdnWAFType, "2025-12-01", cdnEndpointType, "endpointLinks"},
		{"frontdoor", frontDoorWAFType, "2025-11-01", afdSecurityPolicyType, "securityPolicyLinks"},
	} {
		raw := object(object(object(wafExample(t, tc.product, "Policies_Get")["responses"])["200"])["body"])
		raw["id"] = root + "/resourcegroups/test/providers/" + tc.kind + "/policy1"
		raw["name"], raw["etag"] = "policy1", "waf-before-unlink"
		props := object(raw["properties"])
		for field := range wafLinkFields(tc.kind) {
			props[field] = []any{}
		}
		source := cdnAsset(t, assets, tc.source)
		props[tc.link] = []any{map[string]any{"id": source.Identity.NativeID}}
		if tc.product == "frontdoor" {
			raw["sku"] = map[string]any{"name": "Premium_AzureFrontDoor"}
			object(object(s.records[source.Identity.NativeID]["properties"])["parameters"])["wafPolicy"] = map[string]any{"id": raw["id"]}
		} else {
			object(s.records[source.Identity.NativeID]["properties"])["webApplicationFirewallPolicyLink"] = map[string]any{"id": raw["id"]}
		}
		s.add(raw, tc.version)
		collection := root + "/providers/" + strings.ToLower(tc.kind)
		if tc.product == "cdn" {
			collection = root + "/resourcegroups/test/providers/" + strings.ToLower(tc.kind)
		}
		s.lists[collection], s.version[collection] = []any{raw}, tc.version
		assets = append(assets, dnsAsset(t, r, raw))
		for i := range assets {
			if assets[i].ID == source.ID {
				assets[i] = dnsAsset(t, r, s.records[source.Identity.NativeID])
			}
		}
	}
	return s, r, assets
}

func TestWAFNativeDiscoveryAndReviewedUnlinkOrdering(t *testing.T) {
	for _, kind := range []string{cdnWAFType, frontDoorWAFType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := wafScenario(t)
			target := cdnAsset(t, assets, kind)
			batch, err := r.List(context.Background(), productRequest(r, kind))
			if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].NativeID != target.Identity.NativeID {
				t.Fatal("native WAF inventory", err)
			}
			request, input := dnsRequest(t, r, assets, target)
			if len(request.PrerequisiteDeletions) != 1 || len(request.LifecycleImpacts) != 0 {
				t.Fatalf("WAF claimed ownership of shared endpoints: %+v", request)
			}
			solved, _ := plan.Solve(input)
			if len(solved.Steps) != 2 {
				t.Fatalf("unreviewed WAF prerequisites %+v", solved)
			}
			referrer := request.PrerequisiteDeletions[0].Asset
			input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{referrer.Identity.NativeID}}}
			retained, err := plan.Solve(input)
			if err != nil || len(retained.Blockers) == 0 {
				t.Fatal("retained WAF referrer deleted", err)
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("associated WAF policy deleted")
			}
			for _, step := range solved.Steps {
				selected := assets[slices.IndexFunc(assets, func(value asset.Asset) bool { return value.ID == step.AssetID })]
				stepRequest := servicePlanRequest(solved, assets, selected)
				driver, _ := r.ResolveAction(context.Background(), "connection", selected)
				result, err := driver.Execute(context.Background(), stepRequest)
				if err != nil {
					t.Fatalf("WAF prerequisite %s: %v", selected.Identity.NativeType, err)
				}
				for _, impact := range stepRequest.LifecycleImpacts {
					s.gone[impact.Asset.Identity.NativeID] = true
				}
				payload, _ := json.Marshal(stepRequest)
				json.Unmarshal(payload, &stepRequest)
				payload, _ = json.Marshal(result)
				json.Unmarshal(payload, &result)
				driver, _ = r.ResolveAction(context.Background(), "connection", stepRequest.Asset)
				wait, err := driver.Wait(context.Background(), stepRequest, result)
				if err != nil || !wait.Done {
					t.Fatal("persisted WAF readback", err)
				}
				// Synthetic native reverse-index/ETag update after source deletion.
				for field := range wafLinkFields(kind) {
					object(s.records[target.Identity.NativeID]["properties"])[field] = []any{}
				}
				s.records[target.Identity.NativeID]["etag"] = "waf-after-unlink"
			}
			if len(s.deletes) != 2 || s.deletes[0] != referrer.Identity.NativeID || s.deletes[1] != target.Identity.NativeID {
				t.Fatal("WAF prerequisite order", s.deletes)
			}
		})
	}
}

func TestWAFConfigurationAndIncompleteAssociationsPreventWrites(t *testing.T) {
	for _, kind := range []string{cdnWAFType, frontDoorWAFType} {
		for _, mode := range []string{"missing-links", "null-links", "object-links", "duplicate-links", "wrong-type", "new-link", "private-rule", "private-body", "policy-mode", "tags", "sku", "location", "creation", "missing-private", "missing-config", "lock", "managed-group", "denied-get", "runtime"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, r, assets := wafScenario(t)
				target := cdnAsset(t, assets, kind)
				raw := s.records[target.Identity.NativeID]
				for field := range wafLinkFields(kind) {
					object(raw["properties"])[field] = []any{}
				}
				target = dnsAsset(t, r, raw)
				props := object(raw["properties"])
				field := "endpointLinks"
				if kind == frontDoorWAFType {
					field = "securityPolicyLinks"
				}
				link := map[string]any{"id": cdnAsset(t, assets, cdnEndpointType).Identity.NativeID}
				if kind == frontDoorWAFType {
					link["id"] = cdnAsset(t, assets, afdSecurityPolicyType).Identity.NativeID
				}
				switch mode {
				case "missing-links":
					delete(props, field)
				case "null-links":
					props[field] = nil
				case "object-links":
					props[field] = map[string]any{}
				case "duplicate-links":
					props[field] = []any{link, link}
				case "wrong-type":
					props[field] = []any{map[string]any{"id": resourceID(vmType, "other")}}
				case "new-link":
					props[field] = []any{link}
				case "private-rule":
					object(array(object(array(object(props["customRules"])["rules"])[0])["matchConditions"])[0])["matchValue"] = []any{"new-private-match"}
				case "private-body":
					name := "customBlockResponseBody"
					if kind == cdnWAFType {
						name = "defaultCustomBlockResponseBody"
					}
					object(props["policySettings"])[name] = "bmV3LXByaXZhdGUtYm9keQ=="
				case "policy-mode":
					object(props["policySettings"])["mode"] = "Detection"
				case "tags":
					raw["tags"] = map[string]any{"steward/protected": "true"}
				case "sku":
					raw["sku"] = map[string]any{"name": "another"}
				case "location":
					raw["location"] = "eastus"
				case "creation":
					raw["systemData"] = map[string]any{"createdAt": "2026-09-10T00:00:00Z"}
				case "missing-private":
					delete(target.Normalized, "_waf_private_configuration")
				case "missing-config":
					delete(target.Normalized, "_waf_configuration")
				case "lock":
					s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": target.Identity.NativeID + "/providers/Microsoft.Authorization/locks/frozen", "properties": map[string]any{"level": "CanNotDelete"}}}
				case "managed-group":
					s.records["/subscriptions/"+testSubscription+"/resourcegroups/test"] = map[string]any{"id": "/subscriptions/" + testSubscription + "/resourcegroups/test", "managedBy": resourceID(aksType, "owner")}
				case "denied-get":
					s.status[target.Identity.NativeID] = 403
				case "runtime":
					props["resourceState"], props["provisioningState"], raw["etag"] = "Disabled", "Updating", "new-runtime-etag"
				}
				if slices.Contains([]string{"missing-links", "null-links", "object-links", "duplicate-links", "wrong-type"}, mode) {
					if _, err := r.List(context.Background(), productRequest(r, kind)); err == nil {
						t.Fatal("incomplete WAF associations accepted as inventory")
					}
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", target)
				_, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"})
				if mode == "runtime" {
					if err != nil || len(s.deletes) != 1 {
						t.Fatal("WAF runtime state invalidated stable configuration", err)
					}
				} else if err == nil || len(s.deletes) != 0 {
					t.Fatal("WAF protection did not prevent deletion", mode, err)
				}
			})
		}
	}
}

func TestWAFMatchValuesAndResponseBodiesAreNotPersisted(t *testing.T) {
	s, r, assets := wafScenario(t)
	target := cdnAsset(t, assets, frontDoorWAFType)
	props := object(s.records[target.Identity.NativeID]["properties"])
	object(array(object(array(object(props["customRules"])["rules"])[0])["matchConditions"])[0])["matchValue"] = []any{"secret-waf-header"}
	object(props["policySettings"])["customBlockResponseBody"] = "c2VjcmV0LXdhZi1ib2R5"
	for _, value := range array(object(object(props["managedRules"])["exceptionsList"])["exceptions"]) {
		object(value)["matchValues"] = []any{"secret-waf-exception"}
	}
	batch, err := r.List(context.Background(), productRequest(r, frontDoorWAFType))
	if err != nil || len(batch.Items) != 1 {
		t.Fatal(err)
	}
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	c, _ := r.resolve(ctx, "connection")
	kind, _ := findType(frontDoorWAFType)
	endpoint, _ := c.resourceURL(kind, target.Identity.NativeID)
	if _, err := c.request(ctx, "GET", endpoint); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal([]any{batch, logs})
	for _, secret := range []string{"secret-waf-header", "c2VjcmV0LXdhZi1ib2R5", "secret-waf-exception"} {
		if strings.Contains(string(payload), secret) {
			t.Fatal("WAF private content escaped inventory/logs")
		}
	}
	if len(logs) != 2 {
		t.Fatal("missing WAF API diagnostics")
	}
}

func TestWAFReferencesMustBeObservedReciprocalAndStable(t *testing.T) {
	for _, mode := range []string{"missing-inventory", "foreign-subscription", "classic-frontend", "classic-routing", "duplicate-asset", "reciprocal", "wrong-referrer", "denied-referrer", "missing-referrer", "changed-links", "changed-private"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := wafScenario(t)
			target := cdnAsset(t, assets, frontDoorWAFType)
			source := cdnAsset(t, assets, afdSecurityPolicyType)
			raw := s.records[target.Identity.NativeID]
			switch mode {
			case "missing-inventory":
				assets = slices.DeleteFunc(assets, func(value asset.Asset) bool { return value.ID == source.ID })
			case "foreign-subscription":
				object(raw["properties"])["securityPolicyLinks"] = []any{map[string]any{"id": strings.Replace(source.Identity.NativeID, testSubscription, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", 1)}}
			case "classic-frontend", "classic-routing":
				field, collection := "frontendEndpointLinks", "frontendEndpoints"
				if mode == "classic-routing" {
					field, collection = "routingRuleLinks", "routingRules"
				}
				object(raw["properties"])[field] = []any{map[string]any{"id": "/subscriptions/" + testSubscription + "/resourcegroups/test/providers/Microsoft.Network/frontDoors/classic/" + collection + "/member"}}
			case "duplicate-asset":
				duplicate := source
				duplicate.ID = "duplicate-source"
				assets = append(assets, duplicate)
			case "reciprocal":
				object(object(s.records[source.Identity.NativeID]["properties"])["parameters"])["wafPolicy"] = map[string]any{"id": target.Identity.NativeID + "different"}
			case "wrong-referrer":
				s.records[source.Identity.NativeID]["id"] = source.Identity.NativeID + "foreign"
			case "denied-referrer":
				s.status[source.Identity.NativeID] = 403
			case "missing-referrer":
				s.status[source.Identity.NativeID] = 404
			case "changed-links", "changed-private":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, target.Identity.NativeID) {
						reads++
						if reads == 2 {
							if mode == "changed-links" {
								object(raw["properties"])["securityPolicyLinks"] = []any{}
							} else {
								object(object(raw["properties"])["policySettings"])["customBlockResponseBody"] = "Y2hhbmdlZA=="
							}
						}
					}
					return nil, false
				}
			}
			unresolved := slices.Contains([]string{"missing-inventory", "foreign-subscription", "classic-frontend", "classic-routing"}, mode)
			if unresolved {
				for i := range assets {
					if assets[i].ID == target.ID {
						assets[i] = dnsAsset(t, r, raw)
					}
				}
			}
			c, _ := r.resolve(context.Background(), "connection")
			result := governance.Contribution{}
			err := (&serviceCascades{client: c}).contributeWAFReferences(context.Background(), assets, &result)
			if unresolved {
				if err != nil || len(result.Unresolved) == 0 || len(result.Bindings) != 0 {
					t.Fatal("missing/unsupported WAF association was hidden or claimed as owned", err)
				}
			} else if err == nil {
				t.Fatal("inconsistent WAF association accepted", mode)
			}
			if len(s.deletes) != 0 {
				t.Fatal("WAF contribution wrote resources")
			}
		})
	}
}

func TestWAFForgedOrSurvivingPrerequisitesCannotAuthorizeDeletion(t *testing.T) {
	for _, mode := range []string{"alive", "wrong-policy", "foreign-connection", "not-delete", "wrong-controller"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := wafScenario(t)
			target := cdnAsset(t, assets, frontDoorWAFType)
			request, _ := dnsRequest(t, r, assets, target)
			object(s.records[target.Identity.NativeID]["properties"])["securityPolicyLinks"] = []any{}
			if mode != "alive" {
				s.gone[request.PrerequisiteDeletions[0].Asset.Identity.NativeID] = true
			}
			switch mode {
			case "wrong-policy":
				request.PrerequisiteDeletions[0].Asset.Normalized["_waf_policy"] = target.Identity.NativeID + "other"
			case "foreign-connection":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "other"
			case "not-delete":
				request.PrerequisiteDeletions[0].Delete = false
			case "wrong-controller":
				request.PrerequisiteDeletions[0].ControllerID = "other"
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("invalid WAF prerequisite authorized deletion", mode)
			}
		})
	}
}

func TestWAFAsyncDeletionBindsReceiptAndVerifiesFinalAbsence(t *testing.T) {
	for _, mode := range []string{"complete", "native-operation", "http200", "location", "failed", "error", "partial", "expired", "foreign-subscription", "foreign-provider", "foreign-group", "wrong-binding", "missing-binding"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := wafScenario(t)
			target := cdnAsset(t, assets, frontDoorWAFType)
			for field := range wafLinkFields(frontDoorWAFType) {
				object(s.records[target.Identity.NativeID]["properties"])[field] = []any{}
			}
			target = dnsAsset(t, r, s.records[target.Identity.NativeID])
			operation := "/subscriptions/" + testSubscription + "/resourcegroups/test/providers/Microsoft.Network/operations/waf-delete"
			endpoint := apiURL(operation, "2025-11-01")
			if mode == "native-operation" {
				// The official DELETE example returns an older API version and
				// a frontdoors operationResults path, despite deleting a policy.
				endpoint = text(object(object(object(wafExample(t, "frontdoor", "Policies_Delete")["responses"])["202"])["headers"])["azure-asyncoperation"])
				endpoint = strings.Replace(endpoint, "34adfa4f-cedf-4dc0-ba29-b6d1a69ab345", testSubscription, 1)
				operation = strings.Split(strings.TrimPrefix(endpoint, "https://management.azure.com"), "?")[0]
			}
			operationStatus, state := 200, "InProgress"
			polls := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" && strings.EqualFold(req.URL.Path, target.Identity.NativeID) {
					if req.URL.Query().Get("api-version") != "2025-11-01" {
						t.Fatal("wrong WAF delete version")
					}
					s.deletes = append(s.deletes, target.Identity.NativeID)
					if mode == "http200" {
						return jsonResponse(200, nil, nil), true
					}
					header := "Azure-Asyncoperation"
					if mode == "location" {
						header = "Location"
					}
					return jsonResponse(202, nil, http.Header{header: {endpoint}, "X-Ms-Request-Id": {"waf-delete-id"}}), true
				}
				if strings.EqualFold(req.URL.Path, operation) {
					polls++
					body := map[string]any{"status": state}
					if mode == "error" {
						body["error"] = map[string]any{"code": "PolicyFailure"}
					}
					if mode == "location" && state == "Succeeded" {
						return jsonResponse(204, nil, nil), true
					}
					return jsonResponse(operationStatus, body, nil), true
				}
				return nil, false
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			request := contracts.ActionRequest{Asset: target, Action: "delete"}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || len(s.deletes) != 1 {
				t.Fatal("native WAF delete", err)
			}
			if mode == "failed" {
				state = "Failed"
			} else if mode == "partial" {
				operationStatus, state = 206, "Succeeded"
			} else if mode == "expired" {
				operationStatus = 404
			} else if mode == "foreign-subscription" {
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, testSubscription, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", 1)
			} else if mode == "foreign-provider" {
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, "Microsoft.Network", "Microsoft.Cdn", 1)
			} else if mode == "foreign-group" {
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, "/resourcegroups/test/", "/resourcegroups/other/", 1)
			} else if mode == "wrong-binding" {
				result.Data["waf_operation_binding"] = "another-resource"
			} else if mode == "missing-binding" {
				delete(result.Data, "waf_operation_binding")
			}
			payload, _ := json.Marshal(result)
			json.Unmarshal(payload, &result)
			payload, _ = json.Marshal(request)
			json.Unmarshal(payload, &request)
			driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
			wait, err := driver.Wait(context.Background(), request, result)
			if slices.Contains([]string{"complete", "native-operation", "http200", "location", "expired"}, mode) {
				if err != nil || wait.Done {
					t.Fatal("WAF delete completed before resource absence", err)
				}
				state = "Succeeded"
				wait, err = driver.Wait(context.Background(), request, result)
				if err != nil || wait.Done {
					t.Fatal("WAF operation success hid live policy", err)
				}
				s.gone[target.Identity.NativeID] = true
				wait, err = driver.Wait(context.Background(), request, result)
				if err != nil || !wait.Done {
					t.Fatal("WAF final 404 not reconciled", err)
				}
			} else if err == nil || wait.Done {
				t.Fatal("invalid WAF operation accepted", mode, err)
			}
			if strings.HasPrefix(mode, "foreign-") || strings.Contains(mode, "binding") {
				if polls != 0 {
					t.Fatal("invalid receipt performed a request")
				}
			}
		})
	}
}

func TestWAFRecordedCLIResponseAndOriginalRequestIDs(t *testing.T) {
	payload, err := os.ReadFile("fixtures/waf/cli-recording.json")
	var source map[string]any
	if err != nil || json.Unmarshal(payload, &source) != nil || source["source_sha256"] != "74fea5e660d039b73b17087ba1f939d4a136be8a666f35017a68fbd131f519e6" || len(array(source["recordings"])) != 4 {
		t.Fatal("invalid native WAF CLI recording")
	}
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	json.Unmarshal(payload, &source)
	records := map[int]map[string]any{}
	for _, value := range array(source["recordings"]) {
		row := object(value)
		records[int(row["interaction_index"].(float64))] = row
	}
	s := newDNSScenario()
	// Native RG LIST bodies replay through the subscription LIST with the same
	// response schema. Non-target detail bodies are synthesized from list rows.
	for _, value := range array(object(records[27]["body"])["value"]) {
		s.add(object(value), "2025-11-01")
	}
	raw := object(records[26]["body"])
	s.add(raw, "2025-11-01")
	id := strings.ToLower(text(raw["id"]))
	group := strings.Join(strings.Split(id, "/")[:5], "/")
	s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
	collection := "/subscriptions/" + testSubscription + "/providers/microsoft.network/frontdoorwebapplicationfirewallpolicies"
	currentList := 27
	nativeResponse := func(record map[string]any) *http.Response {
		header := http.Header{}
		for name, values := range object(record["headers"]) {
			for _, value := range array(values) {
				header.Add(name, text(value))
			}
		}
		return jsonResponse(int(record["status"].(float64)), record["body"], header)
	}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, collection) {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != "2025-11-01" {
				t.Fatal("wrong native WAF collection request")
			}
			return nativeResponse(records[currentList]), true
		}
		if strings.EqualFold(req.URL.Path, id) {
			if req.Method == "DELETE" {
				if req.Header.Get("x-ms-client-request-id") != azureRequestID("recorded-waf-delete") || req.ContentLength > 0 {
					t.Fatal("incorrect native WAF DELETE")
				}
				s.deletes = append(s.deletes, id)
				return nativeResponse(records[28]), true
			}
			if !s.gone[id] {
				return nativeResponse(records[26]), true
			}
		}
		return nil, false
	}
	r := s.runtime(t)
	batch, err := r.List(context.Background(), productRequest(r, frontDoorWAFType))
	if err != nil || !batch.Complete || len(batch.Items) != 6 || batch.RequestID != "316306a7-cba3-4380-bb92-a6159d63c53d" {
		t.Fatal("native WAF collection/request ID", err, batch.RequestID, len(batch.Items))
	}
	target := dnsAsset(t, r, raw)
	driver, _ := r.ResolveAction(context.Background(), "connection", target)
	request := contracts.ActionRequest{Asset: target, Action: "delete", IdempotencyKey: "recorded-waf-delete"}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || len(s.deletes) != 1 || result.ProviderOperationID != "" {
		t.Fatal("native WAF synchronous deletion", err)
	}
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || wait.Done {
		t.Fatal("native DELETE 204 bypassed final absence", err)
	}
	// The recording proves absence from LIST. Target GET 404 is a separate
	// synthetic readback; it is not represented as recorded cloud evidence.
	s.gone[id], currentList = true, 29
	wait, err = driver.Wait(context.Background(), request, result)
	if err != nil || !wait.Done {
		t.Fatal("WAF final readback", err)
	}
	batch, err = r.List(context.Background(), productRequest(r, frontDoorWAFType))
	if err != nil || len(batch.Items) != 5 || batch.RequestID != "a9234eae-1c42-4a7d-b6e3-d51cd1e8a0e6" {
		t.Fatal("native WAF list after deletion", err)
	}
	if requestID(http.Header{"X-Ms-Request-Id": {"canonical"}, "X-Ms-Original-Request-Ids": {"fallback"}}) != "canonical" {
		t.Fatal("fallback shadowed canonical ARM request ID")
	}
}

func TestWAFNativePaginationAndScopes(t *testing.T) {
	for _, kind := range []string{cdnWAFType, frontDoorWAFType} {
		for _, mode := range []string{"complete", "regional", "global", "wrong-region", "foreign-subscription", "cycle", "version", "foreign-host", "foreign-collection", "denied-list", "partial-list", "foreign-item", "changed-parents"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, r, assets := wafScenario(t)
				target := cdnAsset(t, assets, kind)
				version := "2025-11-01"
				collection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(kind)
				if kind == cdnWAFType {
					version = "2025-12-01"
					collection = "/subscriptions/" + testSubscription + "/resourcegroups/test/providers/" + strings.ToLower(kind)
				}
				raw := s.records[target.Identity.NativeID]
				location := "global"
				if mode == "regional" {
					location = "westus"
				}
				raw["location"] = location
				payload, _ := json.Marshal(raw)
				var second map[string]any
				json.Unmarshal(payload, &second)
				second["id"], second["name"] = target.Identity.NativeID+"second", "policy1second"
				if mode == "foreign-item" {
					second["id"] = strings.Replace(text(second["id"]), testSubscription, "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee", 1)
				}
				s.add(second, version)
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if !strings.EqualFold(req.URL.Path, collection) {
						return nil, false
					}
					if req.Method != "GET" || req.URL.Query().Get("api-version") != version {
						t.Fatal("incorrect native WAF list binding")
					}
					if mode == "denied-list" || mode == "partial-list" {
						status := 403
						if mode == "partial-list" {
							status = 206
						}
						return jsonResponse(status, map[string]any{"value": []any{}}, nil), true
					}
					if req.URL.Query().Get("$skiptoken") == "second" && mode != "cycle" {
						return jsonResponse(200, map[string]any{"value": []any{second}}, nil), true
					}
					next := apiURL(collection, version) + "&%24skiptoken=second"
					switch mode {
					case "version":
						next = strings.Replace(next, version, "1900-01-01", 1)
					case "foreign-host":
						next = strings.Replace(next, "management.azure.com", "untrusted.invalid", 1)
					case "foreign-collection":
						next = strings.Replace(next, strings.ToLower(kind), "microsoft.network/virtualnetworks", 1)
					}
					return jsonResponse(200, map[string]any{"value": []any{raw}, "nextLink": next}, nil), true
				}
				request := productRequest(r, kind)
				switch mode {
				case "regional":
					request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westus"}
				case "global":
					request.Scope = asset.Scope{Kind: asset.ScopeGlobal, NativeID: testSubscription + "/global"}
				case "wrong-region":
					request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
				case "foreign-subscription":
					request.Scope.NativeID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
				}
				first, err := r.List(context.Background(), request)
				if slices.Contains([]string{"foreign-subscription", "version", "foreign-host", "foreign-collection", "denied-list", "partial-list"}, mode) {
					if err == nil {
						t.Fatal("invalid WAF page established inventory")
					}
					return
				}
				if err != nil || first.Complete || first.NextCursor == "" {
					t.Fatal("first WAF page", err)
				}
				if mode == "changed-parents" && kind == cdnWAFType {
					// A newly discovered RG changes the collection set for CDN WAF.
					groups := "/subscriptions/" + testSubscription + "/resourcegroups"
					s.lists[groups] = append(s.lists[groups], map[string]any{"id": groups + "/second", "type": groupType})
				}
				request.Cursor = first.NextCursor
				last, err := r.List(context.Background(), request)
				if mode == "cycle" || mode == "foreign-item" || (mode == "changed-parents" && kind == cdnWAFType) {
					if err == nil {
						t.Fatal("inconsistent WAF continuation established inventory")
					}
					return
				}
				count := 2
				if mode == "wrong-region" {
					count = 0
				}
				if err != nil || !last.Complete || len(first.Items)+len(last.Items) != count {
					t.Fatal("WAF paging/scope coverage", err)
				}
			})
		}
	}
}
