package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"
)

type dataFactoryMember struct {
	id, kind, parent, root, nodeName string
	raw, status                      map[string]any
	state, eventState                string
	refs                             map[string][]string
}

type dataFactoryTree struct {
	root            string
	members         map[string]dataFactoryMember
	work            dataFactoryWork
	incoming, links map[string]any
}

func dataFactoryListQuery(u *url.URL) error {
	if armPathProvider(u.Path) != "microsoft.datafactory" {
		return nil
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != dataFactoryVersion {
		return serviceDenied("datafactory_list_version_changed")
	}
	for key, values := range query {
		if len(values) != 1 || values[0] == "" {
			return serviceDenied("invalid_datafactory_list_query")
		}
		switch key {
		case "api-version", "$skiptoken", "$skipToken", "skipToken", "continuationToken":
		default:
			return serviceDenied("filtered_datafactory_list")
		}
	}
	return nil
}

func (c *client) dataFactoryIndex(ctx context.Context, kind string, parent dataFactoryMember) (map[string]dataFactoryMember, error) {
	result := map[string]dataFactoryMember{}
	var values []any
	if kind == dataFactoryNodeType {
		if parent.kind != dataFactoryIRType {
			return nil, serviceDenied("invalid_datafactory_node_parent")
		}
		props := object(parent.raw["properties"])
		if props["type"] != "SelfHosted" || object(object(props["typeProperties"])["linkedInfo"]) != nil {
			return result, nil
		}
		statusProps := object(object(parent.status["properties"])["typeProperties"])
		value, present := statusProps["nodes"]
		if present && value != nil {
			var ok bool
			values, ok = value.([]any)
			if !ok {
				return nil, serviceDenied("invalid_datafactory_node_index")
			}
		}
	} else {
		collection := c.root() + "/providers/Microsoft.DataFactory/factories"
		if kind != dataFactoryType {
			if c.dataFactoryIdentity(parent.id, parent.kind) != nil || !slices.Contains(dataFactoryChildKinds(parent.kind), kind) {
				return nil, serviceDenied("invalid_datafactory_collection_parent")
			}
			collection = parent.id + "/" + last(kind)
		}
		var err error
		values, err = c.listAll(ctx, collection, dataFactoryVersion)
		if err != nil {
			return nil, err
		}
	}
	for _, value := range values {
		raw, ok := value.(map[string]any)
		if !ok {
			return nil, serviceDenied("invalid_datafactory_index_item")
		}
		nodeName, id := "", strings.ToLower(text(raw["id"]))
		if kind == dataFactoryNodeType {
			nodeName, ok = raw["nodeName"].(string)
			if !ok {
				return nil, serviceDenied("datafactory_node_name_missing")
			}
			id = parent.id + "/nodes/" + strings.ToLower(nodeName)
			if _, err := dataFactoryWireID(id, kind, nodeName); err != nil {
				return nil, err
			}
		}
		if c.dataFactoryIdentity(id, kind) != nil || dataFactoryParent(id, kind) != parent.id || result[id].id != "" {
			return nil, serviceDenied("invalid_datafactory_index_identity")
		}
		if err := dataFactoryMetadata(id, kind, nodeName, raw); err != nil {
			return nil, err
		}
		live, err := c.dataFactoryRead(ctx, id, kind, nodeName)
		if err != nil {
			return nil, err
		}
		if !nativeConfigurationContains(dataFactorySnapshot(kind, raw), dataFactorySnapshot(kind, live)) {
			return nil, serviceDenied("datafactory_index_configuration_changed")
		}
		result[id] = dataFactoryMember{id: id, kind: kind, parent: parent.id, root: dataFactoryRoot(id), nodeName: nodeName, raw: live}
	}
	return result, nil
}

func (c *client) dataFactoryObserved(ctx context.Context, member dataFactoryMember) (dataFactoryMember, error) {
	props := object(member.raw["properties"])
	member.state = text(props["provisioningState"])
	switch member.kind {
	case dataFactoryNodeType:
		member.state = text(member.raw["status"])
	case dataFactoryIRType:
		status, err := c.dataFactoryStatus(ctx, member.id, member.raw)
		if err != nil {
			return member, err
		}
		member.status = status
		member.state = text(object(status["properties"])["state"])
	case dataFactoryCDCType:
		request, err := c.dataFactoryOperation(member.id, member.kind, "ChangeDataCapture_Status", nil)
		if err != nil {
			return member, err
		}
		status, err := c.request(ctx, request.Method, request.URL)
		if err != nil {
			return member, err
		}
		if status.status != 200 || operationLocation(status.header) != "" || len(status.data) != 1 || text(status.data["status"]) == "" {
			return member, serviceDenied("invalid_datafactory_cdc_status")
		}
		member.state = text(status.data["status"])
	case dataFactoryTriggerType:
		member.state = text(props["runtimeState"])
		if !slices.Contains([]string{"BlobEventsTrigger", "CustomEventsTrigger"}, text(props["type"])) {
			break
		}
		request, err := c.dataFactoryOperation(member.id, member.kind, "Triggers_GetEventSubscriptionStatus", nil)
		if err != nil {
			return member, err
		}
		status, err := c.request(ctx, request.Method, request.URL)
		if err != nil {
			return member, err
		}
		if status.status != 200 || operationLocation(status.header) != "" || status.data["error"] != nil || !strings.EqualFold(text(status.data["triggerName"]), last(member.id)) || !slices.Contains([]string{"Enabled", "Provisioning", "Deprovisioning", "Disabled", "Unknown"}, text(status.data["status"])) {
			return member, serviceDenied("invalid_datafactory_event_subscription_status")
		}
		member.eventState = text(status.data["status"])
	}
	return member, nil
}

func dataFactoryRuntimeSnapshot(raw map[string]any) (map[string]any, error) {
	props := object(raw["properties"])
	typ := object(props["typeProperties"])
	links := []any{}
	if value, present := typ["links"]; present && value != nil {
		var ok bool
		links, ok = value.([]any)
		if !ok {
			return nil, serviceDenied("invalid_datafactory_runtime_links")
		}
	}
	byIdentity := map[string]any{}
	for _, value := range links {
		row, ok := value.(map[string]any)
		name, factory, subscription := text(row["name"]), text(row["dataFactoryName"]), text(row["subscriptionId"])
		if !ok || name == "" || factory == "" || row["name"] != name || row["dataFactoryName"] != factory || row["subscriptionId"] != subscription || !uuidPattern.MatchString(subscription) || strings.ContainsAny(name+factory, "/\t\r\n") {
			return nil, serviceDenied("invalid_datafactory_runtime_link")
		}
		key := strings.ToLower(subscription + "/" + factory + "/" + name)
		if byIdentity[key] != nil {
			return nil, serviceDenied("duplicate_datafactory_runtime_link")
		}
		byIdentity[key] = row
	}
	// Runtime state, versions, node liveness, queues and diagnostics are transient.
	// Native createTime and each linked runtime's identity/configuration survive.
	return map[string]any{"type": props["type"], "createTime": typ["createTime"], "links": byIdentity}, nil
}

func dataFactoryMemberSnapshot(member dataFactoryMember) (map[string]any, error) {
	result := map[string]any{"resource": dataFactorySnapshot(member.kind, member.raw)}
	if member.kind == dataFactoryIRType {
		snapshot, err := dataFactoryRuntimeSnapshot(member.status)
		if err != nil {
			return nil, err
		}
		// Sharing links are bound separately. Reviewed consumer deletion and
		// RemoveLinks may remove them without replacing the host runtime.
		delete(snapshot, "links")
		result["runtime"] = snapshot
	}
	return result, nil
}

func (c *client) dataFactoryTree(ctx context.Context, root dataFactoryMember, hints map[string]dataFactoryMember) (dataFactoryTree, error) {
	tree := dataFactoryTree{root: root.id, members: map[string]dataFactoryMember{}}
	var walk func(dataFactoryMember) error
	walk = func(member dataFactoryMember) error {
		if tree.members[member.id].id != "" {
			return serviceDenied("duplicate_datafactory_tree_member")
		}
		var err error
		member, err = c.dataFactoryObserved(ctx, member)
		if err != nil {
			return err
		}
		tree.members[member.id] = member
		for _, childKind := range dataFactoryChildKinds(member.kind) {
			children, err := c.dataFactoryIndex(ctx, childKind, member)
			if err != nil {
				return err
			}
			for id, hint := range hints {
				if hint.parent != member.id || hint.kind != childKind || children[id].id != "" {
					continue
				}
				raw, err := c.dataFactoryRead(ctx, id, hint.kind, hint.nodeName)
				if isNotFound(err) {
					continue
				}
				if err != nil {
					return err
				}
				props := object(member.raw["properties"])
				if hint.kind == dataFactoryNodeType && (props["type"] != "SelfHosted" || object(object(props["typeProperties"])["linkedInfo"]) != nil) {
					return serviceDenied("datafactory_node_runtime_type_changed")
				}
				hint.raw = raw
				children[id] = hint
			}
			for _, id := range slices.Sorted(maps.Keys(children)) {
				if err := walk(children[id]); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if root.kind != dataFactoryType || root.parent != "" || c.dataFactoryIdentity(root.id, root.kind) != nil {
		return tree, serviceDenied("invalid_datafactory_tree_root")
	}
	if err := walk(root); err != nil {
		return tree, err
	}
	after, err := c.dataFactoryRead(ctx, root.id, root.kind, "")
	if err != nil {
		return tree, err
	}
	if c.privateConfiguration(dataFactorySnapshot(root.kind, root.raw)) != c.privateConfiguration(dataFactorySnapshot(root.kind, after)) {
		return tree, serviceDenied("datafactory_root_changed_during_walk")
	}
	return tree, nil
}

func (c *client) dataFactoryForest(ctx context.Context, hints map[string]dataFactoryMember) (map[string]dataFactoryTree, map[string]bool, error) {
	roots, err := c.dataFactoryIndex(ctx, dataFactoryType, dataFactoryMember{})
	if err != nil {
		return nil, nil, err
	}
	for id, hint := range hints {
		if hint.kind != dataFactoryType || roots[id].id != "" {
			continue
		}
		raw, err := c.dataFactoryRead(ctx, id, hint.kind, "")
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		hint.raw = raw
		roots[id] = hint
	}
	trees, found, missing := map[string]dataFactoryTree{}, map[string]bool{}, map[string]bool{}
	for _, id := range slices.Sorted(maps.Keys(roots)) {
		tree, err := c.dataFactoryTree(ctx, roots[id], hints)
		if err != nil {
			return nil, nil, err
		}
		trees[id] = tree
		for child := range tree.members {
			if found[child] {
				return nil, nil, serviceDenied("ambiguous_datafactory_owner")
			}
			found[child] = true
		}
	}
	for _, id := range slices.Sorted(maps.Keys(hints)) {
		if found[id] {
			continue
		}
		hint := hints[id]
		_, err := c.dataFactoryRead(ctx, id, hint.kind, hint.nodeName)
		if isNotFound(err) {
			missing[id] = true
			continue
		}
		if err != nil {
			return nil, nil, err
		}
		return nil, nil, serviceDenied("datafactory_live_resource_has_missing_parent")
	}
	return trees, missing, nil
}
