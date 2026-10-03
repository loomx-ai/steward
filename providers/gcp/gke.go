package gcp

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const clusterType = "container.googleapis.com/Cluster"
const nodePoolType = "container.googleapis.com/NodePool"
const gkeSource = "gcp:gke"

type gkeMember struct {
	id, kind, parent string
	data             map[string]any
	deletes, shared  bool
}

func (c *client) nativeGet(ctx context.Context, kind, id string) (map[string]any, error) {
	resource, ok := findType(kind)
	if !ok {
		return nil, fmt.Errorf("unknown GCP resource kind")
	}
	endpoint, err := c.resourceURL(resource, id)
	if err != nil {
		return nil, err
	}
	return c.request(ctx, "GET", endpoint, nil)
}

// computeReads reads Compute resources of one kind by listing each zone or
// region collection once, filtered to their names, instead of one GET per
// resource. Compute lists return complete resources. Anything a list does not
// return (or a list that fails) is read with its own GET, so the result always
// holds a native read of every requested resource.
func (c *client) computeReads(ctx context.Context, kind string, ids []string) (map[string]map[string]any, error) {
	rule, ok := findType(kind)
	if !ok {
		return nil, fmt.Errorf("unknown GCP resource kind")
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	type collection struct {
		operation  catalog.Operation
		parameters map[string]any
		names      []string
	}
	collections := map[string]*collection{}
	wanted := map[string]bool{}
	for _, id := range ids {
		read, parameters, err := c.resourceOperation(rule, id, "GET")
		if err != nil {
			return nil, err
		}
		list, ok := metadata.catalog.Operation(strings.TrimSuffix(read.ID, ".get") + ".list")
		if !ok || wanted[id] {
			continue
		}
		wanted[id] = true
		scope := map[string]any{}
		for name, value := range parameters {
			if object(list.InputSchema["properties"])[name] != nil {
				scope[name] = value
			}
		}
		key := list.ID + fmt.Sprint(scope)
		if collections[key] == nil {
			collections[key] = &collection{operation: list, parameters: scope}
		}
		collections[key].names = append(collections[key].names, regexp.QuoteMeta(last(id)))
	}
	result := map[string]map[string]any{}
	for _, each := range collections {
		for names := range slices.Chunk(each.names, computeReadChunk) {
			parameters := cloneParameters(each.parameters)
			parameters["filter"] = "name eq '(" + strings.Join(names, "|") + ")'"
			records, err := c.nativeList(ctx, each.operation, parameters, "items")
			if err != nil {
				continue // Read each of these with its own GET below.
			}
			for _, record := range records {
				id := c.canonicalName(text(record["selfLink"]))
				if wanted[id] && result[id] == nil {
					result[id] = record
				}
			}
		}
	}
	for _, id := range ids {
		if result[id] != nil {
			continue
		}
		data, err := c.nativeGet(ctx, kind, id)
		if err != nil {
			return nil, err
		}
		result[id] = data
	}
	return result, nil
}

// computeReadChunk bounds the names one filtered list carries in its URL.
const computeReadChunk = 50

func gkeStable(kind string, data map[string]any) bool {
	switch text(data["status"]) {
	case "RUNNING", "ERROR":
		return true
	case "DEGRADED":
		return kind == clusterType
	case "RUNNING_WITH_ERROR":
		return kind == nodePoolType
	}
	return false
}

func (c *client) nodePoolID(cluster string, data map[string]any) (string, error) {
	name := text(data["name"])
	if !segmentPattern.MatchString(name) || name == "." || name == ".." {
		return "", fmt.Errorf("invalid GKE node pool name")
	}
	id := cluster + "/nodePools/" + name
	kind, _ := findType(nodePoolType)
	if _, err := c.resourceURL(kind, id); err != nil {
		return "", err
	}
	if link := text(data["selfLink"]); link != "" && c.canonicalName(link) != id {
		return "", fmt.Errorf("GKE node pool identity mismatch")
	}
	return id, nil
}

// gkeMembers obtains ownership from native nodePools.instanceGroupUrls. Names,
// labels and GKE-looking prefixes alone never authorize controller deletion.
func (c *client) gkeMembers(ctx context.Context, root asset.Asset, live map[string]any) ([]gkeMember, error) {
	if !gkeStable(root.Identity.NativeType, live) {
		return nil, groupDenied("gke_controller_not_stable")
	}
	clusterID := strings.Split(root.Identity.NativeID, "/nodePools/")[0]
	pools := []map[string]any{live}
	cluster := live
	if root.Identity.NativeType == clusterType {
		records, err := productRecords(live, "nodePools")
		if err != nil {
			return nil, err
		}
		pools = nil
		for _, record := range records {
			pools = append(pools, record.Data)
		}
	} else {
		var err error
		cluster, err = c.nativeGet(ctx, clusterType, clusterID)
		if err != nil {
			return nil, err
		}
		if object(cluster["autopilot"])["enabled"] == true || object(live["autopilotConfig"])["enabled"] == true {
			return nil, groupDenied("gke_autopilot_pool_requires_cluster_cleanup")
		}
	}
	if text(cluster["id"]) == "" || text(cluster["name"]) != last(clusterID) {
		return nil, fmt.Errorf("GKE cluster identity is incomplete")
	}
	if link := text(cluster["selfLink"]); link != "" && c.canonicalName(link) != clusterID {
		return nil, fmt.Errorf("GKE cluster identity mismatch")
	}
	var members []gkeMember
	seen := map[string]bool{}
	add := func(member gkeMember) error {
		key := member.parent + "\x00" + member.id
		if seen[key] {
			return fmt.Errorf("duplicate GKE member")
		}
		seen[key] = true
		members = append(members, member)
		return nil
	}
	groups := map[string]bool{}
	for _, summary := range pools {
		poolID, err := c.nodePoolID(clusterID, summary)
		if err != nil {
			return nil, err
		}
		pool := live
		if root.Identity.NativeType == clusterType {
			pool, err = c.nativeGet(ctx, nodePoolType, poolID)
			if err != nil {
				return nil, err
			}
			if _, err := c.nodePoolID(clusterID, pool); err != nil || text(pool["name"]) != text(summary["name"]) {
				return nil, fmt.Errorf("GKE node pool identity changed")
			}
			if err := add(gkeMember{id: poolID, kind: nodePoolType, parent: clusterID, data: pool, deletes: true}); err != nil {
				return nil, err
			}
		}
		if !gkeStable(nodePoolType, pool) {
			return nil, groupDenied("gke_node_pool_not_stable")
		}
		urls, ok := pool["instanceGroupUrls"].([]any)
		if pool["instanceGroupUrls"] != nil && !ok {
			return nil, fmt.Errorf("invalid GKE instance group list")
		}
		for _, raw := range urls {
			id, err := c.computeID(text(raw), managerType)
			if err != nil {
				return nil, err
			}
			if groups[id] {
				return nil, fmt.Errorf("GKE instance group claimed by multiple node pools")
			}
			groups[id] = true
			groupData, err := c.nativeGet(ctx, managerType, id)
			if err != nil {
				return nil, err
			}
			group, err := c.loadManagedGroup(ctx, id, groupData)
			if err != nil {
				return nil, err
			}
			// GKE uses its own autoscaler. A foreign Compute autoscaler must be
			// resolved explicitly before GKE can delete this group.
			if group.autoscaler != "" {
				return nil, groupDenied("gke_group_has_compute_autoscaler")
			}
			members = append(members, gkeMember{id: id, kind: managerType, parent: poolID, data: groupData, deletes: true})
			ig, err := c.nativeGet(ctx, instanceGroupType, group.instanceGroup)
			if err != nil {
				return nil, err
			}
			members = append(members, gkeMember{id: group.instanceGroup, kind: instanceGroupType, parent: id, data: ig, deletes: true})
			byKind := map[string][]string{}
			for _, node := range group.nodes {
				for _, resource := range node.resources {
					byKind[resource.kind] = append(byKind[resource.kind], resource.id)
				}
			}
			reads := map[string]map[string]any{}
			for kind, ids := range byKind {
				data, err := c.computeReads(ctx, kind, ids)
				if err != nil {
					return nil, err
				}
				maps.Copy(reads, data)
			}
			for _, node := range group.nodes {
				members = append(members, gkeMember{id: node.id, kind: instanceType, parent: id, data: node.data, deletes: true})
				for _, resource := range node.resources {
					for _, raw := range array(node.data["disks"]) {
						disk := object(raw)
						if disk["boot"] == true && c.canonicalName(text(disk["source"])) == resource.id {
							resource.delete = true // GKE deletes node boot disks with the pool.
						}
					}
					if err := add(gkeMember{id: resource.id, kind: resource.kind, parent: node.id, data: reads[resource.id], deletes: resource.delete, shared: resource.shared}); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return members, nil
}

func (h *computeGroups) contributeGKE(ctx context.Context, assets []asset.Asset) (governance.Contribution, map[string]bool, error) {
	result := governance.Contribution{}
	owned := map[string]bool{}
	// Process clusters before standalone node pools so each binding has one
	// native controller and no duplicate Compute contribution.
	for _, rootKind := range []string{clusterType, nodePoolType} {
		for _, root := range assets {
			if root.Identity.Provider != asset.ProviderGCP || root.Identity.NativeType != rootKind || owned[root.Identity.NativeID] {
				continue
			}
			live, err := h.client.nativeGet(ctx, rootKind, root.Identity.NativeID)
			if err != nil {
				return result, owned, err
			}
			members, err := h.client.gkeMembers(ctx, root, live)
			if err != nil {
				return result, owned, err
			}
			if rootKind == clusterType && root.Normalized[gkeNetworkKey] != nil {
				network, err := plannedGKENetwork(root)
				if err != nil {
					return result, owned, err
				}
				for _, resource := range network.Resources {
					data, err := h.client.nativeGet(ctx, resource.Kind, resource.ID)
					if err != nil {
						return result, owned, err
					}
					if text(data["id"]) != resource.UID {
						return result, owned, groupDenied("gke_network_resource_identity_changed")
					}
					members = append(members, gkeMember{id: resource.ID, kind: resource.Kind, parent: root.Identity.NativeID, data: data, deletes: resource.Delete, shared: !resource.Delete})
				}
			}
			controllers := map[string]asset.Asset{root.Identity.NativeID: root}
			for _, member := range members {
				owner, exists := controllers[member.parent]
				if !exists {
					continue // Its missing parent already creates an unresolved reference.
				}
				ownership, policy := graph.OwnershipExclusive, graph.CleanupDelegate
				if member.shared {
					ownership, policy = graph.OwnershipShared, graph.CleanupRetain
				}
				managed, err := addGroupBinding(&result, assets, owner, member.kind, member.id, ownership, policy, member.deletes)
				if err != nil {
					return result, owned, err
				}
				if managed.ID == "" {
					continue
				}
				binding := &result.Bindings[len(result.Bindings)-1]
				// Keep nodes available while cluster workload controllers finalize
				// their load balancers. A Standard pool can still be selected alone.
				binding.DirectCleanupAllowed = member.kind == nodePoolType && object(live["autopilot"])["enabled"] != true && object(member.data["autopilotConfig"])["enabled"] != true
				binding.EvidenceSource = gkeSource
				binding.Evidence["lifecycle_kind"] = "gke"
				binding.Evidence["retention_supported"] = !member.deletes
				binding.Evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] = member.deletes
				controllers[member.id] = managed
				owned[member.id] = true
			}
		}
	}
	return result, owned, nil
}
