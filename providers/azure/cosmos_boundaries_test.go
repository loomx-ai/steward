package azure

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestCosmosLeafDeletionBindsOwnAndAncestorConfiguration(t *testing.T) {
	for _, mode := range []string{"body", "rid", "name", "trigger-type", "trigger-operation", "own-state", "parent-config", "parent-private", "parent-rid", "root-config", "root-rid", "root-api", "root-state", "root-backup", "root-protected", "throughput", "throughput-pending", "throughput-denied", "throughput-absent", "throughput-owner-disappears", "throughput-invalid", "throughput-wrong-owner", "ancestor-lock"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cosmosScenario(t)
			target := cdnAsset(t, assets, cosmosTriggerType)
			request := contracts.ActionRequest{Action: "delete", Asset: target}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if checked, err := driver.Preflight(context.Background(), request); err != nil || !checked.Allowed {
				t.Fatal("invalid baseline", checked, err)
			}
			raw := s.records[target.Identity.NativeID]
			props := object(raw["properties"])
			resource := object(props["resource"])
			parentID := cosmosParentID(target.Identity.NativeID)
			parent := s.records[parentID]
			rootID := cosmosRootID(target.Identity.NativeID)
			root := s.records[rootID]
			settingsID := parentID + "/throughputsettings/default"
			settings := object(object(s.records[settingsID]["properties"])["resource"])
			switch mode {
			case "body":
				resource["body"] = "changed private code"
			case "rid":
				resource["_rid"] = "recreated"
			case "name":
				resource["id"] = strings.ToLower(text(resource["id"]))
			case "trigger-type":
				resource["triggerType"] = "Unknown"
			case "trigger-operation":
				resource["triggerOperation"] = "Unknown"
			case "own-state":
				props["provisioningState"] = "Updating"
			case "parent-config":
				object(object(parent["properties"])["resource"])["defaultTtl"] = 999
			case "parent-private":
				object(object(parent["properties"])["resource"])["connectionString"] = "changed secret"
			case "parent-rid":
				object(object(parent["properties"])["resource"])["_rid"] = "recreated"
			case "root-config":
				object(root["properties"])["disableLocalAuth"] = true
			case "root-rid":
				object(root["properties"])["instanceId"] = "recreated"
			case "root-api":
				root["kind"] = "MongoDB"
			case "root-state":
				object(root["properties"])["provisioningState"] = "Updating"
			case "root-backup":
				object(root["properties"])["backupPolicy"] = map[string]any{"migrationState": map[string]any{"status": "InProgress"}}
			case "root-protected":
				root["tags"] = map[string]any{"steward:protected": "true"}
			case "throughput":
				settings["throughput"] = 1200
			case "throughput-pending":
				settings["offerReplacePending"] = "true"
			case "throughput-invalid":
				settings["throughput"] = "not-a-number"
			case "throughput-denied":
				s.status[settingsID] = 403
			case "throughput-absent":
				s.status[settingsID] = 404
			case "throughput-wrong-owner":
				s.records[settingsID]["id"] = strings.Replace(text(s.records[settingsID]["id"]), "/Orders/", "/orders/", 1)
			case "throughput-owner-disappears":
				base := s.handle
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, settingsID) {
						s.gone[parentID] = true
					}
					return base(req)
				}
			case "ancestor-lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": parentID + "/providers/microsoft.authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("changed Cosmos resource allowed deletion", mode, err)
			}
		})
	}
}

func TestCosmosRegionalInventoryUsesDataCenterLocationAndGlobalParents(t *testing.T) {
	s, r, assets := cosmosScenario(t)
	account := cdnAsset(t, assets, cosmosType)
	cluster := cdnAsset(t, assets, cosmosCassandraType)
	center := cdnAsset(t, assets, cosmosDataCenterType)
	subnet := "/subscriptions/" + testSubscription + "/resourceGroups/net/providers/Microsoft.Network/virtualNetworks/data/subnets/cosmos"
	for _, value := range []asset.Asset{account, cluster, center} {
		s.records[value.Identity.NativeID]["location"] = "East US"
	}
	object(s.records[account.Identity.NativeID]["properties"])["virtualNetworkRules"] = []any{map[string]any{"id": subnet}}
	object(s.records[cluster.Identity.NativeID]["properties"])["delegatedManagementSubnetId"] = subnet
	props := object(s.records[center.Identity.NativeID]["properties"])
	props["dataCenterLocation"], props["delegatedSubnetId"] = "West Europe", subnet
	for _, kind := range []string{cosmosType, cosmosCassandraType, cosmosDataCenterType} {
		request := productRequest(r, kind)
		request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westeurope"}
		if kind != cosmosDataCenterType {
			request.NetworkTarget = &asset.ScanTarget{Kind: asset.ScanTargetVPC, NativeID: cosmosParentID(subnet)}
		}
		batch, err := r.List(context.Background(), request)
		if err != nil || len(batch.Items) == 0 {
			t.Fatal("global parent or deployment region was lost", kind, err)
		}
		id := account.Identity.NativeID
		if kind == cosmosCassandraType {
			id = cluster.Identity.NativeID
		}
		if kind == cosmosDataCenterType {
			id = center.Identity.NativeID
		}
		index := slices.IndexFunc(batch.Items, func(item contracts.InventoryItem) bool { return item.NativeID == id })
		if index < 0 {
			t.Fatal("resource excluded because of group metadata location", kind)
		}
		item := batch.Items[index]
		if !slices.Contains(item.NetworkReferences, strings.ToLower(subnet)) || !slices.Contains(item.NetworkReferences, strings.ToLower(cosmosParentID(subnet))) {
			t.Fatal("missing Cosmos network references", kind, item.NetworkReferences)
		}
		want := "global"
		if kind == cosmosDataCenterType {
			want = "westeurope"
		}
		if item.Location != want {
			t.Fatal("wrong Cosmos deployment location", item.Location, want)
		}
	}
}

func TestCosmosSearchPrivateLinkUsesAccountAPIAndProtection(t *testing.T) {
	for _, mode := range []string{"allowed", "locked", "protected", "recreated", "private-change"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := searchScenario(t)
			link := s.records[cdnAsset(t, assets, searchLinkType).Identity.NativeID]
			raw := cosmosExampleResource(t, "DatabaseAccounts_Get")
			id := "/subscriptions/" + testSubscription + "/resourceGroups/data/providers/Microsoft.DocumentDB/databaseAccounts/account-one"
			raw["id"], raw["name"], raw["type"] = id, "account-one", cosmosType
			props := object(raw["properties"])
			delete(object(props["backupPolicy"]), "migrationState")
			props["capabilities"] = []any{}
			object(link["properties"])["privateLinkResourceId"] = id
			object(link["properties"])["groupId"] = "Sql"
			s.add(raw, "2026-03-15")
			if mode == "protected" {
				raw["tags"] = map[string]any{"steward:protected": "true"}
			}
			target := dnsAsset(t, r, link)
			switch mode {
			case "locked":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": id + "/providers/microsoft.authorization/locks/keep", "properties": map[string]any{"level": "ReadOnly"}}}
			case "recreated":
				props["instanceId"] = "new-account"
			case "private-change":
				props["connectionString"] = "secret-changed"
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			_, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"})
			if mode == "allowed" {
				if err != nil || len(s.deletes) != 1 || s.gone[strings.ToLower(id)] {
					t.Fatal("Search unlink did not preserve Cosmos target", err)
				}
			} else if err == nil || len(s.deletes) != 0 {
				t.Fatal("Search bypassed Cosmos protection", mode, err)
			}
		})
	}
}

func TestCosmosSensitiveBodiesNeverReachInventoryOrLogs(t *testing.T) {
	s, r, assets := cosmosScenario(t)
	secrets := map[string]string{cosmosStoredProcedureType: "private-cosmos-javascript", cosmosKeyType: "private-cosmos-wrapped-key", cosmosCassandraType: "private-cosmos-admin-password", cosmosDataCenterType: "private-cosmos-yaml"}
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	for kind, secret := range secrets {
		target := cdnAsset(t, assets, kind)
		props := object(s.records[target.Identity.NativeID]["properties"])
		switch kind {
		case cosmosStoredProcedureType:
			object(props["resource"])["body"] = secret
		case cosmosKeyType:
			object(props["resource"])["wrappedDataEncryptionKey"] = secret
		case cosmosCassandraType:
			props["initialCassandraAdminPassword"] = secret
		case cosmosDataCenterType:
			props["base64EncodedCassandraYamlFragment"] = secret
		}
		batch, err := r.List(ctx, productRequest(r, kind))
		if err != nil || len(batch.Items) != 1 {
			t.Fatal("private inventory", kind, err)
		}
		payload, _ := json.Marshal(batch)
		logBytes, _ := json.Marshal(logs)
		payload = append(payload, logBytes...)
		for _, secret := range secrets {
			if strings.Contains(string(payload), secret) {
				t.Fatal("private Cosmos payload leaked", kind)
			}
		}
	}
	if len(logs) == 0 {
		t.Fatal("missing native HTTP logs")
	}
}

func TestCosmosControllerViewsNeedReviewedFinalAbsence(t *testing.T) {
	for _, mode := range []string{"builtin-role", "encryption-key"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cosmosScenario(t)
			parentKind, childKind := cosmosSQLDatabaseType, cosmosKeyType
			if mode == "builtin-role" {
				parentKind, childKind = cosmosType, cosmosType+"/sqlRoleDefinitions"
				child := cdnAsset(t, assets, childKind)
				raw := s.records[child.Identity.NativeID]
				object(raw["properties"])["type"] = "BuiltInRole"
				value := dnsAsset(t, r, raw)
				value.Capabilities = nil
				assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.ID == value.ID })] = value
			}
			parent, child := cdnAsset(t, assets, parentKind), cdnAsset(t, assets, childKind)
			request, _ := dnsRequest(t, r, assets, parent)
			if !slices.ContainsFunc(request.LifecycleImpacts, func(impact contracts.ActionImpact) bool { return impact.Asset.ID == child.ID }) {
				t.Fatal("controller view missing from review")
			}
			if driver, err := r.ResolveAction(context.Background(), "connection", child); err == nil {
				if checked, err := driver.Preflight(context.Background(), contracts.ActionRequest{Asset: child, Action: "delete"}); err == nil && checked.Allowed {
					t.Fatal("controller view allowed direct mutation")
				}
			}
			for _, impact := range request.PrerequisiteDeletions {
				s.gone[impact.Asset.Identity.NativeID] = true
			}
			for _, impact := range request.LifecycleImpacts {
				s.gone[impact.Asset.Identity.NativeID] = true
			}
			s.gone[child.Identity.NativeID] = false
			s.gone[parent.Identity.NativeID] = true
			driver, _ := r.ResolveAction(context.Background(), "connection", parent)
			read, err := driver.Readback(context.Background(), request)
			if err != nil || !read.Exists {
				t.Fatal("parent absence hid controller view", read, err)
			}
			s.status[child.Identity.NativeID] = 403
			if _, err := driver.Readback(context.Background(), request); err == nil {
				t.Fatal("unreadable view became absent")
			}
			delete(s.status, child.Identity.NativeID)
			s.gone[child.Identity.NativeID] = true
			read, err = driver.Readback(context.Background(), request)
			if err != nil || read.Exists {
				t.Fatal("verified child absence ignored", read, err)
			}
		})
	}
}

func TestCosmosPrivateEndpointIndexesAndCollectionRaces(t *testing.T) {
	for _, mode := range []string{"missing-child", "unexpected-child", "duplicate-index", "foreign-index", "invalid-index", "child-changed-between-reads", "list-denied"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cosmosScenario(t)
			target := cdnAsset(t, assets, cosmosType)
			raw := s.records[target.Identity.NativeID]
			pec := cdnAsset(t, assets, cosmosPECType)
			collection := target.Identity.NativeID + "/privateendpointconnections"
			props := object(raw["properties"])
			switch mode {
			case "missing-child":
				s.lists[collection] = []any{}
			case "unexpected-child":
				props["privateEndpointConnections"] = []any{}
			case "duplicate-index":
				props["privateEndpointConnections"] = []any{map[string]any{"id": pec.Identity.NativeID}, map[string]any{"id": pec.Identity.NativeID}}
			case "foreign-index":
				props["privateEndpointConnections"] = []any{map[string]any{"id": strings.Replace(pec.Identity.NativeID, "account-sql", "other-account", 1)}}
			case "invalid-index":
				props["privateEndpointConnections"] = "unreadable"
			case "list-denied":
				s.status[collection] = 403
			case "child-changed-between-reads":
				base, reads := s.handle, 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, pec.Identity.NativeID) {
						reads++
						if reads == 2 {
							s.records[pec.Identity.NativeID]["tags"] = map[string]any{"changed": "during-scan"}
						}
					}
					return base(req)
				}
			}
			c, _ := r.resolve(context.Background(), "connection")
			if _, err := c.cosmosChildren(context.Background(), target.Identity, raw); err == nil {
				t.Fatal("incomplete or changed Cosmos child index accepted", mode)
			}
		})
	}
}

func TestCosmosThroughputTypesAndImmutableLargeNumbers(t *testing.T) {
	for _, value := range []any{-1, 1.5, math.NaN(), math.Inf(1), json.Number("2147483648"), json.Number("not-a-number"), "400", true, map[string]any{}} {
		if reason := cosmosThroughputProtection(map[string]any{"present": true, "resource": map[string]any{"throughput": value}}); reason != "azure_cosmos_invalid_throughput" {
			t.Fatal("invalid RU configuration accepted", value, reason)
		}
	}
	for _, value := range []any{400, int64(400), float64(400), json.Number("400"), json.Number("4e2")} {
		if reason := cosmosThroughputProtection(map[string]any{"present": true, "resource": map[string]any{"autoscaleSettings": map[string]any{"maxThroughput": value}}}); reason != "" {
			t.Fatal("valid native RU configuration rejected", value, reason)
		}
	}
	if reason := cosmosThroughputProtection(map[string]any{"resource": map[string]any{"throughput": 400, "autoscaleSettings": "invalid"}}); reason == "" {
		t.Fatal("invalid autoscale settings accepted")
	}
	s, r, assets := cosmosScenario(t)
	target := cdnAsset(t, assets, cosmosStoredProcedureType)
	raw := s.records[target.Identity.NativeID]
	resource := object(object(raw["properties"])["resource"])
	resource["customLargeInteger"] = json.Number("9007199254740992")
	c, _ := r.resolve(context.Background(), "connection")
	before := c.privateConfiguration(cosmosSnapshot(cosmosStoredProcedureType, raw))
	resource["customLargeInteger"] = json.Number("9007199254740993")
	if before == c.privateConfiguration(cosmosSnapshot(cosmosStoredProcedureType, raw)) {
		t.Fatal("configuration hashing lost integer precision")
	}
	before = c.privateConfiguration(cosmosSnapshot(cosmosStoredProcedureType, raw))
	resource["_etag"], resource["_ts"], resource["_self"] = "new etag", 999, "new self"
	if before != c.privateConfiguration(cosmosSnapshot(cosmosStoredProcedureType, raw)) {
		t.Fatal("data writes became ARM configuration drift")
	}
}

func TestCosmosDeleteFailuresAndResumedOperationBoundaries(t *testing.T) {
	for _, mode := range []string{"async", "delete-denied", "delete-conflict", "delete-partial", "failed", "canceled", "partial-poll", "denied-poll", "wrong-id", "wrong-name", "wrong-resource-case", "wrong-receipt", "changed-selector", "expired-live", "expired-absent", "readback-denied", "readback-partial", "readback-wrong-case"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cosmosScenario(t)
			target := cdnAsset(t, assets, cosmosTriggerType)
			wire := text(target.Normalized["_cosmos_wire_id"])
			path := "/subscriptions/" + testSubscription + "/providers/Microsoft.DocumentDB/locations/centraluseuap/operationsStatus/11111111-2222-3333-4444-555555555555"
			endpoint := apiURL(path, "2026-03-15") + "&t=test-t&c=test-c&s=test-s&h=test-h"
			polls, state := 0, "Running"
			base := s.handle
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					s.deletes = append(s.deletes, strings.ToLower(req.URL.Path))
					status := map[string]int{"delete-denied": 403, "delete-conflict": 409, "delete-partial": 206}[mode]
					if status != 0 {
						return jsonResponse(status, map[string]any{}, nil), true
					}
					return jsonResponse(202, nil, http.Header{"Azure-Asyncoperation": {endpoint}}), true
				}
				if req.URL.String() == endpoint {
					polls++
					body := map[string]any{"id": path, "name": last(path), "resourceId": wire, "status": state}
					switch mode {
					case "failed":
						body["status"] = "Failed"
					case "canceled":
						body["status"] = "Canceled"
					case "wrong-id":
						body["id"] = path + "-other"
					case "wrong-name":
						body["name"] = "another-operation"
					case "wrong-resource-case":
						body["resourceId"] = strings.ToLower(wire)
					case "partial-poll":
						return jsonResponse(206, body, nil), true
					case "denied-poll":
						return jsonResponse(403, map[string]any{}, nil), true
					case "expired-live", "expired-absent":
						return jsonResponse(404, map[string]any{}, nil), true
					case "readback-denied", "readback-partial", "readback-wrong-case":
						body["status"] = "Succeeded"
					}
					return jsonResponse(200, body, http.Header{"Retry-After": {"3"}}), true
				}
				if polls > 0 && strings.EqualFold(req.URL.Path, wire) {
					switch mode {
					case "readback-denied":
						return jsonResponse(403, nil, nil), true
					case "readback-partial":
						return jsonResponse(206, s.records[target.Identity.NativeID], nil), true
					case "readback-wrong-case":
						return jsonResponse(200, map[string]any{"id": strings.ToLower(wire), "type": cosmosTriggerType}, nil), true
					}
				}
				return base(req)
			}
			request := contracts.ActionRequest{Asset: target, Action: "delete"}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			result, err := driver.Execute(context.Background(), request)
			if strings.HasPrefix(mode, "delete-") {
				if err == nil || len(s.deletes) != 1 || polls != 0 {
					t.Fatal("invalid DELETE response accepted", mode, err)
				}
				return
			}
			if err != nil {
				t.Fatal("valid asynchronous deletion", err)
			}
			payload, _ := json.Marshal(request)
			json.Unmarshal(payload, &request)
			payload, _ = json.Marshal(result)
			json.Unmarshal(payload, &result)
			driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
			if mode == "wrong-receipt" {
				result.Data["cosmos_operation_binding"] = "different receipt"
			}
			if mode == "changed-selector" {
				request.Asset.Normalized["_cosmos_wire_id"] = strings.ToLower(wire)
			}
			if mode == "expired-absent" {
				s.gone[target.Identity.NativeID] = true
			}
			waited, err := driver.Wait(context.Background(), request, result)
			switch mode {
			case "async":
				if err != nil || waited.Done || waited.RetryAfter.Seconds() != 3 {
					t.Fatal("pending operation mishandled", waited, err)
				}
				state = "Succeeded"
				waited, err = driver.Wait(context.Background(), request, result)
				if err != nil || waited.Done {
					t.Fatal("LRO success hid live resource", waited, err)
				}
				s.gone[target.Identity.NativeID] = true
				waited, err = driver.Wait(context.Background(), request, result)
				if err != nil || !waited.Done {
					t.Fatal("final absence ignored", waited, err)
				}
			case "expired-live", "expired-absent":
				if err != nil || waited.Done != (mode == "expired-absent") {
					t.Fatal("expired operation bypassed readback", waited, err)
				}
			default:
				if err == nil || waited.Done {
					t.Fatal("unsafe resumed operation accepted", mode, waited, err)
				}
			}
			if (mode == "wrong-receipt" || mode == "changed-selector") && polls != 0 {
				t.Fatal("wrong receipt reached HTTP")
			}
		})
	}
}

func TestCosmosFleetAssociationProtectsRetainedAccount(t *testing.T) {
	for _, mode := range []string{"allowed", "config", "private", "protected", "locked", "managed-group", "denied", "partial", "retarget", "reread"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cosmosScenario(t)
			target := cdnAsset(t, assets, cosmosFleetAccountType)
			link := s.records[target.Identity.NativeID]
			wire, _ := cosmosFleetTarget(link)
			id := strings.ToLower(wire)
			raw := s.records[id]
			// A protection already present during inventory must still block
			// unlinking; configuration drift alone cannot establish that.
			if mode == "protected" {
				raw["tags"] = map[string]any{"steward:protected": "true"}
			}
			target = dnsAsset(t, r, link)
			switch mode {
			case "config":
				object(raw["properties"])["disableLocalAuth"] = true
			case "private":
				object(raw["properties"])["connectionString"] = "never-persist-this"
			case "locked":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": id + "/providers/microsoft.authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "managed-group":
				group := strings.Join(strings.Split(id, "/")[:5], "/")
				s.records[group] = map[string]any{"id": group, "managedBy": id}
			case "denied":
				s.status[id] = 403
			case "partial":
				s.status[id] = 206
			case "retarget":
				object(object(link["properties"])["globalDatabaseAccountProperties"])["resourceId"] = wire + "-other"
			case "reread":
				base, reads := s.handle, 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, id) {
						reads++
						if reads == 2 {
							object(raw["properties"])["instanceId"] = "recreated"
						}
					}
					return base(req)
				}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			_, err := driver.Execute(context.Background(), contracts.ActionRequest{Action: "delete", Asset: target})
			if mode == "allowed" {
				if err != nil || len(s.deletes) != 1 || s.gone[id] {
					t.Fatal("valid unlink removed target or failed", err)
				}
			} else if err == nil || len(s.deletes) != 0 {
				t.Fatal("unsafe Fleet unlink accepted", mode, err)
			}
		})
	}
}

func TestCosmosPaginationRejectsCaseCollisionsAndChangedParents(t *testing.T) {
	for _, mode := range []string{"complete", "duplicate-page", "case-collision", "parent-case", "parent-change", "ancestor-change", "throughput-change", "partial", "denied", "missing-array", "cycle", "foreign-host", "foreign-subscription", "foreign-collection", "wrong-version"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cosmosScenario(t)
			target := cdnAsset(t, assets, cosmosTriggerType)
			raw := s.records[target.Identity.NativeID]
			wire := text(raw["id"])
			parent := cosmosParentID(wire)
			collection := parent + "/triggers"
			payload, _ := json.Marshal(raw)
			var second map[string]any
			json.Unmarshal(payload, &second)
			second["id"], second["name"] = wire+"Two", last(wire)+"Two"
			object(object(second["properties"])["resource"])["id"] = last(wire) + "Two"
			s.add(second, "2026-03-15")
			if mode == "duplicate-page" {
				second = raw
			}
			if mode == "case-collision" {
				second["id"] = strings.TrimSuffix(wire, last(wire)) + strings.ToLower(last(wire))
			}
			if mode == "parent-case" {
				second["id"] = strings.Replace(wire, "/SalesDB/", "/salesdb/", 1)
			}
			base := s.handle
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if !strings.EqualFold(req.URL.Path, collection) {
					return base(req)
				}
				if mode == "partial" {
					return jsonResponse(206, map[string]any{"value": []any{}}, nil), true
				}
				if mode == "denied" {
					return jsonResponse(403, map[string]any{}, nil), true
				}
				if mode == "missing-array" {
					return jsonResponse(200, map[string]any{}, nil), true
				}
				if req.URL.Query().Get("$skiptoken") == "second" && mode != "cycle" {
					return jsonResponse(200, map[string]any{"value": []any{second}}, nil), true
				}
				next := apiURL(collection, "2026-03-15") + "&%24skiptoken=second"
				switch mode {
				case "foreign-host":
					next = strings.Replace(next, "management.azure.com", "evil.invalid", 1)
				case "foreign-subscription":
					next = strings.Replace(next, testSubscription, testTenant, 1)
				case "foreign-collection":
					next = strings.Replace(next, "/triggers", "/storedProcedures", 1)
				case "wrong-version":
					next = strings.Replace(next, "2026-03-15", "2025-10-15", 1)
				}
				return jsonResponse(200, map[string]any{"value": []any{raw}, "nextLink": next}, nil), true
			}
			request := productRequest(r, cosmosTriggerType)
			first, err := r.List(context.Background(), request)
			if slices.Contains([]string{"partial", "denied", "missing-array", "foreign-host", "foreign-subscription", "foreign-collection", "wrong-version"}, mode) {
				if err == nil {
					t.Fatal("invalid first page accepted", mode)
				}
				return
			}
			if err != nil || first.Complete || len(first.Items) != 1 {
				t.Fatal("invalid first page", err, len(first.Items))
			}
			if mode == "parent-change" {
				object(object(s.records[strings.ToLower(parent)]["properties"])["resource"])["defaultTtl"] = 900
			}
			if mode == "ancestor-change" {
				object(s.records[strings.ToLower(cosmosRootID(wire))]["properties"])["instanceId"] = "recreated"
			}
			if mode == "throughput-change" {
				object(object(s.records[strings.ToLower(parent+"/throughputSettings/default")]["properties"])["resource"])["throughput"] = 1400
			}
			request.Cursor = first.NextCursor
			final, err := r.List(context.Background(), request)
			if mode == "complete" {
				if err != nil || !final.Complete || len(final.Items) != 1 {
					t.Fatal("lost native second page", err)
				}
			} else if err == nil {
				t.Fatal("unsafe continuation accepted", mode)
			}
		})
	}
}

func TestCosmosRetainedAndUnreviewedDependenciesBlockCleanup(t *testing.T) {
	for _, kind := range []string{cosmosType, cosmosSQLDatabaseType, cosmosContainerType, cosmosMongoDatabaseType, cosmosMongoRoleType, cosmosType + "/sqlRoleDefinitions", cosmosKeyspaceType, cosmosGremlinDatabaseType, cosmosCassandraType, cosmosFleetType, cosmosFleetspaceType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := cosmosScenario(t)
			target := cdnAsset(t, assets, kind)
			request, input := dnsRequest(t, r, assets, target)
			children := append(slices.Clone(request.LifecycleImpacts), request.PrerequisiteDeletions...)
			if len(children) == 0 {
				t.Fatal("missing dependencies", kind)
			}
			for _, child := range children {
				input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{child.Asset.Identity.NativeID}}}
				retained, err := plan.Solve(input)
				if err != nil || len(retained.Blockers) == 0 {
					t.Fatal("retained dependency allowed parent deletion", child.Asset.Identity.NativeType, err)
				}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", target)
			if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: target, Action: "delete"}); err == nil || len(s.deletes) != 0 {
				t.Fatal("unreviewed dependency allowed deletion", err)
			}
		})
	}
}
