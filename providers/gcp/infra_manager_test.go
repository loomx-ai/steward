package gcp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	infraTestParent     = "projects/sample-project/locations/us-central1"
	infraTestDeployment = infraTestParent + "/deployments/stack-a"
	infraTestRevision   = infraTestDeployment + "/revisions/r-1"
	infraTestPreview    = infraTestParent + "/previews/destroy-a"
	infraTestNetwork    = "//compute.googleapis.com/projects/sample-project/global/networks/owned-network"
	infraTestBucket     = "//storage.googleapis.com/infra-owned-data"
)

type infraScenario struct {
	resources, physical, operations map[string]map[string]any
	calls, writes                   []string
	emptyPage                       bool
	policies                        map[string]string
	handle                          func(*http.Request) (*http.Response, bool)
}

func newInfraScenario(t *testing.T) *infraScenario {
	t.Helper()
	s := &infraScenario{operations: map[string]map[string]any{}, policies: map[string]string{}}
	for file, target := range map[string]*map[string]map[string]any{"resources": &s.resources, "physical": &s.physical} {
		raw, err := os.ReadFile("fixtures/infra-manager/" + file + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, target); err != nil {
			t.Fatal(err)
		}
	}
	return s
}

// URLs, response fields and destructive semantics below are literal native
// contracts. This scenario does not derive expectations from Steward's catalog.
func (s *infraScenario) transport(t *testing.T) roundTripFunc {
	return func(req *http.Request) (*http.Response, error) {
		s.calls = append(s.calls, req.Method+" "+req.URL.String())
		if s.handle != nil {
			if response, ok := s.handle(req); ok {
				return response, nil
			}
		}
		respond := func(code int, data any) (*http.Response, error) { return dataformResponse(req, code, data), nil }
		if req.URL.Host == "compute.googleapis.com" {
			if req.Method != "GET" {
				t.Fatalf("Terraform cleanup sent a direct Compute mutation: %s", req.URL)
			}
			prefix := "/compute/v1/projects/sample-project/global/networks"
			if req.URL.Path == prefix {
				values := []any{}
				for id, row := range s.physical {
					if strings.HasPrefix(id, "//compute.googleapis.com/") {
						values = append(values, row)
					}
				}
				return respond(200, map[string]any{"items": values})
			}
			id := "//compute.googleapis.com/" + strings.TrimPrefix(req.URL.Path, "/compute/v1/")
			if row := s.physical[id]; row != nil {
				return respond(200, row)
			}
			if strings.HasPrefix(req.URL.Path, prefix+"/") {
				return respond(404, map[string]any{})
			}
			t.Fatalf("unexpected Compute dependency request: %s", req.URL)
		}
		if req.URL.Host == "storage.googleapis.com" {
			if req.Method != "GET" {
				t.Fatalf("Terraform cleanup sent a direct Storage mutation: %s", req.URL)
			}
			if req.URL.Path == "/storage/v1/b" {
				values := []any{}
				for id, row := range s.physical {
					if strings.HasPrefix(id, "//storage.googleapis.com/") {
						values = append(values, row)
					}
				}
				return respond(200, map[string]any{"items": values})
			}
			name := strings.TrimPrefix(req.URL.Path, "/storage/v1/b/")
			if strings.HasSuffix(name, "/o") {
				if req.URL.RawQuery != "maxResults=1&versions=true" {
					t.Fatalf("bucket preflight did not check all object generations: %s", req.URL)
				}
				return respond(200, map[string]any{})
			}
			if row := s.physical["//storage.googleapis.com/"+name]; row != nil {
				return respond(200, row)
			}
			return respond(404, map[string]any{})
		}
		if req.URL.Host != "config.googleapis.com" || !strings.HasPrefix(req.URL.Path, "/v1/projects/sample-project/") {
			t.Fatalf("unexpected Config origin, version or project: %s", req.URL)
		}
		name := strings.TrimPrefix(req.URL.Path, "/v1/")
		if req.Method == "GET" {
			if strings.Contains(name, "/operations/") {
				if row := s.operations[name]; row != nil {
					return respond(200, row)
				}
				return respond(404, map[string]any{})
			}
			collection := last(name)
			if slices.Contains([]string{"locations", "deployments", "revisions", "resources", "previews", "resourceChanges", "resourceDrifts"}, collection) {
				parent := strings.TrimSuffix(name, "/"+collection)
				if slices.Contains([]string{"revisions", "resources", "resourceChanges", "resourceDrifts"}, collection) && s.resources[parent] == nil {
					return respond(404, map[string]any{})
				}
				if req.URL.Query().Get("filter") != "" || req.URL.Query().Get("orderBy") != "" {
					t.Fatalf("inventory narrowed native membership: %s", req.URL)
				}
				if s.emptyPage && req.URL.Query().Get("pageToken") == "" {
					return respond(200, map[string]any{"nextPageToken": "second"})
				}
				if token := req.URL.Query().Get("pageToken"); token != "" && token != "second" {
					t.Fatalf("invalid pagination: %s", req.URL)
				}
				values := []any{}
				if collection == "locations" {
					values = []any{map[string]any{"name": "projects/sample-project/locations/us-central1", "locationId": "us-central1"}, map[string]any{"name": "projects/sample-project/locations/europe-west1", "locationId": "europe-west1"}}
				} else {
					var keys []string
					for key := range s.resources {
						if strings.HasPrefix(key, name+"/") && !strings.Contains(strings.TrimPrefix(key, name+"/"), "/") {
							keys = append(keys, key)
						}
					}
					sort.Strings(keys)
					for _, key := range keys {
						values = append(values, s.resources[key])
					}
				}
				return respond(200, map[string]any{collection: values})
			}
			if row := s.resources[name]; row != nil {
				return respond(200, row)
			}
			return respond(404, map[string]any{})
		}
		if req.Method != "DELETE" || (name != infraTestDeployment && name != infraTestPreview && name != "projects/sample-project/locations/europe-west1/deployments/stack-a") {
			t.Fatalf("unexpected metadata/physical mutation: %s %s", req.Method, req.URL)
		}
		body, _ := io.ReadAll(req.Body)
		if len(body) != 0 {
			t.Fatal("Config DELETE must have an empty body")
		}
		q := req.URL.Query()
		id := q.Get("requestId")
		if len(id) != 36 || id == "00000000-0000-0000-0000-000000000000" {
			t.Fatalf("native requestId is not a nonzero UUID: %s", req.URL)
		}
		if strings.Contains(name, "/deployments/") {
			if q.Get("force") != "true" || !slices.Contains([]string{"DELETE", "ABANDON"}, q.Get("deletePolicy")) || len(q) != 3 {
				t.Fatalf("wrong native force/policy: %s", req.URL)
			}
		} else if len(q) != 1 {
			t.Fatalf("preview DELETE has no force or policy: %s", req.URL)
		}
		if s.resources[name] == nil {
			return respond(404, map[string]any{})
		}
		s.writes = append(s.writes, req.URL.String())
		op := strings.Join(strings.Split(name, "/")[:4], "/") + "/operations/delete-" + fmt.Sprint(len(s.writes))
		s.operations[op] = map[string]any{"name": op, "done": false, "metadata": map[string]any{"@type": "type.googleapis.com/google.cloud.config.v1.OperationMetadata", "target": name, "verb": "delete", "apiVersion": "v1", "requestedCancellation": false}}
		s.policies[op] = q.Get("deletePolicy")
		s.resources[name]["state"] = "DELETING"
		return respond(200, s.operations[op])
	}
}

func (s *infraScenario) finish(operation string, keepPhysical bool) {
	name := strings.TrimPrefix(operation, "https://config.googleapis.com/v1/")
	op := s.operations[name]
	target := text(object(op["metadata"])["target"])
	kind := "Deployment"
	if strings.Contains(target, "/previews/") {
		kind = "Preview"
	}
	op["done"] = true
	op["response"] = map[string]any{"@type": "type.googleapis.com/google.cloud.config.v1." + kind, "name": target, "state": "DELETED", "createTime": s.resources[target]["createTime"]}
	for id := range s.resources {
		if id == target || strings.HasPrefix(id, target+"/") {
			delete(s.resources, id)
		}
	}
	if target == infraTestDeployment && s.policies[name] == "DELETE" && !keepPhysical {
		delete(s.physical, infraTestNetwork)
		delete(s.physical, infraTestBucket)
	}
}

func (s *infraScenario) inventory(t *testing.T, r *Runtime) []asset.Asset {
	t.Helper()
	var values []asset.Asset
	for _, kind := range []string{infraDeployment, infraRevision, infraResource, infraPreview, infraChange, infraDrift, "compute.googleapis.com/Network", "storage.googleapis.com/Bucket"} {
		request := productRequest(r, kind, "project")
		for attempt := 0; ; attempt++ {
			batch, err := r.List(context.Background(), request)
			if err != nil || attempt > 40 {
				t.Fatalf("Infra Manager inventory %s: %+v %v", kind, batch, err)
			}
			for _, item := range batch.Items {
				capabilities := asset.CapabilitySet{}
				if item.Actionable != nil && *item.Actionable {
					capabilities = append(capabilities, asset.CapabilityActionable)
				}
				values = append(values, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: kind, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: capabilities, Location: item.Location})
			}
			if batch.Complete {
				break
			}
			request.Cursor = batch.NextCursor
		}
	}
	return values
}

func infraReviewed(t *testing.T, s *infraScenario, name string, options map[string]any) (*Runtime, []asset.Asset, plan.Input, contracts.ActionRequest) {
	t.Helper()
	r := protocolRuntime(t, s.transport(t))
	values := s.inventory(t, r)
	hook, err := r.ServiceLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := hook.Contribute(context.Background(), "scope", values)
	if err != nil || len(contribution.Unresolved) > 0 {
		t.Fatalf("Infra Manager ownership: %+v %v", contribution, err)
	}
	root := batchAsset(values, name)
	input := plan.Input{Assets: values, ResolvedAssetIDs: []asset.AssetID{root.ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships, RequestOptions: map[asset.AssetID]map[string]any{root.ID: options}}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) > 0 {
		t.Fatalf("Infra Manager plan: %+v %v", result, err)
	}
	request := dataformRequest(t, result, values, root)
	request.Parameters = options
	request.IdempotencyKey = "infra-manager-cleanup"
	return r, values, input, request
}

func TestInfraManagerNativeInventoryDeleteAndRestart(t *testing.T) {
	for _, test := range []struct {
		name    string
		retain  bool
		impacts int
	}{{infraTestDeployment, false, 7}, {infraTestDeployment, true, 7}, {infraTestPreview, false, 2}} {
		t.Run(fmt.Sprintf("%s/retain=%t", last(test.name), test.retain), func(t *testing.T) {
			s := newInfraScenario(t)
			s.emptyPage = true
			options := map[string]any{"retain_all_resources": test.retain}
			r, values, _, request := infraReviewed(t, s, test.name, options)
			if len(values) != 14 || len(request.LifecycleImpacts) != test.impacts || len(s.writes) != 0 {
				t.Fatalf("inventory/plan: %d assets, %d impacts, %v writes", len(values), len(request.LifecycleImpacts), s.writes)
			}
			encoded, _ := json.Marshal(values)
			if strings.Contains(string(encoded), "INFRA_PRIVATE") {
				t.Fatalf("native secret escaped inventory: %s", encoded)
			}
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || result.ProviderOperationID == "" || result.ProviderRequestID == "" || len(s.writes) != 1 {
				t.Fatalf("native delete: %+v %v", result, err)
			}
			encoded, _ = json.Marshal(result)
			var resumed contracts.ActionResult
			if err := json.Unmarshal(encoded, &resumed); err != nil {
				t.Fatal(err)
			}
			restarted := protocolRuntime(t, s.transport(t))
			driver, err = restarted.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if wait, err := driver.Wait(context.Background(), request, resumed); err != nil || wait.Done {
				t.Fatalf("pending deletion: %+v %v", wait, err)
			}
			s.finish(result.ProviderOperationID, false)
			if wait, err := driver.Wait(context.Background(), request, resumed); err != nil || !wait.Done {
				t.Fatalf("completed deletion: %+v %v", wait, err)
			}
			if read, err := driver.Readback(context.Background(), request); err != nil || read.Exists {
				t.Fatalf("final resource proof: %+v %v", read, err)
			}
			if len(s.writes) != 1 || s.physical["//storage.googleapis.com/infra-source"] == nil || s.physical["//compute.googleapis.com/projects/sample-project/global/networks/old-network"] == nil {
				t.Fatal("cleanup touched source artifacts, historical resources or repeated a delete")
			}
			if (s.physical[infraTestNetwork] != nil) != (test.retain || test.name == infraTestPreview) {
				t.Fatal("native physical policy mismatch")
			}
		})
	}
}

func TestInfraManagerManagedIdentityAndRetentionBoundaries(t *testing.T) {
	s := newInfraScenario(t)
	r, values, input, request := infraReviewed(t, s, infraTestDeployment, nil)
	for _, value := range values {
		if isInfra(value.Identity.NativeType) && value.Identity.NativeType != infraDeployment && value.Identity.NativeType != infraPreview {
			if value.Capabilities.Has(asset.CapabilityActionable) {
				t.Fatal("metadata record exposed an invented DELETE")
			}
			if _, err := r.ResolveAction(context.Background(), "connection", value); err == nil {
				t.Fatal("resolved metadata deletion")
			}
		}
	}
	input.RequestOptions = map[asset.AssetID]map[string]any{request.Asset.ID: {"retain_all_resources": true}}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) > 0 {
		t.Fatalf("retain-all rejected non-retainable metadata: %+v %v", result, err)
	}
	for _, impact := range result.ImpactItems {
		metadata := strings.HasPrefix(string(impact.AssetID), "//config.googleapis.com/")
		if metadata != (impact.Expected == plan.ExpectedDelegatedDelete) {
			t.Fatalf("wrong metadata/resource retention: %+v", impact)
		}
	}
	input.RequestOptions[request.Asset.ID] = map[string]any{"retain_resources": []string{"//config.googleapis.com/" + infraTestRevision}}
	result, err = plan.Solve(input)
	if err != nil || len(result.Blockers) == 0 {
		t.Fatal("explicit revision retention accepted")
	}
	input.RequestOptions[request.Asset.ID] = map[string]any{"retain_resources": []string{infraTestBucket}}
	result, err = plan.Solve(input)
	if err != nil {
		t.Fatal(err)
	}
	partial := dataformRequest(t, result, values, request.Asset)
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(context.Background(), partial); err == nil || len(s.writes) != 0 {
		t.Fatal("native all-or-none policy accepted partial retention")
	}
}
