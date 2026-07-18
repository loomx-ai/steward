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

type discovererStub struct {
	regions []contracts.DiscoveredRegion
	err     error
	calls   []asset.ConnectionID
}

func (s *discovererStub) DiscoverRegions(_ context.Context, connectionID asset.ConnectionID) ([]contracts.DiscoveredRegion, error) {
	s.calls = append(s.calls, connectionID)
	return append([]contracts.DiscoveredRegion(nil), s.regions...), s.err
}

type discovererRegistryStub struct {
	provider   asset.Provider
	discoverer contracts.RegionDiscoverer
}

func (s discovererRegistryStub) ResolveRegionDiscoverer(provider asset.Provider) (contracts.RegionDiscoverer, error) {
	if provider != s.provider {
		return nil, errors.New("unexpected provider")
	}
	return s.discoverer, nil
}

func TestRefreshQueueDeduplicatesPendingAndRunningJobs(t *testing.T) {
	ctx := context.Background()
	repositories, now := refreshFixture(t)
	queue, err := NewRefreshQueue(repositories)
	if err != nil {
		t.Fatal(err)
	}
	queue.now = func() time.Time { return now }
	ids := []string{"refresh-1", "refresh-2"}
	queue.newID = func() string {
		id := ids[0]
		ids = ids[1:]
		return id
	}
	first, err := queue.Enqueue(ctx, "conn-a")
	if err != nil {
		t.Fatal(err)
	}
	second, err := queue.Enqueue(ctx, "conn-a")
	if err != nil || second.ID != first.ID {
		t.Fatalf("second enqueue = %#v, %v", second, err)
	}
	claimed, err := repositories.Jobs().ClaimNext(ctx, "worker", now, time.Minute)
	if err != nil || claimed.ID != first.ID {
		t.Fatalf("claimed = %#v, %v", claimed, err)
	}
	running, err := queue.Enqueue(ctx, "conn-a")
	if err != nil || running.ID != first.ID || running.Status != execution.JobRunning {
		t.Fatalf("running enqueue = %#v, %v", running, err)
	}
	if err := repositories.Jobs().Complete(ctx, first.ID, "worker", execution.JobSucceeded, "", now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	next, err := queue.Enqueue(ctx, "conn-a")
	if err != nil || next.ID != "refresh-2" || next.Payload["connection_id"] != "conn-a" || len(next.Payload) != 1 {
		t.Fatalf("next enqueue = %#v, %v", next, err)
	}
}

func TestRefreshHandlerDiscoversAndMergesRegions(t *testing.T) {
	var entries []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) {
		entries = append(entries, entry)
	}))
	repositories, now := refreshFixture(t)
	service, err := NewService(repositories)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now }
	discoverer := &discovererStub{regions: []contracts.DiscoveredRegion{{RegionID: "cn-hangzhou", Name: "华东 1"}}}
	handler := NewRefreshHandler(repositories, discovererRegistryStub{provider: asset.ProviderAliCloud, discoverer: discoverer}, service)
	job := execution.Job{ID: "refresh-job", ConnectionID: "conn-a", Type: execution.JobRegionRefresh, Attempts: 1, Payload: map[string]any{"connection_id": "untrusted-other"}}
	if err := handler.Handle(ctx, job); err != nil {
		t.Fatal(err)
	}
	if len(discoverer.calls) != 1 || discoverer.calls[0] != "conn-a" {
		t.Fatalf("discoverer calls = %#v", discoverer.calls)
	}
	regions, err := repositories.Regions().ListRegionsByConnection(ctx, "conn-a")
	if err != nil || len(regions) != 1 || regions[0].RegionID != "cn-hangzhou" {
		t.Fatalf("regions = %#v, err = %v", regions, err)
	}
	logs, err := repositories.Jobs().ListLogs(ctx, job.ID, 0, 10)
	if err != nil || len(logs) != 0 {
		t.Fatalf("logs = %#v, err = %v", logs, err)
	}
	if len(entries) != 1 || entries[0].Kind != execution.JobLogText || entries[0].Level != "info" ||
		entries[0].Message != "region discovery merged: 1 added, 0 updated, 0 missing; 1 active, 0 retired, 0 excluded" ||
		entries[0].Payload != nil {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestRefreshHandlerProviderFailurePreservesExistingRegions(t *testing.T) {
	ctx := context.Background()
	repositories, now := refreshFixture(t)
	seedRegions(t, repositories, asset.ConnectionRegion{ID: "existing", ConnectionID: "conn-a", RegionID: "cn-hangzhou", DiscoveredName: "old", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now})
	service, err := NewService(repositories)
	if err != nil {
		t.Fatal(err)
	}
	discoveryErr := errors.New("provider unavailable")
	handler := NewRefreshHandler(repositories, discovererRegistryStub{provider: asset.ProviderAliCloud, discoverer: &discovererStub{err: discoveryErr}}, service)
	if err := handler.Handle(ctx, execution.Job{ID: "refresh-failed", ConnectionID: "conn-a", Type: execution.JobRegionRefresh}); !errors.Is(err, discoveryErr) {
		t.Fatalf("Handle() error = %v", err)
	}
	stored, err := repositories.Regions().GetRegion(ctx, "existing")
	if err != nil || stored.DiscoveredName != "old" || !stored.UpdatedAt.Equal(now) {
		t.Fatalf("stored = %#v, err = %v", stored, err)
	}
}

func TestRefreshQueueAndHandlerRejectUnverifiedConnectionBeforeProviderCall(t *testing.T) {
	ctx := context.Background()
	repositories, _ := refreshFixture(t)
	connection, err := repositories.Connections().GetConnection(ctx, "conn-a")
	if err != nil {
		t.Fatal(err)
	}
	connection.Status = asset.ConnectionUnverified
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	queue, err := NewRefreshQueue(repositories)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queue.Enqueue(ctx, connection.ID); !errors.Is(err, asset.ErrConnectionNotValidated) {
		t.Fatalf("Enqueue() error = %v", err)
	}
	discoverer := &discovererStub{}
	service, err := NewService(repositories)
	if err != nil {
		t.Fatal(err)
	}
	handler := NewRefreshHandler(repositories, discovererRegistryStub{provider: asset.ProviderAliCloud, discoverer: discoverer}, service)
	if err := handler.Handle(ctx, execution.Job{ID: "refresh-unverified", ConnectionID: connection.ID, Type: execution.JobRegionRefresh}); !errors.Is(err, asset.ErrConnectionNotValidated) {
		t.Fatalf("Handle() error = %v", err)
	}
	if len(discoverer.calls) != 0 {
		t.Fatalf("discoverer calls = %#v", discoverer.calls)
	}
}

func TestRefreshQueueRollsBackWhenConnectionChangesDuringTransaction(t *testing.T) {
	ctx := context.Background()
	repositories, now := refreshFixture(t)
	conflicting := refreshConflictRepositories{Repositories: repositories}
	queue, err := NewRefreshQueue(conflicting)
	if err != nil {
		t.Fatal(err)
	}
	queue.now = func() time.Time { return now }
	queue.newID = func() string { return "refresh-conflict" }

	if _, err := queue.Enqueue(ctx, "conn-a"); !errors.Is(err, persistence.ErrConflict) {
		t.Fatalf("Enqueue() error = %v, want ErrConflict", err)
	}
	if _, err := repositories.Jobs().GetJob(ctx, "refresh-conflict"); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("job lookup error = %v", err)
	}
}

func refreshFixture(t *testing.T) (persistence.Repositories, time.Time) {
	t.Helper()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "refresh.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 21, 7, 0, 0, 0, time.UTC)
	if err := repositories.Connections().PutConnection(context.Background(), asset.CloudConnection{
		ID: "conn-a", Name: "a", Provider: asset.ProviderAliCloud, Partition: "public", Principal: "a", Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	return repositories, now
}

type refreshConflictRepositories struct {
	persistence.Repositories
}

func (r refreshConflictRepositories) WithTx(ctx context.Context, action func(persistence.Repositories) error) error {
	return r.Repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		return action(refreshConflictRepositories{Repositories: repositories})
	})
}

func (r refreshConflictRepositories) Connections() persistence.ConnectionRepository {
	return refreshConflictConnectionRepository{ConnectionRepository: r.Repositories.Connections()}
}

type refreshConflictConnectionRepository struct {
	persistence.ConnectionRepository
}

func (refreshConflictConnectionRepository) PutConnectionIfUnchanged(context.Context, asset.CloudConnection, time.Time) error {
	return persistence.ErrConflict
}
