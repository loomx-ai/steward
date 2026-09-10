package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const (
	insightsAPIKeyType        = applicationInsightsType + "/APIKeys"
	insightsLinkedStorageType = applicationInsightsType + "/linkedStorageAccounts"
	insightsStorageVersion    = "2020-03-01-preview"
)

func insightsARMChildKind(kind string) string {
	for _, known := range []string{insightsAPIKeyType, insightsLinkedStorageType} {
		if strings.EqualFold(kind, known) {
			return known
		}
	}
	return ""
}

func insightsChildIdentity(value string) (id, parent, kind, selector string, err error) {
	if strings.HasPrefix(value, "https:") {
		return insightsLegacyIdentity(value)
	}
	id, kind, err = parseID(value)
	parts := strings.Split(id, "/")
	kind = insightsARMChildKind(kind)
	if err != nil || value != strings.TrimSpace(value) || kind == "" || len(parts) != 11 || kind == insightsLinkedStorageType && parts[10] != "serviceprofiler" {
		return "", "", "", "", serviceDenied("invalid_insights_child_identity")
	}
	return id, strings.Join(parts[:9], "/"), kind, parts[10], nil
}

func insightsChildVersion(kind string) string {
	if strings.EqualFold(kind, insightsLinkedStorageType) {
		return insightsStorageVersion
	}
	return insightsLegacyVersion
}

func insightsChildProofKey(kind string) string {
	if insightsARMChildKind(kind) != "" {
		return "_insights_child_private_configuration"
	}
	return "_insights_legacy_private_configuration"
}

func insightsChildReceiptKey(kind string) string {
	if insightsARMChildKind(kind) != "" {
		return "_insights_child_receipt"
	}
	return "_insights_legacy_receipt"
}

func insightsARMChildResponseIdentity(id, kind string, raw map[string]any) error {
	actual, _, actualKind, _, err := insightsChildIdentity(text(raw["id"]))
	if err != nil || actual != id || actualKind != kind {
		return serviceDenied("insights_child_response_identity_mismatch")
	}
	for key := range raw {
		if key != "id" && strings.EqualFold(key, "id") || key != "type" && strings.EqualFold(key, "type") {
			return serviceDenied("ambiguous_insights_child_response_identity")
		}
	}
	if typ, exists := raw["type"]; exists && !strings.EqualFold(text(typ), kind) || kind == insightsLinkedStorageType && raw["type"] == nil {
		return serviceDenied("insights_child_response_type_mismatch")
	}
	if kind == insightsLinkedStorageType {
		_, typ, err := parseID(text(object(raw["properties"])["linkedStorageAccount"]))
		if err != nil || !strings.EqualFold(typ, storageType) {
			return serviceDenied("invalid_insights_linked_storage_target")
		}
	}
	return nil
}

func insightsARMChildSnapshot(raw map[string]any) map[string]any {
	copy := maps.Clone(raw)
	copy["id"], _, _ = parseID(text(raw["id"]))
	if typ, exists := raw["type"]; exists {
		copy["type"] = strings.ToLower(text(typ))
	}
	if props := object(raw["properties"]); props["linkedStorageAccount"] != nil {
		props = maps.Clone(props)
		props["linkedStorageAccount"], _, _ = parseID(text(props["linkedStorageAccount"]))
		copy["properties"] = props
	}
	return copy
}

func (c *client) insightsChildConfiguration(id, kind string, raw map[string]any) string {
	if insightsARMChildKind(kind) == "" {
		return c.insightsLegacyConfiguration(id, kind, raw)
	}
	return c.privateConfiguration(map[string]any{"resource": id, "kind": kind, "configuration": insightsARMChildSnapshot(raw)})
}

func (c *client) insightsChildEndpoint(endpoint, expected string) error {
	if strings.HasPrefix(expected, "https:") {
		return c.insightsLegacyEndpoint(endpoint, expected)
	}
	u, err := url.Parse(endpoint)
	if err != nil || c.validateURL(endpoint) != nil || strings.Contains(endpoint, "#") || u.ForceQuery {
		return serviceDenied("invalid_insights_child_endpoint")
	}
	id, _, kind, _, err := insightsChildIdentity(u.Path)
	want, _, _, _, wantErr := insightsChildIdentity(expected)
	query, queryErr := url.ParseQuery(u.RawQuery)
	if err != nil || wantErr != nil || id != want || queryErr != nil || len(query) != 1 || len(query["api-version"]) != 1 || query.Get("api-version") != insightsChildVersion(kind) {
		return serviceDenied("insights_child_endpoint_changed")
	}
	return nil
}

func (c *client) insightsChildRequest(kind resourceType, id, method string) (catalog.RESTRequest, error) {
	op, params, err := c.resourceOperation(kind, id, method)
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	if typ := insightsARMChildKind(kind.NativeType); typ != "" {
		name := "APIKeys_"
		if typ == insightsLinkedStorageType {
			name = "ComponentLinkedStorageAccounts_"
		}
		suffix := "Get"
		if method == "DELETE" {
			suffix = "Delete"
		} else if method != "GET" {
			return catalog.RESTRequest{}, serviceDenied("invalid_insights_child_method")
		}
		if op.ID != insightsOperationPrefix+name+suffix || op.Call == nil || op.Call.Version != insightsChildVersion(typ) || op.Call.Method != method || op.Call.Style != "azure-rest" {
			return catalog.RESTRequest{}, serviceDenied("invalid_insights_child_operation_binding")
		}
	}
	request, err := bindAzureREST(op, params)
	if err == nil {
		err = c.insightsChildEndpoint(request.URL, id)
	}
	return request, err
}

func (c *client) insightsChildRead(ctx context.Context, kind resourceType, id string) (response, error) {
	if insightsARMChildKind(kind.NativeType) == "" {
		return c.insightsLegacyRead(ctx, kind, id)
	}
	request, err := c.insightsChildRequest(kind, id, "GET")
	if err != nil {
		return response{}, err
	}
	result, err := c.requestAt(ctx, request.Method, request.URL, nil, nil, func(endpoint string) error { return c.insightsChildEndpoint(endpoint, id) })
	if err != nil {
		return result, err
	}
	if result.status != 200 || operationLocation(result.header) != "" || result.data["error"] != nil || result.data["code"] != nil {
		return result, serviceDenied("invalid_insights_child_read_response")
	}
	return result, insightsARMChildResponseIdentity(id, kind.NativeType, result.data)
}

func (c *client) insightsChildren(ctx context.Context, parent, kind string) ([]serviceChild, error) {
	if insightsARMChildKind(kind) == "" {
		return c.insightsLegacyChildren(ctx, parent, kind, insightsAnnotationWindow{})
	}
	parent, typ, err := parseID(parent)
	if err != nil || !strings.EqualFold(typ, applicationInsightsType) || !strings.HasPrefix(parent, c.root()+"/") {
		return nil, serviceDenied("invalid_insights_child_parent")
	}
	kind = insightsARMChildKind(kind)
	before, err := c.insightsComponent(ctx, parent)
	if err != nil {
		return nil, err
	}
	var children []serviceChild
	if kind == insightsLinkedStorageType {
		id := parent + "/linkedstorageaccounts/serviceprofiler"
		mapping := resourceType{NativeType: kind, ReadOperations: []string{insightsOperationPrefix + "ComponentLinkedStorageAccounts_Get"}}
		current, err := c.insightsChildRead(ctx, mapping, id)
		if err != nil && !isNotFound(err) {
			return nil, err
		}
		if err == nil {
			children = append(children, serviceChild{id: id, kind: kind, data: current.data})
		}
	} else {
		data, err := providerData()
		if err != nil {
			return nil, err
		}
		op, ok := data.catalog.Operation(insightsOperationPrefix + "APIKeys_List")
		if !ok || op.Call == nil || op.Call.Method != "GET" || op.Call.Version != insightsLegacyVersion || op.Call.Style != "azure-rest" {
			return nil, serviceDenied("invalid_insights_api_key_list_binding")
		}
		parts := strings.Split(parent, "/")
		request, err := bindAzureREST(op, map[string]any{"subscriptionId": c.subscription, "resourceGroupName": parts[4], "resourceName": parts[8]})
		if err != nil {
			return nil, err
		}
		values, next, result, err := c.listPageResult(ctx, request.URL, parent+"/apikeys")
		if err != nil {
			return nil, err
		}
		if next != "" || operationLocation(result.header) != "" || result.data["code"] != nil {
			return nil, serviceDenied("unexpected_insights_api_key_continuation")
		}
		seen := map[string]bool{}
		for _, value := range values {
			raw := object(value)
			id, owner, typ, _, err := insightsChildIdentity(text(raw["id"]))
			if err != nil || owner != parent || typ != kind || seen[id] || insightsARMChildResponseIdentity(id, kind, raw) != nil {
				return nil, serviceDenied("invalid_insights_api_key_list_identity")
			}
			seen[id] = true
			mapping := resourceType{NativeType: kind, ReadOperations: []string{insightsOperationPrefix + "APIKeys_Get"}}
			current, err := c.insightsChildRead(ctx, mapping, id)
			if err != nil {
				return nil, err
			}
			if !nativeConfigurationContains(insightsARMChildSnapshot(raw), insightsARMChildSnapshot(current.data)) {
				return nil, serviceDenied("insights_api_key_list_configuration_changed")
			}
			children = append(children, serviceChild{id: id, kind: kind, data: current.data})
		}
	}
	// In particular, singleton GET 404 proves absence only while its component
	// still exists with the same identity and configuration.
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
