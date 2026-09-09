package gcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func batchRunnable(job map[string]any) map[string]any {
	group := object(array(job["taskGroups"])[0])
	task := object(group["taskSpec"])
	return object(array(task["runnables"])[0])
}

func TestBatchInventoryRejectsIncompleteForeignAndRecreatedResources(t *testing.T) {
	for _, mode := range []string{"list-403", "list-206", "list-error", "unreachable", "bad-list", "token-type", "token-loop", "duplicate-task", "foreign-project", "foreign-job", "foreign-group", "detail-404", "detail-403", "detail-206", "detail-other", "job-no-uid", "job-no-create-time", "job-invalid-create-time", "job-recreated", "missing-task-groups", "duplicate-task-groups", "foreign-task-group"} {
		t.Run(mode, func(t *testing.T) {
			s := newBatchScenario(t)
			if mode == "job-invalid-create-time" {
				s.resources[batchRoot]["createTime"] = "not-a-timestamp"
			}
			task := batchRoot + "/taskGroups/workers/tasks/0"
			jobReads := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				name := strings.TrimPrefix(req.URL.Path, "/v1/")
				if name == batchRoot {
					jobReads++
					data := roundTripDataformJSON(t, s.resources[name])
					switch mode {
					case "job-no-uid":
						delete(data, "uid")
					case "job-no-create-time":
						delete(data, "createTime")
					case "job-recreated":
						if jobReads > 1 {
							data["uid"] = "other-uid"
						}
					case "missing-task-groups":
						delete(data, "taskGroups")
					case "duplicate-task-groups":
						data["taskGroups"] = []any{array(data["taskGroups"])[0], array(data["taskGroups"])[0]}
					case "foreign-task-group":
						object(array(data["taskGroups"])[0])["name"] = batchOther + "/taskGroups/workers"
					default:
						return nil, false
					}
					return dataformResponse(req, 200, data), true
				}
				if name == task {
					switch mode {
					case "detail-404":
						return dataformResponse(req, 404, map[string]any{}), true
					case "detail-403":
						return dataformResponse(req, 403, map[string]any{}), true
					case "detail-206":
						return dataformResponse(req, 206, s.resources[task]), true
					case "detail-other":
						return dataformResponse(req, 200, s.resources[batchOther+"/taskGroups/workers/tasks/0"]), true
					}
				}
				if name != batchRoot+"/taskGroups/workers/tasks" {
					return nil, false
				}
				code := 200
				data := map[string]any{}
				switch mode {
				case "list-403":
					code = 403
				case "list-206":
					code = 206
				case "list-error":
					data["error"] = map[string]any{"code": 403, "message": "BATCH_PRIVATE_ERROR"}
				case "unreachable":
					data["unreachable"] = []any{"europe-west1"}
				case "bad-list":
					data["tasks"] = "invalid"
				case "token-type":
					data["nextPageToken"] = 3
				case "token-loop":
					data["nextPageToken"] = "stuck"
				case "duplicate-task":
					data["tasks"] = []any{s.resources[task], s.resources[task]}
				case "foreign-project", "foreign-job", "foreign-group":
					copy := roundTripDataformJSON(t, s.resources[task])
					name := task
					if mode == "foreign-project" {
						name = strings.Replace(name, "sample-project", "foreign", 1)
					} else if mode == "foreign-job" {
						name = strings.Replace(name, "/training/", "/sibling/", 1)
					} else {
						name = strings.Replace(name, "/workers/", "/foreign/", 1)
					}
					copy["name"] = name
					data["tasks"] = []any{copy}
				default:
					return nil, false
				}
				return dataformResponse(req, code, data), true
			}
			r := protocolRuntime(t, s.transport(t))
			request := productRequest(r, batchTaskType, "us-central1")
			failed := false
			for attempt := 0; attempt < 5; attempt++ {
				page, err := r.List(context.Background(), request)
				if err != nil {
					failed = true
					break
				}
				if page.Complete {
					break
				}
				request.Cursor = page.NextCursor
			}
			if !failed {
				t.Fatal("invalid native inventory accepted")
			}
			if len(s.mutations) != 0 {
				t.Fatal("inventory mutated Batch")
			}
		})
	}
}

func TestBatchCursorBindsJobConfigurationAndScan(t *testing.T) {
	for _, mode := range []string{"uid", "secret", "task-group", "connection", "scope", "kind", "negative", "oversized", "malformed", "volatile"} {
		t.Run(mode, func(t *testing.T) {
			s := newBatchScenario(t)
			s.emptyPage = true
			r := protocolRuntime(t, s.transport(t))
			request := productRequest(r, batchTaskType, "us-central1")
			page, err := r.List(context.Background(), request)
			if err != nil || page.NextCursor == "" {
				t.Fatalf("cursor start: %+v %v", page, err)
			}
			request.Cursor = page.NextCursor
			switch mode {
			case "uid":
				s.resources[batchRoot]["uid"] = "recreated"
			case "secret":
				object(object(batchRunnable(s.resources[batchRoot])["environment"])["variables"])["APP_VALUE"] = "BATCH_PRIVATE_CHANGED"
			case "task-group":
				object(array(s.resources[batchRoot]["taskGroups"])[0])["name"] = batchRoot + "/taskGroups/renamed"
			case "connection":
				original := r.credentials
				r.credentials = credentialFunc(func(ctx context.Context, _ asset.ConnectionID) (contracts.Credential, error) {
					return original.Resolve(ctx, "connection")
				})
				request.ConnectionID = "another-connection"
			case "scope":
				request.Scope.NativeID = "europe-west1"
			case "kind":
				kind := r.resourceKind(batchJobType)
				request.ResourceKind = &kind
			case "negative", "oversized":
				raw, _ := base64.RawURLEncoding.DecodeString(request.Cursor)
				var cursor productCursor
				_ = json.Unmarshal(raw, &cursor)
				cursor.Target = -1
				if mode == "oversized" {
					cursor.Target = 999
				}
				raw, _ = json.Marshal(cursor)
				request.Cursor = base64.RawURLEncoding.EncodeToString(raw)
			case "malformed":
				request.Cursor = "bad-cursor"
			case "volatile":
				s.resources[batchRoot]["updateTime"] = "2026-09-09T00:00:00Z"
				object(s.resources[batchRoot]["status"])["state"] = "SUCCEEDED"
			}
			page, err = r.List(context.Background(), request)
			if mode == "volatile" {
				if err != nil || len(page.Items) != 2 {
					t.Fatalf("progress invalidated immutable cursor %+v %v", page, err)
				}
			} else if err == nil {
				t.Fatal("cursor crossed its original binding")
			}
		})
	}
}

func TestBatchPreflightRejectsLiveChangesAndForgedPlan(t *testing.T) {
	for _, mode := range []string{"job-recreated", "job-proof-missing", "job-script-changed", "job-state-unknown", "job-protected", "vm-recreated", "vm-label-changed", "vm-node-missing", "vm-service-account", "vm-network", "vm-machine", "vm-protected", "disk-recreated", "disk-shared", "disk-label-removed", "disk-retained", "external-auto-delete", "new-task", "missing-task-impact", "missing-disk-impact", "retain-vm", "retain-disk", "foreign-controller", "foreign-connection", "foreign-partition", "foreign-kind", "duplicate-impact", "list-403", "list-206", "compute-partial", "compute-token-loop", "compute-error", "task-detail-403", "task-disappeared", "template-403"} {
		t.Run(mode, func(t *testing.T) {
			s, r, _, _, request := batchReviewed(t)
			impactIndex := func(name string) int {
				for i, v := range request.LifecycleImpacts {
					if strings.HasSuffix(v.Asset.Identity.NativeID, "/"+name) {
						return i
					}
				}
				t.Fatal("impact missing", name)
				return 0
			}
			switch mode {
			case "job-recreated":
				s.resources[batchRoot]["uid"] = "another-job"
			case "job-proof-missing":
				delete(request.Asset.Normalized, batchProof)
			case "job-script-changed":
				object(batchRunnable(s.resources[batchRoot])["script"])["text"] = "BATCH_PRIVATE_CHANGED"
			case "job-state-unknown":
				object(s.resources[batchRoot]["status"])["state"] = "SOMETHING_NEW"
			case "job-protected":
				object(s.resources[batchRoot]["labels"])["protected"] = "true"
			case "vm-recreated":
				s.resources[batchVM]["id"] = "new-id"
			case "vm-label-changed":
				object(s.resources[batchVM]["labels"])["batch-job-id"] = "somewhere-else"
			case "vm-node-missing":
				delete(object(s.resources[batchVM]["labels"]), "batch-node")
			case "vm-service-account":
				object(array(s.resources[batchVM]["serviceAccounts"])[0])["email"] = "another@sample-project.iam.gserviceaccount.com"
			case "vm-network":
				object(array(s.resources[batchVM]["networkInterfaces"])[0])["network"] = "projects/sample-project/global/networks/other"
			case "vm-machine":
				s.resources[batchVM]["machineType"] = "e2-highmem-8"
			case "vm-protected":
				s.resources[batchVM]["deletionProtection"] = true
			case "disk-recreated":
				s.resources[batchBoot]["id"] = "recreated"
			case "disk-shared":
				s.resources[batchBoot]["users"] = append(array(s.resources[batchBoot]["users"]), "https://www.googleapis.com/compute/v1/projects/sample-project/zones/europe-west1-b/instances/other")
			case "disk-label-removed":
				delete(object(s.resources[batchBoot]["labels"]), "batch-job-uid")
			case "disk-retained":
				object(array(s.resources[batchVM]["disks"])[0])["autoDelete"] = false
			case "external-auto-delete":
				object(array(s.resources[batchVM]["disks"])[2])["autoDelete"] = true
			case "new-task":
				s.resources[batchRoot+"/taskGroups/workers/tasks/2"] = map[string]any{"name": batchRoot + "/taskGroups/workers/tasks/2", "status": map[string]any{"state": "PENDING"}}
			case "missing-task-impact", "missing-disk-impact":
				name := "tasks/0"
				if mode == "missing-disk-impact" {
					name = "worker-boot"
				}
				i := impactIndex(name)
				request.LifecycleImpacts = append(request.LifecycleImpacts[:i], request.LifecycleImpacts[i+1:]...)
			case "retain-vm":
				request.LifecycleImpacts[impactIndex("worker-q91")].Delete = false
			case "retain-disk":
				request.LifecycleImpacts[impactIndex("worker-boot")].Delete = false
			case "foreign-controller":
				request.LifecycleImpacts[0].ControllerID = "unreviewed"
			case "foreign-connection":
				request.LifecycleImpacts[0].Asset.Identity.ConnectionID = "foreign"
			case "foreign-partition":
				request.LifecycleImpacts[0].Asset.Identity.Partition = "foreign"
			case "foreign-kind":
				request.LifecycleImpacts[0].Asset.Identity.NativeType = "compute.googleapis.com/Address"
			case "duplicate-impact":
				request.LifecycleImpacts = append(request.LifecycleImpacts, request.LifecycleImpacts[0])
			case "template-403":
				instance := object(array(object(s.resources[batchRoot]["allocationPolicy"])["instances"])[0])
				delete(instance, "policy")
				instance["instanceTemplate"] = "projects/sample-project/global/instanceTemplates/jobs"
				request.Asset.Normalized[batchProof] = batchConfiguration(s.resources[batchRoot])
				request.Asset.Normalized["allocationPolicy"] = safePayload(s.resources[batchRoot])["allocationPolicy"]
				for i := range request.LifecycleImpacts {
					if request.LifecycleImpacts[i].Asset.Identity.NativeType == batchTaskType {
						request.LifecycleImpacts[i].Asset.Normalized[batchParentProof] = request.Asset.Normalized[batchProof]
					}
				}
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				path := req.URL.Path
				if strings.Contains(path, "/instanceTemplates/") && mode == "template-403" {
					return dataformResponse(req, 403, map[string]any{}), true
				}
				if strings.HasSuffix(path, "/tasks/0") && (mode == "task-detail-403" || mode == "task-disappeared") {
					code := 403
					if mode == "task-disappeared" {
						code = 404
					}
					return dataformResponse(req, code, map[string]any{}), true
				}
				if !strings.Contains(path, "/aggregated/instances") {
					return nil, false
				}
				switch mode {
				case "list-403":
					return dataformResponse(req, 403, map[string]any{}), true
				case "list-206":
					return dataformResponse(req, 206, map[string]any{}), true
				case "compute-partial":
					return dataformResponse(req, 200, map[string]any{"items": map[string]any{"zones/europe-west1-b": map[string]any{"warning": map[string]any{"code": "UNREACHABLE"}}}}), true
				case "compute-token-loop":
					return dataformResponse(req, 200, map[string]any{"nextPageToken": "stuck"}), true
				case "compute-error":
					return dataformResponse(req, 200, map[string]any{"error": map[string]any{"code": 403}}), true
				}
				return nil, false
			}
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Execute(context.Background(), request)
			if err == nil || len(s.mutations) > 0 {
				t.Fatalf("unsafe Batch deletion: mutations=%v error=%v", s.mutations, err)
			}
		})
	}
}

func TestBatchOperationAndRecoveryValidation(t *testing.T) {
	for _, mode := range []string{"foreign-url", "foreign-region", "different-name", "foreign-target", "wrong-verb", "wrong-version", "old-operation", "missing-name", "invalid-done", "operation-error", "operation-403", "operation-429", "operation-404", "phase-uid", "phase-configuration", "phase-resource", "phase-operation", "retained-disk-missing", "retained-disk-recreated", "parent-recreated", "compute-list-403", "unreviewed-compute"} {
		t.Run(mode, func(t *testing.T) {
			s, r, _, _, request := batchReviewed(t)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			s.operation["done"] = true
			switch mode {
			case "foreign-url":
				result.ProviderOperationID = "https://foreign.example/v1/" + batchOperationName
				result.Data["operation"] = result.ProviderOperationID
			case "foreign-region":
				result.ProviderOperationID = strings.Replace(result.ProviderOperationID, "us-central1", "asia-east1", 1)
				result.Data["operation"] = result.ProviderOperationID
			case "different-name":
				s.operation["name"] = strings.Replace(batchOperationName, "delete-training", "delete-other", 1)
			case "foreign-target":
				object(s.operation["metadata"])["target"] = batchOther
			case "wrong-verb":
				object(s.operation["metadata"])["verb"] = "create"
			case "wrong-version":
				object(s.operation["metadata"])["apiVersion"] = "v2"
			case "old-operation":
				object(s.operation["metadata"])["createTime"] = "2020-01-01T00:00:00Z"
			case "missing-name":
				delete(s.operation, "name")
			case "invalid-done":
				s.operation["done"] = "true"
			case "operation-error":
				s.operation["error"] = map[string]any{"code": 9, "message": "BATCH_PRIVATE_FAILURE"}
			case "operation-404":
				s.operation = nil
			case "phase-uid":
				result.Data["uid"] = "another-job"
			case "phase-configuration":
				result.Data["configuration"] = "another-configuration"
			case "phase-resource":
				result.Data["resource"] = "//batch.googleapis.com/" + batchOther
			case "phase-operation":
				result.Data["operation"] = ""
			case "retained-disk-missing":
				delete(s.resources, batchRoot)
				delete(s.resources, batchInput)
			case "retained-disk-recreated":
				delete(s.resources, batchRoot)
				s.resources[batchInput]["id"] = "recreated"
			case "parent-recreated":
				s.resources[batchRoot]["uid"] = "recreated"
			case "compute-list-403":
				delete(s.resources, batchRoot)
			case "unreviewed-compute":
				delete(s.resources, batchRoot)
				for _, name := range []string{batchVM, batchBoot, batchOutput, batchOrphan, batchRoot + "/taskGroups/workers/tasks/0", batchRoot + "/taskGroups/workers/tasks/1"} {
					delete(s.resources, name)
				}
				disk := roundTripDataformJSON(t, s.resources[batchInput])
				disk["id"] = "9000"
				disk["selfLink"] = "https://www.googleapis.com/compute/v1/projects/sample-project/zones/europe-west1-b/disks/late"
				disk["labels"] = map[string]any{"batch-job-uid": "training-uid-a91"}
				s.resources["projects/sample-project/zones/europe-west1-b/disks/late"] = disk
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.Contains(req.URL.Path, "/operations/") && (mode == "operation-403" || mode == "operation-429") {
					code := 403
					if mode == "operation-429" {
						code = 429
					}
					response := dataformResponse(req, code, map[string]any{})
					response.Header.Set("Retry-After", "7")
					return response, true
				}
				if strings.Contains(req.URL.Path, "/aggregated/") && mode == "compute-list-403" {
					return dataformResponse(req, 403, map[string]any{}), true
				}
				return nil, false
			}
			wait, err := driver.Wait(context.Background(), request, result)
			if mode == "operation-404" || mode == "unreviewed-compute" {
				if err != nil || wait.Done {
					t.Fatalf("native absence was not independently checked %+v %v", wait, err)
				}
			} else if err == nil || wait.Done {
				t.Fatalf("invalid Batch waiter accepted %+v %v", wait, err)
			}
			if mode == "retained-disk-missing" {
				var call *contracts.ProviderCallError
				if !errors.As(err, &call) || call.Provider.Category == execution.ErrorNotFound {
					t.Fatalf("retained disk 404 could complete parent: %v", err)
				}
			}
			if mode == "operation-429" {
				var call *contracts.ProviderCallError
				if !errors.As(err, &call) || call.Provider.Category != execution.ErrorThrottled || call.RetryAfter != 7*time.Second || call.Provider.RequestID == "" {
					t.Fatalf("rate limit lost retry or request metadata: %v", err)
				}
			}
			if len(s.mutations) != 1 {
				t.Fatal("resumed waiter repeated deletion")
			}
		})
	}
}

func TestBatchInvokeAndLogsKeepCodeAndEnvironmentPrivate(t *testing.T) {
	s := newBatchScenario(t)
	r := protocolRuntime(t, s.transport(t))
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	result, err := r.Invoke(ctx, contracts.Invocation{ConnectionID: "connection", Operation: "batch.projects.locations.jobs.get", Parameters: map[string]any{"name": batchRoot}})
	if err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal([]any{result, logs})
	if strings.Contains(string(encoded), "BATCH_PRIVATE_") {
		t.Fatalf("Batch secrets escaped provider: %s", encoded)
	}
	if len(logs) == 0 || result.RequestID == "" {
		t.Fatal("native provenance lost during redaction")
	}
}
