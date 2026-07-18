package connection

import (
	"context"
	"fmt"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
)

// GuardActiveWork serializes task acceptance with connection state changes.
// Call it inside the same transaction that persists the task.
func GuardActiveWork(
	ctx context.Context,
	repositories persistence.Repositories,
	connectionID asset.ConnectionID,
	now time.Time,
) error {
	value, err := repositories.Connections().GetConnection(ctx, connectionID)
	if err != nil {
		return err
	}
	if value.Status == asset.ConnectionDeleted {
		return persistence.ErrNotFound
	}
	if value.Status != asset.ConnectionActive {
		return fmt.Errorf("%w: connection %q status is %q", asset.ErrConnectionNotValidated, value.ID, value.Status)
	}
	expectedUpdatedAt := value.UpdatedAt
	value.UpdatedAt = nextConnectionUpdatedAt(now, expectedUpdatedAt)
	return repositories.Connections().PutConnectionIfUnchanged(ctx, value, expectedUpdatedAt)
}

func nextConnectionUpdatedAt(now, current time.Time) time.Time {
	const databasePrecision = time.Microsecond
	candidate := now.UTC().Truncate(databasePrecision)
	current = current.UTC().Truncate(databasePrecision)
	if !candidate.After(current) {
		return current.Add(databasePrecision)
	}
	return candidate
}
