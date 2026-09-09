package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentGroupSettlesAndRecoversWithoutRepeatingDeprovision(t *testing.T) {
	for _, mode := range []string{"settling", "already-deprovisioning", "already-deprovisioned", "expired-deprovision", "expired-delete", "physical-remains", "deployment-remains", "partial-operation-response"} {
		t.Run(mode, func(t *testing.T) {
			s := newDeploymentGroupScenario(t)
			if mode == "settling" {
				s.resources[groupTestName]["state"] = "UPDATING"
			}
			r, _, _, request := deploymentGroupReviewed(t, s, nil)
			if mode == "already-deprovisioning" || mode == "already-deprovisioned" {
				s.resources[groupTestName]["provisioningState"] = "DEPROVISIONING"
				s.requests = []map[string]any{{"deletePolicy": "DELETE"}} // An earlier caller's operation.
				if mode == "already-deprovisioned" {
					s.finishDeprovision(false)
				}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			initial := result.ProviderOperationID
			resume := func() contracts.WaitResult {
				t.Helper()
				encoded, _ := json.Marshal(result)
				var persisted contracts.ActionResult
				if err := json.Unmarshal(encoded, &persisted); err != nil {
					t.Fatal(err)
				}
				restarted := protocolRuntime(t, s.transport(t))
				driver, err := restarted.ResolveAction(context.Background(), "connection", request.Asset)
				if err != nil {
					t.Fatal(err)
				}
				wait, err := driver.Wait(context.Background(), request, persisted)
				if err != nil {
					t.Fatalf("group resume: %+v %v", wait, err)
				}
				if wait.Data != nil {
					result.Data = wait.Data
				}
				if result.ProviderOperationID != initial || result.Data["initial_operation"] != initial {
					t.Fatal("resume replaced the original operation identity")
				}
				return wait
			}
			if mode == "settling" {
				if result.Data["phase"] != "group_settle" || len(s.writes) != 0 || resume().Done {
					t.Fatal("updating group reached deprovision")
				}
				s.resources[groupTestName]["state"] = "ACTIVE"
				resume()
				if result.Data["phase"] != "group_deprovision" || len(s.writes) != 1 {
					t.Fatal("settled group did not start deprovision")
				}
			}
			if mode == "expired-deprovision" {
				delete(s.operations, strings.TrimPrefix(text(result.Data["operation"]), "https://config.googleapis.com/v1/"))
				if resume().Done || len(s.writes) != 1 {
					t.Fatal("expired operation hid live deployments")
				}
			}
			if mode != "already-deprovisioned" {
				live := s.resources[infraTestDeployment]
				s.finishDeprovision(mode == "physical-remains")
				if mode == "deployment-remains" {
					s.resources[infraTestDeployment] = live
				}
			}
			if mode == "partial-operation-response" {
				for _, op := range s.operations {
					response := object(op["response"])
					delete(response, "labels")
					delete(response, "annotations")
				}
			}
			if mode == "physical-remains" || mode == "deployment-remains" {
				if resume().Done || len(s.writes) != 1 || result.Data["phase"] != "group_deprovision" {
					t.Fatal("group metadata deletion skipped surviving resources")
				}
				delete(s.physical, infraTestNetwork)
				delete(s.physical, infraTestBucket)
				delete(s.physical, groupTestRetiredBucket)
				delete(s.resources, infraTestDeployment)
			}
			wait := resume()
			if wait.Done || result.Data["phase"] != "group_delete" {
				t.Fatalf("missing metadata-delete phase: %+v", wait)
			}
			if mode == "expired-delete" {
				delete(s.operations, strings.TrimPrefix(text(result.Data["operation"]), "https://config.googleapis.com/v1/"))
				if resume().Done {
					t.Fatal("expired DELETE operation hid live group metadata")
				}
			}
			s.finishDelete()
			if !resume().Done {
				t.Fatal("verified group absence did not complete")
			}
			posts, deletes := 0, 0
			for _, call := range s.writes {
				if strings.HasSuffix(call, ":deprovision") {
					posts++
				} else {
					deletes++
				}
			}
			wantPosts := 1
			if strings.HasPrefix(mode, "already-") {
				wantPosts = 0
			}
			if posts != wantPosts || deletes != 1 {
				t.Fatalf("repeated native group mutations: %d deprovision, %d delete", posts, deletes)
			}
		})
	}
}

func TestDeploymentGroupFailedOrChangedReadbackCannotDeleteMetadata(t *testing.T) {
	for _, mode := range []string{"failed-deprovision", "new-unit", "changed-dag", "changed-labels", "recreated-root", "unknown-state", "unknown-provisioning-state", "changed-old-revision", "unexpected-new-revision", "two-new-revisions", "new-revision-has-deployment", "new-revision-too-old", "group-disappears-with-live-deployments", "physical-read-denied", "revision-list-missing"} {
		t.Run(mode, func(t *testing.T) {
			s := newDeploymentGroupScenario(t)
			r, _, _, request := deploymentGroupReviewed(t, s, nil)
			driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "failed-deprovision" && mode != "group-disappears-with-live-deployments" {
				s.finishDeprovision(false)
			}
			root := s.resources[groupTestName]
			switch mode {
			case "failed-deprovision":
				root["provisioningState"] = "FAILED_TO_DEPROVISION"
				delete(s.operations, strings.TrimPrefix(result.ProviderOperationID, "https://config.googleapis.com/v1/"))
			case "new-unit":
				root["deploymentUnits"] = append(root["deploymentUnits"].([]any), map[string]any{"id": "new"})
			case "changed-dag":
				object(root["deploymentUnits"].([]any)[1])["dependencies"] = []any{}
			case "changed-labels":
				root["labels"] = map[string]any{"new": "true"}
			case "recreated-root":
				root["createTime"] = "2026-08-01T00:00:01Z"
			case "unknown-state":
				root["state"] = "UNKNOWN"
			case "unknown-provisioning-state":
				root["provisioningState"] = "UNKNOWN"
			case "changed-old-revision":
				s.resources[groupTestName+"/revisions/g-1"]["createTime"] = "2026-08-03T00:00:00Z"
			case "unexpected-new-revision":
				object(s.resources[groupTestName+"/revisions/g-4"]["snapshot"])["provisioningState"] = "PROVISIONED"
			case "two-new-revisions":
				copy := cloneParameters(s.resources[groupTestName+"/revisions/g-4"])
				copy["name"] = groupTestName + "/revisions/g-5"
				s.resources[text(copy["name"])] = copy
			case "new-revision-has-deployment":
				object(object(s.resources[groupTestName+"/revisions/g-4"]["snapshot"])["deploymentUnits"].([]any)[0])["deployment"] = infraTestDeployment
			case "new-revision-too-old":
				s.resources[groupTestName+"/revisions/g-4"]["createTime"] = "2026-08-02T00:00:00Z"
			case "group-disappears-with-live-deployments":
				s.finishDelete()
			case "physical-read-denied", "revision-list-missing":
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if mode == "physical-read-denied" && req.URL.Host == "storage.googleapis.com" {
						return dataformResponse(req, 403, map[string]any{}), true
					}
					if mode == "revision-list-missing" && req.URL.Path == "/v1/"+groupTestName+"/revisions" {
						return dataformResponse(req, 404, map[string]any{}), true
					}
					return nil, false
				}
			}
			wait, err := driver.Wait(context.Background(), request, result)
			if wait.Done || len(s.writes) != 1 || err == nil && mode != "group-disappears-with-live-deployments" {
				t.Fatalf("unsafe group completion: %+v %v writes=%v", wait, err, s.writes)
			}
		})
	}
}

func TestDeploymentGroupMutation404RequiresFullAbsence(t *testing.T) {
	for _, method := range []string{"POST", "DELETE"} {
		for _, absent := range []bool{false, true} {
			t.Run(method+map[bool]string{false: "/live", true: "/absent"}[absent], func(t *testing.T) {
				s := newDeploymentGroupScenario(t)
				r, _, _, request := deploymentGroupReviewed(t, s, nil)
				driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
				var result contracts.ActionResult
				if method == "DELETE" {
					var err error
					result, err = driver.Execute(context.Background(), request)
					if err != nil {
						t.Fatal(err)
					}
					s.finishDeprovision(false)
				}
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if req.Method != method || !strings.Contains(req.URL.Path, "/deploymentGroups/application") {
						return nil, false
					}
					if absent {
						if method == "POST" {
							s.requests = []map[string]any{{"deletePolicy": "DELETE"}}
							s.finishDeprovision(false)
						}
						s.finishDelete()
					}
					return dataformResponse(req, 404, map[string]any{}), true
				}
				var err error
				if method == "POST" {
					result, err = driver.Execute(context.Background(), request)
				} else {
					_, err = driver.Wait(context.Background(), request, result)
				}
				if (err == nil) != absent {
					t.Fatalf("mutation 404 absence=%t: %v", absent, err)
				}
				if absent {
					if read, err := driver.Readback(context.Background(), request); err != nil || read.Exists {
						t.Fatalf("404 completed without absence: %+v %v", read, err)
					}
				}
			})
		}
	}
}

func TestDeploymentGroupCanDisappearBetweenRevisionReads(t *testing.T) {
	s := newDeploymentGroupScenario(t)
	r, _, _, request := deploymentGroupReviewed(t, s, nil)
	driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
	result, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	s.finishDeprovision(false)
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || wait.Data["phase"] != "group_delete" {
		t.Fatalf("native metadata DELETE: %+v %v", wait, err)
	}
	result.Data = wait.Data
	reads := 0
	s.hook = func(req *http.Request) (*http.Response, bool) {
		if req.URL.Path == "/v1/"+groupTestName+"/revisions" {
			reads++
			if reads == 2 {
				s.finishDelete()
				return dataformResponse(req, 404, map[string]any{}), true
			}
		}
		return nil, false
	}
	if wait, err := driver.Wait(context.Background(), request, result); err != nil || wait.Done {
		t.Fatalf("normal group disappearance became a failed read: %+v %v", wait, err)
	}
	if wait, err := driver.Wait(context.Background(), request, result); err != nil || !wait.Done {
		t.Fatalf("next complete absence did not settle: %+v %v", wait, err)
	}
	if len(s.writes) != 2 {
		t.Fatal("normal deletion race repeated a mutation")
	}
}
