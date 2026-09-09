package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	tpuTestParent = "projects/sample-project/locations/us-central2-b"
	tpuTestQueue  = tpuTestParent + "/queuedResources/training"
	tpuTestNode   = tpuTestParent + "/nodes/training-0"
	tpuTestNode1  = tpuTestParent + "/nodes/training-1"
	tpuTestDisk   = "projects/sample-project/zones/us-central2-b/disks/data-0"
)

type tpuScenario struct {
	resources        map[string]map[string]any
	operations       map[string]map[string]any
	calls, mutations []string
	emptyPage        bool
	handle           func(*http.Request) (*http.Response, bool)
}

func newTPUScenario(t *testing.T) *tpuScenario {
	t.Helper()
	raw, err := os.ReadFile("fixtures/tpu/resources.json")
	if err != nil {
		t.Fatal(err)
	}
	s := &tpuScenario{operations: map[string]map[string]any{}}
	if err := json.Unmarshal(raw, &s.resources); err != nil {
		t.Fatal(err)
	}
	return s
}

// Literal native URLs, versions, masks and response fields deliberately do not
// use the catalog or binder whose behavior this scenario verifies.
func (s *tpuScenario) transport(t *testing.T) roundTripFunc {
	t.Helper()
	return func(req *http.Request) (*http.Response, error) {
		s.calls = append(s.calls, req.Method+" "+req.URL.String())
		if s.handle != nil {
			if response, ok := s.handle(req); ok {
				return response, nil
			}
		}
		respond := func(code int, data any) (*http.Response, error) { return dataformResponse(req, code, data), nil }
		if req.URL.Host == "compute.googleapis.com" && req.Method == "GET" && strings.HasPrefix(req.URL.Path, "/compute/v1/projects/sample-project/zones/us-central2-b/disks/") {
			name := strings.TrimPrefix(req.URL.Path, "/compute/v1/")
			if data := s.resources[name]; data != nil {
				return respond(200, data)
			}
			return respond(404, map[string]any{})
		}
		if req.URL.Host != "tpu.googleapis.com" {
			t.Fatalf("unexpected native TPU host: %s", req.URL)
		}
		version := "v2"
		if strings.HasSuffix(req.URL.Path, "/reservations") {
			version = "v2alpha1"
		}
		if !strings.HasPrefix(req.URL.Path, "/"+version+"/projects/sample-project/") {
			t.Fatalf("wrong native TPU version/project: %s", req.URL)
		}
		name := strings.TrimPrefix(req.URL.Path, "/"+version+"/")
		if req.Method == "GET" && name == "projects/sample-project/locations" {
			if s.emptyPage && req.URL.Query().Get("pageToken") == "" {
				return respond(200, map[string]any{"nextPageToken": "second"})
			}
			return respond(200, map[string]any{"locations": []any{map[string]any{"name": "projects/sample-project/locations/us-central2-b", "locationId": "us-central2-b"}, map[string]any{"name": "projects/sample-project/locations/europe-west4-a", "locationId": "europe-west4-a"}}})
		}
		if req.Method == "GET" {
			if strings.Contains(name, "/operations/") {
				if data := s.operations[name]; data != nil {
					return respond(200, data)
				}
				return respond(404, map[string]any{})
			}
			collection := last(name)
			if slices.Contains([]string{"nodes", "queuedResources", "reservations"}, collection) {
				if s.emptyPage && req.URL.Query().Get("pageToken") == "" {
					return respond(200, map[string]any{"nextPageToken": "second"})
				}
				if token := req.URL.Query().Get("pageToken"); token != "" && token != "second" {
					t.Fatalf("wrong page token %s", req.URL)
				}
				var names []string
				for id := range s.resources {
					names = append(names, id)
				}
				sort.Strings(names)
				records := []any{}
				for _, id := range names {
					if strings.HasPrefix(id, name+"/") && !strings.Contains(strings.TrimPrefix(id, name+"/"), "/") {
						records = append(records, s.resources[id])
					}
				}
				return respond(200, map[string]any{collection: records})
			}
			if strings.Contains(name, "/reservations/") {
				t.Fatalf("invented Reservation GET %s", req.URL)
			}
			if data := s.resources[name]; data != nil {
				return respond(200, data)
			}
			return respond(404, map[string]any{})
		}
		if req.Method != "PATCH" && req.Method != "DELETE" {
			t.Fatalf("invented TPU mutation %s %s", req.Method, req.URL)
		}
		node := strings.Contains(name, "/nodes/")
		queue := strings.Contains(name, "/queuedResources/")
		if !node && !queue || queue && req.Method == "PATCH" {
			t.Fatalf("invented TPU action %s %s", req.Method, req.URL)
		}
		verb := "delete"
		if req.Method == "PATCH" {
			var body map[string]any
			if json.NewDecoder(req.Body).Decode(&body) != nil || body["name"] != name || len(body) != 2 {
				t.Fatal("wrong TPU patch body")
			}
			disks, ok := body["dataDisks"].([]any)
			if !ok || len(disks) != 0 || req.URL.RawQuery != "updateMask=data_disks" {
				t.Fatalf("wrong native TPU detach: %s %#v", req.URL, body)
			}
			verb = "update"
		} else {
			if req.Body != nil {
				raw, _ := io.ReadAll(req.Body)
				if len(raw) > 0 {
					t.Fatal("invented TPU delete body")
				}
			}
			query := req.URL.Query()
			if node && len(query) > 0 || queue && (len(query) > 1 || query.Get("force") != "" || len(query) > 0 && query.Get("requestId") == "") {
				t.Fatalf("invented TPU delete query %s", req.URL)
			}
		}
		s.mutations = append(s.mutations, req.Method+" "+name)
		if s.resources[name] == nil {
			return respond(404, map[string]any{})
		}
		op := strings.Join(strings.Split(name, "/")[:4], "/") + fmt.Sprintf("/operations/action-%d", len(s.mutations))
		s.operations[op] = map[string]any{"name": op, "metadata": map[string]any{"@type": "type.googleapis.com/google.cloud.tpu.v2.OperationMetadata", "apiVersion": "v2", "target": name, "verb": verb}}
		return respond(200, s.operations[op])
	}
}
func (s *tpuScenario) finish(operation string) {
	name := strings.TrimPrefix(operation, "https://tpu.googleapis.com/v2/")
	data := s.operations[name]
	data["done"] = true
	metadata := object(data["metadata"])
	target := text(metadata["target"])
	if metadata["verb"] == "update" {
		s.resources[target]["dataDisks"] = []any{}
	} else {
		delete(s.resources, target)
		if strings.Contains(target, "/nodes/") && s.resources[tpuTestQueue] != nil {
			s.resources[tpuTestQueue]["state"] = map[string]any{"state": "SUSPENDING"}
		}
	}
}
func (s *tpuScenario) inventory(t *testing.T, r *Runtime) []asset.Asset {
	t.Helper()
	var assets []asset.Asset
	for _, kind := range []string{tpuNodeType, tpuQueueType, tpuReservationType} {
		req := productRequest(r, kind, "project")
		for attempts := 0; ; attempts++ {
			batch, err := r.List(context.Background(), req)
			if err != nil || attempts > 30 {
				t.Fatalf("TPU inventory %s %+v %v", kind, batch, err)
			}
			for _, item := range batch.Items {
				capabilities := asset.CapabilitySet{}
				if item.Actionable != nil && *item.Actionable {
					capabilities = append(capabilities, asset.CapabilityActionable)
				}
				assets = append(assets, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: kind, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: capabilities, Location: item.Location})
			}
			if batch.Complete {
				break
			}
			req.Cursor = batch.NextCursor
		}
	}
	for name, data := range s.resources {
		if !strings.Contains(name, "/disks/") {
			continue
		}
		id := "//compute.googleapis.com/" + name
		assets = append(assets, asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: "compute.googleapis.com/Disk", NativeID: id}, Normalized: roundTripDataformJSON(t, data), Capabilities: asset.CapabilitySet{asset.CapabilityActionable}})
	}
	return assets
}
func tpuReviewed(t *testing.T, s *tpuScenario, root string) (*Runtime, []asset.Asset, plan.Result) {
	t.Helper()
	r := protocolRuntime(t, s.transport(t))
	assets := s.inventory(t, r)
	hook, err := r.ServiceLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := hook.Contribute(context.Background(), "scope", assets)
	if err != nil || len(contribution.Unresolved) > 0 {
		t.Fatalf("TPU lifecycle %+v %v", contribution, err)
	}
	var relations []graph.Relationship
	for _, a := range assets {
		for _, kind := range []string{tpuQueueType, "compute.googleapis.com/Disk"} {
			refs, _ := discoveryStrings(a.Normalized[referenceKey(kind)])
			for _, id := range refs {
				relations = append(relations, graph.Relationship{SourceAssetID: a.ID, TargetAssetID: asset.AssetID(id), Type: graph.RelationshipDependsOn, Confidence: 1})
			}
		}
	}
	value := batchAsset(assets, root)
	result, err := plan.Solve(plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{value.ID}, LifecycleBindings: contribution.Bindings, Relationships: append(contribution.Relationships, relations...)})
	if err != nil || len(result.Blockers) > 0 {
		t.Fatalf("TPU plan %+v %v", result, err)
	}
	return r, assets, result
}

func TestTPUNativeInventoryPlanDetachDeleteAndRestart(t *testing.T) {
	ctx := context.Background()
	s := newTPUScenario(t)
	s.emptyPage = true
	r, assets, planResult := tpuReviewed(t, s, tpuTestQueue)
	if len(assets) != 7 || len(planResult.Steps) != 3 {
		t.Fatalf("TPU native assets=%d steps=%+v", len(assets), planResult.Steps)
	}
	encoded, _ := json.Marshal(assets)
	if strings.Contains(string(encoded), "TPU_PRIVATE_") {
		t.Fatal("native configuration escaped TPU redaction")
	}
	queue := batchAsset(assets, tpuTestQueue)
	queueRequest := dataformRequest(t, planResult, assets, queue)
	if len(queueRequest.PrerequisiteDeletions) != 2 || len(queueRequest.LifecycleImpacts) != 0 {
		t.Fatalf("wrong queue review %+v", queueRequest)
	}
	queueDriver, err := r.ResolveAction(ctx, "connection", queue)
	if err != nil {
		t.Fatal(err)
	}
	if check, err := queueDriver.Preflight(ctx, queueRequest); err == nil && check.Allowed {
		t.Fatal("queue accepted existing nodes")
	}
	for _, name := range []string{tpuTestNode, tpuTestNode1} {
		node := batchAsset(assets, name)
		request := dataformRequest(t, planResult, assets, node)
		if len(request.LifecycleImpacts) != 1 || request.LifecycleImpacts[0].Delete {
			t.Fatalf("disk must be retained: %+v", request)
		}
		driver, err := r.ResolveAction(ctx, "connection", node)
		if err != nil {
			t.Fatal(err)
		}
		result, err := driver.Execute(ctx, request)
		if err != nil || result.Data["phase"] != "tpu_detach" {
			t.Fatalf("TPU detach %+v %v", result, err)
		}
		result = roundTripDataformJSON(t, result)
		driver, err = r.ResolveAction(ctx, "connection", node)
		if err != nil {
			t.Fatal(err)
		}
		wait, err := driver.Wait(ctx, request, result)
		if err != nil || wait.Done {
			t.Fatalf("pending TPU detach %+v %v", wait, err)
		}
		s.finish(result.ProviderOperationID)
		wait, err = driver.Wait(ctx, request, result)
		if err != nil || wait.Done || wait.Data["phase"] != "tpu_delete" {
			t.Fatalf("TPU detach->delete %+v %v", wait, err)
		}
		result.Data = roundTripDataformJSON(t, wait.Data)
		driver, _ = r.ResolveAction(ctx, "connection", node)
		wait, err = driver.Wait(ctx, request, result)
		if err != nil || wait.Done {
			t.Fatalf("pending native delete %+v %v", wait, err)
		}
		s.finish(text(result.Data["operation"]))
		wait, err = driver.Wait(ctx, request, result)
		if err != nil || !wait.Done {
			t.Fatalf("TPU node completion %+v %v", wait, err)
		}
	}
	result, err := queueDriver.Execute(ctx, queueRequest)
	if err != nil || result.Data["phase"] != "tpu_queue_settle" {
		t.Fatalf("queue settle %+v %v", result, err)
	}
	s.resources[tpuTestQueue]["state"] = map[string]any{"state": "SUSPENDED"}
	wait, err := queueDriver.Wait(ctx, queueRequest, roundTripDataformJSON(t, result))
	if err != nil || wait.Done || wait.Data["phase"] != "tpu_delete" {
		t.Fatalf("queue delete %+v %v", wait, err)
	}
	result.Data = wait.Data
	s.finish(text(result.Data["operation"]))
	wait, err = queueDriver.Wait(ctx, queueRequest, result)
	if err != nil || !wait.Done {
		t.Fatalf("queue completion %+v %v", wait, err)
	}
	want := []string{"PATCH " + tpuTestNode, "DELETE " + tpuTestNode, "PATCH " + tpuTestNode1, "DELETE " + tpuTestNode1, "DELETE " + tpuTestQueue}
	if !slices.Equal(s.mutations, want) || s.resources[tpuTestDisk] == nil {
		t.Fatalf("TPU native writes %+v", s.mutations)
	}
}

func TestTPUZonalScopesNativeAliasesAndReadOnlyReservations(t *testing.T) {
	ctx := context.Background()
	s := newTPUScenario(t)
	r := protocolRuntime(t, s.transport(t))
	req := productRequest(r, tpuNodeType, "us-central2")
	var items []contracts.InventoryItem
	for i := 0; ; i++ {
		batch, err := r.List(ctx, req)
		if err != nil || i > 10 {
			t.Fatalf("zonal TPU list %+v %v", batch, err)
		}
		items = append(items, batch.Items...)
		if batch.Complete {
			break
		}
		req.Cursor = batch.NextCursor
	}
	if len(items) != 2 || items[0].Scope.NativeID != "us-central2" || items[0].Location != "us-central2-b" {
		t.Fatalf("zonal TPU scope %+v", items)
	}
	refs, _ := discoveryStrings(items[0].Normalized[referenceKey("compute.googleapis.com/Subnetwork")])
	if !slices.Contains(refs, "//compute.googleapis.com/projects/sample-project/regions/us-central2/subnetworks/training-subnet") {
		t.Fatalf("TPU subnet reference %+v", refs)
	}
	c, err := r.resolve(ctx, "connection")
	if err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"https://tpu.googleapis.com/v2/" + tpuTestNode, "https://tpu.googleapis.com/v2alpha1/" + strings.Replace(tpuTestNode, "sample-project", "123456", 1)} {
		id, err := c.tpuID(tpuNodeType, alias)
		if err != nil || id != "//tpu.googleapis.com/"+tpuTestNode {
			t.Fatalf("TPU alias %s: %s %v", alias, id, err)
		}
	}
	assets := s.inventory(t, r)
	for _, value := range assets {
		if value.Identity.NativeType != tpuReservationType {
			continue
		}
		if slices.Contains(value.Capabilities, asset.CapabilityActionable) {
			t.Fatal("invented TPU Reservation deletion")
		}
		if _, err := r.ResolveAction(ctx, "connection", value); err == nil {
			t.Fatal("invented Reservation action driver")
		}
	}
	name := "projects/sample-project/locations/europe-west4-a/nodes/standalone"
	node := batchAsset(assets, name)
	request := contracts.ActionRequest{Action: "delete", Asset: node}
	driver, err := r.ResolveAction(ctx, "connection", node)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(ctx, request)
	if err != nil || result.Data["phase"] != "tpu_delete" || len(s.mutations) != 1 || s.mutations[0] != "DELETE "+name {
		t.Fatalf("standalone TPU delete %+v %v", result, err)
	}
	s.finish(result.ProviderOperationID)
	delete(s.operations, strings.TrimPrefix(result.ProviderOperationID, "https://tpu.googleapis.com/v2/"))
	wait, err := driver.Wait(ctx, request, result)
	if err != nil || !wait.Done || len(s.mutations) != 1 {
		t.Fatalf("expired native operation %+v %v", wait, err)
	}
}

func TestTPURetryAndExpiredDetachWaitForActualAttachmentRemoval(t *testing.T) {
	for _, mode := range []string{"already_detached", "done_still_attached", "expired_still_attached", "node_settle", "deleting"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := newTPUScenario(t)
			r, assets, planResult := tpuReviewed(t, s, tpuTestNode)
			node := batchAsset(assets, tpuTestNode)
			request := dataformRequest(t, planResult, assets, node)
			driver, _ := r.ResolveAction(ctx, "connection", node)
			switch mode {
			case "already_detached":
				s.resources[tpuTestNode]["dataDisks"] = []any{}
			case "node_settle":
				s.resources[tpuTestNode]["state"] = "RESTARTING"
			case "deleting":
				s.resources[tpuTestNode]["dataDisks"] = []any{}
				s.resources[tpuTestNode]["state"] = "DELETING"
			}
			result, err := driver.Execute(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "already_detached" {
				if result.Data["phase"] != "tpu_delete" || len(s.mutations) != 1 || s.mutations[0] != "DELETE "+tpuTestNode {
					t.Fatalf("unexpected retry %+v", result)
				}
				return
			}
			if mode == "deleting" {
				if len(s.mutations) != 0 || result.Data["phase"] != "tpu_delete" {
					t.Fatalf("replayed existing delete %+v", result)
				}
				return
			}
			if mode == "node_settle" {
				if result.Data["phase"] != "tpu_node_settle" || len(s.mutations) != 0 {
					t.Fatalf("ignored native state %+v", result)
				}
				s.resources[tpuTestNode]["state"] = "READY"
				wait, err := driver.Wait(ctx, request, result)
				if err != nil || wait.Data["phase"] != "tpu_detach" || len(s.mutations) != 1 {
					t.Fatalf("node did not resume detach %+v %v", wait, err)
				}
				return
			}
			name := strings.TrimPrefix(result.ProviderOperationID, "https://tpu.googleapis.com/v2/")
			if mode == "done_still_attached" {
				s.operations[name]["done"] = true
			} else {
				delete(s.operations, name)
			}
			wait, err := driver.Wait(ctx, request, result)
			if err != nil || wait.Done || len(s.mutations) != 1 {
				t.Fatalf("LRO completion was treated as detachment %+v %v", wait, err)
			}
			s.resources[tpuTestNode]["dataDisks"] = []any{}
			wait, err = driver.Wait(ctx, request, result)
			if err != nil || wait.Done || wait.Data["phase"] != "tpu_delete" || len(s.mutations) != 2 {
				t.Fatalf("actual detachment not observed %+v %v", wait, err)
			}
		})
	}
}

func TestTPUSingleAllocatedNamesEmptyQueuesAndOrphanedNodes(t *testing.T) {
	for _, mode := range []string{"allocated_name", "explicit_name", "empty_request", "orphan_node", "shared_disk"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := newTPUScenario(t)
			switch mode {
			case "allocated_name", "explicit_name":
				spec := object(array(object(s.resources[tpuTestQueue]["tpu"])["nodeSpec"])[0])
				delete(spec, "multisliceParams")
				if mode == "explicit_name" {
					spec["nodeId"] = "training-0"
				}
				delete(s.resources, tpuTestNode1)
			case "empty_request":
				delete(s.resources, tpuTestNode)
				delete(s.resources, tpuTestNode1)
				s.resources[tpuTestQueue]["state"] = map[string]any{"state": "ACCEPTED"}
			case "orphan_node":
				delete(s.resources, tpuTestQueue)
			case "shared_disk":
				s.resources[tpuTestNode]["dataDisks"] = []any{map[string]any{"sourceDisk": tpuTestDisk, "mode": "READ_ONLY"}}
				s.resources[tpuTestNode1]["dataDisks"] = []any{map[string]any{"sourceDisk": tpuTestDisk, "mode": "READ_ONLY"}}
			}
			root := tpuTestQueue
			if mode == "orphan_node" {
				root = tpuTestNode
			}
			r, assets, result := tpuReviewed(t, s, root)
			value := batchAsset(assets, root)
			request := dataformRequest(t, result, assets, value)
			if mode == "allocated_name" || mode == "explicit_name" {
				if len(request.PrerequisiteDeletions) != 1 || request.PrerequisiteDeletions[0].Asset.Identity.NativeID != "//tpu.googleapis.com/"+tpuTestNode {
					t.Fatalf("lost real native node name %+v", request)
				}
				return
			}
			if mode == "shared_disk" {
				for _, name := range []string{tpuTestNode, tpuTestNode1} {
					node := batchAsset(assets, name)
					request := dataformRequest(t, result, assets, node)
					if len(request.LifecycleImpacts) != 1 || request.LifecycleImpacts[0].Delete || request.LifecycleImpacts[0].Asset.Identity.NativeID != "//compute.googleapis.com/"+tpuTestDisk {
						t.Fatalf("shared TPU disk must be retained %+v", request)
					}
				}
				return
			}
			driver, _ := r.ResolveAction(ctx, "connection", value)
			action, err := driver.Execute(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "empty_request" && action.Data["phase"] != "tpu_delete" {
				t.Fatalf("empty queue needs only native delete %+v", action)
			}
			if mode == "orphan_node" && (value.Normalized[tpuQueueProof] != "absent" || action.Data["phase"] != "tpu_detach") {
				t.Fatalf("orphan node proof %+v %+v", value, action)
			}
		})
	}
}
