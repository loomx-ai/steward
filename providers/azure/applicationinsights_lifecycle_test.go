package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func insightsLifecycleAssets(t *testing.T, f *insightsInventoryFixture) []asset.Asset {
	t.Helper()
	var values []asset.Asset
	for _, kind := range append([]string{applicationInsightsType}, insightsComponentChildKinds()...) {
		request := productRequest(f.runtime, kind)
		page, err := f.runtime.List(t.Context(), request)
		if err != nil || !page.Complete {
			t.Fatal("native lifecycle inventory failed", kind, err)
		}
		for _, item := range page.Items {
			capabilities := asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityDetailed}
			if item.Actionable != nil && *item.Actionable {
				capabilities = append(capabilities, asset.CapabilityActionable)
			}
			values = append(values, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: request.ConnectionID, Partition: "azure", NativeType: item.NativeType, NativeID: item.NativeID}, Location: item.Location, Normalized: item.Normalized, Capabilities: capabilities})
		}
	}
	return values
}

func TestApplicationInsightsComponentNativeChildPlan(t *testing.T) {
	f := newInsightsARMInventoryFixture(t)
	// Add the ten case-distinct legacy children to the three original ARM
	// examples. The fixture continues to use each API's native response shape.
	legacy := newInsightsInventoryFixture(t)
	maps.Copy(f.children, legacy.children)
	values := insightsLifecycleAssets(t, f.insightsInventoryFixture)
	parent := values[0]
	contributor, err := f.runtime.ServiceLifecycle(t.Context(), parent.Identity.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	result, err := contributor.Contribute(t.Context(), "scope", values)
	if err != nil || len(result.Bindings) != 13 || len(result.Relationships) != 13 || len(result.Unresolved) != 0 {
		t.Fatal("native child lifecycle was incomplete", result, err)
	}
	seen := map[asset.AssetID]bool{}
	for _, binding := range result.Bindings {
		if seen[binding.ManagedAssetID] || binding.ControllerAssetID != parent.ID || binding.CleanupPolicy != graph.CleanupDirect || !binding.DirectCleanupAllowed || binding.Authority != graph.AuthorityAuthoritative || binding.Ownership != graph.OwnershipExclusive || binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != nil || binding.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] != nil {
			t.Fatal("independent DELETE was replaced by a controller assumption", binding)
		}
		seen[binding.ManagedAssetID] = true
	}
	// The production component remains read-only until its separate workspace
	// deletion driver is complete. Exercise the actual solver's future root
	// ordering here without advertising that unfinished action in the catalog.
	values[0].Capabilities = append(values[0].Capabilities, asset.CapabilityActionable)
	input := plan.Input{Assets: values, Relationships: result.Relationships, LifecycleBindings: result.Bindings, ResolvedAssetIDs: []asset.AssetID{parent.ID}}
	planned, err := plan.Solve(input)
	if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 14 || planned.Steps[len(planned.Steps)-1].AssetID != parent.ID {
		t.Fatal("native children did not precede their controller", planned, err)
	}
	request := servicePlanRequest(planned, values, parent)
	if len(request.LifecycleImpacts) != 0 || len(request.PrerequisiteDeletions) != 13 {
		t.Fatal("native children were not persisted as separate prerequisites", request)
	}
	for _, value := range values[1:] {
		input.ResolvedAssetIDs = []asset.AssetID{value.ID}
		planned, err := plan.Solve(input)
		if err != nil || len(planned.Blockers) != 0 || len(planned.Steps) != 1 || planned.Steps[0].AssetID != value.ID {
			t.Fatal("independent child cleanup selected its controller or siblings", planned, err)
		}
	}
	input.ResolvedAssetIDs = []asset.AssetID{parent.ID}
	input.RequestOptions = map[asset.AssetID]map[string]any{parent.ID: {"retain_resources": []string{string(values[1].ID)}}}
	if retained, err := plan.Solve(input); err != nil || len(retained.Blockers) == 0 {
		t.Fatal("component cleanup silently retained its owned configuration", retained, err)
	}
	payload, _ := json.Marshal(result)
	if strings.Contains(string(payload), "PRIVATE_QUERY") || strings.Contains(string(payload), "PRIVATE_KEY") {
		t.Fatal("private native configuration escaped into graph evidence")
	}
}

func TestApplicationInsightsComponentChildGraphBoundaries(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate", "other-connection", "other-partition", "wrong-kind", "parent-proof", "child-proof", "parent-location", "child-location", "parent-replaced", "new-child", "private-change", "case-distinct", "stale-absent", "omitted-live", "omitted-denied", "list-denied", "list-not-found", "list-partial", "list-lro", "list-continuation", "list-code", "parent-gone", "parent-partial", "protected", "controller-only"} {
		t.Run(mode, func(t *testing.T) {
			f := newInsightsInventoryFixture(t)
			if mode == "protected" {
				f.group["tags"] = map[string]any{"steward:protected": "true"}
			} else if mode == "controller-only" {
				f.group["managedBy"] = resourceID(aksType, "cluster")
			}
			values := insightsLifecycleAssets(t, f)
			parent, first := values[0], values[1]
			missing := false
			switch mode {
			case "missing":
				values = append(values[:1], values[2:]...)
				missing = true
			case "duplicate":
				copy := first
				copy.ID = "second-asset-for-the-same-native-selector"
				values = append(values, copy)
			case "other-connection":
				values[1].Identity.ConnectionID = "another-connection"
				missing = true
			case "other-partition":
				values[1].Identity.Partition = "another-partition"
				missing = true
			case "wrong-kind":
				values[1].Identity.NativeType = insightsMyAnalyticsType
			case "parent-proof":
				values[0].Normalized["_monitor_private_link_target_configuration"] = "changed"
			case "child-proof":
				values[1].Normalized[insightsChildProofKey(first.Identity.NativeType)] = "changed"
			case "protected", "controller-only":
				// Inherit the actual native resource-group protection above.
			case "parent-location":
				values[0].Location = "westus"
			case "child-location":
				values[1].Location = "westus"
			case "parent-replaced":
				object(f.parent["properties"])["AppId"] = "replacement"
			case "new-child", "private-change", "case-distinct":
				passes := 0
				f.before = func(req *http.Request) {
					if !strings.EqualFold(req.URL.Path, f.parentID+"/analyticsItems") {
						return
					}
					passes++
					if passes != 2 {
						return
					}
					if mode == "private-change" {
						f.children[first.Identity.NativeID]["Content"] = "CHANGED_QUERY"
					} else {
						selector := "NewID"
						if mode == "case-distinct" {
							selector = "OPAQUEID"
						}
						id, _ := insightsLegacyURL(f.parentID, insightsAnalyticsType, selector)
						f.children[id] = map[string]any{"Id": selector}
					}
				}
			case "stale-absent":
				delete(f.children, first.Identity.NativeID)
			case "omitted-live", "omitted-denied":
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, f.parentID+"/analyticsItems") {
						rows := []any{}
						for id, raw := range f.children {
							_, _, kind, _, _ := insightsChildIdentity(id)
							if kind == insightsAnalyticsType && id != first.Identity.NativeID {
								rows = append(rows, raw)
							}
						}
						return jsonResponse(200, rows, nil), true
					}
					if mode == "omitted-denied" && req.URL.Query().Get("id") == "OpaqueID" {
						return jsonResponse(403, map[string]any{}, nil), true
					}
					return nil, false
				}
			default:
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.HasPrefix(mode, "list-") && strings.EqualFold(req.URL.Path, f.parentID+"/analyticsItems") {
						switch mode {
						case "list-denied":
							return jsonResponse(403, map[string]any{}, nil), true
						case "list-not-found":
							return jsonResponse(404, map[string]any{}, nil), true
						case "list-partial":
							return jsonResponse(206, []any{}, nil), true
						case "list-lro":
							return jsonResponse(200, []any{}, http.Header{"Location": {apiURL(f.parentID, insightsComponentVersion)}}), true
						case "list-continuation":
							return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": req.URL.String()}, nil), true
						case "list-code":
							return jsonResponse(200, map[string]any{"value": []any{}, "code": "Incomplete"}, nil), true
						}
					}
					if strings.HasPrefix(mode, "parent-") && strings.EqualFold(req.URL.Path, f.parentID) {
						status := 404
						if mode == "parent-partial" {
							status = 206
						}
						return jsonResponse(status, f.parent, nil), true
					}
					return nil, false
				}
			}
			contributor, err := f.runtime.ServiceLifecycle(t.Context(), parent.Identity.ConnectionID)
			if err != nil {
				t.Fatal(err)
			}
			result, err := contributor.Contribute(t.Context(), "scope", values)
			if missing {
				if err != nil || len(result.Unresolved) != 1 || len(result.Bindings) != 9 || result.Unresolved[0].NativeID != first.Identity.NativeID || result.Unresolved[0].ControllerID != parent.ID {
					t.Fatal("unresolved native child was silently omitted", result, err)
				}
			} else if mode == "protected" || mode == "controller-only" {
				if err != nil || !slices.ContainsFunc(result.Bindings, func(binding graph.LifecycleBinding) bool {
					return binding.ManagedAssetID == first.ID && !binding.DirectCleanupAllowed
				}) {
					t.Fatal("protected child gained direct cleanup permission", result, err)
				}
				// The application supplies plan protections separately. Verify
				// the registered native action's final protection gate here.
				driver, err := f.runtime.ResolveAction(t.Context(), parent.Identity.ConnectionID, first)
				if err != nil {
					t.Fatal(err)
				}
				check, err := driver.Preflight(t.Context(), contracts.ActionRequest{Asset: first, Action: "delete"})
				if err != nil || check.Allowed {
					t.Fatal("native protection was lost before independent deletion", check, err)
				}
			} else if mode == "stale-absent" {
				if err != nil || len(result.Bindings) != 9 || slices.ContainsFunc(result.Bindings, func(binding graph.LifecycleBinding) bool { return binding.ManagedAssetID == first.ID }) {
					t.Fatal("exact native absence was not reconciled", result, err)
				}
			} else if err == nil {
				t.Fatal("incomplete or changed native child scope gained authority", mode, result)
			}
			if len(f.deletes) != 0 {
				t.Fatal("graph discovery mutated native resources")
			}
		})
	}
}
