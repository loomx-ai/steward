package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func redisGeoScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	var raws, databases []map[string]any
	for i := 1; i <= 3; i++ {
		group := fmt.Sprintf("%s/resourcegroups/rg%d", root, i)
		s.lists[root+"/resourcegroups"] = append(s.lists[root+"/resourcegroups"], map[string]any{"id": group, "type": groupType})
		cluster := redisExampleBody(t, "RedisEnterpriseGet")
		cluster["id"] = group + fmt.Sprintf("/providers/microsoft.cache/redisenterprise/cache%d", i)
		cluster["name"] = fmt.Sprintf("cache%d", i)
		cluster["type"] = redisEnterpriseType
		object(cluster["properties"])["privateEndpointConnections"] = []any{}
		database := redisExampleBody(t, "RedisEnterpriseDatabasesGet")
		database["id"] = text(cluster["id"]) + "/databases/default"
		database["type"] = redisDatabaseType
		databases = append(databases, database)
		raws = append(raws, cluster, database)
		s.add(cluster, "2025-07-01")
		s.add(database, "2025-07-01")
		s.lists[root+"/providers/microsoft.cache/redisenterprise"] = append(s.lists[root+"/providers/microsoft.cache/redisenterprise"], cluster)
		s.lists[text(cluster["id"])+"/databases"] = []any{database}
		s.lists[text(cluster["id"])+"/privateendpointconnections"] = []any{}
		s.lists[text(database["id"])+"/accesspolicyassignments"] = []any{}
	}
	for _, db := range databases {
		links := []any{}
		for _, peer := range databases {
			links = append(links, map[string]any{"id": text(peer["id"]), "state": "Linked"})
		}
		object(db["properties"])["geoReplication"] = map[string]any{"groupNickname": "group-name", "linkedDatabases": links}
	}
	r := s.runtime(t)
	assets := []asset.Asset{}
	for _, raw := range raws {
		assets = append(assets, dnsAsset(t, r, raw))
	}
	return s, r, assets
}
func TestRedisGeoNativeDeletionAllowsVerifiedDeparturesAndChecksSurvivors(t *testing.T) {
	s, r, assets := redisGeoScenario(t)
	for _, target := range []asset.Asset{assets[1], assets[3], assets[5]} {
		request := contracts.ActionRequest{Action: "delete", Asset: target}
		driver, _ := r.ResolveAction(context.Background(), "connection", target)
		result, err := driver.Execute(context.Background(), request)
		if err != nil {
			t.Fatal("healthy native geo database delete", err)
		}
		payload, _ := json.Marshal(request)
		json.Unmarshal(payload, &request)
		payload, _ = json.Marshal(result)
		json.Unmarshal(payload, &result)
		driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
		if len(s.deletes) < 3 {
			wait, err := driver.Wait(context.Background(), request, result)
			if err != nil || wait.Done {
				t.Fatal("database absence hid a surviving replication link", err)
			}
			check, err := driver.Preflight(context.Background(), request)
			if err != nil || check.Absent {
				t.Fatal("restart preflight hid replication link", err)
			}
		}
		for _, raw := range s.records {
			if text(raw["type"]) != redisDatabaseType {
				continue
			}
			geo := object(object(raw["properties"])["geoReplication"])
			peers := []any{}
			for _, row := range array(geo["linkedDatabases"]) {
				if !strings.EqualFold(text(object(row)["id"]), target.Identity.NativeID) {
					peers = append(peers, row)
				}
			}
			geo["linkedDatabases"] = peers
		}
		wait, err := driver.Wait(context.Background(), request, result)
		if err != nil || !wait.Done {
			t.Fatal("verified replication unlink did not finish", err, wait)
		}
	}
	if len(s.deletes) != 3 {
		t.Fatal("geo cleanup issued extra mutations")
	}
}
func TestRedisGeoChangesFailBeforeMutation(t *testing.T) {
	for _, mode := range []string{"join", "duplicate", "foreign-type", "missing-array", "not-linked", "peer-config", "peer-private", "peer-root", "peer-404", "peer-403", "peer-206", "peer-protected", "peer-root-protected", "peer-lock", "peer-group-managed", "live-unlink", "missing-self", "peer-topology", "peer-root-second-pass"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := redisGeoScenario(t)
			target, peer := assets[1], assets[3]
			raw := s.records[target.Identity.NativeID]
			geo := object(object(raw["properties"])["geoReplication"])
			peerRaw := s.records[peer.Identity.NativeID]
			peerGeo := object(object(peerRaw["properties"])["geoReplication"])
			switch mode {
			case "join":
				geo["linkedDatabases"] = append(array(geo["linkedDatabases"]), map[string]any{"id": strings.Replace(target.Identity.NativeID, "cache1", "new-cache", 1), "state": "Linked"})
			case "duplicate":
				geo["linkedDatabases"] = append(array(geo["linkedDatabases"]), array(geo["linkedDatabases"])[0])
			case "foreign-type":
				object(array(geo["linkedDatabases"])[0])["id"] = redisRootID(target.Identity.NativeID)
			case "missing-array":
				delete(geo, "linkedDatabases")
			case "not-linked":
				object(array(geo["linkedDatabases"])[0])["state"] = "LinkFailed"
			case "peer-config":
				object(peerRaw["properties"])["port"] = 10001
			case "peer-private":
				object(peerRaw["properties"])["redisConfiguration"] = map[string]any{"rdb-storage-connection-string": "never-public"}
			case "peer-root":
				s.records[redisRootID(peer.Identity.NativeID)]["tags"] = map[string]any{"owner": "changed"}
			case "peer-404":
				s.gone[peer.Identity.NativeID] = true
			case "peer-403":
				s.status[peer.Identity.NativeID] = 403
			case "peer-206":
				s.status[peer.Identity.NativeID] = 206
			case "peer-protected":
				peerRaw["tags"] = map[string]any{"steward:protected": "true"}
			case "peer-root-protected":
				s.records[redisRootID(peer.Identity.NativeID)]["tags"] = map[string]any{"steward:protected": "true"}
			case "peer-lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": peer.Identity.NativeID + "/providers/Microsoft.Authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "peer-group-managed":
				group := strings.Join(strings.Split(peer.Identity.NativeID, "/")[:5], "/")
				s.records[group] = map[string]any{"id": group, "managedBy": redisRootID(target.Identity.NativeID)}
			case "live-unlink":
				geo["linkedDatabases"] = slices.Delete(array(geo["linkedDatabases"]), 1, 2)
			case "missing-self":
				geo["linkedDatabases"] = array(geo["linkedDatabases"])[1:]
			case "peer-topology":
				peerGeo["linkedDatabases"] = array(peerGeo["linkedDatabases"])[1:]
			case "peer-root-second-pass":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.EqualFold(req.URL.Path, redisRootID(peer.Identity.NativeID)) {
						reads++
						if reads == 2 {
							s.records[redisRootID(peer.Identity.NativeID)]["tags"] = map[string]any{"new": "value"}
						}
					}
					return nil, false
				}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Action: "delete", Asset: target}); err == nil || len(s.deletes) > 0 {
				t.Fatal("uncertain geo group allowed mutation", mode)
			}
		})
	}
}
