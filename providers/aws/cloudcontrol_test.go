package aws_test

import (
	"context"
	"errors"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	provideraws "github.com/loomx-ai/steward/providers/aws"
)

func TestCloudControlInventoryUsesPrimaryIdentifierAndResourceModel(t *testing.T) {
	client := &cloudControlClient{page: provideraws.CloudControlPage{
		RequestID: "list-request", NextToken: "next-page",
		Resources: []provideraws.CloudControlResource{{
			Identifier: "orders", Properties: `{"TableName":"orders","TableStatus":"ACTIVE","Arn":"arn:aws:dynamodb:us-east-1:123456789012:table/orders","Tags":[{"Key":"env","Value":"prod"}]}`,
		}},
	}}
	kind := asset.ResourceKind{
		ID: "aws:AWS::DynamoDB::Table", Provider: asset.ProviderAWS, NativeType: "AWS::DynamoDB::Table",
		Capabilities: asset.CapabilitySet{asset.CapabilityIndexed, asset.CapabilityDetailed, asset.CapabilityActionable},
	}
	batch, err := provideraws.NewCloudControlInventory(client).List(context.Background(), contracts.InventoryRequest{
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1", Name: "US East", Location: "us-east-1"},
		ResourceKind: &kind, Cursor: "page-1", Limit: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.listCalls) != 1 || client.listCalls[0].TypeName != kind.NativeType || client.listCalls[0].NextToken != "page-1" || client.listCalls[0].Limit != 25 {
		t.Fatalf("list calls = %+v", client.listCalls)
	}
	if len(batch.Items) != 1 || batch.Complete || batch.NextCursor != "next-page" || batch.RequestID != "list-request" {
		t.Fatalf("batch = %+v", batch)
	}
	item := batch.Items[0]
	if item.NativeID != "orders" || item.Name != "orders" || item.Location != "us-east-1" || item.Tags["env"] != "prod" || item.Normalized["cloudControlIdentifier"] != "orders" {
		t.Fatalf("item = %+v", item)
	}
	if len(item.NativeAliases) != 2 || item.NativeAliases[0] != "arn:aws:dynamodb:us-east-1:123456789012:table/orders" || item.NativeAliases[1] != "orders" {
		t.Fatalf("aliases = %+v", item.NativeAliases)
	}
}

func TestCloudControlInventoryRejectsMalformedResourceModel(t *testing.T) {
	client := &cloudControlClient{page: provideraws.CloudControlPage{Resources: []provideraws.CloudControlResource{{Identifier: "broken", Properties: "{"}}}}
	kind := asset.ResourceKind{NativeType: "AWS::DynamoDB::Table"}
	_, err := provideraws.NewCloudControlInventory(client).List(context.Background(), contracts.InventoryRequest{
		Scope: asset.Scope{Kind: asset.ScopeRegion}, ResourceKind: &kind,
	})
	if err == nil {
		t.Fatal("malformed Cloud Control resource model must fail the shard")
	}
}

func TestCloudControlActionRunsPreflightDeleteWaitAndReadback(t *testing.T) {
	client := &cloudControlClient{
		resource:       provideraws.CloudControlResource{Identifier: "orders", Properties: `{"TableName":"orders","TableStatus":"ACTIVE"}`},
		deleteProgress: provideraws.CloudControlProgress{RequestToken: "operation-token", Identifier: "orders", Status: "IN_PROGRESS"},
		waitProgress:   provideraws.CloudControlProgress{RequestToken: "operation-token", Identifier: "orders", Status: "SUCCESS"},
	}
	driver := provideraws.NewCloudControlAction(client)
	request := cloudControlActionRequest()
	preflight, err := driver.Preflight(context.Background(), request)
	if err != nil || !preflight.Allowed {
		t.Fatalf("preflight=%+v err=%v", preflight, err)
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderRequestID != "delete-request" || result.ProviderOperationID != "operation-token" || len(client.deleteCalls) != 1 || client.deleteCalls[0].clientToken != request.IdempotencyKey {
		t.Fatalf("result=%+v delete calls=%+v", result, client.deleteCalls)
	}
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || wait.State != "SUCCESS" || len(client.waitCalls) != 1 || client.waitCalls[0] != "operation-token" {
		t.Fatalf("wait=%+v calls=%+v err=%v", wait, client.waitCalls, err)
	}
	client.getErr = &provideraws.APIError{Code: "ResourceNotFoundException", Message: "not found", StatusCode: 404, RequestID: "read-missing"}
	readback, err := driver.Readback(context.Background(), request)
	if err != nil || readback.Exists || readback.State != "absent" {
		t.Fatalf("readback=%+v err=%v", readback, err)
	}
}

func TestCloudControlActionRequiresAuthoritativeIdentifierForARNAssets(t *testing.T) {
	request := cloudControlActionRequest()
	request.Asset.Identity.NativeID = "arn:aws:dynamodb:us-east-1:123456789012:table/orders"
	request.Asset.Normalized = nil
	_, err := provideraws.NewCloudControlAction(&cloudControlClient{}).Preflight(context.Background(), request)
	if err == nil {
		t.Fatal("Resource Explorer ARN must not be guessed as a Cloud Control identifier")
	}
}

func TestCloudControlActionTreatsDeleteRaceAsIdempotentSuccess(t *testing.T) {
	client := &cloudControlClient{deleteProgress: provideraws.CloudControlProgress{Status: "FAILED", ErrorCode: "NotFound", Message: "already gone"}}
	driver := provideraws.NewCloudControlAction(client)
	request := cloudControlActionRequest()
	result, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || !wait.Done {
		t.Fatalf("wait=%+v err=%v", wait, err)
	}
}

type cloudControlDeleteCall struct {
	typeName    string
	identifier  string
	clientToken string
}

type cloudControlClient struct {
	page           provideraws.CloudControlPage
	resource       provideraws.CloudControlResource
	getErr         error
	deleteProgress provideraws.CloudControlProgress
	waitProgress   provideraws.CloudControlProgress
	listCalls      []provideraws.CloudControlListRequest
	deleteCalls    []cloudControlDeleteCall
	waitCalls      []string
}

func (c *cloudControlClient) ListResources(_ context.Context, request provideraws.CloudControlListRequest) (provideraws.CloudControlPage, error) {
	c.listCalls = append(c.listCalls, request)
	return c.page, nil
}

func (c *cloudControlClient) GetResource(_ context.Context, _, _ string) (provideraws.CloudControlResource, string, error) {
	if c.getErr != nil {
		var apiError *provideraws.APIError
		if errors.As(c.getErr, &apiError) {
			return provideraws.CloudControlResource{}, apiError.RequestID, c.getErr
		}
	}
	return c.resource, "get-request", c.getErr
}

func (c *cloudControlClient) DeleteResource(_ context.Context, typeName, identifier, clientToken string) (provideraws.CloudControlProgress, string, error) {
	c.deleteCalls = append(c.deleteCalls, cloudControlDeleteCall{typeName: typeName, identifier: identifier, clientToken: clientToken})
	return c.deleteProgress, "delete-request", nil
}

func (c *cloudControlClient) GetResourceRequestStatus(_ context.Context, token string) (provideraws.CloudControlProgress, string, error) {
	c.waitCalls = append(c.waitCalls, token)
	return c.waitProgress, "wait-request", nil
}

func cloudControlActionRequest() contracts.ActionRequest {
	return contracts.ActionRequest{
		Asset: asset.Asset{Location: "us-east-1", Normalized: map[string]any{"cloudControlIdentifier": "orders"}, Identity: asset.Identity{
			Provider: asset.ProviderAWS, ConnectionID: "connection-aws", NativeType: "AWS::DynamoDB::Table", NativeID: "orders",
		}},
		Action: "delete", IdempotencyKey: "execution-step-token",
	}
}
