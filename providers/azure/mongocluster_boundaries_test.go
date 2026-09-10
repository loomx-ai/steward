package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestMongoClusterReplicaProtectionAndOwnership(t *testing.T) {
	for _, mode := range []string{"protected-replica", "protected-source", "unrelated-prerequisite", "missing-reviewed-replica", "unreviewed-user", "unreadable-prerequisite"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := mongoClusterScenario(t)
			if mode == "protected-replica" || mode == "protected-source" {
				index := 4
				if mode == "protected-source" {
					index = 0
				}
				raw := s.records[assets[index].Identity.NativeID]
				raw["tags"] = map[string]any{"steward:protected": "true"}
				assets[index] = dnsAsset(t, r, raw)
			}
			contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
			contribution, err := contributor.Contribute(context.Background(), "scope", assets)
			if err != nil {
				t.Fatal(err)
			}
			for _, binding := range contribution.Bindings {
				if binding.ControllerAssetID == assets[0].ID && binding.ManagedAssetID == assets[4].ID {
					t.Fatal("replica was modeled as an exclusively owned child")
				}
			}
			target := assets[0]
			if mode == "protected-source" {
				target = assets[4]
			}
			input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{target.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
			// The application maps native inventory protection into plan policies.
			for _, value := range assets {
				if value.Normalized["cleanup_protected"] == true {
					input.Protections = append(input.Protections, plan.ProtectionPolicy{AssetID: value.ID, Protected: true})
				}
			}
			solved, err := plan.Solve(input)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "protected-replica" {
				if len(solved.Blockers) == 0 {
					t.Fatal("protected replica did not block its source deletion")
				}
				return
			}
			if len(solved.Blockers) != 0 {
				t.Fatal(solved.Blockers)
			}
			request := servicePlanRequest(solved, assets, target)
			for _, prerequisite := range request.PrerequisiteDeletions {
				s.gone[prerequisite.Asset.Identity.NativeID] = true
			}
			mongoClusterAfterDelete(s)
			switch mode {
			case "unrelated-prerequisite":
				for i := range request.PrerequisiteDeletions {
					if request.PrerequisiteDeletions[i].Asset.Identity.NativeType == mongoClusterType {
						// A restore history does not make this cluster a replica.
						copy := map[string]any{}
						for key, value := range request.PrerequisiteDeletions[i].Asset.Normalized {
							copy[key] = value
						}
						copy["replica"] = map[string]any{"role": "Primary"}
						copy["restoreParameters"] = map[string]any{"sourceResourceId": target.Identity.NativeID}
						request.PrerequisiteDeletions[i].Asset.Normalized = copy
					}
				}
			case "missing-reviewed-replica":
				delete(s.gone, assets[4].Identity.NativeID)
				request.PrerequisiteDeletions = slices.DeleteFunc(request.PrerequisiteDeletions, func(item contracts.ActionImpact) bool { return item.Asset.Identity.NativeType == mongoClusterType })
			case "unreadable-prerequisite":
				s.status[assets[4].Identity.NativeID] = 403
			case "unreviewed-user":
				delete(s.gone, assets[3].Identity.NativeID)
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			_, err = driver.Execute(context.Background(), request)
			if mode == "protected-source" {
				if err != nil || !s.gone[target.Identity.NativeID] || s.gone[assets[0].Identity.NativeID] {
					t.Fatal("replica deletion affected protected source", err)
				}
			} else if err == nil || len(s.deletes) != 0 {
				t.Fatal("cluster bypassed reviewed native prerequisites", err)
			}
		})
	}
}

func TestMongoClusterInventoryPaginationBoundaries(t *testing.T) {
	for _, mode := range []string{"complete", "duplicate", "cycle", "foreign-host", "foreign-subscription", "foreign-collection", "wrong-version", "partial", "forbidden", "missing-array", "parent-config", "parent-private"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := mongoClusterScenario(t)
			parent := assets[0].Identity.NativeID
			path := parent + "/users"
			first := s.records[assets[3].Identity.NativeID]
			payload, _ := json.Marshal(first)
			var second map[string]any
			json.Unmarshal(payload, &second)
			second["id"], second["name"] = path+"/other-user", "other-user"
			s.add(second, mongoClusterVersion)
			next := apiURL(path, mongoClusterVersion) + "&$skiptoken=native-page-2"
			switch mode {
			case "foreign-host":
				next = strings.Replace(next, "management.azure.com", "evil.invalid", 1)
			case "foreign-subscription":
				next = strings.Replace(next, testSubscription, testTenant, 1)
			case "foreign-collection":
				next = strings.Replace(next, "/users?", "/firewallRules?", 1)
			case "wrong-version":
				next = strings.Replace(next, mongoClusterVersion, "2025-09-01", 1)
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
					object(s.records[parent]["properties"])["compute"] = map[string]any{"tier": "M80"}
				case "parent-private":
					object(s.records[parent]["properties"])["connectionString"] = "changed-private-value"
				}
				return jsonResponse(200, map[string]any{"value": []any{second}}, nil), true
			}
			request := productRequest(r, mongoClusterUserType)
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
				if err != nil || !complete || count != 3 {
					t.Fatal("DocumentDB complete pagination", err, count, complete)
				}
			} else if err == nil || complete {
				t.Fatal("incomplete DocumentDB collection accepted", count)
			}
		})
	}
}

func TestMongoClusterOperationEndpointsReceiptsAndReadback(t *testing.T) {
	endpoint := apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.DocumentDB/locations/westus2/mongoClusterAzureAsyncOperation/11111111-2222-3333-4444-555555555555", mongoClusterVersion)
	for _, valid := range []string{endpoint, strings.Replace(endpoint, "mongoClusterAzureAsyncOperation", "mongoClusterOperationResults", 1), endpoint + "&t=t-value&c=c-value&s=s-value&h=h-value"} {
		if err := validateMongoClusterOperationURL(testSubscription, "westus2", mongoClusterVersion, valid); err != nil {
			t.Fatal(err)
		}
	}
	for _, invalid := range []string{strings.Replace(endpoint, testSubscription, testTenant, 1), strings.Replace(endpoint, "DocumentDB", "Compute", 1), strings.Replace(endpoint, mongoClusterVersion, "2025-09-01", 1), strings.Replace(endpoint, "management.azure.com", "evil.invalid", 1), strings.Replace(endpoint, "https://", "http://", 1), strings.Replace(endpoint, "westus2", "eastus2", 1), strings.Replace(endpoint, "mongoClusterAzureAsyncOperation", "operationsStatus", 1), strings.Replace(endpoint, "mongoClusterAzureAsyncOperation", "mongoClusters", 1), endpoint + "#fragment", endpoint + "&api-version=" + mongoClusterVersion, endpoint + "&t=alone", endpoint + "&t=1&c=2&s=3&h=4&unknown=5", strings.Replace(endpoint, "/westus2/", "/../", 1), strings.Replace(endpoint, "/westus2/", "/%77estus2/", 1), endpoint + strings.Repeat("a", 33*1024)} {
		if err := validateMongoClusterOperationURL(testSubscription, "westus2", mongoClusterVersion, invalid); err == nil {
			t.Fatal("invalid DocumentDB operation accepted")
		}
	}
	for _, mode := range []string{"success-live", "success-absent", "failed", "canceled", "poll-partial", "poll-forbidden", "wrong-id", "wrong-name", "wrong-resource", "wrong-receipt", "wrong-request", "expired-live", "expired-absent", "readback-partial", "readback-forbidden", "readback-wrong-id", "delete-partial"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := mongoClusterScenario(t)
			target := assets[1]
			pollCalls := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					if mode == "delete-partial" {
						return jsonResponse(206, nil, nil), true
					}
					return jsonResponse(202, nil, http.Header{"Azure-Asyncoperation": {endpoint}}), true
				}
				if req.URL.String() == endpoint {
					pollCalls++
					body := map[string]any{"status": "Succeeded"}
					switch mode {
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
						body["resourceId"] = assets[2].Identity.NativeID
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
			case "success-absent", "expired-absent":
				s.gone[target.Identity.NativeID] = true
			case "wrong-receipt":
				result.Data["mongocluster_operation_binding"] = "wrong-binding"
			case "wrong-request":
				request.Asset = assets[2]
			case "readback-partial":
				s.status[target.Identity.NativeID] = 206
			case "readback-forbidden":
				s.status[target.Identity.NativeID] = 403
			case "readback-wrong-id":
				s.records[target.Identity.NativeID]["id"] = target.Identity.NativeID + "changed"
			}
			waited, err := driver.Wait(context.Background(), request, result)
			switch mode {
			case "success-absent", "expired-absent":
				if err != nil || !waited.Done {
					t.Fatal("native absence not accepted", waited, err)
				}
			case "success-live", "expired-live":
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

func TestMongoClusterCurrentReferencesAndPrivateSnapshot(t *testing.T) {
	_, r, assets := mongoClusterScenario(t)
	c, _ := r.resolve(context.Background(), "connection")
	root := assets[0].Identity.NativeID
	copyID := root + "-restored"
	identity := "/subscriptions/" + testSubscription + "/resourcegroups/testgroup/providers/Microsoft.ManagedIdentity/userAssignedIdentities/encryption"
	raw := mongoClusterExampleBody(t, "MongoClusters_Get")
	raw["id"] = copyID
	props := object(raw["properties"])
	props["restoreParameters"] = map[string]any{"sourceResourceId": root}
	props["replicaParameters"] = map[string]any{"sourceResourceId": root}
	props["encryption"] = map[string]any{"customerManagedKeyEncryption": map[string]any{"keyEncryptionKeyIdentity": map[string]any{"identityType": "UserAssignedIdentity", "userAssignedIdentityResourceId": identity}, "keyEncryptionKeyUrl": "https://sample.vault.azure.net/keys/encryption/version"}}
	refs := references(mongoClusterType, copyID, raw)
	if len(refs[mongoClusterType]) != 0 || !slices.Equal(refs["Microsoft.ManagedIdentity/userAssignedIdentities"], []string{strings.ToLower(identity)}) {
		t.Fatal("restore history was treated as a live replica", refs)
	}
	props["replica"] = map[string]any{"role": "GeoAsyncReplica", "sourceResourceId": root, "replicationState": "Active"}
	if !slices.Equal(references(mongoClusterType, copyID, raw)[mongoClusterType], []string{root}) {
		t.Fatal("missing current replication dependency")
	}
	props["privateCounter"] = json.Number("9007199254740993")
	props["connectionString"] = "mongodb://private-user:private-password@host"
	before := c.privateConfiguration(mongoClusterSnapshot(mongoClusterType, raw))
	snapshot := mongoClusterSnapshot(mongoClusterType, raw)
	if object(snapshot["properties"])["privateCounter"] != json.Number("9007199254740993") {
		t.Fatal("large integer changed while binding configuration")
	}
	object(props["backup"])["earliestRestoreTime"] = "2026-08-01T00:00:00Z"
	props["clusterStatus"] = "Updating"
	if before != c.privateConfiguration(mongoClusterSnapshot(mongoClusterType, raw)) || mongoClusterReady(mongoClusterType, raw) == nil {
		t.Fatal("restore clock or transient state was confused with configuration")
	}
	props["connectionString"] = "mongodb://private-user:other-password@host"
	if before == c.privateConfiguration(mongoClusterSnapshot(mongoClusterType, raw)) {
		t.Fatal("private configuration changed without invalidating review")
	}
	item, err := r.inventoryItem(context.Background(), c, raw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(item)
	if bytes.Contains(data, []byte("other-password")) || bytes.Contains(data, []byte("private-user")) {
		t.Fatal("private database connection string reached inventory")
	}
}
