package events

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/loomx-ai/steward/internal/core/execution"
)

type JobEventStore interface {
	GetJob(context.Context, execution.JobID) (execution.Job, error)
	ListLogs(context.Context, execution.JobID, int64, int) ([]execution.JobLog, error)
}

type JobEventHandler struct {
	store        JobEventStore
	pollInterval time.Duration
}

func NewJobEventHandler(store JobEventStore, pollInterval time.Duration) *JobEventHandler {
	if pollInterval <= 0 {
		pollInterval = time.Second
	}
	return &JobEventHandler{store: store, pollInterval: pollInterval}
}

func (h *JobEventHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	jobID := execution.JobID(r.PathValue("id"))
	if jobID == "" {
		http.Error(w, "missing job id", http.StatusBadRequest)
		return
	}
	after, err := parseLastEventID(r.Header.Get("Last-Event-ID"))
	if err != nil {
		http.Error(w, "invalid Last-Event-ID", http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	flusher, _ := w.(http.Flusher)
	for {
		logs, err := h.store.ListLogs(r.Context(), jobID, after, 100)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		for _, event := range logs {
			payload, err := json.Marshal(event)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			_, _ = fmt.Fprintf(w, "id: %d\nevent: log\ndata: %s\n\n", event.Sequence, payload)
			after = event.Sequence
		}
		if flusher != nil {
			flusher.Flush()
		}
		job, err := h.store.GetJob(r.Context(), jobID)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if job.Status == execution.JobSucceeded || job.Status == execution.JobFailed {
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(h.pollInterval):
		}
	}
}

func parseLastEventID(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	return strconv.ParseInt(value, 10, 64)
}
