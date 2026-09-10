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
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
)

const streamAnalyticsVersion = "2020-03-01"

func streamAnalyticsExampleBody(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/streamanalytics/" + name + ".json")
	var value map[string]any
	if err != nil || json.Unmarshal(payload, &value) != nil {
		t.Fatal("invalid Stream Analytics example", err)
	}
	return object(object(object(value["responses"])["200"])["body"])
}

// Compose a job, its four real child definitions and an independent cluster
// from copies of the original examples. GET without $expand omits definitions,
// matching Azure's API. The private endpoint's storage target is retained.
func streamAnalyticsScenario(t *testing.T, associated bool) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	group := root + "/resourcegroups/testgroup"
	clusterGroup := root + "/resourcegroups/cluster-rg"
	storageGroup := root + "/resourcegroups/data-rg"
	jobID := group + "/providers/microsoft.streamanalytics/streamingjobs/job"
	clusterID := clusterGroup + "/providers/microsoft.streamanalytics/clusters/cluster"
	storageID := storageGroup + "/providers/microsoft.storage/storageaccounts/streamdata"
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}, map[string]any{"id": clusterGroup, "type": groupType}, map[string]any{"id": storageGroup, "type": groupType}}
	storage := map[string]any{"id": storageID, "name": "streamdata", "type": storageType, "location": "westus", "properties": map[string]any{"provisioningState": "Succeeded", "publicNetworkAccess": "Enabled"}}
	storageKind, _ := findType(storageType)
	s.add(storage, storageKind.Version)
	for _, kind := range []string{storageType, sqlServerType} {
		path := root + "/providers/" + strings.ToLower(kind)
		mapping, _ := findType(kind)
		s.lists[path], s.version[path] = []any{}, mapping.Version
		if kind == storageType {
			s.lists[path] = []any{storage}
		}
	}
	var raws []map[string]any
	add := func(raw map[string]any, id, kind string) {
		raw["id"], raw["name"], raw["type"] = id, last(id), kind
		s.add(raw, streamAnalyticsVersion)
		raws = append(raws, raw)
		collection := redisParentID(id) + "/" + strings.ToLower(last(kind))
		if kind == streamAnalyticsJobType || kind == streamAnalyticsClusterType {
			collection = root + "/providers/" + strings.ToLower(kind)
		}
		if kind != streamAnalyticsTransformationType {
			s.lists[collection] = append(s.lists[collection], raw)
			s.version[collection] = streamAnalyticsVersion
		}
		for _, child := range streamAnalyticsOwnedKinds(kind) {
			if child != streamAnalyticsTransformationType {
				path := id + "/" + strings.ToLower(last(child))
				s.lists[path], s.version[path] = []any{}, streamAnalyticsVersion
			}
		}
	}
	job := streamAnalyticsExampleBody(t, "StreamingJob_Get_Expand")
	originalJobID := text(job["id"])
	payload, _ := json.Marshal(job)
	json.Unmarshal([]byte(strings.ReplaceAll(string(payload), originalJobID, jobID)), &job)
	props := object(job["properties"])
	props["jobState"] = "Stopped"
	function := streamAnalyticsExampleBody(t, "Function_Get_JavaScript")
	function["id"], function["name"] = jobID+"/functions/functiontest", "functiontest"
	props["functions"] = []any{function}
	if associated {
		props["cluster"] = map[string]any{"id": clusterID}
	}
	add(job, jobID, streamAnalyticsJobType)
	for field, kind := range map[string]string{"inputs": streamAnalyticsInputType, "outputs": streamAnalyticsOutputType, "functions": streamAnalyticsFunctionType} {
		for _, value := range array(props[field]) {
			raw := object(value)
			add(raw, strings.ToLower(text(raw["id"])), kind)
		}
	}
	transformation := object(props["transformation"])
	add(transformation, strings.ToLower(text(transformation["id"])), streamAnalyticsTransformationType)
	cluster := streamAnalyticsExampleBody(t, "Cluster_Get")
	cluster["location"] = "West US"
	add(cluster, clusterID, streamAnalyticsClusterType)
	endpoint := streamAnalyticsExampleBody(t, "PrivateEndpoint_Get")
	for _, value := range array(object(endpoint["properties"])["manualPrivateLinkServiceConnections"]) {
		object(object(value)["properties"])["privateLinkServiceId"] = storageID
	}
	add(endpoint, clusterID+"/privateendpoints/storage", streamAnalyticsEndpointType)
	s.version[clusterID+"/liststreamingjobs"] = streamAnalyticsVersion
	s.handle = func(req *http.Request) (*http.Response, bool) {
		id := strings.ToLower(req.URL.Path)
		if req.Method == "POST" && id == clusterID+"/liststreamingjobs" {
			if req.Body != nil && req.ContentLength > 0 {
				t.Fatal("ListStreamingJobs must not have a request body")
			}
			if status := s.status[id]; status != 0 {
				return jsonResponse(status, map[string]any{}, nil), true
			}
			values := []any{}
			if !s.gone[jobID] && associated {
				values = append(values, map[string]any{"id": jobID, "jobState": object(s.records[jobID]["properties"])["jobState"], "streamingUnits": 1})
			}
			return jsonResponse(200, map[string]any{"value": values, "nextLink": nil}, nil), true
		}
		if req.Method == "GET" && id == jobID && !s.gone[id] && s.status[id] == 0 {
			payload, _ := json.Marshal(s.records[id])
			var raw map[string]any
			json.Unmarshal(payload, &raw)
			props := object(raw["properties"])
			for _, key := range []string{"inputs", "outputs", "functions"} {
				delete(props, key)
			}
			if req.URL.Query().Get("$expand") == "" {
				delete(props, "transformation")
			} else if req.URL.Query().Get("$expand") != "transformation" {
				t.Fatal("unexpected job expansion", req.URL)
			}
			return jsonResponse(200, raw, nil), true
		}
		return nil, false
	}
	r := s.runtime(t)
	var assets []asset.Asset
	for _, raw := range raws {
		value := dnsAsset(t, r, raw)
		if value.Identity.NativeType == streamAnalyticsTransformationType {
			value.Capabilities = nil
		}
		assets = append(assets, value)
	}
	return s, r, assets
}

func streamAnalyticsAfterDelete(s *dnsScenario) {
	for id := range s.records {
		for deleted, gone := range s.gone {
			if gone && strings.HasPrefix(id, deleted+"/") {
				s.gone[id] = true
			}
		}
	}
}

func TestStreamAnalyticsNativeInventoryAndReviewedCleanup(t *testing.T) {
	for _, kind := range append(streamAnalyticsOwnedKinds(streamAnalyticsJobType), streamAnalyticsJobType, streamAnalyticsClusterType, streamAnalyticsEndpointType) {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := streamAnalyticsScenario(t, false)
			target := cdnAsset(t, assets, kind)
			batch, err := r.List(context.Background(), productRequest(r, kind))
			if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].NativeID != target.Identity.NativeID || batch.Items[0].Location != "westus" {
				t.Fatal("Stream Analytics native inventory", kind, len(batch.Items), batch.Complete, err)
			}
			if batch.Items[0].Normalized["_inventory_source"] != productInventorySource {
				t.Fatal("Stream Analytics did not use the product API")
			}
			if kind == streamAnalyticsTransformationType {
				if _, err := r.ResolveAction(context.Background(), "connection", target); err == nil || batch.Items[0].Normalized["cleanup_controller_only"] != true {
					t.Fatal("query definition invented an independent DELETE")
				}
				contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
				contribution, err := contributor.Contribute(context.Background(), "scope", assets)
				if err != nil {
					t.Fatal(err)
				}
				solved, err := plan.Solve(plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{target.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
				if err != nil || !slices.ContainsFunc(solved.Blockers, func(b plan.Blocker) bool {
					return b.Code == "managed_by_controller" && b.ControllerID == cdnAsset(t, assets, streamAnalyticsJobType).ID
				}) {
					t.Fatal("query definition did not name its actual owning job", err, solved.Blockers)
				}
				return
			}
			_, input := dnsRequest(t, r, assets, target)
			solved, err := plan.Solve(input)
			if err != nil || len(solved.Blockers) != 0 {
				t.Fatal("Stream Analytics cleanup plan", solved.Blockers, err)
			}
			if kind == streamAnalyticsJobType && (len(solved.Steps) != 1 || len(solved.ImpactItems) != 4) {
				t.Fatal("native job cascade is not fully reviewed", len(solved.Steps), len(solved.ImpactItems))
			}
			if kind == streamAnalyticsClusterType && len(solved.Steps) != 2 {
				t.Fatal("cluster must first delete its private endpoint", len(solved.Steps))
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
					t.Fatal("native Stream Analytics deletion", value.Identity.NativeID, err)
				}
				if value.Identity.NativeType == streamAnalyticsJobType {
					waited, err := driver.Wait(context.Background(), request, result)
					if err != nil || waited.Done {
						t.Fatal("parent absence cannot prove child absence", waited, err)
					}
				}
				streamAnalyticsAfterDelete(s)
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
					t.Fatal("resumed Stream Analytics absence check", waited, err)
				}
				before := len(s.deletes)
				if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != before {
					t.Fatal("delete was not idempotent after absence", err)
				}
			}
			for id, raw := range s.records {
				if raw["type"] == storageType && s.gone[id] {
					t.Fatal("Stream Analytics cleanup deleted the data store")
				}
			}
		})
	}
}

func TestStreamAnalyticsClusterDoesNotOwnAssociatedJobs(t *testing.T) {
	s, r, assets := streamAnalyticsScenario(t, true)
	cluster := cdnAsset(t, assets, streamAnalyticsClusterType)
	job := cdnAsset(t, assets, streamAnalyticsJobType)
	contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
	contribution, err := contributor.Contribute(context.Background(), "scope", assets)
	if err != nil || len(contribution.Unresolved) != 0 {
		t.Fatal("native cross-group association", len(contribution.Unresolved), err)
	}
	for _, binding := range contribution.Bindings {
		if binding.ControllerAssetID == cluster.ID && binding.ManagedAssetID == job.ID {
			t.Fatal("cluster falsely owns an independent job")
		}
	}
	for _, relation := range contribution.Relationships {
		if relation.SourceAssetID == cluster.ID && relation.TargetAssetID == job.ID && relation.Evidence[graph.RelationshipEvidenceAutomaticSelection] != false {
			t.Fatal("cluster deletion automatically selected an independent job")
		}
	}
	input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{cluster.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
	solved, err := plan.Solve(input)
	if err != nil || len(solved.Blockers) == 0 || slices.ContainsFunc(solved.Steps, func(step plan.CleanupTaskStep) bool { return step.AssetID == job.ID }) {
		t.Fatal("retained job did not block cluster cleanup", len(solved.Steps), solved.Blockers, err)
	}
	input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, job.ID)
	solved, err = plan.Solve(input)
	if err != nil || len(solved.Blockers) != 0 || len(solved.Steps) != 3 || len(solved.ImpactItems) != 4 {
		t.Fatal("explicit job and cluster selection", len(solved.Steps), len(solved.ImpactItems), solved.Blockers, err)
	}
	for _, step := range solved.Steps {
		value := assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.ID == step.AssetID })]
		request := servicePlanRequest(solved, assets, value)
		driver, _ := r.ResolveAction(context.Background(), "connection", value)
		result, err := driver.Execute(context.Background(), request)
		if err != nil {
			t.Fatal("explicit associated cleanup", value.Identity.NativeType, err)
		}
		streamAnalyticsAfterDelete(s)
		if waited, err := driver.Wait(context.Background(), request, result); err != nil || !waited.Done {
			t.Fatal("associated cleanup absence", waited, err)
		}
	}
	if slices.Index(s.deletes, job.Identity.NativeID) >= slices.Index(s.deletes, cluster.Identity.NativeID) {
		t.Fatal("cluster deletion preceded the selected job")
	}
}
