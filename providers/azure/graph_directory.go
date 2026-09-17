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

// Microsoft Entra users and groups are Microsoft Graph directory objects, the
// equivalent of Alibaba Cloud RAM users and groups. They are read with a
// separate graph.microsoft.com token and inventoried read-only: deleting a
// directory object affects every subscription and application in the tenant.
// Only an object's own 404 closes a previously observed record.
const graphUserType = "Microsoft.Graph/users"
const graphGroupType = "Microsoft.Graph/groups"
const graphDirectorySource = "microsoft-graph-directory"
const graphOrigin = "https://graph.microsoft.com"

var graphObjectID = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type graphKind struct {
	collection, list, get, idParameter string
	fields                             []string
}

var graphKinds = map[string]graphKind{
	graphUserType:  {"users", "Azure.Microsoft.Graph.users.user.ListUser", "Azure.Microsoft.Graph.users.user.GetUser", "userId", []string{"id", "displayName", "userPrincipalName", "accountEnabled", "userType", "createdDateTime", "onPremisesSyncEnabled"}},
	graphGroupType: {"groups", "Azure.Microsoft.Graph.groups.group.ListGroup", "Azure.Microsoft.Graph.groups.group.GetGroup", "groupId", []string{"id", "displayName", "securityEnabled", "mailEnabled", "groupTypes", "createdDateTime", "onPremisesSyncEnabled", "isAssignableToRole"}},
}

func graphKindOf(nativeType string) (graphKind, bool) {
	kind, ok := graphKinds[nativeType]
	return kind, ok
}

// graphIdentity maps a native directory identity to its object ID. Identities
// use the tenant-qualified principal selector shared with RBAC references.
func (c *client) graphIdentity(value string) (string, error) {
	objectID, found := strings.CutPrefix(value, "principal-id:"+c.tenant+"/")
	if !found || !graphObjectID.MatchString(objectID) {
		return "", serviceDenied("invalid_graph_directory_identity")
	}
	return objectID, nil
}

// graphURL admits only the selected collections and object reads with the
// fixed projection and native paging parameters.
func graphURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || u.Host != "graph.microsoft.com" || u.User != nil || u.Fragment != "" || u.RawPath != "" {
		return serviceDenied("graph_request_changed_origin")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	valid := len(parts) >= 2 && parts[0] == "v1.0" && (parts[1] == "users" || parts[1] == "groups") &&
		(len(parts) == 2 || len(parts) == 3 && graphObjectID.MatchString(parts[2]) || len(parts) == 4 && parts[1] == "groups" && graphObjectID.MatchString(parts[2]) && parts[3] == "members")
	if !valid {
		return serviceDenied("graph_request_changed_path")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return serviceDenied("graph_request_changed_query")
	}
	for key, values := range query {
		if len(values) != 1 || key != "$select" && key != "$top" && key != "$skiptoken" {
			return serviceDenied("graph_request_filtered")
		}
	}
	return nil
}

func (c *client) graphRequest(ctx context.Context, operationID string, parameters map[string]any) (response, error) {
	metadata, err := providerData()
	if err != nil {
		return response{}, err
	}
	operation, ok := metadata.catalog.Operation(operationID)
	if !ok || operation.Call == nil || operation.Call.Style != "azure-graph-rest" || operation.Call.Method != "GET" {
		return response{}, serviceDenied("graph_native_contract_missing")
	}
	request, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return response{}, err
	}
	return c.requestUsing(ctx, "GET", request.URL, nil, nil, graphURL, c.graphHTTP, false)
}

func (c *client) graphPages(ctx context.Context, first response, collection string) ([]map[string]any, error) {
	var result []map[string]any
	seen := map[string]bool{}
	for page := first; ; {
		values, ok := page.data["value"].([]any)
		if page.status != 200 || !ok {
			return nil, serviceDenied("graph_list_incomplete")
		}
		for _, value := range values {
			row := object(value)
			if row == nil {
				return nil, serviceDenied("graph_list_incomplete")
			}
			result = append(result, row)
		}
		next := text(page.data["@odata.nextLink"])
		if next == "" {
			return result, nil
		}
		u, err := url.Parse(next)
		if err != nil || u.Path != collection || seen[next] || len(seen) > 100000 {
			return nil, serviceDenied("graph_pagination_changed_collection")
		}
		seen[next] = true
		if page, err = c.requestUsing(ctx, "GET", next, nil, nil, graphURL, c.graphHTTP, false); err != nil {
			return nil, err
		}
	}
}

func graphProjection(raw map[string]any, fields []string) map[string]any {
	result := map[string]any{}
	for _, field := range fields {
		if value, ok := raw[field]; ok && value != nil {
			result[field] = value
		}
	}
	return result
}

func (r *Runtime) graphItem(ctx context.Context, c *client, nativeType string, raw map[string]any) (contracts.InventoryItem, error) {
	kind := graphKinds[nativeType]
	objectID := strings.ToLower(text(raw["id"]))
	if !graphObjectID.MatchString(objectID) {
		return contracts.InventoryItem{}, serviceDenied("graph_object_identity_invalid")
	}
	id := rbacPrincipalSelector(c.tenant, objectID)
	projection := graphProjection(raw, kind.fields)
	name := text(raw["displayName"])
	if name == "" {
		name = objectID
	}
	state := ""
	if enabled, ok := raw["accountEnabled"].(bool); ok {
		state = map[bool]string{true: "enabled", false: "disabled"}[enabled]
	}
	normalized := map[string]any{
		"_inventory_source": graphDirectorySource, "name": name, "object_id": objectID, "tenant_id": c.tenant,
		"cleanup_protected": true, "cleanup_protection_reason": "entra_directory_object_read_only",
	}
	for key, value := range projection {
		if key != "id" {
			normalized[key] = value
		}
	}
	if nativeType == graphGroupType {
		first, err := c.graphRequest(ctx, "Azure.Microsoft.Graph.groups.ListMembers", map[string]any{"groupId": objectID, "$select": "id", "$top": 999})
		if err != nil {
			return contracts.InventoryItem{}, err
		}
		members, err := c.graphPages(ctx, first, "/v1.0/groups/"+objectID+"/members")
		if err != nil {
			return contracts.InventoryItem{}, err
		}
		refs := map[string][]string{}
		for _, member := range members {
			memberType := map[string]string{"#microsoft.graph.user": graphUserType, "#microsoft.graph.group": graphGroupType}[text(member["@odata.type"])]
			memberID := strings.ToLower(text(member["id"]))
			if memberType == "" || !graphObjectID.MatchString(memberID) {
				continue // Devices, service principals and contacts are not inventoried here.
			}
			refs[memberType] = append(refs[memberType], rbacPrincipalSelector(c.tenant, memberID))
		}
		for memberType, ids := range refs {
			slices.Sort(ids)
			normalized[referenceKey(memberType)] = slices.Compact(ids)
		}
	}
	actionable := false
	return contracts.InventoryItem{
		NativeID: id, NativeType: nativeType, ResourceKind: r.resourceKind(nativeType), Name: name, State: state, Location: "global",
		Scope:      contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: c.subscription + "/global", Name: "Global", Location: "global"},
		Normalized: normalized, Raw: projection, NativeAliases: []string{id}, Actionable: &actionable,
	}, nil
}

func (r *Runtime) listGraphDirectory(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	kind, ok := graphKind{}, false
	if request.ResourceKind != nil {
		kind, ok = graphKindOf(request.ResourceKind.NativeType)
	}
	if !ok || request.Source != graphDirectorySource || request.Cursor != "" || request.NetworkTarget != nil || len(request.Options) != 0 ||
		request.Scope.Kind != asset.ScopeSubscription && request.Scope.Kind != asset.ScopeGlobal ||
		request.Scope.Kind == asset.ScopeGlobal && request.Scope.NativeID != "global" && request.Scope.NativeID != c.subscription+"/global" {
		return batch, serviceDenied("invalid_graph_directory_inventory_request")
	}
	nativeType := request.ResourceKind.NativeType
	known := map[string]bool{}
	for _, value := range request.KnownNativeIDs {
		objectID, err := c.graphIdentity(value)
		if err != nil || known[objectID] {
			return batch, serviceDenied("invalid_graph_directory_known_identity")
		}
		known[objectID] = true
	}
	first, err := c.graphRequest(ctx, kind.list, map[string]any{"$select": strings.Join(kind.fields, ","), "$top": 999})
	if err != nil {
		return batch, err
	}
	rows, err := c.graphPages(ctx, first, "/v1.0/"+kind.collection)
	if err != nil {
		return batch, err
	}
	batch = contracts.InventoryBatch{Items: []contracts.InventoryItem{}, Complete: true, RequestID: first.requestID}
	listed := map[string]bool{}
	for _, raw := range rows {
		objectID := strings.ToLower(text(raw["id"]))
		if listed[objectID] {
			return contracts.InventoryBatch{}, serviceDenied("duplicate_graph_directory_object")
		}
		listed[objectID] = true
		item, err := r.graphItem(ctx, c, nativeType, raw)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		batch.Items = append(batch.Items, item)
	}
	missing := make([]string, 0, len(known))
	for objectID := range known {
		if !listed[objectID] {
			missing = append(missing, objectID)
		}
	}
	slices.Sort(missing)
	for _, objectID := range missing {
		res, err := c.graphRequest(ctx, kind.get, map[string]any{kind.idParameter: objectID, "$select": strings.Join(kind.fields, ",")})
		if isNotFound(err) {
			batch.AbsentNativeIDs = append(batch.AbsentNativeIDs, rbacPrincipalSelector(c.tenant, objectID))
			continue
		}
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		if !strings.EqualFold(text(res.data["id"]), objectID) {
			return contracts.InventoryBatch{}, serviceDenied("graph_object_identity_changed")
		}
		item, err := r.graphItem(ctx, c, nativeType, res.data)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		batch.Items = append(batch.Items, item)
	}
	return batch, nil
}
