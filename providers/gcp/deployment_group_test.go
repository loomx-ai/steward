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
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	groupTestName          = infraTestParent + "/deploymentGroups/application"
	groupTestRetired       = "projects/sample-project/locations/europe-west1/deployments/stack-a"
	groupTestHistorical    = infraTestParent + "/deployments/historical-group-unit"
	groupTestRetiredBucket = "//storage.googleapis.com/infra-retired-data"
)

type deploymentGroupScenario struct {
	*infraScenario
	requests []map[string]any
	hook     func(*http.Request) (*http.Response, bool)
}

func newDeploymentGroupScenario(t *testing.T) *deploymentGroupScenario {
	t.Helper()
	s := &deploymentGroupScenario{infraScenario: newInfraScenario(t)}
	for name, target := range map[string]*map[string]map[string]any{"resources": &s.resources, "physical": &s.physical} {
		raw, err := os.ReadFile("fixtures/deployment-group/" + name + ".json")
		if err != nil || json.Unmarshal(raw, target) != nil {
			t.Fatalf("group fixture: %v", err)
		}
	}
	s.resources[groupTestHistorical] = map[string]any{"name": groupTestHistorical, "state": "ACTIVE", "createTime": "2026-01-01T00:00:00Z", "lockState": "UNLOCKED"}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if s.hook != nil {
			if response, handled := s.hook(req); handled {
				return response, true
			}
		}
		if req.URL.Host != "config.googleapis.com" || !strings.Contains(req.URL.Path, "/deploymentGroups") {
			return nil, false
		}
		respond := func(status int, data any) (*http.Response, bool) { return dataformResponse(req, status, data), true }
		name := strings.TrimPrefix(req.URL.Path, "/v1/")
		if req.Method == "GET" {
			if name == infraTestParent+"/deploymentGroups" || name == "projects/sample-project/locations/europe-west1/deploymentGroups" || name == groupTestName+"/revisions" {
				field := "deploymentGroups"
				if strings.HasSuffix(name, "/revisions") {
					field = "deploymentGroupRevisions"
					if s.resources[groupTestName] == nil {
						return respond(404, map[string]any{})
					}
				}
				if req.URL.Query().Get("filter") != "" || req.URL.Query().Get("orderBy") != "" {
					t.Fatal("group inventory filtered native membership")
				}
				if s.emptyPage && req.URL.Query().Get("pageToken") == "" {
					return respond(200, map[string]any{"nextPageToken": "group-next"})
				}
				if token := req.URL.Query().Get("pageToken"); token != "" && token != "group-next" {
					t.Fatal("group inventory did not follow native token")
				}
				var names []string
				for id := range s.resources {
					if strings.HasPrefix(id, name+"/") && !strings.Contains(strings.TrimPrefix(id, name+"/"), "/") {
						names = append(names, id)
					}
				}
				sort.Strings(names)
				rows := []any{}
				for _, id := range names {
					rows = append(rows, s.resources[id])
				}
				return respond(200, map[string]any{field: rows})
			}
			if row := s.resources[name]; row != nil {
				return respond(200, row)
			}
			return respond(404, map[string]any{})
		}
		if req.URL.RawQuery != "" && req.Method == "POST" {
			t.Fatal("deprovision has no native requestId or policy query")
		}
		if name != groupTestName && name != groupTestName+":deprovision" {
			t.Fatalf("unexpected group mutation: %s %s", req.Method, req.URL)
		}
		if s.resources[groupTestName] == nil {
			return respond(404, map[string]any{})
		}
		body, _ := io.ReadAll(req.Body)
		verb, suffix := "delete", "delete"
		if req.Method == "POST" && name == groupTestName+":deprovision" {
			var parameters map[string]any
			if json.Unmarshal(body, &parameters) != nil || len(parameters) != 2 || parameters["force"] != true || !slices.Contains([]string{"DELETE", "ABANDON"}, text(parameters["deletePolicy"])) {
				t.Fatalf("native deprovision body: %s", body)
			}
			s.requests = append(s.requests, parameters)
			verb, suffix = "update", "deprovision"
			s.resources[groupTestName]["provisioningState"] = "DEPROVISIONING"
		} else if req.Method == "DELETE" && name == groupTestName {
			q := req.URL.Query()
			if len(body) != 0 || len(q) != 3 || q.Get("force") != "true" || len(q.Get("requestId")) != 36 || q.Get("requestId") == "00000000-0000-0000-0000-000000000000" || !slices.Contains([]string{"FAIL_IF_ANY_REFERENCES_EXIST", "IGNORE_DEPLOYMENT_REFERENCES"}, q.Get("deploymentReferencePolicy")) {
				t.Fatalf("native group DELETE: %s body=%s", req.URL, body)
			}
			if q.Get("deploymentReferencePolicy") == "FAIL_IF_ANY_REFERENCES_EXIST" && (s.resources[infraTestDeployment] != nil || s.resources[groupTestRetired] != nil) {
				return respond(409, map[string]any{"error": map[string]any{"code": 409, "status": "FAILED_PRECONDITION"}})
			}
			s.requests = append(s.requests, map[string]any{"deploymentReferencePolicy": q.Get("deploymentReferencePolicy")})
			s.resources[groupTestName]["state"] = "DELETING"
		} else {
			t.Fatalf("unexpected group method: %s %s", req.Method, req.URL)
		}
		s.writes = append(s.writes, req.URL.String())
		op := infraTestParent + "/operations/group-" + suffix + "-" + fmt.Sprint(len(s.writes))
		metadata := map[string]any{"@type": "type.googleapis.com/google.cloud.config.v1.OperationMetadata", "target": groupTestName, "verb": verb, "apiVersion": "v1", "requestedCancellation": false}
		if suffix == "deprovision" {
			metadata["provisionDeploymentGroupMetadata"] = map[string]any{"step": "DEPROVISIONING_DEPLOYMENT_UNITS", "deploymentUnitProgresses": []any{map[string]any{"unitId": "current", "deployment": infraTestDeployment, "intent": "DELETE_DEPLOYMENT", "state": "DELETING_DEPLOYMENT"}, map[string]any{"unitId": "revisions/g-2/deploymentUnits/retired", "deployment": groupTestRetired, "intent": "CLEAN_UP", "state": "QUEUED"}}}
		}
		s.operations[op] = map[string]any{"name": op, "done": false, "metadata": metadata}
		return respond(200, s.operations[op])
	}
	return s
}

func (s *deploymentGroupScenario) finishDeprovision(keepPhysical bool) {
	for id := range s.resources {
		if id == infraTestDeployment || strings.HasPrefix(id, infraTestDeployment+"/") || id == groupTestRetired || strings.HasPrefix(id, groupTestRetired+"/") {
			delete(s.resources, id)
		}
	}
	if s.requests[0]["deletePolicy"] == "DELETE" && !keepPhysical {
		delete(s.physical, infraTestNetwork)
		delete(s.physical, infraTestBucket)
		delete(s.physical, groupTestRetiredBucket)
	}
	root := s.resources[groupTestName]
	for _, row := range root["deploymentUnits"].([]any) {
		delete(object(row), "deployment")
	}
	root["provisioningState"], root["updateTime"] = "DEPROVISIONED", "2026-09-01T00:00:00Z"
	encoded, _ := json.Marshal(root)
	var snapshot map[string]any
	_ = json.Unmarshal(encoded, &snapshot)
	s.resources[groupTestName+"/revisions/g-4"] = map[string]any{"name": groupTestName + "/revisions/g-4", "snapshot": snapshot, "createTime": "2026-09-01T00:00:00Z"}
	for _, operation := range s.operations {
		if object(operation["metadata"])["verb"] == "update" {
			operation["done"] = true
			response := cloneParameters(snapshot)
			response["@type"] = "type.googleapis.com/google.cloud.config.v1.DeploymentGroup"
			operation["response"] = response
			phase := object(object(operation["metadata"])["provisionDeploymentGroupMetadata"])
			phase["step"] = "SUCCEEDED"
			for _, row := range phase["deploymentUnitProgresses"].([]any) {
				object(row)["state"] = "SUCCEEDED"
			}
		}
	}
}

func (s *deploymentGroupScenario) finishDelete() {
	for id := range s.resources {
		if id == groupTestName || strings.HasPrefix(id, groupTestName+"/") {
			delete(s.resources, id)
		}
	}
	for _, operation := range s.operations {
		if object(operation["metadata"])["verb"] == "delete" {
			operation["done"] = true
			operation["response"] = map[string]any{"@type": "type.googleapis.com/google.cloud.config.v1.DeploymentGroup", "name": groupTestName, "state": "DELETED"}
		}
	}
}

func (s *deploymentGroupScenario) inventory(t *testing.T, r *Runtime) []asset.Asset {
	values := s.infraScenario.inventory(t, r)
	for _, kind := range []string{infraGroup, infraGroupRevision} {
		request := productRequest(r, kind, "project")
		for count := 0; ; count++ {
			batch, err := r.List(context.Background(), request)
			if err != nil || count > 30 {
				t.Fatalf("group inventory: %+v %v", batch, err)
			}
			for _, item := range batch.Items {
				capabilities := asset.CapabilitySet{}
				if item.Actionable != nil && *item.Actionable {
					capabilities = append(capabilities, asset.CapabilityActionable)
				}
				values = append(values, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: kind, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: capabilities, Location: item.Location})
			}
			if batch.Complete {
				break
			}
			request.Cursor = batch.NextCursor
		}
	}
	return values
}

func deploymentGroupReviewed(t *testing.T, s *deploymentGroupScenario, options map[string]any) (*Runtime, []asset.Asset, plan.Input, contracts.ActionRequest) {
	t.Helper()
	r := protocolRuntime(t, s.transport(t))
	values := s.inventory(t, r)
	hook, err := r.ServiceLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := hook.Contribute(context.Background(), "scope", values)
	if err != nil || len(contribution.Unresolved) != 0 {
		t.Fatalf("group contribution: %+v %v", contribution, err)
	}
	root := batchAsset(values, groupTestName)
	input := plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{root.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships, RequestOptions: map[asset.AssetID]map[string]any{root.ID: options}}
	solved, err := plan.Solve(input)
	if err != nil || len(solved.Blockers) != 0 {
		t.Fatalf("group plan: %+v %v", solved, err)
	}
	request := dataformRequest(t, solved, values, root)
	request.Parameters, request.IdempotencyKey = options, "deployment-group-cleanup"
	return r, values, input, request
}

func TestDeploymentGroupNativeLifecycleAndRestart(t *testing.T) {
	for _, mode := range []string{"DELETE", "ABANDON", "DETACH"} {
		t.Run(mode, func(t *testing.T) {
			s := newDeploymentGroupScenario(t)
			s.emptyPage = true
			options := map[string]any{}
			if mode == "ABANDON" {
				options["retain_all_resources"] = true
			}
			if mode == "DETACH" {
				options["retain_resources"] = []string{"//config.googleapis.com/" + infraTestDeployment, "//config.googleapis.com/" + groupTestRetired}
			}
			r, values, input, request := deploymentGroupReviewed(t, s, options)
			if len(values) != 22 || len(request.LifecycleImpacts) != 15 || len(s.writes) != 0 {
				t.Fatalf("group review: assets=%d impacts=%d writes=%v", len(values), len(request.LifecycleImpacts), s.writes)
			}
			encoded, _ := json.Marshal(values)
			if strings.Contains(string(encoded), "GROUP_PRIVATE") || strings.Contains(string(encoded), "INFRA_PRIVATE") {
				t.Fatal("group inventory leaked private configuration")
			}
			for _, impact := range request.LifecycleImpacts {
				if impact.Asset.Identity.NativeID == "//config.googleapis.com/"+groupTestHistorical {
					t.Fatal("obsolete/failed revision claimed a historical deployment")
				}
			}
			// A deployment remains independently actionable while its group exists.
			input.ResolvedAssetIDs = []asset.AssetID{asset.AssetID("//config.googleapis.com/" + infraTestDeployment)}
			input.RequestOptions = nil
			direct, err := plan.Solve(input)
			if err != nil || len(direct.Blockers) != 0 || len(direct.Steps) != 1 || direct.Steps[0].AssetID != input.ResolvedAssetIDs[0] {
				t.Fatalf("independent deployment fallback: %+v %v", direct, err)
			}
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || result.ProviderOperationID == "" || len(s.writes) != 1 {
				t.Fatalf("native group start: %+v %v", result, err)
			}
			initial := result.ProviderOperationID
			encoded, _ = json.Marshal(result)
			var restored contracts.ActionResult
			_ = json.Unmarshal(encoded, &restored)
			restarted := protocolRuntime(t, s.transport(t))
			driver, err = restarted.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if wait, err := driver.Wait(context.Background(), request, restored); err != nil || wait.Done || len(s.writes) != 1 {
				t.Fatalf("pending native group operation: %+v %v", wait, err)
			}
			if mode != "DETACH" {
				if s.requests[0]["deletePolicy"] != mode {
					t.Fatal("native group physical policy changed")
				}
				s.finishDeprovision(false)
				wait, err := driver.Wait(context.Background(), request, restored)
				if err != nil || wait.Done || wait.Data["phase"] != "group_delete" || len(s.writes) != 2 || wait.Data["initial_operation"] != initial || wait.Data["operation"] == initial {
					t.Fatalf("deprovision-to-delete transition: %+v %v", wait, err)
				}
				restored.Data = wait.Data // The executor preserves the original top-level operation.
				encoded, _ = json.Marshal(restored)
				_ = json.Unmarshal(encoded, &result)
				restored = result
			}
			s.finishDelete()
			if wait, err := driver.Wait(context.Background(), request, restored); err != nil || !wait.Done {
				t.Fatalf("native group completion: %+v %v", wait, err)
			}
			if read, err := driver.Readback(context.Background(), request); err != nil || read.Exists {
				t.Fatalf("native group final readback: %+v %v", read, err)
			}
			if (s.physical[infraTestNetwork] != nil) != (mode != "DELETE") || (s.physical[groupTestRetiredBucket] != nil) != (mode != "DELETE") || (s.resources[infraTestDeployment] != nil) != (mode == "DETACH") || s.resources[groupTestHistorical] == nil || s.physical["//storage.googleapis.com/infra-source"] == nil {
				t.Fatal("group cleanup did not preserve the reviewed resource boundary")
			}
		})
	}
}
