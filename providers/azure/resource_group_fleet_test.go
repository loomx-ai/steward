package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestResourceGroupFleetProductProjection(t *testing.T) {
	for _, mode := range []string{"simple", "descendants"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubFixture(t, false)
			if mode == "descendants" {
				h = newFleetHubMembersFixture(t)
			}
			resourceGroupFleetProductProjection(t, h)
		})
	}
}

func resourceGroupFleetProductProjection(t *testing.T, h *fleetHubFixture) {
	t.Helper()
	fleet := fleetRootCleanupRequest(t, h)
	_, req := groupProductFixture(t)
	c, err := h.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	values := h.graphAssets(t)
	for _, value := range values {
		if value.Identity.NativeID == text(h.group["id"]) {
			req.Asset = value
		}
	}
	req.LifecycleImpacts = append([]contracts.ActionImpact{{Asset: fleet.Asset, ControllerID: req.Asset.ID, Delete: true}}, fleet.LifecycleImpacts...)
	req.PrerequisiteDeletions = fleet.PrerequisiteDeletions
	products, err := c.resourceGroupProductRequests(req)
	if err != nil {
		t.Fatal("native Fleet subtree rejected", err)
	}
	actual := products[fleet.Asset.ID]
	if len(actual.LifecycleImpacts) != len(fleet.LifecycleImpacts) {
		t.Fatal("Fleet lost authenticated Hub members")
	}
	cluster := fleetAssetByKind(t, values, aksType)
	hub := products[cluster.ID]
	want := 0
	state := object(fleet.Asset.Normalized[fleetHubState])
	for _, value := range object(state["members"]) {
		if text(object(value)["group"]) == h.nodes {
			want++
		}
	}
	if len(hub.LifecycleImpacts) != want {
		t.Fatal("Hub lost its node-group projection", len(hub.LifecycleImpacts), want)
	}
	for _, impact := range hub.LifecycleImpacts {
		if impact.ControllerID != cluster.ID {
			t.Fatal("Hub child lost native parent", impact)
		}
	}
	for _, impact := range fleet.LifecycleImpacts {
		if kind, known := findType(impact.Asset.Identity.NativeType); known && kind.ReadOnly && products[impact.Asset.ID].Asset.ID != "" {
			t.Fatal("read-only managed child acquired an independent action")
		}
		if impact.Asset.Identity.NativeID == h.hub || impact.Asset.Identity.NativeID == h.nodes {
			if products[impact.Asset.ID].Asset.ID != "" {
				t.Fatal("managed group became an independent action")
			}
		}
	}
}

func TestResourceGroupFleetNativeLifecycle(t *testing.T) {
	for _, mode := range []string{"simple", "descendants", "running"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubFixture(t, false)
			if mode == "descendants" {
				h = newFleetHubMembersFixture(t)
			}
			resourceGroupFleetNativeLifecycle(t, h, mode == "running")
		})
	}
}

func resourceGroupFleetNativeLifecycle(t *testing.T, h *fleetHubFixture, running bool) {
	t.Helper()
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	for _, raw := range h.fleetFixture.resources {
		if raw["type"] == fleetRunType {
			state := "Completed"
			if running {
				state = "Running"
			}
			object(object(object(raw["properties"])["status"])["status"])["state"] = state
		}
	}
	values := h.graphAssets(t)
	var group asset.Asset
	for i := range values {
		values[i].Capabilities = h.runtime.resourceKind(values[i].Identity.NativeType).Capabilities
		if values[i].Identity.NativeID == text(h.group["id"]) {
			group = values[i]
		}
	}
	built, err := fleetHubContributions(t, h, values)
	if err != nil {
		t.Fatal("group Fleet graph", err)
	}
	planned, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{group.ID}, LifecycleBindings: built.Bindings, Relationships: built.Relationships, Unresolved: built.Unresolved})
	if err != nil || len(planned.Blockers) != 0 {
		t.Fatal("group Fleet plan", len(planned.Steps), len(planned.ImpactItems), planned.Blockers, err)
	}
	req := servicePlanRequest(planned, values, group)
	req.IdempotencyKey = "group-fleet-native"
	driver, err := h.runtime.ResolveAction(ctx, "connection", group)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(ctx, req); err == nil {
		t.Fatal("group bypassed Fleet prerequisites")
	}
	fallback := h.override
	deletes := []string{}
	stops := 0
	reads := []string{}
	var active contracts.ActionRequest
	h.override = func(q *http.Request) (*http.Response, bool) {
		if q.Method == "GET" {
			reads = append(reads, q.URL.Path)
		}
		if q.Method == "POST" {
			fleetAssertMutation(t, h.fleetFixture, active, q, "stop")
			if active.Asset.Identity.NativeType != fleetRunType {
				t.Fatal("unexpected preparation", q.URL)
			}
			raw := h.fleetFixture.resources[active.Asset.Identity.NativeID]
			object(object(object(raw["properties"])["status"])["status"])["state"] = "Stopped"
			raw["eTag"] = "after-stop"
			stops++
			return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody}, true
		}
		if q.Method == "DELETE" {
			id := strings.ToLower(q.URL.Path)
			if id == group.Identity.NativeID {
				if len(h.fleetFixture.resources) != 1 {
					t.Fatal("group preceded Fleet child deletion")
				}
				h.gone[id] = true
				delete(h.fleetFixture.resources, h.fleet)
			} else {
				fleetAssertMutation(t, h.fleetFixture, active, q, "delete")
				delete(h.fleetFixture.resources, id)
				for child, raw := range h.fleetFixture.resources {
					if active.Asset.Identity.NativeType == fleetRunType && raw["type"] == fleetGateType {
						delete(h.fleetFixture.resources, child)
					}
				}
			}
			deletes = append(deletes, id)
			return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody}, true
		}
		return fallback(q)
	}
	completed := map[plan.StepID]bool{}
	for len(completed) < len(planned.Steps)-1 {
		found := false
		for _, step := range planned.Steps {
			if step.AssetID == group.ID || completed[step.ID] || slices.ContainsFunc(step.DependsOn, func(id plan.StepID) bool { return !completed[id] }) {
				continue
			}
			value := values[slices.IndexFunc(values, func(v asset.Asset) bool { return v.ID == step.AssetID })]
			active = servicePlanRequest(planned, values, value)
			active.IdempotencyKey = string(step.ID)
			child, err := h.runtime.ResolveAction(ctx, "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			result, err := child.Execute(ctx, active)
			if err != nil {
				t.Fatal("Fleet prerequisite execution", value.Identity.NativeType, err)
			}
			for round := 0; ; round++ {
				result = fleetSerializedResult(t, result, nil)
				waited, err := child.Wait(ctx, active, result)
				if err != nil || round >= 3 {
					t.Fatal("Fleet prerequisite outcome", waited, err)
				}
				if waited.Done {
					break
				}
				result = fleetSerializedResult(t, result, &waited)
			}
			completed[step.ID], found = true, true
		}
		if !found {
			t.Fatal("Fleet group plan has no executable prerequisite")
		}
	}
	for _, fault := range []string{"hub-owner", "node-owner", "hub-settings", "monitor-settings", "dns-settings", "member-forbidden", "new-member"} {
		t.Run("reject-"+fault, func(t *testing.T) {
			id := h.cluster
			var raw map[string]any
			switch fault {
			case "hub-owner", "node-owner":
				id = h.hub
				if fault == "node-owner" {
					id = h.nodes
				}
				raw = maps.Clone(h.groups[id])
				raw["managedBy"] = resourceID(aksType, "unrelated")
			case "hub-settings":
				raw = maps.Clone(h.resources[id])
				raw["properties"] = maps.Clone(object(raw["properties"]))
				object(raw["properties"])["futurePrivateSetting"] = "changed"
			case "monitor-settings", "dns-settings":
				id = ""
				for _, value := range values {
					if fault == "monitor-settings" && monitorResourceKind(value.Identity.NativeType) != "" || fault == "dns-settings" && value.Identity.NativeType == privateDNSLinkType {
						id = value.Identity.NativeID
						break
					}
				}
				if id == "" {
					return
				}
				raw = maps.Clone(h.resources[id])
				raw["tags"] = map[string]any{"changed": "yes"}
			case "new-member":
				id = h.nodes + "/providers/contoso.example/widgets/new"
				h.resources[id] = map[string]any{"id": id, "type": "Contoso.Example/widgets", "name": "new", "properties": map[string]any{}}
				defer delete(h.resources, id)
			}
			original := h.override
			defer func() { h.override = original }()
			h.override = func(q *http.Request) (*http.Response, bool) {
				if q.Method == "GET" && strings.EqualFold(q.URL.Path, id) {
					if fault == "member-forbidden" {
						return jsonResponse(403, map[string]any{}, nil), true
					}
					if raw != nil {
						return jsonResponse(200, raw, nil), true
					}
				}
				return original(q)
			}
			before := len(deletes)
			if _, err := driver.Execute(ctx, req); err == nil || len(deletes) != before {
				t.Fatal("unverified Fleet group mutation", fault, err)
			}
		})
	}
	c, err := h.runtime.resolve(ctx, "connection")
	if err != nil {
		t.Fatal(err)
	}
	products, err := c.resourceGroupProductRequests(req)
	if err != nil {
		t.Fatal(err)
	}
	for _, product := range products {
		kind := product.Asset.Identity.NativeType
		if kind != aksType && kind != scaleSetType && kind != vnetType && monitorResourceKind(kind) == "" {
			continue
		}
		standalone, err := h.runtime.ResolveAction(ctx, "connection", product.Asset)
		if err != nil {
			t.Fatal(err)
		}
		if check, err := standalone.Preflight(t.Context(), product); err == nil && check.Allowed {
			t.Fatal("native managed scope leaked into standalone action", kind)
		}
	}
	if check, err := driver.Preflight(ctx, req); err != nil || !check.Allowed {
		t.Fatal("native group Fleet preflight", check, err, reads[max(0, len(reads)-12):])
	}
	active = req
	result, err := driver.Execute(ctx, req)
	if err != nil {
		t.Fatal("native group Fleet deletion", err)
	}
	for _, phase := range []string{"hub", "nodes", "cluster", "readonly", "absent"} {
		if phase != "hub" {
			h.gone[h.hub] = true
		}
		if phase == "cluster" || phase == "readonly" || phase == "absent" {
			h.gone[h.nodes] = true
		}
		readonly := false
		if phase == "readonly" || phase == "absent" {
			fleet := fleetAssetByKind(t, values, fleetType)
			for id, member := range object(object(fleet.Normalized[fleetHubState])["members"]) {
				kind, known := findType(text(object(member)["kind"]))
				if phase == "readonly" && known && kind.ReadOnly {
					readonly = true
					continue
				}
				h.gone[id] = true
			}
		}
		if phase == "readonly" && !readonly {
			continue // The minimal fixture has no read-only descendants.
		}
		result.Data = groupOperationJSON(t, result.Data)
		waited, err := driver.Wait(ctx, req, result)
		if err != nil || waited.Done != (phase == "absent") {
			t.Fatal("Fleet group residual", phase, waited, err)
		}
		result.Data = waited.Data
	}
	if len(deletes) != len(planned.Steps) || deletes[len(deletes)-1] != group.Identity.NativeID {
		t.Fatal("group mutation set changed", deletes)
	}
	if (stops == 1) != running || stops > 1 {
		t.Fatal("Fleet update stop phase changed", stops, running)
	}
	wire, err := json.Marshal(logs)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"hub-private-proof", "hub-authored-secret", "do-not-expose", "secret-in-script", "unknown-private-secret", "emailAddress"} {
		if strings.Contains(string(wire), secret) {
			t.Fatal("Fleet group logs exposed private member configuration", secret)
		}
	}
}
