package aws

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	CloudFormationStackNativeType = "AWS::CloudFormation::Stack"
	CloudFormationLogicalIDTagKey = "aws:cloudformation:logical-id"
	CloudFormationStackIDTagKey   = "aws:cloudformation:stack-id"
	CloudFormationStackNameTagKey = "aws:cloudformation:stack-name"
)

const cloudFormationWaitInterval = 10 * time.Second

type ListStackResourcesRequest struct {
	StackID   string
	NextToken string
}

type StackResource struct {
	LogicalID  string `json:"logical_id"`
	PhysicalID string `json:"physical_id"`
	NativeType string `json:"native_type"`
	Status     string `json:"status"`
}

type StackResourcePage struct {
	RequestID string          `json:"request_id"`
	NextToken string          `json:"next_token,omitempty"`
	Resources []StackResource `json:"resources"`
}

type StackDescription struct {
	Exists               bool              `json:"exists"`
	ID                   string            `json:"id,omitempty"`
	Name                 string            `json:"name,omitempty"`
	Status               string            `json:"status,omitempty"`
	TerminationProtected bool              `json:"termination_protected"`
	Tags                 map[string]string `json:"tags,omitempty"`
}

type DeleteStackRequest struct {
	StackID            string
	ClientRequestToken string
	RetainResources    []string
	Force              bool
}

type CloudFormationClient interface {
	ListStackResources(context.Context, ListStackResourcesRequest) (StackResourcePage, error)
	DescribeStack(context.Context, string) (StackDescription, string, error)
	DeleteStack(context.Context, DeleteStackRequest) (string, error)
}

type CloudFormationAction struct {
	client CloudFormationClient
}

func NewCloudFormationAction(client CloudFormationClient) *CloudFormationAction {
	return &CloudFormationAction{client: client}
}

func (a *CloudFormationAction) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	if err := a.validate(request); err != nil {
		return contracts.PreflightResult{}, err
	}
	description, requestID, err := a.client.DescribeStack(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.PreflightResult{}, NormalizeError(err)
	}
	if !description.Exists {
		return contracts.PreflightResult{Absent: true, Reason: "stack no longer exists", Evidence: map[string]any{"provider_request_id": requestID}}, nil
	}
	if cloudFormationDeletionState(description.Status) {
		return contracts.PreflightResult{
			Absent: true,
			Reason: "stack deletion is already in progress or complete",
			Evidence: map[string]any{
				"provider_request_id": requestID, "stack_status": description.Status,
			},
		}, nil
	}
	if description.TerminationProtected {
		return contracts.PreflightResult{
			Allowed: false, Reason: "CloudFormation termination protection is enabled",
			Evidence: map[string]any{"provider_request_id": requestID, "stack_status": description.Status, "termination_protected": true},
		}, nil
	}
	return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{
		"provider_request_id": requestID, "stack_status": description.Status, "termination_protected": false,
	}}, nil
}

func cloudFormationDeletionState(state string) bool {
	switch strings.ToUpper(strings.TrimSpace(state)) {
	case "DELETE_IN_PROGRESS", "DELETE_COMPLETE":
		return true
	default:
		return false
	}
}

func (a *CloudFormationAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.validate(request); err != nil {
		return contracts.ActionResult{}, err
	}
	providerRequestID, err := a.client.DeleteStack(ctx, DeleteStackRequest{
		StackID: request.Asset.Identity.NativeID, ClientRequestToken: request.IdempotencyKey,
		RetainResources: stringSlice(request.Parameters["retain_resources"]), Force: boolValue(request.Parameters["force"]),
	})
	if err != nil {
		return contracts.ActionResult{}, NormalizeError(err)
	}
	return contracts.ActionResult{
		ProviderRequestID: providerRequestID, ProviderOperationID: request.IdempotencyKey,
		RetryAfter: cloudFormationWaitInterval,
	}, nil
}

func (a *CloudFormationAction) Wait(ctx context.Context, request contracts.ActionRequest, _ contracts.ActionResult) (contracts.WaitResult, error) {
	readback, err := a.Readback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !readback.Exists || strings.EqualFold(readback.State, "DELETE_COMPLETE") {
		return contracts.WaitResult{Done: true, State: readback.State}, nil
	}
	if strings.EqualFold(readback.State, "DELETE_FAILED") {
		return contracts.WaitResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{
			Category: execution.ErrorProviderFailure, Code: "DeleteFailed", Message: "CloudFormation stack deletion failed",
		}}
	}
	return contracts.WaitResult{Done: false, RetryAfter: cloudFormationWaitInterval, State: readback.State}, nil
}

func (a *CloudFormationAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.validate(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	description, requestID, err := a.client.DescribeStack(ctx, request.Asset.Identity.NativeID)
	if err != nil {
		return contracts.ReadbackResult{}, NormalizeError(err)
	}
	if !description.Exists {
		return contracts.ReadbackResult{Exists: false, State: "absent", Data: map[string]any{"provider_request_id": requestID}}, nil
	}
	return contracts.ReadbackResult{Exists: true, State: description.Status, Data: map[string]any{
		"provider_request_id": requestID, "stack_id": description.ID, "stack_name": description.Name,
		"termination_protected": description.TerminationProtected,
	}}, nil
}

func (a *CloudFormationAction) validate(request contracts.ActionRequest) error {
	if a == nil || a.client == nil {
		return fmt.Errorf("AWS CloudFormation client is required")
	}
	if request.Asset.Identity.Provider != asset.ProviderAWS || request.Asset.Identity.NativeType != CloudFormationStackNativeType {
		return fmt.Errorf("AWS CloudFormation action requires a stack asset")
	}
	if request.Action != "delete" || strings.TrimSpace(request.Asset.Identity.NativeID) == "" || strings.TrimSpace(request.IdempotencyKey) == "" {
		return fmt.Errorf("AWS CloudFormation delete requires stack ID and idempotency key")
	}
	return nil
}

func stringSlice(value any) []string {
	result := []string{}
	switch typed := value.(type) {
	case []string:
		for _, item := range typed {
			if item = strings.TrimSpace(item); item != "" {
				result = append(result, item)
			}
		}
	case []any:
		for _, item := range typed {
			if text, ok := item.(string); ok && strings.TrimSpace(text) != "" {
				result = append(result, strings.TrimSpace(text))
			}
		}
	}
	return result
}

func boolValue(value any) bool {
	result, _ := value.(bool)
	return result
}
