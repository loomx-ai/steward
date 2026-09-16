package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackStartDelete(t *testing.T) {
	for _, groupScoped := range []bool{false, true} {
		scopeName := "subscription"
		if groupScoped {
			scopeName = "resource_group"
		}
		t.Run(scopeName, func(t *testing.T) {
			for _, mode := range []string{"complete", "async", "forbidden", "bad_response", "root_lock", "parent_lock", "protected", "region_changed", "missing_location", "setup_missing", "setup_tampered", "setup_incomplete", "setup_active", "changed_job", "returned_child"} {
				t.Run(mode, func(t *testing.T) {
					parent, child := actionAsset(hostGroupType, "parent"), actionAsset(hostType, "child")
					child.Identity.NativeID = parent.Identity.NativeID + "/hosts/child"
					_, req := stackDeletePlanFixture(t, groupScoped, contracts.ActionImpact{Asset: parent, ControllerID: "stack", Delete: true}, contracts.ActionImpact{Asset: child, ControllerID: parent.ID, Delete: true})
					req.IdempotencyKey = "stack-start-job"
					req.Asset.Location = "eastus"
					groupID := "/subscriptions/" + testSubscription + "/resourcegroups/test"
					if groupScoped {
						req.Asset.Identity.NativeID = groupID + "/providers/microsoft.resources/deploymentstacks/stack"
					}
					root := map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "location": "eastus", "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": []any{map[string]any{"id": parent.Identity.NativeID, "status": "managed", "denyStatus": "none"}}}}
					groupRaw := map[string]any{"id": groupID, "type": groupType, "location": "eastus"}
					if groupScoped {
						delete(root, "location")
					}
					if mode == "region_changed" {
						if groupScoped {
							groupRaw["location"] = "westus"
						} else {
							root["location"] = "westus"
						}
					}
					if mode == "missing_location" {
						delete(root, "location")
						delete(groupRaw, "location")
					}
					if mode == "protected" {
						root["tags"] = map[string]any{"steward/protected": "true"}
					}
					parentRaw := map[string]any{"id": parent.Identity.NativeID, "type": hostGroupType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
					childRaw := map[string]any{"id": child.Identity.NativeID, "type": hostType, "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
					_, endpoint, _ := stackPollFixture()
					endpoint += "&unmanageAction.ResourcesWithoutDeleteSupport=fail"
					starting, childGone, rootGone, parentGone := false, false, false, false
					calls, stackDeletes, childDeletes, polls := 0, 0, 0, 0
					r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
						calls++
						if reply, handled := emptyMonitorIndexResponse(t, q); handled {
							return reply, nil
						}
						if reply, handled := emptyDiagnosticSourceIndexResponse(t, q); handled {
							return reply, nil
						}
						if q.URL.String() == endpoint {
							polls++
							status := "deletingResources"
							if polls >= 2 {
								status = "succeeded"
								rootGone, parentGone = true, true
							}
							return jsonResponse(200, map[string]any{"id": q.URL.Path, "name": testTenant, "status": status}, nil), nil
						}
						switch path := strings.ToLower(q.URL.Path); path {
						case req.Asset.Identity.NativeID:
							if q.Method == "DELETE" {
								stackDeletes++
								if !starting || childDeletes != 1 || !childGone {
									t.Fatal("Stack DELETE preceded verified setup")
								}
								params := q.URL.Query()
								if len(params) != 6 || params.Get("api-version") != deploymentStackVersion || params.Get("unmanageAction.Resources") != "delete" || params.Get("unmanageAction.ResourceGroups") != "detach" || params.Get("unmanageAction.ManagementGroups") != "detach" || params.Get("unmanageAction.ResourcesWithoutDeleteSupport") != "fail" || params.Get("bypassStackOutOfSyncError") != "false" {
									t.Fatal("native consequences changed", q.URL)
								}
								if q.Header.Get("x-ms-client-request-id") != azureRequestID(req.IdempotencyKey+":stack-delete") {
									t.Fatal("native operation lost stable request ID")
								}
								if q.Body != nil {
									body, _ := io.ReadAll(q.Body)
									if len(body) != 0 {
										t.Fatal("unexpected native DELETE body")
									}
								}
								if mode == "forbidden" {
									return jsonResponse(403, map[string]any{}, nil), nil
								}
								if mode == "bad_response" {
									return jsonResponse(202, map[string]any{}, nil), nil
								}
								headers := http.Header{}
								headers.Set("x-ms-request-id", "native-stack-request")
								if mode == "async" {
									headers.Set("Azure-AsyncOperation", endpoint)
									return jsonResponse(202, map[string]any{}, headers), nil
								}
								rootGone, parentGone = true, true
								return &http.Response{StatusCode: 204, Header: headers, Body: io.NopCloser(strings.NewReader(""))}, nil
							}
							if rootGone {
								return jsonResponse(404, map[string]any{}, nil), nil
							}
							return jsonResponse(200, root, nil), nil
						case parent.Identity.NativeID:
							if q.Method != "GET" {
								t.Fatal("non-prerequisite parent deleted independently")
							}
							if parentGone {
								return jsonResponse(404, map[string]any{}, nil), nil
							}
							return jsonResponse(200, parentRaw, nil), nil
						case child.Identity.NativeID:
							if q.Method == "DELETE" {
								childDeletes++
								childGone = true
								return jsonResponse(200, map[string]any{}, nil), nil
							}
							if childGone {
								return jsonResponse(404, map[string]any{}, nil), nil
							}
							return jsonResponse(200, childRaw, nil), nil
						case parent.Identity.NativeID + "/hosts":
							rows := []any{}
							if !childGone {
								rows = append(rows, childRaw)
							}
							return jsonResponse(200, map[string]any{"value": rows}, nil), nil
						case groupID:
							return jsonResponse(200, groupRaw, nil), nil
						case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
							rows := []any{}
							if starting && (mode == "root_lock" || mode == "parent_lock") {
								id := req.Asset.Identity.NativeID
								if mode == "parent_lock" {
									id = parent.Identity.NativeID
								}
								rows = append(rows, map[string]any{"id": id + "/providers/Microsoft.Authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}})
							}
							return jsonResponse(200, map[string]any{"value": rows}, nil), nil
						default:
							t.Fatalf("unexpected native Stack start request %s %s", q.Method, q.URL)
							return nil, nil
						}
					})
					c, err := r.resolve(t.Context(), "connection")
					if err != nil {
						t.Fatal(err)
					}
					review, err := c.deploymentStackMemberReview(root)
					if err != nil {
						t.Fatal(err)
					}
					req.Asset.Normalized[deploymentStackReviewKey] = review
					req.Asset.Normalized[deploymentStackProofKey] = c.deploymentStackProof(req.Asset.Identity.NativeID, "connection", review)
					var setup map[string]any
					for step := 0; step < 10; step++ {
						out, err := r.deploymentStackAdvanceSetup(t.Context(), req, setup)
						if mode == "protected" {
							if err == nil || out.Data != nil || childDeletes != 0 || stackDeletes != 0 {
								t.Fatal("protected Stack allowed setup mutation", out, err)
							}
							return
						}
						if err != nil {
							t.Fatal("setup", step, err)
						}
						wire, err := json.Marshal(out.Data)
						if err != nil || json.Unmarshal(wire, &setup) != nil {
							t.Fatal(err)
						}
						state := object(setup["state"])
						if mode == "setup_incomplete" {
							break
						}
						if mode == "setup_active" && state["prerequisites"] != nil && object(object(state["prerequisites"])["state"])["active"] != nil {
							break
						}
						if out.Done {
							break
						}
					}
					switch mode {
					case "setup_missing":
						setup = nil
					case "setup_tampered":
						setup["binding"] = "changed"
					case "changed_job":
						req.IdempotencyKey = "other-job"
					case "returned_child":
						childGone = false
					}
					before, _ := json.Marshal(req)
					priorCalls := calls
					starting = true
					result, err := r.deploymentStackStartDelete(t.Context(), req, setup)
					if mode != "complete" && mode != "async" {
						expectedDeletes := 0
						if mode == "forbidden" || mode == "bad_response" {
							expectedDeletes = 1
						}
						if err == nil || result.Data != nil || stackDeletes != expectedDeletes {
							t.Fatal("invalid Stack deletion accepted", result, err, stackDeletes)
						}
						if (strings.HasPrefix(mode, "setup_") || mode == "changed_job") && calls != priorCalls {
							t.Fatal("invalid setup reached HTTP", calls, priorCalls)
						}
						return
					}
					if err != nil || stackDeletes != 1 || result.ProviderRequestID != "native-stack-request" || result.Data["region"] != "eastus" {
						t.Fatal("native Stack DELETE failed", result, err, stackDeletes)
					}
					for _, mutation := range []string{"binding", "setup", "execution", "region", "missing", "unknown", "job", "lost_prerequisites"} {
						t.Run("resume_"+mutation, func(t *testing.T) {
							wire, _ := json.Marshal(result.Data)
							var saved map[string]any
							if err := json.Unmarshal(wire, &saved); err != nil {
								t.Fatal(err)
							}
							changedReq := req
							switch mutation {
							case "binding":
								saved["binding"] = "changed"
							case "setup":
								object(saved["setup"])["binding"] = "changed"
							case "execution":
								object(saved["execution"])["binding"] = "changed"
							case "region":
								saved["region"] = "westus"
							case "missing":
								delete(saved, "execution")
							case "unknown":
								saved["unexpected"] = true
							case "job":
								changedReq.IdempotencyKey = "other-job"
							case "lost_prerequisites":
								delete(object(object(saved["setup"])["state"]), "prerequisites")
							}
							prior := calls
							out, err := r.deploymentStackResumeDeletion(t.Context(), changedReq, saved)
							if err == nil || out.Data != nil || out.Products.Products != nil || calls != prior {
								t.Fatal("invalid resume reached HTTP or returned partial evidence", out, err, calls, prior)
							}
						})
					}
					checkpoint := result.Data
					for attempt := 0; attempt < 3; attempt++ {
						wire, _ := json.Marshal(checkpoint)
						var saved map[string]any
						if err = json.Unmarshal(wire, &saved); err != nil {
							t.Fatal(err)
						}
						out, err := r.deploymentStackResumeDeletion(t.Context(), req, saved)
						if err != nil || out.Products.ProductsReconciled != (mode != "async" || attempt > 0) || stackDeletes != 1 || childDeletes != 1 {
							t.Fatal("persisted deletion did not reconcile without repeated deletion", out, err, attempt)
						}
						afterSaved, _ := json.Marshal(saved)
						if string(wire) != string(afterSaved) {
							t.Fatal("resume changed input checkpoint")
						}
						checkpoint = out.Data
						// Returned nested maps must not alias the saved checkpoint.
						object(saved["setup"])["binding"] = "changed after resume"
						object(saved["execution"])["binding"] = "changed after resume"
					}
					// A completed checkpoint still performs fresh product reads.
					childGone = false
					out, err := r.deploymentStackResumeDeletion(t.Context(), req, checkpoint)
					if err != nil {
						if out.Data != nil || out.Products.Products != nil {
							t.Fatal("partial failed outcome", out)
						}
					} else if out.Products.ProductsReconciled {
						t.Fatal("returned child hidden by completed checkpoint", out)
					}
					if stackDeletes != 1 || childDeletes != 1 {
						t.Fatal("resume mutated returned child")
					}
					childGone = true
					if mode == "async" && polls != 2 {
						t.Fatal("native polling phases lost", polls)
					}
					if _, err := r.deploymentStackStartDelete(t.Context(), req, setup); err == nil || stackDeletes != 1 {
						t.Fatal("absent Stack was submitted again", err, stackDeletes)
					}
					after, _ := json.Marshal(req)
					if string(before) != string(after) {
						t.Fatal("native submission changed frozen plan")
					}
				})
			}
		})
	}
}
