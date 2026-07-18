package cleanup

import (
	"context"
	"time"

	"github.com/loomx-ai/steward/internal/core/execution"
)

type OutboxStore interface {
	ListPendingOutbox(context.Context, int) ([]execution.OutboxEvent, error)
	MarkOutboxPublished(context.Context, execution.OutboxEventID, time.Time) error
}

type EventPublisher interface {
	Publish(context.Context, execution.OutboxEvent) error
}

type OutboxDispatcher struct {
	store     OutboxStore
	publisher EventPublisher
	now       func() time.Time
}

func NewOutboxDispatcher(store OutboxStore, publisher EventPublisher, now func() time.Time) *OutboxDispatcher {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &OutboxDispatcher{store: store, publisher: publisher, now: now}
}

func (d *OutboxDispatcher) ProcessOne(ctx context.Context) (bool, error) {
	events, err := d.store.ListPendingOutbox(ctx, 1)
	if err != nil {
		return false, err
	}
	if len(events) == 0 {
		return false, nil
	}
	event := events[0]
	if err := d.publisher.Publish(ctx, event); err != nil {
		return true, err
	}
	if err := d.store.MarkOutboxPublished(ctx, event.ID, d.now()); err != nil {
		return true, err
	}
	return true, nil
}
