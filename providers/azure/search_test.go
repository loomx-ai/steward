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

func searchExample(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/search/" + name + ".json")
	var raw map[string]any
	if err != nil || json.Unmarshal(payload, &raw) != nil {
		t.Fatal("invalid Search example", err)
	}
	return raw
}
func searchExampleBody(t *testing.T, name string) map[string]any {
	return object(object(object(searchExample(t, name)["responses"])["200"])["body"])
}
func searchScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	group := root + "/resourcegroups/rg1"
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
	var raws []map[string]any
	for _, name := range []string{"SearchGetService", "GetPrivateEndpointConnection", "GetSharedPrivateLinkResource", "NetworkSecurityPerimeterConfigurationsGet"} {
		raw := searchExampleBody(t, name)
		payload, _ := json.Marshal(raw)
		payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
		json.Unmarshal(payload, &raw)
		raw["id"] = strings.ToLower(text(raw["id"]))
		// Search's example omits the state; the documented DELETE needs a terminal
		// state. Keep the original fixture unchanged and complete this scenario.
		if text(raw["type"]) == searchLinkType {
			object(raw["properties"])["provisioningState"] = "Succeeded"
		}
		s.add(raw, "2025-05-01")
		raws = append(raws, raw)
	}
	service := raws[0]
	object(service["properties"])["eTag"] = "scanned-service-etag"
	serviceID := text(service["id"])
	for i, field := range []string{"privateEndpointConnections", "sharedPrivateLinkResources"} {
		object(service["properties"])[field] = []any{map[string]any{"id": raws[i+1]["id"]}}
	}
	s.lists[root+"/providers/microsoft.search/searchservices"] = []any{service}
	s.version[root+"/providers/microsoft.search/searchservices"] = "2025-05-01"
	for _, raw := range raws[1:] {
		path := redisParentID(text(raw["id"])) + "/" + strings.ToLower(last(text(raw["type"])))
		s.lists[path] = []any{raw}
		s.version[path] = "2025-05-01"
	}
	targetID, _ := searchLinkTarget(raws[2])
	target := map[string]any{"id": targetID, "name": last(targetID), "type": storageType, "location": "westus", "sku": map[string]any{"name": "Standard_LRS"}, "properties": map[string]any{"creationTime": "2024-01-01T00:00:00Z", "provisioningState": "Succeeded", "privateEndpointConnections": []any{}}}
	mapping, _ := findType(storageType)
	s.add(target, mapping.Version)
	r := s.runtime(t)
	assets := []asset.Asset{}
	for _, raw := range raws {
		value := dnsAsset(t, r, raw)
		value.Location = text(service["location"])
		if value.Identity.NativeType == searchPerimeterType {
			value.Capabilities = nil
		}
		assets = append(assets, value)
	}
	if serviceID == "" {
		t.Fatal("missing Search service")
	}
	return s, r, assets
}

func TestSearchNativeSchemasAndProvenance(t *testing.T) {
	payload, err := os.ReadFile("fixtures/search/sources.json")
	var manifest []map[string]string
	if err != nil || json.Unmarshal(payload, &manifest) != nil || len(manifest) != 11 {
		t.Fatal("invalid Search sources")
	}
	for _, entry := range manifest {
		payload, err := os.ReadFile("fixtures/search/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] {
			t.Fatal("Search example changed")
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
		if !strings.Contains(doc.SourceURI, "/specification/search/") {
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
				if entry["operation"] != text(op["operationId"]) {
					continue
				}
				raw := searchExampleBody(t, strings.TrimSuffix(entry["file"], ".json"))
				payload, _ := json.Marshal(raw)
				value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
				// Preserve the source's null/non-nullable schema discrepancies.
				paths := map[string][]string{
					"Services_ListBySubscription":                          {"/value/0/properties/serviceUpgradedAt", "/value/1/properties/serviceUpgradedAt"},
					"NetworkSecurityPerimeterConfigurations_ListByService": {"/nextLink"},
					"SharedPrivateLinkResources_ListByService":             {"/value/0/properties/resourceRegion"},
					"SharedPrivateLinkResources_Get":                       {"/properties/resourceRegion"},
				}
				if pointers := paths[text(op["operationId"])]; len(pointers) > 0 {
					if schema.Validate(value) == nil {
						t.Fatal("native Search null mismatch disappeared")
					}
					for _, pointer := range pointers {
						parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
						var node any = value
						for _, part := range parts[:len(parts)-1] {
							if rows, ok := node.([]any); ok {
								if part == "0" {
									node = rows[0]
								} else {
									node = rows[1]
								}
							} else {
								node = object(node)[part]
							}
						}
						field := last(pointer)
						if v, exists := object(node)[field]; !exists || v != nil {
							t.Fatal("unexpected native Search discrepancy", pointer)
						}
						delete(object(node), field)
					}
				}
				if err := schema.Validate(value); err != nil {
					t.Errorf("native %s: %v", op["operationId"], err)
				}
				checked++
			}
		}
	}
	if checked != 8 {
		t.Fatal("missing Search schema checks", checked)
	}
}

func TestSearchNativeInventoryAndReviewedCleanup(t *testing.T) {
	for _, kind := range []string{searchType, searchConnectionType, searchLinkType, searchPerimeterType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := searchScenario(t)
			target := cdnAsset(t, assets, kind)
			batch, err := r.List(context.Background(), productRequest(r, kind))
			if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].Location != "westus" {
				t.Fatal("Search inventory", err, batch)
			}
			if kind == searchPerimeterType {
				if _, err := r.ResolveAction(context.Background(), "connection", target); err == nil || batch.Items[0].Normalized["cleanup_controller_only"] != true {
					t.Fatal("managed Search configuration had independent delete")
				}
				return
			}
			request, input := dnsRequest(t, r, assets, target)
			solved, err := plan.Solve(input)
			if err != nil || len(solved.Blockers) > 0 {
				t.Fatal("Search plan", err, solved.Blockers)
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if kind == searchType {
				if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) > 0 {
					t.Fatal("Search service bypassed prerequisite links")
				}
				if len(request.LifecycleImpacts) != 1 || len(request.PrerequisiteDeletions) != 2 {
					t.Fatal("Search cascade/prerequisites", request)
				}
			}
			for _, step := range solved.Steps {
				value := assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.ID == step.AssetID })]
				subrequest := servicePlanRequest(solved, assets, value)
				actionDriver, _ := r.ResolveAction(context.Background(), "connection", value)
				result, err := actionDriver.Execute(context.Background(), subrequest)
				if err != nil {
					t.Fatalf("Search deletion %s: %v", value.Identity.NativeType, err)
				}
				for _, impact := range subrequest.LifecycleImpacts {
					s.gone[impact.Asset.Identity.NativeID] = true
				}
				parent := s.records[assets[0].Identity.NativeID]
				for _, field := range []string{"privateEndpointConnections", "sharedPrivateLinkResources"} {
					refs := []any{}
					for _, row := range array(object(parent["properties"])[field]) {
						if !s.gone[strings.ToLower(text(object(row)["id"]))] {
							refs = append(refs, row)
						}
					}
					object(parent["properties"])[field] = refs
				}
				object(parent["properties"])["eTag"] = "changed-after-reviewed-child"
				payload, _ := json.Marshal(subrequest)
				json.Unmarshal(payload, &subrequest)
				payload, _ = json.Marshal(result)
				json.Unmarshal(payload, &result)
				actionDriver, _ = r.ResolveAction(context.Background(), "connection", subrequest.Asset)
				waited, err := actionDriver.Wait(context.Background(), subrequest, result)
				if err != nil || !waited.Done {
					t.Fatal("Search resumed wait", err, waited)
				}
			}
			for _, id := range s.deletes {
				if !strings.Contains(id, "/microsoft.search/searchservices/") {
					t.Fatal("Search deleted external target", id)
				}
			}
		})
	}
}

func TestSearchLinkChangesAndProtectionBlockMutation(t *testing.T) {
	for _, mode := range []string{"target-drift", "target-private-drift", "parent-drift", "parent-protected", "target-protected", "target-lock", "target-group-managed", "target-forbidden", "target-partial", "target-identity", "target-reread", "pending", "missing-state", "retarget"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := searchScenario(t)
			target := cdnAsset(t, assets, searchLinkType)
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if checked, err := driver.Preflight(context.Background(), request); err != nil || !checked.Allowed {
				t.Fatal("invalid baseline", checked, err)
			}
			link := s.records[target.Identity.NativeID]
			id, _ := searchLinkTarget(link)
			raw := s.records[id]
			parent := s.records[assets[0].Identity.NativeID]
			switch mode {
			case "target-drift":
				raw["sku"] = map[string]any{"name": "Premium_LRS"}
			case "target-private-drift":
				object(raw["properties"])["connectionString"] = "never-public-new-secret"
			case "parent-drift":
				object(parent["properties"])["replicaCount"] = 7
			case "parent-protected":
				parent["tags"] = map[string]any{"steward:protected": "true"}
			case "target-protected":
				raw["tags"] = map[string]any{"steward:protected": "true"}
			case "target-lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": id + "/providers/microsoft.authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "target-group-managed":
				group := strings.Join(strings.Split(id, "/")[:5], "/")
				s.records[group] = map[string]any{"id": group, "managedBy": id}
			case "target-forbidden":
				s.status[id] = 403
			case "target-partial":
				s.status[id] = 206
			case "target-identity":
				raw["id"] = id + "wrong"
			case "target-reread":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, id) {
						reads++
						if reads == 2 {
							raw["tags"] = map[string]any{"changed": "during-read"}
						}
					}
					return nil, false
				}
			case "pending":
				object(link["properties"])["provisioningState"] = "Deleting"
			case "missing-state":
				delete(object(link["properties"]), "provisioningState")
			case "retarget":
				object(link["properties"])["privateLinkResourceId"] = id + "other"
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("unsafe Search mutation allowed", mode, err)
			}
		})
	}
}

func TestSearchLinkNativeTargetScopeAndRegion(t *testing.T) {
	for _, mode := range []string{"other-group", "other-subscription", "unknown-api", "wrong-region", "region-alias", "invalid-region"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := searchScenario(t)
			link := s.records[assets[2].Identity.NativeID]
			props := object(link["properties"])
			old, _ := searchLinkTarget(link)
			original := s.records[old]
			targetID := old
			switch mode {
			case "other-group":
				targetID = strings.Replace(old, "/resourcegroups/rg1/", "/resourcegroups/data/", 1)
			case "other-subscription":
				targetID = strings.Replace(old, testSubscription, testTenant, 1)
			case "unknown-api":
				targetID = strings.Replace(old, "microsoft.storage/storageaccounts", "microsoft.example/targets", 1)
			case "wrong-region":
				props["resourceRegion"] = "eastus"
			case "region-alias":
				props["resourceRegion"] = "West US"
			case "invalid-region":
				props["resourceRegion"] = 17
			}
			props["privateLinkResourceId"] = targetID
			original["id"] = targetID
			s.records[targetID] = original
			s.version[targetID] = s.version[old]
			c, _ := r.resolve(context.Background(), "connection")
			item, err := r.inventoryItem(context.Background(), c, link, nil, nil)
			if mode != "other-group" && mode != "region-alias" {
				if err == nil {
					t.Fatal("invalid Search target was accepted", mode)
				}
				return
			}
			if err != nil {
				t.Fatal("valid Search target context", err)
			}
			target := assets[2]
			target.Normalized = item.Normalized
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Action: "delete", Asset: target}); err != nil || len(s.deletes) != 1 {
				t.Fatal("valid linked target blocked unlink", err)
			}
			if s.gone[targetID] {
				t.Fatal("unlink deleted external target")
			}
		})
	}
}
