package region

import (
	"context"
	"errors"
	"fmt"
	"time"

	connectionapp "github.com/loomx-ai/steward/internal/app/connection"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/requestmeta"
	"github.com/loomx-ai/steward/internal/idgen"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type RefreshQueue struct {
	repositories persistence.Repositories
	now          func() time.Time
	newID        func() string
}

func NewRefreshQueue(repositories persistence.Repositories) (*RefreshQueue, error) {
	if repositories == nil {
		return nil, fmt.Errorf("region refresh repositories are required")
	}
	return &RefreshQueue{repositories: repositories, now: func() time.Time { return time.Now().UTC() }, newID: func() string { return idgen.MustNew("job") }}, nil
}

func (q *RefreshQueue) Enqueue(ctx context.Context, connectionID asset.ConnectionID) (execution.Job, error) {
	if connectionID == "" {
		return execution.Job{}, persistence.ErrNotFound
	}
	connection, err := q.repositories.Connections().GetConnection(ctx, connectionID)
	if err != nil {
		return execution.Job{}, err
	}
	if connection.Status == asset.ConnectionDeleted {
		return execution.Job{}, persistence.ErrNotFound
	}
	if connection.Status != asset.ConnectionActive {
		return execution.Job{}, asset.ErrConnectionNotValidated
	}
	if existing, err := q.repositories.Jobs().FindActiveByType(ctx, connectionID, execution.JobRegionRefresh); err == nil {
		return existing, nil
	} else if !errors.Is(err, persistence.ErrNotFound) {
		return execution.Job{}, err
	}

	var queued execution.Job
	err = q.repositories.WithTx(ctx, func(repositories persistence.Repositories) error {
		if existing, err := repositories.Jobs().FindActiveByType(ctx, connectionID, execution.JobRegionRefresh); err == nil {
			queued = existing
			return nil
		} else if !errors.Is(err, persistence.ErrNotFound) {
			return err
		}
		now := q.now()
		if err := connectionapp.GuardActiveWork(ctx, repositories, connectionID, now); err != nil {
			return err
		}
		payload := map[string]any{"connection_id": string(connectionID)}
		if requestID := requestmeta.RequestID(ctx); requestID != "" {
			payload["request_id"] = requestID
		}
		queued = execution.Job{
			ID: execution.JobID(q.newID()), ConnectionID: connectionID, Type: execution.JobRegionRefresh, Status: execution.JobPending,
			Payload: payload, RunAt: now, CreatedAt: now, UpdatedAt: now,
		}
		return repositories.Jobs().Enqueue(ctx, queued)
	})
	if err == nil {
		return queued, nil
	}
	if errors.Is(err, persistence.ErrConflict) {
		if existing, findErr := q.repositories.Jobs().FindActiveByType(ctx, connectionID, execution.JobRegionRefresh); findErr == nil {
			return existing, nil
		}
	}
	return execution.Job{}, err
}

type RegionRuntimeRegistry interface {
	ResolveRegionDiscoverer(asset.Provider) (contracts.RegionDiscoverer, error)
}

type RefreshHandler struct {
	repositories persistence.Repositories
	runtimes     RegionRuntimeRegistry
	service      *Service
}

func NewRefreshHandler(repositories persistence.Repositories, runtimes RegionRuntimeRegistry, service *Service) *RefreshHandler {
	return &RefreshHandler{repositories: repositories, runtimes: runtimes, service: service}
}

func (h *RefreshHandler) Handle(ctx context.Context, job execution.Job) error {
	if h == nil || h.repositories == nil || h.runtimes == nil || h.service == nil || job.ConnectionID == "" || job.Type != execution.JobRegionRefresh {
		return fmt.Errorf("region refresh handler requires a region refresh job, connection, repositories, runtime, and service")
	}
	connection, err := h.repositories.Connections().GetConnection(ctx, job.ConnectionID)
	if err != nil {
		return err
	}
	if connection.Status == asset.ConnectionDeleted {
		return persistence.ErrNotFound
	}
	if connection.Status != asset.ConnectionActive {
		return asset.ErrConnectionNotValidated
	}
	discoverer, err := h.runtimes.ResolveRegionDiscoverer(connection.Provider)
	if err != nil {
		return err
	}
	discovered, err := discoverer.DiscoverRegions(ctx, connection.ID)
	if err != nil {
		return err
	}
	summary, err := h.service.Refresh(ctx, connection.ID, discovered, "system", "")
	if err != nil {
		return err
	}
	execution.LogJob(ctx, "info", fmt.Sprintf(
		"region discovery merged: %d added, %d updated, %d missing; %d active, %d retired, %d excluded",
		summary.Added, summary.Updated, summary.Missing, summary.Active, summary.Retired, summary.Excluded,
	))
	return nil
}
