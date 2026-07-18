package alicloud

import (
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const (
	serviceSTS            = "sts"
	serviceVPC            = "vpc"
	serviceResourceCenter = "resourcecenter"
	serviceACK            = "ack"
	serviceROS            = "ros"
)

type siteEndpointRule struct {
	global bool
	sites  map[asset.ConnectionSite]string
}

var siteEndpointRules = map[string]siteEndpointRule{
	serviceROS: {
		global: true,
		sites: map[asset.ConnectionSite]string{
			asset.ConnectionSiteCN:   "ros.aliyuncs.com",
			asset.ConnectionSiteINTL: "ros-intl.aliyuncs.com",
		},
	},
	serviceResourceCenter: {
		global: true,
		sites: map[asset.ConnectionSite]string{
			asset.ConnectionSiteCN:   "resourcecenter.aliyuncs.com",
			asset.ConnectionSiteINTL: "resourcecenter-intl.aliyuncs.com",
		},
	},
}

func resolveEndpoint(service string, site asset.ConnectionSite, region, fallback string) (string, error) {
	service = strings.TrimSpace(service)
	fallback = strings.TrimSpace(fallback)
	rule, mapped := siteEndpointRules[service]
	if !mapped {
		return fallback, nil
	}
	endpoint, supported := rule.sites[site]
	if !supported || strings.TrimSpace(endpoint) == "" {
		return "", fmt.Errorf("Alibaba Cloud service %q does not define an endpoint for site %q", service, site)
	}
	return strings.TrimSpace(endpoint), nil
}
