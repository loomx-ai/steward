package azure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func clusterFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	file := "eventhub-Clusters-" + name + ".json"
	data, err := os.ReadFile("fixtures/" + file)
	if err != nil {
		t.Fatal(err)
	}
	manifestData, err := os.ReadFile("fixtures/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest []map[string]string
	if err := json.Unmarshal(manifestData, &manifest); err != nil {
		t.Fatal(err)
	}
	verified := false
	for _, source := range manifest {
		if source["file"] == file {
			verified = source["source_sha256"] == fmt.Sprintf("%x", sha256.Sum256(data)) && strings.HasPrefix(source["source_uri"], "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/main/specification/eventhub/")
		}
	}
	if !verified {
		t.Fatal("cluster fixture provenance changed")
	}
	var example map[string]any
	if err := json.Unmarshal(data, &example); err != nil {
		t.Fatal(err)
	}
	// Replace only the subscription placeholder, retaining native response shapes.
	data = []byte(strings.ReplaceAll(string(data), text(object(example["parameters"])["subscriptionId"]), testSubscription))
	if err := json.Unmarshal(data, &example); err != nil {
		t.Fatal(err)
	}
	return object(object(object(example["responses"])["200"])["body"])
}

func eventHubClusterScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	cluster := clusterFixture(t, "ClusterGet")
	clusterID := strings.ToLower(text(cluster["id"]))
	s.add(cluster, "2024-01-01")
	s.records[clusterID+"/quotaconfiguration/default"] = clusterFixture(t, "ClusterQuotaConfigurationGet")
	s.version[clusterID+"/quotaconfiguration/default"] = "2024-01-01"
	s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.eventhub/clusters"] = []any{cluster}
	s.version["/subscriptions/"+testSubscription+"/providers/microsoft.eventhub/clusters"] = "2024-01-01"
	members := array(clusterFixture(t, "ListNamespacesInClusterGet")["value"])
	s.lists[clusterID+"/namespaces"], s.version[clusterID+"/namespaces"] = members, "2024-01-01"
	raw := []map[string]any{cluster}
	groups := map[string]bool{}
	for _, entry := range append([]any{cluster}, members...) {
		parts := strings.Split(strings.ToLower(text(object(entry)["id"])), "/")
		group := strings.Join(parts[:5], "/")
		if !groups[group] {
			groups[group] = true
			s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = append(s.lists["/subscriptions/"+testSubscription+"/resourcegroups"], map[string]any{"id": group, "type": groupType, "name": parts[4], "location": "southcentralus"})
		}
	}
	for _, member := range members {
		id := strings.ToLower(text(object(member)["id"]))
		ns := map[string]any{"id": id, "name": last(id), "type": eventHubNamespaceType, "location": "South Central US", "properties": map[string]any{"createdAt": "2020-01-01T00:00:00Z", "provisioningState": "Succeeded", "clusterArmId": text(cluster["id"])}}
		s.add(ns, "2024-01-01")
		raw = append(raw, ns)
		s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.eventhub/namespaces"] = append(s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.eventhub/namespaces"], ns)
		// Literal service collections, independent of the production cascade table.
		for _, collection := range []string{"eventhubs", "authorizationrules", "disasterrecoveryconfigs", "schemagroups", "applicationgroups", "privateendpointconnections", "networkrulesets", "networksecurityperimeterconfigurations"} {
			s.lists[id+"/"+collection], s.version[id+"/"+collection] = []any{}, "2024-01-01"
		}
		hub := map[string]any{"id": id + "/eventhubs/events", "name": "events", "type": eventHubType, "properties": map[string]any{"createdAt": "2020-01-01T00:00:00Z"}}
		consumer := map[string]any{"id": id + "/eventhubs/events/consumergroups/$default", "name": "$Default", "type": eventHubConsumerGroupType, "properties": map[string]any{"createdAt": "2020-01-01T00:00:00Z"}}
		for _, child := range []map[string]any{hub, consumer} {
			s.add(child, "2024-01-01")
			childID := text(child["id"])
			collection := childID[:strings.LastIndex(childID, "/")]
			s.lists[collection], s.version[collection] = []any{child}, "2024-01-01"
			raw = append(raw, child)
		}
		s.lists[id+"/eventhubs/events/authorizationrules"] = []any{}
		s.version[id+"/eventhubs/events/authorizationrules"] = "2024-01-01"
	}
	r := s.runtime(t)
	assets := []asset.Asset{}
	for _, value := range raw {
		assets = append(assets, dnsAsset(t, r, value))
	}
	return s, r, assets
}

func TestEventHubClusterNativeInventoryAndNamespacePrerequisites(t *testing.T) {
	for _, selection := range []string{"cluster", "cluster-and-namespace", "namespace"} {
		t.Run(selection, func(t *testing.T) {
			s, r, assets := eventHubClusterScenario(t)
			root, ns := assets[0], assets[1]
			if !slices.Contains(ns.Normalized[referenceKey(eventHubClusterType)].([]string), root.Identity.NativeID) {
				t.Fatal("namespace omitted native cluster dependency")
			}
			request, input := dnsRequest(t, r, assets, root)
			if len(request.LifecycleImpacts) != 0 || len(request.PrerequisiteDeletions) != 3 {
				t.Fatalf("wrong cluster cleanup model: %+v", request)
			}
			if selection == "cluster-and-namespace" {
				input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, ns.ID)
			}
			if selection == "namespace" {
				input.ResolvedAssetIDs = []asset.AssetID{ns.ID}
			}
			result, err := plan.Solve(input)
			if err != nil || len(result.Blockers) != 0 {
				t.Fatalf("plan=%+v %v", result, err)
			}
			expectedSteps := 4
			if selection == "namespace" {
				expectedSteps = 1
			}
			if len(result.Steps) != expectedSteps {
				t.Fatalf("duplicate or missing steps: %+v", result.Steps)
			}
			pages := 0
			collection := "/subscriptions/" + testSubscription + "/providers/microsoft.eventhub/clusters"
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "GET" && strings.EqualFold(req.URL.Path, collection) && req.URL.Query().Get("$skiptoken") == "" {
					pages++
					return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": apiURL(collection, "2024-01-01") + "&%24skiptoken=two"}, nil), true
				}
				return nil, false
			}
			inventory := productRequest(r, eventHubClusterType)
			items := []contracts.InventoryItem{}
			for i := 0; i < 5; i++ {
				batch, err := r.List(context.Background(), inventory)
				if err != nil {
					t.Fatal(err)
				}
				items = append(items, batch.Items...)
				if batch.Complete {
					break
				}
				inventory.Cursor = batch.NextCursor
			}
			if pages == 0 || len(items) != 1 || items[0].NativeID != root.Identity.NativeID || items[0].Location != "southcentralus" || object(items[0].Normalized["sku"])["name"] != "Dedicated" || items[0].Normalized["metricId"] != "SN6-008" || object(items[0].Normalized["quotaSettings"])["namespaces-per-cluster-quota"] != "200" {
				t.Fatalf("native cluster inventory=%+v", items)
			}
			s.handle = nil
			for _, step := range result.Steps {
				if step.AssetID == root.ID {
					continue
				}
				var current asset.Asset
				for _, value := range assets {
					if value.ID == step.AssetID {
						current = value
					}
				}
				childRequest := servicePlanRequest(result, assets, current)
				if len(childRequest.LifecycleImpacts) != 2 {
					t.Fatalf("namespace omitted nested review: %+v", childRequest)
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", current)
				op, err := driver.Execute(context.Background(), childRequest)
				if err != nil {
					t.Fatal(err)
				}
				// Persist action and request, restore the runtime while descendants remain.
				data, _ := json.Marshal(childRequest)
				var restored contracts.ActionRequest
				_ = json.Unmarshal(data, &restored)
				data, _ = json.Marshal(op)
				_ = json.Unmarshal(data, &op)
				driver, _ = s.runtime(t).ResolveAction(context.Background(), "connection", restored.Asset)
				wait, err := driver.Wait(context.Background(), restored, op)
				if err != nil || wait.Done {
					t.Fatalf("namespace completed before descendants: %+v %v", wait, err)
				}
				for _, child := range childRequest.LifecycleImpacts {
					s.gone[child.Asset.Identity.NativeID] = true
				}
				if wait, err = driver.Wait(context.Background(), restored, op); err != nil || !wait.Done {
					t.Fatalf("namespace completion: %+v %v", wait, err)
				}
			}
			if selection == "namespace" {
				if len(s.deletes) != 1 || s.gone[root.Identity.NativeID] || s.gone[assets[4].Identity.NativeID] {
					t.Fatal("namespace selection deleted cluster or siblings")
				}
				return
			}
			request = servicePlanRequest(result, assets, root)
			var clusterStep plan.CleanupTaskStep
			for _, step := range result.Steps {
				if step.AssetID == root.ID {
					clusterStep = step
				}
			}
			for _, step := range result.Steps {
				if step.AssetID != root.ID && !slices.Contains(clusterStep.DependsOn, step.ID) {
					t.Fatal("cluster does not wait for namespace")
				}
			}
			data, _ := json.Marshal(request)
			var restored contracts.ActionRequest
			_ = json.Unmarshal(data, &restored)
			driver, _ := s.runtime(t).ResolveAction(context.Background(), "connection", restored.Asset)
			operationPath := "/subscriptions/" + testSubscription + "/providers/microsoft.eventhub/locations/southcentralus/operations/cluster-delete"
			finished := false
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" && strings.EqualFold(req.URL.Path, root.Identity.NativeID) {
					if req.Header.Get("X-Ms-Client-Request-Id") == "" {
						t.Fatal("missing native request identity")
					}
					s.deletes = append(s.deletes, root.Identity.NativeID)
					return jsonResponse(202, nil, http.Header{"Azure-Asyncoperation": {apiURL(operationPath, "2024-01-01")}, "X-Ms-Request-Id": {"cluster-delete"}}), true
				}
				if req.Method == "GET" && strings.EqualFold(req.URL.Path, operationPath) {
					state := "InProgress"
					if finished {
						state = "Succeeded"
					}
					return jsonResponse(200, map[string]any{"status": state}, nil), true
				}
				return nil, false
			}
			restored.IdempotencyKey = "cluster-cleanup"
			op, err := driver.Execute(context.Background(), restored)
			if err != nil || op.ProviderRequestID != "cluster-delete" || len(s.deletes) != 4 {
				t.Fatalf("cluster delete=%+v %v %v", op, s.deletes, err)
			}
			data, _ = json.Marshal(op)
			_ = json.Unmarshal(data, &op)
			driver, _ = s.runtime(t).ResolveAction(context.Background(), "connection", restored.Asset)
			if wait, err := driver.Wait(context.Background(), restored, op); err != nil || wait.Done {
				t.Fatalf("pending cluster operation: %+v %v", wait, err)
			}
			finished = true
			if wait, err := driver.Wait(context.Background(), restored, op); err != nil || wait.Done {
				t.Fatalf("cluster completed before root absence: %+v %v", wait, err)
			}
			s.gone[root.Identity.NativeID] = true
			s.gone[ns.Identity.NativeID] = false
			if wait, err := driver.Wait(context.Background(), restored, op); err == nil || wait.Done {
				t.Fatalf("recreated namespace accepted: %+v %v", wait, err)
			}
			s.gone[ns.Identity.NativeID] = true
			if wait, err := driver.Wait(context.Background(), restored, op); err != nil || !wait.Done {
				t.Fatalf("cluster completion: %+v %v", wait, err)
			}
		})
	}
}

func TestEventHubClusterRetainsOrRequiresCompleteInventory(t *testing.T) {
	for _, mode := range []string{"retain-namespace", "retain-consumer", "missing-namespace", "foreign-connection", "foreign-partition", "duplicate-namespace", "missing-link", "changed-link", "recreated-namespace"} {
		t.Run(mode, func(t *testing.T) {
			_, r, assets := eventHubClusterScenario(t)
			if strings.HasPrefix(mode, "retain-") {
				_, input := dnsRequest(t, r, assets, assets[0])
				target := assets[1]
				if mode == "retain-consumer" {
					target = assets[3]
				}
				input.RequestOptions = map[asset.AssetID]map[string]any{assets[0].ID: {"retain_resources": []string{target.Identity.NativeID}}}
				result, err := plan.Solve(input)
				if err != nil || len(result.Blockers) == 0 {
					t.Fatalf("retention accepted: %+v %v", result, err)
				}
				return
			}
			switch mode {
			case "missing-namespace":
				assets = append(assets[:1], assets[2:]...)
			case "foreign-connection":
				assets[1].Identity.ConnectionID = "foreign"
			case "foreign-partition":
				assets[1].Identity.Partition = "foreign"
			case "duplicate-namespace":
				assets = append(assets, assets[1])
			case "missing-link":
				delete(assets[1].Normalized, "clusterArmId")
			case "changed-link":
				assets[1].Normalized["clusterArmId"] = assets[0].Identity.NativeID + "-other"
			case "recreated-namespace":
				assets[1].Normalized["createdAt"] = "1999-01-01T00:00:00Z"
			}
			contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
			result, err := contributor.Contribute(context.Background(), "scope", assets)
			if err == nil && len(result.Unresolved) == 0 {
				t.Fatal("incomplete cluster inventory accepted")
			}
		})
	}
}

func TestEventHubClusterLiveMembershipFailures(t *testing.T) {
	modes := []string{"list-403", "list-404", "list-206", "list-error-body", "list-malformed", "member-403", "member-404", "member-206", "foreign-subscription", "wrong-kind", "duplicate", "bad-link", "missing-link", "nonstring-link", "foreign-detail", "parent-recreated", "member-recreated-during-read", "member-link-during-read", "new-member-final-list", "list-page-cycle"}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := eventHubClusterScenario(t)
			root, ns := assets[0], assets[1]
			c, _ := r.resolve(context.Background(), "connection")
			listPath := root.Identity.NativeID + "/namespaces"
			members := s.lists[listPath]
			switch mode {
			case "list-403":
				s.status[listPath] = 403
			case "list-404":
				s.status[listPath] = 404
			case "list-206":
				s.status[listPath] = 206
			case "member-403":
				s.status[ns.Identity.NativeID] = 403
			case "member-404":
				s.status[ns.Identity.NativeID] = 404
			case "member-206":
				s.status[ns.Identity.NativeID] = 206
			case "foreign-subscription":
				object(members[0])["id"] = strings.Replace(ns.Identity.NativeID, testSubscription, testTenant, 1)
			case "wrong-kind":
				object(members[0])["id"] = resourceID(serviceBusNamespaceType, "foreign")
			case "duplicate":
				s.lists[listPath] = append(members, members[0])
			case "bad-link":
				object(s.records[ns.Identity.NativeID]["properties"])["clusterArmId"] = root.Identity.NativeID + "-foreign"
			case "missing-link":
				delete(object(s.records[ns.Identity.NativeID]["properties"]), "clusterArmId")
			case "nonstring-link":
				object(s.records[ns.Identity.NativeID]["properties"])["clusterArmId"] = map[string]any{"id": root.Identity.NativeID}
			case "foreign-detail":
				s.records[ns.Identity.NativeID]["id"] = ns.Identity.NativeID + "-foreign"
			}
			lists := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "GET" && strings.EqualFold(req.URL.Path, listPath) {
					lists++
					switch mode {
					case "list-error-body":
						return jsonResponse(200, map[string]any{"value": []any{}, "error": map[string]any{"code": "Denied"}}, nil), true
					case "list-malformed":
						return jsonResponse(200, map[string]any{"value": map[string]any{}}, nil), true
					case "list-page-cycle":
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": apiURL(listPath, "2024-01-01")}, nil), true
					}
					if lists == 2 {
						switch mode {
						case "parent-recreated":
							object(s.records[root.Identity.NativeID]["properties"])["createdAt"] = "2020-01-01T00:00:00Z"
						case "member-recreated-during-read":
							object(s.records[ns.Identity.NativeID]["properties"])["createdAt"] = "2021-01-01T00:00:00Z"
						case "member-link-during-read":
							object(s.records[ns.Identity.NativeID]["properties"])["clusterArmId"] = root.Identity.NativeID + "-other"
						case "new-member-final-list":
							s.lists[listPath] = members[1:]
						}
					}
				}
				return nil, false
			}
			// Serialize the initial native parent to avoid sharing the mutable fixture.
			data, _ := json.Marshal(s.records[root.Identity.NativeID])
			var raw map[string]any
			_ = json.Unmarshal(data, &raw)
			_, err := c.serviceChildren(context.Background(), root.Identity, raw)
			if err == nil || len(s.deletes) != 0 {
				t.Fatalf("unsafe membership %s accepted: %v", mode, err)
			}
			// A missing dependent collection/member must never be target absence.
			driver, _ := r.ResolveAction(context.Background(), "connection", root)
			check, err := driver.Preflight(context.Background(), contracts.ActionRequest{Asset: root, Action: "delete"})
			var call *contracts.ProviderCallError
			if (err == nil && (check.Allowed || check.Absent)) || errors.As(err, &call) && call.Provider.Category == execution.ErrorNotFound {
				t.Fatalf("dependency became absence: %+v %v", check, err)
			}
		})
	}
}

func TestEventHubClusterBeforeMutationAndResumedReadback(t *testing.T) {
	for _, mode := range []string{"new-member", "missing-prerequisite", "forged-prerequisite-link", "foreign-connection", "foreign-partition", "wrong-controller", "retain-prerequisite", "duplicate-prerequisite", "parent-recreated", "parent-config-changed", "protected-parent", "locked-parent", "minimum-age", "namespace-linked-elsewhere", "namespace-link-disappeared", "namespace-protected", "namespace-locked", "namespace-managed-group", "readback-403", "delete-403", "delete-409", "delete-429", "delete-500"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := eventHubClusterScenario(t)
			root, ns := assets[0], assets[1]
			request, input := dnsRequest(t, r, assets, root)
			result, _ := plan.Solve(input)
			driver, _ := r.ResolveAction(context.Background(), "connection", root)
			if strings.HasPrefix(mode, "namespace-") {
				request = servicePlanRequest(result, assets, ns)
				driver, _ = r.ResolveAction(context.Background(), "connection", ns)
			} else {
				for _, value := range assets[1:] {
					s.gone[value.Identity.NativeID] = true
				}
			}
			switch mode {
			case "new-member":
				s.gone[ns.Identity.NativeID] = false
				request.PrerequisiteDeletions = request.PrerequisiteDeletions[1:]
			case "missing-prerequisite":
				request.PrerequisiteDeletions = nil
				s.gone[ns.Identity.NativeID] = false
			case "forged-prerequisite-link":
				request.PrerequisiteDeletions[0].Asset.Normalized["clusterArmId"] = root.Identity.NativeID + "-foreign"
			case "foreign-connection":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "foreign"
			case "foreign-partition":
				request.PrerequisiteDeletions[0].Asset.Identity.Partition = "foreign"
			case "wrong-controller":
				request.PrerequisiteDeletions[0].ControllerID = "foreign"
			case "retain-prerequisite":
				request.PrerequisiteDeletions[0].Delete = false
			case "duplicate-prerequisite":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, request.PrerequisiteDeletions[0])
			case "parent-recreated":
				object(s.records[root.Identity.NativeID]["properties"])["createdAt"] = "2020-01-01T00:00:00Z"
			case "parent-config-changed":
				s.records[root.Identity.NativeID]["etag"] = "changed"
				object(s.records[root.Identity.NativeID]["sku"])["capacity"] = 9
			case "protected-parent":
				s.records[root.Identity.NativeID]["tags"] = map[string]any{"steward:protected": "true"}
			case "minimum-age":
				object(s.records[root.Identity.NativeID]["properties"])["createdAt"] = time.Now().UTC().Format(time.RFC3339Nano)
				request.Asset = dnsAsset(t, r, s.records[root.Identity.NativeID])
			case "namespace-linked-elsewhere":
				object(s.records[ns.Identity.NativeID]["properties"])["clusterArmId"] = root.Identity.NativeID + "-elsewhere"
			case "namespace-link-disappeared":
				delete(object(s.records[ns.Identity.NativeID]["properties"]), "clusterArmId")
			case "namespace-protected":
				s.records[ns.Identity.NativeID]["tags"] = map[string]any{"steward/protected": "true"}
			case "readback-403":
				s.gone[root.Identity.NativeID] = true
				s.status[ns.Identity.NativeID] = 403
			}
			if mode == "locked-parent" || mode == "namespace-locked" || mode == "namespace-managed-group" {
				target := root.Identity.NativeID
				if strings.HasPrefix(mode, "namespace-") {
					target = ns.Identity.NativeID
				}
				group := strings.Join(strings.Split(target, "/")[:5], "/")
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if mode == "namespace-managed-group" && req.Method == "GET" && strings.EqualFold(req.URL.Path, group) {
						return jsonResponse(200, map[string]any{"id": group, "managedBy": root.Identity.NativeID}, nil), true
					}
					if mode != "namespace-managed-group" && req.Method == "GET" && strings.HasSuffix(strings.ToLower(req.URL.Path), "/microsoft.authorization/locks") {
						return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": target + "/providers/Microsoft.Authorization/locks/protect", "properties": map[string]any{"level": "CanNotDelete"}}}}, nil), true
					}
					return nil, false
				}
			}
			if strings.HasPrefix(mode, "delete-") {
				s.deleteStatus = map[string]int{"delete-403": 403, "delete-409": 409, "delete-429": 429, "delete-500": 500}[mode]
			}
			_, err := driver.Execute(context.Background(), request)
			expectedWrites := 0
			if strings.HasPrefix(mode, "delete-") {
				expectedWrites = 1
			}
			if err == nil || len(s.deletes) != expectedWrites {
				t.Fatalf("unsafe mutation mode=%s writes=%v err=%v", mode, s.deletes, err)
			}
			var call *contracts.ProviderCallError
			if errors.As(err, &call) && call.Provider.Category == execution.ErrorNotFound {
				t.Fatal("dependency read became target absence")
			}
		})
	}
}

func TestEventHubClusterAgeAndOfficialListFields(t *testing.T) {
	_ = clusterFixture(t, "ClusterDelete") // Preserve the native Delete example and provenance too.
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		value  any
		reason string
	}{
		{now.Add(-4 * time.Hour).Format(time.RFC3339Nano), ""},
		{now.Add(-4*time.Hour + time.Nanosecond).Format(time.RFC3339Nano), "azure_eventhub_cluster_minimum_age"},
		{now.Add(time.Hour).Format(time.RFC3339Nano), "azure_eventhub_cluster_minimum_age"},
		{"bad-date", "azure_eventhub_cluster_invalid_creation_time"},
		{float64(0), "azure_eventhub_cluster_invalid_creation_time"},
		{nil, ""},
	} {
		if got := eventHubClusterMinimumAge(map[string]any{"properties": map[string]any{"createdAt": tc.value}}, now); got != tc.reason {
			t.Fatalf("age %v: %s", tc.value, got)
		}
	}
	raw := object(array(clusterFixture(t, "ClustersListBySubscription")["value"])[0])
	s := newDNSScenario()
	s.add(raw, "2024-01-01")
	s.records[strings.ToLower(text(raw["id"]))+"/quotaconfiguration/default"] = clusterFixture(t, "ClusterQuotaConfigurationGet")
	r := s.runtime(t)
	value := dnsAsset(t, r, raw)
	if value.Identity.NativeType != eventHubClusterType || value.Normalized["createdAt"] != "2016-09-13T23:17:25.24Z" || object(value.Normalized["sku"])["capacity"] != float64(4) {
		t.Fatalf("official list normalization: %+v", value)
	}
}

func TestEventHubClusterSecondaryNamespaceUsesSharedRecoveryPrerequisite(t *testing.T) {
	s, r, assets := recoveryScenario(t, eventHubNamespaceType, false)
	primaryNS, secondaryNS, alias := assets[0], assets[1], assets[2]
	clusterRaw := nativeResource(eventHubClusterType, "dedicated", "westus", map[string]any{"createdAt": "2020-01-01T00:00:00Z", "provisioningState": "Succeeded"})
	s.add(clusterRaw, "2024-01-01")
	s.records[strings.ToLower(text(clusterRaw["id"]))+"/quotaconfiguration/default"] = clusterFixture(t, "ClusterQuotaConfigurationGet")
	cluster := dnsAsset(t, r, clusterRaw)
	object(s.records[secondaryNS.Identity.NativeID]["properties"])["clusterArmId"] = cluster.Identity.NativeID
	assets[1] = dnsAsset(t, r, s.records[secondaryNS.Identity.NativeID])
	secondaryNS = assets[1]
	assets = append(assets, cluster)
	s.lists[cluster.Identity.NativeID+"/namespaces"] = []any{map[string]any{"id": secondaryNS.Identity.NativeID}}
	s.version[cluster.Identity.NativeID+"/namespaces"] = "2024-01-01"
	_, input := dnsRequest(t, r, assets, cluster)
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 3 {
		t.Fatalf("paired cluster plan=%+v %v", result, err)
	}
	requests := map[asset.AssetID]contracts.ActionRequest{}
	for _, value := range []asset.Asset{cluster, secondaryNS, alias} {
		requests[value.ID] = servicePlanRequest(result, assets, value)
	}
	if len(requests[cluster.ID].PrerequisiteDeletions) != 1 || len(requests[secondaryNS.ID].PrerequisiteDeletions) != 1 || len(requests[alias.ID].LifecycleImpacts) != 3 {
		t.Fatalf("incomplete recovery chain: %+v", requests)
	}
	posts := 0
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "POST" && strings.EqualFold(req.URL.Path, alias.Identity.NativeID+"/breakpairing") {
			posts++
			properties := object(s.records[alias.Identity.NativeID]["properties"])
			properties["role"], properties["partnerNamespace"], properties["provisioningState"] = "PrimaryNotReplicating", "", "Succeeded"
			return jsonResponse(200, nil, nil), true
		}
		return nil, false
	}
	for _, value := range []asset.Asset{alias, secondaryNS, cluster} {
		request := requests[value.ID]
		driver, _ := r.ResolveAction(context.Background(), "connection", value)
		op, err := driver.Execute(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		if value.ID == alias.ID {
			waited, err := driver.Wait(context.Background(), request, op)
			if err != nil || waited.Done || text(waited.Data["phase"]) != "delete" {
				t.Fatalf("recovery preparation=%+v %v", waited, err)
			}
			op.Data = waited.Data
		}
		for _, impact := range request.LifecycleImpacts {
			s.gone[impact.Asset.Identity.NativeID] = true
		}
		if waited, err := driver.Wait(context.Background(), request, op); err != nil || !waited.Done {
			t.Fatalf("nested prerequisite=%+v %v", waited, err)
		}
	}
	if posts != 1 || !slices.Equal(s.deletes, []string{alias.Identity.NativeID, secondaryNS.Identity.NativeID, cluster.Identity.NativeID}) || s.gone[primaryNS.Identity.NativeID] || s.gone[assets[6].Identity.NativeID] {
		t.Fatalf("cluster cleanup altered unselected peer: %v", s.deletes)
	}
}

func TestEventHubClusterQuotaReadsCannotAuthorizeDeletion(t *testing.T) {
	for _, mode := range []string{"403", "404", "206", "missing-settings", "nonstring-setting", "error-body", "changed-setting", "missing-proof", "recreated-during-read"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := eventHubClusterScenario(t)
			root := assets[0]
			request, _ := dnsRequest(t, r, assets, root)
			for _, value := range assets[1:] {
				s.gone[value.Identity.NativeID] = true
			}
			path := root.Identity.NativeID + "/quotaconfiguration/default"
			switch mode {
			case "403", "404", "206":
				s.status[path] = map[string]int{"403": 403, "404": 404, "206": 206}[mode]
			case "missing-settings":
				delete(s.records[path], "settings")
			case "nonstring-setting":
				object(s.records[path]["settings"])["limit"] = float64(100)
			case "error-body":
				s.records[path]["error"] = map[string]any{"code": "Unavailable"}
			case "changed-setting":
				object(s.records[path]["settings"])["eventhub-per-namespace-quota"] = "100"
			case "missing-proof":
				delete(request.Asset.Normalized, "quotaSettings")
			case "recreated-during-read":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.EqualFold(req.URL.Path, path) {
						object(s.records[root.Identity.NativeID]["properties"])["createdAt"] = "2020-01-01T00:00:00Z"
					}
					return nil, false
				}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", root)
			_, err := driver.Execute(context.Background(), request)
			if err == nil || len(s.deletes) != 0 {
				t.Fatalf("unproven quota configuration allowed deletion: %v %v", s.deletes, err)
			}
			var call *contracts.ProviderCallError
			if errors.As(err, &call) && call.Provider.Category == execution.ErrorNotFound {
				t.Fatal("quota absence became cluster absence")
			}
			if mode == "403" || mode == "404" || mode == "206" || mode == "missing-settings" || mode == "nonstring-setting" || mode == "error-body" {
				if batch, err := r.List(context.Background(), productRequest(r, eventHubClusterType)); err == nil || batch.Complete {
					t.Fatalf("incomplete quota scan claimed completion: %+v %v", batch, err)
				}
			}
		})
	}
}
