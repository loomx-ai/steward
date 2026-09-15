package azure

import (
	"encoding/json"
	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/execution"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

func stackGraphAssets(t *testing.T) (*client, asset.Asset, asset.Asset) {
	t.Helper()
	c := directClient(nil)
	id := strings.ToLower(c.root() + "/providers/Microsoft.Resources/deploymentStacks/stack")
	member := asset.Asset{ID: "member", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeID: strings.ToLower(resourceID(vmType, "member")), NativeType: vmType}}
	review, err := c.deploymentStackMemberReview(map[string]any{"id": id, "properties": map[string]any{"resources": []any{map[string]any{"id": member.Identity.NativeID, "status": "managed", "denyStatus": "denyDelete"}}}})
	if err != nil {
		t.Fatal(err)
	}
	parent := asset.Asset{ID: "stack", Identity: asset.Identity{Provider: asset.ProviderAzure, Partition: "azure", ConnectionID: "connection", NativeID: id, NativeType: deploymentStackType}, Normalized: map[string]any{deploymentStackReviewKey: review, deploymentStackProofKey: c.deploymentStackProof(id, "connection", review)}}
	// Persistence converts numbers and map types. The proof must survive JSON storage.
	wire, _ := json.Marshal(parent)
	if json.Unmarshal(wire, &parent) != nil {
		t.Fatal("asset roundtrip")
	}
	return c, parent, member
}
func TestDeploymentStackGraphMembershipIsNotDelegation(t *testing.T) {
	c, parent, member := stackGraphAssets(t)
	out, err := c.deploymentStackContribution(parent, []asset.Asset{parent, member})
	if err != nil || len(out.Relationships) != 1 || len(out.Unresolved) != 0 || len(out.Bindings) != 0 {
		t.Fatal(out, err)
	}
	edge := out.Relationships[0]
	if edge.SourceAssetID != member.ID || edge.TargetAssetID != parent.ID || edge.Type != graph.RelationshipMemberOf || edge.Evidence["native_membership"] != true {
		t.Fatal(edge)
	}
}
func TestDeploymentStackGraphRejectsUnverifiedMembership(t *testing.T) {
	for _, fault := range []string{"missing", "wrong_type", "foreign_connection", "foreign_partition", "duplicate", "tampered", "legacy", "copied_stack", "copied_connection", "incomplete", "unknown_state", "foreign_member"} {
		t.Run(fault, func(t *testing.T) {
			c, parent, member := stackGraphAssets(t)
			review := object(parent.Normalized[deploymentStackReviewKey])
			all := []asset.Asset{member}
			switch fault {
			case "missing":
				all = nil
			case "wrong_type":
				all[0].Identity.NativeType = diskType
			case "foreign_connection":
				all[0].Identity.ConnectionID = "other"
			case "foreign_partition":
				all[0].Identity.Partition = "other"
			case "duplicate":
				all = append(all, member)
			case "tampered":
				review["members"] = map[string]any{}
			case "legacy":
				delete(parent.Normalized, deploymentStackProofKey)
			case "copied_stack":
				parent.Identity.NativeID += "other"
			case "copied_connection":
				parent.Identity.ConnectionID = "other"
			case "incomplete":
				review["arm_members_complete"] = false
				parent.Normalized[deploymentStackProofKey] = c.deploymentStackProof(parent.Identity.NativeID, parent.Identity.ConnectionID, review)
			case "unknown_state":
				object(object(review["members"])[member.Identity.NativeID])["status"] = "unknown"
				parent.Normalized[deploymentStackProofKey] = c.deploymentStackProof(parent.Identity.NativeID, parent.Identity.ConnectionID, review)
			case "foreign_member":
				object(object(review["members"])[member.Identity.NativeID])["subscription_local"] = false
				parent.Normalized[deploymentStackProofKey] = c.deploymentStackProof(parent.Identity.NativeID, parent.Identity.ConnectionID, review)
			}
			out, err := c.deploymentStackContribution(parent, all)
			if fault == "duplicate" {
				if err == nil {
					t.Fatal("duplicate member accepted")
				}
				return
			}
			if err != nil || len(out.Unresolved) == 0 || len(out.Bindings) != 0 {
				t.Fatal(out, err)
			}
			for _, u := range out.Unresolved {
				if !u.BlocksCleanup {
					t.Fatal("uncertainty not blocking", u)
				}
			}
		})
	}
}
func TestDeploymentStackRuntimeProducesGraphProof(t *testing.T) {
	f := newStackRuntimeFixture(t)
	req := stackRuntimeRequest(f.runtime)
	batch, err := f.runtime.List(t.Context(), req)
	if err != nil {
		t.Fatal(err)
	}
	c, err := f.runtime.resolve(t.Context(), req.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range batch.Items {
		if item.Normalized[deploymentStackProofKey] != c.deploymentStackProof(item.NativeID, req.ConnectionID, object(item.Normalized[deploymentStackReviewKey])) {
			t.Fatal("missing bound member review")
		}
	}
}

func TestDeploymentStackWorkerPersistsMemberGraph(t *testing.T) {
	f := newStackRuntimeFixture(t)
	root := strings.ToLower("/subscriptions/" + testSubscription + "/providers/Microsoft.Resources/deploymentStacks/stack")
	child := strings.ToLower("/subscriptions/" + testSubscription + "/resourceGroups/group/providers/Microsoft.Resources/deploymentStacks/stack")
	f.members = map[string][]any{root: {map[string]any{"id": child, "status": "managed", "denyStatus": "denyDelete"}}}
	previous := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		if response, ok := fleetGraphEmptyIndexes(t, q); ok {
			return response, nil
		}
		return previous.RoundTrip(q)
	})
	repo, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	creator, err := inventory.NewCreator(repo, registry)
	if err != nil {
		t.Fatal(err)
	}
	created, err := creator.Create(t.Context(), inventory.ScanCreationRequest{ConnectionID: "connection", RequestedBy: "stack-worker-test", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"global"}, ResourceKindIDs: []asset.ResourceKindID{f.runtime.resourceKind(deploymentStackType).ID}})
	if err != nil || len(created.Shards) != 1 {
		t.Fatal("global native shard", len(created.Shards), err)
	}
	if created.Shards[0].Source != deploymentStackSource || created.Shards[0].Authoritative {
		t.Fatal(created.Shards[0])
	}
	handler := inventory.NewScanHandler(repo, registry, inventory.NewService(repo))
	for _, job := range created.Jobs {
		if err := handler.Handle(t.Context(), job); err != nil {
			t.Fatal(err)
		}
	}
	values, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
	if err != nil || len(values) != 2 {
		t.Fatal("persisted stack inventory", len(values), err)
	}
	ids := map[string]asset.AssetID{}
	for _, v := range values {
		ids[v.Identity.NativeID] = v.ID
	}
	jobs, err := repo.ListJobsByAggregate(t.Context(), "scan_task", string(created.ScanRun.ID))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, job := range jobs {
		if job.Type == execution.JobGraph {
			count++
			if err := governance.NewGraphHandler(repo, registry, fleetHubGraphContributors{f.runtime}).Handle(t.Context(), job); err != nil {
				t.Fatal(err)
			}
		}
	}
	if count != 1 {
		t.Fatal("graph job count", count)
	}
	edges, err := repo.ListRelationshipsByConnection(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, edge := range edges {
		if edge.Source == deploymentStackGraphSource && edge.SourceAssetID == ids[child] && edge.TargetAssetID == ids[root] && edge.Type == graph.RelationshipMemberOf {
			found = true
		}
	}
	if !found {
		t.Fatal("native membership not persisted", edges)
	}
	// Known resources survive an omitted parent index and a later failed scan.
	proofs := map[asset.AssetID]any{}
	for _, v := range values {
		proofs[v.ID] = v.Normalized[deploymentStackProofKey]
		if v.ScopeID != created.Shards[0].ScopeID {
			t.Fatal("stack persisted in a different global scope", v.ScopeID, created.Shards[0].ScopeID)
		}
	}
	for _, failure := range []bool{false, true} {
		f.hidden = true
		if failure {
			f.fault = "group_index"
		}
		again, err := creator.Create(t.Context(), inventory.ScanCreationRequest{ConnectionID: "connection", RequestedBy: "stack-rescan-test", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"global"}, ResourceKindIDs: []asset.ResourceKindID{f.runtime.resourceKind(deploymentStackType).ID}})
		if err != nil {
			t.Fatal(err)
		}
		failed := false
		for _, job := range again.Jobs {
			if err := handler.Handle(t.Context(), job); err != nil {
				failed = true
				if !failure {
					t.Fatal(err)
				}
			}
		}
		if failed != failure {
			t.Fatal("unexpected scan outcome", failed, failure)
		}
		current, err := repo.ListActiveAssetsByConnection(t.Context(), "connection", "")
		if err != nil || len(current) != 2 {
			t.Fatal("rescan lost known stacks", len(current), err)
		}
		for _, v := range current {
			if proofs[v.ID] != v.Normalized[deploymentStackProofKey] {
				t.Fatal("rescan replaced stable member proof")
			}
		}
	}
}
