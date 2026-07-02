package scanner

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	ecsclient "github.com/alibabacloud-go/ecs-20140526/v7/client"
	"github.com/alibabacloud-go/tea/tea"
	vpcclient "github.com/alibabacloud-go/vpc-20160428/v7/client"
	"github.com/prodesire/cloud-steward/internal/domain"
)

type AliCloudConnector struct{}

func NewAliCloudConnector() AliCloudConnector {
	return AliCloudConnector{}
}

func (AliCloudConnector) ScanRegion(ctx context.Context, account domain.Account, _ domain.ScanJob, region string) ([]domain.Resource, error) {
	if strings.TrimSpace(account.AccessKeyID) == "" || strings.TrimSpace(account.AccessKeySecret) == "" {
		return nil, errors.New("alicloud access key id and secret are required")
	}
	if strings.TrimSpace(region) == "" {
		return nil, errors.New("alicloud region is required")
	}

	config := &openapi.Config{
		AccessKeyId:     tea.String(account.AccessKeyID),
		AccessKeySecret: tea.String(account.AccessKeySecret),
		RegionId:        tea.String(region),
	}
	ecs, err := ecsclient.NewClient(config)
	if err != nil {
		return nil, err
	}
	vpc, err := vpcclient.NewClient(config)
	if err != nil {
		return nil, err
	}

	var resources []domain.Resource
	collectors := []func(context.Context, domain.Account, string) ([]domain.Resource, error){
		func(ctx context.Context, account domain.Account, region string) ([]domain.Resource, error) {
			return scanAliCloudInstances(ctx, ecs, account, region)
		},
		func(ctx context.Context, account domain.Account, region string) ([]domain.Resource, error) {
			return scanAliCloudDisks(ctx, ecs, account, region)
		},
		func(ctx context.Context, account domain.Account, region string) ([]domain.Resource, error) {
			return scanAliCloudSecurityGroups(ctx, ecs, account, region)
		},
		func(ctx context.Context, account domain.Account, region string) ([]domain.Resource, error) {
			return scanAliCloudSnapshots(ctx, ecs, account, region)
		},
		func(ctx context.Context, account domain.Account, region string) ([]domain.Resource, error) {
			return scanAliCloudEIPs(ctx, vpc, account, region)
		},
		func(ctx context.Context, account domain.Account, region string) ([]domain.Resource, error) {
			return scanAliCloudVPCs(ctx, vpc, account, region)
		},
		func(ctx context.Context, account domain.Account, region string) ([]domain.Resource, error) {
			return scanAliCloudVSwitches(ctx, vpc, account, region)
		},
	}
	for _, collect := range collectors {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		default:
		}
		scanned, err := collect(ctx, account, region)
		if err != nil {
			return nil, err
		}
		resources = append(resources, scanned...)
	}
	return resources, nil
}

func scanAliCloudInstances(_ context.Context, ecs *ecsclient.Client, account domain.Account, region string) ([]domain.Resource, error) {
	var resources []domain.Resource
	var nextToken *string
	for {
		resp, err := ecs.DescribeInstances(&ecsclient.DescribeInstancesRequest{
			RegionId:   tea.String(region),
			MaxResults: tea.Int32(100),
			NextToken:  nextToken,
		})
		if err != nil {
			return nil, err
		}
		if resp != nil && resp.Body != nil && resp.Body.Instances != nil {
			for _, item := range resp.Body.Instances.Instance {
				if item == nil || str(item.InstanceId) == "" {
					continue
				}
				resources = append(resources, domain.Resource{
					Provider:   domain.ProviderAliCloud,
					AccountID:  account.ID,
					Region:     firstNonEmpty(str(item.RegionId), region),
					Type:       domain.ResourceTypeECSInstance,
					NativeID:   str(item.InstanceId),
					Name:       firstNonEmpty(str(item.InstanceName), str(item.InstanceId)),
					State:      str(item.Status),
					Tags:       tagsFromSDK(item.Tags),
					CreatedAt:  parseCloudTime(str(item.CreationTime)),
					LastSeenAt: time.Now().UTC(),
					Raw:        rawMap(item),
				})
			}
			nextToken = resp.Body.NextToken
		}
		if nextToken == nil || str(nextToken) == "" {
			break
		}
	}
	return resources, nil
}

func scanAliCloudDisks(_ context.Context, ecs *ecsclient.Client, account domain.Account, region string) ([]domain.Resource, error) {
	var resources []domain.Resource
	var nextToken *string
	for {
		resp, err := ecs.DescribeDisks(&ecsclient.DescribeDisksRequest{
			RegionId:   tea.String(region),
			MaxResults: tea.Int32(100),
			NextToken:  nextToken,
		})
		if err != nil {
			return nil, err
		}
		if resp != nil && resp.Body != nil && resp.Body.Disks != nil {
			for _, item := range resp.Body.Disks.Disk {
				if item == nil || str(item.DiskId) == "" {
					continue
				}
				resources = append(resources, domain.Resource{
					Provider:   domain.ProviderAliCloud,
					AccountID:  account.ID,
					Region:     firstNonEmpty(str(item.RegionId), region),
					Type:       domain.ResourceTypeDisk,
					NativeID:   str(item.DiskId),
					Name:       firstNonEmpty(str(item.DiskName), str(item.DiskId)),
					State:      str(item.Status),
					Tags:       tagsFromSDK(item.Tags),
					CreatedAt:  parseCloudTime(str(item.CreationTime)),
					LastSeenAt: time.Now().UTC(),
					Raw:        rawMap(item),
				})
			}
			nextToken = resp.Body.NextToken
		}
		if nextToken == nil || str(nextToken) == "" {
			break
		}
	}
	return resources, nil
}

func scanAliCloudSecurityGroups(_ context.Context, ecs *ecsclient.Client, account domain.Account, region string) ([]domain.Resource, error) {
	var resources []domain.Resource
	var nextToken *string
	for {
		resp, err := ecs.DescribeSecurityGroups(&ecsclient.DescribeSecurityGroupsRequest{
			RegionId:   tea.String(region),
			MaxResults: tea.Int32(100),
			NextToken:  nextToken,
		})
		if err != nil {
			return nil, err
		}
		if resp != nil && resp.Body != nil && resp.Body.SecurityGroups != nil {
			for _, item := range resp.Body.SecurityGroups.SecurityGroup {
				if item == nil || str(item.SecurityGroupId) == "" {
					continue
				}
				resources = append(resources, domain.Resource{
					Provider:   domain.ProviderAliCloud,
					AccountID:  account.ID,
					Region:     region,
					Type:       domain.ResourceTypeSecurityGroup,
					NativeID:   str(item.SecurityGroupId),
					Name:       firstNonEmpty(str(item.SecurityGroupName), str(item.SecurityGroupId)),
					State:      "Available",
					Tags:       tagsFromSDK(item.Tags),
					CreatedAt:  parseCloudTime(str(item.CreationTime)),
					LastSeenAt: time.Now().UTC(),
					Raw:        rawMap(item),
				})
			}
			nextToken = resp.Body.NextToken
		}
		if nextToken == nil || str(nextToken) == "" {
			break
		}
	}
	return resources, nil
}

func scanAliCloudSnapshots(_ context.Context, ecs *ecsclient.Client, account domain.Account, region string) ([]domain.Resource, error) {
	var resources []domain.Resource
	var nextToken *string
	for {
		resp, err := ecs.DescribeSnapshots(&ecsclient.DescribeSnapshotsRequest{
			RegionId:   tea.String(region),
			MaxResults: tea.Int32(100),
			NextToken:  nextToken,
		})
		if err != nil {
			return nil, err
		}
		if resp != nil && resp.Body != nil && resp.Body.Snapshots != nil {
			for _, item := range resp.Body.Snapshots.Snapshot {
				if item == nil || str(item.SnapshotId) == "" {
					continue
				}
				resources = append(resources, domain.Resource{
					Provider:   domain.ProviderAliCloud,
					AccountID:  account.ID,
					Region:     firstNonEmpty(str(item.RegionId), region),
					Type:       domain.ResourceTypeSnapshot,
					NativeID:   str(item.SnapshotId),
					Name:       firstNonEmpty(str(item.SnapshotName), str(item.SnapshotId)),
					State:      str(item.Status),
					Tags:       tagsFromSDK(item.Tags),
					CreatedAt:  parseCloudTime(str(item.CreationTime)),
					LastSeenAt: time.Now().UTC(),
					Raw:        rawMap(item),
				})
			}
			nextToken = resp.Body.NextToken
		}
		if nextToken == nil || str(nextToken) == "" {
			break
		}
	}
	return resources, nil
}

func scanAliCloudEIPs(_ context.Context, vpc *vpcclient.Client, account domain.Account, region string) ([]domain.Resource, error) {
	var resources []domain.Resource
	for page := int32(1); ; page++ {
		resp, err := vpc.DescribeEipAddresses(&vpcclient.DescribeEipAddressesRequest{
			RegionId:   tea.String(region),
			PageNumber: tea.Int32(page),
			PageSize:   tea.Int32(50),
		})
		if err != nil {
			return nil, err
		}
		count := 0
		total := int32(0)
		if resp != nil && resp.Body != nil {
			total = int32Value(resp.Body.TotalCount)
			if resp.Body.EipAddresses != nil {
				for _, item := range resp.Body.EipAddresses.EipAddress {
					if item == nil || str(item.AllocationId) == "" {
						continue
					}
					count++
					resources = append(resources, domain.Resource{
						Provider:   domain.ProviderAliCloud,
						AccountID:  account.ID,
						Region:     firstNonEmpty(str(item.RegionId), region),
						Type:       domain.ResourceTypeEIP,
						NativeID:   str(item.AllocationId),
						Name:       firstNonEmpty(str(item.Name), str(item.IpAddress), str(item.AllocationId)),
						State:      str(item.Status),
						Tags:       tagsFromSDK(item.Tags),
						CreatedAt:  parseCloudTime(str(item.AllocationTime)),
						LastSeenAt: time.Now().UTC(),
						Raw:        rawMap(item),
					})
				}
			}
		}
		if count == 0 || page*50 >= total {
			break
		}
	}
	return resources, nil
}

func scanAliCloudVPCs(_ context.Context, vpc *vpcclient.Client, account domain.Account, region string) ([]domain.Resource, error) {
	var resources []domain.Resource
	for page := int32(1); ; page++ {
		resp, err := vpc.DescribeVpcs(&vpcclient.DescribeVpcsRequest{
			RegionId:   tea.String(region),
			PageNumber: tea.Int32(page),
			PageSize:   tea.Int32(50),
		})
		if err != nil {
			return nil, err
		}
		count := 0
		total := int32(0)
		if resp != nil && resp.Body != nil {
			total = int32Value(resp.Body.TotalCount)
			if resp.Body.Vpcs != nil {
				for _, item := range resp.Body.Vpcs.Vpc {
					if item == nil || str(item.VpcId) == "" {
						continue
					}
					count++
					resources = append(resources, domain.Resource{
						Provider:   domain.ProviderAliCloud,
						AccountID:  account.ID,
						Region:     firstNonEmpty(str(item.RegionId), region),
						Type:       domain.ResourceTypeVPC,
						NativeID:   str(item.VpcId),
						Name:       firstNonEmpty(str(item.VpcName), str(item.VpcId)),
						State:      str(item.Status),
						Tags:       tagsFromSDK(item.Tags),
						CreatedAt:  parseCloudTime(str(item.CreationTime)),
						LastSeenAt: time.Now().UTC(),
						Raw:        rawMap(item),
					})
				}
			}
		}
		if count == 0 || page*50 >= total {
			break
		}
	}
	return resources, nil
}

func scanAliCloudVSwitches(_ context.Context, vpc *vpcclient.Client, account domain.Account, region string) ([]domain.Resource, error) {
	var resources []domain.Resource
	for page := int32(1); ; page++ {
		resp, err := vpc.DescribeVSwitches(&vpcclient.DescribeVSwitchesRequest{
			RegionId:   tea.String(region),
			PageNumber: tea.Int32(page),
			PageSize:   tea.Int32(50),
		})
		if err != nil {
			return nil, err
		}
		count := 0
		total := int32(0)
		if resp != nil && resp.Body != nil {
			total = int32Value(resp.Body.TotalCount)
			if resp.Body.VSwitches != nil {
				for _, item := range resp.Body.VSwitches.VSwitch {
					if item == nil || str(item.VSwitchId) == "" {
						continue
					}
					count++
					resources = append(resources, domain.Resource{
						Provider:   domain.ProviderAliCloud,
						AccountID:  account.ID,
						Region:     region,
						Type:       domain.ResourceTypeVSwitch,
						NativeID:   str(item.VSwitchId),
						Name:       firstNonEmpty(str(item.VSwitchName), str(item.VSwitchId)),
						State:      str(item.Status),
						Tags:       tagsFromSDK(item.Tags),
						CreatedAt:  parseCloudTime(str(item.CreationTime)),
						LastSeenAt: time.Now().UTC(),
						Raw:        rawMap(item),
					})
				}
			}
		}
		if count == 0 || page*50 >= total {
			break
		}
	}
	return resources, nil
}

func str(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func int32Value(value *int32) int32 {
	if value == nil {
		return 0
	}
	return *value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func parseCloudTime(value string) time.Time {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Now().UTC()
	}
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04Z",
		"2006-01-02T15:04:05Z",
		"2006-01-02 15:04:05",
	}
	for _, format := range formats {
		parsed, err := time.Parse(format, value)
		if err == nil {
			return parsed.UTC()
		}
	}
	return time.Now().UTC()
}

func rawMap(value any) map[string]any {
	payload, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(payload, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func tagsFromSDK(value any) map[string]string {
	payload := rawMap(value)
	rawTags, ok := payload["Tag"].([]any)
	if !ok {
		return map[string]string{}
	}
	tags := make(map[string]string, len(rawTags))
	for _, rawTag := range rawTags {
		item, ok := rawTag.(map[string]any)
		if !ok {
			continue
		}
		key := firstNonEmpty(anyString(item["TagKey"]), anyString(item["Key"]))
		value := firstNonEmpty(anyString(item["TagValue"]), anyString(item["Value"]))
		if key != "" {
			tags[key] = value
		}
	}
	return tags
}

func anyString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case *string:
		return str(typed)
	default:
		return ""
	}
}
