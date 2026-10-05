package azure

import (
	"encoding/json"
	"fmt"
	"maps"
	"math/rand/v2"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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
				if kind == rbacPrincipalType {
					continue // An external service principal is not an ARM asset.
				}
				for _, id := range stringValues(ids) {
					assets = append(assets, asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: kind, NativeID: id}, Location: "global", Capabilities: asset.CapabilitySet{asset.CapabilityActionable}})
				}
			}
			contribution, err := c.contributeRBACReferences(t.Context(), source, assets)
			unresolved := 0
			if kind == rbacAssignmentType {
				unresolved = 1
			}
			if err != nil || len(contribution.Bindings) != 0 || len(contribution.Unresolved) != unresolved || len(contribution.Relationships) != 2*(len(assets)-1) {
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
	for _, principal := range []bool{false, true} {
		t.Run(map[bool]string{false: "scope-extension", true: "owned-vm-principal"}[principal], func(t *testing.T) {
			testRBACManagedGroupAssignmentRequiresIndependentDeletion(t, principal)
		})
	}
}

func testRBACManagedGroupAssignmentRequiresIndependentDeletion(t *testing.T, principal bool) {
	f := newRBACFixture(t)
	s := newAKSScenario()
	group := strings.ToLower(text(s.group["id"]))
	clear(f.scopes)
	f.scopes[group] = s.group
	clear(f.resources)
	scope := group
	if principal {
		scope = "/subscriptions/" + testSubscription + "/resourcegroups/test"
		f.scopes[scope] = map[string]any{"id": scope, "type": groupType, "name": "test", "location": "eastus", "properties": map[string]any{}}
		s.vm["identity"] = map[string]any{"type": "SystemAssigned", "principalId": rbacTestPrincipal, "tenantId": testTenant}
		// All members in this scenario have native readers. Unknown resource
		// kinds cannot prove the identity that would disappear with their group.
		s.members = []any{s.vm, s.disk}
	}
	raw := rbacTestBody(t, rbacAssignmentType, scope, rbacTestAssignmentName)
	id := strings.ToLower(text(raw["id"]))
	f.resources[id] = raw
	if !principal {
		object(raw["properties"])["principalType"] = "User"
		s.members = append(s.members, raw)
	}
	r := s.runtime(t)
	base := r.transport
	r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if rbacPath(req.URL.Path) || principal && req.Method == "GET" && strings.EqualFold(req.URL.Path, scope) {
			return f.runtime.transport.RoundTrip(req)
		}
		return base.RoundTrip(req)
	})
	values := s.assets(t)
	if principal {
		values = slices.DeleteFunc(values, func(value asset.Asset) bool { return value.Identity.NativeType == "microsoft.example/widgets" })
		c, err := r.resolve(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		for _, value := range values {
			for _, raw := range []map[string]any{s.cluster, s.group, s.vm, s.disk} {
				if strings.EqualFold(text(raw["id"]), value.Identity.NativeID) {
					if err := c.rbacIdentityInventory(value.Identity.NativeID, value.Identity.NativeType, raw, value.Normalized); err != nil {
						t.Fatal(err)
					}
				}
			}
		}
	}
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

// A delete check reads only the listed RBAC rows that can reference its target:
// unrelated assignments are decided by their validated list fields and never
// GET. A linked row, one whose reviewed references named the target, and every
// custom role (its assignableScopes are mutable) is still read and must agree
// with its list row; a role list lagging a scope just added fails closed.
func TestRBACIncomingReadsOnlyRowsThatReferenceTheTarget(t *testing.T) {
	for _, mode := range []string{"linked", "disagreement", "recorded", "role-lag"} {
		t.Run(mode, func(t *testing.T) {
			f, assignment, target, _ := rbacStorageTarget(t)
			group := "/subscriptions/" + testSubscription + "/resourcegroups/test"
			unrelated := []string{}
			for i := range 50 {
				raw := rbacTestBody(t, rbacAssignmentType, group, fmt.Sprintf("cccccccc-0000-0000-0000-%012d", i))
				id := strings.ToLower(text(raw["id"]))
				f.resources[id], unrelated = raw, append(unrelated, id)
			}
			known := []asset.Asset{}
			if mode == "recorded" {
				// Reviewed while the role named the target; the list now omits it.
				object(f.resources[rbacTestRoleID()]["properties"])["assignableScopes"] = []any{group, target.Identity.NativeID}
				known = append(known, f.asset(t, rbacRoleType, rbacTestRoleID()))
				object(f.resources[rbacTestRoleID()]["properties"])["assignableScopes"] = []any{group}
			}
			if mode == "disagreement" {
				base := f.override
				f.override = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.EqualFold(req.URL.Path, assignment.Identity.NativeID) {
						raw := maps.Clone(f.resources[assignment.Identity.NativeID])
						raw["properties"] = maps.Clone(object(raw["properties"]))
						object(raw["properties"])["condition"] = "detail-only"
						return jsonResponse(200, raw, nil), true
					}
					return base(req)
				}
			}
			if mode == "role-lag" {
				// The role now names the target; the list still shows the old scopes.
				base := f.override
				f.override = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" && strings.EqualFold(req.URL.Path, rbacTestRoleID()) {
						raw := maps.Clone(f.resources[rbacTestRoleID()])
						raw["properties"] = maps.Clone(object(raw["properties"]))
						object(raw["properties"])["assignableScopes"] = []any{group, target.Identity.NativeID}
						return jsonResponse(200, raw, nil), true
					}
					return base(req)
				}
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			clear(f.calls)
			incoming, err := c.rbacIncomingObservation(t.Context(), []asset.Asset{target}, known)
			if mode == "disagreement" || mode == "role-lag" {
				if err == nil || !strings.Contains(err.Error(), "rbac_list_detail_disagreement") || len(incoming) != 0 {
					t.Fatal("disagreeing linked row did not block", incoming, err)
				}
				return
			}
			sources := incoming[target.Identity.NativeID]
			if err != nil || len(sources) != 1 || sources[0].resource.id != assignment.Identity.NativeID {
				t.Fatal("linked assignment was not observed", sources, err)
			}
			if got := f.calls["GET "+assignment.Identity.NativeID]; got != 1 {
				t.Fatal("linked assignment GETs", got)
			}
			for _, id := range unrelated {
				if got := f.calls["GET "+id]; got != 0 {
					t.Fatal("unrelated assignment was read", id, got)
				}
			}
			if got := f.calls["GET "+rbacTestRoleID()]; got != 1 {
				t.Fatal("custom role definition GETs", got)
			}
			builtin := "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/roledefinitions/" + rbacTestBuiltinName
			if got := f.calls["GET "+builtin]; got != 0 {
				t.Fatal("built-in role definition was read", got)
			}
		})
	}
}

// Every custom role is detail-read once per observation, inside the shared
// role index: concurrent delete checks read each role once between them.
func TestRBACIncomingCustomRoleDetailsAreShared(t *testing.T) {
	f, _, target, _ := rbacStorageTarget(t)
	root := "/subscriptions/" + testSubscription
	roles := []string{rbacTestRoleID()}
	for i := range 20 {
		raw := rbacTestBody(t, rbacRoleType, root, fmt.Sprintf("dddddddd-0000-0000-0000-%012d", i))
		object(raw["properties"])["assignableScopes"] = []any{root + "/resourcegroups/test"}
		id := strings.ToLower(text(raw["id"]))
		f.resources[id], roles = raw, append(roles, id)
	}
	builtin := "GET " + root + "/providers/microsoft.authorization/roledefinitions/" + rbacTestBuiltinName
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	clear(f.calls)
	for range 2 {
		if _, err := c.rbacIncomingObservation(t.Context(), []asset.Asset{target}, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range roles {
		if got := f.calls["GET "+id]; got != 2 {
			t.Fatal("custom role not read exactly once per observation", id, got)
		}
	}
	if f.calls[builtin] != 0 {
		t.Fatal("built-in role was read", f.calls[builtin])
	}

	// One caller's role index is running; five more queue behind it and share
	// the next one, so each role is read twice, not six times.
	list := root + "/providers/" + strings.ToLower(rbacRoleType)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	f.before = func(req *http.Request) {
		if req.Method == "GET" && strings.ToLower(req.URL.Path) == list {
			once.Do(func() { close(started); <-release })
		}
	}
	clear(f.calls)
	errs := make(chan error, 6)
	observe := func() { _, err := c.rbacIncomingObservation(t.Context(), []asset.Asset{target}, nil); errs <- err }
	go observe()
	<-started
	for range 5 {
		go observe()
	}
	for {
		sharedReads.Lock()
		queued := sharedReads.queued[sharedReadKey{c, "rbac-index:" + rbacRoleType}]
		sharedReads.Unlock()
		if queued != nil {
			break
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(50 * time.Millisecond) // Let the other callers join the queued read.
	close(release)
	for range 6 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range roles {
		if got := f.calls["GET "+id]; got != 2 {
			t.Fatal("concurrent callers did not share custom role reads", id, got)
		}
	}
}

// The target index answers exactly what the per-(row, target) walk over
// rbacPrincipalMatches did, including which error stops it first.
func TestRBACTargetIndexMatchesPerTargetWalk(t *testing.T) {
	c := directClient(nil)
	principals := []string{}
	for i := range 4 {
		principals = append(principals, fmt.Sprintf("%08d-0000-0000-0000-000000000000", i))
	}
	identity := func(name, principal string, proved bool) asset.Asset {
		wire := "/subscriptions/" + testSubscription + "/resourceGroups/ids/providers/Microsoft.ManagedIdentity/userAssignedIdentities/" + name
		id, _, _ := parseID(wire)
		value := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, NativeID: id, NativeType: rbacUserIdentityType}, Normalized: map[string]any{}}
		if proved {
			raw := map[string]any{"id": wire, "properties": map[string]any{"principalId": principal, "tenantId": testTenant, "clientId": rbacTestClientID}}
			if err := c.rbacIdentityInventory(id, rbacUserIdentityType, raw, value.Normalized); err != nil {
				t.Fatal(err)
			}
		}
		return value
	}
	old := func(targets []asset.Asset, refs map[string][]string) ([]int, error) {
		var linked []int
		for i, target := range targets {
			matched := slices.Contains(refs[target.Identity.NativeType], target.Identity.NativeID)
			for _, reference := range refs[rbacPrincipalType] {
				matches, err := c.rbacPrincipalMatches(target, reference)
				if err != nil {
					return nil, err
				}
				matched = matched || matches
			}
			if matched {
				linked = append(linked, i)
			}
		}
		return linked, nil
	}
	random := rand.New(rand.NewPCG(1, 2))
	failures := 0
	for trial := range 5000 {
		var targets []asset.Asset
		for i := range 1 + random.IntN(6) {
			switch random.IntN(8) {
			case 0:
				targets = append(targets, identity(fmt.Sprintf("bad%d", i), "", false))
			case 1:
				targets = append(targets, asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, NativeID: rbacTestRoleID(), NativeType: rbacRoleType}})
			default:
				targets = append(targets, identity(fmt.Sprintf("id%d", random.IntN(4)), principals[random.IntN(len(principals))], true))
			}
		}
		refs := map[string][]string{}
		for range random.IntN(4) {
			switch random.IntN(5) {
			case 0:
				refs[rbacPrincipalType] = append(refs[rbacPrincipalType], "principal-id:INVALID")
			case 1:
				target := targets[random.IntN(len(targets))]
				refs[target.Identity.NativeType] = append(refs[target.Identity.NativeType], target.Identity.NativeID)
			default:
				refs[rbacPrincipalType] = append(refs[rbacPrincipalType], rbacPrincipalSelector(testTenant, principals[random.IntN(len(principals))]))
			}
		}
		want, wantErr := old(targets, refs)
		got, gotErr := c.rbacTargetIndex(targets).linked(refs)
		if fmt.Sprint(wantErr) != fmt.Sprint(gotErr) || len(want)+len(got) != 0 && !slices.Equal(want, got) {
			t.Fatal("target index disagrees with the per-target walk", trial, want, wantErr, got, gotErr)
		}
		if wantErr != nil {
			failures++
		}
	}
	if failures == 0 || failures == 5000 {
		t.Fatal("fixtures did not exercise both outcomes", failures)
	}
}

// Linked rows' details are read concurrently, once each, and the first failure
// in row order is the one returned, as the serial walk did.
func TestRBACIncomingLinkedDetailsReadConcurrentlyInOrder(t *testing.T) {
	for _, fail := range []bool{false, true} {
		f, _, target, _ := rbacStorageTarget(t)
		var linked []string
		for i := range 20 {
			raw := rbacTestBody(t, rbacAssignmentType, target.Identity.NativeID, fmt.Sprintf("eeeeeeee-0000-0000-0000-%012d", i))
			id := strings.ToLower(text(raw["id"]))
			f.resources[id], linked = raw, append(linked, id)
		}
		slices.Sort(linked)
		var mu sync.Mutex
		inFlight, peak := 0, 0
		f.before = func(req *http.Request) {
			if req.Method != "GET" || !slices.Contains(linked, strings.ToLower(req.URL.Path)) {
				return
			}
			mu.Lock()
			inFlight++
			peak = max(peak, inFlight)
			mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			mu.Lock()
			inFlight--
			mu.Unlock()
		}
		if fail {
			base := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				switch strings.ToLower(req.URL.Path) {
				case linked[3]:
					return jsonResponse(403, map[string]any{"error": map[string]any{"code": "FirstDenied", "message": "first"}}, nil), true
				case linked[12]:
					return jsonResponse(403, map[string]any{"error": map[string]any{"code": "LaterDenied", "message": "later"}}, nil), true
				}
				return base(req)
			}
		}
		c, err := f.runtime.resolve(t.Context(), "connection")
		if err != nil {
			t.Fatal(err)
		}
		clear(f.calls)
		incoming, err := c.rbacIncomingObservation(t.Context(), []asset.Asset{target}, nil)
		if fail {
			if err == nil || !strings.Contains(err.Error(), "FirstDenied") || len(incoming) != 0 {
				t.Fatal("first failing linked row was not the reported error", err)
			}
			continue
		}
		if err != nil || len(incoming[target.Identity.NativeID]) != len(linked)+1 {
			t.Fatal("linked assignments were not observed", len(incoming[target.Identity.NativeID]), err)
		}
		for _, id := range linked {
			if got := f.calls["GET "+id]; got != 1 {
				t.Fatal("linked assignment not read exactly once", id, got)
			}
		}
		if peak < 2 || peak > detailReadConcurrency {
			t.Fatal("linked assignment details were not read concurrently within the bound", peak)
		}
	}
}

// monitorLinked answers what the per-target walk over monitorReferenceMatches
// did: the same linked targets before the same stopping target, with an error
// that walk could stop at (its reference kinds come in map order).
func TestMonitorTargetIndexMatchesPerTargetWalk(t *testing.T) {
	c := directClient(nil)
	group := "/subscriptions/" + testSubscription + "/resourcegroups/ids/providers/"
	principals := []string{"00000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000001"}
	customers := []string{"cccccccc-0000-4000-8000-000000000000", "cccccccc-0000-4000-8000-000000000001"}
	identity := func(name, principal string, proved bool) asset.Asset {
		wire := "/subscriptions/" + testSubscription + "/resourceGroups/ids/providers/Microsoft.ManagedIdentity/userAssignedIdentities/" + name
		id, _, _ := parseID(wire)
		value := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, NativeID: id, NativeType: rbacUserIdentityType}, Normalized: map[string]any{}}
		if proved {
			raw := map[string]any{"id": wire, "properties": map[string]any{"principalId": principal, "tenantId": testTenant, "clientId": rbacTestClientID}}
			if err := c.rbacIdentityInventory(id, rbacUserIdentityType, raw, value.Normalized); err != nil {
				t.Fatal(err)
			}
		}
		return value
	}
	workspace := func(name, customer string, proved bool) asset.Asset {
		id := strings.ToLower(group + insightsWorkspaceType + "/" + name)
		value := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, NativeID: id, NativeType: insightsWorkspaceType}, Location: "westus", Normalized: map[string]any{"customerId": customer, "_monitor_private_link_target_configuration": "configuration"}}
		value.Normalized[monitorReceiverTargetProof] = c.monitorReceiverTargetBinding(id, insightsWorkspaceType, "westus", customer, "configuration")
		if !proved {
			value.Normalized[monitorReceiverTargetProof] = "forged"
		}
		return value
	}
	hub := func(namespace, name string) asset.Asset {
		id := strings.ToLower(group + eventHubNamespaceType + "/" + namespace)
		kind := eventHubNamespaceType
		if name != "" {
			id, kind = id+"/eventhubs/"+name, eventHubType
		}
		return asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, NativeID: id, NativeType: kind}}
	}
	type outcome struct {
		linked []int
		stop   int
		err    string
	}
	old := func(targets []asset.Asset, kinds []string, refs map[string][]string) outcome {
		var linked []int
		for i, target := range targets {
			matched := false
			for _, kind := range kinds {
				for _, reference := range refs[kind] {
					matches, err := c.monitorReferenceMatches(target, kind, reference)
					if err != nil {
						return outcome{linked, i, err.Error()}
					}
					matched = matched || matches
				}
			}
			if matched {
				linked = append(linked, i)
			}
		}
		return outcome{linked, len(targets), ""}
	}
	var orders func([]string) [][]string
	orders = func(kinds []string) [][]string {
		if len(kinds) <= 1 {
			return [][]string{kinds}
		}
		var all [][]string
		for i := range kinds {
			rest := append(slices.Clone(kinds[:i]), kinds[i+1:]...)
			for _, order := range orders(rest) {
				all = append(all, append([]string{kinds[i]}, order...))
			}
		}
		return all
	}
	random := rand.New(rand.NewPCG(3, 4))
	failures, matches := 0, 0
	for trial := range 5000 {
		var targets []asset.Asset
		for i := range 1 + random.IntN(6) {
			switch random.IntN(7) {
			case 0:
				targets = append(targets, identity(fmt.Sprintf("bad%d", i), "", false))
			case 1:
				targets = append(targets, identity(fmt.Sprintf("id%d", i), principals[random.IntN(2)], true))
			case 2:
				targets = append(targets, workspace(fmt.Sprintf("ws%d", i), customers[random.IntN(2)], random.IntN(3) != 0))
			case 3:
				targets = append(targets, hub(fmt.Sprintf("ns%d", random.IntN(2)), ""))
			case 4:
				targets = append(targets, hub(fmt.Sprintf("ns%d", random.IntN(2)), fmt.Sprintf("hub%d", random.IntN(2))))
			default:
				targets = append(targets, asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, NativeID: strings.ToLower(group + storageType + fmt.Sprintf("/st%d", i)), NativeType: storageType}})
			}
		}
		refs := map[string][]string{}
		for range random.IntN(5) {
			switch random.IntN(7) {
			case 0:
				refs[rbacPrincipalType] = append(refs[rbacPrincipalType], "principal-id:INVALID")
			case 1:
				refs[rbacPrincipalType] = append(refs[rbacPrincipalType], rbacPrincipalSelector(testTenant, principals[random.IntN(2)]))
			case 2:
				selector := "workspace-id:" + customers[random.IntN(2)]
				if random.IntN(2) == 0 {
					selector = "workspace-id:" + testSubscription + "|" + customers[random.IntN(2)]
				}
				refs[insightsWorkspaceType] = append(refs[insightsWorkspaceType], selector)
			case 3:
				refs[eventHubNamespaceType] = append(refs[eventHubNamespaceType], fmt.Sprintf("eventhub-namespace:%s/ns%d", testSubscription, random.IntN(2)))
			case 4:
				refs[eventHubType] = append(refs[eventHubType], fmt.Sprintf("eventhub:%s/ns%d/hub%d", testSubscription, random.IntN(2), random.IntN(2)))
			default:
				target := targets[random.IntN(len(targets))]
				kind := target.Identity.NativeType
				if random.IntN(2) == 0 {
					kind = strings.ToUpper(kind)
				}
				refs[kind] = append(refs[kind], target.Identity.NativeID)
			}
		}
		linked, stop, err := c.monitorLinked(targets, c.monitorTargetIndex(targets), refs)
		var reached []int
		for _, i := range linked {
			if i < stop {
				reached = append(reached, i)
			}
		}
		got := outcome{reached, stop, fmt.Sprint(err)}
		if err == nil {
			got.err = ""
		}
		agreed := false
		for _, kinds := range orders(slices.Sorted(maps.Keys(refs))) {
			want := old(targets, kinds, refs)
			if want.stop != got.stop || !slices.Equal(want.linked, got.linked) {
				t.Fatal("target index disagrees with the per-target walk", trial, want, got)
			}
			agreed = agreed || want.err == got.err
		}
		if !agreed {
			t.Fatal("target index stopped at an error the per-target walk could not", trial, got)
		}
		if got.err != "" {
			failures++
		}
		matches += len(got.linked)
	}
	if failures == 0 || failures == 5000 || matches == 0 {
		t.Fatal("randomized trials did not cover both outcomes", failures, matches)
	}
}
