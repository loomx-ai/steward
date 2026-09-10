package azure

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const kustoVersion = "2025-02-14"

func kustoExample(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/kusto/" + name + ".json")
	var result map[string]any
	if err != nil || json.Unmarshal(payload, &result) != nil {
		t.Fatal("invalid Kusto example", err)
	}
	return result
}
func kustoExampleBody(t *testing.T, name string) map[string]any {
	return object(object(object(kustoExample(t, name)["responses"])["200"])["body"])
}

// The native examples deliberately use unrelated identities (including a
// self-following attachment and an invalid subscription in sandbox examples).
// Compose a valid topology in these separate copies; originals stay unchanged.
func kustoScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	group := root + "/resourcegroups/testgroup"
	leader := group + "/providers/microsoft.kusto/clusters/leader"
	follower := group + "/providers/microsoft.kusto/clusters/follower"
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
	collection := root + "/providers/microsoft.kusto/clusters"
	s.lists[collection], s.version[collection] = []any{}, kustoVersion
	var raws []map[string]any
	add := func(raw map[string]any, id, kind string) {
		raw["id"], raw["name"], raw["type"] = id, last(id), kind
		s.add(raw, kustoVersion)
		raws = append(raws, raw)
		path := redisParentID(id) + "/" + strings.ToLower(last(kind))
		if kind == kustoType {
			path = collection
		}
		s.lists[path] = append(s.lists[path], raw)
		s.version[path] = kustoVersion
		for _, child := range kustoOwnedKinds(kind) {
			path := id + "/" + strings.ToLower(last(child))
			s.lists[path], s.version[path] = []any{}, kustoVersion
		}
	}
	for _, id := range []string{leader, follower} {
		raw := kustoExampleBody(t, "KustoClustersGet")
		props := object(raw["properties"])
		delete(props, "migrationCluster")
		props["state"], props["privateEndpointConnections"] = "Running", []any{}
		add(raw, id, kustoType)
		s.lists[id+"/listfollowerdatabases"], s.version[id+"/listfollowerdatabases"] = []any{}, kustoVersion
	}
	database := kustoExampleBody(t, "KustoDatabasesGet")
	object(database["properties"])["isFollowed"] = true
	add(database, leader+"/databases/data", kustoDatabaseType)
	attachment := kustoExampleBody(t, "KustoAttachedDatabaseConfigurationsGet")
	props := object(attachment["properties"])
	props["clusterResourceId"], props["databaseName"], props["databaseNamePrefix"] = leader, "data", "copy-"
	props["attachedDatabaseNames"] = []any{"data"}
	add(attachment, follower+"/attacheddatabaseconfigurations/follow", kustoAttachmentType)
	readonly := object(array(kustoExampleBody(t, "KustoDatabasesListByCluster")["value"])[1])
	props = object(readonly["properties"])
	props["leaderClusterResourceId"], props["originalDatabaseName"], props["attachedDatabaseConfigurationName"] = leader, "data", "follow"
	add(readonly, follower+"/databases/copy-data", kustoDatabaseType)
	s.lists[leader+"/listfollowerdatabases"] = []any{map[string]any{"properties": map[string]any{"clusterResourceId": follower, "attachedDatabaseConfigurationName": "follow", "databaseName": "data", "databaseShareOrigin": "Direct"}}}
	for _, tc := range []struct{ kind, name string }{
		{kustoDataConnectionType, "KustoDataConnectionsGet"},
		{kustoDatabasePrincipalType, "KustoDatabasePrincipalAssignmentsGet"},
		{kustoScriptType, "KustoScriptsGet"},
		{kustoManagedEndpointType, "KustoManagedPrivateEndpointsGet"},
		{kustoPrincipalType, "KustoClusterPrincipalAssignmentsGet"},
		{kustoEndpointType, "KustoPrivateEndpointConnectionsGet"},
		{kustoImageType, "KustoSandboxCustomImagesGet"},
	} {
		raw := kustoExampleBody(t, tc.name)
		parent := leader
		if strings.Contains(tc.kind, "/databases/") {
			parent += "/databases/data"
		}
		id := parent + "/" + strings.ToLower(last(tc.kind)) + "/example"
		if tc.kind == kustoManagedEndpointType {
			target := group + "/providers/microsoft.storage/storageaccounts/kustotarget"
			object(raw["properties"])["privateLinkResourceId"] = target
			mapping, _ := findType("Microsoft.Storage/storageAccounts")
			s.add(map[string]any{"id": target, "type": mapping.NativeType, "name": "kustotarget", "location": "westus", "properties": map[string]any{}}, mapping.Version)
		}
		add(raw, id, tc.kind)
		if tc.kind == kustoEndpointType {
			object(s.records[leader]["properties"])["privateEndpointConnections"] = []any{map[string]any{"id": id}}
		}
	}
	r := s.runtime(t)
	var assets []asset.Asset
	for _, raw := range raws {
		value := dnsAsset(t, r, raw)
		value.Location = "westus"
		assets = append(assets, value)
	}
	return s, r, assets
}

func kustoAfterDelete(s *dnsScenario) {
	for id, raw := range s.records {
		if kustoKind(text(raw["type"])) == kustoDatabaseType && raw["kind"] == "ReadOnlyFollowing" {
			attachment, _ := kustoFollowingAttachment(raw)
			if s.gone[attachment] {
				s.gone[id] = true
			}
		}
		if kustoKind(text(raw["type"])) != kustoType {
			continue
		}
		props := object(raw["properties"])
		connections := []any{}
		for _, value := range array(props["privateEndpointConnections"]) {
			if !s.gone[strings.ToLower(text(object(value)["id"]))] {
				connections = append(connections, value)
			}
		}
		props["privateEndpointConnections"] = connections
		followers := []any{}
		for _, value := range s.lists[id+"/listfollowerdatabases"] {
			p := object(object(value)["properties"])
			attachment := strings.ToLower(text(p["clusterResourceId"]) + "/attachedDatabaseConfigurations/" + text(p["attachedDatabaseConfigurationName"]))
			if !s.gone[attachment] {
				followers = append(followers, value)
			}
		}
		s.lists[id+"/listfollowerdatabases"] = followers
		if db := s.records[id+"/databases/data"]; db != nil {
			object(db["properties"])["isFollowed"] = len(followers) > 0
		}
		raw["etag"] = "reviewed-child-removed"
	}
}

func TestKustoNativeInventoryAndReviewedCleanup(t *testing.T) {
	for _, selected := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11} {
		t.Run(string(rune('a'+selected)), func(t *testing.T) {
			s, r, assets := kustoScenario(t)
			target := assets[selected]
			request := productRequest(r, target.Identity.NativeType)
			var items []contracts.InventoryItem
			for {
				batch, err := r.List(context.Background(), request)
				if err != nil {
					t.Fatal("Kusto inventory", target.Identity.NativeType, err)
				}
				items = append(items, batch.Items...)
				if batch.Complete {
					break
				}
				request.Cursor = batch.NextCursor
			}
			want := 1
			if target.Identity.NativeType == kustoType || target.Identity.NativeType == kustoDatabaseType {
				want = 2
			}
			if len(items) != want {
				t.Fatal("Kusto missing inventory", target.Identity.NativeType, len(items), want)
			}
			if selected == 4 {
				contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
				contribution, err := contributor.Contribute(context.Background(), "scope", assets)
				if err != nil {
					t.Fatal(err)
				}
				solved, err := plan.Solve(plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{target.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
				if err != nil || len(solved.Blockers) != 1 || solved.Blockers[0].Code != "managed_by_controller" || solved.Blockers[0].ControllerID != assets[3].ID {
					t.Fatal("follower needs its actual attachment controller", solved, err)
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", target)
				if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"}); err == nil || len(s.deletes) != 0 {
					t.Fatal("readonly database allowed direct deletion")
				}
				return
			}
			_, input := dnsRequest(t, r, assets, target)
			solved, err := plan.Solve(input)
			if err != nil || len(solved.Blockers) != 0 {
				t.Fatal("Kusto plan", err, solved.Blockers)
			}
			if selected == 0 && len(solved.Steps) != 10 {
				t.Fatal("source must remove owned resources and foreign attachment", len(solved.Steps))
			}
			if selected == 3 && (len(solved.Steps) != 1 || len(solved.ImpactItems) != 1) {
				t.Fatal("attachment must delegate its one readonly view", solved)
			}
			for _, step := range solved.Steps {
				value := assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.ID == step.AssetID })]
				request := servicePlanRequest(solved, assets, value)
				driver, err := r.ResolveAction(context.Background(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				result, err := driver.Execute(context.Background(), request)
				if err != nil {
					t.Fatal("Kusto delete", value.Identity.NativeID, err)
				}
				kustoAfterDelete(s)
				payload, _ := json.Marshal(request)
				json.Unmarshal(payload, &request)
				payload, _ = json.Marshal(result)
				json.Unmarshal(payload, &result)
				driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				waited, err := driver.Wait(context.Background(), request, result)
				if err != nil || !waited.Done {
					t.Fatal("Kusto resumed readback", value.Identity.NativeID, err, waited)
				}
			}
			if selected != 0 && s.gone[assets[0].Identity.NativeID] {
				t.Fatal("deleting a reference removed the source cluster")
			}
			if selected == 0 && s.gone[assets[1].Identity.NativeID] {
				t.Fatal("source deletion removed the follower cluster")
			}
			if slices.Contains(s.deletes, assets[4].Identity.NativeID) {
				t.Fatal("read-only follower received DELETE")
			}
		})
	}
}
