package alicloud

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/alibabacloud-go/tea/dara"
	"github.com/alibabacloud-go/tea/tea"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestValidateConnectionReturnsTypedExpiredCredentialError(t *testing.T) {
	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatal(err)
	}
	expiredAt := time.Now().Add(-time.Minute)

	_, err = runtime.ValidateConnection(context.Background(), contracts.Credential{
		Type: asset.CredentialAliCloudSTS, ExpiresAt: &expiredAt,
		Values: map[string]string{
			"access_key_id": "id", "access_key_secret": "secret", "security_token": "token",
		},
	})
	var validationErr *contracts.CredentialValidationError
	if !errors.As(err, &validationErr) || validationErr.Code != "credential_expired" {
		t.Fatalf("ValidateConnection() error = %#v", err)
	}
}

func TestRuntimeDeclaresAlibabaCloudSites(t *testing.T) {
	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatal(err)
	}
	want := []contracts.ProviderSite{
		{Value: asset.ConnectionSiteCN, LabelKey: "sites.alicloudCN"},
		{Value: asset.ConnectionSiteINTL, LabelKey: "sites.alicloudINTL"},
	}
	if got := runtime.ConnectionSites(); len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("ConnectionSites() = %#v, want %#v", got, want)
	}
	var oauthSchema *contracts.CredentialSchema
	for _, schema := range runtime.CredentialSchemas() {
		if schema.Type == asset.CredentialAliCloudOAuth {
			copy := schema
			oauthSchema = &copy
		}
	}
	if oauthSchema == nil || oauthSchema.Flow != "browser_oauth" || len(oauthSchema.Fields) != 0 {
		t.Fatalf("OAuth credential schema = %#v", oauthSchema)
	}
}

func TestRuntimePrefersResourceCenterForSupportedInventoryKinds(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType[vSwitchNativeType]
	if got := runtime.InventorySourceForResourceKind(kind, "product-api"); got != "resource-center" {
		t.Fatalf("supported inventory source = %q", got)
	}
	unknown := asset.ResourceKind{NativeType: "ACS::Future::Unsupported"}
	if got := runtime.InventorySourceForResourceKind(unknown, "product-api"); got != "product-api" {
		t.Fatalf("unsupported inventory source = %q", got)
	}
	var resourceCenter contracts.InventorySource
	for _, source := range runtime.InventorySources() {
		if source.Name == "resource-center" {
			resourceCenter = source
		}
	}
	if !resourceCenter.KindSpecific || !resourceCenter.AuthoritativeDefault ||
		!resourceCenter.NetworkClosure ||
		len(resourceCenter.RootScopeKinds) != 2 {
		t.Fatalf("Resource Center source = %#v", resourceCenter)
	}
}

func TestRuntimeValidatesOAuthWithMaterializedSTSAndReturnsCredentialVersion(t *testing.T) {
	now := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	value := oauthCredentialFixture(now)
	factory := &runtimeFactory{
		accountID: "1234567890123456",
		principal: "acs:ram::1234567890123456:user/oauth",
	}
	api := &oauthAPIStub{}
	runtime, err := newRuntime(
		&credentialSource{value: value},
		factory,
		withOAuthAPI(api),
		withOAuthClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}

	identity, err := runtime.ValidateConnection(context.Background(), value)
	if err != nil {
		t.Fatal(err)
	}
	if factory.credential.Type != asset.CredentialAliCloudSTS ||
		factory.credential.Values["access_key_id"] != "sts-old-id" ||
		identity.CredentialVersion != value.Version {
		t.Fatalf("factory credential = %#v, identity = %#v", factory.credential, identity)
	}
	if len(api.calls) != 0 {
		t.Fatalf("OAuth calls = %#v", api.calls)
	}
}

func TestRuntimeRefreshesNearExpiryOAuthBeforeScanAndCleanupCalls(t *testing.T) {
	now := time.Date(2026, 8, 4, 16, 20, 0, 0, time.UTC)
	for _, test := range []struct {
		name string
		call func(context.Context, *Runtime) error
	}{
		{
			name: "resource center scan",
			call: func(ctx context.Context, runtime *Runtime) error {
				_, err := runtime.List(ctx, contracts.InventoryRequest{
					ConnectionID: "connection-oauth",
					Scope: asset.Scope{
						Kind: asset.ScopeRegion, NativeID: "cn-qingdao", Location: "cn-qingdao",
					},
					Source: "resource-center",
				})
				return err
			},
		},
		{
			name: "product cleanup",
			call: func(ctx context.Context, runtime *Runtime) error {
				_, err := runtime.Invoke(ctx, contracts.Invocation{
					ConnectionID: "connection-oauth",
					Operation:    "AlibabaCloud.DeleteInstance",
					Scope:        map[string]string{"region": "cn-qingdao"},
					Parameters:   map[string]any{"InstanceId": "i-a"},
				})
				return err
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			expected := oauthCredentialFixture(now)
			expected.Values[oauthAccessTokenExpireKey] = formatOAuthUnix(now.Add(4 * time.Minute))
			expected.Values[oauthSTSExpireKey] = formatOAuthUnix(now.Add(4 * time.Minute))
			store := &oauthCredentialStoreStub{value: cloneTestOAuthCredential(expected)}
			api := &oauthAPIStub{
				refresh: oauthTokens{
					AccessToken: "access-new", RefreshToken: "refresh-rotated",
					ExpiresAt: now.Add(time.Hour),
				},
				exchange: oauthSTS{
					AccessKeyID: "sts-new-id", AccessKeySecret: "sts-new-secret",
					SecurityToken: "sts-new-token", ExpiresAt: now.Add(30 * time.Minute),
				},
			}
			factory := &runtimeFactory{client: &runtimeResourceCenterClient{}}
			runtime, err := newRuntime(
				store,
				factory,
				withOAuthAPI(api),
				withOAuthClock(func() time.Time { return now }),
			)
			if err != nil {
				t.Fatal(err)
			}

			if err := test.call(context.Background(), runtime); err != nil {
				t.Fatal(err)
			}
			calls := api.recordedCalls()
			if len(calls) != 2 ||
				calls[0] != "refresh:refresh-old" ||
				calls[1] != "exchange:access-new" {
				t.Fatalf("OAuth calls = %#v", calls)
			}
			if factory.credential.Type != asset.CredentialAliCloudSTS ||
				factory.credential.Values["access_key_id"] != "sts-new-id" ||
				factory.credential.ExpiresAt == nil ||
				factory.credential.ExpiresAt.Unix() != now.Add(30*time.Minute).Unix() {
				t.Fatalf("API credential = %#v", factory.credential)
			}
			if store.updateCount() != 1 {
				t.Fatalf("credential updates = %d, want 1", store.updateCount())
			}
		})
	}
}

func TestRuntimeReturnsMaterializedOAuthVersionWithProviderValidationError(t *testing.T) {
	now := time.Date(2026, 7, 27, 16, 0, 0, 0, time.UTC)
	value := oauthCredentialFixture(now)
	providerErr := errors.New("caller identity unavailable")
	runtime, err := newRuntime(
		&credentialSource{value: value},
		&runtimeFactory{identityErr: providerErr},
		withOAuthAPI(&oauthAPIStub{}),
		withOAuthClock(func() time.Time { return now }),
	)
	if err != nil {
		t.Fatal(err)
	}

	identity, err := runtime.ValidateConnection(context.Background(), value)
	if !errors.Is(err, providerErr) {
		t.Fatalf("ValidateConnection() error = %v, want provider error", err)
	}
	if identity.CredentialVersion != value.Version {
		t.Fatalf("failure credential version = %q, want %q", identity.CredentialVersion, value.Version)
	}
}

type credentialSource struct {
	wantConnection asset.ConnectionID
	value          contracts.Credential
	calls          int
}

func (s *credentialSource) Resolve(_ context.Context, connectionID asset.ConnectionID) (contracts.Credential, error) {
	s.calls++
	if connectionID != s.wantConnection {
		return contracts.Credential{}, errors.New("unexpected connection ID")
	}
	return s.value, nil
}

type runtimeFactory struct {
	client       ResourceCenterClient
	ackClient    ACKClient
	invocation   contracts.Invocation
	credential   contracts.Credential
	region       string
	accountID    string
	principal    string
	identityErr  error
	regions      []providerRegion
	invokeResult contracts.InvocationResult
	invokeErr    error
}

type runtimeResourceCenterClient struct {
	page                  ResourcePage
	configurationPage     ResourceConfigurationPage
	request               SearchRequest
	configurationRequests []ResourceConfigurationRequest
}

type runtimeACKClient struct{}

func (*runtimeACKClient) DescribeClusterResources(context.Context, string, bool) ([]ClusterResource, string, error) {
	return nil, "request", nil
}

func (*runtimeACKClient) DescribeClusterNodes(context.Context, string) ([]ClusterNode, string, error) {
	return nil, "request", nil
}

func (*runtimeACKClient) DeleteCluster(context.Context, DeleteClusterRequest) (DeleteClusterResponse, error) {
	return DeleteClusterResponse{}, nil
}

func (c *runtimeResourceCenterClient) SearchResources(_ context.Context, request SearchRequest) (ResourcePage, error) {
	c.request = request
	return c.page, nil
}

func (c *runtimeResourceCenterClient) BatchGetResourceConfigurations(
	_ context.Context,
	request ResourceConfigurationRequest,
) (ResourceConfigurationPage, error) {
	c.configurationRequests = append(c.configurationRequests, request)
	if c.configurationPage.Resources != nil {
		return c.configurationPage, nil
	}
	page := ResourceConfigurationPage{RequestID: "configuration-request"}
	for _, resource := range request.Resources {
		page.Resources = append(page.Resources, ResourceRecord{
			RegionID: resource.RegionID, ResourceID: resource.ResourceID, ResourceType: resource.ResourceType,
			Configuration: map[string]any{},
		})
	}
	return page, nil
}

func (f *runtimeFactory) ResourceCenter(_ context.Context, credential contracts.Credential, region string) (ResourceCenterClient, error) {
	f.credential = credential
	f.region = region
	return f.client, nil
}

func (f *runtimeFactory) Invoke(_ context.Context, credential contracts.Credential, region string, _ catalog.Operation, invocation contracts.Invocation) (contracts.InvocationResult, error) {
	f.credential = credential
	f.region = region
	f.invocation = invocation
	if f.invokeErr != nil {
		return contracts.InvocationResult{}, f.invokeErr
	}
	if f.invokeResult.Data != nil {
		return f.invokeResult, nil
	}
	return contracts.InvocationResult{RequestID: "provider-request", Data: map[string]any{"ok": true}}, nil
}

func (f *runtimeFactory) ACK(_ context.Context, credential contracts.Credential, region string) (ACKClient, error) {
	if f.ackClient == nil {
		return nil, errors.New("not used")
	}
	f.credential = credential
	f.region = region
	return f.ackClient, nil
}

func (f *runtimeFactory) CallerIdentity(_ context.Context, credential contracts.Credential) (string, string, error) {
	f.credential = credential
	return f.accountID, f.principal, f.identityErr
}

func (f *runtimeFactory) DiscoverRegions(context.Context, contracts.Credential) ([]providerRegion, error) {
	return append([]providerRegion(nil), f.regions...), nil
}

func TestCredentialValidationAndRegionDiscoveryAreIndependent(t *testing.T) {
	t.Parallel()

	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{
		Type:   asset.CredentialAliCloudAccessKey,
		Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"},
	}}
	factory := &runtimeFactory{
		accountID: "1234567890123456", principal: "acs:ram::1234567890123456:user/operator",
		regions: []providerRegion{
			{RegionID: " cn-hangzhou ", Name: " 华东 1（杭州） ", Endpoint: "vpc.cn-hangzhou.aliyuncs.com"},
			{RegionID: "cn-shanghai", Name: "华东 2（上海）"},
		},
	}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	for _, schema := range runtime.CredentialSchemas() {
		for _, field := range schema.Fields {
			if field.Key == "region" {
				t.Fatalf("credential schema still contains region: %#v", schema)
			}
		}
	}
	identity, err := runtime.ValidateConnection(context.Background(), source.value)
	if err != nil {
		t.Fatal(err)
	}
	if identity.TenantID != factory.accountID || identity.Principal != factory.principal || len(identity.RootScopes) != 1 || identity.RootScopes[0].Kind != asset.ScopeAccount || identity.RootScopes[0].NativeID != factory.accountID || identity.RootScopes[0].Location != "" {
		t.Fatalf("identity = %#v", identity)
	}
	regions, err := runtime.DiscoverRegions(context.Background(), "connection-a")
	if err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 || len(regions) != 2 || regions[0].RegionID != "cn-hangzhou" || regions[0].Name != "华东 1（杭州）" || regions[0].Metadata["endpoint"] != "vpc.cn-hangzhou.aliyuncs.com" {
		t.Fatalf("regions = %#v, credential calls = %d", regions, source.calls)
	}
}

func TestValidateConnectionExtractsOnlyTheOriginalAlibabaCloudMessage(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name          string
		err           error
		wantMessage   string
		wantRequestID string
	}{
		{
			name: "Tea structured data",
			err: tea.NewSDKError(map[string]any{
				"code":       "InvalidAccessKeyId.Inactive",
				"message":    "code: 400, Specified access key is disabled request id: tea-request",
				"statusCode": 400,
				"data":       map[string]any{"Message": "Specified access key is disabled", "RequestId": "tea-request"},
			}),
			wantMessage:   "Specified access key is disabled",
			wantRequestID: "tea-request",
		},
		{
			name: "Dara structured data",
			err: dara.NewSDKError(map[string]any{
				"code":       "InvalidAccessKeyId.Inactive",
				"message":    "code: 400, Specified access key is disabled request id: dara-request",
				"statusCode": 400,
				"data":       map[string]any{"message": "Specified access key is disabled", "requestId": "dara-request"},
			}),
			wantMessage:   "Specified access key is disabled",
			wantRequestID: "dara-request",
		},
		{
			name: "nil request ID wrapper",
			err: tea.NewSDKError(map[string]any{
				"code":       "InvalidAccessKeyId.Inactive",
				"message":    "code: 400, Specified access key is disabled request id: <nil>",
				"statusCode": 400,
			}),
			wantMessage: "Specified access key is disabled",
		},
		{
			name: "nested message is not guessed",
			err: tea.NewSDKError(map[string]any{
				"code":       "InvalidAccessKeyId.Inactive",
				"message":    "code: 400, Specified access key is disabled request id: wrapper-request",
				"statusCode": 400,
				"data":       map[string]any{"details": map[string]any{"Message": "unrelated nested diagnostic"}},
			}),
			wantMessage:   "Specified access key is disabled",
			wantRequestID: "wrapper-request",
		},
		{
			name: "unrecognized envelope stays unchanged",
			err: tea.NewSDKError(map[string]any{
				"code":       "TransportError",
				"message":    "dial tcp: connection refused",
				"statusCode": 0,
			}),
			wantMessage: "dial tcp: connection refused",
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{identityErr: test.err})
			if err != nil {
				t.Fatal(err)
			}
			_, err = runtime.ValidateConnection(context.Background(), contracts.Credential{
				Type: asset.CredentialAliCloudAccessKey,
				Values: map[string]string{
					"access_key_id": "id", "access_key_secret": "secret",
				},
			})
			var providerError *contracts.ProviderCallError
			if !errors.As(err, &providerError) {
				t.Fatalf("error=%T %v, want ProviderCallError", err, err)
			}
			if providerError.Provider.Message != test.wantMessage ||
				providerError.Provider.RequestID != test.wantRequestID {
				t.Fatalf("normalized validation error=%+v", providerError)
			}
		})
	}
}

func TestRuntimeResolvesACKActionWithOpaqueCredential(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{Values: map[string]string{
			"type": "access_key", "access_key_id": "key-id", "access_key_secret": "key-secret",
		}},
	}
	factory := &runtimeFactory{ackClient: &runtimeACKClient{}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	driver, err := runtime.ResolveAction(context.Background(), "connection-a", asset.Asset{
		ID: "ack", Identity: asset.Identity{Provider: asset.ProviderAliCloud, NativeType: ACKClusterNativeType, NativeID: "cluster-a"}, Location: "cn-hangzhou",
	})
	if err != nil || driver == nil || source.calls != 1 || factory.region != "cn-hangzhou" {
		t.Fatalf("driver=%T calls=%d region=%q err=%v", driver, source.calls, factory.region, err)
	}
}

func TestRuntimeResolvesVPCGatewayEndpointAction(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatal(err)
	}
	driver, err := runtime.ResolveAction(context.Background(), "connection-a", asset.Asset{
		ID: "gateway-endpoint",
		Identity: asset.Identity{
			Provider: asset.ProviderAliCloud, NativeType: VPCGatewayEndpointNativeType,
			NativeID: "vpce-bp1khxwul8setja1pb32z",
		},
		Location: "cn-hangzhou",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := driver.(*VPCGatewayEndpointAction); !ok {
		t.Fatalf("gateway endpoint driver=%T", driver)
	}
	if operation, ok := runtime.catalog.Operation(vpcGatewayEndpointDissociateOperation); !ok ||
		operation.Call == nil || operation.Call.Product != "Vpc" {
		t.Fatalf("gateway endpoint dissociation operation=%+v found=%t", operation, ok)
	}
}

func TestRuntimeResolvesOpaqueCredentialAndDispatchesCatalogOperation(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{Values: map[string]string{
			"type": "access_key", "access_key_id": "key-id", "access_key_secret": "key-secret",
		}},
	}
	factory := &runtimeFactory{}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, entry)
		},
	))
	result, err := runtime.Invoke(ctx, contracts.Invocation{
		ConnectionID: "connection-a", Operation: "AlibabaCloud.DescribeInstances",
		Scope: map[string]string{"region": "cn-hangzhou"}, Parameters: map[string]any{"InstanceIds": `["i-a"]`},
	})
	if err != nil {
		t.Fatalf("invoke catalog operation: %v", err)
	}
	if source.calls != 1 || factory.region != "cn-hangzhou" || factory.invocation.Operation != "AlibabaCloud.DescribeInstances" || result.RequestID != "provider-request" {
		t.Fatalf("source calls=%d region=%q invocation=%+v result=%+v", source.calls, factory.region, factory.invocation, result)
	}
	if _, leaked := factory.invocation.Parameters["access_key_secret"]; leaked {
		t.Fatalf("credentials leaked into provider invocation: %+v", factory.invocation)
	}
	if len(logs) != 2 ||
		logs[0].Kind != execution.JobLogCloudAPIRequest ||
		logs[0].Message != "call ecs DescribeInstances" ||
		logs[0].Payload["InstanceIds"] != `["i-a"]` ||
		logs[1].Kind != execution.JobLogCloudAPIResponse ||
		logs[1].Message != "ecs DescribeInstances returned" ||
		logs[1].Payload["RequestId"] != "provider-request" {
		t.Fatalf("provider logs=%+v", logs)
	}
}

func TestRuntimeLogsCatalogCloudProductInsteadOfGenericProvider(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{Values: map[string]string{
			"type": "access_key", "access_key_id": "key-id", "access_key_secret": "key-secret",
		}},
	}
	runtime, err := newRuntime(source, &runtimeFactory{})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, entry)
		},
	))

	for _, operation := range []string{
		"AlibabaCloud.ListPrometheusInstances",
		"AlibabaCloud.DBS.DescribeBackupPlanList",
	} {
		_, err := runtime.Invoke(ctx, contracts.Invocation{
			ConnectionID: "connection-a",
			Operation:    operation,
			Scope:        map[string]string{"region": "cn-hangzhou"},
		})
		if err != nil {
			t.Fatalf("invoke %s: %v", operation, err)
		}
	}

	want := []string{
		"call cms ListPrometheusInstances",
		"cms ListPrometheusInstances returned",
		"call dbs DescribeBackupPlanList",
		"dbs DescribeBackupPlanList returned",
	}
	if len(logs) != len(want) {
		t.Fatalf("logs=%+v, want messages=%v", logs, want)
	}
	for index, message := range want {
		if logs[index].Message != message {
			t.Fatalf("logs[%d].Message=%q, want %q", index, logs[index].Message, message)
		}
	}
}

func TestRuntimeLogsCloudProductForProviderErrorResponse(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{Values: map[string]string{
			"type": "access_key", "access_key_id": "key-id", "access_key_secret": "key-secret",
		}},
	}
	factory := &runtimeFactory{invokeErr: tea.NewSDKError(map[string]any{
		"code":       "ServiceUnavailable",
		"message":    "temporary failure",
		"statusCode": 503,
		"data": map[string]any{
			"Code":      "ServiceUnavailable",
			"Message":   "temporary failure",
			"RequestId": "request-failed",
		},
	})}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, entry)
		},
	))

	_, err = runtime.Invoke(ctx, contracts.Invocation{
		ConnectionID: "connection-a",
		Operation:    "AlibabaCloud.ListPrometheusInstances",
		Scope:        map[string]string{"region": "cn-hangzhou"},
	})
	if err == nil {
		t.Fatal("expected provider error")
	}
	if len(logs) != 2 ||
		logs[0].Message != "call cms ListPrometheusInstances" ||
		logs[1].Level != "info" ||
		logs[1].Message != "cms ListPrometheusInstances returned" ||
		logs[1].Payload["Code"] != "ServiceUnavailable" {
		t.Fatalf("provider logs=%+v", logs)
	}
}

func TestRuntimeRejectsUnknownOperationBeforeCredentialResolution(t *testing.T) {
	t.Parallel()

	source := &credentialSource{wantConnection: "connection-a"}
	runtime, err := newRuntime(source, &runtimeFactory{})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	_, err = runtime.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection-a", Operation: "DeleteEverything"})
	if err == nil || source.calls != 0 {
		t.Fatalf("unknown operation err=%v credential calls=%d", err, source.calls)
	}
}

func TestRuntimeInventoryCreatesClientPerCredentialAndScope(t *testing.T) {
	t.Parallel()

	client := &runtimeResourceCenterClient{page: ResourcePage{RequestID: "resource-request", Resources: []ResourceRecord{{ResourceType: "ACS::OSS::Bucket", ResourceID: "bucket-a"}}}}
	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{Values: map[string]string{"type": "access_key", "access_key_id": "id", "access_key_secret": "secret"}}}
	factory := &runtimeFactory{client: client}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if _, ok := any(runtime).(contracts.InventoryBatchEnricher); !ok {
		t.Fatal("Alibaba Cloud runtime does not expose inventory batch enrichment")
	}
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-shanghai"}, Limit: 100,
	})
	if err != nil {
		t.Fatalf("list inventory: %v", err)
	}
	if source.calls != 1 || factory.region != "cn-shanghai" || batch.RequestID != "resource-request" || len(batch.Items) != 1 {
		t.Fatalf("source calls=%d region=%q batch=%+v", source.calls, factory.region, batch)
	}
	foundVPC := false
	for _, nativeType := range client.request.ResourceTypes {
		foundVPC = foundVPC || nativeType == vpcNativeType
	}
	if !foundVPC {
		t.Fatalf("Resource Center inventory does not include supported VPC resources")
	}
}

func TestRuntimeScopesNetworkProductInventoryAndUsesListTopologyFields(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	factory := &runtimeFactory{}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	scope := asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"}

	vpcKind := runtime.resourceKindByNativeType[vpcNativeType]
	factory.invokeResult = contracts.InvocationResult{Data: map[string]any{
		"PageNumber": 1, "PageSize": 50, "TotalCount": 1,
		"Vpcs": map[string]any{"Vpc": []any{map[string]any{
			"VpcId": "vpc-a", "VpcName": "production", "Status": "Available",
		}}},
	}}
	vpcBatch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Scope: scope, ResourceKind: &vpcKind, Limit: 50,
		NetworkTarget: &asset.ScanTarget{
			Kind: asset.ScanTargetVPC, RegionID: "cn-hangzhou", NativeID: "vpc-a",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.invocation.Operation != "AlibabaCloud.DescribeVpcs" ||
		factory.invocation.Parameters["VpcId"] != "vpc-a" ||
		len(vpcBatch.Items) != 1 ||
		vpcBatch.Items[0].Normalized["vpc_id"] != "vpc-a" {
		t.Fatalf("targeted VPC inventory invocation=%+v batch=%+v", factory.invocation, vpcBatch)
	}

	vSwitchKind := runtime.resourceKindByNativeType[vSwitchNativeType]
	factory.invokeResult = contracts.InvocationResult{Data: map[string]any{
		"PageNumber": 1, "PageSize": 50, "TotalCount": 1,
		"VSwitches": map[string]any{"VSwitch": []any{map[string]any{
			"VSwitchId": "vsw-a", "VSwitchName": "application", "Status": "Available",
			"VpcId": "vpc-a", "ZoneId": "cn-hangzhou-h",
		}}},
	}}
	vSwitchBatch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Scope: scope, ResourceKind: &vSwitchKind, Limit: 50,
		NetworkTarget: &asset.ScanTarget{
			Kind: asset.ScanTargetVPC, RegionID: "cn-hangzhou", NativeID: "vpc-a",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.invocation.Operation != "AlibabaCloud.DescribeVSwitches" ||
		factory.invocation.Parameters["VpcId"] != "vpc-a" ||
		len(vSwitchBatch.Items) != 1 ||
		vSwitchBatch.Items[0].Normalized["vpc_id"] != "vpc-a" ||
		vSwitchBatch.Items[0].Normalized["zone_id"] != "cn-hangzhou-h" {
		t.Fatalf("targeted vSwitch inventory invocation=%+v batch=%+v", factory.invocation, vSwitchBatch)
	}

	_, err = runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Scope: scope, ResourceKind: &vSwitchKind, Limit: 50,
		NetworkTarget: &asset.ScanTarget{
			Kind: asset.ScanTargetVSwitch, RegionID: "cn-hangzhou", NativeID: "vsw-a",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.invocation.Parameters["VSwitchId"] != "vsw-a" {
		t.Fatalf("targeted vSwitch invocation=%+v", factory.invocation)
	}

	peerKind := runtime.resourceKindByNativeType[vpcPeerConnectionNativeType]
	factory.invokeResult = contracts.InvocationResult{Data: map[string]any{
		"TotalCount": 1,
		"VpcPeerConnects": []any{map[string]any{
			"InstanceId": "pcc-a", "Status": "Activated",
			"Vpc":          map[string]any{"VpcId": "vpc-requester"},
			"AcceptingVpc": map[string]any{"VpcId": "vpc-accepter"},
		}},
	}}
	peerBatch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Scope: scope, ResourceKind: &peerKind, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.invocation.Operation != "AlibabaCloud.ListVpcPeerConnections" ||
		len(peerBatch.Items) != 1 ||
		peerBatch.Items[0].Normalized["vpcId"] != "vpc-requester" ||
		peerBatch.Items[0].Normalized["acceptingVpcId"] != "vpc-accepter" {
		t.Fatalf("VPC peer inventory invocation=%+v batch=%+v", factory.invocation, peerBatch)
	}

	routerInterfaceKind := runtime.resourceKindByNativeType["ACS::VPC::RouterInterface"]
	factory.invokeResult = contracts.InvocationResult{Data: map[string]any{
		"TotalCount": 1,
		"RouterInterfaceSet": map[string]any{"RouterInterfaceType": []any{map[string]any{
			"RouterInterfaceId": "ri-bp1kbigq0y1gw1qerdswm", "Status": "active",
			"VpcInstanceId":         "vpc-bp11wti8zqjxjoqvlslwo",
			"OppositeVpcInstanceId": "vpc-shanghai", "OppositeRegionId": "cn-shanghai",
		}}},
	}}
	routerInterfaceBatch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Scope: scope, ResourceKind: &routerInterfaceKind, Limit: 50,
		NetworkTarget: &asset.ScanTarget{
			Kind: asset.ScanTargetVPC, RegionID: "cn-hangzhou",
			NativeID: "vpc-bp11wti8zqjxjoqvlslwo",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.invocation.Operation != "AlibabaCloud.DescribeRouterInterfaces" ||
		len(routerInterfaceBatch.Items) != 1 ||
		routerInterfaceBatch.Items[0].NativeID != "ri-bp1kbigq0y1gw1qerdswm" ||
		routerInterfaceBatch.Items[0].Normalized["vpcId"] != "vpc-bp11wti8zqjxjoqvlslwo" ||
		routerInterfaceBatch.Items[0].Normalized["acceptingVpcId"] != "vpc-shanghai" ||
		routerInterfaceBatch.Items[0].Normalized["oppositeRegionId"] != "cn-shanghai" {
		t.Fatalf("router interface inventory invocation=%+v batch=%+v", factory.invocation, routerInterfaceBatch)
	}

	gatewayEndpointKind := runtime.resourceKindByNativeType[VPCGatewayEndpointNativeType]
	factory.invokeResult = contracts.InvocationResult{Data: map[string]any{
		"TotalCount": 1,
		"Endpoints": []any{map[string]any{
			"EndpointId": "vpce-bp1khxwul8setja1pb32z", "EndpointName": "oss-endpoint",
			"EndpointStatus": "Created", "VpcId": "vpc-bp1x56m37b4rwa2fzmnap",
			"AssociatedRouteTables": []any{"vtb-a"},
		}},
	}}
	gatewayEndpointBatch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Scope: scope, ResourceKind: &gatewayEndpointKind, Limit: 100,
		NetworkTarget: &asset.ScanTarget{
			Kind: asset.ScanTargetVPC, RegionID: "cn-hangzhou", NativeID: "vpc-bp1x56m37b4rwa2fzmnap",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.invocation.Operation != "AlibabaCloud.ListVpcGatewayEndpoints" ||
		factory.invocation.Parameters["VpcId"] != "vpc-bp1x56m37b4rwa2fzmnap" ||
		len(gatewayEndpointBatch.Items) != 1 ||
		gatewayEndpointBatch.Items[0].NativeID != "vpce-bp1khxwul8setja1pb32z" ||
		gatewayEndpointBatch.Items[0].Normalized["vpcId"] != "vpc-bp1x56m37b4rwa2fzmnap" ||
		!reflect.DeepEqual(gatewayEndpointBatch.Items[0].Normalized["associatedRouteTableIds"], []any{"vtb-a"}) {
		t.Fatalf("VPC gateway endpoint inventory invocation=%+v batch=%+v", factory.invocation, gatewayEndpointBatch)
	}
}

func TestRuntimeProjectsResourceCenterConfigurationIntoVSwitchRelationship(t *testing.T) {
	t.Parallel()

	client := &runtimeResourceCenterClient{
		page: ResourcePage{
			RequestID: "search-request",
			Resources: []ResourceRecord{{
				RegionID: "cn-hangzhou", ResourceType: vSwitchNativeType,
				ResourceID: "vsw-a", ResourceName: "application",
			}},
		},
		configurationPage: ResourceConfigurationPage{
			RequestID: "configuration-request",
			Resources: []ResourceRecord{{
				RegionID: "cn-hangzhou", ResourceType: vSwitchNativeType,
				ResourceID: "vsw-a", ResourceName: "application",
				Configuration: map[string]any{
					"VpcId": "vpc-a", "ZoneId": "cn-hangzhou-h",
				},
			}},
		},
	}
	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	factory := &runtimeFactory{client: client}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType[vSwitchNativeType]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Source: "resource-center",
		Scope: asset.Scope{
			Kind: asset.ScopeRegion, NativeID: "cn-hangzhou", Location: "cn-hangzhou",
		},
		ResourceKind: &kind,
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.invocation.Operation != "" || len(batch.Items) != 1 {
		t.Fatalf("product invocation=%#v batch=%#v", factory.invocation, batch)
	}
	item := batch.Items[0]
	if item.Normalized["vpc_id"] != "vpc-a" ||
		item.Normalized["zone_id"] != "cn-hangzhou-h" ||
		item.Normalized[inventorySourceField] != "resource-center" {
		t.Fatalf("normalized configuration = %#v", item.Normalized)
	}
	if len(item.NetworkReferences) == 0 {
		t.Fatalf("network references = %#v", item.NetworkReferences)
	}
}

func TestRuntimePlacesRegionalResourceCenterResourcesAndCanonicalizesVPCID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		nativeType string
		nativeID   string
	}{
		{name: "route table", nativeType: "ACS::VPC::RouteTable", nativeID: "vtb-a"},
		{name: "security group", nativeType: securityGroupNativeType, nativeID: "sg-a"},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := &runtimeResourceCenterClient{
				page: ResourcePage{
					RequestID: "search-request",
					Resources: []ResourceRecord{{
						RegionID: "cn-chengdu", ResourceType: test.nativeType,
						ResourceID: test.nativeID,
					}},
				},
				configurationPage: ResourceConfigurationPage{
					RequestID: "configuration-request",
					Resources: []ResourceRecord{{
						RegionID: "cn-chengdu", ResourceType: test.nativeType,
						ResourceID:    test.nativeID,
						Configuration: map[string]any{"VpcId": "vpc-chengdu"},
					}},
				},
			}
			source := &credentialSource{
				wantConnection: "connection-a",
				value: contracts.Credential{
					Type: asset.CredentialAliCloudAccessKey,
					Values: map[string]string{
						"access_key_id": "id", "access_key_secret": "secret",
					},
				},
			}
			runtime, err := newRuntime(source, &runtimeFactory{client: client})
			if err != nil {
				t.Fatal(err)
			}
			kind := runtime.resourceKindByNativeType[test.nativeType]
			batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
				ConnectionID: "connection-a", Source: "resource-center",
				Scope: asset.Scope{
					Kind: asset.ScopeGlobal, NativeID: "global", Name: "Global",
					Location: "cn-hangzhou",
				},
				ResourceKind: &kind,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(batch.Items) != 1 {
				t.Fatalf("Resource Center batch = %#v", batch)
			}
			item := batch.Items[0]
			if item.Scope.Kind != asset.ScopeRegion ||
				item.Scope.NativeID != "cn-chengdu" ||
				item.Scope.Location != "cn-chengdu" {
				t.Fatalf("regional resource scope = %#v", item.Scope)
			}
			if item.Normalized["vpc_id"] != "vpc-chengdu" {
				t.Fatalf("canonical VPC placement = %#v", item.Normalized)
			}
		})
	}
}

func TestRuntimePlacesCENTransitRouterInItsConfiguredRegion(t *testing.T) {
	t.Parallel()

	client := &runtimeResourceCenterClient{
		page: ResourcePage{
			RequestID: "search-request",
			Resources: []ResourceRecord{{
				RegionID: "global", ResourceType: "ACS::CEN::TransitRouter",
				ResourceID: "tr-mj7f5z1btac2s1x4jusp5",
			}},
		},
		configurationPage: ResourceConfigurationPage{
			RequestID: "configuration-request",
			Resources: []ResourceRecord{{
				RegionID: "global", ResourceType: "ACS::CEN::TransitRouter",
				ResourceID: "tr-mj7f5z1btac2s1x4jusp5",
				Configuration: map[string]any{
					"RegionId": "ap-northeast-2",
					"CenId":    "cen-b46rksvi4fcjop46b6",
				},
			}},
		},
	}
	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	runtime, err := newRuntime(source, &runtimeFactory{client: client})
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType["ACS::CEN::TransitRouter"]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Source: "resource-center",
		Scope: asset.Scope{
			Kind: asset.ScopeGlobal, NativeID: "global", Name: "Global",
			Location: "cn-hangzhou",
		},
		ResourceKind: &kind,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 1 {
		t.Fatalf("Resource Center batch = %#v", batch)
	}
	item := batch.Items[0]
	if item.Scope.Kind != asset.ScopeRegion ||
		item.Scope.NativeID != "ap-northeast-2" ||
		item.Location != "ap-northeast-2" ||
		item.Normalized[NormalizedCENRegionIDField] != "ap-northeast-2" ||
		item.Normalized[NormalizedCENInstanceIDField] != "cen-b46rksvi4fcjop46b6" {
		t.Fatalf("CEN transit router placement = %#v", item)
	}
}

func TestRuntimeProjectsResourceCenterCENBandwidthPackageBindings(t *testing.T) {
	t.Parallel()

	client := &runtimeResourceCenterClient{
		page: ResourcePage{
			RequestID: "search-request",
			Resources: []ResourceRecord{{
				RegionID: "global", ResourceType: CENBandwidthPackageNativeType,
				ResourceID: "cenbwp-8hfrdbedeom4q3sei4",
			}},
		},
		configurationPage: ResourceConfigurationPage{
			RequestID: "configuration-request",
			Resources: []ResourceRecord{{
				RegionID: "global", ResourceType: CENBandwidthPackageNativeType,
				ResourceID: "cenbwp-8hfrdbedeom4q3sei4",
				Configuration: map[string]any{
					"CenIds": []any{"cen-v3amwa41xf5k3shiwg"},
				},
			}},
		},
	}
	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	runtime, err := newRuntime(source, &runtimeFactory{client: client})
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType[CENBandwidthPackageNativeType]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Source: "resource-center",
		Scope: asset.Scope{
			Kind: asset.ScopeGlobal, NativeID: "global", Name: "Global",
			Location: "cn-beijing",
		},
		ResourceKind: &kind,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 1 {
		t.Fatalf("Resource Center batch = %#v", batch)
	}
	cenIDs, ok := batch.Items[0].Normalized["cenIds"].([]string)
	if !ok || len(cenIDs) != 1 || cenIDs[0] != "cen-v3amwa41xf5k3shiwg" {
		t.Fatalf("CEN bandwidth package bindings = %#v", batch.Items[0].Normalized)
	}
}

func TestRuntimeProjectsManagedENITopologyFromResourceCenterConfiguration(t *testing.T) {
	t.Parallel()

	client := &runtimeResourceCenterClient{
		page: ResourcePage{
			RequestID: "search-request",
			Resources: []ResourceRecord{{
				RegionID: "eu-west-1", ResourceType: networkInterfaceNativeType,
				ResourceID: "eni-d7o0kwoemjg2fgi3wym7",
			}},
		},
		configurationPage: ResourceConfigurationPage{
			RequestID: "configuration-request",
			Resources: []ResourceRecord{{
				RegionID: "eu-west-1", ResourceType: networkInterfaceNativeType,
				ResourceID: "eni-d7o0kwoemjg2fgi3wym7",
				Configuration: map[string]any{
					"VpcId": "vpc-london", "VSwitchId": "vsw-london",
					"SecurityGroupIds": map[string]any{
						"SecurityGroupId": []any{"sg-d7ob1fsemhw32ptq0jav"},
					},
					"ServiceManaged": true,
					"ServiceID":      float64(429),
				},
			}},
		},
	}
	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	runtime, err := newRuntime(source, &runtimeFactory{client: client})
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType[networkInterfaceNativeType]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Source: "resource-center",
		Scope: asset.Scope{
			Kind: asset.ScopeRegion, NativeID: "eu-west-1", Location: "eu-west-1",
		},
		ResourceKind: &kind,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 1 {
		t.Fatalf("managed ENI batch = %#v", batch)
	}
	normalized := batch.Items[0].Normalized
	if normalized["vpc_id"] != "vpc-london" ||
		normalized["vswitch_id"] != "vsw-london" ||
		normalized[NormalizedServiceManagedField] != true ||
		normalized[NormalizedServiceIDField] != float64(429) ||
		normalized[NormalizedSecurityGroupIDsField] != "sg-d7ob1fsemhw32ptq0jav" {
		t.Fatalf("managed ENI normalized topology = %#v", normalized)
	}
}

func TestRuntimeScansSelectedResourceKindThroughProductAPISpec(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	factory := &runtimeFactory{invokeResult: contracts.InvocationResult{
		RequestID: "product-request",
		Data: map[string]any{
			"NextToken": "",
			"Disks": map[string]any{"Disk": []any{
				map[string]any{
					"DiskId": "d-a", "DiskName": "data", "Status": "Available",
					"CreationTime": "2026-08-01T00:00:00Z",
				},
			}},
		},
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType[diskNativeType]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		ResourceKind: &kind,
		Limit:        25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 ||
		factory.invocation.Operation != "AlibabaCloud.DescribeDisks" ||
		factory.invocation.Parameters["RegionId"] != "cn-hangzhou" ||
		factory.invocation.Parameters["MaxResults"] != 25 {
		t.Fatalf("source calls=%d invocation=%+v", source.calls, factory.invocation)
	}
	if len(batch.Items) != 1 ||
		batch.Items[0].NativeID != "d-a" ||
		batch.Items[0].Name != "data" ||
		batch.Items[0].State != "Available" ||
		batch.RequestID != "product-request" ||
		!batch.Complete {
		t.Fatalf("product API inventory batch=%+v", batch)
	}
}

func TestRuntimeScansNASMountTargetsByFileSystem(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	resourceCenter := &runtimeResourceCenterClient{page: ResourcePage{
		RequestID: "file-systems-request",
		Resources: []ResourceRecord{{
			ResourceType: "ACS::NAS::FileSystem",
			ResourceID:   "31a8e4",
			RegionID:     "cn-hangzhou",
		}},
	}}
	factory := &topologyRuntimeFactory{responses: map[string]contracts.InvocationResult{
		"AlibabaCloud.NAS.DescribeMountTargets": {
			RequestID: "mount-targets-request",
			Data: map[string]any{
				"PageNumber": 1, "PageSize": 100, "TotalCount": 1,
				"MountTargets": map[string]any{"MountTarget": []any{
					map[string]any{
						"MountTargetDomain": "31a8e4-w.cn-hangzhou.nas.aliyuncs.com",
						"Status":            "Active",
						"VpcId":             "vpc-a",
						"VswId":             "vsw-a",
					},
				}},
			},
		},
	}, resourceCenter: resourceCenter}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType["ACS::NAS::MountTarget"]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope: asset.Scope{
			Kind: asset.ScopeRegion, NativeID: "cn-hangzhou",
			Location: "cn-hangzhou",
		},
		ResourceKind: &kind,
		Limit:        100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resourceCenter.request.RegionID != "cn-hangzhou" ||
		!reflect.DeepEqual(
			resourceCenter.request.ResourceTypes,
			[]string{"ACS::NAS::FileSystem"},
		) ||
		len(resourceCenter.configurationRequests) != 0 {
		t.Fatalf("NAS Resource Center parent request = %+v", resourceCenter.request)
	}
	if len(factory.calls) != 1 ||
		factory.calls[0].Operation != "AlibabaCloud.NAS.DescribeMountTargets" ||
		factory.calls[0].Parameters["FileSystemId"] != "31a8e4" {
		t.Fatalf("NAS mount target discovery calls = %+v", factory.calls)
	}
	if len(batch.Items) != 1 ||
		batch.Items[0].NativeID != "31a8e4-w.cn-hangzhou.nas.aliyuncs.com" ||
		batch.Items[0].State != "Active" ||
		batch.Items[0].Normalized["fileSystemId"] != "31a8e4" ||
		batch.Items[0].Normalized["vpcId"] != "vpc-a" ||
		batch.Items[0].Normalized["vSwitchId"] != "vsw-a" ||
		batch.RequestID != "mount-targets-request" ||
		!batch.Complete {
		t.Fatalf("NAS mount target inventory batch = %+v", batch)
	}
}

func TestRuntimeScansFCFunctionsByService(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	resourceCenter := &runtimeResourceCenterClient{page: ResourcePage{
		RequestID: "services-request",
		Resources: []ResourceRecord{{
			ResourceType: "ACS::FC::Service",
			ResourceID:   "service-a",
			RegionID:     "cn-hangzhou",
		}},
	}}
	factory := &topologyRuntimeFactory{responses: map[string]contracts.InvocationResult{
		"AlibabaCloud.FC.ListFunctions": {
			RequestID: "functions-request",
			Data: map[string]any{
				"nextToken": "",
				"functions": []any{map[string]any{
					"functionId":       "function-id-a",
					"functionName":     "function-a",
					"runtime":          "custom-container",
					"lastModifiedTime": "2026-08-01T00:00:00Z",
				}},
			},
		},
	}, resourceCenter: resourceCenter}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType["ACS::FC::Function"]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope: asset.Scope{
			Kind: asset.ScopeRegion, NativeID: "cn-hangzhou",
			Location: "cn-hangzhou",
		},
		ResourceKind: &kind,
		Limit:        100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resourceCenter.request.RegionID != "cn-hangzhou" ||
		!reflect.DeepEqual(
			resourceCenter.request.ResourceTypes,
			[]string{"ACS::FC::Service"},
		) ||
		len(resourceCenter.configurationRequests) != 0 {
		t.Fatalf("FC Resource Center parent request = %+v", resourceCenter.request)
	}
	if len(factory.calls) != 1 ||
		factory.calls[0].Operation != "AlibabaCloud.FC.ListFunctions" ||
		factory.calls[0].Parameters["serviceName"] != "service-a" ||
		factory.calls[0].Parameters["limit"] != 100 {
		t.Fatalf("FC function discovery calls = %+v", factory.calls)
	}
	if len(batch.Items) != 1 ||
		batch.Items[0].NativeID != "function-id-a" ||
		batch.Items[0].Name != "function-a" ||
		batch.Items[0].Normalized["serviceName"] != "service-a" ||
		batch.Items[0].Normalized["functionName"] != "function-a" ||
		batch.Items[0].Normalized["runtime"] != "custom-container" ||
		batch.RequestID != "functions-request" ||
		!batch.Complete {
		t.Fatalf("FC function inventory batch = %+v", batch)
	}

	compiled, ok := runtime.compiledSpec("ACS::FC::Function")
	if !ok || len(compiled.Definition.Relationships) != 1 {
		t.Fatalf("FC function compiled relationships = %+v", compiled.Definition.Relationships)
	}
	relationship := compiled.Definition.Relationships[0]
	if relationship.Type != "member_of" ||
		relationship.TargetType != "ACS::FC::Service" ||
		relationship.TargetIDPath != "serviceName" {
		t.Fatalf("FC function relationship = %+v", relationship)
	}
}

func TestRuntimePropagatesFCFunctionSTSFailure(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	resourceCenter := &runtimeResourceCenterClient{page: ResourcePage{
		RequestID: "services-request",
		Resources: []ResourceRecord{{
			ResourceType: "ACS::FC::Service",
			ResourceID:   "service-a",
			RegionID:     "cn-hangzhou",
		}},
	}}
	factory := &topologyRuntimeFactory{
		resourceCenter: resourceCenter,
		invoke: func(contracts.Invocation) (contracts.InvocationResult, error) {
			return contracts.InvocationResult{}, NormalizeError(&APIError{
				Code:       "AccessDenied",
				Message:    "missing parameter SecurityToken",
				RequestID:  "fc-request",
				StatusCode: 403,
			})
		},
	}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType["ACS::FC::Function"]
	_, err = runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope: asset.Scope{
			Kind: asset.ScopeRegion, NativeID: "cn-hangzhou",
			Location: "cn-hangzhou",
		},
		ResourceKind: &kind,
		Limit:        100,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Code != "AccessDenied" ||
		providerError.Provider.Message != "missing parameter SecurityToken" ||
		providerError.Provider.RequestID != "fc-request" {
		t.Fatalf("FC Function STS failure = %#v", err)
	}
}

func TestRuntimeScansDhcpOptionsSetsAndCompilesVPCRelationship(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	factory := &topologyRuntimeFactory{responses: map[string]contracts.InvocationResult{
		"AlibabaCloud.ListDhcpOptionsSets": {
			RequestID: "dhcp-options-request",
			Data: map[string]any{
				"NextToken": "",
				"DhcpOptionsSets": []any{map[string]any{
					"DhcpOptionsSetId": "dopt-a", "DhcpOptionsSetName": "production-dns",
					"Status": "InUse", "AssociateVpcCount": 1,
				}},
			},
		},
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType["ACS::VPC::DhcpOptionsSet"]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope: asset.Scope{
			Kind: asset.ScopeRegion, NativeID: "cn-hangzhou",
			Location: "cn-hangzhou",
		},
		ResourceKind: &kind,
		Limit:        100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(batch.Items) != 1 || batch.Items[0].NativeID != "dopt-a" ||
		batch.Items[0].Name != "production-dns" || batch.Items[0].State != "InUse" ||
		batch.Items[0].Normalized["associateVpcCount"] != 1 ||
		batch.RequestID != "dhcp-options-request" || !batch.Complete {
		t.Fatalf("DHCP options set inventory batch = %+v", batch)
	}
	compiled, ok := runtime.compiledSpec(vpcNativeType)
	if !ok {
		t.Fatal("VPC compiled spec is missing")
	}
	found := false
	for _, relationship := range compiled.Definition.Relationships {
		if relationship.Type == "uses" &&
			relationship.TargetType == "ACS::VPC::DhcpOptionsSet" &&
			relationship.TargetIDPath == "dhcpOptionsSetId" {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("VPC relationships = %+v", compiled.Definition.Relationships)
	}
}

func TestRuntimeDoesNotCallNASWhenResourceCenterHasNoFileSystems(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	resourceCenter := &runtimeResourceCenterClient{page: ResourcePage{
		RequestID: "file-systems-request",
	}}
	factory := &topologyRuntimeFactory{
		responses:      map[string]contracts.InvocationResult{},
		resourceCenter: resourceCenter,
	}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType["ACS::NAS::MountTarget"]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope: asset.Scope{
			Kind: asset.ScopeRegion, NativeID: "cn-wulanchabu",
			Location: "cn-wulanchabu",
		},
		ResourceKind: &kind,
		Limit:        100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resourceCenter.request.RegionID != "cn-wulanchabu" ||
		len(factory.calls) != 0 ||
		len(batch.Items) != 0 ||
		!batch.Complete {
		t.Fatalf(
			"empty NAS Resource Center discovery request=%+v calls=%+v batch=%+v",
			resourceCenter.request,
			factory.calls,
			batch,
		)
	}
}

func TestRuntimeScansGlobalResourceKindThroughProductAPISpec(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	factory := &runtimeFactory{invokeResult: contracts.InvocationResult{
		RequestID: "cen-request",
		Data: map[string]any{
			"PageNumber": 1, "PageSize": 25, "TotalCount": 1,
			"Cens": map[string]any{"Cen": []any{
				map[string]any{
					"CenId": "cen-a", "Name": "backbone", "Status": "Active",
				},
			}},
		},
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType["ACS::CEN::CenInstance"]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope: asset.Scope{
			Kind: asset.ScopeGlobal, NativeID: "global", Name: "Global",
			Location: "cn-hangzhou",
		},
		ResourceKind: &kind,
		Limit:        25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.region != "cn-hangzhou" ||
		factory.invocation.Operation != "AlibabaCloud.CEN.DescribeCens" {
		t.Fatalf("region=%q invocation=%+v", factory.region, factory.invocation)
	}
	if len(batch.Items) != 1 ||
		batch.Items[0].NativeID != "cen-a" ||
		batch.Items[0].Scope.Kind != asset.ScopeGlobal ||
		batch.Items[0].Scope.NativeID != "global" ||
		batch.Items[0].Location != "cn-hangzhou" {
		t.Fatalf("global product API inventory batch=%+v", batch)
	}
}

func TestRuntimeRoutesCENVPCAttachmentsThroughTopologySource(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType[CENTransitRouterVPCAttachmentNativeType]
	if source := runtime.InventorySourceForResourceKind(kind, "cen-topology"); source != "cen-topology" {
		t.Fatalf("CEN VPC attachment inventory source = %q", source)
	}
	found := false
	for _, source := range runtime.InventorySources() {
		if source.Name == "cen-topology" {
			found = true
			if source.KindSpecific || !source.AuthoritativeDefault || !source.NetworkClosure {
				t.Fatalf("CEN topology source = %#v", source)
			}
		}
	}
	if !found {
		t.Fatal("CEN topology inventory source is missing")
	}
}

func TestRuntimeScansACKThroughRegionalProductAPI(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	factory := &runtimeFactory{invokeResult: contracts.InvocationResult{
		RequestID: "ack-request",
		Data: map[string]any{
			"clusters": []any{map[string]any{
				"cluster_id": "c-a", "name": "production", "state": "running",
			}},
			"page_info": map[string]any{"total_count": 1},
		},
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType["ACS::ACK::Cluster"]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-beijing"},
		ResourceKind: &kind,
		Limit:        250,
	})
	if err != nil {
		t.Fatal(err)
	}
	if source.calls != 1 ||
		factory.invocation.Operation != "AlibabaCloud.DescribeClustersForRegion" ||
		factory.invocation.Parameters["region_id"] != "cn-beijing" ||
		factory.invocation.Parameters["page_number"] != 1 ||
		factory.invocation.Parameters["page_size"] != 100 {
		t.Fatalf("source calls=%d invocation=%+v", source.calls, factory.invocation)
	}
	if len(batch.Items) != 1 || batch.Items[0].NativeID != "c-a" ||
		batch.Items[0].Name != "production" || batch.Items[0].State != "running" ||
		batch.RequestID != "ack-request" || !batch.Complete {
		t.Fatalf("ACK product API inventory batch=%+v", batch)
	}
}

func TestRuntimeSkipsACKInUnsupportedRegion(t *testing.T) {
	t.Parallel()

	source := &credentialSource{wantConnection: "connection-a"}
	factory := &runtimeFactory{}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType["ACS::ACK::Cluster"]
	_, err = runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-dalian"},
		ResourceKind: &kind,
		Limit:        100,
	})
	var providerError *contracts.ProviderCallError
	if !errors.As(err, &providerError) ||
		providerError.Provider.Category != execution.ErrorUnsupported ||
		providerError.Provider.Summary["skip_reason"] != string(asset.SkipProviderRegionUnavailable) {
		t.Fatalf("unsupported ACK region error=%#v", err)
	}
	if source.calls != 0 {
		t.Fatalf("credential source calls=%d", source.calls)
	}
}

func TestRuntimeScansBPStudioWithDocumentedPageLimit(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	factory := &runtimeFactory{invokeResult: contracts.InvocationResult{
		RequestID: "bpstudio-request",
		Data: map[string]any{
			"Data": []any{map[string]any{
				"ApplicationId": "app-a", "Name": "architecture", "Status": "Deployed_Success",
			}},
			"TotalCount": 1,
		},
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType["ACS::BPStudio::Application"]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope: asset.Scope{
			Kind: asset.ScopeGlobal, NativeID: "global", Location: "cn-hangzhou",
		},
		ResourceKind: &kind,
		Limit:        100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.invocation.Operation != "AlibabaCloud.BPStudio.ListApplication" ||
		factory.invocation.Parameters["NextToken"] != 1 ||
		factory.invocation.Parameters["MaxResults"] != 50 {
		t.Fatalf("invocation=%+v", factory.invocation)
	}
	if len(batch.Items) != 1 || batch.Items[0].NativeID != "app-a" || !batch.Complete {
		t.Fatalf("BPStudio product API inventory batch=%+v", batch)
	}
}

func TestRuntimeScansTSDBThroughDedicatedHitsDBProductSpec(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	factory := &runtimeFactory{invokeResult: contracts.InvocationResult{
		RequestID: "tsdb-request",
		Data: map[string]any{
			"PageNumber": 1, "PageSize": 25, "Total": 1,
			"InstanceList": []any{
				map[string]any{
					"InstanceId": "ts-a", "InstanceAlias": "metrics",
					"InstanceStatus": "ACTIVATION", "EngineType": "tsdb_tsdb",
					"ChargeType": "POSTPAY",
				},
			},
		},
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType["ACS::TSDB::Instance"]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		ResourceKind: &kind,
		Limit:        25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.region != "cn-hangzhou" ||
		factory.invocation.Operation !=
			"AlibabaCloud.TSDB.DescribeHiTSDBInstanceList" ||
		factory.invocation.Parameters["RegionId"] != "cn-hangzhou" ||
		factory.invocation.Parameters["EngineType"] != "tsdb_tsdb" ||
		factory.invocation.Parameters["PageNumber"] != 1 ||
		factory.invocation.Parameters["PageSize"] != 25 {
		t.Fatalf("region=%q invocation=%+v", factory.region, factory.invocation)
	}
	if len(batch.Items) != 1 ||
		batch.Items[0].NativeID != "ts-a" ||
		batch.Items[0].Name != "metrics" ||
		batch.Items[0].State != "ACTIVATION" ||
		batch.Items[0].Normalized["engineType"] != "tsdb_tsdb" ||
		batch.Items[0].Normalized["chargeType"] != "POSTPAY" {
		t.Fatalf("TSDB product API inventory batch=%+v", batch)
	}
}

func TestRuntimeScansGraphDatabaseThroughGDBProductSpec(t *testing.T) {
	t.Parallel()

	source := &credentialSource{
		wantConnection: "connection-a",
		value: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "id", "access_key_secret": "secret",
			},
		},
	}
	factory := &runtimeFactory{invokeResult: contracts.InvocationResult{
		RequestID: "gdb-request",
		Data: map[string]any{
			"PageNumber": 1, "PageSize": 25, "TotalCount": 1,
			"Items": map[string]any{"DBInstance": []any{
				map[string]any{
					"DBInstanceId": "gdb-a", "DBInstanceDescription": "graph",
					"DBInstanceStatus": "Running", "PayType": "Postpaid",
					"VpcId": "vpc-a", "VSwitchId": "vsw-a",
				},
			}},
		},
	}}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	kind := runtime.resourceKindByNativeType["ACS::GraphDatabase::DbInstance"]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		ResourceKind: &kind,
		Limit:        25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if factory.region != "cn-hangzhou" ||
		factory.invocation.Operation !=
			"AlibabaCloud.GDB.DescribeDBInstances" ||
		factory.invocation.Parameters["RegionId"] != "cn-hangzhou" ||
		factory.invocation.Parameters["PageNumber"] != 1 ||
		factory.invocation.Parameters["PageSize"] != 25 {
		t.Fatalf("region=%q invocation=%+v", factory.region, factory.invocation)
	}
	if len(batch.Items) != 1 ||
		batch.Items[0].NativeID != "gdb-a" ||
		batch.Items[0].Name != "graph" ||
		batch.Items[0].State != "Running" ||
		batch.Items[0].Normalized["payType"] != "Postpaid" ||
		batch.Items[0].Normalized["vpcId"] != "vpc-a" {
		t.Fatalf("GDB product API inventory batch=%+v", batch)
	}
}

func TestRuntimeListCopiesLocalizedFieldDisplayNames(t *testing.T) {
	t.Parallel()

	client := &runtimeResourceCenterClient{page: ResourcePage{Resources: []ResourceRecord{{
		ResourceType: "ACS::OSS::Bucket", ResourceID: "bucket-a",
	}}}}
	runtime, err := newRuntime(
		&credentialSource{wantConnection: "connection-a", value: contracts.Credential{Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"}}},
		&runtimeFactory{client: client},
	)
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	kind := runtime.resourceKindByNativeType["ACS::OSS::Bucket"]
	kind.FieldDisplayNames = map[string]map[string]string{
		"vpc_id": {"zh-CN": "所属专有网络"},
	}
	runtime.resourceKindByNativeType[kind.NativeType] = kind

	first, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-shanghai"},
	})
	if err != nil {
		t.Fatalf("list inventory: %v", err)
	}
	first.Items[0].ResourceKind.FieldDisplayNames["vpc_id"]["zh-CN"] = "mutated"

	second, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a",
		Scope:        asset.Scope{Kind: asset.ScopeRegion, NativeID: "cn-shanghai"},
	})
	if err != nil {
		t.Fatalf("list inventory again: %v", err)
	}
	if second.Items[0].ResourceKind.FieldDisplayNames["vpc_id"]["zh-CN"] != "所属专有网络" {
		t.Fatalf("returned inventory item mutated runtime resource kind: %+v", second.Items[0].ResourceKind.FieldDisplayNames)
	}
}

func TestRuntimeExposesInstanceAndModeledSubresourceKindsWithCatalogIcons(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatalf("create runtime: %v", err)
	}
	if _, ok := any(runtime).(contracts.ResourceKindMetadata); !ok {
		t.Fatal("Alibaba Cloud runtime does not expose resource kind metadata")
	}
	kinds, revision := runtime.ResourceKinds()
	if len(kinds) != 159 || revision == "" {
		t.Fatalf("resource kinds = %d revision = %q", len(kinds), revision)
	}

	ecs, found := findRuntimeResourceKind(kinds, "ACS::ECS::Instance")
	if !found {
		t.Fatal("ECS instance resource kind is missing")
	}
	if ecs.Icon != "/icons/alicloud/acs-ecs-instance.svg" ||
		ecs.Class != "compute.instance" ||
		!ecs.Capabilities.Has(asset.CapabilityIndexed) ||
		!ecs.Capabilities.Has(asset.CapabilityActionable) ||
		ecs.BundleRevision != revision {
		t.Fatalf("ECS resource kind = %+v revision = %q", ecs, revision)
	}

	oss, found := findRuntimeResourceKind(kinds, "ACS::OSS::Bucket")
	if !found {
		t.Fatal("OSS bucket resource kind is missing")
	}
	if oss.ID != "alicloud:ACS::OSS::Bucket" ||
		oss.Icon != "/icons/alicloud/acs-oss-bucket.svg" ||
		!oss.Capabilities.Has(asset.CapabilityIndexed) ||
		!oss.Capabilities.Has(asset.CapabilityActionable) ||
		len(oss.ScopeKinds) != 1 ||
		oss.ScopeKinds[0] != asset.ScopeRegion ||
		oss.BundleRevision != revision {
		t.Fatalf("OSS resource kind = %+v revision = %q", oss, revision)
	}
	if _, found := findRuntimeResourceKind(kinds, "ACS::ALB::Listener"); found {
		t.Fatal("ALB listener must not be exposed as an instance resource kind")
	}
	attachment, found := findRuntimeResourceKind(
		kinds,
		CENTransitRouterVPCAttachmentNativeType,
	)
	if !found ||
		attachment.Icon != "/icons/alicloud/acs-cen-transitrouter.svg" ||
		attachment.DisplayNames["zh-CN"] != "CEN 地域内连接" ||
		!attachment.Capabilities.Has(asset.CapabilityActionable) ||
		len(attachment.ScopeKinds) != 1 ||
		attachment.ScopeKinds[0] != asset.ScopeRegion {
		t.Fatalf("CEN VPC attachment resource kind = %+v found=%t", attachment, found)
	}
	snapshot, found := findRuntimeResourceKind(kinds, "ACS::ECS::Snapshot")
	if !found || !snapshot.Capabilities.Has(asset.CapabilityActionable) {
		t.Fatalf("snapshot resource kind = %+v found=%t", snapshot, found)
	}
	ack, found := findRuntimeResourceKind(kinds, ACKClusterNativeType)
	if !found || !ack.Capabilities.Has(asset.CapabilityActionable) {
		t.Fatalf("ACK resource kind = %+v found=%t", ack, found)
	}

	kinds[0].Icon = "mutated"
	if len(kinds[0].ScopeKinds) > 0 {
		kinds[0].ScopeKinds[0] = asset.ScopeGlobal
	}
	fresh, _ := runtime.ResourceKinds()
	if fresh[0].Icon == "mutated" ||
		(len(fresh[0].ScopeKinds) > 0 && fresh[0].ScopeKinds[0] == asset.ScopeGlobal) {
		t.Fatal("runtime resource kinds are not defensively copied")
	}
}

func TestRuntimeSpecCoverageMatchesActionableResourceKinds(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatal(err)
	}
	actionableSpecs := 0
	productAPISpecs := 0
	for _, compiled := range runtime.bundle.Specs {
		if compiled.Definition.Discovery.Source == "product-api" {
			productAPISpecs++
			if compiled.Definition.Discovery.List == nil {
				t.Fatalf("%s product API discovery has no list call", compiled.ResourceKind.NativeType)
			}
		}
		_, hasDelete := compiled.Definition.Actions["delete"]
		if hasDelete {
			actionableSpecs++
		}
		if hasDelete != compiled.ResourceKind.Capabilities.Has(asset.CapabilityActionable) {
			t.Fatalf(
				"%s delete=%t capabilities=%v",
				compiled.ResourceKind.NativeType,
				hasDelete,
				compiled.ResourceKind.Capabilities,
			)
		}
	}
	if len(runtime.bundle.Specs) != 159 || productAPISpecs != 145 || actionableSpecs != 139 {
		t.Fatalf(
			"specs=%d product-api=%d actionable=%d",
			len(runtime.bundle.Specs),
			productAPISpecs,
			actionableSpecs,
		)
	}
}

func TestRequestedCleanupResourcesUseDirectProductAPIs(t *testing.T) {
	t.Parallel()

	bundle, err := LoadBundle()
	if err != nil {
		t.Fatal(err)
	}
	expected := map[string]struct {
		source string
		list   string
		delete string
	}{
		"ACS::APIG::Gateway": {
			source: "product-api",
			list:   "AlibabaCloud.APIG.ListGateways",
			delete: "AlibabaCloud.APIG.DeleteGateway",
		},
		"ACS::AliKafka::Instance": {
			source: "product-api",
			list:   "AlibabaCloud.AliKafka.GetInstanceList",
			delete: "AlibabaCloud.AliKafka.ReleaseInstance",
		},
		"ACS::AckOne::Cluster": {
			source: "product-api",
			list:   "AlibabaCloud.AckOne.DescribeHubClusters",
			delete: "AlibabaCloud.AckOne.DeleteHubCluster",
		},
		"ACS::CEN::CenInstance": {
			source: "product-api",
			list:   "AlibabaCloud.CEN.DescribeCens",
			delete: "AlibabaCloud.CEN.DeleteCen",
		},
		CENChildInstanceAttachmentNativeType: {
			source: "cen-topology",
			list:   "AlibabaCloud.CEN.DescribeCenAttachedChildInstances",
			delete: "AlibabaCloud.CEN.DetachCenChildInstance",
		},
		CENTransitRouterVPCAttachmentNativeType: {
			source: "cen-topology",
			list:   "AlibabaCloud.CEN.ListTransitRouterVpcAttachments",
			delete: "AlibabaCloud.CEN.DeleteTransitRouterVpcAttachment",
		},
		CENTransitRouterPeerAttachmentNativeType: {
			source: "cen-topology",
			list:   "AlibabaCloud.CEN.ListTransitRouterPeerAttachments",
			delete: "AlibabaCloud.CEN.DeleteTransitRouterPeerAttachment",
		},
		CENTransitRouterRouteTableNativeType: {
			source: "cen-topology",
			list:   "AlibabaCloud.CEN.ListTransitRouterRouteTables",
			delete: "AlibabaCloud.CEN.DeleteTransitRouterRouteTable",
		},
		CENTransitRouterCidrNativeType: {
			source: "cen-topology",
			list:   "AlibabaCloud.CEN.ListTransitRouterCidr",
			delete: "AlibabaCloud.CEN.DeleteTransitRouterCidr",
		},
		CENRouteMapNativeType: {
			source: "cen-topology",
			list:   "AlibabaCloud.CEN.DescribeCenRouteMaps",
			delete: "AlibabaCloud.CEN.DeleteCenRouteMap",
		},
		"ACS::VPN::VpnConnection": {
			source: "product-api",
			list:   "DescribeVpnConnections",
			delete: "DeleteVpnConnection",
		},
		"ACS::Eflo::Cluster": {
			source: "product-api",
			list:   "AlibabaCloud.Eflo.ListClusters",
			delete: "AlibabaCloud.Eflo.DeleteCluster",
		},
		"ACS::TSDB::Instance": {
			source: "product-api",
			list:   "AlibabaCloud.TSDB.DescribeHiTSDBInstanceList",
			delete: "AlibabaCloud.TSDB.DeleteHiTSDBInstance",
		},
		"ACS::GraphDatabase::DbInstance": {
			source: "product-api",
			list:   "AlibabaCloud.GDB.DescribeDBInstances",
			delete: "AlibabaCloud.GDB.DeleteDBInstance",
		},
		"ACS::NAT::NatIp": {
			source: "product-api",
			list:   "ListNatIps",
			delete: "DeleteNatIp",
		},
		"ACS::NAS::MountTarget": {
			source: "product-api",
			list:   "AlibabaCloud.NAS.DescribeMountTargets",
			delete: "AlibabaCloud.NAS.DeleteMountTarget",
		},
	}
	for _, compiled := range bundle.Specs {
		want, ok := expected[compiled.ResourceKind.NativeType]
		if !ok {
			continue
		}
		if compiled.Definition.Discovery.Source != want.source ||
			compiled.Definition.Discovery.List == nil ||
			compiled.Definition.Discovery.List.Operation != want.list {
			t.Fatalf(
				"%s discovery=%+v, want direct product API %q",
				compiled.ResourceKind.NativeType,
				compiled.Definition.Discovery,
				want.list,
			)
		}
		action, ok := compiled.Definition.Actions["delete"]
		if !ok || action.Operation != want.delete {
			t.Fatalf(
				"%s delete=%+v, want direct product API %q",
				compiled.ResourceKind.NativeType,
				action,
				want.delete,
			)
		}
		delete(expected, compiled.ResourceKind.NativeType)
	}
	if len(expected) != 0 {
		t.Fatalf("missing requested resource specs: %v", expected)
	}
}

func TestNatGatewayCleanupForceDeletesAssociatedResources(t *testing.T) {
	t.Parallel()

	bundle, err := LoadBundle()
	if err != nil {
		t.Fatal(err)
	}
	for _, compiled := range bundle.Specs {
		if compiled.ResourceKind.NativeType != "ACS::NAT::NatGateway" {
			continue
		}
		deleteAction, ok := compiled.Definition.Actions["delete"]
		if !ok {
			t.Fatal("NAT gateway spec is missing its delete action")
		}
		if force, ok := deleteAction.Parameters["Force"].(bool); !ok || !force {
			t.Fatalf("NAT gateway delete Force=%#v, want true", deleteAction.Parameters["Force"])
		}
		return
	}
	t.Fatal("NAT gateway compiled spec is missing")
}

func TestUnsupportedInstanceResourceAuditIsExplicit(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatal(err)
	}
	wantScanOnly := map[string]struct{}{
		"ACS::Alidns::DnsGtmInstance":             {},
		"ACS::Alidns::GtmInstance":                {},
		"ACS::Bastionhost::Instance":              {},
		CENFlowLogNativeType:                      {},
		CENInterRegionTrafficQosPolicyNativeType:  {},
		CENTrafficMarkingPolicyNativeType:         {},
		CENTransitRouterECRAttachmentNativeType:   {},
		CENTransitRouterMulticastDomainNativeType: {},
		CENTransitRouterVBRAttachmentNativeType:   {},
		CENTransitRouterVPNAttachmentNativeType:   {},
		"ACS::CR::Instance":                       {},
		"ACS::DataV::Workspace":                   {},
		"ACS::EBS::DedicatedBlockStorageCluster":  {},
		"ACS::ECS::ElasticityAssurance":           {},
		"ACS::EmrServerlessSpark::Workspace":      {},
		"ACS::FC::Function":                       {},
		"ACS::RTC::Application":                   {},
		"ACS::SDDP::Instance":                     {},
		"ACS::SWAS::Instance":                     {},
		"ACS::ThreatDetection::Instance":          {},
	}
	wantDirectoryOnly := map[string]struct{}{}
	specTypes := make(map[string]struct{}, len(runtime.bundle.Specs))
	gotScanOnly := make(map[string]struct{}, len(wantScanOnly))
	for _, compiled := range runtime.bundle.Specs {
		nativeType := compiled.ResourceKind.NativeType
		specTypes[nativeType] = struct{}{}
		if _, actionable := compiled.Definition.Actions["delete"]; !actionable {
			gotScanOnly[nativeType] = struct{}{}
		}
	}
	gotDirectoryOnly := make(map[string]struct{}, len(wantDirectoryOnly))
	for _, kind := range runtime.resourceKinds {
		if _, hasSpec := specTypes[kind.NativeType]; !hasSpec {
			gotDirectoryOnly[kind.NativeType] = struct{}{}
		}
	}
	assertNativeTypeSet(t, "product API scan-only", gotScanOnly, wantScanOnly)
	assertNativeTypeSet(t, "Resource Center directory-only", gotDirectoryOnly, wantDirectoryOnly)
}

func TestEveryInstanceResourceSpecDeclaresConsoleLink(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatal(err)
	}
	if len(runtime.bundle.Specs) != len(runtime.resourceKinds) {
		t.Fatalf(
			"compiled specs=%d resource kinds=%d",
			len(runtime.bundle.Specs),
			len(runtime.resourceKinds),
		)
	}
	for _, compiled := range runtime.bundle.Specs {
		if compiled.ResourceKind.ConsoleLinkTemplate == "" {
			t.Errorf(
				"%s does not declare presentation.consoleLinkTemplate",
				compiled.ResourceKind.NativeType,
			)
		}
	}
}

func TestNetworkInterfaceConsoleLinkTemplate(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://ecs.console.aliyun.com/networkInterfaces/region/{regionId}/detail/{nativeId}"
	kind, ok := runtime.resourceKindByNativeType[networkInterfaceNativeType]
	if !ok {
		t.Fatalf("missing resource kind %q", networkInterfaceNativeType)
	}
	if kind.ConsoleLinkTemplate != want {
		t.Fatalf("console link template = %q, want %q", kind.ConsoleLinkTemplate, want)
	}
}

func TestPrometheusConsoleLinkTemplate(t *testing.T) {
	t.Parallel()

	runtime, err := newRuntime(&credentialSource{}, &runtimeFactory{})
	if err != nil {
		t.Fatal(err)
	}
	const want = "https://cms.console.aliyun.com/prom/instances/details?clusterId={nativeId}&regionId={regionId}"
	kind, ok := runtime.resourceKindByNativeType[PrometheusNativeType]
	if !ok {
		t.Fatalf("missing resource kind %q", PrometheusNativeType)
	}
	if kind.ConsoleLinkTemplate != want {
		t.Fatalf("console link template = %q, want %q", kind.ConsoleLinkTemplate, want)
	}
}

func assertNativeTypeSet(
	t *testing.T,
	name string,
	got map[string]struct{},
	want map[string]struct{},
) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s count=%d, want %d: got=%v", name, len(got), len(want), got)
	}
	for nativeType := range want {
		if _, ok := got[nativeType]; !ok {
			t.Fatalf("%s is missing %q: got=%v", name, nativeType, got)
		}
	}
}

func findRuntimeResourceKind(values []asset.ResourceKind, nativeType string) (asset.ResourceKind, bool) {
	for _, value := range values {
		if value.NativeType == nativeType {
			return value, true
		}
	}
	return asset.ResourceKind{}, false
}

func TestRuntimeSearchesVPCsAndVSwitchesThroughLiveAPI(t *testing.T) {
	t.Parallel()

	source := &credentialSource{wantConnection: "connection-a", value: contracts.Credential{Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"}}}
	client := &runtimeResourceCenterClient{page: ResourcePage{
		RequestID: "req-vpc",
		Resources: []ResourceRecord{
			{RegionID: "cn-hangzhou", ResourceType: vpcNativeType, ResourceID: "vpc-a", ResourceName: "production"},
			{RegionID: "cn-hangzhou", ResourceType: vpcNativeType, ResourceID: "vpc-b", ResourceName: "sandbox"},
		},
	}}
	factory := &runtimeFactory{client: client}
	runtime, err := newRuntime(source, factory)
	if err != nil {
		t.Fatal(err)
	}
	page, err := runtime.SearchNetworkTargets(context.Background(), contracts.NetworkTargetQuery{
		ConnectionID: "connection-a", Kind: asset.ScanTargetVPC, RegionID: "cn-hangzhou", Query: "prod", Limit: 20,
	})
	if err != nil ||
		len(page.Items) != 1 ||
		page.Items[0].NativeID != "vpc-a" ||
		page.Items[0].Name != "production" ||
		page.Items[0].ParentNativeID != "" ||
		page.RequestID != "req-vpc" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	if factory.invocation.Operation != "" || factory.region != "cn-hangzhou" ||
		client.request.SearchExpression != "prod" ||
		len(client.request.ResourceTypes) != 1 || client.request.ResourceTypes[0] != vpcNativeType {
		t.Fatalf("invocation=%#v region=%q search=%#v", factory.invocation, factory.region, client.request)
	}

	client.page = ResourcePage{RequestID: "req-vsw", Resources: []ResourceRecord{{
		RegionID: "cn-hangzhou", ResourceType: vSwitchNativeType, ResourceID: "vsw-a", ResourceName: "app",
	}}}
	client.configurationPage = ResourceConfigurationPage{
		RequestID: "cfg-vsw",
		Resources: []ResourceRecord{{
			RegionID: "cn-hangzhou", ResourceType: vSwitchNativeType, ResourceID: "vsw-a",
			ResourceName: "app", Configuration: map[string]any{"VpcId": "vpc-a"},
		}},
	}
	page, err = runtime.SearchNetworkTargets(context.Background(), contracts.NetworkTargetQuery{
		ConnectionID: "connection-a", Kind: asset.ScanTargetVSwitch, RegionID: "cn-hangzhou", ParentNativeID: "vpc-a", Limit: 20,
	})
	if err != nil || len(page.Items) != 1 || page.Items[0].NativeID != "vsw-a" || page.Items[0].ParentNativeID != "vpc-a" {
		t.Fatalf("page=%#v err=%v", page, err)
	}
	if factory.invocation.Operation != "" || client.request.VpcID != "vpc-a" {
		t.Fatalf("invocation=%#v search=%#v", factory.invocation, client.request)
	}
}

func TestRuntimeRejectsInvalidNetworkTargetQueryBeforeCredentialResolution(t *testing.T) {
	t.Parallel()

	source := &credentialSource{wantConnection: "connection-a"}
	runtime, err := newRuntime(source, &runtimeFactory{})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runtime.SearchNetworkTargets(context.Background(), contracts.NetworkTargetQuery{ConnectionID: "connection-a", Kind: asset.ScanTargetGlobal})
	if err == nil || source.calls != 0 {
		t.Fatalf("err=%v credential calls=%d", err, source.calls)
	}
}

func TestContractCredentialBuildsSDKCredentialWithoutEmbeddingSecretsInRuntime(t *testing.T) {
	t.Parallel()

	credential, err := cloudCredential(contracts.Credential{Values: map[string]string{
		"type": "sts", "access_key_id": "id", "access_key_secret": "secret", "security_token": "token",
	}})
	if err != nil {
		t.Fatalf("build SDK credential: %v", err)
	}
	model, err := credential.GetCredential()
	if err != nil {
		t.Fatalf("resolve SDK credential: %v", err)
	}
	if model.AccessKeyId == nil || *model.AccessKeyId != "id" || model.SecurityToken == nil || *model.SecurityToken != "token" {
		t.Fatalf("SDK credential model=%+v", model)
	}
}
