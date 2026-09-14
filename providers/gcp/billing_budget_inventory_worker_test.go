package gcp

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/resourcequery"
	"github.com/loomx-ai/steward/internal/persistence"
)

func TestBillingBudgetSQLiteInventoryRestartAndAbsence(t *testing.T) {
	ctx := t.Context()
	scenario, r, mode := billingInventoryScenario(t)
	dsn := filepath.Join(t.TempDir(), "budgets.db")
	repos, closeDB := monitoringSQLite(t, dsn)
	defer func() { closeDB() }()
	now := time.Now().UTC()
	connection := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "global", ConnectionID: connection.ID, Kind: asset.ScopeGlobal, NativeID: "sample-project/global", CreatedAt: now, UpdatedAt: now}
	kind := r.resourceKind(billingBudgetType)
	for _, err := range []error{repos.Connections().PutConnection(ctx, connection), repos.Inventory().PutScope(ctx, scope), repos.Inventory().PutResourceKind(ctx, kind)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	var previous asset.Asset
	for i, phase := range []string{"first", "known-live", "known-account-denied", "known-account-missing", "known-denied", "known-gone", "reappeared"} {
		if i > 0 {
			closeDB()
			repos, closeDB = monitoringSQLite(t, dsn)
		}
		*mode = phase
		if phase == "reappeared" {
			scenario.budget["displayName"] = "Updated budget"
			scenario.budget["etag"] = "new-version"
		}
		now = now.Add(time.Minute)
		run := asset.ScanRun{ID: asset.ScanRunID(phase), ConnectionID: connection.ID, Status: asset.ScanPending, CreatedAt: now}
		shard := asset.ScanShard{ID: asset.ScanShardID(phase), ScanRunID: run.ID, Provider: asset.ProviderGCP, Source: billingBudgetSource, ScopeID: scope.ID, ResourceKindID: kind.ID, Authoritative: false, Status: asset.ShardPending, CreatedAt: now}
		if err := repos.Inventory().CreateScanRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
		fresh := protocolRuntime(t, r.transport.RoundTrip)
		h := inventory.NewScanHandler(repos, identityRegistry(t, fresh), inventory.NewService(repos.Inventory(), inventory.WithClock(func() time.Time { return now })))
		err := h.Handle(ctx, execution.Job{ID: execution.JobID(phase), Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": phase}})
		failed := phase == "known-account-denied" || phase == "known-account-missing" || phase == "known-denied"
		if (err != nil) != failed {
			t.Fatal(phase, err)
		}
		finished, err := repos.Inventory().GetScanShard(ctx, shard.ID)
		if err != nil || finished.Coverage.Complete == failed || finished.Authoritative {
			t.Fatal(phase, finished, err)
		}
		page, err := repos.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if phase == "known-gone" {
			if len(page.Items) != 0 {
				t.Fatal("exact native 404 did not close budget", page)
			}
			closed, err := repos.Inventory().GetAsset(ctx, previous.ID)
			if err != nil || closed.ClosedAt == nil || !closed.LastSeenAt.Equal(previous.LastSeenAt) {
				t.Fatal(closed, err)
			}
			continue
		}
		if len(page.Items) != 1 {
			t.Fatal(phase, page)
		}
		current := page.Items[0]
		if current.ClosedAt != nil || current.Identity.NativeID != testBillingBudgetID {
			t.Fatal(current)
		}
		if failed && (!current.LastSeenAt.Equal(previous.LastSeenAt) || current.Normalized[billingBudgetReview] != previous.Normalized[billingBudgetReview]) {
			t.Fatal("failed scan replaced prior observation")
		}
		if phase == "reappeared" && (current.ID != previous.ID || current.Normalized[billingBudgetReview] == previous.Normalized[billingBudgetReview]) {
			t.Fatal("reappeared budget lost identity or configuration")
		}
		expression, err := resourcequery.Parse(`properties.billingAccount = "billingAccounts/012345-678901-ABCDEF"`)
		if err != nil {
			t.Fatal(err)
		}
		if err := expression.Validate([]asset.ResourceKind{kind}); err != nil {
			t.Fatal(err)
		}
		matched, err := repos.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10, ResourceQuery: expression})
		if err != nil || len(matched.Items) != 1 {
			t.Fatal("budget query lost after restart", matched, err)
		}
		previous = current
	}
}
