package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
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

func redisExample(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/redis/" + name + ".json")
	var example map[string]any
	if err != nil || json.Unmarshal(payload, &example) != nil {
		t.Fatal("invalid Redis example", err)
	}
	return example
}
func redisExampleBody(t *testing.T, name string) map[string]any {
	t.Helper()
	return object(object(object(redisExample(t, name)["responses"])["200"])["body"])
}
func redisScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	group := root + "/resourcegroups/rg1"
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
	var raws []map[string]any
	for _, name := range []string{"RedisCacheGet", "RedisCacheAccessPolicyGet", "RedisCacheAccessPolicyAssignmentGet", "RedisCacheFirewallRuleGet", "RedisCacheLinkedServer_Get", "RedisCachePatchSchedulesGet", "RedisCacheGetPrivateEndpointConnection", "RedisEnterpriseGet", "RedisEnterpriseDatabasesGet", "RedisEnterpriseAccessPolicyAssignmentGet", "RedisEnterpriseGetPrivateEndpointConnection"} {
		raw := redisExampleBody(t, name)
		payload, _ := json.Marshal(raw)
		payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
		payload = bytes.ReplaceAll(payload, []byte("e7b5a9d2-6b6a-4d2f-9143-20d9a10f5b8f"), []byte(testSubscription))
		payload = bytes.ReplaceAll(payload, []byte("rgtest01"), []byte("rg1"))
		payload = bytes.ReplaceAll(payload, []byte("cachetest01"), []byte("cache1"))
		json.Unmarshal(payload, &raw)
		// This official Enterprise example incorrectly carries a classic Redis
		// ID/type. Repair only the composite protocol scenario; provenance and
		// the separate rejection check retain the original response unchanged.
		if name == "RedisEnterpriseAccessPolicyAssignmentGet" {
			raw["id"] = group + "/providers/Microsoft.Cache/redisEnterprise/cache1/databases/default/accessPolicyAssignments/accessPolicyAssignmentName1"
			raw["type"] = redisDatabaseAssignmentType
		}
		id, typ, err := parseID(text(raw["id"]))
		if err != nil {
			t.Fatal(err)
		}
		raw["id"], raw["type"] = id, redisKind(typ)
		raws = append(raws, raw)
	}
	primary := raws[0]
	peerPayload, _ := json.Marshal(primary)
	var peer map[string]any
	json.Unmarshal(peerPayload, &peer)
	peer["id"] = redisRootID(text(primary["id"]))[:strings.LastIndex(text(primary["id"]), "/")] + "/cache2"
	peer["name"], peer["location"] = "cache2", "West US"
	object(peer["properties"])["hostName"] = "cache2.redis.cache.windows.net"
	object(peer["properties"])["privateEndpointConnections"] = []any{}
	reversePayload, _ := json.Marshal(raws[4])
	var reverse map[string]any
	json.Unmarshal(reversePayload, &reverse)
	reverse["id"], reverse["name"] = text(peer["id"])+"/linkedservers/cache1", "cache1"
	object(reverse["properties"])["linkedRedisCacheId"] = text(primary["id"])
	object(reverse["properties"])["linkedRedisCacheLocation"] = text(primary["location"])
	object(reverse["properties"])["serverRole"] = "Primary"
	object(peer["properties"])["linkedServers"] = []any{map[string]any{"id": text(reverse["id"])}}
	builtin := map[string]any{"id": text(primary["id"]) + "/accesspolicies/data owner", "name": "Data Owner", "type": redisPolicyType, "properties": map[string]any{"type": "BuiltIn", "permissions": "+@all allkeys", "provisioningState": "Succeeded"}}
	raws = append(raws, peer, reverse, builtin)
	for _, raw := range raws {
		if text(raw["type"]) == redisType || text(raw["type"]) == redisEnterpriseType {
			connections := []any{}
			for _, child := range raws {
				if strings.HasPrefix(text(child["id"]), text(raw["id"])+"/privateendpointconnections/") {
					connections = append(connections, map[string]any{"id": text(child["id"]), "properties": child["properties"]})
				}
			}
			object(raw["properties"])["privateEndpointConnections"] = connections
		}
	}
	for _, raw := range raws {
		id := text(raw["id"])
		kind := text(raw["type"])
		version := "2024-11-01"
		if strings.HasPrefix(kind, redisEnterpriseType) {
			version = "2025-07-01"
		}
		s.add(raw, version)
		collection := id[:strings.LastIndex(id, "/")]
		if kind == redisType || kind == redisEnterpriseType {
			collection = root + "/providers/" + strings.ToLower(kind)
		}
		s.lists[collection] = append(s.lists[collection], raw)
		s.version[collection] = version
	}
	for _, raw := range raws {
		id := text(raw["id"])
		kind := text(raw["type"])
		if kind == redisType || kind == redisEnterpriseType || kind == redisDatabaseType {
			for _, child := range serviceChildKinds(kind) {
				path := id + "/" + strings.ToLower(last(child))
				if _, exists := s.lists[path]; !exists {
					s.lists[path] = []any{}
				}
				s.version[path] = s.version[id]
			}
		}
	}
	r := s.runtime(t)
	assets := []asset.Asset{}
	for _, raw := range raws {
		assets = append(assets, dnsAsset(t, r, raw))
	}
	return s, r, assets
}
func TestRedisNativeSchemasAndProvenance(t *testing.T) {
	payload, err := os.ReadFile("fixtures/redis/sources.json")
	var manifest []map[string]string
	if err != nil || json.Unmarshal(payload, &manifest) != nil || len(manifest) != 35 {
		t.Fatal("invalid Redis sources")
	}
	for _, entry := range manifest {
		payload, err := os.ReadFile("fixtures/redis/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] {
			t.Fatal("Redis example changed")
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
		if !strings.Contains(doc.SourceURI, "/specification/redis/") && !strings.Contains(doc.SourceURI, "/specification/redisenterprise/") {
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
				if entry["operation"] != text(op["operationId"]) || !strings.HasPrefix(entry["source_uri"], doc.SourceURI[:strings.LastIndex(doc.SourceURI, "/")]+"/") {
					continue
				}
				body := redisExampleBody(t, strings.TrimSuffix(entry["file"], ".json"))
				payload, _ := json.Marshal(body)
				value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
				if entry["file"] == "RedisCacheAsyncOperationStatus.json" {
					if schema.Validate(value) == nil {
						t.Fatal("native null/schema discrepancy unexpectedly disappeared")
					}
					// The example sends null for five non-nullable optional fields.
					// Validate the remaining shape without editing the source fixture.
					for _, key := range []string{"endTime", "error", "percentComplete", "properties", "startTime"} {
						delete(value.(map[string]any), key)
					}
				}
				if entry["file"] == "RedisEnterpriseList.json" {
					if schema.Validate(value) == nil || value.(map[string]any)["nextLink"] != nil {
						t.Fatal("native null nextLink discrepancy unexpectedly disappeared")
					}
					delete(value.(map[string]any), "nextLink")
				}
				if err := schema.Validate(value); err != nil {
					t.Fatalf("native %s: %v", op["operationId"], err)
				}
				checked++
			}
		}
	}
	if checked != 24 {
		t.Fatal("missing Redis schema checks", checked)
	}
	bad := redisExampleBody(t, "RedisEnterpriseAccessPolicyAssignmentGet")
	if validResourceResponse(response{status: 200, data: bad}, strings.Replace(text(bad["id"]), "/redis/cache1/", "/redisEnterprise/cache1/databases/default/", 1), redisDatabaseAssignmentType) {
		t.Fatal("wrong classic identity in native Enterprise example was accepted")
	}
}
func TestRedisNativeDiscoveryAndOrderedCleanup(t *testing.T) {
	kinds := []string{redisType, redisPolicyType, redisAssignmentType, redisFirewallType, redisLinkType, redisPatchType, redisConnectionType, redisEnterpriseType, redisDatabaseType, redisDatabaseAssignmentType, redisEnterpriseConnectionType}
	for _, kind := range kinds {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := redisScenario(t)
			target := cdnAsset(t, assets, kind)
			items := redisProductItems(t, r, kind)
			count := 1
			if kind == redisType || kind == redisPolicyType || kind == redisLinkType {
				count = 2
			}
			if len(items) != count {
				t.Fatal("native Redis inventory count", len(items), count)
			}
			request, input := dnsRequest(t, r, assets, target)
			solved, err := plan.Solve(input)
			if err != nil || len(solved.Blockers) > 0 {
				t.Fatal("Redis plan", err, solved.Blockers)
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if len(request.PrerequisiteDeletions) > 0 {
				if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) > 0 {
					t.Fatal("Redis parent bypassed live prerequisites")
				}
			}
			for _, step := range solved.Steps {
				if step.Action != "delete" {
					t.Fatal("unexpected Redis preparation", step.Action)
				}
				value := assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.ID == step.AssetID })]
				subrequest := servicePlanRequest(solved, assets, value)
				actionDriver, _ := r.ResolveAction(context.Background(), "connection", value)
				result, err := actionDriver.Execute(context.Background(), subrequest)
				if err != nil {
					t.Fatalf("Redis delete %s: %v", value.Identity.NativeType, err)
				}
				for _, impact := range subrequest.LifecycleImpacts {
					s.gone[impact.Asset.Identity.NativeID] = true
				}
				for _, raw := range s.records {
					if text(raw["type"]) == redisType {
						links := []any{}
						for _, row := range array(object(raw["properties"])["linkedServers"]) {
							if !s.gone[strings.ToLower(text(object(row)["id"]))] {
								links = append(links, row)
							}
						}
						object(raw["properties"])["linkedServers"] = links
						connections := []any{}
						for _, row := range array(object(raw["properties"])["privateEndpointConnections"]) {
							if !s.gone[strings.ToLower(text(object(row)["id"]))] {
								connections = append(connections, row)
							}
						}
						object(raw["properties"])["privateEndpointConnections"] = connections
					}
					if text(raw["type"]) == redisEnterpriseType {
						connections := []any{}
						for _, row := range array(object(raw["properties"])["privateEndpointConnections"]) {
							if !s.gone[strings.ToLower(text(object(row)["id"]))] {
								connections = append(connections, row)
							}
						}
						object(raw["properties"])["privateEndpointConnections"] = connections
					}
				}
				payload, _ := json.Marshal(subrequest)
				json.Unmarshal(payload, &subrequest)
				payload, _ = json.Marshal(result)
				json.Unmarshal(payload, &result)
				actionDriver, _ = r.ResolveAction(context.Background(), "connection", subrequest.Asset)
				wait, err := actionDriver.Wait(context.Background(), subrequest, result)
				if err != nil || !wait.Done {
					t.Fatalf("Redis resumed readback %s: %+v %v", value.Identity.NativeType, wait, err)
				}
			}
			if !s.gone[target.Identity.NativeID] || len(s.deletes) != len(solved.Steps) {
				t.Fatal("Redis execution missed a native delete")
			}
		})
	}
}
func TestRedisDirectChildrenRejectParentAndPrivateConfigurationDrift(t *testing.T) {
	for _, kind := range []string{redisType, redisPolicyType, redisAssignmentType, redisFirewallType, redisLinkType, redisPatchType, redisConnectionType, redisEnterpriseType, redisDatabaseType, redisDatabaseAssignmentType, redisEnterpriseConnectionType} {
		for _, mode := range []string{"configuration", "private", "parent", "created", "protected", "partial", "foreign"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, r, assets := redisScenario(t)
				target := cdnAsset(t, assets, kind)
				request := redisReadyRequest(t, s, r, assets, target)
				raw := s.records[target.Identity.NativeID]
				switch mode {
				case "configuration":
					object(raw["properties"])["changed-setting"] = true
				case "private":
					object(raw["properties"])["rdb-storage-connection-string"] = "never-public"
				case "parent":
					if kind == redisType || kind == redisEnterpriseType {
						raw["sku"] = map[string]any{"name": "changed"}
					} else {
						s.records[redisParentID(target.Identity.NativeID)]["tags"] = map[string]any{"new": "value"}
					}
				case "created":
					raw["systemData"] = map[string]any{"createdAt": "2026-01-01T00:00:00Z"}
				case "protected":
					raw["tags"] = map[string]any{"steward:protected": "true"}
				case "partial":
					s.status[target.Identity.NativeID] = 206
				case "foreign":
					raw["id"] = strings.Replace(target.Identity.NativeID, testSubscription, testTenant, 1)
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", target)
				if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) > 0 {
					t.Fatal("Redis drift allowed a delete", mode)
				}
			})
		}
	}
}

func redisProductItems(t *testing.T, r *Runtime, kind string) []contracts.InventoryItem {
	t.Helper()
	request := productRequest(r, kind)
	items := []contracts.InventoryItem{}
	for pages := 0; pages < 100; pages++ {
		batch, err := r.List(context.Background(), request)
		if err != nil {
			t.Fatal("native Redis list", kind, err)
		}
		items = append(items, batch.Items...)
		if batch.Complete {
			return items
		}
		if batch.NextCursor == "" {
			t.Fatal("incomplete Redis cursor")
		}
		request.Cursor = batch.NextCursor
	}
	t.Fatal("Redis page cycle")
	return nil
}

// Simulate only already-reviewed prior steps, then prove the unmodified target
// is deletable before each drift mutation. Otherwise a live prerequisite could
// make a negative test pass even with a missing configuration guard.
func redisReadyRequest(t *testing.T, s *dnsScenario, r *Runtime, assets []asset.Asset, target asset.Asset) contracts.ActionRequest {
	t.Helper()
	request, input := dnsRequest(t, r, assets, target)
	solved, err := plan.Solve(input)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range solved.Steps {
		if step.AssetID == target.ID {
			continue
		}
		value := assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.ID == step.AssetID })]
		prior := servicePlanRequest(solved, assets, value)
		s.gone[value.Identity.NativeID] = true
		for _, impact := range prior.LifecycleImpacts {
			s.gone[impact.Asset.Identity.NativeID] = true
		}
	}
	for _, raw := range s.records {
		if text(raw["type"]) != redisType && text(raw["type"]) != redisEnterpriseType {
			continue
		}
		for _, field := range []string{"linkedServers", "privateEndpointConnections"} {
			if value, present := object(raw["properties"])[field]; present {
				remaining := []any{}
				for _, row := range array(value) {
					if !s.gone[strings.ToLower(text(object(row)["id"]))] {
						remaining = append(remaining, row)
					}
				}
				object(raw["properties"])[field] = remaining
			}
		}
	}
	driver, _ := r.ResolveAction(context.Background(), "connection", target)
	check, err := driver.Preflight(context.Background(), request)
	if err != nil || !check.Allowed || check.Absent {
		t.Fatal("unmodified Redis fixture is not deletable", target.Identity.NativeType, err, check.Reason)
	}
	return request
}
