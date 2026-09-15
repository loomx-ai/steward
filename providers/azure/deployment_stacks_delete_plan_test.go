package azure

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func stackDeletePlanFixture(t *testing.T, groupScoped bool, choices ...contracts.ActionImpact) (*client, contracts.ActionRequest) {
	t.Helper()
	c, parent, member := stackGraphAssets(t)
	if groupScoped {
		parent.Identity.NativeID = strings.ToLower(c.root() + "/resourceGroups/controller/providers/Microsoft.Resources/deploymentStacks/stack")
	}
	if choices == nil {
		choices = []contracts.ActionImpact{{Asset: member, ControllerID: parent.ID, Delete: true}}
	}
	rows := []any{}
	for _, impact := range choices {
		if impact.ControllerID == parent.ID {
			rows = append(rows, map[string]any{"id": impact.Asset.Identity.NativeID, "status": "managed", "denyStatus": "denyDelete"})
		}
	}
	review, err := c.deploymentStackMemberReview(map[string]any{"id": parent.Identity.NativeID, "systemData": map[string]any{"createdAt": "2020-02-01T01:01:01.1075056Z"}, "properties": map[string]any{"resources": rows}})
	if err != nil {
		t.Fatal(err)
	}
	parent.Normalized[deploymentStackReviewKey] = review
	parent.Normalized[deploymentStackProofKey] = c.deploymentStackProof(parent.Identity.NativeID, parent.Identity.ConnectionID, review)
	return c, contracts.ActionRequest{Asset: parent, Action: "delete", LifecycleImpacts: choices}
}

func TestDeploymentStackDeletePlanNativeScopeAndReceipt(t *testing.T) {
	for _, rg := range []bool{false, true} {
		for _, retain := range []bool{false, true} {
			c, req := stackDeletePlanFixture(t, rg)
			req.LifecycleImpacts[0].Delete = !retain
			req.Parameters = map[string]any{"retain_all_resources": retain}
			wire, _ := json.Marshal(req)
			if err := json.Unmarshal(wire, &req); err != nil {
				t.Fatal(err)
			}
			bound, saved, err := c.deploymentStackDeletePlan(req)
			if err != nil {
				t.Fatal(err)
			}
			u, err := url.Parse(bound.URL)
			mode := "delete"
			if retain {
				mode = "detach"
			}
			if err != nil || bound.Method != "DELETE" || !strings.EqualFold(u.Path, req.Asset.Identity.NativeID) || u.Host != "management.azure.com" || len(bound.Body) != 0 || len(u.Query()) != 6 || u.Query().Get("unmanageAction.Resources") != mode || u.Query().Get("unmanageAction.ResourceGroups") != "detach" || u.Query().Get("unmanageAction.ManagementGroups") != "detach" || u.Query().Get("bypassStackOutOfSyncError") != "false" || u.Query().Get("unmanageAction.ResourcesWithoutDeleteSupport") != "fail" {
				t.Fatal(bound, err)
			}
			for key, value := range saved {
				if u.Query().Get(key) != value {
					t.Fatal("receipt differs from DELETE", key)
				}
			}
			receipt, err := c.deploymentStackDeleteReceipt(req.Asset.Identity.NativeID, "westcentralus", saved, response{status: http.StatusNoContent})
			if err != nil {
				t.Fatal(err)
			}
			persisted, _ := json.Marshal(receipt)
			if err := json.Unmarshal(persisted, &receipt); err != nil {
				t.Fatal(err)
			}
			result, err := c.deploymentStackPoll(t.Context(), req.Asset.Identity.NativeID, "westcentralus", receipt)
			if err != nil || !result.Done {
				t.Fatal(result, err)
			}
		}
	}
}

func TestDeploymentStackDeletePlanCategoryConsequences(t *testing.T) {
	_, parent, member := stackGraphAssets(t)
	group := member
	group.ID, group.Identity.NativeType = "group", groupType
	group.Identity.NativeID = "/subscriptions/" + testSubscription + "/resourcegroups/other"
	for _, choice := range []struct {
		name                                     string
		memberDelete, groupDelete, inside, valid bool
	}{
		{"delete_resources_retain_group", true, false, true, true},
		{"retain_resources_delete_other_group", false, true, false, true},
		{"retain_child_delete_containing_group", false, true, true, false},
		{"delete_both", true, true, true, true},
	} {
		t.Run(choice.name, func(t *testing.T) {
			child := member
			if choice.inside {
				child.Identity.NativeID = group.Identity.NativeID + "/providers/microsoft.compute/virtualmachines/member"
			}
			c, req := stackDeletePlanFixture(t, false, contracts.ActionImpact{Asset: child, ControllerID: parent.ID, Delete: choice.memberDelete}, contracts.ActionImpact{Asset: group, ControllerID: parent.ID, Delete: choice.groupDelete})
			_, saved, err := c.deploymentStackDeletePlan(req)
			if (err == nil) != choice.valid {
				t.Fatal(saved, err)
			}
			if err == nil {
				for key, deleting := range map[string]bool{"Resources": choice.memberDelete, "ResourceGroups": choice.groupDelete} {
					want := "detach"
					if deleting {
						want = "delete"
					}
					if saved["unmanageAction."+key] != want {
						t.Fatal(saved)
					}
				}
			}
		})
	}
	c, req := stackDeletePlanFixture(t, false, []contracts.ActionImpact{}...)
	_, saved, err := c.deploymentStackDeletePlan(req)
	if err != nil || saved["unmanageAction.Resources"] != "detach" {
		t.Fatal(saved, err)
	}
}

func TestDeploymentStackDeletePlanRejectsChangedConsequences(t *testing.T) {
	for _, fault := range []string{"proof", "incomplete", "missing", "duplicate_asset", "duplicate_native", "foreign_connection", "foreign_subscription", "wrong_type", "wrong_controller", "self", "unknown_status", "unknown_deny", "mixed_category", "retention_conflict", "unknown_option", "invalid_option", "retain_missing", "type_conflict", "duplicate_type_option", "nested_cycle", "retained_parent_deletion"} {
		t.Run(fault, func(t *testing.T) {
			c, req := stackDeletePlanFixture(t, false)
			member := &req.LifecycleImpacts[0]
			review := object(req.Asset.Normalized[deploymentStackReviewKey])
			resign := false
			switch fault {
			case "proof":
				req.Asset.Normalized[deploymentStackProofKey] = "wrong"
			case "incomplete":
				review["arm_members_complete"] = false
				resign = true
			case "missing":
				req.LifecycleImpacts = nil
			case "duplicate_asset":
				req.LifecycleImpacts = append(req.LifecycleImpacts, *member)
			case "duplicate_native":
				other := *member
				other.Asset.ID = "other"
				req.LifecycleImpacts = append(req.LifecycleImpacts, other)
			case "foreign_connection":
				member.Asset.Identity.ConnectionID = "foreign"
			case "foreign_subscription":
				member.Asset.Identity.NativeID = strings.Replace(member.Asset.Identity.NativeID, testSubscription, "00000000-0000-0000-0000-000000000000", 1)
			case "wrong_type":
				member.Asset.Identity.NativeType = diskType
			case "wrong_controller":
				member.ControllerID = "unknown"
			case "self":
				member.Asset = req.Asset
			case "unknown_status":
				object(object(review["members"])[member.Asset.Identity.NativeID])["status"] = "unknown"
				resign = true
			case "unknown_deny":
				object(object(review["members"])[member.Asset.Identity.NativeID])["deny_status"] = "unknown"
				resign = true
			case "mixed_category":
				other := *member
				other.Asset.ID = "other"
				other.Asset.Identity.NativeID += "other"
				other.Delete = false
				c, req = stackDeletePlanFixture(t, false, *member, other)
			case "retention_conflict":
				req.Parameters = map[string]any{"retain_all_resources": true}
			case "unknown_option":
				req.Parameters = map[string]any{"bypassStackOutOfSyncError": true}
			case "invalid_option":
				req.Parameters = map[string]any{"retain_all_resources": "true"}
			case "retain_missing":
				req.Parameters = map[string]any{"retain_resources": []any{"absent"}}
			case "type_conflict":
				req.Parameters = map[string]any{"delete_options": []any{map[string]any{"resource_type": vmType, "delete_mode": "retain"}}}
			case "duplicate_type_option":
				req.Parameters = map[string]any{"delete_options": []any{map[string]any{"resource_type": vmType, "delete_mode": "delete"}, map[string]any{"resource_type": strings.ToLower(vmType), "delete_mode": "delete"}}}
			case "nested_cycle", "retained_parent_deletion":
				child := *member
				child.Asset.ID = "child"
				child.Asset.Identity.NativeID += "/extensions/child"
				child.Asset.Identity.NativeType = "Microsoft.Compute/virtualMachines/extensions"
				child.ControllerID = member.Asset.ID
				if fault == "nested_cycle" {
					child.ControllerID = child.Asset.ID
				} else {
					member.Delete = false
				}
				req.LifecycleImpacts = append(req.LifecycleImpacts, child)
			}
			if resign {
				req.Asset.Normalized[deploymentStackProofKey] = c.deploymentStackProof(req.Asset.Identity.NativeID, req.Asset.Identity.ConnectionID, review)
			}
			bound, saved, err := c.deploymentStackDeletePlan(req)
			if err == nil || bound.URL != "" || saved != nil {
				t.Fatal("unsafe native request produced", bound, saved, err)
			}
		})
	}
}

func TestDeploymentStackDeletePlanRetentionOptions(t *testing.T) {
	for _, parameters := range []map[string]any{
		{"retain_resources": []string{"member"}},
		{"retain_resources": []any{strings.ToUpper(resourceID(vmType, "member"))}},
		{"delete_options": []any{map[string]any{"resource_type": vmType, "delete_mode": "retain"}}},
	} {
		c, req := stackDeletePlanFixture(t, false)
		req.LifecycleImpacts[0].Delete = false
		req.Parameters = parameters
		_, saved, err := c.deploymentStackDeletePlan(req)
		if err != nil || saved["unmanageAction.Resources"] != "detach" {
			t.Fatal(saved, err)
		}
	}
	c, req := stackDeletePlanFixture(t, false)
	req.Parameters = map[string]any{"delete_options": []any{map[string]any{"resource_type": vmType, "delete_mode": "delete"}}}
	if _, _, err := c.deploymentStackDeletePlan(req); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentStackDeletePlanNestedConsequences(t *testing.T) {
	c, req := stackDeletePlanFixture(t, false)
	child := req.LifecycleImpacts[0]
	child.Asset.ID = "child"
	child.Asset.Identity.NativeID += "/extensions/child"
	child.Asset.Identity.NativeType = "Microsoft.Compute/virtualMachines/extensions"
	child.ControllerID = req.LifecycleImpacts[0].Asset.ID
	req.LifecycleImpacts = append(req.LifecycleImpacts, child)
	if _, _, err := c.deploymentStackDeletePlan(req); err != nil {
		t.Fatal(err)
	}
	req.LifecycleImpacts[1].Delete = false
	if _, _, err := c.deploymentStackDeletePlan(req); err == nil {
		t.Fatal("retained nested child under deleted parent accepted")
	}
	for i := range req.LifecycleImpacts {
		req.LifecycleImpacts[i].Delete = false
	}
	req.Parameters = map[string]any{"retain_all_resources": true}
	if _, _, err := c.deploymentStackDeletePlan(req); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentStackDeletePlanRequiresCreationEvidence(t *testing.T) {
	for _, mode := range []string{"missing", "legacy", "tampered"} {
		c, req := stackDeletePlanFixture(t, false)
		review := object(req.Asset.Normalized[deploymentStackReviewKey])
		switch mode {
		case "missing":
			review["incarnation"] = ""
		case "legacy":
			delete(review, "incarnation")
		case "tampered":
			review["incarnation"] = "forged"
		}
		if mode != "tampered" {
			req.Asset.Normalized[deploymentStackProofKey] = c.deploymentStackProof(req.Asset.Identity.NativeID, req.Asset.Identity.ConnectionID, review)
		}
		if bound, _, err := c.deploymentStackDeletePlan(req); err == nil || bound.URL != "" {
			t.Fatal("delete authorized without authentic creation evidence", mode, bound, err)
		}
	}
}
