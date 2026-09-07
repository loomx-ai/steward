package aws

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	awssdk "github.com/aws/aws-sdk-go-v2/aws"
	awsmiddleware "github.com/aws/aws-sdk-go-v2/aws/middleware"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	awscredentials "github.com/aws/aws-sdk-go-v2/credentials"
	awscloudcontrol "github.com/aws/aws-sdk-go-v2/service/cloudcontrol"
	cloudcontroltypes "github.com/aws/aws-sdk-go-v2/service/cloudcontrol/types"
	awscfn "github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	awsec2 "github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	awsexplorer "github.com/aws/aws-sdk-go-v2/service/resourceexplorer2"
	explorertypes "github.com/aws/aws-sdk-go-v2/service/resourceexplorer2/types"
	awssts "github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"gopkg.in/yaml.v3"
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

func (sdkClientFactory) CloudControl(ctx context.Context, credential contracts.Credential, region string) (CloudControlClient, error) {
	config, err := loadSDKConfig(ctx, credential, region)
	if err != nil {
		return nil, err
	}
	return &cloudControlSDK{client: awscloudcontrol.NewFromConfig(config)}, nil
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
	accessKey := strings.TrimSpace(credential.Values["access_key_id"])
	secretKey := strings.TrimSpace(credential.Values["secret_access_key"])
	if secretKey == "" {
		secretKey = strings.TrimSpace(credential.Values["access_key_secret"])
	}
	if accessKey == "" || secretKey == "" {
		return awssdk.Config{}, errors.New("AWS static credentials require access key ID and secret access key")
	}
	sessionToken := strings.TrimSpace(credential.Values["session_token"])
	if credential.Type == asset.CredentialAWSSession && sessionToken == "" {
		return awssdk.Config{}, errors.New("AWS session credentials require a session token")
	}
	provider := awscredentials.NewStaticCredentialsProvider(accessKey, secretKey, sessionToken)
	options = append(options, awsconfig.WithCredentialsProvider(provider))
	return awsconfig.LoadDefaultConfig(ctx, options...)
}

type resourceExplorerSDK struct{ client *awsexplorer.Client }

type networkSDK struct{ client *awsec2.Client }

func (c *networkSDK) InternetGatewayVPCs(ctx context.Context, id string) ([]string, error) {
	output, err := c.client.DescribeInternetGateways(ctx, &awsec2.DescribeInternetGatewaysInput{InternetGatewayIds: []string{id}})
	if err != nil {
		return nil, err
	}
	var result []string
	for _, gateway := range output.InternetGateways {
		for _, attachment := range gateway.Attachments {
			if id := awssdk.ToString(attachment.VpcId); id != "" {
				result = append(result, id)
			}
		}
	}
	return result, nil
}

func (c *networkSDK) DetachInternetGateway(ctx context.Context, id, vpcID string) error {
	_, err := c.client.DetachInternetGateway(ctx, &awsec2.DetachInternetGatewayInput{
		InternetGatewayId: awssdk.String(id), VpcId: awssdk.String(vpcID),
	})
	if err != nil {
		var providerError *contracts.ProviderCallError
		if errors.As(NormalizeError(err), &providerError) && providerError.Provider.Code == "Gateway.NotAttached" {
			return nil
		}
	}
	return err
}

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
	if raw, err := contracts.CloudRawPayload(output); err == nil {
		page.RawResponse = raw
		page.RawResponse["RequestId"] = requestID
	}
	rawResources, _ := page.RawResponse["Resources"].([]any)
	for resourceIndex, resource := range output.Resources {
		properties := make(map[string]any, len(resource.Properties))
		for propertyIndex, property := range resource.Properties {
			var data any
			if property.Data != nil {
				payload, err := property.Data.MarshalSmithyDocument()
				if err != nil {
					return SearchPage{}, fmt.Errorf("read AWS Resource Explorer property %q: %w", awssdk.ToString(property.Name), err)
				}
				decoder := json.NewDecoder(bytes.NewReader(payload))
				decoder.UseNumber()
				if err := decoder.Decode(&data); err != nil {
					return SearchPage{}, fmt.Errorf("decode AWS Resource Explorer property %q: %w", awssdk.ToString(property.Name), err)
				}
			}
			properties[awssdk.ToString(property.Name)] = data
			// encoding/json cannot decode Smithy documents; preserve the original
			// AWS response shape in API logs with the decoded document inserted.
			if resourceIndex < len(rawResources) {
				rawResource, _ := rawResources[resourceIndex].(map[string]any)
				rawProperties, _ := rawResource["Properties"].([]any)
				if propertyIndex < len(rawProperties) {
					if rawProperty, ok := rawProperties[propertyIndex].(map[string]any); ok {
						rawProperty["Data"] = data
					}
				}
			}
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

type cloudControlSDK struct{ client *awscloudcontrol.Client }

func (c *cloudControlSDK) ListResources(ctx context.Context, request CloudControlListRequest) (CloudControlPage, error) {
	input := &awscloudcontrol.ListResourcesInput{TypeName: awssdk.String(request.TypeName)}
	if request.NextToken != "" {
		input.NextToken = awssdk.String(request.NextToken)
	}
	if request.Limit > 0 {
		input.MaxResults = awssdk.Int32(int32(request.Limit))
	}
	output, err := c.client.ListResources(ctx, input)
	if err != nil {
		return CloudControlPage{}, err
	}
	requestID, _ := awsmiddleware.GetRequestIDMetadata(output.ResultMetadata)
	page := CloudControlPage{
		RequestID: requestID, NextToken: awssdk.ToString(output.NextToken),
		Resources: make([]CloudControlResource, 0, len(output.ResourceDescriptions)),
	}
	for _, resource := range output.ResourceDescriptions {
		page.Resources = append(page.Resources, CloudControlResource{
			Identifier: awssdk.ToString(resource.Identifier), Properties: awssdk.ToString(resource.Properties),
		})
	}
	return page, nil
}

func (c *cloudControlSDK) GetResource(ctx context.Context, typeName, identifier string) (CloudControlResource, string, error) {
	output, err := c.client.GetResource(ctx, &awscloudcontrol.GetResourceInput{
		TypeName: awssdk.String(typeName), Identifier: awssdk.String(identifier),
	})
	if err != nil {
		return CloudControlResource{}, requestIDFromNormalized(NormalizeError(err)), err
	}
	requestID, _ := awsmiddleware.GetRequestIDMetadata(output.ResultMetadata)
	if output.ResourceDescription == nil {
		return CloudControlResource{}, requestID, errors.New("AWS Cloud Control GetResource returned no resource description")
	}
	return CloudControlResource{
		Identifier: awssdk.ToString(output.ResourceDescription.Identifier),
		Properties: awssdk.ToString(output.ResourceDescription.Properties),
	}, requestID, nil
}

func (c *cloudControlSDK) DeleteResource(ctx context.Context, typeName, identifier, clientToken string) (CloudControlProgress, string, error) {
	output, err := c.client.DeleteResource(ctx, &awscloudcontrol.DeleteResourceInput{
		TypeName: awssdk.String(typeName), Identifier: awssdk.String(identifier), ClientToken: awssdk.String(clientToken),
	})
	if err != nil {
		return CloudControlProgress{}, requestIDFromNormalized(NormalizeError(err)), err
	}
	requestID, _ := awsmiddleware.GetRequestIDMetadata(output.ResultMetadata)
	return cloudControlProgress(output.ProgressEvent), requestID, nil
}

func (c *cloudControlSDK) GetResourceRequestStatus(ctx context.Context, token string) (CloudControlProgress, string, error) {
	output, err := c.client.GetResourceRequestStatus(ctx, &awscloudcontrol.GetResourceRequestStatusInput{RequestToken: awssdk.String(token)})
	if err != nil {
		return CloudControlProgress{}, requestIDFromNormalized(NormalizeError(err)), err
	}
	requestID, _ := awsmiddleware.GetRequestIDMetadata(output.ResultMetadata)
	return cloudControlProgress(output.ProgressEvent), requestID, nil
}

func cloudControlProgress(value *cloudcontroltypes.ProgressEvent) CloudControlProgress {
	if value == nil {
		return CloudControlProgress{}
	}
	result := CloudControlProgress{
		RequestToken: awssdk.ToString(value.RequestToken), Identifier: awssdk.ToString(value.Identifier),
		Status: string(value.OperationStatus), ErrorCode: string(value.ErrorCode), Message: awssdk.ToString(value.StatusMessage),
	}
	if value.RetryAfter != nil {
		result.RetryAfter = *value.RetryAfter
	}
	return result
}

func (c *cloudFormationSDK) ListStackResources(ctx context.Context, request ListStackResourcesRequest) (StackResourcePage, error) {
	input := &awscfn.ListStackResourcesInput{StackName: awssdk.String(request.StackID)}
	if request.NextToken != "" {
		input.NextToken = awssdk.String(request.NextToken)
	}
	output, err := c.client.ListStackResources(ctx, input)
	if err != nil {
		if cloudFormationStackNotFound(err) {
			return StackResourcePage{}, nil
		}
		return StackResourcePage{}, err
	}
	// Membership alone does not promise deletion: the template can retain a
	// resource. Read the processed template so transforms are accounted for.
	template, err := c.client.GetTemplate(ctx, &awscfn.GetTemplateInput{
		StackName: input.StackName, TemplateStage: cfntypes.TemplateStageProcessed,
	})
	if err != nil {
		if cloudFormationStackNotFound(err) {
			return StackResourcePage{}, nil
		}
		return StackResourcePage{}, err
	}
	var model struct {
		Resources map[string]struct {
			DeletionPolicy string `yaml:"DeletionPolicy"`
		} `yaml:"Resources"`
	}
	if err := yaml.Unmarshal([]byte(awssdk.ToString(template.TemplateBody)), &model); err != nil {
		return StackResourcePage{}, fmt.Errorf("decode CloudFormation template: %w", err)
	}
	requestID, _ := awsmiddleware.GetRequestIDMetadata(output.ResultMetadata)
	page := StackResourcePage{RequestID: requestID, NextToken: awssdk.ToString(output.NextToken), Resources: make([]StackResource, 0, len(output.StackResourceSummaries))}
	for _, resource := range output.StackResourceSummaries {
		if resource.ResourceStatus == cfntypes.ResourceStatusDeleteComplete || resource.ResourceStatus == cfntypes.ResourceStatusDeleteSkipped {
			continue
		}
		definition, found := model.Resources[awssdk.ToString(resource.LogicalResourceId)]
		policy := definition.DeletionPolicy
		if !found {
			policy = "Unknown"
		}
		page.Resources = append(page.Resources, StackResource{
			LogicalID: awssdk.ToString(resource.LogicalResourceId), PhysicalID: awssdk.ToString(resource.PhysicalResourceId),
			NativeType: awssdk.ToString(resource.ResourceType), Status: string(resource.ResourceStatus),
			DeletionPolicy: policy,
		})
	}
	return page, nil
}

func (c *cloudFormationSDK) DescribeStack(ctx context.Context, stackID string) (StackDescription, string, error) {
	output, err := c.client.DescribeStacks(ctx, &awscfn.DescribeStacksInput{StackName: awssdk.String(stackID)})
	if err != nil {
		normalized := NormalizeError(err)
		if cloudFormationStackNotFound(normalized) {
			return StackDescription{Exists: false}, requestIDFromNormalized(normalized), nil
		}
		return StackDescription{}, requestIDFromNormalized(normalized), normalized
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

func cloudFormationStackNotFound(err error) bool {
	var providerError *contracts.ProviderCallError
	return errors.As(NormalizeError(err), &providerError) && providerError.Provider.Code == "ValidationError" &&
		strings.Contains(strings.ToLower(providerError.Provider.Message), "stack") &&
		strings.Contains(strings.ToLower(providerError.Provider.Message), "does not exist")
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
