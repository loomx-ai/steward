package gcp

import (
	"context"
	"encoding/json"
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
	batchRoot          = "projects/sample-project/locations/us-central1/jobs/training"
	batchOther         = "projects/sample-project/locations/europe-west1/jobs/training"
	batchVM            = "projects/sample-project/zones/europe-west1-b/instances/worker-q91"
	batchBoot          = "projects/sample-project/zones/europe-west1-b/disks/worker-boot"
	batchOutput        = "projects/sample-project/zones/europe-west1-b/disks/output"
	batchInput         = "projects/sample-project/regions/europe-west1/disks/input-data"
	batchOrphan        = "projects/sample-project/zones/europe-west1-b/disks/orphan-output"
	batchOperationName = "projects/sample-project/locations/us-central1/operations/delete-training"
)

// The wire surface is literal and independent of the generated catalog and
// resource rules. The fixture models delayed native cleanup, not a real cloud.
type batchScenario struct {
	resources map[string]map[string]any
	operation map[string]any
	mutations []string
	emptyPage bool
	handle    func(*http.Request) (*http.Response, bool)
}

func newBatchScenario(t *testing.T) *batchScenario {
	t.Helper()
	raw, err := os.ReadFile("fixtures/batch/resources.json")
	if err != nil {
		t.Fatal(err)
	}
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		t.Fatal(err)
	}
	s := &batchScenario{resources: map[string]map[string]any{}, operation: map[string]any{"name": batchOperationName, "metadata": map[string]any{"target": batchRoot, "verb": "delete", "apiVersion": "v1", "createTime": "2026-09-09T00:00:00Z"}}}
	for _, item := range items {
		id := text(item["name"])
		if link := text(item["selfLink"]); link != "" {
			id = strings.TrimPrefix(link, "https://www.googleapis.com/compute/v1/")
		}
		s.resources[id] = item
	}
	return s
}

func (s *batchScenario) transport(t *testing.T) roundTripFunc {
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
		if req.URL.Host == "batch.googleapis.com" {
			if !strings.HasPrefix(req.URL.Path, "/v1/") {
				t.Fatalf("wrong Batch version %s", req.URL)
			}
			if req.Method == "DELETE" {
				if name != batchRoot || len(req.URL.Query()) != 1 || req.URL.Query().Get("requestId") == "" || req.Body != nil && req.ContentLength != 0 {
					t.Fatalf("wrong Batch DELETE %s", req.URL)
				}
				s.mutations = append(s.mutations, req.Method+" "+name+"?"+req.URL.RawQuery)
				object(s.resources[batchRoot]["status"])["state"] = "DELETION_IN_PROGRESS"
				return respond(200, s.operation)
			}
			if req.Method != "GET" {
				t.Fatalf("invented Batch method %s %s", req.Method, req.URL)
			}
			if name == batchOperationName {
				if s.operation == nil {
					return respond(404, map[string]any{})
				}
				return respond(200, s.operation)
			}
			if name == "projects/sample-project/locations" {
				return respond(200, map[string]any{"locations": []any{map[string]any{"name": "projects/sample-project/locations/us-central1", "locationId": "us-central1"}, map[string]any{"name": "projects/sample-project/locations/europe-west1", "locationId": "europe-west1"}}})
			}
			if name == "projects/sample-project/locations/us-central1/jobs" || name == "projects/sample-project/locations/europe-west1/jobs" || name == batchRoot+"/taskGroups/workers/tasks" || name == batchOther+"/taskGroups/workers/tasks" {
				if strings.HasSuffix(name, "/tasks") && s.resources[strings.Split(name, "/taskGroups/")[0]] == nil {
					return respond(404, map[string]any{})
				}
				if s.emptyPage && req.URL.Query().Get("pageToken") == "" {
					return respond(200, map[string]any{"nextPageToken": "batch-page-2"})
				}
				if token := req.URL.Query().Get("pageToken"); token != "" && token != "batch-page-2" {
					t.Fatalf("wrong Batch token %s", req.URL)
				}
				var items []any
				for id, data := range s.resources {
					if strings.HasPrefix(id, name+"/") && !strings.Contains(strings.TrimPrefix(id, name+"/"), "/") {
						items = append(items, data)
					}
				}
				sort.Slice(items, func(i, j int) bool { return text(object(items[i])["name"]) < text(object(items[j])["name"]) })
				return respond(200, map[string]any{last(name): items})
			}
		} else if req.URL.Host == "compute.googleapis.com" {
			if req.Method != "GET" {
				t.Fatalf("Batch cleanup issued a Compute mutation %s %s", req.Method, req.URL)
			}
			name = strings.TrimPrefix(req.URL.Path, "/compute/v1/")
			if strings.HasPrefix(name, "projects/sample-project/aggregated/") {
				collection := last(name)
				if collection != "instances" && collection != "disks" {
					t.Fatalf("unexpected Compute list %s", req.URL)
				}
				uid := ""
				if filter := req.URL.Query().Get("filter"); filter != "" {
					if req.URL.Query().Get("includeAllScopes") != "true" || req.URL.Query().Get("returnPartialSuccess") == "true" {
						t.Fatal("Batch compute scan omitted scopes or allowed incomplete results")
					}
					if !strings.HasPrefix(filter, "labels.batch-job-uid = \"") || !strings.HasSuffix(filter, "\"") {
						t.Fatalf("invented Batch compute filter %s", req.URL)
					}
					uid = strings.TrimSuffix(strings.TrimPrefix(filter, "labels.batch-job-uid = \""), "\"")
				}
				if s.emptyPage && req.URL.Query().Get("pageToken") == "" {
					return respond(200, map[string]any{"items": map[string]any{"zones/europe-west1-b": map[string]any{"warning": map[string]any{"code": "NO_RESULTS_ON_PAGE"}}}, "nextPageToken": "compute-page-2"})
				}
				if token := req.URL.Query().Get("pageToken"); token != "" && token != "compute-page-2" {
					t.Fatalf("wrong Compute token %s", req.URL)
				}
				groups := map[string]any{}
				for id, data := range s.resources {
					parts := strings.Split(id, "/")
					if len(parts) != 6 || parts[4] != collection || text(data["selfLink"]) == "" || uid != "" && batchUID(data) != uid {
						continue
					}
					key := parts[2] + "/" + parts[3]
					if groups[key] == nil {
						groups[key] = map[string]any{collection: []any{}}
					}
					group := object(groups[key])
					group[collection] = append(array(group[collection]), data)
				}
				return respond(200, map[string]any{"items": groups})
			}
			if name == "projects/sample-project/regions/europe-west1/disks" {
				return respond(200, map[string]any{"items": []any{s.resources[batchInput]}})
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

func batchAsset(assets []asset.Asset, name string) asset.Asset {
	for _, value := range assets {
		if strings.TrimPrefix(value.Identity.NativeID, "//"+strings.Split(value.Identity.NativeType, "/")[0]+"/") == name {
			return value
		}
	}
	return asset.Asset{}
}

func (s *batchScenario) inventory(t *testing.T, r *Runtime) []asset.Asset {
	t.Helper()
	var result []asset.Asset
	for _, target := range []struct{ kind, region string }{{batchJobType, "project"}, {batchTaskType, "project"}, {instanceType, "project"}, {"compute.googleapis.com/Disk", "project"}, {"compute.googleapis.com/RegionDisk", "europe-west1"}} {
		request := productRequest(r, target.kind, target.region)
		for attempt := 0; ; attempt++ {
			page, err := r.List(context.Background(), request)
			if err != nil || attempt > 20 {
				t.Fatalf("inventory %s: %+v %v", target.kind, page, err)
			}
			for _, item := range page.Items {
				capabilities := asset.CapabilitySet{}
				if item.Actionable != nil && *item.Actionable {
					capabilities = append(capabilities, asset.CapabilityActionable)
				}
				result = append(result, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: item.NativeType, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: capabilities, Location: item.Location})
			}
			if page.Complete {
				break
			}
			request.Cursor = page.NextCursor
		}
	}
	return result
}

func batchReviewed(t *testing.T) (*batchScenario, *Runtime, []asset.Asset, plan.Input, contracts.ActionRequest) {
	t.Helper()
	s := newBatchScenario(t)
	s.emptyPage = true
	return batchReviewScenario(t, s)
}

func batchReviewScenario(t *testing.T, s *batchScenario) (*batchScenario, *Runtime, []asset.Asset, plan.Input, contracts.ActionRequest) {
	t.Helper()
	r := protocolRuntime(t, s.transport(t))
	assets := s.inventory(t, r)
	service, err := r.ServiceLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := service.Contribute(context.Background(), "scope", assets)
	if err != nil || len(contribution.Unresolved) > 0 {
		t.Fatalf("service contribution: %+v %v", contribution, err)
	}
	compute, err := r.ComputeLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	attachments, err := compute.Contribute(context.Background(), "scope", assets)
	if err != nil || len(attachments.Unresolved) > 0 {
		t.Fatalf("compute contribution: %+v %v", attachments, err)
	}
	root := batchAsset(assets, batchRoot)
	input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{root.ID}, LifecycleBindings: append(contribution.Bindings, attachments.Bindings...), Relationships: append(contribution.Relationships, attachments.Relationships...)}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) > 0 {
		t.Fatalf("Batch plan: %+v %v", result, err)
	}
	if len(result.Steps) != 1 || len(result.ImpactItems) != 7 {
		t.Fatalf("unexpected Batch plan: %+v", result)
	}
	return s, r, assets, input, dataformRequest(t, result, assets, root)
}

func TestBatchInventoryPlanDeleteAndRestart(t *testing.T) {
	s, r, assets, input, request := batchReviewed(t)
	if len(assets) != 11 {
		t.Fatalf("wrong inventory count %d", len(assets))
	}
	raw, _ := json.Marshal(assets)
	if strings.Contains(string(raw), "BATCH_PRIVATE_") {
		t.Fatalf("Batch secrets escaped: %s", raw)
	}
	for _, ref := range []string{"refs_compute_googleapis_com_Network", "refs_compute_googleapis_com_Subnetwork", "refs_compute_googleapis_com_RegionDisk", "refs_iam_googleapis_com_ServiceAccount", "refs_pubsub_googleapis_com_Topic", "refs_secretmanager_googleapis_com_Secret", "refs_storage_googleapis_com_Bucket", "refs_cloudkms_googleapis_com_CryptoKey"} {
		if len(array(request.Asset.Normalized[ref])) != 1 {
			t.Fatalf("missing Batch dependency %s", ref)
		}
	}
	for _, name := range []string{batchRoot + "/taskGroups/workers/tasks/0", batchVM, batchBoot} {
		input.ResolvedAssetIDs = []asset.AssetID{batchAsset(assets, name).ID}
		result, err := plan.Solve(input)
		if err != nil || len(result.Blockers) == 0 || len(result.Steps) > 0 {
			t.Fatalf("standalone member silently selected Job: %+v %v", result, err)
		}
	}
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	check, err := driver.Preflight(context.Background(), request)
	if err != nil || !check.Allowed {
		t.Fatalf("preflight %+v %v", check, err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || result.ProviderOperationID == "" || len(s.mutations) != 1 {
		t.Fatalf("execute %+v %v %v", result, err, s.mutations)
	}
	if !strings.Contains(s.mutations[0], "requestId=") {
		t.Fatal("missing native idempotency token")
	}
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || wait.Done {
		t.Fatalf("unfinished native operation %+v %v", wait, err)
	}
	s.operation["done"] = true
	delete(s.resources, batchRoot)
	wait, err = driver.Wait(context.Background(), request, result)
	if err != nil || wait.Done {
		t.Fatalf("parent 404 completed with live effects %+v %v", wait, err)
	}
	for _, name := range []string{batchRoot + "/taskGroups/workers/tasks/0", batchRoot + "/taskGroups/workers/tasks/1", batchVM, batchBoot, batchOutput} {
		delete(s.resources, name)
	}
	wait, err = driver.Wait(context.Background(), request, result)
	if err != nil || wait.Done {
		t.Fatalf("orphan disk ignored %+v %v", wait, err)
	}
	delete(s.resources, batchOrphan)
	// Real JSON persistence and a new provider runtime must retain every binding.
	encoded, _ := json.Marshal(request)
	var resumedRequest contracts.ActionRequest
	if json.Unmarshal(encoded, &resumedRequest) != nil {
		t.Fatal("request JSON")
	}
	encoded, _ = json.Marshal(result)
	var resumedResult contracts.ActionResult
	if json.Unmarshal(encoded, &resumedResult) != nil {
		t.Fatal("result JSON")
	}
	r = protocolRuntime(t, s.transport(t))
	driver, err = r.ResolveAction(context.Background(), "connection", resumedRequest.Asset)
	if err != nil {
		t.Fatal(err)
	}
	wait, err = driver.Wait(context.Background(), resumedRequest, resumedResult)
	if err != nil || !wait.Done {
		t.Fatalf("restart completion %+v %v", wait, err)
	}
	if s.resources[batchInput] == nil || s.resources[batchOther] == nil || s.resources[batchOther+"/taskGroups/workers/tasks/0"] == nil || len(s.mutations) != 1 {
		t.Fatal("external data or another job was deleted")
	}
	check, err = driver.Preflight(context.Background(), resumedRequest)
	if err != nil || !check.Absent {
		t.Fatalf("completed readback %+v %v", check, err)
	}
}

func TestBatchTemplatePreservesExistingDisk(t *testing.T) {
	s := newBatchScenario(t)
	template := "projects/sample-project/global/instanceTemplates/jobs"
	instance := object(array(object(s.resources[batchRoot]["allocationPolicy"])["instances"])[0])
	delete(instance, "policy")
	instance["instanceTemplate"] = "global/instanceTemplates/jobs"
	s.resources[template] = map[string]any{
		"name": "jobs", "id": "7001", "selfLink": "https://www.googleapis.com/compute/v1/" + template,
		"properties": map[string]any{"disks": []any{
			map[string]any{"boot": true, "autoDelete": true, "initializeParams": map[string]any{"sourceImage": "projects/debian-cloud/global/images/family/debian-12"}},
			map[string]any{"source": "https://www.googleapis.com/compute/v1/" + batchInput, "autoDelete": false, "mode": "READ_ONLY"},
		}},
	}
	templateReads := 0
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.URL.Path == "/compute/v1/"+template {
			templateReads++
		}
		return nil, false
	}
	_, r, _, _, request := batchReviewScenario(t, s)
	refs := array(request.Asset.Normalized["refs_compute_googleapis_com_InstanceTemplate"])
	if len(refs) != 1 || refs[0] != "//compute.googleapis.com/"+template {
		t.Fatalf("partial template reference lost: %v", refs)
	}
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || templateReads < 2 || len(s.mutations) != 1 {
		t.Fatalf("template cleanup: reads=%d mutations=%v error=%v", templateReads, s.mutations, err)
	}
	s.operation["done"] = true
	for _, name := range []string{batchRoot, batchRoot + "/taskGroups/workers/tasks/0", batchRoot + "/taskGroups/workers/tasks/1", batchVM, batchBoot, batchOutput, batchOrphan} {
		delete(s.resources, name)
	}
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || s.resources[template] == nil || s.resources[batchInput] == nil || len(s.mutations) != 1 {
		t.Fatalf("template or external disk was not preserved: %+v %v", wait, err)
	}
}

func TestBatchPlanRetentionProtectionAndDeduplication(t *testing.T) {
	_, _, assets, input, _ := batchReviewed(t)
	root := batchAsset(assets, batchRoot)
	for _, name := range []string{batchVM, batchBoot, batchOrphan, batchRoot + "/taskGroups/workers/tasks/0"} {
		copy := input
		copy.RequestOptions = map[asset.AssetID]map[string]any{root.ID: {"retain_resources": []any{string(batchAsset(assets, name).ID)}}}
		result, err := plan.Solve(copy)
		if err != nil || len(result.Blockers) == 0 {
			t.Fatalf("unsupported Batch retention %s: %+v %v", name, result, err)
		}
	}
	copy := input
	copy.Protections = []plan.ProtectionPolicy{{AssetID: batchAsset(assets, batchBoot).ID, Protected: true}}
	result, err := plan.Solve(copy)
	if err != nil || len(result.Blockers) == 0 {
		t.Fatalf("protected descendant not blocked %+v %v", result, err)
	}
	input.ResolvedAssetIDs = append(input.ResolvedAssetIDs, root.ID, batchAsset(assets, batchVM).ID, batchAsset(assets, batchBoot).ID)
	result, err = plan.Solve(input)
	if err != nil || len(result.Blockers) > 0 || len(result.Steps) != 1 || len(result.ImpactItems) != 7 {
		t.Fatalf("overlapping selection duplicated cleanup %+v %v", result, err)
	}
	for _, impact := range result.ImpactItems {
		if impact.AssetID == batchAsset(assets, batchInput).ID && impact.Expected != plan.ExpectedRetainShared {
			t.Fatalf("external disk lost retention %+v", impact)
		}
	}
}

func TestBatchAlreadyDeletingAndFinishedVMTransitions(t *testing.T) {
	for _, state := range []string{"DELETION_IN_PROGRESS", "SUCCEEDED"} {
		t.Run(state, func(t *testing.T) {
			s, r, _, _, request := batchReviewed(t)
			object(s.resources[batchRoot]["status"])["state"] = state
			delete(s.resources, batchVM)
			for _, name := range []string{batchBoot, batchOutput} {
				s.resources[name]["users"] = []any{}
			}
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if state == "DELETION_IN_PROGRESS" && (len(s.mutations) != 0 || result.ProviderOperationID != "" || result.Data["phase"] != "batch_delete") {
				t.Fatalf("native deletion was replayed %+v %v", result, s.mutations)
			}
			if state == "SUCCEEDED" && len(s.mutations) != 1 {
				t.Fatal("finished job did not delete")
			}
		})
	}
}

func TestBatchNativeTaskHasNoIndependentDelete(t *testing.T) {
	_, r, assets, _, _ := batchReviewed(t)
	value := batchAsset(assets, batchRoot+"/taskGroups/workers/tasks/0")
	if _, err := r.ResolveAction(context.Background(), "connection", value); err == nil {
		t.Fatal("invented native task deletion")
	}
}

func TestBatchContributionKeepsMissingInventoryAndDetectsLateMembers(t *testing.T) {
	for _, mode := range []string{"missing-task", "missing-vm", "foreign-connection", "foreign-partition", "duplicate", "stale-task", "stale-vm", "late-task", "new-vm"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, _, _ := batchReviewed(t)
			name := batchVM
			if mode == "missing-task" || mode == "stale-task" {
				name = batchRoot + "/taskGroups/workers/tasks/0"
			}
			for i := range assets {
				if assets[i].ID == batchAsset(assets, name).ID {
					switch mode {
					case "missing-task", "missing-vm":
						assets = append(assets[:i], assets[i+1:]...)
					case "foreign-connection":
						assets[i].Identity.ConnectionID = "foreign"
					case "foreign-partition":
						assets[i].Identity.Partition = "foreign"
					case "duplicate":
						assets = append(assets, assets[i])
					case "stale-task":
						delete(assets[i].Normalized, batchParentProof)
					case "stale-vm":
						assets[i].Normalized["id"] = "old-id"
					}
					break
				}
			}
			if mode == "new-vm" {
				vm := roundTripDataformJSON(t, s.resources[batchVM])
				id := strings.Replace(batchVM, "worker-q91", "worker-new", 1)
				vm["name"] = "worker-new"
				vm["id"] = "9000"
				vm["selfLink"] = "https://www.googleapis.com/compute/v1/" + id
				vm["disks"] = []any{}
				s.resources[id] = vm
			}
			late := false
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if mode == "late-task" && !late && strings.HasSuffix(req.URL.Path, "/instances/worker-q91") {
					late = true
					name := batchRoot + "/taskGroups/workers/tasks/2"
					s.resources[name] = map[string]any{"name": name, "status": map[string]any{"state": "PENDING"}}
				}
				return nil, false
			}
			contributor, err := r.ServiceLifecycle(context.Background(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			result, err := contributor.Contribute(context.Background(), "scope", assets)
			switch mode {
			case "missing-task", "missing-vm", "foreign-connection", "foreign-partition", "new-vm":
				if err != nil || len(result.Unresolved) != 1 {
					t.Fatalf("missing native member hidden: %d %v", len(result.Unresolved), err)
				}
			default:
				if err == nil {
					t.Fatal("changed or ambiguous native membership accepted")
				}
			}
			if len(s.mutations) != 0 {
				t.Fatal("contribution mutated Batch")
			}
		})
	}
}

func TestBatchOrphanVMRequiresNativeJobAbsence(t *testing.T) {
	for _, mode := range []string{"live-job", "orphan", "recreated-job", "permission-denied", "invalid-job", "incomplete-locations"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, _, _ := batchReviewed(t)
			vm := batchAsset(assets, batchVM)
			remaining := make([]asset.Asset, 0, len(assets))
			for _, value := range assets {
				if value.Identity.NativeType != batchJobType {
					remaining = append(remaining, value)
				}
			}
			contribution, err := NewInstanceDisks().Contribute(context.Background(), "scope", remaining)
			if err != nil {
				t.Fatal(err)
			}
			input := plan.Input{Assets: remaining, ResolvedAssetIDs: []asset.AssetID{vm.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
			planned, err := plan.Solve(input)
			if err != nil || len(planned.Blockers) > 0 {
				t.Fatalf("orphan plan: %+v %v", planned, err)
			}
			request := dataformRequest(t, planned, remaining, vm)
			if mode != "live-job" {
				delete(s.resources, batchRoot)
			}
			if mode == "recreated-job" {
				s.resources[batchRoot] = map[string]any{"name": batchRoot, "uid": "replacement"}
			}
			writes := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Path == "/v1/"+batchRoot {
					if mode == "permission-denied" {
						return dataformResponse(req, 403, map[string]any{}), true
					}
					if mode == "invalid-job" {
						return dataformResponse(req, 200, map[string]any{"name": batchRoot}), true
					}
				}
				if mode == "incomplete-locations" && req.URL.Path == "/v1/projects/sample-project/locations" {
					return dataformResponse(req, 200, map[string]any{"unreachable": []any{"us-central1"}}), true
				}
				if req.Method == "DELETE" {
					if req.URL.Host != "compute.googleapis.com" || req.URL.Path != "/compute/v1/"+batchVM {
						t.Fatalf("orphan cleanup expanded to another resource %s", req.URL)
					}
					writes++
					return dataformResponse(req, 200, map[string]any{"name": "delete-orphan"}), true
				}
				return nil, false
			}
			driver, err := r.ResolveAction(context.Background(), "connection", vm)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if mode == "orphan" || mode == "recreated-job" {
				if err != nil || writes != 1 || result.ProviderOperationID == "" {
					t.Fatalf("explicit orphan deletion rejected: %+v %d %v", result, writes, err)
				}
			} else if err == nil || writes != 0 {
				t.Fatalf("unproven orphan mutated: %d %v", writes, err)
			}
			if len(s.mutations) != 0 {
				t.Fatal("VM selection deleted a Batch job")
			}
		})
	}
}
