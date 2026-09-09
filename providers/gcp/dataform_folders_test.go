package gcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	dataformUS       = "projects/sample-project/locations/us-central1"
	dataformEU       = "projects/sample-project/locations/europe-west1"
	dataformTeam     = dataformUS + "/teamFolders/analytics"
	dataformNested   = dataformUS + "/folders/nested"
	dataformLeaf     = dataformUS + "/folders/leaf"
	dataformPersonal = dataformUS + "/folders/personal"
)

// A stateful native-wire fixture. IDs are physical siblings; membership is
// returned independently by the query endpoints and containingFolder fields.
type dataformFolderScenario struct {
	*dataformScenario
	seeds    map[string][]string
	contents map[string][]string
	queries  []string
	paginate bool
	override func(*http.Request) (*http.Response, bool)
}

func newDataformFolderScenario(t *testing.T) *dataformFolderScenario {
	s := &dataformFolderScenario{dataformScenario: newDataformScenario(t), seeds: map[string][]string{}, contents: map[string][]string{}, paginate: true}
	for _, row := range []struct{ name, display, parent, team string }{
		{dataformTeam, "Analytics", "", ""},
		{dataformNested, "Nested", dataformTeam, dataformTeam},
		{dataformLeaf, "Leaf", dataformNested, dataformTeam},
		{dataformPersonal, "Personal", "", ""},
		{dataformEU + "/folders/other", "Other", "", ""},
		{dataformEU + "/teamFolders/other", "Other team", "", ""},
	} {
		raw := map[string]any{"name": row.name, "displayName": row.display, "createTime": "2025-01-01T00:00:00Z", "creatorIamPrincipal": "user:fixture@example.com"}
		if row.parent != "" {
			raw["containingFolder"], raw["teamFolderName"] = row.parent, row.team
		}
		s.resources[row.name] = raw
		s.contents[row.name] = []string{}
	}
	s.resources[dataformRoot]["containingFolder"], s.resources[dataformRoot]["teamFolderName"] = dataformLeaf, dataformTeam
	s.contents[dataformTeam] = []string{dataformNested}
	s.contents[dataformNested] = []string{dataformLeaf}
	s.contents[dataformLeaf] = []string{dataformRoot}
	s.seeds[dataformUS+"/teamFolders:search"] = []string{dataformTeam}
	s.seeds[dataformUS+":queryUserRootContents"] = []string{dataformPersonal, dataformNested} // A shared non-root folder is also visible.
	s.seeds[dataformEU+"/teamFolders:search"] = []string{dataformEU + "/teamFolders/other"}
	s.seeds[dataformEU+":queryUserRootContents"] = []string{dataformEU + "/folders/other"}
	s.dataformScenario.handle = func(req *http.Request) (*http.Response, bool) {
		if s.override != nil {
			if response, handled := s.override(req); handled {
				return response, true
			}
		}
		name := strings.TrimPrefix(req.URL.Path, "/v1/")
		if req.Method == "DELETE" && (strings.Contains(name, "/folders/") || strings.Contains(name, "/teamFolders/")) {
			if req.URL.RawQuery != "" {
				t.Fatalf("invented recursive deletion: %s", req.URL)
			}
			for _, child := range s.contents[name] {
				if s.resources[child] != nil {
					t.Fatal("folder deleted before a native member")
				}
			}
		}
		if req.Method != "GET" {
			return nil, false
		}
		names, known := s.seeds[name]
		field := "entries"
		if strings.HasSuffix(name, "/teamFolders:search") {
			field = "results"
		}
		for _, suffix := range []string{":queryFolderContents", ":queryContents"} {
			if strings.HasSuffix(name, suffix) {
				names, known = s.contents[strings.TrimSuffix(name, suffix)]
			}
		}
		if !known {
			return nil, false
		}
		s.queries = append(s.queries, name+"?"+req.URL.RawQuery)
		if req.URL.Query().Get("pageSize") != "100" {
			t.Fatal("missing native contents page size")
		}
		if s.paginate && req.URL.Query().Get("pageToken") == "" {
			return dataformResponse(req, 200, map[string]any{field: []any{}, "nextPageToken": "visible-2"}), true
		}
		if token := req.URL.Query().Get("pageToken"); token != "" && token != "visible-2" {
			t.Fatal("incorrect contents token")
		}
		entries := []any{}
		for _, name := range names {
			if raw := s.resources[name]; raw != nil {
				wrapper := "folder"
				if strings.Contains(name, "/teamFolders/") {
					wrapper = "teamFolder"
				}
				if strings.Contains(name, "/repositories/") {
					wrapper = "repository"
				}
				entries = append(entries, map[string]any{wrapper: raw})
			}
		}
		return dataformResponse(req, 200, map[string]any{field: entries}), true
	}
	return s
}

func folderRequest(r *Runtime, kind, region string) contracts.InventoryRequest {
	request := productRequest(r, kind, region)
	request.Source = dataformInventorySource
	return request
}

func (s *dataformFolderScenario) inventory(t *testing.T, r *Runtime, region string) []asset.Asset {
	t.Helper()
	assets := s.dataformScenario.inventory(t, r, region)
	for _, kind := range []string{dataformFolderType, dataformTeamFolderType} {
		request := folderRequest(r, kind, region)
		request.Limit = 1
		for i := 0; i < 20; i++ {
			batch, err := r.List(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			for _, item := range batch.Items {
				if item.Actionable == nil || !*item.Actionable || item.Normalized["_inventory_source"] != dataformInventorySource || text(item.Normalized[dataformProof]) == "" {
					t.Fatalf("missing native folder evidence: %+v", item)
				}
				assets = append(assets, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: item.NativeType, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}, Location: item.Location})
			}
			if batch.Complete {
				break
			}
			if batch.NextCursor == "" || i == 19 {
				t.Fatal("unfinished folder pagination")
			}
			request.Cursor = batch.NextCursor
		}
	}
	return assets
}

func folderPlan(t *testing.T, r *Runtime, assets []asset.Asset, root string) (plan.Result, plan.Input) {
	t.Helper()
	_, input := dataformPlan(t, r, assets)
	input.ResolvedAssetIDs = []asset.AssetID{dataformAsset(assets, root).ID}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) > 0 {
		t.Fatalf("folder plan: %+v %v", result, err)
	}
	return result, input
}

func TestDataformFolderNativeInventoryAndLifecycle(t *testing.T) {
	s := newDataformFolderScenario(t)
	r := protocolRuntime(t, s.transport(t))
	assets := s.inventory(t, r, "project")
	if len(assets) != 14 {
		t.Fatalf("lost region, ancestor, or repository child: %d", len(assets))
	}
	for _, source := range r.InventorySources() {
		if source.Name == dataformInventorySource && (source.AuthoritativeDefault || !source.KindSpecific || !source.NetworkClosure) {
			t.Fatalf("visibility-filtered search claimed authority: %+v", source)
		}
	}
	for _, name := range []string{dataformNested, dataformLeaf, dataformRoot} {
		value := dataformAsset(assets, name)
		parent := text(s.resources[name]["containingFolder"])
		kind := dataformFolderType
		if parent == dataformTeam {
			kind = dataformTeamFolderType
		}
		refs := array(value.Normalized[referenceKey(kind)])
		if text(value.Normalized["_dataform_container_name"]) != "//dataform.googleapis.com/"+parent || len(refs) != 1 || text(refs[0]) != "//dataform.googleapis.com/"+parent || text(value.Normalized["_dataform_container_configuration"]) == "" {
			t.Fatalf("lost native folder membership: %+v", value)
		}
	}
	result, input := folderPlan(t, r, assets, dataformTeam)
	if len(result.Steps) != 8 || len(result.ImpactItems) != 1 {
		t.Fatalf("incomplete nested cleanup: %+v", result)
	}
	for _, mode := range []string{"folder-only", "repo-only", "deduplicate", "retain-folder", "retain-repository", "protect-folder"} {
		t.Run(mode, func(t *testing.T) {
			in := roundTripDataformJSON(t, input)
			switch mode {
			case "folder-only":
				in.ResolvedAssetIDs = []asset.AssetID{dataformAsset(assets, dataformLeaf).ID}
			case "repo-only":
				in.ResolvedAssetIDs = []asset.AssetID{dataformAsset(assets, dataformRoot).ID}
			case "deduplicate":
				in.ResolvedAssetIDs = append(in.ResolvedAssetIDs, dataformAsset(assets, dataformLeaf).ID)
			case "retain-folder":
				in.RequestOptions = map[asset.AssetID]map[string]any{dataformAsset(assets, dataformTeam).ID: {"retain_resources": []string{string(dataformAsset(assets, dataformNested).ID)}}}
			case "retain-repository":
				in.RequestOptions = map[asset.AssetID]map[string]any{dataformAsset(assets, dataformLeaf).ID: {"retain_resources": []string{string(dataformAsset(assets, dataformRoot).ID)}}}
			case "protect-folder":
				in.Protections = []plan.ProtectionPolicy{{AssetID: dataformAsset(assets, dataformLeaf).ID, Protected: true, Reason: "protected folder", Source: "user-policy"}}
			}
			p, err := plan.Solve(in)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "retain-") || mode == "protect-folder" {
				if len(p.Blockers) == 0 {
					t.Fatalf("unsafe folder policy accepted: %+v", p)
				}
				return
			}
			want := 8
			if mode == "folder-only" {
				want = 6
			}
			if mode == "repo-only" {
				want = 5
			}
			if len(p.Blockers) != 0 || len(p.Steps) != want {
				t.Fatalf("incorrect scope: %+v", p)
			}
		})
	}
	root := dataformAsset(assets, dataformTeam)
	driver, _ := r.ResolveAction(context.Background(), "connection", root)
	if _, err := driver.Execute(context.Background(), dataformRequest(t, result, assets, root)); err == nil || len(s.mutations) > 0 {
		t.Fatal("folder deleted before children")
	}
	for _, name := range []string{dataformRoot + "/workflowConfigs/transform", dataformRoot + "/releaseConfigs/hourly", dataformRoot + "/workspaces/development", dataformRoot + "/workflowInvocations/running", dataformRoot, dataformLeaf, dataformNested, dataformTeam} {
		value := dataformAsset(assets, name)
		request := roundTripDataformJSON(t, dataformRequest(t, result, assets, value))
		driver, err := protocolRuntime(t, s.transport(t)).ResolveAction(context.Background(), "connection", value)
		if err != nil {
			t.Fatal(err)
		}
		response, err := driver.Execute(context.Background(), request)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		response = roundTripDataformJSON(t, response)
		if value.Identity.NativeType == dataformInvocationType {
			if response.Data["phase"] != "dataform_cancel" {
				t.Fatal("running workflow not cancelled")
			}
			s.resources[name]["state"] = "CANCELLED"
			wait, err := driver.Wait(context.Background(), request, response)
			if err != nil || wait.Done || wait.Data["phase"] != "dataform_delete" {
				t.Fatalf("cancel -> delete: %+v %v", wait, err)
			}
			response.Data = roundTripDataformJSON(t, wait.Data)
		}
		wait, err := driver.Wait(context.Background(), request, response)
		if err != nil || wait.Done {
			t.Fatalf("HTTP acknowledgment mistaken for absence: %+v %v", wait, err)
		}
		delete(s.resources, name)
		if name == dataformRoot {
			delete(s.resources, dataformRoot+"/compilationResults/compiled")
		}
		driver, _ = protocolRuntime(t, s.transport(t)).ResolveAction(context.Background(), "connection", value)
		wait, err = driver.Wait(context.Background(), request, response)
		if err != nil || !wait.Done {
			t.Fatalf("%s restart readback: %+v %v", name, wait, err)
		}
	}
	if len(s.mutations) != 9 || len(s.resources) != 5 {
		t.Fatalf("wrong native effects: %+v remaining=%+v", s.mutations, s.resources)
	}
	if s.resources[dataformPersonal] == nil || s.resources[dataformEU+"/repositories/other"] == nil {
		t.Fatal("unrelated resource removed")
	}
}

func TestDataformFolderCursorAndVisibility(t *testing.T) {
	for _, mode := range []string{"volatile-metadata", "changed-folder", "changed-member", "changed-connection", "changed-kind", "changed-scope", "negative-offset", "oversized-offset", "malformed", "shared-repository-seed", "hidden-team-search"} {
		t.Run(mode, func(t *testing.T) {
			s := newDataformFolderScenario(t)
			r := protocolRuntime(t, s.transport(t))
			request := folderRequest(r, dataformFolderType, "project")
			request.Limit = 1
			first, err := r.List(context.Background(), request)
			if err != nil || first.Complete || first.NextCursor == "" {
				t.Fatalf("first page: %+v %v", first, err)
			}
			request.Cursor = first.NextCursor
			switch mode {
			case "volatile-metadata":
				for _, raw := range s.resources {
					raw["updateTime"] = "2026-09-09T00:00:00Z"
					raw["internalMetadata"] = "{\"generation\":2}"
				}
			case "changed-folder":
				s.resources[dataformPersonal]["displayName"] = "Renamed"
			case "changed-member":
				s.resources[dataformRoot]["displayName"] = "Moved repository configuration"
			case "changed-connection":
				// Bind the cursor to a different connection without asking the
				// fixture's deliberately single-connection credential resolver.
				request.ConnectionID = "another"
			case "changed-kind":
				kind := r.resourceKind(dataformTeamFolderType)
				request.ResourceKind = &kind
			case "changed-scope":
				request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-central1"}
			case "negative-offset", "oversized-offset":
				raw, _ := base64.RawURLEncoding.DecodeString(request.Cursor)
				var cursor map[string]any
				_ = json.Unmarshal(raw, &cursor)
				cursor["offset"] = -1
				if mode == "oversized-offset" {
					cursor["offset"] = 999
				}
				raw, _ = json.Marshal(cursor)
				request.Cursor = base64.RawURLEncoding.EncodeToString(raw)
			case "malformed":
				request.Cursor = "%%%"
			case "shared-repository-seed":
				s.seeds[dataformUS+"/teamFolders:search"] = nil
				s.seeds[dataformUS+":queryUserRootContents"] = []string{dataformRoot, dataformPersonal}
			case "hidden-team-search":
				s.seeds[dataformUS+"/teamFolders:search"] = nil
				s.seeds[dataformUS+":queryUserRootContents"] = []string{dataformPersonal}
				request.Cursor = ""
				request.Limit = 100
			}
			var batch contracts.InventoryBatch
			if mode == "changed-connection" {
				c, resolveErr := r.resolve(context.Background(), "connection")
				if resolveErr != nil {
					t.Fatal(resolveErr)
				}
				batch, err = r.listDataformFolders(context.Background(), c, request)
			} else {
				batch, err = r.List(context.Background(), request)
			}
			switch mode {
			case "volatile-metadata", "shared-repository-seed":
				if err != nil || len(batch.Items) != 1 {
					t.Fatalf("stable tree lost: %+v %v", batch, err)
				}
			case "hidden-team-search":
				if err != nil || !batch.Complete || len(batch.Items) != 2 {
					t.Fatalf("visibility restriction: %+v %v", batch, err)
				}
			default:
				if err == nil || batch.Complete {
					t.Fatalf("invalid cursor accepted: %+v %v", batch, err)
				}
			}
		})
	}
}

func TestDataformFolderInventoryRejectsIncompleteOrInvalidTree(t *testing.T) {
	for _, mode := range []string{"permission", "partial", "error-body", "unreachable", "malformed-list", "invalid-token", "token-cycle", "duplicate", "foreign-project", "foreign-region", "wrong-kind", "multiple-wrappers", "missing-detail", "detail-permission", "detail-partial", "detail-recreated", "missing-parent", "wrong-parent", "wrong-team", "parent-cycle", "self-parent", "invalid-container-type", "invalid-team-type", "team-without-parent", "changed-final-set", "parent-recreated"} {
		t.Run(mode, func(t *testing.T) {
			s := newDataformFolderScenario(t)
			count := 0
			switch mode {
			case "missing-parent":
				delete(s.resources, dataformTeam)
			case "wrong-parent":
				s.resources[dataformLeaf]["containingFolder"] = dataformPersonal
			case "wrong-team":
				s.resources[dataformLeaf]["teamFolderName"] = dataformEU + "/teamFolders/other"
			case "parent-cycle":
				s.resources[dataformNested]["containingFolder"] = dataformLeaf
			case "self-parent":
				s.resources[dataformNested]["containingFolder"] = dataformNested
			case "invalid-container-type":
				s.resources[dataformNested]["containingFolder"] = 123
			case "invalid-team-type":
				s.resources[dataformNested]["teamFolderName"] = 123
			case "team-without-parent":
				s.resources[dataformPersonal]["teamFolderName"] = dataformTeam
			}
			s.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "GET" {
					return nil, false
				}
				if req.URL.Path == "/v1/"+dataformLeaf {
					switch mode {
					case "missing-detail":
						return dataformResponse(req, 404, map[string]any{}), true
					case "detail-permission":
						return dataformResponse(req, 403, map[string]any{}), true
					case "detail-partial":
						return dataformResponse(req, 206, s.resources[dataformLeaf]), true
					case "detail-recreated":
						raw := roundTripDataformJSON(t, s.resources[dataformLeaf])
						raw["createTime"] = "2026-01-01T00:00:00Z"
						return dataformResponse(req, 200, raw), true
					case "parent-recreated":
						s.resources[dataformNested]["createTime"] = "2026-01-01T00:00:00Z"
					}
				}
				if req.URL.Path != "/v1/"+dataformNested+":queryFolderContents" {
					return nil, false
				}
				count++
				raw := roundTripDataformJSON(t, s.resources[dataformLeaf])
				body := map[string]any{"entries": []any{map[string]any{"folder": raw}}}
				switch mode {
				case "permission":
					return dataformResponse(req, 403, map[string]any{}), true
				case "partial":
					return dataformResponse(req, 206, body), true
				case "error-body":
					body = map[string]any{"error": map[string]any{"code": 404}}
				case "unreachable":
					body["unreachable"] = []string{"us-central1"}
				case "malformed-list":
					body["entries"] = 123
				case "invalid-token":
					body["nextPageToken"] = 123
				case "token-cycle":
					body["nextPageToken"] = "loop"
				case "duplicate":
					body["entries"] = []any{map[string]any{"folder": raw}, map[string]any{"folder": raw}}
				case "foreign-project":
					raw["name"] = strings.Replace(dataformLeaf, "sample-project", "foreign", 1)
				case "foreign-region":
					raw["name"] = strings.Replace(dataformLeaf, "us-central1", "europe-west1", 1)
				case "wrong-kind":
					body["entries"] = []any{map[string]any{"teamFolder": s.resources[dataformTeam]}}
				case "multiple-wrappers":
					body["entries"] = []any{map[string]any{"folder": raw, "repository": s.resources[dataformRoot]}}
				case "changed-final-set":
					if count > 1 {
						body["entries"] = []any{}
					}
				default:
					return nil, false
				}
				return dataformResponse(req, 200, body), true
			}
			r := protocolRuntime(t, s.transport(t))
			batch, err := r.List(context.Background(), folderRequest(r, dataformFolderType, "project"))
			if err == nil || batch.Complete || len(s.mutations) > 0 {
				t.Fatalf("invalid tree accepted: %+v %v", batch, err)
			}
		})
	}
}

func TestDataformFolderPrerequisitesAndMoves(t *testing.T) {
	for _, mode := range []string{"child-present", "new-child", "moved-child", "child-403", "child-206", "child-error-body", "parent-moved", "parent-recreated", "container-recreated", "missing-container-proof", "container-404", "container-403", "foreign-kind", "foreign-controller", "foreign-connection", "foreign-partition", "foreign-parent", "false-team", "retained-child", "duplicate-child", "parent-404-child-present", "parent-404-child-403", "parent-404-child-absent"} {
		t.Run(mode, func(t *testing.T) {
			s := newDataformFolderScenario(t)
			r := protocolRuntime(t, s.transport(t))
			assets := s.inventory(t, r, "project")
			result, _ := folderPlan(t, r, assets, dataformTeam)
			value := dataformAsset(assets, dataformNested)
			request := roundTripDataformJSON(t, dataformRequest(t, result, assets, value))
			if len(request.PrerequisiteDeletions) != 1 {
				t.Fatal("missing nested prerequisite")
			}
			if mode != "child-present" && mode != "parent-404-child-present" && mode != "moved-child" {
				delete(s.resources, dataformLeaf)
			}
			switch mode {
			case "new-child":
				name := dataformUS + "/repositories/new"
				s.resources[name] = map[string]any{"name": name, "containingFolder": dataformNested, "teamFolderName": dataformTeam}
				s.contents[dataformNested] = append(s.contents[dataformNested], name)
			case "moved-child":
				s.resources[dataformLeaf]["containingFolder"] = dataformTeam
				s.contents[dataformNested] = nil
			case "parent-moved":
				s.resources[dataformNested]["containingFolder"] = dataformPersonal
				delete(s.resources[dataformNested], "teamFolderName")
			case "parent-recreated":
				s.resources[dataformNested]["createTime"] = "2026-01-01T00:00:00Z"
			case "container-recreated":
				s.resources[dataformTeam]["createTime"] = "2026-01-01T00:00:00Z"
			case "missing-container-proof":
				delete(request.Asset.Normalized, "_dataform_container_configuration")
			case "container-404":
				delete(s.resources, dataformTeam)
			case "foreign-kind":
				request.PrerequisiteDeletions[0].Asset.Identity.NativeType = dataformTeamFolderType
			case "foreign-controller":
				request.PrerequisiteDeletions[0].ControllerID = "other"
			case "foreign-connection":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "other"
			case "foreign-partition":
				request.PrerequisiteDeletions[0].Asset.Identity.Partition = "other"
			case "foreign-parent":
				request.PrerequisiteDeletions[0].Asset.Normalized["containingFolder"] = dataformPersonal
			case "false-team":
				request.PrerequisiteDeletions[0].Asset.Normalized["teamFolderName"] = dataformEU + "/teamFolders/other"
			case "retained-child":
				request.PrerequisiteDeletions[0].Delete = false
			case "duplicate-child":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, request.PrerequisiteDeletions[0])
			}
			if strings.HasPrefix(mode, "parent-404") {
				delete(s.resources, dataformNested)
			}
			s.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "GET" {
					return nil, false
				}
				if req.URL.Path == "/v1/"+dataformTeam && mode == "container-403" {
					return dataformResponse(req, 403, map[string]any{}), true
				}
				if req.URL.Path == "/v1/"+dataformLeaf {
					switch mode {
					case "child-403", "parent-404-child-403":
						return dataformResponse(req, 403, map[string]any{}), true
					case "child-206":
						return dataformResponse(req, 206, map[string]any{}), true
					case "child-error-body":
						return dataformResponse(req, 200, map[string]any{"error": map[string]any{"code": 404}}), true
					}
				}
				return nil, false
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", value)
			check, err := driver.Preflight(context.Background(), request)
			if mode == "parent-404-child-absent" {
				if err != nil || !check.Allowed || !check.Absent {
					t.Fatalf("confirmed absence: %+v %v", check, err)
				}
				return
			}
			if err == nil && check.Allowed {
				t.Fatalf("unsafe preflight accepted: %+v", check)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.mutations) > 0 {
				t.Fatal("unsafe mutation executed")
			}
		})
	}
}

func TestDataformFolderContributionNeedsCompleteReviewedMembers(t *testing.T) {
	for _, mode := range []string{"missing", "duplicate", "configuration", "foreign-connection", "foreign-partition", "missing-live-member", "late-member"} {
		t.Run(mode, func(t *testing.T) {
			s := newDataformFolderScenario(t)
			r := protocolRuntime(t, s.transport(t))
			assets := s.inventory(t, r, "project")
			index := slices.IndexFunc(assets, func(value asset.Asset) bool {
				return value.Identity.NativeID == "//dataform.googleapis.com/"+dataformLeaf
			})
			switch mode {
			case "missing":
				assets = slices.Delete(assets, index, index+1)
			case "duplicate":
				assets = append(assets, assets[index])
			case "configuration":
				s.resources[dataformLeaf]["displayName"] = "Unreviewed"
			case "foreign-connection":
				assets[index].Identity.ConnectionID = "other"
			case "foreign-partition":
				assets[index].Identity.Partition = "other"
			case "missing-live-member":
				delete(s.resources, dataformLeaf)
			case "late-member":
				s.override = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v1/"+dataformLeaf {
						name := dataformUS + "/folders/new"
						s.resources[name] = map[string]any{"name": name, "containingFolder": dataformNested, "teamFolderName": dataformTeam}
						if !slices.Contains(s.contents[dataformNested], name) {
							s.contents[dataformNested] = append(s.contents[dataformNested], name)
						}
					}
					return nil, false
				}
			}
			contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
			contribution, err := contributor.Contribute(context.Background(), "scope", assets)
			if err == nil && len(contribution.Unresolved) == 0 {
				t.Fatalf("unreviewed membership accepted: %+v", contribution)
			}
		})
	}
}

func TestDataformFolderRegionalInventoryAndEmptyRoots(t *testing.T) {
	s := newDataformFolderScenario(t)
	r := protocolRuntime(t, s.transport(t))
	batch, err := r.List(context.Background(), folderRequest(r, dataformFolderType, "us-central1"))
	if err != nil || !batch.Complete || len(batch.Items) != 3 {
		t.Fatalf("regional tree: %+v %v", batch, err)
	}
	for _, query := range s.queries {
		if strings.Contains(query, "europe-west1") {
			t.Fatal("regional scan escaped location")
		}
	}
	s.seeds = map[string][]string{dataformUS + ":queryUserRootContents": {}, dataformUS + "/teamFolders:search": {}}
	batch, err = r.List(context.Background(), folderRequest(r, dataformFolderType, "us-central1"))
	if err != nil || !batch.Complete || len(batch.Items) != 0 {
		t.Fatalf("empty visible roots: %+v %v", batch, err)
	}
	for _, scope := range []string{"global", "other-region"} {
		batch, err = r.List(context.Background(), folderRequest(r, dataformFolderType, scope))
		if scope == "global" && (err == nil || batch.Complete) {
			t.Fatal("invented global folders")
		}
	}
}

func TestDataformFolderDirectOrdering(t *testing.T) {
	s := newDataformFolderScenario(t)
	r := protocolRuntime(t, s.transport(t))
	assets := s.inventory(t, r, "project")
	result, input := folderPlan(t, r, assets, dataformTeam)
	for _, binding := range input.LifecycleBindings {
		if binding.ControllerAssetID == dataformAsset(assets, dataformTeam).ID || binding.ControllerAssetID == dataformAsset(assets, dataformNested).ID || binding.ControllerAssetID == dataformAsset(assets, dataformLeaf).ID {
			if binding.CleanupPolicy != graph.CleanupDirect || !binding.DirectCleanupAllowed || binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] == true {
				t.Fatalf("unreviewed recursive delete guarantee: %+v", binding)
			}
		}
	}
	indices := map[asset.AssetID]int{}
	for i, step := range result.Steps {
		indices[step.AssetID] = i
	}
	// Solve returns the concrete prerequisite order consumed by execution.
	order := []string{dataformRoot, dataformLeaf, dataformNested, dataformTeam}
	positions := []int{}
	for _, name := range order {
		positions = append(positions, indices[dataformAsset(assets, name).ID])
	}
	if !sort.IntsAreSorted(positions) {
		t.Fatalf("folder before repository: %+v", result.Steps)
	}
}

func TestDataformFolderAncestorChangesStopChildWritesAndRestarts(t *testing.T) {
	for _, mode := range []string{"moved-ancestor", "recreated-team", "ancestor-403", "ancestor-404", "missing-chain", "cancel-restart"} {
		t.Run(mode, func(t *testing.T) {
			s := newDataformFolderScenario(t)
			r := protocolRuntime(t, s.transport(t))
			assets := s.inventory(t, r, "project")
			value := dataformAsset(assets, dataformRoot+"/workflowInvocations/running")
			request := roundTripDataformJSON(t, contracts.ActionRequest{Asset: value, Action: "delete"})
			driver, _ := r.ResolveAction(context.Background(), "connection", value)
			var response contracts.ActionResult
			if mode == "cancel-restart" {
				var err error
				response, err = driver.Execute(context.Background(), request)
				if err != nil || response.Data["phase"] != "dataform_cancel" {
					t.Fatalf("cancellation: %+v %v", response, err)
				}
				response = roundTripDataformJSON(t, response)
				s.resources[dataformRoot+"/workflowInvocations/running"]["state"] = "CANCELLED"
			}
			before := len(s.mutations)
			switch mode {
			case "moved-ancestor", "cancel-restart":
				s.resources[dataformNested]["containingFolder"] = dataformUS + "/folders/new-parent"
				s.resources[dataformUS+"/folders/new-parent"] = map[string]any{"name": dataformUS + "/folders/new-parent", "containingFolder": dataformTeam, "teamFolderName": dataformTeam}
			case "recreated-team":
				s.resources[dataformTeam]["createTime"] = "2026-01-01T00:00:00Z"
			case "missing-chain":
				delete(request.Asset.Normalized, dataformContainerChain)
			case "ancestor-404":
				delete(s.resources, dataformTeam)
			case "ancestor-403":
				s.override = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v1/"+dataformTeam {
						return dataformResponse(req, 403, map[string]any{}), true
					}
					return nil, false
				}
			}
			driver, _ = protocolRuntime(t, s.transport(t)).ResolveAction(context.Background(), "connection", value)
			if mode == "cancel-restart" {
				if wait, err := driver.Wait(context.Background(), request, response); err == nil || wait.Done {
					t.Fatalf("changed ancestor resumed cancellation: %+v %v", wait, err)
				}
			} else if _, err := driver.Execute(context.Background(), request); err == nil {
				t.Fatal("child write ignored changed ancestor")
			}
			if len(s.mutations) != before {
				t.Fatalf("unreviewed ancestor caused write: %+v", s.mutations)
			}
		})
	}
}
