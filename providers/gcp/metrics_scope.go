package gcp

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	metricsHost          = "monitoring.googleapis.com"
	metricsScopeType     = metricsHost + "/MetricsScope"
	monitoredProjectType = metricsHost + "/MonitoredProject"
	metricsGet           = "monitoring.locations.global.metricsScopes.get"
	metricsReverse       = "monitoring.locations.global.metricsScopes.listMetricsScopesByMonitoredProject"
	metricsDelete        = "monitoring.locations.global.metricsScopes.projects.delete"
	metricsParentTime    = "_metrics_scope_create_time"
)

func isMetricsScope(kind string) bool {
	return kind == metricsScopeType || kind == monitoredProjectType
}

// Metrics Scope names place the owning project after metricsScopes, not projects.
// Keep their native output numbers, including the independently numbered target.
func (c *client) metricsID(kind, value string, local bool) (string, error) {
	if strings.HasPrefix(value, "https://") {
		u, err := url.Parse(value)
		if err != nil || u.Host != metricsHost || u.User != nil || u.RawQuery != "" || u.Fragment != "" || !strings.HasPrefix(u.Path, "/v1/locations/") {
			return "", groupDenied("metrics_scope_endpoint_invalid")
		}
		value = strings.TrimPrefix(u.Path, "/v1/")
	}
	name := strings.TrimPrefix(value, "//"+metricsHost+"/")
	p := strings.Split(name, "/")
	length := 4
	if kind == monitoredProjectType {
		length = 6
	}
	if !isMetricsScope(kind) || len(p) != length || p[0] != "locations" || p[1] != "global" || p[2] != "metricsScopes" || (length == 6 && p[4] != "projects") {
		return "", groupDenied("metrics_scope_identity_invalid")
	}
	for _, part := range p {
		if !segmentPattern.MatchString(part) || part == "." || part == ".." {
			return "", groupDenied("metrics_scope_path_invalid")
		}
	}
	if local && (c.number == "" || (p[3] != c.project && p[3] != c.number)) {
		return "", groupDenied("metrics_scope_project_changed")
	}
	if c.number != "" {
		if p[3] == c.project {
			p[3] = c.number
		}
		if length == 6 && p[5] == c.project {
			p[5] = c.number
		}
	}
	return "//" + metricsHost + "/" + strings.Join(p, "/"), nil
}

func (c *client) metricsOperation(kind, id, method string) (catalog.Operation, map[string]any, error) {
	canonical, err := c.metricsID(kind, id, true)
	if err != nil || canonical != id {
		return catalog.Operation{}, nil, groupDenied("metrics_scope_identity_not_canonical")
	}
	name := strings.TrimPrefix(id, "//"+metricsHost+"/")
	op := metricsGet
	if method == "DELETE" {
		if kind != monitoredProjectType || last(id) == c.number {
			return catalog.Operation{}, nil, groupDenied("metrics_scope_self_protected")
		}
		op = metricsDelete
	} else if method != "GET" {
		return catalog.Operation{}, nil, groupDenied("metrics_scope_method_invalid")
	} else if kind == monitoredProjectType {
		name = strings.Join(strings.Split(name, "/")[:4], "/")
	}
	metadata, err := providerData()
	operation, ok := metadata.catalog.Operation(op)
	if err != nil || !ok {
		return catalog.Operation{}, nil, groupDenied("metrics_scope_method_missing")
	}
	return operation, map[string]any{"name": name}, nil
}

func metricsComplete(data map[string]any) error {
	if err := checkListCompleteness(data); err != nil {
		return err
	}
	// Neither scope method accepts paging. A continuation cannot be discarded.
	if value, present := data["nextPageToken"]; present && value != "" {
		return groupDenied("metrics_scope_unexpected_pagination")
	}
	return nil
}

func (c *client) metricsIdentity(kind, id string, data map[string]any) error {
	actual, err := c.metricsID(kind, text(data["name"]), true)
	if err != nil || actual != id {
		return groupDenied("metrics_scope_identity_changed")
	}
	if _, err := time.Parse(time.RFC3339Nano, text(data["createTime"])); err != nil {
		return groupDenied("metrics_scope_creation_time_missing")
	}
	if value, present := data["updateTime"]; present {
		if _, err := time.Parse(time.RFC3339Nano, text(value)); err != nil {
			return groupDenied("metrics_scope_update_time_invalid")
		}
	}
	if value, present := data["isTombstoned"]; present {
		if _, ok := value.(bool); !ok {
			return groupDenied("metrics_scope_tombstone_invalid")
		}
	}
	return metricsComplete(data)
}

func (c *client) metricsRead(ctx context.Context, id string) (contracts.InvocationResult, error) {
	kind, _ := findType(metricsScopeType)
	endpoint, err := c.resourceURL(kind, id)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	result, err := c.requestResult(ctx, "GET", endpoint, nil, nil)
	if err != nil {
		return result, err
	}
	if err := c.metricsIdentity(metricsScopeType, id, result.Data); err != nil {
		return result, err
	}
	if _, err := c.metricsMembers(id, result.Data); err != nil {
		return result, err
	}
	return result, nil
}

func (c *client) metricsMembers(parent string, data map[string]any) (map[string]map[string]any, error) {
	values, present := data["monitoredProjects"]
	list, ok := values.([]any)
	if present && !ok {
		return nil, groupDenied("metrics_scope_members_invalid")
	}
	members := map[string]map[string]any{}
	for _, value := range list {
		data, ok := value.(map[string]any)
		if !ok {
			return nil, groupDenied("metrics_scope_member_invalid")
		}
		id, err := c.metricsID(monitoredProjectType, text(data["name"]), true)
		if err != nil || !strings.HasPrefix(id, parent+"/projects/") {
			return nil, groupDenied("metrics_scope_member_parent_changed")
		}
		if members[id] != nil {
			return nil, groupDenied("metrics_scope_duplicate_member")
		}
		if err := c.metricsIdentity(monitoredProjectType, id, data); err != nil {
			return nil, err
		}
		members[id] = data
	}
	// The scope always monitors its owning project. Missing self membership is
	// incomplete authority, even if the HTTP response was successful.
	if members[parent+"/projects/"+c.number] == nil {
		return nil, groupDenied("metrics_scope_self_missing")
	}
	return members, nil
}

func (r *Runtime) listMetricsScope(ctx context.Context, c *client, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{}, Complete: true}
	if request.Cursor != "" {
		return contracts.InventoryBatch{}, groupDenied("metrics_scope_cursor_invalid")
	}
	if request.Scope.Kind == asset.ScopeRegion {
		return batch, nil
	}
	if request.Scope.Kind != asset.ScopeGlobal && request.Scope.Kind != asset.ScopeProject {
		return contracts.InventoryBatch{}, groupDenied("metrics_scope_scan_scope_invalid")
	}
	if request.Scope.Kind == asset.ScopeGlobal && request.Scope.NativeID != "global" && request.Scope.NativeID != c.project+"/global" && request.Scope.NativeID != c.number+"/global" {
		return contracts.InventoryBatch{}, groupDenied("metrics_scope_scan_project_changed")
	}
	id, err := c.metricsID(metricsScopeType, "locations/global/metricsScopes/"+c.project, true)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	first, err := c.metricsRead(ctx, id)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	var inbound []string
	if request.ResourceKind.NativeType == metricsScopeType {
		result, err := r.Invoke(ctx, contracts.Invocation{ConnectionID: request.ConnectionID, Operation: metricsReverse, Parameters: map[string]any{"monitoredResourceContainer": "projects/" + c.number}})
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		if err := metricsComplete(result.Data); err != nil {
			return contracts.InventoryBatch{}, err
		}
		values, ok := result.Data["metricsScopes"].([]any)
		if !ok || len(values) == 0 {
			return contracts.InventoryBatch{}, groupDenied("metrics_scope_reverse_missing")
		}
		seen := map[string]bool{}
		for i, value := range values {
			other, err := c.metricsID(metricsScopeType, text(object(value)["name"]), false)
			if err != nil || seen[other] || (i == 0 && other != id) {
				return contracts.InventoryBatch{}, groupDenied("metrics_scope_reverse_invalid")
			}
			seen[other] = true
			inbound = append(inbound, other)
		}
		slices.Sort(inbound)
	}
	second, err := c.metricsRead(ctx, id)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	// A single GET returns the entire member set. Sort by canonical identities
	// before comparing so harmless provider array ordering does not invalidate it.
	firstMembers, _ := c.metricsMembers(id, first.Data)
	members, _ := c.metricsMembers(id, second.Data)
	a, _ := json.Marshal(firstMembers)
	b, _ := json.Marshal(members)
	if string(a) != string(b) || first.Data["createTime"] != second.Data["createTime"] || first.Data["updateTime"] != second.Data["updateTime"] {
		return contracts.InventoryBatch{}, groupDenied("metrics_scope_snapshot_changed")
	}
	kind := request.ResourceKind.NativeType
	if kind == metricsScopeType {
		members = map[string]map[string]any{id: second.Data}
	}
	var ids []string
	for key := range members {
		ids = append(ids, key)
	}
	slices.Sort(ids)
	for _, key := range ids {
		item, err := r.inventoryItem(c, map[string]any{"assetType": kind, "name": key, "resource": map[string]any{"location": "global", "data": members[key]}})
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		item.Normalized["_inventory_source"] = productInventorySource
		item.Normalized[metricsParentTime] = second.Data["createTime"]
		if kind == metricsScopeType {
			item.Normalized["monitored_by_scopes"] = inbound
		}
		batch.Items = append(batch.Items, item)
	}
	batch.RequestID = second.RequestID
	return batch, nil
}
