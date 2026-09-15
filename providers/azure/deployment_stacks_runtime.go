package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const deploymentStackSource = "deployment-stacks"

func (r *Runtime) deploymentStackSnapshot(ctx context.Context, c *client, req contracts.InventoryRequest) ([]contracts.InventoryItem, []string, error) {
	scopes := map[string][]string{strings.ToLower(c.root()): {}}
	seenKnown := map[string]bool{}
	for _, id := range req.KnownNativeIDs {
		if seenKnown[strings.ToLower(id)] {
			continue
		}
		seenKnown[strings.ToLower(id)] = true
		kind, params, err := deploymentStackParameters(id)
		if err != nil || kind == "ManagementGroup" || !strings.EqualFold(text(params["subscriptionId"]), c.subscription) {
			return nil, nil, serviceDenied("invalid_deployment_stack_known_scope")
		}
		scope := strings.ToLower(id[:strings.LastIndex(strings.ToLower(id), "/providers/microsoft.resources/deploymentstacks/")])
		scopes[scope] = append(scopes[scope], id)
	}
	groups, err := c.deploymentStackGroups(ctx)
	if err != nil {
		return nil, nil, err
	}
	listed := map[string]bool{}
	for _, value := range groups {
		raw := object(value)
		id, kind, err := parseID(text(raw["id"]))
		if err != nil || !strings.EqualFold(kind, groupType) || !strings.HasPrefix(id, strings.ToLower(c.root())+"/") || !strings.EqualFold(text(raw["type"]), groupType) || listed[id] {
			return nil, nil, serviceDenied("invalid_deployment_stack_group_index")
		}
		listed[id] = true
		if scopes[id] == nil {
			scopes[id] = []string{}
		}
	}
	ordered := []string{}
	for id := range scopes {
		ordered = append(ordered, id)
	}
	sort.Strings(ordered)
	items := []contracts.InventoryItem{}
	absent := []string{}
	for _, scope := range ordered {
		if !strings.EqualFold(scope, c.root()) {
			res, err := c.request(ctx, "GET", apiURL(scope, resourcesVersion))
			if err != nil {
				if !isNotFound(err) || listed[scope] {
					return nil, nil, err
				}
				// A missing parent does not by itself prove that a known child is absent.
				for _, id := range scopes[scope] {
					_, ownErr := c.deploymentStackRead(ctx, id)
					if !isNotFound(ownErr) {
						if ownErr != nil {
							return nil, nil, ownErr
						}
						return nil, nil, serviceDenied("deployment_stack_parent_changed")
					}
					absent = append(absent, strings.ToLower(id))
				}
				continue
			}
			if res.status != 200 || !strings.EqualFold(text(res.data["id"]), scope) || !strings.EqualFold(text(res.data["type"]), groupType) || res.data["error"] != nil || operationLocation(res.header) != "" {
				return nil, nil, serviceDenied("invalid_deployment_stack_group_read")
			}
		}
		rows, gone, err := c.deploymentStackInventory(ctx, scope, scopes[scope])
		if err != nil {
			return nil, nil, err
		}
		absent = append(absent, gone...)
		for _, raw := range rows {
			id := strings.ToLower(text(raw["id"]))
			state := text(object(raw["properties"])["provisioningState"])
			safe := object(deploymentStackSafeValue(raw))
			normalized := map[string]any{"name": last(id), "state": state, "subscriptionId": c.subscription, "scope_id": scope, "_inventory_source": deploymentStackSource, "_deployment_stack_review": raw["_deployment_stack_review"], "cleanup_protected": true, "cleanup_protection_reason": "deployment_stack_cleanup_not_implemented"}
			actionable := false
			items = append(items, contracts.InventoryItem{NativeID: id, NativeType: deploymentStackType, ResourceKind: r.resourceKind(deploymentStackType), Name: last(id), State: state, Location: text(raw["location"]), Scope: contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: "global", Name: "Global"}, Normalized: normalized, Raw: safe, NativeAliases: []string{id}, Actionable: &actionable})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].NativeID < items[j].NativeID })
	sort.Strings(absent)
	return items, absent, nil
}

func (r *Runtime) listDeploymentStacks(ctx context.Context, c *client, req contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if req.ResourceKind == nil || !strings.EqualFold(req.ResourceKind.NativeType, deploymentStackType) || req.Source != deploymentStackSource || len(req.Options) != 0 || req.NetworkTarget != nil || !(req.Scope.Kind == asset.ScopeSubscription && strings.EqualFold(req.Scope.NativeID, c.subscription) || req.Scope.Kind == asset.ScopeGlobal && req.Scope.NativeID == "global") {
		return batch, serviceDenied("invalid_deployment_stack_inventory_request")
	}
	cursor := productCursor{}
	if req.Cursor != "" {
		wire, e := base64.RawURLEncoding.DecodeString(req.Cursor)
		if e != nil || len(req.Cursor) > 128<<10 || json.Unmarshal(wire, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_deployment_stack_cursor")
		}
	}
	first, absent, err := r.deploymentStackSnapshot(ctx, c, req)
	if err != nil {
		return batch, err
	}
	second, gone, err := r.deploymentStackSnapshot(ctx, c, req)
	if err != nil {
		return batch, err
	}
	observation := map[string]any{"items": first, "absent": absent}
	if c.privateConfiguration(observation) != c.privateConfiguration(map[string]any{"items": second, "absent": gone}) {
		return batch, serviceDenied("deployment_stack_snapshot_changed")
	}
	boundary := req
	boundary.Cursor = ""
	boundary.Limit = 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "observation": observation})
	if cursor.Fingerprint != "" && cursor.Fingerprint != fingerprint || cursor.Target > len(first) {
		return batch, serviceDenied("deployment_stack_cursor_changed")
	}
	limit := req.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	end := min(cursor.Target+limit, len(first))
	batch.Items = first[cursor.Target:end]
	batch.Complete = end == len(first)
	if batch.Complete {
		batch.AbsentNativeIDs = absent
	} else {
		wire, _ := json.Marshal(productCursor{Target: end, Fingerprint: fingerprint})
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(wire)
	}
	return batch, nil
}

func (c *client) deploymentStackGroups(ctx context.Context) ([]any, error) {
	path := c.root() + "/resourcegroups"
	next := apiURL(path, resourcesVersion)
	rows := []any{}
	seen := map[string]bool{}
	for next != "" {
		u, err := url.Parse(next)
		if err != nil || seen[next] {
			return nil, serviceDenied("invalid_deployment_stack_group_page")
		}
		seen[next] = true
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || query.Get("api-version") != resourcesVersion {
			return nil, serviceDenied("invalid_deployment_stack_group_page_version")
		}
		for key, values := range query {
			if len(values) != 1 || key != "api-version" && key != "$skiptoken" && key != "skipToken" {
				return nil, serviceDenied("filtered_deployment_stack_group_page")
			}
		}
		page, cursor, res, err := c.listPageResult(ctx, next, path)
		if err != nil {
			return nil, err
		}
		if operationLocation(res.header) != "" {
			return nil, serviceDenied("incomplete_deployment_stack_group_page")
		}
		rows = append(rows, page...)
		next = cursor
	}
	return rows, nil
}
