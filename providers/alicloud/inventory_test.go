package alicloud_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alibabacloud-go/tea/tea"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/providers/alicloud"
)

type resourceCenterClient struct {
	page                  alicloud.ResourcePage
	configurationPage     alicloud.ResourceConfigurationPage
	err                   error
	configurationErr      error
	requests              []alicloud.SearchRequest
	configurationRequests []alicloud.ResourceConfigurationRequest
}

func (c *resourceCenterClient) SearchResources(_ context.Context, request alicloud.SearchRequest) (alicloud.ResourcePage, error) {
	c.requests = append(c.requests, request)
	return c.page, c.err
}

func (c *resourceCenterClient) BatchGetResourceConfigurations(
	_ context.Context,
	request alicloud.ResourceConfigurationRequest,
) (alicloud.ResourceConfigurationPage, error) {
	c.configurationRequests = append(c.configurationRequests, request)
	if c.configurationErr != nil {
		return alicloud.ResourceConfigurationPage{}, c.configurationErr
	}
	if c.configurationPage.Resources != nil {
		return c.configurationPage, nil
	}
	page := alicloud.ResourceConfigurationPage{RequestID: "configuration-request"}
	for _, resource := range request.Resources {
		page.Resources = append(page.Resources, alicloud.ResourceRecord{
			RegionID: resource.RegionID, ResourceID: resource.ResourceID, ResourceType: resource.ResourceType,
			Configuration: map[string]any{},
		})
	}
	return page, nil
}

func newTestInventory(client alicloud.ResourceCenterClient) *alicloud.Inventory {
	return alicloud.NewInventory(client, []string{
		"ACS::ECS::Instance",
		"ACS::OSS::Bucket",
		"ACS::VPC::VPC",
	})
}

func TestInventoryEmitsCatalogOnlyResourceAsIndexed(t *testing.T) {
	t.Parallel()

	client := &resourceCenterClient{page: readResourcePage(t, "resource-center-page.json")}
	inventory := newTestInventory(client)
	batch, err := inventory.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Scope: asset.Scope{ID: "scope-hangzhou", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"}, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list resource center inventory: %v", err)
	}
	if len(batch.Items) != 2 || batch.NextCursor != "page-2" || batch.RequestID != "resource-request-1" || batch.Complete {
		t.Fatalf("batch=%+v", batch)
	}
	if len(client.requests) != 1 || client.requests[0].MaxResults != 500 || client.requests[0].RegionID != "cn-hangzhou" {
		t.Fatalf("bounded request=%+v", client.requests)
	}
	item := batch.Items[0]
	if item.NativeType != "ACS::OSS::Bucket" || item.NativeID != "bucket-a" || item.Location != "cn-hangzhou" || !item.ResourceKind.Capabilities.Has(asset.CapabilityIndexed) || item.ResourceKind.Capabilities.Has(asset.CapabilityActionable) {
		t.Fatalf("catalog-only item=%+v", item)
	}
	if item.Normalized["accountId"] != "1234567890123456" || item.Normalized["resourceGroupId"] != "rg-a" {
		t.Fatalf("identity and scope projection=%+v", item.Normalized)
	}
	if len(item.NativeAliases) != 1 || item.NativeAliases[0] != "bucket-a" {
		t.Fatalf("native aliases=%v", item.NativeAliases)
	}
}

func TestInventoryNormalizesVPCSelfMembershipFromResourceCenterIdentity(t *testing.T) {
	t.Parallel()

	client := &resourceCenterClient{page: alicloud.ResourcePage{
		Resources: []alicloud.ResourceRecord{{
			ResourceType: "ACS::VPC::VPC",
			ResourceID:   "vpc-production",
			ResourceName: "production",
			RegionID:     "cn-hangzhou",
		}},
	}}
	batch, err := newTestInventory(client).List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{ID: "region-a", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
	})
	if err != nil {
		t.Fatalf("list VPC inventory: %v", err)
	}
	if len(batch.Items) != 1 || batch.Items[0].NativeID != "vpc-production" {
		t.Fatalf("batch = %+v", batch)
	}
	if batch.Items[0].Normalized["vpc_id"] != "vpc-production" {
		t.Fatalf("VPC normalized identity = %#v", batch.Items[0].Normalized)
	}
}

func TestInventoryLogsRawResourceCenterRequestAndResponse(t *testing.T) {
	client := &resourceCenterClient{page: readResourcePage(t, "resource-center-page.json")}
	var logs []capturedJobLog
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, capturedJobLog{kind: entry.Kind, level: entry.Level, message: entry.Message, payload: entry.Payload})
		},
	))

	_, err := newTestInventory(client).List(ctx, contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		ResourceKind: &asset.ResourceKind{NativeType: "ACS::ECS::Instance"},
		Cursor:       "page-1",
		Limit:        100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(logs) != 4 ||
		logs[0].kind != execution.JobLogCloudAPIRequest ||
		logs[0].message != "call resource-center SearchResources" ||
		logs[1].kind != execution.JobLogCloudAPIResponse ||
		logs[1].message != "resource-center SearchResources returned" ||
		logs[2].message != "call resource-center BatchGetResourceConfigurations" ||
		logs[3].message != "resource-center BatchGetResourceConfigurations returned" {
		t.Fatalf("logs=%#v", logs)
	}
	if logs[0].payload["MaxResults"] != float64(500) || logs[0].payload["NextToken"] != "page-1" {
		t.Fatalf("request log=%#v", logs[0])
	}
	filters, ok := logs[0].payload["Filter"].([]any)
	if !ok || len(filters) != 2 {
		t.Fatalf("request filters=%#v", logs[0].payload["Filter"])
	}
	if logs[1].payload["RequestId"] != "resource-request-1" || logs[1].payload["NextToken"] != "page-2" {
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
	if !strings.Contains(string(encoded), `"Tags"`) || !strings.Contains(string(encoded), "environment") {
		t.Fatalf("raw response fields missing: %s", encoded)
	}
}

func TestInventoryLogsResourceCenterFailureWithoutFabricatedResponse(t *testing.T) {
	client := &resourceCenterClient{err: &alicloud.APIError{
		Code: "Throttling.User", Message: "too many requests", RequestID: "request-throttled", StatusCode: 429, RetryAfter: 2 * time.Second,
	}}
	var logs []capturedJobLog
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, capturedJobLog{kind: entry.Kind, level: entry.Level, message: entry.Message, payload: entry.Payload})
		},
	))

	_, err := newTestInventory(client).List(ctx, contracts.InventoryRequest{Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"}, Limit: 100})
	if err == nil {
		t.Fatal("expected provider error")
	}
	if len(logs) != 2 ||
		logs[1].kind != execution.JobLogCloudAPIResponse ||
		logs[1].level != "info" ||
		logs[1].message != "resource-center SearchResources failed: Throttling.User: too many requests" ||
		logs[1].payload != nil {
		t.Fatalf("logs=%#v", logs)
	}
}

func TestInventoryLogsOriginalResourceCenterErrorResponse(t *testing.T) {
	client := &resourceCenterClient{err: tea.NewSDKError(map[string]any{
		"code":       "Throttling.User",
		"message":    "code: 429, too many requests request id: request-throttled",
		"statusCode": 429,
		"data": map[string]any{
			"Code":       "Throttling.User",
			"Message":    "too many requests",
			"RequestId":  "request-throttled",
			"statusCode": 429,
		},
	})}
	var logs []capturedJobLog
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, capturedJobLog{kind: entry.Kind, level: entry.Level, message: entry.Message, payload: entry.Payload})
		},
	))

	_, err := newTestInventory(client).List(ctx, contracts.InventoryRequest{
		Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		Limit: 100,
	})
	if err == nil {
		t.Fatal("expected provider error")
	}
	if len(logs) != 2 ||
		logs[1].kind != execution.JobLogCloudAPIResponse ||
		logs[1].level != "info" ||
		logs[1].message != "resource-center SearchResources returned" ||
		logs[1].payload["Code"] != "Throttling.User" ||
		logs[1].payload["Message"] != "too many requests" ||
		logs[1].payload["RequestId"] != "request-throttled" {
		t.Fatalf("logs=%#v", logs)
	}
	for _, synthetic := range []string{"statusCode", "error_category", "error_code", "error_message"} {
		if _, exists := logs[1].payload[synthetic]; exists {
			t.Fatalf("response log contains synthetic field %q: %#v", synthetic, logs[1].payload)
		}
	}
}

func TestInventoryNormalizesThrottlingAndRequestID(t *testing.T) {
	t.Parallel()

	client := &resourceCenterClient{err: &alicloud.APIError{
		Code: "Throttling.User", Message: "too many requests", RequestID: "request-throttled", StatusCode: 429, RetryAfter: 2 * time.Second,
	}}
	_, err := newTestInventory(client).List(context.Background(), contracts.InventoryRequest{Limit: 100})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) {
		t.Fatalf("error=%T %v, want ProviderCallError", err, err)
	}
	if providerError.Provider.Category != execution.ErrorThrottled || providerError.Provider.RequestID != "request-throttled" || providerError.RetryAfter != 2*time.Second {
		t.Fatalf("normalized provider error=%+v", providerError)
	}
}

func TestInventoryNormalizesAlibabaCloudSDKError(t *testing.T) {
	t.Parallel()

	client := &resourceCenterClient{err: tea.NewSDKError(map[string]any{
		"code":       "Throttling.User",
		"message":    "code: 429, too many requests request id: sdk-request-throttled",
		"statusCode": 429,
		"data":       `{"Message":"too many requests","RequestId":"sdk-request-throttled"}`,
	})}
	_, err := newTestInventory(client).List(context.Background(), contracts.InventoryRequest{Limit: 100})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) {
		t.Fatalf("error=%T %v, want ProviderCallError", err, err)
	}
	if providerError.Provider.Category != execution.ErrorThrottled ||
		providerError.Provider.Code != "Throttling.User" ||
		providerError.Provider.Message != "too many requests" ||
		providerError.Provider.RequestID != "sdk-request-throttled" {
		t.Fatalf("normalized SDK error=%+v", providerError)
	}
}

func TestInventoryClassifiesOnlyExplicitUnsupportedErrorsAsSkippable(t *testing.T) {
	for _, test := range []struct {
		code   string
		reason asset.SkipReason
	}{
		{code: "UnsupportedOperation", reason: asset.SkipProductUnsupported},
		{code: "UnsupportedHTTPMethod", reason: asset.SkipProductUnsupported},
		{code: "InvalidAction", reason: asset.SkipProductUnsupported},
		{code: "InvalidAction.NotFound", reason: asset.SkipProductUnsupported},
		{code: "DcdnipaServiceNotFound", reason: asset.SkipProductUnsupported},
		{code: "InvalidRegionId.NotFound", reason: asset.SkipProviderRegionUnavailable},
		{code: "InvalidRegion", reason: asset.SkipProviderRegionUnavailable},
		{code: "InvalidRegionId", reason: asset.SkipProviderRegionUnavailable},
		{code: "UnauthorizedRegion", reason: asset.SkipProviderRegionUnavailable},
		{code: "RegionNotSupportError", reason: asset.SkipProviderRegionUnavailable},
		{code: "DDosBgp.CheckError.InvalidRegion", reason: asset.SkipProviderRegionUnavailable},
		{code: "InvalidOperation.NotSupportedEndpoint", reason: asset.SkipProviderRegionUnavailable},
	} {
		_, err := newTestInventory(&resourceCenterClient{err: &alicloud.APIError{Code: test.code, Message: "unsupported", StatusCode: 400}}).List(context.Background(), contracts.InventoryRequest{Limit: 100})
		var providerError *contracts.ProviderCallError
		if !errors.As(err, &providerError) || providerError.Provider.Category != execution.ErrorUnsupported || providerError.Provider.Summary["skip_reason"] != string(test.reason) {
			t.Fatalf("code=%s error=%#v", test.code, err)
		}
	}
}

func TestNormalizeErrorClassifiesInvalidRegionParameterAsSkippable(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&alicloud.APIError{
		Code: "InvalidParameter", Message: "The specified parameter RegionId:cn-example is not valid", StatusCode: 400,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorUnsupported ||
		providerError.Provider.Summary["skip_reason"] != string(asset.SkipProviderRegionUnavailable) {
		t.Fatalf("normalized invalid region error=%#v", err)
	}
}

func TestNormalizeErrorClassifiesNetworkFailureAsRetryable(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&net.OpError{
		Op: "read", Net: "tcp", Err: errors.New("connection reset by peer"),
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) || providerError.Provider.Category != execution.ErrorRetryable {
		t.Fatalf("normalized network error=%#v", err)
	}
}

func TestNormalizeErrorRedactsAuthorizationDetails(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&alicloud.APIError{
		Code: "InvalidAuthorization",
		Message: "invalid authorization 'acs temporary-id:sensitive-signature'\n" +
			"x-acs-accesskey-id: temporary-id\nrequest id: request-auth",
		RequestID:  "request-auth",
		StatusCode: http.StatusBadRequest,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) {
		t.Fatalf("normalized authorization error=%#v", err)
	}
	message := providerError.Provider.Message
	if !strings.Contains(message, "[REDACTED]") ||
		strings.Contains(message, "temporary-id") ||
		strings.Contains(message, "sensitive-signature") {
		t.Fatalf("authorization details were not redacted: %q", message)
	}
}

func TestNormalizeErrorUsesFCProviderErrorFields(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&tea.SDKError{
		Code:       tea.String("<nil>"),
		Message:    tea.String("code: 500, <nil> request id: <nil>"),
		StatusCode: tea.Int(http.StatusInternalServerError),
		Data: tea.String(`{
			"ErrorCode":"InternalServerError",
			"ErrorMessage":"an internal error has occurred. Please retry."
		}`),
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Code != "InternalServerError" ||
		providerError.Provider.Message != "an internal error has occurred. Please retry." ||
		providerError.Provider.Category != execution.ErrorRetryable {
		t.Fatalf("normalized FC error=%#v", err)
	}
}

func TestNormalizeErrorClassifiesSLSProjectDeletionProtection(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&alicloud.APIError{
		Code:       "ProjectDeletionNotAllowed",
		Message:    "Project is protected from deletion. Disable deletion protection first.",
		RequestID:  "request-protected-project",
		StatusCode: http.StatusBadRequest,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorProtected ||
		providerError.Provider.Code != "ProjectDeletionNotAllowed" ||
		providerError.Provider.RequestID != "request-protected-project" {
		t.Fatalf("normalized SLS deletion protection error=%#v", err)
	}
}

func TestNormalizeErrorClassifiesVPCPeerExistsAsDependencyViolation(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&alicloud.APIError{
		Code:       "OperationDenied.VpcPeerExists",
		Message:    "The operation is not allowed because the VpcPeer exists.",
		RequestID:  "request-vpc-peer-exists",
		StatusCode: http.StatusBadRequest,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorDependencyViolation ||
		providerError.Provider.Code != "OperationDenied.VpcPeerExists" ||
		providerError.Provider.RequestID != "request-vpc-peer-exists" {
		t.Fatalf("normalized VPC peer dependency error=%#v", err)
	}
}

func TestNormalizeErrorClassifiesVPCGatewayEndpointAsDependencyViolation(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&alicloud.APIError{
		Code:       "DependencyViolation.GatewayEndpoint",
		Message:    "The VPC contains endpoints and cannot be deleted.",
		RequestID:  "request-vpc-gateway-endpoint",
		StatusCode: http.StatusBadRequest,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorDependencyViolation ||
		providerError.Provider.Code != "DependencyViolation.GatewayEndpoint" ||
		providerError.Provider.RequestID != "request-vpc-gateway-endpoint" {
		t.Fatalf("normalized VPC gateway endpoint dependency error=%#v", err)
	}
}

func TestNormalizeErrorClassifiesVPCRouterInterfacesAsDependencyViolation(t *testing.T) {
	t.Parallel()

	for _, code := range []string{
		"DependencyViolation.RouterInterface",
		"DependencyViolation.OppositeRouterInterface",
	} {
		err := alicloud.NormalizeError(&alicloud.APIError{
			Code: code, Message: "The VPC contains router interfaces.",
			RequestID: "request-vpc-router-interface", StatusCode: http.StatusBadRequest,
		})
		var providerError *contracts.ProviderCallError
		if !errors.As(err, &providerError) ||
			providerError.Provider.Category != execution.ErrorDependencyViolation ||
			providerError.Provider.Code != code ||
			providerError.Provider.RequestID != "request-vpc-router-interface" {
			t.Fatalf("normalized VPC router interface dependency error=%#v", err)
		}
	}
}

func TestNormalizeErrorClassifiesDeletionAndReleaseProtectionVariants(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		code    string
		message string
	}{
		{
			name:    "deletion protection code",
			code:    "InvalidOperation.DeletionProtection",
			message: "Deletion protection is enabled.",
		},
		{
			name:    "release protection code",
			code:    "OperationDenied.InstanceReleaseProtection",
			message: "Release protection is enabled.",
		},
		{
			name:    "Chinese release protection",
			code:    "OperationDenied",
			message: "实例已开启释放保护，请关闭后重试。",
		},
		{
			name:    "Chinese deletion lock",
			code:    "OperationDenied",
			message: "集群已开启保护锁。",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := alicloud.NormalizeError(&alicloud.APIError{
				Code:       test.code,
				Message:    test.message,
				StatusCode: http.StatusBadRequest,
			})
			var providerError *contracts.ProviderCallError
			if !errors.As(err, &providerError) ||
				providerError.Provider.Category != execution.ErrorProtected {
				t.Fatalf("normalized deletion protection error=%#v", err)
			}
		})
	}
}

func TestNormalizeErrorUsesFC2ServiceNotFoundFields(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&tea.SDKError{
		Code:       tea.String("ServiceNotFound"),
		Message:    tea.String("code: 404, service 'svc-42d270g0' does not exist request id: <nil>"),
		StatusCode: tea.Int(http.StatusNotFound),
		Data: tea.String(`{
			"RequestID":"1-6a741c76-12daeaae-4f6364a4eb6a",
			"Code":"ServiceNotFound",
			"Message":"service 'svc-42d270g0' does not exist"
		}`),
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorNotFound ||
		providerError.Provider.Code != "ServiceNotFound" ||
		providerError.Provider.Message != "service 'svc-42d270g0' does not exist" ||
		providerError.Provider.RequestID != "1-6a741c76-12daeaae-4f6364a4eb6a" {
		t.Fatalf("normalized FC 2.0 ServiceNotFound error=%#v", err)
	}
}

func TestNormalizeErrorClassifiesMissingEIPAllocationAsNotFound(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&alicloud.APIError{
		Code:       "InvalidAllocationId.NotFound",
		Message:    "The specified AllocationId does not exist.",
		RequestID:  "request-eip-not-found",
		StatusCode: http.StatusBadRequest,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorNotFound ||
		providerError.Provider.Code != "InvalidAllocationId.NotFound" ||
		providerError.Provider.RequestID != "request-eip-not-found" {
		t.Fatalf("normalized missing EIP allocation error=%#v", err)
	}
}

func TestNormalizeErrorClassifiesSemanticInvalidRequestBeforeHTTPStatus(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&alicloud.APIError{
		Code:       "InvalidSecurityGroupId.EniMustHaveSecurityGroup",
		Message:    "The specified network card does not have an associated security group.",
		RequestID:  "request-eni",
		StatusCode: http.StatusInternalServerError,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorInvalidRequest ||
		providerError.Provider.RequestID != "request-eni" {
		t.Fatalf("normalized ENI error=%#v", err)
	}
}

func TestNormalizeErrorClassifiesDataWorksMissingProjectAsNotFound(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&alicloud.APIError{
		Code: "400", Message: "项目‘376’不存在 | null", RequestID: "dataworks-missing-project", StatusCode: http.StatusBadRequest,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorNotFound ||
		providerError.Provider.RequestID != "dataworks-missing-project" {
		t.Fatalf("normalized missing DataWorks project error=%#v", err)
	}
}

func TestNormalizeErrorClassifiesMaxComputeInvalidProjectAsNotFound(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&alicloud.APIError{
		Code: "ODPS-0420061", Message: "Invalid parameter in HTTP request - Invalid project: missing_project", StatusCode: http.StatusBadRequest,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorNotFound ||
		providerError.Provider.Code != "ODPS-0420061" {
		t.Fatalf("normalized MaxCompute invalid project error=%#v", err)
	}
}

func TestNormalizeErrorClassifiesDeletedDataWorksResourceGroupAsNotFound(t *testing.T) {
	t.Parallel()

	for name, test := range map[string]struct {
		message  string
		category execution.ErrorCategory
	}{
		"already deleted": {
			message:  "资源组订单释放失败: now resource group status is DELETED, not NORMAL",
			category: execution.ErrorNotFound,
		},
		"other release failure": {
			message:  "资源组订单释放失败: order cancellation was rejected",
			category: execution.ErrorInvalidRequest,
		},
	} {
		t.Run(name, func(t *testing.T) {
			err := alicloud.NormalizeError(&alicloud.APIError{
				Code: "704203", Message: test.message, StatusCode: http.StatusBadRequest,
			})
			var providerError *contracts.ProviderCallError
			if !errors.As(err, &providerError) ||
				providerError.Provider.Category != test.category ||
				providerError.Provider.Code != "704203" {
				t.Fatalf("normalized DataWorks resource group error=%#v", err)
			}
		})
	}
}

func TestNormalizeErrorClassifiesDNSLookupFailureAsRetryable(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&net.DNSError{
		Err: "no such host", Name: "service.cn-example.aliyuncs.com", IsNotFound: true,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorRetryable {
		t.Fatalf("normalized DNS error=%#v", err)
	}
}

func TestNormalizeErrorDelegatesNATManagedSecurityGroupDeletion(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&alicloud.APIError{
		Code: "InvalidOperation.ResourceManagedByCloudProduct",
		Message: `The specified SecurityGroup "sg-a" has been managed by ` +
			`serviceID "1679259531804325"; product "natgw".`,
		StatusCode: http.StatusForbidden,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorUnsupported ||
		providerError.Provider.Summary["skip_reason"] !=
			"delegated_to_nat_gateway" {
		t.Fatalf("normalized NAT-managed security group error=%#v", err)
	}
}

func TestNormalizeErrorClassifiesRejectedRegionFilterAsSkippable(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&alicloud.APIError{
		Code: "400", Message: "cn-example in filterRegionIds is illegal or not belong to current domain", StatusCode: 400,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorUnsupported ||
		providerError.Provider.Summary["skip_reason"] != string(asset.SkipProviderRegionUnavailable) {
		t.Fatalf("normalized region filter error=%#v", err)
	}
}

func TestNormalizeErrorClassifiesMethodNotAllowedAsSkippable(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&alicloud.APIError{Code: "<nil>", Message: "<nil>", StatusCode: http.StatusMethodNotAllowed})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorUnsupported ||
		providerError.Provider.Summary["skip_reason"] != string(asset.SkipProductUnsupported) {
		t.Fatalf("normalized method-not-allowed error=%#v", err)
	}
}

func TestNormalizeErrorClassifiesMaxComputeMissingTenantAsSkippable(t *testing.T) {
	t.Parallel()

	err := alicloud.NormalizeError(&alicloud.APIError{
		Code: "ILLEGAL_REQUEST", Message: "Tenant id is empty.", StatusCode: http.StatusBadRequest,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorUnsupported ||
		providerError.Provider.Summary["skip_reason"] != string(asset.SkipProductUnsupported) {
		t.Fatalf("normalized MaxCompute error=%#v", err)
	}
}

func TestInventoryBroadScanFiltersAllInstanceTypesInOneCondition(t *testing.T) {
	t.Parallel()

	client := &resourceCenterClient{page: alicloud.ResourcePage{RequestID: "request-a"}}
	inventory := alicloud.NewInventory(client, []string{
		"ACS::VPC::VPC",
		"ACS::ECS::Instance",
		"ACS::OSS::Bucket",
	})
	_, err := inventory.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.requests) != 1 {
		t.Fatalf("SearchResources calls = %d, want 1", len(client.requests))
	}
	want := []string{"ACS::ECS::Instance", "ACS::OSS::Bucket", "ACS::VPC::VPC"}
	if got := client.requests[0].ResourceTypes; !equalStrings(got, want) {
		t.Fatalf("resource types = %v, want %v", got, want)
	}
}

func TestInventoryPaginationKeepsStableInstanceTypeOrder(t *testing.T) {
	t.Parallel()

	client := &resourceCenterClient{page: alicloud.ResourcePage{RequestID: "request-a"}}
	inventory := alicloud.NewInventory(client, []string{
		"ACS::VPC::VPC",
		"ACS::ECS::Instance",
	})
	for _, cursor := range []string{"", "page-2"} {
		_, err := inventory.List(context.Background(), contracts.InventoryRequest{
			Scope:  asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
			Cursor: cursor,
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(client.requests) != 2 ||
		!equalStrings(client.requests[0].ResourceTypes, client.requests[1].ResourceTypes) ||
		client.requests[1].NextToken != "page-2" {
		t.Fatalf("requests = %+v", client.requests)
	}
}

func TestInventoryBatchesResourceConfigurationsByOneHundred(t *testing.T) {
	t.Parallel()

	resources := make([]alicloud.ResourceRecord, 205)
	for index := range resources {
		resources[index] = alicloud.ResourceRecord{
			RegionID: "cn-hangzhou", ResourceType: "ACS::ECS::Instance",
			ResourceID: fmt.Sprintf("i-%03d", index),
		}
	}
	client := &resourceCenterClient{page: alicloud.ResourcePage{Resources: resources}}
	batch, err := newTestInventory(client).List(context.Background(), contracts.InventoryRequest{
		Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		ResourceKind: &asset.ResourceKind{
			NativeType: "ACS::ECS::Instance",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 205 || len(client.configurationRequests) != 3 {
		t.Fatalf("items=%d configuration requests=%d", len(batch.Items), len(client.configurationRequests))
	}
	want := []int{100, 100, 5}
	for index, request := range client.configurationRequests {
		if len(request.Resources) != want[index] {
			t.Fatalf("configuration request %d size=%d, want %d", index, len(request.Resources), want[index])
		}
	}
}

func TestInventoryDropsResourcesOmittedByConfigurationLookupAndWarns(t *testing.T) {
	t.Parallel()

	client := &resourceCenterClient{
		page: alicloud.ResourcePage{
			RequestID: "search-request",
			Resources: []alicloud.ResourceRecord{
				{
					RegionID: "cn-hangzhou", ResourceType: "ACS::VPC::VPC",
					ResourceID: "vpc-present",
				},
				{
					RegionID: "cn-hangzhou", ResourceType: "ACS::VPC::VPC",
					ResourceID: "vpc-deleted-a",
				},
				{
					RegionID: "cn-hangzhou", ResourceType: "ACS::VPC::VPC",
					ResourceID: "vpc-deleted-b",
				},
			},
		},
		configurationPage: alicloud.ResourceConfigurationPage{
			RequestID: "configuration-request",
			Resources: []alicloud.ResourceRecord{{
				RegionID: "cn-hangzhou", ResourceType: "ACS::VPC::VPC",
				ResourceID: "vpc-present", Configuration: map[string]any{
					"VpcId": "vpc-present",
				},
			}},
		},
	}
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, entry)
		},
	))

	batch, err := newTestInventory(client).List(ctx, contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope: asset.Scope{
			Kind: asset.ScopeRegion, NativeID: "cn-hangzhou",
		},
		ResourceKind: &asset.ResourceKind{NativeType: "ACS::VPC::VPC"},
	})
	if err != nil {
		t.Fatalf("list inventory with concurrently deleted resources: %v", err)
	}
	if !batch.Complete ||
		len(batch.Items) != 1 ||
		batch.Items[0].NativeID != "vpc-present" {
		t.Fatalf("inventory batch = %+v", batch)
	}

	var warnings []execution.JobLogEntry
	for _, entry := range logs {
		if entry.Level == "warn" {
			warnings = append(warnings, entry)
		}
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %#v", warnings)
	}
	for index, nativeID := range []string{"vpc-deleted-a", "vpc-deleted-b"} {
		want := fmt.Sprintf(
			"resource-center BatchGetResourceConfigurations omitted ACS::VPC::VPC %s in cn-hangzhou; treating the resource as deleted during the scan",
			nativeID,
		)
		if warnings[index].Kind != execution.JobLogText ||
			warnings[index].Message != want ||
			warnings[index].Payload != nil {
			t.Errorf("warning %d = %#v, want %q", index, warnings[index], want)
		}
	}
}

func TestInventoryRejectsExplicitSubresourceBeforeCallingAPI(t *testing.T) {
	t.Parallel()

	client := &resourceCenterClient{}
	inventory := alicloud.NewInventory(client, []string{"ACS::ALB::LoadBalancer"})
	_, err := inventory.List(context.Background(), contracts.InventoryRequest{
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		ResourceKind: &asset.ResourceKind{NativeType: "ACS::ALB::Listener"},
	})
	if err == nil || !strings.Contains(err.Error(), "not an instance resource") {
		t.Fatalf("error = %v, want instance-resource rejection", err)
	}
	if len(client.requests) != 0 {
		t.Fatalf("SearchResources calls = %d, want 0", len(client.requests))
	}
}

func TestInventoryDropsReturnedSubresourceAndUnknownType(t *testing.T) {
	client := &resourceCenterClient{page: alicloud.ResourcePage{
		RequestID: "request-a",
		Resources: []alicloud.ResourceRecord{
			{ResourceType: "ACS::ALB::LoadBalancer", ResourceID: "alb-a"},
			{ResourceType: "ACS::ALB::Listener", ResourceID: "listener-a"},
			{ResourceType: "ACS::Future::Unknown", ResourceID: "future-a"},
		},
	}}
	var logs []capturedJobLog
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, capturedJobLog{
				kind: entry.Kind, level: entry.Level, message: entry.Message, payload: entry.Payload,
			})
		},
	))

	batch, err := alicloud.NewInventory(client, []string{"ACS::ALB::LoadBalancer"}).List(
		ctx,
		contracts.InventoryRequest{Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 1 || batch.Items[0].NativeID != "alb-a" {
		t.Fatalf("admitted items = %+v", batch.Items)
	}
	if len(logs) != 4 || logs[1].level != "info" ||
		logs[1].payload["ignored_resource_count"] != float64(2) {
		t.Fatalf("response logs = %#v", logs)
	}
	ignored, ok := logs[1].payload["ignored_resource_types"].([]any)
	if !ok || len(ignored) != 2 ||
		ignored[0] != "ACS::ALB::Listener" ||
		ignored[1] != "ACS::Future::Unknown" {
		t.Fatalf("ignored resource types = %#v", logs[1].payload["ignored_resource_types"])
	}
}

func TestInventoryNeverRetriesWithoutResourceTypeFilter(t *testing.T) {
	t.Parallel()

	client := &resourceCenterClient{err: &alicloud.APIError{
		Code: "InvalidParameter.Filter", Message: "type filter rejected", StatusCode: 400,
	}}
	_, err := alicloud.NewInventory(client, []string{"ACS::ECS::Instance"}).List(
		context.Background(),
		contracts.InventoryRequest{Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"}},
	)
	if err == nil {
		t.Fatal("expected filtered SearchResources failure")
	}
	if len(client.requests) != 1 || len(client.requests[0].ResourceTypes) != 1 {
		t.Fatalf("requests = %+v, want one filtered call", client.requests)
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func TestEmbeddedCatalogAndStrictSpecsCompile(t *testing.T) {
	t.Parallel()

	bundle, err := alicloud.LoadBundle()
	if err != nil {
		t.Fatalf("load Alibaba Cloud provider bundle: %v", err)
	}
	if bundle.Provider != asset.ProviderAliCloud || len(bundle.Specs) != 159 || len(bundle.Hash) != 64 {
		t.Fatalf("compiled bundle=%+v", bundle)
	}
}

func readResourcePage(t *testing.T, name string) alicloud.ResourcePage {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join("fixtures", name))
	if err != nil {
		t.Fatal(err)
	}
	var page alicloud.ResourcePage
	if err := json.Unmarshal(payload, &page); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(payload, &page.RawResponse); err != nil {
		t.Fatal(err)
	}
	return page
}

type capturedJobLog struct {
	kind    execution.JobLogKind
	level   string
	message string
	payload map[string]any
}
