package gcp

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/persistence"
)

func TestMonitoringGroupSQLiteInventoryAndGraphRestart(t *testing.T) {
	ctx := t.Context()
	s := newMonitoringGroupScenario(t)
	dsn := filepath.Join(t.TempDir(), "groups.db")
	repos, closeDB := monitoringSQLite(t, dsn)
	defer func() { closeDB() }()
	now := time.Now().UTC()
	conn := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "global", ConnectionID: conn.ID, Kind: asset.ScopeGlobal, NativeID: "sample-project/global", CreatedAt: now, UpdatedAt: now}
	kind := s.r.resourceKind(monitoringGroupType)
	vmKind := s.r.resourceKind(instanceType)
	target := asset.Asset{ID: "vm", ScopeID: scope.ID, ResourceKindID: vmKind.ID, Identity: asset.Identity{ConnectionID: conn.ID, Provider: asset.ProviderGCP, Partition: "gcp", NativeType: instanceType, NativeID: uptimeVM}, Normalized: map[string]any{"id": "87654321"}, FirstSeenAt: now, LastSeenAt: now, Capabilities: vmKind.Capabilities}
	for _, err := range []error{repos.Connections().PutConnection(ctx, conn), repos.Inventory().PutScope(ctx, scope), repos.Inventory().PutResourceKind(ctx, kind), repos.Inventory().PutResourceKind(ctx, vmKind), repos.Inventory().PutAsset(ctx, target)} {
		if err != nil {
			t.Fatal(err)
		}
	}
	var previous asset.Asset
	for _, phase := range []string{"first", "members-page-denied", "get-missing", "members-total", "members-removed", "empty", "reappeared"} {
		closeDB()
		repos, closeDB = monitoringSQLite(t, dsn)
		now = now.Add(time.Minute)
		s.mode = phase
		s.reads = 0
		s.memberReads = 0
		s.interval = ""
		if phase == "members-removed" {
			s.members = []any{}
		}
		if phase == "reappeared" {
			s.members = []any{monitoringGroupMemberFixture()}
			s.group["displayName"] = "New observation"
		}
		run := asset.ScanRun{ID: asset.ScanRunID(phase), ConnectionID: conn.ID, Status: asset.ScanPending, CreatedAt: now}
		shard := asset.ScanShard{ID: asset.ScanShardID(phase), ScanRunID: run.ID, Provider: asset.ProviderGCP, Source: productInventorySource, ScopeID: scope.ID, ResourceKindID: kind.ID, Authoritative: true, Status: asset.ShardPending, CreatedAt: now}
		if err := repos.Inventory().CreateScanRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
		fresh := protocolRuntime(t, s.r.transport.RoundTrip)
		registry := identityRegistry(t, fresh)
		h := inventory.NewScanHandler(repos, registry, inventory.NewService(repos.Inventory(), inventory.WithClock(func() time.Time { return now })))
		err := h.Handle(ctx, execution.Job{ID: execution.JobID(phase), Type: execution.JobScan, Payload: map[string]any{"scan_shard_id": phase}})
		failed := phase == "members-page-denied" || phase == "get-missing" || phase == "members-total"
		if (err != nil) != failed {
			t.Fatal(phase, err)
		}
		if !failed {
			if err := governance.NewGraphHandler(repos, registry, uptimeTargetContributors{}).Handle(ctx, execution.Job{Type: execution.JobGraph, Payload: map[string]any{"scan_run_id": phase}}); err != nil {
				t.Fatal(err)
			}
		}
		page, err := repos.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		var current asset.Asset
		for _, value := range page.Items {
			if value.Identity.NativeType == monitoringGroupType {
				current = value
			}
		}
		if phase == "empty" {
			if current.ID != "" {
				t.Fatal("complete empty group list retained group")
			}
			closed, err := repos.Inventory().GetAsset(ctx, previous.ID)
			if err != nil || closed.ClosedAt == nil {
				t.Fatal(closed, err)
			}
			continue
		}
		if current.ID == "" {
			t.Fatal("group disappeared after scan", phase)
		}
		if failed && (!current.LastSeenAt.Equal(previous.LastSeenAt) || current.Normalized[monitoringGroupReview] != previous.Normalized[monitoringGroupReview]) {
			t.Fatal("failed scan replaced group")
		}
		relationships, err := repos.Graph().ListRelationshipsForAsset(ctx, conn.ID, current.ID)
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if phase == "members-removed" {
			want = 0
		}
		if len(relationships) != want {
			t.Fatal(phase, relationships)
		}
		if want == 1 && (relationships[0].TargetAssetID != target.ID || relationships[0].Source != "gcp:monitoring-group-targets" || relationships[0].Type != graph.RelationshipDependsOn) {
			t.Fatal(relationships)
		}
		if phase == "reappeared" && (current.ID != previous.ID || current.Normalized[monitoringGroupReview] == previous.Normalized[monitoringGroupReview]) {
			t.Fatal("reappeared group lost identity or proof")
		}
		b, _ := json.Marshal(current)
		if strings.Contains(string(b), "PRIVATE_GROUP") {
			t.Fatal("private filter persisted")
		}
		if _, err := fresh.ResolveAction(ctx, conn.ID, current); err == nil {
			t.Fatal("observation enabled group deletion")
		}
		previous = current
	}
}
