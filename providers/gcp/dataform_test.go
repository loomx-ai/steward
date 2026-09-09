package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const dataformRoot = "projects/sample-project/locations/us-central1/repositories/warehouse"

// Literal API collections and names are deliberately independent of catalog
// generation, YAML discovery rules, and serviceCascadeRules.
var dataformWireKinds = map[string]string{
	"repositories": "Repository", "workspaces": "Workspace", "releaseConfigs": "ReleaseConfig",
	"workflowConfigs": "WorkflowConfig", "workflowInvocations": "WorkflowInvocation", "compilationResults": "CompilationResult",
}

type dataformScenario struct {
	resources map[string]map[string]any
	members   map[string][]string
	mutations []string
	emptyPage bool
	handle    func(*http.Request) (*http.Response, bool)
}

func newDataformScenario(t *testing.T) *dataformScenario {
	t.Helper()
	raw, err := os.ReadFile("fixtures/dataform/resources.json")
	if err != nil {
		t.Fatal(err)
	}
	var records map[string][]map[string]any
	if err := json.Unmarshal(raw, &records); err != nil {
		t.Fatal(err)
	}
	s := &dataformScenario{resources: map[string]map[string]any{}, members: map[string][]string{}}
	for _, resources := range records {
		for _, raw := range resources {
			name := text(raw["name"])
			s.resources[name] = raw
			collection := name[:strings.LastIndex(name, "/")]
			s.members[collection] = append(s.members[collection], name)
		}
	}
	for name := range s.resources {
		if strings.Contains(name, "/repositories/") && len(strings.Split(name, "/")) == 6 {
			for collection := range dataformWireKinds {
				if collection != "repositories" && s.members[name+"/"+collection] == nil {
					s.members[name+"/"+collection] = []string{}
				}
			}
		}
	}
	return s
}

func (s *dataformScenario) transport(t *testing.T) roundTripFunc {
	t.Helper()
	return func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "dataform.googleapis.com" || !strings.HasPrefix(req.URL.Path, "/v1/") {
			t.Fatalf("unexpected native API: %s %s", req.Method, req.URL)
		}
		if s.handle != nil {
			if result, handled := s.handle(req); handled {
				return result, nil
			}
		}
		name := strings.TrimPrefix(req.URL.Path, "/v1/")
		respond := func(status int, data any) (*http.Response, error) {
			raw, _ := json.Marshal(data)
			return apiResponse(req, status, string(raw)), nil
		}
		if req.Method == "GET" {
			if name == "projects/sample-project/locations" {
				if req.URL.Query().Get("pageToken") == "" {
					return respond(200, map[string]any{"locations": []any{}, "nextPageToken": "locations-2"})
				}
				if req.URL.Query().Get("pageToken") != "locations-2" {
					t.Fatal("wrong locations token")
				}
				return respond(200, map[string]any{"locations": []any{map[string]any{"name": "projects/sample-project/locations/us-central1", "locationId": "us-central1"}, map[string]any{"name": "projects/sample-project/locations/europe-west1", "locationId": "europe-west1"}}})
			}
			if names, isList := s.members[name]; isList {
				if s.emptyPage && req.URL.Query().Get("pageToken") == "" {
					return respond(200, map[string]any{last(name): []any{}, "nextPageToken": "resources-2"})
				}
				if token := req.URL.Query().Get("pageToken"); token != "" && token != "resources-2" {
					t.Fatal("wrong resource token")
				}
				items := []any{}
				for _, name := range names {
					if raw := s.resources[name]; raw != nil {
						items = append(items, raw)
					}
				}
				return respond(200, map[string]any{last(name): items})
			}
			if raw := s.resources[name]; raw != nil {
				return respond(200, raw)
			}
			return respond(404, map[string]any{"error": map[string]any{"code": 404, "status": "NOT_FOUND"}})
		}
		s.mutations = append(s.mutations, req.Method+" "+name)
		if req.Method == "POST" && name == dataformRoot+"/workflowInvocations/running:cancel" {
			var body map[string]any
			if json.NewDecoder(req.Body).Decode(&body) != nil || body == nil || len(body) != 0 {
				t.Fatal("cancellation must use native empty request")
			}
			return respond(200, map[string]any{})
		}
		if req.Method != "DELETE" || s.resources[name] == nil {
			t.Fatalf("unexpected mutation: %s %s", req.Method, req.URL)
		}
		if strings.Contains(name, "/compilationResults/") {
			t.Fatal("CompilationResult has no native DELETE")
		}
		if name == dataformRoot {
			if req.URL.Query().Get("force") != "true" {
				t.Fatal("missing reviewed native force")
			}
			for _, collection := range []string{"workspaces", "releaseConfigs", "workflowConfigs", "workflowInvocations"} {
				for _, name := range s.members[dataformRoot+"/"+collection] {
					if s.resources[name] != nil {
						t.Fatal("parent deleted before prerequisite")
					}
				}
			}
		} else if req.URL.RawQuery != "" {
			t.Fatalf("invented child delete parameters %s", req.URL)
		}
		return respond(200, map[string]any{})
	}
}

func (s *dataformScenario) inventory(t *testing.T, r *Runtime, region string) []asset.Asset {
	t.Helper()
	var result []asset.Asset
	for _, collection := range []string{"repositories", "workspaces", "releaseConfigs", "workflowConfigs", "workflowInvocations", "compilationResults"} {
		request := productRequest(r, "dataform.googleapis.com/"+dataformWireKinds[collection], region)
		complete := false
		for i := 0; i < 20; i++ {
			page, err := r.List(context.Background(), request)
			if err != nil {
				t.Fatalf("%s inventory: %v", collection, err)
			}
			for _, item := range page.Items {
				capabilities := asset.CapabilitySet{}
				if item.Actionable != nil && *item.Actionable {
					capabilities = append(capabilities, asset.CapabilityActionable)
				}
				result = append(result, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: item.NativeType, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: capabilities, Location: item.Location})
			}
			if page.Complete {
				complete = true
				break
			}
			request.Cursor = page.NextCursor
		}
		if !complete {
			t.Fatal("inventory pagination failed to finish")
		}
	}
	return result
}

func dataformAsset(assets []asset.Asset, name string) asset.Asset {
	for _, value := range assets {
		if value.Identity.NativeID == "//dataform.googleapis.com/"+name {
			return value
		}
	}
	panic("missing Dataform fixture asset: " + name)
}

func dataformPlan(t *testing.T, r *Runtime, assets []asset.Asset) (plan.Result, plan.Input) {
	t.Helper()
	contributor, err := r.ServiceLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := contributor.Contribute(context.Background(), "scope", assets)
	if err != nil || len(contribution.Unresolved) != 0 {
		t.Fatalf("contribution=%+v error=%v", contribution, err)
	}
	input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{dataformAsset(assets, dataformRoot).ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships}
	// WorkflowConfig references a live release configuration; immutable source
	// fields in compilations/invocations describe history, not deletion barriers.
	workflow, release := dataformAsset(assets, dataformRoot+"/workflowConfigs/transform"), dataformAsset(assets, dataformRoot+"/releaseConfigs/hourly")
	refs := array(workflow.Normalized[referenceKey(release.Identity.NativeType)])
	if len(refs) != 1 || text(refs[0]) != release.Identity.NativeID {
		t.Fatal("missing native release configuration reference")
	}
	input.Relationships = append(input.Relationships, graph.Relationship{SourceAssetID: workflow.ID, TargetAssetID: release.ID, Type: graph.RelationshipDependsOn, Confidence: 1})
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) > 0 {
		t.Fatalf("plan=%+v error=%v", result, err)
	}
	return result, input
}

func dataformRequest(t *testing.T, result plan.Result, assets []asset.Asset, value asset.Asset) contracts.ActionRequest {
	t.Helper()
	request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "dataform-" + string(value.ID)}
	var stepID plan.StepID
	for _, step := range result.Steps {
		if step.AssetID == value.ID {
			stepID = step.ID
			request.Parameters = step.RequestOptions
		}
	}
	if stepID == "" {
		t.Fatal("missing native delete step")
	}
	for _, step := range result.Steps {
		if step.Evidence["lifecycle_controller"] == string(value.ID) {
			for _, child := range assets {
				if child.ID == step.AssetID {
					request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, contracts.ActionImpact{Asset: child, ControllerID: value.ID, Delete: true})
				}
			}
		}
	}
	for _, impact := range result.ImpactItems {
		if impact.DelegatedTo != stepID {
			continue
		}
		for _, child := range assets {
			if child.ID == impact.AssetID {
				request.LifecycleImpacts = append(request.LifecycleImpacts, contracts.ActionImpact{Asset: child, ControllerID: impact.ControllerID, Delete: impact.Expected == plan.ExpectedDelegatedDelete})
			}
		}
	}
	return request
}

func roundTripDataformJSON[T any](t *testing.T, value T) T {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var result T
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func TestDataformNativeInventoryPlanCancellationAndRestart(t *testing.T) {
	s := newDataformScenario(t)
	s.emptyPage = true
	r := protocolRuntime(t, s.transport(t))
	assets := s.inventory(t, r, "project")
	if len(assets) != 8 {
		t.Fatalf("lost region or children: %d", len(assets))
	}
	root := dataformAsset(assets, dataformRoot)
	if text(root.Normalized[dataformProof]) == "" {
		t.Fatal("missing native configuration proof")
	}
	refs := array(root.Normalized[referenceKey("secretmanager.googleapis.com/Secret")])
	if len(refs) != 2 {
		t.Fatalf("secret references lost before redaction: %+v", refs)
	}
	result, input := dataformPlan(t, r, assets)
	if len(result.Steps) != 5 || len(result.ImpactItems) != 1 {
		t.Fatalf("wrong native cleanup model: %+v", result)
	}
	request := dataformRequest(t, result, assets, root)
	if len(request.PrerequisiteDeletions) != 4 || len(request.LifecycleImpacts) != 1 {
		t.Fatalf("missing frozen child contracts: %+v", request)
	}
	for _, mode := range []string{"workspace-only", "root-and-workspace", "retain-workspace", "retain-compilation"} {
		t.Run(mode, func(t *testing.T) {
			input := input
			workspace := dataformAsset(assets, dataformRoot+"/workspaces/development")
			switch mode {
			case "workspace-only":
				input.ResolvedAssetIDs = []asset.AssetID{workspace.ID}
			case "root-and-workspace":
				input.ResolvedAssetIDs = append(slices.Clone(input.ResolvedAssetIDs), workspace.ID)
			case "retain-workspace":
				input.RequestOptions = map[asset.AssetID]map[string]any{root.ID: {"retain_resources": []string{string(workspace.ID)}}}
			case "retain-compilation":
				input.RequestOptions = map[asset.AssetID]map[string]any{root.ID: {"retain_resources": []string{string(request.LifecycleImpacts[0].Asset.ID)}}}
			}
			p, err := plan.Solve(input)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(mode, "retain") {
				if len(p.Blockers) == 0 {
					t.Fatal("unsupported retention accepted")
				}
				return
			}
			want := 5
			if mode == "workspace-only" {
				want = 1
			}
			if len(p.Blockers) != 0 || len(p.Steps) != want {
				t.Fatalf("wrong selection behavior: %+v", p)
			}
		})
	}
	driver, err := r.ResolveAction(context.Background(), "connection", root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(context.Background(), request); err == nil || len(s.mutations) != 0 {
		t.Fatal("parent authorized before children")
	}
	for _, name := range []string{dataformRoot + "/workflowConfigs/transform", dataformRoot + "/releaseConfigs/hourly", dataformRoot + "/workspaces/development", dataformRoot + "/workflowInvocations/running", dataformRoot} {
		value := dataformAsset(assets, name)
		req := roundTripDataformJSON(t, dataformRequest(t, result, assets, value))
		driver, err := protocolRuntime(t, s.transport(t)).ResolveAction(context.Background(), "connection", value)
		if err != nil {
			t.Fatal(err)
		}
		result, err := driver.Execute(context.Background(), req)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if result.ProviderRequestID != "request-123" {
			t.Fatal("lost native request provenance")
		}
		result = roundTripDataformJSON(t, result)
		if strings.Contains(name, "/workflowInvocations/") {
			if result.Data["phase"] != "dataform_cancel" || result.ProviderOperationID != "" {
				t.Fatalf("wrong cancellation phase: %+v", result)
			}
			before := len(s.mutations)
			for _, state := range []string{"RUNNING", "CANCELING"} {
				s.resources[name]["state"] = state
				driver, _ = protocolRuntime(t, s.transport(t)).ResolveAction(context.Background(), "connection", value)
				wait, err := driver.Wait(context.Background(), req, result)
				if err != nil || wait.Done || len(s.mutations) != before {
					t.Fatalf("cancel resent or finished early: %+v %v", wait, err)
				}
			}
			s.resources[name]["state"] = "CANCELLED"
			object(s.resources[name]["invocationTiming"])["endTime"] = "2025-01-04T00:10:00Z"
			wait, err := driver.Wait(context.Background(), req, result)
			if err != nil || wait.Done || wait.Data["phase"] != "dataform_delete" {
				t.Fatalf("missing terminal delete: %+v %v", wait, err)
			}
			result.Data = roundTripDataformJSON(t, wait.Data)
		}
		wait, err := driver.Wait(context.Background(), req, result)
		if err != nil || wait.Done {
			t.Fatalf("HTTP success was mistaken for absence: %+v %v", wait, err)
		}
		delete(s.resources, name)
		if name == dataformRoot {
			wait, err = driver.Wait(context.Background(), req, result)
			if err != nil || wait.Done {
				t.Fatalf("unconfirmed compiled artifact: %+v %v", wait, err)
			}
			delete(s.resources, dataformRoot+"/compilationResults/compiled")
		}
		driver, _ = protocolRuntime(t, s.transport(t)).ResolveAction(context.Background(), "connection", value)
		wait, err = driver.Wait(context.Background(), req, result)
		if err != nil || !wait.Done {
			t.Fatalf("final native absence not recognized: %+v %v", wait, err)
		}
	}
	if len(s.mutations) != 6 || len(s.resources) != 2 {
		t.Fatalf("unexpected native effects: %+v remaining=%d", s.mutations, len(s.resources))
	}
	if s.resources["projects/sample-project/locations/europe-west1/repositories/other"] == nil {
		t.Fatal("sibling repository deleted")
	}
}

func TestDataformCompiledArtifactHasNoInventedDelete(t *testing.T) {
	s := newDataformScenario(t)
	r := protocolRuntime(t, s.transport(t))
	assets := s.inventory(t, r, "us-central1")
	value := dataformAsset(assets, dataformRoot+"/compilationResults/compiled")
	if slices.Contains(value.Capabilities, asset.CapabilityActionable) {
		t.Fatal("compiled artifact falsely actionable")
	}
	if _, err := r.ResolveAction(context.Background(), "connection", value); err == nil {
		t.Fatal("invented independent compiler delete")
	}
	if len(assets) != 6 {
		t.Fatalf("region filtering failed: %d", len(assets))
	}
}

func dataformReviewed(t *testing.T) (*dataformScenario, *Runtime, []asset.Asset, contracts.ActionRequest) {
	t.Helper()
	s := newDataformScenario(t)
	r := protocolRuntime(t, s.transport(t))
	assets := s.inventory(t, r, "us-central1")
	result, _ := dataformPlan(t, r, assets)
	return s, r, assets, dataformRequest(t, result, assets, dataformAsset(assets, dataformRoot))
}

func dataformResponse(req *http.Request, status int, data any) *http.Response {
	raw, _ := json.Marshal(data)
	return apiResponse(req, status, string(raw))
}

func TestDataformContributionRejectsIncompleteOrChangedNativeMembership(t *testing.T) {
	for _, mode := range []string{"missing-inventory", "foreign-connection", "foreign-partition", "duplicate-inventory", "stale-child", "stale-parent", "read-403", "read-404", "read-206", "read-error", "wrong-read-name", "list-403", "list-404", "list-206", "list-error", "list-unreachable", "list-malformed", "token-type", "token-loop", "duplicate-native", "foreign-project", "foreign-parent", "foreign-kind", "changed-final-list", "changed-child-detail", "parent-recreated-during-list"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, _ := dataformReviewed(t)
			name := dataformRoot + "/workspaces/development"
			for i := range assets {
				if assets[i].Identity.NativeID == "//dataform.googleapis.com/"+name {
					switch mode {
					case "missing-inventory":
						assets = append(assets[:i], assets[i+1:]...)
					case "foreign-connection":
						assets[i].Identity.ConnectionID = "other"
					case "foreign-partition":
						assets[i].Identity.Partition = "other"
					case "duplicate-inventory":
						assets = append(assets, assets[i])
					case "stale-child":
						s.resources[name]["createTime"] = "2026-01-01T00:00:00Z"
					}
					break
				}
			}
			if mode == "stale-parent" {
				s.resources[dataformRoot]["displayName"] = "Unreviewed rename"
			}
			lists := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "GET" {
					t.Fatal("discovery mutated cloud state")
				}
				if req.URL.Path == "/v1/"+name {
					switch mode {
					case "read-403":
						return dataformResponse(req, 403, map[string]any{}), true
					case "read-404":
						return dataformResponse(req, 404, map[string]any{}), true
					case "read-206":
						return dataformResponse(req, 206, s.resources[name]), true
					case "read-error":
						return dataformResponse(req, 200, map[string]any{"error": map[string]any{"code": 403, "status": "PERMISSION_DENIED"}}), true
					case "wrong-read-name":
						raw := roundTripDataformJSON(t, s.resources[name])
						raw["name"] = name + "-other"
						return dataformResponse(req, 200, raw), true
					case "changed-child-detail":
						raw := roundTripDataformJSON(t, s.resources[name])
						raw["disableMoves"] = true
						return dataformResponse(req, 200, raw), true
					}
				}
				if req.URL.Path != "/v1/"+dataformRoot+"/workspaces" {
					return nil, false
				}
				lists++
				switch mode {
				case "list-403":
					return dataformResponse(req, 403, map[string]any{}), true
				case "list-404":
					return dataformResponse(req, 404, map[string]any{}), true
				case "list-206":
					return dataformResponse(req, 206, map[string]any{"workspaces": []any{}}), true
				case "list-error":
					return dataformResponse(req, 200, map[string]any{"error": map[string]any{"code": 403}}), true
				case "list-unreachable":
					return dataformResponse(req, 200, map[string]any{"unreachable": []string{"us-central1"}}), true
				case "list-malformed":
					return dataformResponse(req, 200, map[string]any{"workspaces": "invalid"}), true
				case "token-type":
					return dataformResponse(req, 200, map[string]any{"nextPageToken": 3}), true
				case "token-loop":
					return dataformResponse(req, 200, map[string]any{"nextPageToken": "stuck"}), true
				case "duplicate-native":
					return dataformResponse(req, 200, map[string]any{"workspaces": []any{s.resources[name], s.resources[name]}}), true
				case "foreign-project", "foreign-parent", "foreign-kind":
					raw := roundTripDataformJSON(t, s.resources[name])
					foreign := strings.Replace(name, "sample-project", "foreign-project", 1)
					if mode == "foreign-parent" {
						foreign = strings.Replace(name, "warehouse", "sibling", 1)
					}
					if mode == "foreign-kind" {
						foreign = strings.Replace(name, "workspaces", "releaseConfigs", 1)
					}
					raw["name"] = foreign
					return dataformResponse(req, 200, map[string]any{"workspaces": []any{raw}}), true
				case "changed-final-list":
					if lists > 1 {
						return dataformResponse(req, 200, map[string]any{"workspaces": []any{}}), true
					}
				case "parent-recreated-during-list":
					s.resources[dataformRoot]["createTime"] = "2026-01-01T00:00:00Z"
				}
				return nil, false
			}
			contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
			result, err := contributor.Contribute(context.Background(), "scope", assets)
			if slices.Contains([]string{"missing-inventory", "foreign-connection", "foreign-partition"}, mode) {
				if err != nil || len(result.Unresolved) != 1 || result.Unresolved[0].NativeID != "//dataform.googleapis.com/"+name {
					t.Fatalf("missing member was hidden: %+v %v", result, err)
				}
			} else if err == nil {
				t.Fatalf("invalid native discovery accepted: %+v", result)
			}
			if len(s.mutations) != 0 {
				t.Fatal("native discovery caused mutation")
			}
		})
	}
}

func TestDataformParentDeleteBoundaryAndAbsenceProof(t *testing.T) {
	for _, mode := range []string{"new-workspace", "new-invocation", "new-compilation", "unreviewed-compilation", "retained-compilation", "stale-compilation", "changed-parent", "recreated-parent", "protected-parent", "missing-parent-proof", "foreign-prerequisite", "foreign-partition", "foreign-provider", "foreign-kind", "foreign-parent", "wrong-controller", "duplicate-prerequisite", "retained-prerequisite", "prerequisite-reappears", "prerequisite-read-403", "prerequisite-read-206", "impact-read-403", "parent-404-prerequisite-alive", "parent-404-impact-alive", "parent-404-impact-403"} {
		t.Run(mode, func(t *testing.T) {
			s, r, _, request := dataformReviewed(t)
			for _, p := range request.PrerequisiteDeletions {
				delete(s.resources, strings.TrimPrefix(p.Asset.Identity.NativeID, "//dataform.googleapis.com/"))
			}
			workspace := dataformRoot + "/workspaces/development"
			compiled := dataformRoot + "/compilationResults/compiled"
			switch mode {
			case "new-workspace", "new-invocation", "new-compilation":
				collection := "workspaces"
				if mode == "new-invocation" {
					collection = "workflowInvocations"
				}
				if mode == "new-compilation" {
					collection = "compilationResults"
				}
				name := dataformRoot + "/" + collection + "/new"
				s.resources[name] = map[string]any{"name": name, "createTime": "2026-01-01T00:00:00Z"}
				s.members[dataformRoot+"/"+collection] = append(s.members[dataformRoot+"/"+collection], name)
			case "unreviewed-compilation":
				request.LifecycleImpacts = nil
			case "retained-compilation":
				request.LifecycleImpacts[0].Delete = false
			case "stale-compilation":
				s.resources[compiled]["createTime"] = "2026-01-01T00:00:00Z"
			case "changed-parent":
				s.resources[dataformRoot]["displayName"] = "Changed"
			case "recreated-parent":
				s.resources[dataformRoot]["createTime"] = "2026-01-01T00:00:00Z"
			case "protected-parent":
				object(s.resources[dataformRoot]["labels"])["steward:protect"] = "true"
			case "missing-parent-proof":
				delete(request.Asset.Normalized, dataformProof)
			case "foreign-prerequisite":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "other"
			case "foreign-partition":
				request.PrerequisiteDeletions[0].Asset.Identity.Partition = "other"
			case "foreign-provider":
				request.PrerequisiteDeletions[0].Asset.Identity.Provider = asset.ProviderAzure
			case "foreign-kind":
				request.PrerequisiteDeletions[0].Asset.Identity.NativeType = "dataform.googleapis.com/CompilationResult"
			case "foreign-parent":
				request.PrerequisiteDeletions[0].Asset.Identity.NativeID = strings.Replace(request.PrerequisiteDeletions[0].Asset.Identity.NativeID, "warehouse", "sibling", 1)
			case "wrong-controller":
				request.PrerequisiteDeletions[0].ControllerID = "other"
			case "duplicate-prerequisite":
				request.PrerequisiteDeletions = append(request.PrerequisiteDeletions, request.PrerequisiteDeletions[0])
			case "retained-prerequisite":
				request.PrerequisiteDeletions[0].Delete = false
			case "prerequisite-reappears", "parent-404-prerequisite-alive":
				s.resources[workspace] = map[string]any{"name": workspace, "createTime": "2026-01-01T00:00:00Z"}
			}
			if strings.HasPrefix(mode, "parent-404") {
				delete(s.resources, dataformRoot)
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "GET" && req.URL.Path == "/v1/"+workspace && strings.HasPrefix(mode, "prerequisite-read") {
					status := 403
					if mode == "prerequisite-read-206" {
						status = 206
					}
					return dataformResponse(req, status, map[string]any{}), true
				}
				if req.Method == "GET" && req.URL.Path == "/v1/"+compiled && (mode == "impact-read-403" || mode == "parent-404-impact-403") {
					return dataformResponse(req, 403, map[string]any{}), true
				}
				return nil, false
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
			if mode == "parent-404-impact-alive" {
				check, err := driver.Preflight(context.Background(), request)
				if err != nil || check.Absent {
					t.Fatalf("child falsely absent: %+v %v", check, err)
				}
				_, err = driver.Execute(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
			} else {
				_, err := driver.Execute(context.Background(), request)
				if err == nil {
					t.Fatal("invalid native mutation authorized")
				}
				var call *contracts.ProviderCallError
				if errors.As(err, &call) && call.Provider.Category == execution.ErrorNotFound {
					t.Fatal("dependency failure falsely completes parent")
				}
			}
			if len(s.mutations) != 0 {
				t.Fatalf("unsafe native mutation: %+v", s.mutations)
			}
		})
	}
}

func TestDataformNativeMutationFailuresNeverComplete(t *testing.T) {
	for _, target := range []string{"repository", "cancel", "invocation-delete"} {
		for _, status := range []int{403, 409, 429, 500} {
			t.Run(fmt.Sprintf("%s/%d", target, status), func(t *testing.T) {
				s, r, assets, request := dataformReviewed(t)
				if target == "repository" {
					for _, p := range request.PrerequisiteDeletions {
						delete(s.resources, strings.TrimPrefix(p.Asset.Identity.NativeID, "//dataform.googleapis.com/"))
					}
				} else {
					request = contracts.ActionRequest{Asset: dataformAsset(assets, dataformRoot+"/workflowInvocations/running"), Action: "delete"}
					if target == "invocation-delete" {
						s.resources[dataformRoot+"/workflowInvocations/running"]["state"] = "SUCCEEDED"
					}
				}
				mutations := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.Method == "GET" {
						return nil, false
					}
					mutations++
					return dataformResponse(req, status, map[string]any{"error": map[string]any{"code": status, "message": "PRIVATE_MESSAGE"}}), true
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
				_, err := driver.Execute(context.Background(), request)
				var call *contracts.ProviderCallError
				if err == nil || !errors.As(err, &call) || call.Provider.RequestID != "request-123" || mutations != 1 || strings.Contains(err.Error(), "PRIVATE_MESSAGE") {
					t.Fatalf("lost native failure/provenance: %v mutations=%d", err, mutations)
				}
			})
		}
	}
}

func TestDataformCancellationResumeRejectsDriftAndForgedState(t *testing.T) {
	for _, mode := range []string{"name", "configuration", "start-time", "parent", "protected-parent", "unknown-state", "missing-proof", "foreign-phase", "foreign-target", "foreign-proof", "foreign-operation", "read-403", "read-206", "read-error", "parent-404", "native-absent"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets, _ := dataformReviewed(t)
			name := dataformRoot + "/workflowInvocations/running"
			request := contracts.ActionRequest{Asset: dataformAsset(assets, name), Action: "delete"}
			driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			result = roundTripDataformJSON(t, result)
			request = roundTripDataformJSON(t, request)
			s.resources[name]["state"] = "CANCELLED"
			switch mode {
			case "name":
				s.resources[name]["name"] = name + "-other"
			case "configuration":
				s.resources[name]["workflowConfig"] = dataformRoot + "/workflowConfigs/new"
			case "start-time":
				object(s.resources[name]["invocationTiming"])["startTime"] = "2026-01-01T00:00:00Z"
			case "parent":
				s.resources[dataformRoot]["createTime"] = "2026-01-01T00:00:00Z"
			case "protected-parent":
				object(s.resources[dataformRoot]["labels"])["steward_protect"] = "true"
			case "unknown-state":
				s.resources[name]["state"] = "STATE_UNSPECIFIED"
			case "missing-proof":
				delete(request.Asset.Normalized, dataformProof)
			case "foreign-phase":
				result.Data["phase"] = "delete"
			case "foreign-target":
				result.Data["resource"] = "//dataform.googleapis.com/" + name + "-other"
			case "foreign-proof":
				result.Data["configuration"] = "forged"
			case "foreign-operation":
				result.ProviderOperationID = "https://dataform.googleapis.com/v1/projects/other/operations/delete"
			case "parent-404":
				delete(s.resources, dataformRoot)
			case "native-absent":
				delete(s.resources, name)
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "GET" && req.URL.Path == "/v1/"+name {
					switch mode {
					case "read-403":
						return dataformResponse(req, 403, map[string]any{}), true
					case "read-206":
						return dataformResponse(req, 206, s.resources[name]), true
					case "read-error":
						return dataformResponse(req, 200, map[string]any{"error": map[string]any{"code": 404}}), true
					}
				}
				return nil, false
			}
			driver, _ = protocolRuntime(t, s.transport(t)).ResolveAction(context.Background(), "connection", request.Asset)
			wait, err := driver.Wait(context.Background(), request, result)
			if mode == "native-absent" {
				if err != nil || !wait.Done {
					t.Fatalf("native absence lost: %+v %v", wait, err)
				}
			} else if err == nil || wait.Done {
				t.Fatalf("unsafe restart accepted: %+v %v", wait, err)
			}
			if len(s.mutations) != 1 {
				t.Fatalf("recovery issued unsafe mutation: %+v", s.mutations)
			}
		})
	}
}

func TestDataformInventoryFailuresDoNotEstablishAbsence(t *testing.T) {
	for _, mode := range []string{"locations-403", "locations-206", "locations-loop", "locations-foreign", "list-403", "list-206", "list-malformed", "detail-404", "detail-206", "detail-foreign", "detail-drift", "parent-drift", "parent-404", "cursor-parent-drift", "cursor-connection", "cursor-scope", "cursor-kind"} {
		t.Run(mode, func(t *testing.T) {
			s := newDataformScenario(t)
			r := protocolRuntime(t, s.transport(t))
			request := productRequest(r, "dataform.googleapis.com/Workspace", "project")
			name := dataformRoot + "/workspaces/development"
			if strings.HasPrefix(mode, "cursor-") {
				s.emptyPage = true
				page, err := r.List(context.Background(), request)
				if err != nil || page.Complete || page.NextCursor == "" {
					t.Fatalf("missing first cursor: %+v %v", page, err)
				}
				request.Cursor = page.NextCursor
				switch mode {
				case "cursor-parent-drift":
					s.resources[dataformRoot]["createTime"] = "2026-01-01T00:00:00Z"
				case "cursor-connection":
					request.ConnectionID = "changed" // Fingerprint uses the public request; the client remains bound below.
				case "cursor-scope":
					request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-central1"}
				case "cursor-kind":
					kind := r.resourceKind("dataform.googleapis.com/CompilationResult")
					request.ResourceKind = &kind
				}
			}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Path == "/v1/projects/sample-project/locations" {
					switch mode {
					case "locations-403":
						return dataformResponse(req, 403, map[string]any{}), true
					case "locations-206":
						return dataformResponse(req, 206, map[string]any{}), true
					case "locations-loop":
						return dataformResponse(req, 200, map[string]any{"nextPageToken": "repeat"}), true
					case "locations-foreign":
						return dataformResponse(req, 200, map[string]any{"locations": []any{map[string]any{"name": "projects/foreign-project/locations/us-central1", "locationId": "us-central1"}}}), true
					}
				}
				if req.URL.Path == "/v1/"+dataformRoot+"/workspaces" {
					switch mode {
					case "list-403":
						return dataformResponse(req, 403, map[string]any{}), true
					case "list-206":
						return dataformResponse(req, 206, map[string]any{"workspaces": []any{}}), true
					case "list-malformed":
						return dataformResponse(req, 200, map[string]any{"workspaces": map[string]any{}}), true
					case "parent-drift":
						s.resources[dataformRoot]["createTime"] = "2026-01-01T00:00:00Z"
					case "parent-404":
						delete(s.resources, dataformRoot)
					}
				}
				if req.URL.Path == "/v1/"+name {
					switch mode {
					case "detail-404":
						return dataformResponse(req, 404, map[string]any{}), true
					case "detail-206":
						return dataformResponse(req, 206, s.resources[name]), true
					case "detail-foreign", "detail-drift":
						raw := roundTripDataformJSON(t, s.resources[name])
						if mode == "detail-foreign" {
							raw["name"] = name + "-other"
						} else {
							raw["createTime"] = "2026-01-01T00:00:00Z"
						}
						return dataformResponse(req, 200, raw), true
					}
				}
				return nil, false
			}
			var batch contracts.InventoryBatch
			var err error
			// Exercise the real cursor boundary without asking the fixture credential
			// resolver to authorize a deliberately foreign connection.
			if mode == "cursor-connection" {
				c, _ := r.resolve(context.Background(), "connection")
				batch, err = r.listProduct(context.Background(), c, request, nil)
			} else {
				for i := 0; i < 10; i++ {
					batch, err = r.List(context.Background(), request)
					if err != nil || batch.Complete {
						break
					}
					request.Cursor = batch.NextCursor
				}
			}
			if err == nil || batch.Complete {
				t.Fatalf("failed shard established absence: %+v %v", batch, err)
			}
			if len(s.mutations) > 0 {
				t.Fatal("scan mutated resources")
			}
		})
	}
}

func TestDataformMembershipRechecksEveryNativePage(t *testing.T) {
	s := newDataformScenario(t)
	first := dataformRoot + "/compilationResults/compiled"
	second := dataformRoot + "/compilationResults/second"
	s.resources[second] = roundTripDataformJSON(t, s.resources[first])
	s.resources[second]["name"] = second
	s.members[dataformRoot+"/compilationResults"] = append(s.members[dataformRoot+"/compilationResults"], second)
	pages := map[string]int{}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method != "GET" || req.URL.Path != "/v1/"+dataformRoot+"/compilationResults" {
			return nil, false
		}
		token := req.URL.Query().Get("pageToken")
		pages[token]++
		if token == "" {
			return dataformResponse(req, 200, map[string]any{"compilationResults": []any{s.resources[first]}, "nextPageToken": "two"}), true
		}
		if token != "two" {
			t.Fatal("unexpected token")
		}
		return dataformResponse(req, 200, map[string]any{"compilationResults": []any{s.resources[second]}}), true
	}
	r := protocolRuntime(t, s.transport(t))
	assets := s.inventory(t, r, "us-central1")
	pages = map[string]int{}
	result, _ := dataformPlan(t, r, assets)
	if len(result.ImpactItems) != 2 || pages[""] != 2 || pages["two"] != 2 {
		t.Fatalf("second membership read skipped an earlier page: %+v pages=%+v", result, pages)
	}
}

func TestDataformNewInvocationDuringCompilationReadsBlocksForce(t *testing.T) {
	s, r, _, request := dataformReviewed(t)
	for _, child := range request.PrerequisiteDeletions {
		delete(s.resources, strings.TrimPrefix(child.Asset.Identity.NativeID, "//dataform.googleapis.com/"))
	}
	added := false
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if !added && req.Method == "GET" && req.URL.Path == "/v1/"+dataformRoot+"/compilationResults/compiled" {
			name := dataformRoot + "/workflowInvocations/new"
			s.resources[name] = map[string]any{"name": name, "state": "RUNNING", "invocationTiming": map[string]any{"startTime": "2026-01-01T00:00:00Z"}}
			s.members[dataformRoot+"/workflowInvocations"] = append(s.members[dataformRoot+"/workflowInvocations"], name)
			added = true
		}
		return nil, false
	}
	driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
	if _, err := driver.Execute(context.Background(), request); err == nil || !added || len(s.mutations) > 0 {
		t.Fatalf("force deleted unreviewed running work: %v mutations=%+v", err, s.mutations)
	}
}

func TestDataformInventoryAcceptsCanonicalProjectNumberNames(t *testing.T) {
	s := newDataformScenario(t)
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "GET" && req.URL.Path == "/v1/"+dataformRoot {
			raw := roundTripDataformJSON(t, s.resources[dataformRoot])
			raw["name"] = strings.Replace(dataformRoot, "sample-project", "123456", 1)
			return dataformResponse(req, 200, raw), true
		}
		return nil, false
	}
	r := protocolRuntime(t, s.transport(t))
	assets := s.inventory(t, r, "us-central1")
	root := dataformAsset(assets, dataformRoot)
	if root.Identity.NativeID != "//dataform.googleapis.com/"+dataformRoot {
		t.Fatal("project number was not canonicalized")
	}
	if _, err := r.ServiceLifecycle(context.Background(), "connection"); err != nil {
		t.Fatal(err)
	}
	dataformPlan(t, r, assets)
}

func TestDataformTerminalWorkflowStatesAndNativeStatus(t *testing.T) {
	for _, state := range []string{"SUCCEEDED", "FAILED", "CANCELLED", "CANCELING"} {
		t.Run(state, func(t *testing.T) {
			s, r, assets, _ := dataformReviewed(t)
			name := dataformRoot + "/workflowInvocations/running"
			s.resources[name]["state"] = state
			request := contracts.ActionRequest{Asset: dataformAsset(assets, name), Action: "delete"}
			driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if state == "CANCELING" {
				if len(s.mutations) != 0 || result.Data["phase"] != "dataform_cancel" {
					t.Fatalf("cancellation unnecessarily resent: %+v", result)
				}
			} else if len(s.mutations) != 1 || s.mutations[0] != "DELETE "+name {
				t.Fatalf("terminal invocation incorrectly cancelled: %+v", s.mutations)
			}
		})
	}
	for _, status := range []int{201, 202, 204, 206} {
		t.Run(fmt.Sprintf("unsupported-native-status/%d", status), func(t *testing.T) {
			s, r, assets, _ := dataformReviewed(t)
			request := contracts.ActionRequest{Asset: dataformAsset(assets, dataformRoot+"/workflowInvocations/running"), Action: "delete"}
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method != "GET" {
					return dataformResponse(req, status, map[string]any{}), true
				}
				return nil, false
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", request.Asset)
			if _, err := driver.Execute(context.Background(), request); err == nil {
				t.Fatal("incomplete cancellation acknowledgment accepted")
			}
		})
	}
}
