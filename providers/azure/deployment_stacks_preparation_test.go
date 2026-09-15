package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func stackAttachmentRequest(t *testing.T, c *client) (contracts.ActionRequest, map[string]any) {
	t.Helper()
	vm, _ := attachmentPlanRequest(t, "boot", "data", "ip")
	vm.Asset.Identity.Partition = "azure"
	choices := []contracts.ActionImpact{{Asset: vm.Asset, ControllerID: "stack", Delete: true}}
	for _, impact := range vm.LifecycleImpacts {
		impact.Asset.Identity.Partition = "azure"
		choices = append(choices, impact)
	}
	root := vm.Asset
	root.ID = "stack"
	root.Identity.NativeID = strings.ToLower(c.root() + "/providers/Microsoft.Resources/deploymentStacks/stack")
	root.Identity.NativeType = deploymentStackType
	raw := map[string]any{"id": root.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": []any{map[string]any{"id": vm.Asset.Identity.NativeID, "status": "managed", "denyStatus": "none"}}}}
	review, err := c.deploymentStackMemberReview(raw)
	if err != nil {
		t.Fatal(err)
	}
	root.Normalized = map[string]any{deploymentStackReviewKey: review, deploymentStackProofKey: c.deploymentStackProof(root.Identity.NativeID, root.Identity.ConnectionID, review)}
	return contracts.ActionRequest{Asset: root, Action: "delete", IdempotencyKey: "stack-delete-job", LifecycleImpacts: choices}, raw
}

func TestDeploymentStackMemberPreparationResume(t *testing.T) {
	for _, fault := range []string{"none", "job", "member", "receipt", "retention", "root_missing", "root_changed", "vm_missing", "async_pending", "async_failed", "nic_recreated", "nic_missing"} {
		t.Run(fault, func(t *testing.T) {
			c := directClient(nil)
			req, root := stackAttachmentRequest(t, c)
			live := attachmentResources()
			object(live["nic"]["properties"])["resourceGuid"] = "original-nic"
			for i := range req.LifecycleImpacts {
				if req.LifecycleImpacts[i].Asset.ID == "nic" {
					req.LifecycleImpacts[i].Asset.Normalized["_arm_creation_generation"] = creationGeneration(live["nic"])
				}
			}

			writes, reads := []string{}, 0
			activeFault := false
			operation := apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.Network/locations/eastus/operations/retain", "2024-05-01")
			c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method == "DELETE" {
					t.Fatal("preparation independently deleted a member")
				}
				if r.Method == "GET" {
					reads++
				}
				if r.URL.String() == operation {
					state := "Running"
					if fault == "async_failed" {
						state = "Failed"
					}
					return jsonResponse(200, map[string]any{"status": state}, nil), nil
				}
				if strings.EqualFold(r.URL.Path, req.Asset.Identity.NativeID) {
					if activeFault && fault == "root_missing" {
						return jsonResponse(404, map[string]any{}, nil), nil
					}
					if activeFault && fault == "root_changed" {
						object(root["systemData"])["createdAt"] = "2026-09-15T01:00:00Z"
					}
					return jsonResponse(200, root, nil), nil
				}
				if strings.HasSuffix(strings.ToLower(r.URL.Path), "/locks") || strings.EqualFold(r.URL.Path, text(live["vm"]["id"])+"/extensions") {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				if strings.HasSuffix(strings.ToLower(r.URL.Path), "/resourcegroups/test") {
					return jsonResponse(200, map[string]any{"id": r.URL.Path}, nil), nil
				}
				for name, raw := range live {
					if !strings.EqualFold(r.URL.Path, text(raw["id"])) {
						continue
					}
					if r.Method == "GET" {
						if activeFault && name == "nic" {
							if fault == "nic_missing" {
								return jsonResponse(404, map[string]any{}, nil), nil
							}
							if fault == "nic_recreated" {
								object(raw["properties"])["resourceGuid"] = "replacement-nic"
							}
						}
						if activeFault && fault == "vm_missing" && name == "vm" {
							return jsonResponse(404, map[string]any{}, nil), nil
						}
						return jsonResponse(200, raw, nil), nil
					}
					if name != "nic" && name != "vm" || r.Method != "PUT" && r.Method != "PATCH" {
						t.Fatal("unexpected mutation", name, r.Method)
					}
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Fatal(err)
					}
					if r.Method == "PUT" {
						guid := object(raw["properties"])["resourceGuid"]
						raw["properties"] = body["properties"]
						object(raw["properties"])["resourceGuid"] = guid
					} else {
						for key, value := range object(body["properties"]) {
							object(raw["properties"])[key] = value
						}
					}
					writes = append(writes, r.Method+" "+name)
					if r.Header.Get("x-ms-client-request-id") != azureRequestID(req.IdempotencyKey+":member:vm:retain:"+strings.ToLower(text(raw["id"]))) {
						t.Fatal("unstable member mutation idempotency key")
					}
					headers := http.Header{}
					if strings.HasPrefix(fault, "async_") {
						headers.Set("Azure-AsyncOperation", operation)
					}
					return jsonResponse(200, raw, headers), nil
				}
				t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
				return nil, nil
			})
			out, err := c.deploymentStackPrepareMember(t.Context(), req, "vm", nil)
			if err != nil || out.Done || len(writes) != 1 || writes[0] != "PUT nic" {
				t.Fatal(out, err, writes)
			}
			saved := out.Data
			id := asset.AssetID("vm")
			beforeReads := reads
			activeFault = true
			switch fault {
			case "job":
				req.IdempotencyKey = "other-job"
			case "member":
				id = "nic"
			case "receipt":
				saved["phase"] = "attachments_prepared"
			case "retention":
				for i := range req.LifecycleImpacts {
					if req.LifecycleImpacts[i].Asset.ID == "data" {
						req.LifecycleImpacts[i].Delete = true
					}
				}
			}
			for step := 0; step < 3; step++ {
				wire, err := json.Marshal(saved)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(wire, &saved); err != nil {
					t.Fatal(err)
				}
				out, err = c.deploymentStackPrepareMember(t.Context(), req, id, saved)
				if fault == "async_pending" {
					if err != nil || out.Done || out.Data == nil || len(writes) != 1 {
						t.Fatal("pending operation advanced preparation", out, err, writes)
					}
					saved = out.Data
					continue
				}
				if fault != "none" {
					if err == nil || out.Data != nil || len(writes) != 1 {
						t.Fatal("changed preparation continued", out, err, writes)
					}
					if (fault == "job" || fault == "member" || fault == "receipt" || fault == "retention") && reads != beforeReads {
						t.Fatal("invalid checkpoint reached HTTP")
					}
					return
				}
				if err != nil || out.Done != (step > 0) || len(writes) != 2 || writes[1] != "PATCH vm" {
					t.Fatal(step, out, err, writes)
				}
				saved = out.Data
			}
		})
	}
}

func TestDeploymentStackMemberRequestProjection(t *testing.T) {
	c := directClient(nil)
	req, raw := stackAttachmentRequest(t, c)
	other := actionAsset(diskType, "unrelated")
	other.Identity.Partition = "azure"
	req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: other, ControllerID: req.Asset.ID, Delete: true})
	props := object(raw["properties"])
	props["resources"] = append(array(props["resources"]), map[string]any{"id": other.Identity.NativeID, "status": "managed", "denyStatus": "none"})
	review, err := c.deploymentStackMemberReview(raw)
	if err != nil {
		t.Fatal(err)
	}
	req.Asset.Normalized[deploymentStackReviewKey] = review
	req.Asset.Normalized[deploymentStackProofKey] = c.deploymentStackProof(req.Asset.Identity.NativeID, req.Asset.Identity.ConnectionID, review)
	req.Parameters = map[string]any{"retain_resources": []string{"boot", "data", "ip"}}
	before, err := json.Marshal(req)
	if err != nil {
		t.Fatal(err)
	}
	vm, err := c.deploymentStackMemberRequest(req, "vm")
	if err != nil || len(vm.LifecycleImpacts) != 4 || vm.Asset.ID != "vm" || len(vm.Parameters) != 0 || vm.IdempotencyKey != req.IdempotencyKey+":member:vm" {
		t.Fatal(vm, err)
	}
	for _, impact := range vm.LifecycleImpacts {
		if impact.Asset.ID == other.ID || impact.Delete != (impact.Asset.ID == "nic") {
			t.Fatal("projection changed reviewed consequences", impact)
		}
	}
	nic, err := c.deploymentStackMemberRequest(req, "nic")
	if err != nil || len(nic.LifecycleImpacts) != 1 || nic.LifecycleImpacts[0].Asset.ID != "ip" || nic.LifecycleImpacts[0].Delete {
		t.Fatal(nic, err)
	}
	for _, id := range []asset.AssetID{"boot", "stack", "missing", "unrelated"} {
		if _, err := c.deploymentStackMemberRequest(req, id); err == nil {
			t.Fatal("invalid preparation target accepted", id)
		}
	}
	after, err := json.Marshal(req)
	if err != nil || string(before) != string(after) {
		t.Fatal("projection mutated the frozen Stack request")
	}
}
