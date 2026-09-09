package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const dataCollectionVersion = "2024-03-11"

func dataCollectionFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/data-collection/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func dataCollectionScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	for _, tc := range []struct{ name, kind, id string }{
		{"DataCollectionRulesGet", dataCollectionRuleType, resourceID(dataCollectionRuleType, "rule")},
		{"DataCollectionEndpointsGet", dataCollectionEndpointType, resourceID(dataCollectionEndpointType, "endpoint")},
		{"DataCollectionRuleAssociationsGet", dataCollectionAssociationType, resourceID(vmType, "monitored") + "/providers/Microsoft.Insights/dataCollectionRuleAssociations/metrics"},
		{"DataCollectionRuleAssociationsGet", dataCollectionAssociationType, resourceID(vmType, "monitored") + "/providers/Microsoft.Insights/dataCollectionRuleAssociations/configurationAccessEndpoint"},
	} {
		raw := object(object(object(dataCollectionFixture(t, tc.name)["responses"])["200"])["body"])
		raw["id"], raw["name"], raw["type"] = tc.id, last(tc.id), tc.kind
		properties := object(raw["properties"])
		collection := root + "/providers/" + strings.ToLower(tc.kind)
		if tc.kind == dataCollectionAssociationType {
			if strings.HasSuffix(tc.id, "/metrics") {
				properties["dataCollectionRuleId"] = resourceID(dataCollectionRuleType, "rule")
				collection = strings.ToLower(resourceID(dataCollectionRuleType, "rule") + "/associations")
			} else {
				delete(properties, "dataCollectionRuleId")
				properties["dataCollectionEndpointId"] = resourceID(dataCollectionEndpointType, "endpoint")
				collection = strings.ToLower(resourceID(dataCollectionEndpointType, "endpoint") + "/associations")
			}
		}
		s.add(raw, dataCollectionVersion)
		s.lists[collection] = append(s.lists[collection], raw)
		s.version[collection] = dataCollectionVersion
	}
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": root + "/resourceGroups/test", "type": groupType}}
	r := s.runtime(t)
	var assets []asset.Asset
	for _, kind := range []string{dataCollectionRuleType, dataCollectionEndpointType, dataCollectionAssociationType} {
		request := productRequest(r, kind)
		for {
			batch, err := r.List(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range batch.Items {
				if item.Location != "eastus" || item.Normalized["_inventory_source"] != productInventorySource {
					t.Fatalf("native collection location/source: %+v", item)
				}
				assets = append(assets, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: item.NativeID, NativeType: item.NativeType}, Location: item.Location, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}})
			}
			if batch.Complete {
				break
			}
			request.Cursor = batch.NextCursor
		}
	}
	if len(assets) != 4 {
		t.Fatalf("missing rule, endpoint or association: %+v", assets)
	}
	return s, r, assets
}

func TestDataCollectionNativePlanUnlinksBeforeDeleteAndResumes(t *testing.T) {
	for _, parentIndex := range []int{0, 1} {
		t.Run(fmt.Sprint(parentIndex), func(t *testing.T) {
			s, r, assets := dataCollectionScenario(t)
			parent := assets[parentIndex]
			request, input := dnsRequest(t, r, assets, parent)
			result, _ := plan.Solve(input)
			if len(result.Steps) != 2 || len(request.PrerequisiteDeletions) != 1 || len(result.ImpactItems) != 0 {
				t.Fatalf("association was not a distinct reviewed step: %+v %+v", result, request)
			}
			for _, binding := range input.LifecycleBindings {
				if binding.CleanupPolicy != graph.CleanupDirect || binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] == true {
					t.Fatal("association delegated to an implicit delete")
				}
			}
			child := request.PrerequisiteDeletions[0].Asset
			input.RequestOptions = map[asset.AssetID]map[string]any{parent.ID: {"retain_resources": []string{child.Identity.NativeID}}}
			retained, err := plan.Solve(input)
			if err != nil || len(retained.Blockers) == 0 {
				t.Fatalf("association retention allowed parent deletion: %+v %v", retained, err)
			}
			parentDriver, _ := r.ResolveAction(context.Background(), "connection", parent)
			if _, err := parentDriver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("parent deleted with a surviving association")
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "DELETE" {
					return nil, false
				}
				id := strings.ToLower(req.URL.Path)
				if req.URL.Query().Get("api-version") != dataCollectionVersion || req.Header.Get("If-Match") != "" || req.ContentLength > 0 || req.Header.Get("x-ms-client-request-id") != azureRequestID(id) {
					t.Fatalf("wrong native DELETE: %s %v", req.URL, req.Header)
				}
				if id == strings.ToLower(resourceID(dataCollectionRuleType, "rule")) {
					if req.URL.Query().Get("deleteAssociations") != "false" {
						t.Fatal("DCR delete enabled an implicit association cascade")
					}
				} else if req.URL.Query().Has("deleteAssociations") {
					t.Fatal("invented native endpoint/association deletion parameter")
				}
				s.deletes = append(s.deletes, id)
				return jsonResponse(200, nil, http.Header{"X-Ms-Request-Id": {"monitor-delete"}}), true
			}
			for _, value := range []asset.Asset{child, parent} {
				req := servicePlanRequest(result, assets, value)
				req.IdempotencyKey = value.Identity.NativeID
				driver, err := r.ResolveAction(context.Background(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				op, err := driver.Execute(context.Background(), req)
				if err != nil || op.ProviderRequestID != "monitor-delete" {
					t.Fatalf("native delete: %+v %v", op, err)
				}
				payload, _ := json.Marshal(req)
				json.Unmarshal(payload, &req)
				payload, _ = json.Marshal(op)
				json.Unmarshal(payload, &op)
				driver, _ = s.runtime(t).ResolveAction(context.Background(), "connection", value)
				if wait, err := driver.Wait(context.Background(), req, op); err != nil || wait.Done {
					t.Fatalf("200 deletion proved false absence: %+v %v", wait, err)
				}
				s.gone[value.Identity.NativeID] = true
				if value.ID == parent.ID {
					s.gone[child.Identity.NativeID] = false
					if wait, err := driver.Wait(context.Background(), req, op); err == nil || wait.Done {
						t.Fatalf("parent absence hid a surviving association: %+v %v", wait, err)
					}
					s.gone[child.Identity.NativeID] = true
				}
				if wait, err := driver.Wait(context.Background(), req, op); err != nil || !wait.Done {
					t.Fatalf("native readback did not finish: %+v %v", wait, err)
				}
				before := len(s.deletes)
				if _, err := driver.Execute(context.Background(), req); err != nil || len(s.deletes) != before {
					t.Fatal("recovery repeated completed DELETE")
				}
			}
			if len(s.deletes) != 2 || s.deletes[0] != child.Identity.NativeID || s.deletes[1] != parent.Identity.NativeID {
				t.Fatalf("wrong cleanup order: %v", s.deletes)
			}
		})
	}
}

func TestDataCollectionConfigurationProtectionAndDiscoveryFailures(t *testing.T) {
	for _, mode := range []string{"rule-immutable", "rule-flows", "endpoint-immutable", "endpoint-network", "association-target", "association-etag", "association-created", "association-bad-reference", "list-denied", "list-not-found", "list-partial", "detail-denied", "duplicate", "foreign-association", "wrong-target", "new-association", "disappearing-association", "management-lock", "managed-group"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := dataCollectionScenario(t)
			parent := assets[0]
			request, _ := dnsRequest(t, r, assets, parent)
			child := request.PrerequisiteDeletions[0].Asset
			index := 0
			if strings.HasPrefix(mode, "endpoint-") {
				index = 1
			} else if strings.HasPrefix(mode, "association-") {
				index = slices.IndexFunc(assets, func(v asset.Asset) bool { return v.ID == child.ID })
			}
			value := assets[index]
			raw := s.records[value.Identity.NativeID]
			listPath := parent.Identity.NativeID + "/associations"
			if strings.HasPrefix(mode, "rule-") || strings.HasPrefix(mode, "endpoint-") || mode == "new-association" {
				// Remove the reviewed links so failure must come from the changed
				// configuration/new member, not a surviving old prerequisite.
				for _, association := range assets[2:] {
					s.gone[association.Identity.NativeID] = true
				}
			}
			switch mode {
			case "rule-immutable", "endpoint-immutable":
				object(raw["properties"])["immutableId"] = "recreated"
			case "rule-flows":
				object(raw["properties"])["dataFlows"] = []any{}
			case "endpoint-network":
				object(object(raw["properties"])["networkAcls"])["publicNetworkAccess"] = "Disabled"
			case "association-target":
				object(raw["properties"])["dataCollectionRuleId"] = resourceID(dataCollectionRuleType, "other")
			case "association-etag":
				raw["etag"] = "changed"
			case "association-created":
				object(raw["systemData"])["createdAt"] = "2026-09-10T00:00:00Z"
			case "association-bad-reference":
				object(raw["properties"])["dataCollectionEndpointId"] = map[string]any{"id": resourceID(dataCollectionEndpointType, "endpoint")}
			case "list-denied":
				s.status[listPath] = 403
			case "list-not-found":
				s.status[listPath] = 404
			case "detail-denied":
				s.status[child.Identity.NativeID] = 403
			case "duplicate":
				s.lists[listPath] = append(s.lists[listPath], s.lists[listPath][0])
			case "wrong-target":
				object(s.records[child.Identity.NativeID]["properties"])["dataCollectionRuleId"] = resourceID(dataCollectionRuleType, "other")
			case "foreign-association":
				s.records[child.Identity.NativeID]["id"] = strings.Replace(child.Identity.NativeID, testSubscription, testTenant, 1)
			case "new-association":
				added := nativeResource(dataCollectionAssociationType, "new", "eastus", map[string]any{"dataCollectionRuleId": parent.Identity.NativeID})
				added["id"] = strings.Replace(child.Identity.NativeID, "/metrics", "/new", 1)
				s.add(added, dataCollectionVersion)
				s.lists[listPath] = append(s.lists[listPath], added)
			case "list-partial":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					return jsonResponse(200, map[string]any{"nextLink": ""}, nil), strings.EqualFold(req.URL.Path, listPath)
				}
			case "disappearing-association":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if !strings.EqualFold(req.URL.Path, listPath) {
						return nil, false
					}
					reads++
					values := s.lists[listPath]
					if reads > 1 {
						values = []any{}
					}
					return jsonResponse(200, map[string]any{"value": values}, nil), true
				}
			case "management-lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": child.Identity.NativeID + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
				value, request = child, contracts.ActionRequest{Action: "delete", Asset: child}
			case "managed-group":
				group := "/subscriptions/" + testSubscription + "/resourcegroups/test"
				s.add(map[string]any{"id": group, "type": groupType, "managedBy": resourceID(aksType, "owner")}, resourcesVersion)
			}
			if index != 0 {
				request = contracts.ActionRequest{Action: "delete", Asset: value}
			}
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatalf("changed/protected state allowed deletion: %v", err)
			}
			if strings.Contains(mode, "list-") || slices.Contains([]string{"duplicate", "foreign-association", "wrong-target", "disappearing-association"}, mode) {
				contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
				if contribution, err := contributor.Contribute(context.Background(), "scope", assets); err == nil {
					t.Fatalf("uncertain native member set was accepted: %+v", contribution)
				}
			}
		})
	}
}

func TestDataCollectionExtensionIdentityAndDependencyBoundaries(t *testing.T) {
	s, r, assets := dataCollectionScenario(t)
	c, _ := r.resolve(context.Background(), "connection")
	kind, _ := findType(dataCollectionAssociationType)
	for _, parent := range []string{resourceID(vmType, "machine"), resourceID(aksType, "cluster"), "/subscriptions/" + testSubscription + "/resourceGroups/test", resourceID("Microsoft.HybridCompute/machines", "arc")} {
		id := parent + "/providers/Microsoft.Insights/dataCollectionRuleAssociations/metrics"
		endpoint, err := c.resourceURL(kind, id)
		if err != nil {
			t.Fatal(err)
		}
		u, _ := url.Parse(endpoint)
		if !strings.EqualFold(u.Path, id) || strings.Contains(u.EscapedPath(), "%2F") {
			t.Fatalf("extension parent path escaped or truncated: %s", endpoint)
		}
	}
	validID := resourceID(vmType, "monitored") + "/providers/Microsoft.Insights/dataCollectionRuleAssociations/metrics"
	for _, id := range []string{resourceID(vmType, "vm") + "/providers/Microsoft.Insights/dataCollectionRuleAssociations/a/children/b", strings.Replace(validID, testSubscription, testTenant, 1), strings.Replace(validID, "/metrics", "/..", 1), strings.Replace(validID, "/metrics", "/a%2Fb", 1), "/providers/Microsoft.Insights/monitoredObjects/tenant/providers/Microsoft.Insights/dataCollectionRuleAssociations/a"} {
		if _, err := c.resourceURL(kind, id); err == nil {
			t.Fatalf("accepted out-of-scope extension path %s", id)
		}
	}
	for _, value := range assets[2:] {
		if refs, ok := value.Normalized[referenceKey(vmType)].([]string); !ok || len(refs) != 1 || !strings.EqualFold(refs[0], resourceID(vmType, "monitored")) {
			t.Fatalf("lost monitored-resource edge: %+v", value.Normalized)
		}
	}
	// Preserve richer routing and identity references without treating data
	// destination accounts, VMs or identities as owned children.
	root := s.records[assets[0].Identity.NativeID]
	properties := object(root["properties"])
	properties["dataCollectionEndpointId"] = assets[1].Identity.NativeID
	properties["references"] = map[string]any{"enrichmentData": map[string]any{"storageBlobs": []any{map[string]any{"resourceId": resourceID(storageType, "external"), "blobUrl": "https://user:secret@external.blob.core.windows.net/lookup/items.csv?sig=do-not-store-sas&se=2026-09-10#private"}}}}
	root["identity"] = map[string]any{"type": "UserAssigned", "userAssignedIdentities": map[string]any{resourceID("Microsoft.ManagedIdentity/userAssignedIdentities", "identity"): map[string]any{}}}
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	batch, err := r.List(ctx, productRequest(r, dataCollectionRuleType))
	if err != nil {
		t.Fatal(err)
	}
	serialized, _ := json.Marshal(map[string]any{"inventory": batch, "logs": logs})
	if strings.Contains(string(serialized), "do-not-store-sas") || strings.Contains(string(serialized), "user:secret") || strings.Contains(string(serialized), "#private") || !strings.Contains(string(serialized), "https://external.blob.core.windows.net/lookup/items.csv") {
		t.Fatal("Blob enrichment URL credentials escaped inventory")
	}
	if len(logs) == 0 || !strings.Contains(fmt.Sprint(properties["references"]), "do-not-store-sas") {
		t.Fatal("native transport was not logged, or redaction mutated the live payload")
	}
	for _, target := range []string{dataCollectionEndpointType, storageType, "Microsoft.ManagedIdentity/userAssignedIdentities", "Microsoft.OperationalInsights/workspaces"} {
		if refs, ok := batch.Items[0].Normalized[referenceKey(target)].([]string); !ok || len(refs) != 1 {
			t.Fatalf("missing routing dependency %s", target)
		}
	}
}

func TestDataCollectionNativeSchemasAndProvenance(t *testing.T) {
	manifestBytes, _ := os.ReadFile("fixtures/data-collection/sources.json")
	var manifest []map[string]string
	if json.Unmarshal(manifestBytes, &manifest) != nil || len(manifest) != 14 {
		t.Fatal("incomplete native fixture manifest")
	}
	for _, entry := range manifest {
		payload, err := os.ReadFile("fixtures/data-collection/" + entry["file"])
		if err != nil || entry["source_sha256"] != fmt.Sprintf("%x", sha256.Sum256(payload)) || !strings.Contains(entry["source_uri"], "/e45039baa985c442877529906e705982a6e0099d/") {
			t.Fatalf("native example provenance changed: %s", entry["file"])
		}
	}
	payload, _ := os.ReadFile("catalog/source/swagger.json")
	var sources catalog.RESTDocumentSet
	json.Unmarshal(payload, &sources)
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	root := ""
	queryRoot := ""
	for _, source := range sources.Documents {
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(source.Document))
		if err := compiler.AddResource(source.SourceURI, value); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(source.SourceURI, "/2024-03-11/dataCollection.json") {
			root = source.SourceURI
			if source.SourceSHA256 != "78e41e00ae17c4099d855d933fc18ab2b88728e7730ed9760525cbc80893df8c" {
				t.Fatal("native Swagger fingerprint changed")
			}
		}
		if strings.Contains(source.SourceURI, "/2024-04-01/resourcegraph.json") {
			queryRoot = source.SourceURI
			if source.SourceSHA256 != "b9cc4a440858bb51f10474cd36208dee324ddbddb8ce06fd31378efea5a6d111" {
				t.Fatal("native Resource Graph Swagger fingerprint changed")
			}
		}
	}
	for _, name := range []string{"ResourcesBasicQuery", "ResourcesFirstPageQuery", "ResourcesNextPageQuery"} {
		fixture := dataCollectionFixture(t, name)
		for definition, value := range map[string]any{"QueryRequest": object(fixture["parameters"])["query"], "QueryResponse": object(object(fixture["responses"])["200"])["body"]} {
			schema, err := compiler.Compile(queryRoot + "#/definitions/" + definition)
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(value)
			value, _ = jsonschema.UnmarshalJSON(bytes.NewReader(payload))
			if err := schema.Validate(value); err != nil {
				t.Fatalf("official graph %s %s violates its native schema: %v", name, definition, err)
			}
		}
	}
	for _, tc := range []struct{ name, schema string }{{"DataCollectionRulesGet", "DataCollectionRuleResource"}, {"DataCollectionEndpointsGet", "DataCollectionEndpointResource"}, {"DataCollectionRuleAssociationsGet", "DataCollectionRuleAssociationProxyOnlyResource"}} {
		schema, err := compiler.Compile(root + "#/definitions/" + tc.schema)
		if err != nil {
			t.Fatal(err)
		}
		raw := object(object(object(dataCollectionFixture(t, tc.name)["responses"])["200"])["body"])
		payload, _ := json.Marshal(raw)
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
		if err := schema.Validate(value); err != nil {
			t.Fatalf("official native payload violates schema %s: %v", tc.name, err)
		}
	}
	// This unmodified official reverse-list example names a different endpoint
	// in its parameters and returned association. Reject the mismatch.
	list := dataCollectionFixture(t, "DataCollectionRuleAssociationsListByDataCollectionEndpoint")
	parameters := object(list["parameters"])
	parent := "/subscriptions/" + text(parameters["subscriptionId"]) + "/resourceGroups/" + text(parameters["resourceGroupName"]) + "/providers/Microsoft.Insights/dataCollectionEndpoints/" + text(parameters["dataCollectionEndpointName"])
	body := object(object(object(list["responses"])["200"])["body"])
	if dataCollectionAssociationMembership(object(array(body["value"])[0]), parent) == nil {
		t.Fatal("accepted upstream example's mismatched endpoint backlink")
	}
}

func TestDataCollectionDualTargetReferencesShareOneReviewedPrerequisite(t *testing.T) {
	for _, mode := range []string{"reviewed", "missing-rule", "missing-reverse-index"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := dataCollectionScenario(t)
			var child asset.Asset
			for _, value := range assets {
				if strings.HasSuffix(value.Identity.NativeID, "/metrics") {
					child = value
				}
			}
			raw := s.records[child.Identity.NativeID]
			object(raw["properties"])["dataCollectionEndpointId"] = assets[1].Identity.NativeID
			s.lists[assets[1].Identity.NativeID+"/associations"] = append(s.lists[assets[1].Identity.NativeID+"/associations"], raw)
			if mode == "missing-rule" {
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.insights/datacollectionrules"] = nil
			} else if mode == "missing-reverse-index" {
				s.lists[assets[0].Identity.NativeID+"/associations"] = nil
			}
			request := productRequest(r, dataCollectionAssociationType)
			counts := map[string]int{}
			var failure error
			for {
				batch, err := r.List(context.Background(), request)
				if err != nil {
					failure = err
					break
				}
				for _, item := range batch.Items {
					counts[item.NativeID]++
					if item.NativeID == child.Identity.NativeID {
						child.Normalized, child.Location = item.Normalized, item.Location
					}
				}
				if batch.Complete {
					break
				}
				request.Cursor = batch.NextCursor
			}
			if mode != "reviewed" {
				if failure == nil {
					t.Fatal("asymmetric native indexes silently omitted the association")
				}
				return
			}
			if failure != nil || counts[child.Identity.NativeID] != 1 {
				t.Fatalf("dual-target inventory not unique: %v %v", counts, failure)
			}
			for i, value := range assets {
				if value.ID == child.ID {
					assets[i] = child
				}
			}
			contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
			contribution, err := contributor.Contribute(context.Background(), "scope", assets)
			if err != nil {
				t.Fatal(err)
			}
			for _, binding := range contribution.Bindings {
				if binding.ManagedAssetID == child.ID {
					t.Fatal("dual-target association acquired an exclusive owner")
				}
			}
			input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{assets[0].ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
			result, err := plan.Solve(input)
			if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 2 || len(servicePlanRequest(result, assets, assets[0]).PrerequisiteDeletions) != 1 {
				t.Fatalf("parent did not include the shared link for review: %+v %v", result, err)
			}
			both := input
			both.ResolvedAssetIDs = append(slices.Clone(input.ResolvedAssetIDs), assets[1].ID)
			combined, err := plan.Solve(both)
			if err != nil || len(combined.Blockers) != 0 || len(combined.Steps) != 4 || len(servicePlanRequest(combined, assets, assets[1]).PrerequisiteDeletions) != 2 {
				t.Fatalf("two targets duplicated or lost the shared prerequisite: %+v %v", combined, err)
			}
			for _, value := range []asset.Asset{child, assets[0]} {
				driver, _ := r.ResolveAction(context.Background(), "connection", value)
				req := servicePlanRequest(result, assets, value)
				if _, err := driver.Execute(context.Background(), req); err != nil {
					t.Fatalf("explicit shared-link native cleanup: %v", err)
				}
			}
			if aksExternalRelation(assets[0], child) {
				t.Fatal("AKS resource-group ownership expanded to an external association")
			}
		})
	}
}

func TestDataCollectionPaginationBindsBothTargetSets(t *testing.T) {
	for _, mode := range []string{"complete", "endpoint-configuration", "rule-immutable", "new-parent", "page-denied", "foreign-next-link", "cycle", "list-detail-retarget"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := dataCollectionScenario(t)
			var child asset.Asset
			for _, value := range assets {
				if strings.HasSuffix(value.Identity.NativeID, "/configurationaccessendpoint") {
					child = value
				}
			}
			payload, _ := json.Marshal(s.records[child.Identity.NativeID])
			var second map[string]any
			json.Unmarshal(payload, &second)
			second["id"], second["name"] = child.Identity.NativeID+"two", "configurationAccessEndpointTwo"
			s.add(second, dataCollectionVersion)
			collection := assets[1].Identity.NativeID + "/associations"
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if !strings.EqualFold(req.URL.Path, collection) {
					return nil, false
				}
				body := map[string]any{"value": []any{s.records[child.Identity.NativeID]}}
				if req.URL.Query().Get("$skipToken") == "next" {
					if mode == "page-denied" {
						return jsonResponse(403, nil, nil), true
					}
					body["value"] = []any{second}
					if mode == "list-detail-retarget" {
						encoded, _ := json.Marshal(second)
						var changed map[string]any
						json.Unmarshal(encoded, &changed)
						object(changed["properties"])["dataCollectionEndpointId"] = resourceID(dataCollectionEndpointType, "different")
						s.records[strings.ToLower(text(second["id"]))] = changed
					}
				} else {
					body["nextLink"] = apiURL(collection, dataCollectionVersion) + "&%24skipToken=next"
				}
				if mode == "cycle" {
					body["nextLink"] = apiURL(collection, dataCollectionVersion) + "&%24skipToken=next"
				}
				if mode == "foreign-next-link" {
					body["nextLink"] = "https://attacker.invalid/associations"
				}
				return jsonResponse(200, body, http.Header{"X-Ms-Request-Id": {"association-page"}}), true
			}
			request := productRequest(r, dataCollectionAssociationType)
			first, err := r.List(context.Background(), request) // Endpoint sorts before rule.
			if mode == "foreign-next-link" {
				if err == nil || first.Complete {
					t.Fatal("foreign native nextLink was accepted")
				}
				return
			}
			if err != nil || first.Complete || len(first.Items) != 1 || first.Items[0].NativeID != child.Identity.NativeID || first.RequestID != "association-page" {
				t.Fatalf("first extension page: %+v %v", first, err)
			}
			switch mode {
			case "endpoint-configuration":
				object(object(s.records[assets[1].Identity.NativeID]["properties"])["networkAcls"])["publicNetworkAccess"] = "Disabled"
			case "rule-immutable":
				object(s.records[assets[0].Identity.NativeID]["properties"])["immutableId"] = "new-rule"
			case "new-parent":
				newParent := nativeResource(dataCollectionRuleType, "new", "eastus", map[string]any{"immutableId": "dcr-new"})
				s.add(newParent, dataCollectionVersion)
				path := "/subscriptions/" + testSubscription + "/providers/microsoft.insights/datacollectionrules"
				s.lists[path] = append(s.lists[path], newParent)
			}
			request.Cursor = first.NextCursor
			next, err := r.List(context.Background(), request)
			if mode != "complete" {
				if err == nil || next.Complete {
					t.Fatalf("changed/uncertain page accepted: %+v %v", next, err)
				}
				return
			}
			if err != nil || len(next.Items) != 1 || next.Items[0].NativeID != text(second["id"]) || next.Complete {
				t.Fatalf("second extension page: %+v %v", next, err)
			}
			request.Cursor = next.NextCursor
			last, err := r.List(context.Background(), request)
			if err != nil || !last.Complete || len(last.Items) != 1 || !strings.HasSuffix(last.Items[0].NativeID, "/metrics") {
				t.Fatalf("rule reverse-index target omitted: %+v %v", last, err)
			}
		})
	}
}

func TestDataCollectionMicrosoftCLIDeletionResponses(t *testing.T) {
	payload, err := os.ReadFile("fixtures/data-collection/cli-delete-recordings.json")
	if err != nil {
		t.Fatal(err)
	}
	var recordings []map[string]any
	if json.Unmarshal(payload, &recordings) != nil || len(recordings) != 3 {
		t.Fatal("incomplete Microsoft CLI recordings")
	}
	for index, recording := range recordings {
		t.Run(fmt.Sprint(index), func(t *testing.T) {
			fingerprints := []string{"4f8d428504c2753dcaabb216cf94ebe87368db5a35c53f76499ce510f61cbff8", "d1d9000711991da88e0a8e87cd507aa0df605bad991d1222d260e7b9c0e958d1", "192f5de256176dbfdee45698cd67506e68fa90b8ea2635d27b63ea574a30efb2"}
			if text(recording["source_sha256"]) != fingerprints[index] || !strings.Contains(text(recording["source_uri"]), "/b10329ba54a03c1d0da9e7ed862f83b36e8a177f/") || text(recording["recorded_api_version"]) != "2023-03-11" {
				t.Fatal("unexpected native CLI provenance")
			}
			var deletion grafanaRecordedResponse
			encoded, _ := json.Marshal(recording["delete"])
			json.Unmarshal(encoded, &deletion)
			if deletion.Status != 200 || deletion.Body != nil {
				t.Fatal("unexpected native CLI deletion response")
			}
			if index == 2 && (recording["read_index"] != float64(22) || recording["read_method"] != "PUT" || recording["delete_index"] != float64(23)) {
				t.Fatal("association fixture must use the actual updated resource response")
			}
			// Scope/API rebinding only. The retained record predates the selected
			// Swagger; its response shape is independent native protocol evidence.
			encoded, _ = json.Marshal(object(recording["read"])["body"])
			encoded = bytes.ReplaceAll(encoded, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
			var raw map[string]any
			json.Unmarshal(encoded, &raw)
			s := newDNSScenario()
			s.add(raw, dataCollectionVersion)
			id := strings.ToLower(text(raw["id"]))
			s.lists[id+"/associations"], s.version[id+"/associations"] = []any{}, dataCollectionVersion
			r := s.runtime(t)
			value := dnsAsset(t, r, raw)
			deleteURI := strings.ReplaceAll(text(recording["resource_uri"]), "00000000-0000-0000-0000-000000000000", testSubscription)
			deleteURI = strings.ReplaceAll(deleteURI, "api-version=2023-03-11", "api-version=2024-03-11")
			expected, _ := url.Parse(deleteURI)
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "DELETE" {
					return nil, false
				}
				if !strings.EqualFold(req.URL.Path, expected.Path) || req.URL.Query().Encode() != expected.Query().Encode() || req.ContentLength > 0 || req.Header.Get("If-Match") != "" {
					t.Fatalf("request differs from recorded native DELETE: %s", req.URL)
				}
				s.deletes = append(s.deletes, id)
				return deletion.response(), true
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", value)
			request := contracts.ActionRequest{Asset: value, Action: "delete"}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || len(s.deletes) != 1 {
				t.Fatalf("native recorded delete: %+v %v", result, err)
			}
			if wait, err := driver.Wait(context.Background(), request, result); err != nil || wait.Done {
				t.Fatalf("native 200 was mistaken for absence: %+v %v", wait, err)
			}
			s.gone[id] = true // Synthetic readback, explicitly not part of the recording.
			if wait, err := driver.Wait(context.Background(), request, result); err != nil || !wait.Done {
				t.Fatalf("final native-resource readback: %+v %v", wait, err)
			}
		})
	}
}

func TestDataCollectionResourceGraphNativePagingAndScope(t *testing.T) {
	for _, mode := range []string{"complete", "denied", "page-denied", "missing-data", "table-data", "count-mismatch", "total-changed", "truncated", "lost-token", "cycle", "duplicate", "foreign", "wrong-type", "empty-page", "bad-token"} {
		t.Run(mode, func(t *testing.T) {
			s := newDNSScenario()
			pages := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				pages++
				var request map[string]any
				if req.Method != "POST" || req.URL.String() != apiURL("/providers/Microsoft.ResourceGraph/resources", "2024-04-01") || json.NewDecoder(req.Body).Decode(&request) != nil || fmt.Sprint(request["subscriptions"]) != "["+testSubscription+"]" || request["query"] != dataCollectionGraphQuery || request["managementGroups"] != nil || object(request["options"])["allowPartialScopes"] != false {
					t.Fatalf("unscoped graph request: %s %+v", req.URL, request)
				}
				name := "ResourcesFirstPageQuery"
				if pages > 1 {
					name = "ResourcesNextPageQuery"
				}
				body := object(object(object(dataCollectionFixture(t, name)["responses"])["200"])["body"])
				// Retain the native page envelope/token. Rebind fixture identities
				// and the query's total to a six-association test subscription.
				body["totalRecords"] = 6
				if mode == "cycle" {
					body["totalRecords"] = 12
				}
				for i, row := range array(body["data"]) {
					raw := object(row)
					raw["id"] = resourceID(vmType, "monitored") + fmt.Sprintf("/providers/Microsoft.Insights/dataCollectionRuleAssociations/association-%d-%d", pages, i)
					raw["type"] = dataCollectionAssociationType
				}
				firstToken := text(object(object(object(dataCollectionFixture(t, "ResourcesFirstPageQuery")["responses"])["200"])["body"])["$skipToken"])
				if pages > 1 {
					if object(request["options"])["$skipToken"] != firstToken {
						t.Fatal("native continuation token changed")
					}
					delete(body, "$skipToken")
				}
				switch mode {
				case "denied":
					return jsonResponse(403, nil, nil), true
				case "page-denied":
					if pages > 1 {
						return jsonResponse(429, nil, nil), true
					}
				case "missing-data":
					delete(body, "data")
				case "table-data":
					body["data"] = map[string]any{"rows": []any{}}
				case "count-mismatch":
					body["count"] = 2
				case "total-changed":
					if pages > 1 {
						body["totalRecords"] = 7
					}
				case "truncated":
					if pages > 1 {
						body["resultTruncated"] = "true"
					}
				case "lost-token":
					delete(body, "$skipToken")
				case "cycle":
					body["$skipToken"] = firstToken
				case "duplicate":
					object(array(body["data"])[1])["id"] = object(array(body["data"])[0])["id"]
				case "foreign":
					object(array(body["data"])[0])["id"] = strings.ReplaceAll(text(object(array(body["data"])[0])["id"]), testSubscription, "99999999-9999-4999-8999-999999999999")
				case "wrong-type":
					object(array(body["data"])[0])["type"] = vmType
				case "empty-page":
					body["data"], body["count"] = []any{}, 0
				case "bad-token":
					body["$skipToken"] = []any{"next"}
				}
				return jsonResponse(200, body, nil), true
			}
			r := s.runtime(t)
			c, _ := r.resolve(context.Background(), "connection")
			rows, err := c.dataCollectionGraph(context.Background())
			if mode == "complete" {
				if err != nil || len(rows) != 6 || pages != 2 {
					t.Fatalf("native graph paging: %d %d %v", len(rows), pages, err)
				}
			} else if err == nil {
				t.Fatal("partial/invalid graph response accepted")
			}
			before := pages
			endpoint := apiURL("/providers/Microsoft.ResourceGraph/resources", "2024-04-01")
			if _, err := c.request(context.Background(), "GET", endpoint); err == nil {
				t.Fatal("ordinary transport opened a global scope")
			}
			if _, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.ResourceGraph.Resources", Parameters: map[string]any{"query": map[string]any{"query": "resources"}}}); err == nil || pages != before {
				t.Fatal("generic invoke opened unscoped graph queries")
			}
		})
	}
}

func TestDataCollectionOrphanDiscoveryAndNativeUnlink(t *testing.T) {
	for _, mode := range []string{"orphan", "both-gone", "foreign-target", "endpoint-survives", "indexed", "stale-index", "get-denied", "list-denied", "list-incomplete", "wrong-monitored-resource", "missing-reverse-index", "orphan-generation"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := dataCollectionScenario(t)
			id := assets[3].Identity.NativeID
			raw := s.records[id]
			monitored, _ := dataCollectionMonitoredResource(id)
			collection := monitored + "/providers/microsoft.insights/datacollectionruleassociations"
			s.lists[collection], s.version[collection] = []any{raw}, dataCollectionVersion
			s.graph = []any{map[string]any{"id": id, "type": dataCollectionAssociationType, "location": "eastus"}}
			if mode != "indexed" && mode != "missing-reverse-index" {
				s.gone[assets[0].Identity.NativeID] = true
			}
			switch mode {
			case "both-gone", "endpoint-survives":
				object(raw["properties"])["dataCollectionEndpointId"] = assets[1].Identity.NativeID
				s.lists[assets[1].Identity.NativeID+"/associations"] = append(s.lists[assets[1].Identity.NativeID+"/associations"], raw)
				if mode == "both-gone" {
					s.gone[assets[1].Identity.NativeID] = true
					// The second endpoint-only link also needs a native orphan seed.
					s.graph = append(s.graph, map[string]any{"id": assets[2].Identity.NativeID, "type": dataCollectionAssociationType})
					s.lists[collection] = append(s.lists[collection], s.records[assets[2].Identity.NativeID])
				}
			case "foreign-target":
				object(raw["properties"])["dataCollectionRuleId"] = strings.ReplaceAll(assets[0].Identity.NativeID, testSubscription, "99999999-9999-4999-8999-999999999999")
			case "stale-index":
				s.gone[id] = true
			case "get-denied":
				s.status[id] = 403
			case "list-denied":
				s.status[collection] = 403
			case "list-incomplete":
				s.lists[collection] = []any{}
			case "wrong-monitored-resource":
				s.lists[collection] = append(s.lists[collection], map[string]any{"id": resourceID(vmType, "different") + "/providers/Microsoft.Insights/dataCollectionRuleAssociations/other", "type": dataCollectionAssociationType, "properties": map[string]any{"dataCollectionRuleId": assets[0].Identity.NativeID}})
			case "missing-reverse-index":
				s.lists[assets[0].Identity.NativeID+"/associations"] = []any{}
			}
			request := productRequest(r, dataCollectionAssociationType)
			counts := map[string]int{}
			var found contracts.InventoryItem
			var failure error
			for {
				batch, err := r.List(context.Background(), request)
				if err != nil {
					failure = err
					break
				}
				for _, item := range batch.Items {
					counts[item.NativeID]++
					if item.NativeID == id {
						found = item
					}
				}
				if batch.Complete {
					break
				}
				request.Cursor = batch.NextCursor
				if mode == "orphan-generation" {
					object(raw["properties"])["description"] = "changed after the first page"
				}
			}
			if slices.Contains([]string{"get-denied", "list-denied", "list-incomplete", "wrong-monitored-resource", "missing-reverse-index", "orphan-generation"}, mode) {
				if failure == nil {
					t.Fatal("uncertain orphan inventory accepted")
				}
				return
			}
			if mode == "stale-index" {
				if failure != nil || counts[id] != 0 {
					t.Fatalf("stale index invented a resource: %v %v", counts, failure)
				}
				return
			}
			if failure != nil || counts[id] != 1 {
				t.Fatalf("orphan/dual link lost or duplicated: %v %v", counts, failure)
			}
			if mode != "orphan" && mode != "foreign-target" {
				return
			}
			value := asset.Asset{ID: asset.AssetID(id), Identity: assets[3].Identity, Location: found.Location, Normalized: found.Normalized, Capabilities: assets[3].Capabilities}
			driver, _ := r.ResolveAction(context.Background(), "connection", value)
			req := contracts.ActionRequest{Asset: value, Action: "delete"}
			op, err := driver.Execute(context.Background(), req)
			if err != nil || !slices.Equal(s.deletes, []string{id}) {
				t.Fatalf("orphan was not independently unlinked: %v %v", s.deletes, err)
			}
			if wait, err := driver.Wait(context.Background(), req, op); err != nil || !wait.Done {
				t.Fatalf("orphan absence not checked: %+v %v", wait, err)
			}
		})
	}
}
