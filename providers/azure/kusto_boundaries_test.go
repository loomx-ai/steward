package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestKustoFollowerAndParentIndexBoundaries(t *testing.T) {
	for _, mode := range []string{"duplicate", "foreign-subscription", "self", "wrong-source", "wrong-database", "wrong-sharing", "attachment-forbidden", "attachment-absent", "attachment-partial", "index-forbidden", "index-partial", "index-missing-array", "wrong-prefix", "wrong-local-source", "unowned-readonly", "attached-index", "last-attached-index", "database-index", "last-database-index", "private-endpoint-index", "last-private-endpoint-index", "private-parent", "pending-parent", "pending-child"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := kustoScenario(t)
			parent := assets[0]
			attachment, readonly := s.records[assets[3].Identity.NativeID], s.records[assets[4].Identity.NativeID]
			index := parent.Identity.NativeID + "/listfollowerdatabases"
			entry := object(object(s.lists[index][0])["properties"])
			switch mode {
			case "duplicate":
				s.lists[index] = append(s.lists[index], s.lists[index][0])
			case "foreign-subscription":
				entry["clusterResourceId"] = strings.Replace(assets[1].Identity.NativeID, testSubscription, "22222222-2222-2222-2222-222222222222", 1)
			case "self":
				entry["clusterResourceId"] = parent.Identity.NativeID
			case "wrong-source":
				object(attachment["properties"])["clusterResourceId"] = parent.Identity.NativeID + "other"
			case "wrong-database":
				entry["databaseName"] = "other"
			case "wrong-sharing":
				entry["tableLevelSharingProperties"] = map[string]any{"tablesToInclude": []any{"sensitive"}}
			case "attachment-forbidden":
				s.status[assets[3].Identity.NativeID] = 403
			case "attachment-absent":
				s.status[assets[3].Identity.NativeID] = 404
			case "attachment-partial":
				s.status[assets[3].Identity.NativeID] = 206
			case "index-forbidden":
				s.status[index] = 403
			case "index-partial":
				s.status[index] = 206
			case "wrong-prefix":
				parent = assets[3]
				object(attachment["properties"])["databaseNamePrefix"] = "other-"
			case "wrong-local-source":
				parent = assets[3]
				object(readonly["properties"])["leaderClusterResourceId"] = assets[0].Identity.NativeID + "other"
			case "unowned-readonly":
				parent = assets[1]
				s.lists[parent.Identity.NativeID+"/attacheddatabaseconfigurations"] = []any{}
			case "attached-index":
				parent = assets[3]
				object(attachment["properties"])["attachedDatabaseNames"] = []any{}
			case "last-attached-index":
				parent = assets[3]
			case "database-index":
				parent = assets[2]
				object(s.records[parent.Identity.NativeID]["properties"])["isFollowed"] = false
			case "last-database-index":
				parent = assets[2]
			case "private-endpoint-index":
				object(s.records[parent.Identity.NativeID]["properties"])["privateEndpointConnections"] = []any{}
			case "pending-child":
				object(attachment["properties"])["provisioningState"] = "Running"
			}
			payload, _ := json.Marshal(s.records[parent.Identity.NativeID])
			var initial map[string]any
			json.Unmarshal(payload, &initial)
			reads := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				id := strings.ToLower(req.URL.Path)
				if mode == "index-missing-array" && id == index {
					return jsonResponse(200, map[string]any{}, nil), true
				}
				if id == parent.Identity.NativeID {
					reads++
					props := object(s.records[id]["properties"])
					if reads >= 1 {
						switch mode {
						case "last-attached-index":
							props["attachedDatabaseNames"] = []any{}
						case "last-database-index":
							props["isFollowed"] = false
						case "last-private-endpoint-index":
							props["privateEndpointConnections"] = []any{}
						case "private-parent":
							props["password"] = "changed-secret"
						case "pending-parent":
							props["state"] = "Updating"
						}
					}
				}
				return nil, false
			}
			c, _ := r.resolve(context.Background(), "connection")
			if _, err := c.kustoChildren(context.Background(), parent.Identity, initial); err == nil {
				t.Fatal("incomplete or changing Kusto index accepted")
			}
		})
	}
}

func TestKustoLeafAncestorsAndPrivateConfiguration(t *testing.T) {
	for _, mode := range []string{"own-change", "own-secret", "parent-change", "parent-secret", "root-change", "root-protected", "root-lock", "root-forbidden", "root-partial", "root-absent", "root-pending", "own-pending", "wrong-id", "wrong-type"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := kustoScenario(t)
			target := assets[7]
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			request := contracts.ActionRequest{Asset: target, Action: "delete"}
			if checked, err := driver.Preflight(context.Background(), request); err != nil || !checked.Allowed {
				t.Fatal("invalid baseline", checked, err)
			}
			own, parent, root := s.records[target.Identity.NativeID], s.records[assets[2].Identity.NativeID], s.records[assets[0].Identity.NativeID]
			switch mode {
			case "own-change":
				object(own["properties"])["forceUpdateTag"] = "new"
			case "own-secret":
				object(own["properties"])["scriptContent"] = ".set secret-data"
			case "parent-change":
				object(parent["properties"])["softDeletePeriod"] = "P30D"
			case "parent-secret":
				object(parent["properties"])["password"] = "changed-secret"
			case "root-change":
				object(root["sku"])["capacity"] = 4
			case "root-protected":
				root["tags"] = map[string]any{"steward:protected": "true"}
			case "root-lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": assets[0].Identity.NativeID + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "root-forbidden":
				s.status[assets[0].Identity.NativeID] = 403
			case "root-partial":
				s.status[assets[0].Identity.NativeID] = 206
			case "root-absent":
				s.gone[assets[0].Identity.NativeID] = true
			case "root-pending":
				object(root["properties"])["state"] = "Updating"
			case "own-pending":
				object(own["properties"])["provisioningState"] = "Running"
			case "wrong-id":
				own["id"] = target.Identity.NativeID + "other"
			case "wrong-type":
				own["type"] = kustoDataConnectionType
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("Kusto deleted through drift or protection", err)
			}
		})
	}
}

func TestKustoActiveImageUsesClusterCleanup(t *testing.T) {
	s, r, assets := kustoScenario(t)
	root, image := assets[0], assets[11]
	object(s.records[root.Identity.NativeID]["properties"])["languageExtensions"] = map[string]any{"value": []any{map[string]any{"languageExtensionName": "PYTHON", "languageExtensionImageName": "PythonCustomImage", "languageExtensionCustomImageName": "example"}}}
	for i, value := range assets {
		assets[i] = dnsAsset(t, r, s.records[value.Identity.NativeID])
		assets[i].Location = "westus"
	}
	root, image = assets[0], assets[11]
	if image.Normalized["cleanup_controller_only"] != true {
		t.Fatal("active image did not require controller")
	}
	driver, _ := r.ResolveAction(context.Background(), "connection", image)
	if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: image, Action: "delete"}); err == nil {
		t.Fatal("active image deleted directly")
	}
	_, input := dnsRequest(t, r, assets, root)
	solved, err := plan.Solve(input)
	if err != nil || len(solved.Blockers) != 0 {
		t.Fatal(solved, err)
	}
	if slices.ContainsFunc(solved.Steps, func(step plan.CleanupTaskStep) bool { return step.AssetID == image.ID }) {
		t.Fatal("active image became a prerequisite DELETE")
	}
	for _, step := range solved.Steps {
		value := assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.ID == step.AssetID })]
		request := servicePlanRequest(solved, assets, value)
		driver, _ := r.ResolveAction(context.Background(), "connection", value)
		result, err := driver.Execute(context.Background(), request)
		if err != nil {
			t.Fatal(value.Identity.NativeID, err)
		}
		kustoAfterDelete(s)
		if value.ID == root.ID {
			if wait, err := driver.Wait(context.Background(), request, result); err != nil || wait.Done {
				t.Fatal("cluster completion skipped image readback", wait, err)
			}
			s.gone[image.Identity.NativeID] = true
		}
		if wait, err := driver.Wait(context.Background(), request, result); err != nil || !wait.Done {
			t.Fatal("Kusto readback", wait, err)
		}
	}
}

func TestKustoProxyLocationComesFromCluster(t *testing.T) {
	s, r, assets := kustoScenario(t)
	target := assets[8]
	s.records[target.Identity.NativeID]["location"] = "DummyLocation"
	request := productRequest(r, kustoManagedEndpointType)
	request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westus"}
	var items []contracts.InventoryItem
	for {
		batch, err := r.List(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		items = append(items, batch.Items...)
		if batch.Complete {
			break
		}
		request.Cursor = batch.NextCursor
	}
	if len(items) != 1 || items[0].Location != "westus" || items[0].Scope.NativeID != "westus" {
		t.Fatal("proxy disappeared from its cluster region", items)
	}
}

func TestKustoOperationURLAndGenericInvocation(t *testing.T) {
	endpoint := apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.Kusto/locations/West%20US%202/operationResults/11111111-2222-3333-4444-555555555555", kustoVersion)
	valid := []string{endpoint, strings.ReplaceAll(endpoint, "%20", " "), endpoint + "&operationResultResponseType=Location"}
	for _, version := range []string{"2022-02-01", "2022-12-29", "2023-05-02"} {
		valid = append(valid, strings.Replace(endpoint, kustoVersion, version, 1))
	}
	invalid := []string{
		strings.Replace(endpoint, testSubscription, testTenant, 1), strings.Replace(endpoint, "Microsoft.Kusto", "Microsoft.Compute", 1),
		strings.Replace(endpoint, kustoVersion, "2020-09-18", 1), strings.Replace(endpoint, "management.azure.com", "evil.invalid", 1),
		strings.Replace(endpoint, "https://", "http://", 1), strings.Replace(endpoint, "operationResults", "clusters", 1),
		strings.Replace(endpoint, "operationResults", "operationsStatus", 1), strings.Replace(endpoint, "/West%20US%202/", "/../", 1),
		strings.Replace(endpoint, "/West%20US%202/", "/%57est%20US%202/", 1), endpoint + "#fragment", endpoint + "&unknown=1",
		endpoint + "&api-version=" + kustoVersion, endpoint + "&operationResultResponseType=Status", endpoint + "&operationResultResponseType=Location&operationResultResponseType=Location",
		strings.Replace(endpoint, "management.azure.com", "user@management.azure.com", 1), endpoint + strings.Repeat("a", 33*1024),
	}
	for _, value := range valid {
		if err := validateKustoOperationURL(testSubscription, "westus2", kustoVersion, value); err != nil {
			t.Fatal("native Kusto endpoint rejected", err)
		}
	}
	for _, value := range append(slices.Clone(invalid), strings.Replace(endpoint, "West%20US%202", "eastus", 1)) {
		if err := validateKustoOperationURL(testSubscription, "westus2", kustoVersion, value); err == nil {
			t.Fatal("invalid endpoint accepted", value)
		}
	}
	// detachFollowerDatabases is an action path, not an ARM resource ID. Its
	// Invoke result must still use the Kusto-specific operation boundary.
	for _, value := range append(valid, invalid...) {
		s, r, assets := kustoScenario(t)
		s.handle = func(req *http.Request) (*http.Response, bool) {
			if req.Method != "POST" {
				return nil, false
			}
			if strings.ToLower(req.URL.Path) != assets[0].Identity.NativeID+"/detachfollowerdatabases" || req.URL.Query().Get("api-version") != kustoVersion {
				t.Fatal("incorrect native action binding")
			}
			return jsonResponse(202, nil, http.Header{"Azure-Asyncoperation": {value}}), true
		}
		result, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.Kusto.Clusters_DetachFollowerDatabases", Parameters: map[string]any{"resourceGroupName": "testgroup", "clusterName": "leader", "followerDatabaseToRemove": map[string]any{"clusterResourceId": assets[1].Identity.NativeID, "attachedDatabaseConfigurationName": "follow"}}})
		if slices.Contains(valid, value) {
			if err != nil || result.OperationID != value {
				t.Fatal("native Invoke poll", err)
			}
		} else if err == nil {
			t.Fatal("Invoke accepted an unrelated operation")
		}
	}
}

func TestKustoRetainedAndUnreviewedDependencies(t *testing.T) {
	for _, selected := range []int{0, 1, 2, 3} {
		s, r, assets := kustoScenario(t)
		target := assets[selected]
		request, input := dnsRequest(t, r, assets, target)
		children := append(slices.Clone(request.LifecycleImpacts), request.PrerequisiteDeletions...)
		if len(children) == 0 {
			t.Fatal("missing Kusto dependencies")
		}
		for _, child := range children {
			input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{child.Asset.Identity.NativeID}}}
			if retained, err := plan.Solve(input); err != nil || len(retained.Blockers) == 0 {
				t.Fatal("retained Kusto dependency allowed deletion", child.Asset.Identity.NativeID, err)
			}
		}
		driver, _ := r.ResolveAction(context.Background(), "connection", target)
		if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"}); err == nil || len(s.deletes) != 0 {
			t.Fatal("unreviewed Kusto dependency allowed deletion", err)
		}
	}
}

func TestKustoPrivateScriptSnapshot(t *testing.T) {
	s, r, assets := kustoScenario(t)
	target := assets[7]
	raw := s.records[target.Identity.NativeID]
	props := object(raw["properties"])
	props["scriptContent"], props["scriptUrlSasToken"] = "private-kql-content", "private-sas-value"
	props["scriptUrl"] = "https://private-user:private-password@example.blob.core.windows.net/scripts/run.kql?sig=private-query#private-fragment"
	props["privateCounter"] = json.Number("9007199254740993")
	c, _ := r.resolve(context.Background(), "connection")
	snapshot := kustoSnapshot(kustoScriptType, raw)
	if object(snapshot["properties"])["privateCounter"] != json.Number("9007199254740993") {
		t.Fatal("private integer lost precision")
	}
	before := c.privateConfiguration(snapshot)
	props["scriptContent"] = "new-private-content"
	if before == c.privateConfiguration(kustoSnapshot(kustoScriptType, raw)) {
		t.Fatal("script change did not invalidate review")
	}
	item, err := r.inventoryItem(context.Background(), c, raw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(item)
	for _, secret := range []string{"new-private-content", "private-sas-value", "private-user", "private-password", "private-query", "private-fragment"} {
		if bytes.Contains(data, []byte(secret)) {
			t.Fatal("script material reached inventory", secret)
		}
	}
	props["requirementsFileContent"] = "private-requirements-content"
	data, _ = json.Marshal(safeResource(raw))
	if bytes.Contains(data, []byte("private-requirements-content")) {
		t.Fatal("custom image requirements exposed")
	}
}

func TestKustoManagedEndpointTargetProtection(t *testing.T) {
	for _, mode := range []string{"normal", "target-config", "target-secret", "target-protected", "target-lock", "target-group-managed", "target-forbidden", "target-partial", "target-absent", "target-identity", "target-reread", "foreign-subscription", "unknown-target", "invalid-group"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := kustoScenario(t)
			target := assets[8]
			link := s.records[target.Identity.NativeID]
			id, _ := kustoEndpointTarget(link)
			raw := s.records[id]
			reads := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if path == id {
					reads++
					if mode == "target-reread" && reads >= 2 {
						object(raw["properties"])["supportsHttpsTrafficOnly"] = true
					}
				}
				if mode == "target-group-managed" && path == strings.Join(strings.Split(id, "/")[:5], "/") {
					return jsonResponse(200, map[string]any{"id": path, "type": groupType, "managedBy": assets[1].Identity.NativeID}, nil), true
				}
				return nil, false
			}
			switch mode {
			case "target-config":
				object(raw["properties"])["supportsHttpsTrafficOnly"] = true
			case "target-secret":
				object(raw["properties"])["password"] = "changed-secret"
			case "target-protected":
				raw["tags"] = map[string]any{"steward:protected": "true"}
			case "target-lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": id + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "target-forbidden":
				s.status[id] = 403
			case "target-partial":
				s.status[id] = 206
			case "target-absent":
				s.gone[id] = true
			case "target-identity":
				raw["id"] = id + "other"
			case "foreign-subscription":
				object(link["properties"])["privateLinkResourceId"] = strings.Replace(id, testSubscription, testTenant, 1)
			case "unknown-target":
				object(link["properties"])["privateLinkResourceId"] = strings.Replace(id, "microsoft.storage/storageaccounts", "microsoft.unknown/targets", 1)
			case "invalid-group":
				object(link["properties"])["groupId"] = "blob/other"
			}
			if mode == "foreign-subscription" || mode == "unknown-target" || mode == "invalid-group" {
				c, _ := r.resolve(context.Background(), "connection")
				if _, err := r.inventoryItem(context.Background(), c, link, nil, nil); err == nil {
					t.Fatal("invalid linked target accepted during inventory")
				}
				return
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			_, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"})
			if mode == "normal" {
				if err != nil || !s.gone[target.Identity.NativeID] || s.gone[id] {
					t.Fatal("MPE deletion did not preserve target", err)
				}
			} else if err == nil || len(s.deletes) != 0 {
				t.Fatal("Kusto deleted through target protection", err)
			}
		})
	}
}

func TestKustoFollowerProtectionAndWildcardImpacts(t *testing.T) {
	for _, mode := range []string{"protected-source", "protected-follower", "wildcard", "override"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := kustoScenario(t)
			leader, follower, attachment, readonly := assets[0], assets[1], assets[3], assets[4]
			if mode == "protected-source" {
				s.records[leader.Identity.NativeID]["tags"] = map[string]any{"steward:protected": "true"}
			}
			if mode == "protected-follower" {
				s.records[follower.Identity.NativeID]["tags"] = map[string]any{"steward:protected": "true"}
			}
			if mode == "wildcard" {
				props := object(s.records[attachment.Identity.NativeID]["properties"])
				props["databaseName"], props["attachedDatabaseNames"] = "*", []any{"data", "other"}
				object(object(s.lists[leader.Identity.NativeID+"/listfollowerdatabases"][0])["properties"])["databaseName"] = "*"
				for _, selected := range []int{2, 4} {
					payload, _ := json.Marshal(s.records[assets[selected].Identity.NativeID])
					var raw map[string]any
					json.Unmarshal(payload, &raw)
					id := redisParentID(assets[selected].Identity.NativeID) + "/databases/other"
					if selected == 4 {
						id = redisParentID(assets[selected].Identity.NativeID) + "/databases/copy-other"
						object(raw["properties"])["originalDatabaseName"] = "other"
					}
					raw["id"], raw["name"] = id, last(id)
					s.add(raw, kustoVersion)
					path := redisParentID(id) + "/databases"
					s.lists[path] = append(s.lists[path], raw)
					for _, child := range kustoOwnedKinds(kustoDatabaseType) {
						path := id + "/" + strings.ToLower(last(child))
						s.lists[path], s.version[path] = []any{}, kustoVersion
					}
					assets = append(assets, dnsAsset(t, r, raw))
				}
			}
			if mode == "override" {
				props := object(s.records[attachment.Identity.NativeID]["properties"])
				props["databaseNamePrefix"], props["databaseNameOverride"] = "", "renamed"
				old := readonly.Identity.NativeID
				raw := s.records[old]
				raw["id"], raw["name"] = redisParentID(old)+"/databases/renamed", "renamed"
				delete(s.records, old)
				s.add(raw, kustoVersion)
				for _, child := range kustoOwnedKinds(kustoDatabaseType) {
					path := text(raw["id"]) + "/" + strings.ToLower(last(child))
					s.lists[path], s.version[path] = []any{}, kustoVersion
				}
				assets[4] = dnsAsset(t, r, raw)
			}
			for i, value := range assets {
				assets[i] = dnsAsset(t, r, s.records[value.Identity.NativeID])
			}
			attachment = assets[3]
			request, input := dnsRequest(t, r, assets, attachment)
			if mode == "wildcard" {
				if len(request.LifecycleImpacts) != 2 {
					t.Fatal("wildcard attachment did not review both local databases", len(request.LifecycleImpacts))
				}
				input.RequestOptions = map[asset.AssetID]map[string]any{attachment.ID: {"retain_resources": []string{assets[4].Identity.NativeID}}}
				if retained, err := plan.Solve(input); err != nil || len(retained.Blockers) == 0 {
					t.Fatal("wildcard detached retained database", err)
				}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", attachment)
			result, err := driver.Execute(context.Background(), request)
			if mode == "protected-follower" {
				if err == nil || len(s.deletes) != 0 {
					t.Fatal("protected follower allowed detach")
				}
				return
			}
			if err != nil {
				t.Fatal("attachment cleanup", err)
			}
			kustoAfterDelete(s)
			if waited, err := driver.Wait(context.Background(), request, result); err != nil || !waited.Done {
				t.Fatal("attachment readback", waited, err)
			}
			if s.gone[leader.Identity.NativeID] || s.gone[follower.Identity.NativeID] {
				t.Fatal("detaching removed a cluster")
			}
		})
	}
}

func TestKustoInventoryPaginationBoundaries(t *testing.T) {
	for _, mode := range []string{"complete", "duplicate", "cycle", "foreign-host", "foreign-subscription", "foreign-collection", "wrong-version", "partial", "forbidden", "missing-array", "parent-config", "parent-private"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := kustoScenario(t)
			parent := assets[2].Identity.NativeID
			path := parent + "/scripts"
			first := s.records[assets[7].Identity.NativeID]
			payload, _ := json.Marshal(first)
			var second map[string]any
			json.Unmarshal(payload, &second)
			second["id"], second["name"] = path+"/other-script", "other-script"
			s.add(second, kustoVersion)
			next := apiURL(path, kustoVersion) + "&$skiptoken=native-page-2"
			switch mode {
			case "foreign-host":
				next = strings.Replace(next, "management.azure.com", "evil.invalid", 1)
			case "foreign-subscription":
				next = strings.Replace(next, testSubscription, testTenant, 1)
			case "foreign-collection":
				next = strings.Replace(next, "/scripts?", "/firewallRules?", 1)
			case "wrong-version":
				next = strings.Replace(next, kustoVersion, "2025-09-01", 1)
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.ToLower(req.URL.Path) != path {
					return nil, false
				}
				if req.URL.Query().Get("$skiptoken") == "" {
					return jsonResponse(200, map[string]any{"value": []any{first}, "nextLink": next}, nil), true
				}
				switch mode {
				case "partial":
					return jsonResponse(206, map[string]any{"value": []any{second}}, nil), true
				case "forbidden":
					return jsonResponse(403, nil, nil), true
				case "missing-array":
					return jsonResponse(200, map[string]any{}, nil), true
				case "cycle":
					return jsonResponse(200, map[string]any{"value": []any{second}, "nextLink": next}, nil), true
				case "duplicate":
					return jsonResponse(200, map[string]any{"value": []any{first}}, nil), true
				case "parent-config":
					object(s.records[parent]["properties"])["softDeletePeriod"] = "P30D"
				case "parent-private":
					object(s.records[parent]["properties"])["password"] = "changed-private-value"
				}
				return jsonResponse(200, map[string]any{"value": []any{second}}, nil), true
			}
			request := productRequest(r, kustoScriptType)
			var err error
			count, complete := 0, false
			for range 5 {
				var batch contracts.InventoryBatch
				batch, err = r.List(context.Background(), request)
				if err != nil {
					break
				}
				count += len(batch.Items)
				if batch.Complete {
					complete = true
					break
				}
				request.Cursor = batch.NextCursor
			}
			if mode == "complete" {
				if err != nil || !complete || count != 2 {
					t.Fatal("Kusto complete pagination", err, count, complete)
				}
			} else if err == nil || complete {
				t.Fatal("incomplete Kusto collection accepted", count)
			}
		})
	}
}

func TestKustoOperationReceiptsAndReadback(t *testing.T) {
	endpoint := apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.Kusto/locations/westus/operationResults/30972f1b-b61d-4fd8-bd34-3dcfa24670f3", kustoVersion)
	for _, mode := range []string{"pending", "unknown", "success-live", "success-absent", "failed", "native-failed", "error-body", "location-success", "canceled", "poll-partial", "poll-forbidden", "wrong-id", "wrong-name", "wrong-resource", "wrong-receipt", "wrong-request", "expired-live", "expired-absent", "readback-partial", "readback-forbidden", "readback-wrong-id", "delete-partial"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := kustoScenario(t)
			target := assets[7]
			pollCalls := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					if mode == "delete-partial" {
						return jsonResponse(206, nil, nil), true
					}
					if mode == "location-success" {
						return jsonResponse(202, nil, http.Header{"Location": {endpoint + "&operationResultResponseType=Location"}}), true
					}
					return jsonResponse(202, nil, http.Header{"Azure-Asyncoperation": {endpoint}}), true
				}
				if req.URL.String() == endpoint || req.URL.String() == endpoint+"&operationResultResponseType=Location" {
					pollCalls++
					body := map[string]any{"status": "Succeeded"}
					switch mode {
					case "location-success":
						return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, true
					case "native-failed":
						body := kustoExampleBody(t, "KustoOperationResultsGet")
						body["id"] = strings.Replace(text(body["id"]), "12345678-1234-1234-1234-123456789098", testSubscription, 1)
						return jsonResponse(200, body, nil), true
					case "error-body":
						body["error"] = map[string]any{"code": "CannotAlterFollowerDatabase"}
					case "pending":
						body["status"] = "Running"
					case "unknown":
						body["status"] = "Unknown"
					case "failed":
						body["status"] = "Failed"
					case "canceled":
						body["status"] = "Canceled"
					case "poll-partial":
						return jsonResponse(206, body, nil), true
					case "poll-forbidden":
						return jsonResponse(403, nil, nil), true
					case "expired-live", "expired-absent":
						return jsonResponse(404, nil, nil), true
					case "wrong-id":
						body["id"] = "/wrong"
					case "wrong-name":
						body["name"] = "other-operation"
					case "wrong-resource":
						body["resourceId"] = assets[5].Identity.NativeID
					}
					return jsonResponse(200, body, nil), true
				}
				return nil, false
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			result, err := driver.Execute(context.Background(), request)
			if mode == "delete-partial" {
				if err == nil {
					t.Fatal("partial native DELETE accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "success-absent", "expired-absent", "location-success":
				s.gone[target.Identity.NativeID] = true
			case "wrong-receipt":
				result.Data["kusto_operation_binding"] = "wrong-binding"
			case "wrong-request":
				request.Asset = assets[5]
			case "readback-partial":
				s.status[target.Identity.NativeID] = 206
			case "readback-forbidden":
				s.status[target.Identity.NativeID] = 403
			case "readback-wrong-id":
				s.records[target.Identity.NativeID]["id"] = target.Identity.NativeID + "changed"
			}
			waited, err := driver.Wait(context.Background(), request, result)
			switch mode {
			case "success-absent", "expired-absent", "location-success":
				if err != nil || !waited.Done {
					t.Fatal("native absence not accepted", waited, err)
				}
			case "success-live", "expired-live", "pending", "unknown":
				if err != nil || waited.Done {
					t.Fatal("live resource treated as absent", waited, err)
				}
			default:
				if err == nil || waited.Done {
					t.Fatal("invalid native completion accepted", waited, err)
				}
			}
			if (mode == "wrong-request" || mode == "wrong-receipt") && pollCalls != 0 {
				t.Fatal("tampered request reached the polling endpoint")
			}
		})
	}
}
