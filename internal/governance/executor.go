package governance

import (
	"context"
	"errors"
	"strings"

	"github.com/prodesire/cloud-steward/internal/domain"
)

type TagClient interface {
	TagResource(context.Context, domain.Resource, map[string]string) (string, error)
}

type TagExecutor struct {
	Client TagClient
}

func (TagExecutor) DryRun() bool {
	return false
}

func (e TagExecutor) ExecutePlanItem(ctx context.Context, request ExecutionRequest) (ExecutionResult, error) {
	if !strings.EqualFold(strings.TrimSpace(request.Item.Action), "tag") {
		return ExecutionResult{Result: "skipped", RequestID: requestID()}, nil
	}
	if e.Client == nil {
		return ExecutionResult{}, errors.New("tag executor requires a tag client")
	}
	requestID, err := e.Client.TagResource(ctx, request.Resource, planItemTags(request))
	if err != nil {
		return ExecutionResult{}, err
	}
	return ExecutionResult{Result: "tagged", RequestID: requestID}, nil
}

func planItemTags(request ExecutionRequest) map[string]string {
	return map[string]string{
		"cloud-steward:managed-by":   "cloud-steward",
		"cloud-steward:plan-id":      request.Plan.ID,
		"cloud-steward:plan-item-id": request.Item.ID,
		"cloud-steward:action":       strings.TrimSpace(request.Item.Action),
	}
}
