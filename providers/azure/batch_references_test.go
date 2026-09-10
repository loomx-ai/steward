package azure

import (
	"encoding/json"
	"math"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func TestBatchIntegerBoundsSurviveJSONRoundTrip(t *testing.T) {
	for _, value := range []int64{-2147483648, -1000000, 0, 1000000, 2147483647} {
		payload, _ := json.Marshal(value)
		var restored any
		json.Unmarshal(payload, &restored)
		for _, native := range []any{value, json.Number(string(payload)), restored} {
			if n, err := batchInteger(native, 32); err != nil || n != value {
				t.Fatal("int32 changed after request restart", value, n, err)
			}
		}
	}
	for _, value := range []any{nil, true, "4", 1.5, float64(2147483648), json.Number("-2147483649"), math.Inf(1), math.NaN()} {
		if _, err := batchInteger(value, 32); err == nil {
			t.Fatal("invalid native int32 accepted", value)
		}
	}
	if !batchCapacityDecrement(float64(10000000), float64(9999999), 1) || batchCapacityDecrement(float64(1<<63), 0, 1) {
		t.Fatal("restored pool capacity lost numeric boundaries")
	}
}

func TestBatchTaskPlacementDoesNotPreventNativeNodeRequeue(t *testing.T) {
	s, r, assets := newBatchScenario(t)
	task := s.records["/jobs/jobid/tasks/taskid"]
	task["nodeInfo"] = map[string]any{"poolId": "poolid", "nodeId": "nodeid", "nodeUrl": s.origin + "/pools/poolid/nodes/nodeid"}
	page, err := r.List(t.Context(), productRequest(r, batchTaskType))
	if err != nil || len(page.Items) != 1 {
		t.Fatal(err)
	}
	item := page.Items[0]
	for kind, id := range map[string]string{batchPoolType: s.account + "/pools/poolid", batchNodeType: s.origin + "/pools/poolid/nodes/nodeid"} {
		if !slices.Equal(item.Normalized[referenceKey(kind)].([]string), []string{id}) || !slices.Contains(item.NetworkReferences, id) {
			t.Fatal("task placement missing from inventory", kind)
		}
	}
	for i := range assets {
		if assets[i].Identity.NativeType == batchTaskType {
			assets[i].Normalized = item.Normalized
		}
	}
	node := cdnAsset(t, assets, batchNodeType)
	solved := batchPlan(t, r, assets, node)
	if len(solved.Blockers) != 0 || len(solved.Steps) != 1 || solved.Steps[0].AssetID != node.ID {
		t.Fatal("historical task placement required deleting retained tasks", solved.Blockers)
	}
	removed := false
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "POST" && strings.HasSuffix(req.URL.Path, "/removenodes") {
			var body map[string]any
			json.NewDecoder(req.Body).Decode(&body)
			if body["nodeDeallocationOption"] != "requeue" || !slices.Equal(array(body["nodeList"]), []any{"nodeid"}) {
				t.Fatal("task placement changed native node removal")
			}
			removed = true
			s.gone["/pools/poolid/nodes/nodeid"] = true
			return jsonResponse(202, nil, nil), true
		}
		return nil, false
	}
	request := servicePlanRequest(solved, assets, node)
	driver, _ := r.ResolveAction(t.Context(), "connection", node)
	receipt, err := driver.Execute(t.Context(), request)
	if err != nil || !removed {
		t.Fatal("task placement prevented native node removal", err)
	}
	if wait, err := driver.Wait(t.Context(), request, receipt); err != nil || !wait.Done || s.gone["/jobs/jobid/tasks/taskid"] {
		t.Fatal("node removal changed a retained task", wait, err)
	}
	// A task's placement remains useful even after the referenced node is gone.
	if page, err := r.List(t.Context(), productRequest(r, batchTaskType)); err != nil || len(page.Items) != 1 {
		t.Fatal("historical node absence made the task undiscoverable", err)
	}
}

func TestBatchTaskPlacementValidatesNativeAccountAndOptionalFields(t *testing.T) {
	account := batchAccountContext{id: "/subscriptions/" + testSubscription + "/resourcegroups/rg/providers/microsoft.batch/batchaccounts/account", endpoint: "https://account.japaneast.batch.azure.com"}
	nodeURL := account.endpoint + "/pools/pool/nodes/node"
	for _, info := range []map[string]any{{}, {"affinityId": "opaque"}, {"poolId": "pool"}, {"nodeUrl": nodeURL}, {"poolId": "POOL", "nodeId": "NODE"}, {"poolId": "pool", "nodeId": "node", "nodeUrl": nodeURL}} {
		node, pool, err := batchTaskNode(account, info)
		if err != nil || (node != "" && node != nodeURL) || (pool != "" && pool != account.id+"/pools/pool") {
			t.Fatal("valid native placement rejected", info, err)
		}
	}
	for _, info := range []map[string]any{
		{"nodeUrl": "https://foreign.japaneast.batch.azure.com/pools/pool/nodes/node"},
		{"nodeUrl": nodeURL, "poolId": "other"}, {"nodeUrl": nodeURL, "nodeId": "other"},
		{"nodeUrl": nodeURL + "?sig=private"}, {"nodeUrl": account.endpoint + "/jobs/job"},
		{"nodeUrl": nodeURL, "poolId": 12}, {"nodeId": "node"}, {"poolId": "../pool"},
		{"poolId": "pool", "nodeId": "node/other"}, {"poolId": " pool"},
	} {
		if _, _, err := batchTaskNode(account, info); err == nil {
			t.Fatal("invalid native placement accepted", info)
		}
	}
}

func TestBatchTaskRangesRetainLeadingZeroAliasesAndExplicitPrerequisites(t *testing.T) {
	s, r, assets := newBatchScenario(t)
	c, _ := r.resolve(t.Context(), "connection")
	account, _ := c.batchAccount(t.Context(), s.account)
	task := s.records["/jobs/jobid/tasks/taskid"]
	for _, name := range []string{"-2147483648", "-004", "0", "4", "04", "004", "2147483647", "2147483648", "4other"} {
		raw := batchClone(task)
		raw["id"], raw["url"] = name, s.origin+"/jobs/jobid/tasks/"+name
		s.records["/jobs/jobid/tasks/"+name] = raw
		s.lists["/jobs/jobid/tasks"] = append(s.lists["/jobs/jobid/tasks"], raw)
	}
	task["dependsOn"] = map[string]any{"taskIds": []any{"04", "004"}, "taskIdRanges": []any{map[string]any{"start": -2147483648, "end": 2147483647}, map[string]any{"start": 4, "end": 4}}}
	refs, err := c.batchReferences(t.Context(), account, text(task["url"]), batchTaskType, task)
	want := []string{}
	for _, name := range []string{"-004", "-2147483648", "0", "004", "04", "2147483647", "4"} {
		want = append(want, s.origin+"/jobs/jobid/tasks/"+name)
	}
	slices.Sort(want)
	if err != nil || !slices.Equal(refs[batchTaskType], want) {
		t.Fatal("native range lost an integer alias or expanded out-of-range tasks", refs[batchTaskType], err)
	}
	page, err := r.List(t.Context(), productRequest(r, batchTaskType))
	if err != nil {
		t.Fatal(err)
	}
	assets = slices.DeleteFunc(assets, func(a asset.Asset) bool { return a.Identity.NativeType == batchTaskType })
	for _, item := range page.Items {
		assets = append(assets, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: item.NativeID, NativeType: batchTaskType}, Location: item.Location, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}})
	}
	upstream := assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.Identity.NativeID == s.origin+"/jobs/jobid/tasks/004" })]
	downstream := assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.Identity.NativeID == text(task["url"]) })]
	if solved := batchPlan(t, r, assets, upstream); len(solved.Blockers) == 0 {
		t.Fatal("range alias deletion ignored a retained dependent task")
	}
	contributor, _ := r.ServiceLifecycle(t.Context(), "connection")
	contribution, err := contributor.Contribute(t.Context(), "scope", assets)
	if err != nil {
		t.Fatal(err)
	}
	solved, err := plan.Solve(plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{upstream.ID, downstream.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
	if err != nil || len(solved.Blockers) != 0 || len(solved.Steps) != 2 || solved.Steps[0].AssetID != downstream.ID {
		t.Fatal("explicit dependency cleanup lost its order", solved.Blockers, err)
	}
	for _, target := range []asset.Asset{downstream, upstream} {
		request := servicePlanRequest(solved, assets, target)
		driver, _ := r.ResolveAction(t.Context(), "connection", target)
		receipt, err := driver.Execute(t.Context(), request)
		if err != nil {
			t.Fatal("ordered task range cleanup", err)
		}
		if wait, err := driver.Wait(t.Context(), request, receipt); err != nil || !wait.Done {
			t.Fatal(wait, err)
		}
	}
}

func TestBatchTaskReferencesRejectMalformedNativeRangesAndIdentities(t *testing.T) {
	s, r, _ := newBatchScenario(t)
	c, _ := r.resolve(t.Context(), "connection")
	account, _ := c.batchAccount(t.Context(), s.account)
	raw := s.records["/jobs/jobid/tasks/taskid"]
	for _, span := range []any{
		map[string]any{"start": "1", "end": 4}, map[string]any{"start": 1, "end": true},
		map[string]any{"start": 1.5, "end": 4}, map[string]any{"start": 1, "end": 2147483648},
		map[string]any{"start": -2147483649, "end": 4}, map[string]any{"start": 4, "end": 1},
		map[string]any{"end": 4}, "1-4",
	} {
		raw["dependsOn"] = map[string]any{"taskIdRanges": []any{span}}
		if _, err := c.batchReferences(t.Context(), account, text(raw["url"]), batchTaskType, raw); err == nil {
			t.Fatal("invalid native task range was accepted", span)
		}
	}
	delete(raw, "dependsOn")
	identity := "/subscriptions/" + testSubscription + "/resourcegroups/identities/providers/microsoft.managedidentity/userassignedidentities/reader"
	raw["resourceFiles"] = []any{map[string]any{"identityReference": map[string]any{"resourceId": identity}}}
	refs, err := c.batchReferences(t.Context(), account, text(raw["url"]), batchTaskType, raw)
	if err != nil || !slices.Equal(refs["Microsoft.ManagedIdentity/userAssignedIdentities"], []string{identity}) {
		t.Fatal("resource-file managed identity was lost", refs, err)
	}
	object(object(array(raw["resourceFiles"])[0])["identityReference"])["resourceId"] = s.account
	if _, err := c.batchReferences(t.Context(), account, text(raw["url"]), batchTaskType, raw); err == nil {
		t.Fatal("wrong identity reference type was accepted")
	}
}
