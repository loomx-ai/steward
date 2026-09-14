package gcp

import (
	"context"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const monitoringGroupDelete = "monitoring.projects.groups.delete"
const monitoringGroupType = "monitoring.googleapis.com/Group"
const monitoringGroupReview = "_monitoring_group_configuration"
const monitoringGroupMembers = "_monitoring_group_member_references"
const monitoringGroupUnmapped = "_monitoring_group_unmapped_members"
const monitoringGroupMembersList = "monitoring.projects.groups.members.list"

func (c *client) monitoringGroupData(id string, data map[string]any) error {
	if err := checkListCompleteness(data); err != nil {
		return err
	}
	if err := cloudNatScalars(data, []string{"name", "displayName", "parentName", "filter"}, []string{"isCluster"}, nil, nil); err != nil {
		return err
	}
	if !strings.HasPrefix(text(data["name"]), "projects/") || c.canonicalName("//monitoring.googleapis.com/"+text(data["name"])) != id {
		return groupDenied("monitoring_group_identity_changed")
	}
	kind, _ := findType(monitoringGroupType)
	if _, err := c.resourceURL(kind, id); err != nil {
		return err
	}
	if parent := text(data["parentName"]); parent != "" {
		if !strings.HasPrefix(parent, "projects/") {
			return groupDenied("monitoring_group_parent_invalid")
		}
		parentID := c.canonicalName("//monitoring.googleapis.com/" + parent)
		if parentID == id {
			return groupDenied("monitoring_group_parent_cycle")
		}
		if _, err := c.resourceURL(kind, parentID); err != nil {
			return err
		}
	}
	if text(data["filter"]) == "" {
		return groupDenied("monitoring_group_filter_missing")
	}
	return nil
}

func (c *client) monitoringGroupConfiguration(id string, data map[string]any) string {
	value := cloneParameters(data)
	value["name"] = id
	if parent := text(value["parentName"]); parent != "" {
		value["parentName"] = c.canonicalName("//monitoring.googleapis.com/" + parent)
	}
	return firewallDigest(value)
}

func (c *client) monitoringGroupRead(ctx context.Context, id string) (map[string]any, error) {
	kind, _ := findType(monitoringGroupType)
	endpoint, err := c.resourceURL(kind, id)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	if err := c.monitoringGroupData(id, data); err != nil {
		return nil, err
	}
	return data, nil
}

func (c *client) monitoringGroupInventory(ctx context.Context, id string, listed map[string]any) (map[string]any, error) {
	if err := c.monitoringGroupData(id, listed); err != nil {
		return nil, err
	}
	live, err := c.monitoringGroupRead(ctx, id)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if c.monitoringGroupConfiguration(id, listed) != c.monitoringGroupConfiguration(id, live) {
		return nil, groupDenied("monitoring_group_configuration_changed")
	}
	return live, nil
}

func (c *client) monitoringMembers(ctx context.Context, id, start, end string) (map[string]map[string]any, error) {
	kind, _ := findType(monitoringGroupType)
	if _, err := c.resourceURL(kind, id); err != nil {
		return nil, err
	}
	rows, err := c.batchList(ctx, monitoringGroupMembersList, map[string]any{"name": strings.TrimPrefix(id, "//monitoring.googleapis.com/"), "pageSize": 100, "interval.startTime": start, "interval.endTime": end}, "members")
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	values := map[string]map[string]any{}
	for _, row := range rows {
		if err := cloudNatScalars(row, []string{"type"}, nil, nil, nil); err != nil {
			return nil, err
		}
		if !uptimeSegment(text(row["type"])) {
			return nil, groupDenied("monitoring_group_member_type_invalid")
		}
		if object(row["labels"]) == nil {
			return nil, groupDenied("monitoring_group_member_labels_missing")
		}
		if err := uptimeStringMap(row, "labels"); err != nil {
			return nil, err
		}
		key := firewallDigest(row)
		if values[key] != nil {
			return nil, groupDenied("monitoring_group_member_duplicate")
		}
		values[key] = row
	}
	return values, nil
}

func (c *client) enrichMonitoringGroup(ctx context.Context, item *contracts.InventoryItem, data map[string]any) error {
	// A fixed past minute keeps all pages and the repeated query on one interval.
	// This is a time-bounded observation, never member ownership or cascade proof.
	end := time.Now().UTC().Truncate(time.Second)
	start := end.Add(-time.Minute)
	startText, endText := start.Format(time.RFC3339), end.Format(time.RFC3339)
	members, err := c.monitoringMembers(ctx, item.NativeID, startText, endText)
	if err != nil {
		return err
	}
	refs := map[string][]string{}
	unmapped := map[string]int{}
	keys := make([]string, 0, len(members))
	for key := range members {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		member := members[key]
		targets, err := c.uptimeReferences("", map[string]any{"monitoredResource": member})
		if err != nil {
			return err
		}
		if len(targets) == 0 {
			unmapped[text(member["type"])]++
			continue
		}
		for kind, ids := range targets {
			refs[kind] = append(refs[kind], ids...)
		}
	}
	again, err := c.monitoringMembers(ctx, item.NativeID, startText, endText)
	if err != nil {
		return err
	}
	if firewallDigest(members) != firewallDigest(again) {
		return groupDenied("monitoring_group_members_changed")
	}
	live, err := c.monitoringGroupRead(ctx, item.NativeID)
	if err != nil {
		return contracts.DependencyReadError(err)
	}
	if c.monitoringGroupConfiguration(item.NativeID, data) != c.monitoringGroupConfiguration(item.NativeID, live) {
		return groupDenied("monitoring_group_configuration_changed")
	}
	for kind, ids := range refs {
		slices.Sort(ids)
		refs[kind] = slices.Compact(ids)
		item.NetworkReferences = append(item.NetworkReferences, refs[kind]...)
	}
	slices.Sort(item.NetworkReferences)
	item.NetworkReferences = slices.Compact(item.NetworkReferences)
	item.Normalized[monitoringGroupMembers] = safePayload(map[string]any{"references": refs})["references"]
	item.Normalized[monitoringGroupUnmapped] = safePayload(map[string]any{"unmapped": unmapped})["unmapped"]
	item.Normalized["_monitoring_group_member_configuration"] = firewallDigest(members)
	item.Normalized["_monitoring_group_member_interval"] = map[string]any{"startTime": startText, "endTime": endText}
	return nil
}

func redactMonitoringGroupPayload(data map[string]any) {
	if name := text(data["name"]); strings.HasPrefix(name, "projects/") && strings.Contains(name, "/groups/") {
		if _, ok := data["filter"]; ok {
			data["filter"] = "[REDACTED]"
		}
	}
	// Native monitored-resource labels may carry private descriptor-defined data.
	for _, raw := range array(data["members"]) {
		row := object(raw)
		if text(row["type"]) != "" {
			if _, ok := row["labels"]; ok {
				row["labels"] = "[REDACTED]"
			}
		}
	}
}

func (c *client) monitoringGroupReferences(data map[string]any) (map[string][]string, error) {
	refs := map[string][]string{}
	for kind, raw := range object(data[monitoringGroupMembers]) {
		if err := cloudNatScalars(map[string]any{"ids": raw}, nil, nil, nil, []string{"ids"}); err != nil {
			return nil, err
		}
		host, _, _ := strings.Cut(kind, "/")
		for _, rawID := range array(raw) {
			id := text(rawID)
			if !strings.HasPrefix(id, "//"+host+"/") {
				return nil, groupDenied("monitoring_group_member_reference_invalid")
			}
			for _, part := range strings.Split(strings.TrimPrefix(id, "//"+host+"/"), "/") {
				if !uptimeSegment(part) {
					return nil, groupDenied("monitoring_group_member_reference_invalid")
				}
			}
			refs[kind] = append(refs[kind], c.canonicalName(id))
		}
	}
	if parent := text(data["parentName"]); parent != "" {
		id := c.canonicalName("//monitoring.googleapis.com/" + parent)
		kind, _ := findType(monitoringGroupType)
		if _, err := c.resourceURL(kind, id); err != nil {
			return nil, err
		}
		refs[monitoringGroupType] = append(refs[monitoringGroupType], id)
	}
	return refs, nil
}
