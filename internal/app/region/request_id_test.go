package region

import (
	"context"
	"testing"

	"github.com/loomx-ai/steward/internal/core/requestmeta"
)

func TestRefreshQueuePersistsInitiatingRequestID(t *testing.T) {
	repositories, _ := refreshFixture(t)
	queue, err := NewRefreshQueue(repositories)
	if err != nil {
		t.Fatal(err)
	}

	job, err := queue.Enqueue(
		requestmeta.WithRequestID(context.Background(), "application-request-id"),
		"conn-a",
	)
	if err != nil || job.Payload["request_id"] != "application-request-id" {
		t.Fatalf("job=%#v err=%v", job, err)
	}
}
