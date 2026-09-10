package azure

import (
	"context"
	"maps"
	"net/url"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const insightsSettingsProof = "_insights_settings_configuration"

// Fixed configuration endpoints have no independent DELETE. Their authored
// settings belong to the component plan; capabilities and quota are observations.
func (c *client) insightsConfigurationRead(ctx context.Context, parent, operation, path, version, selector string) (map[string]any, error) {
	id, kind, err := parseID(parent)
	if err != nil || id != parent || !strings.EqualFold(kind, applicationInsightsType) || !strings.HasPrefix(id, c.root()+"/") || selector != "" && !insightsLegacySelector(selector, insightsLegacyResource{}) {
		return nil, serviceDenied("invalid_insights_configuration_parent")
	}
	data, err := providerData()
	if err != nil {
		return nil, err
	}
	operationID := insightsOperationPrefix + operation
	if operation == "ComponentCurrentPricingPlan_Get" {
		operationID = "Azure.microsoft.insights." + operation // Published 2017 namespace is lowercase.
	}
	op, ok := data.catalog.Operation(operationID)
	if !ok || op.Call == nil || op.Call.Method != "GET" || op.Call.Version != version || op.Call.Style != "azure-rest" {
		return nil, serviceDenied("invalid_insights_configuration_binding")
	}
	parts := strings.Split(id, "/")
	params := map[string]any{"subscriptionId": c.subscription, "resourceGroupName": parts[4], "resourceName": parts[8]}
	if selector != "" {
		params["ConfigurationId"] = selector
		path += "/" + selector
	}
	request, err := bindAzureREST(op, params)
	if err != nil {
		return nil, err
	}
	validate := func(endpoint string) error {
		u, err := url.Parse(endpoint)
		if err != nil || u.Scheme != "https" || u.Host != "management.azure.com" || u.User != nil || u.Fragment != "" || !strings.EqualFold(u.Path, id+"/"+path) || len(u.Query()) != 1 || len(u.Query()["api-version"]) != 1 || u.Query().Get("api-version") != version {
			return serviceDenied("insights_configuration_endpoint_changed")
		}
		return nil
	}
	result, err := c.requestAt(ctx, "GET", request.URL, nil, nil, validate)
	if err != nil {
		return nil, err
	}
	if result.status != 200 || operationLocation(result.header) != "" || len(result.data) == 0 || result.data["error"] != nil || result.data["code"] != nil || result.data["nextLink"] != nil || result.data["NextLink"] != nil {
		return nil, serviceDenied("invalid_insights_configuration_response")
	}
	return result.data, nil
}

func insightsDetectionSnapshot(raw map[string]any) map[string]any {
	copy := maps.Clone(raw)
	delete(copy, "lastUpdatedTime")
	delete(copy, "ruleDefinitions") // Native schema: static rule definitions shared by all components.
	return copy
}

func insightsDetectionIdentity(raw map[string]any, name string) error {
	if !insightsLegacySelector(name, insightsLegacyResource{}) || insightsLegacyResponseIdentity(insightsLegacyResource{field: "name"}, name, raw) != nil {
		return serviceDenied("invalid_insights_detection_identity")
	}
	if _, ok := raw["enabled"].(bool); !ok {
		return serviceDenied("invalid_insights_detection_configuration")
	}
	if value, present := raw["sendEmailsToSubscriptionOwners"]; present {
		if _, ok := value.(bool); !ok {
			return serviceDenied("invalid_insights_detection_configuration")
		}
	}
	if value, present := raw["customEmails"]; present && value != nil {
		values, ok := value.([]any)
		if !ok {
			return serviceDenied("invalid_insights_detection_configuration")
		}
		for _, entry := range values {
			if _, ok := entry.(string); !ok {
				return serviceDenied("invalid_insights_detection_configuration")
			}
		}
	}
	return nil
}

func (c *client) insightsConfigurations(ctx context.Context, parent string, raw map[string]any, observations bool) (map[string]any, string, error) {
	values, authored := map[string]any{}, map[string]any{"component": parent}
	for _, row := range []struct{ key, operation, path, version string }{
		{"billing_features", "ComponentCurrentBillingFeatures_Get", "currentbillingfeatures", insightsLegacyVersion},
		{"pricing_plan", "ComponentCurrentPricingPlan_Get", "pricingPlans/current", "2017-10-01"},
		{"feature_capabilities", "ComponentFeatureCapabilities_Get", "featurecapabilities", insightsLegacyVersion},
		{"available_billing_features", "ComponentAvailableFeatures_Get", "getavailablebillingfeatures", insightsLegacyVersion},
		{"quota_status", "ComponentQuotaStatus_Get", "quotastatus", insightsLegacyVersion},
	} {
		if !observations && row.key != "billing_features" && row.key != "pricing_plan" {
			continue
		}
		value, err := c.insightsConfigurationRead(ctx, parent, row.operation, row.path, row.version, "")
		if err != nil {
			return nil, "", err
		}
		values[row.key] = value
		switch row.key {
		case "billing_features":
			features, ok := value["CurrentBillingFeatures"].([]any)
			cap, capOK := value["DataVolumeCap"].(map[string]any)
			if !ok || !capOK {
				return nil, "", serviceDenied("invalid_insights_billing_configuration")
			}
			for _, feature := range features {
				if name, ok := feature.(string); !ok || name == "" {
					return nil, "", serviceDenied("invalid_insights_billing_configuration")
				}
			}
			copy, capCopy := maps.Clone(value), maps.Clone(cap)
			delete(capCopy, "MaxHistoryCap")
			delete(capCopy, "ResetTime")
			copy["DataVolumeCap"] = capCopy
			authored[row.key] = copy
		case "pricing_plan":
			id, kind, err := parseID(text(value["id"]))
			properties, ok := value["properties"].(map[string]any)
			if err != nil || id != parent+"/pricingplans/current" || !strings.EqualFold(kind, applicationInsightsType+"/pricingPlans") || !strings.EqualFold(text(value["type"]), kind) || !strings.EqualFold(text(value["name"]), "current") || !ok {
				return nil, "", serviceDenied("invalid_insights_pricing_configuration")
			}
			copy := maps.Clone(properties)
			delete(copy, "maxHistoryCap")
			delete(copy, "resetHour")
			authored[row.key] = copy
		case "available_billing_features":
			if _, ok := value["Result"].([]any); !ok {
				return nil, "", serviceDenied("invalid_insights_available_features")
			}
		case "quota_status":
			if appID := text(value["AppId"]); appID == "" || appID != text(object(raw["properties"])["AppId"]) {
				return nil, "", serviceDenied("insights_quota_component_changed")
			}
		}
	}
	list, err := c.insightsConfigurationRead(ctx, parent, "ProactiveDetectionConfigurations_List", "ProactiveDetectionConfigs", insightsLegacyVersion, "")
	if err != nil {
		return nil, "", err
	}
	rows, ok := list["value"].([]any)
	if !ok || len(list) != 1 {
		return nil, "", serviceDenied("invalid_insights_detection_list")
	}
	detections, settings, seen := map[string]any{}, map[string]any{}, map[string]bool{}
	for _, row := range rows {
		listed, ok := row.(map[string]any)
		name := text(listed["name"])
		if !ok || insightsDetectionIdentity(listed, name) != nil || seen[strings.ToLower(name)] {
			return nil, "", serviceDenied("invalid_insights_detection_list_identity")
		}
		seen[strings.ToLower(name)] = true
		current, err := c.insightsConfigurationRead(ctx, parent, "ProactiveDetectionConfigurations_Get", "ProactiveDetectionConfigs", insightsLegacyVersion, name)
		if err != nil {
			return nil, "", err
		}
		if err := insightsDetectionIdentity(current, name); err != nil {
			return nil, "", err
		}
		if !nativeConfigurationContains(insightsDetectionSnapshot(listed), insightsDetectionSnapshot(current)) {
			return nil, "", serviceDenied("insights_detection_list_configuration_changed")
		}
		detections[name], settings[name] = current, insightsDetectionSnapshot(current)
	}
	values["proactive_detection"], authored["proactive_detection"] = detections, settings
	current, err := c.insightsComponent(ctx, parent)
	if err != nil {
		return nil, "", err
	}
	if c.privateConfiguration(monitorPrivateLinkTargetSnapshot(current)) != c.privateConfiguration(monitorPrivateLinkTargetSnapshot(raw)) {
		return nil, "", serviceDenied("insights_configuration_component_changed")
	}
	return values, c.privateConfiguration(authored), nil
}

func (c *client) insightsConfigurationInventory(ctx context.Context, item *contracts.InventoryItem, raw map[string]any) error {
	values, proof, err := c.insightsConfigurations(ctx, item.NativeID, raw, true)
	if err != nil {
		return err
	}
	maps.Copy(item.Normalized, safePayload(object(applicationInsightsSafeValue(values))))
	item.Normalized[insightsSettingsProof] = proof
	return nil
}
