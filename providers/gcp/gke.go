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
	var order []string
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
			order = append(order, key)
		}
		collections[key].names = append(collections[key].names, regexp.QuoteMeta(last(id)))
	}
	type chunk struct {
		each  *collection
		names []string
	}
	var chunks []chunk
	for _, key := range order {
		for names := range slices.Chunk(collections[key].names, computeReadChunk) {
			chunks = append(chunks, chunk{collections[key], names})
		}
	}
	// Lists and GETs run concurrently and merge in a fixed order; a failed list
	// leaves its names to their own GETs below.
	listed := make([][]map[string]any, len(chunks))
	_ = forEachConcurrently(len(chunks), groupReadConcurrency, func(index int) error {
		parameters := cloneParameters(chunks[index].each.parameters)
		parameters["filter"] = "name eq '(" + strings.Join(chunks[index].names, "|") + ")'"
		listed[index], _ = c.nativeList(ctx, chunks[index].each.operation, parameters, "items")
		return nil
	})
	result := map[string]map[string]any{}
	for _, records := range listed {
		for _, record := range records {
			id := c.canonicalName(text(record["selfLink"]))
			if wanted[id] && result[id] == nil {
				result[id] = record
			}
		}
	}
	var missing []string
	queued := map[string]bool{}
	for _, id := range ids {
		if result[id] == nil && !queued[id] {
			queued[id] = true
			missing = append(missing, id)
		}
	}
	reads := make([]map[string]any, len(missing))
	read := make([]bool, len(missing))
	err = forEachConcurrently(len(missing), groupReadConcurrency, func(index int) (err error) {
		reads[index], err = c.nativeGet(ctx, kind, missing[index])
		read[index] = err == nil
		return err
	})
	for index, id := range missing {
		if read[index] {
			result[id] = reads[index]
		}
	}
	// On error the result still holds every successful read, for callers
	// that use it as a prefetch and re-read the rest in their own order.
	return result, err
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
	// Pools and their managed groups are read concurrently up front; the walk
	// below checks them in pool and URL order, as a serial walk did.
	poolReads, poolErrs := make([]map[string]any, len(pools)), make([]error, len(pools))
	if root.Identity.NativeType == clusterType {
		_ = forEachConcurrently(len(pools), groupReadConcurrency, func(i int) error {
			if poolID, err := c.nodePoolID(clusterID, pools[i]); err == nil {
				poolReads[i], poolErrs[i] = c.nativeGet(ctx, nodePoolType, poolID)
			}
			return nil
		})
	} else {
		poolReads[0] = live
	}
	var groupIDs []string
	queued := map[string]bool{}
	for i := range pools {
		if poolErrs[i] != nil {
			continue
		}
		for _, raw := range array(poolReads[i]["instanceGroupUrls"]) {
			if id, err := c.computeID(text(raw), managerType); err == nil && !queued[id] {
				queued[id] = true
				groupIDs = append(groupIDs, id)
			}
		}
	}
	groupReads := make([]gkeGroupRead, len(groupIDs))
	_ = forEachConcurrently(len(groupIDs), groupReadConcurrency, func(i int) error {
		groupReads[i] = c.readGKEGroup(ctx, groupIDs[i])
		return nil
	})
	readGroups := map[string]gkeGroupRead{}
	for i, id := range groupIDs {
		readGroups[id] = groupReads[i]
	}
	groups := map[string]bool{}
	for index, summary := range pools {
		poolID, err := c.nodePoolID(clusterID, summary)
		if err != nil {
			return nil, err
		}
		pool := live
		if root.Identity.NativeType == clusterType {
			pool, err = poolReads[index], poolErrs[index]
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
			read, found := readGroups[id]
			if !found {
				read = c.readGKEGroup(ctx, id)
			}
			if read.err != nil {
				return nil, read.err
			}
			group := read.group
			members = append(members, gkeMember{id: id, kind: managerType, parent: poolID, data: read.data, deletes: true})
			members = append(members, gkeMember{id: group.instanceGroup, kind: instanceGroupType, parent: id, data: read.ig, deletes: true})
			for _, node := range group.nodes {
				members = append(members, gkeMember{id: node.id, kind: instanceType, parent: id, data: node.data, deletes: true})
				for _, resource := range node.resources {
					for _, raw := range array(node.data["disks"]) {
						disk := object(raw)
						if disk["boot"] == true && c.canonicalName(text(disk["source"])) == resource.id {
							resource.delete = true // GKE deletes node boot disks with the pool.
						}
					}
					if err := add(gkeMember{id: resource.id, kind: resource.kind, parent: node.id, data: read.reads[resource.id], deletes: resource.delete, shared: resource.shared}); err != nil {
						return nil, err
					}
				}
			}
		}
	}
	return members, nil
}

// gkeGroupRead is one node pool managed group with its instance group and node
// resources, or the first error reading them in the serial order.
type gkeGroupRead struct {
	data, ig map[string]any
	group    managedGroup
	reads    map[string]map[string]any
	err      error
}

func (c *client) readGKEGroup(ctx context.Context, id string) (read gkeGroupRead) {
	if read.data, read.err = c.nativeGet(ctx, managerType, id); read.err != nil {
		return read
	}
	if read.group, read.err = c.loadManagedGroup(ctx, id, read.data); read.err != nil {
		return read
	}
	// GKE uses its own autoscaler. A foreign Compute autoscaler must be
	// resolved explicitly before GKE can delete this group.
	if read.group.autoscaler != "" {
		read.err = groupDenied("gke_group_has_compute_autoscaler")
		return read
	}
	if read.ig, read.err = c.nativeGet(ctx, instanceGroupType, read.group.instanceGroup); read.err != nil {
		return read
	}
	byKind := map[string][]string{}
	for _, node := range read.group.nodes {
		for _, resource := range node.resources {
			byKind[resource.kind] = append(byKind[resource.kind], resource.id)
		}
	}
	read.reads = map[string]map[string]any{}
	for kind, ids := range byKind {
		data, err := c.computeReads(ctx, kind, ids)
		if err != nil {
			read.err = err
			return read
		}
		maps.Copy(read.reads, data)
	}
	return read
}

// readGKENetwork reads the planned network resources selected by want (all
// when nil) concurrently; callers check the results in plan order.
func (c *client) readGKENetwork(ctx context.Context, resources []gkeNetworkResource, want func(gkeNetworkResource) bool) ([]map[string]any, []error) {
	data, errs := make([]map[string]any, len(resources)), make([]error, len(resources))
	_ = forEachConcurrently(len(resources), groupReadConcurrency, func(i int) error {
		if want == nil || want(resources[i]) {
			data[i], errs[i] = c.nativeGet(ctx, resources[i].Kind, resources[i].ID)
		}
		return nil
	})
	return data, errs
}

func (h *computeGroups) contributeGKE(ctx context.Context, assets []asset.Asset) (governance.Contribution, map[string]bool, error) {
	indexed := indexManagedAssets(assets)
	result := governance.Contribution{}
	owned := map[string]bool{}
	// Process clusters before standalone node pools so each binding has one
	// native controller and no duplicate Compute contribution.
	for _, rootKind := range []string{clusterType, nodePoolType} {
		var roots []asset.Asset
		for _, root := range assets {
			if root.Identity.Provider == asset.ProviderGCP && root.Identity.NativeType == rootKind && !owned[root.Identity.NativeID] {
				roots = append(roots, root)
			}
		}
		// Live reads run concurrently; bindings are added in asset order.
		lives := make([]map[string]any, len(roots))
		reads := make([][]gkeMember, len(roots))
		if err := forEachConcurrently(len(roots), groupReadConcurrency, func(index int) error {
			root := roots[index]
			live, err := h.client.nativeGet(ctx, rootKind, root.Identity.NativeID)
			if err != nil {
				return err
			}
			members, err := h.client.gkeMembers(ctx, root, live)
			if err != nil {
				return err
			}
			if rootKind == clusterType && root.Normalized[gkeNetworkKey] != nil {
				network, err := plannedGKENetwork(root)
				if err != nil {
					return err
				}
				datas, errs := h.client.readGKENetwork(ctx, network.Resources, nil)
				for i, resource := range network.Resources {
					data, err := datas[i], errs[i]
					if err != nil {
						return err
					}
					if text(data["id"]) != resource.UID {
						return groupDenied("gke_network_resource_identity_changed")
					}
					members = append(members, gkeMember{id: resource.ID, kind: resource.Kind, parent: root.Identity.NativeID, data: data, deletes: resource.Delete, shared: !resource.Delete})
				}
			}
			lives[index], reads[index] = live, members
			return nil
		}); err != nil {
			return result, owned, err
		}
		for index, root := range roots {
			// A root claimed by an earlier root of this pass is skipped, as before.
			if owned[root.Identity.NativeID] {
				continue
			}
			live, members := lives[index], reads[index]
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
				managed, err := addGroupBinding(&result, indexed, owner, member.kind, member.id, ownership, policy, member.deletes)
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
