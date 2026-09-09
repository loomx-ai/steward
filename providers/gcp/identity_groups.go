package gcp

import (
	"context"
	"encoding/json"
	"net/url"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const identityHost = "cloudidentity.googleapis.com"
const identityGroupType = identityHost + "/Group"
const identityMemberType = identityHost + "/Membership"
const identityInventorySource = "identity-groups-visible"
const identityScope = "_identity_group_scope"
const identityProof = "_identity_group_configuration"
const identityParentProof = "_identity_group_parent_configuration"
const identityMembers = "_identity_group_members"
const identitySecurity = "_identity_group_security_settings"
const identitySnapshot = "_identity_group_snapshot"

var identitySegment = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func isIdentityGroup(kind string) bool {
	return kind == identityGroupType || kind == identityMemberType
}
func identityParentValid(parent string) bool {
	parts := strings.Split(parent, "/")
	return len(parts) == 2 && identitySegment.MatchString(parts[1]) && parts[1] != "-" && (parts[0] == "customers" && strings.HasPrefix(parts[1], "C") && len(parts[1]) > 1 || parts[0] == "identitysources")
}
func identityName(kind, id string) (string, error) {
	name := strings.TrimPrefix(id, "//"+identityHost+"/")
	parts := strings.Split(name, "/")
	count := 2
	if kind == identityMemberType {
		count = 4
	}
	if !isIdentityGroup(kind) || name == id || len(parts) != count || parts[0] != "groups" || count == 4 && parts[2] != "memberships" {
		return "", groupDenied("identity_group_name_invalid")
	}
	for i := 1; i < len(parts); i += 2 {
		if !identitySegment.MatchString(parts[i]) || parts[i] == "-" {
			return "", groupDenied("identity_group_name_invalid")
		}
	}
	return name, nil
}
func identityGroupName(name string) string {
	parts := strings.Split(name, "/")
	if len(parts) < 2 {
		return ""
	}
	return strings.Join(parts[:2], "/")
}
func identityConfiguration(data map[string]any) string {
	value := map[string]any{}
	for _, key := range []string{"name", "parent", "groupKey", "additionalGroupKeys", "displayName", "description", "labels", "dynamicGroupMetadata", "createTime", "updateTime", "preferredMemberKey", "type", "roles", "deliverySetting", identitySecurity} {
		if raw, present := data[key]; present {
			value[key] = raw
		}
	}
	// Dynamic statusTime is an observation timestamp, not group configuration.
	if raw, present := value["dynamicGroupMetadata"]; present {
		metadata := cloneParameters(object(raw))
		delete(metadata, "status")
		value["dynamicGroupMetadata"] = metadata
	}
	for _, key := range []string{"roles", "additionalGroupKeys"} {
		if rows, ok := value[key].([]any); ok {
			copy := slices.Clone(rows)
			slices.SortFunc(copy, func(a, b any) int { return strings.Compare(firewallDigest(a), firewallDigest(b)) })
			value[key] = copy
		}
	}
	return firewallDigest(value)
}
func identityManifest(data map[string]any) string {
	return firewallDigest(map[string]any{"name": data["name"], "scope": data[identityScope], "configuration": data[identityProof], "parent": data[identityParentProof], "members": data[identityMembers]})
}
func (c *client) identityValidate(kind, name string, data map[string]any) error {
	if !identityParentValid(c.identityParent) || data["name"] != name {
		return groupDenied("identity_group_scope_or_identity_changed")
	}
	if _, err := identityName(kind, "//"+identityHost+"/"+name); err != nil {
		return err
	}
	for _, field := range []string{"createTime", "updateTime"} {
		if _, err := time.Parse(time.RFC3339Nano, text(data[field])); err != nil {
			return groupDenied("identity_group_creation_missing")
		}
	}
	if kind == identityGroupType {
		if data["parent"] != c.identityParent || text(object(data["groupKey"])["id"]) == "" {
			return groupDenied("identity_group_parent_changed")
		}
		labels, ok := data["labels"].(map[string]any)
		if !ok || len(labels) == 0 {
			return groupDenied("identity_group_labels_missing")
		}
		for _, value := range labels {
			if _, ok := value.(string); !ok {
				return groupDenied("identity_group_labels_invalid")
			}
		}
		namespace := text(object(data["groupKey"])["namespace"])
		if strings.HasPrefix(c.identityParent, "identitysources/") && namespace != c.identityParent || strings.HasPrefix(c.identityParent, "customers/") && namespace != "" {
			return groupDenied("identity_group_namespace_changed")
		}
		if identityGroupDynamic(data) {
			metadata := object(data["dynamicGroupMetadata"])
			queries, ok := metadata["queries"].([]any)
			if !ok || len(queries) == 0 {
				return groupDenied("identity_dynamic_query_missing")
			}
			for _, raw := range queries {
				query := object(raw)
				if query["resourceType"] != "USER" || text(query["query"]) == "" {
					return groupDenied("identity_dynamic_query_invalid")
				}
			}
			state := object(metadata["status"])
			if !slices.Contains([]string{"UP_TO_DATE", "UPDATING_MEMBERSHIPS", "INVALID_QUERY"}, text(state["status"])) {
				return groupDenied("identity_dynamic_status_invalid")
			}
			if _, err := time.Parse(time.RFC3339Nano, text(state["statusTime"])); err != nil {
				return groupDenied("identity_dynamic_status_time_invalid")
			}
		}
	} else {
		if text(object(data["preferredMemberKey"])["id"]) == "" || !slices.Contains([]string{"USER", "SERVICE_ACCOUNT", "GROUP", "SHARED_DRIVE", "CBCM_BROWSER", "CHROME_OS_DEVICE", "OTHER"}, text(data["type"])) {
			return groupDenied("identity_membership_entity_invalid")
		}
		roles, ok := data["roles"].([]any)
		if _, present := data["roles"]; present && !ok {
			return groupDenied("identity_membership_roles_invalid")
		}
		// The native resource contract defaults an unspecified role set to MEMBER.
		if len(roles) == 0 {
			roles = []any{map[string]any{"name": "MEMBER"}}
			data["roles"] = roles
		}
		seen := map[string]bool{}
		for _, raw := range roles {
			role := object(raw)
			name := text(role["name"])
			if !slices.Contains([]string{"MEMBER", "OWNER", "MANAGER"}, name) || seen[name] {
				return groupDenied("identity_membership_role_invalid")
			}
			seen[name] = true
			if expiry, present := role["expiryDetail"]; present {
				if name != "MEMBER" {
					return groupDenied("identity_membership_expiry_invalid")
				}
				if _, err := time.Parse(time.RFC3339Nano, text(object(expiry)["expireTime"])); err != nil {
					return groupDenied("identity_membership_expiry_invalid")
				}
			}
		}
	}
	return nil
}
func (c *client) identityRead(ctx context.Context, kind, name string) (map[string]any, error) {
	data, err := c.request(ctx, "GET", "https://"+identityHost+"/v1/"+name, nil)
	if err != nil {
		return nil, err
	}
	if err := c.identityComplete(ctx, kind, name, data); err != nil {
		return nil, err
	}
	return data, nil
}
func (c *client) identityComplete(ctx context.Context, kind, name string, data map[string]any) error {
	if err := c.identityValidate(kind, name, data); err != nil {
		return err
	}
	if kind == identityGroupType {
		if _, security := object(data["labels"])[identityHost+"/groups.security"]; security {
			settings, err := c.request(ctx, "GET", "https://"+identityHost+"/v1/"+name+"/securitySettings", nil)
			if err != nil {
				return err
			}
			if settings["name"] != name+"/securitySettings" {
				return groupDenied("identity_group_security_settings_changed")
			}
			data[identitySecurity] = settings
		}
	}
	return nil
}

type identityMemberProof struct {
	ID    string `json:"id"`
	Proof string `json:"proof"`
}
type identityGroupView struct {
	Group   map[string]any
	Members map[string]map[string]any
}

// FULL views are required: BASIC omits configuration and role fields. Compare
// two complete native snapshots; neither list offers a snapshot/CAS token.
func (c *client) identityGroupView(ctx context.Context, name string) (identityGroupView, error) {
	var previous identityGroupView
	for pass := 0; pass < 2; pass++ {
		group, err := c.identityRead(ctx, identityGroupType, name)
		if err != nil {
			return previous, err
		}
		rows, err := c.batchList(ctx, "cloudidentity.groups.memberships.list", map[string]any{"parent": name, "view": "FULL", "pageSize": 500}, "memberships")
		if err != nil {
			return previous, err
		}
		current := identityGroupView{Group: group, Members: map[string]map[string]any{}}
		proofs := []identityMemberProof{}
		for _, row := range rows {
			memberName := text(row["name"])
			if err := c.identityValidate(identityMemberType, memberName, row); err != nil {
				return previous, err
			}
			if identityGroupName(memberName) != name || current.Members[memberName] != nil {
				return previous, groupDenied("identity_membership_list_invalid")
			}
			live, err := c.identityRead(ctx, identityMemberType, memberName)
			if err != nil {
				return previous, err
			}
			if identityConfiguration(row) != identityConfiguration(live) {
				return previous, groupDenied("identity_membership_list_detail_changed")
			}
			current.Members[memberName] = live
			proofs = append(proofs, identityMemberProof{memberName, identityConfiguration(live)})
		}
		slices.SortFunc(proofs, func(a, b identityMemberProof) int { return strings.Compare(a.ID, b.ID) })
		finalGroup, err := c.identityRead(ctx, identityGroupType, name)
		if err != nil {
			return previous, err
		}
		if identityConfiguration(group) != identityConfiguration(finalGroup) {
			return previous, groupDenied("identity_group_changed_during_membership_read")
		}
		encoded, _ := json.Marshal(proofs)
		group[identityScope], group[identityProof], group[identityParentProof], group[identityMembers] = c.identityParent, identityConfiguration(group), identityConfiguration(group), string(encoded)
		group[identitySnapshot] = identityManifest(group)
		for _, member := range current.Members {
			member[identityScope], member[identityProof], member[identityParentProof], member[identityMembers] = c.identityParent, identityConfiguration(member), group[identityProof], string(encoded)
			member[identitySnapshot] = identityManifest(member)
		}
		if pass > 0 && previous.Group[identitySnapshot] != current.Group[identitySnapshot] {
			return previous, groupDenied("identity_group_snapshot_changed")
		}
		previous = current
	}
	return previous, nil
}

func (r *Runtime) listIdentityGroups(ctx context.Context, c *client, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{}, Complete: true}
	if request.Source != identityInventorySource || request.ResourceKind == nil || !isIdentityGroup(request.ResourceKind.NativeType) || request.Cursor != "" || request.NetworkTarget != nil || request.Scope.Kind != asset.ScopeProject && request.Scope.Kind != asset.ScopeGlobal {
		return batch, groupDenied("identity_group_inventory_scope_invalid")
	}
	if request.Scope.Kind == asset.ScopeGlobal && !slices.Contains([]string{"global", c.project + "/global", c.number + "/global"}, request.Scope.NativeID) {
		return batch, groupDenied("identity_group_inventory_project_changed")
	}
	if c.identityParent == "" {
		return batch, nil
	}
	list := func() (map[string]string, error) {
		rows, err := c.batchList(ctx, "cloudidentity.groups.list", map[string]any{"parent": c.identityParent, "view": "FULL", "pageSize": 500}, "groups")
		if err != nil {
			return nil, err
		}
		result := map[string]string{}
		for _, row := range rows {
			name := text(row["name"])
			if err := c.identityValidate(identityGroupType, name, row); err != nil {
				return nil, err
			}
			if _, duplicate := result[name]; duplicate {
				return nil, groupDenied("identity_group_list_duplicate")
			}
			result[name] = identityConfiguration(row)
		}
		return result, nil
	}
	first, err := list()
	if err != nil {
		return batch, err
	}
	names := []string{}
	for name := range first {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		view, err := c.identityGroupView(ctx, name)
		if err != nil {
			return batch, err
		}
		withoutSecurity := cloneParameters(view.Group)
		delete(withoutSecurity, identitySecurity)
		if first[name] != identityConfiguration(withoutSecurity) {
			return batch, groupDenied("identity_group_list_detail_changed")
		}
		rows := view.Members
		if request.ResourceKind.NativeType == identityGroupType {
			rows = map[string]map[string]any{name: view.Group}
		}
		ids := []string{}
		for id := range rows {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			item, err := r.inventoryItem(c, map[string]any{"name": "//" + identityHost + "/" + id, "assetType": request.ResourceKind.NativeType, "resource": map[string]any{"data": rows[id], "location": "global"}})
			if err != nil {
				return batch, err
			}
			delete(item.Normalized, "project_id")
			delete(item.Normalized, "project_number")
			item.Normalized["_inventory_source"] = identityInventorySource
			batch.Items = append(batch.Items, item)
		}
	}
	second, err := list()
	if err != nil {
		return batch, err
	}
	if !reflect.DeepEqual(first, second) {
		return batch, groupDenied("identity_group_directory_changed")
	}
	return batch, nil
}
func identityResourceOperation(metadata providerMetadata, kind, id, method string) (catalog.Operation, map[string]any, error) {
	name, err := identityName(kind, id)
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	stem := "cloudidentity.groups"
	if kind == identityMemberType {
		stem += ".memberships"
	}
	if method == "GET" {
		stem += ".get"
	} else if method == "DELETE" {
		stem += ".delete"
	} else {
		return catalog.Operation{}, nil, groupDenied("identity_group_method_invalid")
	}
	op, ok := metadata.catalog.Operation(stem)
	if !ok {
		return op, nil, groupDenied("identity_group_method_missing")
	}
	return op, map[string]any{"name": name}, nil
}
func (c *client) identityInvocation(ctx context.Context, op catalog.Operation, parameters map[string]any) error {
	if op.Call.Product != "cloudidentity" {
		return nil
	}
	if !identityParentValid(c.identityParent) {
		return groupDenied("identity_group_directory_required")
	}
	if op.ID == "cloudidentity.groups.list" {
		if parameters["parent"] != c.identityParent {
			return groupDenied("identity_group_directory_changed")
		}
		return nil
	}
	name := text(parameters["name"])
	if op.ID == "cloudidentity.groups.memberships.list" {
		name = text(parameters["parent"])
	}
	if op.ID == "cloudidentity.groups.getSecuritySettings" {
		if !strings.HasSuffix(name, "/securitySettings") {
			return groupDenied("identity_group_security_name_invalid")
		}
		name = strings.TrimSuffix(name, "/securitySettings")
	}
	kind := identityGroupType
	if strings.Contains(op.ID, ".memberships.") && op.ID != "cloudidentity.groups.memberships.list" {
		kind = identityMemberType
	}
	if _, err := identityName(kind, "//"+identityHost+"/"+name); err != nil {
		return err
	}
	group, err := c.identityRead(ctx, identityGroupType, identityGroupName(name))
	if err != nil {
		return err
	}
	if op.Call.Method == "DELETE" {
		if identityGroupProtected(group) {
			return groupDenied("identity_group_locked")
		}
		if kind == identityMemberType && identityGroupDynamic(group) {
			return groupDenied("identity_dynamic_membership_managed")
		}
	}
	return nil
}
func identityGroupProtected(data map[string]any) bool {
	_, locked := object(data["labels"])[identityHost+"/groups.locked"]
	return locked || protectedComputeLabels(data)
}
func identityGroupDynamic(data map[string]any) bool {
	_, dynamic := object(data["labels"])[identityHost+"/groups.dynamic"]
	return dynamic
}

func (c *client) validateIdentityDirectory(ctx context.Context) error {
	if c.identityParent == "" {
		return nil
	}
	data, err := c.request(ctx, "GET", "https://"+identityHost+"/v1/groups", url.Values{"parent": {c.identityParent}, "view": {"FULL"}, "pageSize": {"1"}})
	if err != nil {
		return err
	}
	if err := identityListShape(data, "groups"); err != nil {
		return err
	}
	for _, raw := range array(data["groups"]) {
		row := object(raw)
		if err := c.identityValidate(identityGroupType, text(row["name"]), row); err != nil {
			return err
		}
	}
	return nil
}

func identityListShape(data map[string]any, field string) error {
	for key, value := range data {
		if key == "nextPageToken" {
			if _, ok := value.(string); !ok {
				return groupDenied("identity_list_token_invalid")
			}
			continue
		}
		if key != field {
			return groupDenied("identity_list_collection_invalid")
		}
		if _, ok := value.([]any); !ok {
			return groupDenied("identity_list_shape_invalid")
		}
	}
	return nil
}
