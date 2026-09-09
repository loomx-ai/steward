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
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const monitorWorkspaceVersion = "2023-04-03"

func monitorWorkspaceFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/monitor-workspace/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var raw map[string]any
	if json.Unmarshal(payload, &raw) != nil {
		t.Fatal("invalid native Monitor fixture")
	}
	return raw
}

func monitorWorkspaceScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	id := strings.ToLower(resourceID(monitorWorkspaceType, "workspace"))
	group := root + "/resourcegroups/purpose-built-owned"
	rule := group + "/providers/microsoft.insights/datacollectionrules/default-rule"
	endpoint := group + "/providers/microsoft.insights/datacollectionendpoints/default-endpoint"
	raw := object(object(object(monitorWorkspaceFixture(t, "AzureMonitorWorkspacesGet")["responses"])["200"])["body"])
	raw["id"], raw["name"] = id, "workspace"
	props := object(raw["properties"])
	props["defaultIngestionSettings"] = map[string]any{"dataCollectionRuleResourceId": rule, "dataCollectionEndpointResourceId": endpoint}
	connection := object(array(props["privateEndpointConnections"])[0])
	connection["id"] = id + "/privateEndpointConnections/private"
	object(object(connection["properties"])["privateEndpoint"])["id"] = resourceID(privateEndpointType, "external")
	s.add(raw, monitorWorkspaceVersion)
	s.lists[root+"/providers/microsoft.monitor/accounts"] = []any{raw}
	s.version[root+"/providers/microsoft.monitor/accounts"] = monitorWorkspaceVersion
	owned := map[string]any{"id": group, "name": "purpose-built-owned", "type": groupType, "location": "eastus", "managedBy": id, "properties": map[string]any{"provisioningState": "Succeeded"}}
	s.add(owned, resourcesVersion)
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": root + "/resourcegroups/test", "type": groupType}, owned}
	records := []map[string]any{raw, owned}
	for _, tc := range []struct{ name, id, kind string }{{"DataCollectionRulesGet", rule, dataCollectionRuleType}, {"DataCollectionEndpointsGet", endpoint, dataCollectionEndpointType}} {
		value := object(object(object(dataCollectionFixture(t, tc.name)["responses"])["200"])["body"])
		value["id"], value["name"], value["type"] = tc.id, last(tc.id), tc.kind
		if tc.kind == dataCollectionRuleType {
			object(value["properties"])["dataCollectionEndpointId"] = endpoint
			object(value["properties"])["destinations"] = map[string]any{"monitoringAccounts": []any{map[string]any{"name": "metrics", "accountResourceId": id}}}
			object(value["properties"])["dataSources"] = map[string]any{"prometheusForwarder": []any{map[string]any{"name": "prometheus", "streams": []any{"Microsoft-PrometheusMetrics"}}}}
			object(value["properties"])["dataFlows"] = []any{map[string]any{"streams": []any{"Microsoft-PrometheusMetrics"}, "destinations": []any{"metrics"}}}
		}
		s.add(value, dataCollectionVersion)
		records = append(records, value)
	}
	unknown := map[string]any{"id": group + "/providers/Contoso.Unknown/things/user-added", "type": "Contoso.Unknown/things", "name": "user-added", "location": "eastus", "tags": map[string]any{"review": "whole-group"}}
	s.add(unknown, resourcesVersion)
	records = append(records, unknown)
	s.lists[group+"/resources"] = []any{records[2], records[3], unknown}
	s.version[group+"/resources"] = resourcesVersion
	association := object(object(object(dataCollectionFixture(t, "DataCollectionRuleAssociationsGet")["responses"])["200"])["body"])
	association["id"] = strings.ToLower(resourceID(vmType, "external") + "/providers/Microsoft.Insights/dataCollectionRuleAssociations/metrics")
	association["name"] = "metrics"
	object(association["properties"])["dataCollectionRuleId"] = rule
	object(association["properties"])["dataCollectionEndpointId"] = endpoint
	s.add(association, dataCollectionVersion)
	records = append(records, association)
	for _, target := range []string{rule, endpoint} {
		s.lists[target+"/associations"] = []any{association}
		s.version[target+"/associations"] = dataCollectionVersion
	}
	r := s.runtime(t)
	batch, err := r.List(context.Background(), productRequest(r, monitorWorkspaceType))
	if err != nil || len(batch.Items) != 1 || !batch.Complete {
		t.Fatalf("workspace native list: %+v %v", batch, err)
	}
	assets := []asset.Asset{}
	for _, record := range records {
		assets = append(assets, dnsAsset(t, r, record))
	}
	refs, _ := assets[2].Normalized[referenceKey(monitorWorkspaceType)].([]string)
	if !slices.Contains(refs, id) {
		t.Fatal("native metrics destination did not reference the workspace")
	}
	return s, r, assets
}

func TestMonitorWorkspaceReviewsWholeGroupAndUnlinksSharedAssociation(t *testing.T) {
	s, r, assets := monitorWorkspaceScenario(t)
	request, input := dnsRequest(t, r, assets, assets[0])
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 2 || len(result.ImpactItems) != 4 || len(request.LifecycleImpacts) != 4 || len(request.PrerequisiteDeletions) != 1 {
		t.Fatalf("workspace plan: %+v %+v %v", result, request, err)
	}
	for _, binding := range input.LifecycleBindings {
		if binding.ManagedAssetID == assets[5].ID {
			t.Fatal("external association acquired workspace ownership")
		}
		if binding.ControllerAssetID != assets[0].ID || binding.CleanupPolicy != graph.CleanupDelegate || binding.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] != true {
			t.Fatalf("incomplete managed group ownership: %+v", binding)
		}
	}
	for _, value := range assets[1:] {
		retained := input
		retained.RequestOptions = map[asset.AssetID]map[string]any{assets[0].ID: {"retain_resources": []string{value.Identity.NativeID}}}
		planned, err := plan.Solve(retained)
		if err != nil || len(planned.Blockers) == 0 {
			t.Fatalf("retention ignored: %s %+v %v", value.Identity.NativeID, planned, err)
		}
	}
	driver, _ := r.ResolveAction(context.Background(), "connection", assets[0])
	if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
		t.Fatal("workspace deletion preceded external unlink")
	}
	childDriver, _ := r.ResolveAction(context.Background(), "connection", assets[5])
	childRequest := servicePlanRequest(result, assets, assets[5])
	if _, err := childDriver.Execute(context.Background(), childRequest); err != nil {
		t.Fatal(err)
	}
	// The native service may change target ETags when unlinking. Configuration and
	// creation identifiers remain unchanged and are checked independently.
	s.records[assets[0].Identity.NativeID]["etag"] = "after-unlink"
	s.records[assets[2].Identity.NativeID]["etag"] = "after-unlink"
	operationURL := apiURL("/subscriptions/"+testSubscription+"/resourceGroups/test/providers/Microsoft.Monitor/locations/eastus/operationStatus/default/operationId/00000000-0000-0000-0000-000000000000", monitorWorkspaceVersion)
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "DELETE" {
			if !strings.EqualFold(req.URL.Path, assets[0].Identity.NativeID) || req.URL.Query().Get("api-version") != monitorWorkspaceVersion || req.Header.Get("If-Match") != "" || req.ContentLength > 0 {
				t.Fatalf("invented Monitor DELETE: %s", req.URL)
			}
			s.deletes = append(s.deletes, strings.ToLower(req.URL.Path))
			return jsonResponse(202, nil, http.Header{"Location": {operationURL}, "X-Ms-Request-Id": {"monitor-workspace-delete"}}), true
		}
		if req.URL.String() == operationURL {
			return jsonResponse(204, nil, nil), true
		}
		return nil, false
	}
	operation, err := driver.Execute(context.Background(), request)
	if err != nil || operation.ProviderRequestID != "monitor-workspace-delete" || operation.ProviderOperationID != operationURL {
		t.Fatalf("native workspace delete: %+v %v", operation, err)
	}
	payload, _ := json.Marshal(operation)
	json.Unmarshal(payload, &operation)
	payload, _ = json.Marshal(request)
	json.Unmarshal(payload, &request)
	driver, _ = s.runtime(t).ResolveAction(context.Background(), "connection", assets[0])
	s.gone[assets[0].Identity.NativeID] = true
	if wait, err := driver.Wait(context.Background(), request, operation); err != nil || wait.Done {
		t.Fatalf("workspace absence hid managed group: %+v %v", wait, err)
	}
	s.gone[assets[1].Identity.NativeID] = true
	if wait, err := driver.Wait(context.Background(), request, operation); err != nil || wait.Done {
		t.Fatalf("group absence hid known resources: %+v %v", wait, err)
	}
	for _, value := range assets[2:5] {
		s.gone[value.Identity.NativeID] = true
	}
	s.gone[assets[5].Identity.NativeID] = false
	if wait, err := driver.Wait(context.Background(), request, operation); err == nil || wait.Done {
		t.Fatalf("root absence hid a surviving external association: %+v %v", wait, err)
	}
	s.gone[assets[5].Identity.NativeID] = true
	if wait, err := driver.Wait(context.Background(), request, operation); err != nil || !wait.Done {
		t.Fatalf("workspace readback: %+v %v", wait, err)
	}
	before := len(s.deletes)
	if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != before || !slices.Equal(s.deletes, []string{assets[5].Identity.NativeID, assets[0].Identity.NativeID}) {
		t.Fatalf("native recovery repeated group/child deletes: %v %v", s.deletes, err)
	}
}

func TestMonitorWorkspaceNativeSchemasAndProvenance(t *testing.T) {
	payload, err := os.ReadFile("fixtures/monitor-workspace/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest []map[string]string
	if json.Unmarshal(payload, &manifest) != nil || len(manifest) != 3 {
		t.Fatal("incomplete native workspace evidence")
	}
	for _, entry := range manifest {
		payload, err := os.ReadFile("fixtures/monitor-workspace/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] || !strings.Contains(entry["source_uri"], "/e45039baa985c442877529906e705982a6e0099d/") {
			t.Fatal("native workspace example changed")
		}
	}
	payload, _ = os.ReadFile("catalog/source/swagger.json")
	var sources catalog.RESTDocumentSet
	json.Unmarshal(payload, &sources)
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	root := ""
	for _, source := range sources.Documents {
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(source.Document))
		if err := compiler.AddResource(source.SourceURI, value); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(source.SourceURI, "/2023-04-03/monitoringAccounts_API.json") {
			root = source.SourceURI
			if source.SourceSHA256 != "4aa6210b8356fdd786edcea65d4e5269cc3a7b7baf01669159927d3f4c15fb47" {
				t.Fatal("native workspace Swagger fingerprint changed")
			}
		}
	}
	for name, definition := range map[string]string{"AzureMonitorWorkspacesGet": "AzureMonitorWorkspaceResource", "AzureMonitorWorkspacesListBySubscription": "AzureMonitorWorkspaceResourceListResult"} {
		schema, err := compiler.Compile(root + "#/definitions/" + definition)
		if err != nil {
			t.Fatal(err)
		}
		body := object(object(object(monitorWorkspaceFixture(t, name)["responses"])["200"])["body"])
		payload, _ := json.Marshal(body)
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
		if err := schema.Validate(value); name == "AzureMonitorWorkspacesListBySubscription" {
			if err == nil || body["nextLink"] != nil {
				t.Fatal("upstream's null nextLink inconsistency was hidden")
			}
			// The retained example has null for a string-only optional field.
			// Native pagination already accepts null as the end of the list.
			delete(body, "nextLink")
			payload, _ = json.Marshal(body)
			value, _ = jsonschema.UnmarshalJSON(bytes.NewReader(payload))
			if err := schema.Validate(value); err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatalf("official native %s schema: %v", name, err)
		}
	}
}

func TestMonitorWorkspaceRejectsChangedScopeMembershipAndProtection(t *testing.T) {
	for _, mode := range []string{"group-owner", "group-owner-disappears", "group-denied", "group-list-denied", "group-list-not-found", "group-list-partial", "group-list-duplicate", "group-list-foreign", "default-omitted", "new-member", "unlisted-survivor", "unresolved-member", "unresolved-association", "root-account", "root-created", "root-network", "root-private-link", "root-default-scope", "default-groups-disagree", "group-is-root-group", "child-immutable", "child-created", "child-config", "new-external-association", "new-member-between-passes", "child-protected", "unknown-protected", "group-lock", "association-denied", "association-list-denied", "forged-impact", "retained-impact", "forged-prerequisite"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := monitorWorkspaceScenario(t)
			request, _ := dnsRequest(t, r, assets, assets[0])
			root := assets[0].Identity.NativeID
			group := assets[1].Identity.NativeID
			rule := assets[2].Identity.NativeID
			link := assets[5].Identity.NativeID
			s.gone[link] = true // Prior deletion completed; test the actual new fault.
			switch mode {
			case "group-owner":
				s.records[group]["managedBy"] = resourceID(monitorWorkspaceType, "different")
			case "group-owner-disappears":
				calls := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.EqualFold(req.URL.Path, group) {
						calls++
						body := map[string]any{}
						for k, v := range s.records[group] {
							body[k] = v
						}
						if calls > 1 {
							delete(body, "managedBy")
						}
						return jsonResponse(200, body, nil), true
					}
					return nil, false
				}
			case "group-denied":
				s.status[group] = 403
			case "group-list-denied":
				s.status[group+"/resources"] = 403
			case "group-list-not-found":
				s.status[group+"/resources"] = 404
			case "group-list-partial":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, group+"/resources") {
						return jsonResponse(200, map[string]any{"value": nil}, nil), true
					}
					return nil, false
				}
			case "group-list-duplicate":
				s.lists[group+"/resources"] = append(s.lists[group+"/resources"], s.records[rule])
			case "group-list-foreign":
				s.lists[group+"/resources"] = append(s.lists[group+"/resources"], map[string]any{"id": resourceID(vmType, "foreign"), "type": vmType})
			case "default-omitted":
				s.lists[group+"/resources"] = s.lists[group+"/resources"][1:]
			case "new-member":
				value := map[string]any{"id": group + "/providers/Contoso.Unknown/things/new", "type": "Contoso.Unknown/things"}
				s.lists[group+"/resources"] = append(s.lists[group+"/resources"], value)
			case "unlisted-survivor":
				s.lists[group+"/resources"] = s.lists[group+"/resources"][:2]
				s.records[assets[4].Identity.NativeID] = nil
				value := nativeResource(dataCollectionEndpointType, "other", "eastus", map[string]any{"immutableId": "test"})
				value["id"] = group + "/providers/Microsoft.Insights/dataCollectionEndpoints/other"
				s.add(value, dataCollectionVersion)
				extra := dnsAsset(t, r, value)
				request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{Asset: extra, ControllerID: assets[0].ID, Delete: true})
			case "unresolved-member", "unresolved-association":
				s.gone[link] = false
				candidate := slices.Clone(assets)
				index := 4
				if mode == "unresolved-association" {
					index = 5
				}
				candidate = append(candidate[:index], candidate[index+1:]...)
				contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
				contribution, err := contributor.Contribute(context.Background(), "scope", candidate)
				if err != nil || len(contribution.Unresolved) != 1 {
					t.Fatalf("missing inventory hidden: %+v %v", contribution, err)
				}
				return
			case "root-account":
				object(s.records[root]["properties"])["accountId"] = "recreated"
			case "root-created":
				object(s.records[root]["systemData"])["createdAt"] = "2026-09-10T00:00:00Z"
			case "root-network":
				object(s.records[root]["properties"])["publicNetworkAccess"] = "Disabled"
			case "root-private-link":
				object(array(object(s.records[root]["properties"])["privateEndpointConnections"])[0])["id"] = root + "/privateEndpointConnections/changed"
			case "root-default-scope":
				object(object(s.records[root]["properties"])["defaultIngestionSettings"])["dataCollectionRuleResourceId"] = resourceID(dataCollectionRuleType, "elsewhere")
			case "default-groups-disagree":
				object(object(s.records[root]["properties"])["defaultIngestionSettings"])["dataCollectionEndpointResourceId"] = resourceID(dataCollectionEndpointType, "elsewhere")
				assets[0] = dnsAsset(t, r, s.records[root])
				request.Asset = assets[0]
			case "group-is-root-group":
				defaults := object(object(s.records[root]["properties"])["defaultIngestionSettings"])
				for _, field := range []string{"dataCollectionRuleResourceId", "dataCollectionEndpointResourceId"} {
					defaults[field] = strings.ReplaceAll(text(defaults[field]), "purpose-built-owned", "test")
				}
				request.Asset = dnsAsset(t, r, s.records[root])
			case "child-immutable":
				object(s.records[rule]["properties"])["immutableId"] = "recreated"
			case "child-created":
				object(s.records[rule]["systemData"])["createdAt"] = "2026-09-10T00:00:00Z"
			case "child-config":
				object(s.records[rule]["properties"])["description"] = "changed by another writer"
			case "new-external-association":
				data, _ := json.Marshal(s.records[link])
				var other map[string]any
				json.Unmarshal(data, &other)
				other["id"] = link + "-new"
				s.add(other, dataCollectionVersion)
				s.lists[rule+"/associations"] = append(s.lists[rule+"/associations"], other)
			case "new-member-between-passes":
				calls := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, group+"/resources") {
						calls++
						rows := slices.Clone(s.lists[group+"/resources"])
						if calls > 1 {
							rows = append(rows, map[string]any{"id": group + "/providers/Contoso.Unknown/things/new", "type": "Contoso.Unknown/things"})
						}
						return jsonResponse(200, map[string]any{"value": rows}, nil), true
					}
					return nil, false
				}
			case "child-protected":
				s.records[rule]["tags"] = map[string]any{"steward/protected": "true"}
			case "unknown-protected":
				s.records[assets[4].Identity.NativeID]["tags"] = map[string]any{"steward/protected": "true"}
			case "group-lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": group + "/providers/Microsoft.Authorization/locks/protected", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "association-denied":
				s.status[link] = 403
			case "association-list-denied":
				s.status[rule+"/associations"] = 403
			case "forged-impact":
				request.LifecycleImpacts[0].ControllerID = "some-other-root"
			case "retained-impact":
				request.LifecycleImpacts[0].Delete = false
			case "forged-prerequisite":
				request.PrerequisiteDeletions[0].Asset.Identity.NativeID = strings.ReplaceAll(link, testSubscription, "99999999-9999-4999-8999-999999999999")
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatalf("workspace accepted %s: %v %v", mode, s.deletes, err)
			}
		})
	}
}

func TestMonitorWorkspaceOptionalGroupOwnerAndAlreadyAbsentGroup(t *testing.T) {
	for _, mode := range []string{"owner-omitted", "owner-removed-after-review", "group-already-gone"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := monitorWorkspaceScenario(t)
			group := assets[1].Identity.NativeID
			if mode == "owner-omitted" {
				delete(s.records[group], "managedBy")
				assets[1] = dnsAsset(t, r, s.records[group])
			}
			request, _ := dnsRequest(t, r, assets, assets[0])
			s.gone[assets[5].Identity.NativeID] = true
			if mode == "owner-removed-after-review" {
				delete(s.records[group], "managedBy")
			}
			if mode == "group-already-gone" {
				for _, value := range assets[1:5] {
					s.gone[value.Identity.NativeID] = true
				}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", assets[0])
			_, err := driver.Execute(context.Background(), request)
			if mode == "owner-removed-after-review" {
				if err == nil || len(s.deletes) != 0 {
					t.Fatalf("changed reviewed group owner accepted: %v %v", s.deletes, err)
				}
				return
			}
			if err != nil || !slices.Equal(s.deletes, []string{assets[0].Identity.NativeID}) {
				t.Fatalf("native optional/gone group: %v %v", s.deletes, err)
			}
		})
	}
}

func TestMonitorWorkspaceMicrosoftCLIReadResponses(t *testing.T) {
	payload, err := os.ReadFile("fixtures/monitor-workspace/cli-read-recordings.json")
	if err != nil {
		t.Fatal(err)
	}
	var recording map[string]any
	if json.Unmarshal(payload, &recording) != nil || recording["source_sha256"] != "dcb807788e905124f7e77fa36be0d8fced7a5235a8c4213e9808f7e8e457454c" || !strings.Contains(text(recording["source_uri"]), "/27554ab8a5375aab7529e6862934bf26afbb8762/") {
		t.Fatal("native CLI provenance changed")
	}
	rows := array(recording["recordings"])
	if len(rows) != 2 {
		t.Fatal("native GET/list evidence missing")
	}
	for _, row := range rows {
		if object(row)["method"] != "GET" || object(row)["status"] != float64(200) || !strings.HasSuffix(text(object(row)["uri"]), "api-version=2023-04-03") {
			t.Fatal("native CLI read operation changed")
		}
	}
	// Read/list payloads are native; only the subscription is rebound. Surrounding
	// permission checks and already-absent managed resources are protocol fixtures.
	encoded, _ := json.Marshal(object(rows[0])["body"])
	encoded = bytes.ReplaceAll(encoded, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	var raw map[string]any
	json.Unmarshal(encoded, &raw)
	s := newDNSScenario()
	s.add(raw, monitorWorkspaceVersion)
	id := strings.ToLower(text(raw["id"]))
	root := "/subscriptions/" + testSubscription
	resourceGroup := strings.Join(strings.Split(id, "/")[:5], "/")
	encoded, _ = json.Marshal(object(rows[1])["body"])
	encoded = bytes.ReplaceAll(encoded, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	var listed map[string]any
	json.Unmarshal(encoded, &listed)
	s.lists[root+"/providers/microsoft.monitor/accounts"] = array(listed["value"])
	s.version[root+"/providers/microsoft.monitor/accounts"] = monitorWorkspaceVersion
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": resourceGroup, "type": groupType}}
	r := s.runtime(t)
	batch, err := r.List(context.Background(), productRequest(r, monitorWorkspaceType))
	if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].NativeID != id || batch.Items[0].Location != "westus" {
		t.Fatalf("native CLI inventory: %+v %v", batch, err)
	}
	group, err := controllerResourceGroup(testSubscription, monitorWorkspaceType, object(raw["properties"]))
	if err != nil || !strings.HasSuffix(group, "/resourcegroups/ma_ac000002_westus_managed") {
		t.Fatalf("native ingestion group: %s %v", group, err)
	}
	s.gone[group] = true
	for _, field := range []string{"dataCollectionRuleResourceId", "dataCollectionEndpointResourceId"} {
		s.gone[strings.ToLower(text(object(object(raw["properties"])["defaultIngestionSettings"])[field]))] = true
	}
	value := dnsAsset(t, r, raw)
	request, _ := dnsRequest(t, r, []asset.Asset{value}, value)
	driver, _ := r.ResolveAction(context.Background(), "connection", value)
	result, err := driver.Execute(context.Background(), request)
	if err != nil || len(s.deletes) != 1 {
		t.Fatalf("workspace with absent native defaults: %v %v", s.deletes, err)
	}
	if wait, err := driver.Wait(context.Background(), request, result); err != nil || !wait.Done {
		t.Fatalf("recorded response shape failed native absence checks: %+v %v", wait, err)
	}
}

func TestMonitorWorkspaceNativeDeleteHeadersAndPolling(t *testing.T) {
	for _, mode := range []string{"status", "location", "both", "failed", "expired"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := monitorWorkspaceScenario(t)
			request, _ := dnsRequest(t, r, assets, assets[0])
			s.gone[assets[5].Identity.NativeID] = true
			driver, _ := r.ResolveAction(context.Background(), "connection", assets[0])
			native := object(object(object(monitorWorkspaceFixture(t, "AzureMonitorWorkspacesDelete")["responses"])["202"])["headers"])
			headers := http.Header{}
			for name, value := range native {
				if driver.(*action).validateOperationURL(text(value)) == nil {
					t.Fatal("upstream deletion example's foreign subscription accepted")
				}
				endpoint := strings.ReplaceAll(strings.ReplaceAll(text(value), "00000000-0000-0000-0000-000000000000", testSubscription), "/resourceGroups/myResourceGroup/", "/resourceGroups/test/")
				headers.Set(name, endpoint)
			}
			if mode == "location" || mode == "expired" {
				headers.Del("Azure-AsyncOperation")
			}
			if mode == "status" || mode == "failed" {
				headers.Del("Location")
			}
			target := operationLocation(headers)
			polls := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					if !strings.EqualFold(req.URL.Path, assets[0].Identity.NativeID) || req.URL.Query().Get("api-version") != monitorWorkspaceVersion || req.Header.Get("If-Match") != "" || req.ContentLength > 0 {
						t.Fatalf("invalid native workspace delete: %s", req.URL)
					}
					s.deletes = append(s.deletes, strings.ToLower(req.URL.Path))
					return jsonResponse(202, nil, headers), true
				}
				if req.URL.String() == target {
					polls++
					if mode == "expired" {
						return jsonResponse(404, nil, nil), true
					}
					if mode == "failed" {
						return jsonResponse(200, map[string]any{"status": "Failed", "error": map[string]any{"code": "DeletionFailed"}}, nil), true
					}
					if polls == 1 {
						return jsonResponse(202, map[string]any{"status": "InProgress"}, nil), true
					}
					if mode == "location" {
						return jsonResponse(204, nil, nil), true
					}
					return jsonResponse(200, map[string]any{"status": "Succeeded"}, nil), true
				}
				return nil, false
			}
			op, err := driver.Execute(context.Background(), request)
			if err != nil || op.ProviderOperationID != target {
				t.Fatalf("native delete headers: %+v %v", op, err)
			}
			wait, err := driver.Wait(context.Background(), request, op)
			if mode == "failed" {
				if err == nil || wait.Done {
					t.Fatalf("native failure became success: %+v %v", wait, err)
				}
				return
			}
			if err != nil || wait.Done {
				t.Fatalf("accepted operation proved premature absence: %+v %v", wait, err)
			}
			for _, value := range assets {
				s.gone[value.Identity.NativeID] = true
			}
			if wait, err := driver.Wait(context.Background(), request, op); err != nil || !wait.Done {
				t.Fatalf("native polling plus resource readback: %+v %v", wait, err)
			}
		})
	}
}
