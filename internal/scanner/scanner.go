package scanner

import (
	"context"

	"github.com/prodesire/cloud-steward/internal/domain"
)

type Connector interface {
	ScanRegion(ctx context.Context, account domain.Account, job domain.ScanJob, region string) ([]domain.Resource, error)
}

type Registry map[domain.ScanMode]Connector
