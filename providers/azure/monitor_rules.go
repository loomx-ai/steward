package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	monitorMetricAlertType   = "Microsoft.Insights/metricAlerts"
	monitorActionGroupType   = "Microsoft.Insights/actionGroups"
	monitorActivityAlertType = "Microsoft.Insights/activityLogAlerts"
	monitorScheduledRuleType = "Microsoft.Insights/scheduledQueryRules"
	monitorSmartAlertType    = "Microsoft.AlertsManagement/smartDetectorAlertRules"
	monitorPrometheusType    = "Microsoft.AlertsManagement/prometheusRuleGroups"
	monitorProcessingType    = "Microsoft.AlertsManagement/actionRules"
	insightsWebTestType      = "Microsoft.Insights/webtests"
)

type monitorRuleResource struct{ kind, version, prefix, get, list, deletion string }

func monitorRuleKind(kind string) monitorRuleResource {
	for _, row := range []monitorRuleResource{
		{monitorMetricAlertType, "2026-01-01", "Azure.Microsoft.Insights.", "MetricAlerts_Get", "MetricAlerts_ListBySubscription", "MetricAlerts_Delete"},
		{monitorActionGroupType, "2023-01-01", "Azure.Microsoft.Insights.", "ActionGroups_Get", "ActionGroups_ListBySubscriptionId", "ActionGroups_Delete"},
		{monitorActivityAlertType, "2026-01-01", "Azure.Microsoft.Insights.", "ActivityLogAlerts_Get", "ActivityLogAlerts_ListBySubscriptionId", "ActivityLogAlerts_Delete"},
		{monitorScheduledRuleType, "2026-03-01", "Azure.Microsoft.Insights.", "ScheduledQueryRules_Get", "ScheduledQueryRules_ListBySubscription", "ScheduledQueryRules_Delete"},
		{monitorSmartAlertType, "2021-04-01", "Azure.microsoft.alertsManagement.", "SmartDetectorAlertRules_Get", "SmartDetectorAlertRules_List", "SmartDetectorAlertRules_Delete"},
		{monitorPrometheusType, "2023-03-01", "Azure.Microsoft.AlertsManagement.", "PrometheusRuleGroups_Get", "PrometheusRuleGroups_ListBySubscription", "PrometheusRuleGroups_Delete"},
		{monitorProcessingType, "2021-08-08", "Azure.Microsoft.AlertsManagement.", "AlertProcessingRules_GetByName", "AlertProcessingRules_ListBySubscription", "AlertProcessingRules_Delete"},
		{insightsWebTestType, "2022-06-15", "Azure.Microsoft.Insights.", "WebTests_Get", "WebTests_List", "WebTests_Delete"},
	} {
		if strings.EqualFold(row.kind, kind) {
			return row
		}
	}
	return monitorRuleResource{}
}

func (c *client) monitorRuleRequest(kind, id, method string) (catalog.RESTRequest, error) {
	row := monitorRuleKind(kind)
	if row.kind == "" || method != "GET" && method != "DELETE" {
		return catalog.RESTRequest{}, serviceDenied("invalid_monitor_rule_operation")
	}
	mapping := resourceType{NativeType: row.kind, ReadOperations: []string{row.prefix + row.get}, DeleteOperations: []string{row.prefix + row.deletion}}
	op, params, err := c.resourceOperation(mapping, id, method)
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	if op.Call == nil || op.Call.Version != row.version || op.Call.Method != method {
		return catalog.RESTRequest{}, serviceDenied("monitor_rule_operation_changed")
	}
	return bindAzureREST(op, params)
}

func monitorRuleIdentity(raw map[string]any, id, kind string) error {
	if err := monitorRuleFields(raw, "id", "type", "name", "properties", "location", "tags"); err != nil {
		return err
	}
	actual, typ, err := parseID(text(raw["id"]))
	if err != nil || actual != id || monitorRuleKind(typ).kind != kind || raw["name"] != nil && !strings.EqualFold(text(raw["name"]), last(id)) || text(raw["location"]) == "" || !validResponseType(kind, text(raw["type"])) {
		return serviceDenied("invalid_monitor_rule_identity")
	}
	if value := raw["type"]; value != nil {
		if _, ok := value.(string); !ok {
			return serviceDenied("invalid_monitor_rule_type")
		}
	}
	props, ok := raw["properties"].(map[string]any)
	if !ok {
		return serviceDenied("monitor_rule_properties_missing")
	}
	if tags := raw["tags"]; tags != nil {
		tags, ok := tags.(map[string]any)
		if !ok {
			return serviceDenied("invalid_monitor_rule_tags")
		}
		for _, value := range tags {
			if _, ok := value.(string); !ok {
				return serviceDenied("invalid_monitor_rule_tags")
			}
		}
	}
	stringsRequired, arraysRequired, objectsRequired := []string{}, []string{}, []string{}
	switch kind {
	case monitorMetricAlertType:
		stringsRequired, arraysRequired, objectsRequired = []string{"evaluationFrequency"}, []string{"scopes"}, []string{"criteria"}
		severity, err := batchInteger(props["severity"], 8)
		if _, ok := props["enabled"].(bool); !ok || err != nil || severity < 0 || severity > 4 || text(object(props["criteria"])["odata.type"]) == "" {
			return serviceDenied("metric_alert_configuration_missing")
		}
	case monitorActionGroupType:
		stringsRequired = []string{"groupShortName"}
		if _, ok := props["enabled"].(bool); !ok {
			return serviceDenied("action_group_configuration_missing")
		}
		for field, value := range props {
			if strings.HasSuffix(field, "Receivers") {
				if _, ok := value.([]any); !ok {
					return serviceDenied("action_group_receivers_missing")
				}
				for _, receiver := range array(value) {
					if _, ok := receiver.(map[string]any); !ok {
						return serviceDenied("invalid_action_group_receiver")
					}
				}
			}
		}
	case monitorActivityAlertType:
		objectsRequired = []string{"condition", "actions"}
	case monitorScheduledRuleType:
		arraysRequired, objectsRequired = []string{"scopes"}, []string{"criteria"}
	case monitorSmartAlertType:
		stringsRequired, arraysRequired, objectsRequired = []string{"state", "severity", "frequency"}, []string{"scope"}, []string{"detector"}
		if text(object(props["detector"])["id"]) == "" || props["actionGroups"] == nil {
			return serviceDenied("smart_alert_configuration_missing")
		}
	case monitorPrometheusType:
		arraysRequired = []string{"scopes", "rules"}
		for _, rule := range array(props["rules"]) {
			if text(object(rule)["expression"]) == "" {
				return serviceDenied("prometheus_expression_missing")
			}
		}
	case monitorProcessingType:
		arraysRequired = []string{"scopes", "actions"}
	case insightsWebTestType:
		stringsRequired, arraysRequired = []string{"SyntheticMonitorId", "Name", "Kind"}, []string{"Locations"}
		// The recorded native LIST has kind=ping and properties.Kind=standard.
		// Bind each value separately; equality is not the native contract.
		if !slices.Contains([]string{"ping", "multistep", "standard"}, text(props["Kind"])) || raw["kind"] != nil && !slices.Contains([]string{"ping", "multistep", "standard"}, text(raw["kind"])) {
			return serviceDenied("invalid_web_test_kind")
		}
	}
	for _, key := range stringsRequired {
		if text(props[key]) == "" {
			return serviceDenied("monitor_rule_configuration_missing")
		}
	}
	for _, key := range arraysRequired {
		if _, ok := props[key].([]any); !ok {
			return serviceDenied("monitor_rule_configuration_missing")
		}
	}
	for _, key := range objectsRequired {
		if _, ok := props[key].(map[string]any); !ok {
			return serviceDenied("monitor_rule_configuration_missing")
		}
	}
	_, err = monitorRuleScopes(kind, raw)
	if err != nil {
		return err
	}
	_, err = monitorRuleActionGroups(kind, raw)
	return err
}

// A broad alert scope is not a fabricated ARM resource. Preserve subscription
// scopes alongside group/resource IDs so later reference matching can evaluate
// their scope explicitly, without guessing resource ownership.
func monitorRuleScopes(kind string, raw map[string]any) ([]string, error) {
	props := object(raw["properties"])
	field := "scopes"
	if kind == monitorSmartAlertType {
		field = "scope"
	}
	if err := monitorRuleFields(props, field); err != nil {
		return nil, err
	}
	value, present := props[field]
	if !present {
		return nil, nil
	}
	values, ok := value.([]any)
	if !ok {
		return nil, serviceDenied("invalid_monitor_rule_scopes")
	}
	seen := map[string]bool{}
	var scopes []string
	for _, value := range values {
		scope, ok := value.(string)
		if !ok || scope != strings.TrimSpace(scope) {
			return nil, serviceDenied("invalid_monitor_rule_scope")
		}
		// Activity Log's original examples use subscription prefixes without
		// a leading slash. This is a scope representation, not a resource ID.
		if kind == monitorActivityAlertType && strings.HasPrefix(strings.ToLower(scope), "subscriptions/") {
			scope = "/" + scope
		}
		parts := strings.Split(strings.ToLower(scope), "/")
		if len(parts) == 3 && parts[0] == "" && parts[1] == "subscriptions" && uuidPattern.MatchString(parts[2]) {
			scope = strings.ToLower(scope)
		} else {
			var err error
			scope, _, err = parseID(scope)
			if err != nil {
				return nil, serviceDenied("invalid_monitor_rule_scope")
			}
		}
		if seen[scope] {
			return nil, serviceDenied("duplicate_monitor_rule_scope")
		}
		seen[scope] = true
		scopes = append(scopes, scope)
	}
	slices.Sort(scopes)
	return scopes, nil
}

// Explicit native action-group slots avoid interpreting an ARM-looking string
// in a query, webhook payload, condition or receiver as a notification reference.
func monitorRuleActionGroups(kind string, raw map[string]any) ([]string, error) {
	props := object(raw["properties"])
	if err := monitorRuleFields(props, "actions", "actionGroups", "rules"); err != nil {
		return nil, err
	}
	var values []any
	list := func(value any, objects bool) error {
		if value == nil {
			return nil
		}
		items, ok := value.([]any)
		if !ok {
			return serviceDenied("invalid_monitor_rule_action_groups")
		}
		for _, item := range items {
			if objects {
				if err := monitorRuleFields(object(item), "actionGroupId"); err != nil {
					return err
				}
				item = object(item)["actionGroupId"]
			}
			values = append(values, item)
		}
		return nil
	}
	var err error
	switch kind {
	case monitorMetricAlertType:
		err = list(props["actions"], true)
	case monitorActivityAlertType:
		if err := monitorRuleFields(object(props["actions"]), "actionGroups"); err != nil {
			return nil, err
		}
		err = list(object(props["actions"])["actionGroups"], true)
	case monitorScheduledRuleType:
		if actions := props["actions"]; actions != nil {
			if _, ok := actions.(map[string]any); !ok {
				return nil, serviceDenied("invalid_scheduled_rule_actions")
			}
		}
		if err := monitorRuleFields(object(props["actions"]), "actionGroups"); err != nil {
			return nil, err
		}
		err = list(object(props["actions"])["actionGroups"], false)
	case monitorSmartAlertType:
		if groups, ok := props["actionGroups"].(map[string]any); ok {
			if err := monitorRuleFields(groups, "groupIds"); err != nil {
				return nil, err
			}
			if groups["groupIds"] == nil {
				return nil, serviceDenied("smart_alert_action_groups_missing")
			}
			err = list(groups["groupIds"], false)
		} else {
			err = list(props["actionGroups"], true)
		}
	case monitorPrometheusType:
		for _, value := range array(props["rules"]) {
			if err := monitorRuleFields(object(value), "actions"); err != nil {
				return nil, err
			}
			if err = list(object(value)["actions"], true); err != nil {
				break
			}
		}
	case monitorProcessingType:
		for _, value := range array(props["actions"]) {
			action := object(value)
			if err := monitorRuleFields(action, "actionType", "actionGroupIds"); err != nil {
				return nil, err
			}
			switch text(action["actionType"]) {
			case "AddActionGroups":
				if action["actionGroupIds"] == nil {
					return nil, serviceDenied("processing_rule_action_groups_missing")
				}
				err = list(action["actionGroupIds"], false)
			case "RemoveAllActionGroups":
				if action["actionGroupIds"] != nil {
					return nil, serviceDenied("ambiguous_processing_rule_action")
				}
			default:
				return nil, serviceDenied("unknown_processing_rule_action")
			}
			if err != nil {
				break
			}
		}
	}
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var ids []string
	for _, value := range values {
		wire, ok := value.(string)
		id, typ, err := parseID(wire)
		if !ok || wire != strings.TrimSpace(wire) || err != nil || !strings.EqualFold(typ, monitorActionGroupType) {
			return nil, serviceDenied("invalid_monitor_rule_action_group_identity")
		}
		// A Prometheus group can use the same action group in multiple rules.
		if !seen[id] {
			ids = append(ids, id)
			seen[id] = true
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func monitorRuleFields(raw map[string]any, fields ...string) error {
	for key := range raw {
		for _, field := range fields {
			if key != field && strings.EqualFold(key, field) {
				return serviceDenied("ambiguous_monitor_rule_field")
			}
		}
	}
	return nil
}

func monitorRuleSnapshot(kind string, raw map[string]any) map[string]any {
	copy := maps.Clone(raw)
	id, _, _ := parseID(text(raw["id"]))
	copy["id"], copy["name"], copy["type"] = id, last(id), kind
	props := maps.Clone(object(raw["properties"]))
	copy["properties"] = props
	switch kind {
	case monitorMetricAlertType:
		delete(props, "lastUpdatedTime")
		delete(props, "isMigrated")
	case monitorScheduledRuleType:
		delete(props, "createdWithApiVersion")
		delete(props, "isLegacyLogAnalyticsRule")
		delete(props, "isWorkspaceAlertsStorageConfigured")
	case monitorSmartAlertType:
		detector := maps.Clone(object(props["detector"]))
		for _, field := range []string{"name", "description", "supportedResourceTypes", "imagePaths", "parameterDefinitions", "supportedCadences"} {
			delete(detector, field)
		}
		props["detector"] = detector
	case monitorActionGroupType:
		for _, field := range []string{"emailReceivers", "smsReceivers"} {
			if props[field] == nil {
				continue
			}
			var receivers []any
			for _, value := range array(props[field]) {
				receiver := maps.Clone(object(value))
				delete(receiver, "status")
				receivers = append(receivers, receiver)
			}
			props[field] = receivers
		}
	case insightsWebTestType:
		delete(props, "provisioningState")
	}
	return copy
}

func (c *client) monitorRuleRead(ctx context.Context, kind, id string) (response, error) {
	request, err := c.monitorRuleRequest(kind, id, "GET")
	if err != nil {
		return response{}, err
	}
	result, err := c.request(ctx, "GET", request.URL)
	if err != nil {
		return result, err
	}
	if result.status != 200 || result.data["code"] != nil || result.data["error"] != nil || operationLocation(result.header) != "" || result.data["nextLink"] != nil || result.data["NextLink"] != nil {
		return result, serviceDenied("invalid_monitor_rule_get_response")
	}
	if kind == insightsWebTestType {
		props := object(result.data["properties"])
		if text(object(props["Configuration"])["WebTest"]) == "" && text(object(props["Request"])["RequestUrl"]) == "" {
			return result, serviceDenied("web_test_content_missing")
		}
	}
	return result, monitorRuleIdentity(result.data, id, kind)
}

func (c *client) monitorRuleIndex(ctx context.Context, kind string) (items map[string]map[string]any, requestID string, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	row := monitorRuleKind(kind)
	metadata, err := providerData()
	if err != nil {
		return nil, "", err
	}
	op, ok := metadata.catalog.Operation(row.prefix + row.list)
	if !ok || row.kind == "" || op.Call == nil || op.Call.Method != "GET" || op.Call.Version != row.version {
		return nil, "", serviceDenied("invalid_monitor_rule_list_operation")
	}
	request, err := bindAzureREST(op, map[string]any{"subscriptionId": c.subscription})
	if err != nil {
		return nil, "", err
	}
	initial, _ := url.Parse(request.URL)
	seen := map[string]bool{}
	values, provenance := map[string]map[string]any{}, ""
	for endpoint := request.URL; endpoint != ""; {
		u, err := url.Parse(endpoint)
		if err != nil || seen[endpoint] || strings.Contains(endpoint, "#") {
			return nil, "", serviceDenied("invalid_monitor_rule_page")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || !slices.Equal(query["api-version"], initial.Query()["api-version"]) || len(query["$skiptoken"])+len(query["skiptoken"])+len(query["ctoken"]) > 1 {
			return nil, "", serviceDenied("invalid_monitor_rule_page_query")
		}
		for key, entries := range query {
			if key != "api-version" && key != "skiptoken" && key != "$skiptoken" && (kind != monitorProcessingType || key != "ctoken") || len(entries) != 1 || entries[0] == "" {
				return nil, "", serviceDenied("filtered_monitor_rule_page")
			}
		}
		seen[endpoint] = true
		items, next, result, err := c.listPageResult(ctx, endpoint, initial.Path)
		if err != nil {
			return nil, "", err
		}
		if operationLocation(result.header) != "" || result.data["code"] != nil {
			return nil, "", serviceDenied("invalid_monitor_rule_list_response")
		}
		if err := monitorRuleFields(result.data, "value", "nextLink"); err != nil {
			return nil, "", err
		}
		for _, value := range items {
			raw := object(value)
			id, typ, err := parseID(text(raw["id"]))
			if err != nil || monitorRuleKind(typ).kind != kind || !strings.HasPrefix(id, c.root()+"/") || values[id] != nil || monitorRuleIdentity(raw, id, kind) != nil {
				return nil, "", serviceDenied("invalid_monitor_rule_list_identity")
			}
			current, err := c.monitorRuleRead(ctx, kind, id)
			if err != nil {
				return nil, "", err
			}
			// These native lists return the same resource schema as GET, not
			// metadata-only summaries. Newly added configuration matters too.
			if c.privateConfiguration(monitorRuleSnapshot(kind, raw)) != c.privateConfiguration(monitorRuleSnapshot(kind, current.data)) {
				return nil, "", serviceDenied("monitor_rule_list_configuration_changed")
			}
			values[id] = current.data
		}
		if result.requestID != "" {
			provenance = result.requestID
		}
		endpoint = next
	}
	return values, provenance, nil
}

// Original Alert Processing Rule LIST examples use HTTPS's explicit default
// port and ctoken. Normalize only this native continuation family; the shared
// transport still binds its subscription, collection and API version.
func monitorRuleNextLink(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err == nil && u.Scheme == "https" && u.Host == "management.azure.com:443" && u.User == nil && u.Query().Get("api-version") == "2021-08-08" && strings.HasSuffix(strings.ToLower(u.Path), "/providers/microsoft.alertsmanagement/actionrules") {
		u.Host = "management.azure.com"
		return u.String()
	}
	return endpoint
}
