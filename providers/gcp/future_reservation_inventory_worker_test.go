package gcp

import (
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestFutureReservationSQLiteScanFailureAndReconciliation(t *testing.T) {
	phase := "first"
	transport := func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Host != "compute.googleapis.com" || req.URL.Path != "/compute/v1/projects/sample-project/aggregated/futureReservations" {
			t.Fatalf("unexpected reservation request %s %s", req.Method, req.URL)
		}
		if phase == "denied" {
			return apiResponse(req, 403, `{"error":{"code":403}}`), nil
		}
		if phase == "malformed" {
			return apiResponse(req, 200, `{"items":{"zones/us-central1-a":{"futureReservations":{}}}}`), nil
		}
		if phase == "empty" {
			return apiResponse(req, 200, `{"items":{}}`), nil
		}
		first := futureReservationFixture("us-central1-a", "first")
		if phase == "reconcile" {
			object(first["status"])["procurementStatus"] = "FULFILLED"
			object(first["status"])["fulfilledCount"] = "9007199254740993"
		}
		rows := map[string]any{"zones/us-central1-a": map[string]any{"futureReservations": []any{first}}}
		if phase != "reconcile" {
			rows["zones/us-central1-b"] = map[string]any{"futureReservations": []any{futureReservationFixture("us-central1-b", "second")}}
		}
		if phase == "partial" {
			rows["zones/us-central1-b"] = map[string]any{"warning": map[string]any{"code": "UNREACHABLE"}}
		}
		raw, _ := json.Marshal(map[string]any{"items": rows})
		return apiResponse(req, 200, string(raw)), nil
	}
	runtime := protocolRuntime(t, transport)
	var source contracts.InventorySource
	for _, candidate := range runtime.InventorySources() {
		if candidate.Name == productInventorySource {
			source = candidate
		}
	}
	if !source.AuthoritativeDefault || !source.KindSpecific {
		t.Fatal("native source semantics lost")
	}
	db := filepath.Join(t.TempDir(), "future-reservations.db")
	repo, err := sqlite.Open(db, "../../migrations")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 14, 0, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{ID: "connection", Name: "Future reservations", Provider: asset.ProviderGCP, Partition: "google-cloud", Principal: "fixture", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "future-reservation-region", ConnectionID: connection.ID, Kind: asset.ScopeRegion, NativeID: "us-central1", Name: "us-central1", CreatedAt: now, UpdatedAt: now}
	kind := runtime.resourceKind(futureReservationTestType)
	for _, write := range []func() error{
		func() error { return repo.Connections().PutConnection(t.Context(), connection) },
		func() error { return repo.Inventory().PutScope(t.Context(), scope) },
		func() error { return repo.Inventory().PutResourceKind(t.Context(), kind) },
	} {
		if err := write(); err != nil {
			t.Fatal(err)
		}
	}
	for _, step := range []string{"first", "denied", "partial", "malformed", "reconcile", "empty"} {
		phase = step
		if step == "denied" {
			repo, err = sqlite.Open(db, "../../migrations")
			if err != nil {
				t.Fatal(err)
			}
			fresh, err := NewRuntime(runtime.credentials)
			if err != nil {
				t.Fatal(err)
			}
			fresh.transport = runtime.transport
			runtime = fresh
		}
		run := asset.ScanRun{ID: asset.ScanRunID(step), ConnectionID: connection.ID, Status: asset.ScanPending, RequestedBy: "fixture", CreatedAt: now}
		shard := asset.ScanShard{ID: asset.ScanShardID("future-reservation-" + step), ScanRunID: run.ID, Provider: asset.ProviderGCP, Source: source.Name, ScopeID: scope.ID, ResourceKindID: kind.ID, Authoritative: source.AuthoritativeDefault, Status: asset.ShardPending, CreatedAt: now}
		if err := repo.Inventory().CreateScanRun(t.Context(), run); err != nil {
			t.Fatal(err)
		}
		if err := repo.Inventory().PutScanShard(t.Context(), shard); err != nil {
			t.Fatal(err)
		}
		service := inventory.NewService(repo.Inventory(), inventory.WithClock(func() time.Time { return now }))
		handler := inventory.NewScanHandler(repo, dataformVisibilityRuntime{adapter: runtime}, service)
		err := handler.Handle(t.Context(), execution.Job{ID: execution.JobID("future-reservation-" + step), Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": string(shard.ID)}})
		failed := step == "denied" || step == "partial" || step == "malformed"
		if (err != nil) != failed {
			t.Fatal("wrong native scan outcome", step, err)
		}
		completed, err := repo.Inventory().GetScanShard(t.Context(), shard.ID)
		if err != nil || completed.Coverage.Complete == failed {
			t.Fatal("wrong coverage", step, completed, err)
		}
		page, err := repo.Inventory().ListAssets(t.Context(), persistence.ListOptions{Limit: 10, IncludeClosed: true})
		if err != nil || len(page.Items) != 2 {
			t.Fatal("reservation projection lost", len(page.Items), err)
		}
		closed := 0
		for _, value := range page.Items {
			if value.ClosedAt != nil {
				closed++
			}
			wantState, wantCount := "FAILED_PARTIALLY_FULFILLED", "9007199254740992"
			if (step == "reconcile" || step == "empty") && strings.HasSuffix(value.Identity.NativeID, "/first") {
				wantState, wantCount = "FULFILLED", "9007199254740993"
			}
			if value.State != wantState || value.Normalized["state"] != wantState || value.Normalized["fulfilledCount"] != wantCount || value.Normalized["requestedInstanceCount"] != "9007199254740993" || value.Normalized["planningStatus"] != "SUBMITTED" || len(array(value.Normalized["autoCreatedReservations"])) != 2 || !value.Capabilities.Has(asset.CapabilityActionable) {
				t.Fatal("persisted reservation metadata changed", step, value)
			}
		}

		expected := 0
		if step == "reconcile" {
			expected = 1
		}
		if step == "empty" {
			expected = 2
		}
		if closed != expected {
			t.Fatal("failed scan established absence", step, closed)
		}
		now = now.Add(time.Minute)
	}
}
