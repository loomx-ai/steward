package gcp

import (
	"context"
	"reflect"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const securitySubscriptionType = "securitycenter.googleapis.com/Subscription"
const securitySubscriptionGet = "securitycenter.organizations.getSubscription"
const securitySubscriptionHost = "securitycenter.googleapis.com"

func securitySubscriptionOperation(metadata providerMetadata, id, method string) (catalog.Operation, map[string]any, error) {
	name := strings.TrimPrefix(id, "//"+securitySubscriptionHost+"/")
	parent := strings.TrimSuffix(name, "/subscription")
	if method != "GET" || name == id || parent == name || !strings.HasPrefix(parent, "organizations/") || !firewallContainerName(parent) {
		return catalog.Operation{}, nil, groupDenied("security_subscription_identity_invalid")
	}
	op, ok := metadata.catalog.Operation(securitySubscriptionGet)
	if !ok {
		return catalog.Operation{}, nil, groupDenied("security_subscription_method_missing")
	}
	return op, map[string]any{"name": name}, nil
}

// The caller must establish the selected project's ancestry before this read,
// then recheck it afterwards. An organization subscription is not project billing.
func (c *client) readSecuritySubscription(ctx context.Context, name string) (contracts.InvocationResult, error) {
	metadata, err := providerData()
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	op, parameters, err := securitySubscriptionOperation(metadata, "//"+securitySubscriptionHost+"/"+name, "GET")
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	bound, err := catalog.BindREST(op, parameters)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	result, err := c.requestResult(ctx, bound.Method, bound.URL, nil, nil)
	if err != nil {
		return result, err
	}
	if result.Data["name"] != name {
		return contracts.InvocationResult{}, groupDenied("security_subscription_identity_changed")
	}
	if value, present := result.Data["tier"]; present {
		if _, ok := value.(string); !ok {
			return contracts.InvocationResult{}, groupDenied("security_subscription_tier_invalid")
		}
	}
	if value, present := result.Data["details"]; present {
		details, ok := value.(map[string]any)
		if !ok {
			return contracts.InvocationResult{}, groupDenied("security_subscription_details_invalid")
		}
		if value, present := details["type"]; present {
			if _, ok := value.(string); !ok {
				return contracts.InvocationResult{}, groupDenied("security_subscription_type_invalid")
			}
		}
		for _, field := range []string{"startTime", "endTime"} {
			if value, present := details[field]; present {
				if _, err := time.Parse(time.RFC3339Nano, text(value)); err != nil {
					return contracts.InvocationResult{}, groupDenied("security_subscription_time_invalid")
				}
			}
		}
	}
	result.Data = safePayload(result.Data)
	return result, nil
}

func (c *client) invokeSecuritySubscription(ctx context.Context, parameters map[string]any) (contracts.InvocationResult, error) {
	first, err := c.organizationAncestry(ctx)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	if first.Organization == nil || parameters["name"] != text(first.Organization["name"])+"/subscription" {
		return contracts.InvocationResult{}, groupDenied("security_subscription_outside_ancestry")
	}
	result, err := c.readSecuritySubscription(ctx, text(parameters["name"]))
	if err != nil {
		return result, err
	}
	second, err := c.organizationAncestry(ctx)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	if !reflect.DeepEqual(first, second) {
		return contracts.InvocationResult{}, groupDenied("organization_ancestry_changed")
	}
	return result, nil
}
