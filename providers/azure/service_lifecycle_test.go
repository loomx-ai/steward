package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type watcherScenario struct {
	parent                      map[string]any
	children                    []map[string]any
	locks                       []any
	mode                        string
	gone                        map[string]bool
	removed                     map[string]bool
	deletes, parentReads, pages int
}

func newWatcherScenario() *watcherScenario {
	s := &watcherScenario{parent: nativeResource(networkWatcherType, "watcher", "eastus", map[string]any{"provisioningState": "Succeeded"}), locks: []any{}, gone: map[string]bool{}, removed: map[string]bool{}}
	s.parent["etag"] = "parent-incarnation"
	for _, entry := range []struct{ collection, name string }{{"flowLogs", "log1"}, {"flowLogs", "log2"}, {"connectionMonitors", "monitor"}, {"packetCaptures", "capture"}} {
		s.children = append(s.children, map[string]any{"id": text(s.parent["id"]) + "/" + entry.collection + "/" + entry.name, "name": entry.name, "etag": entry.name + "-incarnation", "properties": map[string]any{"provisioningState": "Succeeded"}})
	}
	return s
}

func (s *watcherScenario) runtime(t *testing.T) *Runtime {
	t.Helper()
	return protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if response, handled := emptyMonitorIndexResponse(t, req); handled {
			return response, nil
		}
		path := strings.ToLower(req.URL.Path)
		root := "/subscriptions/" + testSubscription
		parent := strings.ToLower(text(s.parent["id"]))
		if req.Method == "DELETE" {
			if path != parent {
				t.Fatalf("unreviewed child write: %s", req.URL)
			}
			if req.URL.Query().Get("api-version") != "2024-05-01" || req.Header.Get("x-ms-client-request-id") != azureRequestID("delete-watcher") {
				t.Fatalf("wrong native delete %s", req.URL)
			}
			s.deletes++
			return jsonResponse(202, map[string]any{}, http.Header{"Azure-Asyncoperation": {apiURL(root+"/providers/Microsoft.Network/locations/eastus/operations/delete-watcher", "2024-05-01")}, "X-Ms-Request-Id": {"watcher-delete"}}), nil
		}
		if req.Method != "GET" {
			t.Fatalf("unexpected write %s %s", req.Method, req.URL)
		}
		switch path {
		case root + "/resourcegroups":
			return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": root + "/resourcegroups/test", "name": "test", "type": groupType}}}, nil), nil
		case root + "/resourcegroups/test/resources":
			return jsonResponse(200, map[string]any{"value": []any{s.parent}}, nil), nil
		case root + "/resourcegroups/test":
			return jsonResponse(200, map[string]any{"id": path}, nil), nil
		case root + "/providers/microsoft.authorization/locks":
			return jsonResponse(200, map[string]any{"value": s.locks}, nil), nil
		case root + "/providers/microsoft.network/networkwatchers":
			return jsonResponse(200, map[string]any{"value": []any{s.parent}}, nil), nil
		case root + "/providers/microsoft.network/locations/eastus/operations/delete-watcher":
			state := "Succeeded"
			if s.mode == "operation-failed" {
				state = "Failed"
			}
			return jsonResponse(200, map[string]any{"status": state}, nil), nil
		}
		if path == parent {
			s.parentReads++
			if s.gone[path] {
				return jsonResponse(404, map[string]any{}, nil), nil
			}
			if s.mode == "parent-drift" && s.parentReads > 1 {
				changed := map[string]any{}
				for k, v := range s.parent {
					changed[k] = v
				}
				changed["etag"] = "new-parent"
				return jsonResponse(200, changed, nil), nil
			}
			return jsonResponse(200, s.parent, nil), nil
		}
		for _, child := range s.children {
			id := strings.ToLower(text(child["id"]))
			if path == id {
				if s.gone[id] || s.mode == "child-missing-read" {
					return jsonResponse(404, map[string]any{}, nil), nil
				}
				data := child
				if s.mode == "detail-foreign" || s.mode == "detail-drift" {
					data = map[string]any{}
					for k, v := range child {
						data[k] = v
					}
					if s.mode == "detail-foreign" {
						data["id"] = id + "-other"
					} else {
						data["etag"] = "recreated"
					}
				}
				return jsonResponse(200, data, nil), nil
			}
		}
		for _, collection := range []string{"flowLogs", "connectionMonitors", "packetCaptures"} {
			if path != parent+"/"+strings.ToLower(collection) {
				continue
			}
			if req.URL.Query().Get("api-version") != "2024-05-01" {
				t.Fatalf("invalid child version %s", req.URL)
			}
			if s.mode == "permission" || s.mode == "collection-missing" {
				status := 403
				if s.mode == "collection-missing" {
					status = 404
				}
				return jsonResponse(status, map[string]any{"error": map[string]any{"code": "blocked"}}, nil), nil
			}
			records := []any{}
			for _, child := range s.children {
				id := strings.ToLower(text(child["id"]))
				if strings.HasPrefix(id, path+"/") && !s.gone[id] && !s.removed[id] {
					records = append(records, child)
				}
			}
			if s.mode == "duplicate" && len(records) > 0 {
				records = append(records, records[0])
			}
			if s.mode == "foreign-list" {
				records = append(records, map[string]any{"id": parent + "-other/" + collection + "/foreign"})
			}
			status := 200
			data := map[string]any{"value": records}
			if s.mode == "partial" {
				status = 206
			}
			if s.mode == "missing-array" {
				delete(data, "value")
			}
			if collection == "flowLogs" && len(records) > 1 {
				s.pages++
				if req.URL.Query().Get("$skiptoken") == "second" {
					data["value"] = records[1:]
				} else {
					data["value"] = records[:1]
					data["nextLink"] = req.URL.String() + "&%24skiptoken=second"
				}
			}
			if s.mode == "cursor-cycle" {
				data["nextLink"] = apiURL(path, "2024-05-01") + "&%24skiptoken=second"
			}
			if s.mode == "cursor-version" {
				data["nextLink"] = apiURL(path, "2025-05-01") + "&%24skiptoken=second"
			}
			return jsonResponse(status, data, http.Header{"X-Ms-Request-Id": {"watcher-child-list"}}), nil
		}
		t.Fatalf("unexpected watcher request %s", req.URL)
		return nil, nil
	})
}

func (s *watcherScenario) assets(t *testing.T) []asset.Asset {
	t.Helper()
	raw := append([]map[string]any{s.parent}, s.children...)
	var result []asset.Asset
	for _, record := range raw {
		id, kind, err := parseID(text(record["id"]))
		if err != nil {
			t.Fatal(err)
		}
		rule, ok := findType(kind)
		if !ok {
			t.Fatal(kind)
		}
		payload, _ := json.Marshal(object(record["properties"]))
		var normalized map[string]any
		json.Unmarshal(payload, &normalized)
		normalized["_arm_generation"] = productGeneration(record)
		result = append(result, asset.Asset{ID: asset.AssetID(last(id)), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure-public", NativeType: rule.NativeType, NativeID: id}, Location: "eastus", Capabilities: asset.CapabilitySet{asset.CapabilityActionable}, Normalized: normalized})
	}
	return result
}

func watcherRequest(t *testing.T, r *Runtime, assets []asset.Asset) (contracts.ActionRequest, plan.Input) {
	t.Helper()
	contributor, err := r.ServiceLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := contributor.Contribute(context.Background(), "scope", assets)
	if err != nil || len(contribution.Unresolved) != 0 || len(contribution.Bindings) != 4 {
		t.Fatalf("contribution=%+v error=%v", contribution, err)
	}
	for _, binding := range contribution.Bindings {
		if binding.Authority != graph.AuthorityAuthoritative || binding.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] != true {
			t.Fatalf("unverified cascade %+v", binding)
		}
	}
	input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{assets[0].ID}, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 1 || len(result.ImpactItems) != 4 {
		t.Fatalf("plan=%+v error=%v", result, err)
	}
	request := contracts.ActionRequest{Asset: assets[0], Action: "delete", IdempotencyKey: "delete-watcher"}
	for _, impact := range result.ImpactItems {
		for _, value := range assets {
			if impact.AssetID == value.ID {
				request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{ControllerID: impact.ControllerID, Asset: value, Delete: impact.Expected == plan.ExpectedDelegatedDelete})
			}
		}
	}
	return request, input
}

func TestNetworkWatcherCascadePlanExecutionAndRecoveredChildReadback(t *testing.T) {
	s := newWatcherScenario()
	r := s.runtime(t)
	request, input := watcherRequest(t, r, s.assets(t))
	for _, retained := range []string{"monitor", input.Assets[3].Identity.NativeID} {
		input.RequestOptions = map[asset.AssetID]map[string]any{"watcher": {"retain_resources": []string{retained}}}
		result, err := plan.Solve(input)
		if err != nil || len(result.Blockers) == 0 {
			t.Fatalf("impossible retention accepted %+v %v", result, err)
		}
	}
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || s.deletes != 1 || result.ProviderRequestID != "watcher-delete" || s.pages < 4 {
		t.Fatalf("execute=%+v error=%v deletes=%d pages=%d", result, err, s.deletes, s.pages)
	}
	// Frozen impacts and operation state survive worker restart.
	encoded, _ := json.Marshal(request)
	json.Unmarshal(encoded, &request)
	encoded, _ = json.Marshal(result)
	json.Unmarshal(encoded, &result)
	driver, err = s.runtime(t).ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	s.gone[request.Asset.Identity.NativeID] = true
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || wait.Done || wait.State != "service_children_deleting" {
		t.Fatalf("lost delayed child: %+v %v", wait, err)
	}
	check, err := driver.Preflight(context.Background(), request)
	if err != nil || !check.Allowed || check.Absent {
		t.Fatalf("premature absent preflight: %+v %v", check, err)
	}
	if _, err := driver.Execute(context.Background(), request); err != nil || s.deletes != 1 {
		t.Fatalf("repeated parent delete: %v count=%d", err, s.deletes)
	}
	for _, child := range s.children {
		s.gone[strings.ToLower(text(child["id"]))] = true
	}
	wait, err = driver.Wait(context.Background(), request, result)
	if err != nil || !wait.Done {
		t.Fatalf("final readback=%+v %v", wait, err)
	}
}

func TestNetworkWatcherCascadeRejectsIncompleteChangedOrUnreviewedChildren(t *testing.T) {
	for _, mode := range []string{"unreviewed", "retained", "changed", "new-child", "protected", "locked", "bad-lock", "foreign-impact", "foreign-kind", "duplicate-impact", "orphan", "cycle-impact", "permission", "collection-missing", "child-missing-read", "partial", "missing-array", "duplicate", "foreign-list", "detail-foreign", "detail-drift", "parent-drift", "cursor-cycle", "cursor-version", "removed-live"} {
		t.Run(mode, func(t *testing.T) {
			s := newWatcherScenario()
			r := s.runtime(t)
			request, _ := watcherRequest(t, r, s.assets(t))
			s.parentReads = 0
			switch mode {
			case "unreviewed":
				request.LifecycleImpacts = nil
			case "retained":
				request.LifecycleImpacts[0].Delete = false
			case "changed":
				s.children[0]["etag"] = "new-incarnation"
			case "new-child":
				s.children = append(s.children, map[string]any{"id": text(s.parent["id"]) + "/flowLogs/new", "properties": map[string]any{}})
			case "protected":
				s.children[0]["tags"] = map[string]any{"Steward:Protected": "true"}
			case "locked":
				s.locks = []any{map[string]any{"id": text(s.children[0]["id"]) + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "bad-lock":
				s.locks = []any{map[string]any{"properties": map[string]any{"level": "CanNotDelete"}}}
			case "foreign-impact":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "another"
			case "foreign-kind":
				request.LifecycleImpacts[0].Asset.Identity.NativeType = vmType
			case "duplicate-impact":
				request.LifecycleImpacts = append(request.LifecycleImpacts, request.LifecycleImpacts[0])
			case "orphan":
				request.LifecycleImpacts[0].ControllerID = "foreign-parent"
			case "cycle-impact":
				request.LifecycleImpacts[0].ControllerID = request.LifecycleImpacts[0].Asset.ID
			case "removed-live":
				s.removed[strings.ToLower(text(s.children[0]["id"]))] = true
			default:
				s.mode = mode
			}
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || s.deletes != 0 {
				t.Fatalf("unsafe cascade reached DELETE: mode=%s count=%d error=%v", mode, s.deletes, err)
			}
		})
	}
}

func TestNetworkWatcherCascadeKeepsUnresolvedAndProvesRemovedChildAbsence(t *testing.T) {
	for _, mode := range []string{"missing", "foreign-connection", "duplicate", "changed"} {
		t.Run(mode, func(t *testing.T) {
			s := newWatcherScenario()
			assets := s.assets(t)
			r := s.runtime(t)
			switch mode {
			case "missing":
				assets = assets[:len(assets)-1]
			case "foreign-connection":
				assets[len(assets)-1].Identity.ConnectionID = "another"
			case "duplicate":
				assets = append(assets, assets[len(assets)-1])
			case "changed":
				assets[len(assets)-1].Normalized["_arm_generation"] = "old"
			}
			contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
			result, err := contributor.Contribute(context.Background(), "scope", assets)
			if mode == "duplicate" || mode == "changed" {
				if err == nil {
					t.Fatal("invalid snapshot accepted")
				}
				return
			}
			if err != nil || len(result.Unresolved) != 1 {
				t.Fatalf("lost unresolved child: %+v %v", result, err)
			}
		})
	}
	s := newWatcherScenario()
	r := s.runtime(t)
	request, _ := watcherRequest(t, r, s.assets(t))
	s.gone[strings.ToLower(text(s.children[0]["id"]))] = true
	driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
	result, err := driver.Execute(context.Background(), request)
	if err != nil || s.deletes != 1 {
		t.Fatalf("already removed child not accepted: %+v %v", result, err)
	}
	s.mode = "operation-failed"
	if _, err := driver.Wait(context.Background(), request, result); err == nil {
		t.Fatal("failed native operation accepted")
	}
}

func TestNetworkWatcherNativeReferencesAndSecretValues(t *testing.T) {
	for _, mode := range []string{"packetCaptures", "connectionMonitors"} {
		properties := map[string]any{"target": resourceID(vmType, "vm"), "storageLocation": map[string]any{"storageId": resourceID(storageType, "storage"), "storagePath": "https://storage.blob.core.windows.net/logs/capture?sig=sensitive-sas"}, "endpoints": []any{map[string]any{"resourceId": resourceID(vnetType, "vnet")}, map[string]any{"resourceId": resourceID(vnetType, "vnet") + "/subnets/subnet"}}, "source": map[string]any{"resourceId": resourceID(vmType, "source")}, "outputs": []any{map[string]any{"workspaceSettings": map[string]any{"workspaceResourceId": resourceID("Microsoft.OperationalInsights/workspaces", "logs")}}}, "testConfigurations": []any{map[string]any{"httpConfiguration": map[string]any{"requestHeaders": []any{map[string]any{"name": "x-private", "value": "sensitive-header"}}}}}}
		kind := networkWatcherType + "/" + mode
		refs := references(kind, strings.ToLower(resourceID(networkWatcherType, "watcher")+"/"+mode+"/item"), map[string]any{"properties": properties})
		if len(refs[networkWatcherType]) != 1 || len(refs[vmType]) != 1 || len(refs[storageType]) != 1 {
			t.Fatalf("missing native refs: %v", refs)
		}
		if mode == "connectionMonitors" && (len(refs[vnetType]) != 1 || len(refs[subnetType]) != 1 || len(refs["Microsoft.OperationalInsights/workspaces"]) != 1) {
			t.Fatalf("missing endpoint refs: %v", refs)
		}
		payload, _ := json.Marshal(safePayload(map[string]any{"properties": properties}))
		if strings.Contains(string(payload), "sensitive") {
			t.Fatalf("monitor secret leaked: %s", payload)
		}
		if !strings.Contains(text(object(properties["storageLocation"])["storagePath"]), "sensitive-sas") {
			t.Fatal("sanitizer mutated native payload")
		}
	}
}

func TestAzureManagementLocksRejectMalformedEntries(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	for _, mode := range []string{"valid-subscription", "valid-group", "valid-resource", "missing-id", "missing-level", "unknown-level", "foreign", "trailing-path", "dot-path", "wrong-type", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			lock := map[string]any{"id": root + "/providers/Microsoft.Authorization/locks/keep", "type": "Microsoft.Authorization/locks", "properties": map[string]any{"level": "CanNotDelete"}}
			switch mode {
			case "valid-group":
				lock["id"] = root + "/resourceGroups/test/providers/Microsoft.Authorization/locks/keep"
			case "valid-resource":
				lock["id"] = resourceID(vmType, "vm") + "/providers/Microsoft.Authorization/locks/keep"
			case "missing-id":
				delete(lock, "id")
			case "missing-level":
				delete(lock, "properties")
			case "unknown-level":
				lock["properties"] = map[string]any{"level": "Future"}
			case "foreign":
				lock["id"] = strings.Replace(text(lock["id"]), testSubscription, "22222222-3333-4444-8555-666666666666", 1)
			case "trailing-path":
				lock["id"] = text(lock["id"]) + "/child"
			case "dot-path":
				lock["id"] = root + "/resourceGroups/../providers/Microsoft.Authorization/locks/keep"
			case "wrong-type":
				lock["type"] = vmType
			}
			locks := []any{lock}
			if mode == "duplicate" {
				locks = append(locks, lock)
			}
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || !strings.EqualFold(req.URL.Path, root+"/providers/Microsoft.Authorization/locks") {
					t.Fatalf("unexpected lock read %s", req.URL)
				}
				return jsonResponse(200, map[string]any{"value": locks}, nil), nil
			})
			c, err := r.resolve(context.Background(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			got, err := c.managementLocks(context.Background())
			if strings.HasPrefix(mode, "valid") {
				if err != nil || len(got) != 1 || !locked(strings.ToLower(resourceID(vmType, "vm")), got) {
					t.Fatalf("valid lock rejected %+v %v", got, err)
				}
			} else if err == nil {
				t.Fatal("malformed lock became an unlocked result")
			}
		})
	}
}

func TestAKSResourceGroupIncludesNativeServiceChildren(t *testing.T) {
	s := newWatcherScenario()
	r := s.runtime(t)
	c, err := r.resolve(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	group := "/subscriptions/" + testSubscription + "/resourcegroups/test"
	records, err := c.managedGroupResources(context.Background(), resourceID(aksType, "cluster"), group)
	if err != nil || len(records) != 6 {
		t.Fatalf("group lost native service children: %+v %v", records, err)
	}
	s.mode = "collection-missing"
	if _, err := c.managedGroupResources(context.Background(), resourceID(aksType, "cluster"), group); err == nil {
		t.Fatal("missing service collection accepted for AKS group deletion")
	}
}
