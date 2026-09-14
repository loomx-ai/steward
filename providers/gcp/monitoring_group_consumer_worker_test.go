package gcp

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
)

func TestMonitoringGroupConsumerSQLiteGraphRestart(t *testing.T) {
	ctx := t.Context()
	s := newMonitoringGroupConsumerScenario(t)
	dsn := filepath.Join(t.TempDir(), "group-consumers.db")
	repos, closeDB := monitoringSQLite(t, dsn)
	defer func() { closeDB() }()
	now := time.Now().UTC()
	conn := asset.CloudConnection{ID: "connection", Provider: asset.ProviderGCP, Partition: "gcp", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
	scope := asset.Scope{ID: "global", ConnectionID: conn.ID, Kind: asset.ScopeGlobal, NativeID: "sample-project/global", CreatedAt: now, UpdatedAt: now}
	if err := repos.Connections().PutConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	if err := repos.Inventory().PutScope(ctx, scope); err != nil {
		t.Fatal(err)
	}
	// The parent and policy are inventoried; child/Uptime consumers are deliberately
	// missing, so their blocking references must survive failed graph refreshes.
	for _, value := range []asset.Asset{s.assets[0], s.assets[3]} {
		value.ScopeID = scope.ID
		value.FirstSeenAt = now
		value.LastSeenAt = now
		kind := s.r.resourceKind(value.Identity.NativeType)
		if err := repos.Inventory().PutResourceKind(ctx, kind); err != nil {
			t.Fatal(err)
		}
		if err := repos.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	var previousEdges []graph.Relationship
	var previousUnresolved []graph.UnresolvedReference
	for _, phase := range []string{"first", "page-denied", "get-missing", "unknown-dashboard", "cleared", "returned"} {
		closeDB()
		repos, closeDB = monitoringSQLite(t, dsn)
		now = now.Add(time.Minute)
		s.lists = map[string]int{}
		s.gets = map[string]int{}
		s.mode, s.collection = "normal", "dashboards"
		switch phase {
		case "page-denied":
			s.mode = "page-denied"
		case "get-missing":
			s.mode = "get-missing"
		case "unknown-dashboard":
			s.values["dashboards"][0]["futureWidget"] = map[string]any{"query": "PRIVATE_QUERY"}
		case "cleared":
			s.values["groups"] = s.values["groups"][:1]
			s.values["uptimeCheckConfigs"] = nil
			s.values["alertPolicies"] = nil
			s.values["dashboards"] = nil
		case "returned":
			fresh := newMonitoringGroupConsumerScenario(t)
			s.values = fresh.values
		}
		if phase == "cleared" || phase == "returned" {
			batch, err := s.r.List(ctx, productRequest(s.r, alertPolicyType, "global"))
			if err != nil || !batch.Complete {
				t.Fatal(batch, err)
			}
			policy, err := repos.Inventory().GetAsset(ctx, s.assets[3].ID)
			if err != nil {
				t.Fatal(err)
			}
			if phase == "cleared" {
				if len(batch.Items) != 0 {
					t.Fatal(batch)
				}
				policy.ClosedAt = &now
			} else {
				if len(batch.Items) != 1 {
					t.Fatal(batch)
				}
				policy.ClosedAt = nil
				policy.Normalized = batch.Items[0].Normalized
			}
			if err := repos.Inventory().PutAsset(ctx, policy); err != nil {
				t.Fatal(err)
			}
		}
		run := asset.ScanRun{ID: asset.ScanRunID(phase), ConnectionID: conn.ID, Status: asset.ScanSucceeded, CreatedAt: now}
		if err := repos.Inventory().CreateScanRun(ctx, run); err != nil {
			t.Fatal(err)
		}
		shard := asset.ScanShard{ID: asset.ScanShardID(phase), ScanRunID: run.ID, Provider: asset.ProviderGCP, ScopeID: scope.ID, ResourceKindID: s.assets[0].ResourceKindID, Source: productInventorySource, Status: asset.ShardSucceeded, Authoritative: true, CreatedAt: now}
		if err := repos.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
		fresh := protocolRuntime(t, s.r.transport.RoundTrip)
		err := governance.NewGraphHandler(repos, identityRegistry(t, fresh), monitoringDependencyContributors{r: fresh}).Handle(ctx, execution.Job{Type: execution.JobGraph, Payload: map[string]any{"scan_run_id": phase}})
		failed := phase == "page-denied" || phase == "get-missing"
		if (err != nil) != failed {
			t.Fatal(phase, err)
		}
		edges, err := repos.Graph().ListRelationshipsForAsset(ctx, conn.ID, s.assets[0].ID)
		if err != nil {
			t.Fatal(err)
		}
		unresolved, err := repos.Graph().ListUnresolvedByConnection(ctx, conn.ID)
		if err != nil {
			t.Fatal(err)
		}
		if failed {
			if firewallDigest(edges) != firewallDigest(previousEdges) || firewallDigest(unresolved) != firewallDigest(previousUnresolved) {
				t.Fatal("failed refresh erased persisted consumers", phase)
			}
		}
		wantEdges, wantUnresolved := 1, 2
		if phase == "unknown-dashboard" {
			wantUnresolved = 4
		}
		if phase == "cleared" {
			wantEdges, wantUnresolved = 0, 0
		}
		if len(edges) != wantEdges || len(unresolved) != wantUnresolved {
			t.Fatal(phase, edges, unresolved)
		}
		for _, edge := range edges {
			if edge.TargetAssetID != s.assets[3].ID || edge.Evidence[graph.RelationshipEvidenceAutomaticSelection] != false {
				t.Fatal(edge)
			}
		}
		for _, ref := range unresolved {
			if !ref.BlocksCleanup || (ref.ControllerID != s.assets[0].ID && !(phase == "unknown-dashboard" && ref.ControllerID == s.assets[3].ID)) {
				t.Fatal(ref)
			}
		}
		payload, _ := json.Marshal(map[string]any{"edges": edges, "unresolved": unresolved})
		if strings.Contains(string(payload), "PRIVATE_") {
			t.Fatal("private native query persisted")
		}
		previousEdges, previousUnresolved = edges, unresolved
	}
}
