package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestRedisNestedPaginationBindsParentsAndRejectsIncompleteCollections(t *testing.T) {
	for _, mode := range []string{"complete", "cycle", "version", "host", "subscription", "collection", "parent-change", "root-change", "root-during-list", "partial", "denied", "missing-array", "duplicate", "foreign-child", "wrong-region"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := redisScenario(t)
			target := cdnAsset(t, assets, redisDatabaseAssignmentType)
			parentID, rootID := redisParentID(target.Identity.NativeID), redisRootID(target.Identity.NativeID)
			raw := s.records[target.Identity.NativeID]
			payload, _ := json.Marshal(raw)
			var second map[string]any
			json.Unmarshal(payload, &second)
			second["id"], second["name"] = target.Identity.NativeID+"2", last(target.Identity.NativeID)+"2"
			s.add(second, "2025-07-01")
			collection := redisParentID(target.Identity.NativeID) + "/accesspolicyassignments"
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if !strings.EqualFold(req.URL.Path, collection) {
					return nil, false
				}
				if mode == "root-during-list" {
					s.records[rootID]["tags"] = map[string]any{"changed": "root"}
				}
				if mode == "partial" || mode == "denied" {
					status := 206
					if mode == "denied" {
						status = 403
					}
					return jsonResponse(status, map[string]any{"value": []any{}}, nil), true
				}
				if mode == "missing-array" {
					return jsonResponse(200, map[string]any{}, nil), true
				}
				if mode == "duplicate" {
					return jsonResponse(200, map[string]any{"value": []any{raw, raw}}, nil), true
				}
				if req.URL.Query().Get("$skiptoken") == "second" && mode != "cycle" {
					return jsonResponse(200, map[string]any{"value": []any{second}}, nil), true
				}
				next := apiURL(collection, "2025-07-01") + "&%24skiptoken=second"
				switch mode {
				case "version":
					next = strings.Replace(next, "2025-07-01", "1900-01-01", 1)
				case "host":
					next = strings.Replace(next, "management.azure.com", "untrusted.invalid", 1)
				case "subscription":
					next = strings.Replace(next, testSubscription, testTenant, 1)
				case "collection":
					next = strings.Replace(next, "/accesspolicyassignments", "/privateendpointconnections", 1)
				case "foreign-child":
					return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": strings.Replace(target.Identity.NativeID, "/databases/default/", "/databases/foreign/", 1), "type": redisDatabaseAssignmentType}}}, nil), true
				}
				return jsonResponse(200, map[string]any{"value": []any{raw}, "nextLink": next}, nil), true
			}
			request := productRequest(r, redisDatabaseAssignmentType)
			if mode == "wrong-region" {
				request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "unrelatedregion"}
			}
			first, err := r.List(context.Background(), request)
			if !slices.Contains([]string{"complete", "cycle", "parent-change", "root-change", "wrong-region"}, mode) {
				if err == nil {
					t.Fatal("incomplete Redis collection accepted", mode)
				}
				return
			}
			if err != nil || first.Complete || first.NextCursor == "" {
				t.Fatal("first native Redis page", err)
			}
			if mode == "parent-change" {
				object(s.records[parentID]["properties"])["clientProtocol"] = "Plaintext"
			}
			if mode == "root-change" {
				s.records[rootID]["tags"] = map[string]any{"changed": "root"}
			}
			request.Cursor = first.NextCursor
			final, err := r.List(context.Background(), request)
			if mode == "cycle" || mode == "parent-change" || mode == "root-change" {
				if err == nil {
					t.Fatal("changed Redis continuation accepted")
				}
				return
			}
			count := 2
			if mode == "wrong-region" {
				count = 0
			}
			if err != nil || !final.Complete || len(first.Items)+len(final.Items) != count {
				t.Fatal("native Redis paging", err)
			}
		})
	}
}

func TestRedisSharedPrimaryLinkIsDeletedOnceForBothCaches(t *testing.T) {
	s, r, assets := redisScenario(t)
	primary := cdnAsset(t, assets, redisLinkType)
	peerID := strings.ToLower(text(primary.Normalized["linkedRedisCacheId"]))
	peer := assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.Identity.NativeID == peerID })]
	_, input := dnsRequest(t, r, assets, peer)
	solved, err := plan.Solve(input)
	if err != nil || len(solved.Blockers) > 0 {
		t.Fatal("secondary cache plan", err, solved.Blockers)
	}
	count := 0
	for _, step := range solved.Steps {
		if step.AssetID == primary.ID {
			count++
		}
	}
	if count != 1 {
		t.Fatal("secondary cache did not require exactly one primary unlink", count)
	}
	request := servicePlanRequest(solved, assets, peer)
	driver, _ := r.ResolveAction(context.Background(), "connection", peer)
	if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
		t.Fatal("secondary cache bypassed live primary link")
	}
	linkRequest := servicePlanRequest(solved, assets, primary)
	linkDriver, _ := r.ResolveAction(context.Background(), "connection", primary)
	result, err := linkDriver.Execute(context.Background(), linkRequest)
	if err != nil || len(s.deletes) != 1 || s.deletes[0] != primary.Identity.NativeID {
		t.Fatal("shared native unlink failed", err)
	}
	check, err := linkDriver.Preflight(context.Background(), linkRequest)
	if err != nil || check.Absent {
		t.Fatal("missing primary hid surviving reciprocal view", err)
	}
	wait, err := linkDriver.Wait(context.Background(), linkRequest, result)
	if err != nil || wait.Done {
		t.Fatal("missing primary hid surviving reciprocal view", err)
	}
	for _, impact := range linkRequest.LifecycleImpacts {
		s.gone[impact.Asset.Identity.NativeID] = true
	}
	for _, raw := range s.records {
		if text(raw["type"]) == redisType {
			object(raw["properties"])["linkedServers"] = []any{}
		}
	}
	wait, err = linkDriver.Wait(context.Background(), linkRequest, result)
	if err != nil || !wait.Done {
		t.Fatal("completed reciprocal unlink", err)
	}
	if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != 2 || s.deletes[1] != peerID {
		t.Fatal("secondary cache could not follow reviewed unlink", err)
	}
}

func TestRedisClassicLinkAmbiguityAndPeerProtectionFailBeforeMutation(t *testing.T) {
	for _, mode := range []string{"peer-config", "peer-private", "peer-changes-during-read", "peer-protected", "reverse-protected", "reverse-missing", "reverse-role", "duplicate-primary", "index-disagrees", "primary-missing", "peer-denied", "peer-partial", "peer-lock", "managed-peer-group", "secondary-direct"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := redisScenario(t)
			target := cdnAsset(t, assets, redisLinkType)
			request := redisReadyRequest(t, s, r, assets, target)
			peerID := strings.ToLower(text(target.Normalized["linkedRedisCacheId"]))
			reverseID := peerID + "/linkedservers/cache1"
			peer, reverse := s.records[peerID], s.records[reverseID]
			switch mode {
			case "peer-config":
				peer["tags"] = map[string]any{"changed": "peer"}
			case "peer-private":
				object(peer["properties"])["redisConfiguration"] = map[string]any{"rdb-storage-connection-string": "private-change"}
			case "peer-changes-during-read":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, peerID) {
						reads++
						if reads > 1 {
							peer["tags"] = map[string]any{"changed": "peer"}
						}
					}
					return nil, false
				}
			case "peer-protected":
				peer["tags"] = map[string]any{"steward:protected": "true"}
			case "reverse-protected":
				reverse["tags"] = map[string]any{"steward:protected": "true"}
			case "reverse-missing":
				s.gone[reverseID] = true
				object(peer["properties"])["linkedServers"] = []any{}
			case "reverse-role":
				object(reverse["properties"])["serverRole"] = "Secondary"
			case "duplicate-primary":
				payload, _ := json.Marshal(s.records[target.Identity.NativeID])
				var extra map[string]any
				json.Unmarshal(payload, &extra)
				extra["id"], extra["name"] = target.Identity.NativeID+"2", last(target.Identity.NativeID)+"2"
				s.add(extra, "2024-11-01")
				collection := redisRootID(target.Identity.NativeID) + "/linkedservers"
				s.lists[collection] = append(s.lists[collection], extra)
				object(s.records[redisRootID(target.Identity.NativeID)]["properties"])["linkedServers"] = append(array(object(s.records[redisRootID(target.Identity.NativeID)]["properties"])["linkedServers"]), map[string]any{"id": extra["id"]})
				// Root cleanup must reject two primary views of the same pair.
				c, _ := r.resolve(context.Background(), "connection")
				if _, err := c.redisIncomingLinks(context.Background(), peerID); err == nil {
					t.Fatal("duplicate primary pair accepted")
				}
				return
			case "index-disagrees":
				object(peer["properties"])["linkedServers"] = []any{}
			case "primary-missing":
				s.gone[target.Identity.NativeID] = true
				object(s.records[redisRootID(target.Identity.NativeID)]["properties"])["linkedServers"] = []any{}
				c, _ := r.resolve(context.Background(), "connection")
				if _, err := c.redisIncomingLinks(context.Background(), peerID); err == nil {
					t.Fatal("secondary view without primary accepted")
				}
				return
			case "peer-denied":
				s.status[peerID] = 403
			case "peer-partial":
				s.status[peerID] = 206
			case "peer-lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": peerID + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "managed-peer-group":
				groupID := strings.Join(strings.Split(peerID, "/")[:5], "/")
				s.records[groupID] = map[string]any{"id": groupID, "managedBy": "/managed/controller"}
			case "secondary-direct":
				target = assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.Identity.NativeID == reverseID })]
				request = contracts.ActionRequest{Asset: target, Action: "delete"}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("unsafe Redis link deleted", mode, err)
			}
		})
	}
}

func TestRedisPrivatePersistenceConfigurationIsRedactedAndBound(t *testing.T) {
	s, r, assets := redisScenario(t)
	target := cdnAsset(t, assets, redisType)
	raw := s.records[target.Identity.NativeID]
	object(raw["properties"])["redisConfiguration"] = map[string]any{"rdb-storage-connection-string": "redis-persistence-secret", "aof_storage_connection_string_0": "redis-aof-secret"}
	fresh := dnsAsset(t, r, raw)
	assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.ID == target.ID })] = fresh
	// Refresh child parent bindings for the intentional new inventory snapshot.
	for i, value := range assets {
		assets[i] = dnsAsset(t, r, s.records[value.Identity.NativeID])
	}
	request := redisReadyRequest(t, s, r, assets, fresh)
	payload, _ := json.Marshal(request)
	if strings.Contains(string(payload), "redis-persistence-secret") || strings.Contains(string(payload), "redis-aof-secret") {
		t.Fatal("Redis persistence secret stored in plan")
	}
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	c, _ := r.resolve(ctx, "connection")
	if _, err := c.redisResource(ctx, fresh.Identity.NativeID); err != nil {
		t.Fatal(err)
	}
	payload, _ = json.Marshal(logs)
	if len(logs) != 2 || strings.Contains(string(payload), "redis-persistence-secret") || strings.Contains(string(payload), "redis-aof-secret") {
		t.Fatal("Redis persistence secret escaped logs")
	}
	if text(object(object(raw["properties"])["redisConfiguration"])["rdb-storage-connection-string"]) != "redis-persistence-secret" {
		t.Fatal("redaction mutated native response")
	}
	object(object(raw["properties"])["redisConfiguration"])["rdb-storage-connection-string"] = "changed-persistence-secret"
	driver, _ := r.ResolveAction(ctx, "connection", fresh)
	if _, err := driver.Execute(ctx, request); err == nil || len(s.deletes) != 0 {
		t.Fatal("private persistence configuration change allowed deletion")
	}
}

func TestRedisDeleteFailuresAndRestartedOperationIdentity(t *testing.T) {
	for _, kind := range []string{redisFirewallType, redisDatabaseAssignmentType} {
		for _, mode := range []string{"async", "forbidden", "conflict", "partial-delete", "failed", "canceled", "partial-poll", "wrong-receipt", "wrong-operation", "wrong-id", "wrong-name", "wrong-resource", "wrong-region", "wrong-subscription", "wrong-provider"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, r, assets := redisScenario(t)
				target := cdnAsset(t, assets, kind)
				// Native product discovery inherits the proxy's root location.
				target.Location = resourceRegion(s.records[redisRootID(target.Identity.NativeID)])
				request := redisReadyRequest(t, s, r, assets, target)
				version := "2024-11-01"
				collection := "asyncOperations"
				if kind == redisDatabaseAssignmentType {
					version, collection = "2025-07-01", "operationsStatus"
				}
				operation := "/subscriptions/" + testSubscription + "/providers/Microsoft.Cache/locations/" + target.Location + "/" + collection + "/deleting-cache"
				endpoint := apiURL(operation, version)
				state := "InProgress"
				polls := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "DELETE" {
						s.deletes = append(s.deletes, strings.ToLower(req.URL.Path))
						if mode == "forbidden" || mode == "conflict" || mode == "partial-delete" {
							status := 403
							if mode == "conflict" {
								status = 409
							} else if mode == "partial-delete" {
								status = 206
							}
							return jsonResponse(status, map[string]any{}, nil), true
						}
						returned := endpoint
						switch mode {
						case "wrong-region":
							returned = apiURL(strings.Replace(operation, "/locations/"+target.Location+"/", "/locations/unrelated/", 1), version)
						case "wrong-subscription":
							returned = strings.Replace(endpoint, testSubscription, testTenant, 1)
						case "wrong-provider":
							returned = strings.Replace(endpoint, "Microsoft.Cache", "Microsoft.Other", 1)
						}
						return jsonResponse(202, nil, http.Header{"Azure-Asyncoperation": {returned}}), true
					}
					if strings.EqualFold(req.URL.Path, operation) {
						polls++
						body := map[string]any{"id": operation, "name": last(operation), "resourceId": target.Identity.NativeID, "status": state}
						switch mode {
						case "partial-poll":
							return jsonResponse(206, body, nil), true
						case "wrong-id":
							body["id"] = operation + "other"
						case "wrong-name":
							body["name"] = "another-operation"
						case "wrong-resource":
							body["resourceId"] = target.Identity.NativeID + "other"
						}
						return jsonResponse(200, body, nil), true
					}
					return nil, false
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", target)
				result, err := driver.Execute(context.Background(), request)
				if slices.Contains([]string{"forbidden", "conflict", "partial-delete", "wrong-region", "wrong-subscription", "wrong-provider"}, mode) {
					if err == nil || len(s.deletes) != 1 || polls != 0 {
						t.Fatal("invalid native delete response accepted", err)
					}
					return
				}
				if err != nil {
					t.Fatal("native asynchronous delete", err)
				}
				payload, _ := json.Marshal(request)
				json.Unmarshal(payload, &request)
				payload, _ = json.Marshal(result)
				json.Unmarshal(payload, &result)
				driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
				switch mode {
				case "wrong-receipt":
					result.Data["redis_operation_binding"] = "another-resource"
				case "wrong-operation":
					u, _ := url.Parse(result.ProviderOperationID)
					u.Path += "other"
					result.ProviderOperationID = u.String()
				case "failed":
					state = "Failed"
				case "canceled":
					state = "Canceled"
				}
				wait, err := driver.Wait(context.Background(), request, result)
				if mode != "async" {
					if err == nil || wait.Done || ((mode == "wrong-receipt" || mode == "wrong-operation") && polls != 0) {
						t.Fatal("invalid resumed Redis operation accepted", mode, err)
					}
					return
				}
				if err != nil || wait.Done || polls != 1 {
					t.Fatal("pending Redis operation", err)
				}
				state = "Succeeded"
				wait, err = driver.Wait(context.Background(), request, result)
				if err != nil || wait.Done {
					t.Fatal("successful Redis operation hid a live target", err)
				}
				s.gone[target.Identity.NativeID] = true
				wait, err = driver.Wait(context.Background(), request, result)
				if err != nil || !wait.Done {
					t.Fatal("completed resumed Redis operation", err)
				}
			})
		}
	}
}

func TestRedisRetainedAndUnreviewedChildrenBlockCleanup(t *testing.T) {
	for _, kind := range []string{redisType, redisPolicyType, redisLinkType, redisEnterpriseType, redisDatabaseType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := redisScenario(t)
			target := cdnAsset(t, assets, kind)
			request, input := dnsRequest(t, r, assets, target)
			children := append(slices.Clone(request.LifecycleImpacts), request.PrerequisiteDeletions...)
			if len(children) == 0 {
				t.Fatal("Redis parent lost its reviewed children")
			}
			for _, child := range children {
				input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{child.Asset.Identity.NativeID}}}
				retained, err := plan.Solve(input)
				if err != nil || len(retained.Blockers) == 0 {
					t.Fatal("retained Redis child allowed parent deletion", child.Asset.Identity.NativeType, err)
				}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"}); err == nil || len(s.deletes) != 0 {
				t.Fatal("unreviewed Redis children allowed parent deletion")
			}
		})
	}
}

func TestRedisLinkedServerInventoryRequiresConfirmedPremiumSKU(t *testing.T) {
	for _, sku := range []string{"Basic", "Standard", "Unknown"} {
		t.Run(sku, func(t *testing.T) {
			s, r, assets := redisScenario(t)
			for _, value := range assets {
				if value.Identity.NativeType == redisType {
					props := object(s.records[value.Identity.NativeID]["properties"])
					object(props["sku"])["name"] = sku
					props["linkedServers"] = []any{}
				}
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.HasSuffix(strings.ToLower(req.URL.Path), "/linkedservers") {
					t.Fatal("unsupported SKU caused native linked-server List")
				}
				return nil, false
			}
			batch, err := r.List(context.Background(), productRequest(r, redisLinkType))
			if sku == "Unknown" {
				if err == nil {
					t.Fatal("unknown SKU proved an empty link collection")
				}
			} else if err != nil || !batch.Complete || len(batch.Items) != 0 {
				t.Fatal("known unsupported link SKU", err)
			}
		})
	}
}
