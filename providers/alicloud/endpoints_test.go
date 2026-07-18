package alicloud

import (
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestResolveEndpointUsesExplicitSiteRulesAndPreservesFallbacks(t *testing.T) {
	tests := []struct {
		service  string
		site     asset.ConnectionSite
		region   string
		fallback string
		want     string
	}{
		{"ros", asset.ConnectionSiteCN, "cn-hangzhou", "", "ros.aliyuncs.com"},
		{"ros", asset.ConnectionSiteINTL, "eu-central-1", "", "ros-intl.aliyuncs.com"},
		{"resourcecenter", asset.ConnectionSiteCN, "cn-beijing", "", "resourcecenter.aliyuncs.com"},
		{"resourcecenter", asset.ConnectionSiteINTL, "ap-southeast-1", "", "resourcecenter-intl.aliyuncs.com"},
		{"arms", asset.ConnectionSiteINTL, "ap-southeast-1", "metrics.ap-southeast-1.aliyuncs.com", "metrics.ap-southeast-1.aliyuncs.com"},
		{"ack", asset.ConnectionSiteCN, "cn-hangzhou", "cs.cn-hangzhou.aliyuncs.com", "cs.cn-hangzhou.aliyuncs.com"},
		{"ecs", asset.ConnectionSiteINTL, "eu-central-1", "", ""},
	}
	for _, test := range tests {
		got, err := resolveEndpoint(test.service, test.site, test.region, test.fallback)
		if err != nil {
			t.Fatalf("resolveEndpoint(%q, %q, %q): %v", test.service, test.site, test.region, err)
		}
		if got != test.want {
			t.Fatalf("resolveEndpoint(%q, %q, %q) = %q, want %q", test.service, test.site, test.region, got, test.want)
		}
	}
}

func TestResolveEndpointRejectsMissingOrUnsupportedMappedSite(t *testing.T) {
	for _, site := range []asset.ConnectionSite{"", "moon"} {
		if _, err := resolveEndpoint(serviceROS, site, "cn-hangzhou", "fallback.example"); err == nil {
			t.Fatalf("resolveEndpoint(ros, %q) must reject the site", site)
		}
	}

	siteEndpointRules["test-cn-only"] = siteEndpointRule{
		global: true,
		sites:  map[asset.ConnectionSite]string{asset.ConnectionSiteCN: "cn-only.example"},
	}
	defer delete(siteEndpointRules, "test-cn-only")
	if _, err := resolveEndpoint("test-cn-only", asset.ConnectionSiteINTL, "eu-central-1", "fallback.example"); err == nil {
		t.Fatal("mapped service must not fall back to a China endpoint for the international site")
	}
}

func TestResolveEndpointGlobalServicesIgnoreRegion(t *testing.T) {
	for _, service := range []string{serviceROS, serviceResourceCenter} {
		first, err := resolveEndpoint(service, asset.ConnectionSiteINTL, "eu-central-1", "")
		if err != nil {
			t.Fatal(err)
		}
		second, err := resolveEndpoint(service, asset.ConnectionSiteINTL, "ap-southeast-1", "")
		if err != nil {
			t.Fatal(err)
		}
		if first != second {
			t.Fatalf("%s endpoint changed by region: %q != %q", service, first, second)
		}
	}
}
