package gcp

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const alertPolicyType = "monitoring.googleapis.com/AlertPolicy"
const alertPolicyReview = "_alert_policy_configuration"

func isMonitoringConfig(kind string) bool {
	return kind == uptimeType || kind == alertPolicyType || kind == notificationChannelType || kind == monitoringGroupType || kind == monitoringDashboardType
}
func monitoringReviewKey(kind string) string {
	if kind == monitoringDashboardType {
		return monitoringDashboardReview
	}
	if kind == monitoringGroupType {
		return monitoringGroupReview
	}
	if kind == notificationChannelType {
		return notificationChannelReview
	}
	if kind == alertPolicyType {
		return alertPolicyReview
	}
	return uptimeReview
}
func (c *client) monitoringConfiguration(kind, id string, data map[string]any) string {
	if kind == monitoringGroupType {
		return c.monitoringGroupConfiguration(id, data)
	}
	return monitoringConfiguration(kind, id, data)
}

func monitoringConfiguration(kind, id string, data map[string]any) string {
	if kind == monitoringDashboardType {
		return monitoringDashboardConfiguration(id, data)
	}
	if kind == notificationChannelType {
		return notificationChannelConfiguration(id, data)
	}
	if kind != alertPolicyType {
		return uptimeConfiguration(id, data)
	}
	value := cloneParameters(data)
	value["name"] = id
	delete(value, "validity") // Output-only runtime diagnostic, not policy configuration.
	conditions := []any{}
	for _, raw := range array(data["conditions"]) {
		condition := cloneParameters(object(raw))
		condition["name"] = id + "/conditions/" + last(text(condition["name"]))
		conditions = append(conditions, condition)
	}
	value["conditions"] = conditions
	return firewallDigest(value)
}

func (c *client) alertPolicyData(id string, data map[string]any) error {
	if err := checkListCompleteness(data); err != nil {
		return err
	}
	name := text(data["name"])
	if data["name"] != name || !strings.HasPrefix(name, "projects/") || c.canonicalName("//monitoring.googleapis.com/"+name) != id {
		return groupDenied("alert_policy_identity_changed")
	}
	kind, _ := findType(alertPolicyType)
	if _, err := c.resourceURL(kind, id); err != nil {
		return err
	}
	if err := cloudNatScalars(data, []string{"name", "displayName", "combiner", "severity"}, []string{"enabled"}, nil, []string{"notificationChannels"}); err != nil {
		return err
	}
	if _, ok := data["enabled"].(bool); !ok {
		return groupDenied("alert_policy_enabled_missing")
	}
	if text(data["displayName"]) == "" || text(data["combiner"]) == "" {
		return groupDenied("alert_policy_configuration_invalid")
	}
	if err := uptimeStringMap(data, "userLabels"); err != nil {
		return err
	}
	for _, key := range []string{"documentation", "creationRecord", "mutationRecord", "validity", "alertStrategy"} {
		if raw, ok := data[key]; ok && object(raw) == nil {
			return groupDenied("alert_policy_object_invalid")
		}
	}
	for _, key := range []string{"creationRecord", "mutationRecord"} {
		if err := cloudNatScalars(object(data[key]), []string{"mutateTime", "mutatedBy"}, nil, nil, nil); err != nil {
			return err
		}
	}
	if _, err := c.alertPolicyChannels(data); err != nil {
		return err
	}
	conditions, err := cloudNatObjects(data, "conditions")
	if err != nil {
		return err
	}
	if len(conditions) < 1 || len(conditions) > 6 {
		return groupDenied("alert_policy_conditions_invalid")
	}
	seen := map[string]bool{}
	for _, condition := range conditions {
		name, _ := condition["name"].(string)
		native := c.canonicalName("//monitoring.googleapis.com/" + name)
		if !strings.HasPrefix(native, id+"/conditions/") || !uptimeSegment(strings.TrimPrefix(native, id+"/conditions/")) || name != text(name) || seen[native] {
			return groupDenied("alert_policy_condition_identity_invalid")
		}
		seen[native] = true
		if err := cloudNatScalars(condition, []string{"name", "displayName"}, nil, nil, nil); err != nil {
			return err
		}
		count := 0
		for _, key := range []string{"conditionThreshold", "conditionAbsent", "conditionMatchedLog", "conditionMonitoringQueryLanguage", "conditionPrometheusQueryLanguage", "conditionSql"} {
			raw, present := condition[key]
			if !present {
				continue
			}
			count++
			body := object(raw)
			if body == nil {
				return groupDenied("alert_policy_condition_invalid")
			}
			if err := cloudNatScalars(body, []string{"filter", "denominatorFilter", "query", "duration", "comparison", "evaluationMissingData", "evaluationInterval", "alertRule", "ruleGroup"}, []string{"disableMetricValidation"}, nil, nil); err != nil {
				return err
			}
			if key == "conditionThreshold" || key == "conditionAbsent" || key == "conditionMatchedLog" {
				if text(body["filter"]) == "" {
					return groupDenied("alert_policy_filter_missing")
				}
			} else if text(body["query"]) == "" {
				return groupDenied("alert_policy_query_missing")
			}
			for _, field := range []string{"aggregations", "denominatorAggregations"} {
				if _, err := cloudNatObjects(body, field); err != nil {
					return err
				}
			}
			for _, field := range []string{"trigger", "forecastOptions", "booleanTest", "daily", "hourly", "minutes", "rowCountTest"} {
				if raw, present := body[field]; present && object(raw) == nil {
					return groupDenied("alert_policy_condition_invalid")
				}
			}
			for _, field := range []string{"labels", "labelExtractors"} {
				if err := uptimeStringMap(body, field); err != nil {
					return err
				}
			}
		}
		if count != 1 {
			return groupDenied("alert_policy_condition_union_invalid")
		}
	}
	return nil
}

func (c *client) monitoringRead(ctx context.Context, kind, id string) (map[string]any, error) {
	if kind == monitoringDashboardType {
		return c.monitoringDashboardRead(ctx, id)
	}
	if kind == monitoringGroupType {
		return c.monitoringGroupRead(ctx, id)
	}
	if kind == notificationChannelType {
		return c.notificationChannelRead(ctx, id)
	}
	if kind == uptimeType {
		return c.uptimeRead(ctx, id)
	}
	spec, _ := findType(kind)
	endpoint, err := c.resourceURL(spec, id)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	if err := c.alertPolicyData(id, data); err != nil {
		return nil, err
	}
	return data, nil
}
func (c *client) alertPolicyInventory(ctx context.Context, id string, listed map[string]any) (map[string]any, error) {
	if err := c.alertPolicyData(id, listed); err != nil {
		return nil, err
	}
	live, err := c.monitoringRead(ctx, alertPolicyType, id)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if monitoringConfiguration(alertPolicyType, id, listed) != monitoringConfiguration(alertPolicyType, id, live) {
		return nil, groupDenied("alert_policy_configuration_changed")
	}
	return live, nil
}

func redactAlertPolicyPayload(data map[string]any) {
	if !strings.Contains(text(data["name"]), "/alertPolicies/") {
		return
	}
	// Invalid-policy diagnostics can repeat private expressions. Keep only status.
	if raw, present := data["validity"]; present {
		data["validity"] = map[string]any{"code": object(raw)["code"]}
	}
	if _, present := data["documentation"]; present {
		data["documentation"] = "[REDACTED]"
	}
	for _, raw := range array(data["conditions"]) {
		for key, raw := range object(raw) {
			if !strings.HasPrefix(key, "condition") {
				continue
			}
			body := object(raw)
			for _, field := range []string{"filter", "denominatorFilter", "query", "labelExtractors", "labels"} {
				if _, present := body[field]; present {
					body[field] = "[REDACTED]"
				}
			}
		}
	}
}
