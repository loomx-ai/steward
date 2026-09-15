package azure

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackMemberExecution(t *testing.T) {
	for _, mode := range []string{"complete", "pending", "failed", "delete_forbidden", "readback_forbidden", "recreated", "changed_job", "changed_member", "changed_receipt", "changed_choice", "root_changed", "missing_before_delete", "retained", "changed_result", "root_changes_during_readback"} {
		t.Run(mode, func(t *testing.T) {
			member := actionAsset(diskType, "disk")
			raw := nativeResource(diskType, "disk", "eastus", map[string]any{"uniqueId": "original-disk", "provisioningState": "Succeeded"})
			member.Normalized = map[string]any{"_arm_creation_generation": creationGeneration(raw)}
			_, req := stackDeletePlanFixture(t, false, contracts.ActionImpact{Asset: member, ControllerID: "stack", Delete: mode != "retained"})
			req.IdempotencyKey = "member-job"
			root := map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": []any{map[string]any{"id": member.Identity.NativeID, "status": "managed", "denyStatus": "none"}}}}
			kind, _ := findType(diskType)
			operation := apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.Compute/locations/eastus/operations/"+testTenant, kind.Version)
			calls, deletes, polls, ownReads := 0, 0, 0, 0
			deleted, readbackFault := false, false
			r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
				calls++
				if q.Method != "GET" && (q.Method != "DELETE" || !strings.EqualFold(q.URL.Path, member.Identity.NativeID)) {
					t.Fatal("member execution changed another resource", q.Method, q.URL)
				}
				if reply, handled := emptyMonitorIndexResponse(t, q); handled {
					return reply, nil
				}
				if reply, handled := emptyDiagnosticSourceIndexResponse(t, q); handled {
					return reply, nil
				}
				if q.URL.String() == operation {
					polls++
					state := "Succeeded"
					if mode == "pending" {
						state = "Running"
					}
					if mode == "failed" {
						state = "Failed"
					}
					if state == "Succeeded" {
						deleted = true
					}
					return jsonResponse(200, map[string]any{"status": state}, nil), nil
				}
				switch path := strings.ToLower(q.URL.Path); path {
				case req.Asset.Identity.NativeID:
					return jsonResponse(200, root, nil), nil
				case member.Identity.NativeID:
					if q.Method == "DELETE" {
						deletes++
						if q.Header.Get("x-ms-client-request-id") != azureRequestID(req.IdempotencyKey+":member:disk") {
							t.Fatal("member delete lost stable operation identity")
						}
						if mode == "delete_forbidden" {
							return jsonResponse(403, map[string]any{}, nil), nil
						}
						headers := http.Header{}
						headers.Set("Azure-AsyncOperation", operation)
						return jsonResponse(202, map[string]any{}, headers), nil
					}
					ownReads++
					if readbackFault {
						if mode == "root_changes_during_readback" {
							root["tags"] = map[string]any{"changed": true}
						}
						if mode == "readback_forbidden" {
							return jsonResponse(403, map[string]any{}, nil), nil
						}
						if mode == "recreated" {
							object(raw["properties"])["uniqueId"] = "replacement-disk"
							return jsonResponse(200, raw, nil), nil
						}
					}
					if deleted || mode == "missing_before_delete" {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					return jsonResponse(200, raw, nil), nil
				case "/subscriptions/" + testSubscription + "/resourcegroups/test":
					return jsonResponse(200, map[string]any{"id": path, "type": groupType}, nil), nil
				case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				default:
					t.Fatalf("unexpected member execution request %s", q.URL)
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
			out, err := r.deploymentStackExecuteMember(t.Context(), req, member.ID, nil)
			if mode == "missing_before_delete" {
				var call *contracts.ProviderCallError
				if !errors.As(err, &call) || call.Provider.Category != execution.ErrorDependencyViolation {
					t.Fatal("member absence could be mistaken for Stack absence", err)
				}
			}
			if mode == "delete_forbidden" || mode == "missing_before_delete" || mode == "retained" {
				if err == nil || out.Data != nil || out.Done || (mode != "delete_forbidden" && deletes != 0) {
					t.Fatal("invalid member deletion started", out, err, deletes)
				}
				return
			}
			if err != nil || out.Done || deletes != 1 || out.Data["phase"] != "wait" {
				t.Fatal("native Execute did not yield persisted progress", out, err, deletes)
			}
			id := member.ID
			before := calls
			switch mode {
			case "changed_job":
				req.IdempotencyKey = "another-job"
			case "changed_member":
				id = asset.AssetID("other")
			case "changed_receipt":
				out.Data["phase"] = "complete"
			case "changed_result":
				out.Data["result"] = contracts.ActionResult{ProviderOperationID: "https://other.invalid/operation"}
			case "changed_choice":
				req.LifecycleImpacts[0].Delete = false
			case "root_changed":
				root["tags"] = map[string]any{"changed": true}
			}
			for step := 0; step < 3; step++ {
				wire, err := json.Marshal(out.Data)
				if err != nil {
					t.Fatal(err)
				}
				var saved map[string]any
				if err := json.Unmarshal(wire, &saved); err != nil {
					t.Fatal(err)
				}
				requestWire, err := json.Marshal(req)
				if err != nil || json.Unmarshal(requestWire, &req) != nil {
					t.Fatal("could not persist member request", err)
				}
				out, err = r.deploymentStackExecuteMember(t.Context(), req, id, saved)
				if strings.HasPrefix(mode, "changed_") || mode == "root_changed" || mode == "failed" {
					if err == nil || out.Done || out.Data != nil || deletes != 1 {
						t.Fatal("invalid member execution resumed", out, err, deletes)
					}
					if strings.HasPrefix(mode, "changed_") && calls != before {
						t.Fatal("tampered checkpoint reached native HTTP")
					}
					return
				}
				if err != nil || deletes != 1 {
					t.Fatal("member execution repeated deletion", out, err, deletes)
				}
				if mode == "pending" {
					if out.Done || out.Data["phase"] != "wait" || polls != step+1 {
						t.Fatal("pending native operation completed", out, polls)
					}
					continue
				}
				if out.Done != (step > 0) || polls != 1 {
					t.Fatal("readback phase or completed resume lost", out, step, polls)
				}
			}
			if mode == "readback_forbidden" || mode == "recreated" || mode == "root_changes_during_readback" {
				readbackFault = true
				beforeReads := ownReads
				out, err = r.deploymentStackExecuteMember(t.Context(), req, member.ID, out.Data)
				if err == nil || out.Done || out.Data != nil || deletes != 1 || ownReads <= beforeReads {
					t.Fatal("completed checkpoint hid new member state", out, err, deletes, ownReads)
				}
			}
		})
	}
}

func TestDeploymentStackMemberExecutionPreservesProductPhases(t *testing.T) {
	live := attachmentResources()
	var req contracts.ActionRequest
	var root map[string]any
	writes := []string{}
	deleted := false
	r := protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		if reply, handled := emptyMonitorIndexResponse(t, q); handled {
			return reply, nil
		}
		if reply, handled := emptyDiagnosticSourceIndexResponse(t, q); handled {
			return reply, nil
		}
		if strings.EqualFold(q.URL.Path, req.Asset.Identity.NativeID) && q.Method == "GET" {
			return jsonResponse(200, root, nil), nil
		}
		if q.Method == "GET" && (strings.HasSuffix(strings.ToLower(q.URL.Path), "/locks") || strings.EqualFold(q.URL.Path, text(live["vm"]["id"])+"/extensions")) {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		if q.Method == "GET" && strings.HasSuffix(strings.ToLower(q.URL.Path), "/resourcegroups/test") {
			return jsonResponse(200, map[string]any{"id": q.URL.Path, "type": groupType}, nil), nil
		}
		for name, raw := range live {
			if !strings.EqualFold(q.URL.Path, text(raw["id"])) {
				continue
			}
			if q.Method == "GET" {
				if deleted && (name == "vm" || name == "nic") {
					return jsonResponse(404, map[string]any{}, nil), nil
				}
				return jsonResponse(200, raw, nil), nil
			}
			writes = append(writes, q.Method+" "+name)
			if q.Method == "DELETE" && name == "vm" {
				deleted = true
				delete(live["boot"], "managedBy")
				delete(live["data"], "managedBy")
				return jsonResponse(200, map[string]any{}, nil), nil
			}
			if q.Method != "PUT" && q.Method != "PATCH" || name != "vm" && name != "nic" {
				t.Fatal("unreviewed product mutation", q.Method, name)
			}
			var body map[string]any
			if err := json.NewDecoder(q.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			previous := object(raw["properties"])
			if q.Method == "PUT" {
				raw["properties"] = body["properties"]
				for _, key := range []string{"resourceGuid", "virtualMachine", "privateEndpoint", "hostedWorkloads"} {
					if value, found := previous[key]; found {
						object(raw["properties"])[key] = value
					}
				}
			} else {
				for key, value := range object(body["properties"]) {
					previous[key] = value
				}
			}
			raw["etag"] = "after-" + name
			return jsonResponse(200, raw, nil), nil
		}
		t.Fatalf("unexpected product phase request %s %s", q.Method, q.URL)
		return nil, nil
	})
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	req, root = stackAttachmentRequest(t, c)
	var saved map[string]any
	completed := false
	for step := 0; step < 8; step++ {
		// The full request binding treats impact order as immaterial; the native
		// product receipt must use the same deterministic member projection.
		slices.Reverse(req.LifecycleImpacts)
		out, err := r.deploymentStackExecuteMember(t.Context(), req, "vm", saved)
		if err != nil {
			t.Fatal("product phase failed", step, writes, err)
		}
		wire, err := json.Marshal(out.Data)
		if err != nil || json.Unmarshal(wire, &saved) != nil {
			t.Fatal("product receipt could not survive restart", err)
		}
		if out.Done {
			completed = true
			break
		}
	}
	if !completed || strings.Join(writes, ",") != "PUT nic,PATCH vm,DELETE vm" {
		t.Fatal("product phases skipped or repeated", completed, writes)
	}
	out, err := r.deploymentStackExecuteMember(t.Context(), req, "vm", saved)
	if err != nil || !out.Done || len(writes) != 3 {
		t.Fatal("completed product execution repeated a mutation", out, err, writes)
	}
	observed, err := r.deploymentStackObserveProgress(t.Context(), req, deploymentStackProgress{Executions: []map[string]any{out.Data}})
	if err == nil || observed.Completed != nil || observed.Members != nil || len(writes) != 3 {
		t.Fatal("VM completion silently certified its deleted NIC", observed, err, writes)
	}
}
