package relational

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/finding"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/core/resourcequery"
	"github.com/loomx-ai/steward/internal/persistence"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestNormalizeLegacyCleanupActionRequestID(t *testing.T) {
	legacy := execution.AuditEvent{
		Action:    "cleanup.action.succeeded",
		RequestID: "provider-request",
		Evidence:  map[string]any{"provider_operation_id": "provider-operation"},
	}
	normalizeLegacyCleanupActionRequestID(&legacy)
	if legacy.RequestID != "" || legacy.Evidence["provider_request_id"] != "provider-request" {
		t.Fatalf("legacy audit = %#v", legacy)
	}

	current := execution.AuditEvent{
		Action:    "cleanup.action.succeeded",
		RequestID: "steward-request",
		Evidence:  map[string]any{"provider_request_id": "provider-request"},
	}
	normalizeLegacyCleanupActionRequestID(&current)
	if current.RequestID != "steward-request" || current.Evidence["provider_request_id"] != "provider-request" {
		t.Fatalf("current audit = %#v", current)
	}
}

func TestPrimaryListReadsUseOneSQLStatement(t *testing.T) {
	countQueries := false
	queryCount := 0
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "list-queries.db")), &gorm.Config{
		TranslateError: true,
		Logger: queryCountingLogger{
			Interface: logger.Default.LogMode(logger.Silent),
			enabled:   &countQueries,
			count:     &queryCount,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := persistence.Migrate(sqlDB, "sqlite3", filepath.Join("..", "..", "..", "migrations")); err != nil {
		t.Fatal(err)
	}
	store := New(db)
	seedPrimaryListData(t, store)

	assertOneQuery := func(t *testing.T, read func() error) {
		t.Helper()
		queryCount = 0
		countQueries = true
		err := read()
		countQueries = false
		if err != nil {
			t.Fatal(err)
		}
		if queryCount != 1 {
			t.Fatalf("SQL statement count = %d, want 1", queryCount)
		}
	}

	tests := []struct {
		name string
		read func() error
	}{
		{
			name: "connections",
			read: func() error {
				_, err := store.ListConnectionAggregates(context.Background(), persistence.ListOptions{Limit: 50})
				return err
			},
		},
		{
			name: "regions",
			read: func() error {
				_, err := store.ListActiveConnectionRegions(context.Background(), persistence.RegionListOptions{
					ConnectionID: "connection-list", Limit: 50,
				})
				return err
			},
		},
		{
			name: "scopes",
			read: func() error {
				_, err := store.ListScopes(context.Background(), persistence.ListOptions{
					ConnectionID: "connection-list", Limit: 50,
				})
				return err
			},
		},
		{
			name: "scans",
			read: func() error {
				_, err := store.ListScanRunListItems(context.Background(), persistence.ListOptions{
					ConnectionID: "connection-list", Limit: 50,
				})
				return err
			},
		},
		{
			name: "scan logs",
			read: func() error {
				_, _, err := store.ListScanLogsBefore(
					context.Background(), "connection-list", "scan-list", "", time.Time{}, "", 50,
				)
				return err
			},
		},
		{
			name: "assets/account",
			read: func() error {
				_, err := store.ListAssets(context.Background(), persistence.ListOptions{
					ConnectionID: "connection-list", AssetCanvas: persistence.AssetCanvasAccount, Limit: 50,
				})
				return err
			},
		},
		{
			name: "assets/resource-query",
			read: func() error {
				expression, err := resourcequery.Parse(`type = "ACS::VPC::VPC" AND properties.vpc_id = "vpc-1"`)
				if err != nil {
					return err
				}
				page, err := store.ListAssets(context.Background(), persistence.ListOptions{
					ConnectionID: "connection-list", ResourceQuery: expression, Limit: 50,
				})
				if err == nil && (len(page.Items) != 1 || page.Items[0].ID != "asset-vpc-list") {
					t.Fatalf("resource-query assets = %#v", page.Items)
				}
				return err
			},
		},
		{
			name: "assets/global",
			read: func() error {
				page, err := store.ListAssets(context.Background(), persistence.ListOptions{
					ConnectionID: "connection-list", AssetCanvas: persistence.AssetCanvasGlobal, Limit: 50,
				})
				if err == nil && len(page.Items) != 1 {
					t.Fatalf("global assets = %#v", page.Items)
				}
				return err
			},
		},
		{
			name: "assets/region",
			read: func() error {
				page, err := store.ListAssets(context.Background(), persistence.ListOptions{
					ConnectionID: "connection-list", AssetCanvas: persistence.AssetCanvasRegion,
					RegionID: "cn-hangzhou", Limit: 50,
				})
				if err == nil && len(page.Items) != 1 {
					t.Fatalf("region assets = %#v", page.Items)
				}
				return err
			},
		},
		{
			name: "assets/region-public",
			read: func() error {
				_, err := store.ListAssets(context.Background(), persistence.ListOptions{
					ConnectionID: "connection-list", AssetCanvas: persistence.AssetCanvasRegionPublic,
					RegionID: "cn-hangzhou", Limit: 50,
				})
				return err
			},
		},
		{
			name: "assets/vpc",
			read: func() error {
				page, err := store.ListAssets(context.Background(), persistence.ListOptions{
					ConnectionID: "connection-list", AssetCanvas: persistence.AssetCanvasVPC,
					RegionID: "cn-hangzhou", VPCID: "vpc-1", Limit: 50,
				})
				if err == nil && len(page.Items) != 1 {
					t.Fatalf("VPC assets = %#v", page.Items)
				}
				return err
			},
		},
		{
			name: "findings",
			read: func() error {
				_, err := store.ListFindings(context.Background(), persistence.ListOptions{
					ConnectionID: "connection-list", Limit: 50,
				})
				return err
			},
		},
		{
			name: "findings/asset",
			read: func() error {
				_, err := store.ListFindingsForAsset(
					context.Background(), "connection-list", "asset-vpc-list",
				)
				return err
			},
		},
		{
			name: "relationships/asset",
			read: func() error {
				_, err := store.ListRelationshipsForAsset(
					context.Background(), "connection-list", "asset-vpc-list",
				)
				return err
			},
		},
		{
			name: "lifecycle bindings/asset",
			read: func() error {
				_, err := store.ListLifecycleBindingsForAsset(
					context.Background(), "connection-list", "asset-vpc-list",
				)
				return err
			},
		},
		{
			name: "cleanup tasks",
			read: func() error {
				_, err := store.ListTasks(context.Background(), persistence.ListOptions{
					ConnectionID: "connection-list", Limit: 50,
				})
				return err
			},
		},
		{
			name: "cleanup logs",
			read: func() error {
				_, _, err := store.ListCleanupLogsBefore(
					context.Background(), "connection-list", "cln-list", persistence.CleanupLogFilter{}, time.Time{}, "", 50,
				)
				return err
			},
		},
		{
			name: "executions",
			read: func() error {
				_, err := store.ListExecutions(context.Background(), persistence.ListOptions{
					ConnectionID: "connection-list", Limit: 50,
				})
				return err
			},
		},
		{
			name: "executions/cleanup-task",
			read: func() error {
				_, err := store.ListCleanupTaskExecutions(
					context.Background(), "connection-list", "cln-list",
					persistence.ListOptions{Limit: 50},
				)
				return err
			},
		},
		{
			name: "actions/execution",
			read: func() error {
				_, err := store.ListExecutionActions(
					context.Background(), "connection-list", "execution-list",
				)
				return err
			},
		},
		{
			name: "audits",
			read: func() error {
				_, err := store.ListAuditEvents(context.Background(), persistence.ListOptions{
					ConnectionID: "connection-list", Limit: 50,
				})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			assertOneQuery(t, test.read)
		})
	}
}

func seedPrimaryListData(t *testing.T, store *Store) {
	t.Helper()
	ctx := context.Background()
	now := time.Date(2026, 8, 4, 9, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-list", Name: "list account", Provider: asset.ProviderAliCloud,
		Partition: "public", Principal: "list account", Status: asset.ConnectionActive,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := store.PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if err := store.PutCredential(ctx, asset.ConnectionCredential{
		ConnectionID: connection.ID, Provider: connection.Provider, Type: asset.CredentialAliCloudAccessKey,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutRegion(ctx, asset.ConnectionRegion{
		ID: "region-list", ConnectionID: connection.ID, RegionID: "cn-hangzhou",
		Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, scope := range []asset.Scope{
		{ID: "scope-account-list", ConnectionID: connection.ID, Kind: asset.ScopeAccount, NativeID: "account", CreatedAt: now, UpdatedAt: now},
		{ID: "scope-global-list", ConnectionID: connection.ID, ParentID: "scope-account-list", Kind: asset.ScopeGlobal, NativeID: "global", CreatedAt: now, UpdatedAt: now},
		{ID: "scope-region-list", ConnectionID: connection.ID, ParentID: "scope-account-list", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", CreatedAt: now, UpdatedAt: now},
		{ID: "scope-vpc-list", ConnectionID: connection.ID, ParentID: "scope-region-list", Kind: asset.ScopeProject, NativeID: "vpc-1", CreatedAt: now, UpdatedAt: now},
	} {
		if err := store.PutScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.PutResourceKind(ctx, asset.ResourceKind{
		ID: "kind-vpc-list", Provider: connection.Provider, NativeType: "ACS::VPC::VPC",
		Class: "network.vpc", BundleRevision: "test",
	}); err != nil {
		t.Fatal(err)
	}
	for _, value := range []asset.Asset{
		{
			ID: "asset-global-list",
			Identity: asset.Identity{
				Provider: connection.Provider, Partition: connection.Partition,
				ConnectionID: connection.ID, NativeType: "ACS::RAM::Role", NativeID: "role-1",
			},
			ScopeID: "scope-account-list", ResourceKindID: "kind-global-list",
			FirstSeenAt: now, LastSeenAt: now,
		},
		{
			ID: "asset-vpc-list",
			Identity: asset.Identity{
				Provider: connection.Provider, Partition: connection.Partition,
				ConnectionID: connection.ID, NativeType: "ACS::VPC::VPC", NativeID: "vpc-1",
			},
			ScopeID: "scope-vpc-list", ResourceKindID: "kind-vpc-list",
			Normalized:  map[string]any{"vpc_id": "vpc-1"},
			FirstSeenAt: now.Add(time.Second), LastSeenAt: now.Add(time.Second),
		},
	} {
		if err := store.PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.ReplaceGraph(ctx, "scope-vpc-list", "graph-list", []graph.Relationship{{
		ID: "relationship-list", SourceAssetID: "asset-vpc-list", TargetAssetID: "asset-global-list",
		Type: graph.RelationshipMemberOf, Source: "test", Confidence: 1,
		GraphRevision: "graph-list", ObservedAt: now,
	}}, []graph.LifecycleBinding{{
		ID: "binding-list", ControllerAssetID: "asset-global-list", ManagedAssetID: "asset-vpc-list",
		Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive,
		CleanupPolicy: graph.CleanupDelegate, Confidence: 1,
		GraphRevision: "graph-list", ObservedAt: now,
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateTask(ctx, plan.CleanupTask{
		ID: "cln-list", ConnectionID: connection.ID, Status: plan.StatusReady,
		CreatedBy: "tester", CreatedAt: now,
	}, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateExecution(ctx, execution.ExecutionAttempt{
		ID: "execution-list", ConnectionID: connection.ID, CleanupTaskID: "cln-list",
		Status: execution.ExecutionPending, IdempotencyKey: "execution-list", CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateScanRun(ctx, asset.ScanRun{
		ID: "scan-list", ConnectionID: connection.ID, Status: asset.ScanSucceeded,
		Targets: []asset.ScanTarget{{
			Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou",
		}},
		CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAction(ctx, execution.ActionAttempt{
		ID: "action-list", ExecutionID: "execution-list", CleanupTaskStepID: "step-list",
		AssetID: "asset-vpc-list", Status: execution.ActionPending,
		IdempotencyKey: "action-list", CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.PutFinding(ctx, finding.Finding{
		ID: "finding-list", AssetID: "asset-vpc-list", RuleID: "rule-list",
		Status: finding.StatusOpen, Severity: finding.SeverityLow,
		FirstSeenAt: now, LastSeenAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AppendAuditEvent(ctx, execution.AuditEvent{
		ID: "audit-list", ConnectionID: connection.ID, Actor: "tester",
		Action: "list.test", TargetType: "connection", TargetID: string(connection.ID), CreatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.Enqueue(ctx, execution.Job{
		ID: "region-refresh-list", ConnectionID: connection.ID, Type: execution.JobRegionRefresh,
		Status: execution.JobSucceeded, RunAt: now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	for _, log := range []execution.JobLog{
		{
			ID: "scan-log-list", JobID: "scan-job-list", Sequence: 1,
			AggregateType: "scan_task", AggregateID: "scan-list",
			TargetKey: "region:cn-hangzhou", CreatedAt: now,
		},
		{
			ID: "cleanup-log-list", JobID: "cleanup-job-list", Sequence: 1,
			AggregateType: "cleanup_task", AggregateID: "cln-list", CreatedAt: now,
		},
	} {
		if err := store.AppendLog(ctx, log); err != nil {
			t.Fatal(err)
		}
	}
}
