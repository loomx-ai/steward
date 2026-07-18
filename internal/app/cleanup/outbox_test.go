package cleanup_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/app/cleanup"
	"github.com/loomx-ai/steward/internal/core/execution"
)

func TestOutboxMarksEventOnlyAfterPublish(t *testing.T) {
	now := time.Date(2026, 7, 13, 5, 3, 0, 0, time.UTC)
	store := &fakeOutboxStore{events: []execution.OutboxEvent{{ID: "event-1", Topic: "scan.completed", AggregateID: "scan-1", CreatedAt: now}}}
	publisher := &recordingPublisher{}
	dispatcher := cleanup.NewOutboxDispatcher(store, publisher, func() time.Time { return now })
	processed, err := dispatcher.ProcessOne(context.Background())
	if err != nil || !processed || len(publisher.events) != 1 || store.publishedID != "event-1" || !store.publishedAt.Equal(now) {
		t.Fatalf("processed=%v events=%#v published=%q at=%s err=%v", processed, publisher.events, store.publishedID, store.publishedAt, err)
	}
}

func TestOutboxLeavesEventPendingWhenPublishFails(t *testing.T) {
	store := &fakeOutboxStore{events: []execution.OutboxEvent{{ID: "event-1", Topic: "scan.completed"}}}
	dispatcher := cleanup.NewOutboxDispatcher(store, &recordingPublisher{err: errors.New("offline")}, time.Now)
	processed, err := dispatcher.ProcessOne(context.Background())
	if !processed || err == nil || store.publishedID != "" {
		t.Fatalf("processed=%v published=%q err=%v", processed, store.publishedID, err)
	}
}

type fakeOutboxStore struct {
	events      []execution.OutboxEvent
	publishedID execution.OutboxEventID
	publishedAt time.Time
}

func (f *fakeOutboxStore) ListPendingOutbox(context.Context, int) ([]execution.OutboxEvent, error) {
	return f.events, nil
}

func (f *fakeOutboxStore) MarkOutboxPublished(_ context.Context, id execution.OutboxEventID, publishedAt time.Time) error {
	f.publishedID = id
	f.publishedAt = publishedAt
	return nil
}

type recordingPublisher struct {
	events []execution.OutboxEvent
	err    error
}

func (p *recordingPublisher) Publish(_ context.Context, event execution.OutboxEvent) error {
	if p.err != nil {
		return p.err
	}
	p.events = append(p.events, event)
	return nil
}
