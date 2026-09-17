package azure

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Management groups form the tenant's resource directory above subscriptions,
// the Azure equivalent of an Alibaba Cloud resource directory. They are
// inventoried read-only: the visible directory is not proof of the whole tree,
// and only a saved group's own 404 closes it.
const managementGroupType = "Microsoft.Management/managementGroups"
const managementGroupSource = "management-groups-visible"
const managementGroupCollection = "/providers/Microsoft.Management/managementGroups"

var managementGroupName = regexp.MustCompile(`^[A-Za-z0-9_().-]{1,90}$`)

func managementGroupIdentity(nativeID string) (string, string, error) {
	id := strings.ToLower(strings.TrimSpace(nativeID))
	prefix := strings.ToLower(managementGroupCollection) + "/"
	name := strings.TrimPrefix(id, prefix)
	if nativeID != strings.TrimSpace(nativeID) || name == id || !managementGroupName.MatchString(name) {
		return "", "", serviceDenied("invalid_management_group_identity")
	}
	return id, name, nil
}

func managementGroupOperation(nativeID, method string) (catalog.Operation, map[string]any, error) {
	_, name, err := managementGroupIdentity(nativeID)
	if err != nil || method != "GET" {
		return catalog.Operation{}, nil, serviceDenied("invalid_management_group_operation")
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	operation, ok := metadata.catalog.Operation("Azure.Microsoft.Management.ManagementGroups_Get")
	if !ok || operation.Call == nil {
		return catalog.Operation{}, nil, serviceDenied("management_group_native_contract_missing")
	}
	return operation, map[string]any{"groupId": name}, nil
}

func (c *client) readManagementGroup(ctx context.Context, id string) (response, error) {
	operation, parameters, err := managementGroupOperation(id, "GET")
	if err != nil {
		return response{}, err
	}
	request, err := bindAzureREST(operation, parameters)
	if err != nil {
		return response{}, err
	}
	res, err := c.requestAt(ctx, "GET", request.URL, nil, nil, validateManagementGroupURL)
	if err != nil {
		return res, err
	}
	if !strings.EqualFold(text(res.data["id"]), id) || !strings.EqualFold(text(res.data["type"]), managementGroupType) {
		return response{}, serviceDenied("management_group_identity_changed")
	}
	if tenant := text(object(res.data["properties"])["tenantId"]); tenant != "" && !strings.EqualFold(tenant, c.tenant) {
		return response{}, serviceDenied("management_group_tenant_changed")
	}
	return res, nil
}

func (r *Runtime) managementGroupItem(c *client, raw map[string]any) contracts.InventoryItem {
	id := strings.ToLower(text(raw["id"]))
	props := object(raw["properties"])
	details := object(props["details"])
	parent := strings.ToLower(text(object(details["parent"])["id"]))
	safeDetails := map[string]any{}
	for _, key := range []string{"parent", "version", "updatedTime", "updatedBy"} {
		if value, ok := details[key]; ok && value != nil {
			safeDetails[key] = value
		}
	}
	safeProperties := map[string]any{"details": safeDetails}
	for _, key := range []string{"displayName", "tenantId"} {
		if value, ok := props[key]; ok && value != nil {
			safeProperties[key] = value
		}
	}
	actionable := false
	normalized := map[string]any{
		"_inventory_source": managementGroupSource, "name": text(raw["name"]), "display_name": text(props["displayName"]),
		"tenant_id": strings.ToLower(text(props["tenantId"])), "parent_id": parent,
		"root": strings.EqualFold(text(raw["name"]), c.tenant),
	}
	if parent != "" {
		normalized["refs_microsoft_management_managementgroups"] = []string{parent}
	}
	name := text(props["displayName"])
	if name == "" {
		name = text(raw["name"])
	}
	return contracts.InventoryItem{
		NativeID: id, NativeType: managementGroupType, ResourceKind: r.resourceKind(managementGroupType), Name: name, Location: "global",
		Scope:      contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: c.subscription + "/global", Name: "Global", Location: "global"},
		Actionable: &actionable, Normalized: normalized, NativeAliases: []string{id},
		Raw: map[string]any{"id": id, "type": managementGroupType, "name": text(raw["name"]), "properties": safeProperties},
	}
}

func (r *Runtime) listManagementGroups(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.Source != managementGroupSource || request.ResourceKind == nil || request.ResourceKind.NativeType != managementGroupType || request.Cursor != "" || request.NetworkTarget != nil || len(request.Options) != 0 {
		return batch, serviceDenied("invalid_management_group_inventory_request")
	}
	if request.Scope.Kind != asset.ScopeSubscription && request.Scope.Kind != asset.ScopeGlobal || request.Scope.Kind == asset.ScopeGlobal && request.Scope.NativeID != "global" && request.Scope.NativeID != c.subscription+"/global" {
		return batch, serviceDenied("invalid_management_group_inventory_scope")
	}
	known := map[string]bool{}
	for _, value := range request.KnownNativeIDs {
		id, _, err := managementGroupIdentity(value)
		if err != nil || known[id] {
			return batch, serviceDenied("invalid_management_group_known_identity")
		}
		known[id] = true
	}
	metadata, err := providerData()
	if err != nil {
		return batch, err
	}
	listOperation, ok := metadata.catalog.Operation("Azure.Microsoft.Management.ManagementGroups_List")
	if !ok || listOperation.Call == nil {
		return batch, serviceDenied("management_group_native_contract_missing")
	}
	values, provenance, err := c.listManagementGroupPages(ctx, apiURL(managementGroupCollection, listOperation.Call.Version))
	if err != nil {
		return batch, err
	}
	batch = contracts.InventoryBatch{Items: []contracts.InventoryItem{}, Complete: true, RequestID: provenance}
	listed := map[string]bool{}
	ids := []string{}
	for _, value := range values {
		id, _, err := managementGroupIdentity(text(object(value)["id"]))
		if err != nil || listed[id] {
			return contracts.InventoryBatch{}, serviceDenied("management_group_list_invalid")
		}
		listed[id] = true
		ids = append(ids, id)
	}
	for id := range known {
		if !listed[id] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	for _, id := range ids {
		res, err := c.readManagementGroup(ctx, id)
		if isNotFound(err) && known[id] {
			batch.AbsentNativeIDs = append(batch.AbsentNativeIDs, id)
			continue
		}
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		batch.Items = append(batch.Items, r.managementGroupItem(c, res.data))
	}
	return batch, nil
}

// Management groups are tenant resources outside the subscription root, so
// they use a validator limited to the management group collection.
func validateManagementGroupURL(endpoint string) error {
	u, err := url.Parse(endpoint)
	path := strings.ToLower(u.Path)
	collection := strings.ToLower(managementGroupCollection)
	if err != nil || u.Scheme != "https" || u.Host != "management.azure.com" || u.User != nil || u.Fragment != "" || strings.ContainsAny(u.Path, "\\\x00\r\n%") ||
		path != collection && !(strings.HasPrefix(path, collection+"/") && managementGroupName.MatchString(strings.TrimPrefix(path, collection+"/"))) {
		return serviceDenied("invalid_management_group_endpoint")
	}
	return nil
}

func (c *client) listManagementGroupPages(ctx context.Context, next string) ([]any, string, error) {
	var items []any
	provenance := ""
	seen := map[string]bool{}
	for next != "" {
		if seen[next] || len(seen) > 1000 {
			return nil, "", serviceDenied("management_group_pagination_repeated")
		}
		seen[next] = true
		if u, err := url.Parse(next); err != nil || !strings.EqualFold(u.Path, managementGroupCollection) {
			return nil, "", serviceDenied("management_group_pagination_changed_collection")
		}
		res, err := c.requestAt(ctx, "GET", next, nil, nil, validateManagementGroupURL)
		if err != nil {
			return nil, "", err
		}
		page, ok := res.data["value"].([]any)
		if res.status != 200 || !ok {
			return nil, "", serviceDenied("management_group_list_incomplete")
		}
		if res.requestID != "" {
			provenance = res.requestID
		}
		items = append(items, page...)
		next = text(res.data["nextLink"])
	}
	return items, provenance, nil
}
