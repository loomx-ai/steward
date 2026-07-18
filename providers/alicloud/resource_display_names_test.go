package alicloud

import (
	"strings"
	"testing"
)

func TestCuratedResourceDisplayNamesAreCompleteAndDistinct(t *testing.T) {
	t.Parallel()

	if got := len(resourceDisplayNames); got != 140 {
		t.Fatalf("curated resource display name count = %d, want 140", got)
	}

	seenZhCN := make(map[string]string, len(resourceDisplayNames))
	seenEnUS := make(map[string]string, len(resourceDisplayNames))
	for nativeType, names := range resourceDisplayNames {
		assertCuratedResourceDisplayName(t, nativeType, "zh-CN", names.zhCN, seenZhCN)
		assertCuratedResourceDisplayName(t, nativeType, "en-US", names.enUS, seenEnUS)
	}
}

func TestCuratedResourceDisplayNameExamples(t *testing.T) {
	t.Parallel()

	expected := map[string]localizedResourceDisplayName{
		"ACS::ACK::Cluster": {
			zhCN: "ACK 集群",
			enUS: "ACK Cluster",
		},
		"ACS::ALB::LoadBalancer": {
			zhCN: "ALB 实例",
			enUS: "ALB Instance",
		},
		"ACS::ARMS::Prometheus": {
			zhCN: "Prometheus 实例",
			enUS: "Prometheus Instance",
		},
		"ACS::ARMS::Environment": {
			zhCN: "ARMS VPC 环境",
			enUS: "ARMS VPC Environment",
		},
		"ACS::CBWP::CommonBandwidthPackage": {
			zhCN: "共享带宽实例",
			enUS: "Shared Bandwidth Instance",
		},
		"ACS::CR::Instance": {
			zhCN: "容器镜像实例",
			enUS: "Container Registry Instance",
		},
		"ACS::ECS::AutoSnapshotPolicy": {
			zhCN: "自动快照策略",
			enUS: "Auto Snapshot Policy",
		},
		"ACS::ECS::Image": {
			zhCN: "镜像",
			enUS: "Image",
		},
		"ACS::ECS::NetworkInterface": {
			zhCN: "弹性网卡",
			enUS: "Elastic Network Interface",
		},
		"ACS::ECS::SecurityGroup": {
			zhCN: "安全组",
			enUS: "Security Group",
		},
		"ACS::ECS::Snapshot": {
			zhCN: "快照",
			enUS: "Snapshot",
		},
		"ACS::VPC::Ipv4Gateway": {
			zhCN: "IPv4 网关",
			enUS: "IPv4 Gateway",
		},
		"ACS::VPC::Ipv6Address": {
			zhCN: "IPv6 地址",
			enUS: "IPv6 Address",
		},
		"ACS::VPC::Ipv6Gateway": {
			zhCN: "IPv6 网关",
			enUS: "IPv6 Gateway",
		},
		"ACS::VPC::VPC": {
			zhCN: "专有网络",
			enUS: "VPC",
		},
		"ACS::VPC::VSwitch": {
			zhCN: "交换机",
			enUS: "vSwitch",
		},
	}
	for nativeType, want := range expected {
		if got := resourceDisplayNames[nativeType]; got != want {
			t.Errorf("%s display names = %+v, want %+v", nativeType, got, want)
		}
	}
}

func assertCuratedResourceDisplayName(
	t *testing.T,
	nativeType string,
	locale string,
	name string,
	seen map[string]string,
) {
	t.Helper()

	name = strings.TrimSpace(name)
	if name == "" {
		t.Errorf("%s is missing its %s display name", nativeType, locale)
		return
	}
	if strings.Contains(name, "/") {
		t.Errorf("%s %s display name still uses product/type composition: %q", nativeType, locale, name)
	}
	if previous, exists := seen[name]; exists {
		t.Errorf("%s and %s share the ambiguous %s display name %q", previous, nativeType, locale, name)
		return
	}
	seen[name] = nativeType
}
