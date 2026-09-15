package azure

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func TestDeploymentStackNativeMembersRemainAccounted(t *testing.T) {
	wire, err := os.ReadFile("fixtures/deployment-stacks/DeploymentStackResourceGroupGet.json")
	if err != nil {
		t.Fatal(err)
	}
	var example map[string]any
	if json.Unmarshal(wire, &example) != nil {
		t.Fatal("invalid example")
	}
	raw := object(object(object(example["responses"])["200"])["body"])
	c := &client{subscription: "00000000-0000-0000-0000-000000000000"}
	review, err := c.deploymentStackMemberReview(raw)
	if err != nil {
		t.Fatal(err)
	}
	if review["current_member_count"] != 3 || len(object(review["members"])) != 2 || review["unresolved_members"] != 1 || review["arm_members_complete"] != false {
		t.Fatal(review)
	}
	local, foreign := 0, 0
	for _, value := range object(review["members"]) {
		if object(value)["subscription_local"] == true {
			local++
		} else {
			foreign++
		}
	}
	if local != 1 || foreign != 1 {
		t.Fatal("native cross-subscription member lost", review)
	}
	projected, _ := json.Marshal(review)
	for _, secret := range []string{"example1", "ExtensionGeneratedConfigIdHereWithAnyFormat", "parameter1", "myOut"} {
		if strings.Contains(string(projected), secret) {
			t.Fatal("private values in review", secret)
		}
	}
	before := review["configuration"]
	ext := object(object(raw["properties"])["resources"].([]any)[2])
	object(ext["identifiers"])["id"] = "private-drift"
	changed, err := c.deploymentStackMemberReview(raw)
	if err != nil || changed["configuration"] == before {
		t.Fatal("extension drift omitted", err)
	}
}

func TestDeploymentStackMemberHistoryAndUnknownStates(t *testing.T) {
	c := &client{subscription: testSubscription}
	id := c.root() + "/resourceGroups/group/providers/Microsoft.Compute/virtualMachines/member"
	for _, status := range []string{"managed", "removeDenyFailed", "deleteFailed", "futureState", "managed removeDenyFailed"} {
		props := map[string]any{"resources": []any{map[string]any{"id": id, "status": status, "denyStatus": "futureDeny"}}, "deletedResources": []any{map[string]any{"id": id}}, "detachedResources": []any{map[string]any{"id": id}}, "failedResources": []any{map[string]any{"id": id, "error": map[string]any{"message": "secret"}}}}
		review, err := c.deploymentStackMemberReview(map[string]any{"properties": props})
		if err != nil || review["current_member_count"] != 1 || len(object(review["members"])) != 1 || review["arm_members_complete"] != true {
			t.Fatal(review, err)
		}
		for _, key := range []string{"deletedResources_count", "detachedResources_count", "failedResources_count"} {
			if review[key] != 1 {
				t.Fatal(key, review)
			}
		}
		member := object(object(review["members"])[strings.ToLower(id)])
		expected := status
		if status == "futureState" || status == "managed removeDenyFailed" {
			expected = "unknown"
		}
		if member["status"] != expected || member["deny_status"] != "unknown" {
			t.Fatal(member)
		}
	}
	for _, present := range []bool{false, true} {
		props := map[string]any{}
		if present {
			props["resources"] = []any{}
		}
		review, err := c.deploymentStackMemberReview(map[string]any{"properties": props})
		if err != nil || review["arm_members_complete"] != present {
			t.Fatal(review, err)
		}
		if !present && review["current_member_count"] != nil {
			t.Fatal("unknown count became empty", review)
		}
	}
}

func TestDeploymentStackMemberInvalidEvidence(t *testing.T) {
	c := &client{subscription: testSubscription}
	id := c.root() + "/resourceGroups/group/providers/Microsoft.Compute/virtualMachines/member"
	for _, fault := range []string{"array", "row", "missing_identity", "invalid_id", "duplicate", "type", "status", "history", "history_row"} {
		t.Run(fault, func(t *testing.T) {
			m := map[string]any{"id": id}
			props := map[string]any{"resources": []any{m}}
			switch fault {
			case "array":
				props["resources"] = "invalid"
			case "row":
				props["resources"] = []any{42}
			case "missing_identity":
				delete(m, "id")
			case "invalid_id":
				m["id"] = id + "?query"
			case "duplicate":
				props["resources"] = []any{m, map[string]any{"id": strings.ToUpper(id)}}
			case "type":
				m["type"] = "Microsoft.Compute/disks"
			case "status":
				m["status"] = 42
			case "history":
				props["deletedResources"] = 42
			case "history_row":
				props["failedResources"] = []any{"invalid"}
			}
			if _, err := c.deploymentStackMemberReview(map[string]any{"properties": props}); err == nil {
				t.Fatal("accepted", fault)
			}
		})
	}
}

func TestDeploymentStackMemberIdentityScopes(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	for _, id := range []string{root, root + "/resourceGroups/group", root + "/providers/Microsoft.Storage/storageAccounts/account", root + "/resourceGroups/group/providers/Microsoft.Compute/virtualMachines/vm/providers/Microsoft.Insights/diagnosticSettings/log", "/providers/Microsoft.Management/managementGroups/group"} {
		got, kind, err := deploymentStackMemberID(id)
		if err != nil || got != strings.ToLower(id) || kind == "" {
			t.Fatal(id, got, kind, err)
		}
	}
	for _, id := range []string{root + "/resourceGroups", root + "/resourceGroups/group/providers/Microsoft.Compute/virtualMachines", root + "/providers/invalid/type/name", root + "/providers/Microsoft.Compute/type/name/child", root + "//providers/Microsoft.Compute/type/name", root + "/providers/Microsoft.Compute/type/%2e", root + "/providers/Microsoft.Compute/type/.."} {
		if _, _, err := deploymentStackMemberID(id); err == nil {
			t.Fatal("invalid identity accepted", id)
		}
	}
}
