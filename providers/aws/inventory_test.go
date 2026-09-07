package aws_test

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	provideraws "github.com/loomx-ai/steward/providers/aws"
)

func TestInventoryMapsPagedGlobalAndRegionalResources(t *testing.T) {
	page := readSearchPage(t)
	client := &resourceExplorerClient{page: page}
	inventory := provideraws.NewInventory(client, nil)

	batch, err := inventory.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-aws",
		Scope:        asset.Scope{Kind: asset.ScopeAccount, NativeID: "123456789012"}, Cursor: "aws-page-1", Limit: 500,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 || client.requests[0].NextToken != "aws-page-1" || client.requests[0].MaxResults != provideraws.ResourceExplorerPageLimit {
		t.Fatalf("request = %+v", client.requests)
	}
	if batch.NextCursor != "aws-page-2" || batch.RequestID != "aws-search-request-1" || batch.Complete || len(batch.Items) != 2 {
		t.Fatalf("batch = %+v", batch)
	}
	global, regional := batch.Items[0], batch.Items[1]
	if global.NativeID != "arn:aws:iam::123456789012:role/Admin" || global.Scope.Kind != asset.ScopeGlobal || global.ResourceKind.NativeType != "AWS::IAM::Role" {
		t.Fatalf("global item = %+v", global)
	}
	if regional.Scope.Kind != asset.ScopeRegion || regional.Scope.NativeID != "us-east-1" || regional.State != "running" || regional.Tags["Name"] != "worker" {
		t.Fatalf("regional item = %+v", regional)
	}
	if _, sdkTypeLeaked := regional.Raw["ResultMetadata"]; sdkTypeLeaked || regional.Raw["arn"] == nil || regional.Normalized["accountId"] != "123456789012" {
		t.Fatalf("raw/normalized separation = raw:%+v normalized:%+v", regional.Raw, regional.Normalized)
	}
	if !global.ResourceKind.Capabilities.Has(asset.CapabilityIndexed) || global.ResourceKind.Capabilities.Has(asset.CapabilityActionable) {
		t.Fatalf("catalog-only capabilities = %v", global.ResourceKind.Capabilities)
	}
}

func TestInventoryLogsRawResourceExplorerRequestAndResponse(t *testing.T) {
	client := &resourceExplorerClient{page: readSearchPage(t)}
	var logs []capturedJobLog
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, capturedJobLog{kind: entry.Kind, level: entry.Level, message: entry.Message, payload: entry.Payload})
		},
	))

	_, err := provideraws.NewInventory(client, nil).List(ctx, contracts.InventoryRequest{
		ConnectionID: "connection-aws",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1"},
		ResourceKind: &asset.ResourceKind{NativeType: "AWS::EC2::Instance"},
		Cursor:       "aws-page-1",
		Limit:        100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 2 ||
		logs[0].kind != execution.JobLogCloudAPIRequest ||
		logs[0].message != "call resource-explorer-2 Search" ||
		logs[1].kind != execution.JobLogCloudAPIResponse ||
		logs[1].message != "resource-explorer-2 Search returned" {
		t.Fatalf("logs=%#v", logs)
	}
	if logs[0].payload["MaxResults"] != float64(100) || logs[0].payload["NextToken"] != "aws-page-1" {
		t.Fatalf("request log=%#v", logs[0])
	}
	filters, ok := logs[0].payload["Filters"].(map[string]any)
	if !ok || filters["FilterString"] != "region:us-east-1 resourcetype:ec2:instance" {
		t.Fatalf("request filters=%#v", logs[0].payload["Filters"])
	}
	if logs[1].payload["RequestId"] != "aws-search-request-1" || logs[1].payload["NextToken"] != "aws-page-2" {
		t.Fatalf("response log=%#v", logs[1])
	}
	resources, ok := logs[1].payload["Resources"].([]any)
	if !ok || len(resources) != 2 {
		t.Fatalf("response resources=%#v", logs[1].payload["Resources"])
	}
	encoded, err := json.Marshal(logs[1].payload)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"Properties"`) || !strings.Contains(string(encoded), "platform") {
		t.Fatalf("raw response fields missing: %s", encoded)
	}
}

func TestInventoryLogsResourceExplorerFailureWithoutFabricatedResponse(t *testing.T) {
	client := &resourceExplorerClient{err: &provideraws.APIError{
		Code: "AccessDeniedException", Message: "denied", RequestID: "aws-denied", StatusCode: 403,
	}}
	var logs []capturedJobLog
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, capturedJobLog{kind: entry.Kind, level: entry.Level, message: entry.Message, payload: entry.Payload})
		},
	))

	_, err := provideraws.NewInventory(client, nil).List(ctx, contracts.InventoryRequest{Scope: asset.Scope{Kind: asset.ScopeAccount, NativeID: "123456789012"}, Limit: 100})
	if err == nil {
		t.Fatal("expected provider error")
	}
	if len(logs) != 2 ||
		logs[1].kind != execution.JobLogCloudAPIResponse ||
		logs[1].level != "info" ||
		logs[1].message != "resource-explorer-2 Search failed: AccessDeniedException: denied" ||
		logs[1].payload != nil {
		t.Fatalf("logs=%#v", logs)
	}
}

func TestInventoryScopesSearchToSelectedRegion(t *testing.T) {
	client := &resourceExplorerClient{page: contractsToEmptySearchPage()}
	_, err := provideraws.NewInventory(client, nil).List(context.Background(), contracts.InventoryRequest{
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-west-2"},
		ResourceKind: &asset.ResourceKind{NativeType: "AWS::EC2::Instance"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 || client.requests[0].Query != "region:us-west-2 resourcetype:ec2:instance" {
		t.Fatalf("requests = %+v", client.requests)
	}
}

func TestInventoryExposesPhysicalAliasesAndNetworkReferences(t *testing.T) {
	t.Parallel()

	client := &resourceExplorerClient{page: provideraws.SearchPage{Resources: []provideraws.SearchResource{{
		ARN: "arn:aws:ec2:us-east-1:123456789012:instance/i-123", NativeType: "AWS::EC2::Instance", Region: "us-east-1", AccountID: "123456789012",
		Properties: map[string]any{"VpcId": "vpc-1", "SubnetId": "subnet-1", "attachment": map[string]any{"NetworkInterfaceId": "eni-1"}},
	}}}}
	batch, err := provideraws.NewInventory(client, nil).List(context.Background(), contracts.InventoryRequest{Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "us-east-1"}})
	if err != nil {
		t.Fatal(err)
	}
	item := batch.Items[0]
	if !containsString(item.NativeAliases, "i-123") || !containsString(item.NetworkReferences, "vpc-1") || !containsString(item.NetworkReferences, "subnet-1") || !containsString(item.NetworkReferences, "eni-1") {
		t.Fatalf("aliases=%v references=%v", item.NativeAliases, item.NetworkReferences)
	}
}

func TestInventoryScopesSearchToGlobalResources(t *testing.T) {
	client := &resourceExplorerClient{page: contractsToEmptySearchPage()}
	_, err := provideraws.NewInventory(client, nil).List(context.Background(), contracts.InventoryRequest{
		Scope:        asset.Scope{Kind: asset.ScopeGlobal, NativeID: "123456789012/global", Location: "us-east-1"},
		ResourceKind: &asset.ResourceKind{NativeType: "AWS::IAM::Role"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 || client.requests[0].Query != "region:global resourcetype:iam:role" {
		t.Fatalf("requests = %+v", client.requests)
	}
}

func contractsToEmptySearchPage() provideraws.SearchPage { return provideraws.SearchPage{} }

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestInventoryNormalizesPermissionAndThrottlingErrors(t *testing.T) {
	for _, test := range []struct {
		name     string
		err      error
		category execution.ErrorCategory
	}{
		{name: "permission", err: &provideraws.APIError{Code: "AccessDeniedException", Message: "denied", RequestID: "aws-denied", StatusCode: 403}, category: execution.ErrorPermissionDenied},
		{name: "throttling", err: &provideraws.APIError{Code: "ThrottlingException", Message: "slow down", RequestID: "aws-throttle", StatusCode: 429, RetryAfter: 3 * time.Second}, category: execution.ErrorThrottled},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := provideraws.NewInventory(&resourceExplorerClient{err: test.err}, nil).List(context.Background(), contracts.InventoryRequest{Scope: asset.Scope{Kind: asset.ScopeAccount, NativeID: "123456789012"}})
			var normalized *contracts.ProviderCallError
			if !errors.As(err, &normalized) || normalized.Provider.Category != test.category || normalized.Provider.RequestID == "" {
				t.Fatalf("normalized error = %#v", err)
			}
		})
	}
}

func TestInventoryClassifiesOnlyExplicitUnsupportedErrorsAsSkippable(t *testing.T) {
	for _, test := range []struct {
		code   string
		reason asset.SkipReason
	}{
		{code: "UnsupportedOperation", reason: asset.SkipProductUnsupported},
		{code: "UnsupportedActionException", reason: asset.SkipProductUnsupported},
		{code: "TypeNotFoundException", reason: asset.SkipProductUnsupported},
		{code: "OptInRequired", reason: asset.SkipProviderRegionUnavailable},
	} {
		_, err := provideraws.NewInventory(&resourceExplorerClient{err: &provideraws.APIError{Code: test.code, Message: "unsupported", StatusCode: 400}}, nil).List(context.Background(), contracts.InventoryRequest{Scope: asset.Scope{Kind: asset.ScopeAccount, NativeID: "123456789012"}})
		var providerError *contracts.ProviderCallError
		if !errors.As(err, &providerError) || providerError.Provider.Category != execution.ErrorUnsupported || providerError.Provider.Summary["skip_reason"] != string(test.reason) {
			t.Fatalf("code=%s error=%#v", test.code, err)
		}
	}
}

type resourceExplorerClient struct {
	page     provideraws.SearchPage
	err      error
	requests []provideraws.SearchRequest
}

type capturedJobLog struct {
	kind    execution.JobLogKind
	level   string
	message string
	payload map[string]any
}

func (c *resourceExplorerClient) Search(_ context.Context, request provideraws.SearchRequest) (provideraws.SearchPage, error) {
	c.requests = append(c.requests, request)
	return c.page, c.err
}

func readSearchPage(t *testing.T) provideraws.SearchPage {
	t.Helper()
	payload, err := os.ReadFile("fixtures/search-page.json")
	if err != nil {
		t.Fatal(err)
	}
	var page provideraws.SearchPage
	if err := json.Unmarshal(payload, &page); err != nil {
		t.Fatal(err)
	}
	return page
}
