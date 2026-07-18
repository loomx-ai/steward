package inventory_test

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

func TestScanControlPausesResumesAndCancelsPendingTask(t *testing.T) {
	ctx := context.Background()
	repositories, creator, _ := creatorFixture(t)
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: "connection-a", RequestedBy: "alice", RegionMode: inventory.RegionModeAllActive})
	if err != nil {
		t.Fatal(err)
	}
	controller, err := inventory.NewControlService(repositories)
	if err != nil {
		t.Fatal(err)
	}
	paused, err := controller.Pause(ctx, created.ScanRun.ID, "alice")
	if err != nil || paused.Status != asset.ScanPaused || paused.ControlVersion != 1 {
		t.Fatalf("paused = %+v, err = %v", paused, err)
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "scan_task", string(created.ScanRun.ID))
	if err != nil || len(jobs) != 2 {
		t.Fatalf("paused jobs = %+v, err = %v", jobs, err)
	}
	for _, job := range jobs {
		if job.Status != execution.JobPaused {
			t.Fatalf("paused job = %+v", job)
		}
	}
	shards, err := repositories.Inventory().ListScanShardsByRun(ctx, created.ScanRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, shard := range shards {
		if shard.Status != asset.ShardPaused {
			t.Fatalf("paused shard = %+v", shard)
		}
	}
	resumed, err := controller.Resume(ctx, created.ScanRun.ID, "alice")
	if err != nil || resumed.Status != asset.ScanPending || resumed.ControlVersion != 2 || resumed.RetryGeneration != 0 {
		t.Fatalf("resumed = %+v, err = %v", resumed, err)
	}
	shards, err = repositories.Inventory().ListScanShardsByRun(ctx, created.ScanRun.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, shard := range shards {
		if shard.Status != asset.ShardPending {
			t.Fatalf("resumed shard = %+v", shard)
		}
	}
	canceled, err := controller.Cancel(ctx, created.ScanRun.ID, "alice")
	if err != nil || canceled.Status != asset.ScanCanceled || canceled.FinishedAt == nil || canceled.ControlVersion != 3 {
		t.Fatalf("canceled = %+v, err = %v", canceled, err)
	}
	if _, err := controller.Resume(ctx, created.ScanRun.ID, "alice"); err == nil {
		t.Fatal("canceled task resumed")
	}
	if _, err := controller.Retry(ctx, created.ScanRun.ID, "alice"); err == nil {
		t.Fatal("canceled task retried")
	}
}

func TestScanControlRetryRequeuesOnlyFailedAndBlockedShards(t *testing.T) {
	ctx := context.Background()
	repositories, creator, _ := creatorFixture(t)
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: "connection-a", RequestedBy: "alice", RegionMode: inventory.RegionModeAllActive})
	if err != nil {
		t.Fatal(err)
	}
	task := created.ScanRun
	task.Status = asset.ScanFailed
	if err := repositories.Inventory().PutScanRun(ctx, task); err != nil {
		t.Fatal(err)
	}
	for index, shard := range created.Shards {
		if index == 0 {
			shard.Status = asset.ShardFailed
		} else {
			shard.Status = asset.ShardBlocked
		}
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
	}
	for _, job := range created.Jobs {
		job.Status = execution.JobFailed
		if err := repositories.Jobs().UpdateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	controller, _ := inventory.NewControlService(repositories)
	retried, err := controller.Retry(ctx, task.ID, "alice")
	if err != nil || retried.ID != task.ID || retried.Status != asset.ScanPending || retried.RetryGeneration != 1 || retried.RetryCount != 1 {
		t.Fatalf("retried = %+v, err = %v", retried, err)
	}
	shards, err := repositories.Inventory().ListScanShardsByRun(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, shard := range shards {
		if shard.Status != asset.ShardPending || shard.RetryGeneration != 1 {
			t.Fatalf("retried shard = %+v", shard)
		}
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "scan_task", string(task.ID))
	if err != nil {
		t.Fatal(err)
	}
	pattern := regexp.MustCompile(`^job-[23456789abcdefghjkmnpqrstuvwxyz]{16}$`)
	retryJobs := 0
	for _, job := range jobs {
		if job.RetryGeneration != 1 {
			continue
		}
		retryJobs++
		if job.Status != execution.JobPending || !pattern.MatchString(string(job.ID)) {
			t.Fatalf("retry job = %+v", job)
		}
	}
	if retryJobs != 2 {
		t.Fatalf("retry jobs = %d, all = %+v", retryJobs, jobs)
	}
}

func TestScanControlRetryRequeuesOnlyFailedGraphReconciliation(t *testing.T) {
	ctx := context.Background()
	repositories, creator, now := creatorFixture(t)
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{
		ConnectionID: "connection-a", RequestedBy: "alice", RegionMode: inventory.RegionModeAllActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := created.ScanRun
	task.Status = asset.ScanFailed
	task.CompletionStatus = asset.ScanSucceeded
	task.FinishedAt = &now
	if err := repositories.Inventory().PutScanRun(ctx, task); err != nil {
		t.Fatal(err)
	}
	for _, shard := range created.Shards {
		shard.Status = asset.ShardSucceeded
		shard.Coverage.Complete = true
		shard.FinishedAt = &now
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
	}
	for _, job := range created.Jobs {
		job.Status = execution.JobSucceeded
		job.FinishedAt = &now
		job.UpdatedAt = now
		if err := repositories.Jobs().UpdateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}
	controller, err := inventory.NewControlService(repositories)
	if err != nil {
		t.Fatal(err)
	}

	retried, err := controller.Retry(ctx, task.ID, "alice")
	if err != nil || retried.Status != asset.ScanReconciling ||
		retried.CompletionStatus != asset.ScanSucceeded ||
		retried.RetryGeneration != 1 || retried.RetryCount != 1 ||
		retried.FinishedAt != nil {
		t.Fatalf("retried = %+v, err = %v", retried, err)
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "scan_task", string(task.ID))
	if err != nil {
		t.Fatal(err)
	}
	graphJobs := 0
	for _, job := range jobs {
		if job.Type != execution.JobGraph {
			continue
		}
		graphJobs++
		if job.Status != execution.JobPending || job.RetryGeneration != 1 ||
			job.IdempotencyKey != "scan-graph:"+string(task.ID)+":retry:1" {
			t.Fatalf("graph retry job = %+v", job)
		}
	}
	if graphJobs != 1 {
		t.Fatalf("graph retry jobs = %d, jobs = %+v", graphJobs, jobs)
	}
}

func TestScanControlRetryRequeuesFailedAndUnstartedShardsAfterPartialScanReconciliationFails(t *testing.T) {
	ctx := context.Background()
	repositories, creator, now := creatorFixture(t)
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{
		ConnectionID: "connection-a", RequestedBy: "alice", RegionMode: inventory.RegionModeAllActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := created.ScanRun
	task.Status = asset.ScanFailed
	task.CompletionStatus = asset.ScanPartial
	task.FinishedAt = &now
	if err := repositories.Inventory().PutScanRun(ctx, task); err != nil {
		t.Fatal(err)
	}
	succeededShardID := created.Shards[0].ID
	for index, shard := range created.Shards {
		shard.FinishedAt = &now
		switch index {
		case 0:
			shard.Status = asset.ShardSucceeded
			shard.Coverage.Complete = true
		default:
			shard.Status = asset.ShardFailed
			shard.Coverage.FailureReason = "provider request failed"
		}
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
	}
	unstarted := created.Shards[len(created.Shards)-1]
	unstarted.ID = "shard-unstarted"
	unstarted.Status = asset.ShardBlocked
	unstarted.Coverage.Complete = false
	unstarted.Coverage.FailureReason = "waiting for an earlier source"
	if err := repositories.Inventory().PutScanShard(ctx, unstarted); err != nil {
		t.Fatal(err)
	}
	controller, err := inventory.NewControlService(repositories)
	if err != nil {
		t.Fatal(err)
	}

	retried, err := controller.Retry(ctx, task.ID, "alice")
	if err != nil || retried.Status != asset.ScanPending ||
		retried.RetryGeneration != 1 || retried.RetryCount != 1 ||
		retried.FinishedAt != nil {
		t.Fatalf("retried = %+v, err = %v", retried, err)
	}
	shards, err := repositories.Inventory().ListScanShardsByRun(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, shard := range shards {
		if shard.ID == succeededShardID {
			if shard.Status != asset.ShardSucceeded || shard.RetryGeneration != 0 {
				t.Fatalf("succeeded shard was retried = %+v", shard)
			}
			continue
		}
		if shard.Status != asset.ShardPending || shard.RetryGeneration != 1 ||
			shard.StartedAt != nil || shard.FinishedAt != nil {
			t.Fatalf("failed or unstarted shard was not retried = %+v", shard)
		}
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "scan_task", string(task.ID))
	if err != nil {
		t.Fatal(err)
	}
	retryScanJobs := 0
	retryGraphJobs := 0
	for _, job := range jobs {
		if job.RetryGeneration != 1 {
			continue
		}
		switch job.Type {
		case execution.JobScan:
			retryScanJobs++
		case execution.JobGraph:
			retryGraphJobs++
		}
	}
	if retryScanJobs != 1 || retryGraphJobs != 0 {
		t.Fatalf("retry scan jobs = %d, graph jobs = %d, all = %+v", retryScanJobs, retryGraphJobs, jobs)
	}
}

func TestScanControlRetrySkipsFailedShardsRemovedFromCurrentProviderPlan(t *testing.T) {
	ctx := context.Background()
	repositories, creator, _ := creatorFixture(t)
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{
		ConnectionID: "connection-a", RequestedBy: "alice", RegionMode: inventory.RegionModeAllActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := created.ScanRun
	task.Status = asset.ScanPartial
	if err := repositories.Inventory().PutScanRun(ctx, task); err != nil {
		t.Fatal(err)
	}
	obsolete := created.Shards[0]
	obsolete.Status = asset.ShardFailed
	obsolete.ResourceKindID = "alicloud:ACS::VOD::Domain"
	obsolete.Coverage.FailureReason = "endpoint does not support this region"
	if err := repositories.Inventory().PutScanShard(ctx, obsolete); err != nil {
		t.Fatal(err)
	}
	active := created.Shards[1]
	active.Status = asset.ShardBlocked
	if err := repositories.Inventory().PutScanShard(ctx, active); err != nil {
		t.Fatal(err)
	}
	directory := creatorDirectory{
		sources: []contracts.InventorySource{
			{Name: "resource-center", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion}},
		},
	}
	controller, err := inventory.NewControlService(
		repositories,
		inventory.WithControlDirectory(directory),
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := controller.Retry(ctx, task.ID, "alice"); err != nil {
		t.Fatal(err)
	}
	storedObsolete, err := repositories.Inventory().GetScanShard(ctx, obsolete.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedObsolete.Status != asset.ShardSkipped ||
		storedObsolete.Coverage.SkipReason != asset.SkipProductUnsupported ||
		storedObsolete.Authoritative {
		t.Fatalf("obsolete shard=%+v", storedObsolete)
	}
	storedActive, err := repositories.Inventory().GetScanShard(ctx, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	if storedActive.Status != asset.ShardPending || storedActive.RetryGeneration != 1 {
		t.Fatalf("active shard=%+v", storedActive)
	}
}

func TestScanControlRetryRepairsSucceededAllRegionPlanWithCurrentRegionalAndGlobalShards(t *testing.T) {
	ctx := context.Background()
	repositories, creator, now := creatorFixture(t)
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{
		ConnectionID: "connection-a", RequestedBy: "alice", RegionMode: inventory.RegionModeAllActive,
	})
	if err != nil {
		t.Fatal(err)
	}
	task := created.ScanRun
	task.Status = asset.ScanSucceeded
	finished := now.Add(time.Minute)
	task.FinishedAt = &finished
	if err := repositories.Inventory().PutScanRun(ctx, task); err != nil {
		t.Fatal(err)
	}
	for _, shard := range created.Shards {
		shard.Status = asset.ShardSucceeded
		shard.Coverage.Complete = true
		shard.FinishedAt = &finished
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
	}
	for _, job := range created.Jobs {
		job.Status = execution.JobSucceeded
		job.FinishedAt = &finished
		job.UpdatedAt = finished
		if err := repositories.Jobs().UpdateJob(ctx, job); err != nil {
			t.Fatal(err)
		}
	}

	regionKind := asset.ResourceKind{
		ID: "alicloud:ACS::ECS::Instance", Provider: asset.ProviderAliCloud,
		NativeType: "ACS::ECS::Instance", ScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "current",
	}
	globalKind := asset.ResourceKind{
		ID: "alicloud:ACS::CEN::CenInstance", Provider: asset.ProviderAliCloud,
		NativeType: "ACS::CEN::CenInstance", ScopeKinds: []asset.ScopeKind{asset.ScopeGlobal},
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "current",
	}
	directory := creatorDirectory{
		sources: []contracts.InventorySource{
			{Name: "resource-center", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion}},
			{
				Name: "product-api", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal},
				AuthoritativeDefault: true, KindSpecific: true,
			},
		},
		bundle: spec.Bundle{
			Provider: asset.ProviderAliCloud,
			Specs: []spec.CompiledSpec{
				{
					Definition:   spec.ResourceKindSpec{Discovery: spec.DiscoverySpec{Source: "product-api"}},
					ResourceKind: regionKind,
				},
				{
					Definition:   spec.ResourceKindSpec{Discovery: spec.DiscoverySpec{Source: "product-api"}},
					ResourceKind: globalKind,
				},
			},
		},
	}
	controller, err := inventory.NewControlService(
		repositories,
		inventory.WithControlDirectory(directory),
	)
	if err != nil {
		t.Fatal(err)
	}
	canRetry, err := controller.CanRetry(ctx, task, created.Shards)
	if err != nil || !canRetry {
		t.Fatalf("CanRetry() = %v, %v", canRetry, err)
	}

	retried, err := controller.Retry(ctx, task.ID, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if retried.Status != asset.ScanPending || retried.RetryGeneration != 1 ||
		retried.RetryCount != 1 || retried.FinishedAt != nil ||
		len(retried.Targets) != 3 || retried.Targets[0].Kind != asset.ScanTargetGlobal ||
		retried.Targets[0].Key != "global" {
		t.Fatalf("retried task = %+v", retried)
	}
	shards, err := repositories.Inventory().ListScanShardsByRun(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(shards) != 5 {
		t.Fatalf("reconciled shards = %+v", shards)
	}
	retryShards := 0
	targets := map[string]int{}
	for _, shard := range shards {
		targets[shard.TargetKey]++
		if shard.RetryGeneration != 1 {
			continue
		}
		retryShards++
		if shard.Status != asset.ShardPending || shard.Source != "product-api" {
			t.Fatalf("repaired shard = %+v", shard)
		}
	}
	if retryShards != 3 ||
		targets["region:cn-hangzhou"] != 2 ||
		targets["region:cn-shanghai"] != 2 ||
		targets["global"] != 1 {
		t.Fatalf("reconciled targets = %+v, retry shards = %d", targets, retryShards)
	}
	jobs, err := repositories.Jobs().ListJobsByAggregate(ctx, "scan_task", string(task.ID))
	if err != nil {
		t.Fatal(err)
	}
	retryJobs := 0
	for _, job := range jobs {
		if job.RetryGeneration == 1 {
			retryJobs++
		}
	}
	if retryJobs != 3 {
		t.Fatalf("retry jobs = %d, all = %+v", retryJobs, jobs)
	}
	projection := inventory.ProjectScanTask(retried, shards)
	if len(projection.TargetProgress) != 3 ||
		projection.TargetProgress[0].Kind != asset.ScanTargetGlobal ||
		projection.TargetProgress[0].RegionID != "global" ||
		projection.TargetProgress[0].Total != 1 ||
		projection.TargetProgress[1].Completed != 1 ||
		projection.TargetProgress[1].Total != 2 ||
		projection.TargetProgress[1].Summary != "已完成 1/2" {
		t.Fatalf("target progress = %+v", projection.TargetProgress)
	}
}

func TestScanControlRetryDoesNotAddUnselectedGlobalRegion(t *testing.T) {
	ctx := context.Background()
	repositories, creator, now := creatorFixture(t)
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{
		ConnectionID: "connection-a", RequestedBy: "alice", ScopeMode: asset.ScanSelectedRegions,
		RegionIDs: []string{"cn-hangzhou"},
	})
	if err != nil {
		t.Fatal(err)
	}
	task := created.ScanRun
	task.Status = asset.ScanSucceeded
	task.FinishedAt = &now
	if err := repositories.Inventory().PutScanRun(ctx, task); err != nil {
		t.Fatal(err)
	}
	for _, shard := range created.Shards {
		shard.Status = asset.ShardSucceeded
		shard.Coverage.Complete = true
		shard.FinishedAt = &now
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
	}
	globalKind := asset.ResourceKind{
		ID: "alicloud:ACS::CEN::CenInstance", Provider: asset.ProviderAliCloud,
		NativeType: "ACS::CEN::CenInstance", ScopeKinds: []asset.ScopeKind{asset.ScopeGlobal},
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed}, BundleRevision: "current",
	}
	controller, err := inventory.NewControlService(
		repositories,
		inventory.WithControlDirectory(creatorDirectory{
			sources: []contracts.InventorySource{
				{Name: "resource-center", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion}},
				{
					Name: "product-api", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal},
					AuthoritativeDefault: true, KindSpecific: true,
				},
			},
			bundle: spec.Bundle{
				Provider: asset.ProviderAliCloud,
				Specs: []spec.CompiledSpec{{
					Definition:   spec.ResourceKindSpec{Discovery: spec.DiscoverySpec{Source: "product-api"}},
					ResourceKind: globalKind,
				}},
			},
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	canRetry, err := controller.CanRetry(ctx, task, created.Shards)
	if err != nil || canRetry {
		t.Fatalf("CanRetry() = %v, %v; unselected global must not create a retry gap", canRetry, err)
	}
}

func TestScanControlRetryRejectsUnverifiedConnection(t *testing.T) {
	ctx := context.Background()
	repositories, creator, now := creatorFixture(t)
	created, err := creator.Create(ctx, inventory.ScanCreationRequest{ConnectionID: "connection-a", RequestedBy: "alice", RegionMode: inventory.RegionModeAllActive})
	if err != nil {
		t.Fatal(err)
	}
	task := created.ScanRun
	task.Status = asset.ScanFailed
	if err := repositories.Inventory().PutScanRun(ctx, task); err != nil {
		t.Fatal(err)
	}
	for _, shard := range created.Shards {
		shard.Status = asset.ShardFailed
		if err := repositories.Inventory().PutScanShard(ctx, shard); err != nil {
			t.Fatal(err)
		}
	}
	connection, err := repositories.Connections().GetConnection(ctx, task.ConnectionID)
	if err != nil {
		t.Fatal(err)
	}
	connection.Status = asset.ConnectionUnverified
	connection.UpdatedAt = now.Add(time.Minute)
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	controller, err := inventory.NewControlService(repositories)
	if err != nil {
		t.Fatal(err)
	}

	_, err = controller.Retry(ctx, task.ID, "alice")
	if !errors.Is(err, asset.ErrConnectionNotValidated) {
		t.Fatalf("Retry() error = %v, want ErrConnectionNotValidated", err)
	}
}
