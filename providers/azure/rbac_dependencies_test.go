package azure

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestRBACNativeReferencesRequireIndependentCleanup(t *testing.T) {
	for _, kind := range []string{rbacRoleType, rbacAssignmentType} {
		t.Run(kind, func(t *testing.T) {
			f := newRBACFixture(t)
			id := rbacTestRoleID()
			if kind == rbacAssignmentType {
				id = rbacTestAssignmentID()
			}
			source := f.asset(t, kind, id)
			c, _ := f.runtime.resolve(t.Context(), "connection")
			refs, err := c.rbacRecordedReferences(source)
			if err != nil {
				t.Fatal(err)
			}
			assets := []asset.Asset{source}
			for kind, ids := range refs {
				for _, id := range stringValues(ids) {
					assets = append(assets, asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: kind, NativeID: id}, Location: "global", Capabilities: asset.CapabilitySet{asset.CapabilityActionable}})
				}
			}
			contribution, err := c.contributeRBACReferences(t.Context(), source, assets)
			if err != nil || len(contribution.Bindings)+len(contribution.Unresolved) != 0 || len(contribution.Relationships) != 2*(len(assets)-1) {
				t.Fatal("RBAC references lost ordering or acquired ownership", contribution, err)
			}
			wire, _ := json.Marshal(contribution.Relationships)
			var relationships []graph.Relationship
			if json.Unmarshal(wire, &relationships) != nil {
				t.Fatal("RBAC relationships could not survive persistence")
			}
			for _, target := range assets[1:] {
				alone, err := plan.Solve(plan.Input{Assets: assets, Relationships: relationships, ResolvedAssetIDs: []asset.AssetID{target.ID}})
				if err != nil || len(alone.Blockers) == 0 {
					t.Fatal("retained RBAC source did not block target", alone, err)
				}
				selected, err := plan.Solve(plan.Input{Assets: assets, Relationships: relationships, ResolvedAssetIDs: []asset.AssetID{source.ID, target.ID}})
				if err != nil || len(selected.Blockers) != 0 || len(selected.Steps) != 2 || selected.Steps[0].AssetID != source.ID || !slices.Contains(selected.Steps[1].DependsOn, selected.Steps[0].ID) || len(selected.ImpactItems) != 0 {
					t.Fatal("RBAC prerequisite was not independently ordered", selected, err)
				}
			}
			alone, err := plan.Solve(plan.Input{Assets: assets, Relationships: relationships, ResolvedAssetIDs: []asset.AssetID{source.ID}})
			if err != nil || len(alone.Blockers)+len(alone.ImpactItems) != 0 || len(alone.Steps) != 1 {
				t.Fatal("RBAC deletion selected its role or scopes", alone, err)
			}
		})
	}
}

func rbacStorageTarget(t *testing.T) (*rbacFixture, asset.Asset, asset.Asset, *[]string) {
	t.Helper()
	f := newRBACFixture(t)
	storage := nativeResource(storageType, "rbacstorage", "westus", map[string]any{"provisioningState": "Succeeded"})
	storage["kind"] = "StorageV2"
	id := strings.ToLower(text(storage["id"]))
	f.scopes[id] = storage
	assignment := rbacTestBody(t, rbacAssignmentType, id, rbacTestSecondAssignment)
	assignmentID := strings.ToLower(text(assignment["id"]))
	for key, raw := range f.resources {
		if raw["type"] == rbacAssignmentType {
			delete(f.resources, key)
		}
	}
	f.resources[assignmentID] = assignment
	deleted := []string{}
	f.override = func(req *http.Request) (*http.Response, bool) {
		for _, collection := range []string{"blobservices/default/containers", "fileservices/default/shares", "queueservices/default/queues", "tableservices/default/tables"} {
			if strings.EqualFold(req.URL.Path, id+"/"+collection) {
				if req.Method != "GET" || req.URL.Query().Get("api-version") != "2023-05-01" {
					t.Fatal("RBAC target lost native storage validation", req.Method, req.URL)
				}
				return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
			}
		}
		if strings.EqualFold(req.URL.Path, id) {
			if f.scopes[id] == nil {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
			}
			if req.Method == "DELETE" {
				deleted = append(deleted, id)
				delete(f.scopes, id)
				return jsonResponse(204, nil, nil), true
			}
		}
		return nil, false
	}
	return f, f.asset(t, rbacAssignmentType, assignmentID), dnsAsset(t, f.runtime, storage), &deleted
}

func TestRBACUnindexedAndLateSourcesProtectNativeTargets(t *testing.T) {
	for _, mode := range []string{"unindexed", "target-absent", "late-after-delete", "collection-403", "collection-404", "get-404", "changed-between-passes", "known-list-omission"} {
		t.Run(mode, func(t *testing.T) {
			f, assignment, target, deleted := rbacStorageTarget(t)
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			request := contracts.ActionRequest{Asset: target, Action: "delete"}
			raw := f.resources[assignment.Identity.NativeID]
			if mode == "late-after-delete" {
				delete(f.resources, assignment.Identity.NativeID)
				result, err := driver.Execute(t.Context(), request)
				if err != nil || len(*deleted) != 1 {
					t.Fatal("empty RBAC index blocked target deletion", err)
				}
				f.resources[assignment.Identity.NativeID] = raw
				wire, _ := json.Marshal(result)
				if json.Unmarshal(wire, &result) != nil {
					t.Fatal("invalid recovered target receipt")
				}
				driver, err = f.runtime.ResolveAction(t.Context(), "connection", target)
				if err != nil {
					t.Fatal(err)
				}
				if wait, err := driver.Wait(t.Context(), request, result); err == nil || isNotFound(err) || wait.Done {
					t.Fatal("late assignment disappeared with its target", wait, err)
				}
				return
			}
			if mode == "target-absent" {
				delete(f.scopes, target.Identity.NativeID)
			}
			base := f.override
			passes := 0
			f.override = func(req *http.Request) (*http.Response, bool) {
				collection := strings.EqualFold(req.URL.Path, "/subscriptions/"+testSubscription+"/providers/"+rbacAssignmentType)
				if collection {
					passes++
					if mode == "changed-between-passes" && passes == 2 {
						object(raw["properties"])["condition"] = "changed"
					}
					if mode == "known-list-omission" {
						return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
					}
				}
				if collection && strings.HasPrefix(mode, "collection-") || mode == "get-404" && strings.EqualFold(req.URL.Path, assignment.Identity.NativeID) {
					status := 404
					if mode == "collection-403" {
						status = 403
					}
					return jsonResponse(status, map[string]any{"error": map[string]any{"code": "AuthorizationFailedOrMissing"}}, nil), true
				}
				return base(req)
			}
			if mode == "known-list-omission" {
				request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: assignment, ControllerID: target.ID, Delete: true}}
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || isNotFound(err) || len(*deleted) != 0 {
				t.Fatal("unsafe RBAC target deletion accepted", mode, err, *deleted)
			}
			if mode == "unindexed" || mode == "target-absent" {
				c, _ := f.runtime.resolve(t.Context(), "connection")
				contribution, err := c.contributeMonitorIncoming(t.Context(), []asset.Asset{target}, []asset.Asset{target})
				if err != nil || len(contribution.Unresolved) != 1 || contribution.Unresolved[0].NativeID != assignment.Identity.NativeID || contribution.Unresolved[0].Evidence[graph.RelationshipEvidenceRequiredDeletion] != true {
					t.Fatal("unindexed RBAC source did not become a blocker", contribution, err)
				}
			}
		})
	}
}

func TestRBACReviewedScopePrerequisiteSurvivesRecovery(t *testing.T) {
	f, assignment, target, deleted := rbacStorageTarget(t)
	request := contracts.ActionRequest{Asset: target, Action: "delete", PrerequisiteDeletions: []contracts.ActionImpact{{Asset: assignment, ControllerID: target.ID, Delete: true}}}
	if _, err := f.action(t, assignment).Execute(t.Context(), contracts.ActionRequest{Asset: assignment, Action: "delete"}); err != nil {
		t.Fatal(err)
	}
	driver, err := f.runtime.ResolveAction(t.Context(), "connection", target)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), request)
	if err != nil || len(*deleted) != 1 || len(f.deleted) != 1 {
		t.Fatal("reviewed RBAC absence did not permit independent scope deletion", result, err)
	}
	wire, _ := json.Marshal(struct {
		Request contracts.ActionRequest
		Result  contracts.ActionResult
	}{request, result})
	var restored struct {
		Request contracts.ActionRequest
		Result  contracts.ActionResult
	}
	if json.Unmarshal(wire, &restored) != nil {
		t.Fatal("invalid stored RBAC target state")
	}
	f.runtime.clients = map[asset.ConnectionID]*client{}
	driver, err = f.runtime.ResolveAction(t.Context(), "connection", restored.Request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(t.Context(), restored.Request, restored.Result); err != nil || !wait.Done {
		t.Fatal("RBAC scope recovery failed", wait, err)
	}
	restored.Request.PrerequisiteDeletions[0].Asset.Normalized["_rbac_references"] = map[string]any{}
	if wait, err := driver.Wait(t.Context(), restored.Request, restored.Result); err == nil || wait.Done {
		t.Fatal("forged absent RBAC prerequisite authorized completion", wait, err)
	}
}

func TestRBACManagedGroupAssignmentRequiresIndependentDeletion(t *testing.T) {
	f := newRBACFixture(t)
	s := newAKSScenario()
	group := strings.ToLower(text(s.group["id"]))
	clear(f.scopes)
	f.scopes[group] = s.group
	clear(f.resources)
	raw := rbacTestBody(t, rbacAssignmentType, group, rbacTestAssignmentName)
	id := strings.ToLower(text(raw["id"]))
	f.resources[id] = raw
	s.members = append(s.members, raw)
	r := s.runtime(t)
	base := r.transport
	r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if rbacPath(req.URL.Path) {
			return f.runtime.transport.RoundTrip(req)
		}
		return base.RoundTrip(req)
	})
	values := s.assets(t)
	assignment := f.asset(t, rbacAssignmentType, id)
	assignment.Identity.Partition = values[0].Identity.Partition
	values = append(values, assignment)
	controller := values[0]
	service, _ := r.ServiceLifecycle(t.Context(), "connection")
	cluster, _ := r.ClusterLifecycle(t.Context(), "connection")
	store := batchReferenceGraph{assets: values}
	built, err := governance.NewService(store, store).RebuildGraph(t.Context(), "scope", "connection", "rbac-managed", r.bundle, []governance.Contributor{service, cluster})
	if err != nil {
		t.Fatal("RBAC managed-group graph failed", err)
	}
	for _, binding := range built.Bindings {
		if binding.ManagedAssetID == assignment.ID {
			t.Fatal("RBAC assignment acquired group ownership", binding)
		}
	}
	alone, err := plan.Solve(plan.Input{Assets: values, Relationships: built.Relationships, LifecycleBindings: built.Bindings, ResolvedAssetIDs: []asset.AssetID{controller.ID}})
	if err != nil || len(alone.Blockers) == 0 {
		t.Fatal("managed-group deletion bypassed retained assignment", alone, err)
	}
	selected, err := plan.Solve(plan.Input{Assets: values, Relationships: built.Relationships, LifecycleBindings: built.Bindings, ResolvedAssetIDs: []asset.AssetID{controller.ID, assignment.ID}})
	if err != nil || len(selected.Blockers) != 0 || len(selected.Steps) != 2 || selected.Steps[0].AssetID != assignment.ID {
		t.Fatal("RBAC assignment was not independently ordered before its managed group", selected, err)
	}
	request := servicePlanRequest(selected, values, controller)
	request.IdempotencyKey = "delete-aks"
	driver, err := r.ResolveAction(t.Context(), "connection", controller)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(t.Context(), request); err == nil || s.deletes != 0 {
		t.Fatal("managed controller deleted a live assignment", err)
	}
	if _, err := f.action(t, assignment).Execute(t.Context(), servicePlanRequest(selected, values, assignment)); err != nil || len(f.deleted) != 1 || s.deletes != 0 {
		t.Fatal("independent RBAC assignment deletion touched its controller", err)
	}
	result, err := driver.Execute(t.Context(), request)
	if err != nil || s.deletes != 1 {
		t.Fatal("reviewed RBAC absence did not permit controller cleanup", result, err)
	}
	if wait, err := driver.Wait(t.Context(), request, result); err != nil || wait.Done {
		t.Fatal("managed controller skipped native pending state", wait, err)
	}
	s.clusterGone, s.groupGone, s.childrenGone = true, true, true
	if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
		t.Fatal("managed controller readback lost its independent RBAC prerequisite", wait, err)
	}
}
