package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestDeploymentStackOutcomeOwnReads(t *testing.T) {
	for _, mode := range []string{"deleted", "remaining", "retained", "parent_present", "retained_missing", "retained_recreated", "retained_no_birth", "member_forbidden", "member_wrong_identity", "member_async", "parent_forbidden", "parent_recreated", "parent_recreated_during_read", "parent_disappears_during_read", "tampered_plan"} {
		t.Run(mode, func(t *testing.T) {
			c, req := stackDeletePlanFixture(t, false)
			member := &req.LifecycleImpacts[0]
			raw := map[string]any{"id": member.Asset.Identity.NativeID, "type": member.Asset.Identity.NativeType, "properties": map[string]any{"vmId": "original-vm"}}
			member.Asset.Normalized = map[string]any{"_arm_creation_generation": creationGeneration(raw), "_arm_generation": "old-etag"}
			if strings.HasPrefix(mode, "retained") {
				member.Delete = false
			}
			if mode == "retained_no_birth" {
				delete(member.Asset.Normalized, "_arm_creation_generation")
			}
			if mode == "retained_recreated" {
				object(raw["properties"])["vmId"] = "replacement-vm"
			}
			if mode == "tampered_plan" {
				req.Asset.Normalized[deploymentStackProofKey] = "forged"
			}
			// Resume from JSON must keep both the frozen review and creation identity.
			wire, _ := json.Marshal(req)
			if err := json.Unmarshal(wire, &req); err != nil {
				t.Fatal(err)
			}
			calls, roots := 0, 0
			c.http.Transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
				calls++
				if q.Method != "GET" {
					t.Fatalf("unexpected mutation: %s", q.Method)
				}
				if strings.EqualFold(q.URL.Path, req.Asset.Identity.NativeID) {
					roots++
					if mode == "parent_forbidden" {
						return jsonResponse(403, map[string]any{}, nil), nil
					}
					if mode == "parent_present" || mode == "parent_recreated" || mode == "parent_recreated_during_read" && roots == 2 || mode == "parent_disappears_during_read" && roots == 1 {
						created := "2020-02-01T01:01:01.1075056Z"
						if strings.Contains(mode, "recreated") {
							created = "2026-09-15T01:00:00Z"
						}
						return jsonResponse(200, map[string]any{"id": req.Asset.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": created}, "properties": map[string]any{"provisioningState": "deleting"}}, nil), nil
					}
					return jsonResponse(404, map[string]any{}, nil), nil
				}
				if !strings.EqualFold(q.URL.Path, req.LifecycleImpacts[0].Asset.Identity.NativeID) {
					t.Fatalf("unexpected endpoint: %s", q.URL.Path)
				}
				if mode == "deleted" || mode == "retained_missing" {
					return jsonResponse(404, map[string]any{}, nil), nil
				}
				if mode == "member_forbidden" {
					return jsonResponse(403, map[string]any{}, nil), nil
				}
				if mode == "member_wrong_identity" {
					raw["id"] = text(raw["id"]) + "other"
				}
				res := jsonResponse(200, raw, nil)
				if mode == "member_async" {
					res.Header.Set("Azure-AsyncOperation", "https://management.azure.com/unexpected")
				}
				return res, nil
			})
			out, err := c.deploymentStackObserveOutcome(t.Context(), req)
			success := mode == "deleted" || mode == "remaining" || mode == "retained" || mode == "parent_present"
			if !success {
				if err == nil || out.MembersAbsent != nil || out.StackAbsent {
					t.Fatal("failed observation exposed partial success", out, err)
				}
				if mode == "tampered_plan" && calls != 0 {
					t.Fatal("tampered plan issued requests", calls)
				}
				return
			}
			if err != nil || calls != 3 || out.StackAbsent != (mode != "parent_present") || len(out.MembersAbsent) != 1 || out.MembersAbsent[member.Asset.ID] != (mode == "deleted") {
				t.Fatal(out, err, calls)
			}
		})
	}
}

func TestDeploymentStackOutcomeDoesNotHideLostRetainedMember(t *testing.T) {
	c, parent, vm := stackGraphAssets(t)
	group := asset.Asset{ID: "managed-group", Identity: vm.Identity}
	group.Identity.NativeID = c.root() + "/resourceGroups/managed"
	group.Identity.NativeType = groupType
	// A deleted group and retained VM in a different group use distinct native
	// categories, so this is a real representable mixed-consequence plan.
	c, req := stackDeletePlanFixture(t, false,
		contracts.ActionImpact{Asset: group, ControllerID: parent.ID, Delete: true},
		contracts.ActionImpact{Asset: vm, ControllerID: parent.ID, Delete: false})
	seenRetained := false
	c.http.Transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		if q.Method != "GET" {
			t.Fatal(q.Method)
		}
		if strings.EqualFold(q.URL.Path, group.Identity.NativeID) {
			return jsonResponse(200, map[string]any{"id": group.Identity.NativeID, "type": groupType}, nil), nil
		}
		if strings.EqualFold(q.URL.Path, vm.Identity.NativeID) {
			seenRetained = true
		}
		return jsonResponse(404, map[string]any{}, nil), nil
	})
	out, err := c.deploymentStackObserveOutcome(t.Context(), req)
	if err == nil || !seenRetained || out.MembersAbsent != nil {
		t.Fatal("remaining deletion hid retained member loss", out, err, seenRetained)
	}
}

func TestDeploymentStackOutcomeReadsNestedMembers(t *testing.T) {
	for _, retain := range []bool{false, true} {
		t.Run(map[bool]string{false: "delete", true: "retain"}[retain], func(t *testing.T) {
			c, nested, vm := stackGraphAssets(t)
			nested.ID = "nested-stack"
			nested.Identity.NativeID = c.root() + "/resourceGroups/nested/providers/Microsoft.Resources/deploymentStacks/child"
			review := object(nested.Normalized[deploymentStackReviewKey])
			nested.Normalized[deploymentStackProofKey] = c.deploymentStackProof(nested.Identity.NativeID, nested.Identity.ConnectionID, review)
			c, req := stackDeletePlanFixture(t, false,
				contracts.ActionImpact{Asset: nested, ControllerID: "stack", Delete: !retain},
				contracts.ActionImpact{Asset: vm, ControllerID: nested.ID, Delete: !retain})
			vmRaw := map[string]any{"id": vm.Identity.NativeID, "type": vmType, "properties": map[string]any{"vmId": "retained-vm"}}
			req.LifecycleImpacts[1].Asset.Normalized = map[string]any{"_arm_creation_generation": creationGeneration(vmRaw)}
			reads := map[string]int{}
			c.http.Transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
				if q.Method != "GET" {
					t.Fatal(q.Method)
				}
				reads[strings.ToLower(q.URL.Path)]++
				if retain && strings.EqualFold(q.URL.Path, nested.Identity.NativeID) {
					return jsonResponse(200, map[string]any{"id": nested.Identity.NativeID, "type": deploymentStackType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"deploymentId": "mutable-deployment"}}, nil), nil
				}
				if retain && strings.EqualFold(q.URL.Path, vm.Identity.NativeID) {
					return jsonResponse(200, vmRaw, nil), nil
				}
				return jsonResponse(404, map[string]any{}, nil), nil
			})
			out, err := c.deploymentStackObserveOutcome(t.Context(), req)
			if err != nil || !out.StackAbsent || len(out.MembersAbsent) != 2 || out.MembersAbsent[nested.ID] != !retain || out.MembersAbsent[vm.ID] != !retain || reads[strings.ToLower(nested.Identity.NativeID)] != 1 || reads[strings.ToLower(vm.Identity.NativeID)] != 1 {
				t.Fatal(out, err, reads)
			}
		})
	}
}

func TestDeploymentStackOutcomeRetainsGroupAndDeletesListedResource(t *testing.T) {
	c, initial := stackDeletePlanFixture(t, false)
	vm := initial.LifecycleImpacts[0]
	group := vm
	group.Asset.ID = "retained-group"
	group.Asset.Identity.NativeID = strings.Split(vm.Asset.Identity.NativeID, "/providers/")[0]
	group.Asset.Identity.NativeType = groupType
	group.Delete = false
	raw := map[string]any{"id": group.Asset.Identity.NativeID, "type": groupType, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01Z"}}
	group.Asset.Normalized = map[string]any{"_arm_creation_generation": creationGeneration(raw)}
	c, req := stackDeletePlanFixture(t, false, group, vm)
	req.LifecycleImpacts[1].ControllerID = group.Asset.ID
	seen := map[string]int{}
	c.http.Transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		if q.Method != "GET" {
			t.Fatal(q.Method)
		}
		seen[strings.ToLower(q.URL.Path)]++
		if strings.EqualFold(q.URL.Path, group.Asset.Identity.NativeID) {
			return jsonResponse(200, raw, nil), nil
		}
		return jsonResponse(404, map[string]any{}, nil), nil
	})
	out, err := c.deploymentStackObserveOutcome(t.Context(), req)
	if err != nil || !out.StackAbsent || len(out.MembersAbsent) != 2 || out.MembersAbsent[group.Asset.ID] || !out.MembersAbsent[vm.Asset.ID] || seen[strings.ToLower(group.Asset.Identity.NativeID)] != 1 || seen[strings.ToLower(vm.Asset.Identity.NativeID)] != 1 {
		t.Fatal(out, err, seen)
	}
}
