package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const mongoClusterVersion = "2026-06-01"

func mongoClusterExample(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/mongocluster/" + name + ".json")
	var result map[string]any
	if err != nil || json.Unmarshal(payload, &result) != nil {
		t.Fatal("invalid DocumentDB example", err)
	}
	return result
}

func mongoClusterExampleBody(t *testing.T, name string) map[string]any {
	return object(object(object(mongoClusterExample(t, name)["responses"])["200"])["body"])
}

// Compose two independent clusters and their proxy resources from unchanged
// native examples. Replica membership uses the shape seen in CLI recordings.
func mongoClusterScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	group := root + "/resourcegroups/testgroup"
	collection := root + "/providers/microsoft.documentdb/mongoclusters"
	primary := group + "/providers/microsoft.documentdb/mongoclusters/primary"
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
	s.version[collection] = mongoClusterVersion
	var raws []map[string]any
	for _, name := range []string{"primary", "replica"} {
		cluster := mongoClusterExampleBody(t, "MongoClusters_Get")
		id := group + "/providers/microsoft.documentdb/mongoclusters/" + name
		cluster["id"], cluster["name"] = id, name
		props := object(cluster["properties"])
		props["clusterStatus"] = "Ready"
		props["privateEndpointConnections"] = []any{}
		if name == "replica" {
			props["replica"] = map[string]any{"role": "GeoAsyncReplica", "replicationState": "Active", "sourceResourceId": primary}
			cluster["location"] = "eastus2"
			s.lists[primary+"/replicas"] = []any{map[string]any{"id": id, "name": name, "location": cluster["location"]}}
		}
		s.add(cluster, mongoClusterVersion)
		raws = append(raws, cluster)
		s.lists[collection] = append(s.lists[collection], cluster)
		s.lists[id+"/replicas"] = []any{}
		s.version[id+"/replicas"] = mongoClusterVersion
		for i, example := range []string{"MongoClusters_FirewallRuleGet", "MongoClusters_PrivateEndpointConnectionGet", "MongoClusters_UserGet"} {
			raw := mongoClusterExampleBody(t, example)
			kind := mongoClusterOwnedKinds()[i]
			path := id + "/" + strings.ToLower(last(kind))
			raw["id"] = path + "/" + strings.ToLower(last(text(raw["id"])))
			raw["name"] = last(text(raw["id"]))
			s.add(raw, mongoClusterVersion)
			raws = append(raws, raw)
			s.lists[path], s.version[path] = []any{raw}, mongoClusterVersion
			if kind == mongoClusterPECType {
				props["privateEndpointConnections"] = []any{map[string]any{"id": raw["id"]}}
			}
		}
	}
	r := s.runtime(t)
	var assets []asset.Asset
	for _, raw := range raws {
		value := dnsAsset(t, r, raw)
		if value.Identity.NativeType != mongoClusterType {
			value.Location = text(s.records[redisParentID(value.Identity.NativeID)]["location"])
		}
		assets = append(assets, value)
	}
	return s, r, assets
}

func mongoClusterAfterDelete(s *dnsScenario) {
	for _, raw := range s.records {
		if raw["type"] != mongoClusterType {
			continue
		}
		props := object(raw["properties"])
		index := []any{}
		for _, value := range array(props["privateEndpointConnections"]) {
			if !s.gone[strings.ToLower(text(object(value)["id"]))] {
				index = append(index, value)
			}
		}
		props["privateEndpointConnections"] = index
		raw["etag"] = "after-reviewed-child"
		object(props["backup"])["earliestRestoreTime"] = "2026-08-01T00:00:00Z"
	}
}

func TestMongoClusterNativeInventoryAndReviewedCleanup(t *testing.T) {
	for _, selected := range []int{0, 1, 2, 3, 4} {
		t.Run([]string{"primary", "firewall", "connection", "user", "replica"}[selected], func(t *testing.T) {
			s, r, assets := mongoClusterScenario(t)
			target := assets[selected]
			inventory := productRequest(r, target.Identity.NativeType)
			var items []contracts.InventoryItem
			for {
				batch, err := r.List(context.Background(), inventory)
				if err != nil {
					t.Fatal("DocumentDB inventory", err)
				}
				items = append(items, batch.Items...)
				if batch.Complete {
					break
				}
				inventory.Cursor = batch.NextCursor
			}
			if len(items) != 2 {
				t.Fatal("DocumentDB inventory", len(items))
			}
			request, input := dnsRequest(t, r, assets, target)
			solved, err := plan.Solve(input)
			if err != nil || len(solved.Blockers) != 0 {
				t.Fatal("DocumentDB plan", err, solved.Blockers)
			}
			if selected == 0 || selected == 4 {
				expected := 8
				prerequisites := 4
				if selected == 4 {
					expected, prerequisites = 4, 3
				}
				if len(solved.Steps) != expected || len(request.PrerequisiteDeletions) != prerequisites || len(request.LifecycleImpacts) != 0 {
					t.Fatal("DocumentDB deletion dependencies", len(solved.Steps), len(request.PrerequisiteDeletions), len(request.LifecycleImpacts))
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", target)
				if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
					t.Fatal("cluster bypassed live prerequisites")
				}
			}
			for _, step := range solved.Steps {
				value := assets[slices.IndexFunc(assets, func(candidate asset.Asset) bool { return candidate.ID == step.AssetID })]
				request := servicePlanRequest(solved, assets, value)
				driver, err := r.ResolveAction(context.Background(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				result, err := driver.Execute(context.Background(), request)
				if err != nil {
					t.Fatal("DocumentDB delete", value.Identity.NativeID, err)
				}
				mongoClusterAfterDelete(s)
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
					t.Fatal("DocumentDB resumed readback", err, waited)
				}
			}
			if selected == 4 && s.gone[assets[0].Identity.NativeID] {
				t.Fatal("replica deletion removed its source")
			}
			for _, id := range s.deletes {
				if !strings.Contains(id, "/microsoft.documentdb/mongoclusters/") {
					t.Fatal("DocumentDB removed an external identity or endpoint", id)
				}
			}
		})
	}
}

func TestMongoClusterLeafDriftAndProtection(t *testing.T) {
	for _, mode := range []string{"own-change", "private-change", "parent-change", "parent-private-change", "parent-protected", "parent-lock", "parent-forbidden", "parent-partial", "parent-absent", "parent-pending", "parent-promoting", "own-pending", "wrong-id", "wrong-type", "missing-properties"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := mongoClusterScenario(t)
			target := assets[3]
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			if checked, err := driver.Preflight(context.Background(), request); err != nil || !checked.Allowed {
				t.Fatal("invalid baseline", checked, err)
			}
			raw := s.records[target.Identity.NativeID]
			parent := s.records[assets[0].Identity.NativeID]
			switch mode {
			case "own-change":
				object(raw["properties"])["roles"] = []any{}
			case "private-change":
				object(raw["properties"])["password"] = "changed-secret"
			case "parent-change":
				object(parent["properties"])["compute"] = map[string]any{"tier": "M80"}
			case "parent-private-change":
				object(parent["properties"])["connectionString"] = "changed-secret"
			case "parent-protected":
				parent["tags"] = map[string]any{"steward:protected": "true"}
			case "parent-lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": assets[0].Identity.NativeID + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "parent-forbidden":
				s.status[assets[0].Identity.NativeID] = 403
			case "parent-partial":
				s.status[assets[0].Identity.NativeID] = 206
			case "parent-absent":
				s.gone[assets[0].Identity.NativeID] = true
			case "parent-pending":
				object(parent["properties"])["clusterStatus"] = "Updating"
			case "parent-promoting":
				object(object(parent["properties"])["replica"])["replicationState"] = "Reconfiguring"
			case "own-pending":
				object(raw["properties"])["provisioningState"] = "Updating"
			case "wrong-id":
				raw["id"] = target.Identity.NativeID + "changed"
			case "wrong-type":
				raw["type"] = cosmosMongoUserType
			case "missing-properties":
				delete(raw, "properties")
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("DocumentDB mutation bypassed changed protection or identity", err)
			}
		})
	}
}

func TestMongoClusterReplicaIndexBoundaries(t *testing.T) {
	for _, mode := range []string{"duplicate", "self", "foreign-subscription", "wrong-kind", "retarget", "wrong-role", "replica-forbidden", "replica-partial", "replica-absent", "list-forbidden", "list-partial", "missing-array", "configuration-reread", "private-reread", "parent-reread", "parent-last-pending", "connection-index", "last-connection-index"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := mongoClusterScenario(t)
			parent, replica := assets[0], assets[4]
			path := parent.Identity.NativeID + "/replicas"
			raw := s.records[replica.Identity.NativeID]
			switch mode {
			case "duplicate":
				s.lists[path] = append(s.lists[path], s.lists[path][0])
			case "self":
				s.lists[path] = []any{map[string]any{"id": parent.Identity.NativeID}}
			case "foreign-subscription":
				object(s.lists[path][0])["id"] = strings.Replace(replica.Identity.NativeID, testSubscription, "22222222-2222-2222-2222-222222222222", 1)
			case "wrong-kind":
				object(s.lists[path][0])["type"] = cosmosType
			case "retarget":
				object(object(raw["properties"])["replica"])["sourceResourceId"] = parent.Identity.NativeID + "other"
			case "wrong-role":
				object(raw["properties"])["replica"] = map[string]any{"role": "Primary"}
			case "replica-forbidden":
				s.status[replica.Identity.NativeID] = 403
			case "replica-partial":
				s.status[replica.Identity.NativeID] = 206
			case "replica-absent":
				s.status[replica.Identity.NativeID] = 404
			case "list-forbidden":
				s.status[path] = 403
			case "list-partial":
				s.status[path] = 206
			case "connection-index":
				object(s.records[parent.Identity.NativeID]["properties"])["privateEndpointConnections"] = []any{}
			}
			reads := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				id := strings.ToLower(req.URL.Path)
				if mode == "missing-array" && id == path {
					return jsonResponse(200, map[string]any{}, nil), true
				}
				if id == replica.Identity.NativeID {
					reads++
					if reads == 2 {
						switch mode {
						case "configuration-reread":
							object(raw["properties"])["serverVersion"] = "9.0"
						case "private-reread":
							object(raw["properties"])["connectionString"] = "changed-secret"
						}
					}
				}
				if id == parent.Identity.NativeID && (mode == "parent-reread" || mode == "parent-last-pending" || mode == "last-connection-index") {
					reads++
					if reads >= 2 {
						props := object(s.records[parent.Identity.NativeID]["properties"])
						switch mode {
						case "parent-reread":
							props["connectionString"] = "changed-parent-secret"
						case "parent-last-pending":
							props["clusterStatus"] = "Updating"
						case "last-connection-index":
							props["privateEndpointConnections"] = []any{map[string]any{"id": parent.Identity.NativeID + "/privateEndpointConnections/new"}}
						}
					}
				}
				return nil, false
			}
			c, _ := r.resolve(context.Background(), "connection")
			// Use an independent initial GET payload so transport mutations are
			// changes observed during the walk, not changes to its input object.
			payload, _ := json.Marshal(s.records[parent.Identity.NativeID])
			var initial map[string]any
			json.Unmarshal(payload, &initial)
			if _, err := c.mongoClusterChildren(context.Background(), parent.Identity, initial); err == nil {
				t.Fatal("incomplete or changing replica index accepted")
			}
		})
	}
}
