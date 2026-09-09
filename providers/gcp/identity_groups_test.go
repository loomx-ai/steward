package gcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const identityTestGroup = "groups/g-primary"
const identityTestNested = "groups/g-retained"

type identityScenario struct {
	root                       string
	groups, members            map[string]map[string]any
	security                   map[string]any
	calls, writes              []string
	hook                       func(*http.Request) (*http.Response, bool)
	emptyPage, delayed, linger bool
	operation                  map[string]any
}

func newIdentityScenario() *identityScenario {
	s := &identityScenario{root: "customers/C01234567", groups: map[string]map[string]any{}, members: map[string]map[string]any{}, operation: map[string]any{"done": true}, security: map[string]any{"name": identityTestGroup + "/securitySettings", "memberRestriction": map[string]any{"query": "member.type == 1", "evaluation": map[string]any{"state": "COMPLIANT"}}}}
	for _, name := range []string{identityTestGroup, identityTestNested} {
		s.groups[name] = map[string]any{"name": name, "parent": s.root, "groupKey": map[string]any{"id": last(name) + "@example.test"}, "additionalGroupKeys": []any{map[string]any{"id": last(name) + "-alias@example.test"}}, "displayName": last(name), "description": "Reviewed identity group", "labels": map[string]any{"cloudidentity.googleapis.com/groups.discussion_forum": ""}, "createTime": "2026-01-01T00:00:00Z", "updateTime": "2026-01-02T00:00:00Z"}
	}
	for _, m := range []struct{ id, entity, kind string }{{"m-user", "member@example.test", "USER"}, {"m-nested", "g-retained@example.test", "GROUP"}} {
		name := identityTestGroup + "/memberships/" + m.id
		s.members[name] = map[string]any{"name": name, "preferredMemberKey": map[string]any{"id": m.entity}, "type": m.kind, "roles": []any{map[string]any{"name": "MEMBER"}}, "createTime": "2026-01-03T00:00:00Z", "updateTime": "2026-01-04T00:00:00Z", "deliverySetting": "ALL_MAIL"}
	}
	return s
}

// Routes and FULL/pageSize queries are literal native v1 contracts, independent
// of the generated catalog, identity parser and deletion implementation.
func (s *identityScenario) transport(t *testing.T) roundTripFunc {
	return func(r *http.Request) (*http.Response, error) {
		s.calls = append(s.calls, r.Method+" "+r.URL.String())
		if s.hook != nil {
			if response, handled := s.hook(r); handled {
				return response, nil
			}
		}
		reply := func(code int, data any) (*http.Response, error) { return dataformResponse(r, code, data), nil }
		if r.URL.Host == "cloudasset.googleapis.com" && r.Method == "GET" {
			return reply(200, map[string]any{})
		}
		if r.URL.Host != "cloudidentity.googleapis.com" || !strings.HasPrefix(r.URL.Path, "/v1/") {
			t.Fatalf("unexpected identity URL %s", r.URL)
		}
		q := r.URL.Query()
		name := strings.TrimPrefix(r.URL.Path, "/v1/")
		if r.Method == "GET" && (name == "groups" || strings.HasSuffix(name, "/memberships")) {
			if q.Get("view") != "FULL" || q.Get("pageSize") != "500" && q.Get("pageSize") != "1" {
				t.Fatalf("incomplete identity list query %s", r.URL)
			}
			field := "memberships"
			rows := []any{}
			if name == "groups" {
				field = "groups"
				if q.Get("parent") != s.root {
					t.Fatalf("unexpected directory %s", r.URL)
				}
				ids := []string{}
				for id := range s.groups {
					ids = append(ids, id)
				}
				slices.Sort(ids)
				for _, id := range ids {
					rows = append(rows, s.groups[id])
				}
			} else {
				parent := strings.TrimSuffix(name, "/memberships")
				if q.Get("parent") != "" || s.groups[parent] == nil {
					t.Fatalf("invalid membership list %s", r.URL)
				}
				ids := []string{}
				for id := range s.members {
					if strings.HasPrefix(id, parent+"/memberships/") {
						ids = append(ids, id)
					}
				}
				slices.Sort(ids)
				for _, id := range ids {
					rows = append(rows, s.members[id])
				}
			}
			if s.emptyPage && q.Get("pageToken") == "" && q.Get("pageSize") == "500" {
				return reply(200, map[string]any{"nextPageToken": "next-native-page"})
			}
			if q.Get("pageToken") != "" && q.Get("pageToken") != "next-native-page" {
				t.Fatal("wrong native cursor")
			}
			return reply(200, map[string]any{field: rows})
		}
		if r.URL.RawQuery != "" {
			t.Fatalf("unexpected identity query %s", r.URL)
		}
		if r.Method == "GET" && name == identityTestGroup+"/securitySettings" {
			return reply(200, s.security)
		}
		if r.Method == "GET" {
			if row := s.groups[name]; row != nil {
				return reply(200, row)
			}
			if row := s.members[name]; row != nil {
				return reply(200, row)
			}
			if strings.Contains(name, "/memberships/") {
				return reply(404, map[string]any{"error": map[string]any{"code": 404, "status": "NOT_FOUND", "message": "Error(4006): Membership does not exist."}})
			}
			return reply(403, map[string]any{"error": map[string]any{"code": 403, "status": "PERMISSION_DENIED", "message": "Error(2017): Permission denied for group resource (or it may not exist)."}})
		}
		if r.Method == "DELETE" {
			body, _ := io.ReadAll(r.Body)
			if len(body) != 0 {
				t.Fatal("DELETE must have no body")
			}
			if s.groups[name] == nil && s.members[name] == nil {
				t.Fatal("unexpected native delete target")
			}
			s.writes = append(s.writes, name)
			if !s.delayed {
				s.remove(name)
			}
			return reply(200, s.operation)
		}
		t.Fatalf("unexpected identity method %s %s", r.Method, r.URL)
		return nil, nil
	}
}
func (s *identityScenario) remove(name string) {
	delete(s.groups, name)
	delete(s.members, name)
	if !s.linger {
		for id := range s.members {
			if strings.HasPrefix(id, name+"/memberships/") {
				delete(s.members, id)
			}
		}
	}
}
func (s *identityScenario) runtime(t *testing.T) *Runtime {
	r := protocolRuntime(t, s.transport(t))
	credentials := r.credentials
	r.credentials = credentialFunc(func(ctx context.Context, id asset.ConnectionID) (contracts.Credential, error) {
		c, err := credentials.Resolve(ctx, id)
		c.Values["identity_group_parent"] = s.root
		return c, err
	})
	return r
}
func identityInventoryRequest(r *Runtime, kind string) contracts.InventoryRequest {
	resource := r.resourceKind(kind)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: identityInventorySource, Scope: asset.Scope{Kind: asset.ScopeGlobal, NativeID: "sample-project/global"}, ResourceKind: &resource}
}
func (s *identityScenario) inventory(t *testing.T, r *Runtime) []asset.Asset {
	t.Helper()
	values := []asset.Asset{}
	for _, kind := range []string{identityGroupType, identityMemberType} {
		batch, err := r.List(context.Background(), identityInventoryRequest(r, kind))
		if err != nil || !batch.Complete {
			t.Fatalf("identity inventory: %+v %v", batch, err)
		}
		for _, item := range batch.Items {
			if item.Normalized["project_id"] != nil || item.Normalized["project_number"] != nil || item.Normalized["_inventory_source"] != identityInventorySource || item.Scope.Kind != asset.ScopeGlobal {
				t.Fatalf("directory asset incorrectly project-owned: %+v", item)
			}
			values = append(values, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: kind, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}, Location: item.Location})
		}
	}
	return values
}
func identityReviewed(t *testing.T, s *identityScenario) (*Runtime, []asset.Asset, plan.Result, contracts.ActionRequest) {
	t.Helper()
	r := s.runtime(t)
	values := s.inventory(t, r)
	contributor, err := r.ServiceLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := contributor.Contribute(context.Background(), "scope", values)
	if err != nil || len(contribution.Unresolved) != 0 || len(contribution.Bindings) != len(s.members) {
		t.Fatalf("identity lifecycle: %+v %v", contribution, err)
	}
	for _, binding := range contribution.Bindings {
		if binding.CleanupPolicy != graph.CleanupDelegate || binding.Ownership != graph.OwnershipExclusive || binding.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] != true {
			t.Fatalf("wrong membership cascade: %+v", binding)
		}
	}
	parent := batchAsset(values, identityTestGroup)
	solved, err := plan.Solve(plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{parent.ID}, Relationships: contribution.Relationships, LifecycleBindings: contribution.Bindings})
	if err != nil || len(solved.Blockers) != 0 || len(solved.Steps) != 1 {
		t.Fatalf("group plan: %+v %v", solved, err)
	}
	request := dataformRequest(t, solved, values, parent)
	if len(request.LifecycleImpacts) != len(s.members) || len(request.PrerequisiteDeletions) != 0 {
		t.Fatalf("member links must be reviewed cascade impacts: %+v", request)
	}
	return r, values, solved, request
}
func TestIdentityGroupsNativeInventoryCascadeAndRestart(t *testing.T) {
	for _, variant := range []string{"ordinary", "empty_page", "security", "dynamic", "external", "no_members"} {
		t.Run(variant, func(t *testing.T) {
			s := newIdentityScenario()
			switch variant {
			case "empty_page":
				s.emptyPage = true
			case "security":
				object(s.groups[identityTestGroup]["labels"])["cloudidentity.googleapis.com/groups.security"] = ""
			case "dynamic":
				object(s.groups[identityTestGroup]["labels"])["cloudidentity.googleapis.com/groups.dynamic"] = ""
				s.groups[identityTestGroup]["dynamicGroupMetadata"] = map[string]any{"queries": []any{map[string]any{"resourceType": "USER", "query": "user.organizations.exists(org, org.department == 'Engineering')"}}, "status": map[string]any{"status": "UP_TO_DATE", "statusTime": "2026-01-05T00:00:00Z"}}
			case "external":
				s.root = "identitysources/source-1"
				for _, row := range s.groups {
					row["parent"] = s.root
					object(row["groupKey"])["namespace"] = s.root
					row["labels"] = map[string]any{"system/groups/external": ""}
				}
				for _, row := range s.members {
					object(row["preferredMemberKey"])["namespace"] = s.root
				}
			case "no_members":
				s.members = map[string]map[string]any{}
			}
			r, _, _, request := identityReviewed(t, s)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || result.Data["phase"] != "identity_group_delete" {
				t.Fatalf("identity delete: %+v %v", result, err)
			}
			if _, err := driver.Readback(context.Background(), request); err == nil {
				t.Fatal("ambiguous 403 succeeded without persisted receipt")
			}
			result = roundTripDataformJSON(t, result)
			restarted := s.runtime(t)
			driver, err = restarted.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(context.Background(), request, result)
			if err != nil || !wait.Done {
				t.Fatalf("identity restarted wait: %+v %v", wait, err)
			}
			request.ExecutionResult = &result
			read, err := driver.Readback(context.Background(), request)
			if err != nil || read.Exists || read.State != "delete_confirmed" {
				t.Fatalf("identity receipt readback: %+v %v", read, err)
			}
			if len(s.writes) != 1 || s.writes[0] != identityTestGroup || s.groups[identityTestNested] == nil || len(s.members) != 0 {
				t.Fatalf("native cascade altered users or nested group: writes=%v groups=%v", s.writes, s.groups)
			}
		})
	}
}
func TestIdentityMembershipIndependentUnlinkPreservesOtherMembersAndGroup(t *testing.T) {
	s := newIdentityScenario()
	r, values, _, _ := identityReviewed(t, s)
	for _, name := range []string{identityTestGroup + "/memberships/m-user", identityTestGroup + "/memberships/m-nested"} {
		value := batchAsset(values, name)
		request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: name}
		driver, err := r.ResolveAction(context.Background(), "connection", value)
		if err != nil {
			t.Fatal(err)
		}
		result, err := driver.Execute(context.Background(), request)
		if err != nil {
			t.Fatal(err)
		}
		wait, err := driver.Wait(context.Background(), request, result)
		if err != nil || !wait.Done {
			t.Fatalf("unlink wait: %+v %v", wait, err)
		}
		request.ExecutionResult = &result
		read, err := driver.Readback(context.Background(), request)
		if err != nil || read.Exists {
			t.Fatalf("unlink readback: %+v %v", read, err)
		}
	}
	if len(s.writes) != 2 || len(s.groups) != 2 || len(s.members) != 0 {
		t.Fatal("membership unlink deleted containing group or entity")
	}
}
func TestIdentityGroupsChangedPlanAndConfigurationBlockDeletion(t *testing.T) {
	for _, test := range []struct {
		name   string
		change func(*identityScenario, *contracts.ActionRequest)
	}{
		{"group_parent", func(s *identityScenario, _ *contracts.ActionRequest) {
			s.groups[identityTestGroup]["parent"] = "customers/Cother"
		}},
		{"group_identity", func(s *identityScenario, _ *contracts.ActionRequest) {
			s.groups[identityTestGroup]["name"] = "groups/replaced"
		}},
		{"group_recreated", func(s *identityScenario, _ *contracts.ActionRequest) {
			s.groups[identityTestGroup]["createTime"] = "2026-02-01T00:00:00Z"
		}},
		{"description", func(s *identityScenario, _ *contracts.ActionRequest) {
			s.groups[identityTestGroup]["description"] = "changed"
		}},
		{"alias", func(s *identityScenario, _ *contracts.ActionRequest) {
			s.groups[identityTestGroup]["additionalGroupKeys"] = []any{map[string]any{"id": "new@example.test"}}
		}},
		{"member_role", func(s *identityScenario, _ *contracts.ActionRequest) {
			row := s.members[identityTestGroup+"/memberships/m-user"]
			row["roles"] = append(array(row["roles"]), map[string]any{"name": "OWNER"})
		}},
		{"member_added", func(s *identityScenario, _ *contracts.ActionRequest) {
			row := cloneParameters(s.members[identityTestGroup+"/memberships/m-user"])
			row["name"] = identityTestGroup + "/memberships/new"
			s.members[text(row["name"])] = row
		}},
		{"member_removed", func(s *identityScenario, _ *contracts.ActionRequest) {
			delete(s.members, identityTestGroup+"/memberships/m-user")
		}},
		{"locked", func(s *identityScenario, _ *contracts.ActionRequest) {
			object(s.groups[identityTestGroup]["labels"])["cloudidentity.googleapis.com/groups.locked"] = ""
		}},
		{"missing_impact", func(_ *identityScenario, r *contracts.ActionRequest) { r.LifecycleImpacts = r.LifecycleImpacts[:1] }},
		{"retained_link", func(_ *identityScenario, r *contracts.ActionRequest) { r.LifecycleImpacts[0].Delete = false }},
		{"wrong_controller", func(_ *identityScenario, r *contracts.ActionRequest) { r.LifecycleImpacts[0].ControllerID = "foreign" }},
		{"wrong_child", func(_ *identityScenario, r *contracts.ActionRequest) {
			r.LifecycleImpacts[0].Asset.Identity.NativeType = identityGroupType
		}},
		{"wrong_connection", func(_ *identityScenario, r *contracts.ActionRequest) { r.Asset.Identity.ConnectionID = "another" }},
		{"wrong_partition", func(_ *identityScenario, r *contracts.ActionRequest) { r.Asset.Identity.Partition = "foreign" }},
		{"parameters", func(_ *identityScenario, r *contracts.ActionRequest) { r.Parameters = map[string]any{"force": true} }},
		{"tampered_proof", func(_ *identityScenario, r *contracts.ActionRequest) { r.Asset.Normalized[identityProof] = "tampered" }},
		{"tampered_manifest", func(_ *identityScenario, r *contracts.ActionRequest) { r.Asset.Normalized[identityMembers] = "[]" }},
		{"wrong_child_proof", func(_ *identityScenario, r *contracts.ActionRequest) {
			r.LifecycleImpacts[0].Asset.Normalized[identityParentProof] = "changed"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newIdentityScenario()
			r, _, _, request := identityReviewed(t, s)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			test.change(s, &request)
			if _, err := driver.Execute(context.Background(), request); err == nil {
				t.Fatal("changed identity plan accepted")
			}
			if len(s.writes) != 0 {
				t.Fatal("mutation preceded validation")
			}
		})
	}
}
func TestIdentityGroupsListFailuresDoNotProduceAuthoritativeAbsence(t *testing.T) {
	for _, test := range []struct {
		name     string
		response any
		status   int
	}{
		{"permission", map[string]any{}, 403}, {"not_found", map[string]any{}, 404}, {"server", map[string]any{}, 500},
		{"null_collection", map[string]any{"groups": nil}, 200}, {"object_collection", map[string]any{"groups": map[string]any{}}, 200},
		{"wrong_collection", map[string]any{"memberships": []any{}}, 200}, {"null_token", map[string]any{"nextPageToken": nil}, 200},
		{"bad_token", map[string]any{"nextPageToken": 123}, 200}, {"cycle", map[string]any{"nextPageToken": "cycle"}, 200},
		{"partial", map[string]any{"unreachable": []any{"directory"}}, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newIdentityScenario()
			r := s.runtime(t)
			if _, err := r.resolve(context.Background(), "connection"); err != nil {
				t.Fatal(err)
			}
			s.hook = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Path == "/v1/groups" && req.URL.Query().Get("pageSize") == "500" {
					return dataformResponse(req, test.status, test.response), true
				}
				return nil, false
			}
			if _, err := r.List(context.Background(), identityInventoryRequest(r, identityGroupType)); err == nil {
				t.Fatal("invalid directory list accepted")
			}
			for _, source := range r.InventorySources() {
				if source.Name == identityInventorySource && source.AuthoritativeDefault {
					t.Fatal("visibility-filtered directory marked authoritative")
				}
			}
		})
	}
}
func TestIdentityGroupReceiptCannotHideLiveOrUnverifiedResources(t *testing.T) {
	for _, variant := range []string{"surviving_group", "surviving_membership", "no_done", "wrong_binding", "operation_error", "malformed_done", "wrong_operation", "lost_receipt", "token_failure", "unauthenticated", "server_error", "malformed_403", "security_permission", "security_not_found", "changed_survivor"} {
		t.Run(variant, func(t *testing.T) {
			s := newIdentityScenario()
			s.delayed = true
			if variant == "security_permission" || variant == "security_not_found" {
				object(s.groups[identityTestGroup]["labels"])["cloudidentity.googleapis.com/groups.security"] = ""
			}
			r, _, _, request := identityReviewed(t, s)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			switch variant {
			case "surviving_group":
			case "surviving_membership":
				s.linger = true
				s.remove(identityTestGroup)
			case "security_permission", "security_not_found":
				status := 403
				if variant == "security_not_found" {
					status = 404
				}
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if strings.HasSuffix(req.URL.Path, "/securitySettings") {
						return dataformResponse(req, status, map[string]any{}), true
					}
					return nil, false
				}
			case "changed_survivor":
				s.groups[identityTestGroup]["description"] = "changed after delete"
			default:
				s.remove(identityTestGroup)
			}
			switch variant {
			case "no_done":
				object(result.Data["operation"])["done"] = false
			case "wrong_binding":
				result.Data["binding"] = "other"
			case "operation_error":
				object(result.Data["operation"])["error"] = map[string]any{"code": 7}
			case "malformed_done":
				object(result.Data["operation"])["done"] = "true"
			case "wrong_operation":
				result.ProviderOperationID = "operations/foreign"
			case "lost_receipt":
				result.Data = nil
			case "token_failure", "unauthenticated", "server_error", "malformed_403":
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v1/"+identityTestGroup {
						switch variant {
						case "token_failure":
							return dataformResponse(req, 403, map[string]any{"error": map[string]any{"status": "token_exchange_failed"}}), true
						case "unauthenticated":
							return dataformResponse(req, 401, map[string]any{"error": map[string]any{"status": "PERMISSION_DENIED"}}), true
						case "server_error":
							return dataformResponse(req, 503, map[string]any{}), true
						case "malformed_403":
							return apiResponse(req, 403, "not JSON"), true
						}
					}
					return nil, false
				}
			}
			request.ExecutionResult = &result
			read, err := driver.Readback(context.Background(), request)
			if err == nil && !read.Exists {
				t.Fatalf("unproven deletion succeeded: %+v", read)
			}
		})
	}
}
func TestIdentityGroupDynamicMembershipCannotBeUnlinkedDirectly(t *testing.T) {
	s := newIdentityScenario()
	object(s.groups[identityTestGroup]["labels"])["cloudidentity.googleapis.com/groups.dynamic"] = ""
	s.groups[identityTestGroup]["dynamicGroupMetadata"] = map[string]any{"queries": []any{map[string]any{"resourceType": "USER", "query": "user.name.value == 'Example'"}}, "status": map[string]any{"status": "UP_TO_DATE", "statusTime": "2026-01-05T00:00:00Z"}}
	r, values, _, _ := identityReviewed(t, s)
	member := batchAsset(values, identityTestGroup+"/memberships/m-user")
	driver, err := r.ResolveAction(context.Background(), "connection", member)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Action: "delete", Asset: member}); err == nil {
		t.Fatal("dynamic membership directly deleted")
	}
	if len(s.writes) != 0 {
		t.Fatal("unexpected dynamic member mutation")
	}
}
func TestIdentityGroupUnknownOperationCompletionUsesOnlyNativeReadback(t *testing.T) {
	s := newIdentityScenario()
	s.operation = map[string]any{"name": "operations/native-opaque"}
	s.delayed = true
	r, _, _, request := identityReviewed(t, s)
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || wait.Done {
		t.Fatalf("unfinished native group: %+v %v", wait, err)
	}
	s.remove(identityTestGroup)
	// A false/missing done flag cannot resolve the native ambiguous group 403.
	if wait, err := driver.Wait(context.Background(), request, result); err == nil && wait.Done {
		t.Fatal("unknown operation invented completed deletion")
	}
	s.hook = func(req *http.Request) (*http.Response, bool) {
		if req.URL.Path == "/v1/"+identityTestGroup {
			return dataformResponse(req, 404, map[string]any{}), true
		}
		return nil, false
	}
	wait, err = driver.Wait(context.Background(), request, result)
	if err != nil || !wait.Done {
		t.Fatalf("exact native absence not accepted: %+v %v", wait, err)
	}
	for _, call := range s.calls {
		if strings.Contains(call, "/operations/") {
			t.Fatal("invented operations endpoint")
		}
	}
}
func TestIdentityGroupsNativeSchemas(t *testing.T) {
	compiler := infraFixtureSchemas(t, "fixtures/identity-groups/native-schemas.json", "20260906", "29aa2b8422f6f11c5f5a10fcdeb601dbbd84dc16cb4c4c64a17bb9fa5a86ae3e")
	s := newIdentityScenario()
	for name, data := range map[string]map[string]any{"Group": s.groups[identityTestGroup], "Membership": s.members[identityTestGroup+"/memberships/m-user"], "SecuritySettings": s.security, "Operation": s.operation} {
		schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/" + name)
		if err != nil {
			t.Fatal(err)
		}
		if err := schema.Validate(data); err != nil {
			t.Fatalf("native %s: %v", name, err)
		}
		wrong := cloneParameters(data)
		wrong["name"] = 123
		if schema.Validate(wrong) == nil {
			t.Fatal("native schema accepted fabricated name")
		}
	}
	// The transient receipt must never be caller-supplied JSON.
	var request contracts.ActionRequest
	if err := json.Unmarshal([]byte(`{"execution_result":{"data":{"done":true}}}`), &request); err != nil || request.ExecutionResult != nil {
		t.Fatal("caller injected execution receipt")
	}
}

func TestIdentityGroupCredentialAndInvocationScope(t *testing.T) {
	credential, _ := testCredential(t)
	first, err := newClient(credential, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, root := range []string{"customers/C01234567", "identitysources/source-1"} {
		credential.Values["identity_group_parent"] = root
		c, err := newClient(credential, nil)
		if err != nil || c.identityParent != root || c.fingerprint == first.fingerprint {
			t.Fatalf("directory credential/cache scope: %+v %v", c, err)
		}
	}
	for _, root := range []string{"customers/my_customer", "customers/C", "customers/-", "customers/C123/extra", "customers/C123?alt=json", "customers/C123#x", "customers/C%2f123", "identitysources/..", "identitysources/-", "identitysources/", "organizations/123", "https://cloudidentity.googleapis.com/customers/C123"} {
		credential.Values["identity_group_parent"] = root
		if _, err := newClient(credential, nil); err == nil {
			t.Fatalf("invalid identity directory accepted: %s", root)
		}
	}
	for _, name := range []string{"groups/", "groups/-", "groups/../escape", "groups/g%2fescape", "groups/g?alt=json", "groups/g#fragment", "groups/g/memberships/m/extra", "projects/sample-project/groups/g"} {
		if _, err := identityName(identityGroupType, "//"+identityHost+"/"+name); err == nil {
			t.Fatalf("invalid group ID accepted: %s", name)
		}
	}
	s := newIdentityScenario()
	r := s.runtime(t)
	for _, call := range []contracts.Invocation{
		{Operation: "cloudidentity.groups.list", Parameters: map[string]any{"parent": s.root, "view": "FULL", "pageSize": 500}},
		{Operation: "cloudidentity.groups.get", Parameters: map[string]any{"name": identityTestGroup}},
		{Operation: "cloudidentity.groups.memberships.list", Parameters: map[string]any{"parent": identityTestGroup, "view": "FULL", "pageSize": 500}},
		{Operation: "cloudidentity.groups.memberships.get", Parameters: map[string]any{"name": identityTestGroup + "/memberships/m-user"}},
	} {
		call.ConnectionID = "connection"
		if _, err := r.Invoke(context.Background(), call); err != nil {
			t.Fatal(err)
		}
	}
	for _, call := range []contracts.Invocation{
		{Operation: "cloudidentity.groups.list", Parameters: map[string]any{"parent": "customers/Cforeign", "view": "FULL", "pageSize": 500}},
		{Operation: "cloudidentity.groups.delete", Parameters: map[string]any{"name": "groups/foreign"}},
		{Operation: "cloudidentity.groups.memberships.delete", Parameters: map[string]any{"name": "groups/foreign/memberships/m-user"}},
		{Operation: "cloudidentity.groups.memberships.get", Parameters: map[string]any{"name": identityTestGroup + "/memberships/../secret"}},
		{Operation: "cloudidentity.groups.getSecuritySettings", Parameters: map[string]any{"name": identityTestGroup + "/securitySettings/extra"}},
	} {
		call.ConnectionID = "connection"
		if _, err := r.Invoke(context.Background(), call); err == nil {
			t.Fatalf("unscoped raw invocation accepted: %+v", call)
		}
	}
	if len(s.writes) != 0 {
		t.Fatal("scope validation occurred after DELETE")
	}
	s.root = ""
	if _, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: "cloudidentity.groups.delete", Parameters: map[string]any{"name": identityTestGroup}}); err == nil {
		t.Fatal("project-only connection authorized directory deletion")
	}
	batch, err := r.List(context.Background(), identityInventoryRequest(r, identityGroupType))
	if err != nil || !batch.Complete || len(batch.Items) != 0 {
		t.Fatalf("optional directory default: %+v %v", batch, err)
	}
}
func TestIdentityGroupsMembershipPagingAndReadRaces(t *testing.T) {
	for _, variant := range []string{"wrong_member_parent", "invalid_roles", "duplicate_member", "members_page_cycle", "members_page_null", "member_detail_changed", "group_during_read", "security_during_read", "directory_during_read"} {
		t.Run(variant, func(t *testing.T) {
			s := newIdentityScenario()
			if variant == "security_during_read" {
				object(s.groups[identityTestGroup]["labels"])["cloudidentity.googleapis.com/groups.security"] = ""
			}
			r := s.runtime(t)
			if _, err := r.resolve(context.Background(), "connection"); err != nil {
				t.Fatal(err)
			}
			reads := 0
			s.hook = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Path == "/v1/"+identityTestGroup+"/memberships" {
					switch variant {
					case "wrong_member_parent":
						row := cloneParameters(s.members[identityTestGroup+"/memberships/m-user"])
						row["name"] = identityTestNested + "/memberships/m-user"
						return dataformResponse(req, 200, map[string]any{"memberships": []any{row}}), true
					case "invalid_roles":
						row := cloneParameters(s.members[identityTestGroup+"/memberships/m-user"])
						row["roles"] = nil
						return dataformResponse(req, 200, map[string]any{"memberships": []any{row}}), true
					case "duplicate_member":
						row := s.members[identityTestGroup+"/memberships/m-user"]
						return dataformResponse(req, 200, map[string]any{"memberships": []any{row, row}}), true
					case "members_page_cycle":
						return dataformResponse(req, 200, map[string]any{"nextPageToken": "cycle"}), true
					case "members_page_null":
						return dataformResponse(req, 200, map[string]any{"memberships": nil}), true
					case "group_during_read":
						s.groups[identityTestGroup]["description"] = "changed inside membership interval"
					}
				}
				if req.URL.Path == "/v1/"+identityTestGroup+"/memberships/m-user" && variant == "member_detail_changed" {
					row := cloneParameters(s.members[identityTestGroup+"/memberships/m-user"])
					row["updateTime"] = "2026-02-02T00:00:00Z"
					return dataformResponse(req, 200, row), true
				}
				if req.URL.Path == "/v1/"+identityTestGroup+"/securitySettings" && variant == "security_during_read" {
					reads++
					if reads > 1 {
						s.security["memberRestriction"] = map[string]any{"query": "changed"}
					}
				}
				if req.URL.Path == "/v1/groups" && req.URL.Query().Get("pageSize") == "500" && variant == "directory_during_read" {
					reads++
					if reads > 1 {
						return dataformResponse(req, 200, map[string]any{"groups": []any{s.groups[identityTestNested]}}), true
					}
				}
				return nil, false
			}
			if _, err := r.List(context.Background(), identityInventoryRequest(r, identityMemberType)); err == nil {
				t.Fatal("incomplete or racing native membership established coverage")
			}
		})
	}
}
func TestIdentityGroupRejectsUnexpectedDeleteResponses(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		body   any
	}{
		{"not_found", 404, map[string]any{}}, {"permission", 403, map[string]any{}}, {"conflict", 409, map[string]any{}}, {"server", 503, map[string]any{}}, {"empty_204", 204, map[string]any{}},
		{"done_string", 200, map[string]any{"done": "true"}}, {"error", 200, map[string]any{"done": true, "error": map[string]any{"code": 7}}}, {"name_url", 200, map[string]any{"done": true, "name": "https://foreign.test/operations/op"}}, {"response_array", 200, map[string]any{"done": true, "response": []any{}}},
	} {
		t.Run(test.name, func(t *testing.T) {
			s := newIdentityScenario()
			r, _, _, request := identityReviewed(t, s)
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			s.hook = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					return dataformResponse(req, test.status, test.body), true
				}
				return nil, false
			}
			if _, err := driver.Execute(context.Background(), request); err == nil {
				t.Fatal("unverified deletion response became completion receipt")
			}
		})
	}
}

func TestIdentityMembershipNativeDefaultRolesAndSetOrdering(t *testing.T) {
	s := newIdentityScenario()
	for _, member := range s.members {
		delete(member, "roles")
	}
	r := s.runtime(t)
	if values := s.inventory(t, r); len(values) != 4 {
		t.Fatal("native default membership role not inventoried")
	}
	row := s.members[identityTestGroup+"/memberships/m-user"]
	row["roles"] = []any{map[string]any{"name": "MEMBER"}, map[string]any{"name": "OWNER"}}
	reads := 0
	s.hook = func(req *http.Request) (*http.Response, bool) {
		if req.URL.Path == "/v1/"+identityTestGroup+"/memberships/m-user" {
			reads++
			copy := cloneParameters(row)
			if reads%2 == 0 {
				copy["roles"] = []any{map[string]any{"name": "OWNER"}, map[string]any{"name": "MEMBER"}}
			}
			return dataformResponse(req, 200, copy), true
		}
		return nil, false
	}
	if _, err := r.List(context.Background(), identityInventoryRequest(r, identityMemberType)); err != nil {
		t.Fatal("native role set ordering changed identity", err)
	}
}

func TestIdentityGroupDynamicReadinessAndQueryReview(t *testing.T) {
	s := newIdentityScenario()
	group := s.groups[identityTestGroup]
	object(group["labels"])["cloudidentity.googleapis.com/groups.dynamic"] = ""
	group["dynamicGroupMetadata"] = map[string]any{"queries": []any{map[string]any{"resourceType": "USER", "query": "user.name.value == 'Example'"}}, "status": map[string]any{"status": "UP_TO_DATE", "statusTime": "2026-01-05T00:00:00Z"}}
	r, _, _, request := identityReviewed(t, s)
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	state := object(object(group["dynamicGroupMetadata"])["status"])
	state["statusTime"] = "2026-01-06T00:00:00Z"
	if check, err := driver.Preflight(context.Background(), request); err != nil || !check.Allowed {
		t.Fatalf("observation timestamp invalidated unchanged dynamic query: %+v %v", check, err)
	}
	state["status"] = "UPDATING_MEMBERSHIPS"
	if check, err := driver.Preflight(context.Background(), request); err == nil && check.Allowed {
		t.Fatal("updating dynamic memberships allowed group deletion")
	}
	state["status"] = "UP_TO_DATE"
	object(array(object(group["dynamicGroupMetadata"])["queries"])[0])["query"] = "changed query"
	if check, err := driver.Preflight(context.Background(), request); err == nil && check.Allowed {
		t.Fatal("changed dynamic query accepted")
	}
	if len(s.writes) != 0 {
		t.Fatal("readiness checks mutated native group")
	}
}
func TestIdentityGroupsInventoryScopeBoundary(t *testing.T) {
	for name, change := range map[string]func(*contracts.InventoryRequest){
		"foreign_global": func(r *contracts.InventoryRequest) { r.Scope.NativeID = "foreign/global" },
		"foreign_project": func(r *contracts.InventoryRequest) {
			r.Scope.Kind = asset.ScopeProject
			r.Scope.NativeID = "foreign-project"
		},
		"region": func(r *contracts.InventoryRequest) {
			r.Scope.Kind = asset.ScopeRegion
			r.Scope.NativeID = "us-central1"
		},
		"cursor":       func(r *contracts.InventoryRequest) { r.Cursor = "stale" },
		"missing_kind": func(r *contracts.InventoryRequest) { r.ResourceKind = nil },
		"wrong_kind": func(r *contracts.InventoryRequest) {
			r.ResourceKind = &asset.ResourceKind{NativeType: organizationType}
		},
		"network": func(r *contracts.InventoryRequest) { r.NetworkTarget = &asset.ScanTarget{Kind: asset.ScanTargetVPC} },
	} {
		t.Run(name, func(t *testing.T) {
			s := newIdentityScenario()
			r := s.runtime(t)
			request := identityInventoryRequest(r, identityGroupType)
			change(&request)
			if _, err := r.List(context.Background(), request); err == nil {
				t.Fatal("invalid directory inventory scope accepted")
			}
		})
	}
}
