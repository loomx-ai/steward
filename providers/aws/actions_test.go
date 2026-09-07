package aws_test

import (
	"context"
	"errors"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	provideraws "github.com/loomx-ai/steward/providers/aws"
)

func TestCloudFormationActionUsesControllerAPIAndIdempotencyToken(t *testing.T) {
	client := &actionCloudFormationClient{description: provideraws.StackDescription{Exists: true, Status: "CREATE_COMPLETE"}}
	driver := provideraws.NewCloudFormationAction(client)
	request := contracts.ActionRequest{
		Asset: awsStackAsset(), Action: "delete", IdempotencyKey: "execution-step-token",
		Parameters: map[string]any{"retain_resources": []any{"AuditBucket"}},
	}
	preflight, err := driver.Preflight(context.Background(), request)
	if err != nil || !preflight.Allowed {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if len(client.deleteCalls) != 1 || client.deleteCalls[0].ClientRequestToken != request.IdempotencyKey || len(client.deleteCalls[0].RetainResources) != 1 {
		t.Fatalf("delete calls = %+v", client.deleteCalls)
	}
	if result.ProviderRequestID != "cfn-delete-request" {
		t.Fatalf("action result = %+v", result)
	}
}

func TestCloudFormationWaiterTreatsNotFoundAsSuccessfulReadback(t *testing.T) {
	client := &actionCloudFormationClient{describeErr: &provideraws.APIError{Code: "ValidationError", Message: "Stack does not exist", StatusCode: 400, RequestID: "describe-missing"}}
	driver := provideraws.NewCloudFormationAction(client)
	request := contracts.ActionRequest{Asset: awsStackAsset(), Action: "delete", IdempotencyKey: "token"}
	wait, err := driver.Wait(context.Background(), request, contracts.ActionResult{})
	if err != nil || !wait.Done || wait.State != "absent" {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
	readback, err := driver.Readback(context.Background(), request)
	if err != nil || readback.Exists || readback.State != "absent" {
		t.Fatalf("readback=%+v err=%v", readback, err)
	}
}

func TestCloudFormationPreflightBlocksTerminationProtection(t *testing.T) {
	client := &actionCloudFormationClient{description: provideraws.StackDescription{Exists: true, Status: "CREATE_COMPLETE", TerminationProtected: true}}
	result, err := provideraws.NewCloudFormationAction(client).Preflight(context.Background(), contracts.ActionRequest{Asset: awsStackAsset(), Action: "delete", IdempotencyKey: "token"})
	if err != nil || result.Allowed || result.Reason == "" {
		t.Fatalf("preflight=%+v err=%v", result, err)
	}
}

func TestCloudFormationPreflightAllowsDeletionInProgress(t *testing.T) {
	client := &actionCloudFormationClient{description: provideraws.StackDescription{
		Exists: true, Status: "DELETE_IN_PROGRESS",
	}}
	driver := provideraws.NewCloudFormationAction(client)
	result, err := driver.Preflight(context.Background(), contracts.ActionRequest{
		Asset: awsStackAsset(), Action: "delete", IdempotencyKey: "token",
	})
	if err != nil || result.Absent || !result.Allowed {
		t.Fatalf("preflight=%+v err=%v", result, err)
	}
	if len(client.deleteCalls) != 0 {
		t.Fatalf("delete calls=%+v", client.deleteCalls)
	}
}

type actionCloudFormationClient struct {
	description provideraws.StackDescription
	describeErr error
	deleteCalls []provideraws.DeleteStackRequest
}

func (c *actionCloudFormationClient) ListStackResources(context.Context, provideraws.ListStackResourcesRequest) (provideraws.StackResourcePage, error) {
	return provideraws.StackResourcePage{}, nil
}

func (c *actionCloudFormationClient) DescribeStack(context.Context, string) (provideraws.StackDescription, string, error) {
	if c.describeErr != nil {
		var apiError *provideraws.APIError
		if errors.As(c.describeErr, &apiError) && apiError.Code == "ValidationError" {
			return provideraws.StackDescription{Exists: false}, apiError.RequestID, nil
		}
	}
	return c.description, "cfn-describe-request", c.describeErr
}

func (c *actionCloudFormationClient) DeleteStack(_ context.Context, request provideraws.DeleteStackRequest) (string, error) {
	c.deleteCalls = append(c.deleteCalls, request)
	return "cfn-delete-request", nil
}

func awsStackAsset() asset.Asset {
	return asset.Asset{ID: "stack", Location: "us-east-1", Identity: asset.Identity{
		Provider: asset.ProviderAWS, Partition: "aws", ConnectionID: "connection-aws",
		NativeType: provideraws.CloudFormationStackNativeType, NativeID: "arn:aws:cloudformation:us-east-1:123456789012:stack/application/id",
	}}
}
