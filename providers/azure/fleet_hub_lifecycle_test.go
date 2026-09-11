package azure

import (
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

func (h *fleetHubFixture) graphAssets(t *testing.T) []asset.Asset {
	t.Helper()
	values := h.fleetFixture.assets(t)
	c, _ := h.runtime.resolve(t.Context(), "connection")
	owners := map[string]string{}
	for id, group := range h.groups {
		owners[id] = text(group["managedBy"])
	}
	resources := maps.Clone(h.resources)
	maps.Copy(resources, h.groups)
	for _, id := range slices.Sorted(maps.Keys(resources)) {
		item, err := h.runtime.inventoryItem(t.Context(), c, resources[id], owners, nil)
		if err != nil {
			t.Fatal("cannot normalize native Hub asset", id, err)
		}
		values = append(values, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: item.NativeID, NativeType: item.NativeType}, Location: item.Location, Name: item.Name, Normalized: item.Normalized})
	}
	data, _ := json.Marshal(values)
	if err := json.Unmarshal(data, &values); err != nil {
		t.Fatal(err)
	}
	h.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if _, listed := h.lists[path]; listed || h.resources[path] != nil || h.groups[path] != nil || strings.HasSuffix(path, "/resourcegroups") || h.groups[strings.TrimSuffix(path, "/resources")] != nil {
			return nil, false
		}
		return fleetGraphEmptyIndexes(t, req)
	}
	return values
}

func fleetHubContributions(t *testing.T, h *fleetHubFixture, values []asset.Asset) (governance.Contribution, error) {
	t.Helper()
	service, err := h.runtime.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	clusters, err := h.runtime.ClusterLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	var result governance.Contribution
	// Match the production resolver order, including its static contributor.
	for _, contributor := range []governance.Contributor{NewResourceAttachments(), service, clusters} {
		current, err := contributor.Contribute(t.Context(), "scope", values)
		if err != nil {
			return governance.Contribution{}, err
		}
		result.Bindings = append(result.Bindings, current.Bindings...)
		result.Relationships = append(result.Relationships, current.Relationships...)
		result.Unresolved = append(result.Unresolved, current.Unresolved...)
	}
	return result, nil
}

func TestFleetHubNativeLifecycleDelegation(t *testing.T) {
	h := newFleetHubMembersFixture(t)
	values := h.graphAssets(t)
	root := fleetAssetByKind(t, values, fleetType)
	members := object(object(root.Normalized[fleetHubState])["members"])
	built, err := fleetHubContributions(t, h, values)
	if err != nil {
		t.Fatal("combined native Hub graph failed", err)
	}
	seen := map[asset.AssetID]bool{}
	for _, binding := range built.Bindings {
		if seen[binding.ManagedAssetID] {
			t.Fatal("native Hub member acquired multiple lifecycle owners", binding)
		}
		seen[binding.ManagedAssetID] = true
		if binding.EvidenceSource == fleetHubSource {
			if members[string(binding.ManagedAssetID)] == nil || binding.ControllerAssetID != root.ID || binding.Authority != graph.AuthorityAuthoritative || binding.Ownership != graph.OwnershipExclusive || binding.CleanupPolicy != graph.CleanupDelegate || binding.DirectCleanupAllowed || binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true || binding.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] != true {
				t.Fatal("Hub escaped its verified Fleet owner or lost native residual verification", binding)
			}
		}
	}
	for id := range members {
		if !seen[asset.AssetID(id)] {
			t.Fatal("native Hub member missing from lifecycle graph", id)
		}
	}
	for _, reference := range built.Unresolved {
		if reference.Evidence["managed_resource_group"] != nil {
			t.Fatal("normalized Hub member remained unresolved", reference)
		}
	}
	for call := range h.calls {
		if !strings.HasPrefix(call, "GET ") && !strings.HasPrefix(call, "POST ") {
			t.Fatal("graph discovery mutated a native resource", call)
		}
	}
}

func TestFleetHubLifecycleBoundaries(t *testing.T) {
	for _, mode := range []string{"omitted-member", "missing-asset", "foreign-connection", "foreign-partition", "duplicate-native-id", "duplicate-asset-id", "blank-asset-id", "wrong-kind", "changed-native", "changed-normalized", "changed-hub-node-group", "tampered-proof", "tampered-member", "changed-group-owner", "new-member", "unobserved-asset", "known-404", "own-403", "index-404", "unknown-omitted", "unknown-changed-between-observations"} {
		t.Run(mode, func(t *testing.T) {
			h := newFleetHubMembersFixture(t)
			values := h.graphAssets(t)
			root := fleetAssetByKind(t, values, fleetType)
			id := h.hub + "/providers/microsoft.insights/actiongroups/hub-alerts"
			index := slices.IndexFunc(values, func(value asset.Asset) bool { return value.Identity.NativeID == id })
			unknown := h.hub + "/providers/contoso.example/controllers/custom"
			fallback := h.override
			switch mode {
			case "omitted-member", "known-404", "own-403":
				h.lists["/subscriptions/"+testSubscription+"/providers/microsoft.insights/actiongroups"] = []any{}
				if mode == "known-404" {
					h.gone[id] = true
				}
				if mode == "own-403" {
					h.override = func(req *http.Request) (*http.Response, bool) {
						if strings.EqualFold(req.URL.Path, id) {
							return jsonResponse(403, map[string]any{}, nil), true
						}
						return fallback(req)
					}
				}
			case "missing-asset":
				values = slices.Delete(values, index, index+1)
			case "foreign-connection":
				values[index].Identity.ConnectionID = "other"
			case "foreign-partition":
				values[index].Identity.Partition = "other"
			case "duplicate-native-id":
				duplicate := values[index]
				duplicate.ID = "duplicate"
				values = append(values, duplicate)
			case "duplicate-asset-id":
				values[index].ID = asset.AssetID(h.cluster)
			case "blank-asset-id":
				values[index].ID = ""
			case "wrong-kind":
				values[index].Identity.NativeType = vmType
			case "changed-native":
				object(h.resources[id]["properties"])["futureSetting"] = "changed"
			case "changed-normalized":
				values[index].Normalized[monitorConfigurationProof] = "changed"
			case "changed-hub-node-group":
				hub := fleetAssetByKind(t, values, aksType)
				hub.Normalized["nodeResourceGroup"] = "shared"
			case "tampered-proof":
				root.Normalized[fleetHubProof] = "forged"
			case "tampered-member":
				delete(object(object(root.Normalized[fleetHubState])["members"]), id)
			case "changed-group-owner":
				h.groups[h.nodes]["managedBy"] = resourceID(aksType, "unrelated")
			case "new-member", "unobserved-asset":
				copy := maps.Clone(h.resources[unknown])
				copy["id"], copy["name"] = unknown+"2", "custom2"
				if mode == "new-member" {
					h.resources[unknown+"2"] = copy
				} else {
					values = append(values, dnsAsset(t, h.runtime, copy))
				}
			case "index-404":
				h.gone[h.hub+"/resources"] = true
			case "unknown-omitted":
				h.omit[unknown] = true
			case "unknown-changed-between-observations":
				passes := 0
				h.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, h.hub+"/resources") {
						passes++
						if passes == 3 {
							object(h.resources[unknown]["properties"])["authored"] = "changed"
						}
					}
					return fallback(req)
				}
			}
			before := len(h.calls)
			built, err := fleetHubContributions(t, h, values)
			if slices.Contains([]string{"omitted-member", "missing-asset", "foreign-connection", "foreign-partition"}, mode) {
				if err != nil {
					t.Fatal("valid Hub reconciliation failed", err)
				}
				missing := mode != "omitted-member"
				found := slices.ContainsFunc(built.Unresolved, func(ref graph.UnresolvedReference) bool {
					return ref.NativeID == id && ref.ControllerID == root.ID && ref.ConnectionID == root.Identity.ConnectionID
				})
				if found != missing {
					t.Fatal("missing/foreign Hub asset did not retain unresolved ownership", built.Unresolved)
				}
				return
			}
			if err == nil || len(built.Bindings)+len(built.Relationships) != 0 {
				t.Fatal("invalid Hub graph was usable", built, err)
			}
			if strings.HasPrefix(mode, "tampered-") && len(h.calls) != before {
				t.Fatal("invalid Hub receipt reached native discovery")
			}
		})
	}
}
