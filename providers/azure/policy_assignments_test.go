package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type policyAssignmentFixture struct {
	runtime     *Runtime
	assignments map[string]map[string]any
	deleted     []string
}

func policyAssignmentBody(scope, name, assignmentType string, metadata map[string]any) map[string]any {
	return map[string]any{
		"id": scope + "/providers/Microsoft.Authorization/policyAssignments/" + name, "name": name,
		"type":     "Microsoft.Authorization/policyAssignments",
		"identity": map[string]any{"type": "SystemAssigned", "principalId": "3f2504e0-4f89-41d3-9a0c-0305e82c3301"},
		"properties": map[string]any{
			"displayName": name, "policyDefinitionId": "/providers/Microsoft.Authorization/policyDefinitions/deny-public-ip",
			"scope": scope, "enforcementMode": "Default", "assignmentType": assignmentType,
			"instanceId": name + "-instance", "metadata": metadata,
		},
	}
}

func newPolicyAssignmentFixture(t *testing.T) *policyAssignmentFixture {
	t.Helper()
	root := "/subscriptions/" + testSubscription
	f := &policyAssignmentFixture{assignments: map[string]map[string]any{}}
	initiative := policyAssignmentBody(root+"/resourceGroups/Test", "rg-initiative", "Custom", nil)
	object(initiative["properties"])["policyDefinitionId"] = "/providers/Microsoft.Authorization/policySetDefinitions/cis"
	for _, raw := range []map[string]any{
		policyAssignmentBody(root, "custom", "Custom", nil),
		initiative,
		policyAssignmentBody(root, "system", "System", nil),
		policyAssignmentBody(root, "SecurityCenterBuiltIn", "NotSpecified", map[string]any{"assignedBy": "Security Center"}),
	} {
		f.assignments[strings.ToLower(text(raw["id"]))] = raw
	}
	// Inherited from a management group; the subscription does not own it.
	inherited := policyAssignmentBody("/providers/Microsoft.Management/managementGroups/corp", "mg-baseline", "Custom", nil)
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		if req.URL.Host != "management.azure.com" || !strings.HasPrefix(path, strings.ToLower(root)+"/") {
			t.Fatal("policy fixture crossed subscription", req.URL)
		}
		if req.Method == "GET" && path == strings.ToLower(root)+"/providers/microsoft.authorization/locks" {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		if path == strings.ToLower(root)+"/providers/microsoft.authorization/policyassignments" {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != policyAssignmentVersion || req.URL.Query().Has("$filter") {
				t.Fatal("unexpected policy assignment list", req.URL)
			}
			values := []any{inherited}
			for _, raw := range f.assignments {
				values = append(values, raw)
			}
			return jsonResponse(200, map[string]any{"value": values}, http.Header{"X-Ms-Request-Id": {"policy-list"}}), nil
		}
		if strings.Contains(path, policyAssignmentPath) {
			if req.URL.Query().Get("api-version") != policyAssignmentVersion {
				t.Fatal("policy assignment version changed", req.URL)
			}
			raw := f.assignments[path]
			if raw == nil {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "PolicyAssignmentNotFound"}}, nil), nil
			}
			if req.Method == "DELETE" {
				f.deleted = append(f.deleted, path)
				delete(f.assignments, path)
				return jsonResponse(200, raw, http.Header{"X-Ms-Request-Id": {"policy-delete"}}), nil
			}
			return jsonResponse(200, raw, nil), nil
		}
		t.Fatal("unexpected policy fixture call", req.Method, req.URL)
		return nil, nil
	})
	return f
}

func (f *policyAssignmentFixture) items(t *testing.T) map[string]contracts.InventoryItem {
	t.Helper()
	kind := f.runtime.resourceKind(policyAssignmentType)
	batch, err := f.runtime.List(t.Context(), contracts.InventoryRequest{ConnectionID: "connection", Source: productInventorySource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}})
	if err != nil || !batch.Complete {
		t.Fatal("policy assignment inventory failed", batch, err)
	}
	result := map[string]contracts.InventoryItem{}
	for _, item := range batch.Items {
		result[text(item.Normalized["name"])] = item
	}
	return result
}

func policyAssignmentAsset(item contracts.InventoryItem) asset.Asset {
	return asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: policyAssignmentType, NativeID: item.NativeID}, Location: item.Location, Normalized: item.Normalized}
}

func TestPolicyAssignmentsInventoryOwnScopesAndProtectServiceManagedOnes(t *testing.T) {
	f := newPolicyAssignmentFixture(t)
	items := f.items(t)
	if len(items) != 4 || items["mg-baseline"].NativeID != "" {
		t.Fatalf("inherited management-group assignment was inventoried: %v", items)
	}
	if items["rg-initiative"].Normalized["initiative"] != true || items["custom"].Normalized["initiative"] != false {
		t.Fatal("initiative assignments are not told apart", items["rg-initiative"].Normalized)
	}
	for name, reason := range map[string]string{"system": "azure_policy_assignment_system_managed", "SecurityCenterBuiltIn": "azure_policy_assignment_defender_managed", "custom": ""} {
		if got := text(items[name].Normalized["cleanup_protection_reason"]); got != reason {
			t.Fatalf("%s protection = %q, want %q", name, got, reason)
		}
	}
}

func TestPolicyAssignmentDeletionConfirmsAbsenceAndRefusesARecreatedAssignment(t *testing.T) {
	f := newPolicyAssignmentFixture(t)
	items := f.items(t)
	value := policyAssignmentAsset(items["rg-initiative"])
	driver, err := newPolicyAssignmentAction(f.runtime.clients["connection"], "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "delete-initiative"}
	result, err := driver.Execute(t.Context(), request)
	if err != nil || result.ProviderRequestID != "policy-delete" || len(f.deleted) != 1 || !strings.HasSuffix(f.deleted[0], "/resourcegroups/test/providers/microsoft.authorization/policyassignments/rg-initiative") {
		t.Fatal("policy assignment deletion failed", result, f.deleted, err)
	}
	if done, err := driver.Wait(t.Context(), request, result); err != nil || !done.Done {
		t.Fatal("absence was not confirmed", done, err)
	}

	custom := policyAssignmentAsset(items["custom"])
	recreated := f.assignments[strings.ToLower(custom.Identity.NativeID)]
	object(recreated["properties"])["instanceId"] = "another-incarnation"
	driver, err = newPolicyAssignmentAction(f.runtime.clients["connection"], "connection", custom)
	if err != nil {
		t.Fatal(err)
	}
	check, err := driver.Preflight(t.Context(), contracts.ActionRequest{Asset: custom, Action: "delete", IdempotencyKey: "delete-custom"})
	if err != nil || check.Allowed || check.Reason != "policy_assignment_recreated" {
		t.Fatal("a recreated assignment was not refused", check, err)
	}

	system := policyAssignmentAsset(items["system"])
	driver, err = newPolicyAssignmentAction(f.runtime.clients["connection"], "connection", system)
	if err != nil {
		t.Fatal(err)
	}
	check, err = driver.Preflight(t.Context(), contracts.ActionRequest{Asset: system, Action: "delete", IdempotencyKey: "delete-system"})
	if err != nil || check.Allowed || check.Reason != "azure_policy_assignment_system_managed" {
		t.Fatal("a system-managed assignment was not refused", check, err)
	}
}

func TestPolicyAssignmentActionRefusesManagementGroupScope(t *testing.T) {
	f := newPolicyAssignmentFixture(t)
	f.items(t)
	id := "/providers/Microsoft.Management/managementGroups/corp/providers/Microsoft.Authorization/policyAssignments/mg-baseline"
	value := asset.Asset{ID: "mg", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: policyAssignmentType, NativeID: strings.ToLower(id)}, Location: "global", Normalized: map[string]any{policyAssignmentWire: id, policyAssignmentInstance: "mg-instance"}}
	if _, err := newPolicyAssignmentAction(f.runtime.clients["connection"], "connection", value); err == nil {
		t.Fatal("a management-group assignment could be deleted from a subscription connection")
	}
}

func TestIPGroupUsersAreDeletedFirstAndUnscannedUsersBlock(t *testing.T) {
	root := "/subscriptions/" + testSubscription + "/resourcegroups/net/providers/microsoft.network/"
	group := asset.Asset{ID: "group", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: ipGroupType, NativeID: root + "ipgroups/office"}, Normalized: map[string]any{
		"firewalls":        []any{map[string]any{"id": root + "azureFirewalls/edge"}},
		"firewallPolicies": []any{map[string]any{"id": root + "firewallPolicies/unscanned"}},
	}}
	firewall := asset.Asset{ID: "firewall", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: "Microsoft.Network/azureFirewalls", NativeID: root + "azurefirewalls/edge"}}
	contribution, err := NewResourceAttachments().Contribute(t.Context(), "scope", []asset.Asset{group, firewall})
	if err != nil {
		t.Fatal(err)
	}
	if len(contribution.Relationships) != 1 || contribution.Relationships[0].SourceAssetID != "firewall" || contribution.Relationships[0].TargetAssetID != "group" {
		t.Fatalf("relationships = %+v", contribution.Relationships)
	}
	if len(contribution.Unresolved) != 1 || !contribution.Unresolved[0].BlocksCleanup || contribution.Unresolved[0].NativeType != "Microsoft.Network/firewallPolicies" {
		t.Fatalf("unresolved = %+v", contribution.Unresolved)
	}
}

func TestAttachedMachineLearningComputeIsProtected(t *testing.T) {
	kind := resourceType{NativeType: mlComputeType}
	for properties, want := range map[string]string{
		`{"computeType":"ComputeInstance","isAttachedCompute":false}`: "",
		`{"computeType":"AmlCompute","isAttachedCompute":false}`:      "",
		`{"computeType":"Kubernetes","isAttachedCompute":true}`:       "azure_ml_attached_compute",
		`{"computeType":"VirtualMachine","isAttachedCompute":false}`:  "azure_ml_attached_compute",
	} {
		var props map[string]any
		if err := json.Unmarshal([]byte(properties), &props); err != nil {
			t.Fatal(err)
		}
		if got := protectionReason(kind, map[string]any{"properties": props}); got != want {
			t.Fatalf("%s: reason = %q, want %q", properties, got, want)
		}
	}
}
