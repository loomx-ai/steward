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
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (f *fleetFixture) assets(t *testing.T) []asset.Asset {
	t.Helper()
	var values []asset.Asset
	for _, kind := range fleetTestKinds {
		batch, err := f.runtime.List(t.Context(), f.request(kind))
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range batch.Items {
			values = append(values, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: kind, NativeID: item.NativeID}, Location: item.Location, Name: item.Name, Normalized: item.Normalized})
		}
	}
	// Exercise the same serialized representation used by graph/cleanup jobs.
	encoded, err := json.Marshal(values)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &values); err != nil {
		t.Fatal(err)
	}
	return values
}

func fleetAssetByKind(t *testing.T, values []asset.Asset, kind string) asset.Asset {
	t.Helper()
	for _, value := range values {
		if value.Identity.NativeType == kind {
			return value
		}
	}
	t.Fatal("missing Fleet asset", kind)
	return asset.Asset{}
}

func fleetGraphEmptyIndexes(t *testing.T, req *http.Request) (*http.Response, bool) {
	if fleetPath(req.URL.Path) {
		return nil, false
	}
	if response, ok := emptyMonitorIndexResponse(t, req); ok {
		return response, true
	}
	return emptyDiagnosticSourceIndexResponse(t, req)
}

func TestFleetNativeLifecycleGraph(t *testing.T) {
	f := newFleetFixture(t)
	values := f.assets(t)
	f.override = func(req *http.Request) (*http.Response, bool) { return fleetGraphEmptyIndexes(t, req) }
	lifecycle, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
	if err != nil {
		t.Fatal(err)
	}
	root, run, gate := fleetAssetByKind(t, values, fleetType), fleetAssetByKind(t, values, fleetRunType), fleetAssetByKind(t, values, fleetGateType)
	member, namespace := fleetAssetByKind(t, values, fleetMemberType), fleetAssetByKind(t, values, fleetNamespaceType)
	strategy, profile := fleetAssetByKind(t, values, fleetStrategyType), fleetAssetByKind(t, values, fleetProfileType)
	if len(contribution.Bindings) != 6 {
		t.Fatal("Fleet lost explicit child lifecycles", contribution.Bindings)
	}
	for _, binding := range contribution.Bindings {
		if binding.ManagedAssetID == gate.ID {
			if binding.ControllerAssetID != run.ID || binding.CleanupPolicy != graph.CleanupDelegate || binding.DirectCleanupAllowed || binding.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] != true {
				t.Fatal("Gate acquired an independent delete or the wrong owner", binding)
			}
		} else if binding.ControllerAssetID != root.ID || binding.CleanupPolicy != graph.CleanupDirect || binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != nil {
			t.Fatal("Fleet bypassed a child's native lifecycle", binding)
		}
	}
	profileFirst, namespaceFirst := false, false
	for _, relation := range contribution.Relationships {
		if relation.SourceAssetID == strategy.ID && relation.TargetAssetID == profile.ID && relation.Type == graph.RelationshipDependsOn {
			profileFirst = relation.Evidence[graph.RelationshipEvidenceRequiredDeletion] == true && relation.Evidence[graph.RelationshipEvidenceAutomaticSelection] == false
		}
		if relation.SourceAssetID == member.ID && relation.TargetAssetID == namespace.ID && relation.Type == graph.RelationshipDependsOn {
			namespaceFirst = relation.Evidence[graph.RelationshipEvidenceAutomaticSelection] == false && relation.Evidence["placement_dynamic"] == true
		}
		if relation.SourceAssetID == run.ID && (relation.TargetAssetID == profile.ID || relation.TargetAssetID == strategy.ID) {
			t.Fatal("Run provenance became a current dependency", relation)
		}
	}
	if !profileFirst || !namespaceFirst {
		t.Fatal("Fleet lost shared/dynamic prerequisites", contribution.Relationships)
	}
	// Only independently referenced AKS/subnet/identity IDs can be unresolved;
	// no request may fetch these foreign or externally managed configurations.
	for _, ref := range contribution.Unresolved {
		if fleetKind(ref.NativeType).kind != "" || ref.Relationship != graph.RelationshipUses {
			t.Fatal("native Fleet child lost from graph", ref)
		}
	}
	for call := range f.calls {
		if strings.Contains(call, "/managedclusters/") {
			t.Fatal("member enrollment became cluster ownership", call)
		}
	}
}

func TestFleetLifecycleReconcilesKnownChildrenAndNativeGateTargets(t *testing.T) {
	for _, scenario := range []string{"omitted child", "omitted run discovered by gate", "missing gate asset", "orphan gate", "forbidden child", "collection 404", "changed child", "changed parent", "retargeted gate", "foreign gate"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFleetFixture(t)
			values := f.assets(t)
			root, run, gate := fleetAssetByKind(t, values, fleetType), fleetAssetByKind(t, values, fleetRunType), fleetAssetByKind(t, values, fleetGateType)
			member := fleetAssetByKind(t, values, fleetMemberType)
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			clear(f.calls)
			var override func(*http.Request) (*http.Response, bool)
			switch scenario {
			case "omitted child":
				f.omitted[member.Identity.NativeID] = true
			case "omitted run discovered by gate":
				f.omitted[run.Identity.NativeID] = true
				values = slices.DeleteFunc(values, func(v asset.Asset) bool { return v.ID == run.ID })
			case "missing gate asset":
				values = slices.DeleteFunc(values, func(v asset.Asset) bool { return v.ID == gate.ID })
			case "orphan gate":
				delete(f.resources, run.Identity.NativeID)
			case "forbidden child", "collection 404":
				override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, member.Identity.NativeID) && scenario == "forbidden child" {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					if strings.EqualFold(req.URL.Path, root.Identity.NativeID+"/members") && scenario == "collection 404" {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
					}
					return nil, false
				}
			case "changed child":
				object(f.resources[member.Identity.NativeID]["properties"])["futurePrivateSetting"] = "changed"
			case "changed parent":
				f.resources[root.Identity.NativeID]["tags"] = map[string]any{"steward:protected": "true"}
			case "retargeted gate", "foreign gate":
				target := run.Identity.NativeID + "changed"
				if scenario == "foreign gate" {
					target = strings.Replace(run.Identity.NativeID, "/fleets/fleet1/", "/fleets/other/", 1)
				}
				object(object(f.resources[gate.Identity.NativeID]["properties"])["target"])["id"] = target
			}
			f.override = func(req *http.Request) (*http.Response, bool) {
				if override != nil {
					if response, ok := override(req); ok {
						return response, true
					}
				}
				return fleetGraphEmptyIndexes(t, req)
			}
			contribution, err := (&serviceCascades{client: c}).Contribute(t.Context(), "scope", values)
			switch scenario {
			case "omitted child":
				if err != nil || len(contribution.Bindings) != 6 || f.calls["GET "+member.Identity.NativeID] < 2 {
					t.Fatal("omitted live child escaped its lifecycle", contribution, err)
				}
			case "omitted run discovered by gate", "missing gate asset":
				id := run.Identity.NativeID
				if scenario == "missing gate asset" {
					id = gate.Identity.NativeID
				}
				if err != nil || !slices.ContainsFunc(contribution.Unresolved, func(ref graph.UnresolvedReference) bool {
					return ref.NativeID == id && ref.Relationship == graph.RelationshipAttachedTo
				}) {
					t.Fatal("unindexed native child was not blocked", contribution, err)
				}
			default:
				if err == nil || isNotFound(err) {
					t.Fatal("unsafe native graph read became child absence", contribution, err)
				}
			}
		})
	}
}

func TestFleetLifecyclePrivateProofAndTwoPassChanges(t *testing.T) {
	for _, scenario := range []string{"proof", "reference", "location", "dynamic placement", "group", "late child", "late gate"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFleetFixture(t)
			values := f.assets(t)
			root, namespace := fleetAssetByKind(t, values, fleetType), fleetAssetByKind(t, values, fleetNamespaceType)
			c, _ := f.runtime.resolve(t.Context(), "connection")
			planned := namespace
			planned.Normalized = maps.Clone(planned.Normalized)
			clear(f.calls)
			switch scenario {
			case "proof":
				planned.Normalized[fleetConfigurationProof] = "forged"
			case "reference":
				planned.Normalized["_fleet_references"] = map[string]any{fleetMemberType: []string{root.Identity.NativeID + "/members/not-reviewed"}}
			case "location":
				planned.Location = "westus"
			case "dynamic placement":
				planned.Normalized["placement_dynamic"] = false
			case "group":
				f.group["managedBy"] = resourceID(aksType, "different-owner")
			case "late child", "late gate":
				collection := root.Identity.NativeID + "/members"
				if scenario == "late gate" {
					collection = root.Identity.NativeID + "/gates"
				}
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, collection) && f.calls["GET "+collection] == 2 {
						kind, name := fleetMemberType, "late"
						if scenario == "late gate" {
							kind, name = fleetGateType, "44444444-4444-4444-4444-444444444444"
						}
						raw := fleetTestBody(t, kind, name)
						f.resources[text(raw["id"])] = raw
					}
					return nil, false
				}
			}
			var err error
			if strings.HasPrefix(scenario, "late") {
				_, err = c.fleetChildren(t.Context(), root.Identity, f.resources[root.Identity.NativeID], values...)
			} else {
				_, err = c.contributeFleetReferences(t.Context(), planned, values)
			}
			if err == nil || isNotFound(err) {
				t.Fatal("Fleet private/temporal boundary did not fail closed", err)
			}
			if slices.Contains([]string{"proof", "reference", "location", "dynamic placement"}, scenario) && len(f.calls) != 0 {
				t.Fatal("tampered saved proof reached a native API", f.calls)
			}
		})
	}
	// The shared cascade driver must also authenticate private gate fields.
	f := newFleetFixture(t)
	values := f.assets(t)
	run, gate := fleetAssetByKind(t, values, fleetRunType), fleetAssetByKind(t, values, fleetGateType)
	kind, _ := findType(fleetRunType)
	c, _ := f.runtime.resolve(t.Context(), "connection")
	a := action{client: c, kind: kind, id: run.Identity.NativeID}
	request := contracts.ActionRequest{Asset: run, LifecycleImpacts: []contracts.ActionImpact{{Asset: gate, ControllerID: run.ID, Delete: true}}}
	if _, err := a.serviceImpacts(request); err != nil {
		t.Fatal("native Gate target lost its run controller", err)
	}
	gate.Normalized = maps.Clone(gate.Normalized)
	gate.Normalized["_fleet_references"] = map[string]any{fleetRunType: []string{run.Identity.NativeID}}
	request.LifecycleImpacts[0].Asset = gate
	if _, err := a.serviceImpacts(request); err == nil {
		t.Fatal("cascade request accepted a forged Gate owner projection")
	}
	if err := c.servicePrivateIncarnation(gate, f.resources[gate.Identity.NativeID]); err == nil {
		t.Fatal("cascade accepted an altered private Gate reference proof")
	}
}
