package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	insightsWorkbookType         = "Microsoft.Insights/workbooks"
	insightsMyWorkbookType       = "Microsoft.Insights/myWorkbooks"
	insightsWorkbookTemplateType = "Microsoft.Insights/workbooktemplates"
	insightsWorkbookSource       = "application-insights-workbooks"
	insightsWorkbookProof        = "_insights_workbook_configuration"
)

func insightsWorkbookKind(kind string) string {
	for _, value := range []string{insightsWorkbookType, insightsMyWorkbookType, insightsWorkbookTemplateType} {
		if strings.EqualFold(kind, value) {
			return value
		}
	}
	return ""
}

func insightsWorkbookVersion(kind string) string {
	switch insightsWorkbookKind(kind) {
	case insightsWorkbookType:
		return "2023-06-01"
	case insightsMyWorkbookType:
		return "2021-03-08"
	case insightsWorkbookTemplateType:
		return "2020-11-20"
	}
	return ""
}

func (c *client) workbookRequest(kind, id, method string) (catalog.RESTRequest, error) {
	mapping, ok := findType(kind)
	if !ok || insightsWorkbookKind(kind) == "" || method != "GET" && method != "DELETE" {
		return catalog.RESTRequest{}, serviceDenied("invalid_workbook_operation")
	}
	op, params, err := c.resourceOperation(mapping, id, method)
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	if op.Call == nil || op.Call.Version != insightsWorkbookVersion(kind) || op.Call.Method != method {
		return catalog.RESTRequest{}, serviceDenied("invalid_workbook_operation_binding")
	}
	if kind == insightsWorkbookType && method == "GET" {
		params["canFetchContent"] = true
	}
	return bindAzureREST(op, params)
}

func insightsWorkbookIdentity(raw map[string]any, expectedID, kind string, content bool) error {
	id, typ, err := parseID(text(raw["id"]))
	if err != nil || id != expectedID || !strings.EqualFold(typ, kind) || !validResponseType(kind, text(raw["type"])) || !strings.EqualFold(text(raw["name"]), last(id)) || text(raw["location"]) == "" {
		return serviceDenied("invalid_workbook_native_identity")
	}
	// Original CLI recordings return null type. The full ARM ID and bound
	// operation establish identity; a present type must remain a valid string.
	if value := raw["type"]; value != nil {
		if _, ok := value.(string); !ok {
			return serviceDenied("invalid_workbook_native_type")
		}
	}
	props, ok := raw["properties"].(map[string]any)
	if !ok {
		return serviceDenied("invalid_workbook_native_properties")
	}
	if kind == insightsWorkbookTemplateType {
		if content {
			if _, ok := props["templateData"].(map[string]any); !ok {
				return serviceDenied("workbook_template_content_missing")
			}
			if _, ok := props["galleries"].([]any); !ok {
				return serviceDenied("workbook_template_galleries_missing")
			}
		}
	} else {
		if text(props["category"]) == "" || text(props["displayName"]) == "" || text(raw["kind"]) != "shared" && (kind != insightsMyWorkbookType || text(raw["kind"]) != "user") {
			return serviceDenied("invalid_workbook_metadata")
		}
		if content && text(props["serializedData"]) == "" {
			return serviceDenied("workbook_content_missing")
		}
		for _, key := range []string{"sourceId", "storageUri", "revision"} {
			if value := props[key]; value != nil {
				if _, ok := value.(string); !ok {
					return serviceDenied("invalid_workbook_reference")
				}
			}
		}
	}
	if tags := raw["tags"]; tags != nil {
		switch tags := tags.(type) {
		case map[string]any:
			for _, value := range tags {
				if _, ok := value.(string); !ok {
					return serviceDenied("invalid_workbook_tags")
				}
			}
		case []any:
			// Retained MyWorkbooks examples use string arrays despite the ARM
			// map schema. They remain private-workbook labels, not ARM tag pairs.
			if kind != insightsMyWorkbookType {
				return serviceDenied("invalid_workbook_tags")
			}
			for _, value := range tags {
				if _, ok := value.(string); !ok {
					return serviceDenied("invalid_workbook_tags")
				}
			}
		default:
			return serviceDenied("invalid_workbook_tags")
		}
	}
	return nil
}

func insightsWorkbookSnapshot(raw map[string]any, summary bool) map[string]any {
	copy := maps.Clone(raw)
	id, kind, _ := parseID(text(raw["id"]))
	copy["id"], copy["name"], copy["type"] = id, last(id), insightsWorkbookKind(kind)
	props := maps.Clone(object(raw["properties"]))
	copy["properties"] = props
	// A native list/revision summary may explicitly omit the content. The
	// individual GET still requires full content before authorizing cleanup.
	if summary && props["serializedData"] == nil {
		delete(props, "serializedData")
	}
	return copy
}

func (c *client) workbookRead(ctx context.Context, kind, id string) (response, error) {
	request, err := c.workbookRequest(kind, id, "GET")
	if err != nil {
		return response{}, err
	}
	result, err := c.request(ctx, "GET", request.URL)
	if err != nil {
		return result, err
	}
	if result.status != 200 || result.data["code"] != nil || result.data["error"] != nil || operationLocation(result.header) != "" || result.data["nextLink"] != nil || result.data["NextLink"] != nil {
		return result, serviceDenied("invalid_workbook_get_response")
	}
	return result, insightsWorkbookIdentity(result.data, id, kind, true)
}

// A continuation may only add a native skip token. Category, content mode,
// subscription, collection and API version cannot change during enumeration.
func (c *client) workbookList(ctx context.Context, operation, kind string, params map[string]any) ([]any, string, error) {
	data, err := providerData()
	if err != nil {
		return nil, "", err
	}
	op, ok := data.catalog.Operation(insightsOperationPrefix + operation)
	if !ok || op.Call == nil || op.Call.Method != "GET" || op.Call.Version != insightsWorkbookVersion(kind) {
		return nil, "", serviceDenied("invalid_workbook_list_binding")
	}
	request, err := bindAzureREST(op, params)
	if err != nil {
		return nil, "", err
	}
	initial, _ := url.Parse(request.URL)
	seen := map[string]bool{}
	var values []any
	provenance := ""
	for endpoint := request.URL; endpoint != ""; {
		u, err := url.Parse(endpoint)
		if err != nil || seen[endpoint] || u.Fragment != "" {
			return nil, "", serviceDenied("invalid_workbook_list_cursor")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(query["$skiptoken"])+len(query["skiptoken"]) > 1 {
			return nil, "", serviceDenied("invalid_workbook_list_query")
		}
		for key, values := range query {
			if len(values) != 1 || values[0] == "" {
				return nil, "", serviceDenied("invalid_workbook_list_query")
			}
			if key != "skiptoken" && key != "$skiptoken" && !slices.Equal(values, initial.Query()[key]) {
				return nil, "", serviceDenied("filtered_workbook_list")
			}
		}
		for key, values := range initial.Query() {
			if !slices.Equal(query[key], values) {
				return nil, "", serviceDenied("workbook_list_query_changed")
			}
		}
		seen[endpoint] = true
		page, next, result, err := c.listPageResult(ctx, endpoint, initial.Path)
		if err != nil {
			return nil, "", err
		}
		if operationLocation(result.header) != "" || result.data["code"] != nil {
			return nil, "", serviceDenied("incomplete_workbook_list")
		}
		values = append(values, page...)
		if result.requestID != "" {
			provenance = result.requestID
		}
		endpoint = next
	}
	return values, provenance, nil
}

type insightsWorkbookRecord struct {
	raw       map[string]any
	revisions map[string]any
	requestID string
}

func (c *client) workbookRecord(ctx context.Context, kind, id string) (insightsWorkbookRecord, error) {
	before, err := c.workbookRead(ctx, kind, id)
	if err != nil {
		return insightsWorkbookRecord{}, err
	}
	record := insightsWorkbookRecord{raw: before.data, revisions: map[string]any{}, requestID: before.requestID}
	// BYOS has no provider-managed revision history. Blob versions, the
	// container and the assigned identity remain external dependencies.
	if kind == insightsWorkbookType && text(object(before.data["properties"])["storageUri"]) == "" {
		params := map[string]any{"subscriptionId": c.subscription, "resourceGroupName": strings.Split(id, "/")[4], "resourceName": last(id)}
		rows, _, err := c.workbookList(ctx, "Workbooks_RevisionsList", kind, params)
		if err != nil {
			return record, contracts.DependencyReadError(err)
		}
		data, _ := providerData()
		op, ok := data.catalog.Operation(insightsOperationPrefix + "Workbooks_RevisionGet")
		if !ok || op.Call == nil || op.Call.Method != "GET" || op.Call.Version != insightsWorkbookVersion(kind) {
			return record, serviceDenied("invalid_workbook_revision_binding")
		}
		for _, value := range rows {
			raw := object(value)
			revision := text(object(raw["properties"])["revision"])
			if !insightsLegacySelector(revision, insightsLegacyResource{}) || record.revisions[revision] != nil || insightsWorkbookIdentity(raw, id, kind, false) != nil {
				return record, serviceDenied("invalid_workbook_revision_identity")
			}
			params["revisionId"] = revision
			request, err := bindAzureREST(op, params)
			if err != nil {
				return record, err
			}
			result, err := c.request(ctx, "GET", request.URL)
			if err != nil {
				return record, contracts.DependencyReadError(err)
			}
			if !insightsARMReadValid(result, id, kind) || result.data["nextLink"] != nil || result.data["NextLink"] != nil || insightsWorkbookIdentity(result.data, id, kind, true) != nil || text(object(result.data["properties"])["revision"]) != revision || !nativeConfigurationContains(insightsWorkbookSnapshot(raw, true), insightsWorkbookSnapshot(result.data, false)) {
				return record, serviceDenied("workbook_revision_changed")
			}
			record.revisions[revision] = insightsWorkbookSnapshot(result.data, false)
		}
	}
	after, err := c.workbookRead(ctx, kind, id)
	if err != nil {
		return record, contracts.DependencyReadError(err)
	}
	if c.privateConfiguration(insightsWorkbookSnapshot(before.data, false)) != c.privateConfiguration(insightsWorkbookSnapshot(after.data, false)) {
		return record, serviceDenied("workbook_changed_during_read")
	}
	return record, nil
}

func (c *client) workbookConfiguration(record insightsWorkbookRecord) string {
	return c.privateConfiguration(map[string]any{"resource": insightsWorkbookSnapshot(record.raw, false), "revisions": record.revisions})
}

// Managed resource-group deletion must review the same private history as an
// independent workbook DELETE. The caller has already observed the live root.
func (c *client) workbookManagedIncarnation(ctx context.Context, planned asset.Asset) error {
	if insightsWorkbookKind(planned.Identity.NativeType) == "" {
		return nil
	}
	record, err := c.workbookRecord(ctx, planned.Identity.NativeType, planned.Identity.NativeID)
	if err != nil {
		return contracts.DependencyReadError(err)
	}
	if text(planned.Normalized[insightsWorkbookProof]) != c.workbookConfiguration(record) || resourceRegion(record.raw) != planned.Location {
		return serviceDenied("managed_workbook_configuration_changed")
	}
	return nil
}

func (c *client) workbookGroup(ctx context.Context, id string) (map[string]any, error) {
	groupID := strings.Join(strings.Split(id, "/")[:5], "/")
	result, err := c.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return nil, err
	}
	if !insightsARMReadValid(result, groupID, groupType) {
		return nil, serviceDenied("invalid_workbook_resource_group")
	}
	if _, err := insightsManagedBy(result.data); err != nil {
		return nil, err
	}
	return result.data, nil
}

func (c *client) workbookInventory(ctx context.Context, id, kind string, raw, normalized map[string]any) error {
	if insightsWorkbookKind(kind) == "" {
		return nil
	}
	record, err := c.workbookRecord(ctx, kind, id)
	if err != nil {
		return err
	}
	if !nativeConfigurationContains(insightsWorkbookSnapshot(raw, false), insightsWorkbookSnapshot(record.raw, false)) {
		return serviceDenied("workbook_inventory_changed")
	}
	group, err := c.workbookGroup(ctx, id)
	if err != nil {
		return err
	}
	normalized[insightsWorkbookProof] = c.workbookConfiguration(record)
	normalized["_insights_workbook_current_configuration"] = c.privateConfiguration(insightsWorkbookSnapshot(record.raw, false))
	normalized["_insights_workbook_group"] = c.privateConfiguration(insightsWorkspaceResourceSnapshot(group))
	if kind == insightsWorkbookType {
		var revisions []any
		for _, revision := range slices.Sorted(maps.Keys(record.revisions)) {
			revisions = append(revisions, safePayload(object(applicationInsightsSafeValue(record.revisions[revision]))))
		}
		normalized["revisions"] = revisions
	}
	if protectedAzureTags(object(group["tags"])) {
		normalized["cleanup_protected"], normalized["cleanup_protection_reason"] = true, "azure_protected_tag"
	}
	if text(group["managedBy"]) != "" && normalized["cleanup_protected"] != true {
		normalized["cleanup_controller_only"], normalized["cleanup_protection_reason"] = true, "azure_managed_resource_group"
	}
	return nil
}

func workbookReferences(kind, self string, raw map[string]any) map[string][]string {
	refs := map[string][]string{}
	if kind == insightsWorkbookTemplateType {
		return refs
	}
	add := func(value string) {
		id, typ, err := parseID(value)
		if err != nil || id == self {
			return
		}
		if mapping, known := findType(typ); known {
			typ = mapping.NativeType
		}
		addReference(refs, typ, id)
	}
	properties := object(raw["properties"])
	add(text(properties["sourceId"]))
	add(text(properties["storageUri"]))
	for id := range object(object(raw["identity"])["userAssignedIdentities"]) {
		add(id)
	}
	return refs
}
