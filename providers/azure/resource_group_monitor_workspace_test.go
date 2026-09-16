package azure

import (
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"io"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestResourceGroupMonitorWorkspaceProjection(t *testing.T) {
	_, r, values := monitorWorkspaceScenario(t)
	c, err := r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	_, req := groupProductFixture(t)
	req.LifecycleImpacts = nil
	for i, value := range values[:5] {
		value.Identity.Partition = "azure"
		owner := values[0].ID
		if i == 0 {
			owner = req.Asset.ID
		}
		req.LifecycleImpacts = append(req.LifecycleImpacts, contracts.ActionImpact{Asset: value, ControllerID: owner, Delete: true})
	}
	prerequisite := values[5]
	prerequisite.Identity.Partition = "azure"
	req.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: prerequisite, ControllerID: values[0].ID, Delete: true}}
	products, err := c.resourceGroupProductRequests(req)
	if err != nil {
		t.Fatal("workspace managed subtree rejected", err)
	}
	if len(products) != 3 || len(products[values[0].ID].LifecycleImpacts) != 4 || len(products[values[0].ID].PrerequisiteDeletions) != 1 {
		t.Fatal("workspace lost native children or external unlink", len(products))
	}
	if products[values[1].ID].Asset.ID != "" || products[values[4].ID].Asset.ID != "" {
		t.Fatal("managed group or unknown member acquired independent action")
	}
	settings := object(req.LifecycleImpacts[0].Asset.Normalized["defaultIngestionSettings"])
	settings["dataCollectionEndpointResourceId"] = strings.ReplaceAll(text(settings["dataCollectionEndpointResourceId"]), "purpose-built-owned", "other-group")
	if out, err := c.resourceGroupProductRequests(req); err == nil || out != nil {
		t.Fatal("disagreeing native ingestion groups accepted")
	}

}

func TestResourceGroupMonitorWorkspaceNativeLifecycle(t *testing.T) {
	for _, mode := range []string{"complete", "owner-changed", "rule-forbidden", "ingestion-changed", "late-association", "readback-forbidden"} {
		t.Run(mode, func(t *testing.T) { testResourceGroupMonitorWorkspaceNativeLifecycle(t, mode) })
	}
}
func testResourceGroupMonitorWorkspaceNativeLifecycle(t *testing.T, mode string) {
	t.Helper()

	s, r, values := monitorWorkspaceScenario(t)
	_, req := groupProductFixture(t)
	raw := map[string]any{"id": req.Asset.Identity.NativeID, "type": groupType, "name": "test", "location": "eastus", "properties": map[string]any{"provisioningState": "Succeeded"}}
	s.add(raw, resourcesVersion)
	s.lists[req.Asset.Identity.NativeID+"/resources"] = []any{s.records[values[0].Identity.NativeID]}
	s.version[req.Asset.Identity.NativeID+"/resources"] = resourcesVersion
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
	req.Asset.Capabilities = item.ResourceKind.Capabilities
	req.Asset.ResourceKindID = item.ResourceKind.ID
	for i := range values {
		values[i].Identity.Partition = "azure"
	}
	all := append([]asset.Asset{req.Asset}, values...)
	req, input := dnsRequest(t, r, all, req.Asset)
	req.IdempotencyKey = "group-monitor-workspace"
	planned, err := plan.Solve(input)
	if err != nil || len(planned.Steps) != 2 || len(planned.ImpactItems) != 5 || len(req.PrerequisiteDeletions) != 1 {
		t.Fatal("combined group workspace plan", planned, err)
	}
	driver, err := r.ResolveAction(t.Context(), "connection", req.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(t.Context(), req); err == nil || len(s.deletes) != 0 {
		t.Fatal("group deleted before association unlink", err)
	}
	child, err := r.ResolveAction(t.Context(), "connection", values[5])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := child.Execute(t.Context(), servicePlanRequest(planned, all, values[5])); err != nil {
		t.Fatal(err)
	}

	switch mode {
	case "owner-changed":
		s.records[values[1].Identity.NativeID]["managedBy"] = resourceID(monitorWorkspaceType, "other")
	case "rule-forbidden":
		s.status[values[2].Identity.NativeID] = 403
	case "ingestion-changed":
		object(object(s.records[values[0].Identity.NativeID]["properties"])["defaultIngestionSettings"])["dataCollectionEndpointResourceId"] = resourceID(dataCollectionEndpointType, "other")
	case "late-association":
		late := maps.Clone(s.records[values[5].Identity.NativeID])
		late["id"] = values[5].Identity.NativeID + "-late"
		late["name"] = "metrics-late"
		s.add(late, dataCollectionVersion)
		for _, target := range values[2:4] {
			s.lists[target.Identity.NativeID+"/associations"] = append(s.lists[target.Identity.NativeID+"/associations"], late)
		}
	}
	check, err := driver.Preflight(t.Context(), req)
	if mode != "complete" && mode != "readback-forbidden" {
		if err == nil && check.Allowed || len(s.deletes) != 1 {
			t.Fatal("changed workspace scope accepted", mode, check, err)
		}
		return
	}

	if err != nil || !check.Allowed {
		t.Fatal("workspace subtree preflight", check, err)
	}
	s.handle = func(q *http.Request) (*http.Response, bool) {
		if q.Method != "DELETE" {
			return nil, false
		}
		if !strings.EqualFold(q.URL.Path, req.Asset.Identity.NativeID) {
			t.Fatal("independent managed delete", q.URL)
		}
		s.deletes = append(s.deletes, req.Asset.Identity.NativeID)
		s.gone[req.Asset.Identity.NativeID] = true
		s.gone[values[0].Identity.NativeID] = true
		return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, true
	}
	result, err := driver.Execute(t.Context(), req)
	if err != nil {
		t.Fatal("native group delete", err)
	}

	if mode == "readback-forbidden" {
		for _, value := range values[2:5] {
			s.gone[value.Identity.NativeID] = true
		}
		s.status[values[1].Identity.NativeID] = 403
	}
	for _, phase := range []string{"managed-group", "members", "absent"} {
		if phase != "managed-group" {
			s.gone[values[1].Identity.NativeID] = true
		}
		if phase == "absent" {
			for _, value := range values[2:5] {
				s.gone[value.Identity.NativeID] = true
			}
		}
		result.Data = groupOperationJSON(t, result.Data)
		out, err := driver.Wait(t.Context(), req, result)
		if mode == "readback-forbidden" {
			if err == nil || out.Done {
				t.Fatal("forbidden managed group observation succeeded", out, err)
			}
			return
		}

		if err != nil || out.Done != (phase == "absent") {
			t.Fatal("workspace native residual", phase, out, err)
		}
		result.Data = out.Data
	}
	if len(s.deletes) != 2 || s.deletes[0] != values[5].Identity.NativeID || s.deletes[1] != req.Asset.Identity.NativeID {
		t.Fatal("unexpected mutation set", s.deletes)
	}
}
