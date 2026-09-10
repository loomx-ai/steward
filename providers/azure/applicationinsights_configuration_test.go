package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestApplicationInsightsConfigurationInventory(t *testing.T) {
	f := newInsightsInventoryFixture(t)
	detection := f.detections["slowpageloadtime"]
	detection["customEmails"] = []any{"PRIVATE_DETECTION_EMAIL"}
	object(detection["ruleDefinitions"])["Description"] = "PRIVATE_RULE_DEFINITION"
	page, err := f.runtime.List(t.Context(), productRequest(f.runtime, applicationInsightsType))
	if err != nil || !page.Complete || len(page.Items) != 1 {
		t.Fatal("native configuration inventory", page, err)
	}
	item := page.Items[0]
	if text(item.Normalized[insightsSettingsProof]) == "" || object(item.Normalized["billing_features"])["DataVolumeCap"] == nil || text(object(object(item.Normalized["pricing_plan"])["properties"])["planType"]) != "Basic" || object(item.Normalized["quota_status"])["ShouldBeThrottled"] != true || object(item.Normalized["feature_capabilities"])["DailyCap"] != 0.0323 || len(array(object(item.Normalized["available_billing_features"])["Result"])) != 2 || object(object(item.Normalized["proactive_detection"])["slowpageloadtime"])["enabled"] != true {
		t.Fatal("lost original configuration fields or private binding", item.Normalized)
	}
	encoded, err := json.Marshal(page)
	if err != nil || strings.Contains(string(encoded), "PRIVATE_") || strings.Contains(string(encoded), "customEmails") || strings.Contains(string(encoded), "ruleDefinitions") {
		t.Fatal("private native configuration leaked", err)
	}
	request := productRequest(f.runtime, applicationInsightsType)
	// Read-only values can change between snapshots. They are still returned,
	// but do not invalidate the authored configuration proof or scan cursor.
	f.before = func(req *http.Request) {
		object(f.configurations["currentbillingfeatures"]["DataVolumeCap"])["MaxHistoryCap"] = float64(time.Now().UnixNano())
		object(f.configurations["pricingplans/current"]["properties"])["resetHour"] = float64(time.Now().Nanosecond())
		f.configurations["quotastatus"]["ExpirationTime"] = time.Now().String()
		f.configurations["featurecapabilities"]["DailyCap"] = float64(time.Now().Nanosecond())
		detection["lastUpdatedTime"] = time.Now().String()
		object(detection["ruleDefinitions"])["Description"] = time.Now().String()
	}
	current, err := f.runtime.List(t.Context(), request)
	if err != nil || len(current.Items) != 1 || current.Items[0].Normalized[insightsSettingsProof] != item.Normalized[insightsSettingsProof] {
		t.Fatal("read-only observations changed authored configuration", err)
	}
}

func TestApplicationInsightsConfigurationReadFailures(t *testing.T) {
	for _, mode := range []string{"403", "404", "206", "202", "lro", "error", "code", "empty", "next-link", "billing-shape", "pricing-foreign", "pricing-type", "pricing-shape", "quota-foreign", "available-shape", "detection-object-list", "detection-duplicate-original", "detection-name-alias", "detection-selector", "detection-get-name", "detection-get-404", "detection-private-disagrees", "detection-invalid-enabled", "detection-invalid-emails", "authored-snapshot-drift", "parent-drift"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsInventoryFixture(t)
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if path == f.parentID+"/currentbillingfeatures" {
					raw := maps.Clone(f.configurations["currentbillingfeatures"])
					switch mode {
					case "403":
						return jsonResponse(403, nil, nil), true
					case "404":
						return jsonResponse(404, nil, nil), true
					case "206":
						return jsonResponse(206, raw, nil), true
					case "202":
						return jsonResponse(202, raw, nil), true
					case "lro":
						return jsonResponse(200, raw, http.Header{"Azure-Asyncoperation": {apiURL(f.parentID+"/operations/one", insightsLegacyVersion)}}), true
					case "error", "code":
						raw[mode] = "NotAuthorized"
					case "empty":
						raw = map[string]any{}
					case "next-link":
						raw["nextLink"] = "https://example.invalid/another"
					case "billing-shape":
						raw["CurrentBillingFeatures"] = "Basic"
					case "authored-snapshot-drift":
						if f.componentLists > 1 {
							raw["CurrentBillingFeatures"] = []any{"Application Insights Enterprise"}
						}
					case "parent-drift":
						object(f.parent["properties"])["AppId"] = "replacement"
					default:
						return nil, false
					}
					return jsonResponse(200, raw, nil), true
				}
				if path == f.parentID+"/pricingplans/current" && strings.HasPrefix(mode, "pricing-") {
					raw := maps.Clone(f.configurations["pricingplans/current"])
					switch mode {
					case "pricing-foreign":
						raw["id"] = strings.Replace(text(raw["id"]), testSubscription, testTenant, 1)
					case "pricing-type":
						raw["type"] = applicationInsightsType
					case "pricing-shape":
						raw["properties"] = []any{}
					}
					return jsonResponse(200, raw, nil), true
				}
				if path == f.parentID+"/quotastatus" && mode == "quota-foreign" {
					return jsonResponse(200, f.configurations["quotastatus"], nil), true // Original example belongs to another AppId.
				}
				if path == f.parentID+"/getavailablebillingfeatures" && mode == "available-shape" {
					return jsonResponse(200, map[string]any{"Result": "Basic"}, nil), true
				}
				detection := maps.Clone(f.detections["slowpageloadtime"])
				if path == f.parentID+"/proactivedetectionconfigs" {
					switch mode {
					case "detection-object-list":
						return jsonResponse(200, map[string]any{"value": []any{detection}}, nil), true
					case "detection-duplicate-original":
						data, err := os.ReadFile("fixtures/applicationinsights/stable/2015-05-01/examples/ProactiveDetectionConfigurationsList.json")
						if err != nil {
							t.Fatal(err)
						}
						var example map[string]any
						if err := json.Unmarshal(data, &example); err != nil {
							t.Fatal(err)
						}
						return jsonResponse(200, object(object(example["responses"])["200"])["body"], nil), true
					case "detection-name-alias":
						detection["Name"] = detection["name"]
					case "detection-selector":
						detection["name"] = "one/../two"
					case "detection-private-disagrees":
						detection["customEmails"] = []any{"PRIVATE_CHANGED_EMAIL"}
					case "detection-invalid-enabled":
						detection["enabled"] = "true"
					case "detection-invalid-emails":
						detection["customEmails"] = []any{false}
					default:
						return nil, false
					}
					return jsonResponse(200, []any{detection}, nil), true
				}
				if path == f.parentID+"/proactivedetectionconfigs/slowpageloadtime" {
					switch mode {
					case "detection-get-name":
						detection["name"] = "slowserverresponsetime"
						return jsonResponse(200, detection, nil), true
					case "detection-get-404":
						return jsonResponse(404, nil, nil), true
					}
				}
				return nil, false
			}
			page, err := f.runtime.List(t.Context(), productRequest(f.runtime, applicationInsightsType))
			if err == nil || page.Complete || len(page.Items) != 0 || len(f.deletes) != 0 {
				t.Fatal("unproved native configuration became authoritative", page, err)
			}
		})
	}
}

func TestApplicationInsightsConfigurationDeletionBindings(t *testing.T) {
	for _, mode := range []string{"billing-cap", "pricing-cap", "pricing-plan", "notification", "private-email", "enabled", "new-rule", "removed-rule", "unknown-setting", "proof-removed", "proof-replaced", "request-proof-changed", "preflight-drift", "observations", "original"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsComponentFixture(t)
			request, planned, values := insightsComponentPlan(t, f)
			if text(request.Asset.Normalized[insightsSettingsProof]) == "" {
				t.Fatal("SQLite projection lost configuration binding")
			}
			deleteInsightsPrerequisites(t, f, planned, values, request.Asset.ID)
			f.deletes = nil
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			detection := f.detections["slowpageloadtime"]
			switch mode {
			case "billing-cap":
				object(f.configurations["currentbillingfeatures"]["DataVolumeCap"])["Cap"] = float64(600)
			case "pricing-cap":
				object(f.configurations["pricingplans/current"]["properties"])["cap"] = float64(600)
			case "pricing-plan":
				object(f.configurations["pricingplans/current"]["properties"])["planType"] = "Application Insights Enterprise"
			case "notification":
				detection["sendEmailsToSubscriptionOwners"] = false
			case "private-email":
				detection["customEmails"] = []any{"PRIVATE_CHANGED_EMAIL"}
			case "enabled":
				detection["enabled"] = false
			case "new-rule":
				row := maps.Clone(detection)
				row["name"] = "slowserverresponsetime"
				f.detections[text(row["name"])] = row
			case "removed-rule":
				delete(f.detections, "slowpageloadtime")
			case "unknown-setting":
				detection["notificationConfiguration"] = "PRIVATE_FUTURE_VALUE"
			case "proof-removed", "proof-replaced", "request-proof-changed":
				request.Asset.Normalized = maps.Clone(request.Asset.Normalized)
				delete(request.Asset.Normalized, insightsSettingsProof)
				if mode != "proof-removed" {
					request.Asset.Normalized[insightsSettingsProof] = "other-proof"
				}
				if mode != "request-proof-changed" {
					driver, err = f.runtime.ResolveAction(t.Context(), "connection", request.Asset)
					if err != nil {
						return // Missing persisted configuration must require a fresh scan.
					}
				}
			case "preflight-drift":
				reads := 0
				f.before = func(req *http.Request) {
					if strings.HasSuffix(strings.ToLower(req.URL.Path), "/currentbillingfeatures") {
						reads++
						if reads > 1 {
							detection["customEmails"] = []any{"PRIVATE_CHANGED_EMAIL"}
						}
					}
				}
			case "observations":
				object(f.configurations["currentbillingfeatures"]["DataVolumeCap"])["MaxHistoryCap"] = float64(1000)
				object(f.configurations["pricingplans/current"]["properties"])["resetHour"] = float64(4)
				f.configurations["quotastatus"]["ShouldBeThrottled"] = false
				detection["lastUpdatedTime"] = time.Now().String()
				object(detection["ruleDefinitions"])["DisplayName"] = "New localized title"
			}
			result, err := driver.Execute(t.Context(), request)
			if mode != "observations" && mode != "original" {
				if err == nil || len(f.deletes) != 0 {
					t.Fatal("unreviewed settings allowed component DELETE", err, f.deletes)
				}
				return
			}
			if err != nil || len(f.deletes) != 1 || f.deletes[0] != f.parentID {
				t.Fatal("reviewed component failed native deletion", err, f.deletes)
			}
			// A restart reconstructs the driver and binds the new settings proof
			// into the persisted deletion receipt as well as the original request.
			encoded, _ := json.Marshal(request)
			var restarted contracts.ActionRequest
			if err := json.Unmarshal(encoded, &restarted); err != nil {
				t.Fatal(err)
			}
			driver, err = f.runtime.ResolveAction(t.Context(), "connection", restarted.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if wait, err := driver.Wait(t.Context(), restarted, result); err != nil || !wait.Done {
				t.Fatal("native completion lost settings receipt after restart", wait, err)
			}
			restarted.Asset.Normalized[insightsSettingsProof] = "substituted-settings"
			driver, err = f.runtime.ResolveAction(t.Context(), "connection", restarted.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if wait, err := driver.Wait(t.Context(), restarted, result); err == nil || wait.Done {
				t.Fatal("receipt accepted replaced settings proof", wait, err)
			}
		})
	}
}
