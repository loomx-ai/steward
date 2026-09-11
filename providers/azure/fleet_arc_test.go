package azure

import (
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestFleetMemberClusterReferenceBoundaries(t *testing.T) {
	for _, kind := range []string{aksType, fleetArcClusterType} {
		id := resourceID(kind, "cluster1")
		for _, valid := range []string{id, strings.ToUpper(id), strings.Replace(id, testSubscription, rbacOtherSubscription, 1)} {
			raw := fleetTestBody(t, fleetMemberType, "member1")
			object(raw["properties"])["clusterResourceId"] = valid
			refs, err := fleetReferences(fleetMemberType, raw)
			if err != nil || fleetValidate(fleetMemberType, raw) != nil || len(refs) != 1 || !slices.Equal(refs[kind], []string{strings.ToLower(valid)}) {
				t.Fatal("documented cluster reference rejected", valid, refs, err)
			}
		}
		for _, invalid := range []any{nil, 1, map[string]any{"id": id}, " " + id, id + "\t", id + "/extensions/addon", id + "?x=1", id + "/../other", strings.Replace(id, "/providers/", "/providers/Microsoft.Storage/storageAccounts/storage/providers/", 1), resourceID(vmType, "cluster1")} {
			raw := fleetTestBody(t, fleetMemberType, "member1")
			object(raw["properties"])["clusterResourceId"] = invalid
			if fleetValidate(fleetMemberType, raw) == nil {
				t.Fatal("invalid cluster identity accepted", invalid)
			}
		}
	}
}

func TestFleetArcMemberRegisteredLifecycle(t *testing.T) {
	f := newFleetFixture(t)
	memberID := strings.ToLower(resourceID(fleetType, "fleet1")) + "/members/member1"
	clusterID := strings.ToLower(resourceID(fleetArcClusterType, "arc-cluster"))
	object(f.resources[memberID]["properties"])["clusterResourceId"] = clusterID
	values := f.assets(t)
	member := fleetAssetByKind(t, values, fleetMemberType)
	namespace := fleetAssetByKind(t, values, fleetNamespaceType)
	cluster := asset.Asset{ID: "arc-cluster", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: fleetArcClusterType, NativeID: clusterID}, Location: "westus"}
	values = append(values, cluster)
	f.override = func(req *http.Request) (*http.Response, bool) { return fleetGraphEmptyIndexes(t, req) }
	lifecycle, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := lifecycle.Contribute(t.Context(), "scope", values)
	if err != nil {
		t.Fatal("Arc member did not survive registered inventory and graph", err)
	}
	uses, required := false, false
	for _, binding := range contribution.Bindings {
		if binding.ManagedAssetID == cluster.ID {
			t.Fatal("Arc membership acquired cluster ownership", binding)
		}
	}
	for _, relation := range contribution.Relationships {
		if relation.SourceAssetID == member.ID && relation.TargetAssetID == cluster.ID && relation.Type == graph.RelationshipUses {
			uses = true
		}
		if relation.SourceAssetID == cluster.ID && relation.TargetAssetID == member.ID && relation.Type == graph.RelationshipDependsOn {
			required = relation.Evidence[graph.RelationshipEvidenceRequiredDeletion] == true && relation.Evidence[graph.RelationshipEvidenceAutomaticSelection] == false
		}
	}
	if !uses || !required {
		t.Fatal("Arc lost its explicit enrollment dependency", contribution.Relationships)
	}
	c, _ := f.runtime.resolve(t.Context(), "connection")
	guard := &monitorTargetAction{client: c, planned: cluster}
	clusterRequest := contracts.ActionRequest{Asset: cluster, Action: "delete", PrerequisiteDeletions: []contracts.ActionImpact{{Asset: member, ControllerID: cluster.ID, Delete: true}}}
	if _, _, err := guard.request(t.Context(), clusterRequest); err == nil {
		t.Fatal("planned enrollment cleanup became actual absence")
	}
	request := contracts.ActionRequest{Asset: member, Action: "delete", IdempotencyKey: "arc-member-cleanup", PrerequisiteDeletions: []contracts.ActionImpact{{Asset: namespace, ControllerID: member.ID, Delete: true}}}
	driver := fleetCleanupDriver(t, f, request)
	if _, err := driver.Execute(t.Context(), request); err == nil {
		t.Fatal("Arc member escaped its live managed namespace")
	}
	delete(f.resources, namespace.Identity.NativeID)
	deletes := 0
	f.override = func(req *http.Request) (*http.Response, bool) {
		if req.Method != "GET" {
			fleetAssertMutation(t, f, request, req, "delete")
			deletes++
			object(f.resources[memberID]["properties"])["provisioningState"] = "Deleting"
			return jsonResponse(204, nil, nil), true
		}
		return fleetGraphEmptyIndexes(t, req)
	}
	result, err := driver.Execute(t.Context(), request)
	if err != nil || deletes != 1 {
		t.Fatal("registered Arc enrollment deletion failed", result, err)
	}
	result = fleetSerializedResult(t, result, nil)
	driver = fleetCleanupDriver(t, f, request)
	if waited, err := driver.Wait(t.Context(), request, result); err != nil || waited.Done {
		t.Fatal("DELETE response became member absence", waited, err)
	}
	delete(f.resources, memberID)
	if waited, err := driver.Wait(t.Context(), request, result); err != nil || !waited.Done {
		t.Fatal("Arc enrollment own absence did not complete recovery", waited, err)
	}
	request.ExecutionResult = &result
	if read, err := driver.Readback(t.Context(), request); err != nil || read.Exists {
		t.Fatal("Arc enrollment residual readback failed", read, err)
	}
	if _, err := driver.Execute(t.Context(), request); err != nil || deletes != 1 {
		t.Fatal("recovered Arc enrollment repeated deletion", err, deletes)
	}
	if filtered, _, err := guard.request(t.Context(), clusterRequest); err != nil || len(filtered.PrerequisiteDeletions) != 0 {
		t.Fatal("own enrollment absence did not release its dependency", filtered, err)
	}
	for call := range f.calls {
		_, path, _ := strings.Cut(call, " ")
		_, kind, _ := parseID(path)
		if fleetClusterKind(kind) != "" || strings.HasPrefix(kind, "microsoft.kubernetesconfiguration/") {
			t.Fatal("enrollment read or mutated an external cluster", call)
		}
	}
}

func TestFleetArcReferenceScopeAndDrift(t *testing.T) {
	for _, scenario := range []string{"missing asset", "foreign subscription", "foreign connection", "foreign partition", "duplicate asset", "retargeted cluster", "tampered reference"} {
		t.Run(scenario, func(t *testing.T) {
			f := newFleetFixture(t)
			memberID := strings.ToLower(resourceID(fleetType, "fleet1")) + "/members/member1"
			clusterID := strings.ToLower(resourceID(fleetArcClusterType, "arc-cluster"))
			if scenario == "foreign subscription" {
				clusterID = strings.Replace(clusterID, testSubscription, rbacOtherSubscription, 1)
			}
			object(f.resources[memberID]["properties"])["clusterResourceId"] = clusterID
			values := f.assets(t)
			member := fleetAssetByKind(t, values, fleetMemberType)
			cluster := asset.Asset{ID: "arc-cluster", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: fleetArcClusterType, NativeID: clusterID}}
			if scenario == "foreign connection" {
				cluster.Identity.ConnectionID = "other"
			}
			if scenario == "foreign partition" {
				cluster.Identity.Partition = "azure-us-government"
			}
			if scenario != "missing asset" {
				values = append(values, cluster)
			}
			if scenario == "duplicate asset" {
				cluster.ID = "duplicate"
				values = append(values, cluster)
			}
			if scenario == "retargeted cluster" {
				object(f.resources[memberID]["properties"])["clusterResourceId"] = resourceID(aksType, "cluster1")
			}
			if scenario == "tampered reference" {
				member.Normalized = maps.Clone(member.Normalized)
				member.Normalized["_fleet_references"] = map[string]any{aksType: []string{strings.ToLower(resourceID(aksType, "cluster1"))}}
			}
			c, _ := f.runtime.resolve(t.Context(), "connection")
			clear(f.calls)
			contribution, err := c.contributeFleetReferences(t.Context(), member, values)
			if scenario == "retargeted cluster" || scenario == "tampered reference" || scenario == "duplicate asset" {
				if err == nil || scenario == "tampered reference" && len(f.calls) != 0 {
					t.Fatal("altered or ambiguous cluster reference accepted", contribution, err, f.calls)
				}
			} else if err != nil || len(contribution.Unresolved) != 1 || contribution.Unresolved[0].NativeID != clusterID || contribution.Unresolved[0].NativeType != fleetArcClusterType || contribution.Unresolved[0].Relationship != graph.RelationshipUses {
				t.Fatal("external Arc scope lost its unresolved reference", contribution, err)
			}
		})
	}
}
