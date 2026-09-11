package azure

import (
	"context"
	"maps"
	"slices"
	"strings"
)

// Native GetStatus links identify the consuming subscription, factory and
// runtime, but omit the resource group. Resolve them through the native
// factory index; do not manufacture an ARM ID for an unobserved factory.
func (c *client) dataFactorySharing(ctx context.Context, trees map[string]dataFactoryTree, known map[string]map[string]any) (map[string]any, error) {
	knownLinks := map[string]any{}
	for id, normalized := range known {
		_, typ, _ := parseID(id)
		if _, err := c.dataFactoryRecorded(id, dataFactoryKind(typ), normalized); err != nil {
			return nil, err
		}
		for owner, values := range object(normalized["_datafactory_links"]) {
			for key, raw := range object(values) {
				entry := object(raw)
				if source := text(entry["source"]); source != "" {
					name := owner + "/" + key
					if previous := object(knownLinks[name]); previous != nil && (previous["source"] != source || previous["configuration"] != entry["configuration"]) {
						return nil, serviceDenied("conflicting_datafactory_known_link")
					}
					knownLinks[name] = entry
				}
			}
		}
	}
	factories := map[string]string{}
	queue := []string{}
	for rootID, tree := range trees {
		key := strings.ToLower(strings.Split(rootID, "/")[2] + "/" + last(rootID))
		if factories[key] != "" && factories[key] != rootID {
			return nil, serviceDenied("ambiguous_datafactory_factory_name")
		}
		factories[key] = rootID
		for id, member := range tree.members {
			if member.kind == dataFactoryIRType {
				queue = append(queue, id)
			}
		}
	}
	slices.Sort(queue)
	links, consumers := map[string]any{}, map[string]string{}
	for at := 0; at < len(queue); at++ {
		if at >= 10000 {
			return nil, serviceDenied("datafactory_sharing_index_too_large")
		}
		ownerID := queue[at]
		owner := trees[dataFactoryRoot(ownerID)].members[ownerID]
		snapshot, err := dataFactoryRuntimeSnapshot(owner.status)
		if err != nil {
			return nil, err
		}
		for _, key := range slices.Sorted(maps.Keys(object(snapshot["links"]))) {
			row := object(object(snapshot["links"])[key])
			factory, subscription, name := text(row["dataFactoryName"]), strings.ToLower(text(row["subscriptionId"])), text(row["name"])
			if object(owner.raw["properties"])["type"] != "SelfHosted" {
				return nil, serviceDenied("datafactory_managed_runtime_has_sharing_links")
			}
			entry := map[string]any{"factoryName": factory, "subscription": subscription, "runtimeName": name, "configuration": c.privateConfiguration(row)}
			rootID := factories[subscription+"/"+strings.ToLower(factory)]
			if rootID == "" {
				entry["unresolved"] = true
				// A reviewed consumer factory may already be deleted. Its
				// authenticated native identity can resolve a stale host link,
				// but only after both named resources independently return 404.
				if previous := object(knownLinks[ownerID+"/"+key]); previous != nil && previous["configuration"] == entry["configuration"] {
					source := text(previous["source"])
					root := dataFactoryRoot(source)
					if c.dataFactoryIdentity(source, dataFactoryIRType) != nil || !strings.EqualFold(last(root), factory) || !strings.EqualFold(last(source), name) || strings.Split(source, "/")[2] != subscription {
						return nil, serviceDenied("invalid_datafactory_known_link_source")
					}
					for _, target := range []struct{ id, kind string }{{root, dataFactoryType}, {source, dataFactoryIRType}} {
						_, err := c.dataFactoryRead(ctx, target.id, target.kind, "")
						if !isNotFound(err) {
							if err == nil {
								err = serviceDenied("datafactory_link_source_changed_during_walk")
							}
							return nil, err
						}
					}
					delete(entry, "unresolved")
					entry["source"], entry["absent"] = source, true
				}
			} else {
				id := rootID + "/integrationruntimes/" + strings.ToLower(name)
				if c.dataFactoryIdentity(id, dataFactoryIRType) != nil || id == ownerID {
					return nil, serviceDenied("invalid_datafactory_linked_runtime_identity")
				}
				entry["source"] = id
				tree := trees[rootID]
				consumer := tree.members[id]
				if consumer.id == "" {
					// A native link is also a discovery hint. A present but
					// unlisted consumer retains its complete own configuration.
					raw, err := c.dataFactoryRead(ctx, id, dataFactoryIRType, "")
					if isNotFound(err) {
						entry["absent"] = true
					} else if err != nil {
						return nil, err
					} else {
						consumer, err = c.dataFactoryObserved(ctx, dataFactoryMember{id: id, kind: dataFactoryIRType, parent: rootID, root: rootID, raw: raw})
						if err != nil {
							return nil, err
						}
						tree.members[id] = consumer
						nodes, err := c.dataFactoryIndex(ctx, dataFactoryNodeType, consumer)
						if err != nil {
							return nil, err
						}
						for nodeID, node := range nodes {
							node, err = c.dataFactoryObserved(ctx, node)
							if err != nil || tree.members[nodeID].id != "" {
								return nil, serviceDenied("invalid_datafactory_linked_runtime_nodes")
							}
							tree.members[nodeID] = node
						}
						trees[rootID] = tree
						queue = append(queue, id)
					}
				}
				if consumer.id != "" {
					linked := object(object(object(consumer.raw["properties"])["typeProperties"])["linkedInfo"])
					if object(consumer.raw["properties"])["type"] != "SelfHosted" || !slices.Contains([]string{"RBAC", "Key"}, text(linked["authorizationType"])) || linked["authorizationType"] == "RBAC" && !strings.EqualFold(text(linked["resourceId"]), ownerID) {
						return nil, serviceDenied("datafactory_shared_runtime_owner_changed")
					}
					if consumers[id] != "" && consumers[id] != ownerID {
						return nil, serviceDenied("ambiguous_datafactory_shared_runtime_owner")
					}
					consumers[id] = ownerID
					configuration, err := dataFactoryMemberSnapshot(consumer)
					if err != nil {
						return nil, err
					}
					entry["source_configuration"] = c.privateConfiguration(configuration)
				}
			}
			if links[ownerID] == nil {
				links[ownerID] = map[string]any{}
			}
			object(links[ownerID])[key] = entry
		}
	}
	return links, nil
}

func (c *client) dataFactoryRelations(ctx context.Context, trees map[string]dataFactoryTree, known map[string]map[string]any) error {
	links, err := c.dataFactorySharing(ctx, trees, known)
	if err != nil {
		return err
	}
	sharedOwners := map[string][]string{}
	for ownerID, values := range links {
		for _, value := range object(values) {
			entry := object(value)
			if source := text(entry["source"]); source != "" && entry["absent"] != true {
				sharedOwners[source] = append(sharedOwners[source], ownerID)
			}
		}
	}
	observedOwners := maps.Clone(sharedOwners)
	for _, normalized := range known {
		for owner, values := range object(normalized["_datafactory_incoming"]) {
			if trees[dataFactoryRoot(owner)].members[owner].kind != dataFactoryIRType {
				continue
			}
			for source, value := range object(values) {
				consumer := trees[dataFactoryRoot(source)].members[source]
				linked := object(object(object(consumer.raw["properties"])["typeProperties"])["linkedInfo"])
				if object(value)["shared"] == true && consumer.kind == dataFactoryIRType && linked["authorizationType"] == "Key" && len(observedOwners[source]) == 0 {
					// An omitted GetStatus link cannot prove a live Key-linked
					// consumer was detached: its own GET has no owner resourceId.
					if len(sharedOwners[source]) != 0 && !slices.Contains(sharedOwners[source], owner) {
						return serviceDenied("conflicting_datafactory_known_sharing_owner")
					}
					sharedOwners[source] = []string{owner}
				}
			}
		}
	}
	incoming, external := map[string]any{}, map[string][]serviceChild{}
	for _, root := range slices.Sorted(maps.Keys(trees)) {
		tree := trees[root]
		for _, id := range slices.Sorted(maps.Keys(tree.members)) {
			member := tree.members[id]
			member.refs, err = c.dataFactoryReferences(ctx, id, member.kind, member.raw, external)
			if err != nil {
				return err
			}
			for _, ownerID := range sharedOwners[id] {
				addReference(member.refs, dataFactoryIRType, ownerID)
			}
			configuration, err := dataFactoryMemberSnapshot(member)
			if err != nil {
				return err
			}
			for kind, ids := range member.refs {
				slices.Sort(ids)
				if dataFactoryKind(kind) == "" {
					continue
				}
				for _, target := range ids {
					if target == member.parent || target == id {
						continue // Immediate ownership has its own lifecycle binding.
					}
					if incoming[target] == nil {
						incoming[target] = map[string]any{}
					}
					entry := map[string]any{"kind": member.kind, "configuration": c.privateConfiguration(configuration)}
					if slices.Contains(sharedOwners[id], target) {
						entry["shared"] = true
					}
					object(incoming[target])[id] = entry
				}
			}
			tree.members[id] = member
		}
		trees[root] = tree
	}
	for root, tree := range trees {
		tree.incoming, tree.links = map[string]any{}, map[string]any{}
		for target, values := range incoming {
			if dataFactoryRoot(target) == root {
				tree.incoming[target] = values
			}
		}
		for target, values := range links {
			if dataFactoryRoot(target) == root {
				tree.links[target] = values
			}
		}
		for id, member := range tree.members {
			if member.kind == dataFactoryNodeType && incoming[member.parent] != nil {
				// A physical registration serves its owning runtime's consumers.
				// A linked runtime's node status view is not another registration.
				tree.incoming[id] = maps.Clone(object(incoming[member.parent]))
			}
		}
		trees[root] = tree
	}
	return nil
}

func dataFactoryDirectMembers(tree dataFactoryTree) map[string]bool {
	direct := map[string]bool{}
	queue := []string{}
	for id, member := range tree.members {
		props := object(member.raw["properties"])
		ssis := member.kind == dataFactoryIRType && props["type"] == "Managed" && object(object(props["typeProperties"])["ssisProperties"]) != nil
		linked := member.kind == dataFactoryIRType && object(object(props["typeProperties"])["linkedInfo"]) != nil
		if member.kind == dataFactoryTriggerType || member.kind == dataFactoryCDCType || ssis || linked {
			direct[id] = true
			queue = append(queue, id)
		}
	}
	// SSIS runtimes are stopped and removed before their factory. Consumers
	// of a direct prerequisite must also run before that prerequisite; other
	// authored artifacts may disappear in the factory's native cascade.
	for i := 0; i < len(queue); i++ {
		for source := range object(tree.incoming[queue[i]]) {
			if member := tree.members[source]; member.id != "" && member.parent == tree.root && !direct[source] {
				direct[source] = true
				queue = append(queue, source)
			}
		}
	}
	return direct
}

func dataFactoryController(member dataFactoryMember) string {
	if member.kind == dataFactoryEndpointType {
		// Managed virtual networks have no DELETE operation. The factory's
		// native cascade owns endpoint deletion; the network is an ancestor
		// required by the endpoint's independently supported DELETE.
		return member.root
	}
	return member.parent
}

func dataFactorySharingProtection(id, kind string, links map[string]any) string {
	for ownerID, values := range links {
		if ownerID != id && !(kind == dataFactoryType && dataFactoryRoot(ownerID) == id) && !(kind == dataFactoryNodeType && dataFactoryParent(id, kind) == ownerID) {
			continue
		}
		for _, value := range object(values) {
			if object(value)["unresolved"] == true {
				return "azure_datafactory_shared_runtime_unresolved"
			}
		}
	}
	return ""
}
