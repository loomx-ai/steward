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
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	fusionTestParent   = "projects/sample-project/locations/us-central1"
	fusionTestInstance = fusionTestParent + "/instances/integration"
	fusionTestDNS      = fusionTestInstance + "/dnsPeerings/private"
	fusionTestNS       = fusionTestInstance + "/namespaces/default"
	fusionTestEurope   = "projects/sample-project/locations/europe-west1/instances/integration"
)

type fusionScenario struct {
	resources        map[string]map[string]any
	operations       map[string]map[string]any
	calls, mutations []string
	emptyPage        bool
	handle           func(*http.Request) (*http.Response, bool)
}

func newFusionScenario(t *testing.T) *fusionScenario {
	t.Helper()
	raw, err := os.ReadFile("fixtures/datafusion/resources.json")
	if err != nil {
		t.Fatal(err)
	}
	s := &fusionScenario{operations: map[string]map[string]any{}}
	if err := json.Unmarshal(raw, &s.resources); err != nil {
		t.Fatal(err)
	}
	return s
}

// Expected native URLs, versions, query parameters, response fields and cascade
// behavior are literal contracts, independent of the production catalog/binder.
func (s *fusionScenario) transport(t *testing.T) roundTripFunc {
	t.Helper()
	return func(req *http.Request) (*http.Response, error) {
		s.calls = append(s.calls, req.Method+" "+req.URL.String())
		if s.handle != nil {
			if response, ok := s.handle(req); ok {
				return response, nil
			}
		}
		respond := func(code int, data any) (*http.Response, error) { return dataformResponse(req, code, data), nil }
		if req.URL.Host != "datafusion.googleapis.com" {
			t.Fatalf("unexpected Data Fusion host, data plane or dependency mutation: %s", req.URL)
		}
		version := "v1"
		if strings.HasSuffix(req.URL.Path, "/namespaces") {
			version = "v1beta1"
			if req.URL.Query().Get("view") != "NAMESPACE_VIEW_FULL" {
				t.Fatalf("namespace list must read the full policy: %s", req.URL)
			}
		}
		if !strings.HasPrefix(req.URL.Path, "/"+version+"/projects/sample-project/") {
			t.Fatalf("wrong native Data Fusion project/version: %s", req.URL)
		}
		name := strings.TrimPrefix(req.URL.Path, "/"+version+"/")
		if req.Method == "GET" {
			if strings.Contains(name, "/operations/") {
				if data := s.operations[name]; data != nil {
					return respond(200, data)
				}
				return respond(404, map[string]any{})
			}
			collection := last(name)
			if slices.Contains([]string{"locations", "instances", "dnsPeerings", "namespaces"}, collection) {
				if collection == "dnsPeerings" || collection == "namespaces" {
					parent := strings.TrimSuffix(name, "/"+collection)
					if s.resources[parent] == nil {
						return respond(404, map[string]any{})
					}
				}
				if s.emptyPage && req.URL.Query().Get("pageToken") == "" {
					return respond(200, map[string]any{"nextPageToken": "second"})
				}
				if token := req.URL.Query().Get("pageToken"); token != "" && token != "second" {
					t.Fatalf("unexpected page token: %s", req.URL)
				}
				var values []any
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
				if values == nil {
					return respond(200, map[string]any{}) // Omitted repeated field is an empty native set.
				}
				return respond(200, map[string]any{collection: values})
			}
			if strings.Contains(name, "/namespaces/") || strings.Contains(name, "/dnsPeerings/") {
				t.Fatalf("invented native child GET: %s", req.URL)
			}
			if data := s.resources[name]; data != nil {
				return respond(200, data)
			}
			return respond(404, map[string]any{})
		}
		if req.Method != "DELETE" || strings.Contains(name, "/namespaces/") {
			t.Fatalf("unexpected native Data Fusion write %s %s", req.Method, req.URL)
		}
		body, _ := io.ReadAll(req.Body)
		if len(body) != 0 {
			t.Fatalf("Data Fusion DELETE has no body: %s", body)
		}
		if s.resources[name] == nil {
			return respond(404, map[string]any{})
		}
		s.mutations = append(s.mutations, "DELETE "+name)
		if strings.Contains(name, "/dnsPeerings/") {
			if req.URL.RawQuery != "" {
				t.Fatalf("DNS peering has no delete query: %s", req.URL)
			}
			delete(s.resources, name)
			return respond(200, map[string]any{})
		}
		if req.URL.RawQuery != "force=true" {
			t.Fatalf("instance delete must use the reviewed native cascade: %s", req.URL)
		}
		parent := strings.Join(strings.Split(name, "/")[:4], "/")
		op := parent + "/operations/delete-" + fmt.Sprint(len(s.mutations))
		s.operations[op] = map[string]any{"name": op, "done": false, "metadata": map[string]any{"@type": "type.googleapis.com/google.cloud.datafusion.v1.OperationMetadata", "apiVersion": "v1", "verb": "delete", "target": name, "requestedCancellation": false}}
		s.resources[name]["state"] = "DELETING"
		return respond(200, s.operations[op])
	}
}

func (s *fusionScenario) finish(operation string) {
	name := strings.TrimPrefix(operation, "https://datafusion.googleapis.com/v1/")
	op := s.operations[name]
	op["done"] = true
	root := text(object(op["metadata"])["target"])
	for key := range s.resources {
		if key == root || strings.HasPrefix(key, root+"/") {
			delete(s.resources, key)
		}
	}
}

func (s *fusionScenario) inventory(t *testing.T, r *Runtime) []asset.Asset {
	t.Helper()
	var assets []asset.Asset
	for _, kind := range []string{fusionInstanceType, fusionDNSType, fusionNamespaceType} {
		req := productRequest(r, kind, "project")
		for attempt := 0; ; attempt++ {
			batch, err := r.List(context.Background(), req)
			if err != nil || attempt > 30 {
				t.Fatalf("Data Fusion inventory %s: %+v %v", kind, batch, err)
			}
			for _, item := range batch.Items {
				capabilities := asset.CapabilitySet{}
				if item.Actionable != nil && *item.Actionable {
					capabilities = append(capabilities, asset.CapabilityActionable)
				}
				assets = append(assets, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "google-cloud", NativeType: item.NativeType, NativeID: item.NativeID}, Normalized: item.Normalized, Capabilities: capabilities, Location: item.Location})
			}
			if batch.Complete {
				break
			}
			req.Cursor = batch.NextCursor
		}
	}
	return assets
}

func fusionReviewed(t *testing.T, s *fusionScenario, root string) (*Runtime, []asset.Asset, plan.Input, contracts.ActionRequest) {
	t.Helper()
	r := protocolRuntime(t, s.transport(t))
	assets := s.inventory(t, r)
	hook, err := r.ServiceLifecycle(context.Background(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	contribution, err := hook.Contribute(context.Background(), "scope", assets)
	if err != nil || len(contribution.Unresolved) > 0 {
		t.Fatalf("Data Fusion lifecycle: %+v %v", contribution, err)
	}
	var relations []graph.Relationship
	for _, value := range assets {
		refs, _ := discoveryStrings(value.Normalized[referenceKey(fusionInstanceType)])
		for _, id := range refs {
			relations = append(relations, graph.Relationship{SourceAssetID: value.ID, TargetAssetID: asset.AssetID(id), Type: graph.RelationshipDependsOn, Confidence: 1})
		}
	}
	value := batchAsset(assets, root)
	input := plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{value.ID}, LifecycleBindings: contribution.Bindings, Relationships: append(contribution.Relationships, relations...)}
	result, err := plan.Solve(input)
	if err != nil || len(result.Blockers) > 0 {
		t.Fatalf("Data Fusion plan: %+v %v", result, err)
	}
	return r, assets, input, dataformRequest(t, result, assets, value)
}

func TestDataFusionNativeInventoryReviewedCascadeAndRestart(t *testing.T) {
	ctx := context.Background()
	s := newFusionScenario(t)
	s.emptyPage = true
	r, assets, input, request := fusionReviewed(t, s, fusionTestInstance)
	result, _ := plan.Solve(input)
	if len(assets) != 7 || len(result.Steps) != 1 || len(request.LifecycleImpacts) != 3 {
		t.Fatalf("native Data Fusion assets=%d plan=%+v", len(assets), result)
	}
	encoded, _ := json.Marshal(assets)
	if strings.Contains(string(encoded), "FUSION_PRIVATE_") {
		t.Fatal("Data Fusion private options/IAM policy escaped inventory redaction")
	}
	for _, value := range assets {
		if text(value.Normalized[fusionProof]) == "" || value.Identity.NativeType != fusionInstanceType && text(value.Normalized[fusionParentProof]) == "" {
			t.Fatalf("missing native snapshot proof: %+v", value)
		}
	}
	root := request.Asset.Normalized
	for kind, expected := range map[string]string{
		"compute.googleapis.com/Network":    "//compute.googleapis.com/projects/shared-project/global/networks/shared",
		"storage.googleapis.com/Bucket":     "//storage.googleapis.com/cdf-sample-staging",
		"cloudkms.googleapis.com/CryptoKey": "//cloudkms.googleapis.com/projects/sample-project/locations/us-central1/keyRings/cdf/cryptoKeys/data",
		"pubsub.googleapis.com/Topic":       "//pubsub.googleapis.com/projects/sample-project/topics/pipeline-events",
		"iam.googleapis.com/ServiceAccount": "//iam.googleapis.com/projects/tenant-project/serviceAccounts/management@tenant-project.iam.gserviceaccount.com",
	} {
		refs, _ := discoveryStrings(root[referenceKey(kind)])
		if !slices.Contains(refs, expected) {
			t.Fatalf("missing Data Fusion dependency %s: %+v", expected, refs)
		}
	}
	driver, err := r.ResolveAction(ctx, "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := driver.Execute(ctx, request)
	if err != nil || applied.Data["phase"] != "datafusion_delete" {
		t.Fatalf("instance delete: %+v %v", applied, err)
	}
	applied = roundTripDataformJSON(t, applied)
	driver, _ = r.ResolveAction(ctx, "connection", request.Asset)
	if wait, err := driver.Wait(ctx, request, applied); err != nil || wait.Done {
		t.Fatalf("pending native LRO: %+v %v", wait, err)
	}
	s.finish(applied.ProviderOperationID)
	if wait, err := driver.Wait(ctx, request, applied); err != nil || !wait.Done {
		t.Fatalf("completed native cascade: %+v %v", wait, err)
	}
	if read, err := driver.Readback(ctx, request); err != nil || read.Exists {
		t.Fatalf("native absence: %+v %v", read, err)
	}
	if !slices.Equal(s.mutations, []string{"DELETE " + fusionTestInstance}) || s.resources[fusionTestEurope] == nil {
		t.Fatalf("wrong native cascade scope: %+v", s.mutations)
	}
}

func TestDataFusionIndependentDNSAndReadOnlyNamespace(t *testing.T) {
	ctx := context.Background()
	s := newFusionScenario(t)
	r, assets, _, request := fusionReviewed(t, s, fusionTestDNS)
	if len(request.LifecycleImpacts) != 0 {
		t.Fatalf("DNS peering has no native cascade: %+v", request)
	}
	driver, _ := r.ResolveAction(ctx, "connection", request.Asset)
	applied, err := driver.Execute(ctx, request)
	if err != nil || applied.ProviderOperationID != "" || applied.Data["phase"] != "datafusion_delete" {
		t.Fatalf("synchronous DNS peering DELETE: %+v %v", applied, err)
	}
	if wait, err := driver.Wait(ctx, request, roundTripDataformJSON(t, applied)); err != nil || !wait.Done {
		t.Fatalf("DNS list readback: %+v %v", wait, err)
	}
	if !slices.Equal(s.mutations, []string{"DELETE " + fusionTestDNS}) || s.resources[fusionTestInstance] == nil || s.resources[fusionTestNS] == nil {
		t.Fatalf("independent DNS affected another resource: %+v", s.mutations)
	}
	for _, value := range assets {
		if value.Identity.NativeType == fusionNamespaceType {
			if slices.Contains(value.Capabilities, asset.CapabilityActionable) {
				t.Fatal("invented namespace actionability")
			}
			if _, err := r.ResolveAction(ctx, "connection", value); err == nil {
				t.Fatal("invented namespace DELETE driver")
			}
		}
	}
}

func TestDataFusionNativeRegionsAliasesAndPSCReferences(t *testing.T) {
	s := newFusionScenario(t)
	r := protocolRuntime(t, s.transport(t))
	batch, err := r.List(context.Background(), productRequest(r, fusionInstanceType, "europe-west1"))
	if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].Location != "europe-west1" {
		t.Fatalf("native regional inventory: %+v %v", batch, err)
	}
	refs, _ := discoveryStrings(batch.Items[0].Normalized[referenceKey("compute.googleapis.com/NetworkAttachment")])
	if !slices.Equal(refs, []string{"//compute.googleapis.com/projects/sample-project/regions/europe-west1/networkAttachments/cdf-psc"}) {
		t.Fatalf("PSC attachment dependency: %+v", refs)
	}
	c, _ := r.resolve(context.Background(), "connection")
	for _, value := range []string{fusionTestInstance, "https://datafusion.googleapis.com/v1/" + fusionTestInstance, "https://datafusion.googleapis.com/v1beta1/" + strings.Replace(fusionTestInstance, "sample-project", "123456", 1)} {
		id, err := c.fusionID(fusionInstanceType, value, "")
		if err != nil || id != "//datafusion.googleapis.com/"+fusionTestInstance {
			t.Fatalf("native alias %s: %s %v", value, id, err)
		}
	}
}

func TestDataFusionSettleBeforeDeleteAndResume(t *testing.T) {
	for _, target := range []string{fusionTestInstance, fusionTestDNS} {
		t.Run(last(strings.TrimSuffix(target, "/private")), func(t *testing.T) {
			ctx := context.Background()
			s := newFusionScenario(t)
			r, _, _, request := fusionReviewed(t, s, target)
			s.resources[fusionTestInstance]["state"] = "UPDATING"
			driver, _ := r.ResolveAction(ctx, "connection", request.Asset)
			result, err := driver.Execute(ctx, request)
			if err != nil || result.Data["phase"] != "datafusion_settle" || result.ProviderOperationID != "" || len(s.mutations) != 0 {
				t.Fatalf("native update must settle: %+v %v", result, err)
			}
			if wait, err := driver.Wait(ctx, request, result); err != nil || wait.Done || wait.Data["phase"] != "datafusion_settle" || len(s.mutations) != 0 {
				t.Fatalf("pending native update: %+v %v", wait, err)
			}
			s.resources[fusionTestInstance]["state"] = "ACTIVE"
			wait, err := driver.Wait(ctx, request, roundTripDataformJSON(t, result))
			if err != nil || wait.Done || wait.Data["phase"] != "datafusion_delete" || len(s.mutations) != 1 {
				t.Fatalf("settle -> native delete: %+v %v", wait, err)
			}
			result.Data = roundTripDataformJSON(t, wait.Data)
			if result.ProviderOperationID != "" || result.Data["initial_operation"] != "" {
				t.Fatal("persisted initial operation identity changed")
			}
			driver, _ = r.ResolveAction(ctx, "connection", request.Asset)
			if target == fusionTestInstance {
				s.finish(text(result.Data["operation"]))
			}
			if wait, err := driver.Wait(ctx, request, result); err != nil || !wait.Done {
				t.Fatalf("restart after native delete: %+v %v", wait, err)
			}
			if check, err := driver.Preflight(ctx, request); err != nil || !check.Allowed || !check.Absent {
				t.Fatalf("idempotent preflight: %+v %v", check, err)
			}
			if result, err := driver.Execute(ctx, request); err != nil || len(result.Data) != 0 || len(s.mutations) != 1 {
				t.Fatalf("idempotent execute: %+v %v", result, err)
			}
		})
	}
}

func TestDataFusionOperationCompletionRequiresNativeAbsence(t *testing.T) {
	for _, mode := range []string{"done_but_present", "expired_but_present", "absent_but_pending", "child_still_visible"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			s := newFusionScenario(t)
			r, _, _, request := fusionReviewed(t, s, fusionTestInstance)
			driver, _ := r.ResolveAction(ctx, "connection", request.Asset)
			result, err := driver.Execute(ctx, request)
			if err != nil {
				t.Fatal(err)
			}
			op := s.operations[fusionTestParent+"/operations/delete-1"]
			switch mode {
			case "done_but_present":
				op["done"] = true
			case "expired_but_present":
				delete(s.operations, text(op["name"]))
			case "absent_but_pending":
				s.finish(result.ProviderOperationID)
				op["done"] = false
			case "child_still_visible":
				child := roundTripDataformJSON(t, s.resources[fusionTestDNS])
				s.finish(result.ProviderOperationID)
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v1/"+fusionTestInstance+"/dnsPeerings" {
						return dataformResponse(req, 200, map[string]any{"dnsPeerings": []any{child}}), true
					}
					return nil, false
				}
			}
			if wait, err := driver.Wait(ctx, request, result); err != nil || wait.Done {
				t.Fatalf("premature completion %s: %+v %v", mode, wait, err)
			}
			s.operations[text(op["name"])] = op
			s.finish(result.ProviderOperationID)
			s.handle = nil
			if wait, err := driver.Wait(ctx, request, result); err != nil || !wait.Done {
				t.Fatalf("native absence %s: %+v %v", mode, wait, err)
			}
			if len(s.mutations) != 1 {
				t.Fatalf("readback retried DELETE: %+v", s.mutations)
			}
		})
	}
}

func TestDataFusionRemovedReviewedChildAndAlreadyDeletingInstance(t *testing.T) {
	ctx := context.Background()
	s := newFusionScenario(t)
	r, _, _, request := fusionReviewed(t, s, fusionTestInstance)
	delete(s.resources, fusionTestDNS)
	driver, _ := r.ResolveAction(ctx, "connection", request.Asset)
	if check, err := driver.Preflight(ctx, request); err != nil || !check.Allowed || check.Absent {
		t.Fatalf("complete list proves removed reviewed DNS absent: %+v %v", check, err)
	}
	s.resources[fusionTestInstance]["state"] = "DELETING"
	result, err := driver.Execute(ctx, request)
	if err != nil || result.Data["phase"] != "datafusion_delete" || result.ProviderOperationID != "" || len(s.mutations) != 0 {
		t.Fatalf("observe in-flight native deletion: %+v %v", result, err)
	}
	if wait, err := driver.Wait(ctx, request, result); err != nil || wait.Done || len(s.mutations) != 0 {
		t.Fatalf("observe existing deletion: %+v %v", wait, err)
	}
}

func TestDataFusionLogsAndInvokeRedactOptionsAndNamespacePolicy(t *testing.T) {
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	s := newFusionScenario(t)
	r := protocolRuntime(t, s.transport(t))
	for _, entry := range []struct {
		method string
		params map[string]any
	}{
		{"datafusion.projects.locations.instances.get", map[string]any{"name": fusionTestInstance}},
		{"datafusion.projects.locations.instances.dnsPeerings.list", map[string]any{"parent": fusionTestInstance}},
		{"datafusion.projects.locations.instances.namespaces.list", map[string]any{"parent": fusionTestInstance, "view": "NAMESPACE_VIEW_FULL"}},
	} {
		result, err := r.Invoke(ctx, contracts.Invocation{ConnectionID: "connection", Operation: entry.method, Parameters: entry.params})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(result)
		if strings.Contains(string(raw), "FUSION_PRIVATE_") {
			t.Fatalf("Data Fusion Invoke leak: %s", raw)
		}
	}
	raw, _ := json.Marshal(logs)
	if strings.Contains(string(raw), "FUSION_PRIVATE_") || len(logs) == 0 {
		t.Fatalf("Data Fusion log redaction: %s", raw)
	}
	c, _ := r.resolve(ctx, "connection")
	live, err := c.fusionRead(ctx, fusionInstanceType, "//datafusion.googleapis.com/"+fusionTestInstance)
	if err != nil || object(live["options"])["custom"] != "FUSION_PRIVATE_OPTION" {
		t.Fatal("sanitization mutated native proof input")
	}
}

func TestDataFusionCursorBindsParentIncarnation(t *testing.T) {
	s := newFusionScenario(t)
	s.emptyPage = true
	r := protocolRuntime(t, s.transport(t))
	req := productRequest(r, fusionNamespaceType, "us-central1")
	page, err := r.List(context.Background(), req)
	if err != nil || page.Complete || page.NextCursor == "" {
		t.Fatalf("namespace first page: %+v %v", page, err)
	}
	req.Cursor = page.NextCursor
	s.resources[fusionTestInstance]["createTime"] = "2026-08-02T00:00:00Z"
	if _, err := r.List(context.Background(), req); err == nil {
		t.Fatal("namespace cursor survived instance recreation")
	}
}
