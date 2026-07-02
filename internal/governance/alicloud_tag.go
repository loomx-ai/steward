package governance

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	ecsclient "github.com/alibabacloud-go/ecs-20140526/v7/client"
	"github.com/alibabacloud-go/tea/tea"
	vpcclient "github.com/alibabacloud-go/vpc-20160428/v7/client"
	"github.com/prodesire/cloud-steward/internal/domain"
	"github.com/prodesire/cloud-steward/internal/store"
)

type AliCloudResourceTagger interface {
	TagECSResource(context.Context, domain.Account, string, string, string, map[string]string) (string, error)
	TagVPCResource(context.Context, domain.Account, string, string, string, map[string]string) (string, error)
}

type AliCloudTagClient struct {
	Repo   store.Repository
	Tagger AliCloudResourceTagger
}

type AliCloudSDKTagger struct{}

func NewAliCloudTagClient(repo store.Repository) AliCloudTagClient {
	return AliCloudTagClient{Repo: repo, Tagger: AliCloudSDKTagger{}}
}

func (c AliCloudTagClient) TagResource(ctx context.Context, resource domain.Resource, tags map[string]string) (string, error) {
	if resource.Provider != domain.ProviderAliCloud {
		return "", fmt.Errorf("unsupported provider for alicloud tag client: %s", resource.Provider)
	}
	if c.Repo == nil {
		return "", errors.New("alicloud tag client requires a repository")
	}
	account, err := c.Repo.GetAccount(ctx, resource.AccountID)
	if err != nil {
		return "", err
	}
	tagger := c.Tagger
	if tagger == nil {
		tagger = AliCloudSDKTagger{}
	}
	product, resourceType, err := aliCloudTagRoute(resource.Type)
	if err != nil {
		return "", err
	}
	switch product {
	case "ecs":
		return tagger.TagECSResource(ctx, account, resource.Region, resourceType, resource.NativeID, tags)
	case "vpc":
		return tagger.TagVPCResource(ctx, account, resource.Region, resourceType, resource.NativeID, tags)
	default:
		return "", fmt.Errorf("unsupported alicloud tag product: %s", product)
	}
}

func (AliCloudSDKTagger) TagECSResource(ctx context.Context, account domain.Account, region string, resourceType string, nativeID string, tags map[string]string) (string, error) {
	if err := validateAliCloudTagRequest(ctx, account, region, resourceType, nativeID, tags); err != nil {
		return "", err
	}
	ecs, err := ecsclient.NewClient(aliCloudConfig(account, region))
	if err != nil {
		return "", err
	}
	resp, err := ecs.TagResources(&ecsclient.TagResourcesRequest{
		RegionId:     tea.String(region),
		ResourceId:   []*string{tea.String(nativeID)},
		ResourceType: tea.String(resourceType),
		Tag:          ecsTagList(tags),
	})
	if err != nil {
		return "", err
	}
	if resp != nil && resp.Body != nil && resp.Body.RequestId != nil {
		return tea.StringValue(resp.Body.RequestId), nil
	}
	return "", nil
}

func (AliCloudSDKTagger) TagVPCResource(ctx context.Context, account domain.Account, region string, resourceType string, nativeID string, tags map[string]string) (string, error) {
	if err := validateAliCloudTagRequest(ctx, account, region, resourceType, nativeID, tags); err != nil {
		return "", err
	}
	vpc, err := vpcclient.NewClient(aliCloudConfig(account, region))
	if err != nil {
		return "", err
	}
	resp, err := vpc.TagResources(&vpcclient.TagResourcesRequest{
		RegionId:     tea.String(region),
		ResourceId:   []*string{tea.String(nativeID)},
		ResourceType: tea.String(resourceType),
		Tag:          vpcTagList(tags),
	})
	if err != nil {
		return "", err
	}
	if resp != nil && resp.Body != nil && resp.Body.RequestId != nil {
		return tea.StringValue(resp.Body.RequestId), nil
	}
	return "", nil
}

func aliCloudConfig(account domain.Account, region string) *openapi.Config {
	return &openapi.Config{
		AccessKeyId:     tea.String(account.AccessKeyID),
		AccessKeySecret: tea.String(account.AccessKeySecret),
		RegionId:        tea.String(region),
	}
}

func validateAliCloudTagRequest(ctx context.Context, account domain.Account, region string, resourceType string, nativeID string, tags map[string]string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if strings.TrimSpace(account.AccessKeyID) == "" || strings.TrimSpace(account.AccessKeySecret) == "" {
		return errors.New("alicloud access key id and secret are required")
	}
	if strings.TrimSpace(region) == "" {
		return errors.New("alicloud region is required")
	}
	if strings.TrimSpace(resourceType) == "" {
		return errors.New("alicloud resource type is required")
	}
	if strings.TrimSpace(nativeID) == "" {
		return errors.New("alicloud resource id is required")
	}
	if len(tags) == 0 {
		return errors.New("at least one tag is required")
	}
	return nil
}

func aliCloudTagRoute(resourceType domain.ResourceType) (string, string, error) {
	switch resourceType {
	case domain.ResourceTypeECSInstance:
		return "ecs", "instance", nil
	case domain.ResourceTypeDisk:
		return "ecs", "disk", nil
	case domain.ResourceTypeSecurityGroup:
		return "ecs", "securitygroup", nil
	case domain.ResourceTypeSnapshot:
		return "ecs", "snapshot", nil
	case domain.ResourceTypeEIP:
		return "vpc", "EIP", nil
	case domain.ResourceTypeVPC:
		return "vpc", "VPC", nil
	case domain.ResourceTypeVSwitch:
		return "vpc", "VSWITCH", nil
	default:
		return "", "", fmt.Errorf("unsupported alicloud tag resource type: %s", resourceType)
	}
}

func ecsTagList(tags map[string]string) []*ecsclient.TagResourcesRequestTag {
	keys := sortedTagKeys(tags)
	out := make([]*ecsclient.TagResourcesRequestTag, 0, len(keys))
	for _, key := range keys {
		out = append(out, &ecsclient.TagResourcesRequestTag{
			Key:   tea.String(key),
			Value: tea.String(tags[key]),
		})
	}
	return out
}

func vpcTagList(tags map[string]string) []*vpcclient.TagResourcesRequestTag {
	keys := sortedTagKeys(tags)
	out := make([]*vpcclient.TagResourcesRequestTag, 0, len(keys))
	for _, key := range keys {
		out = append(out, &vpcclient.TagResourcesRequestTag{
			Key:   tea.String(key),
			Value: tea.String(tags[key]),
		})
	}
	return out
}

func sortedTagKeys(tags map[string]string) []string {
	keys := make([]string, 0, len(tags))
	for key := range tags {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
