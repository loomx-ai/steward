package azure

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// This shape is produced by the AKS graph: the node group and all descendants
// remain directly controlled by the cluster, even where VM attachments nest.
func TestResourceGroupManagedAKSProductProjection(t *testing.T) {
	s := newAKSScenario()
	values := s.assets(t)
	c, req := groupProductFixture(t)
	req.LifecycleImpacts = nil
	for i, value := range values {
		value.Identity.Partition = "azure"
		controller := values[0].ID
		if i == 0 {
			controller = req.Asset.ID
		}
		req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: value, ControllerID: controller, Delete: true})
	}
	products, err := c.resourceGroupProductRequests(req)
	if err != nil {
		t.Fatal("native AKS subtree cannot compose with containing group", err)
	}
	if len(products[values[0].ID].LifecycleImpacts) != 4 {
		t.Fatal("AKS lost its full native ownership context")
	}
	if _, ok := products[values[1].ID]; ok {
		t.Fatal("managed node group became an independent ordinary group action")
	}
	if _, ok := products[values[4].ID]; ok {
		t.Fatal("unknown managed descendant gained an invented product action")
	}
	vm := products[values[2].ID]
	if vm.Asset.ID == "" || len(vm.LifecycleImpacts) != 1 || vm.LifecycleImpacts[0].Asset.ID != values[3].ID || vm.LifecycleImpacts[0].ControllerID != vm.Asset.ID || !vm.LifecycleImpacts[0].Delete {
		t.Fatal("native VM attachment context missing", vm)
	}
	for _, product := range products {
		if product.ExecutionResult != nil || len(product.Parameters) != 0 {
			t.Fatal("fabricated product execution context", product.Asset.ID)
		}
	}
	// Input retains the graph's controller identity; product-local projection is private.
	for _, impact := range req.LifecycleImpacts[1:] {
		if impact.ControllerID != asset.AssetID("cluster") {
			t.Fatal("projection mutated reviewed tree")
		}
	}
}

func TestResourceGroupManagedAKSProjectionBoundaries(t *testing.T) {
	for _, mode := range []string{"missing-group", "unrelated-group", "external-member", "wrong-controller", "retained-member", "foreign-connection", "group-prerequisite"} {
		t.Run(mode, func(t *testing.T) {
			values := newAKSScenario().assets(t)
			c, req := groupProductFixture(t)
			req.LifecycleImpacts = nil
			for i, value := range values {
				value.Identity.Partition = "azure"
				controller := values[0].ID
				if i == 0 {
					controller = req.Asset.ID
				}
				req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: value, ControllerID: controller, Delete: true})
			}
			switch mode {
			case "missing-group":
				req.LifecycleImpacts = append(req.LifecycleImpacts[:1], req.LifecycleImpacts[2:]...)
			case "unrelated-group":
				req.LifecycleImpacts[1].Asset.Identity.NativeID += "-other"
			case "external-member":
				req.LifecycleImpacts[4].Asset.Identity.NativeID = strings.ReplaceAll(req.LifecycleImpacts[4].Asset.Identity.NativeID, "custom-nodes", "unrelated")
			case "wrong-controller":
				req.LifecycleImpacts[1].ControllerID = req.Asset.ID
			case "retained-member":
				req.LifecycleImpacts[2].Delete = false
			case "foreign-connection":
				req.LifecycleImpacts[2].Asset.Identity.ConnectionID = "other"
			case "group-prerequisite":
				req.PrerequisiteDeletions = []contracts.ActionImpact{req.LifecycleImpacts[1]}
				req.PrerequisiteDeletions[0].Asset.ID = "extra-group"
				req.PrerequisiteDeletions[0].Asset.Identity.NativeID += "-extra"
			}
			if out, err := c.resourceGroupProductRequests(req); err == nil || out != nil {
				t.Fatal("invalid managed scope accepted", out, err)
			}
		})
	}
}

func TestResourceGroupManagedAKSNativeLifecycle(t *testing.T) {
	for _, mode := range []string{"complete", "group-owner", "group-forbidden", "member-forbidden", "omitted-member", "new-member", "owner-after-preflight", "readback-forbidden"} {
		t.Run(mode, func(t *testing.T) { testResourceGroupManagedAKSNativeLifecycle(t, mode) })
	}
}
func testResourceGroupManagedAKSNativeLifecycle(t *testing.T, mode string) {
	t.Helper()

	s := newAKSScenario()
	r := s.runtime(t)
	rootID := "/subscriptions/" + testSubscription + "/resourcegroups/test"
	raw := map[string]any{"id": rootID, "type": groupType, "name": "test", "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
	gone, deletes := false, 0
	faults := false
	previous := r.transport
	r.transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		path := strings.ToLower(q.URL.Path)
		if faults && (mode == "group-forbidden" && path == strings.ToLower(text(s.group["id"])) || mode == "member-forbidden" && path == strings.ToLower(text(s.disk["id"])) || mode == "readback-forbidden" && gone && path == strings.ToLower(text(s.group["id"]))) {
			return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), nil
		}

		if q.Method == "DELETE" {
			if path != rootID {
				t.Fatal("independent managed deletion", q.URL)
			}
			deletes++
			gone = true
			s.clusterGone = true
			if mode == "readback-forbidden" {
				s.childrenGone = true
			}
			return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
		}
		if path == rootID {
			if gone {
				return jsonResponse(404, map[string]any{}, nil), nil
			}
			return jsonResponse(200, raw, nil), nil
		}
		if path == rootID+"/resources" {
			return jsonResponse(200, map[string]any{"value": []any{s.cluster}}, nil), nil
		}
		return previous.RoundTrip(q)
	})
	values := s.assets(t)
	_, req := groupProductFixture(t)
	req.LifecycleImpacts = nil
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	item, err := r.inventoryItem(t.Context(), c, raw, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Asset.Normalized = item.Normalized
	req.Asset.Location = item.Location
	for i, value := range values {
		value.Identity.Partition = "azure"
		controller := values[0].ID
		if i == 0 {
			controller = req.Asset.ID
		}
		req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: value, ControllerID: controller, Delete: true})
	}

	req.Asset.Capabilities = item.ResourceKind.Capabilities
	req.Asset.ResourceKindID = item.ResourceKind.ID
	all := []asset.Asset{req.Asset}
	for _, impact := range req.LifecycleImpacts {
		all = append(all, impact.Asset)
	}
	services, err := r.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	clusters, err := r.ClusterLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	combined := governance.Contribution{}
	for _, contributor := range []governance.Contributor{NewResourceAttachments(), services, clusters} {
		built, err := contributor.Contribute(t.Context(), "scope", all)
		if err != nil {
			t.Fatal("native combined graph", err)
		}
		combined.Bindings = append(combined.Bindings, built.Bindings...)
		combined.Relationships = append(combined.Relationships, built.Relationships...)
		combined.Unresolved = append(combined.Unresolved, built.Unresolved...)
	}
	planned, err := plan.Solve(plan.Input{Assets: all, ResolvedAssetIDs: []asset.AssetID{req.Asset.ID}, LifecycleBindings: combined.Bindings, Relationships: combined.Relationships, Unresolved: combined.Unresolved})
	if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 1 || len(planned.ImpactItems) != 5 {
		t.Fatal("native group AKS plan", planned, err)
	}
	req = servicePlanRequest(planned, all, req.Asset)
	req.IdempotencyKey = "managed-group-job"
	driver, err := r.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}

	faults = true
	switch mode {
	case "group-owner":
		s.group["managedBy"] = resourceID(aksType, "other")
	case "omitted-member":
		s.members = s.members[1:]
	case "new-member":
		s.members = append(s.members, map[string]any{"id": text(s.group["id"]) + "/providers/Contoso.Example/widgets/new", "type": "Contoso.Example/widgets", "name": "new"})
	}
	check, err := driver.Preflight(t.Context(), req)
	if mode == "group-owner" || mode == "group-forbidden" || mode == "member-forbidden" || mode == "omitted-member" || mode == "new-member" {
		if err == nil && check.Allowed || deletes != 0 {
			t.Fatal("unverified managed subtree passed preflight", mode, check, err)
		}
		return
	}

	if err != nil || !check.Allowed {
		t.Fatal("native managed subtree preflight", check, err)
	}
	products, err := c.resourceGroupProductRequests(req)
	if err != nil {
		t.Fatal(err)
	}
	standalone, err := r.ResolveAction(t.Context(), "connection", products[values[2].ID].Asset)
	if err != nil {
		t.Fatal(err)
	}
	isolated, err := standalone.Preflight(t.Context(), products[values[2].ID])
	if err == nil && isolated.Allowed {
		t.Fatal("managed preflight context leaked into independent VM deletion")
	}

	if mode == "owner-after-preflight" {
		s.group["managedBy"] = resourceID(aksType, "other")
	}
	result, err := driver.Execute(t.Context(), req)
	if mode == "owner-after-preflight" {
		if err == nil || deletes != 0 {
			t.Fatal("owner changed before DELETE was accepted", err, deletes)
		}
		return
	}

	if err != nil || deletes != 1 {
		t.Fatal("native containing group delete", result, err)
	}
	for _, phase := range []string{"group", "members", "absent"} {
		if phase != "group" {
			s.groupGone = true
		}
		if phase == "absent" {
			s.childrenGone = true
		}
		result.Data = groupOperationJSON(t, result.Data)
		wait, err := driver.Wait(t.Context(), req, result)
		if mode == "readback-forbidden" {
			if err == nil || wait.Done || deletes != 1 {
				t.Fatal("forbidden managed readback completed", wait, err)
			}
			return
		}

		if err != nil || wait.Done != (phase == "absent") {
			t.Fatal("managed subtree residual", phase, wait, err)
		}
		result.Data = wait.Data
	}
	if deletes != 1 || s.deletes != 0 {
		t.Fatal("native deletion repeated", deletes, s.deletes)
	}
}

func TestResourceGroupManagedAKSMultilevelAttachments(t *testing.T) {
	c, req := groupProductFixture(t)
	wire, err := json.Marshal(req.LifecycleImpacts)
	if err != nil {
		t.Fatal(err)
	}
	var attachments []contracts.ActionImpact
	if err = json.Unmarshal([]byte(strings.NewReplacer("/resourcegroups/test/", "/resourcegroups/custom-nodes/", "/resourceGroups/test/", "/resourceGroups/custom-nodes/").Replace(string(wire))), &attachments); err != nil {
		t.Fatal(err)
	}
	values := newAKSScenario().assets(t)
	req.LifecycleImpacts = nil
	for i, value := range values[:2] {
		value.Identity.Partition = "azure"
		controller := values[0].ID
		if i == 0 {
			controller = req.Asset.ID
		}
		req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: value, ControllerID: controller, Delete: true})
	}
	for _, impact := range attachments {
		impact.ControllerID = values[0].ID
		req.LifecycleImpacts = append(req.LifecycleImpacts, impact)
	}
	products, err := c.resourceGroupProductRequests(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(products["vm"].LifecycleImpacts) != 4 || len(products["nic"].LifecycleImpacts) != 1 || len(products["cluster"].LifecycleImpacts) != 6 {
		t.Fatal("nested managed attachments lost", len(products["vm"].LifecycleImpacts), len(products["nic"].LifecycleImpacts), len(products["cluster"].LifecycleImpacts))
	}
	for _, impact := range products["vm"].LifecycleImpacts {
		if impact.Asset.ID == "ip" && impact.ControllerID != "nic" {
			t.Fatal("IP lost its immediate NIC controller")
		}
	}
	twin := req.LifecycleImpacts[2]
	if twin.Asset.ID != "vm" {
		t.Fatal("fixture VM ordering changed")
	}
	twin.Asset.ID = "second-vm"
	twin.Asset.Identity.NativeID += "-other"
	req.LifecycleImpacts = append(req.LifecycleImpacts, twin)
	if out, err := c.resourceGroupProductRequests(req); err == nil || out != nil {
		t.Fatal("ambiguous managed attachment controller accepted")
	}

}
