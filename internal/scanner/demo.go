package scanner

import (
	"context"
	"time"

	"github.com/prodesire/cloud-steward/internal/domain"
)

type DemoConnector struct{}

func (DemoConnector) ScanRegion(_ context.Context, account domain.Account, _ domain.ScanJob, region string) ([]domain.Resource, error) {
	now := time.Now().UTC()
	created := now.AddDate(0, -1, 0)
	common := map[string]string{
		"team":        "platform",
		"application": "checkout",
		"env":         "dev",
		"cost-center": "cc-demo",
	}
	return []domain.Resource{
		{
			Provider:   domain.ProviderDemo,
			AccountID:  account.ID,
			Region:     region,
			Type:       domain.ResourceTypeVPC,
			NativeID:   "vpc-demo-001",
			Name:       "demo-vpc",
			State:      "Available",
			Tags:       copyTags(common),
			CreatedAt:  created,
			LastSeenAt: now,
			Raw:        map[string]any{"VpcId": "vpc-demo-001", "CidrBlock": "10.0.0.0/16"},
		},
		{
			Provider:   domain.ProviderDemo,
			AccountID:  account.ID,
			Region:     region,
			Type:       domain.ResourceTypeVSwitch,
			NativeID:   "vsw-demo-001",
			Name:       "demo-vswitch",
			State:      "Available",
			Tags:       copyTags(common),
			CreatedAt:  created,
			LastSeenAt: now,
			Raw:        map[string]any{"VSwitchId": "vsw-demo-001", "VpcId": "vpc-demo-001"},
		},
		{
			Provider:   domain.ProviderDemo,
			AccountID:  account.ID,
			Region:     region,
			Type:       domain.ResourceTypeSecurityGroup,
			NativeID:   "sg-demo-001",
			Name:       "demo-sg",
			State:      "Available",
			Tags:       copyTags(common),
			CreatedAt:  created,
			LastSeenAt: now,
			Raw:        map[string]any{"SecurityGroupId": "sg-demo-001", "VpcId": "vpc-demo-001"},
		},
		{
			Provider:   domain.ProviderDemo,
			AccountID:  account.ID,
			Region:     region,
			Type:       domain.ResourceTypeECSInstance,
			NativeID:   "i-demo-001",
			Name:       "demo-api",
			State:      "Stopped",
			Tags:       copyTags(common),
			CreatedAt:  created,
			LastSeenAt: now,
			Raw:        map[string]any{"InstanceId": "i-demo-001", "VpcId": "vpc-demo-001", "VSwitchId": "vsw-demo-001"},
		},
		{
			Provider:   domain.ProviderDemo,
			AccountID:  account.ID,
			Region:     region,
			Type:       domain.ResourceTypeDisk,
			NativeID:   "d-demo-001",
			Name:       "demo-orphan-disk",
			State:      "Available",
			Tags:       copyTags(common),
			CreatedAt:  created,
			LastSeenAt: now,
			Raw:        map[string]any{"DiskId": "d-demo-001", "AttachedTime": ""},
		},
		{
			Provider:   domain.ProviderDemo,
			AccountID:  account.ID,
			Region:     region,
			Type:       domain.ResourceTypeEIP,
			NativeID:   "eip-demo-001",
			Name:       "demo-unused-eip",
			State:      "Available",
			Tags:       copyTags(common),
			CreatedAt:  created,
			LastSeenAt: now,
			Raw:        map[string]any{"AllocationId": "eip-demo-001", "InstanceId": ""},
		},
		{
			Provider:   domain.ProviderDemo,
			AccountID:  account.ID,
			Region:     region,
			Type:       domain.ResourceTypeSnapshot,
			NativeID:   "s-demo-001",
			Name:       "demo-old-snapshot",
			State:      "accomplished",
			Tags:       copyTags(common),
			CreatedAt:  now.AddDate(0, -4, 0),
			LastSeenAt: now,
			Raw:        map[string]any{"SnapshotId": "s-demo-001", "SourceDiskId": "d-demo-001"},
		},
	}, nil
}

func copyTags(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
