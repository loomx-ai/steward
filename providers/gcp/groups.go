package gcp

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const managerType = "compute.googleapis.com/InstanceGroupManager"
const instanceGroupType = "compute.googleapis.com/InstanceGroup"
const autoscalerType = "compute.googleapis.com/Autoscaler"
const groupSource = "gcp:managed-instance-group"

type groupResource struct {
	id, kind, slot string
	delete         bool
	stateful       bool
	shared         bool
	state          map[string]any
}

type managedNode struct {
	id        string
	data      map[string]any
	config    map[string]any
	resources []groupResource
}

type managedGroup struct {
	id, instanceGroup, autoscaler string
	data                          map[string]any
	nodes                         []managedNode
}

type computeGroups struct{ client *client }

func (r *Runtime) ComputeLifecycle(ctx context.Context, id asset.ConnectionID) (governance.Contributor, error) {
	c, err := r.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	return &computeGroups{client: c}, nil
}

func groupBusy() error {
	return &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorRetryable, Code: "managed_group_not_stable", Message: contracts.SafeProviderValidationMessage}, RetryAfter: 10 * time.Second}
}

func (c *client) computeID(value, nativeType string) (string, error) {
	if strings.HasPrefix(value, "projects/") {
		value = "//compute.googleapis.com/" + value
	}
	id := c.canonicalName(value)
	kind, found := findType(nativeType)
	if !found {
		return "", fmt.Errorf("unknown Compute resource kind")
	}
	if _, err := c.resourceURL(kind, id); err != nil {
		return "", err
	}
	return id, nil
}

// groupList uses the native POST list methods. The requests are read-only even
// though Compute exposes them as POSTs; they never send a modification body.
func (c *client) groupList(ctx context.Context, id, method, itemsPath string) ([]map[string]any, error) {
	kind, _ := findType(managerType)
	read, parameters, err := c.resourceOperation(kind, id, "GET")
	if err != nil {
		return nil, err
	}
	metadata, _ := providerData()
	operation, ok := metadata.catalog.Operation(strings.TrimSuffix(read.ID, ".get") + "." + method)
	if !ok {
		return nil, fmt.Errorf("managed group has no native %s operation", method)
	}
	return c.nativeList(ctx, operation, parameters, itemsPath)
}

func (c *client) nativeList(ctx context.Context, operation catalog.Operation, parameters map[string]any, itemsPath string) ([]map[string]any, error) {
	properties := object(operation.InputSchema["properties"])
	if properties["maxResults"] != nil {
		parameters["maxResults"] = 500
	}
	var result []map[string]any
	seen := map[string]bool{}
	for {
		bound, err := catalog.BindREST(operation, parameters)
		if err != nil {
			return nil, err
		}
		response, err := c.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
		if err != nil {
			return nil, err
		}
		if err := checkListCompleteness(response.Data); err != nil {
			return nil, err
		}
		records, err := productRecords(response.Data, itemsPath)
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			result = append(result, record.Data)
		}
		next := text(response.Data["nextPageToken"])
		if next == "" {
			return result, nil
		}
		if seen[next] || properties["pageToken"] == nil {
			return nil, fmt.Errorf("Google lifecycle pagination did not advance")
		}
		seen[next] = true
		parameters["pageToken"] = next
	}
}

func (c *client) loadManagedGroup(ctx context.Context, id string, data map[string]any, pendingVM ...string) (managedGroup, error) {
	result := managedGroup{id: id, data: data}
	if object(data["status"])["isStable"] != true {
		return result, groupBusy()
	}
	if object(object(data["status"])["versionTarget"])["isReached"] == false {
		return result, groupBusy()
	}
	var err error
	result.instanceGroup, err = c.computeID(text(data["instanceGroup"]), instanceGroupType)
	if err != nil {
		return result, err
	}
	if strings.TrimSuffix(result.instanceGroup, "/instanceGroups/"+last(result.instanceGroup)) != strings.TrimSuffix(id, "/instanceGroupManagers/"+last(id)) {
		return result, fmt.Errorf("managed instance group belongs to another scope")
	}
	if source := text(object(data["status"])["autoscaler"]); source != "" {
		result.autoscaler, err = c.computeID(source, autoscalerType)
		if err != nil {
			return result, err
		}
		kind, _ := findType(autoscalerType)
		endpoint, _ := c.resourceURL(kind, result.autoscaler)
		autoscaler, err := c.request(ctx, "GET", endpoint, nil)
		if isNotFound(err) {
			result.autoscaler = ""
		} else if err != nil {
			return result, err
		} else if target, err := c.computeID(text(autoscaler["target"]), managerType); err != nil || target != id {
			return result, fmt.Errorf("autoscaler targets another managed instance group")
		}
	}
	configs, err := c.groupList(ctx, id, "listPerInstanceConfigs", "items")
	if err != nil {
		return result, err
	}
	byName := map[string]map[string]any{}
	for _, config := range configs {
		name := text(config["name"])
		if !segmentPattern.MatchString(name) || strings.Contains(name, "/") || name == "." || name == ".." || byName[name] != nil {
			return result, fmt.Errorf("invalid or duplicate per-instance configuration")
		}
		pending := len(pendingVM) == 1 && last(pendingVM[0]) == name && text(config["status"]) == "UNAPPLIED"
		if text(config["status"]) != "EFFECTIVE" && !pending {
			return result, groupBusy()
		}
		byName[name] = config
	}
	members, err := c.groupList(ctx, id, "listManagedInstances", "managedInstances")
	if err != nil {
		return result, err
	}
	seen, names := map[string]bool{}, map[string]bool{}
	vmKind, _ := findType(instanceType)
	for _, member := range members {
		nativeID, err := c.computeID(text(member["instance"]), instanceType)
		if err != nil {
			return result, err
		}
		if seen[nativeID] || names[last(nativeID)] {
			return result, fmt.Errorf("duplicate managed instance identity")
		}
		seen[nativeID], names[last(nativeID)] = true, true
		// A regional MIG spans zones in its region; a zonal MIG cannot claim a
		// VM in another zone. The native project was already checked above.
		groupParts := strings.Split(strings.TrimPrefix(id, "//compute.googleapis.com/"), "/")
		vmParts := strings.Split(strings.TrimPrefix(nativeID, "//compute.googleapis.com/"), "/")
		if (groupParts[2] == "zones" && groupParts[3] != vmParts[3]) || (groupParts[2] == "regions" && groupParts[3] != regionOf(vmParts[3])) {
			return result, fmt.Errorf("managed instance is outside its group location")
		}
		if text(member["currentAction"]) != "NONE" || text(member["id"]) == "" {
			return result, groupBusy()
		}
		endpoint, _ := c.resourceURL(vmKind, nativeID)
		vm, err := c.request(ctx, "GET", endpoint, nil)
		if err != nil {
			return result, err
		}
		if text(vm["id"]) != text(member["id"]) {
			return result, fmt.Errorf("managed VM incarnation changed during discovery")
		}
		if link := text(vm["selfLink"]); link != "" && c.canonicalName(link) != nativeID {
			return result, fmt.Errorf("managed VM identity mismatch")
		}
		node := managedNode{id: nativeID, data: vm, config: byName[last(nativeID)]}
		node.resources, err = c.managedNodeResources(ctx, node, member)
		if err != nil {
			return result, err
		}
		result.nodes = append(result.nodes, node)
	}
	for name := range byName {
		if !names[name] {
			return result, groupBusy()
		}
	}
	target := int64(0)
	for _, key := range []string{"targetSize", "targetStoppedSize", "targetSuspendedSize"} {
		if data[key] == nil && key != "targetSize" {
			continue
		}
		size, err := strconv.ParseInt(fmt.Sprint(data[key]), 10, 32)
		if err != nil || size < 0 {
			return result, fmt.Errorf("invalid managed group target size")
		}
		target += size
	}
	if target != int64(len(result.nodes)) {
		return result, groupBusy()
	}
	sort.Slice(result.nodes, func(i, j int) bool { return result.nodes[i].id < result.nodes[j].id })
	return result, nil
}

func (c *client) managedNodeResources(ctx context.Context, node managedNode, member map[string]any) ([]groupResource, error) {
	disks, err := instanceDisks(c, node.data)
	if err != nil {
		return nil, err
	}
	resources := map[string]groupResource{}
	for _, disk := range disks {
		resources["disks:"+disk.device] = groupResource{id: disk.id, kind: disk.kind, slot: "disks:" + disk.device, delete: disk.autoDelete}
	}
	for _, raw := range array(node.data["disks"]) {
		disk := object(raw)
		if text(disk["mode"]) == "READ_ONLY" {
			slot := "disks:" + text(disk["deviceName"])
			resource, found := resources[slot]
			if found {
				if resource.delete {
					return nil, fmt.Errorf("read-only disk cannot be automatically deleted")
				}
				resource.shared = true
				resources[slot] = resource
			}
		}
	}
	// Effective per-instance state takes precedence over the group policy.
	for _, key := range []string{"preservedStateFromPolicy", "preservedStateFromConfig"} {
		state := object(member[key])
		if state == nil && member[key] != nil {
			return nil, fmt.Errorf("invalid managed instance preserved state")
		}
		for category, raw := range state {
			if category == "metadata" {
				continue
			}
			if category != "disks" && category != "internalIPs" && category != "externalIPs" {
				return nil, fmt.Errorf("unsupported managed instance preserved resource")
			}
			values := object(raw)
			if values == nil && raw != nil {
				return nil, fmt.Errorf("invalid managed instance preserved resource map")
			}
			for device, value := range values {
				entry := object(value)
				if entry == nil || device == "" || !segmentPattern.MatchString(device) {
					return nil, fmt.Errorf("invalid managed instance preserved resource")
				}
				policy := text(entry["autoDelete"])
				if policy != "" && policy != "NEVER" && policy != "ON_PERMANENT_INSTANCE_DELETION" {
					return nil, fmt.Errorf("invalid stateful resource deletion rule")
				}
				slot := category + ":" + device
				resource := groupResource{slot: slot, delete: policy == "ON_PERMANENT_INSTANCE_DELETION", stateful: true, state: entry}
				if category == "disks" {
					resource.kind = "compute.googleapis.com/Disk"
					if strings.Contains(text(entry["source"]), "/regions/") {
						resource.kind = "compute.googleapis.com/RegionDisk"
					}
					resource.id, err = c.computeID(text(entry["source"]), resource.kind)
					attached, ok := resources[slot]
					if err != nil || !ok || resource.id != attached.id || (text(entry["mode"]) == "READ_ONLY" && resource.delete) {
						return nil, fmt.Errorf("stateful disk does not match its live VM attachment")
					}
					resource.shared = attached.shared || text(entry["mode"]) == "READ_ONLY"
					if resource.shared && resource.delete {
						return nil, fmt.Errorf("shared disk cannot be automatically deleted")
					}
				} else {
					resource.id, err = c.statefulAddress(ctx, node, category, device, entry)
					if err != nil {
						return nil, err
					}
					resource.kind = "compute.googleapis.com/Address"
				}
				resources[slot] = resource
			}
		}
	}
	result, seen := []groupResource{}, map[string]bool{}
	for _, resource := range resources {
		if seen[resource.id] {
			return nil, fmt.Errorf("duplicate managed resource identity")
		}
		seen[resource.id] = true
		result = append(result, resource)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].id < result[j].id })
	return result, nil
}

func (c *client) statefulAddress(ctx context.Context, node managedNode, category, device string, state map[string]any) (string, error) {
	address := object(state["ipAddress"])
	id, literal := "", text(address["literal"])
	if source := text(address["address"]); source != "" {
		var err error
		id, err = c.computeID(source, "compute.googleapis.com/Address")
		if err != nil {
			return "", err
		}
		vm := strings.Split(strings.TrimPrefix(node.id, "//compute.googleapis.com/"), "/")
		ip := strings.Split(strings.TrimPrefix(id, "//compute.googleapis.com/"), "/")
		if ip[3] != regionOf(vm[3]) {
			return "", fmt.Errorf("stateful IP belongs to another region")
		}
		kind, _ := findType("compute.googleapis.com/Address")
		endpoint, _ := c.resourceURL(kind, id)
		reservation, err := c.request(ctx, "GET", endpoint, nil)
		if err != nil {
			return "", err
		}
		if literal != "" && literal != text(reservation["address"]) {
			return "", fmt.Errorf("stateful IP reservation has a different address")
		}
		literal = text(reservation["address"])
	}
	if literal == "" {
		return "", fmt.Errorf("stateful IP has no address identity")
	}
	matched := false
	for _, raw := range array(node.data["networkInterfaces"]) {
		nic := object(raw)
		if text(nic["name"]) != device {
			continue
		}
		if category == "internalIPs" {
			matched = text(nic["networkIP"]) == literal
		} else {
			for _, raw := range array(nic["accessConfigs"]) {
				matched = matched || text(object(raw)["natIP"]) == literal
			}
		}
	}
	if !matched {
		return "", fmt.Errorf("stateful IP does not match its live VM interface")
	}
	if id != "" {
		return id, nil
	}
	parts := strings.Split(strings.TrimPrefix(node.id, "//compute.googleapis.com/"), "/")
	metadata, _ := providerData()
	operation, _ := metadata.catalog.Operation("compute.addresses.list")
	addresses, err := c.nativeList(ctx, operation, map[string]any{"project": c.project, "region": regionOf(parts[3])}, "items")
	if err != nil {
		return "", err
	}
	for _, value := range addresses {
		if text(value["address"]) != literal {
			continue
		}
		if id != "" {
			return "", fmt.Errorf("ambiguous stateful IP address")
		}
		id, err = c.computeID(text(value["selfLink"]), "compute.googleapis.com/Address")
		if err != nil {
			return "", err
		}
	}
	if id == "" {
		return "", fmt.Errorf("stateful IP reservation could not be resolved")
	}
	return id, nil
}

func findManagedAsset(assets []asset.Asset, controller asset.Asset, nativeType, id string) (asset.Asset, bool, error) {
	var result asset.Asset
	for _, candidate := range assets {
		if candidate.Identity.Provider != controller.Identity.Provider || candidate.Identity.ConnectionID != controller.Identity.ConnectionID || candidate.Identity.Partition != controller.Identity.Partition || candidate.Identity.NativeType != nativeType || candidate.Identity.NativeID != id {
			continue
		}
		if result.ID != "" {
			return result, false, fmt.Errorf("ambiguous managed resource identity")
		}
		result = candidate
	}
	return result, result.ID != "", nil
}

func addGroupBinding(result *governance.Contribution, assets []asset.Asset, controller asset.Asset, nativeType, id string, ownership graph.Ownership, policy graph.CleanupPolicy, deletes bool) (asset.Asset, error) {
	managed, found, err := findManagedAsset(assets, controller, nativeType, id)
	if err != nil {
		return managed, err
	}
	evidence := map[string]any{"resource_type": nativeType, "instance_id": id, "delete_by_default": deletes, "lifecycle_kind": "gcp_managed_instance_group"}
	if deletes && policy == graph.CleanupDelegate {
		evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
	}
	if nativeType == instanceGroupType {
		evidence["retention_supported"] = false
		evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] = true
	}
	if !found {
		result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: controller.Identity.Provider, ConnectionID: controller.Identity.ConnectionID, NativeType: nativeType, NativeID: id, ControllerID: controller.ID, Relationship: graph.RelationshipMemberOf, Evidence: evidence})
		return managed, nil
	}
	result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: controller.ID, ManagedAssetID: managed.ID, Authority: graph.AuthorityAuthoritative, Ownership: ownership, CleanupPolicy: policy, EvidenceSource: groupSource, Evidence: evidence, Confidence: 1})
	result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: managed.ID, TargetAssetID: controller.ID, Type: graph.RelationshipMemberOf, Source: groupSource, Evidence: evidence, Confidence: 1})
	return managed, nil
}

func (h *computeGroups) Contribute(ctx context.Context, scope asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	result, gkeOwned, err := h.contributeGKE(ctx, assets)
	if err != nil {
		return result, err
	}
	managedVMs := map[asset.AssetID]bool{}
	for _, value := range assets {
		if gkeOwned[value.Identity.NativeID] && value.Identity.NativeType == instanceType {
			managedVMs[value.ID] = true
		}
	}
	for _, manager := range assets {
		if manager.Identity.Provider != asset.ProviderGCP || manager.Identity.NativeType != managerType || gkeOwned[manager.Identity.NativeID] {
			continue
		}
		kind, _ := findType(managerType)
		endpoint, err := h.client.resourceURL(kind, manager.Identity.NativeID)
		if err != nil {
			return result, err
		}
		live, err := h.client.request(ctx, "GET", endpoint, nil)
		if err != nil {
			return result, err
		}
		group, err := h.client.loadManagedGroup(ctx, manager.Identity.NativeID, live)
		if err != nil {
			return result, err
		}
		if _, err := addGroupBinding(&result, assets, manager, instanceGroupType, group.instanceGroup, graph.OwnershipExclusive, graph.CleanupDelegate, true); err != nil {
			return result, err
		}
		if group.autoscaler != "" {
			if _, err := addGroupBinding(&result, assets, manager, autoscalerType, group.autoscaler, graph.OwnershipExclusive, graph.CleanupDirect, true); err != nil {
				return result, err
			}
		}
		for _, node := range group.nodes {
			vm, err := addGroupBinding(&result, assets, manager, instanceType, node.id, graph.OwnershipExclusive, graph.CleanupDelegate, true)
			if err != nil {
				return result, err
			}
			if vm.ID == "" {
				continue
			}
			managedVMs[vm.ID] = true
			for _, resource := range node.resources {
				ownership, policy := graph.OwnershipExclusive, graph.CleanupDelegate
				if resource.shared {
					ownership, policy = graph.OwnershipShared, graph.CleanupRetain
				}
				if _, err := addGroupBinding(&result, assets, vm, resource.kind, resource.id, ownership, policy, resource.delete); err != nil {
					return result, err
				}
			}
		}
	}
	// Use the existing attachment contributor for VMs outside managed groups.
	// A MIG's effective stateful policy supersedes the VM's autoDelete flag.
	remaining := make([]asset.Asset, 0, len(assets))
	for _, value := range assets {
		if !managedVMs[value.ID] {
			remaining = append(remaining, value)
		}
	}
	attachments, err := NewInstanceDisks().Contribute(ctx, scope, remaining)
	result.Bindings = append(result.Bindings, attachments.Bindings...)
	result.Relationships = append(result.Relationships, attachments.Relationships...)
	result.Unresolved = append(result.Unresolved, attachments.Unresolved...)
	return result, err
}
