package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	monitorConsumptionBudgetType = "Microsoft.Consumption/budgets"
	monitorCostBudgetType        = "Microsoft.CostManagement/budgets"
)

func monitorBudgetKind(kind string) (string, string) {
	for _, row := range [][2]string{{monitorConsumptionBudgetType, "2024-08-01"}, {monitorCostBudgetType, "2025-03-01"}} {
		if strings.EqualFold(row[0], kind) {
			return row[0], row[1]
		}
	}
	return "", ""
}

// Budget scopes include subscription roots, which are not ordinary group-scoped
// ARM asset IDs. Consumption's original responses omit the leading slash.
func monitorBudgetID(wire string) (id, scope, kind string, err error) {
	if wire != strings.TrimSpace(wire) || strings.ContainsAny(wire, "%?#\\\x00\r\n") {
		return "", "", "", serviceDenied("invalid_monitor_budget_identity")
	}
	id = strings.ToLower(wire)
	bare := !strings.HasPrefix(id, "/")
	parts := strings.Split(strings.TrimPrefix(id, "/"), "/")
	if len(parts) != 6 && len(parts) != 8 || parts[0] != "subscriptions" || !uuidPattern.MatchString(parts[1]) {
		return "", "", "", serviceDenied("invalid_monitor_budget_scope")
	}
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", "", "", serviceDenied("invalid_monitor_budget_path")
		}
	}
	index := len(parts) - 4
	if index == 4 && parts[2] != "resourcegroups" || parts[index] != "providers" || parts[index+2] != "budgets" {
		return "", "", "", serviceDenied("invalid_monitor_budget_path")
	}
	kind, _ = monitorBudgetKind(parts[index+1] + "/budgets")
	if kind == "" || bare && kind != monitorConsumptionBudgetType {
		return "", "", "", serviceDenied("invalid_monitor_budget_type")
	}
	return "/" + strings.Join(parts, "/"), "/" + strings.Join(parts[:index], "/"), kind, nil
}

func (c *client) monitorBudgetRequest(kind, scope, name, method string) (catalog.RESTRequest, error) {
	canonical, version := monitorBudgetKind(kind)
	if canonical == "" || method != "GET" && method != "DELETE" || method == "DELETE" && name == "" {
		return catalog.RESTRequest{}, serviceDenied("invalid_monitor_budget_operation")
	}
	selector := name
	if selector == "" {
		selector = "index"
	}
	_, actualScope, actualKind, err := monitorBudgetID(scope + "/providers/" + canonical + "/" + selector)
	if err != nil || actualScope != scope || actualKind != canonical || scope != c.root() && !strings.HasPrefix(scope, c.root()+"/resourcegroups/") {
		return catalog.RESTRequest{}, serviceDenied("monitor_budget_subscription_changed")
	}
	operation := "Budgets_Get"
	params := map[string]any{"scope": strings.TrimPrefix(scope, "/"), "budgetName": name}
	if name == "" {
		operation = "Budgets_List"
		delete(params, "budgetName")
	} else if method == "DELETE" {
		operation = "Budgets_Delete"
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	op, ok := metadata.catalog.Operation("Azure." + strings.Split(canonical, "/")[0] + "." + operation)
	if !ok || op.Call == nil || op.Call.Method != method || op.Call.Version != version {
		return catalog.RESTRequest{}, serviceDenied("monitor_budget_operation_changed")
	}
	return bindAzureREST(op, params)
}

func monitorBudgetActionGroups(raw map[string]any) ([]string, error) {
	props, ok := raw["properties"].(map[string]any)
	if !ok {
		return nil, serviceDenied("monitor_budget_properties_missing")
	}
	if err := monitorRuleFields(props, "notifications"); err != nil {
		return nil, err
	}
	value, present := props["notifications"]
	if !present {
		return nil, nil
	}
	notifications, ok := value.(map[string]any)
	if !ok {
		return nil, serviceDenied("invalid_monitor_budget_notifications")
	}
	seen := map[string]bool{}
	var groups []string
	for _, value := range notifications {
		notification, ok := value.(map[string]any)
		if !ok {
			return nil, serviceDenied("invalid_monitor_budget_notification")
		}
		if err := monitorRuleFields(notification, "contactGroups"); err != nil {
			return nil, err
		}
		value, present := notification["contactGroups"]
		if !present {
			continue
		}
		values, ok := value.([]any)
		if !ok {
			return nil, serviceDenied("invalid_monitor_budget_contact_groups")
		}
		for _, value := range values {
			wire, ok := value.(string)
			id, kind, err := parseID(wire)
			if !ok || wire != strings.TrimSpace(wire) || err != nil || !strings.EqualFold(kind, monitorActionGroupType) {
				return nil, serviceDenied("invalid_monitor_budget_action_group")
			}
			if !seen[id] {
				seen[id] = true
				groups = append(groups, id)
			}
		}
	}
	slices.Sort(groups)
	return groups, nil
}

func monitorBudgetIdentity(raw map[string]any, expected, kind string) error {
	if err := monitorRuleFields(raw, "id", "name", "type", "properties", "eTag"); err != nil {
		return err
	}
	wire, valid := raw["id"].(string)
	id, _, actualKind, err := monitorBudgetID(wire)
	if !valid || err != nil || id != expected || actualKind != kind || !strings.EqualFold(text(raw["name"]), last(id)) || !validResponseType(kind, text(raw["type"])) {
		return serviceDenied("monitor_budget_identity_changed")
	}
	if value := raw["type"]; value != nil {
		if _, ok := value.(string); !ok {
			return serviceDenied("invalid_monitor_budget_type")
		}
	}
	props, ok := raw["properties"].(map[string]any)
	if !ok || text(props["category"]) == "" || text(props["timeGrain"]) == "" || text(object(props["timePeriod"])["startDate"]) == "" {
		return serviceDenied("monitor_budget_configuration_missing")
	}
	number := func(value any) bool {
		switch value.(type) {
		case json.Number, float64, int, int64:
			return true
		}
		return false
	}
	if props["category"] == "Cost" || kind == monitorConsumptionBudgetType {
		if !number(props["amount"]) {
			return serviceDenied("monitor_budget_amount_missing")
		}
	}
	for _, value := range object(props["notifications"]) {
		notification := object(value)
		_, enabled := notification["enabled"].(bool)
		emails, validEmails := notification["contactEmails"].([]any)
		if !enabled || !validEmails || text(notification["operator"]) == "" || !number(notification["threshold"]) {
			return serviceDenied("monitor_budget_notification_configuration_missing")
		}
		for _, email := range emails {
			if _, ok := email.(string); !ok {
				return serviceDenied("invalid_monitor_budget_contact_email")
			}
		}
	}
	_, err = monitorBudgetActionGroups(raw)
	return err
}

func monitorBudgetSnapshot(raw map[string]any) map[string]any {
	copy := maps.Clone(raw)
	id, _, kind, _ := monitorBudgetID(text(raw["id"]))
	copy["id"], copy["name"], copy["type"] = id, last(id), kind
	props := maps.Clone(object(raw["properties"]))
	delete(props, "currentSpend")
	delete(props, "forecastSpend")
	copy["properties"] = props
	return copy
}

func (c *client) monitorBudgetRead(ctx context.Context, id string) (response, error) {
	canonical, scope, kind, err := monitorBudgetID(id)
	if err != nil || canonical != id {
		return response{}, serviceDenied("invalid_monitor_budget_read_identity")
	}
	request, err := c.monitorBudgetRequest(kind, scope, last(id), "GET")
	if err != nil {
		return response{}, err
	}
	result, err := c.request(ctx, "GET", request.URL)
	if err != nil {
		return result, err
	}
	if result.status != 200 || result.data["code"] != nil || result.data["error"] != nil || result.data["nextLink"] != nil || result.data["NextLink"] != nil || operationLocation(result.header) != "" {
		return result, serviceDenied("invalid_monitor_budget_get_response")
	}
	return result, monitorBudgetIdentity(result.data, id, kind)
}

// Subscription lists may include group budgets. Group lists must not escape
// their exact group. A listed resource's failed GET cannot prove absence.
func (c *client) monitorBudgetIndex(ctx context.Context, kind, scope string) (values map[string]map[string]any, requestID string, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	kind, _ = monitorBudgetKind(kind)
	request, err := c.monitorBudgetRequest(kind, scope, "", "GET")
	if err != nil {
		return nil, "", err
	}
	initial, _ := url.Parse(request.URL)
	values, seen := map[string]map[string]any{}, map[string]bool{}
	for endpoint := request.URL; endpoint != ""; {
		u, err := url.Parse(endpoint)
		if err != nil || seen[endpoint] || strings.Contains(endpoint, "#") {
			return nil, "", serviceDenied("invalid_monitor_budget_page")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || !slices.Equal(query["api-version"], initial.Query()["api-version"]) || len(query["skiptoken"])+len(query["$skiptoken"]) > 1 {
			return nil, "", serviceDenied("invalid_monitor_budget_page_query")
		}
		for key, entries := range query {
			if key != "api-version" && key != "skiptoken" && key != "$skiptoken" || len(entries) != 1 || entries[0] == "" {
				return nil, "", serviceDenied("filtered_monitor_budget_page")
			}
		}
		seen[endpoint] = true
		rows, next, result, err := c.listPageResult(ctx, endpoint, initial.Path)
		if err != nil {
			return nil, "", err
		}
		if operationLocation(result.header) != "" || result.data["code"] != nil {
			return nil, "", serviceDenied("invalid_monitor_budget_list_response")
		}
		if err := monitorRuleFields(result.data, "value", "nextLink"); err != nil {
			return nil, "", err
		}
		for _, value := range rows {
			raw := object(value)
			id, actualScope, actualKind, err := monitorBudgetID(text(raw["id"]))
			if err != nil || actualKind != kind || !strings.HasPrefix(id, c.root()+"/") || scope != c.root() && actualScope != scope || values[id] != nil || monitorBudgetIdentity(raw, id, kind) != nil {
				return nil, "", serviceDenied("invalid_monitor_budget_list_identity")
			}
			current, err := c.monitorBudgetRead(ctx, id)
			if err != nil {
				return nil, "", err
			}
			if c.privateConfiguration(monitorBudgetSnapshot(raw)) != c.privateConfiguration(monitorBudgetSnapshot(current.data)) {
				return nil, "", serviceDenied("monitor_budget_list_configuration_changed")
			}
			values[id] = current.data
		}
		if result.requestID != "" {
			requestID = result.requestID
		}
		endpoint = next
	}
	return values, requestID, nil
}
