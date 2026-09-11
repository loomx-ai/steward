package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	communicationInventorySource = "communication-services"
	communicationConfiguration   = "_communication_configuration"
	communicationMembers         = "_communication_members"
	communicationProof           = "_communication_binding"
)

func communicationChildKinds(kind string) []string {
	switch kind {
	case communicationType:
		return []string{communicationSMTPType, communicationPhoneType, communicationReservationType, communicationRoomType}
	case communicationEmailType:
		return []string{communicationDomainType}
	case communicationDomainType:
		return []string{communicationSenderType, communicationSuppressionType}
	case communicationSuppressionType:
		return []string{communicationAddressType}
	}
	return nil
}

func communicationRootKind(kind string) string {
	if strings.HasPrefix(kind, communicationEmailType) {
		return communicationEmailType
	}
	return communicationType
}

func communicationRootID(id, kind string) string {
	if isCommunicationDataType(kind) {
		return ""
	}
	parts := strings.Split(id, "/")
	if len(parts) < 9 {
		return ""
	}
	return strings.Join(parts[:9], "/")
}

func (c *client) communicationIdentity(id, kind string) error {
	if kind == "" || communicationKind(kind) != kind {
		return serviceDenied("invalid_communication_kind")
	}
	if isCommunicationDataType(kind) {
		canonical, typ, _, _, err := communicationDataIdentity(id)
		if err != nil || canonical != id || typ != kind {
			return serviceDenied("invalid_communication_identity")
		}
		return nil
	}
	canonical, typ, err := parseID(id)
	if err != nil || canonical != id || !strings.EqualFold(typ, kind) || !strings.HasPrefix(id, c.root()+"/") || len(strings.Split(id, "/")) != 7+2*(len(strings.Split(kind, "/"))-1) {
		return serviceDenied("invalid_communication_identity")
	}
	return nil
}

func (c *client) communicationARMRead(ctx context.Context, id, kind string) (map[string]any, error) {
	if err := c.communicationIdentity(id, kind); err != nil || isCommunicationDataType(kind) {
		return nil, serviceDenied("invalid_communication_arm_read")
	}
	raw, err := c.linkedResource(ctx, id)
	if err != nil {
		return nil, err
	}
	if err := communicationARMMetadata(kind, raw); err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *client) communicationARMIndex(ctx context.Context, kind string, parent map[string]any) (map[string]map[string]any, error) {
	items := map[string]map[string]any{}
	if kind == communicationType || kind == communicationEmailType {
		values, err := c.listAll(ctx, c.root()+"/providers/Microsoft.Communication/"+strings.Split(kind, "/")[1], communicationARMVersion)
		if err != nil {
			return nil, err
		}
		for _, value := range values {
			raw := object(value)
			id := responseID(kind, text(raw["id"]))
			if c.communicationIdentity(id, kind) != nil || items[id] != nil || !validResponseType(kind, text(raw["type"])) {
				return nil, serviceDenied("invalid_communication_root_index")
			}
			current, err := c.communicationARMRead(ctx, id, kind)
			if err != nil {
				return nil, err
			}
			if !nativeConfigurationContains(communicationSnapshot(kind, raw), communicationSnapshot(kind, current)) {
				return nil, serviceDenied("communication_root_index_changed")
			}
			items[id] = current
		}
		return items, nil
	}
	id, typ, err := parseID(responseID(communicationKind(text(parent["type"])), text(parent["id"])))
	parentKind := communicationKind(typ)
	if err != nil || !slices.Contains(communicationChildKinds(parentKind), kind) {
		return nil, serviceDenied("invalid_communication_child_scope")
	}
	children, err := c.nativeServiceChildren(ctx, asset.Identity{NativeID: id, NativeType: parentKind}, parent, []string{kind})
	if err != nil {
		return nil, err
	}
	for _, child := range children {
		items[child.id] = child.data
	}
	return items, nil
}

func (c *client) communicationObservedData(ctx context.Context, account communicationAccountContext, id, kind string) (map[string]any, error) {
	result, err := c.communicationDataRead(ctx, account, id, kind)
	if err != nil {
		return nil, err
	}
	if kind == communicationRoomType {
		participants, err := c.communicationRoomParticipants(ctx, account, id)
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		after, err := c.communicationDataRead(ctx, account, id, kind)
		if err != nil {
			return nil, err
		}
		if c.privateConfiguration(communicationSnapshot(kind, result.data)) != c.privateConfiguration(communicationSnapshot(kind, after.data)) {
			return nil, serviceDenied("communication_room_changed_during_roster_read")
		}
		result.data["_participants"] = participants
	}
	return result.data, nil
}

func (c *client) communicationDataIndex(ctx context.Context, account communicationAccountContext, kind string) (map[string]map[string]any, error) {
	op := map[string]string{communicationPhoneType: "PhoneNumbers_ListPhoneNumbers", communicationReservationType: "PhoneNumbers_ListReservations", communicationRoomType: "Rooms_List"}[kind]
	values, _, err := c.communicationDataList(ctx, account, op, nil)
	if err != nil {
		return nil, err
	}
	items := map[string]map[string]any{}
	for _, raw := range values {
		path := "/rooms/" + text(raw["id"])
		if kind == communicationPhoneType {
			path = "/phoneNumbers/" + text(raw["phoneNumber"])
		}
		if kind == communicationReservationType {
			path = "/availablePhoneNumbers/reservations/" + text(raw["id"])
		}
		id := account.endpoint + path
		if c.communicationIdentity(id, kind) != nil || items[id] != nil {
			return nil, serviceDenied("invalid_communication_data_index")
		}
		current, err := c.communicationObservedData(ctx, account, id, kind)
		if err != nil {
			return nil, err
		}
		// Reservation collections omit phoneNumbers. Compare the native fields
		// they expose and always fetch the full reservation through its own GET.
		if !nativeConfigurationContains(communicationSnapshot(kind, raw), communicationSnapshot(kind, current)) {
			return nil, serviceDenied("communication_data_index_changed")
		}
		items[id] = current
	}
	return items, nil
}

type communicationMember struct {
	id, kind, parent, root string
	raw                    map[string]any
}

type communicationTree struct {
	root     string
	account  communicationAccountContext
	members  map[string]communicationMember
	incoming map[string]any
}

func (tree communicationTree) descendants(parent string) map[string]any {
	result := map[string]any{}
	for id, member := range tree.members {
		if id == parent {
			continue
		}
		for current := member.parent; current != ""; current = tree.members[current].parent {
			if current == parent {
				result[id] = map[string]any{"kind": member.kind, "parent": member.parent, "configuration": communicationSnapshot(member.kind, member.raw)}
				break
			}
		}
	}
	return result
}

func (c *client) communicationTree(ctx context.Context, root map[string]any, hints map[string]communicationMember) (communicationTree, error) {
	id, typ, err := parseID(text(root["id"]))
	kind := communicationKind(typ)
	tree := communicationTree{root: id, members: map[string]communicationMember{}}
	if err != nil || kind != communicationType && kind != communicationEmailType {
		return tree, serviceDenied("invalid_communication_tree_root")
	}
	if kind == communicationType {
		endpoint, err := communicationAccountEndpoint(root)
		if err != nil {
			return tree, err
		}
		tree.account = communicationAccountContext{id: id, endpoint: endpoint, raw: root}
	}
	var walk func(communicationMember) error
	walk = func(member communicationMember) error {
		if tree.members[member.id].id != "" {
			return serviceDenied("duplicate_communication_tree_member")
		}
		tree.members[member.id] = member
		for _, childKind := range communicationChildKinds(member.kind) {
			var values map[string]map[string]any
			var err error
			if isCommunicationDataType(childKind) {
				values, err = c.communicationDataIndex(ctx, tree.account, childKind)
			} else {
				values, err = c.communicationARMIndex(ctx, childKind, member.raw)
			}
			if err != nil {
				return err
			}
			for childID, hint := range hints {
				if hint.kind != childKind || hint.parent != member.id || values[childID] != nil {
					continue
				}
				var raw map[string]any
				if isCommunicationDataType(childKind) {
					raw, err = c.communicationObservedData(ctx, tree.account, childID, childKind)
				} else {
					raw, err = c.communicationARMRead(ctx, childID, childKind)
				}
				if isNotFound(err) {
					continue
				}
				if err != nil {
					return err
				}
				values[childID] = raw
			}
			for _, childID := range slices.Sorted(maps.Keys(values)) {
				if err := walk(communicationMember{id: childID, kind: childKind, parent: member.id, root: id, raw: values[childID]}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(communicationMember{id: id, kind: kind, root: id, raw: root}); err != nil {
		return tree, err
	}
	after, err := c.communicationARMRead(ctx, id, kind)
	if err != nil {
		return tree, err
	}
	if c.privateConfiguration(communicationSnapshot(kind, root)) != c.privateConfiguration(communicationSnapshot(kind, after)) {
		return tree, serviceDenied("communication_root_changed_during_walk")
	}
	return tree, nil
}

func (c *client) communicationBinding(id, kind string, normalized map[string]any) string {
	bound := map[string]any{"id": id, "kind": kind, "protocol": "communication-native-1"}
	for _, key := range []string{communicationConfiguration, communicationMembers, "_communication_connection", "_communication_root", "_communication_parent", "_communication_endpoint", "_communication_ancestors", "_communication_group", "_communication_references", "_communication_incoming", "arm_parameters", "_communication_parameters"} {
		bound[key] = normalized[key]
	}
	return c.privateConfiguration(bound)
}

func (c *client) communicationRecorded(id, kind string, normalized map[string]any) (map[string]any, error) {
	if c.communicationIdentity(id, kind) != nil || text(normalized[communicationConfiguration]) == "" || text(normalized["_communication_group"]) == "" || normalized["_inventory_source"] != communicationInventorySource || text(normalized[communicationProof]) != c.communicationBinding(id, kind, normalized) {
		return nil, serviceDenied("invalid_communication_recorded_binding")
	}
	members, ok := normalized[communicationMembers].(map[string]any)
	ancestors, ancestorOK := normalized["_communication_ancestors"].(map[string]any)
	refs, refsOK := normalized["_communication_references"].(map[string]any)
	incoming, incomingOK := normalized["_communication_incoming"].(map[string]any)
	if !ok || !ancestorOK || !refsOK || !incomingOK || members == nil || ancestors == nil || refs == nil || incoming == nil {
		return nil, serviceDenied("communication_recorded_context_missing")
	}
	root := text(normalized["_communication_root"])
	if c.communicationIdentity(root, communicationRootKind(kind)) != nil {
		return nil, serviceDenied("invalid_communication_recorded_root")
	}
	if isCommunicationDataType(kind) {
		_, _, endpoint, _, _ := communicationDataIdentity(id)
		if endpoint != normalized["_communication_endpoint"] || text(ancestors[root]) == "" {
			return nil, serviceDenied("communication_recorded_endpoint_changed")
		}
	} else if communicationRootID(id, kind) != root {
		return nil, serviceDenied("communication_recorded_root_changed")
	}
	if communicationRootKind(kind) == communicationType {
		if !communicationEndpointPattern.MatchString(text(normalized["_communication_endpoint"])) {
			return nil, serviceDenied("invalid_communication_recorded_endpoint")
		}
	} else if normalized["_communication_endpoint"] != "" {
		return nil, serviceDenied("communication_email_has_data_endpoint")
	}
	return members, nil
}

func communicationReferences(member communicationMember) (map[string][]string, error) {
	refs := map[string][]string{}
	if member.parent != "" {
		parentKind := communicationType
		if !isCommunicationDataType(member.kind) {
			_, typ, _ := parseID(member.parent)
			parentKind = communicationKind(typ)
		}
		addReference(refs, parentKind, member.parent)
	}
	if member.kind != communicationType {
		return refs, nil
	}
	if value := object(member.raw["properties"])["notificationHubId"]; value != nil && value != "" {
		id, kind, err := parseID(text(value))
		const hubType = "Microsoft.NotificationHubs/namespaces/notificationHubs"
		if err != nil || !strings.EqualFold(kind, hubType) || len(strings.Split(id, "/")) != 11 {
			return nil, serviceDenied("invalid_communication_notification_hub")
		}
		addReference(refs, hubType, id)
	}
	if raw, exists := object(member.raw["properties"])["linkedDomains"]; exists && raw != nil {
		values, ok := raw.([]any)
		if !ok {
			return nil, serviceDenied("invalid_communication_domain_links")
		}
		seen := map[string]bool{}
		for _, value := range values {
			id, kind, err := parseID(text(value))
			if err != nil || !strings.EqualFold(kind, communicationDomainType) || seen[id] {
				return nil, serviceDenied("invalid_communication_domain_link")
			}
			seen[id] = true
			addReference(refs, communicationDomainType, id)
		}
	}
	for id := range object(object(member.raw["identity"])["userAssignedIdentities"]) {
		canonical, kind, err := parseID(id)
		if err != nil || !strings.EqualFold(kind, rbacUserIdentityType) {
			return nil, serviceDenied("invalid_communication_managed_identity")
		}
		addReference(refs, rbacUserIdentityType, canonical)
	}
	return refs, nil
}

func communicationProtection(kind string, raw map[string]any) string {
	if protectedAzureTags(object(raw["tags"])) {
		return "azure_protected_tag"
	}
	if kind == communicationType || kind == communicationEmailType || kind == communicationDomainType {
		if _, err := time.Parse(time.RFC3339Nano, text(object(raw["systemData"])["createdAt"])); err != nil {
			return "azure_communication_creation_unverified"
		}
		if !slices.Contains([]string{"Succeeded", "Running", "Failed", "Canceled"}, text(object(raw["properties"])["provisioningState"])) {
			return "azure_communication_resource_busy"
		}
	}
	if kind == communicationReservationType && raw["status"] == "submitted" {
		return "azure_communication_purchase_in_progress"
	}
	return ""
}

func (r *Runtime) communicationInventoryItem(c *client, connection asset.ConnectionID, tree communicationTree, member communicationMember, group map[string]any, locks []any) (contracts.InventoryItem, error) {
	normalized := map[string]any{"name": last(member.id), "subscription_id": c.subscription, "resource_group": strings.Split(member.root, "/")[4], "tags": member.raw["tags"], "_inventory_source": communicationInventorySource, "_communication_connection": string(connection), "_communication_root": member.root, "_communication_endpoint": tree.account.endpoint, "_communication_parent": member.parent}
	safe := safePayload(object(communicationSafeValue(member.raw)))
	maps.Copy(normalized, object(safe["properties"]))
	for _, key := range []string{"phoneNumber", "countryCode", "phoneNumberType", "assignmentType", "purchaseDate", "expiresAt", "createdAt", "validFrom", "validUntil", "pstnDialOutEnabled"} {
		if entry, present := safe[key]; present {
			normalized[key] = entry
		}
	}
	normalized[communicationConfiguration] = c.privateConfiguration(communicationSnapshot(member.kind, member.raw))
	normalized["_communication_group"] = c.privateConfiguration(insightsWorkspaceResourceSnapshot(group))
	ancestors := map[string]any{}
	reason := communicationProtection(member.kind, member.raw)
	for id := member.parent; id != ""; id = tree.members[id].parent {
		parent := tree.members[id]
		ancestors[id] = c.privateConfiguration(communicationSnapshot(parent.kind, parent.raw))
		if parentReason := communicationProtection(parent.kind, parent.raw); parentReason != "" {
			reason = parentReason
		}
	}
	normalized["_communication_ancestors"] = ancestors
	members := tree.descendants(member.id)
	for _, value := range members {
		entry := object(value)
		entry["configuration"] = c.privateConfiguration(object(entry["configuration"]))
	}
	normalized[communicationMembers] = members
	incoming := map[string]any{}
	if member.kind == communicationDomainType {
		incoming[member.id] = tree.incoming[member.id]
	}
	for id, value := range members {
		if object(value)["kind"] == communicationDomainType {
			incoming[id] = tree.incoming[id]
		}
	}
	normalized["_communication_incoming"] = incoming
	refs, err := communicationReferences(member)
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	normalized["_communication_references"] = monitorReferenceProjection(refs)
	var network []string
	for kind, ids := range refs {
		normalized[referenceKey(kind)] = ids
		network = append(network, ids...)
	}
	normalized["_communication_email_domains"] = refs[communicationDomainType]
	slices.Sort(network)
	kind, _ := findType(member.kind)
	_, params, err := c.resourceOperation(kind, member.id, "GET")
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	if isCommunicationDataType(member.kind) {
		normalized["_communication_parameters"] = params
	} else {
		normalized["arm_parameters"] = params
	}
	if text(group["managedBy"]) != "" {
		reason = "azure_managed_resource_group"
	}
	if protectedAzureTags(object(group["tags"])) {
		reason = "azure_protected_tag"
	}
	lockID := member.id
	if isCommunicationDataType(member.kind) {
		lockID = member.root
	}
	if locked(lockID, locks) {
		reason = "azure_management_lock"
	}
	normalized["cleanup_protected"], normalized["cleanup_protection_reason"] = reason != "", reason
	state := text(object(member.raw["properties"])["provisioningState"])
	if isCommunicationDataType(member.kind) {
		state = text(member.raw["status"])
		if member.kind == communicationPhoneType {
			state = "purchased"
		}
	}
	normalized["state"] = state
	normalized[communicationProof] = c.communicationBinding(member.id, member.kind, normalized)
	if err := c.rbacIdentityInventory(member.id, member.kind, member.raw, normalized); err != nil {
		return contracts.InventoryItem{}, err
	}
	tags := map[string]string{}
	for key, value := range object(safe["tags"]) {
		if s, ok := value.(string); ok {
			tags[key] = s
		}
	}
	actionable := reason == ""
	return contracts.InventoryItem{NativeID: member.id, NativeType: member.kind, ResourceKind: r.resourceKind(member.kind), Actionable: &actionable, Scope: contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: c.subscription + "/global", Name: "Global", Location: "global"}, Name: last(member.id), State: state, Location: "global", Tags: tags, Normalized: normalized, Raw: safe, NetworkReferences: network, NativeAliases: []string{member.id}}, nil
}

func (c *client) communicationKnown(request contracts.InventoryRequest) (map[string]communicationMember, map[string]communicationAccountContext, error) {
	hints := map[string]communicationMember{}
	accounts := map[string]communicationAccountContext{}
	seen := map[string]bool{}
	for _, id := range request.KnownNativeIDs {
		kind := request.ResourceKind.NativeType
		if seen[id] {
			return nil, nil, serviceDenied("duplicate_communication_known_id")
		}
		seen[id] = true
		normalized := request.KnownNativeMetadata[id]
		if normalized["_communication_connection"] != string(request.ConnectionID) {
			return nil, nil, serviceDenied("communication_known_connection_changed")
		}
		members, err := c.communicationRecorded(id, kind, normalized)
		if err != nil {
			return nil, nil, err
		}
		root := text(normalized["_communication_root"])
		add := func(id, kind, parent string) error {
			if c.communicationIdentity(id, kind) != nil {
				return serviceDenied("invalid_communication_known_member")
			}
			if isCommunicationDataType(kind) {
				_, _, endpoint, _, _ := communicationDataIdentity(id)
				if endpoint != normalized["_communication_endpoint"] || parent != root {
					return serviceDenied("communication_known_endpoint_changed")
				}
			} else if communicationRootID(id, kind) != root || communicationARMParent(id, kind) != parent {
				return serviceDenied("communication_known_parent_changed")
			}
			if previous := hints[id]; previous.id != "" && (previous.kind != kind || previous.parent != parent || previous.root != root) {
				return serviceDenied("communication_known_member_conflict")
			}
			hints[id] = communicationMember{id: id, kind: kind, parent: parent, root: root}
			return nil
		}
		parent := communicationARMParent(id, kind)
		if isCommunicationDataType(kind) {
			parent = root
		}
		if err := add(id, kind, parent); err != nil {
			return nil, nil, err
		}
		for childID, value := range members {
			entry := object(value)
			if text(entry["configuration"]) == "" {
				return nil, nil, serviceDenied("communication_known_configuration_missing")
			}
			if err := add(childID, text(entry["kind"]), text(entry["parent"])); err != nil {
				return nil, nil, err
			}
		}
		if communicationRootKind(kind) == communicationType {
			endpoint := text(normalized["_communication_endpoint"])
			if previous, ok := accounts[root]; ok && previous.endpoint != endpoint {
				return nil, nil, serviceDenied("communication_known_account_conflict")
			}
			accounts[root] = communicationAccountContext{id: root, endpoint: endpoint, historical: true, raw: map[string]any{"id": root, "properties": map[string]any{"hostName": strings.TrimPrefix(endpoint, "https://")}}}
		}
	}
	for id := range request.KnownNativeMetadata {
		if !seen[id] {
			return nil, nil, serviceDenied("unrelated_communication_known_metadata")
		}
	}
	// Reconstruct only native ARM ancestors of the authenticated hints.
	for _, hint := range slices.Collect(maps.Values(hints)) {
		id := hint.parent
		for id != "" {
			_, typ, _ := parseID(id)
			kind := communicationKind(typ)
			parent := communicationARMParent(id, kind)
			if hints[id].id == "" {
				hints[id] = communicationMember{id: id, kind: kind, parent: parent, root: hint.root}
			}
			id = parent
		}
	}
	return hints, accounts, nil
}

func (r *Runtime) communicationInventorySnapshot(ctx context.Context, c *client, request contracts.InventoryRequest) ([]contracts.InventoryItem, []string, map[string]any, error) {
	kind := request.ResourceKind.NativeType
	hints, historical, err := c.communicationKnown(request)
	if err != nil {
		return nil, nil, nil, err
	}
	roots, err := c.communicationARMIndex(ctx, communicationRootKind(kind), nil)
	if err != nil {
		return nil, nil, nil, err
	}
	for id, hint := range hints {
		if hint.parent != "" || roots[id] != nil {
			continue
		}
		raw, err := c.communicationARMRead(ctx, id, hint.kind)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, nil, nil, err
		}
		roots[id] = raw
	}
	trees := map[string]communicationTree{}
	rows := map[string]communicationMember{}
	for _, id := range slices.Sorted(maps.Keys(roots)) {
		tree, err := c.communicationTree(ctx, roots[id], hints)
		if err != nil {
			return nil, nil, nil, err
		}
		trees[id] = tree
		for childID, member := range tree.members {
			if rows[childID].id != "" {
				return nil, nil, nil, serviceDenied("ambiguous_communication_resource_owner")
			}
			rows[childID] = member
		}
	}
	missing := map[string]bool{}
	for _, id := range slices.Sorted(maps.Keys(hints)) {
		if rows[id].id != "" {
			continue
		}
		hint := hints[id]
		var err error
		if isCommunicationDataType(hint.kind) {
			account := historical[hint.root]
			if tree, ok := trees[hint.root]; ok {
				account = tree.account
			}
			_, err = c.communicationDataRead(ctx, account, id, hint.kind)
		} else {
			_, err = c.communicationARMRead(ctx, id, hint.kind)
		}
		if isNotFound(err) {
			missing[id] = true
			continue
		}
		if err != nil {
			return nil, nil, nil, err
		}
		return nil, nil, nil, serviceDenied("communication_live_resource_has_missing_parent")
	}
	var absent []string
	for _, id := range request.KnownNativeIDs {
		if missing[id] {
			absent = append(absent, id)
		}
	}
	slices.Sort(absent)
	if communicationRootKind(kind) == communicationEmailType {
		domains := map[string]bool{}
		for id, member := range rows {
			if member.kind == communicationDomainType {
				domains[id] = true
			}
		}
		known := map[string]any{}
		for _, normalized := range request.KnownNativeMetadata {
			if err := communicationMergeIncoming(known, object(normalized["_communication_incoming"])); err != nil {
				return nil, nil, nil, err
			}
		}
		incoming, _, err := c.communicationIncoming(ctx, domains, known)
		if err != nil {
			return nil, nil, nil, err
		}
		for id, tree := range trees {
			tree.incoming = incoming
			trees[id] = tree
		}
	}
	groups, err := c.insightsGroups(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	verifiedGroups := map[string]map[string]any{}
	bindings := map[string]any{}
	items := []contracts.InventoryItem{}
	for _, id := range slices.Sorted(maps.Keys(rows)) {
		member := rows[id]
		groupID := strings.Join(strings.Split(member.root, "/")[:5], "/")
		group := verifiedGroups[groupID]
		if group == nil {
			if groups[groupID] == nil {
				return nil, nil, nil, serviceDenied("communication_group_missing_from_index")
			}
			group, err = c.insightsGroup(ctx, groupID, groups[groupID])
			if err != nil {
				return nil, nil, nil, err
			}
			verifiedGroups[groupID] = group
		}
		item, err := r.communicationInventoryItem(c, request.ConnectionID, trees[member.root], member, group, locks)
		if err != nil {
			return nil, nil, nil, err
		}
		bindings[id] = map[string]any{"proof": item.Normalized[communicationProof], "protection": item.Normalized["cleanup_protection_reason"], "state": item.State}
		if member.kind == kind && productScopeMatches(request, item) {
			items = append(items, item)
		}
	}
	return items, absent, bindings, nil
}

func (r *Runtime) listCommunication(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.Source != communicationInventorySource || request.ResourceKind == nil || request.ResourceKind.NativeType == "" || communicationKind(request.ResourceKind.NativeType) != request.ResourceKind.NativeType || len(request.Options) != 0 {
		return batch, serviceDenied("invalid_communication_inventory_source")
	}
	switch request.Scope.Kind {
	case asset.ScopeSubscription:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription) {
			return batch, serviceDenied("communication_inventory_subscription_changed")
		}
	case asset.ScopeGlobal:
		if request.Scope.NativeID != "global" && !strings.EqualFold(request.Scope.NativeID, c.subscription+"/global") {
			return batch, serviceDenied("communication_inventory_global_scope_changed")
		}
	default:
		return batch, serviceDenied("invalid_communication_inventory_scope")
	}
	cursor := productCursor{}
	if request.Cursor != "" {
		if len(request.Cursor) > 128<<10 {
			return batch, serviceDenied("communication_inventory_cursor_too_large")
		}
		data, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_communication_inventory_cursor")
		}
	}
	items, absent, before, err := r.communicationInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	_, afterAbsent, after, err := r.communicationInventorySnapshot(ctx, c, request)
	if err != nil {
		return batch, err
	}
	if !slices.Equal(absent, afterAbsent) || c.privateConfiguration(before) != c.privateConfiguration(after) {
		return batch, serviceDenied("communication_collection_changed_during_scan")
	}
	boundary := request
	boundary.Cursor, boundary.Limit = "", 0
	fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "bindings": before, "absent": absent})
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(items)) {
		return batch, serviceDenied("communication_inventory_cursor_changed")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := cursor.Target + min(limit, len(items)-cursor.Target)
	batch = contracts.InventoryBatch{Items: items[cursor.Target:end], Complete: end == len(items)}
	if batch.Complete {
		batch.AbsentNativeIDs = absent
	} else {
		cursor.Fingerprint, cursor.Target = fingerprint, end
		data, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return batch, nil
}
