package alicloud

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	resourcecenterclient "github.com/alibabacloud-go/resourcecenter-20221201/client"
	"github.com/alibabacloud-go/tea/dara"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type resourceCenterSDKCaller struct {
	searchRequest        *resourcecenterclient.SearchResourcesRequest
	configurationRequest *resourcecenterclient.BatchGetResourceConfigurationsRequest
	searchRuntime        *dara.RuntimeOptions
	configurationRuntime *dara.RuntimeOptions
	searchTimeout        time.Duration
	configurationTimeout time.Duration
	searchFailures       []error
	searchCalls          int
}

func (c *resourceCenterSDKCaller) BatchGetResourceConfigurationsWithContext(
	ctx context.Context,
	request *resourcecenterclient.BatchGetResourceConfigurationsRequest,
	runtime *dara.RuntimeOptions,
) (*resourcecenterclient.BatchGetResourceConfigurationsResponse, error) {
	c.configurationRequest = request
	c.configurationRuntime = runtime
	if deadline, ok := ctx.Deadline(); ok {
		c.configurationTimeout = time.Until(deadline)
	}
	return &resourcecenterclient.BatchGetResourceConfigurationsResponse{
		Body: &resourcecenterclient.BatchGetResourceConfigurationsResponseBody{
			RequestId: dara.String("configuration-request"),
			Resources: []*resourcecenterclient.BatchGetResourceConfigurationsResponseBodyResources{{
				AccountId: dara.String("1234567890123456"),
				Configuration: map[string]interface{}{
					"VpcId": "vpc-a",
				},
				RegionId: dara.String("cn-hangzhou"), ResourceId: dara.String("vsw-a"),
				ResourceName: dara.String("application"), ResourceType: dara.String("ACS::VPC::VSwitch"),
				ZoneId:      dara.String("cn-hangzhou-h"),
				IpAddresses: []*string{dara.String("10.0.0.1")},
				IpAddressAttributes: []*resourcecenterclient.BatchGetResourceConfigurationsResponseBodyResourcesIpAddressAttributes{{
					IpAddress: dara.String("10.0.0.1"), NetworkType: dara.String("Private"), Version: dara.String("Ipv4"),
				}},
				Tags: []*resourcecenterclient.BatchGetResourceConfigurationsResponseBodyResourcesTags{{
					Key: dara.String("environment"), Value: dara.String("production"),
				}},
			}},
		},
	}, nil
}

func TestResourceCenterConfigKeepsRegionAndUsesInternationalEndpoint(t *testing.T) {
	credential, err := cloudCredential(contracts.Credential{
		Type: asset.CredentialAliCloudAccessKey,
		Values: map[string]string{
			"access_key_id": "id", "access_key_secret": "secret",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	config, err := openAPIConfig(
		credential,
		asset.ConnectionSiteINTL,
		serviceResourceCenter,
		"ap-southeast-1",
		"",
	)
	if err != nil {
		t.Fatal(err)
	}
	if dara.StringValue(config.RegionId) != "ap-southeast-1" ||
		dara.StringValue(config.Endpoint) != "resourcecenter-intl.aliyuncs.com" {
		t.Fatalf("Resource Center config = %#v", config)
	}
}

func (c *resourceCenterSDKCaller) SearchResourcesWithContext(ctx context.Context, request *resourcecenterclient.SearchResourcesRequest, runtime *dara.RuntimeOptions) (*resourcecenterclient.SearchResourcesResponse, error) {
	c.searchCalls++
	c.searchRequest = request
	c.searchRuntime = runtime
	if deadline, ok := ctx.Deadline(); ok {
		c.searchTimeout = time.Until(deadline)
	}
	if len(c.searchFailures) > 0 {
		err := c.searchFailures[0]
		c.searchFailures = c.searchFailures[1:]
		if err != nil {
			return nil, err
		}
	}
	return &resourcecenterclient.SearchResourcesResponse{Body: &resourcecenterclient.SearchResourcesResponseBody{}}, nil
}

func TestSDKResourceCenterSearchResourcesUsesOneMultiValueTypeFilter(t *testing.T) {
	t.Parallel()

	caller := &resourceCenterSDKCaller{}
	_, err := (&sdkResourceCenter{client: caller}).SearchResources(context.Background(), SearchRequest{
		NextToken:  "page-2",
		MaxResults: 500,
		RegionID:   "cn-hangzhou",
		ResourceTypes: []string{
			"ACS::ECS::Instance",
			"ACS::OSS::Bucket",
			"ACS::VPC::VPC",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if caller.searchRequest == nil ||
		dara.StringValue(caller.searchRequest.NextToken) != "page-2" ||
		len(caller.searchRequest.Filter) != 2 {
		t.Fatalf("search request = %#v", caller.searchRequest)
	}
	if dara.IntValue(caller.searchRuntime.ReadTimeout) != int(resourceCenterCallTimeout.Milliseconds()) ||
		caller.searchTimeout <= 4*time.Second ||
		caller.searchTimeout > resourceCenterCallTimeout {
		t.Fatalf("search runtime=%#v timeout=%v", caller.searchRuntime, caller.searchTimeout)
	}
	var resourceTypeFilter *resourcecenterclient.SearchResourcesRequestFilter
	for _, filter := range caller.searchRequest.Filter {
		if dara.StringValue(filter.Key) == "ResourceType" {
			resourceTypeFilter = filter
		}
	}
	if resourceTypeFilter == nil || len(resourceTypeFilter.Value) != 3 {
		t.Fatalf("resource type filter = %#v", resourceTypeFilter)
	}
	want := []string{"ACS::ECS::Instance", "ACS::OSS::Bucket", "ACS::VPC::VPC"}
	for index, value := range resourceTypeFilter.Value {
		if dara.StringValue(value) != want[index] {
			t.Fatalf("resource type value %d = %q, want %q", index, dara.StringValue(value), want[index])
		}
	}
}

func TestSDKResourceCenterBatchGetResourceConfigurations(t *testing.T) {
	t.Parallel()

	caller := &resourceCenterSDKCaller{}
	page, err := (&sdkResourceCenter{client: caller}).BatchGetResourceConfigurations(
		context.Background(),
		ResourceConfigurationRequest{Resources: []ResourceConfigurationReference{{
			RegionID: "cn-hangzhou", ResourceType: "ACS::VPC::VSwitch", ResourceID: "vsw-a",
		}}},
	)
	if err != nil {
		t.Fatalf("batch get resource configurations: %v", err)
	}
	if caller.configurationRequest == nil || len(caller.configurationRequest.Resources) != 1 {
		t.Fatalf("configuration request = %#v", caller.configurationRequest)
	}
	requested := caller.configurationRequest.Resources[0]
	if dara.StringValue(requested.RegionId) != "cn-hangzhou" ||
		dara.StringValue(requested.ResourceType) != "ACS::VPC::VSwitch" ||
		dara.StringValue(requested.ResourceId) != "vsw-a" {
		t.Fatalf("configuration resource = %#v", requested)
	}
	if dara.IntValue(caller.configurationRuntime.ReadTimeout) != int(resourceCenterCallTimeout.Milliseconds()) ||
		caller.configurationTimeout <= 4*time.Second ||
		caller.configurationTimeout > resourceCenterCallTimeout {
		t.Fatalf("configuration runtime=%#v timeout=%v", caller.configurationRuntime, caller.configurationTimeout)
	}
	if page.RequestID != "configuration-request" || len(page.Resources) != 1 {
		t.Fatalf("configuration page = %#v", page)
	}
	resource := page.Resources[0]
	if resource.ResourceID != "vsw-a" || resource.Configuration["VpcId"] != "vpc-a" ||
		len(resource.IPAddresses) != 1 || len(resource.IPAddressAttributes) != 1 ||
		len(resource.Tags) != 1 {
		t.Fatalf("configuration resource = %#v", resource)
	}
}

func TestSDKResourceCenterRetriesTransientSearchFailures(t *testing.T) {
	t.Parallel()

	temporary := errors.New("temporary Resource Center failure")
	caller := &resourceCenterSDKCaller{searchFailures: []error{
		temporary, temporary, temporary, nil,
	}}
	if _, err := (&sdkResourceCenter{client: caller}).SearchResources(
		context.Background(),
		SearchRequest{MaxResults: 100, RegionID: "cn-hangzhou"},
	); err != nil {
		t.Fatal(err)
	}
	if caller.searchCalls != resourceCenterRetryCount+1 {
		t.Fatalf("search calls=%d, want %d", caller.searchCalls, resourceCenterRetryCount+1)
	}
	if dara.IntValue(caller.searchRuntime.ReadTimeout) != int(resourceCenterCallTimeout.Milliseconds()) ||
		dara.IntValue(caller.searchRuntime.IdleTimeout) != int(cloudProductIdleTimeout.Milliseconds()) ||
		caller.searchTimeout <= 4*time.Second || caller.searchTimeout > resourceCenterCallTimeout {
		t.Fatalf("search runtime=%#v timeout=%v", caller.searchRuntime, caller.searchTimeout)
	}
}

type roaInvocation struct {
	action      string
	method      string
	path        string
	query       map[string]*string
	headers     map[string]*string
	body        any
	readTimeout int
	idleTimeout int
	timeout     time.Duration
}

type roaCaller struct {
	responses []map[string]any
	failures  []error
	calls     []roaInvocation
}

func (c *roaCaller) CallApiWithCtx(ctx context.Context, params *openapi.Params, request *openapi.OpenApiRequest, runtime *dara.RuntimeOptions) (map[string]interface{}, error) {
	timeout := time.Duration(0)
	if deadline, ok := ctx.Deadline(); ok {
		timeout = time.Until(deadline)
	}
	c.calls = append(c.calls, roaInvocation{
		action: dara.StringValue(params.Action), method: dara.StringValue(params.Method),
		path: dara.StringValue(params.Pathname), query: request.Query, headers: request.Headers, body: request.Body,
		readTimeout: dara.IntValue(runtime.ReadTimeout), idleTimeout: dara.IntValue(runtime.IdleTimeout), timeout: timeout,
	})
	if len(c.failures) > 0 {
		err := c.failures[0]
		c.failures = c.failures[1:]
		if err != nil {
			return nil, err
		}
	}
	response := c.responses[0]
	c.responses = c.responses[1:]
	return response, nil
}

func (c *roaCaller) ExecuteWithCtx(ctx context.Context, params *openapi.Params, request *openapi.OpenApiRequest, runtime *dara.RuntimeOptions) (map[string]interface{}, error) {
	return c.CallApiWithCtx(ctx, params, request, runtime)
}

func TestSDKACKDeleteUsesControllerEndpointAndReturnsProviderMetadata(t *testing.T) {
	t.Parallel()

	caller := &roaCaller{responses: []map[string]any{{
		"body":    map[string]any{"cluster_id": "c-a", "task_id": "task-a"},
		"headers": map[string]*string{"x-acs-request-id": dara.String("delete-request")},
	}}}
	client := &sdkACK{caller: caller}
	response, err := client.DeleteCluster(context.Background(), DeleteClusterRequest{
		ClusterID: "c-a", RetainResources: []string{"vpc-a"},
		DeleteOptions: []DeleteOption{{ResourceType: "SLB", DeleteMode: "delete"}},
	})
	if err != nil {
		t.Fatalf("delete ACK cluster: %v", err)
	}
	if response.ClusterID != "c-a" || response.TaskID != "task-a" || response.RequestID != "delete-request" {
		t.Fatalf("delete response=%+v", response)
	}
	if len(caller.calls) != 1 || caller.calls[0].action != "DeleteCluster" || caller.calls[0].method != "DELETE" || caller.calls[0].path != "/clusters/c-a" {
		t.Fatalf("ROA call=%+v", caller.calls)
	}
	var retained []string
	if err := json.Unmarshal([]byte(dara.StringValue(caller.calls[0].query["retain_resources"])), &retained); err != nil || len(retained) != 1 || retained[0] != "vpc-a" {
		t.Fatalf("retained query=%q err=%v", dara.StringValue(caller.calls[0].query["retain_resources"]), err)
	}
	var options []DeleteOption
	if err := json.Unmarshal([]byte(dara.StringValue(caller.calls[0].query["delete_options"])), &options); err != nil || len(options) != 1 || options[0].ResourceType != "SLB" {
		t.Fatalf("delete options query=%q err=%v", dara.StringValue(caller.calls[0].query["delete_options"]), err)
	}
}

func TestSDKACKReadsAndDisablesClusterDeletionProtection(t *testing.T) {
	t.Parallel()

	caller := &roaCaller{responses: []map[string]any{
		{
			"body": map[string]any{
				"cluster_id": "c-protected", "state": "running",
				"deletion_protection": true,
			},
			"headers": map[string]*string{
				"x-acs-request-id": dara.String("detail-request"),
			},
		},
		{
			"body": map[string]any{},
			"headers": map[string]*string{
				"x-acs-request-id": dara.String("modify-request"),
			},
		},
	}}
	client := &sdkACK{caller: caller}
	detail, requestID, err := client.DescribeClusterDetail(
		context.Background(),
		"c-protected",
	)
	if err != nil || requestID != "detail-request" ||
		detail.ClusterID != "c-protected" || !detail.DeletionProtection {
		t.Fatalf("detail=%+v requestID=%q err=%v", detail, requestID, err)
	}
	requestID, err = client.ModifyClusterDeletionProtection(
		context.Background(),
		"c-protected",
		false,
	)
	if err != nil || requestID != "modify-request" {
		t.Fatalf("modify requestID=%q err=%v", requestID, err)
	}
	if len(caller.calls) != 2 ||
		caller.calls[0].action != "DescribeClusterDetail" ||
		caller.calls[0].method != "GET" ||
		caller.calls[0].path != "/clusters/c-protected" ||
		caller.calls[1].action != "ModifyCluster" ||
		caller.calls[1].method != "PUT" ||
		caller.calls[1].path != "/api/v2/clusters/c-protected" {
		t.Fatalf("ROA calls=%+v", caller.calls)
	}
	body, ok := caller.calls[1].body.(map[string]any)
	if !ok || body["deletion_protection"] != false {
		t.Fatalf("modify body=%#v", caller.calls[1].body)
	}
}

func TestSDKACKNodeSourceAloneNeverClaimsAutoCreation(t *testing.T) {
	t.Parallel()

	caller := &roaCaller{responses: []map[string]any{{
		"body": map[string]any{
			"nodes": []any{map[string]any{"instance_id": "i-a", "nodepool_id": "np-a", "source": "ess"}},
			"page":  map[string]any{"total_count": 1},
		},
		"headers": map[string]*string{"x-acs-request-id": dara.String("node-request")},
	}}}
	nodes, requestID, err := (&sdkACK{caller: caller}).DescribeClusterNodes(context.Background(), "c-a")
	if err != nil {
		t.Fatalf("describe ACK nodes: %v", err)
	}
	if requestID != "node-request" || len(nodes) != 1 || nodes[0].InstanceID != "i-a" || nodes[0].AutoCreated != nil {
		t.Fatalf("nodes=%+v requestID=%q", nodes, requestID)
	}
}

func TestSDKACKDescribeClusterResourcesAcceptsEmptyObject(t *testing.T) {
	t.Parallel()

	caller := &roaCaller{responses: []map[string]any{{
		"body":    map[string]any{},
		"headers": map[string]*string{"x-acs-request-id": dara.String("resource-request")},
	}}}
	resources, requestID, err := (&sdkACK{caller: caller}).DescribeClusterResources(
		context.Background(),
		"c-a",
		true,
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(resources) != 0 || requestID != "resource-request" {
		t.Fatalf("resources=%+v requestID=%q", resources, requestID)
	}
	if len(caller.calls) != 1 || caller.calls[0].path != "/clusters/c-a/resources" {
		t.Fatalf("ROA calls=%+v", caller.calls)
	}
}

func TestProductAPICallerUsesCatalogTransportAndInjectsIdempotency(t *testing.T) {
	t.Parallel()

	caller := &roaCaller{responses: []map[string]any{{
		"body":    map[string]any{"RequestId": "req-vpc"},
		"headers": map[string]*string{},
	}}}
	operation := catalog.Operation{
		ID: "AlibabaCloud.DeleteVpc", Name: "DeleteVpc",
		Call: &catalog.OperationCall{
			Product: "Vpc", Version: "2016-04-28", Style: "RPC", Protocol: "HTTPS",
			Method: "POST", Path: "/", Endpoint: "vpc.{region}.aliyuncs.com",
			RequestBodyType: "formData", BodyType: "json", ParameterPosition: "query",
			IdempotencyParameter: "ClientToken",
		},
	}
	result, err := invokeProductAPICaller(context.Background(), caller, operation, contracts.Invocation{
		Operation: "AlibabaCloud.DeleteVpc", Parameters: map[string]any{
			"VpcId": "vpc-a", "ForceDelete": false,
		},
		IdempotencyKey: "step-a",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestID != "req-vpc" || len(caller.calls) != 1 {
		t.Fatalf("result=%+v calls=%+v", result, caller.calls)
	}
	call := caller.calls[0]
	if call.action != "DeleteVpc" || call.method != "POST" || call.path != "/" ||
		dara.StringValue(call.query["VpcId"]) != "vpc-a" ||
		dara.StringValue(call.query["ForceDelete"]) != "false" ||
		dara.StringValue(call.query["ClientToken"]) != "step-a" {
		t.Fatalf("product API call=%+v", call)
	}
}

func TestProductAPICallerPreservesRawOSSObjectPathForSigning(t *testing.T) {
	t.Parallel()

	caller := &roaCaller{responses: []map[string]any{{
		"body": map[string]any{},
	}}}
	operation := catalog.Operation{
		ID: "AlibabaCloud.OSS.DeleteObject", Name: "DeleteObject", Destructive: true,
		Call: &catalog.OperationCall{
			Product: "Oss", Version: "2019-05-17", Style: "ROA", Protocol: "HTTPS",
			Method: "DELETE", Path: "/{object}", Endpoint: "oss-{location}.aliyuncs.com",
			EndpointParameters: []string{"location"}, HostParameters: []string{"bucket"},
			RawPathParameters: []string{"object"}, RequestBodyType: "xml", BodyType: "xml",
			ParameterPosition: "query",
		},
	}
	_, err := invokeProductAPICaller(context.Background(), caller, operation, contracts.Invocation{
		Parameters: map[string]any{
			"location": "cn-shanghai", "bucket": "bucket-a",
			"object": `logs/["cn-beijing"]/snapshot.json`, "versionId": "version-a",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 || caller.calls[0].path != `/logs/["cn-beijing"]/snapshot.json` ||
		dara.StringValue(caller.calls[0].query["versionId"]) != "version-a" {
		t.Fatalf("OSS DeleteObject call=%+v", caller.calls)
	}
}

func TestProductAPICallerSendsOSSHDFSHeadersAndXMLBody(t *testing.T) {
	t.Parallel()

	caller := &roaCaller{responses: []map[string]any{{
		"body":    map[string]any{},
		"headers": map[string]*string{"x-oss-request-id": dara.String("request-dls")},
	}}}
	operation := catalog.Operation{
		ID: "AlibabaCloud.OSS.PutBucketHDFSConfig", Name: "PostDataLakeStorageAdminOperation",
		Destructive: true,
		Call: &catalog.OperationCall{
			Product: "Oss", Version: "2019-05-17", Style: "ROA", Protocol: "HTTPS",
			Method: "POST", Path: "/?dfsadmin", Endpoint: "bucket.region-cross.oss-dls.aliyuncs.com",
			HeaderParameters: []string{
				"x-oss-dfs-ns", "x-oss-dfs-requester", "x-oss-dfs-source-addr",
				"x-oss-hdfs-extend-field",
			},
			RequestBodyType: "xml", BodyType: "xml", ParameterPosition: "body",
		},
	}
	_, err := invokeProductAPICaller(context.Background(), caller, operation, contracts.Invocation{
		Parameters: map[string]any{
			"x-oss-dfs-ns": "bucket-a", "x-oss-dfs-requester": "steward",
			"x-oss-dfs-source-addr": "steward", "x-oss-hdfs-extend-field": "putConfig",
			"request": map[string]any{
				"instanceName": "bucket-a", "requestType": "putConfig",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := caller.calls[0].body.(map[string]any)
	requestBody, _ := body["request"].(map[string]any)
	if len(caller.calls) != 1 || caller.calls[0].path != "/?dfsadmin" ||
		dara.StringValue(caller.calls[0].headers["x-oss-hdfs-extend-field"]) != "putConfig" ||
		requestBody["requestType"] != "putConfig" || len(caller.calls[0].query) != 0 {
		t.Fatalf("OSS HDFS admin call=%+v", caller.calls)
	}
}

func TestProductAPICallerSendsOSSVersioningConfigurationAsXMLBody(t *testing.T) {
	t.Parallel()

	caller := &roaCaller{responses: []map[string]any{{
		"body":    map[string]any{},
		"headers": map[string]*string{"x-oss-request-id": dara.String("put-versioning-request")},
	}}}
	operation := catalog.Operation{
		ID: "AlibabaCloud.OSS.PutBucketVersioning", Name: "PutBucketVersioning",
		Destructive: true,
		Call: &catalog.OperationCall{
			Product: "Oss", Version: "2019-05-17", Style: "ROA", Protocol: "HTTPS",
			Method: "PUT", Path: "/?versioning", Endpoint: "oss-{location}.aliyuncs.com",
			EndpointParameters: []string{"location"}, HostParameters: []string{"bucket"},
			RequestBodyType: "xml", BodyType: "xml", ParameterPosition: "body",
		},
	}
	result, err := invokeProductAPICaller(context.Background(), caller, operation, contracts.Invocation{
		Operation: operation.ID,
		Parameters: map[string]any{
			"location": "cn-hangzhou", "bucket": "bucket-a",
			"VersioningConfiguration": map[string]any{"Status": "Suspended"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestID != "put-versioning-request" || len(caller.calls) != 1 {
		t.Fatalf("result=%+v calls=%+v", result, caller.calls)
	}
	call := caller.calls[0]
	body, _ := call.body.(map[string]any)
	configuration, _ := body["VersioningConfiguration"].(map[string]any)
	if call.method != "PUT" || call.path != "/?versioning" ||
		configuration["Status"] != "Suspended" || len(call.query) != 0 {
		t.Fatalf("PutBucketVersioning call=%+v", call)
	}
}

func TestRequestIDFromUsesProductSpecificResponseHeader(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		productCode string
		headers     map[string]*string
		want        string
	}{
		{name: "OSS", productCode: "Oss", headers: map[string]*string{"x-oss-request-id": dara.String("oss-request")}, want: "oss-request"},
		{name: "TableStore", productCode: "Tablestore", headers: map[string]*string{"x-ots-requestid": dara.String("ots-request")}, want: "ots-request"},
		{name: "Function Compute", productCode: "FC-Open", headers: map[string]*string{"x-fc-request-id": dara.String("fc-request")}, want: "fc-request"},
		{name: "Log Service", productCode: "Sls", headers: map[string]*string{"x-log-requestid": dara.String("sls-request")}, want: "sls-request"},
		{name: "standard OpenAPI", productCode: "Ecs", headers: map[string]*string{"x-acs-request-id": dara.String("acs-request")}, want: "acs-request"},
		{name: "does not read another product header", productCode: "Oss", headers: map[string]*string{"x-ots-requestid": dara.String("ots-request")}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if got := requestIDFrom(test.productCode, nil, test.headers); got != test.want {
				t.Fatalf("requestIDFrom() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestProductAPICallerBoundsLongClientToken(t *testing.T) {
	t.Parallel()

	caller := &roaCaller{responses: []map[string]any{{
		"body": map[string]any{"RequestId": "req-router-interface"},
	}}}
	operation := catalog.Operation{
		ID: "AlibabaCloud.DeleteRouterInterface", Name: "DeleteRouterInterface",
		Call: &catalog.OperationCall{
			Product: "Vpc", Version: "2016-04-28", Style: "RPC", Protocol: "HTTPS",
			Method: "POST", Path: "/", Endpoint: "vpc.{region}.aliyuncs.com",
			RequestBodyType: "formData", BodyType: "json", ParameterPosition: "query",
			IdempotencyParameter: "ClientToken",
		},
	}
	longKey := "action-" + strings.Repeat("a", 64)
	_, err := invokeProductAPICaller(context.Background(), caller, operation, contracts.Invocation{
		Parameters: map[string]any{"RouterInterfaceId": "ri-a"}, IdempotencyKey: longKey,
	})
	if err != nil {
		t.Fatal(err)
	}
	token := dara.StringValue(caller.calls[0].query["ClientToken"])
	if len(token) != 64 || token == longKey {
		t.Fatalf("bounded ClientToken=%q length=%d", token, len(token))
	}
}

func TestSLSProductAPIUsesItsOfficialGateway(t *testing.T) {
	t.Parallel()

	client := &openapi.Client{}
	useExecutor, err := configureProductAPIGateway(client, "Sls", "GetLogs")
	if err != nil {
		t.Fatal(err)
	}
	if !useExecutor || client.Spi == nil {
		t.Fatalf("SLS gateway executor=%t spi=%T", useExecutor, client.Spi)
	}

	caller := &roaCaller{responses: []map[string]any{{
		"body": map[string]any{
			"projects": []any{map[string]any{"projectName": "project-a"}},
			"total":    float64(1),
		},
		"headers": map[string]*string{"x-log-requestid": dara.String("request-sls")},
	}}}
	operation := catalog.Operation{
		ID: "AlibabaCloud.SLS.ListProject", Name: "ListProject",
		Call: &catalog.OperationCall{
			Product: "Sls", Version: "2020-12-30", Style: "ROA",
			Protocol: "HTTPS", Method: "GET", Path: "/",
			Endpoint: "{region}.log.aliyuncs.com", RequestBodyType: "json",
			BodyType: "json", ParameterPosition: "query",
		},
	}
	result, err := invokeProductAPICaller(
		context.Background(),
		caller,
		operation,
		contracts.Invocation{
			Operation: operation.ID,
			Parameters: map[string]any{
				"projectName": "project-a", "offset": 0, "size": 1,
			},
		},
		productAPICallOptions{UseGatewayExecutor: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestID != "request-sls" || len(caller.calls) != 1 {
		t.Fatalf("result=%+v calls=%+v", result, caller.calls)
	}
	call := caller.calls[0]
	if call.action != "ListProject" || call.method != "GET" || call.path != "/" ||
		dara.StringValue(call.query["projectName"]) != "project-a" {
		t.Fatalf("SLS product API call=%+v", call)
	}
}

func TestSLSDeleteProjectAcceptsStringResponseAndKeepsRequestID(t *testing.T) {
	t.Parallel()

	body := ""
	caller := &roaCaller{responses: []map[string]any{{
		"body":    &body,
		"headers": map[string]*string{"x-log-requestid": dara.String("delete-project-request")},
	}}}
	operation := catalog.Operation{
		ID: "AlibabaCloud.SLS.DeleteProject", Name: "DeleteProject", Destructive: true,
		Call: &catalog.OperationCall{
			Product: "Sls", Version: "2020-12-30", Style: "ROA",
			Protocol: "HTTPS", Method: "DELETE", Path: "/",
			Endpoint: "{project}.{region}.log.aliyuncs.com", RequestBodyType: "json",
			BodyType: "json", ParameterPosition: "query",
		},
	}

	result, err := invokeProductAPICaller(
		context.Background(),
		caller,
		operation,
		contracts.Invocation{
			Operation: operation.ID,
			Parameters: map[string]any{
				"project": "actiontrail-log", "forceDelete": true,
			},
		},
		productAPICallOptions{UseGatewayExecutor: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestID != "delete-project-request" || len(result.Data) != 0 {
		t.Fatalf("result=%+v", result)
	}
	if len(caller.calls) != 1 ||
		dara.StringValue(caller.calls[0].query["forceDelete"]) != "true" {
		t.Fatalf("DeleteProject call=%+v", caller.calls)
	}
}

func TestSLSUpdateProjectSendsDeletionProtectionInRequestBody(t *testing.T) {
	t.Parallel()

	caller := &roaCaller{responses: []map[string]any{{
		"body":    "",
		"headers": map[string]*string{"x-log-requestid": dara.String("update-project-request")},
	}}}
	operation := catalog.Operation{
		ID: "AlibabaCloud.SLS.UpdateProject", Name: "UpdateProject", Destructive: true,
		Call: &catalog.OperationCall{
			Product: "Sls", Version: "2020-12-30", Style: "ROA",
			Protocol: "HTTPS", Method: "PUT", Path: "/",
			Endpoint: "{project}.{region}.log.aliyuncs.com", RequestBodyType: "json",
			BodyType: "none", ParameterPosition: "body",
		},
	}
	result, err := invokeProductAPICaller(
		context.Background(),
		caller,
		operation,
		contracts.Invocation{
			Operation: operation.ID,
			Parameters: map[string]any{
				"description": "Managed by CMS Workspace", "deletionProtection": false,
			},
		},
		productAPICallOptions{UseGatewayExecutor: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestID != "update-project-request" || len(caller.calls) != 1 {
		t.Fatalf("result=%+v calls=%+v", result, caller.calls)
	}
	body, ok := caller.calls[0].body.(map[string]any)
	if !ok ||
		body["description"] != "Managed by CMS Workspace" ||
		body["deletionProtection"] != false {
		t.Fatalf("UpdateProject body=%#v", caller.calls[0].body)
	}
}

func TestProductAPICallerRetriesQueriesThreeTimesWithFiveSecondTimeout(t *testing.T) {
	t.Parallel()

	temporary := errors.New("temporary product API failure")
	caller := &roaCaller{
		failures: []error{temporary, temporary, temporary, nil},
		responses: []map[string]any{{
			"body": map[string]any{"RequestId": "req-vswitch"},
		}},
	}
	operation := catalog.Operation{
		ID: "AlibabaCloud.DescribeVSwitches", Name: "DescribeVSwitches",
		Call: &catalog.OperationCall{
			Product: "Vpc", Version: "2016-04-28", Style: "RPC",
			Protocol: "HTTPS", Method: "POST", Path: "/",
			Endpoint: "vpc.{region}.aliyuncs.com", ParameterPosition: "query",
		},
	}

	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(
		func(_ context.Context, entry execution.JobLogEntry) {
			logs = append(logs, entry)
		},
	))
	result, err := invokeProductAPICaller(ctx, caller, operation, contracts.Invocation{
		Operation: operation.ID,
		Parameters: map[string]any{
			"RegionId": "us-east-1", "VSwitchId": "vsw-a",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.RequestID != "req-vswitch" || len(caller.calls) != cloudProductQueryRetryCount+1 {
		t.Fatalf("result=%+v calls=%+v", result, caller.calls)
	}
	for index, call := range caller.calls {
		if call.readTimeout != int(cloudProductQueryTimeout.Milliseconds()) ||
			call.idleTimeout != int(cloudProductIdleTimeout.Milliseconds()) ||
			call.timeout <= 4*time.Second ||
			call.timeout > cloudProductQueryTimeout {
			t.Fatalf("call %d timeout policy=%+v", index, call)
		}
	}
	if len(logs) != 6 {
		t.Fatalf("retry logs=%#v", logs)
	}
	for index := 0; index < 3; index++ {
		failedAttempt := index + 1
		nextAttempt := index + 2
		failure := logs[index*2]
		retry := logs[index*2+1]
		if failure.Kind != execution.JobLogCloudAPIResponse ||
			failure.Level != "info" ||
			failure.Message != fmt.Sprintf("vpc DescribeVSwitches (attempt %d) failed: temporary product API failure", failedAttempt) ||
			retry.Kind != execution.JobLogCloudAPIRequest ||
			retry.Message != fmt.Sprintf("call vpc DescribeVSwitches (attempt %d)", nextAttempt) {
			t.Fatalf("retry log pair %d = %#v %#v", index, failure, retry)
		}
	}
}

func TestProductAPICallerRetriesDNSLookupFailures(t *testing.T) {
	t.Parallel()

	lookupFailure := &net.DNSError{
		Err:        "no such host",
		Name:       "vpc.cn-chengdu.aliyuncs.com",
		IsNotFound: true,
	}
	caller := &roaCaller{
		failures: []error{lookupFailure, nil},
		responses: []map[string]any{{
			"body": map[string]any{"RequestId": "req-nat"},
		}},
	}
	operation := catalog.Operation{
		ID: "AlibabaCloud.DescribeNatGateways", Name: "DescribeNatGateways",
		Call: &catalog.OperationCall{
			Product: "Vpc", Version: "2016-04-28", Style: "RPC",
			Protocol: "HTTPS", Method: "POST", Path: "/",
			Endpoint: "vpc.{region}.aliyuncs.com", ParameterPosition: "query",
		},
	}

	result, err := invokeProductAPICaller(
		context.Background(),
		caller,
		operation,
		contracts.Invocation{
			Operation: operation.ID,
			Parameters: map[string]any{
				"RegionId":     "cn-chengdu",
				"NatGatewayId": "ngw-2vc0qygs7rkx1algk6urj",
				"PageSize":     1,
			},
		},
	)
	if err != nil || result.RequestID != "req-nat" || len(caller.calls) != 2 {
		t.Fatalf("result=%+v err=%v calls=%+v", result, err, caller.calls)
	}
}

func TestProductAPICallerDoesNotRetryDestructiveOperations(t *testing.T) {
	t.Parallel()

	caller := &roaCaller{failures: []error{errors.New("delete failed")}}
	operation := catalog.Operation{
		ID: "AlibabaCloud.DeleteVpc", Name: "DeleteVpc", Destructive: true,
		Call: &catalog.OperationCall{
			Product: "Vpc", Version: "2016-04-28", Style: "RPC",
			Protocol: "HTTPS", Method: "POST", Path: "/",
			Endpoint: "vpc.{region}.aliyuncs.com", ParameterPosition: "query",
		},
	}

	_, err := invokeProductAPICaller(context.Background(), caller, operation, contracts.Invocation{
		Operation: operation.ID, Parameters: map[string]any{"VpcId": "vpc-a"},
	})
	if err == nil {
		t.Fatal("expected delete failure")
	}
	if len(caller.calls) != 1 ||
		caller.calls[0].readTimeout != 0 ||
		caller.calls[0].timeout != 0 {
		t.Fatalf("destructive calls=%+v", caller.calls)
	}
}

func TestProductAPICallerDoesNotRetryAmbiguousNetworkErrorsForDestructiveOperations(t *testing.T) {
	t.Parallel()

	networkError := errors.New("Post \"https://ecs.us-west-1.aliyuncs.com\": read tcp: connection reset by peer")
	caller := &roaCaller{failures: []error{networkError, nil}}
	operation := catalog.Operation{
		ID: "AlibabaCloud.DeleteSnapshot", Name: "DeleteSnapshot", Destructive: true,
		Call: &catalog.OperationCall{
			Product: "Ecs", Version: "2014-05-26", Style: "RPC",
			Protocol: "HTTPS", Method: "POST", Path: "/",
			Endpoint: "ecs.{region}.aliyuncs.com", ParameterPosition: "query",
		},
	}

	_, err := invokeProductAPICaller(context.Background(), caller, operation, contracts.Invocation{
		Operation: operation.ID, Parameters: map[string]any{"SnapshotId": "s-a"},
	})
	if !errors.Is(err, networkError) || len(caller.calls) != 1 {
		t.Fatalf("err=%v destructive calls=%+v", err, caller.calls)
	}
}

func TestProductAPICallerRetriesPreconnectBadFileDescriptorForDestructiveOperations(t *testing.T) {
	t.Parallel()

	networkError := errors.New("Post \"https://ecs.us-west-1.aliyuncs.com\": dial tcp: connect: bad file descriptor")
	caller := &roaCaller{
		failures: []error{networkError, nil},
		responses: []map[string]any{{
			"body": map[string]any{"RequestId": "req-delete"},
		}},
	}
	operation := catalog.Operation{
		ID: "AlibabaCloud.DeleteSnapshot", Name: "DeleteSnapshot", Destructive: true,
		Call: &catalog.OperationCall{
			Product: "Ecs", Version: "2014-05-26", Style: "RPC",
			Protocol: "HTTPS", Method: "POST", Path: "/",
			Endpoint: "ecs.{region}.aliyuncs.com", ParameterPosition: "query",
		},
	}

	result, err := invokeProductAPICaller(context.Background(), caller, operation, contracts.Invocation{
		Operation: operation.ID, Parameters: map[string]any{"SnapshotId": "s-a"},
	})
	if err != nil || result.RequestID != "req-delete" || len(caller.calls) != 2 {
		t.Fatalf("result=%+v err=%v destructive calls=%+v", result, err, caller.calls)
	}
}

func TestProductAPIEndpointPrefersCatalogRegionOverride(t *testing.T) {
	t.Parallel()

	call := &catalog.OperationCall{
		Endpoint: "rds.{region}.aliyuncs.com",
		EndpointOverrides: map[string]string{
			"cn-hangzhou": "rds.aliyuncs.com",
		},
	}
	hangzhou, err := productAPIEndpoint(
		call,
		asset.ConnectionSiteCN,
		"cn-hangzhou",
		map[string]any{},
	)
	if err != nil || hangzhou != "rds.aliyuncs.com" {
		t.Fatalf("Hangzhou endpoint=%q err=%v", hangzhou, err)
	}
	shanghai, err := productAPIEndpoint(
		call,
		asset.ConnectionSiteCN,
		"cn-shanghai",
		map[string]any{},
	)
	if err != nil || shanghai != "rds.cn-shanghai.aliyuncs.com" {
		t.Fatalf("Shanghai endpoint=%q err=%v", shanghai, err)
	}
}

func TestProductAPIEndpointConsumesConfiguredHostParameter(t *testing.T) {
	t.Parallel()

	call := &catalog.OperationCall{
		Endpoint:           "{project}.{region}.log.aliyuncs.com",
		EndpointParameters: []string{"project"},
	}
	parameters := map[string]any{"project": "project-a", "forceDelete": false}
	endpoint, err := productAPIEndpoint(
		call,
		asset.ConnectionSiteCN,
		"cn-hangzhou",
		parameters,
	)
	if err != nil || endpoint != "project-a.cn-hangzhou.log.aliyuncs.com" {
		t.Fatalf("endpoint=%q err=%v", endpoint, err)
	}
	if _, exists := parameters["project"]; exists {
		t.Fatalf("endpoint parameter leaked into API request: %+v", parameters)
	}
	if parameters["forceDelete"] != false {
		t.Fatalf("non-endpoint parameter was changed: %+v", parameters)
	}
	_, err = productAPIEndpoint(
		call,
		asset.ConnectionSiteCN,
		"cn-hangzhou",
		map[string]any{"project": "project-a.evil"},
	)
	if err == nil {
		t.Fatal("endpoint parameter containing multiple DNS labels must fail")
	}
}

func TestProductAPIEndpointPrefersConnectionSiteEndpoint(t *testing.T) {
	t.Parallel()

	call := &catalog.OperationCall{
		Endpoint: "esa.cn-hangzhou.aliyuncs.com",
		SiteEndpoints: map[asset.ConnectionSite]string{
			asset.ConnectionSiteCN:   "esa.cn-hangzhou.aliyuncs.com",
			asset.ConnectionSiteINTL: "esa.ap-southeast-1.aliyuncs.com",
		},
	}
	endpoint, err := productAPIEndpoint(
		call,
		asset.ConnectionSiteINTL,
		"ap-northeast-1",
		map[string]any{},
	)
	if err != nil || endpoint != "esa.ap-southeast-1.aliyuncs.com" {
		t.Fatalf("endpoint=%q err=%v", endpoint, err)
	}
}

func TestBastionhostProductAPICallUsesOfficialRegionalEndpointOverrides(t *testing.T) {
	t.Parallel()

	providerCatalog, err := loadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	operation, ok := providerCatalog.Operation(
		"AlibabaCloud.Bastionhost.DescribeInstances",
	)
	if !ok || operation.Call == nil {
		t.Fatalf("Bastionhost catalog operation=%+v found=%t", operation, ok)
	}
	tests := map[string]string{
		"cn-hangzhou":    "yundun-bastionhost.aliyuncs.com",
		"cn-zhangjiakou": "bastionhost.cn-zhangjiakou.aliyuncs.com",
		"eu-central-1":   "bastionhost.eu-central-1.aliyuncs.com",
	}
	for region, want := range tests {
		endpoint, err := productAPIEndpoint(
			operation.Call,
			asset.ConnectionSiteCN,
			region,
			map[string]any{},
		)
		if err != nil || endpoint != want {
			t.Fatalf(
				"region=%q endpoint=%q err=%v, want %q",
				region,
				endpoint,
				err,
				want,
			)
		}
	}
}

func TestModifyImageSharePermissionUsesECSRPCMetadata(t *testing.T) {
	t.Parallel()

	providerCatalog, err := loadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	operation, ok := providerCatalog.Operation("AlibabaCloud.ModifyImageSharePermission")
	if !ok || operation.Call == nil {
		t.Fatalf("ModifyImageSharePermission catalog operation=%+v found=%t", operation, ok)
	}
	if operation.Call.Product != "Ecs" ||
		operation.Call.Version != "2014-05-26" ||
		operation.Call.Style != "RPC" ||
		operation.Call.Method != "POST" {
		t.Fatalf("ModifyImageSharePermission call metadata=%+v", operation.Call)
	}
}

func TestDescribeImageSharePermissionUsesECSRPCMetadata(t *testing.T) {
	t.Parallel()

	providerCatalog, err := loadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	operation, ok := providerCatalog.Operation("AlibabaCloud.DescribeImageSharePermission")
	if !ok || operation.Call == nil {
		t.Fatalf("DescribeImageSharePermission catalog operation=%+v found=%t", operation, ok)
	}
	if operation.Call.Product != "Ecs" ||
		operation.Call.Version != "2014-05-26" ||
		operation.Call.Style != "RPC" ||
		operation.Call.Method != "POST" {
		t.Fatalf("DescribeImageSharePermission call metadata=%+v", operation.Call)
	}
}

func TestGraphDatabaseProductAPICallUsesOfficialSDKEndpointMap(t *testing.T) {
	t.Parallel()

	providerCatalog, err := loadCatalog()
	if err != nil {
		t.Fatal(err)
	}
	operation, ok := providerCatalog.Operation(
		"AlibabaCloud.GDB.DescribeDBInstances",
	)
	if !ok || operation.Call == nil {
		t.Fatalf("GDB catalog operation=%+v found=%t", operation, ok)
	}
	tests := map[string]string{
		"cn-hangzhou":    "gdb-api.aliyuncs.com",
		"cn-zhangjiakou": "gdb-api.aliyuncs.com",
		"cn-hongkong":    "gdb-api.aliyuncs.com",
		"ap-southeast-1": "gdb-api.aliyuncs.com",
		"ap-south-1":     "gdb-api.ap-south-1.aliyuncs.com",
	}
	for region, want := range tests {
		endpoint, err := productAPIEndpoint(
			operation.Call,
			asset.ConnectionSiteCN,
			region,
			map[string]any{},
		)
		if err != nil || endpoint != want {
			t.Fatalf(
				"region=%q endpoint=%q err=%v, want %q",
				region,
				endpoint,
				err,
				want,
			)
		}
	}
}

func TestFunctionComputeQueryUsesItsOfficialGateway(t *testing.T) {
	t.Parallel()

	client := &openapi.Client{}
	useExecutor, err := configureProductAPIGateway(client, "FC-Open", "ListServices")
	if err != nil {
		t.Fatal(err)
	}
	if !useExecutor || client.Spi == nil {
		t.Fatalf("Function Compute gateway executor=%t spi=%T", useExecutor, client.Spi)
	}
}

func TestFunctionComputeDeleteServiceUsesGenericACS3Caller(t *testing.T) {
	t.Parallel()

	client := &openapi.Client{}
	useExecutor, err := configureProductAPIGateway(client, "FC-Open", "DeleteService")
	if err != nil {
		t.Fatal(err)
	}
	if useExecutor || client.Spi != nil {
		t.Fatalf("Function Compute DeleteService gateway executor=%t spi=%T", useExecutor, client.Spi)
	}
}

func TestProductAPICallerUsesCatalogBodyTransport(t *testing.T) {
	t.Parallel()

	caller := &roaCaller{responses: []map[string]any{{
		"body": map[string]any{"RequestId": "req-ots"},
	}}}
	operation := catalog.Operation{
		ID: "AlibabaCloud.OTS.DeleteInstance", Name: "DeleteInstance",
		Call: &catalog.OperationCall{
			Product: "Tablestore", Version: "2020-12-09", Style: "ROA",
			Protocol: "HTTPS", Method: "POST", Path: "/v2/openapi/deleteinstance",
			Endpoint: "tablestore.{region}.aliyuncs.com", RequestBodyType: "json",
			BodyType: "json", ParameterPosition: "body",
		},
	}
	_, err := invokeProductAPICaller(context.Background(), caller, operation, contracts.Invocation{
		Operation:  operation.ID,
		Parameters: map[string]any{"InstanceName": "instance-a"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(caller.calls) != 1 {
		t.Fatalf("product API body calls=%+v", caller.calls)
	}
	body, bodyOK := caller.calls[0].body.(map[string]any)
	if !bodyOK ||
		body["InstanceName"] != "instance-a" ||
		len(caller.calls[0].query) != 0 {
		t.Fatalf("product API body call=%+v", caller.calls)
	}
}

func TestProductAPICallerWrapsTopLevelArrayResponse(t *testing.T) {
	t.Parallel()

	caller := &roaCaller{responses: []map[string]any{{
		"body": []any{
			map[string]any{"cluster_id": "c-a", "name": "cluster-a"},
		},
		"headers": map[string]*string{"x-acs-request-id": dara.String("req-ack-list")},
	}}}
	operation := catalog.Operation{
		ID: "AlibabaCloud.DescribeClusters", Name: "DescribeClusters",
		Call: &catalog.OperationCall{
			Product: "CS", Version: "2015-12-15", Style: "ROA",
			Protocol: "HTTPS", Method: "GET", Path: "/clusters",
			Endpoint: "cs.{region}.aliyuncs.com", RequestBodyType: "json",
			BodyType: "json", ParameterPosition: "query",
		},
	}
	result, err := invokeProductAPICaller(
		context.Background(),
		caller,
		operation,
		contracts.Invocation{Operation: operation.ID},
	)
	if err != nil {
		t.Fatal(err)
	}
	items, ok := result.Data["items"].([]any)
	if !ok || len(items) != 1 || result.RequestID != "req-ack-list" {
		t.Fatalf("array response result=%+v", result)
	}
}

func TestQueryParametersFlattensRPCStringArrays(t *testing.T) {
	t.Parallel()

	query := queryParameters(map[string]any{
		"NetworkInterfaceId": []string{"eni-a", "eni-b"},
	})
	if dara.StringValue(query["NetworkInterfaceId.1"]) != "eni-a" ||
		dara.StringValue(query["NetworkInterfaceId.2"]) != "eni-b" {
		t.Fatalf("flattened query=%+v", query)
	}
	if _, exists := query["NetworkInterfaceId"]; exists {
		t.Fatalf("array query retained unflattened parameter: %+v", query)
	}
}
