package azure

import (
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestResourceGroupInsightsProductProjection(t *testing.T) {
	f := newInsightsComponentFixture(t)
	component, _, values := insightsComponentPlan(t, f)
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	_, req := groupProductFixture(t)
	found := false
	for _, value := range values {
		if value.Identity.NativeID == f.groupID {
			req.Asset = value
			found = true
		}
	}
	if !found {
		t.Fatal("native containing group missing")
	}
	req.LifecycleImpacts = append([]contracts.ActionImpact{{Asset: component.Asset, ControllerID: req.Asset.ID, Delete: true}}, component.LifecycleImpacts...)
	req.PrerequisiteDeletions = component.PrerequisiteDeletions
	products, err := c.resourceGroupProductRequests(req)
	if err != nil {
		t.Fatal("signed Insights subtree rejected", err)
	}
	actual := products[component.Asset.ID]
	if len(actual.LifecycleImpacts) != len(component.LifecycleImpacts) || len(actual.PrerequisiteDeletions) != len(component.PrerequisiteDeletions) {
		t.Fatal("native component context lost")
	}
	for _, impact := range component.LifecycleImpacts {
		if impact.Asset.Identity.NativeType == groupType && products[impact.Asset.ID].Asset.ID != "" {
			t.Fatal("managed group became independent action")
		}
	}
	expected := map[string]bool{}
	for _, p := range component.PrerequisiteDeletions {
		expected[p.Asset.Identity.NativeID] = true
	}
	for _, p := range actual.PrerequisiteDeletions {
		if !expected[p.Asset.Identity.NativeID] {
			t.Fatal("legacy identity changed", p.Asset.Identity.NativeID)
		}
	}
	t.Run("worker-root-prerequisites", func(t *testing.T) {
		flattened := req
		flattened.PrerequisiteDeletions = slices.Clone(req.PrerequisiteDeletions)
		for i := range flattened.PrerequisiteDeletions {
			flattened.PrerequisiteDeletions[i].ControllerID = req.Asset.ID
		}
		projected, err := c.resourceGroupProductRequests(flattened)
		if err != nil {
			t.Fatal("worker prerequisite projection", err)
		}
		children := projected[component.Asset.ID].PrerequisiteDeletions
		if len(children) != len(expected) {
			t.Fatal("worker prerequisites lost", len(children), len(expected))
		}
		for _, child := range children {
			if child.ControllerID != component.Asset.ID || !expected[child.Asset.Identity.NativeID] {
				t.Fatal("worker prerequisite identity or owner changed", child)
			}
		}
		for _, child := range flattened.PrerequisiteDeletions {
			if child.ControllerID != req.Asset.ID {
				t.Fatal("projection mutated reviewed group request")
			}
		}
	})
	for _, mode := range []string{"host", "wrong-parent", "wrong-controller", "missing-proof", "duplicate", "foreign-partition"} {
		t.Run(mode, func(t *testing.T) {
			changed := req
			changed.PrerequisiteDeletions = slices.Clone(req.PrerequisiteDeletions)
			at := -1
			for i, p := range changed.PrerequisiteDeletions {
				if insightsLegacyKind(p.Asset.Identity.NativeType).kind != "" {
					at = i
					break
				}
			}
			if at < 0 {
				t.Fatal("legacy fixture missing")
			}
			target := &changed.PrerequisiteDeletions[at]
			switch mode {
			case "host":
				target.Asset.Identity.NativeID = strings.Replace(target.Asset.Identity.NativeID, "management.azure.com", "untrusted.example", 1)
			case "wrong-parent":
				target.Asset.Identity.NativeID = strings.Replace(target.Asset.Identity.NativeID, "/components/app/", "/components/other/", 1)
			case "wrong-controller":
				target.ControllerID = "unrelated-member"
			case "missing-proof":
				target.Asset.Normalized = maps.Clone(target.Asset.Normalized)
				delete(target.Asset.Normalized, insightsChildProofKey(target.Asset.Identity.NativeType))
			case "duplicate":
				duplicate := *target
				duplicate.Asset.ID = "duplicate-legacy"
				changed.PrerequisiteDeletions = append(changed.PrerequisiteDeletions, duplicate)
			case "foreign-partition":
				target.Asset.Identity.Partition = "other"
			}
			if out, err := c.resourceGroupProductRequests(changed); err == nil || out != nil {
				t.Fatal("invalid legacy prerequisite accepted", mode)
			}
		})
	}

}

func TestResourceGroupInsightsNativeLifecycle(t *testing.T) {
	for _, mode := range []string{"managed", "shared", "ampls"} {
		t.Run(mode, func(t *testing.T) { resourceGroupInsightsNativeLifecycle(t, mode) })
	}
}

func resourceGroupInsightsNativeLifecycle(t *testing.T, mode string) {
	t.Helper()
	f := newInsightsComponentFixture(t)
	if mode == "shared" {
		delete(f.managed, "managedBy")
		delete(f.group, "type") // ResourceGroup GET may omit the synthetic inventory type.
	}
	f.group["properties"] = map[string]any{"provisioningState": "Succeeded"}
	storageLists := func(q *http.Request) (*http.Response, bool) {
		path := strings.ToLower(q.URL.Path)
		for _, suffix := range []string{"blobservices/default/containers", "fileservices/default/shares", "queueservices/default/queues", "tableservices/default/tables"} {
			if path == f.groupID+"/providers/microsoft.storage/storageaccounts/storageaccountname/"+suffix {
				if q.Method != "GET" || q.URL.Query().Get("api-version") != "2023-05-01" {
					t.Fatal("unexpected storage list", q.Method, q.URL)
				}
				return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
			}
		}
		return nil, false
	}
	f.response = storageLists
	var extraKinds []string
	var links map[string]map[string]any
	var externalScope string
	if mode == "ampls" {
		externalScope = strings.Replace(strings.ToLower(resourceID(monitorPrivateLinkType, "shared-scope")), "/resourcegroups/test/", "/resourcegroups/shared-links/", 1)
		_, links = insightsComponentPrivateLinksAt(t, f, externalScope)
		extraKinds = []string{monitorPrivateLinkType, monitorScopedResourceType}
	}
	_, _, values := insightsComponentPlan(t, f, extraKinds...)
	var group asset.Asset
	for _, value := range values {
		if value.Identity.NativeID == f.groupID {
			group = value
		}
	}
	req, input := dnsRequest(t, f.runtime, values, group)
	req.IdempotencyKey = "group-insights-native"
	planned, err := plan.Solve(input)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", group)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(t.Context(), req); err == nil || len(f.deletes) != 0 {
		t.Fatal("group ignored Insights prerequisites", err)
	}
	if mode == "ampls" {
		c, err := f.runtime.resolve(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		for _, change := range []string{"target", "workspace-proof", "owner", "partition", "retained"} {
			t.Run("association-"+change, func(t *testing.T) {
				changed := req
				changed.PrerequisiteDeletions = slices.Clone(req.PrerequisiteDeletions)
				at := slices.IndexFunc(changed.PrerequisiteDeletions, func(p contracts.ActionImpact) bool {
					return p.Asset.Identity.NativeType == monitorScopedResourceType && text(p.Asset.Normalized["linkedResourceId"]) == f.workspaceID
				})
				if at < 0 {
					t.Fatal("workspace association missing")
				}
				p := &changed.PrerequisiteDeletions[at]
				p.Asset.Normalized = maps.Clone(p.Asset.Normalized)
				switch change {
				case "target":
					p.Asset.Normalized["linkedResourceId"] = resourceID(insightsWorkspaceType, "unrelated")
				case "workspace-proof":
					p.Asset.Normalized["_monitor_private_link_private_configuration"] = "changed"
				case "owner":
					p.ControllerID = "unrelated-owner"
				case "partition":
					p.Asset.Identity.Partition = "other"
				case "retained":
					p.Delete = false
				}
				if _, err := c.resourceGroupProductRequests(changed); err == nil {
					t.Fatal("invalid external association accepted", change)
				}
			})
		}
	}
	deleteInsightsPrerequisites(t, f, planned, values, group.ID)
	for _, change := range []string{"component-settings", "workspace-settings", "managed-owner", "group-type", "group-tags", "legacy-forbidden"} {
		if mode == "shared" && (change == "workspace-settings" || change == "managed-owner") {
			continue
		}
		t.Run("reject-"+change, func(t *testing.T) {
			original := f.response
			defer func() { f.response = original }()
			f.response = func(q *http.Request) (*http.Response, bool) {
				path := strings.ToLower(q.URL.Path)
				var raw map[string]any
				switch {
				case change == "component-settings" && path == f.parentID:
					raw = maps.Clone(f.parent)
					raw["properties"] = maps.Clone(object(f.parent["properties"]))
					object(raw["properties"])["publicNetworkAccessForIngestion"] = "Disabled"
				case change == "workspace-settings" && path == f.workspaceID:
					raw = maps.Clone(f.workspace)
					raw["properties"] = maps.Clone(object(f.workspace["properties"]))
					object(raw["properties"])["retentionInDays"] = float64(90)
				case change == "managed-owner" && path == f.managedID:
					raw = maps.Clone(f.managed)
					raw["managedBy"] = resourceID(applicationInsightsType, "other")
				case change == "group-type" && path == f.groupID:
					raw = maps.Clone(f.group)
					raw["type"] = storageType
				case change == "group-tags" && path == f.groupID:
					raw = maps.Clone(f.group)
					raw["tags"] = map[string]any{"changed": "yes"}
				case change == "legacy-forbidden":
					for _, p := range req.PrerequisiteDeletions {
						if insightsLegacyKind(p.Asset.Identity.NativeType).kind != "" && q.URL.Path == strings.TrimPrefix(strings.Split(p.Asset.Identity.NativeID, "?")[0], "https://management.azure.com") {
							return jsonResponse(403, map[string]any{}, nil), true
						}
					}
				}
				if raw != nil {
					if q.Method != "GET" {
						t.Fatal("mutated changed resource", q.Method, q.URL)
					}
					return jsonResponse(200, raw, nil), true
				}
				return original(q)
			}
			before := len(f.deletes)
			if _, err := driver.Execute(t.Context(), req); err == nil || len(f.deletes) != before {
				t.Fatal("group failed to enforce product boundary", change, err)
			}
		})
	}
	check, err := driver.Preflight(t.Context(), req)
	if err != nil || !check.Allowed {
		t.Fatal("Insights composed group preflight", check, err)
	}
	if mode == "ampls" {
		if len(links) != 0 || len(req.PrerequisiteDeletions) != 15 {
			t.Fatal("external AMPLS prerequisites missing", len(links), len(req.PrerequisiteDeletions))
		}
		for _, impact := range req.LifecycleImpacts {
			if impact.Asset.Identity.NativeID == externalScope {
				t.Fatal("group acquired external scope")
			}
		}
	}
	rootGone := false
	nativeResponse := f.response
	f.response = func(q *http.Request) (*http.Response, bool) {
		path := strings.ToLower(q.URL.Path)
		if q.Method == "DELETE" {
			if path != f.groupID {
				t.Fatal("independent native cascade delete", q.URL)
			}
			f.deletes = append(f.deletes, path)
			rootGone = true
			f.parentGone = true
			return &http.Response{StatusCode: 204, Header: http.Header{}, Body: http.NoBody}, true
		}
		if rootGone && (path == f.groupID || f.groupMembers[path] != nil) {
			return jsonResponse(404, map[string]any{}, nil), true
		}
		return nativeResponse(q)
	}
	result, err := driver.Execute(t.Context(), req)
	if err != nil {
		t.Fatal("native group delete", err)
	}
	phases := []string{"group", "workspace", "absent"}
	if mode == "shared" {
		for _, impact := range req.LifecycleImpacts {
			if impact.Asset.Identity.NativeID == f.workspaceID || impact.Asset.Identity.NativeID == f.managedID {
				t.Fatal("group acquired shared workspace", impact)
			}
		}
		phases = []string{"retained"}
	}
	for _, phase := range phases {
		if phase == "workspace" || phase == "absent" {
			f.groupGone = true
		}
		if phase == "absent" {
			f.workspaceGone = true
		}
		result.Data = groupOperationJSON(t, result.Data)
		wait, err := driver.Wait(t.Context(), req, result)
		if err != nil || wait.Done != (phase == "absent" || phase == "retained") {
			t.Fatal("Insights native residual", phase, wait, err)
		}
		result.Data = wait.Data
	}
	if mode == "ampls" {
		c, err := f.runtime.resolve(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		if current, err := c.request(t.Context(), "GET", apiURL(externalScope, monitorPrivateLinkVersion)); err != nil || current.status != 200 || len(links) != 0 {
			t.Fatal("external shared scope not retained", current.status, err)
		}
	}
	if mode == "shared" && (f.groupGone || f.workspaceGone) {
		t.Fatal("shared workspace changed during group cleanup")
	}
	if len(f.deletes) != len(req.PrerequisiteDeletions)+1 || f.deletes[len(f.deletes)-1] != f.groupID {
		t.Fatal("wrong native mutation set", f.deletes)
	}
}
