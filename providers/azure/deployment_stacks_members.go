package azure

import (
	"slices"
	"strings"
)

// Member IDs can be subscription-scoped resources or resource groups, unlike
// the resource-group-only IDs accepted by the generic Azure resource reader.
// This parser establishes identity, never permission to read or delete a member.
func deploymentStackMemberID(wire string) (string, string, error) {
	if wire != strings.TrimSpace(wire) || !strings.HasPrefix(wire, "/") || strings.ContainsAny(wire, "%?#\\\x00\r\n\t") {
		return "", "", serviceDenied("invalid_deployment_stack_member_id")
	}
	id := strings.ToLower(wire)
	p := strings.Split(id[1:], "/")
	for _, part := range p {
		if part == "" || part == "." || part == ".." {
			return "", "", serviceDenied("invalid_deployment_stack_member_id")
		}
	}
	start, kind := 0, ""
	if len(p) >= 2 && p[0] == "subscriptions" && uuidPattern.MatchString(p[1]) {
		start, kind = 2, "Microsoft.Resources/subscriptions"
		if len(p) >= 4 && p[2] == "resourcegroups" {
			start, kind = 4, groupType
		}
	} else if len(p) >= 4 && p[0] == "providers" && p[1] == "microsoft.management" && p[2] == "managementgroups" {
		start, kind = 4, "Microsoft.Management/managementGroups"
	} else {
		return "", "", serviceDenied("invalid_deployment_stack_member_scope")
	}
	for start < len(p) {
		if p[start] != "providers" || start+3 >= len(p) || !strings.Contains(p[start+1], ".") {
			return "", "", serviceDenied("invalid_deployment_stack_member_type")
		}
		parts := []string{p[start+1]}
		start += 2
		for start < len(p) && p[start] != "providers" {
			if start+1 >= len(p) {
				return "", "", serviceDenied("invalid_deployment_stack_member_type")
			}
			parts = append(parts, p[start])
			start += 2
		}
		if len(parts) < 2 {
			return "", "", serviceDenied("invalid_deployment_stack_member_type")
		}
		kind = strings.Join(parts, "/")
	}
	return id, kind, nil
}

// Preserve complete current membership separately from historical outcomes.
// Unaddressable extensible members remain counted and covered by the private
// fingerprint; they must not disappear through the public payload projection.
func (c *client) deploymentStackMemberReview(raw map[string]any) (map[string]any, error) {
	props := object(raw["properties"])
	if props == nil {
		return nil, serviceDenied("invalid_deployment_stack_members")
	}
	review := map[string]any{"configuration": c.privateConfiguration(raw)}
	members := map[string]any{}
	unresolved := 0
	rows, present := props["resources"].([]any)
	if props["resources"] != nil && !present {
		return nil, serviceDenied("invalid_deployment_stack_members")
	}
	review["current_member_count"] = nil
	if present {
		review["current_member_count"] = len(rows)
	}
	for _, value := range rows {
		member := object(value)
		if member == nil {
			return nil, serviceDenied("invalid_deployment_stack_member")
		}
		wire, hasID := member["id"].(string)
		if member["id"] != nil && !hasID {
			return nil, serviceDenied("invalid_deployment_stack_member_id")
		}
		if wire == "" {
			if object(member["extension"]) == nil || text(member["type"]) == "" || len(object(member["identifiers"])) == 0 {
				return nil, serviceDenied("unidentified_deployment_stack_member")
			}
			unresolved++
			continue
		}
		id, kind, err := deploymentStackMemberID(wire)
		if err != nil {
			return nil, err
		}
		if members[id] != nil {
			return nil, serviceDenied("duplicate_deployment_stack_member")
		}
		if typ := member["type"]; typ != nil && (!strings.EqualFold(text(typ), kind) || text(typ) == "") {
			return nil, serviceDenied("mismatched_deployment_stack_member_type")
		}
		status, deny := "unknown", "unknown"
		for key, target := range map[string]*string{"status": &status, "denyStatus": &deny} {
			if member[key] != nil {
				if _, ok := member[key].(string); !ok {
					return nil, serviceDenied("invalid_deployment_stack_member_status")
				}
			}
			val := text(member[key])
			allowed := " managed removeDenyFailed deleteFailed "
			if key == "denyStatus" {
				allowed = " denyDelete notSupported inapplicable denyWriteAndDelete removedBySystem none unknown "
			}
			if val != "" && slices.Contains(strings.Fields(allowed), val) {
				*target = val
			}
		}
		local := strings.HasPrefix(id, strings.ToLower(c.root())+"/") || strings.EqualFold(id, c.root())
		members[id] = map[string]any{"type": kind, "status": status, "deny_status": deny, "subscription_local": local}
		if member["extension"] != nil || member["identifiers"] != nil {
			unresolved++
		}
	}
	review["members"], review["unresolved_members"], review["arm_members_complete"] = members, unresolved, present && unresolved == 0
	for _, key := range []string{"deletedResources", "detachedResources", "failedResources"} {
		count := 0
		if value := props[key]; value != nil {
			history, ok := value.([]any)
			if !ok {
				return nil, serviceDenied("invalid_deployment_stack_history")
			}
			for _, item := range history {
				if object(item) == nil {
					return nil, serviceDenied("invalid_deployment_stack_history")
				}
			}
			count = len(history)
		}
		review[key+"_count"] = count
	}
	return review, nil
}
