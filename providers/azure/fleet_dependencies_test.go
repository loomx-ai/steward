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

func TestFleetUnindexedIncomingReferencesAndKnownAbsence(t *testing.T) {
	for _, scenario := range []string{"unindexed", "type casing", "omitted source", "omitted parent", "source absent", "both absent", "parent absent source live", "forbidden source", "forbidden parent", "collection 404", "late source", "late change"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFleetFixture(t)
			values := f.assets(t)
			root, member := fleetAssetByKind(t, values, fleetType), fleetAssetByKind(t, values, fleetMemberType)
			target := asset.Asset{ID: "aks", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: strings.ToLower(resourceID(aksType, "cluster1")), NativeType: aksType}, Location: "westus"}
			known := []asset.Asset{member}
			clear(f.calls)
			var override func(*http.Request) (*http.Response, bool)
			switch scenario {
			case "type casing":
				target.Identity.NativeType = strings.ToLower(aksType)
			case "unindexed":
				known = nil
			case "omitted source":
				f.omitted[member.Identity.NativeID] = true
			case "omitted parent":
				f.omitted[root.Identity.NativeID], f.omitted[member.Identity.NativeID] = true, true
			case "source absent":
				delete(f.resources, member.Identity.NativeID)
			case "both absent":
				delete(f.resources, member.Identity.NativeID)
				delete(f.resources, root.Identity.NativeID)
			case "parent absent source live":
				delete(f.resources, root.Identity.NativeID)
			case "forbidden source", "forbidden parent", "collection 404":
				override = func(req *http.Request) (*http.Response, bool) {
					path, status := member.Identity.NativeID, 403
					if scenario == "forbidden parent" {
						path = root.Identity.NativeID
					}
					if scenario == "collection 404" {
						path, status = root.Identity.NativeID+"/members", 404
					}
					if strings.EqualFold(req.URL.Path, path) {
						return jsonResponse(status, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					}
					return nil, false
				}
			case "late source", "late change":
				if scenario == "late source" {
					delete(f.resources, member.Identity.NativeID)
				}
				override = func(req *http.Request) (*http.Response, bool) {
					collection := root.Identity.NativeID + "/members"
					if strings.EqualFold(req.URL.Path, collection) && f.calls["GET "+collection] == 2 {
						if scenario == "late source" {
							f.resources[member.Identity.NativeID] = fleetTestBody(t, fleetMemberType, "member1")
						} else {
							object(f.resources[member.Identity.NativeID]["properties"])["futurePrivateSetting"] = "changed-between-passes"
						}
					}
					return nil, false
				}
			}
			f.override = func(req *http.Request) (*http.Response, bool) {
				if override != nil {
					if response, ok := override(req); ok {
						return response, true
					}
				}
				return fleetGraphEmptyIndexes(t, req)
			}
			c, _ := f.runtime.resolve(t.Context(), "connection")
			incoming, err := c.monitorIncomingTargets(t.Context(), []asset.Asset{target}, known...)
			switch scenario {
			case "unindexed", "type casing", "omitted source", "omitted parent":
				if err != nil || len(incoming[target.Identity.NativeID]) != 1 || incoming[target.Identity.NativeID][0].resource.id != member.Identity.NativeID {
					t.Fatal("live Fleet enrollment escaped reverse discovery", incoming, err)
				}
				contribution, err := c.contributeIncomingSources([]asset.Asset{target}, []asset.Asset{target}, incoming)
				if err != nil || len(contribution.Unresolved) != 1 || len(contribution.Bindings)+len(contribution.Relationships) != 0 || contribution.Unresolved[0].Relationship != graph.RelationshipDependsOn || contribution.Unresolved[0].Evidence[graph.RelationshipEvidenceAutomaticSelection] != false {
					t.Fatal("unindexed Fleet member lost its deletion blocker", contribution, err)
				}
			case "source absent", "both absent":
				if err != nil || len(incoming[target.Identity.NativeID]) != 0 || f.calls["GET "+member.Identity.NativeID] != 2 {
					t.Fatal("Fleet absence did not require the member's own GET", incoming, err, f.calls)
				}
			default:
				if err == nil || isNotFound(err) {
					t.Fatal("failed/changed reverse index became absence", incoming, err)
				}
			}
		})
	}
}

func TestFleetRegisteredTargetDriverRequiresNativeSourceAbsence(t *testing.T) {
	f := newFleetFixture(t)
	values := f.assets(t)
	root := fleetAssetByKind(t, values, fleetType)
	id := strings.ToLower(resourceID(vnetType, "network") + "/subnets/agents")
	target := asset.Asset{ID: "agents-subnet", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: id, NativeType: subnetType}, Location: "westus", Normalized: map[string]any{}}
	deleted := false
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, id) {
			if req.Method == "DELETE" {
				deleted = true
				return jsonResponse(204, nil, nil), true
			}
			if deleted {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
			}
			return jsonResponse(200, map[string]any{"id": id, "name": "agents", "type": subnetType, "location": "westus", "properties": map[string]any{}}, nil), true
		}
		return fleetGraphEmptyIndexes(t, req)
	}
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", target)
	if err != nil {
		t.Fatal(err)
	}
	request := contracts.ActionRequest{Asset: target, Action: "delete", IdempotencyKey: "fleet-subnet-prerequisite"}
	if _, err := driver.Execute(t.Context(), request); err == nil || deleted {
		t.Fatal("registered subnet driver deleted a live Fleet dependency", err)
	}
	request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: root, ControllerID: target.ID, Delete: true}}
	if _, err := driver.Execute(t.Context(), request); err == nil || deleted {
		t.Fatal("planned Fleet deletion became native absence", err)
	}
	delete(f.resources, root.Identity.NativeID)
	result, err := driver.Execute(t.Context(), request)
	if err != nil || !deleted {
		t.Fatal("native source absence did not unblock the registered target driver", result, err)
	}
	encoded, _ := json.Marshal(result)
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
		t.Fatal("persisted target receipt failed after source deletion", wait, err)
	}
	if readback, err := driver.Readback(t.Context(), request); err != nil || readback.Exists {
		t.Fatal("registered target did not verify its own absence", readback, err)
	}
	root.Normalized = maps.Clone(root.Normalized)
	root.Normalized["_fleet_references"] = map[string]any{subnetType: []string{id}}
	// Remove the original Fleet context signature as well as changing the
	// projection: even a deleted prerequisite needs its original private proof.
	root.Normalized[fleetContextProof] = "different-context"
	request.PrerequisiteDeletions[0].Asset = root
	clear(f.calls)
	if _, err := driver.Execute(t.Context(), request); err == nil || len(f.calls) != 0 {
		t.Fatal("tampered deleted Fleet prerequisite reached an API", err, f.calls)
	}
}

func TestFleetIncomingPlacementIdentityAndGateOwnership(t *testing.T) {
	f := newFleetFixture(t)
	rootID := strings.ToLower(resourceID(fleetType, "fleet1"))
	uamiType := "Microsoft.ManagedIdentity/userAssignedIdentities"
	identityID := strings.ToLower(resourceID(uamiType, "fleet-identity"))
	f.resources[rootID]["identity"] = map[string]any{"type": "UserAssigned", "userAssignedIdentities": map[string]any{identityID: map[string]any{}}}
	values := f.assets(t)
	root, run, gate := fleetAssetByKind(t, values, fleetType), fleetAssetByKind(t, values, fleetRunType), fleetAssetByKind(t, values, fleetGateType)
	member, namespace := fleetAssetByKind(t, values, fleetMemberType), fleetAssetByKind(t, values, fleetNamespaceType)
	identity := asset.Asset{ID: "fleet-uami", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: uamiType, NativeID: identityID}, Location: "westus"}
	f.override = func(req *http.Request) (*http.Response, bool) { return fleetGraphEmptyIndexes(t, req) }
	c, _ := f.runtime.resolve(t.Context(), "connection")
	targets := []asset.Asset{identity, member, run}
	incoming, err := c.monitorIncomingTargets(t.Context(), targets, values...)
	if err != nil {
		t.Fatal(err)
	}
	for target, source := range map[string]string{identityID: rootID, member.Identity.NativeID: namespace.Identity.NativeID, run.Identity.NativeID: gate.Identity.NativeID} {
		if len(incoming[target]) != 1 || incoming[target][0].resource.id != source {
			t.Fatal("Fleet source family missing", target, incoming[target])
		}
	}
	guard := &monitorTargetAction{client: c, planned: run}
	request := contracts.ActionRequest{Asset: run, Action: "delete", LifecycleImpacts: []contracts.ActionImpact{{Asset: gate, ControllerID: run.ID, Delete: true}}}
	if err := guard.dependencies(t.Context(), request, []asset.Asset{run, gate}); err != nil {
		t.Fatal("reviewed native Gate blocked its owning Run", err)
	}
	for _, wrong := range []contracts.ActionRequest{{Asset: root, Action: "delete", LifecycleImpacts: request.LifecycleImpacts}, {Asset: run, Action: "delete"}} {
		if err := guard.dependencies(t.Context(), wrong, []asset.Asset{run, gate}); err == nil {
			t.Fatal("unreviewed Gate acquired implicit cascade authority")
		}
	}
	guard = &monitorTargetAction{client: c, planned: member}
	request = contracts.ActionRequest{Asset: member, Action: "delete", PrerequisiteDeletions: []contracts.ActionImpact{{Asset: namespace, ControllerID: member.ID, Delete: true}}}
	if _, _, err := guard.request(t.Context(), request); err == nil {
		t.Fatal("dynamic namespace presence became an empty placement")
	}
	delete(f.resources, namespace.Identity.NativeID)
	if filtered, _, err := guard.request(t.Context(), request); err != nil || len(filtered.PrerequisiteDeletions) != 0 {
		t.Fatal("authenticated dynamic namespace absence did not unblock a member", filtered, err)
	}
	// A different Fleet never acquires the deleted namespace's prerequisite.
	other := member
	other.Identity.NativeID = strings.Replace(member.Identity.NativeID, "/fleets/fleet1/", "/fleets/other/", 1)
	other.ID = "other-member"
	guard.planned, request.Asset = other, other
	request.PrerequisiteDeletions[0].ControllerID = other.ID
	if _, _, err := guard.request(t.Context(), request); err == nil {
		t.Fatal("dynamic placement escaped its Fleet boundary")
	}
	// Saved Gate target references remain private even when their public
	// projection is otherwise syntactically well formed.
	gate.Normalized = maps.Clone(gate.Normalized)
	gate.Normalized[fleetReferencesProof] = "changed"
	entry := incoming[run.Identity.NativeID][0]
	if err := c.fleetIncomingUnchanged(gate, entry); err == nil {
		t.Fatal("forged Gate snapshot acquired native ownership")
	}
	if !slices.Contains(fleetIncomingKinds(aksType), fleetMemberType) {
		t.Fatal("AKS reverse index registration disappeared")
	}
}
