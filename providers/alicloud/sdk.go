package alicloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	fcgateway "github.com/alibabacloud-go/alibabacloud-gateway-fc/client"
	ossgateway "github.com/alibabacloud-go/alibabacloud-gateway-oss/client"
	slsgateway "github.com/alibabacloud-go/alibabacloud-gateway-sls/client"
	csclient "github.com/alibabacloud-go/cs-20151215/client"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	openapiutils "github.com/alibabacloud-go/darabonba-openapi/v2/utils"
	resourcecenterclient "github.com/alibabacloud-go/resourcecenter-20221201/client"
	stsclient "github.com/alibabacloud-go/sts-20150401/v2/client"
	roaclient "github.com/alibabacloud-go/tea-roa/client"
	"github.com/alibabacloud-go/tea/dara"
	"github.com/alibabacloud-go/tea/tea"
	vpcclient "github.com/alibabacloud-go/vpc-20160428/v7/client"
	cloudcredentials "github.com/aliyun/credentials-go/credentials"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	containerServiceAPIDate     = "2015-12-15"
	containerServiceProductCode = "CS"
)

type sdkClientFactory struct{}

const alicloudRegionBootstrap = "cn-hangzhou"

func (sdkClientFactory) CallerIdentity(ctx context.Context, credential contracts.Credential) (string, string, error) {
	if err := ctx.Err(); err != nil {
		return "", "", err
	}
	cloudCredential, err := cloudCredential(credential)
	if err != nil {
		return "", "", err
	}
	config, err := openAPIConfig(cloudCredential, credential.Site, serviceSTS, "cn-hangzhou", "")
	if err != nil {
		return "", "", err
	}
	client, err := stsclient.NewClient(config)
	if err != nil {
		return "", "", fmt.Errorf("create Alibaba Cloud STS client: %w", err)
	}
	response, err := callCloudProductQuery(ctx, func(_ context.Context, options *dara.RuntimeOptions) (*stsclient.GetCallerIdentityResponse, error) {
		return client.GetCallerIdentityWithOptions(options)
	})
	if err != nil {
		return "", "", err
	}
	if response == nil || response.Body == nil {
		return "", "", fmt.Errorf("Alibaba Cloud GetCallerIdentity response body is empty")
	}
	return dara.StringValue(response.Body.AccountId), dara.StringValue(response.Body.Arn), nil
}

func (sdkClientFactory) DiscoverRegions(ctx context.Context, credential contracts.Credential) ([]providerRegion, error) {
	cloudCredential, err := cloudCredential(credential)
	if err != nil {
		return nil, err
	}
	config, err := openAPIConfig(cloudCredential, credential.Site, serviceVPC, alicloudRegionBootstrap, "")
	if err != nil {
		return nil, err
	}
	client, err := vpcclient.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("create Alibaba Cloud VPC client: %w", err)
	}
	return discoverRegionsWithVPC(ctx, client)
}

type vpcRegionCaller interface {
	DescribeRegionsWithContext(context.Context, *vpcclient.DescribeRegionsRequest, *dara.RuntimeOptions) (*vpcclient.DescribeRegionsResponse, error)
}

func discoverRegionsWithVPC(ctx context.Context, client vpcRegionCaller) ([]providerRegion, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	response, err := callCloudProductQuery(ctx, func(attemptCtx context.Context, options *dara.RuntimeOptions) (*vpcclient.DescribeRegionsResponse, error) {
		return client.DescribeRegionsWithContext(
			attemptCtx,
			&vpcclient.DescribeRegionsRequest{AcceptLanguage: dara.String("zh-CN")},
			options,
		)
	})
	if err != nil {
		return nil, err
	}
	if response == nil || response.Body == nil || response.Body.Regions == nil {
		return nil, fmt.Errorf("Alibaba Cloud DescribeRegions response body is empty")
	}
	regions := make([]providerRegion, 0, len(response.Body.Regions.Region))
	for _, region := range response.Body.Regions.Region {
		if region == nil {
			continue
		}
		regions = append(regions, providerRegion{
			RegionID: dara.StringValue(region.RegionId),
			Name:     dara.StringValue(region.LocalName),
			Endpoint: dara.StringValue(region.RegionEndpoint),
		})
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return regions, nil
}

func (sdkClientFactory) ResourceCenter(_ context.Context, credential contracts.Credential, region string) (ResourceCenterClient, error) {
	cloudCredential, err := cloudCredential(credential)
	if err != nil {
		return nil, err
	}
	config, err := openAPIConfig(cloudCredential, credential.Site, serviceResourceCenter, region, "")
	if err != nil {
		return nil, err
	}
	client, err := resourcecenterclient.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("create Alibaba Cloud Resource Center client: %w", err)
	}
	return &sdkResourceCenter{client: client}, nil
}

func (sdkClientFactory) Invoke(
	ctx context.Context,
	credential contracts.Credential,
	region string,
	operation catalog.Operation,
	invocation contracts.Invocation,
) (contracts.InvocationResult, error) {
	cloudCredential, err := cloudCredential(credential)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	if operation.Call != nil {
		return invokeProductAPI(ctx, cloudCredential, credential.Site, region, operation, invocation)
	}
	switch invocation.Operation {
	case "AlibabaCloud.SearchResources":
		return contracts.InvocationResult{}, fmt.Errorf("SearchResources is exposed through the inventory adapter")
	default:
		return contracts.InvocationResult{}, fmt.Errorf("Alibaba Cloud operation %q has no runtime handler", invocation.Operation)
	}
}

func invokeProductAPI(
	ctx context.Context,
	credential cloudcredentials.Credential,
	site asset.ConnectionSite,
	region string,
	operation catalog.Operation,
	invocation contracts.Invocation,
) (contracts.InvocationResult, error) {
	call := operation.Call
	if call == nil {
		return contracts.InvocationResult{}, fmt.Errorf("Alibaba Cloud operation %q has no product API call metadata", operation.Key())
	}
	parameters := clonedParameters(invocation.Parameters)
	endpoint, err := productAPIEndpoint(call, site, region, parameters)
	if err != nil {
		return contracts.InvocationResult{}, fmt.Errorf(
			"resolve Alibaba Cloud operation %q endpoint: %w",
			operation.Key(),
			err,
		)
	}
	config, err := openAPIConfig(credential, site, strings.ToLower(call.Product), region, endpoint)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	// Keep HttpClient unset so Tea can reuse its endpoint-scoped transports.
	// A per-call client with keep-alives disabled caused bursts of fresh
	// sockets, which surfaced intermittent connect(2) EBADF errors on macOS.
	client, err := openapi.NewClient(config)
	if err != nil {
		return contracts.InvocationResult{}, fmt.Errorf("create Alibaba Cloud %s product client: %w", call.Product, err)
	}
	useGatewayExecutor, err := configureProductAPIGateway(client, call.Product, operation.Name)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	invocation.Parameters = parameters
	return invokeProductAPICaller(
		ctx,
		client,
		operation,
		invocation,
		productAPICallOptions{UseGatewayExecutor: useGatewayExecutor},
	)
}

func configureProductAPIGateway(client *openapi.Client, product, operation string) (bool, error) {
	switch {
	case strings.EqualFold(product, "FC-Open") && strings.EqualFold(operation, "DeleteService"):
		// FC 2.0 DeleteService uses the account-level endpoint and ACS3 signing.
		// The FC gateway response adapter only recognizes the regional fc.*
		// endpoint as POP and otherwise misreads ServiceNotFound as a server
		// error. The generic Darabonba caller uses the same ACS3 request format
		// as the FC 2.0 API workbench and preserves its 404 response.
		return false, nil
	case strings.EqualFold(product, "FC-Open"):
		spi, err := fcgateway.NewClient()
		if err != nil {
			return false, fmt.Errorf("create Alibaba Cloud Function Compute API gateway: %w", err)
		}
		client.Spi = spi
		return true, nil
	case strings.EqualFold(product, "Oss"):
		spi, err := ossgateway.NewClient()
		if err != nil {
			return false, fmt.Errorf("create Alibaba Cloud OSS API gateway: %w", err)
		}
		client.Spi = spi
		return true, nil
	case strings.EqualFold(product, "Sls"):
		spi, err := slsgateway.NewClient()
		if err != nil {
			return false, fmt.Errorf("create Alibaba Cloud SLS API gateway: %w", err)
		}
		client.Spi = spi
		return true, nil
	default:
		return false, nil
	}
}

func productAPIEndpoint(
	call *catalog.OperationCall,
	site asset.ConnectionSite,
	region string,
	parameters map[string]any,
) (string, error) {
	if call == nil {
		return "", fmt.Errorf("product API call metadata is required")
	}
	endpoint := strings.TrimSpace(call.SiteEndpoints[site])
	if endpoint == "" {
		endpoint = strings.TrimSpace(call.EndpointOverrides[region])
	}
	if endpoint == "" {
		endpoint = strings.ReplaceAll(call.Endpoint, "{region}", region)
	} else if len(call.EndpointParameters) > 0 {
		return "", fmt.Errorf("endpoint parameters cannot be combined with a region override")
	}
	for _, parameter := range call.EndpointParameters {
		value, exists := parameters[parameter]
		text := strings.TrimSpace(fmt.Sprint(value))
		if !exists || text == "" || text == "<nil>" {
			return "", fmt.Errorf("endpoint parameter %q is required", parameter)
		}
		if !validEndpointLabel(text) {
			return "", fmt.Errorf("endpoint parameter %q is not a valid DNS label", parameter)
		}
		endpoint = strings.ReplaceAll(endpoint, "{"+parameter+"}", text)
		delete(parameters, parameter)
	}
	if strings.Contains(endpoint, "{") || strings.TrimSpace(endpoint) == "" {
		return "", fmt.Errorf("endpoint %q is unresolved", endpoint)
	}
	endpoint = strings.TrimSpace(endpoint)
	parsed, err := url.Parse("https://" + endpoint)
	if err != nil ||
		parsed.User != nil ||
		parsed.Host != endpoint ||
		parsed.Hostname() == "" ||
		parsed.Path != "" ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return "", fmt.Errorf("endpoint %q is invalid", endpoint)
	}
	return endpoint, nil
}

func validEndpointLabel(value string) bool {
	if len(value) == 0 || len(value) > 63 ||
		!isEndpointAlphaNumeric(value[0]) ||
		!isEndpointAlphaNumeric(value[len(value)-1]) {
		return false
	}
	for index := 1; index < len(value)-1; index++ {
		if !isEndpointAlphaNumeric(value[index]) && value[index] != '-' {
			return false
		}
	}
	return true
}

func isEndpointAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' ||
		value >= 'A' && value <= 'Z' ||
		value >= '0' && value <= '9'
}

func invokeProductAPICaller(
	ctx context.Context,
	client contextualOpenAPI,
	operation catalog.Operation,
	invocation contracts.Invocation,
	options ...productAPICallOptions,
) (contracts.InvocationResult, error) {
	call := operation.Call
	if call == nil {
		return contracts.InvocationResult{}, fmt.Errorf("Alibaba Cloud operation %q has no product API call metadata", operation.Key())
	}
	parameters := clonedParameters(invocation.Parameters)
	if call.IdempotencyParameter != "" && invocation.IdempotencyKey != "" {
		if _, exists := parameters[call.IdempotencyParameter]; !exists {
			parameters[call.IdempotencyParameter] = alicloudIdempotencyToken(invocation.IdempotencyKey)
		}
	}
	path, err := resolveOperationPath(call.Path, parameters, call.RawPathParameters)
	if err != nil {
		return contracts.InvocationResult{}, fmt.Errorf("resolve Alibaba Cloud operation %q path: %w", operation.Key(), err)
	}
	params := &openapi.Params{
		Action: dara.String(operation.Name), Version: dara.String(call.Version),
		Protocol: dara.String(defaultString(call.Protocol, "HTTPS")),
		Pathname: dara.String(path), Method: dara.String(call.Method),
		AuthType: dara.String("AK"), Style: dara.String(call.Style),
		ReqBodyType: dara.String(defaultString(call.RequestBodyType, "formData")),
		BodyType:    dara.String(defaultString(call.BodyType, "json")),
	}
	request := &openapi.OpenApiRequest{}
	if len(call.HostParameters) > 0 {
		request.HostMap = make(map[string]*string, len(call.HostParameters))
		for _, name := range call.HostParameters {
			value, exists := parameters[name]
			text := strings.TrimSpace(fmt.Sprint(value))
			if !exists || text == "" || text == "<nil>" {
				return contracts.InvocationResult{}, fmt.Errorf(
					"Alibaba Cloud operation %q host parameter %q is required",
					operation.Key(),
					name,
				)
			}
			request.HostMap[name] = dara.String(text)
			delete(parameters, name)
		}
	}
	if len(call.HeaderParameters) > 0 {
		request.Headers = make(map[string]*string, len(call.HeaderParameters))
		for _, name := range call.HeaderParameters {
			value, exists := parameters[name]
			text := strings.TrimSpace(fmt.Sprint(value))
			if !exists || text == "" || text == "<nil>" {
				return contracts.InvocationResult{}, fmt.Errorf(
					"Alibaba Cloud operation %q header parameter %q is required",
					operation.Key(),
					name,
				)
			}
			request.Headers[name] = dara.String(text)
			delete(parameters, name)
		}
	}
	switch call.ParameterPosition {
	case "", "query":
		request.Query = queryParameters(parameters)
	case "body":
		request.Body = parameters
	case "header":
		request.Headers = tea.Merge(request.Headers, queryParameters(parameters))
	default:
		return contracts.InvocationResult{}, fmt.Errorf(
			"Alibaba Cloud operation %q has unsupported parameter position %q",
			operation.Key(),
			call.ParameterPosition,
		)
	}
	callOptions := productAPICallOptions{}
	if len(options) > 0 {
		callOptions = options[0]
	}
	callProductAPI := func(callCtx context.Context, options *dara.RuntimeOptions) (map[string]interface{}, error) {
		if callOptions.UseGatewayExecutor {
			executor, ok := client.(contextualOpenAPIExecutor)
			if !ok {
				return nil, fmt.Errorf(
					"Alibaba Cloud %s operation %q requires the product API executor",
					call.Product,
					operation.Key(),
				)
			}
			return executor.ExecuteWithCtx(callCtx, params, request, options)
		}
		return client.CallApiWithCtx(callCtx, params, request, options)
	}
	var response map[string]interface{}
	if operation.Destructive {
		response, err = callDestructiveProductAPI(
			ctx,
			callProductAPI,
			logCloudAPIRetry(
				ctx,
				strings.ToLower(call.Product),
				operation.Name,
				parameters,
			),
		)
	} else {
		response, err = callCloudProductQuery(
			ctx,
			callProductAPI,
			logCloudAPIRetry(
				ctx,
				strings.ToLower(call.Product),
				operation.Name,
				parameters,
			),
		)
	}
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	body := response["body"]
	headers := responseHeaders(response)
	data, err := productAPIResponseData(body, operation.Destructive)
	if err != nil {
		return contracts.InvocationResult{}, fmt.Errorf("decode Alibaba Cloud operation %q response: %w", operation.Key(), err)
	}
	return contracts.InvocationResult{
		RequestID: requestIDFrom(call.Product, body, headers),
		OperationID: firstString(
			data,
			"TaskId", "TaskID", "task_id", "taskId",
			"OperationId", "OperationID", "operation_id", "operationId",
		),
		Data: data,
	}, nil
}

func alicloudIdempotencyToken(value string) string {
	if len(value) <= 64 {
		return value
	}
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

type productAPICallOptions struct {
	UseGatewayExecutor bool
}

func productAPIResponseData(body any, destructive bool) (map[string]any, error) {
	data, err := bodyMap(body)
	if err == nil || !destructive {
		return data, err
	}
	var text string
	switch value := body.(type) {
	case string:
		text = value
	case *string:
		if value != nil {
			text = *value
		}
	default:
		return nil, err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return map[string]any{}, nil
	}
	var object map[string]any
	if json.Unmarshal([]byte(text), &object) == nil {
		return object, nil
	}
	return map[string]any{"response": text}, nil
}

func resolveOperationPath(path string, parameters map[string]any, rawParameters []string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", fmt.Errorf("path is required")
	}
	raw := make(map[string]struct{}, len(rawParameters))
	for _, name := range rawParameters {
		raw[name] = struct{}{}
	}
	for {
		start := strings.Index(path, "{")
		if start < 0 {
			return path, nil
		}
		endOffset := strings.Index(path[start+1:], "}")
		if endOffset < 0 {
			return "", fmt.Errorf("path %q has an unmatched placeholder", path)
		}
		end := start + 1 + endOffset
		name := path[start+1 : end]
		value, ok := parameters[name]
		text := strings.TrimSpace(fmt.Sprint(value))
		if !ok || text == "" || text == "<nil>" {
			return "", fmt.Errorf("path parameter %q is required", name)
		}
		encoded := url.PathEscape(text)
		if _, ok := raw[name]; ok {
			encoded = text
		}
		path = path[:start] + encoded + path[end+1:]
		delete(parameters, name)
	}
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func (sdkClientFactory) ACK(_ context.Context, credential contracts.Credential, region string) (ACKClient, error) {
	cloudCredential, err := cloudCredential(credential)
	if err != nil {
		return nil, err
	}
	return newSDKACK(cloudCredential, credential.Site, region)
}

func openAPIConfig(
	credential cloudcredentials.Credential,
	site asset.ConnectionSite,
	service string,
	region string,
	fallbackEndpoint string,
) (*openapiutils.Config, error) {
	endpoint, err := resolveEndpoint(service, site, region, fallbackEndpoint)
	if err != nil {
		return nil, err
	}
	config := &openapiutils.Config{Credential: credential, RegionId: dara.String(region)}
	if strings.TrimSpace(endpoint) != "" {
		config.Endpoint = dara.String(strings.TrimSpace(endpoint))
	}
	return config, nil
}

type resourceCenterCaller interface {
	SearchResourcesWithContext(context.Context, *resourcecenterclient.SearchResourcesRequest, *dara.RuntimeOptions) (*resourcecenterclient.SearchResourcesResponse, error)
	BatchGetResourceConfigurationsWithContext(context.Context, *resourcecenterclient.BatchGetResourceConfigurationsRequest, *dara.RuntimeOptions) (*resourcecenterclient.BatchGetResourceConfigurationsResponse, error)
}

type sdkResourceCenter struct {
	client resourceCenterCaller
}

func (c *sdkResourceCenter) SearchResources(ctx context.Context, request SearchRequest) (ResourcePage, error) {
	filters := make([]*resourcecenterclient.SearchResourcesRequestFilter, 0, 4)
	if request.RegionID != "" {
		filters = append(filters, newResourceFilter("RegionId", []string{request.RegionID}))
	}
	if len(request.ResourceTypes) > 0 {
		filters = append(filters, newResourceFilter("ResourceType", request.ResourceTypes))
	}
	if request.VpcID != "" {
		filters = append(filters, newResourceFilter("VpcId", []string{request.VpcID}))
	}
	if request.VSwitchID != "" {
		filters = append(filters, newResourceFilter("VSwitchId", []string{request.VSwitchID}))
	}
	sdkRequest := &resourcecenterclient.SearchResourcesRequest{
		Filter: filters, MaxResults: dara.Int32(int32(request.MaxResults)), IncludeDeletedResources: dara.Bool(false),
	}
	if request.NextToken != "" {
		sdkRequest.NextToken = dara.String(request.NextToken)
	}
	if request.SearchExpression != "" {
		sdkRequest.SearchExpression = dara.String(request.SearchExpression)
	}
	response, err := callResourceCenterQuery(ctx, func(callCtx context.Context, options *dara.RuntimeOptions) (*resourcecenterclient.SearchResourcesResponse, error) {
		return c.client.SearchResourcesWithContext(callCtx, sdkRequest, options)
	}, logCloudAPIRetry(
		ctx,
		"resource-center",
		"SearchResources",
		rawCloudPayload(sdkRequest),
	))
	if err != nil {
		return ResourcePage{}, err
	}
	if response == nil || response.Body == nil {
		return ResourcePage{}, fmt.Errorf("Alibaba Cloud Resource Center response body is empty")
	}
	page := ResourcePage{RequestID: dara.StringValue(response.Body.RequestId), NextToken: dara.StringValue(response.Body.NextToken)}
	if raw, rawErr := contracts.CloudRawPayload(response.Body); rawErr == nil {
		page.RawResponse = raw
	}
	page.Resources = make([]ResourceRecord, 0, len(response.Body.Resources))
	for _, resource := range response.Body.Resources {
		if resource == nil {
			continue
		}
		record := ResourceRecord{
			AccountID: dara.StringValue(resource.AccountId), ResourceGroupID: dara.StringValue(resource.ResourceGroupId),
			ResourceID: dara.StringValue(resource.ResourceId), ResourceName: dara.StringValue(resource.ResourceName),
			ResourceType: dara.StringValue(resource.ResourceType), RegionID: dara.StringValue(resource.RegionId),
			ZoneID: dara.StringValue(resource.ZoneId), CreateTime: dara.StringValue(resource.CreateTime),
			ExpireTime: dara.StringValue(resource.ExpireTime), Deleted: dara.BoolValue(resource.Deleted),
		}
		for _, tag := range resource.Tags {
			if tag != nil {
				record.Tags = append(record.Tags, ResourceTag{Key: dara.StringValue(tag.Key), Value: dara.StringValue(tag.Value)})
			}
		}
		page.Resources = append(page.Resources, record)
	}
	return page, nil
}

func (c *sdkResourceCenter) BatchGetResourceConfigurations(
	ctx context.Context,
	request ResourceConfigurationRequest,
) (ResourceConfigurationPage, error) {
	sdkRequest := &resourcecenterclient.BatchGetResourceConfigurationsRequest{
		Resources: make([]*resourcecenterclient.BatchGetResourceConfigurationsRequestResources, 0, len(request.Resources)),
	}
	for _, resource := range request.Resources {
		sdkRequest.Resources = append(sdkRequest.Resources, &resourcecenterclient.BatchGetResourceConfigurationsRequestResources{
			RegionId: dara.String(resource.RegionID), ResourceId: dara.String(resource.ResourceID),
			ResourceType: dara.String(resource.ResourceType),
		})
	}
	response, err := callResourceCenterQuery(ctx, func(callCtx context.Context, options *dara.RuntimeOptions) (*resourcecenterclient.BatchGetResourceConfigurationsResponse, error) {
		return c.client.BatchGetResourceConfigurationsWithContext(callCtx, sdkRequest, options)
	}, logCloudAPIRetry(
		ctx,
		"resource-center",
		"BatchGetResourceConfigurations",
		rawCloudPayload(sdkRequest),
	))
	if err != nil {
		return ResourceConfigurationPage{}, err
	}
	if response == nil || response.Body == nil {
		return ResourceConfigurationPage{}, fmt.Errorf("Alibaba Cloud Resource Center configuration response body is empty")
	}
	page := ResourceConfigurationPage{RequestID: dara.StringValue(response.Body.RequestId)}
	if raw, rawErr := contracts.CloudRawPayload(response.Body); rawErr == nil {
		page.RawResponse = raw
	}
	page.Resources = make([]ResourceRecord, 0, len(response.Body.Resources))
	for _, resource := range response.Body.Resources {
		if resource == nil {
			continue
		}
		record := ResourceRecord{
			AccountID: dara.StringValue(resource.AccountId), ResourceGroupID: dara.StringValue(resource.ResourceGroupId),
			ResourceID: dara.StringValue(resource.ResourceId), ResourceName: dara.StringValue(resource.ResourceName),
			ResourceType: dara.StringValue(resource.ResourceType), RegionID: dara.StringValue(resource.RegionId),
			ZoneID: dara.StringValue(resource.ZoneId), CreateTime: dara.StringValue(resource.CreateTime),
			ExpireTime: dara.StringValue(resource.ExpireTime), Configuration: resource.Configuration,
		}
		for _, address := range resource.IpAddresses {
			record.IPAddresses = append(record.IPAddresses, dara.StringValue(address))
		}
		for _, attribute := range resource.IpAddressAttributes {
			if attribute == nil {
				continue
			}
			record.IPAddressAttributes = append(record.IPAddressAttributes, IPAddressAttribute{
				IPAddress: dara.StringValue(attribute.IpAddress), NetworkType: dara.StringValue(attribute.NetworkType),
				Version: dara.StringValue(attribute.Version),
			})
		}
		for _, tag := range resource.Tags {
			if tag != nil {
				record.Tags = append(record.Tags, ResourceTag{Key: dara.StringValue(tag.Key), Value: dara.StringValue(tag.Value)})
			}
		}
		page.Resources = append(page.Resources, record)
	}
	return page, nil
}

func newResourceFilter(key string, values []string) *resourcecenterclient.SearchResourcesRequestFilter {
	result := make([]*string, 0, len(values))
	for _, value := range values {
		result = append(result, dara.String(value))
	}
	return &resourcecenterclient.SearchResourcesRequestFilter{
		Key: dara.String(key), MatchType: dara.String("Equals"), Value: result,
	}
}

type contextualOpenAPI interface {
	CallApiWithCtx(context.Context, *openapi.Params, *openapi.OpenApiRequest, *dara.RuntimeOptions) (map[string]interface{}, error)
}

type contextualOpenAPIExecutor interface {
	ExecuteWithCtx(context.Context, *openapi.Params, *openapi.OpenApiRequest, *dara.RuntimeOptions) (map[string]interface{}, error)
}

type sdkACK struct {
	caller contextualOpenAPI
}

func newSDKACK(credential cloudcredentials.Credential, site asset.ConnectionSite, region string) (*sdkACK, error) {
	endpointClient, err := csclient.NewClient(&roaclient.Config{Credential: credential, RegionId: tea.String(region)})
	if err != nil {
		return nil, fmt.Errorf("resolve Alibaba Cloud Container Service endpoint: %w", err)
	}
	config, err := openAPIConfig(credential, site, serviceACK, region, tea.StringValue(endpointClient.EndpointHost))
	if err != nil {
		return nil, err
	}
	client, err := openapi.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("create Alibaba Cloud Container Service client: %w", err)
	}
	return &sdkACK{caller: client}, nil
}

func (c *sdkACK) DescribeClusterResources(ctx context.Context, clusterID string, includeAddons bool) ([]ClusterResource, string, error) {
	query := map[string]*string{"with_addon_resources": tea.String(strconv.FormatBool(includeAddons))}
	body, headers, err := callROA(ctx, c.caller, "DescribeClusterResources", containerServiceAPIDate, "GET", "/clusters/"+url.PathEscape(clusterID)+"/resources", query, nil)
	if err != nil {
		return nil, "", err
	}
	if object, ok := body.(map[string]any); ok && len(object) == 0 {
		return []ClusterResource{}, requestIDFrom(containerServiceProductCode, body, headers), nil
	}
	var resources []ClusterResource
	if err := convertBody(body, &resources); err != nil {
		return nil, "", fmt.Errorf("decode DescribeClusterResources response: %w", err)
	}
	return resources, requestIDFrom(containerServiceProductCode, body, headers), nil
}

func (c *sdkACK) DescribeClusterNodes(ctx context.Context, clusterID string) ([]ClusterNode, string, error) {
	const pageSize = 100
	var nodes []ClusterNode
	var requestID string
	for pageNumber := 1; ; pageNumber++ {
		query := map[string]*string{"pageNumber": tea.String(strconv.Itoa(pageNumber)), "pageSize": tea.String(strconv.Itoa(pageSize))}
		body, headers, err := callROA(ctx, c.caller, "DescribeClusterNodes", containerServiceAPIDate, "GET", "/clusters/"+url.PathEscape(clusterID)+"/nodes", query, nil)
		if err != nil {
			return nil, requestID, err
		}
		if requestID == "" {
			requestID = requestIDFrom(containerServiceProductCode, body, headers)
		}
		object, err := bodyMap(body)
		if err != nil {
			return nil, requestID, fmt.Errorf("decode DescribeClusterNodes response: %w", err)
		}
		var pageNodes []ClusterNode
		if err := convertBody(object["nodes"], &pageNodes); err != nil {
			return nil, requestID, fmt.Errorf("decode DescribeClusterNodes nodes: %w", err)
		}
		nodes = append(nodes, pageNodes...)
		total := nestedInt(object, "page", "total_count")
		if len(pageNodes) < pageSize || (total > 0 && len(nodes) >= total) {
			break
		}
	}
	return nodes, requestID, nil
}

func (c *sdkACK) DeleteCluster(ctx context.Context, request DeleteClusterRequest) (DeleteClusterResponse, error) {
	query := map[string]*string{"retain_all_resources": tea.String(strconv.FormatBool(request.RetainAllResources))}
	if len(request.RetainResources) > 0 {
		encoded, err := json.Marshal(request.RetainResources)
		if err != nil {
			return DeleteClusterResponse{}, fmt.Errorf("encode retained ACK resources: %w", err)
		}
		query["retain_resources"] = tea.String(string(encoded))
	}
	if len(request.DeleteOptions) > 0 {
		encoded, err := json.Marshal(request.DeleteOptions)
		if err != nil {
			return DeleteClusterResponse{}, fmt.Errorf("encode ACK delete options: %w", err)
		}
		query["delete_options"] = tea.String(string(encoded))
	}
	body, headers, err := callROA(ctx, c.caller, "DeleteCluster", containerServiceAPIDate, "DELETE", "/clusters/"+url.PathEscape(request.ClusterID), query, nil)
	if err != nil {
		return DeleteClusterResponse{}, err
	}
	data, err := bodyMap(body)
	if err != nil {
		return DeleteClusterResponse{}, fmt.Errorf("decode DeleteCluster response: %w", err)
	}
	return DeleteClusterResponse{
		ClusterID: firstString(data, "cluster_id", "clusterId"),
		RequestID: requestIDFrom(containerServiceProductCode, body, headers),
		TaskID:    firstString(data, "task_id", "taskId"),
	}, nil
}

func (c *sdkACK) DescribeClusterDetail(
	ctx context.Context,
	clusterID string,
) (ACKClusterDetail, string, error) {
	body, headers, err := callROA(
		ctx,
		c.caller,
		"DescribeClusterDetail",
		containerServiceAPIDate,
		"GET",
		"/clusters/"+url.PathEscape(clusterID),
		nil,
		nil,
	)
	if err != nil {
		return ACKClusterDetail{}, "", err
	}
	data, err := bodyMap(body)
	if err != nil {
		return ACKClusterDetail{}, "", fmt.Errorf(
			"decode DescribeClusterDetail response: %w",
			err,
		)
	}
	return ACKClusterDetail{
		ClusterID:          firstString(data, "cluster_id", "clusterId"),
		State:              firstString(data, "state", "State"),
		DeletionProtection: boolParameter(data, "deletion_protection", false),
	}, requestIDFrom(containerServiceProductCode, body, headers), nil
}

func (c *sdkACK) ModifyClusterDeletionProtection(
	ctx context.Context,
	clusterID string,
	enabled bool,
) (string, error) {
	body, headers, err := callROA(
		ctx,
		c.caller,
		"ModifyCluster",
		containerServiceAPIDate,
		"PUT",
		"/api/v2/clusters/"+url.PathEscape(clusterID),
		nil,
		map[string]any{"deletion_protection": enabled},
	)
	if err != nil {
		return "", err
	}
	return requestIDFrom(containerServiceProductCode, body, headers), nil
}

func (c *sdkACK) Invoke(ctx context.Context, invocation contracts.Invocation) (contracts.InvocationResult, error) {
	clusterID, err := requiredParameter(invocation.Parameters, "ClusterId", "cluster_id", "id")
	if invocation.Operation == "AlibabaCloud.DescribeClusters" {
		body, headers, callErr := callROA(ctx, c.caller, "DescribeClusters", containerServiceAPIDate, "GET", "/clusters", queryParameters(invocation.Parameters), nil)
		if callErr != nil {
			return contracts.InvocationResult{}, callErr
		}
		data, mapErr := bodyMap(body)
		if mapErr != nil {
			return contracts.InvocationResult{}, mapErr
		}
		return contracts.InvocationResult{RequestID: requestIDFrom(containerServiceProductCode, body, headers), Data: data}, nil
	}
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	switch invocation.Operation {
	case "AlibabaCloud.DescribeClusterDetail":
		body, headers, err := callROA(ctx, c.caller, "DescribeClusterDetail", containerServiceAPIDate, "GET", "/clusters/"+url.PathEscape(clusterID), nil, nil)
		if err != nil {
			return contracts.InvocationResult{}, err
		}
		data, err := bodyMap(body)
		if err != nil {
			return contracts.InvocationResult{}, err
		}
		return contracts.InvocationResult{RequestID: requestIDFrom(containerServiceProductCode, body, headers), Data: data}, nil
	case "AlibabaCloud.DescribeClusterResources":
		resources, requestID, err := c.DescribeClusterResources(ctx, clusterID, boolParameter(invocation.Parameters, "with_addon_resources", true))
		if err != nil {
			return contracts.InvocationResult{}, err
		}
		data, err := modelMap(resources)
		if err != nil {
			return contracts.InvocationResult{}, err
		}
		return contracts.InvocationResult{RequestID: requestID, Data: map[string]any{"resources": data["items"]}}, nil
	case "AlibabaCloud.DescribeClusterNodes":
		nodes, requestID, err := c.DescribeClusterNodes(ctx, clusterID)
		if err != nil {
			return contracts.InvocationResult{}, err
		}
		payload, err := json.Marshal(nodes)
		if err != nil {
			return contracts.InvocationResult{}, err
		}
		var items []any
		if err := json.Unmarshal(payload, &items); err != nil {
			return contracts.InvocationResult{}, err
		}
		return contracts.InvocationResult{RequestID: requestID, Data: map[string]any{"nodes": items}}, nil
	case "AlibabaCloud.DeleteCluster":
		request, err := mapDeleteRequest(clusterID, invocation.Parameters)
		if err != nil {
			return contracts.InvocationResult{}, err
		}
		response, err := c.DeleteCluster(ctx, request)
		if err != nil {
			return contracts.InvocationResult{}, err
		}
		return contracts.InvocationResult{RequestID: response.RequestID, OperationID: response.TaskID, Data: map[string]any{"cluster_id": response.ClusterID}}, nil
	default:
		return contracts.InvocationResult{}, fmt.Errorf("unsupported ACK operation %q", invocation.Operation)
	}
}

func callROA(ctx context.Context, caller contextualOpenAPI, action, apiDate, method, path string, query map[string]*string, body any) (any, map[string]*string, error) {
	params := &openapi.Params{
		Action: dara.String(action), Version: dara.String(apiDate), Protocol: dara.String("HTTPS"),
		Pathname: dara.String(path), Method: dara.String(method), AuthType: dara.String("AK"),
		Style: dara.String("ROA"), ReqBodyType: dara.String("json"), BodyType: dara.String("json"),
	}
	request := &openapi.OpenApiRequest{Query: query, Body: body}
	callAPI := func(callCtx context.Context, options *dara.RuntimeOptions) (map[string]interface{}, error) {
		return caller.CallApiWithCtx(callCtx, params, request, options)
	}
	var response map[string]interface{}
	var err error
	if strings.EqualFold(method, "GET") {
		response, err = callCloudProductQuery(ctx, callAPI)
	} else {
		response, err = callAPI(ctx, &dara.RuntimeOptions{})
	}
	if err != nil {
		return nil, nil, err
	}
	headers := responseHeaders(response)
	return response["body"], headers, nil
}

func responseHeaders(response map[string]any) map[string]*string {
	for _, key := range []string{"headers", "_headers"} {
		if headers, ok := response[key].(map[string]*string); ok {
			return headers
		}
		if raw, ok := response[key].(map[string]any); ok {
			headers := make(map[string]*string, len(raw))
			for name, value := range raw {
				if text, ok := value.(string); ok {
					headers[name] = tea.String(text)
				}
			}
			return headers
		}
	}
	return nil
}

func modelMap(value any) (map[string]any, error) {
	payload, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode Alibaba Cloud SDK model: %w", err)
	}
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		var items []any
		if listErr := json.Unmarshal(payload, &items); listErr == nil {
			return map[string]any{"items": items}, nil
		}
		return nil, fmt.Errorf("decode Alibaba Cloud SDK model: %w", err)
	}
	return object, nil
}

func bodyMap(body any) (map[string]any, error) {
	if body == nil {
		return map[string]any{}, nil
	}
	if object, ok := body.(map[string]any); ok {
		return object, nil
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		var items []any
		if listErr := json.Unmarshal(payload, &items); listErr == nil {
			return map[string]any{"items": items}, nil
		}
		return nil, fmt.Errorf("response body has type %T", body)
	}
	return object, nil
}

func convertBody(source, target any) error {
	payload, err := json.Marshal(source)
	if err != nil {
		return err
	}
	return json.Unmarshal(payload, target)
}

func clonedParameters(parameters map[string]any) map[string]any {
	result := make(map[string]any, len(parameters)+2)
	for key, value := range parameters {
		result[key] = value
	}
	return result
}

func requiredParameter(parameters map[string]any, names ...string) (string, error) {
	for _, name := range names {
		if value, ok := parameters[name].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
	}
	return "", fmt.Errorf("Alibaba Cloud operation requires one of parameters %s", strings.Join(names, ", "))
}

func boolParameter(parameters map[string]any, name string, fallback bool) bool {
	if value, ok := parameters[name].(bool); ok {
		return value
	}
	return fallback
}

func queryParameters(parameters map[string]any) map[string]*string {
	query := make(map[string]*string, len(parameters))
	for key, value := range parameters {
		switch typed := value.(type) {
		case string:
			query[key] = tea.String(typed)
		case []string:
			for index, item := range typed {
				query[fmt.Sprintf("%s.%d", key, index+1)] = tea.String(item)
			}
		default:
			payload, err := json.Marshal(value)
			if err == nil {
				query[key] = tea.String(string(payload))
			}
		}
	}
	return query
}

func firstString(object map[string]any, names ...string) string {
	for _, name := range names {
		if value, ok := object[name].(string); ok && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func requestIDFrom(productCode string, body any, headers map[string]*string) string {
	object, _ := bodyMap(body)
	if value := firstString(object, "request_id", "requestId", "RequestId", "RequestID"); value != "" {
		return value
	}
	var productHeader string
	switch strings.ToLower(strings.TrimSpace(productCode)) {
	case "oss":
		productHeader = "x-oss-request-id"
	case "tablestore":
		productHeader = "x-ots-requestid"
	case "fc-open":
		productHeader = "x-fc-request-id"
	case "sls":
		productHeader = "x-log-requestid"
	}
	if productHeader != "" {
		if requestID := headerValue(headers, productHeader); requestID != "" {
			return requestID
		}
	}
	return headerValue(headers, "x-acs-request-id")
}

func headerValue(headers map[string]*string, name string) string {
	for key, value := range headers {
		if strings.EqualFold(key, name) {
			return strings.TrimSpace(tea.StringValue(value))
		}
	}
	return ""
}

func nestedInt(object map[string]any, objectKey, valueKey string) int {
	nested, _ := object[objectKey].(map[string]any)
	switch value := nested[valueKey].(type) {
	case float64:
		return int(value)
	case json.Number:
		integer, _ := value.Int64()
		return int(integer)
	case int:
		return value
	default:
		return 0
	}
}
