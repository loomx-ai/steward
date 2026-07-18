package contract

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/finding"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/persistence"
)

type Factory func(t *testing.T) persistence.Repositories

func Run(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("connection site compatibility", func(t *testing.T) {
		repositories := factory(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 21, 3, 0, 0, 0, time.UTC)
		for _, connection := range []asset.CloudConnection{
			{ID: "site-alicloud", Name: "Alibaba Cloud", Provider: asset.ProviderAliCloud, Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now},
			{ID: "site-aws", Name: "AWS", Provider: asset.ProviderAWS, Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now},
		} {
			if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
				t.Fatal(err)
			}
		}
		alicloud, err := repositories.Connections().GetConnection(ctx, "site-alicloud")
		if err != nil {
			t.Fatal(err)
		}
		if alicloud.Site != asset.ConnectionSiteCN {
			t.Fatalf("legacy Alibaba Cloud site = %q, want %q", alicloud.Site, asset.ConnectionSiteCN)
		}
		aws, err := repositories.Connections().GetConnection(ctx, "site-aws")
		if err != nil {
			t.Fatal(err)
		}
		if aws.Site != "" {
			t.Fatalf("AWS site = %q, want empty", aws.Site)
		}
	})
	t.Run("natural entity identity", func(t *testing.T) {
		repositories := factory(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 21, 4, 0, 0, 0, time.UTC)
		connection := asset.CloudConnection{ID: "con-23456789abcdefgh", Name: "identity", Provider: asset.ProviderAliCloud, Partition: "public", Principal: "identity", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}
		if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
			t.Fatal(err)
		}
		scope := asset.Scope{ID: "scp-23456789abcdefgh", ConnectionID: connection.ID, Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Name: "杭州", CreatedAt: now, UpdatedAt: now}
		if err := repositories.Inventory().PutScope(ctx, scope); err != nil {
			t.Fatal(err)
		}
		storedScope, err := repositories.Inventory().GetScopeByNaturalKey(ctx, connection.ID, scope.Kind, scope.NativeID)
		if err != nil || storedScope.ID != scope.ID {
			t.Fatalf("GetScopeByNaturalKey = %#v, %v", storedScope, err)
		}
		identity, err := asset.NewIdentity(string(connection.Provider), connection.Partition, connection.ID, "ALIYUN::ECS::Instance", "i-example")
		if err != nil {
			t.Fatal(err)
		}
		value := asset.Asset{ID: "ast-23456789abcdefgh", Identity: identity, ResourceKindID: "alicloud:ALIYUN::ECS::Instance", FirstSeenAt: now, LastSeenAt: now}
		if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
			t.Fatal(err)
		}
		storedAsset, err := repositories.Inventory().GetAssetByIdentity(ctx, identity)
		if err != nil || storedAsset.ID != value.ID {
			t.Fatalf("GetAssetByIdentity = %#v, %v", storedAsset, err)
		}

		for _, scoped := range []asset.Asset{
			{
				ID: "asset-scoped-hangzhou",
				Identity: asset.Identity{
					Provider: connection.Provider, Partition: connection.Partition,
					ConnectionID: connection.ID, NativeType: "ACS::ECS::KeyPair",
					NativeID: "shared-name", ScopeKey: "region:cn-hangzhou",
				},
				ScopeID: "scope-scoped-hangzhou", ResourceKindID: "alicloud:ACS::ECS::KeyPair",
				FirstSeenAt: now, LastSeenAt: now,
			},
			{
				ID: "asset-scoped-shanghai",
				Identity: asset.Identity{
					Provider: connection.Provider, Partition: connection.Partition,
					ConnectionID: connection.ID, NativeType: "ACS::ECS::KeyPair",
					NativeID: "shared-name", ScopeKey: "region:cn-shanghai",
				},
				ScopeID: "scope-scoped-shanghai", ResourceKindID: "alicloud:ACS::ECS::KeyPair",
				FirstSeenAt: now, LastSeenAt: now,
			},
		} {
			if err := repositories.Inventory().PutAsset(ctx, scoped); err != nil {
				t.Fatal(err)
			}
			stored, err := repositories.Inventory().GetAssetByIdentity(ctx, scoped.Identity)
			if err != nil || stored.ID != scoped.ID || stored.Identity.ScopeKey != scoped.Identity.ScopeKey {
				t.Fatalf("scoped GetAssetByIdentity = %#v, %v", stored, err)
			}
		}
	})
	t.Run("connection regions", func(t *testing.T) {
		repositories := factory(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 21, 5, 0, 0, 0, time.UTC)
		for _, connection := range []asset.CloudConnection{
			{ID: "region-conn-a", Name: "a", Provider: asset.ProviderAliCloud, Partition: "public", Principal: "a", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now},
			{ID: "region-conn-b", Name: "b", Provider: asset.ProviderAWS, Partition: "aws", Principal: "b", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now},
		} {
			if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
				t.Fatal(err)
			}
		}

		firstSeenAt := now.Add(-time.Hour)
		regions := []asset.ConnectionRegion{
			{ID: "region-shanghai", ConnectionID: "region-conn-a", RegionID: "cn-shanghai", DiscoveredName: "华东 2", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, FirstSeenAt: &firstSeenAt, LastSeenAt: &now, CreatedAt: now, UpdatedAt: now},
			{ID: "region-hangzhou", ConnectionID: "region-conn-a", RegionID: "cn-hangzhou", DiscoveredName: "华东 1", NameOverride: "杭州", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, FirstSeenAt: &firstSeenAt, LastSeenAt: &now, CreatedAt: now, UpdatedAt: now},
			{ID: "region-retired", ConnectionID: "region-conn-a", RegionID: "cn-qingdao", DiscoveredName: "华北 1", Origin: asset.RegionOriginManual, Lifecycle: asset.RegionRetired, CreatedAt: now, UpdatedAt: now},
			{ID: "region-other-connection", ConnectionID: "region-conn-b", RegionID: "cn-hangzhou", DiscoveredName: "Hangzhou", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
		}
		for _, region := range regions {
			if err := repositories.Regions().PutRegion(ctx, region); err != nil {
				t.Fatalf("PutRegion(%s): %v", region.ID, err)
			}
		}

		stored, err := repositories.Regions().GetRegion(ctx, "region-hangzhou")
		if err != nil || stored.EffectiveName() != "杭州" || stored.RegionID != "cn-hangzhou" {
			t.Fatalf("GetRegion = %#v, err = %v", stored, err)
		}
		all, err := repositories.Regions().ListRegionsByConnection(ctx, "region-conn-a")
		if err != nil || len(all) != 3 || all[0].RegionID != "cn-hangzhou" || all[1].RegionID != "cn-qingdao" || all[2].RegionID != "cn-shanghai" {
			t.Fatalf("ListRegionsByConnection = %#v, err = %v", all, err)
		}
		active, err := repositories.Regions().ListRegions(ctx, persistence.RegionListOptions{ConnectionID: "region-conn-a", Lifecycle: asset.RegionActive, Query: "hangzhou", Limit: 10})
		if err != nil || len(active.Items) != 1 || active.Items[0].ID != "region-hangzhou" {
			t.Fatalf("active search = %#v, err = %v", active, err)
		}
		localized, err := repositories.Regions().ListRegions(ctx, persistence.RegionListOptions{ConnectionID: "region-conn-a", Query: "华东 2", Limit: 10})
		if err != nil || len(localized.Items) != 1 || localized.Items[0].ID != "region-shanghai" {
			t.Fatalf("localized search = %#v, err = %v", localized, err)
		}
		retired, err := repositories.Regions().ListRegions(ctx, persistence.RegionListOptions{ConnectionID: "region-conn-a", Lifecycle: asset.RegionRetired, Limit: 10})
		if err != nil || len(retired.Items) != 1 || retired.Items[0].ID != "region-retired" {
			t.Fatalf("retired regions = %#v, err = %v", retired, err)
		}
		counts, err := repositories.Regions().CountRegionsByLifecycle(ctx, "region-conn-a")
		if err != nil || counts[asset.RegionActive] != 2 || counts[asset.RegionRetired] != 1 || counts[asset.RegionExcluded] != 0 {
			t.Fatalf("region lifecycle counts = %#v, err = %v", counts, err)
		}
		firstPage, err := repositories.Regions().ListRegions(ctx, persistence.RegionListOptions{ConnectionID: "region-conn-a", Limit: 1})
		if err != nil || len(firstPage.Items) != 1 || firstPage.Items[0].RegionID != "cn-hangzhou" || firstPage.NextCursor == "" {
			t.Fatalf("first region page = %#v, err = %v", firstPage, err)
		}
		secondPage, err := repositories.Regions().ListRegions(ctx, persistence.RegionListOptions{ConnectionID: "region-conn-a", Cursor: firstPage.NextCursor, Limit: 1})
		if err != nil || len(secondPage.Items) != 1 || secondPage.Items[0].RegionID != "cn-qingdao" || secondPage.NextCursor == "" {
			t.Fatalf("second region page = %#v, err = %v", secondPage, err)
		}
		activeConnectionPage, err := repositories.Regions().ListActiveConnectionRegions(ctx, persistence.RegionListOptions{
			ConnectionID: "region-conn-a", Lifecycle: asset.RegionActive, Limit: 1, Cursor: "ignored",
		})
		if err != nil || len(activeConnectionPage.Items) != 2 ||
			activeConnectionPage.Items[0].RegionID != "cn-hangzhou" ||
			activeConnectionPage.Items[1].RegionID != "cn-shanghai" ||
			activeConnectionPage.NextCursor != "" {
			t.Fatalf("active connection regions = %#v, err = %v", activeConnectionPage, err)
		}
		if _, err := repositories.Regions().ListActiveConnectionRegions(ctx, persistence.RegionListOptions{
			ConnectionID: "missing", Limit: 10,
		}); !errors.Is(err, persistence.ErrNotFound) {
			t.Fatalf("missing connection regions err = %v", err)
		}

		stale := stored
		adminUpdate := stored
		adminUpdate.Lifecycle = asset.RegionExcluded
		adminUpdate.UpdatedAt = now.Add(30 * time.Second)
		if err := repositories.Regions().PutRegionIfUnchanged(ctx, adminUpdate, stored.Revision); err != nil {
			t.Fatalf("current compare-and-swap update: %v", err)
		}
		stale.DiscoveredName = "stale discovery"
		stale.UpdatedAt = now.Add(45 * time.Second)
		if err := repositories.Regions().PutRegionIfUnchanged(ctx, stale, stale.Revision); !errors.Is(err, persistence.ErrConflict) {
			t.Fatalf("stale compare-and-swap error = %v", err)
		}
		stored, err = repositories.Regions().GetRegion(ctx, stored.ID)
		if err != nil || stored.Lifecycle != asset.RegionExcluded || stored.DiscoveredName == "stale discovery" || stored.Revision != stale.Revision+1 {
			t.Fatalf("region after stale update = %#v, err = %v", stored, err)
		}

		stored.NameOverride = "  新名称  "
		stored.UpdatedAt = now.Add(time.Minute)
		if err := repositories.Regions().PutRegion(ctx, stored); err != nil {
			t.Fatal(err)
		}
		updated, err := repositories.Regions().GetRegion(ctx, stored.ID)
		if err != nil || updated.NameOverride != "新名称" {
			t.Fatalf("updated region = %#v, err = %v", updated, err)
		}
		duplicate := stored
		duplicate.ID = "duplicate-region"
		if err := repositories.Regions().PutRegion(ctx, duplicate); !errors.Is(err, persistence.ErrConflict) {
			t.Fatalf("duplicate region err = %v", err)
		}
		stored.RegionID = "cn-beijing"
		if err := repositories.Regions().PutRegion(ctx, stored); !errors.Is(err, persistence.ErrConflict) {
			t.Fatalf("mutable region ID err = %v", err)
		}
		invalid := regions[0]
		invalid.ID = "empty-region-id"
		invalid.RegionID = "  "
		if err := repositories.Regions().PutRegion(ctx, invalid); err == nil {
			t.Fatal("empty region ID was accepted")
		}

		rollback := errors.New("rollback region")
		err = repositories.WithTx(ctx, func(tx persistence.Repositories) error {
			if err := tx.Regions().PutRegion(ctx, asset.ConnectionRegion{ID: "rolled-back-region", ConnectionID: "region-conn-a", RegionID: "cn-chengdu", Origin: asset.RegionOriginManual, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
				return err
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("rollback error = %v", err)
		}
		if _, err := repositories.Regions().GetRegion(ctx, "rolled-back-region"); !errors.Is(err, persistence.ErrNotFound) {
			t.Fatalf("rolled back region err = %v", err)
		}
	})

	t.Run("transaction rollback and stable cursor", func(t *testing.T) {
		repositories := factory(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 13, 4, 0, 0, 0, time.UTC)
		rollback := errors.New("rollback")
		err := repositories.WithTx(ctx, func(tx persistence.Repositories) error {
			if err := tx.Connections().PutConnection(ctx, asset.CloudConnection{ID: "rolled-back", Name: "test", Provider: asset.ProviderAWS, Partition: "aws", Principal: "test", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now}); err != nil {
				return err
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("rollback error = %v", err)
		}
		if _, err := repositories.Connections().GetConnection(ctx, "rolled-back"); !errors.Is(err, persistence.ErrNotFound) {
			t.Fatalf("rolled back connection err = %v", err)
		}

		connections := []asset.CloudConnection{
			{ID: "conn-a", Name: "a", Provider: asset.ProviderAliCloud, Partition: "public", Principal: "a", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now},
			{ID: "conn-b", Name: "b", Provider: asset.ProviderAWS, Partition: "aws", Principal: "b", Status: asset.ConnectionActive, CreatedAt: now.Add(time.Second), UpdatedAt: now.Add(time.Second)},
		}
		for _, connection := range connections {
			if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
				t.Fatal(err)
			}
		}
		first, err := repositories.Connections().ListConnections(ctx, persistence.ListOptions{Limit: 1})
		if err != nil || len(first.Items) != 1 || first.Items[0].ID != "conn-a" || first.NextCursor == "" {
			t.Fatalf("first page = %#v, err = %v", first, err)
		}
		second, err := repositories.Connections().ListConnections(ctx, persistence.ListOptions{Limit: 1, Cursor: first.NextCursor})
		if err != nil || len(second.Items) != 1 || second.Items[0].ID != "conn-b" || second.NextCursor != "" {
			t.Fatalf("second page = %#v, err = %v", second, err)
		}
		alicloudConnections, err := repositories.Connections().ListConnections(ctx, persistence.ListOptions{Limit: 1, Provider: "alicloud"})
		if err != nil || len(alicloudConnections.Items) != 1 || alicloudConnections.Items[0].ID != "conn-a" || alicloudConnections.NextCursor != "" {
			t.Fatalf("filtered connections = %#v, err = %v", alicloudConnections, err)
		}
		versionCandidate := connections[1]
		versionCandidate.Name = "updated"
		versionCandidate.UpdatedAt = now.Add(time.Minute)
		if err := repositories.Connections().PutConnectionIfUnchanged(ctx, versionCandidate, now); !errors.Is(err, persistence.ErrConflict) {
			t.Fatalf("stale connection compare-and-swap error = %v", err)
		}
		if err := repositories.Connections().PutConnectionIfUnchanged(ctx, versionCandidate, connections[1].UpdatedAt); err != nil {
			t.Fatalf("current connection compare-and-swap update: %v", err)
		}
		credential := asset.ConnectionCredential{
			ConnectionID: "conn-a", Provider: asset.ProviderAliCloud, Type: asset.CredentialAliCloudAccessKey,
			EnvelopeVersion: 1, Nonce: "nonce", Ciphertext: "ciphertext", CreatedAt: now, UpdatedAt: now,
		}
		if err := repositories.Credentials().PutCredential(ctx, credential); err != nil {
			t.Fatal(err)
		}
		storedCredential, err := repositories.Credentials().GetCredential(ctx, "conn-a")
		if err != nil || storedCredential.Ciphertext != "ciphertext" {
			t.Fatalf("credential = %#v, err = %v", storedCredential, err)
		}
		connectionAggregates, err := repositories.Connections().ListConnectionAggregates(ctx, persistence.ListOptions{
			Limit: 1, Provider: "alicloud",
		})
		if err != nil || len(connectionAggregates.Items) != 1 ||
			connectionAggregates.Items[0].Connection.ID != "conn-a" ||
			connectionAggregates.Items[0].Credential.Ciphertext != "ciphertext" {
			t.Fatalf("connection aggregates = %#v, err = %v", connectionAggregates, err)
		}
		staleCredential := storedCredential
		storedCredential.Ciphertext = "rotated-ciphertext"
		if err := repositories.Credentials().PutCredential(ctx, storedCredential); err != nil {
			t.Fatal(err)
		}
		candidate := connections[0]
		candidate.Status = asset.ConnectionInvalid
		candidate.UpdatedAt = now.Add(time.Minute)
		if err := repositories.Connections().PutConnectionIfCredentialUnchanged(ctx, candidate, connections[0].UpdatedAt, staleCredential); !errors.Is(err, persistence.ErrConflict) {
			t.Fatalf("stale credential compare-and-swap error = %v", err)
		}
		storedConnection, err := repositories.Connections().GetConnection(ctx, candidate.ID)
		if err != nil || storedConnection.Status != asset.ConnectionActive {
			t.Fatalf("stale credential changed connection = %#v, err = %v", storedConnection, err)
		}
		if err := repositories.Connections().PutConnectionIfCredentialUnchanged(ctx, candidate, connections[0].UpdatedAt, storedCredential); err != nil {
			t.Fatalf("current credential compare-and-swap update: %v", err)
		}
		if err := repositories.Credentials().DeleteCredential(ctx, "conn-a"); err != nil {
			t.Fatal(err)
		}
		if _, err := repositories.Credentials().GetCredential(ctx, "conn-a"); !errors.Is(err, persistence.ErrNotFound) {
			t.Fatalf("deleted credential err = %v", err)
		}
	})

	t.Run("credential compare and swap requires unchanged active connection", func(t *testing.T) {
		repositories := factory(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 27, 14, 0, 0, 0, time.UTC)
		connection := asset.CloudConnection{
			ID: "credential-cas", Name: "credential CAS", Provider: asset.ProviderAliCloud, Site: asset.ConnectionSiteCN,
			Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
		}
		if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
			t.Fatal(err)
		}
		original := asset.ConnectionCredential{
			ConnectionID: connection.ID, Provider: connection.Provider, Type: asset.CredentialAliCloudAccessKey,
			EnvelopeVersion: 1, Nonce: "nonce-original", Ciphertext: "ciphertext-original", CreatedAt: now, UpdatedAt: now,
		}
		if err := repositories.Credentials().PutCredential(ctx, original); err != nil {
			t.Fatal(err)
		}
		refreshed := original
		refreshed.Nonce = "nonce-refreshed"
		refreshed.Ciphertext = "ciphertext-refreshed"
		refreshed.UpdatedAt = now.Add(time.Minute)
		if err := repositories.Credentials().PutCredentialIfConnectionUnchanged(
			ctx, refreshed, connection.UpdatedAt, original,
		); err != nil {
			t.Fatalf("current credential compare and swap: %v", err)
		}

		userReplacement := refreshed
		userReplacement.Nonce = "nonce-user"
		userReplacement.Ciphertext = "ciphertext-user"
		userReplacement.UpdatedAt = now.Add(2 * time.Minute)
		if err := repositories.Credentials().PutCredential(ctx, userReplacement); err != nil {
			t.Fatal(err)
		}
		staleRefresh := refreshed
		staleRefresh.Nonce = "nonce-stale"
		staleRefresh.Ciphertext = "ciphertext-stale"
		staleRefresh.UpdatedAt = now.Add(3 * time.Minute)
		if err := repositories.Credentials().PutCredentialIfConnectionUnchanged(
			ctx, staleRefresh, connection.UpdatedAt, refreshed,
		); !errors.Is(err, persistence.ErrConflict) {
			t.Fatalf("stale credential CAS error = %v", err)
		}
		stored, err := repositories.Credentials().GetCredential(ctx, connection.ID)
		if err != nil || stored.Ciphertext != userReplacement.Ciphertext {
			t.Fatalf("credential after conflict = %#v, err = %v", stored, err)
		}

		connection.Status = asset.ConnectionUnverified
		connection.UpdatedAt = now.Add(4 * time.Minute)
		if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Credentials().PutCredentialIfConnectionUnchanged(
			ctx, staleRefresh, now, userReplacement,
		); !errors.Is(err, persistence.ErrConflict) {
			t.Fatalf("changed connection CAS error = %v", err)
		}
		stored, err = repositories.Credentials().GetCredential(ctx, connection.ID)
		if err != nil || stored.Ciphertext != userReplacement.Ciphertext {
			t.Fatalf("credential after connection conflict = %#v, err = %v", stored, err)
		}
	})

	t.Run("immutable inventory and graph", func(t *testing.T) {
		repositories := factory(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 13, 4, 1, 0, 0, time.UTC)
		if err := repositories.Inventory().PutScope(ctx, asset.Scope{ID: "scope-global", ConnectionID: "conn-a", Kind: asset.ScopeGlobal, NativeID: "global", Name: "Global", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		storedScope, err := repositories.Inventory().GetScope(ctx, "scope-global")
		if err != nil || storedScope.ConnectionID != "conn-a" {
			t.Fatalf("scope = %#v, err = %v", storedScope, err)
		}
		scopes, err := repositories.Inventory().ListScopes(ctx, persistence.ListOptions{Limit: 10})
		if err != nil || len(scopes.Items) != 1 || scopes.Items[0].ID != "scope-global" {
			t.Fatalf("scopes = %#v, err = %v", scopes, err)
		}
		connectionScopes, err := repositories.Inventory().ListScopesByConnection(ctx, "conn-a")
		if err != nil || len(connectionScopes) != 1 || connectionScopes[0].ID != "scope-global" {
			t.Fatalf("connection scopes = %#v, err = %v", connectionScopes, err)
		}
		if err := repositories.Inventory().PutResourceKind(ctx, asset.ResourceKind{ID: "kind-ecs", Provider: asset.ProviderAliCloud, NativeType: "ACS::ECS::Instance", Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, DisplayName: "ECS", BundleRevision: "bundle-1"}); err != nil {
			t.Fatal(err)
		}
		storedKind, err := repositories.Inventory().GetResourceKind(ctx, "kind-ecs")
		if err != nil || storedKind.NativeType != "ACS::ECS::Instance" {
			t.Fatalf("resource kind = %#v, err = %v", storedKind, err)
		}
		if err := repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{ID: "scan-1", ConnectionID: "conn-a", Status: asset.ScanRunning, RequestedBy: "tester", CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory().PutScanShard(ctx, asset.ScanShard{ID: "shard-1", ScanRunID: "scan-1", Provider: asset.ProviderAliCloud, Source: "resource-center", ScopeID: "scope-global", ResourceKindID: "kind-ecs", Authoritative: true, Status: asset.ShardRunning, Coverage: asset.Coverage{ItemCount: 3}, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		storedRun, err := repositories.Inventory().GetScanRun(ctx, "scan-1")
		if err != nil || storedRun.RequestedBy != "tester" {
			t.Fatalf("scan run = %#v, err = %v", storedRun, err)
		}
		for _, run := range []asset.ScanRun{
			{ID: "scan-2", ConnectionID: "conn-a", Status: asset.ScanSucceeded, RequestedBy: "tester", CreatedAt: now.Add(time.Minute)},
			{ID: "scan-3", ConnectionID: "conn-a", Status: asset.ScanSucceeded, RequestedBy: "tester", CreatedAt: now.Add(2 * time.Minute)},
		} {
			if err := repositories.Inventory().CreateScanRun(ctx, run); err != nil {
				t.Fatal(err)
			}
		}
		runs, err := repositories.Inventory().ListScanRuns(ctx, persistence.ListOptions{Limit: 1})
		if err != nil || len(runs.Items) != 1 || runs.Items[0].ID != "scan-3" || runs.NextCursor == "" {
			t.Fatalf("newest scan runs = %#v, err = %v", runs, err)
		}
		runs, err = repositories.Inventory().ListScanRuns(ctx, persistence.ListOptions{Limit: 1, Cursor: runs.NextCursor})
		if err != nil || len(runs.Items) != 1 || runs.Items[0].ID != "scan-2" || runs.NextCursor == "" {
			t.Fatalf("middle scan runs = %#v, err = %v", runs, err)
		}
		runs, err = repositories.Inventory().ListScanRuns(ctx, persistence.ListOptions{Limit: 1, Cursor: runs.NextCursor})
		if err != nil || len(runs.Items) != 1 || runs.Items[0].ID != "scan-1" || runs.NextCursor != "" {
			t.Fatalf("oldest scan runs = %#v, err = %v", runs, err)
		}
		foreignRuns, err := repositories.Inventory().ListScanRuns(ctx, persistence.ListOptions{Limit: 10, ConnectionID: "conn-b"})
		if err != nil || len(foreignRuns.Items) != 0 {
			t.Fatalf("foreign scan runs = %#v, err = %v", foreignRuns, err)
		}
		connectionRuns, err := repositories.Inventory().ListScanRunsByConnection(ctx, "conn-a")
		if err != nil || len(connectionRuns) != 3 || connectionRuns[0].ID != "scan-3" {
			t.Fatalf("connection scan runs = %#v, err = %v", connectionRuns, err)
		}
		storedShard, err := repositories.Inventory().GetScanShard(ctx, "shard-1")
		if err != nil || !storedShard.Authoritative {
			t.Fatalf("scan shard = %#v, err = %v", storedShard, err)
		}
		shards, err := repositories.Inventory().ListScanShards(ctx, persistence.ListOptions{Limit: 10})
		if err != nil || len(shards.Items) != 1 || shards.Items[0].ID != "shard-1" {
			t.Fatalf("scan shards = %#v, err = %v", shards, err)
		}
		runShards, err := repositories.Inventory().ListScanShardsByRun(ctx, "scan-1")
		if err != nil || len(runShards) != 1 || runShards[0].ID != "shard-1" {
			t.Fatalf("scan run shards = %#v, err = %v", runShards, err)
		}
		listItems, err := repositories.Inventory().ListScanRunListItems(ctx, persistence.ListOptions{Limit: 10, ConnectionID: "conn-a"})
		if err != nil || len(listItems.Items) != 3 ||
			listItems.Items[0].ScanRun.ID != "scan-3" ||
			listItems.Items[2].ScanRun.ID != "scan-1" ||
			listItems.Items[2].ResourceCount != 3 {
			t.Fatalf("scan run list items = %#v, err = %v", listItems, err)
		}
		listItems, err = repositories.Inventory().ListScanRunListItems(ctx, persistence.ListOptions{Limit: 1, ConnectionID: "conn-a"})
		if err != nil || len(listItems.Items) != 1 || listItems.Items[0].ScanRun.ID != "scan-3" || listItems.NextCursor == "" {
			t.Fatalf("newest scan run list item = %#v, err = %v", listItems, err)
		}
		listItems, err = repositories.Inventory().ListScanRunListItems(ctx, persistence.ListOptions{Limit: 1, ConnectionID: "conn-a", Cursor: listItems.NextCursor})
		if err != nil || len(listItems.Items) != 1 || listItems.Items[0].ScanRun.ID != "scan-2" || listItems.NextCursor == "" {
			t.Fatalf("middle scan run list item = %#v, err = %v", listItems, err)
		}
		listItems, err = repositories.Inventory().ListScanRunListItems(ctx, persistence.ListOptions{Limit: 1, ConnectionID: "conn-a", Cursor: listItems.NextCursor})
		if err != nil || len(listItems.Items) != 1 || listItems.Items[0].ScanRun.ID != "scan-1" || listItems.Items[0].ResourceCount != 3 || listItems.NextCursor != "" {
			t.Fatalf("oldest scan run list item = %#v, err = %v", listItems, err)
		}
		identity, err := asset.NewIdentity("alicloud", "public", "conn-a", "ACS::ECS::Instance", "i-1")
		if err != nil {
			t.Fatal(err)
		}
		storedAsset := asset.Asset{ID: "asset-1", Identity: identity, ScopeID: "scope-global", ResourceKindID: "kind-ecs", CurrentObservationID: "obs-1", Name: "node", Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, FirstSeenAt: now, LastSeenAt: now}
		if err := repositories.Inventory().PutAsset(ctx, storedAsset); err != nil {
			t.Fatal(err)
		}
		closedIdentity, err := asset.NewIdentity("alicloud", "public", "conn-a", "ACS::ECS::Instance", "i-closed")
		if err != nil {
			t.Fatal(err)
		}
		closedAt := now.Add(time.Minute)
		closedAsset := asset.Asset{
			ID: "asset-closed", Identity: closedIdentity, ScopeID: "scope-global", ResourceKindID: "kind-ecs",
			Name: "closed node", Capabilities: asset.CapabilitySet{asset.CapabilityIndexed},
			FirstSeenAt: now, LastSeenAt: now, ClosedAt: &closedAt, DeletedAt: &closedAt,
		}
		if err := repositories.Inventory().PutAsset(ctx, closedAsset); err != nil {
			t.Fatal(err)
		}
		assetsByID, err := repositories.Inventory().ListAssetsByIDs(
			ctx,
			[]asset.AssetID{"asset-1", "asset-missing", "asset-1"},
		)
		if err != nil || len(assetsByID) != 1 || assetsByID[0].ID != storedAsset.ID {
			t.Fatalf("assets by IDs = %#v, err = %v", assetsByID, err)
		}
		scopedAssets, err := repositories.Inventory().ListActiveAssetsByScopes(
			ctx,
			"conn-a",
			[]asset.ScopeID{"scope-global"},
			"",
		)
		if err != nil || len(scopedAssets) != 1 || scopedAssets[0].ID != storedAsset.ID {
			t.Fatalf("scoped active assets = %#v, err = %v", scopedAssets, err)
		}
		scopeCounts, err := repositories.Inventory().CountActiveAssetsByScope(ctx, "conn-a", nil)
		if err != nil || scopeCounts["scope-global"] != 1 {
			t.Fatalf("active asset counts = %#v, err = %v", scopeCounts, err)
		}
		assets, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10, ConnectionID: "conn-a"})
		if err != nil || len(assets.Items) != 1 || assets.Items[0].ID != storedAsset.ID {
			t.Fatalf("connection assets = %#v, err = %v", assets, err)
		}
		globalAssets, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{
			Limit: 10, ConnectionID: "conn-a", AssetCanvas: persistence.AssetCanvasGlobal,
		})
		if err != nil || len(globalAssets.Items) != 1 || globalAssets.Items[0].ID != storedAsset.ID {
			t.Fatalf("global canvas assets = %#v, err = %v", globalAssets, err)
		}
		assetsIncludingClosed, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{
			Limit: 10, ConnectionID: "conn-a", IncludeClosed: true,
		})
		if err != nil || len(assetsIncludingClosed.Items) != 2 {
			t.Fatalf("connection assets including closed = %#v, err = %v", assetsIncludingClosed, err)
		}
		var persistedDeletedAt *time.Time
		for _, value := range assetsIncludingClosed.Items {
			if value.ID == closedAsset.ID {
				persistedDeletedAt = value.DeletedAt
				break
			}
		}
		if persistedDeletedAt == nil || !persistedDeletedAt.Equal(closedAt) {
			t.Fatalf("cleanup deletion tombstone was not persisted: %#v", assetsIncludingClosed.Items)
		}
		foreignAssets, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{Limit: 10, ConnectionID: "conn-b"})
		if err != nil || len(foreignAssets.Items) != 0 {
			t.Fatalf("foreign assets = %#v, err = %v", foreignAssets, err)
		}
		filteredAssets, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{
			Limit: 10, ConnectionID: "conn-a", Provider: "alicloud", Query: "I-1", Capability: "indexed", ResourceKindID: "kind-ecs",
		})
		if err != nil || len(filteredAssets.Items) != 1 || filteredAssets.Items[0].ID != storedAsset.ID {
			t.Fatalf("filtered assets = %#v, err = %v", filteredAssets, err)
		}
		nativeIDAssets, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{
			Limit: 10, ConnectionID: "conn-a", NativeIDs: []string{"missing", storedAsset.Identity.NativeID},
		})
		if err != nil || len(nativeIDAssets.Items) != 1 || nativeIDAssets.Items[0].ID != storedAsset.ID {
			t.Fatalf("native ID assets = %#v, err = %v", nativeIDAssets, err)
		}
		assetIDAssets, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{
			Limit: 10, ConnectionID: "conn-a", AssetIDs: []asset.AssetID{"missing", storedAsset.ID},
		})
		if err != nil || len(assetIDAssets.Items) != 1 || assetIDAssets.Items[0].ID != storedAsset.ID {
			t.Fatalf("asset ID assets = %#v, err = %v", assetIDAssets, err)
		}
		multiKindAssets, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{
			Limit: 10, ConnectionID: "conn-a", ResourceKindIDs: []asset.ResourceKindID{"kind-other", "kind-ecs"},
		})
		if err != nil || len(multiKindAssets.Items) != 1 || multiKindAssets.Items[0].ID != storedAsset.ID {
			t.Fatalf("multi-kind assets = %#v, err = %v", multiKindAssets, err)
		}
		wrongKindAssets, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{
			Limit: 10, ConnectionID: "conn-a", ResourceKindID: "kind-other",
		})
		if err != nil || len(wrongKindAssets.Items) != 0 {
			t.Fatalf("wrong-kind assets = %#v, err = %v", wrongKindAssets, err)
		}
		noAssets, err := repositories.Inventory().ListAssets(ctx, persistence.ListOptions{
			Limit: 10, ConnectionID: "conn-a", Query: "not-present",
		})
		if err != nil || len(noAssets.Items) != 0 {
			t.Fatalf("non-matching assets = %#v, err = %v", noAssets, err)
		}
		observation := asset.Observation{ID: "obs-1", AssetID: "asset-1", ScanRunID: "scan-1", ScanShardID: "shard-1", ObservedAt: now, Source: "resource-center", SchemaRevision: "schema-1", ContentHash: "hash-1", Authoritative: true}
		if err := repositories.Inventory().AppendObservation(ctx, observation); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory().AppendObservation(ctx, observation); !errors.Is(err, persistence.ErrConflict) {
			t.Fatalf("duplicate observation err = %v", err)
		}
		got, err := repositories.Inventory().GetAsset(ctx, "asset-1")
		if err != nil || got.Identity.Key() != identity.Key() {
			t.Fatalf("asset = %#v, err = %v", got, err)
		}
		marked, err := repositories.Inventory().SetAssetDirty(ctx, storedAsset.ID, true)
		if err != nil || !marked.Dirty {
			t.Fatalf("mark asset dirty = %#v, err = %v", marked, err)
		}
		// Inventory refreshes must not erase an operator quality annotation.
		storedAsset.Name = "refreshed node"
		if err := repositories.Inventory().PutAsset(ctx, storedAsset); err != nil {
			t.Fatal(err)
		}
		marked, err = repositories.Inventory().GetAsset(ctx, storedAsset.ID)
		if err != nil || !marked.Dirty || marked.Name != "refreshed node" {
			t.Fatalf("dirty asset after inventory refresh = %#v, err = %v", marked, err)
		}
		unmarked, err := repositories.Inventory().SetAssetDirty(ctx, storedAsset.ID, false)
		if err != nil || unmarked.Dirty {
			t.Fatalf("unmark asset dirty = %#v, err = %v", unmarked, err)
		}

		relationships := []graph.Relationship{{ID: "rel-1", SourceAssetID: "asset-1", TargetAssetID: "controller", Type: graph.RelationshipMemberOf, Source: "spec", Confidence: 1, GraphRevision: "graph-1", ObservedAt: now}}
		bindings := []graph.LifecycleBinding{{ID: "binding-1", ControllerAssetID: "controller", ManagedAssetID: "asset-1", Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, Confidence: 1, GraphRevision: "graph-1", ObservedAt: now}}
		if err := repositories.Graph().ReplaceGraph(ctx, "scope-global", "graph-1", relationships, bindings); err != nil {
			t.Fatal(err)
		}
		if revision, err := repositories.Graph().GetGraphRevision(ctx, "scope-global"); err != nil || revision != "graph-1" {
			t.Fatalf("graph revision = %q, err = %v", revision, err)
		}
		storedBindings, err := repositories.Graph().ListLifecycleBindings(ctx, "asset-1")
		if err != nil || len(storedBindings) != 1 || storedBindings[0].ControllerAssetID != "controller" {
			t.Fatalf("bindings = %#v, err = %v", storedBindings, err)
		}
		assetRelationships, err := repositories.Graph().ListRelationshipsForAsset(ctx, "conn-a", "asset-1")
		if err != nil || len(assetRelationships) != 1 || assetRelationships[0].ID != "rel-1" {
			t.Fatalf("asset relationships = %#v, err = %v", assetRelationships, err)
		}
		assetBindings, err := repositories.Graph().ListLifecycleBindingsForAsset(ctx, "conn-a", "asset-1")
		if err != nil || len(assetBindings) != 1 || assetBindings[0].ID != "binding-1" {
			t.Fatalf("asset lifecycle bindings = %#v, err = %v", assetBindings, err)
		}
		if _, err := repositories.Graph().ListRelationshipsForAsset(ctx, "conn-b", "asset-1"); !errors.Is(err, persistence.ErrNotFound) {
			t.Fatalf("foreign asset relationships error = %v", err)
		}
		if _, err := repositories.Graph().ListLifecycleBindingsForAsset(ctx, "conn-a", "missing"); !errors.Is(err, persistence.ErrNotFound) {
			t.Fatalf("missing asset lifecycle bindings error = %v", err)
		}
		connectionRelationships, err := repositories.Graph().ListRelationshipsByConnection(ctx, "conn-a")
		if err != nil || len(connectionRelationships) != 1 || connectionRelationships[0].ID != "rel-1" {
			t.Fatalf("connection relationships = %#v, err = %v", connectionRelationships, err)
		}
		connectionBindings, err := repositories.Graph().ListLifecycleBindingsByConnection(ctx, "conn-a")
		if err != nil || len(connectionBindings) != 1 || connectionBindings[0].ID != "binding-1" {
			t.Fatalf("connection bindings = %#v, err = %v", connectionBindings, err)
		}
		scopeRelationships, err := repositories.Graph().ListRelationshipsByScope(ctx, "scope-global")
		if err != nil || len(scopeRelationships) != 1 || scopeRelationships[0].ID != "rel-1" {
			t.Fatalf("scope relationships = %#v, err = %v", scopeRelationships, err)
		}
		scopeBindings, err := repositories.Graph().ListLifecycleBindingsByScope(ctx, "scope-global")
		if err != nil || len(scopeBindings) != 1 || scopeBindings[0].ID != "binding-1" {
			t.Fatalf("scope bindings = %#v, err = %v", scopeBindings, err)
		}
		focusedRelationships, err := repositories.Graph().ListRelationshipsByAssetIDs(
			ctx,
			[]asset.AssetID{"asset-1"},
		)
		if err != nil || len(focusedRelationships) != 1 || focusedRelationships[0].ID != "rel-1" {
			t.Fatalf("focused relationships = %#v, err = %v", focusedRelationships, err)
		}
		focusedBindings, err := repositories.Graph().ListLifecycleBindingsByAssetIDs(
			ctx,
			[]asset.AssetID{"asset-1"},
		)
		if err != nil || len(focusedBindings) != 1 || focusedBindings[0].ID != "binding-1" {
			t.Fatalf("focused bindings = %#v, err = %v", focusedBindings, err)
		}
		revisions, err := repositories.Graph().ListGraphRevisionsByConnection(ctx, "conn-a")
		if err != nil || revisions["scope-global"] != "graph-1" {
			t.Fatalf("connection graph revisions = %#v, err = %v", revisions, err)
		}
		if err := repositories.Graph().CloseAssetTopology(ctx, "asset-1", now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		storedRelationships, err := repositories.Graph().ListRelationships(ctx, "asset-1")
		if err != nil || len(storedRelationships) != 0 {
			t.Fatalf("closed asset relationships = %#v, err = %v", storedRelationships, err)
		}
		storedBindings, err = repositories.Graph().ListLifecycleBindings(ctx, "asset-1")
		if err != nil || len(storedBindings) != 0 {
			t.Fatalf("closed asset lifecycle bindings = %#v, err = %v", storedBindings, err)
		}
		if err := repositories.Graph().ReplaceGraph(ctx, "scope-global", "graph-1", relationships, bindings); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Graph().ReplaceGraph(ctx, "scope-global", "graph-1", nil, nil); err != nil {
			t.Fatal(err)
		}
		storedRelationships, err = repositories.Graph().ListRelationships(ctx, "asset-1")
		if err != nil || len(storedRelationships) != 0 {
			t.Fatalf("same-revision exact replacement relationships = %#v, err = %v", storedRelationships, err)
		}
		storedBindings, err = repositories.Graph().ListLifecycleBindings(ctx, "asset-1")
		if err != nil || len(storedBindings) != 0 {
			t.Fatalf("same-revision exact replacement bindings = %#v, err = %v", storedBindings, err)
		}
		if err := repositories.Graph().ReplaceGraph(ctx, "scope-global", "graph-1", relationships, bindings); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Graph().ReplaceGraph(ctx, "scope-global", "graph-2", nil, nil); err != nil {
			t.Fatal(err)
		}
		if revision, err := repositories.Graph().GetGraphRevision(ctx, "scope-global"); err != nil || revision != "graph-2" {
			t.Fatalf("empty graph revision = %q, err = %v", revision, err)
		}
		storedRelationships, err = repositories.Graph().ListRelationships(ctx, "asset-1")
		if err != nil || len(storedRelationships) != 0 {
			t.Fatalf("old graph revision relationships = %#v, err = %v", storedRelationships, err)
		}
		storedBindings, err = repositories.Graph().ListLifecycleBindings(ctx, "asset-1")
		if err != nil || len(storedBindings) != 0 {
			t.Fatalf("old graph revision bindings = %#v, err = %v", storedBindings, err)
		}
		staleRelationship := graph.Relationship{
			ID: "rel-closed-asset", SourceAssetID: closedAsset.ID, TargetAssetID: "controller",
			Type: graph.RelationshipMemberOf, Source: "stale-worker", Confidence: 1,
			GraphRevision: "graph-stale-closed", ObservedAt: now,
		}
		staleBinding := graph.LifecycleBinding{
			ID: "binding-closed-asset", ControllerAssetID: "controller", ManagedAssetID: closedAsset.ID,
			Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive,
			CleanupPolicy: graph.CleanupDelegate, Confidence: 1,
			GraphRevision: "graph-stale-closed", ObservedAt: now,
		}
		if err := repositories.Graph().ReplaceGraph(
			ctx,
			"scope-global",
			"graph-stale-closed",
			[]graph.Relationship{staleRelationship},
			[]graph.LifecycleBinding{staleBinding},
		); err != nil {
			t.Fatal(err)
		}
		if values, err := repositories.Graph().ListRelationships(ctx, closedAsset.ID); err != nil || len(values) != 0 {
			t.Fatalf("stale rebuild reopened deleted asset relationships = %#v, err = %v", values, err)
		}
		if values, err := repositories.Graph().ListLifecycleBindings(ctx, closedAsset.ID); err != nil || len(values) != 0 {
			t.Fatalf("stale rebuild reopened deleted asset bindings = %#v, err = %v", values, err)
		}

		if err := repositories.Inventory().PutScope(ctx, asset.Scope{ID: "scope-conflict", ConnectionID: "conn-a", Kind: asset.ScopeGlobal, NativeID: "global", Name: "Duplicate", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)}); !errors.Is(err, persistence.ErrConflict) {
			t.Fatalf("duplicate natural scope identity error = %v", err)
		}

		t.Run("legacy scope consolidation", func(t *testing.T) {
			t.Skip("new-project database baseline rejects duplicate natural scope identities")
			if err := repositories.Inventory().PutScope(ctx, asset.Scope{ID: "scope-duplicate", ConnectionID: "conn-a", Kind: asset.ScopeGlobal, NativeID: "global", Name: "Duplicate", CreatedAt: now.Add(time.Minute), UpdatedAt: now.Add(time.Minute)}); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory().PutScope(ctx, asset.Scope{ID: "scope-child", ConnectionID: "conn-a", ParentID: "scope-duplicate", Kind: asset.ScopeZone, NativeID: "zone-a", Name: "Zone", CreatedAt: now, UpdatedAt: now}); err != nil {
				t.Fatal(err)
			}
			mergeIdentity, err := asset.NewIdentity("alicloud", "public", "conn-a", "ACS::ECS::Instance", "i-merge")
			if err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory().PutAsset(ctx, asset.Asset{ID: "asset-merge", Identity: mergeIdentity, ScopeID: "scope-duplicate", ResourceKindID: "kind-ecs", FirstSeenAt: now, LastSeenAt: now}); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{ID: "scan-merge", ConnectionID: "conn-a", Status: asset.ScanSucceeded, RequestedBy: "tester", CreatedAt: now}); err != nil {
				t.Fatal(err)
			}
			mergeShard := asset.ScanShard{ID: "shard-merge", ScanRunID: "scan-merge", Provider: asset.ProviderAliCloud, Source: "resource-center", ScopeID: "scope-duplicate", Status: asset.ShardRunning, Coverage: asset.Coverage{ScopeID: "scope-duplicate"}, CreatedAt: now}
			if err := repositories.Inventory().PutScanShard(ctx, mergeShard); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Graph().ReplaceGraph(ctx, "scope-global", "graph-legacy", relationships, bindings); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory().ConsolidateScopes(ctx, "scope-global", []asset.ScopeID{"scope-duplicate"}, "scope-global"); !errors.Is(err, persistence.ErrConflict) {
				t.Fatalf("active shard consolidation error = %v", err)
			}
			if scope, err := repositories.Inventory().GetScope(ctx, "scope-duplicate"); err != nil || scope.ID != "scope-duplicate" {
				t.Fatalf("rolled-back alias scope = %#v, err = %v", scope, err)
			}
			if revision, err := repositories.Graph().GetGraphRevision(ctx, "scope-global"); err != nil || revision != "graph-legacy" {
				t.Fatalf("active shard consolidation changed graph revision = %q, err = %v", revision, err)
			}

			storedShard.Status = asset.ShardSucceeded
			if err := repositories.Inventory().PutScanShard(ctx, storedShard); err != nil {
				t.Fatal(err)
			}
			mergeShard.Status = asset.ShardSucceeded
			if err := repositories.Inventory().PutScanShard(ctx, mergeShard); err != nil {
				t.Fatal(err)
			}
			scanJob := execution.Job{ID: "scan-scope-merge", ConnectionID: "conn-a", Type: execution.JobScan, Status: execution.JobPending, Payload: map[string]any{"scan_run_id": "scan-merge", "scan_shard_id": "shard-merge"}, RunAt: now, CreatedAt: now, UpdatedAt: now}
			if err := repositories.Jobs().Enqueue(ctx, scanJob); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory().ConsolidateScopes(ctx, "scope-global", []asset.ScopeID{"scope-duplicate"}, "scope-global"); !errors.Is(err, persistence.ErrConflict) {
				t.Fatalf("terminal shard handoff consolidation error = %v", err)
			}
			claimedScan, err := repositories.Jobs().ClaimNext(ctx, "scan-worker", now, time.Minute)
			if err != nil || claimedScan.ID != scanJob.ID {
				t.Fatalf("claimed scan job = %#v, err = %v", claimedScan, err)
			}
			if err := repositories.Jobs().Complete(ctx, scanJob.ID, "scan-worker", execution.JobSucceeded, "", now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			graphJob := execution.Job{ID: "graph-scope-merge", ConnectionID: "conn-a", Type: execution.JobGraph, Status: execution.JobPending, Payload: map[string]any{"scan_run_id": "scan-merge"}, RunAt: now, CreatedAt: now, UpdatedAt: now}
			if err := repositories.Jobs().Enqueue(ctx, graphJob); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory().ConsolidateScopes(ctx, "scope-global", []asset.ScopeID{"scope-duplicate"}, "scope-global"); !errors.Is(err, persistence.ErrConflict) {
				t.Fatalf("active graph consolidation error = %v", err)
			}
			claimedGraph, err := repositories.Jobs().ClaimNext(ctx, "graph-worker", now, time.Minute)
			if err != nil || claimedGraph.ID != graphJob.ID {
				t.Fatalf("claimed graph job = %#v, err = %v", claimedGraph, err)
			}
			if err := repositories.Jobs().Complete(ctx, graphJob.ID, "graph-worker", execution.JobSucceeded, "", now.Add(time.Second)); err != nil {
				t.Fatal(err)
			}
			if err := repositories.Inventory().ConsolidateScopes(ctx, "scope-global", []asset.ScopeID{"scope-duplicate"}, "scope-global"); err != nil {
				t.Fatal(err)
			}
			if scope, err := repositories.Inventory().GetScope(ctx, "scope-duplicate"); err != nil || scope.ID != "scope-global" {
				t.Fatalf("resolved alias scope = %#v, err = %v", scope, err)
			}
			if err := repositories.Inventory().PutScope(ctx, asset.Scope{ID: "scope-duplicate", ConnectionID: "conn-a", Kind: asset.ScopeGlobal, NativeID: "global", Name: "Old writer", CreatedAt: now, UpdatedAt: now.Add(2 * time.Minute)}); err != nil {
				t.Fatal(err)
			}
			if scope, err := repositories.Inventory().GetScope(ctx, "scope-duplicate"); err != nil || scope.ID != "scope-global" {
				t.Fatalf("old writer removed scope alias = %#v, err = %v", scope, err)
			}
			allScopes, err := repositories.Inventory().ListScopesByConnectionIncludingAliases(ctx, "conn-a")
			if err != nil {
				t.Fatal(err)
			}
			aliasFound := false
			for _, scope := range allScopes {
				if scope.ID == "scope-duplicate" && scope.SupersededByID == "scope-global" {
					aliasFound = true
				}
			}
			if !aliasFound {
				t.Fatalf("persistent scope alias missing: %#v", allScopes)
			}
			mergedChild, err := repositories.Inventory().GetScope(ctx, "scope-child")
			if err != nil || mergedChild.ParentID != "scope-global" {
				t.Fatalf("merged child = %#v, err = %v", mergedChild, err)
			}
			mergedAsset, err := repositories.Inventory().GetAsset(ctx, "asset-merge")
			if err != nil || mergedAsset.ScopeID != "scope-global" {
				t.Fatalf("merged asset = %#v, err = %v", mergedAsset, err)
			}
			mergedShard, err := repositories.Inventory().GetScanShard(ctx, "shard-merge")
			if err != nil || mergedShard.ScopeID != "scope-global" || mergedShard.Coverage.ScopeID != "scope-global" {
				t.Fatalf("merged shard = %#v, err = %v", mergedShard, err)
			}
			if revision, err := repositories.Graph().GetGraphRevision(ctx, "scope-global"); !errors.Is(err, persistence.ErrNotFound) || revision != "" {
				t.Fatalf("consolidated graph revision = %q, err = %v", revision, err)
			}
			if relationships, err := repositories.Graph().ListRelationships(ctx, "asset-1"); err != nil || len(relationships) != 0 {
				t.Fatalf("consolidated relationships = %#v, err = %v", relationships, err)
			}
			if bindings, err := repositories.Graph().ListLifecycleBindings(ctx, "asset-1"); err != nil || len(bindings) != 0 {
				t.Fatalf("consolidated lifecycle bindings = %#v, err = %v", bindings, err)
			}
			if err := repositories.Graph().ReplaceGraph(ctx, "scope-duplicate", "stale-old-worker", relationships, bindings); err != nil {
				t.Fatal(err)
			}
			if relationships, err := repositories.Graph().ListRelationships(ctx, "asset-1"); err != nil || len(relationships) != 0 {
				t.Fatalf("superseded-scope relationships leaked = %#v, err = %v", relationships, err)
			}
			if bindings, err := repositories.Graph().ListLifecycleBindingsByConnection(ctx, "conn-a"); err != nil || len(bindings) != 0 {
				t.Fatalf("superseded-scope lifecycle bindings leaked = %#v, err = %v", bindings, err)
			}
			if revisions, err := repositories.Graph().ListGraphRevisionsByConnection(ctx, "conn-a"); err != nil || len(revisions) != 0 {
				t.Fatalf("superseded-scope graph revision leaked = %#v, err = %v", revisions, err)
			}
		})
	})

	t.Run("active asset counts can be constrained by resource kind", func(t *testing.T) {
		repositories := factory(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 27, 7, 0, 0, 0, time.UTC)
		connectionID := asset.ConnectionID("conn-kind-count")
		scopeID := asset.ScopeID("scope-kind-count")
		if err := repositories.Connections().PutConnection(ctx, asset.CloudConnection{
			ID: connectionID, Name: "kind count", Provider: asset.ProviderAliCloud,
			Partition: "public", Principal: "kind-count", Status: asset.ConnectionActive,
			CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory().PutScope(ctx, asset.Scope{
			ID: scopeID, ConnectionID: connectionID, Kind: asset.ScopeRegion,
			NativeID: "cn-hangzhou", Name: "杭州", CreatedAt: now, UpdatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		for _, value := range []asset.Asset{
			{
				ID: "asset-instance-count",
				Identity: asset.Identity{
					Provider: asset.ProviderAliCloud, Partition: "public",
					ConnectionID: connectionID, NativeType: "ACS::ECS::Instance", NativeID: "i-a",
				},
				ScopeID: scopeID, ResourceKindID: "alicloud:ACS::ECS::Instance",
				FirstSeenAt: now, LastSeenAt: now,
			},
			{
				ID: "asset-child-count",
				Identity: asset.Identity{
					Provider: asset.ProviderAliCloud, Partition: "public",
					ConnectionID: connectionID, NativeType: "ACS::ALB::Listener", NativeID: "listener-a",
				},
				ScopeID: scopeID, ResourceKindID: "alicloud:ACS::ALB::Listener",
				FirstSeenAt: now, LastSeenAt: now,
			},
		} {
			if err := repositories.Inventory().PutAsset(ctx, value); err != nil {
				t.Fatal(err)
			}
		}

		filtered, err := repositories.Inventory().CountActiveAssetsByScope(
			ctx,
			connectionID,
			[]asset.ResourceKindID{"alicloud:ACS::ECS::Instance"},
		)
		if err != nil || filtered[scopeID] != 1 {
			t.Fatalf("filtered active asset counts = %#v, err = %v", filtered, err)
		}
		unfiltered, err := repositories.Inventory().CountActiveAssetsByScope(ctx, connectionID, nil)
		if err != nil || unfiltered[scopeID] != 2 {
			t.Fatalf("unfiltered active asset counts = %#v, err = %v", unfiltered, err)
		}
	})

	t.Run("inventory and finding application transactions", func(t *testing.T) {
		repositories := factory(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 13, 4, 1, 30, 0, time.UTC)
		if err := repositories.Inventory().PutScope(ctx, asset.Scope{ID: "scope-app", ConnectionID: "conn-app", Kind: asset.ScopeGlobal, NativeID: "global", Name: "Global", CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory().PutResourceKind(ctx, asset.ResourceKind{ID: "kind-app", Provider: asset.ProviderAWS, NativeType: "AWS::EC2::Instance", Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "bundle-app"}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{ID: "scan-app", ConnectionID: "conn-app", Status: asset.ScanRunning, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := repositories.Inventory().PutScanShard(ctx, asset.ScanShard{ID: "shard-app", ScanRunID: "scan-app", Provider: asset.ProviderAWS, Source: "config", ScopeID: "scope-app", ResourceKindID: "kind-app", Status: asset.ShardRunning, CreatedAt: now}); err != nil {
			t.Fatal(err)
		}
		identity, err := asset.NewIdentity("aws", "aws", "conn-app", "AWS::EC2::Instance", "i-app")
		if err != nil {
			t.Fatal(err)
		}
		active := asset.Asset{ID: "asset-app", Identity: identity, ScopeID: "scope-app", ResourceKindID: "kind-app", FirstSeenAt: now, LastSeenAt: now}
		if err := repositories.Inventory().PutAsset(ctx, active); err != nil {
			t.Fatal(err)
		}
		rollback := errors.New("rollback application transaction")
		err = repositories.Inventory().WithinInventoryTx(ctx, func(tx persistence.InventoryRepository) error {
			if err := tx.AppendObservation(ctx, asset.Observation{ID: "obs-rolled-back", AssetID: active.ID, ScanRunID: "scan-app", ScanShardID: "shard-app", ObservedAt: now, Source: "config"}); err != nil {
				return err
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("inventory rollback error = %v", err)
		}
		observations, err := repositories.Inventory().ListObservations(ctx, active.ID)
		if err != nil || len(observations) != 0 {
			t.Fatalf("rolled back observations = %+v, err = %v", observations, err)
		}
		if err := repositories.Inventory().AppendObservation(ctx, asset.Observation{ID: "obs-app", AssetID: active.ID, ScanRunID: "scan-app", ScanShardID: "shard-app", ObservedAt: now, Source: "config"}); err != nil {
			t.Fatal(err)
		}
		activeAssets, err := repositories.Inventory().ListActiveAssets(ctx, "scope-app", "kind-app")
		if err != nil || len(activeAssets) != 1 || activeAssets[0].ID != active.ID {
			t.Fatalf("active assets = %+v, err = %v", activeAssets, err)
		}
		observedIDs, err := repositories.Inventory().ListAssetIDsObservedByShard(ctx, "shard-app")
		if err != nil || len(observedIDs) != 1 || observedIDs[0] != active.ID {
			t.Fatalf("observed asset IDs = %+v, err = %v", observedIDs, err)
		}
		err = repositories.Findings().WithinFindingTx(ctx, func(tx persistence.FindingRepository) error {
			if err := tx.PutFinding(ctx, finding.Finding{ID: "finding-rolled-back", AssetID: active.ID, RuleID: "rule", Status: finding.StatusOpen, Severity: finding.SeverityHigh, FirstSeenAt: now, LastSeenAt: now}); err != nil {
				return err
			}
			return rollback
		})
		if !errors.Is(err, rollback) {
			t.Fatalf("finding rollback error = %v", err)
		}
		findings, err := repositories.Findings().ListFindingsByAsset(ctx, active.ID)
		if err != nil || len(findings) != 0 {
			t.Fatalf("rolled back findings = %+v, err = %v", findings, err)
		}
		persistedFinding := finding.Finding{ID: "finding-app", AssetID: active.ID, RuleID: "rule", Status: finding.StatusOpen, Severity: finding.SeverityHigh, FirstSeenAt: now, LastSeenAt: now}
		if err := repositories.Findings().PutFinding(ctx, persistedFinding); err != nil {
			t.Fatal(err)
		}
		assetFindings, err := repositories.Findings().ListFindingsForAsset(ctx, "conn-app", active.ID)
		if err != nil || len(assetFindings) != 1 || assetFindings[0].ID != persistedFinding.ID {
			t.Fatalf("asset findings = %+v, err = %v", assetFindings, err)
		}
		openFindingCounts, err := repositories.Findings().CountOpenFindingsByAssetIDs(
			ctx,
			[]asset.AssetID{active.ID, "asset-missing", active.ID},
		)
		if err != nil || openFindingCounts[active.ID] != 1 || len(openFindingCounts) != 1 {
			t.Fatalf("open finding counts = %+v, err = %v", openFindingCounts, err)
		}
		findingPage, err := repositories.Findings().ListFindings(ctx, persistence.ListOptions{Limit: 10})
		if err != nil || len(findingPage.Items) != 1 || findingPage.Items[0].ID != persistedFinding.ID {
			t.Fatalf("finding page = %+v, err = %v", findingPage, err)
		}
		connectionFindings, err := repositories.Findings().ListFindings(ctx, persistence.ListOptions{Limit: 10, ConnectionID: "conn-app"})
		if err != nil || len(connectionFindings.Items) != 1 {
			t.Fatalf("connection findings = %+v, err = %v", connectionFindings, err)
		}
		foreignFindings, err := repositories.Findings().ListFindings(ctx, persistence.ListOptions{Limit: 10, ConnectionID: "conn-other"})
		if err != nil || len(foreignFindings.Items) != 0 {
			t.Fatalf("foreign findings = %+v, err = %v", foreignFindings, err)
		}
	})

	t.Run("cleanup task intent outbox and lease", func(t *testing.T) {
		repositories := factory(t)
		ctx := context.Background()
		now := time.Date(2026, 7, 13, 4, 2, 0, 0, time.UTC)
		if err := repositories.Inventory().PutAsset(ctx, asset.Asset{
			ID: "asset-1",
			Identity: asset.Identity{
				Provider: asset.ProviderAliCloud, Partition: "public", ConnectionID: "conn-a",
				NativeType: "ACS::ECS::Instance", NativeID: "i-1",
			},
			ScopeID: "scope-global", ResourceKindID: "kind-ecs", FirstSeenAt: now, LastSeenAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		cleanupTask := plan.CleanupTask{ID: "cln-1", ConnectionID: "conn-a", Status: plan.StatusReady, Selectors: []plan.CleanupSelector{{Kind: plan.SelectorAsset, AssetID: "asset-1"}}, ResolvedAssetIDs: []asset.AssetID{"asset-1"}, Revision: plan.RevisionBinding{InventoryRevision: "inventory-1", GraphRevision: "graph-1", SpecBundleRevision: "bundle-1", SpecHash: "hash-1"}, CreatedBy: "tester", CreatedAt: now}
		steps := []plan.CleanupTaskStep{{ID: "step-1", CleanupTaskID: "cln-1", AssetID: "asset-1", Kind: plan.StepController, Action: "delete"}}
		impacts := []plan.ImpactItem{{ID: "impact-1", CleanupTaskID: "cln-1", AssetID: "child-1", ControllerID: "asset-1", DelegatedTo: "step-1", Expected: plan.ExpectedDelegatedDelete}}
		if err := repositories.CleanupTasks().CreateTask(ctx, cleanupTask, steps, impacts); err != nil {
			t.Fatal(err)
		}
		aggregate, err := repositories.CleanupTasks().GetTask(ctx, "cln-1")
		if err != nil || len(aggregate.Steps) != 1 || len(aggregate.ImpactItems) != 1 {
			t.Fatalf("task aggregate = %#v, err = %v", aggregate, err)
		}
		plans, err := repositories.CleanupTasks().ListTasks(ctx, persistence.ListOptions{Limit: 10})
		if err != nil || len(plans.Items) != 1 || plans.Items[0].ID != cleanupTask.ID {
			t.Fatalf("task page = %#v, err = %v", plans, err)
		}
		for _, value := range []plan.CleanupTask{
			{ID: "cln-2", ConnectionID: "conn-a", Status: plan.StatusReady, Selectors: cleanupTask.Selectors, ResolvedAssetIDs: cleanupTask.ResolvedAssetIDs, Revision: cleanupTask.Revision, CreatedBy: "tester", CreatedAt: now.Add(time.Minute)},
			{ID: "cln-3", ConnectionID: "conn-a", Status: plan.StatusReady, Selectors: cleanupTask.Selectors, ResolvedAssetIDs: cleanupTask.ResolvedAssetIDs, Revision: cleanupTask.Revision, CreatedBy: "tester", CreatedAt: now.Add(2 * time.Minute)},
		} {
			if err := repositories.CleanupTasks().CreateTask(ctx, value, nil, nil); err != nil {
				t.Fatal(err)
			}
		}
		plans, err = repositories.CleanupTasks().ListTasks(ctx, persistence.ListOptions{Limit: 1})
		if err != nil || len(plans.Items) != 1 || plans.Items[0].ID != "cln-3" || plans.NextCursor == "" {
			t.Fatalf("newest plans = %#v, err = %v", plans, err)
		}
		plans, err = repositories.CleanupTasks().ListTasks(ctx, persistence.ListOptions{Limit: 1, Cursor: plans.NextCursor})
		if err != nil || len(plans.Items) != 1 || plans.Items[0].ID != "cln-2" || plans.NextCursor == "" {
			t.Fatalf("middle plans = %#v, err = %v", plans, err)
		}
		plans, err = repositories.CleanupTasks().ListTasks(ctx, persistence.ListOptions{Limit: 1, Cursor: plans.NextCursor})
		if err != nil || len(plans.Items) != 1 || plans.Items[0].ID != cleanupTask.ID || plans.NextCursor != "" {
			t.Fatalf("oldest plans = %#v, err = %v", plans, err)
		}
		foreignTasks, err := repositories.CleanupTasks().ListTasks(ctx, persistence.ListOptions{Limit: 10, ConnectionID: "conn-b"})
		if err != nil || len(foreignTasks.Items) != 0 {
			t.Fatalf("foreign plans = %#v, err = %v", foreignTasks, err)
		}
		cleanupTask.Status = plan.StatusInvalidated
		cleanupTask.InvalidationReason = "snapshot changed"
		if err := repositories.CleanupTasks().UpdateTask(ctx, cleanupTask); err != nil {
			t.Fatal(err)
		}
		aggregate, err = repositories.CleanupTasks().GetTask(ctx, "cln-1")
		if err != nil || aggregate.Task.Status != plan.StatusInvalidated || aggregate.Task.InvalidationReason != "snapshot changed" {
			t.Fatalf("updated task aggregate = %#v, err = %v", aggregate, err)
		}
		impacts[0].Result = plan.ImpactDelegated
		if err := repositories.CleanupTasks().UpdateImpactItems(ctx, cleanupTask.ID, impacts); err != nil {
			t.Fatal(err)
		}
		aggregate, err = repositories.CleanupTasks().GetTask(ctx, cleanupTask.ID)
		if err != nil || aggregate.ImpactItems[0].Result != plan.ImpactDelegated {
			t.Fatalf("updated impacts = %#v, err = %v", aggregate.ImpactItems, err)
		}

		executionAttempt := execution.ExecutionAttempt{ID: "execution-1", ConnectionID: "conn-a", CleanupTaskID: "cln-1", Status: execution.ExecutionPending, RequestedBy: "tester", IdempotencyKey: "execution-key", CreatedAt: now}
		actionAttempt := execution.ActionAttempt{ID: "action-1", ExecutionID: "execution-1", CleanupTaskStepID: "step-1", AssetID: "asset-1", Action: "delete", Status: execution.ActionIntentPersisted, IdempotencyKey: "action-key", CreatedAt: now, UpdatedAt: now}
		outbox := execution.OutboxEvent{ID: "outbox-1", Topic: "action.intent", AggregateID: "action-1", CreatedAt: now}
		if err := repositories.WithTx(ctx, func(tx persistence.Repositories) error {
			if err := tx.Executions().CreateExecution(ctx, executionAttempt); err != nil {
				return err
			}
			if err := tx.Executions().AppendAction(ctx, actionAttempt); err != nil {
				return err
			}
			return tx.Executions().AppendOutbox(ctx, outbox)
		}); err != nil {
			t.Fatal(err)
		}
		storedExecution, err := repositories.Executions().GetExecutionByIdempotencyKey(ctx, "execution-key")
		if err != nil || storedExecution.ID != executionAttempt.ID {
			t.Fatalf("execution by idempotency key = %#v, err = %v", storedExecution, err)
		}
		executions, err := repositories.Executions().ListExecutions(ctx, persistence.ListOptions{Limit: 10})
		if err != nil || len(executions.Items) != 1 || executions.Items[0].ID != executionAttempt.ID {
			t.Fatalf("execution page = %#v, err = %v", executions, err)
		}
		planExecutions, err := repositories.Executions().ListCleanupTaskExecutions(ctx, "conn-a", "cln-1", persistence.ListOptions{Limit: 10})
		if err != nil || len(planExecutions.Items) != 1 || planExecutions.Items[0].ID != executionAttempt.ID {
			t.Fatalf("cleanup task execution page = %#v, err = %v", planExecutions, err)
		}
		foreignExecutions, err := repositories.Executions().ListExecutions(ctx, persistence.ListOptions{Limit: 10, ConnectionID: "conn-b"})
		if err != nil || len(foreignExecutions.Items) != 0 {
			t.Fatalf("foreign executions = %#v, err = %v", foreignExecutions, err)
		}
		for _, event := range []execution.AuditEvent{
			{ID: "audit-older", ConnectionID: "conn-a", Actor: "tester", Action: "test", TargetType: "cleanup_task", TargetID: "cln-1", CreatedAt: now.Add(-time.Second)},
			{ID: "audit-1", ConnectionID: "conn-a", Actor: "tester", Action: "test", TargetType: "cleanup_task", TargetID: "cln-1", CreatedAt: now},
			{ID: "audit-2", ConnectionID: "conn-a", Actor: "tester", Action: "test", TargetType: "cleanup_task", TargetID: "cln-1", CreatedAt: now},
		} {
			if err := repositories.Audits().AppendAuditEvent(ctx, event); err != nil {
				t.Fatal(err)
			}
		}
		firstAudits, err := repositories.Audits().ListAuditEvents(ctx, persistence.ListOptions{Limit: 2, ConnectionID: "conn-a"})
		if err != nil || len(firstAudits.Items) != 2 || firstAudits.Items[0].ID != "audit-2" || firstAudits.Items[1].ID != "audit-1" || firstAudits.NextCursor == "" {
			t.Fatalf("first audit page = %#v, err = %v", firstAudits, err)
		}
		secondAudits, err := repositories.Audits().ListAuditEvents(ctx, persistence.ListOptions{Limit: 2, ConnectionID: "conn-a", Cursor: firstAudits.NextCursor})
		if err != nil || len(secondAudits.Items) != 1 || secondAudits.Items[0].ID != "audit-older" || secondAudits.NextCursor != "" {
			t.Fatalf("second audit page = %#v, err = %v", secondAudits, err)
		}
		foreignAudits, err := repositories.Audits().ListAuditEvents(ctx, persistence.ListOptions{Limit: 10, ConnectionID: "conn-b"})
		if err != nil || len(foreignAudits.Items) != 0 {
			t.Fatalf("foreign audits = %#v, err = %v", foreignAudits, err)
		}
		executionAttempt.Status = execution.ExecutionRunning
		if err := repositories.Executions().UpdateExecution(ctx, executionAttempt); err != nil {
			t.Fatal(err)
		}
		storedExecution, err = repositories.Executions().GetExecution(ctx, executionAttempt.ID)
		if err != nil || storedExecution.Status != execution.ExecutionRunning {
			t.Fatalf("updated execution = %#v, err = %v", storedExecution, err)
		}
		storedAction, err := repositories.Executions().GetActionByExecutionStep(ctx, executionAttempt.ID, actionAttempt.CleanupTaskStepID)
		if err != nil || storedAction.ID != actionAttempt.ID {
			t.Fatalf("action by execution step = %#v, err = %v", storedAction, err)
		}
		actions, err := repositories.Executions().ListActions(ctx, executionAttempt.ID)
		if err != nil || len(actions) != 1 || actions[0].ID != actionAttempt.ID {
			t.Fatalf("execution actions = %#v, err = %v", actions, err)
		}
		authorizedActions, err := repositories.Executions().ListExecutionActions(ctx, "conn-a", executionAttempt.ID)
		if err != nil || len(authorizedActions) != 1 || authorizedActions[0].ID != actionAttempt.ID {
			t.Fatalf("authorized execution actions = %#v, err = %v", authorizedActions, err)
		}
		pending, err := repositories.Executions().ListPendingOutbox(ctx, 10)
		if err != nil || len(pending) != 1 || pending[0].AggregateID != "action-1" {
			t.Fatalf("outbox = %#v, err = %v", pending, err)
		}
		if err := repositories.Executions().MarkOutboxPublished(ctx, "outbox-1", now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		pending, err = repositories.Executions().ListPendingOutbox(ctx, 10)
		if err != nil || len(pending) != 0 {
			t.Fatalf("published outbox still pending = %#v, err = %v", pending, err)
		}

		if err := repositories.Inventory().CreateScanRun(ctx, asset.ScanRun{
			ID: "scan-history", ConnectionID: "conn-a", Status: asset.ScanSucceeded,
			Targets: []asset.ScanTarget{{
				Key: "region:cn-hangzhou", Kind: asset.ScanTargetRegion, RegionID: "cn-hangzhou",
			}},
			CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		job := execution.Job{ID: "job-1", ConnectionID: "conn-a", AggregateType: "cleanup_task", AggregateID: "cln-1", Type: execution.JobExecute, Status: execution.JobPending, Payload: map[string]any{"execution_id": "execution-1"}, RunAt: now, CreatedAt: now, UpdatedAt: now}
		if err := repositories.Jobs().Enqueue(ctx, job); err != nil {
			t.Fatal(err)
		}
		refreshJob := execution.Job{ID: "region-refresh-1", ConnectionID: "conn-a", Type: execution.JobRegionRefresh, Status: execution.JobPending, Payload: map[string]any{"connection_id": "conn-a"}, RunAt: now.Add(time.Hour), CreatedAt: now, UpdatedAt: now}
		if err := repositories.Jobs().Enqueue(ctx, refreshJob); err != nil {
			t.Fatal(err)
		}
		activeRefresh, err := repositories.Jobs().FindActiveByType(ctx, "conn-a", execution.JobRegionRefresh)
		if err != nil || activeRefresh.ID != refreshJob.ID {
			t.Fatalf("active refresh = %#v, err = %v", activeRefresh, err)
		}
		latestRefresh, err := repositories.Jobs().FindLatestByType(ctx, "conn-a", execution.JobRegionRefresh)
		if err != nil || latestRefresh.ID != refreshJob.ID {
			t.Fatalf("latest refresh = %#v, err = %v", latestRefresh, err)
		}
		duplicateRefresh := refreshJob
		duplicateRefresh.ID = "region-refresh-2"
		if err := repositories.Jobs().Enqueue(ctx, duplicateRefresh); !errors.Is(err, persistence.ErrConflict) {
			t.Fatalf("duplicate active refresh error = %v", err)
		}
		claimed, err := repositories.Jobs().ClaimNext(ctx, "worker-a", now, time.Minute)
		if err != nil || claimed.ID != "job-1" || claimed.LeaseOwner != "worker-a" {
			t.Fatalf("claimed = %#v, err = %v", claimed, err)
		}
		if _, err := repositories.Jobs().ClaimNext(ctx, "worker-b", now.Add(30*time.Second), time.Minute); !errors.Is(err, persistence.ErrNotFound) {
			t.Fatalf("active lease claim err = %v", err)
		}
		reclaimed, err := repositories.Jobs().ClaimNext(ctx, "worker-b", now.Add(2*time.Minute), time.Minute)
		if err != nil || reclaimed.ID != "job-1" || reclaimed.LeaseOwner != "worker-b" {
			t.Fatalf("reclaimed = %#v, err = %v", reclaimed, err)
		}
		storedJob, err := repositories.Jobs().GetJob(ctx, "job-1")
		if err != nil || storedJob.LeaseOwner != "worker-b" {
			t.Fatalf("stored job = %#v, err = %v", storedJob, err)
		}
		duration, recorded := execution.ActiveDuration([]execution.Job{storedJob}, now.Add(150*time.Second))
		if !recorded || duration != 90*time.Second {
			t.Fatalf("persisted active duration = %s, %v; want 1m30s, true (retry wait excluded)", duration, recorded)
		}
		jobsByAggregate, err := repositories.Jobs().ListJobsByAggregates(ctx, "cleanup_task", []string{"cln-1"})
		if err != nil || len(jobsByAggregate["cln-1"]) != 1 {
			t.Fatalf("jobs by aggregates = %#v, err = %v", jobsByAggregate, err)
		}
		fullLogPayload := strings.Repeat("cloud response ", 10_000)
		if err := repositories.Jobs().AppendLog(ctx, execution.JobLog{
			ID: "log-001", JobID: "job-1", AggregateType: "scan_task", AggregateID: "scan-history",
			TargetKey: "region:cn-hangzhou", Sequence: 1, Kind: execution.JobLogCloudAPIResponse,
			Level: "info", Message: "resource-center SearchResources returned",
			Payload: map[string]any{"Response": fullLogPayload}, CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		logs, err := repositories.Jobs().ListLogs(ctx, "job-1", 0, 10)
		if err != nil || len(logs) != 1 || logs[0].Kind != execution.JobLogCloudAPIResponse || logs[0].Payload["Response"] != fullLogPayload {
			t.Fatalf("logs = %#v, err = %v", logs, err)
		}
		for index := 2; index <= 205; index++ {
			if err := repositories.Jobs().AppendLog(ctx, execution.JobLog{
				ID: fmt.Sprintf("log-%03d", index), JobID: "job-1",
				AggregateType: "scan_task", AggregateID: "scan-history",
				TargetKey: "region:cn-hangzhou", Sequence: int64(index), Kind: execution.JobLogText,
				Level: "info", Message: fmt.Sprintf("log %d", index),
				CreatedAt: now.Add(time.Duration(index-1) * time.Millisecond),
			}); err != nil {
				t.Fatal(err)
			}
		}
		for index := 1; index <= 205; index++ {
			if err := repositories.Jobs().AppendLog(ctx, execution.JobLog{
				ID: fmt.Sprintf("west-log-%03d", index), JobID: "job-1",
				AggregateType: "scan_task", AggregateID: "scan-history",
				TargetKey: "region:us-west-1", Sequence: int64(205 + index), Kind: execution.JobLogText,
				Level: "info", Message: fmt.Sprintf("west log %d", index),
				CreatedAt: now.Add(time.Duration(index-1)*time.Millisecond + 500*time.Microsecond),
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := repositories.Jobs().AppendLog(ctx, execution.JobLog{
			ID: "cleanup-log-001", JobID: "job-1", AggregateType: "cleanup_task", AggregateID: "cln-1",
			TargetKey: "asset-1", Sequence: 411, Kind: execution.JobLogText, Level: "info", Message: "cleanup log", CreatedAt: now,
		}); err != nil {
			t.Fatal(err)
		}
		for index := 1; index <= 105; index++ {
			if err := repositories.Jobs().AppendLog(ctx, execution.JobLog{
				ID: fmt.Sprintf("cleanup-task-log-%03d", index), JobID: "job-1",
				AggregateType: "cleanup_task", AggregateID: "cln-1",
				Sequence: int64(411 + index), Kind: execution.JobLogText,
				Level: "info", Message: fmt.Sprintf("task cleanup log %d", index),
				CreatedAt: now.Add(time.Duration(index) * time.Millisecond),
			}); err != nil {
				t.Fatal(err)
			}
		}
		taskLatest, err := repositories.Jobs().ListLogsByAggregateBefore(ctx, "scan_task", "scan-history", "", time.Time{}, "", 100)
		if err != nil || len(taskLatest) != 100 || taskLatest[0].ID != "log-156" || taskLatest[99].ID != "west-log-205" {
			t.Fatalf("task latest logs = %d %#v, err = %v", len(taskLatest), taskLatest, err)
		}
		latest, err := repositories.Jobs().ListLogsByAggregateBefore(ctx, "scan_task", "scan-history", "region:cn-hangzhou", time.Time{}, "", 100)
		if err != nil || len(latest) != 100 || latest[0].ID != "log-106" || latest[99].ID != "log-205" {
			t.Fatalf("latest logs = %d %#v, err = %v", len(latest), latest, err)
		}
		middle, err := repositories.Jobs().ListLogsByAggregateBefore(ctx, "scan_task", "scan-history", "region:cn-hangzhou", latest[0].CreatedAt, latest[0].ID, 100)
		if err != nil || len(middle) != 100 || middle[0].ID != "log-006" || middle[99].ID != "log-105" {
			t.Fatalf("middle logs = %d %#v, err = %v", len(middle), middle, err)
		}
		oldest, err := repositories.Jobs().ListLogsByAggregateBefore(ctx, "scan_task", "scan-history", "region:cn-hangzhou", middle[0].CreatedAt, middle[0].ID, 100)
		if err != nil || len(oldest) != 5 || oldest[0].ID != "log-001" || oldest[4].ID != "log-005" {
			t.Fatalf("oldest logs = %d %#v, err = %v", len(oldest), oldest, err)
		}
		westLatest, err := repositories.Jobs().ListLogsByAggregateBefore(ctx, "scan_task", "scan-history", "region:us-west-1", time.Time{}, "", 100)
		if err != nil || len(westLatest) != 100 || westLatest[0].ID != "west-log-106" || westLatest[99].ID != "west-log-205" {
			t.Fatalf("west latest logs = %d %#v, err = %v", len(westLatest), westLatest, err)
		}
		scanTask, joinedScanLogs, err := repositories.Jobs().ListScanLogsBefore(
			ctx, "conn-a", "scan-history", "region:cn-hangzhou", time.Time{}, "", 100,
		)
		if err != nil || scanTask.ID != "scan-history" || len(joinedScanLogs) != 100 ||
			joinedScanLogs[0].ID != "log-106" || joinedScanLogs[99].ID != "log-205" {
			t.Fatalf("joined scan logs task=%#v logs=%d err=%v", scanTask, len(joinedScanLogs), err)
		}
		logTask, joinedCleanupLogs, err := repositories.Jobs().ListCleanupLogsBefore(
			ctx, "conn-a", "cln-1", persistence.CleanupLogFilter{ResourceID: "i-1", ResourceKindIDs: []asset.ResourceKindID{"kind-ecs"}}, time.Time{}, "", 100,
		)
		if err != nil || logTask.ID != "cln-1" || len(joinedCleanupLogs) != 1 || joinedCleanupLogs[0].ID != "cleanup-log-001" {
			t.Fatalf("joined cleanup logs task=%#v logs=%#v err=%v", logTask, joinedCleanupLogs, err)
		}
		_, missingCleanupLogs, err := repositories.Jobs().ListCleanupLogsBefore(
			ctx, "conn-a", "cln-1", persistence.CleanupLogFilter{ResourceID: "missing"}, time.Time{}, "", 100,
		)
		if err != nil || len(missingCleanupLogs) != 0 {
			t.Fatalf("filtered cleanup logs=%#v err=%v", missingCleanupLogs, err)
		}
	})
}
