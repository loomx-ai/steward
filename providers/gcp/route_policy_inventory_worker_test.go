package gcp

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/resourcequery"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	providerruntime "github.com/loomx-ai/steward/internal/provider/runtime"
)

func TestRoutePolicySQLiteFailureAbsenceAndRecovery(t *testing.T) {
	testRouterComponentSQLiteRecovery(t, routePolicyType)
}

func testRouterComponentSQLiteRecovery(t *testing.T, nativeType string) {
	list, get, field := "listRoutePolicies", "getRoutePolicy", "terms"
	fixture := routePolicyFixture
	reviewField, firstReview, recoveredReview := "fingerprint", "ZnAx", "ZnAy"
	if nativeType == namedSetType {
		list, get, field = "listNamedSets", "getNamedSet", "elements"
		fixture = namedSetFixture
	}
	if nativeType == cloudNatType {
		fixture = cloudNatFixture
		field = "rules"
		reviewField, firstReview, recoveredReview = "sourceSubnetworkIpRangesToNat", "LIST_OF_SUBNETWORKS", "ALL_SUBNETWORKS_ALL_IP_RANGES"
	}
	ctx := t.Context()
	phase := "first"
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Host != "compute.googleapis.com" {
			t.Fatal("unexpected API", req.URL)
		}
		if strings.HasSuffix(req.URL.Path, "/routers") {
			return dataformResponse(req, 200, map[string]any{"items": []any{map[string]any{"name": "router-a", "id": "1001", "selfLink": "https://www.googleapis.com" + req.URL.Path + "/router-a"}}}), nil
		}
		if nativeType == cloudNatType && strings.HasSuffix(req.URL.Path, "/router-a") {
			if phase == "denied" {
				return apiResponse(req, 403, `{}`), nil
			}
			if phase == "missing" {
				return apiResponse(req, 404, `{}`), nil
			}
			parent := cloudNatParent(req.URL.Path)
			if phase == "changed" {
				parent["id"] = "2000"
			}
			if phase != "absent" {
				data := fixture("policy-a")
				if phase == "recovered" {
					data[reviewField] = recoveredReview
					delete(data, "subnetworks")
				}
				parent["nats"] = []any{data}
			}
			return dataformResponse(req, 200, parent), nil
		}
		if strings.HasSuffix(req.URL.Path, "/"+list) {
			if phase == "absent" {
				return apiResponse(req, 200, `{"warning":{"code":"NO_RESULTS_ON_PAGE"}}`), nil
			}
			return apiResponse(req, 200, `{"result":[{"name":"policy-a"}]}`), nil
		}
		if strings.HasSuffix(req.URL.Path, "/"+get) {
			if phase == "denied" {
				return apiResponse(req, 403, `{}`), nil
			}
			if phase == "missing" {
				return apiResponse(req, 404, `{}`), nil
			}
			data := fixture("policy-a")
			if phase == "changed" {
				data["name"] = "other-policy"
			}
			if phase == "unresolved-set" {
				object(array(data["terms"])[0])["match"] = map[string]any{"expression": "prefixSets(name)"}
			}
			if phase == "recovered" {
				data["fingerprint"] = "ZnAy"
			}
			return dataformResponse(req, 200, map[string]any{"resource": data}), nil
		}
		t.Fatal("unexpected request", req.URL)
		return nil, nil
	})
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "route-policy.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "region", ConnectionID: connection.ID, Kind: asset.ScopeRegion, NativeID: "us-central1", CreatedAt: now, UpdatedAt: now}
	kind := r.resourceKind(nativeType)
	for _, write := range []func() error{
		func() error { return repositories.Connections().PutConnection(ctx, connection) },
		func() error { return repositories.Inventory().PutScope(ctx, scope) },
		func() error { return repositories.Inventory().PutResourceKind(ctx, kind) },
	} {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
	registry := providerruntime.NewRegistry()
	if err := registry.Register(r); err != nil {
		t.Fatal(err)
	}
	if err := registry.RegisterBundle(r.Bundle()); err != nil {
		t.Fatal(err)
	}
	handler := inventory.NewScanHandler(repositories, registry, inventory.NewService(repositories.Inventory()))
	var first asset.Asset
	steps := []string{"first", "denied", "missing", "changed"}
	if nativeType == routePolicyType {
		steps = append(steps, "unresolved-set")
	}
	steps = append(steps, "absent", "recovered")
	for _, step := range steps {
		phase = step
		run := asset.ScanRun{ID: asset.ScanRunID(step), ConnectionID: connection.ID, Status: asset.ScanPending, RequestedBy: "test", CreatedAt: now}
		shard := asset.ScanShard{ID: asset.ScanShardID(step), ScanRunID: run.ID, Provider: asset.ProviderGCP, Source: productInventorySource, ScopeID: scope.ID, ResourceKindID: kind.ID, Authoritative: true, Status: asset.ShardPending, CreatedAt: now}
		if err := repositories.Inventory().CreateScanRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
		failed := step == "denied" || step == "missing" || step == "changed" || step == "unresolved-set"
		err := handler.Handle(ctx, execution.Job{ID: execution.JobID(step), Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": string(shard.ID)}})
		if (err != nil) != failed {
			t.Fatalf("%s scan: %v", step, err)
		}
		finished, err := repositories.Inventory().GetScanShard(ctx, shard.ID)
		if err != nil || failed && (finished.Status != asset.ShardFailed || finished.Coverage.Complete) || !failed && (finished.Status != asset.ShardSucceeded || !finished.Coverage.Complete) {
			t.Fatal("wrong coverage", step, finished, err)
		}
		expression, err := resourcequery.Parse(`properties.type = "` + text(fixture("policy-a")["type"]) + `"`)
		if err != nil {
			t.Fatal(err)
		}
		if err := expression.Validate([]asset.ResourceKind{kind}); err != nil {
			t.Fatal(err)
		}
		page, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10, ResourceQuery: expression})
		if err != nil {
			t.Fatal(err)
		}
		if step == "absent" {
			if len(page.Items) != 0 {
				t.Fatal("authoritative empty list kept active policy")
			}
			old, err := repositories.Inventory().GetAsset(ctx, first.ID)
			if err != nil || old.ClosedAt == nil || !old.LastSeenAt.Equal(first.LastSeenAt) {
				t.Fatal("absence lost historical observation", old, err)
			}
			continue
		}
		if len(page.Items) != 1 {
			t.Fatal("query lost observed policy", step, page)
		}
		value := page.Items[0]
		if step == "first" {
			first = value
			if nativeType == cloudNatType {
				assertCloudNatSQLiteParentGraph(t, repositories, r, value)
			}
		}
		fingerprint := firstReview
		if step == "recovered" {
			fingerprint = recoveredReview
		}
		if value.ID != first.ID || value.ClosedAt != nil || value.Normalized[reviewField] != fingerprint || len(array(value.Normalized[field])) != 1 {
			t.Fatal("policy observation changed incorrectly", step, value)
		}
		if failed && !value.LastSeenAt.Equal(first.LastSeenAt) || step == "recovered" && !value.LastSeenAt.After(first.LastSeenAt) {
			t.Fatal("incorrect observation freshness", step)
		}
	}
}
