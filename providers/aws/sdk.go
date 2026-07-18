package aws

import (
	"context"
	"errors"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	awscfn "github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	awsexplorer "github.com/aws/aws-sdk-go-v2/service/resourceexplorer2"
	explorertypes "github.com/aws/aws-sdk-go-v2/service/resourceexplorer2/types"
	awssts "github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type sdkClientFactory struct{}

func (sdkClientFactory) CallerIdentity(ctx context.Context, credential contracts.Credential, region string) (string, string, error) {
	config, err := loadSDKConfig(ctx, credential, region)
	if err != nil {
		return "", "", err
	}
	output, err := awssts.NewFromConfig(config).GetCallerIdentity(ctx, &awssts.GetCallerIdentityInput{})
	if err != nil {
		return "", "", err
	}
	return awssdk.ToString(output.Account), awssdk.ToString(output.Arn), nil
}

func (sdkClientFactory) DiscoverRegions(ctx context.Context, credential contracts.Credential, region string) ([]providerRegion, error) {
	config, err := loadSDKConfig(ctx, credential, region)
	if err != nil {
		return nil, err
	}
	output, err := awsec2.NewFromConfig(config).DescribeRegions(ctx, &awsec2.DescribeRegionsInput{AllRegions: awssdk.Bool(false)})
	if err != nil {
		return nil, err
	}
	regions := make([]providerRegion, 0, len(output.Regions))
	for _, region := range output.Regions {
		regionID := awssdk.ToString(region.RegionName)
		regions = append(regions, providerRegion{
			RegionID: regionID, Name: regionID, Endpoint: awssdk.ToString(region.Endpoint), OptInStatus: awssdk.ToString(region.OptInStatus),
		})
	}
	return regions, nil
}

func (sdkClientFactory) ResourceExplorer(ctx context.Context, credential contracts.Credential, region string) (ResourceExplorerClient, error) {
	region = strings.TrimSpace(region)
	if region == "" {
		return nil, errors.New("AWS Resource Explorer request scope requires a region")
	}
	config, err := loadSDKConfig(ctx, credential, region)
	if err != nil {
		return nil, err
	}
	return &resourceExplorerSDK{client: awsexplorer.NewFromConfig(config)}, nil
}

func (sdkClientFactory) CloudFormation(ctx context.Context, credential contracts.Credential, region string) (CloudFormationClient, error) {
	config, err := loadSDKConfig(ctx, credential, region)
	if err != nil {
		return nil, err
	}
	return &cloudFormationSDK{client: awscfn.NewFromConfig(config)}, nil
}

func (sdkClientFactory) Network(ctx context.Context, credential contracts.Credential, region string) (NetworkClient, error) {
	if strings.TrimSpace(region) == "" {
		return nil, errors.New("AWS network request requires a region")
	}
	config, err := loadSDKConfig(ctx, credential, region)
	if err != nil {
		return nil, err
	}
	return &networkSDK{client: awsec2.NewFromConfig(config)}, nil
}

func loadSDKConfig(ctx context.Context, credential contracts.Credential, region string) (awssdk.Config, error) {
	options := []func(*awsconfig.LoadOptions) error{}
	if region != "" {
		options = append(options, awsconfig.WithRegion(region))
	}
	if profile := strings.TrimSpace(credential.Values["profile"]); profile != "" {
		options = append(options, awsconfig.WithSharedConfigProfile(profile))
	}
	accessKey := strings.TrimSpace(credential.Values["access_key_id"])
	secretKey := strings.TrimSpace(credential.Values["secret_access_key"])
	if secretKey == "" {
		secretKey = strings.TrimSpace(credential.Values["access_key_secret"])
	}
	if accessKey != "" || secretKey != "" {
		if accessKey == "" || secretKey == "" {
			return awssdk.Config{}, errors.New("AWS static credentials require access key ID and secret access key")
		}
		provider := awscredentials.NewStaticCredentialsProvider(accessKey, secretKey, strings.TrimSpace(credential.Values["session_token"]))
		options = append(options, awsconfig.WithCredentialsProvider(provider))
	}
	return awsconfig.LoadDefaultConfig(ctx, options...)
}

type resourceExplorerSDK struct{ client *awsexplorer.Client }

type networkSDK struct{ client *awsec2.Client }

func (c *networkSDK) ListVPCs(ctx context.Context, request NetworkListRequest) (NetworkPage, error) {
	input := &awsec2.DescribeVpcsInput{}
	if request.Cursor != "" {
		input.NextToken = awssdk.String(request.Cursor)
	}
	if request.Limit > 0 {
		limit := request.Limit
		if limit < 5 {
			limit = 5
		}
		input.MaxResults = awssdk.Int32(int32(limit))
	}
	output, err := c.client.DescribeVpcs(ctx, input)
	if err != nil {
		return NetworkPage{}, err
	}
	requestID, _ := awsmiddleware.GetRequestIDMetadata(output.ResultMetadata)
	page := NetworkPage{RequestID: requestID, NextToken: awssdk.ToString(output.NextToken), Items: make([]NetworkItem, 0, len(output.Vpcs))}
	for _, item := range output.Vpcs {
		page.Items = append(page.Items, NetworkItem{NativeID: awssdk.ToString(item.VpcId), Name: awsNameTag(item.Tags)})
	}
	return page, nil
}

func (c *networkSDK) ListVSwitches(ctx context.Context, request NetworkListRequest) (NetworkPage, error) {
	input := &awsec2.DescribeSubnetsInput{}
	if request.ParentNativeID != "" {
		input.Filters = []ec2types.Filter{{Name: awssdk.String("vpc-id"), Values: []string{request.ParentNativeID}}}
	}
	if request.Cursor != "" {
		input.NextToken = awssdk.String(request.Cursor)
	}
	if request.Limit > 0 {
		limit := request.Limit
		if limit < 5 {
			limit = 5
		}
		input.MaxResults = awssdk.Int32(int32(limit))
	}
	output, err := c.client.DescribeSubnets(ctx, input)
	if err != nil {
		return NetworkPage{}, err
	}
	requestID, _ := awsmiddleware.GetRequestIDMetadata(output.ResultMetadata)
	page := NetworkPage{RequestID: requestID, NextToken: awssdk.ToString(output.NextToken), Items: make([]NetworkItem, 0, len(output.Subnets))}
	for _, item := range output.Subnets {
		page.Items = append(page.Items, NetworkItem{NativeID: awssdk.ToString(item.SubnetId), Name: awsNameTag(item.Tags), ParentNativeID: awssdk.ToString(item.VpcId)})
	}
	return page, nil
}

func awsNameTag(tags []ec2types.Tag) string {
	for _, tag := range tags {
		if awssdk.ToString(tag.Key) == "Name" {
			return awssdk.ToString(tag.Value)
		}
	}
	return ""
}

func (c *resourceExplorerSDK) Search(ctx context.Context, request SearchRequest) (SearchPage, error) {
	input := &awsexplorer.ListResourcesInput{MaxResults: awssdk.Int32(int32(request.MaxResults))}
	if request.Query != "" {
		input.Filters = &explorertypes.SearchFilter{FilterString: awssdk.String(request.Query)}
	}
	if request.NextToken != "" {
		input.NextToken = awssdk.String(request.NextToken)
	}
	output, err := c.client.ListResources(ctx, input)
	if err != nil {
		return SearchPage{}, err
	}
	requestID, _ := awsmiddleware.GetRequestIDMetadata(output.ResultMetadata)
	page := SearchPage{RequestID: requestID, NextToken: awssdk.ToString(output.NextToken), Resources: make([]SearchResource, 0, len(output.Resources))}
	if raw, rawErr := contracts.CloudRawPayload(output); rawErr == nil {
		page.RawResponse = raw
		if requestID != "" {
			page.RawResponse["RequestId"] = requestID
		}
	}
	for _, resource := range output.Resources {
		properties := make(map[string]any, len(resource.Properties))
		for _, property := range resource.Properties {
			properties[awssdk.ToString(property.Name)] = property.Data
		}
		lastReported := ""
		if resource.LastReportedAt != nil {
			lastReported = resource.LastReportedAt.UTC().Format(timeFormat)
		}
		page.Resources = append(page.Resources, SearchResource{
			ARN: awssdk.ToString(resource.Arn), NativeType: awssdk.ToString(resource.CfnResourceType),
			ResourceType: awssdk.ToString(resource.ResourceType), Service: awssdk.ToString(resource.Service),
			Region: awssdk.ToString(resource.Region), AccountID: awssdk.ToString(resource.OwningAccountId),
			LastReported: lastReported, Properties: properties,
		})
	}
	return page, nil
}

const timeFormat = "2006-01-02T15:04:05.999999999Z07:00"

type cloudFormationSDK struct{ client *awscfn.Client }

func (c *cloudFormationSDK) ListStackResources(ctx context.Context, request ListStackResourcesRequest) (StackResourcePage, error) {
	input := &awscfn.ListStackResourcesInput{StackName: awssdk.String(request.StackID)}
	if request.NextToken != "" {
		input.NextToken = awssdk.String(request.NextToken)
	}
	output, err := c.client.ListStackResources(ctx, input)
	if err != nil {
		return StackResourcePage{}, err
	}
	requestID, _ := awsmiddleware.GetRequestIDMetadata(output.ResultMetadata)
	page := StackResourcePage{RequestID: requestID, NextToken: awssdk.ToString(output.NextToken), Resources: make([]StackResource, 0, len(output.StackResourceSummaries))}
	for _, resource := range output.StackResourceSummaries {
		page.Resources = append(page.Resources, StackResource{
			LogicalID: awssdk.ToString(resource.LogicalResourceId), PhysicalID: awssdk.ToString(resource.PhysicalResourceId),
			NativeType: awssdk.ToString(resource.ResourceType), Status: string(resource.ResourceStatus),
		})
	}
	return page, nil
}

func (c *cloudFormationSDK) DescribeStack(ctx context.Context, stackID string) (StackDescription, string, error) {
	output, err := c.client.DescribeStacks(ctx, &awscfn.DescribeStacksInput{StackName: awssdk.String(stackID)})
	if err != nil {
		var apiError *APIError
		normalized := NormalizeError(err)
		if errors.As(normalized, &apiError) {
			return StackDescription{}, "", normalized
		}
		if strings.Contains(strings.ToLower(err.Error()), "does not exist") {
			return StackDescription{Exists: false}, requestIDFromNormalized(normalized), nil
		}
		return StackDescription{}, "", err
	}
	requestID, _ := awsmiddleware.GetRequestIDMetadata(output.ResultMetadata)
	if len(output.Stacks) == 0 {
		return StackDescription{Exists: false}, requestID, nil
	}
	stack := output.Stacks[0]
	tags := make(map[string]string, len(stack.Tags))
	for _, tag := range stack.Tags {
		tags[awssdk.ToString(tag.Key)] = awssdk.ToString(tag.Value)
	}
	return StackDescription{
		Exists: true, ID: awssdk.ToString(stack.StackId), Name: awssdk.ToString(stack.StackName), Status: string(stack.StackStatus),
		TerminationProtected: awssdk.ToBool(stack.EnableTerminationProtection), Tags: tags,
	}, requestID, nil
}

func (c *cloudFormationSDK) DeleteStack(ctx context.Context, request DeleteStackRequest) (string, error) {
	input := &awscfn.DeleteStackInput{
		StackName: awssdk.String(request.StackID), ClientRequestToken: awssdk.String(request.ClientRequestToken),
		RetainResources: append([]string(nil), request.RetainResources...),
	}
	if request.Force {
		input.DeletionMode = cfntypes.DeletionModeForceDeleteStack
	}
	output, err := c.client.DeleteStack(ctx, input)
	if err != nil {
		return "", err
	}
	requestID, _ := awsmiddleware.GetRequestIDMetadata(output.ResultMetadata)
	return requestID, nil
}

func requestIDFromNormalized(err error) string {
	var providerError *contracts.ProviderCallError
	if errors.As(err, &providerError) {
		return providerError.Provider.RequestID
	}
	return ""
}
