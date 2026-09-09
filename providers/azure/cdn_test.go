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
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const cdnVersion = "2025-04-15"

func cdnNativeVersion(kind string) string {
	if strings.EqualFold(kind, afdRuleSetType) {
		return "2025-12-01"
	}
	return cdnVersion
}

func cdnExample(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/cdn/" + name + ".json")
	var value map[string]any
	if err != nil || json.Unmarshal(payload, &value) != nil {
		t.Fatal("invalid CDN example", err)
	}
	return value
}

func TestCDNNativeExamplesAndSchemas(t *testing.T) {
	payload, err := os.ReadFile("fixtures/cdn/sources.json")
	var manifest []map[string]string
	if err != nil || json.Unmarshal(payload, &manifest) != nil || len(manifest) != 42 {
		t.Fatal("invalid CDN source manifest")
	}
	payload, err = os.ReadFile("fixtures/cdn/batch-sources.json")
	var batchManifest []map[string]string
	if err != nil || json.Unmarshal(payload, &batchManifest) != nil || len(batchManifest) != 3 {
		t.Fatal("invalid CDN batch source manifest")
	}
	manifest = append(manifest, batchManifest...)
	for _, entry := range manifest {
		payload, err := os.ReadFile("fixtures/cdn/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] {
			t.Fatal("modified native CDN example")
		}
	}
	payload, _ = os.ReadFile("catalog/source/swagger.json")
	var set catalog.RESTDocumentSet
	json.Unmarshal(payload, &set)
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	for _, document := range set.Documents {
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(document.Document))
		if err := compiler.AddResource(document.SourceURI, value); err != nil {
			t.Fatal(err)
		}
	}
	checked := 0
	for _, document := range set.Documents {
		if !strings.Contains(document.SourceURI, "/Cdn/stable/") {
			continue
		}
		var native map[string]any
		json.Unmarshal(document.Document, &native)
		for path, value := range object(native["paths"]) {
			for method, value := range object(value) {
				op := object(value)
				if object(object(op["responses"])["200"])["schema"] == nil {
					continue
				}
				schema, err := compiler.Compile(document.SourceURI + "#/paths/" + strings.ReplaceAll(path, "/", "~1") + "/" + method + "/responses/200/schema")
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(document.SourceURI, "/2025-12-01/") && op["operationId"] == "RuleSets_Get" {
					s, _, assets := cdnBatchScenario(t)
					raw := s.records[cdnAsset(t, assets, afdRuleSetType).Identity.NativeID]
					payload, _ := json.Marshal(raw)
					value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
					if err := schema.Validate(value); err != nil {
						t.Fatalf("synthetic batch response differs from independent native schema: %v", err)
					}
					delete(object(array(object(raw["properties"])["rules"])[0]), "ruleName")
					payload, _ = json.Marshal(raw)
					value, _ = jsonschema.UnmarshalJSON(bytes.NewReader(payload))
					if err := schema.Validate(value); err == nil {
						t.Fatal("native batch schema accepted a rule without its required name")
					}
				}
				for _, reference := range object(op["x-ms-examples"]) {
					name := strings.TrimSuffix(last(text(object(reference)["$ref"])), ".json")
					file := name
					if strings.Contains(document.SourceURI, "/2025-12-01/") {
						file = "2025-12-01/" + name
					}
					example := cdnExample(t, file)
					body := object(object(example["responses"])["200"])["body"]
					payload, _ := json.Marshal(body)
					value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
					originalError := schema.Validate(value)
					invalidFields := map[string][]string{
						"Origins":       {"httpPort", "httpsPort"},
						"AFDOrigins":    {"priority", "weight", "sharedPrivateLinkResource"},
						"Routes":        {"originPath", "cacheConfiguration.queryParameters"},
						"Endpoints":     {"originPath", "customDomains.0.properties.validationData"},
						"CustomDomains": {"validationData", "provisioningState", "customHttpsProvisioningSubstate"},
					}[strings.Split(name, "_")[0]]
					if len(invalidFields) == 0 {
						if originalError != nil {
							t.Fatalf("native %s: %v", name, originalError)
						}
					} else {
						if originalError == nil {
							t.Fatalf("documented native schema inconsistency changed: %s", name)
						}
						// These examples explicitly set optional typed fields to null;
						// classic domains also use two undeclared enum values. Assert
						// the original failure, then omit only those fields in a copy.
						rows := []any{body}
						if listed, ok := object(body)["value"].([]any); ok {
							rows = listed
						}
						for _, row := range rows {
							for _, field := range invalidFields {
								var parent any = object(row)["properties"]
								parts := strings.Split(field, ".")
								for _, part := range parts[:len(parts)-1] {
									if part == "0" {
										parent = array(parent)[0]
									} else {
										parent = object(parent)[part]
									}
								}
								delete(object(parent), parts[len(parts)-1])
							}
						}
						payload, _ := json.Marshal(body)
						value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
						if err := schema.Validate(value); err != nil {
							t.Fatalf("unexpected native inconsistency %s: %v", name, err)
						}
					}
					checked++
				}
			}
		}
	}
	if checked != 28 {
		t.Fatalf("validated %d native response bodies", checked)
	}
}

// Original examples are independent resources. Bind their documented IDs into
// two internally consistent fixture profiles; preserve the original files.
func cdnScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": root + "/resourcegroups/test", "type": groupType}}
	var raws []map[string]any
	groups := []string{"Profiles", "Endpoints", "Origins", "OriginGroups", "CustomDomains", "AFDEndpoints", "AFDCustomDomains", "AFDOriginGroups", "AFDOrigins", "Routes", "RuleSets", "Rules", "SecurityPolicies", "Secrets"}
	ids := map[string]string{}
	for _, group := range groups {
		example := cdnExample(t, group+"_Get")
		raw := object(object(object(example["responses"])["200"])["body"])
		payload, _ := json.Marshal(raw)
		value := strings.ReplaceAll(string(payload), "/subscriptions/subid/resourceGroups/RG/", root+"/resourcegroups/test/")
		value = strings.ReplaceAll(value, "/subscriptions/subid/resourcegroups/RG/", root+"/resourcegroups/test/")
		json.Unmarshal([]byte(value), &raw)
		if group == "Profiles" {
			raw["id"] = root + "/resourcegroups/test/providers/Microsoft.Cdn/profiles/afd"
		} else {
			family := "afd"
			if slices.Contains([]string{"Endpoints", "Origins", "OriginGroups", "CustomDomains"}, group) {
				family = "classic"
			}
			payload, _ = json.Marshal(raw)
			json.Unmarshal([]byte(strings.ReplaceAll(string(payload), "/profiles/profile1/", "/profiles/"+family+"/")), &raw)
		}
		raw["name"] = last(text(raw["id"]))
		ids[group] = strings.ToLower(text(raw["id"]))
		raws = append(raws, raw)
	}
	profile := cdnExample(t, "Profiles_Get")
	classic := object(object(object(profile["responses"])["200"])["body"])
	classic["id"], classic["name"], classic["sku"] = root+"/resourcegroups/test/providers/Microsoft.Cdn/profiles/classic", "classic", map[string]any{"name": "Standard_Microsoft"}
	delete(classic, "kind")
	delete(object(classic["properties"]), "frontDoorId")
	raws = append(raws, classic)
	lookup := func(group string) map[string]any {
		for _, raw := range raws {
			if strings.EqualFold(text(raw["id"]), ids[group]) {
				return object(raw["properties"])
			}
		}
		t.Fatal(group)
		return nil
	}
	lookup("Routes")["customDomains"] = []any{map[string]any{"id": ids["AFDCustomDomains"]}}
	lookup("Routes")["originGroup"] = map[string]any{"id": ids["AFDOriginGroups"]}
	lookup("Routes")["ruleSets"] = []any{map[string]any{"id": ids["RuleSets"]}}
	lookup("AFDCustomDomains")["tlsSettings"] = map[string]any{"certificateType": "CustomerCertificate", "secret": map[string]any{"id": ids["Secrets"]}}
	object(lookup("SecurityPolicies")["parameters"])["associations"] = []any{map[string]any{"domains": []any{map[string]any{"id": ids["AFDCustomDomains"]}, map[string]any{"id": ids["AFDEndpoints"]}}, "patternsToMatch": []any{"/*"}}}
	lookup("Rules")["actions"] = []any{map[string]any{"name": "RouteConfigurationOverride", "parameters": map[string]any{"typeName": "DeliveryRuleRouteConfigurationOverrideActionParameters", "originGroupOverride": map[string]any{"originGroup": map[string]any{"id": ids["AFDOriginGroups"]}}}}, map[string]any{"name": "ModifyRequestHeader", "parameters": map[string]any{"typeName": "DeliveryRuleHeaderActionParameters", "headerName": "X-Innocent", "headerAction": "Overwrite", "value": "secret-rule-value"}}}
	// The independent origin example has a different name from the deep-create
	// example. Reconcile only that identity; native child GET config stays intact.
	lookup("Endpoints")["origins"] = []any{map[string]any{"name": last(ids["Origins"]), "properties": lookup("Origins")}}
	lookup("OriginGroups")["origins"] = []any{map[string]any{"id": ids["Origins"]}}
	lookup("Endpoints")["originGroups"] = []any{map[string]any{"name": last(ids["OriginGroups"]), "properties": lookup("OriginGroups")}}
	lookup("Endpoints")["customDomains"] = []any{map[string]any{"name": last(ids["CustomDomains"]), "properties": lookup("CustomDomains")}}
	lookup("Endpoints")["defaultOriginGroup"] = map[string]any{"id": ids["OriginGroups"]}
	for _, raw := range raws {
		id, kind, _ := parseID(text(raw["id"]))
		s.add(raw, cdnNativeVersion(kind))
		canonical, _ := findType(kind)
		collection := id[:strings.LastIndex(id, "/")]
		if canonical.NativeType == cdnProfileType {
			collection = root + "/providers/microsoft.cdn/profiles"
		}
		s.lists[collection] = append(s.lists[collection], raw)
		s.version[collection] = cdnNativeVersion(kind)
		for _, childKind := range serviceChildKinds(canonical.NativeType) {
			if canonical.NativeType == cdnProfileType {
				applies, err := cdnChildApplies(childKind, raw)
				if err != nil {
					t.Fatal(err)
				}
				if !applies {
					continue
				}
			}
			list := id + "/" + strings.ToLower(last(childKind))
			if _, ok := s.lists[list]; !ok {
				s.lists[list] = []any{}
			}
			s.version[list] = cdnNativeVersion(childKind)
		}
	}
	r := s.runtime(t)
	assets := []asset.Asset{}
	for _, raw := range raws {
		assets = append(assets, dnsAsset(t, r, raw))
	}
	return s, r, assets
}

func cdnAsset(t *testing.T, assets []asset.Asset, kind string) asset.Asset {
	t.Helper()
	for _, value := range assets {
		if value.Identity.NativeType == kind {
			return value
		}
	}
	t.Fatal(kind)
	return asset.Asset{}
}

func TestCDNProductInventoryUsesNativeFamiliesAndReferences(t *testing.T) {
	_, r, assets := cdnScenario(t)
	seen := map[string]bool{}
	for _, value := range assets {
		if seen[value.Identity.NativeType] {
			continue
		}
		seen[value.Identity.NativeType] = true
		request := productRequest(r, value.Identity.NativeType)
		items := []contracts.InventoryItem{}
		for {
			batch, err := r.List(context.Background(), request)
			if err != nil {
				t.Fatalf("%s inventory: %v", value.Identity.NativeType, err)
			}
			items = append(items, batch.Items...)
			if batch.Complete {
				break
			}
			request.Cursor = batch.NextCursor
		}
		expected := 1
		if value.Identity.NativeType == cdnProfileType {
			expected = 2
		}
		if len(items) != expected {
			t.Fatalf("%s: %d items", value.Identity.NativeType, len(items))
		}
		payload, _ := json.Marshal(items)
		if strings.Contains(string(payload), "secret-rule-value") {
			t.Fatal("persisted rule secret")
		}
		if value.Identity.NativeType == afdRouteType {
			for _, kind := range []string{afdEndpointType, afdDomainType, afdOriginGroupType, afdRuleSetType} {
				if len(items[0].Normalized[referenceKey(kind)].([]string)) != 1 {
					t.Fatal("missing route reference", kind)
				}
			}
		}
		if value.Identity.NativeType == afdDomainType && len(items[0].Normalized[referenceKey(afdSecretType)].([]string)) != 1 {
			t.Fatal("missing sanitized TLS secret reference")
		}
	}
}

func TestCDNProfileCascadePlansEveryNativeChildAndVerifiesAbsence(t *testing.T) {
	for _, family := range []string{"afd", "classic"} {
		t.Run(family, func(t *testing.T) {
			s, r, assets := cdnScenario(t)
			var root asset.Asset
			for _, value := range assets {
				if value.Identity.NativeType == cdnProfileType && last(value.Identity.NativeID) == family {
					root = value
				}
			}
			request, input := dnsRequest(t, r, assets, root)
			solved, err := plan.Solve(input)
			count := 9
			if family == "classic" {
				count = 4
			}
			if err != nil || len(request.LifecycleImpacts) != count || len(solved.Steps) != 1 {
				t.Fatalf("CDN cascade: %+v %v", solved, err)
			}
			input.RequestOptions = map[asset.AssetID]map[string]any{root.ID: {"retain_resources": []string{request.LifecycleImpacts[0].Asset.Identity.NativeID}}}
			retained, err := plan.Solve(input)
			if err != nil || len(retained.Blockers) == 0 {
				t.Fatal("retained CDN child was cascaded", err)
			}
			driver, err := r.ResolveAction(context.Background(), "connection", root)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || len(s.deletes) != 1 || s.deletes[0] != root.Identity.NativeID {
				t.Fatal("profile delete", err, s.deletes)
			}
			payload, _ := json.Marshal(request)
			json.Unmarshal(payload, &request)
			payload, _ = json.Marshal(result)
			json.Unmarshal(payload, &result)
			driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
			wait, err := driver.Wait(context.Background(), request, result)
			if err != nil || wait.Done {
				t.Fatal("profile 404 hid surviving descendants", err)
			}
			for _, impact := range request.LifecycleImpacts {
				s.gone[impact.Asset.Identity.NativeID] = true
			}
			wait, err = driver.Wait(context.Background(), request, result)
			if err != nil || !wait.Done {
				t.Fatal("profile descendants not reconciled", err)
			}
			if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != 1 {
				t.Fatal("absent profile repeated DELETE", err)
			}
		})
	}
}

func TestCDNSharedReferencesAreIndependentReviewedPrerequisites(t *testing.T) {
	for _, kind := range []string{afdDomainType, afdEndpointType, afdOriginGroupType, afdRuleSetType, afdSecretType, cdnOriginType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := cdnScenario(t)
			if kind == cdnOriginType {
				endpoint := cdnAsset(t, assets, cdnEndpointType)
				delete(object(s.records[endpoint.Identity.NativeID]["properties"]), "defaultOriginGroup")
				for i := range assets {
					if assets[i].ID == endpoint.ID {
						assets[i] = dnsAsset(t, r, s.records[endpoint.Identity.NativeID])
					}
				}
			}
			target := cdnAsset(t, assets, kind)
			request, input := dnsRequest(t, r, assets, target)
			solved, err := plan.Solve(input)
			if err != nil || len(request.PrerequisiteDeletions) == 0 {
				t.Fatalf("missing reviewed CDN prerequisites: %+v %v", solved, err)
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("referenced CDN target deleted")
			}
			for _, step := range solved.Steps {
				var selected asset.Asset
				for _, value := range assets {
					if value.ID == step.AssetID {
						selected = value
					}
				}
				stepRequest := servicePlanRequest(solved, assets, selected)
				native, _ := r.ResolveAction(context.Background(), "connection", selected)
				result, err := native.Execute(context.Background(), stepRequest)
				if err != nil {
					t.Fatalf("native step %s: %v", selected.Identity.NativeType, err)
				}
				for _, impact := range stepRequest.LifecycleImpacts {
					s.gone[impact.Asset.Identity.NativeID] = true
				}
				wait, err := native.Wait(context.Background(), stepRequest, result)
				if err != nil || !wait.Done {
					t.Fatalf("native step readback %s: %v", selected.Identity.NativeType, err)
				}
			}
			if len(s.deletes) != len(solved.Steps) || s.deletes[len(s.deletes)-1] != target.Identity.NativeID {
				t.Fatal("wrong prerequisite order", s.deletes)
			}
		})
	}
}

func TestCDNConfigurationAndReferenceFailuresPreventWrites(t *testing.T) {
	for _, mode := range []string{"sku", "profile", "private", "missing-private", "missing-configuration", "creation", "new-route", "changed-route", "missing-impact", "retained-impact", "lock", "protected-child", "denied-list", "partial-list", "missing-array", "duplicate-child", "cross-profile", "unknown-sku", "migration", "runtime"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cdnScenario(t)
			root := cdnAsset(t, assets, cdnProfileType)
			request, _ := dnsRequest(t, r, assets, root)
			route := cdnAsset(t, assets, afdRouteType)
			rule := cdnAsset(t, assets, afdRuleType)
			list := strings.TrimSuffix(route.Identity.NativeID, "/"+last(route.Identity.NativeID))
			switch mode {
			case "sku":
				s.records[root.Identity.NativeID]["sku"] = map[string]any{"name": "Standard_Microsoft"}
			case "profile":
				object(s.records[root.Identity.NativeID]["properties"])["originResponseTimeoutSeconds"] = 90
			case "private":
				object(object(array(object(s.records[rule.Identity.NativeID]["properties"])["actions"])[1])["parameters"])["value"] = "changed-secret"
			case "missing-private":
				delete(request.Asset.Normalized, "_cdn_private_configuration")
			case "missing-configuration":
				delete(request.Asset.Normalized, "_cdn_configuration")
			case "creation":
				s.records[root.Identity.NativeID]["systemData"] = map[string]any{"createdAt": "new-incarnation"}
			case "new-route":
				raw := map[string]any{"id": route.Identity.NativeID + "-new", "properties": map[string]any{"originGroup": map[string]any{"id": cdnAsset(t, assets, afdOriginGroupType).Identity.NativeID}}}
				s.add(raw, cdnVersion)
				s.lists[list] = append(s.lists[list], raw)
			case "changed-route":
				object(s.records[route.Identity.NativeID]["properties"])["enabledState"] = "Disabled"
			case "missing-impact":
				request.LifecycleImpacts = request.LifecycleImpacts[1:]
			case "retained-impact":
				request.LifecycleImpacts[0].Delete = false
			case "lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": route.Identity.NativeID + "/providers/Microsoft.Authorization/locks/protect", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "protected-child":
				s.records[route.Identity.NativeID]["tags"] = map[string]any{"steward/protected": "true"}
			case "denied-list":
				s.status[list] = 403
			case "partial-list", "missing-array":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, list) {
						status := 200
						body := map[string]any{}
						if mode == "partial-list" {
							status = 206
							body["value"] = []any{}
						}
						return jsonResponse(status, body, nil), true
					}
					return nil, false
				}
			case "duplicate-child":
				s.lists[list] = append(s.lists[list], s.lists[list][0])
			case "cross-profile":
				object(s.records[route.Identity.NativeID]["properties"])["originGroup"] = map[string]any{"id": strings.Replace(cdnAsset(t, assets, afdOriginGroupType).Identity.NativeID, "/profiles/afd/", "/profiles/foreign/", 1)}
			case "unknown-sku":
				s.records[root.Identity.NativeID]["sku"] = map[string]any{"name": "Unknown_New_Sku"}
			case "migration":
				object(s.records[root.Identity.NativeID]["properties"])["resourceState"] = "Migrating"
			case "runtime":
				for _, raw := range s.records {
					properties := object(raw["properties"])
					properties["provisioningState"] = "Updating"
					properties["deploymentStatus"] = "InProgress"
				}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
			_, err := driver.Execute(context.Background(), request)
			if mode == "runtime" {
				if err != nil || len(s.deletes) != 1 {
					t.Fatal("runtime-only changes blocked", err)
				}
				return
			}
			if err == nil || len(s.deletes) != 0 {
				t.Fatal("CDN protection failure", mode, err, s.deletes)
			}
		})
	}
}

func TestCDNRecordedNativeDeletesAndSignedOperationRecovery(t *testing.T) {
	for _, tc := range []struct {
		source, read, parent, list, del, pending, success int
		kind                                              string
	}{
		{0, 28, -1, 7, 35, 36, 69, cdnProfileType},
		{1, 12, 6, 11, 13, 14, 16, afdRuleSetType},
		{2, 8, 5, -1, 9, -1, -1, afdSecretType},
	} {
		modes := []string{"complete"}
		if tc.kind == afdRuleSetType {
			modes = append(modes, "failed", "error-detail", "partial", "expired", "foreign", "rebound", "location", "operation-location")
		}
		for _, mode := range modes {
			t.Run(tc.kind+"/"+mode, func(t *testing.T) {
				payload, err := os.ReadFile("fixtures/cdn/cli-recordings.json")
				var sources []map[string]any
				if err != nil || json.Unmarshal(payload, &sources) != nil || len(sources) != 3 {
					t.Fatal("invalid CDN CLI responses")
				}
				payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
				json.Unmarshal(payload, &sources)
				records := map[int]map[string]any{}
				for _, row := range array(sources[tc.source]["recordings"]) {
					record := object(row)
					records[int(record["interaction_index"].(float64))] = record
				}
				read := object(records[tc.read]["body"])
				s := newDNSScenario()
				s.add(read, cdnNativeVersion(tc.kind))
				id := strings.ToLower(text(read["id"]))
				group := strings.Join(strings.Split(id, "/")[:5], "/")
				profile := read
				if tc.parent >= 0 {
					profile = object(records[tc.parent]["body"])
					s.add(profile, cdnVersion)
				}
				profileID := strings.ToLower(text(profile["id"]))
				s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.cdn/profiles"] = []any{profile}
				for _, kind := range serviceChildKinds(cdnProfileType) {
					if applies, _ := cdnChildApplies(kind, profile); applies {
						s.lists[profileID+"/"+strings.ToLower(last(kind))] = []any{}
					}
				}
				collection := id[:strings.LastIndex(id, "/")]
				if tc.kind == cdnProfileType {
					collection = "/subscriptions/" + testSubscription + "/providers/microsoft.cdn/profiles"
				}
				s.lists[collection] = []any{read}
				if tc.list >= 0 {
					s.lists[collection] = array(object(records[tc.list]["body"])["value"])
				}
				r := s.runtime(t)
				batch, err := r.List(context.Background(), productRequest(r, tc.kind))
				if err != nil || len(batch.Items) != 1 {
					t.Fatalf("recorded inventory: items=%d %v", len(batch.Items), err)
				}
				if tc.kind == afdRuleSetType {
					if batch.Items[0].Normalized["batchMode"] != true || len(array(batch.Items[0].Normalized["rules"])) != 1 {
						t.Fatal("native batch-mode detail lost its embedded rules")
					}
					if rules, err := r.List(context.Background(), productRequest(r, afdRuleType)); err != nil || !rules.Complete || len(rules.Items) != 0 {
						t.Fatal("native batch mode invented independent rules", err)
					}
				}
				root := dnsAsset(t, r, read)
				request := contracts.ActionRequest{Asset: root, Action: "delete", IdempotencyKey: "cdn-recorded-delete"}
				deletion := records[tc.del]
				headers := http.Header{}
				for key, values := range object(deletion["headers"]) {
					for _, value := range array(values) {
						headers.Add(key, text(value))
					}
				}
				operation := headers.Get("Azure-AsyncOperation")
				if mode == "foreign" {
					operation = strings.Replace(operation, testSubscription, testTenant, 1)
					headers.Set("Azure-AsyncOperation", operation)
				}
				if mode == "location" {
					operation = headers.Get("Location")
					headers.Del("Azure-AsyncOperation")
				}
				if mode == "operation-location" {
					headers.Set("Operation-Location", operation)
					headers.Del("Azure-AsyncOperation")
					headers.Del("Location")
				}
				polls, deletes := 0, 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "DELETE" {
						if !strings.EqualFold(req.URL.Path, id) || req.URL.Query().Get("api-version") != cdnNativeVersion(tc.kind) || req.Header.Get("x-ms-client-request-id") != azureRequestID(request.IdempotencyKey) {
							t.Fatal("recorded delete binding changed")
						}
						deletes++
						return jsonResponse(int(deletion["status"].(float64)), object(deletion["body"]), headers), true
					}
					if operation != "" && req.URL.String() == operation {
						polls++
						if mode == "expired" {
							return jsonResponse(404, map[string]any{}, nil), true
						}
						body := object(records[tc.pending]["body"])
						if polls > 1 {
							body = object(records[tc.success]["body"])
						}
						if mode == "failed" {
							body = map[string]any{"status": "Failed", "error": map[string]any{"code": "None", "message": nil}}
						}
						if mode == "error-detail" {
							body = map[string]any{"status": "Succeeded", "error": map[string]any{"code": "None", "message": "still-an-error"}}
						}
						status := 200
						if mode == "partial" {
							status = 206
						}
						return jsonResponse(status, body, nil), true
					}
					return nil, false
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", root)
				result, err := driver.Execute(context.Background(), request)
				if mode == "foreign" {
					if err == nil || polls != 0 {
						t.Fatal("foreign operation accepted")
					}
					return
				}
				if err != nil || deletes != 1 {
					t.Fatal("native CDN DELETE", err)
				}
				payload, _ = json.Marshal(result)
				json.Unmarshal(payload, &result)
				payload, _ = json.Marshal(request)
				json.Unmarshal(payload, &request)
				if mode == "rebound" {
					result.Data["cdn_operation_binding"] = "another-resource"
				}
				driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
				wait, err := driver.Wait(context.Background(), request, result)
				if slices.Contains([]string{"failed", "error-detail", "partial", "rebound"}, mode) {
					if err == nil || wait.Done {
						t.Fatal("native CDN polling error ignored")
					}
					return
				}
				if err != nil || wait.Done {
					t.Fatal("pending operation or surviving resource disappeared", err)
				}
				if operation != "" && mode != "expired" {
					wait, err = driver.Wait(context.Background(), request, result)
					if err != nil || wait.Done {
						t.Fatal("native Succeeded hid a surviving resource", err)
					}
				}
				s.gone[id] = true
				wait, err = driver.Wait(context.Background(), request, result)
				if err != nil || !wait.Done || deletes != 1 {
					t.Fatal("persisted native CDN deletion not reconciled", err)
				}
			})
		}
	}
}

func TestCDNDefaultOriginGroupRequiresEndpointCleanup(t *testing.T) {
	s, r, assets := cdnScenario(t)
	group := cdnAsset(t, assets, cdnOriginGroupType)
	driver, _ := r.ResolveAction(context.Background(), "connection", group)
	if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: group, Action: "delete"}); err == nil || len(s.deletes) != 0 {
		t.Fatal("default origin group removed through its independent API")
	}
	endpoint := cdnAsset(t, assets, cdnEndpointType)
	request, _ := dnsRequest(t, r, assets, endpoint)
	driver, _ = r.ResolveAction(context.Background(), "connection", endpoint)
	if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != 1 || s.deletes[0] != endpoint.Identity.NativeID {
		t.Fatal("reviewed endpoint cascade failed", err)
	}
}

func TestCDNPaginationAndParentChangesNeverProveFalseAbsence(t *testing.T) {
	for _, mode := range []string{"complete", "cycle", "version", "foreign", "changed-parent", "changed-second-read", "missing-embedded-child", "wrong-location"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cdnScenario(t)
			root := cdnAsset(t, assets, cdnProfileType)
			route := cdnAsset(t, assets, afdRouteType)
			collection := route.Identity.NativeID[:strings.LastIndex(route.Identity.NativeID, "/")]
			payload, _ := json.Marshal(s.records[route.Identity.NativeID])
			var second map[string]any
			json.Unmarshal(payload, &second)
			second["id"], second["name"] = route.Identity.NativeID+"-second", "route1-second"
			s.add(second, cdnVersion)
			reads := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.EqualFold(req.URL.Path, collection) {
					reads++
					if mode == "changed-second-read" {
						rows := []any{s.records[route.Identity.NativeID]}
						if reads > 1 {
							rows = append(rows, second)
						}
						return jsonResponse(200, map[string]any{"value": rows}, nil), true
					}
					if req.URL.Query().Get("$skiptoken") == "second" && mode != "cycle" {
						return jsonResponse(200, map[string]any{"value": []any{second}}, nil), true
					}
					next := apiURL(collection, cdnVersion) + "&%24skiptoken=second"
					if mode == "version" {
						next = strings.Replace(next, cdnVersion, "1900-01-01", 1)
					}
					if mode == "foreign" {
						next = strings.Replace(next, "management.azure.com", "untrusted.invalid", 1)
					}
					return jsonResponse(200, map[string]any{"value": []any{s.records[route.Identity.NativeID]}, "nextLink": next}, nil), true
				}
				return nil, false
			}
			if mode == "changed-second-read" {
				contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
				if _, err := contributor.Contribute(context.Background(), "scope", assets); err == nil {
					t.Fatal("changed native reference set accepted")
				}
				return
			}
			if mode == "missing-embedded-child" {
				s.handle = nil
				endpoint := cdnAsset(t, assets, cdnEndpointType)
				s.lists[endpoint.Identity.NativeID+"/origins"] = []any{}
				contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
				if _, err := contributor.Contribute(context.Background(), "scope", assets); err == nil {
					t.Fatal("inline origins contradicted complete native child list")
				}
				return
			}
			request := productRequest(r, afdRouteType)
			if mode == "wrong-location" {
				request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}
			}
			first, err := r.List(context.Background(), request)
			if mode == "version" || mode == "foreign" {
				if err == nil {
					t.Fatal("unsafe native continuation accepted")
				}
				return
			}
			if err != nil || first.Complete || first.NextCursor == "" {
				t.Fatal("first native page", err)
			}
			if mode == "changed-parent" {
				object(s.records[root.Identity.NativeID]["properties"])["originResponseTimeoutSeconds"] = 99
			}
			request.Cursor = first.NextCursor
			last, err := r.List(context.Background(), request)
			if mode == "cycle" || mode == "changed-parent" {
				if err == nil {
					t.Fatal("stale/repeated CDN cursor accepted")
				}
				return
			}
			if err != nil || !last.Complete {
				t.Fatal("native second page", err)
			}
			if mode == "wrong-location" {
				if len(first.Items)+len(last.Items) != 0 {
					t.Fatal("global proxy escaped regional filter")
				}
			} else if len(first.Items) != 1 || len(last.Items) != 1 {
				t.Fatal("native page dropped route")
			}
		})
	}
}

func TestCDNSensitiveRuleConditionsAndActionsAreNotPersisted(t *testing.T) {
	s, r, assets := cdnScenario(t)
	rule := cdnAsset(t, assets, afdRuleType)
	raw := s.records[rule.Identity.NativeID]
	object(raw["properties"])["conditions"] = []any{map[string]any{"name": "RequestHeader", "parameters": map[string]any{"typeName": "DeliveryRuleRequestHeaderConditionParameters", "selector": "X-Innocent", "operator": "Equal", "matchValues": []any{"secret-condition-value"}}}}
	batch, err := r.List(context.Background(), productRequest(r, afdRuleType))
	if err != nil || len(batch.Items) != 1 {
		t.Fatal("native sensitive rule discovery", err)
	}
	payload, _ := json.Marshal(batch)
	if strings.Contains(string(payload), "secret-condition-value") || strings.Contains(string(payload), "secret-rule-value") {
		t.Fatal("rule secrets persisted")
	}
	if len(array(object(object(array(object(raw["properties"])["conditions"])[0])["parameters"])["matchValues"])) != 1 {
		t.Fatal("source response mutated")
	}
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	c, _ := r.resolve(ctx, "connection")
	kind, _ := findType(afdRuleType)
	endpoint, _ := c.resourceURL(kind, rule.Identity.NativeID)
	if _, err := c.request(ctx, "GET", endpoint); err != nil {
		t.Fatal(err)
	}
	payload, _ = json.Marshal(logs)
	if len(logs) != 2 || strings.Contains(string(payload), "secret-condition-value") || strings.Contains(string(payload), "secret-rule-value") {
		t.Fatal("rule secrets escaped API diagnostics")
	}
	fresh := dnsAsset(t, r, raw)
	object(object(array(object(raw["properties"])["conditions"])[0])["parameters"])["matchValues"] = []any{"changed-private-condition"}
	driver, _ := r.ResolveAction(ctx, "connection", fresh)
	if _, err := driver.Execute(ctx, contracts.ActionRequest{Asset: fresh, Action: "delete"}); err == nil || len(s.deletes) != 0 {
		t.Fatal("private condition drift did not invalidate deletion")
	}
}

func TestCDNSharedReferenceDiscoveryScalesByCollection(t *testing.T) {
	s, r, assets := cdnScenario(t)
	route := cdnAsset(t, assets, afdRouteType)
	collection := strings.TrimSuffix(route.Identity.NativeID, "/"+last(route.Identity.NativeID))
	reads := 0
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, collection) {
			reads++
		}
		return nil, false
	}
	contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
	if _, err := contributor.Contribute(context.Background(), "scope", assets); err != nil {
		t.Fatal(err)
	}
	baseline := reads
	reads = 0
	domain := cdnAsset(t, assets, afdDomainType)
	for i := 0; i < 20; i++ {
		payload, _ := json.Marshal(s.records[domain.Identity.NativeID])
		var raw map[string]any
		json.Unmarshal(payload, &raw)
		raw["id"] = domain.Identity.NativeID + fmt.Sprint(i)
		raw["name"] = last(text(raw["id"]))
		s.add(raw, cdnVersion)
		s.lists[cdnProfileID(domain.Identity.NativeID)+"/customdomains"] = append(s.lists[cdnProfileID(domain.Identity.NativeID)+"/customdomains"], raw)
		assets = append(assets, dnsAsset(t, r, raw))
	}
	if _, err := contributor.Contribute(context.Background(), "scope", assets); err != nil {
		t.Fatal(err)
	}
	if baseline == 0 || reads > baseline+2 {
		t.Fatalf("extra domains repeated whole route inventory: before=%d after=%d", baseline, reads)
	}
}
