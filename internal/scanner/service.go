package scanner

import (
	"context"
	"fmt"

	"github.com/prodesire/cloud-steward/internal/domain"
	"github.com/prodesire/cloud-steward/internal/store"
)

type Service struct {
	repo     store.Repository
	registry Registry
}

func NewService(repo store.Repository, registry Registry) *Service {
	return &Service{repo: repo, registry: registry}
}

func (s *Service) ProcessNext(ctx context.Context) (bool, error) {
	job, ok, err := s.repo.ClaimNextPendingScanJob(ctx)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}

	if err := s.processJob(ctx, job); err != nil {
		_ = s.repo.MarkScanJobFailed(ctx, job.ID, err.Error())
	}
	return true, nil
}

func (s *Service) processJob(ctx context.Context, job domain.ScanJob) error {
	connector, ok := s.registry[job.Mode]
	if !ok || connector == nil {
		return fmt.Errorf("no connector registered for mode %q", job.Mode)
	}
	account, err := s.repo.GetAccount(ctx, job.AccountID)
	if err != nil {
		return fmt.Errorf("load account: %w", err)
	}

	resources := make([]domain.Resource, 0)
	for _, region := range job.Regions {
		scanned, err := connector.ScanRegion(ctx, account, job, region)
		if err != nil {
			return err
		}
		resources = append(resources, scanned...)
	}
	if err := s.repo.UpsertResources(ctx, job.ID, resources); err != nil {
		return fmt.Errorf("store resources: %w", err)
	}
	return s.repo.MarkScanJobSucceeded(ctx, job.ID, len(resources))
}
