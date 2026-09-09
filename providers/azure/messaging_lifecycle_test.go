package azure

import (
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
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Explicit native URI suffixes keep discovery tests independent of the catalog
// and the production cascade table. Default rule sets have no native DELETE.
var messagingCases = []struct {
	kind, suffix, fixture string
	readOnly              bool
}{
	{serviceBusNamespaceType, "", "servicebus-NameSpaces-SBNameSpaceGet.json", false},
	{serviceBusQueueType, "/queues/queue", "servicebus-Queues-SBQueueGet.json", false},
	{serviceBusQueueType + "/authorizationRules", "/queues/queue/authorizationRules/auth", "servicebus-Queues-SBQueueAuthorizationRuleGet.json", false},
	{serviceBusTopicType, "/topics/topic", "servicebus-Topics-SBTopicGet.json", false},
	{serviceBusSubscriptionType, "/topics/topic/subscriptions/subscription", "servicebus-Subscriptions-SBSubscriptionGet.json", false},
	{serviceBusRuleType, "/topics/topic/subscriptions/subscription/rules/$Default", "servicebus-Rules-RuleGet.json", false},
	{serviceBusTopicType + "/authorizationRules", "/topics/topic/authorizationRules/auth", "servicebus-Topics-SBTopicAuthorizationRuleGet.json", false},
	{serviceBusNamespaceType + "/authorizationRules", "/authorizationRules/RootManageSharedAccessKey", "servicebus-NameSpaces-SBNameSpaceAuthorizationRuleGet.json", false},
	{serviceBusRecoveryType, "/disasterRecoveryConfigs/alias", "servicebus-disasterRecoveryConfigs-SBAliasGet.json", false},
	{serviceBusRecoveryType + "/authorizationRules", "/disasterRecoveryConfigs/alias/authorizationRules/auth", "servicebus-disasterRecoveryConfigs-SBAliasAuthorizationRuleGet.json", true},
	{serviceBusMigrationType, "/migrationConfigurations/$default", "servicebus-Migrationconfigurations-SBMigrationconfigurationGet.json", false},
	{serviceBusNamespaceType + "/privateEndpointConnections", "/privateEndpointConnections/connection", "servicebus-NameSpaces-PrivateEndPointConnectionGet.json", false},
	{serviceBusNamespaceType + "/networkRuleSets", "/networkRuleSets/default", "servicebus-NameSpaces-VirtualNetworkRule-SBNetworkRuleSetGet.json", true},
	{eventHubNamespaceType, "", "eventhub-NameSpaces-EHNameSpaceGet.json", false},
	{eventHubType, "/eventhubs/hub", "eventhub-EventHubs-EHEventHubGet.json", false},
	{eventHubConsumerGroupType, "/eventhubs/hub/consumergroups/$Default", "eventhub-ConsumerGroup-EHConsumerGroupGet.json", false},
	{eventHubType + "/authorizationRules", "/eventhubs/hub/authorizationRules/auth", "eventhub-EventHubs-EHEventHubAuthorizationRuleGet.json", false},
	{eventHubNamespaceType + "/authorizationRules", "/authorizationRules/RootManageSharedAccessKey", "eventhub-NameSpaces-EHNameSpaceAuthorizationRuleGet.json", false},
	{eventHubRecoveryType, "/disasterRecoveryConfigs/alias", "eventhub-disasterRecoveryConfigs-EHAliasGet.json", false},
	{eventHubRecoveryType + "/authorizationRules", "/disasterRecoveryConfigs/alias/authorizationRules/auth", "eventhub-disasterRecoveryConfigs-EHAliasAuthorizationRuleGet.json", true},
	{eventHubNamespaceType + "/schemagroups", "/schemagroups/schemas", "eventhub-SchemaRegistry-SchemaRegistryGet.json", false},
	{eventHubNamespaceType + "/applicationGroups", "/applicationGroups/application", "eventhub-ApplicationGroup-ApplicationGroupGet.json", false},
	{eventHubNamespaceType + "/privateEndpointConnections", "/privateEndpointConnections/connection", "eventhub-NameSpaces-PrivateEndPointConnectionGet.json", false},
	{eventHubNamespaceType + "/networkRuleSets", "/networkRuleSets/default", "eventhub-NameSpaces-VirtualNetworkRule-EHNetworkRuleSetGet.json", true},
	{eventHubNamespaceType + "/networkSecurityPerimeterConfigurations", "/networkSecurityPerimeterConfigurations/perimeter", "", true},
}

func messagingScenario(t *testing.T, namespaceKind string) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	root := strings.ToLower(resourceID(namespaceKind, "messages"))
	canonical := []map[string]any{}
	for _, tc := range messagingCases {
		if !strings.HasPrefix(tc.kind, namespaceKind) {
			continue
		}
		id := root + strings.ToLower(tc.suffix)
		raw := map[string]any{"id": id, "name": last(id), "type": tc.kind, "properties": map[string]any{"provisioningState": "Succeeded", "createdAt": "2026-01-01T00:00:00Z"}}
		if tc.suffix == "" {
			raw["location"] = "East US"
		}
		if tc.kind == serviceBusRecoveryType || tc.kind == eventHubRecoveryType {
			object(raw["properties"])["role"] = "PrimaryNotReplicating"
			object(raw["properties"])["pendingReplicationOperationsCount"] = 0
		}
		if tc.kind == serviceBusMigrationType {
			object(raw["properties"])["migrationState"] = "Active"
		}
		canonical = append(canonical, raw)
		// Preserve the irregular ID/type spelling from the official GETs.
		payload := map[string]any{}
		for k, v := range raw {
			payload[k] = v
		}
		switch tc.kind {
		case serviceBusRecoveryType, eventHubRecoveryType:
			payload["id"] = strings.Replace(id, "/disasterrecoveryconfigs/", "/disasterRecoveryConfig/", 1)
			payload["type"] = namespaceKind + "/DisasterRecoveryConfig"
		case serviceBusMigrationType:
			payload["id"] = strings.Replace(id, "/migrationconfigurations/", "/migrationConfigs/", 1)
			payload["type"] = serviceBusNamespaceType + "/disasterrecoveryconfigs"
		case serviceBusNamespaceType + "/networkRuleSets", eventHubNamespaceType + "/networkRuleSets":
			payload["id"] = strings.Replace(id, "/networkrulesets/", "/networkruleset/", 1)
			payload["type"] = namespaceKind + "/NetworkRuleSet"
		case serviceBusRecoveryType + "/authorizationRules":
			payload["type"] = serviceBusNamespaceType + "/DisasterRecoveryConfig/AuthorizationRules"
		case eventHubRecoveryType + "/authorizationRules":
			payload["type"] = eventHubNamespaceType + "/AuthorizationRules"
		case serviceBusNamespaceType + "/authorizationRules":
			payload["id"] = id + "/"
		}
		s.records[id], s.version[id] = payload, "2024-01-01"
		collection := id[:strings.LastIndex(id, "/")]
		if tc.suffix == "" {
			collection = "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(namespaceKind)
		}
		s.lists[collection] = append(s.lists[collection], payload)
		s.version[collection] = "2024-01-01"
	}
	s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{map[string]any{"id": "/subscriptions/" + testSubscription + "/resourceGroups/test", "type": groupType, "location": "eastus"}}
	r := s.runtime(t)
	assets := []asset.Asset{}
	for _, raw := range canonical {
		assets = append(assets, dnsAsset(t, r, raw))
	}
	return s, r, assets
}

func TestMessagingNativeProductRoutesAndIndependentDeletion(t *testing.T) {
	for _, tc := range messagingCases {
		t.Run(tc.kind, func(t *testing.T) {
			namespaceKind := serviceBusNamespaceType
			if strings.HasPrefix(tc.kind, eventHubNamespaceType) {
				namespaceKind = eventHubNamespaceType
			}
			s, r, assets := messagingScenario(t, namespaceKind)
			var target asset.Asset
			for _, value := range assets {
				if value.Identity.NativeType == tc.kind {
					target = value
				}
			}
			request := productRequest(r, tc.kind)
			// A parent page with no values must still follow its nextLink.
			pages := 0
			collection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(namespaceKind)
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "GET" && strings.EqualFold(req.URL.Path, collection) && req.URL.Query().Get("$skiptoken") == "" {
					pages++
					return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": apiURL(collection, "2024-01-01") + "&%24skiptoken=two"}, nil), true
				}
				return nil, false
			}
			items := []contracts.InventoryItem{}
			for i := 0; i < 5; i++ {
				batch, err := r.List(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				items = append(items, batch.Items...)
				if batch.Complete {
					break
				}
				request.Cursor = batch.NextCursor
				if i == 4 {
					t.Fatal("pagination did not complete")
				}
			}
			if len(items) != 1 || pages == 0 || items[0].NativeID != target.Identity.NativeID || items[0].Location != "eastus" || items[0].Actionable == nil || *items[0].Actionable == tc.readOnly {
				t.Fatalf("native inventory: %+v pages=%d", items, pages)
			}
			s.handle = nil
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if tc.readOnly {
				if err == nil {
					t.Fatal("invented native DELETE for read-only configuration")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			action := contracts.ActionRequest{Asset: target, Action: "delete"}
			if HasServiceCascade(tc.kind) {
				action, _ = dnsRequest(t, r, assets, target)
			}
			operation, err := driver.Execute(context.Background(), action)
			if tc.kind == namespaceKind+"/authorizationRules" {
				if err == nil || len(s.deletes) != 0 || items[0].Normalized["cleanup_controller_only"] != true {
					t.Fatal("default namespace authorization rule allowed independent deletion")
				}
				return
			}
			if err != nil || len(s.deletes) != 1 || s.deletes[0] != target.Identity.NativeID {
				t.Fatalf("native delete %+v %v", s.deletes, err)
			}
			for _, impact := range action.LifecycleImpacts {
				s.gone[impact.Asset.Identity.NativeID] = true
			}
			if wait, err := driver.Wait(context.Background(), action, operation); err != nil || !wait.Done {
				t.Fatalf("native wait %+v %v", wait, err)
			}
			for _, value := range assets {
				if value.ID != target.ID && !strings.HasPrefix(value.Identity.NativeID, target.Identity.NativeID+"/") && s.gone[value.Identity.NativeID] {
					t.Fatal("independent delete removed sibling or parent")
				}
			}
		})
	}
}

func TestMessagingNamespaceCascadeRetentionAndRestart(t *testing.T) {
	for _, kind := range []string{serviceBusNamespaceType, eventHubNamespaceType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := messagingScenario(t, kind)
			request, input := dnsRequest(t, r, assets, assets[0])
			if len(request.LifecycleImpacts) != len(assets)-1 {
				t.Fatalf("incomplete namespace impacts: %+v", request)
			}
			for _, child := range assets[1:] {
				input.RequestOptions = map[asset.AssetID]map[string]any{assets[0].ID: {"retain_resources": []string{child.Identity.NativeID}}}
				result, err := plan.Solve(input)
				if err != nil || len(result.Blockers) == 0 {
					t.Fatalf("retained %s accepted: %+v %v", child.Identity.NativeType, result, err)
				}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
			operation, err := driver.Execute(context.Background(), request)
			if err != nil || len(s.deletes) != 1 {
				t.Fatalf("delete=%v error=%v", s.deletes, err)
			}
			encoded, _ := json.Marshal(request)
			var restored contracts.ActionRequest
			if err := json.Unmarshal(encoded, &restored); err != nil {
				t.Fatal(err)
			}
			encoded, _ = json.Marshal(operation)
			if err := json.Unmarshal(encoded, &operation); err != nil {
				t.Fatal(err)
			}
			driver, _ = s.runtime(t).ResolveAction(context.Background(), "connection", restored.Asset)
			for _, child := range assets[1:] {
				if wait, err := driver.Wait(context.Background(), restored, operation); err != nil || wait.Done {
					t.Fatalf("child %s disappeared prematurely: %+v %v", child.Identity.NativeType, wait, err)
				}
				if _, err := driver.Execute(context.Background(), restored); err != nil || len(s.deletes) != 1 {
					t.Fatalf("restart repeated DELETE: %v", err)
				}
				s.gone[child.Identity.NativeID] = true
			}
			if wait, err := driver.Wait(context.Background(), restored, operation); err != nil || !wait.Done {
				t.Fatalf("namespace completion=%+v %v", wait, err)
			}
		})
	}
}

func TestMessagingNamespaceRejectsUnreviewedChildren(t *testing.T) {
	for _, kind := range []string{serviceBusNamespaceType, eventHubNamespaceType} {
		for _, mode := range []string{"missing-inventory", "stale-inventory", "new-child", "list-403", "list-404", "list-206", "get-403", "get-404", "get-206", "foreign-id", "foreign-type", "duplicate-alias", "protected-child", "locked-child", "child-recreated", "parent-recreated", "missing-impact", "retained-impact", "foreign-controller", "foreign-connection", "foreign-partition", "foreign-kind", "duplicate-impact"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, r, assets := messagingScenario(t, kind)
				root := assets[0]
				if mode == "missing-inventory" || mode == "stale-inventory" {
					if mode == "missing-inventory" {
						assets = assets[:len(assets)-1]
					} else {
						assets[1].Normalized["_arm_generation"] = "old"
					}
					contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
					result, err := contributor.Contribute(context.Background(), "scope", assets)
					if err == nil && len(result.Unresolved) == 0 {
						t.Fatal("incomplete discovery accepted")
					}
					return
				}
				request, _ := dnsRequest(t, r, assets, root)
				childID := root.Identity.NativeID + "/networkrulesets/default"
				collection := root.Identity.NativeID + "/networkrulesets"
				child := s.records[childID]
				switch mode {
				case "new-child":
					copy := map[string]any{}
					for k, v := range child {
						copy[k] = v
					}
					copy["id"] = root.Identity.NativeID + "/networkRuleSet/new"
					s.lists[collection] = append(s.lists[collection], copy)
				case "list-403":
					s.status[collection] = 403
				case "list-404":
					s.status[collection] = 404
				case "list-206":
					s.status[collection] = 206
				case "get-403":
					s.status[childID] = 403
				case "get-404":
					s.status[childID] = 404
				case "get-206":
					s.status[childID] = 206
				case "foreign-id":
					child["id"] = strings.Replace(text(child["id"]), "/messages/", "/foreign/", 1)
				case "foreign-type":
					child["type"] = nicType
				case "duplicate-alias":
					copy := map[string]any{}
					for k, v := range child {
						copy[k] = v
					}
					copy["id"] = childID
					s.lists[collection] = append(s.lists[collection], copy)
				case "protected-child":
					child["tags"] = map[string]any{"steward:protected": "true"}
				case "locked-child":
					s.handle = func(req *http.Request) (*http.Response, bool) {
						if strings.Contains(req.URL.Path, "/Microsoft.Authorization/locks") || strings.Contains(req.URL.Path, "/microsoft.authorization/locks") {
							return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": childID + "/providers/Microsoft.Authorization/locks/protect", "properties": map[string]any{"level": "CanNotDelete"}}}}, nil), true
						}
						return nil, false
					}
				case "child-recreated":
					object(child["properties"])["createdAt"] = "2026-02-01T00:00:00Z"
				case "parent-recreated":
					object(s.records[root.Identity.NativeID]["properties"])["createdAt"] = "2026-02-01T00:00:00Z"
				case "missing-impact":
					request.LifecycleImpacts = request.LifecycleImpacts[:len(request.LifecycleImpacts)-1]
				case "retained-impact":
					request.LifecycleImpacts[0].Delete = false
				case "foreign-controller":
					request.LifecycleImpacts[0].ControllerID = "foreign"
				case "foreign-connection":
					request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "foreign"
				case "foreign-partition":
					request.LifecycleImpacts[0].Asset.Identity.Partition = "foreign"
				case "foreign-kind":
					request.LifecycleImpacts[0].Asset.Identity.NativeType = nicType
				case "duplicate-impact":
					request.LifecycleImpacts = append(request.LifecycleImpacts, request.LifecycleImpacts[0])
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", root)
				if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
					t.Fatalf("unsafe namespace DELETE accepted for %s: %v", mode, err)
				}
			})
		}
	}
}

func TestMessagingOfficialResponseAliases(t *testing.T) {
	manifestData, err := os.ReadFile("fixtures/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest []map[string]string
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	for _, tc := range messagingCases {
		if tc.fixture == "" {
			continue
		}
		t.Run(tc.kind, func(t *testing.T) {
			data, err := os.ReadFile("fixtures/" + tc.fixture)
			if err != nil {
				t.Fatal(err)
			}
			provenance := false
			for _, entry := range manifest {
				if entry["file"] == tc.fixture {
					provenance = entry["source_sha256"] == fmt.Sprintf("%x", sha256.Sum256(data)) && strings.HasPrefix(entry["source_uri"], "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/main/specification/")
				}
			}
			if !provenance {
				t.Fatal("official messaging fixture provenance changed")
			}
			var example map[string]any
			if err := json.Unmarshal(data, &example); err != nil {
				t.Fatal(err)
			}
			raw := object(object(object(example["responses"])["200"])["body"])
			parts := strings.Split(strings.TrimRight(text(raw["id"]), "/"), "/")
			parts[2] = testSubscription
			raw["id"] = strings.Join(parts, "/")
			// The expected request collection comes from the explicit native type,
			// independently of responseID and of ResponseIDTypes metadata.
			parts[len(parts)-2] = last(tc.kind)
			id := strings.ToLower(strings.Join(parts, "/"))
			if !validResourceResponse(response{status: 200, data: raw}, id, tc.kind) {
				t.Fatalf("official response rejected: %+v", raw)
			}
			for _, index := range []int{2, 4, 8, len(parts) - 1} {
				foreign := slices.Clone(parts)
				foreign[index] += "-foreign"
				copy := map[string]any{}
				for k, v := range raw {
					copy[k] = v
				}
				copy["id"] = strings.Join(foreign, "/")
				if validResourceResponse(response{status: 200, data: copy}, id, tc.kind) {
					t.Fatal("response alias permitted another resource")
				}
			}
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || !strings.EqualFold(req.URL.Path, id) || req.URL.Query().Get("api-version") != "2024-01-01" {
					t.Fatalf("wrong native GET %s", req.URL)
				}
				return jsonResponse(200, raw, nil), nil
			})
			c, _ := r.resolve(context.Background(), "connection")
			kind, _ := findType(tc.kind)
			endpoint, err := c.resourceURL(kind, id)
			if err != nil {
				t.Fatal(err)
			}
			res, err := c.request(context.Background(), "GET", endpoint)
			if err != nil || !validResourceResponse(res, id, tc.kind) {
				t.Fatalf("native response=%+v %v", res, err)
			}
		})
	}
}

func TestAzureResponseIDAliasCannotChangeAncestors(t *testing.T) {
	for _, alias := range []string{"Microsoft.Other/namespaces/networkRuleSet", "Microsoft.EventHub/other/networkRuleSet", "Microsoft.EventHub/namespaces/networkRuleSets", "Microsoft.EventHub/namespaces/networkRuleSets/children", "Microsoft.EventHub/namespaces/", "Microsoft.EventHub"} {
		if validResponseIDType(eventHubNamespaceType+"/networkRuleSets", alias) {
			t.Fatalf("unsafe response alias accepted: %s", alias)
		}
	}
	if !validResponseIDType(eventHubNamespaceType+"/networkRuleSets", eventHubNamespaceType+"/NetworkRuleSet") {
		t.Fatal("native leaf alias rejected")
	}
}
