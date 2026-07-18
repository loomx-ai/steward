package execution

import (
	"context"
	"fmt"
)

type JobLogKind string

const (
	JobLogText             JobLogKind = "text"
	JobLogCloudAPIRequest  JobLogKind = "cloud_api_request"
	JobLogCloudAPIResponse JobLogKind = "cloud_api_response"
)

type JobLogEntry struct {
	Kind    JobLogKind
	Level   string
	Message string
	Payload map[string]any
}

type JobLogSink interface {
	Log(context.Context, JobLogEntry)
}

type JobLogSinkFunc func(context.Context, JobLogEntry)

func (f JobLogSinkFunc) Log(ctx context.Context, entry JobLogEntry) {
	if f != nil {
		f(ctx, entry)
	}
}

type jobLogSinkContextKey struct{}

func WithJobLogSink(ctx context.Context, sink JobLogSink) context.Context {
	if sink == nil {
		return ctx
	}
	return context.WithValue(ctx, jobLogSinkContextKey{}, sink)
}

func LogJob(ctx context.Context, level, message string) {
	logJobEntry(ctx, JobLogEntry{Kind: JobLogText, Level: level, Message: message})
}

func LogCloudAPIRequest(ctx context.Context, service, operation string, payload map[string]any) {
	logJobEntry(ctx, JobLogEntry{
		Kind: JobLogCloudAPIRequest, Level: "info",
		Message: fmt.Sprintf("call %s %s", service, operation), Payload: payload,
	})
}

func LogCloudAPIResponse(ctx context.Context, service, operation string, payload map[string]any) {
	logJobEntry(ctx, JobLogEntry{
		Kind: JobLogCloudAPIResponse, Level: "info",
		Message: fmt.Sprintf("%s %s returned", service, operation), Payload: payload,
	})
}

// LogCloudAPIFailure records a call that did not yield a provider response
// payload, keeping the original error in the terminal-style message. Cloud API
// traffic is always informational; the owning job records an error separately
// when the provider failure makes the operation fail.
func LogCloudAPIFailure(ctx context.Context, service, operation string, err error) {
	message := fmt.Sprintf("%s %s failed", service, operation)
	if err != nil {
		message += ": " + err.Error()
	}
	logJobEntry(ctx, JobLogEntry{
		Kind: JobLogCloudAPIResponse, Level: "info", Message: message,
	})
}

func logJobEntry(ctx context.Context, entry JobLogEntry) {
	if ctx == nil {
		return
	}
	sink, ok := ctx.Value(jobLogSinkContextKey{}).(JobLogSink)
	if !ok || sink == nil {
		return
	}
	sink.Log(ctx, entry)
}
