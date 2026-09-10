package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	cdnProfileType        = "Microsoft.Cdn/profiles"
	cdnEndpointType       = cdnProfileType + "/endpoints"
	cdnOriginType         = cdnEndpointType + "/origins"
	cdnOriginGroupType    = cdnEndpointType + "/originGroups"
	cdnDomainType         = cdnEndpointType + "/customDomains"
	afdEndpointType       = cdnProfileType + "/afdEndpoints"
	afdRouteType          = afdEndpointType + "/routes"
	afdDomainType         = cdnProfileType + "/customDomains"
	afdOriginGroupType    = cdnProfileType + "/originGroups"
	afdOriginType         = afdOriginGroupType + "/origins"
	afdRuleSetType        = cdnProfileType + "/ruleSets"
	afdRuleType           = afdRuleSetType + "/rules"
	afdSecurityPolicyType = cdnProfileType + "/securityPolicies"
	afdSecretType         = cdnProfileType + "/secrets"
)

func isCDNType(kind string) bool {
	return strings.EqualFold(kind, cdnProfileType) || strings.HasPrefix(strings.ToLower(kind), strings.ToLower(cdnProfileType)+"/")
}

// The common profile API exposes both products. Their child collections are
// different APIs; a 404 from an inapplicable collection is not an empty list.
func cdnProfileFamily(raw map[string]any) (string, error) {
	switch text(object(raw["sku"])["name"]) {
	case "Standard_AzureFrontDoor", "Premium_AzureFrontDoor":
		return "afd", nil
	case "Standard_Verizon", "Premium_Verizon", "Custom_Verizon", "Standard_Akamai", "Standard_Microsoft", "Standard_ChinaCdn", "Standard_955BandWidth_ChinaCdn", "Standard_AvgBandWidth_ChinaCdn", "StandardPlus_ChinaCdn", "StandardPlus_955BandWidth_ChinaCdn", "StandardPlus_AvgBandWidth_ChinaCdn":
		return "cdn", nil
	default:
		return "", serviceDenied("unknown_cdn_profile_sku")
	}
}

func cdnChildApplies(kind string, profile map[string]any) (bool, error) {
	family, err := cdnProfileFamily(profile)
	classic := strings.EqualFold(kind, cdnEndpointType) || strings.HasPrefix(strings.ToLower(kind), strings.ToLower(cdnEndpointType)+"/")
	return (family == "cdn") == classic, err
}

func cdnProfileID(id string) string {
	parts := strings.Split(strings.ToLower(id), "/")
	if len(parts) < 9 {
		return ""
	}
	return strings.Join(parts[:9], "/")
}

var cdnBatchRuleName = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9]{0,59}$`)

// Batch rules are embedded RuleSet properties, without independent resource
// IDs. Only the rule-set API owns their atomic lifecycle. Require a complete
// detail response; the native rule-set LIST omits the rules array.
func cdnBatchMode(raw map[string]any) (bool, error) {
	properties := object(raw["properties"])
	value, present := properties["batchMode"]
	if !present {
		return false, nil
	}
	batch, ok := value.(bool)
	if !ok {
		return false, serviceDenied("invalid_cdn_batch_mode")
	}
	if !batch {
		return false, nil
	}
	rules, ok := properties["rules"].([]any)
	if !ok {
		return false, serviceDenied("incomplete_cdn_batch_rules")
	}
	seen := map[string]bool{}
	for _, value := range rules {
		rule := object(value)
		name, _ := rule["ruleName"].(string)
		if !cdnBatchRuleName.MatchString(name) || seen[strings.ToLower(name)] {
			return false, serviceDenied("invalid_cdn_batch_rule_name")
		}
		seen[strings.ToLower(name)] = true
		if parent, exists := rule["ruleSetName"]; exists && !strings.EqualFold(text(parent), last(text(raw["id"]))) {
			return false, serviceDenied("invalid_cdn_batch_rule_parent")
		}
		for _, field := range []string{"actions", "conditions"} {
			if value, exists := rule[field]; exists {
				members, ok := value.([]any)
				if !ok {
					return false, serviceDenied("invalid_cdn_batch_rule_configuration")
				}
				for _, member := range members {
					if text(object(member)["name"]) == "" || object(object(member)["parameters"]) == nil {
						return false, serviceDenied("invalid_cdn_batch_rule_configuration")
					}
				}
			}
		}
	}
	return true, nil
}

func (c *client) cdnRuleParent(ctx context.Context, id string) (map[string]any, error) {
	id = strings.ToLower(id)
	index := strings.LastIndex(id, "/rules/")
	if index < 0 {
		return nil, serviceDenied("invalid_cdn_rule_identity")
	}
	parentID := id[:index]
	kind, _ := findType(afdRuleSetType)
	endpoint, err := c.resourceURL(kind, parentID)
	if err != nil {
		return nil, err
	}
	live, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(live, parentID, afdRuleSetType) {
		return nil, serviceDenied("invalid_cdn_rule_set_identity")
	}
	batch, err := cdnBatchMode(live.data)
	if err != nil {
		return nil, err
	}
	if batch {
		return nil, serviceDenied("cdn_batch_rule_requires_rule_set")
	}
	return live.data, nil
}

// Preserve configuration and stable native identities. Proxy resources acquire
// a location from inventory's parent; it is not part of their native GET schema.
func cdnSnapshot(kind string, raw map[string]any) map[string]any {
	payload, _ := json.Marshal(raw)
	var snapshot map[string]any
	json.Unmarshal(payload, &snapshot)
	snapshot["id"] = strings.ToLower(text(raw["id"]))
	snapshot["name"] = last(text(snapshot["id"]))
	delete(snapshot, "type")
	delete(snapshot, "etag")
	if !strings.EqualFold(kind, cdnProfileType) && !strings.EqualFold(kind, cdnEndpointType) && !strings.EqualFold(kind, afdEndpointType) {
		delete(snapshot, "location")
	}
	for _, field := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(snapshot["systemData"]), field)
	}
	var clean func(any)
	clean = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for key, nested := range value {
				switch key {
				case "provisioningState", "deploymentStatus", "resourceState", "domainValidationState", "validationProperties", "customHttpsProvisioningState", "customHttpsProvisioningSubstate":
					delete(value, key)
				default:
					clean(nested)
				}
			}
		case []any:
			for _, nested := range value {
				clean(nested)
			}
		}
	}
	clean(object(snapshot["properties"]))
	return snapshot
}

func cdnConfiguration(kind string, raw map[string]any) string {
	return serviceParentConfiguration(kind, cdnSnapshot(kind, raw))
}

func cdnIncarnation(planned asset.Asset, live map[string]any) error {
	kind := planned.Identity.NativeType
	if !isCDNType(kind) {
		return nil
	}
	if kind == cdnProfileType {
		if _, err := cdnProfileFamily(live); err != nil {
			return err
		}
	}
	if expected := text(planned.Normalized["_cdn_configuration"]); expected == "" || expected != cdnConfiguration(kind, live) {
		return serviceDenied("cdn_configuration_changed")
	}
	if _, err := cdnReferenceIDs(kind, live); err != nil {
		return err
	}
	return nil
}

func (c *client) cdnProfile(ctx context.Context, id string) (map[string]any, error) {
	profileID := cdnProfileID(id)
	kind, _ := findType(cdnProfileType)
	endpoint, err := c.resourceURL(kind, profileID)
	if err != nil {
		return nil, err
	}
	live, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(live, profileID, cdnProfileType) {
		return nil, fmt.Errorf("Azure CDN profile identity mismatch")
	}
	if _, err := cdnProfileFamily(live.data); err != nil {
		return nil, err
	}
	return live.data, nil
}

func (c *client) cdnInventory(ctx context.Context, id, kind string, raw, normalized map[string]any) error {
	if !isCDNType(kind) {
		return nil
	}
	refs, err := cdnReferenceIDs(kind, raw)
	if err != nil {
		return err
	}
	normalized["_cdn_dependencies"] = refs
	normalized["_cdn_configuration"] = cdnConfiguration(kind, raw)
	normalized["_cdn_private_configuration"] = c.privateConfiguration(cdnSnapshot(kind, raw))
	if kind == cdnProfileType {
		_, err := cdnProfileFamily(raw)
		return err
	}
	profile, err := c.cdnProfile(ctx, id)
	if err != nil {
		return contracts.DependencyReadError(err)
	}
	if applies, err := cdnChildApplies(kind, profile); err != nil || !applies {
		return serviceDenied("cdn_profile_family_changed")
	}
	normalized["_cdn_profile_configuration"] = cdnConfiguration(cdnProfileType, profile)
	if kind == afdRuleType {
		parent, err := c.cdnRuleParent(ctx, id)
		if err != nil {
			return contracts.DependencyReadError(err)
		}
		normalized["_cdn_rule_set_configuration"] = cdnConfiguration(afdRuleSetType, parent)
	}
	return nil
}

func (a *action) cdnPreflight(ctx context.Context, planned asset.Asset, raw map[string]any) error {
	kind := planned.Identity.NativeType
	if !isCDNType(kind) {
		return nil
	}
	if err := cdnIncarnation(planned, raw); err != nil {
		return err
	}
	if kind == cdnProfileType {
		return nil
	}
	profile, err := a.client.cdnProfile(ctx, planned.Identity.NativeID)
	if err != nil {
		return err
	}
	if expected := text(planned.Normalized["_cdn_profile_configuration"]); expected == "" || expected != cdnConfiguration(cdnProfileType, profile) {
		return serviceDenied("cdn_profile_changed")
	}
	if applies, err := cdnChildApplies(kind, profile); err != nil || !applies {
		return serviceDenied("cdn_profile_family_changed")
	}
	if kind == afdRuleType {
		parent, err := a.client.cdnRuleParent(ctx, planned.Identity.NativeID)
		if err != nil {
			return err
		}
		if expected := text(planned.Normalized["_cdn_rule_set_configuration"]); expected == "" || expected != cdnConfiguration(afdRuleSetType, parent) {
			return serviceDenied("cdn_rule_set_changed")
		}
	}
	incoming, err := a.client.cdnIncoming(ctx, planned.Identity, profile)
	if err != nil {
		return err
	}
	if len(incoming) != 0 {
		return serviceDenied("cdn_references_require_prior_deletion")
	}
	return nil
}

// Repeat the native child set and compare complete configurations. Profiles do
// not expose an ETag that changes for every child insertion/removal.
func (c *client) cdnChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	if parent.NativeType == afdRuleSetType {
		if batch, err := cdnBatchMode(raw); err != nil || batch {
			return nil, err
		}
	}
	kinds := slices.Clone(serviceChildKinds(parent.NativeType))
	if parent.NativeType == cdnProfileType {
		filtered := []string{}
		for _, kind := range kinds {
			applies, err := cdnChildApplies(kind, raw)
			if err != nil {
				return nil, err
			}
			if applies {
				filtered = append(filtered, kind)
			}
		}
		kinds = filtered
	}
	first, err := c.nativeServiceChildren(ctx, parent, raw, kinds)
	if err != nil {
		return nil, err
	}
	second, err := c.nativeServiceChildren(ctx, parent, raw, kinds)
	if err != nil {
		return nil, err
	}
	if !slices.EqualFunc(first, second, func(a, b serviceChild) bool {
		return a.id == b.id && c.privateConfiguration(cdnSnapshot(a.kind, a.data)) == c.privateConfiguration(cdnSnapshot(b.kind, b.data))
	}) {
		return nil, serviceDenied("cdn_children_changed")
	}
	if parent.NativeType == cdnEndpointType {
		for _, field := range []string{"origins", "originGroups", "customDomains"} {
			value, exists := object(raw["properties"])[field]
			if !exists && field != "origins" {
				continue
			}
			members, ok := value.([]any)
			if !ok {
				return nil, serviceDenied("invalid_cdn_endpoint_children")
			}
			listed := map[string]bool{}
			for _, child := range second {
				if strings.EqualFold(child.kind, parent.NativeType+"/"+field) {
					listed[child.id] = true
				}
			}
			for _, value := range members {
				name := text(object(value)["name"])
				id := strings.ToLower(parent.NativeID + "/" + field + "/" + name)
				if !listed[id] {
					return nil, serviceDenied("cdn_endpoint_children_disagree")
				}
				delete(listed, id)
			}
			if len(listed) != 0 {
				return nil, serviceDenied("cdn_endpoint_children_disagree")
			}
		}
	}
	return second, nil
}

// Sensitive content in rule actions and key references is compared through a
// credential-keyed digest. Ordinary JSON hashes only contain sanitized values.
func (c *client) servicePrivateIncarnation(planned asset.Asset, live map[string]any) error {
	if isStreamAnalyticsType(planned.Identity.NativeType) {
		if expected := text(planned.Normalized["_stream_analytics_private_configuration"]); expected == "" || expected != c.privateConfiguration(streamAnalyticsSnapshot(planned.Identity.NativeType, live)) {
			return serviceDenied("stream_analytics_private_configuration_changed")
		}
	}
	if isCosmosType(planned.Identity.NativeType) {
		if _, err := c.plannedResourceID(planned); err != nil {
			return err
		}
		if expected := text(planned.Normalized["_cosmos_private_configuration"]); expected == "" || expected != c.privateConfiguration(cosmosSnapshot(planned.Identity.NativeType, live)) {
			return serviceDenied("cosmos_private_configuration_changed")
		}
	}
	if isKustoType(planned.Identity.NativeType) {
		if expected := text(planned.Normalized["_kusto_private_configuration"]); expected == "" || expected != c.privateConfiguration(kustoSnapshot(planned.Identity.NativeType, live)) {
			return serviceDenied("kusto_private_configuration_changed")
		}
	}
	if isMongoClusterType(planned.Identity.NativeType) {
		if expected := text(planned.Normalized["_mongocluster_private_configuration"]); expected == "" || expected != c.privateConfiguration(mongoClusterSnapshot(planned.Identity.NativeType, live)) {
			return serviceDenied("mongocluster_private_configuration_changed")
		}
	}
	if isCognitiveType(planned.Identity.NativeType) {
		if expected := text(planned.Normalized["_cognitive_private_configuration"]); expected == "" || expected != c.privateConfiguration(cognitiveSnapshot(planned.Identity.NativeType, live)) {
			return serviceDenied("cognitive_private_configuration_changed")
		}
	}
	if isSearchType(planned.Identity.NativeType) {
		if expected := text(planned.Normalized["_search_private_configuration"]); expected == "" || expected != c.privateConfiguration(searchSnapshot(planned.Identity.NativeType, live)) {
			return serviceDenied("search_private_configuration_changed")
		}
	}
	if isRedisType(planned.Identity.NativeType) {
		if expected := text(planned.Normalized["_redis_private_configuration"]); expected == "" || expected != c.privateConfiguration(redisSnapshot(planned.Identity.NativeType, live)) {
			return serviceDenied("redis_private_configuration_changed")
		}
	}
	if isAppServiceType(planned.Identity.NativeType) {
		if expected := text(planned.Normalized["_app_service_private_configuration"]); expected == "" || expected != c.privateConfiguration(appServiceSnapshot(planned.Identity.NativeType, live)) {
			return serviceDenied("app_service_private_configuration_changed")
		}
	}
	if err := c.containerGroupPrivateIncarnation(planned, live); err != nil {
		return err
	}
	if isCDNType(planned.Identity.NativeType) {
		if expected := text(planned.Normalized["_cdn_private_configuration"]); expected == "" || expected != c.privateConfiguration(cdnSnapshot(planned.Identity.NativeType, live)) {
			return serviceDenied("cdn_private_configuration_changed")
		}
	}
	if isWAFType(planned.Identity.NativeType) {
		if expected := text(planned.Normalized["_waf_private_configuration"]); expected == "" || expected != c.privateConfiguration(wafSnapshot(planned.Identity.NativeType, live)) {
			return serviceDenied("waf_private_configuration_changed")
		}
	}
	return nil
}

func cdnDependencies(planned asset.Asset) []string {
	return stringValues(planned.Normalized["_cdn_dependencies"])
}

func stringValues(value any) []string {
	values := []string{}
	switch refs := value.(type) {
	case []string:
		values = slices.Clone(refs)
	case []any:
		for _, ref := range refs {
			values = append(values, text(ref))
		}
	}
	return values
}

func cdnPrerequisite(parent, referrer asset.Asset) bool {
	return isCDNType(parent.Identity.NativeType) && isCDNType(referrer.Identity.NativeType) &&
		cdnProfileID(parent.Identity.NativeID) == cdnProfileID(referrer.Identity.NativeID) &&
		slices.Contains(cdnDependencies(referrer), strings.ToLower(parent.Identity.NativeID))
}

// Microsoft's recorded CDN operations return error:{code:"None",message:null}
// on successful and pending polls. Preserve all other errors, including a failed
// status carrying that placeholder. This exception is specific to this API.
func (a *action) operationError(res response) error {
	if isStreamAnalyticsType(a.kind.NativeType) && res.status != 200 && res.status != 202 && res.status != 204 {
		return fmt.Errorf("incomplete Stream Analytics operation response")
	}
	if isKustoType(a.kind.NativeType) && res.status != 200 && res.status != 202 && res.status != 204 {
		return fmt.Errorf("incomplete Kusto operation response")
	}
	if isMongoClusterType(a.kind.NativeType) && res.status != 200 && res.status != 202 && res.status != 204 {
		return fmt.Errorf("incomplete DocumentDB operation response")
	}
	if isCosmosType(a.kind.NativeType) && res.status != 200 && res.status != 202 && res.status != 204 {
		return fmt.Errorf("incomplete Cosmos DB operation response")
	}
	if isCognitiveType(a.kind.NativeType) && res.status != 200 && res.status != 202 && res.status != 204 {
		return fmt.Errorf("incomplete Cognitive Services operation response")
	}
	if isSearchType(a.kind.NativeType) && res.status != 200 && res.status != 202 && res.status != 204 {
		return fmt.Errorf("incomplete Search operation response")
	}
	if isRedisType(a.kind.NativeType) && res.status != 200 && res.status != 202 && res.status != 204 {
		return fmt.Errorf("incomplete Redis operation response")
	}
	if isAppServiceType(a.kind.NativeType) && res.status != 200 && res.status != 202 && res.status != 204 {
		return fmt.Errorf("incomplete App Service operation response")
	}
	if isWAFType(a.kind.NativeType) && res.status != 200 && res.status != 202 && res.status != 204 {
		return fmt.Errorf("incomplete WAF operation response")
	}
	if isCDNType(a.kind.NativeType) {
		if res.status != 200 && res.status != 202 && res.status != 204 {
			return fmt.Errorf("incomplete CDN operation response")
		}
		native := object(res.data["error"])
		state := text(res.data["status"])
		if len(native) == 2 && native["code"] == "None" && native["message"] == nil && (state == "InProgress" || state == "Succeeded") {
			res.data = maps.Clone(res.data)
			delete(res.data, "error")
		}
	}
	return operationError(res)
}
