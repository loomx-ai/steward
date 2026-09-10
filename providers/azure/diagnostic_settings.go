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

const diagnosticSettingsType = "Microsoft.Insights/diagnosticSettings"
const diagnosticCategoryType = "Microsoft.Insights/diagnosticSettingsCategories"
const diagnosticSettingsVersion = "2021-05-01-preview"

// These native operations cover subscription and resource scopes. Management
// group settings use a separate API and cannot borrow subscription authority.
func diagnosticScope(wire string) (string, error) {
	if wire != strings.TrimSpace(wire) || strings.ContainsAny(wire, "%?#\\\x00\r\n\t") {
		return "", serviceDenied("invalid_diagnostic_scope")
	}
	id := strings.ToLower(wire)
	parts := strings.Split(id, "/")
	if len(parts) == 3 && parts[0] == "" && parts[1] == "subscriptions" && uuidPattern.MatchString(parts[2]) {
		return id, nil
	}
	id, _, err := parseID(wire)
	if err != nil || strings.Contains(id, "/providers/microsoft.insights/diagnosticsettings") {
		return "", serviceDenied("invalid_diagnostic_resource_scope")
	}
	return id, nil
}

func diagnosticResourceID(wire string) (id, scope, kind string, err error) {
	if wire != strings.TrimSpace(wire) || strings.ContainsAny(wire, "%?#\\\x00\r\n\t") {
		return "", "", "", serviceDenied("invalid_diagnostic_identity")
	}
	id = strings.ToLower(wire)
	parts := strings.Split(strings.TrimPrefix(id, "/"), "/")
	// Only the subscription API's original AzureResourceManager alias may
	// omit its leading slash. Resource-scoped malformed examples stay invalid.
	alias := len(parts) == 6 && parts[2] == "providers" && parts[3] == "azureresourcemanager" && parts[4] == "diagnosticsettings"
	if alias {
		parts[3] = "microsoft.insights"
		id = "/" + strings.Join(parts, "/")
	}
	if !strings.HasPrefix(id, "/") || len(parts) < 6 {
		return "", "", "", serviceDenied("invalid_diagnostic_identity")
	}
	i := len(parts) - 4
	if parts[i] != "providers" || parts[i+1] != "microsoft.insights" || !monitorReceiverName(parts[i+3]) {
		return "", "", "", serviceDenied("invalid_diagnostic_resource_type")
	}
	switch parts[i+2] {
	case "diagnosticsettings":
		kind = diagnosticSettingsType
	case "diagnosticsettingscategories":
		kind = diagnosticCategoryType
	default:
		return "", "", "", serviceDenied("invalid_diagnostic_resource_type")
	}
	scope, err = diagnosticScope("/" + strings.Join(parts[:i], "/"))
	if err != nil || kind == diagnosticCategoryType && i == 2 {
		return "", "", "", serviceDenied("invalid_diagnostic_resource_scope")
	}
	return id, scope, kind, nil
}

// Graph identities remain case-insensitive ARM IDs. Native source selectors
// retain the same case-sensitive Cosmos names as its existing resource adapter.
func diagnosticSourceWire(wire string) (string, error) {
	id, err := diagnosticScope(wire)
	if err != nil {
		return "", err
	}
	if _, kind, err := parseID(wire); err == nil && isCosmosType(kind) {
		return cosmosWireSignature(responseID(kind, wire)), nil
	}
	return id, nil
}

func diagnosticWireID(wire string) (string, error) {
	id, scope, _, err := diagnosticResourceID(wire)
	if err != nil {
		return "", err
	}
	if len(strings.Split(scope, "/")) == 3 {
		return id, nil // Includes the native AzureResourceManager alias.
	}
	source, err := diagnosticSourceWire(wire[:strings.LastIndex(strings.ToLower(wire), "/providers/")])
	if err != nil {
		return "", err
	}
	return source + strings.TrimPrefix(id, scope), nil
}

func diagnosticWireScope(wire string) string {
	return wire[:strings.LastIndex(wire, "/providers/")]
}

func (c *client) diagnosticRequest(scope, name, kind, method string) (catalog.RESTRequest, error) {
	op, parameters, err := c.diagnosticOperation(scope, name, kind, method)
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	return bindAzureREST(op, parameters)
}

func (c *client) diagnosticOperation(scope, name, kind, method string) (catalog.Operation, map[string]any, error) {
	canonical, err := diagnosticScope(scope)
	wire, wireErr := diagnosticSourceWire(scope)
	if err != nil || wireErr != nil || wire != scope || canonical != c.root() && !strings.HasPrefix(canonical, c.root()+"/") || name != "" && !monitorReceiverName(name) || method != "GET" && method != "DELETE" || method == "DELETE" && (name == "" || kind != diagnosticSettingsType) {
		return catalog.Operation{}, nil, serviceDenied("invalid_diagnostic_operation_scope")
	}
	prefix := "DiagnosticSettings"
	parameters := map[string]any{"resourceUri": strings.TrimPrefix(scope, "/")}
	if kind == diagnosticCategoryType && scope != c.root() {
		prefix = "DiagnosticSettingsCategory"
	} else if kind != diagnosticSettingsType {
		return catalog.Operation{}, nil, serviceDenied("invalid_diagnostic_operation_type")
	} else if scope == c.root() {
		prefix = "SubscriptionDiagnosticSettings"
		parameters = map[string]any{"subscriptionId": c.subscription}
	}
	operation := "Get"
	if name == "" {
		operation = "List"
	} else {
		parameters["name"] = name
		if method == "DELETE" {
			operation = "Delete"
		}
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	op, ok := metadata.catalog.Operation("Azure.Microsoft.Insights." + prefix + "_" + operation)
	if !ok || op.Call == nil || op.Call.Version != diagnosticSettingsVersion || op.Call.Method != method {
		return catalog.Operation{}, nil, serviceDenied("diagnostic_operation_changed")
	}
	return op, parameters, nil
}

func diagnosticMetadata(raw map[string]any) error {
	if err := diagnosticSourceMetadata(raw); err != nil {
		return err
	}
	if monitorRuleFields(raw, "properties", "etag") != nil {
		return serviceDenied("ambiguous_diagnostic_resource_field")
	}
	if value := raw["etag"]; value != nil {
		if _, ok := value.(string); !ok {
			return serviceDenied("invalid_diagnostic_metadata_field")
		}
	}
	return nil
}

func diagnosticSourceMetadata(raw map[string]any) error {
	// Source resource providers have their own ETag spelling (e.g. eTag for
	// budgets). Only shared ARM identity and protection fields are checked here.
	if monitorRuleFields(raw, "id", "name", "type", "location", "tags", "systemData", "managedBy") != nil {
		return serviceDenied("ambiguous_diagnostic_resource_field")
	}
	for _, field := range []string{"id", "name", "type", "location"} {
		if value := raw[field]; value != nil {
			if _, ok := value.(string); !ok {
				return serviceDenied("invalid_diagnostic_metadata_field")
			}
		}
	}
	if value := raw["tags"]; value != nil {
		if _, kind, err := parseID(text(raw["id"])); err == nil && strings.EqualFold(kind, insightsMyWorkbookType) {
			if labels, ok := value.([]any); ok {
				for _, label := range labels {
					if _, ok := label.(string); !ok {
						return serviceDenied("invalid_diagnostic_source_label")
					}
				}
				return nil // Native private-workbook labels are not ARM tag pairs.
			}
		}
		tags, ok := value.(map[string]any)
		if !ok {
			return serviceDenied("invalid_diagnostic_tags")
		}
		for _, value := range tags {
			if _, ok := value.(string); !ok {
				return serviceDenied("invalid_diagnostic_tag")
			}
		}
	}
	return nil
}

func diagnosticIdentity(raw map[string]any, expected, kind string) error {
	if err := diagnosticMetadata(raw); err != nil {
		return err
	}
	id, _, actualKind, err := diagnosticResourceID(text(raw["id"]))
	if err != nil || id != expected || actualKind != kind || !strings.EqualFold(text(raw["name"]), last(id)) {
		return serviceDenied("diagnostic_identity_changed")
	}
	if typ := text(raw["type"]); typ != "" && !strings.EqualFold(typ, kind) {
		return serviceDenied("invalid_diagnostic_response_type")
	}
	props, ok := raw["properties"].(map[string]any)
	if !ok {
		return serviceDenied("diagnostic_properties_missing")
	}
	if kind == diagnosticCategoryType {
		if monitorRuleFields(props, "categoryType", "categoryGroups") != nil || props["categoryType"] != "Logs" && props["categoryType"] != "Metrics" {
			return serviceDenied("invalid_diagnostic_category")
		}
		if value := props["categoryGroups"]; value != nil {
			groups, ok := value.([]any)
			if !ok {
				return serviceDenied("invalid_diagnostic_category_groups")
			}
			seen := map[string]bool{}
			for _, group := range groups {
				name, ok := group.(string)
				if !ok || !monitorReceiverName(name) || seen[name] {
					return serviceDenied("invalid_diagnostic_category_group")
				}
				seen[name] = true
			}
		}
		return nil
	}
	if monitorRuleFields(props, "logs", "metrics", "logAnalyticsDestinationType") != nil {
		return serviceDenied("ambiguous_diagnostic_configuration")
	}
	if value := props["logAnalyticsDestinationType"]; value != nil {
		if _, ok := value.(string); !ok {
			return serviceDenied("invalid_diagnostic_destination_type")
		}
	}
	for _, field := range []string{"logs", "metrics"} {
		value := props[field]
		if value == nil {
			continue
		}
		rows, ok := value.([]any)
		if !ok {
			return serviceDenied("invalid_diagnostic_collection")
		}
		for _, value := range rows {
			row, ok := value.(map[string]any)
			if !ok || monitorRuleFields(row, "category", "categoryGroup", "enabled", "retentionPolicy", "timeGrain") != nil {
				return serviceDenied("invalid_diagnostic_collection_entry")
			}
			if _, ok := row["enabled"].(bool); !ok {
				return serviceDenied("diagnostic_enabled_missing")
			}
			for _, key := range []string{"category", "categoryGroup", "timeGrain"} {
				if value := row[key]; value != nil {
					if _, ok := value.(string); !ok {
						return serviceDenied("invalid_diagnostic_category_selector")
					}
				}
			}
			if row["retentionPolicy"] != nil {
				retention, ok := row["retentionPolicy"].(map[string]any)
				if !ok || monitorRuleFields(retention, "enabled", "days") != nil {
					return serviceDenied("invalid_diagnostic_retention")
				}
				days, err := batchInteger(retention["days"], 32)
				if _, ok := retention["enabled"].(bool); !ok || err != nil || days < 0 {
					return serviceDenied("invalid_diagnostic_retention")
				}
			}
		}
	}
	_, err = diagnosticReferences(id, raw)
	return err
}

// Retain every authored property, including unknown extensions, in the private
// snapshot. Native identity casing and the documented subscription alias are
// the only normalization; no query or destination content is discarded.
func diagnosticSnapshot(raw map[string]any) map[string]any {
	copy := maps.Clone(raw)
	id, _, kind, _ := diagnosticResourceID(text(raw["id"]))
	wire, _ := diagnosticWireID(text(raw["id"]))
	copy["id"], copy["name"], copy["type"] = wire, last(id), kind
	return copy
}

func (c *client) diagnosticRead(ctx context.Context, wire, kind string) (response, error) {
	id, _, actualKind, err := diagnosticResourceID(wire)
	requestWire, wireErr := diagnosticWireID(wire)
	if err != nil || wireErr != nil || requestWire != wire || actualKind != kind {
		return response{}, serviceDenied("invalid_diagnostic_read_identity")
	}
	request, err := c.diagnosticRequest(diagnosticWireScope(wire), last(id), kind, "GET")
	if err != nil {
		return response{}, err
	}
	result, err := c.request(ctx, "GET", request.URL)
	if err != nil {
		return result, err
	}
	if result.status != 200 || result.data["error"] != nil || result.data["code"] != nil || result.data["nextLink"] != nil || operationLocation(result.header) != "" || monitorRuleFields(result.data, "error", "code", "nextLink") != nil {
		return result, serviceDenied("invalid_diagnostic_get_response")
	}
	if err := diagnosticIdentity(result.data, id, kind); err != nil {
		return result, err
	}
	responseWire, err := diagnosticWireID(text(result.data["id"]))
	if err != nil || responseWire != wire {
		return result, serviceDenied("diagnostic_native_source_name_changed")
	}
	return result, nil
}

func (c *client) diagnosticIndex(ctx context.Context, scope, kind string) (items map[string]map[string]any, requestID string, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	request, err := c.diagnosticRequest(scope, "", kind, "GET")
	if err != nil {
		return nil, "", err
	}
	initial, _ := url.Parse(request.URL)
	canonicalScope, _ := diagnosticScope(scope)
	seen, values := map[string]bool{}, map[string]map[string]any{}
	for endpoint := request.URL; endpoint != ""; {
		u, err := url.Parse(endpoint)
		if err != nil || seen[endpoint] || strings.Contains(endpoint, "#") {
			return nil, "", serviceDenied("invalid_diagnostic_page")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || !slices.Equal(query["api-version"], initial.Query()["api-version"]) || len(query["$skiptoken"])+len(query["skiptoken"]) > 1 {
			return nil, "", serviceDenied("invalid_diagnostic_page_query")
		}
		for key, values := range query {
			if key != "api-version" && key != "$skiptoken" && key != "skiptoken" || len(values) != 1 || values[0] == "" {
				return nil, "", serviceDenied("filtered_diagnostic_page")
			}
		}
		seen[endpoint] = true
		rows, next, result, err := c.listPageResult(ctx, endpoint, initial.Path)
		if err != nil {
			return nil, "", err
		}
		if result.data["code"] != nil || operationLocation(result.header) != "" || monitorRuleFields(result.data, "value", "nextLink", "error", "code") != nil || scope == c.root() && next != "" {
			return nil, "", serviceDenied("invalid_diagnostic_list_response")
		}
		for _, value := range rows {
			raw := object(value)
			id, actualScope, actualKind, err := diagnosticResourceID(text(raw["id"]))
			if err != nil || actualScope != canonicalScope || actualKind != kind || values[id] != nil || diagnosticIdentity(raw, id, kind) != nil {
				return nil, "", serviceDenied("invalid_diagnostic_list_identity")
			}
			wire, err := diagnosticWireID(text(raw["id"]))
			if err != nil || diagnosticWireScope(wire) != scope {
				return nil, "", serviceDenied("diagnostic_index_source_name_changed")
			}
			current, err := c.diagnosticRead(ctx, wire, kind)
			if err != nil {
				return nil, "", err
			}
			if c.privateConfiguration(diagnosticSnapshot(raw)) != c.privateConfiguration(diagnosticSnapshot(current.data)) {
				return nil, "", serviceDenied("diagnostic_list_configuration_changed")
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
