package gcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) firewallPolicies(ctx context.Context, kind string, containers []firewallContainer) ([]map[string]any, error) {
	if kind == networkFirewallPolicyType {
		return c.firewallList(ctx, "compute.networkFirewallPolicies.aggregatedList", map[string]any{"project": c.project, "includeAllScopes": true, "maxResults": 500}, "items.*.firewallPolicies")
	}
	var result []map[string]any
	for _, parent := range containers {
		rows, err := c.firewallList(ctx, "compute.firewallPolicies.list", map[string]any{"parentId": parent.Name, "maxResults": 500}, "items")
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			if row["parent"] != parent.Name {
				return nil, groupDenied("firewall_list_parent_changed")
			}
		}
		result = append(result, rows...)
	}
	return result, nil
}

func (c *client) firewallListedPolicies(kind string, rows []map[string]any) (map[string]map[string]any, error) {
	result := map[string]map[string]any{}
	for _, row := range rows {
		id := c.canonicalName(text(row["selfLink"]))
		if err := c.firewallPolicyIdentity(kind, id, row); err != nil {
			return nil, err
		}
		if result[id] != nil {
			return nil, groupDenied("firewall_policy_list_duplicate")
		}
		if kind == networkFirewallPolicyType {
			scope := text(row["_firewall_list_scope"])
			if !strings.HasPrefix(id, "//compute.googleapis.com/projects/"+c.project+"/"+scope+"/firewallPolicies/") {
				return nil, groupDenied("firewall_aggregate_identity_changed")
			}
		}
		result[id] = row
	}
	return result, nil
}

func (r *Runtime) listFirewall(ctx context.Context, c *client, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{}, Complete: true}
	if request.ResourceKind == nil || !isFirewall(request.ResourceKind.NativeType) {
		return batch, groupDenied("firewall_inventory_kind_invalid")
	}
	kind, parentKind := request.ResourceKind.NativeType, firewallParentType(request.ResourceKind.NativeType)
	if request.Scope.Kind != asset.ScopeProject && request.Scope.Kind != asset.ScopeRegion && request.Scope.Kind != asset.ScopeGlobal {
		return batch, groupDenied("firewall_inventory_scope_invalid")
	}
	if request.Scope.Kind == asset.ScopeGlobal && !slices.Contains([]string{"global", c.project + "/global", c.number + "/global"}, request.Scope.NativeID) {
		return batch, groupDenied("firewall_inventory_project_changed")
	}
	if parentKind == firewallPolicyType && (request.Scope.Kind == asset.ScopeRegion || c.firewallParent == "") {
		if request.Cursor != "" {
			return batch, groupDenied("firewall_inventory_scope_changed")
		}
		return batch, nil
	}
	var containers []firewallContainer
	var err error
	if parentKind == firewallPolicyType {
		containers, err = c.firewallContainers(ctx)
		if err != nil {
			return batch, err
		}
	}
	rows, err := c.firewallPolicies(ctx, parentKind, containers)
	if err != nil {
		return batch, err
	}
	listed, err := c.firewallListedPolicies(parentKind, rows)
	if err != nil {
		return batch, err
	}
	var ids []string
	for id := range listed {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	var proofs []string
	for _, id := range ids {
		raw := map[string]any{"name": id, "assetType": parentKind, "resource": map[string]any{"data": listed[id]}}
		_, region := assetLocation(raw)
		if request.Scope.Kind == asset.ScopeGlobal && region != "global" || request.Scope.Kind == asset.ScopeRegion && region != request.Scope.NativeID && !(request.NetworkTarget != nil && region == "global") {
			continue
		}
		policy, err := c.firewallReadPolicy(ctx, parentKind, id)
		if err != nil {
			return batch, err
		}
		if firewallConfiguration(listed[id], false) != policy[firewallProof] {
			return batch, groupDenied("firewall_policy_list_detail_changed")
		}
		children, err := c.firewallSnapshot(ctx, parentKind, id, policy)
		if err != nil {
			return batch, err
		}
		proofs = append(proofs, id+"\n"+text(policy[firewallSnapshotKey]))
		resources := children
		if isFirewallPolicy(kind) {
			resources = map[string]map[string]any{id: policy}
		}
		for resourceID, data := range resources {
			item, err := r.inventoryItem(c, map[string]any{"name": resourceID, "assetType": kind, "resource": map[string]any{"data": data, "location": region}})
			if err != nil {
				return batch, err
			}
			item.Normalized["_inventory_source"] = productInventorySource
			if parentKind == firewallPolicyType {
				item.Normalized["_inventory_source"] = firewallInventorySource
				delete(item.Normalized, "project_id")
				delete(item.Normalized, "project_number")
				if isFirewallPolicy(kind) && text(data["shortName"]) != "" {
					item.Name = text(data["shortName"])
				}
			}
			batch.Items = append(batch.Items, item)
		}
	}
	if parentKind == firewallPolicyType {
		again, err := c.firewallContainers(ctx)
		if err != nil {
			return batch, err
		}
		if !slices.Equal(containers, again) {
			return batch, groupDenied("firewall_inventory_tree_changed")
		}
	}
	rows, err = c.firewallPolicies(ctx, parentKind, containers)
	if err != nil {
		return batch, err
	}
	again, err := c.firewallListedPolicies(parentKind, rows)
	if err != nil {
		return batch, err
	}
	if len(listed) != len(again) {
		return batch, groupDenied("firewall_inventory_policy_set_changed")
	}
	for id, row := range listed {
		if again[id] == nil || firewallConfiguration(row, false) != firewallConfiguration(again[id], false) {
			return batch, groupDenied("firewall_inventory_policy_changed")
		}
	}
	slices.SortFunc(batch.Items, func(a, b contracts.InventoryItem) int { return strings.Compare(a.NativeID, b.NativeID) })
	// ponytail: materialize the policy set to bind each cursor to one reviewed
	// snapshot; use durable snapshot storage if very large hierarchies need paging.
	fingerprint := firewallDigest(struct {
		Connection             asset.ConnectionID
		Scope                  asset.Scope
		Kind, Parent, Revision string
		Network                *asset.ScanTarget
		Containers             []firewallContainer
		Proofs                 []string
	}{request.ConnectionID, request.Scope, kind, c.firewallParent, r.bundle.Revision, request.NetworkTarget, containers, proofs})
	cursor := struct {
		Fingerprint string
		Offset      int
	}{Fingerprint: fingerprint}
	if request.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if len(request.Cursor) > 4096 || err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Fingerprint != fingerprint || cursor.Offset <= 0 || cursor.Offset >= len(batch.Items) {
			return contracts.InventoryBatch{}, groupDenied("firewall_inventory_cursor_changed")
		}
	}
	limit := request.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	end := min(cursor.Offset+limit, len(batch.Items))
	batch.Complete = end == len(batch.Items)
	batch.Items = slices.Clone(batch.Items[cursor.Offset:end])
	if !batch.Complete {
		cursor.Offset = end
		raw, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return batch, nil
}
