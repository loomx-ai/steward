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

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const appServiceVersion = "2025-05-01"

func appServiceExample(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/appservice/" + name + ".json")
	var example map[string]any
	if err != nil || json.Unmarshal(payload, &example) != nil {
		t.Fatal("invalid native App Service example", err)
	}
	return example
}

func appServiceScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	group := root + "/resourcegroups/testrg123"
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
	var raws []map[string]any
	for _, op := range []string{"WebApps_Get", "WebApps_GetSlot", "Certificates_Get", "SiteCertificates_Get", "SiteCertificates_GetSlot"} {
		raw := object(object(object(appServiceExample(t, op)["responses"])["200"])["body"])
		payload, _ := json.Marshal(raw)
		payload = bytes.ReplaceAll(payload, []byte("34adfa4f-cedf-4dc0-ba29-b6d1a69ab345"), []byte(testSubscription))
		payload = bytes.ReplaceAll(payload, []byte("testSiteName"), []byte("sitef6141"))
		json.Unmarshal(payload, &raw)
		id, typ, _ := parseID(text(raw["id"]))
		raw["id"], raw["type"] = id, typ
		if strings.EqualFold(typ, appSiteType) || strings.EqualFold(typ, appSlotType) {
			raw["kind"] = "functionapp"
		}
		s.add(raw, appServiceVersion)
		raws = append(raws, raw)
	}
	for _, parent := range raws[:2] {
		id := text(parent["id"])
		props := object(parent["properties"])
		function := map[string]any{"id": id + "/functions/http", "type": appFunctionType, "name": "http", "properties": map[string]any{"function_app_id": id, "language": "JavaScript", "isDisabled": false, "config": map[string]any{"bindings": []any{map[string]any{"type": "httpTrigger", "direction": "in", "authLevel": "function", "name": "req"}}}, "files": map[string]any{"index.js": "private-function-source"}, "script_href": "https://example.scm.azurewebsites.net/api/vfs/index.js?code=private-function-link"}}
		if strings.Contains(id, "/slots/") {
			function["type"] = appSlotFunctionType
		}
		s.add(function, appServiceVersion)
		raws = append(raws, function)
		for _, name := range []string{text(props["defaultHostName"]), "custom-" + last(id) + ".example.com"} {
			bindingType := appBindingType
			if strings.Contains(id, "/slots/") {
				bindingType = appSlotBindingType
			}
			binding := map[string]any{"id": id + "/hostnamebindings/" + name, "type": bindingType, "name": name, "properties": map[string]any{"siteName": "sitef6141", "hostNameType": "Verified", "sslState": "Disabled"}}
			s.add(binding, appServiceVersion)
			raws = append(raws, binding)
			if strings.HasPrefix(name, "custom-") {
				props["hostNameSslStates"] = append(array(props["hostNameSslStates"]), map[string]any{"name": name, "hostType": "Standard", "sslState": "Disabled"})
				props["hostNames"] = append(array(props["hostNames"]), name)
			}
		}
	}
	for _, raw := range raws {
		id := text(raw["id"])
		_, kind, _ := parseID(id)
		collection := id[:strings.LastIndex(id, "/")]
		if strings.EqualFold(kind, appSiteType) || strings.EqualFold(kind, appCertificateType) {
			collection = root + "/providers/" + kind
		}
		s.lists[collection] = append(s.lists[collection], raw)
		s.version[collection] = appServiceVersion
	}
	r := s.runtime(t)
	assets := []asset.Asset{}
	for _, raw := range raws {
		assets = append(assets, dnsAsset(t, r, raw))
	}
	return s, r, assets
}

func TestAppServiceNativeSchemasAndProvenance(t *testing.T) {
	payload, err := os.ReadFile("fixtures/appservice/sources.json")
	var manifest []map[string]string
	if err != nil || json.Unmarshal(payload, &manifest) != nil || len(manifest) != 15 {
		t.Fatal("invalid App Service sources")
	}
	for _, entry := range manifest {
		payload, err := os.ReadFile("fixtures/appservice/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] {
			t.Fatal("native example changed")
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
	checked, synthetic := 0, 0
	for _, doc := range set.Documents {
		if !strings.Contains(doc.SourceURI, "/AppService/stable/2025-05-01/") {
			continue
		}
		var native map[string]any
		json.Unmarshal(doc.Document, &native)
		for path, item := range object(native["paths"]) {
			op := object(object(item)["get"])
			if len(op) == 0 {
				continue
			}
			schema, err := compiler.Compile(doc.SourceURI + "#/paths/" + strings.ReplaceAll(path, "/", "~1") + "/get/responses/200/schema")
			if err != nil {
				t.Fatal(err)
			}
			for _, entry := range manifest {
				if entry["file"] != text(op["operationId"])+".json" {
					continue
				}
				body := object(object(appServiceExample(t, strings.TrimSuffix(entry["file"], ".json"))["responses"])["200"])["body"]
				payload, _ := json.Marshal(body)
				value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
				if err := schema.Validate(value); err != nil {
					t.Fatalf("native %s: %v", op["operationId"], err)
				}
				checked++
			}
			if op["operationId"] == "WebApps_GetFunction" || op["operationId"] == "WebApps_GetHostNameBinding" || op["operationId"] == "WebApps_GetInstanceFunctionSlot" || op["operationId"] == "WebApps_GetHostNameBindingSlot" {
				s, _, assets := appServiceScenario(t)
				kind := map[string]string{"WebApps_GetFunction": appFunctionType, "WebApps_GetHostNameBinding": appBindingType, "WebApps_GetInstanceFunctionSlot": appSlotFunctionType, "WebApps_GetHostNameBindingSlot": appSlotBindingType}[text(op["operationId"])]
				raw := s.records[cdnAsset(t, assets, kind).Identity.NativeID]
				payload, _ := json.Marshal(raw)
				value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
				if err := schema.Validate(value); err != nil {
					t.Fatalf("synthetic %s: %v", kind, err)
				}
				synthetic++
			}
		}
	}
	if checked != 10 || synthetic != 4 {
		t.Fatal("missing independent App Service schema checks", checked, synthetic)
	}
}

func TestAppServiceDiscoveryCascadeAndIndependentActions(t *testing.T) {
	for _, kind := range []string{appSiteType, appSlotType, appFunctionType, appSlotFunctionType, appCertificateType, appSiteCertificateType, appSlotCertificateType, appBindingType, appSlotBindingType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := appServiceScenario(t)
			target := cdnAsset(t, assets, kind)
			if isAppBinding(kind) {
				target = assets[slices.IndexFunc(assets, func(a asset.Asset) bool {
					return a.Identity.NativeType == kind && strings.HasPrefix(last(a.Identity.NativeID), "custom-")
				})]
			}
			batch, err := r.List(context.Background(), productRequest(r, kind))
			count := 1
			if isAppBinding(kind) {
				count = 2
			}
			if err != nil || !batch.Complete || len(batch.Items) != count {
				t.Fatal("native App Service discovery", err, len(batch.Items))
			}
			request, input := dnsRequest(t, r, assets, target)
			expected := 0
			if kind == appSiteType {
				expected = 9
			} else if kind == appSlotType {
				expected = 4
			}
			if len(request.LifecycleImpacts) != expected {
				t.Fatal("missing native cascade impacts", len(request.LifecycleImpacts), expected)
			}
			solved, err := plan.Solve(input)
			if err != nil || len(solved.Steps) != 1 {
				t.Fatal("native App Service plan", err, solved)
			}
			if expected > 0 {
				input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{request.LifecycleImpacts[0].Asset.Identity.NativeID}}}
				retained, err := plan.Solve(input)
				if err != nil || len(retained.Blockers) == 0 {
					t.Fatal("retained child did not block app/slot deletion", err)
				}
			}
			writes := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "DELETE" {
					return nil, false
				}
				writes++
				if !strings.EqualFold(req.URL.Path, target.Identity.NativeID) || req.URL.Query().Get("api-version") != appServiceVersion {
					t.Fatal("wrong native App Service delete")
				}
				if (kind == appSiteType || kind == appSlotType) && req.URL.Query().Get("deleteEmptyServerFarm") != "false" {
					t.Fatal("App Service plan was not retained")
				}
				return jsonResponse(204, nil, http.Header{"X-Ms-Request-Id": {"native-app-delete"}}), true
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			result, err := driver.Execute(context.Background(), request)
			if err != nil || writes != 1 {
				t.Fatal("native App Service execution", err)
			}
			payload, _ := json.Marshal(request)
			json.Unmarshal(payload, &request)
			payload, _ = json.Marshal(result)
			json.Unmarshal(payload, &result)
			driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
			wait, err := driver.Wait(context.Background(), request, result)
			if err != nil || wait.Done {
				t.Fatal("DELETE success hid live app resource", err)
			}
			s.gone[target.Identity.NativeID] = true
			if expected > 0 {
				wait, err = driver.Wait(context.Background(), request, result)
				if err != nil || wait.Done {
					t.Fatal("parent absence hid surviving app children", err)
				}
			}
			for _, impact := range request.LifecycleImpacts {
				s.gone[impact.Asset.Identity.NativeID] = true
			}
			wait, err = driver.Wait(context.Background(), request, result)
			if err != nil || !wait.Done {
				t.Fatal("persisted App Service final absence", err)
			}
		})
	}
}

func TestAppServiceConfigurationProtectionAndParentDrift(t *testing.T) {
	for _, kind := range []string{appSiteType, appSlotType, appFunctionType, appSlotFunctionType, appCertificateType, appSiteCertificateType, appSlotCertificateType, appBindingType, appSlotBindingType} {
		for _, mode := range []string{"configuration", "missing-public", "missing-private", "lock", "creation", "parent-change", "runtime"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, r, assets := appServiceScenario(t)
				target := cdnAsset(t, assets, kind)
				if isAppBinding(kind) {
					target = assets[slices.IndexFunc(assets, func(a asset.Asset) bool {
						return a.Identity.NativeType == kind && strings.HasPrefix(last(a.Identity.NativeID), "custom-")
					})]
				}
				request, _ := dnsRequest(t, r, assets, target)
				raw := s.records[target.Identity.NativeID]
				switch mode {
				case "configuration":
					raw["tags"] = map[string]any{"changed": "configuration"}
				case "missing-public":
					delete(request.Asset.Normalized, "_app_service_configuration")
				case "missing-private":
					delete(request.Asset.Normalized, "_app_service_private_configuration")
				case "lock":
					s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": target.Identity.NativeID + "/providers/microsoft.authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}
				case "creation":
					raw["systemData"] = map[string]any{"createdAt": "2026-09-10T01:00:00Z"}
				case "parent-change":
					if kind != appSiteType && kind != appCertificateType {
						s.records[target.Identity.NativeID[:strings.LastIndex(strings.TrimSuffix(target.Identity.NativeID, "/"+last(target.Identity.NativeID)), "/")]]["tags"] = map[string]any{"changed": "parent"}
					} else {
						raw["tags"] = map[string]any{"changed": "self"}
					}
				case "runtime":
					object(raw["properties"])["provisioningState"] = "Updating"
					if kind == appSiteType || kind == appSlotType {
						object(raw["properties"])["lastModifiedTimeUtc"] = "2026-09-10T01:00:00Z"
					}
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", target)
				_, err := driver.Execute(context.Background(), request)
				if mode == "runtime" {
					if err != nil {
						t.Fatal("documented runtime field blocked stable App Service configuration", err)
					}
				} else if err == nil || len(s.deletes) != 0 {
					t.Fatal("App Service protection allowed a write", mode, err)
				}
			})
		}
	}
}

func TestAppServicePrivateValuesStayPrivateAndBindCleanup(t *testing.T) {
	for _, kind := range []string{appSiteType, appFunctionType, appCertificateType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := appServiceScenario(t)
			target := cdnAsset(t, assets, kind)
			raw := s.records[target.Identity.NativeID]
			props := object(raw["properties"])
			secretField := "pfxBlob"
			if kind == appFunctionType {
				secretField = "test_data"
			} else if kind == appSiteType {
				secretField = "customDomainVerificationId"
			}
			props[secretField] = "private-app-sensitive-marker"
			target = dnsAsset(t, r, raw)
			payload, _ := json.Marshal(target)
			if strings.Contains(string(payload), "private-app-sensitive-marker") || strings.Contains(string(payload), "private-function-") {
				t.Fatal("App Service content escaped persisted inventory")
			}
			props[secretField] = "private-app-sensitive-changed"
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"}); err == nil || len(s.deletes) != 0 {
				t.Fatal("private App Service change allowed deletion", err)
			}
		})
	}
}

func TestAppServiceCertificateBindingsAndDefaultHostnames(t *testing.T) {
	for _, mode := range []string{"site-state", "slot-state", "site-binding", "slot-binding", "missing-states", "unknown-tls-state", "missing-thumbprint", "second-pass", "default-hostname"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := appServiceScenario(t)
			target := cdnAsset(t, assets, appCertificateType)
			thumbprint := object(s.records[target.Identity.NativeID]["properties"])["thumbprint"]
			site := cdnAsset(t, assets, appSiteType)
			if strings.HasPrefix(mode, "slot-") {
				site = cdnAsset(t, assets, appSlotType)
			}
			if strings.HasSuffix(mode, "state") {
				object(s.records[site.Identity.NativeID]["properties"])["hostNameSslStates"] = []any{map[string]any{"name": "bound.example.com", "sslState": "SniEnabled", "thumbprint": thumbprint}}
				if mode == "unknown-tls-state" {
					object(array(object(s.records[site.Identity.NativeID]["properties"])["hostNameSslStates"])[0])["sslState"] = "Unknown"
				}
			} else if strings.HasSuffix(mode, "binding") {
				binding := cdnAsset(t, assets, appBindingType)
				if mode == "slot-binding" {
					binding = cdnAsset(t, assets, appSlotBindingType)
				}
				props := object(s.records[binding.Identity.NativeID]["properties"])
				props["sslState"], props["thumbprint"] = "SniEnabled", thumbprint
			} else if mode == "missing-states" {
				delete(object(s.records[site.Identity.NativeID]["properties"]), "hostNameSslStates")
			} else if mode == "missing-thumbprint" {
				delete(object(s.records[target.Identity.NativeID]["properties"]), "thumbprint")
				target = dnsAsset(t, r, s.records[target.Identity.NativeID])
			} else if mode == "default-hostname" {
				target = cdnAsset(t, assets, appBindingType)
				if target.Normalized["cleanup_controller_only"] != true {
					t.Fatal("default binding was independently actionable")
				}
			} else if mode == "second-pass" {
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, "/subscriptions/"+testSubscription+"/providers/Microsoft.Web/sites") {
						reads++
						if reads == 2 {
							object(s.records[site.Identity.NativeID]["properties"])["hostNameSslStates"] = []any{map[string]any{"name": "bound.example.com", "sslState": "SniEnabled", "thumbprint": thumbprint}}
						}
					}
					return nil, false
				}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"}); err == nil || len(s.deletes) != 0 {
				t.Fatal("bound certificate/default hostname deleted", mode, err)
			}
		})
	}
}

func appServiceRecordedResponses(t *testing.T, file string) map[int]map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/appservice/cli-recordings.json")
	var sources []map[string]any
	if err != nil || json.Unmarshal(payload, &sources) != nil || len(sources) != 3 {
		t.Fatal("invalid App Service CLI fixture")
	}
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	json.Unmarshal(payload, &sources)
	for _, source := range sources {
		if source["file"] != file {
			continue
		}
		records := map[int]map[string]any{}
		for _, value := range array(source["recordings"]) {
			row := object(value)
			records[int(row["interaction_index"].(float64))] = row
		}
		return records
	}
	t.Fatal("missing native CLI recording", file)
	return nil
}

func TestAppServiceRecordedTLSBindingsAndCertificateDeletion(t *testing.T) {
	for _, mode := range []string{"unbound", "bound-site", "bound-slot", "space-name"} {
		t.Run(mode, func(t *testing.T) {
			records := appServiceRecordedResponses(t, "test_webapp_ssl.yaml")
			s := newDNSScenario()
			site, slot := object(records[16]["body"]), object(records[50]["body"])
			certRecord, deleteRecord := 17, 42
			if mode == "space-name" {
				certRecord, deleteRecord = 83, 84
			}
			certificate := object(array(object(records[certRecord]["body"])["value"])[0])
			// Certificate and hostname detail bodies are synthesized from native
			// LIST rows. Native site/slot GET and DELETE responses are unchanged.
			for _, raw := range []map[string]any{site, slot, certificate, object(array(object(records[18]["body"])["value"])[0]), object(array(object(records[60]["body"])["value"])[0])} {
				s.add(raw, appServiceVersion)
			}
			siteID, slotID, certID := strings.ToLower(text(site["id"])), strings.ToLower(text(slot["id"])), strings.ToLower(text(certificate["id"]))
			root := "/subscriptions/" + testSubscription
			s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": root + "/resourcegroups/clitest.rg000001", "type": groupType}}
			s.lists[root+"/providers/microsoft.web/sites"] = []any{site}
			s.lists[siteID+"/slots"] = []any{slot}
			s.lists[siteID+"/hostnamebindings"] = array(object(records[18]["body"])["value"])
			s.lists[slotID+"/hostnamebindings"] = array(object(records[60]["body"])["value"])
			s.lists[root+"/providers/microsoft.web/certificates"] = array(object(records[certRecord]["body"])["value"])
			r := s.runtime(t)
			batch, err := r.List(context.Background(), productRequest(r, appCertificateType))
			if err != nil || len(batch.Items) != 1 || batch.Items[0].NativeID != certID {
				t.Fatal("native certificate list", err)
			}
			bindings, err := r.List(context.Background(), productRequest(r, appSlotBindingType))
			if err != nil || len(bindings.Items) != 1 || bindings.Items[0].Normalized["cleanup_controller_only"] != true {
				t.Fatal("native slot binding type alias and omitted TLS fields", err)
			}
			target := dnsAsset(t, r, certificate)
			if mode == "bound-site" {
				s.records[siteID] = object(records[20]["body"])
			} else if mode == "bound-slot" {
				s.records[slotID] = object(records[62]["body"])
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "DELETE" {
					return nil, false
				}
				if !strings.EqualFold(req.URL.Path, certID) || req.URL.Query().Get("api-version") != appServiceVersion {
					t.Fatal("native certificate DELETE binding")
				}
				if mode == "space-name" && !strings.Contains(req.URL.EscapedPath(), "%20") {
					t.Fatal("native certificate space was not URL encoded")
				}
				s.deletes = append(s.deletes, certID)
				return jsonResponse(int(records[deleteRecord]["status"].(float64)), records[deleteRecord]["body"], nil), true
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			request := contracts.ActionRequest{Asset: target, Action: "delete"}
			result, err := driver.Execute(context.Background(), request)
			if strings.HasPrefix(mode, "bound-") {
				if err == nil || len(s.deletes) != 0 {
					t.Fatal("native active TLS binding allowed deletion", err)
				}
				return
			}
			if err != nil || len(s.deletes) != 1 {
				t.Fatal("native unbound certificate DELETE", err)
			}
			wait, err := driver.Wait(context.Background(), request, result)
			if err != nil || wait.Done {
				t.Fatal("recorded DELETE 200 bypassed final absence", err)
			}
			s.gone[certID] = true // The original recording does not include final GET 404.
			wait, err = driver.Wait(context.Background(), request, result)
			if err != nil || !wait.Done {
				t.Fatal("native certificate final readback", err)
			}
		})
	}
}

func TestAppServiceRecordedFunctionAppPlanRetention(t *testing.T) {
	records := appServiceRecordedResponses(t, "test_functionapp_retain_plan.yaml")
	s := newDNSScenario()
	raw := object(records[16]["body"])
	s.add(raw, appServiceVersion)
	id := strings.ToLower(text(raw["id"]))
	root := "/subscriptions/" + testSubscription
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": root + "/resourcegroups/clitest.rg000001", "type": groupType}}
	for _, collection := range []string{"slots", "functions", "certificates", "hostnamebindings"} {
		s.lists[id+"/"+collection] = []any{}
	}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method != "DELETE" {
			return nil, false
		}
		if !strings.EqualFold(req.URL.Path, id) || req.URL.Query().Get("deleteEmptyServerFarm") != "false" || req.URL.Query().Get("api-version") != appServiceVersion {
			t.Fatal("native function app plan-retention request")
		}
		s.deletes = append(s.deletes, id)
		return jsonResponse(int(records[18]["status"].(float64)), records[18]["body"], nil), true
	}
	r := s.runtime(t)
	target := dnsAsset(t, r, raw)
	driver, _ := r.ResolveAction(context.Background(), "connection", target)
	request := contracts.ActionRequest{Asset: target, Action: "delete"}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || len(s.deletes) != 1 {
		t.Fatal("recorded function app deletion", err)
	}
	s.gone[id] = true
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || !wait.Done {
		t.Fatal("function app synthetic final readback", err)
	}
}

func TestAppServiceRecordedSlotRuntimeUpdate(t *testing.T) {
	records := appServiceRecordedResponses(t, "test_functionapp_update_slot.yaml")
	s := newDNSScenario()
	root, slot := object(records[20]["body"]), object(records[23]["body"])
	s.add(root, appServiceVersion)
	s.add(slot, appServiceVersion)
	id := strings.ToLower(text(slot["id"]))
	s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{map[string]any{"id": "/subscriptions/" + testSubscription + "/resourcegroups/clitest.rg000001", "type": groupType}}
	for _, collection := range []string{"functions", "certificates", "hostnamebindings"} {
		s.lists[id+"/"+collection] = []any{}
	}
	r := s.runtime(t)
	target := dnsAsset(t, r, slot)
	s.records[id] = object(records[27]["body"])
	driver, _ := r.ResolveAction(context.Background(), "connection", target)
	check, err := driver.Preflight(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"})
	if err != nil || !check.Allowed {
		t.Fatal("recorded runtime-only slot modification invalidated configuration", err)
	}
}

func TestAppServiceChildPaginationAndRootChanges(t *testing.T) {
	for _, mode := range []string{"complete", "cycle", "version", "foreign", "root-change", "root-between-reads", "wrong-region", "non-function-app", "missing-kind", "list-denied", "partial", "foreign-function-parent"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := appServiceScenario(t)
			target := cdnAsset(t, assets, appSlotFunctionType)
			root := cdnAsset(t, assets, appSiteType)
			slot := cdnAsset(t, assets, appSlotType)
			raw := s.records[target.Identity.NativeID]
			payload, _ := json.Marshal(raw)
			var second map[string]any
			json.Unmarshal(payload, &second)
			second["id"], second["name"] = target.Identity.NativeID+"second", "httpsecond"
			s.add(second, appServiceVersion)
			collection := strings.TrimSuffix(target.Identity.NativeID, "/"+last(target.Identity.NativeID))
			reads := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if !strings.EqualFold(req.URL.Path, collection) {
					return nil, false
				}
				reads++
				if mode == "non-function-app" || mode == "missing-kind" {
					t.Fatal("invalid parent caused a function collection request")
				}
				if mode == "root-between-reads" {
					s.records[root.Identity.NativeID]["tags"] = map[string]any{"changed": "root"}
				}
				if mode == "list-denied" || mode == "partial" {
					status := 403
					if mode == "partial" {
						status = 206
					}
					return jsonResponse(status, map[string]any{"value": []any{}}, nil), true
				}
				if req.URL.Query().Get("$skiptoken") == "second" && mode != "cycle" {
					return jsonResponse(200, map[string]any{"value": []any{second}}, nil), true
				}
				next := apiURL(collection, appServiceVersion) + "&%24skiptoken=second"
				if mode == "version" {
					next = strings.Replace(next, appServiceVersion, "1900-01-01", 1)
				}
				if mode == "foreign" {
					next = strings.Replace(next, "management.azure.com", "untrusted.invalid", 1)
				}
				return jsonResponse(200, map[string]any{"value": []any{raw}, "nextLink": next}, nil), true
			}
			if mode == "non-function-app" {
				s.records[slot.Identity.NativeID]["kind"] = "app,linux"
			}
			if mode == "missing-kind" {
				delete(s.records[slot.Identity.NativeID], "kind")
			}
			if mode == "foreign-function-parent" {
				object(raw["properties"])["function_app_id"] = root.Identity.NativeID + "-other"
			}
			request := productRequest(r, appSlotFunctionType)
			if mode == "wrong-region" {
				request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westus"}
			}
			first, err := r.List(context.Background(), request)
			if slices.Contains([]string{"version", "foreign", "root-between-reads", "missing-kind", "list-denied", "partial", "foreign-function-parent"}, mode) {
				if err == nil {
					t.Fatal("incomplete App Service scan succeeded", mode)
				}
				return
			}
			if mode == "non-function-app" {
				if err != nil || !first.Complete || len(first.Items) != 0 || reads != 0 {
					t.Fatal("ordinary web app function discovery", err)
				}
				return
			}
			if err != nil || first.Complete || first.NextCursor == "" {
				t.Fatal("native first function page", err)
			}
			if mode == "root-change" {
				s.records[root.Identity.NativeID]["tags"] = map[string]any{"changed": "root"}
			}
			request.Cursor = first.NextCursor
			last, err := r.List(context.Background(), request)
			if mode == "cycle" || mode == "root-change" {
				if err == nil {
					t.Fatal("stale/repeated function cursor succeeded")
				}
				return
			}
			count := 2
			if mode == "wrong-region" {
				count = 0
			}
			if err != nil || !last.Complete || len(first.Items)+len(last.Items) != count {
				t.Fatal("native function paging or scope", err)
			}
		})
	}
}

func TestAppServiceSiteCertificateNamingCorrectionIsNarrow(t *testing.T) {
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	for _, opID := range []string{"SiteCertificates_Get", "SiteCertificates_Delete", "SiteCertificates_List", "SiteCertificates_GetSlot", "SiteCertificates_DeleteSlot", "SiteCertificates_ListSlot"} {
		op, _ := metadata.catalog.Operation("Azure.Microsoft.Web." + opID)
		for _, name := range []string{"function-app", "1app", "xn--bcher-kva", "-app", "app-", "app_name", "a/other", "a", "app?query", "a\\b"} {
			t.Run(opID+"/"+name, func(t *testing.T) {
				params := map[string]any{"subscriptionId": testSubscription, "resourceGroupName": "test", "name": name}
				if strings.Contains(op.Call.Path, "{slot}") {
					params["slot"] = "staging"
				}
				if strings.Contains(op.Call.Path, "{certificateName}") {
					params["certificateName"] = "cert"
				}
				_, err := bindAzureREST(op, params)
				valid := slices.Contains([]string{"function-app", "1app", "xn--bcher-kva"}, name)
				if (err == nil) != valid {
					t.Fatal("incorrect documented site-name handling", name, err)
				}
				if object(object(op.InputSchema["properties"])["name"])["pattern"] != "^[A-z][A-z0-9]*$" {
					t.Fatal("runtime mutated native source schema")
				}
			})
		}
	}
}

func TestAppServiceCascadeCannotHideChangedOrUnreviewedChildren(t *testing.T) {
	for _, mode := range []string{"missing-inventory", "new-function", "changed-file", "root-recreated", "child-list-denied"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := appServiceScenario(t)
			root := cdnAsset(t, assets, appSiteType)
			child := cdnAsset(t, assets, appSlotFunctionType)
			request, _ := dnsRequest(t, r, assets, root)
			switch mode {
			case "missing-inventory":
				request.LifecycleImpacts = request.LifecycleImpacts[1:]
			case "new-function":
				payload, _ := json.Marshal(s.records[child.Identity.NativeID])
				var raw map[string]any
				json.Unmarshal(payload, &raw)
				raw["id"] = child.Identity.NativeID + "new"
				s.add(raw, appServiceVersion)
				collection := strings.TrimSuffix(child.Identity.NativeID, "/"+last(child.Identity.NativeID))
				s.lists[collection] = append(s.lists[collection], raw)
			case "changed-file":
				object(s.records[child.Identity.NativeID]["properties"])["files"] = map[string]any{"index.js": "changed-private-source"}
			case "root-recreated":
				s.records[root.Identity.NativeID]["systemData"] = map[string]any{"createdAt": "2026-09-10T01:00:00Z"}
			case "child-list-denied":
				s.status[strings.TrimSuffix(child.Identity.NativeID, "/"+last(child.Identity.NativeID))] = 403
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", root)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("incomplete app cascade authorized deletion", mode, err)
			}
		})
	}
}

func TestAppServiceNativeDeleteFailuresAndPersistedOperations(t *testing.T) {
	for _, mode := range []string{"read-only-package", "forbidden", "conflict", "partial", "async", "failed-operation", "wrong-receipt"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := appServiceScenario(t)
			target := cdnAsset(t, assets, appFunctionType)
			operation := "/subscriptions/" + testSubscription + "/providers/Microsoft.Web/locations/eastus/operations/deleting-function"
			state := "InProgress"
			polls := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					s.deletes = append(s.deletes, target.Identity.NativeID)
					status := map[string]int{"read-only-package": 400, "forbidden": 403, "conflict": 409, "partial": 206}[mode]
					if status != 0 {
						return jsonResponse(status, map[string]any{"error": map[string]any{"code": "OperationRejected", "message": "Function deletion rejected"}}, nil), true
					}
					return jsonResponse(202, nil, http.Header{"Azure-Asyncoperation": {apiURL(operation, appServiceVersion)}}), true
				}
				if strings.EqualFold(req.URL.Path, operation) {
					polls++
					return jsonResponse(200, map[string]any{"status": state}, nil), true
				}
				return nil, false
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			request := contracts.ActionRequest{Asset: target, Action: "delete"}
			result, err := driver.Execute(context.Background(), request)
			if slices.Contains([]string{"read-only-package", "forbidden", "conflict", "partial"}, mode) {
				if err == nil || len(s.deletes) != 1 {
					t.Fatal("native delete rejection hidden", err)
				}
				return
			}
			if err != nil {
				t.Fatal("App Service injected async delete", err)
			}
			payload, _ := json.Marshal(request)
			json.Unmarshal(payload, &request)
			payload, _ = json.Marshal(result)
			json.Unmarshal(payload, &result)
			driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
			if mode == "failed-operation" {
				state = "Failed"
			} else if mode == "wrong-receipt" {
				result.Data["app_service_operation_binding"] = "wrong"
			}
			wait, err := driver.Wait(context.Background(), request, result)
			if mode != "async" {
				if err == nil || wait.Done || (mode == "wrong-receipt" && polls != 0) {
					t.Fatal("unsafe App Service operation", err)
				}
				return
			}
			if err != nil || wait.Done {
				t.Fatal("pending App Service operation", err)
			}
			state = "Succeeded"
			wait, err = driver.Wait(context.Background(), request, result)
			if err != nil || wait.Done {
				t.Fatal("operation success hid live function", err)
			}
			s.gone[target.Identity.NativeID] = true
			wait, err = driver.Wait(context.Background(), request, result)
			if err != nil || !wait.Done {
				t.Fatal("persisted operation final absence", err)
			}
		})
	}
}
