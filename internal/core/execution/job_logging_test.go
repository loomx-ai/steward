package execution_test

import (
	"context"
	"errors"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
)

func TestLogJobWithoutContextSinkIsNoop(t *testing.T) {
	execution.LogJob(context.Background(), "info", "ignored")
}

func TestLogJobUsesTextEntryWithoutPayload(t *testing.T) {
	var got execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			got = entry
		},
	))

	execution.LogJob(ctx, "info", "job claimed by worker-1")

	if got.Kind != execution.JobLogText ||
		got.Level != "info" ||
		got.Message != "job claimed by worker-1" ||
		got.Payload != nil {
		t.Fatalf("entry = %#v", got)
	}
}

func TestCloudAPILogHelpersUseStructuredKinds(t *testing.T) {
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, entry)
		},
	))

	execution.LogCloudAPIRequest(ctx, "resource-center", "SearchResources", map[string]any{"MaxResults": 100})
	execution.LogCloudAPIResponse(ctx, "resource-center", "SearchResources", map[string]any{"RequestId": "req-1"})
	execution.LogCloudAPIFailure(ctx, "vpc", "DescribeVSwitches", errors.New("EOF"))

	if len(logs) != 3 ||
		logs[0].Kind != execution.JobLogCloudAPIRequest ||
		logs[0].Message != "call resource-center SearchResources" ||
		logs[0].Payload["MaxResults"] != 100 ||
		logs[1].Kind != execution.JobLogCloudAPIResponse ||
		logs[1].Level != "info" ||
		logs[1].Message != "resource-center SearchResources returned" ||
		logs[1].Payload["RequestId"] != "req-1" ||
		logs[2].Kind != execution.JobLogCloudAPIResponse ||
		logs[2].Level != "info" ||
		logs[2].Message != "vpc DescribeVSwitches failed: EOF" ||
		logs[2].Payload != nil {
		t.Fatalf("logs = %#v", logs)
	}
}
