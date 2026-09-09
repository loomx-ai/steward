package gcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	dpRoot      = "projects/sample-project/regions/us-central1/clusters/analytics"
	dpOther     = "projects/sample-project/regions/europe-west1/clusters/analytics"
	dpNode      = dpRoot + "/nodeGroups/aux-real-17"
	dpJob       = "projects/sample-project/regions/us-central1/jobs/spark-1"
	dpVM        = "projects/sample-project/zones/us-central1-a/instances/analytics-m"
	dpWorker    = "projects/sample-project/zones/us-central1-a/instances/analytics-w"
	dpMIG       = "projects/sample-project/zones/us-central1-a/instanceGroupManagers/analytics-workers"
	dpTemplate  = "projects/sample-project/global/instanceTemplates/analytics-template"
	dpInput     = "projects/sample-project/regions/us-central1/disks/input-data"
	dpOperation = "projects/sample-project/regions/us-central1/operations/delete-analytics"
)

type dataprocScenario struct {
	resources map[string]map[string]any
	operation map[string]any
	mutations []string
	emptyPage bool
	handle    func(*http.Request) (*http.Response, bool)
}

func newDataprocScenario(t *testing.T) *dataprocScenario {
	t.Helper()
	raw, err := os.ReadFile("fixtures/dataproc/resources.json")
	if err != nil {
		t.Fatal(err)
	}
	s := &dataprocScenario{}
	if err := json.Unmarshal(raw, &s.resources); err != nil {
		t.Fatal(err)
	}
	s.operation = map[string]any{"name": dpOperation, "metadata": map[string]any{"clusterName": "analytics", "clusterUuid": s.resources[dpRoot]["clusterUuid"], "operationType": "delete", "description": "DATAPROC_PRIVATE_OPERATION"}}
	return s
}

// Literal service paths, enum values and response fields do not use the catalog
// under test. Cleanup timing is deliberately controlled by each test.
func (s *dataprocScenario) transport(t *testing.T) roundTripFunc {
	t.Helper()
	return func(req *http.Request) (*http.Response, error) {
		if s.handle != nil {
			if response, handled := s.handle(req); handled {
				return response, nil
			}
		}
		respond := func(code int, value any) (*http.Response, error) {
			raw, _ := json.Marshal(value)
			return apiResponse(req, code, string(raw)), nil
		}
		name := strings.TrimPrefix(req.URL.Path, "/v1/")
		if req.URL.Host == "dataproc.googleapis.com" {
			if !strings.HasPrefix(req.URL.Path, "/v1/") {
				t.Fatalf("wrong Dataproc API %s", req.URL)
			}
			if req.Method == "POST" && strings.HasSuffix(name, ":cancel") {
				target := strings.TrimSuffix(name, ":cancel")
				if target != dpJob {
					t.Fatalf("wrong cancel target %s", req.URL)
				}
				raw, err := io.ReadAll(req.Body)
				if err != nil || string(raw) != "{}" {
					t.Fatalf("wrong cancel body %s %v", raw, err)
				}
				s.mutations = append(s.mutations, "POST "+name)
				if s.resources[target] == nil {
					return respond(404, map[string]any{})
				}
				object(s.resources[target]["status"])["state"] = "CANCEL_PENDING"
				return respond(200, s.resources[target])
			}
			if req.Method == "DELETE" {
				s.mutations = append(s.mutations, "DELETE "+name)
				if name == dpRoot {
					if req.URL.Query().Get("clusterUuid") != text(s.resources[dpRoot]["clusterUuid"]) || req.URL.Query().Get("requestId") == "" || len(req.URL.Query()) != 2 {
						t.Fatalf("missing native UUID guard %s", req.URL)
					}
					object(s.resources[dpRoot]["status"])["state"] = "DELETING"
					return respond(200, s.operation)
				}
				if name == dpJob {
					if state := text(object(s.resources[dpJob]["status"])["state"]); state != "DONE" && state != "ERROR" && state != "CANCELLED" {
						t.Fatalf("deleted active job %s", state)
					}
				} else if strings.HasSuffix(name, "/workflowTemplates/hourly") {
					if req.URL.Query().Get("version") != "3" {
						t.Fatalf("workflow version omitted %s", req.URL)
					}
				} else if !strings.HasSuffix(name, "/autoscalingPolicies/elastic") {
					t.Fatalf("invented DELETE %s", req.URL)
				}
				delete(s.resources, name)
				return respond(200, map[string]any{})
			}
			if req.Method != "GET" {
				t.Fatalf("invented Dataproc method %s %s", req.Method, req.URL)
			}
			if name == dpOperation {
				if s.operation == nil {
					return respond(404, map[string]any{})
				}
				return respond(200, s.operation)
			}
			collection := last(name)
			if collection == "clusters" || collection == "jobs" || collection == "autoscalingPolicies" || collection == "workflowTemplates" {
				if !strings.HasPrefix(name, "projects/sample-project/regions/us-central1/") && !strings.HasPrefix(name, "projects/sample-project/regions/europe-west1/") {
					t.Fatalf("wrong regional scope %s", req.URL)
				}
				if s.emptyPage && req.URL.Query().Get("pageToken") == "" {
					return respond(200, map[string]any{"nextPageToken": "dp-page-2"})
				}
				if token := req.URL.Query().Get("pageToken"); token != "" && token != "dp-page-2" {
					t.Fatalf("wrong Dataproc cursor %s", req.URL)
				}
				var items []any
				ids := []string{}
				for id := range s.resources {
					ids = append(ids, id)
				}
				sort.Strings(ids)
				for _, id := range ids {
					data := s.resources[id]
					if strings.HasPrefix(id, name+"/") && !strings.Contains(strings.TrimPrefix(id, name+"/"), "/") {
						if cluster := req.URL.Query().Get("clusterName"); cluster != "" && text(object(data["placement"])["clusterName"]) != cluster {
							continue
						}
						items = append(items, data)
					}
				}
				key := collection
				if collection == "autoscalingPolicies" {
					key = "policies"
				}
				if collection == "workflowTemplates" {
					key = "templates"
				}
				return respond(200, map[string]any{key: items})
			}
		} else if req.URL.Host == "compute.googleapis.com" {
			name = strings.TrimPrefix(req.URL.Path, "/compute/v1/")
			if name == dpMIG+"/listManagedInstances" {
				if req.Method != "POST" {
					t.Fatalf("wrong native MIG listing %s", req.Method)
				}
				var records []any
				if vm := s.resources[dpWorker]; vm != nil {
					records = append(records, map[string]any{"instance": vm["selfLink"], "id": vm["id"], "currentAction": "NONE"})
				}
				return respond(200, map[string]any{"managedInstances": records})
			}
			if name == dpMIG+"/listPerInstanceConfigs" {
				if req.Method != "POST" {
					t.Fatalf("wrong config listing %s", req.Method)
				}
				return respond(200, map[string]any{})
			}
			if req.Method != "GET" {
				t.Fatalf("Dataproc cleanup issued Compute write %s %s", req.Method, req.URL)
			}
			if name == "projects/sample-project/regions" {
				return respond(200, map[string]any{"items": []any{map[string]any{"name": "us-central1", "status": "UP"}, map[string]any{"name": "europe-west1", "status": "UP"}}})
			}
			if strings.HasPrefix(name, "projects/sample-project/aggregated/") {
				collection := last(name)
				uid := ""
				if filter := req.URL.Query().Get("filter"); filter != "" {
					if !strings.HasPrefix(filter, "labels.goog-dataproc-cluster-uuid = \"") || !strings.HasSuffix(filter, "\"") || req.URL.Query().Get("includeAllScopes") != "true" || req.URL.Query().Get("returnPartialSuccess") == "true" {
						t.Fatalf("wrong complete Dataproc UID query %s", req.URL)
					}
					uid = strings.TrimSuffix(strings.TrimPrefix(filter, "labels.goog-dataproc-cluster-uuid = \""), "\"")
				}
				if s.emptyPage && req.URL.Query().Get("pageToken") == "" {
					return respond(200, map[string]any{"items": map[string]any{"zones/us-central1-a": map[string]any{"warning": map[string]any{"code": "NO_RESULTS_ON_PAGE"}}}, "nextPageToken": "compute-page-2"})
				}
				groups := map[string]any{}
				for id, data := range s.resources {
					parts := strings.Split(id, "/")
					if len(parts) < 5 || text(data["selfLink"]) == "" || parts[len(parts)-2] != collection || uid != "" && text(object(data["labels"])["goog-dataproc-cluster-uuid"]) != uid {
						continue
					}
					key := parts[2]
					if key != "global" {
						key += "/" + parts[3]
					}
					if groups[key] == nil {
						groups[key] = map[string]any{collection: []any{}}
					}
					group := object(groups[key])
					group[collection] = append(array(group[collection]), data)
				}
				return respond(200, map[string]any{"items": groups})
			}
			if name == "projects/sample-project/regions/us-central1/disks" {
				var records []any
				if s.resources[dpInput] != nil {
					records = append(records, s.resources[dpInput])
				}
				return respond(200, map[string]any{"items": records})
			}
		} else if req.URL.Host == "container.googleapis.com" {
			if req.Method != "GET" {
				t.Fatalf("Dataproc changed GKE %s", req.URL)
			}
			if strings.HasSuffix(name, "/clusters") {
				return respond(200, map[string]any{})
			}
		} else {
			t.Fatalf("unexpected API %s", req.URL)
		}
		if data := s.resources[name]; data != nil {
			return respond(200, data)
		}
		return respond(404, map[string]any{})
	}
}

func (s *dataprocScenario) inventory(t *testing.T, r *Runtime) []asset.Asset {
	t.Helper()
	var assets []asset.Asset
	for _, target := range []struct{ kind, scope string }{{dataprocClusterType, "project"}, {dataprocJobType, "project"}, {dataprocNodeGroupType, "project"}, {dataprocPolicyType, "project"}, {dataprocTemplateType, "project"}, {instanceType, "project"}, {managerType, "project"}, {instanceGroupType, "project"}, {"compute.googleapis.com/Disk", "project"}, {"compute.googleapis.com/RegionDisk", "us-central1"}, {"compute.googleapis.com/InstanceTemplate", "project"}} {
		req := productRequest(r, target.kind, target.scope)
		for attempt := 0; ; attempt++ {
			page, err := r.List(context.Background(), req)
			if err != nil || attempt > 30 {
				t.Fatalf("Dataproc inventory %s: %+v %v", target.kind, page, err)
			}
			for _, item := range page.Items {
				capabilities := asset.CapabilitySet{}
				if item.Actionable != nil && *item.Actionable {
					capabilities = append(capabilities, asset.CapabilityActionable)
				}
				assets = append(assets, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: item.NativeType, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: capabilities, Location: item.Location})
			}
			if page.Complete {
				break
			}
			req.Cursor = page.NextCursor
		}
	}
	return assets
}

func dataprocReviewed(t *testing.T, s *dataprocScenario) (*Runtime, []asset.Asset, plan.Input, contracts.ActionRequest) {
	t.Helper()
	r := protocolRuntime(t, s.transport(t))
	assets := s.inventory(t, r)
	hook, err := r.ComputeLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := hook.Contribute(context.Background(), "scope", assets)
	if err != nil || len(contribution.Unresolved) > 0 {
		t.Fatalf("Dataproc contribution: %+v %v", contribution, err)
	}
	root := batchAsset(assets, dpRoot)
	input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{root.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) > 0 {
		t.Fatalf("Dataproc plan: %+v %v", result, err)
	}
	return r, assets, input, dataformRequest(t, result, assets, root)
}

func TestDataprocInventoryPlanDeleteAndRestart(t *testing.T) {
	s := newDataprocScenario(t)
	s.emptyPage = true
	r, assets, input, request := dataprocReviewed(t, s)
	if len(assets) != 17 || len(request.LifecycleImpacts) != 12 {
		t.Fatalf("wrong inventory/impact count %d/%d", len(assets), len(request.LifecycleImpacts))
	}
	raw, _ := json.Marshal(assets)
	if strings.Contains(string(raw), "DATAPROC_PRIVATE_") {
		t.Fatalf("native secrets escaped: %s", raw)
	}
	for _, kind := range []string{"compute.googleapis.com/Network", "compute.googleapis.com/Subnetwork", "storage.googleapis.com/Bucket", "iam.googleapis.com/ServiceAccount", dataprocPolicyType, "cloudkms.googleapis.com/CryptoKey"} {
		if len(array(request.Asset.Normalized[referenceKey(kind)])) == 0 {
			t.Fatalf("missing native dependency %s", kind)
		}
	}
	for _, name := range []string{dpNode, dpVM, dpMIG, dpTemplate} {
		input.ResolvedAssetIDs = []asset.AssetID{batchAsset(assets, name).ID}
		result, err := plan.Solve(input)
		if err != nil || len(result.Blockers) == 0 || len(result.Steps) > 0 {
			t.Fatalf("standalone managed cleanup bypassed: %+v %v", result, err)
		}
	}
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || result.ProviderOperationID == "" || len(s.mutations) != 1 {
		t.Fatalf("execute %+v %v %v", result, err, s.mutations)
	}
	raw, _ = json.Marshal(result)
	if strings.Contains(string(raw), "DATAPROC_PRIVATE_") {
		t.Fatal("operation details leaked")
	}
	// Restart with only the persisted action result; an acknowledged DELETE is not replayed.
	restored := contracts.ActionResult{}
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	r = protocolRuntime(t, s.transport(t))
	driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(context.Background(), request, restored)
	if err != nil || wait.Done {
		t.Fatalf("premature native operation completion %+v %v", wait, err)
	}
	s.operation["done"] = true
	delete(s.resources, dpRoot)
	wait, err = driver.Wait(context.Background(), request, restored)
	if err != nil || wait.Done {
		t.Fatalf("root 404 hid remaining members %+v %v", wait, err)
	}
	for _, impact := range request.LifecycleImpacts {
		if impact.Delete {
			delete(s.resources, strings.TrimPrefix(impact.Asset.Identity.NativeID, "//"+strings.Split(impact.Asset.Identity.NativeType, "/")[0]+"/"))
		}
	}
	wait, err = driver.Wait(context.Background(), request, restored)
	if err != nil || wait.Done {
		t.Fatalf("active retained job was treated as complete %+v %v", wait, err)
	}
	object(s.resources[dpJob]["status"])["state"] = "ERROR"
	wait, err = driver.Wait(context.Background(), request, restored)
	if err != nil || !wait.Done {
		t.Fatalf("finished cleanup %+v %v", wait, err)
	}
	read, err := driver.Readback(context.Background(), request)
	if err != nil || read.Exists || s.resources[dpInput] == nil || s.resources[dpOther] == nil || s.resources[dpJob] == nil || len(s.mutations) != 1 {
		t.Fatalf("wrong retained resources %+v %v %v", read, err, s.mutations)
	}
}

func TestDataprocSelectedJobCancelsBeforeCluster(t *testing.T) {
	s := newDataprocScenario(t)
	r, assets, input, _ := dataprocReviewed(t, s)
	root, job := batchAsset(assets, dpRoot), batchAsset(assets, dpJob)
	input.ResolvedAssetIDs = []asset.AssetID{root.ID, job.ID}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) > 0 || len(result.Steps) != 2 {
		t.Fatalf("selected job plan %+v %v", result, err)
	}
	rootRequest := dataformRequest(t, result, assets, root)
	jobRequest := dataformRequest(t, result, assets, job)
	if len(rootRequest.PrerequisiteDeletions) != 1 {
		t.Fatal("selected job not a prerequisite")
	}
	rootDriver, err := r.ResolveAction(context.Background(), "connection", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rootDriver.Execute(context.Background(), rootRequest); err == nil || len(s.mutations) != 0 {
		t.Fatal("cluster skipped live job prerequisite")
	}
	driver, err := r.ResolveAction(context.Background(), "connection", job)
	if err != nil {
		t.Fatal(err)
	}
	phase, err := driver.Execute(context.Background(), jobRequest)
	if err != nil || phase.Data["phase"] != "dataproc_job_cancel" {
		t.Fatalf("cancel %+v %v", phase, err)
	}
	restarted := protocolRuntime(t, s.transport(t))
	driver, err = restarted.ResolveAction(context.Background(), "connection", job)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(context.Background(), jobRequest, phase)
	if err != nil || wait.Done || len(s.mutations) != 1 {
		t.Fatalf("cancel replayed %+v %v %v", wait, err, s.mutations)
	}
	object(s.resources[dpJob]["status"])["state"] = "CANCELLED"
	wait, err = driver.Wait(context.Background(), jobRequest, phase)
	if err != nil || wait.Data["phase"] != "dataproc_job_delete" {
		t.Fatalf("cancel to delete %+v %v", wait, err)
	}
	phase.Data = wait.Data
	wait, err = driver.Wait(context.Background(), jobRequest, phase)
	if err != nil || !wait.Done {
		t.Fatalf("delete wait %+v %v", wait, err)
	}
	if _, err := rootDriver.Execute(context.Background(), rootRequest); err != nil {
		t.Fatal(err)
	}
	expected := []string{"POST " + dpJob + ":cancel", "DELETE " + dpJob, "DELETE " + dpRoot}
	if strings.Join(s.mutations, "|") != strings.Join(expected, "|") {
		t.Fatalf("wrong write order %v", s.mutations)
	}
}

func TestDataprocVirtualClusterRetainsGKEAndPools(t *testing.T) {
	s := newDataprocScenario(t)
	for id, data := range s.resources {
		if data["selfLink"] != nil || id == dpNode {
			delete(s.resources, id)
		}
	}
	delete(s.resources[dpRoot], "config")
	gke := "projects/sample-project/locations/us-central1/clusters/shared"
	pool := gke + "/nodePools/spark"
	s.resources[dpRoot]["virtualClusterConfig"] = map[string]any{"stagingBucket": "spark-staging", "kubernetesClusterConfig": map[string]any{"gkeClusterConfig": map[string]any{"gkeClusterTarget": gke, "nodePoolTarget": []any{map[string]any{"nodePool": pool, "roles": []any{"DEFAULT"}}}}}}
	s.resources[gke] = map[string]any{"name": "shared", "id": "gke-shared-uid"}
	s.resources[pool] = map[string]any{"name": "spark", "id": "gke-pool-uid"}
	r, _, _, request := dataprocReviewed(t, s)
	if len(request.LifecycleImpacts) != 1 || len(array(request.Asset.Normalized[referenceKey(clusterType)])) != 1 || len(array(request.Asset.Normalized[referenceKey(nodePoolType)])) != 1 {
		t.Fatal("GKE dependencies mistaken for Dataproc-owned compute")
	}
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	phase, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	delete(s.resources, dpRoot)
	s.operation["done"] = true
	object(s.resources[dpJob]["status"])["state"] = "ERROR"
	wait, err := driver.Wait(context.Background(), request, phase)
	if err != nil || !wait.Done || s.resources[gke] == nil || s.resources[pool] == nil || len(s.mutations) != 1 {
		t.Fatalf("virtual cleanup changed GKE %+v %v %v", wait, err, s.mutations)
	}
}

func TestDataprocDeletedVMDoesNotHideRemainingDisk(t *testing.T) {
	s := newDataprocScenario(t)
	r, _, _, request := dataprocReviewed(t, s)
	delete(s.resources, dpVM)
	s.resources["projects/sample-project/zones/us-central1-a/disks/analytics-m-boot"]["users"] = []any{}
	s.resources[dpInput]["users"] = []any{}
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	phase, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	delete(s.resources, dpRoot)
	s.operation["done"] = true
	wait, err := driver.Wait(context.Background(), request, phase)
	if err != nil || wait.Done {
		t.Fatalf("orphan disk skipped %+v %v", wait, err)
	}
}

func TestDataprocOrphanVMAndDiskAreBothReviewed(t *testing.T) {
	s := newDataprocScenario(t)
	vm := roundTripDataformJSON(t, s.resources[dpWorker])
	vmID := strings.Replace(dpWorker, "analytics-w", "orphan-vm", 1)
	diskID := strings.Replace(vmID, "/instances/", "/disks/", 1)
	vm["id"] = "888"
	vm["name"] = "orphan-vm"
	vm["selfLink"] = "https://www.googleapis.com/compute/v1/" + vmID
	vm["disks"] = []any{map[string]any{"source": "https://www.googleapis.com/compute/v1/" + diskID, "deviceName": "boot", "boot": true, "autoDelete": true, "type": "PERSISTENT", "mode": "READ_WRITE"}}
	disk := roundTripDataformJSON(t, s.resources["projects/sample-project/zones/us-central1-a/disks/analytics-w-boot"])
	disk["id"] = "889"
	disk["name"] = "orphan-vm"
	disk["selfLink"] = "https://www.googleapis.com/compute/v1/" + diskID
	disk["users"] = []any{vm["selfLink"]}
	s.resources[vmID], s.resources[diskID] = vm, disk
	r, _, _, request := dataprocReviewed(t, s)
	found := 0
	for _, impact := range request.LifecycleImpacts {
		if strings.HasSuffix(impact.Asset.Identity.NativeID, "/orphan-vm") && impact.Delete {
			found++
		}
	}
	if found != 2 {
		t.Fatalf("orphan VM/disk missing %d", found)
	}
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if len(s.mutations) != 1 {
		t.Fatal("orphan created independent Compute deletion")
	}
}

func TestDataprocNodeGroupCursorBindsClusterIncarnation(t *testing.T) {
	for _, mode := range []string{"uuid", "secret", "node-id", "scope", "volatile"} {
		t.Run(mode, func(t *testing.T) {
			s := newDataprocScenario(t)
			r := protocolRuntime(t, s.transport(t))
			req := productRequest(r, dataprocNodeGroupType, "project")
			page, err := r.List(context.Background(), req)
			if err != nil || page.NextCursor == "" {
				t.Fatalf("parent cursor %+v %v", page, err)
			}
			req.Cursor = page.NextCursor
			switch mode {
			case "uuid":
				s.resources[dpRoot]["clusterUuid"] = "recreated"
			case "secret":
				object(object(s.resources[dpRoot]["config"])["softwareConfig"])["properties"] = map[string]any{"secret": "changed"}
			case "node-id":
				object(array(object(s.resources[dpRoot]["config"])["auxiliaryNodeGroups"])[0])["nodeGroupId"] = "other"
			case "scope":
				req.Scope.Kind = asset.ScopeRegion
				req.Scope.NativeID = "us-central1"
			case "volatile":
				object(s.resources[dpRoot]["status"])["detail"] = "new progress"
			}
			page, err = r.List(context.Background(), req)
			if mode == "volatile" {
				if err != nil || len(page.Items) != 1 {
					t.Fatalf("progress invalidated cursor %+v %v", page, err)
				}
			} else if err == nil {
				t.Fatal("cursor crossed parent identity or config")
			}
		})
	}
}

func TestDataprocNativeAliasesFallbackAndReservationReference(t *testing.T) {
	s := newDataprocScenario(t)
	entry := object(array(object(s.resources[dpRoot]["config"])["auxiliaryNodeGroups"])[0])
	delete(object(entry["nodeGroup"]), "name")
	for id, data := range s.resources {
		if strings.Contains(id, "/autoscalingPolicies/") || strings.Contains(id, "/workflowTemplates/") {
			data["name"] = strings.Replace(id, "/regions/", "/locations/", 1)
		}
	}
	object(object(s.resources[dpRoot]["config"])["gceClusterConfig"])["reservationAffinity"] = map[string]any{"consumeReservationType": "SPECIFIC_RESERVATION", "key": "compute.googleapis.com/reservation-name", "values": []any{"spark-capacity"}}
	r, assets, _, request := dataprocReviewed(t, s)
	if batchAsset(assets, dpNode).ID == "" {
		t.Fatal("native nodeGroupId fallback lost auxiliary group")
	}
	historical := batchAsset(assets, "projects/sample-project/regions/us-central1/jobs/historical")
	if len(array(historical.Normalized[referenceKey(dataprocClusterType)])) != 0 {
		t.Fatal("historical job falsely linked to same-name replacement cluster")
	}
	refs := array(request.Asset.Normalized[referenceKey("compute.googleapis.com/Reservation")])
	if len(refs) != 1 || text(refs[0]) != "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/reservations/spark-capacity" {
		t.Fatalf("native reservation dependency missing %v", refs)
	}
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
}
