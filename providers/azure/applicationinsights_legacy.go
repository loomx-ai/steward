package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const (
	insightsAnalyticsType    = applicationInsightsType + "/analyticsItems"
	insightsMyAnalyticsType  = applicationInsightsType + "/myanalyticsItems"
	insightsExportType       = applicationInsightsType + "/exportconfiguration"
	insightsFavoriteType     = applicationInsightsType + "/favorites"
	insightsWorkItemType     = applicationInsightsType + "/workItemConfigs"
	insightsAnnotationType   = applicationInsightsType + "/annotations"
	insightsLegacyVersion    = "2015-05-01"
	insightsComponentVersion = "2020-02-02"
	insightsOperationPrefix  = "Azure.Microsoft.Insights."
)

type insightsLegacyResource struct {
	kind, collection, field, parameter string
	list, read, delete                 string
}

func insightsLegacyOperationID(name string) string {
	// The published analytics document uses a lowercase provider namespace.
	if strings.HasPrefix(name, "AnalyticsItems_") {
		return "Azure.microsoft.insights." + name
	}
	return insightsOperationPrefix + name
}

func insightsLegacyKind(kind string) insightsLegacyResource {
	for _, row := range []insightsLegacyResource{
		{insightsAnalyticsType, "analyticsItems", "Id", "id", "AnalyticsItems_List", "AnalyticsItems_Get", "AnalyticsItems_Delete"},
		{insightsMyAnalyticsType, "myanalyticsItems", "Id", "id", "AnalyticsItems_List", "AnalyticsItems_Get", "AnalyticsItems_Delete"},
		{insightsExportType, "exportconfiguration", "ExportId", "exportId", "ExportConfigurations_List", "ExportConfigurations_Get", "ExportConfigurations_Delete"},
		{insightsFavoriteType, "favorites", "FavoriteId", "favoriteId", "Favorites_List", "Favorites_Get", "Favorites_Delete"},
		{insightsWorkItemType, "WorkItemConfigs", "Id", "workItemConfigId", "WorkItemConfigurations_List", "WorkItemConfigurations_GetItem", "WorkItemConfigurations_Delete"},
		{insightsAnnotationType, "Annotations", "Id", "annotationId", "Annotations_List", "Annotations_Get", "Annotations_Delete"},
	} {
		if strings.EqualFold(kind, row.kind) {
			return row
		}
	}
	return insightsLegacyResource{}
}

// These native objects publish flat opaque IDs, not ARM resource IDs. Use the
// actual GET/DELETE URL without the protocol version as their identity. In
// particular, analyticsItems/item?id=X must never become analyticsItems/X.
func insightsLegacyURL(parent, kind, selector string) (string, error) {
	parent, typ, err := parseID(parent)
	row := insightsLegacyKind(kind)
	if err != nil || !strings.EqualFold(typ, applicationInsightsType) || row.kind == "" || !insightsLegacySelector(selector, row) {
		return "", serviceDenied("invalid_insights_legacy_identity")
	}
	endpoint := (&url.URL{Scheme: "https", Host: "management.azure.com", Path: parent + "/" + strings.ToLower(row.collection)}).String()
	if row.parameter == "id" {
		return endpoint + "/item?" + url.Values{"id": {selector}}.Encode(), nil
	}
	return endpoint + "/" + url.PathEscape(selector), nil
}

func insightsLegacySelector(value string, row insightsLegacyResource) bool {
	if value == "" || value != strings.TrimSpace(value) || !utf8.ValidString(value) || strings.ContainsFunc(value, unicode.IsControl) || strings.Contains(value, "\\") {
		return false
	}
	if row.parameter == "id" {
		return true // This selector is a query value, never a path fragment.
	}
	if strings.Contains(value, "%") {
		return false
	}
	if row.kind != insightsExportType && strings.Contains(value, "/") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "." || part == ".." {
			return false
		}
	}
	return true
}

func insightsLegacyIdentity(value string) (id, parent, kind, selector string, err error) {
	invalid := func() (string, string, string, string, error) {
		return "", "", "", "", serviceDenied("invalid_insights_legacy_identity")
	}
	u, e := url.Parse(value)
	if e != nil || value != strings.TrimSpace(value) || strings.Contains(value, "#") || u.Scheme != "https" || !strings.EqualFold(u.Host, "management.azure.com") || u.User != nil || u.Fragment != "" || u.Opaque != "" || u.ForceQuery {
		return invalid()
	}
	parts := strings.Split(u.EscapedPath(), "/")
	if len(parts) != 11 {
		return invalid()
	}
	for i := range parts[:10] {
		parts[i], e = url.PathUnescape(parts[i])
		if e != nil || strings.ContainsAny(parts[i], "/\\%") {
			return invalid()
		}
	}
	parent, typ, e := parseID(strings.Join(parts[:9], "/"))
	row := insightsLegacyKind(applicationInsightsType + "/" + parts[9])
	if e != nil || !strings.EqualFold(typ, applicationInsightsType) || row.kind == "" {
		return invalid()
	}
	query, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		return invalid()
	}
	if row.parameter == "id" {
		if !strings.EqualFold(parts[10], "item") || len(query) != 1 || len(query["id"]) != 1 {
			return invalid()
		}
		selector = query.Get("id")
	} else {
		if u.RawQuery != "" {
			return invalid()
		}
		selector, e = url.PathUnescape(parts[10])
		if e != nil {
			return invalid()
		}
	}
	id, e = insightsLegacyURL(parent, row.kind, selector)
	if e != nil {
		return invalid()
	}
	return id, parent, row.kind, selector, nil
}

func (c *client) insightsLegacyOperation(kind resourceType, nativeID, method string) (catalog.Operation, map[string]any, error) {
	_, parent, typ, selector, err := insightsLegacyIdentity(nativeID)
	row := insightsLegacyKind(kind.NativeType)
	if err != nil || row.kind == "" || typ != row.kind || !strings.HasPrefix(parent, c.root()+"/") {
		return catalog.Operation{}, nil, serviceDenied("insights_legacy_operation_identity_mismatch")
	}
	ids, expected := kind.ReadOperations, row.read
	if method == "DELETE" {
		ids, expected = kind.DeleteOperations, row.delete
	} else if method != "GET" {
		return catalog.Operation{}, nil, serviceDenied("invalid_insights_legacy_method")
	}
	if len(ids) != 1 || ids[0] != insightsLegacyOperationID(expected) {
		return catalog.Operation{}, nil, serviceDenied("invalid_insights_legacy_operation_binding")
	}
	data, err := providerData()
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	op, ok := data.catalog.Operation(ids[0])
	if !ok || op.Call == nil || op.Call.Method != method || op.Call.Version != insightsLegacyVersion || op.Call.Style != "azure-rest" {
		return catalog.Operation{}, nil, serviceDenied("invalid_insights_legacy_operation")
	}
	parts := strings.Split(parent, "/")
	params := map[string]any{"subscriptionId": c.subscription, "resourceGroupName": parts[4], "resourceName": parts[8], row.parameter: selector}
	if row.parameter == "id" {
		params["scopePath"] = row.collection
	}
	bound, err := bindAzureREST(op, params)
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	if err := c.insightsLegacyEndpoint(bound.URL, nativeID); err != nil {
		return catalog.Operation{}, nil, err
	}
	return op, params, nil
}

func (c *client) insightsLegacyEndpoint(endpoint, expected string) error {
	u, err := url.Parse(endpoint)
	if err != nil || strings.Contains(endpoint, "#") {
		return serviceDenied("invalid_insights_legacy_endpoint")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != insightsLegacyVersion {
		return serviceDenied("invalid_insights_legacy_version")
	}
	delete(query, "api-version")
	u.RawQuery = query.Encode()
	id, parent, _, _, err := insightsLegacyIdentity(u.String())
	want, _, _, _, wantErr := insightsLegacyIdentity(expected)
	if err != nil || wantErr != nil || id != want || !strings.HasPrefix(parent, c.root()+"/") {
		return serviceDenied("insights_legacy_endpoint_changed")
	}
	return nil
}

func (c *client) insightsLegacyRead(ctx context.Context, kind resourceType, id string) (response, error) {
	op, params, err := c.resourceOperation(kind, id, "GET")
	if err != nil {
		return response{}, err
	}
	request, err := bindAzureREST(op, params)
	if err != nil {
		return response{}, err
	}
	result, err := c.requestAt(ctx, request.Method, request.URL, nil, nil, func(endpoint string) error { return c.insightsLegacyEndpoint(endpoint, id) })
	if err != nil {
		return result, err
	}
	row := insightsLegacyKind(kind.NativeType)
	if result.status != 200 || operationLocation(result.header) != "" || result.data["error"] != nil || result.data["code"] != nil {
		return result, serviceDenied("invalid_insights_legacy_read_response")
	}
	if row.kind == insightsAnnotationType {
		values, ok := result.data["value"].([]any)
		if !ok || len(values) != 1 {
			return result, serviceDenied("insights_annotation_get_identity_missing")
		}
		result.data = object(values[0])
	}
	_, _, _, selector, _ := insightsLegacyIdentity(id)
	if err := insightsLegacyResponseIdentity(row, selector, result.data); err != nil {
		return result, err
	}
	return result, nil
}

func insightsLegacyResponseIdentity(row insightsLegacyResource, selector string, raw map[string]any) error {
	value, ok := raw[row.field].(string)
	if !ok || value != selector {
		return serviceDenied("insights_legacy_response_identity_mismatch")
	}
	for key := range raw {
		if key != row.field && strings.EqualFold(key, row.field) {
			return serviceDenied("ambiguous_insights_legacy_response_identity")
		}
	}
	return nil
}

func insightsLegacySnapshot(kind string, raw map[string]any) map[string]any {
	copy := maps.Clone(raw)
	if strings.EqualFold(kind, insightsExportType) {
		for _, key := range []string{"ExportStatus", "LastSuccessTime", "LastGapTime", "PermanentErrorReason"} {
			delete(copy, key)
		}
	}
	return copy
}

func (c *client) insightsLegacyConfiguration(id, kind string, raw map[string]any) string {
	return c.privateConfiguration(map[string]any{"resource": id, "kind": kind, "configuration": insightsLegacySnapshot(kind, raw)})
}

func (c *client) insightsComponent(ctx context.Context, id string) (map[string]any, error) {
	kind := resourceType{NativeType: applicationInsightsType, ReadOperations: []string{insightsOperationPrefix + "Components_Get"}}
	op, params, err := c.resourceOperation(kind, id, "GET")
	if err != nil {
		return nil, err
	}
	request, err := bindAzureREST(op, params)
	if err != nil || op.Call.Version != insightsComponentVersion {
		return nil, serviceDenied("invalid_insights_component_binding")
	}
	result, err := c.request(ctx, request.Method, request.URL)
	if err != nil {
		return nil, err
	}
	_, hasProperties := result.data["properties"].(map[string]any)
	if !insightsARMReadValid(result, id, applicationInsightsType) || !hasProperties || text(result.data["location"]) == "" || !strings.EqualFold(text(result.data["name"]), last(id)) {
		return nil, serviceDenied("invalid_insights_component_response")
	}
	if _, err := insightsWorkspaceID(result.data); err != nil {
		return nil, err
	}
	return result.data, nil
}

// The native annotation index is explicitly bounded to the last 90 days.
// Callers must carry this window as evidence; it never proves older history
// absent, and no caller may silently turn this into an unbounded inventory.
type insightsAnnotationWindow struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

func insightsLegacyListParameters(row insightsLegacyResource, window insightsAnnotationWindow) ([]map[string]any, error) {
	if row.kind == "" {
		return nil, serviceDenied("invalid_insights_legacy_list_kind")
	}
	if row.kind == insightsAnnotationType {
		start, startErr := time.Parse(time.RFC3339Nano, window.Start)
		end, endErr := time.Parse(time.RFC3339Nano, window.End)
		now := time.Now().UTC()
		if startErr != nil || endErr != nil || !end.After(start) || end.After(now) || start.Before(now.Add(-90*24*time.Hour)) {
			return nil, serviceDenied("invalid_insights_annotation_window")
		}
		return []map[string]any{{"start": window.Start, "end": window.End}}, nil
	}
	if window.Start != "" || window.End != "" {
		return nil, serviceDenied("unexpected_insights_annotation_window")
	}
	if row.kind == insightsFavoriteType {
		// Omission means sourceType=other, not all source types. Enumerate
		// every published source type and both user/shared favorite scopes.
		var filters []map[string]any
		for _, scope := range []string{"shared", "user"} {
			for _, source := range []string{"", "retention", "notebook", "sessions", "events", "userflows", "funnel", "impact", "segmentation"} {
				filter := map[string]any{"favoriteType": scope, "canFetchContent": true}
				if source != "" {
					filter["sourceType"] = source
				}
				filters = append(filters, filter)
			}
		}
		return filters, nil
	}
	if row.parameter == "id" {
		return []map[string]any{{"scopePath": row.collection, "includeContent": true}}, nil
	}
	return []map[string]any{{}}, nil
}

func (c *client) insightsLegacyChildren(ctx context.Context, parent, kind string, window insightsAnnotationWindow) ([]serviceChild, error) {
	parent, parentKind, err := parseID(parent)
	row := insightsLegacyKind(kind)
	if err != nil || !strings.EqualFold(parentKind, applicationInsightsType) || !strings.HasPrefix(parent, c.root()+"/") {
		return nil, serviceDenied("invalid_insights_legacy_parent")
	}
	filters, err := insightsLegacyListParameters(row, window)
	if err != nil {
		return nil, err
	}
	data, err := providerData()
	if err != nil {
		return nil, err
	}
	op, ok := data.catalog.Operation(insightsLegacyOperationID(row.list))
	if !ok || op.Call == nil || op.Call.Version != insightsLegacyVersion || op.Call.Method != "GET" || op.Call.Style != "azure-rest" {
		return nil, serviceDenied("invalid_insights_legacy_list_binding")
	}
	before, err := c.insightsComponent(ctx, parent)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(parent, "/")
	seen := map[string]bool{}
	var children []serviceChild
	for _, filter := range filters {
		params := maps.Clone(filter)
		params["subscriptionId"], params["resourceGroupName"], params["resourceName"] = c.subscription, parts[4], parts[8]
		request, err := bindAzureREST(op, params)
		if err != nil {
			return nil, err
		}
		u, _ := url.Parse(request.URL)
		if !strings.EqualFold(u.Path, parent+"/"+row.collection) {
			return nil, serviceDenied("insights_legacy_list_parent_changed")
		}
		values, next, result, err := c.listPageResult(ctx, request.URL, u.Path)
		if err != nil {
			return nil, err
		}
		// Selected legacy array lists and nextLinkName:null envelopes have
		// no continuation protocol. A new/partial protocol requires review.
		if next != "" || operationLocation(result.header) != "" || result.data["code"] != nil {
			return nil, serviceDenied("unexpected_insights_legacy_continuation")
		}
		for _, value := range values {
			raw, ok := value.(map[string]any)
			selector, valid := raw[row.field].(string)
			if !ok || !valid {
				return nil, serviceDenied("invalid_insights_legacy_list_identity")
			}
			id, err := insightsLegacyURL(parent, row.kind, selector)
			if err != nil || seen[id] || insightsLegacyResponseIdentity(row, selector, raw) != nil {
				return nil, serviceDenied("invalid_insights_legacy_list_identity")
			}
			seen[id] = true
			current, err := c.insightsLegacyRead(ctx, resourceType{NativeType: row.kind, ReadOperations: []string{insightsLegacyOperationID(row.read)}}, id)
			if err != nil {
				return nil, err
			}
			if !nativeConfigurationContains(insightsLegacySnapshot(row.kind, raw), insightsLegacySnapshot(row.kind, current.data)) {
				return nil, serviceDenied("insights_legacy_list_configuration_changed")
			}
			if row.kind == insightsFavoriteType {
				if current.data["FavoriteType"] != filter["favoriteType"] {
					return nil, serviceDenied("insights_favorite_scope_disagrees")
				}
				source := current.data["SourceType"]
				if expected, explicit := filter["sourceType"]; explicit && source != expected || !explicit && source != nil && source != "" && source != "other" {
					return nil, serviceDenied("insights_favorite_source_disagrees")
				}
			}
			children = append(children, serviceChild{id: id, kind: row.kind, data: current.data})
		}
	}
	after, err := c.insightsComponent(ctx, parent)
	if err != nil {
		return nil, err
	}
	if c.privateConfiguration(monitorPrivateLinkTargetSnapshot(before)) != c.privateConfiguration(monitorPrivateLinkTargetSnapshot(after)) {
		return nil, serviceDenied("insights_component_changed_during_child_inventory")
	}
	slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
	return children, nil
}
