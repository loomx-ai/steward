package region

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestRefreshMergesDiscoveryWithoutOverwritingUserDecisions(t *testing.T) {
	ctx := context.Background()
	repositories, service, now := regionFixture(t)
	firstSeen := now.Add(-24 * time.Hour)
	lastSeen := now.Add(-time.Hour)
	seedRegions(t, repositories,
		asset.ConnectionRegion{ID: "hangzhou", ConnectionID: "conn-a", RegionID: "cn-hangzhou", DiscoveredName: "old", NameOverride: "杭州", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: firstSeen, UpdatedAt: lastSeen},
		asset.ConnectionRegion{ID: "shanghai", ConnectionID: "conn-a", RegionID: "cn-shanghai", DiscoveredName: "old", Origin: asset.RegionOriginManual, Lifecycle: asset.RegionRetired, FirstSeenAt: &firstSeen, LastSeenAt: &lastSeen, CreatedAt: firstSeen, UpdatedAt: lastSeen},
		asset.ConnectionRegion{ID: "beijing", ConnectionID: "conn-a", RegionID: "cn-beijing", DiscoveredName: "old", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionExcluded, FirstSeenAt: &firstSeen, LastSeenAt: &lastSeen, CreatedAt: firstSeen, UpdatedAt: lastSeen},
		asset.ConnectionRegion{ID: "qingdao", ConnectionID: "conn-a", RegionID: "cn-qingdao", DiscoveredName: "missing", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, FirstSeenAt: &firstSeen, LastSeenAt: &lastSeen, CreatedAt: firstSeen, UpdatedAt: lastSeen},
	)

	summary, err := service.Refresh(ctx, "conn-a", []contracts.DiscoveredRegion{
		{RegionID: " cn-shanghai ", Name: " 华东 2 "},
		{RegionID: "cn-hangzhou", Name: "华东 1"},
		{RegionID: "cn-beijing", Name: "华北 2"},
		{RegionID: "cn-shenzhen", Name: "华南 1"},
	}, "system", "provider-request")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Added != 1 || summary.Updated != 3 || summary.Missing != 1 || summary.Active != 3 || summary.Retired != 1 || summary.Excluded != 1 {
		t.Fatalf("summary = %#v", summary)
	}
	regions, err := repositories.Regions().ListRegionsByConnection(ctx, "conn-a")
	if err != nil {
		t.Fatal(err)
	}
	byID := regionMap(regions)
	if got := byID["cn-hangzhou"]; got.NameOverride != "杭州" || got.DiscoveredName != "华东 1" || got.FirstSeenAt == nil || got.LastSeenAt == nil || !got.LastSeenAt.Equal(now) || got.Origin != asset.RegionOriginAPI || got.Lifecycle != asset.RegionActive {
		t.Fatalf("hangzhou = %#v", got)
	}
	if got := byID["cn-shanghai"]; got.Origin != asset.RegionOriginManual || got.Lifecycle != asset.RegionRetired || got.DiscoveredName != "华东 2" {
		t.Fatalf("shanghai = %#v", got)
	}
	if got := byID["cn-beijing"]; got.Lifecycle != asset.RegionExcluded || got.DiscoveredName != "华北 2" {
		t.Fatalf("beijing = %#v", got)
	}
	if got := byID["cn-qingdao"]; got.DiscoveredName != "missing" || got.LastSeenAt == nil || !got.LastSeenAt.Equal(lastSeen) || !got.UpdatedAt.Equal(lastSeen) {
		t.Fatalf("qingdao = %#v", got)
	}
	if got := byID["cn-shenzhen"]; got.Origin != asset.RegionOriginAPI || got.Lifecycle != asset.RegionActive || got.FirstSeenAt == nil || !got.FirstSeenAt.Equal(now) {
		t.Fatalf("shenzhen = %#v", got)
	}
	audits, err := repositories.Audits().ListAuditEvents(ctx, persistence.ListOptions{ConnectionID: "conn-a", Limit: 10})
	if err != nil || len(audits.Items) != 1 || audits.Items[0].Action != "region.refresh" || audits.Items[0].RequestID != "" || audits.Items[0].Evidence["provider_request_id"] != "provider-request" || audits.Items[0].Evidence["added"] != float64(1) && audits.Items[0].Evidence["added"] != 1 {
		t.Fatalf("audits = %#v, err = %v", audits, err)
	}
}

func TestRefreshAutomaticallyRetiresClosedRegions(t *testing.T) {
	ctx := context.Background()
	repositories, service, now := regionFixture(t)
	seedRegions(t, repositories,
		asset.ConnectionRegion{ID: "fuzhou", ConnectionID: "conn-a", RegionID: "cn-fuzhou", DiscoveredName: "华东 6（福州）", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
		asset.ConnectionRegion{ID: "russia-west", ConnectionID: "conn-a", RegionID: "rus-west-1", DiscoveredName: "Russia West 1", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
	)

	summary, err := service.Refresh(ctx, "conn-a", []contracts.DiscoveredRegion{
		{RegionID: "cn-fuzhou", Name: "华东 6（福州-本地地域）关停中"},
		{RegionID: "rus-west-1", Name: "Russia West 1"},
		{RegionID: "cn-hangzhou", Name: "华东 1（杭州）"},
		{RegionID: "cn-retired", Name: "测试地域（已关停）"},
	}, "system", "")
	if err != nil {
		t.Fatal(err)
	}
	if summary.Added != 2 || summary.Updated != 2 || summary.Active != 1 || summary.Retired != 3 || summary.Excluded != 0 {
		t.Fatalf("summary = %#v", summary)
	}
	regions, err := repositories.Regions().ListRegionsByConnection(ctx, "conn-a")
	if err != nil {
		t.Fatal(err)
	}
	byID := regionMap(regions)
	for _, regionID := range []string{"cn-fuzhou", "rus-west-1", "cn-retired"} {
		if got := byID[regionID]; got.Lifecycle != asset.RegionRetired {
			t.Errorf("%s lifecycle = %q, want %q", regionID, got.Lifecycle, asset.RegionRetired)
		}
	}
	if got := byID["cn-hangzhou"]; got.Lifecycle != asset.RegionActive {
		t.Errorf("cn-hangzhou lifecycle = %q, want %q", got.Lifecycle, asset.RegionActive)
	}
}

func TestRefreshRejectsIncompleteDiscoveryWithoutMutation(t *testing.T) {
	ctx := context.Background()
	repositories, service, now := regionFixture(t)
	seedRegions(t, repositories, asset.ConnectionRegion{ID: "existing", ConnectionID: "conn-a", RegionID: "cn-hangzhou", DiscoveredName: "old", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now})

	tests := []struct {
		name    string
		regions []contracts.DiscoveredRegion
	}{
		{name: "empty"},
		{name: "blank id", regions: []contracts.DiscoveredRegion{{RegionID: "  ", Name: "invalid"}}},
		{name: "duplicate", regions: []contracts.DiscoveredRegion{{RegionID: "cn-hangzhou"}, {RegionID: " cn-hangzhou "}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := service.Refresh(ctx, "conn-a", test.regions, "system", ""); !errors.Is(err, ErrInvalidDiscovery) {
				t.Fatalf("Refresh() error = %v", err)
			}
			stored, err := repositories.Regions().GetRegion(ctx, "existing")
			if err != nil || stored.DiscoveredName != "old" || !stored.UpdatedAt.Equal(now) {
				t.Fatalf("stored = %#v, err = %v", stored, err)
			}
		})
	}
}

func TestManualRegionLifecycleAndOwnership(t *testing.T) {
	ctx := context.Background()
	repositories, service, _ := regionFixture(t)
	created, err := service.Add(ctx, "conn-a", " cn-hangzhou ", " 杭州 ", "admin")
	if err != nil || created.RegionID != "cn-hangzhou" || created.NameOverride != "杭州" || created.Origin != asset.RegionOriginManual || created.Lifecycle != asset.RegionActive {
		t.Fatalf("Add() = %#v, %v", created, err)
	}
	if _, err := service.Add(ctx, "conn-a", "cn-empty-name", "  ", "admin"); !errors.Is(err, ErrRegionNameEmpty) {
		t.Fatalf("empty-name Add() error = %v", err)
	}
	if _, err := service.Add(ctx, "conn-a", "cn-hangzhou", "duplicate", "admin"); !errors.Is(err, ErrRegionConflict) {
		t.Fatalf("duplicate Add() error = %v", err)
	}
	renamed, err := service.Rename(ctx, "conn-a", "cn-hangzhou", " 华东生产 ", "admin")
	if err != nil || renamed.EffectiveName() != "华东生产" {
		t.Fatalf("Rename() = %#v, %v", renamed, err)
	}
	reset, err := service.ResetName(ctx, "conn-a", "cn-hangzhou", "admin")
	if err != nil || reset.NameOverride != "" || reset.EffectiveName() != "cn-hangzhou" {
		t.Fatalf("ResetName() = %#v, %v", reset, err)
	}
	retired, err := service.Retire(ctx, "conn-a", "cn-hangzhou", "admin")
	if err != nil || retired.Lifecycle != asset.RegionRetired {
		t.Fatalf("Retire() = %#v, %v", retired, err)
	}
	active, err := service.Activate(ctx, "conn-a", "cn-hangzhou", "admin")
	if err != nil || active.Lifecycle != asset.RegionActive {
		t.Fatalf("Activate() = %#v, %v", active, err)
	}
	excluded, err := service.Exclude(ctx, "conn-a", "cn-hangzhou", "admin")
	if err != nil || excluded.Lifecycle != asset.RegionExcluded {
		t.Fatalf("Exclude() = %#v, %v", excluded, err)
	}
	if _, err := service.Add(ctx, "conn-a", "cn-hangzhou", "duplicate", "admin"); !errors.Is(err, ErrRegionConflict) {
		t.Fatalf("excluded duplicate Add() error = %v", err)
	}
	restored, err := service.Restore(ctx, "conn-a", "cn-hangzhou", "admin")
	if err != nil || restored.Lifecycle != asset.RegionActive {
		t.Fatalf("Restore() = %#v, %v", restored, err)
	}
	if _, err := service.Retire(ctx, "conn-b", "cn-hangzhou", "admin"); !errors.Is(err, ErrRegionNotFound) {
		t.Fatalf("cross-connection operation error = %v", err)
	}
	summary, err := service.Summary(ctx, "conn-a")
	if err != nil || summary.Active != 1 || summary.Retired != 0 || summary.Excluded != 0 {
		t.Fatalf("Summary() = %#v, %v", summary, err)
	}
	audits, err := repositories.Audits().ListAuditEvents(ctx, persistence.ListOptions{ConnectionID: "conn-a", Limit: 20})
	if err != nil || len(audits.Items) != 7 {
		t.Fatalf("audits = %#v, err = %v", audits, err)
	}
}

func TestRefreshRollsBackRegionChangesWhenAuditFails(t *testing.T) {
	ctx := context.Background()
	repositories, service, now := regionFixture(t)
	seedRegions(t, repositories, asset.ConnectionRegion{ID: "existing", ConnectionID: "conn-a", RegionID: "cn-hangzhou", DiscoveredName: "old", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now})
	if err := repositories.Audits().AppendAuditEvent(ctx, execution.AuditEvent{ID: "duplicate-audit", ConnectionID: "conn-a", Actor: "test", Action: "seed", TargetType: "test", TargetID: "test", Result: "seeded", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	service.newID = func() string { return "duplicate-audit" }
	if _, err := service.Refresh(ctx, "conn-a", []contracts.DiscoveredRegion{{RegionID: "cn-hangzhou", Name: "new"}}, "system", ""); err == nil {
		t.Fatal("Refresh() succeeded despite duplicate audit ID")
	}
	stored, err := repositories.Regions().GetRegion(ctx, "existing")
	if err != nil || stored.DiscoveredName != "old" || !stored.UpdatedAt.Equal(now) {
		t.Fatalf("stored after rollback = %#v, err = %v", stored, err)
	}
}

func TestRegionServiceRejectsUnverifiedConnection(t *testing.T) {
	ctx := context.Background()
	repositories, service, _ := regionFixture(t)
	connection, err := repositories.Connections().GetConnection(ctx, "conn-a")
	if err != nil {
		t.Fatal(err)
	}
	connection.Status = asset.ConnectionUnverified
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}

	if _, err := service.List(ctx, persistence.RegionListOptions{ConnectionID: connection.ID, Limit: 10}); !errors.Is(err, asset.ErrConnectionNotValidated) {
		t.Fatalf("List() error = %v", err)
	}
	if _, err := service.Add(ctx, connection.ID, "cn-hangzhou", "杭州", "admin"); !errors.Is(err, asset.ErrConnectionNotValidated) {
		t.Fatalf("Add() error = %v", err)
	}
	if _, err := service.Refresh(ctx, connection.ID, []contracts.DiscoveredRegion{{RegionID: "cn-hangzhou"}}, "system", "request"); !errors.Is(err, asset.ErrConnectionNotValidated) {
		t.Fatalf("Refresh() error = %v", err)
	}
	regions, err := repositories.Regions().ListRegionsByConnection(ctx, connection.ID)
	if err != nil || len(regions) != 0 {
		t.Fatalf("regions = %#v, err = %v", regions, err)
	}
}

func regionFixture(t *testing.T) (persistence.Repositories, *Service, time.Time) {
	t.Helper()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "regions.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 6, 0, 0, 0, time.UTC)
	for _, connection := range []asset.CloudConnection{
		{ID: "conn-a", Name: "a", Provider: asset.ProviderAliCloud, Partition: "public", Principal: "a", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now},
		{ID: "conn-b", Name: "b", Provider: asset.ProviderAWS, Partition: "aws", Principal: "b", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Connections().PutConnection(context.Background(), connection); err != nil {
			t.Fatal(err)
		}
	}
	service, err := NewService(repositories)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	return repositories, service, now
}

func seedRegions(t *testing.T, repositories persistence.Repositories, regions ...asset.ConnectionRegion) {
	t.Helper()
	for _, value := range regions {
		if err := repositories.Regions().PutRegion(context.Background(), value); err != nil {
			t.Fatal(err)
		}
	}
}
