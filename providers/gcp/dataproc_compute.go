package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const dataprocSource = "gcp:dataproc"

type dataprocMember struct {
	kind, id, parent string
	data             map[string]any
	retain           bool
}

func dataprocComputeConfiguration(kind string, data map[string]any) string {
	fields := []string{"id", "name", "selfLink", "creationTimestamp"}
	switch kind {
	case instanceType:
		fields = append(fields, "machineType", "serviceAccounts", "deletionProtection")
	case managerType:
		fields = append(fields, "instanceGroup", "instanceTemplate", "versions", "targetSize", "targetStoppedSize", "targetSuspendedSize", "statefulPolicy")
	case instanceGroupType:
		fields = append(fields, "network", "subnetwork")
	case "compute.googleapis.com/InstanceTemplate":
		fields = append(fields, "properties")
	case "compute.googleapis.com/Disk", "compute.googleapis.com/RegionDisk":
		fields = append(fields, "sizeGb", "type")
	default:
		fields = append(fields, "address", "addressType", "network", "subnetwork")
	}
	value := map[string]any{}
	for _, field := range fields {
		value[field] = data[field]
	}
	if kind == instanceType {
		value["dataproc"] = dataprocMetadata(data)
		for field, keys := range map[string][]string{"disks": {"source", "deviceName", "autoDelete", "boot", "type", "mode"}, "networkInterfaces": {"name", "network", "subnetwork", "nicType", "stackType"}} {
			var items []any
			for _, raw := range array(data[field]) {
				item := map[string]any{}
				for _, key := range keys {
					item[key] = object(raw)[key]
				}
				items = append(items, item)
			}
			value[field] = items
		}
	}
	if kind == "compute.googleapis.com/InstanceTemplate" {
		value["properties"] = safePayload(object(data["properties"]))
	}
	labels := object(data["labels"])
	value["labels"] = map[string]any{"goog-dataproc-cluster-uuid": labels["goog-dataproc-cluster-uuid"], "goog-dataproc-cluster-name": labels["goog-dataproc-cluster-name"], "goog-dataproc-location": labels["goog-dataproc-location"]}
	encoded, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func dataprocSameMember(member dataprocMember, planned map[string]any) error {
	if isDataproc(member.kind) {
		if err := dataprocSameResource(member.kind, planned, member.data); err != nil {
			return err
		}
		if member.kind == dataprocNodeGroupType && (text(planned[dataprocParentProof]) == "" || planned[dataprocParentProof] != member.data[dataprocParentProof] || planned["_dataproc_cluster_uuid"] != member.data["_dataproc_cluster_uuid"]) {
			return groupDenied("dataproc_node_group_parent_changed")
		}
	} else if text(planned["id"]) == "" || dataprocComputeConfiguration(member.kind, planned) != dataprocComputeConfiguration(member.kind, member.data) {
		return groupDenied("dataproc_compute_changed")
	}
	return nil
}

func (c *client) dataprocCompute(ctx context.Context, uuid string) ([]dataprocMember, error) {
	if len(uuid) == 0 || len(uuid) > 63 || strings.Trim(uuid, "abcdefghijklmnopqrstuvwxyz0123456789_-") != "" {
		return nil, groupDenied("dataproc_cluster_uuid_invalid")
	}
	var result []dataprocMember
	for _, collection := range []string{"instances", "disks"} {
		path := "items.*." + collection
		records, err := c.batchList(ctx, "compute."+collection+".aggregatedList", map[string]any{"project": c.project, "includeAllScopes": true, "filter": "labels.goog-dataproc-cluster-uuid = \"" + uuid + "\""}, path)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, record := range records {
			kind := instanceType
			if collection == "disks" {
				kind = "compute.googleapis.com/Disk"
				if strings.Contains(text(record["selfLink"]), "/regions/") {
					kind = "compute.googleapis.com/RegionDisk"
				}
			}
			id, err := c.computeID(text(record["selfLink"]), kind)
			if err != nil || seen[id] || object(record["labels"])["goog-dataproc-cluster-uuid"] != uuid {
				return nil, groupDenied("dataproc_compute_query_invalid")
			}
			seen[id] = true
			result = append(result, dataprocMember{kind: kind, id: id, data: record})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].id < result[j].id })
	return result, nil
}

func (c *client) dataprocJobs(ctx context.Context, root asset.Asset, data map[string]any) ([]dataprocMember, error) {
	params := map[string]any{"projectId": c.project, "region": dataprocRegion(root.Identity.NativeID), "clusterName": data["clusterName"], "pageSize": 100}
	read := func() (map[string]map[string]any, error) {
		records, err := c.batchList(ctx, "dataproc.projects.regions.jobs.list", params, "jobs")
		if err != nil {
			return nil, err
		}
		result := map[string]map[string]any{}
		for _, record := range records {
			id := "//dataproc.googleapis.com/projects/" + c.project + "/regions/" + text(params["region"]) + "/jobs/" + text(object(record["reference"])["jobId"])
			if err := c.dataprocIdentity(dataprocJobType, id, record); err != nil {
				return nil, err
			}
			placement := object(record["placement"])
			if placement["clusterName"] != data["clusterName"] {
				return nil, groupDenied("dataproc_job_placement_invalid")
			}
			if placement["clusterUuid"] != data["clusterUuid"] {
				continue
			} // Retained history from an older same-name cluster.
			if result[id] != nil {
				return nil, groupDenied("dataproc_job_duplicate")
			}
			result[id] = record
		}
		return result, nil
	}
	records, err := read()
	if err != nil {
		return nil, err
	}
	var result []dataprocMember
	for id, record := range records {
		live, err := c.nativeGet(ctx, dataprocJobType, id)
		if err != nil {
			return nil, err
		}
		if err = c.dataprocIdentity(dataprocJobType, id, live); err != nil {
			return nil, err
		}
		if err = dataprocSameResource(dataprocJobType, record, live); err != nil {
			return nil, err
		}
		if _, err = dataprocTerminalJob(live); err != nil {
			return nil, err
		}
		result = append(result, dataprocMember{kind: dataprocJobType, id: id, parent: root.Identity.NativeID, data: live, retain: true})
	}
	again, err := read()
	if err != nil {
		return nil, err
	}
	if len(records) != len(again) {
		return nil, groupDenied("dataproc_job_membership_changed")
	}
	for id, record := range records {
		if again[id] == nil || dataprocConfiguration(record) != dataprocConfiguration(again[id]) {
			return nil, groupDenied("dataproc_job_membership_changed")
		}
	}
	return result, nil
}

func (c *client) dataprocVMIdentity(root asset.Asset, cluster, vm map[string]any, id string) error {
	if c.canonicalName(text(vm["selfLink"])) != id || text(vm["id"]) == "" {
		return groupDenied("dataproc_vm_identity_invalid")
	}
	metadata := dataprocMetadata(vm)
	if metadata["dataproc-cluster-uuid"] != text(cluster["clusterUuid"]) || metadata["dataproc-cluster-name"] != text(cluster["clusterName"]) || metadata["dataproc-region"] != dataprocRegion(root.Identity.NativeID) {
		return groupDenied("dataproc_vm_controller_changed")
	}
	gce := object(object(cluster["config"])["gceClusterConfig"])
	zone := last(text(gce["zoneUri"]))
	if zone != "" && !strings.Contains(id, "/zones/"+zone+"/instances/") {
		return groupDenied("dataproc_vm_zone_changed")
	}
	if account := text(gce["serviceAccount"]); account != "" {
		accounts := array(vm["serviceAccounts"])
		if len(accounts) != 1 || object(accounts[0])["email"] != account {
			return groupDenied("dataproc_vm_service_account_changed")
		}
	}
	refs := c.dataprocReferences(dataprocClusterType, root.Identity.NativeID, cluster)
	for _, kind := range []string{"compute.googleapis.com/Network", "compute.googleapis.com/Subnetwork"} {
		field := "network"
		if kind == "compute.googleapis.com/Subnetwork" {
			field = "subnetwork"
		}
		for _, expected := range refs[kind] {
			found := false
			for _, raw := range array(vm["networkInterfaces"]) {
				nic := object(raw)
				if c.canonicalName(text(nic[field])) == expected {
					found = true
				}
			}
			if !found {
				return groupDenied("dataproc_vm_network_changed")
			}
		}
	}
	if gce["internalIpOnly"] == true {
		for _, raw := range array(vm["networkInterfaces"]) {
			nic := object(raw)
			if len(array(nic["accessConfigs"])) > 0 || len(array(nic["ipv6AccessConfigs"])) > 0 {
				return groupDenied("dataproc_vm_external_ip_changed")
			}
		}
	}
	return nil
}

func (c *client) dataprocMembers(ctx context.Context, root asset.Asset, cluster map[string]any) ([]dataprocMember, error) {
	if err := c.dataprocIdentity(dataprocClusterType, root.Identity.NativeID, cluster); err != nil {
		return nil, err
	}
	if err := dataprocSameResource(dataprocClusterType, root.Normalized, cluster); err != nil {
		return nil, err
	}
	result, err := c.dataprocJobs(ctx, root, cluster)
	if err != nil {
		return nil, err
	}
	config := object(cluster["config"])
	// Virtual clusters schedule work on separately managed GKE pools. Dataproc
	// deletion retains those pools; their native lifecycle remains with GKE.
	virtual := object(cluster["virtualClusterConfig"])
	if len(virtual) > 0 || len(object(config["gkeClusterConfig"])) > 0 {
		refs := c.dataprocReferences(dataprocClusterType, root.Identity.NativeID, cluster)
		if len(refs[clusterType]) != 1 {
			return nil, groupDenied("dataproc_gke_target_missing")
		}
		for _, kind := range []string{clusterType, nodePoolType} {
			for _, id := range refs[kind] {
				live, err := c.nativeGet(ctx, kind, id)
				if err != nil {
					return nil, err
				}
				if text(live["id"]) == "" || text(live["name"]) != last(id) {
					return nil, groupDenied("dataproc_gke_target_invalid")
				}
			}
		}
		live, err := c.nativeGet(ctx, dataprocClusterType, root.Identity.NativeID)
		if err != nil {
			return nil, err
		}
		if err = dataprocSameResource(dataprocClusterType, cluster, live); err != nil {
			return nil, err
		}
		return result, nil
	}
	if config == nil {
		return nil, groupDenied("dataproc_cluster_config_missing")
	}
	type groupConfig struct {
		parent string
		data   map[string]any
	}
	groups := []groupConfig{}
	for _, field := range []string{"masterConfig", "workerConfig", "secondaryWorkerConfig"} {
		if raw := config[field]; raw != nil {
			group := object(raw)
			if group == nil {
				return nil, groupDenied("dataproc_instance_group_invalid")
			}
			groups = append(groups, groupConfig{root.Identity.NativeID, group})
		}
	}
	nodeGroups, err := c.dataprocNodeGroups(root.Identity.NativeID, cluster)
	if err != nil {
		return nil, err
	}
	for _, summary := range nodeGroups {
		id := c.canonicalName("//dataproc.googleapis.com/" + text(summary["name"]))
		live, err := c.nativeGet(ctx, dataprocNodeGroupType, id)
		if err != nil {
			return nil, err
		}
		if err = c.dataprocIdentity(dataprocNodeGroupType, id, live); err != nil {
			return nil, err
		}
		if err = dataprocSameResource(dataprocNodeGroupType, summary, live); err != nil {
			return nil, err
		}
		live[dataprocParentProof] = dataprocConfiguration(cluster)
		live["_dataproc_cluster_uuid"] = cluster["clusterUuid"]
		result = append(result, dataprocMember{kind: dataprocNodeGroupType, id: id, parent: root.Identity.NativeID, data: live})
		groups = append(groups, groupConfig{id, object(live["nodeGroupConfig"])})
	}
	candidates, err := c.dataprocCompute(ctx, text(cluster["clusterUuid"]))
	if err != nil {
		return nil, err
	}
	generation := map[string]string{}
	for _, candidate := range candidates {
		generation[candidate.id] = dataprocComputeConfiguration(candidate.kind, candidate.data)
	}
	seen := map[string]bool{}
	for _, member := range result {
		seen[member.id] = true
	}
	add := func(member dataprocMember) error {
		if seen[member.id] {
			for _, prior := range result {
				if member.kind == "compute.googleapis.com/InstanceTemplate" && prior.kind == member.kind && prior.id == member.id && prior.parent == member.parent && dataprocComputeConfiguration(member.kind, prior.data) == dataprocComputeConfiguration(member.kind, member.data) {
					return nil
				}
				if prior.id == member.id && prior.retain && member.retain && prior.kind == member.kind && dataprocComputeConfiguration(member.kind, prior.data) == dataprocComputeConfiguration(member.kind, member.data) {
					if prior.parent != member.parent {
						result = append(result, member)
					}
					return nil
				}
			}
			return groupDenied("dataproc_duplicate_controller")
		}
		seen[member.id] = true
		result = append(result, member)
		return nil
	}
	managedSets := map[string]map[string]string{}
	managedConfig := map[string]string{}
	addDisk := func(parent, kind, id string, retain bool) error {
		live, err := c.nativeGet(ctx, kind, id)
		if err != nil {
			return err
		}
		if text(live["id"]) == "" || c.canonicalName(text(live["selfLink"])) != id {
			return groupDenied("dataproc_disk_identity_invalid")
		}
		if err = c.batchDiskUsers(live, parent, !retain); err != nil {
			return err
		}
		if retain && object(live["labels"])["goog-dataproc-cluster-uuid"] == cluster["clusterUuid"] {
			return groupDenied("dataproc_created_disk_retention_unverified")
		}
		return add(dataprocMember{kind: kind, id: id, parent: parent, data: live, retain: retain})
	}
	addVM := func(parent, id string, vm map[string]any, resources []groupResource) error {
		if err := c.dataprocVMIdentity(root, cluster, vm, id); err != nil {
			return err
		}
		if err := add(dataprocMember{kind: instanceType, id: id, parent: parent, data: vm}); err != nil {
			return err
		}
		if resources == nil {
			disks, err := instanceDisks(c, vm)
			if err != nil {
				return err
			}
			for _, disk := range disks {
				if err = addDisk(id, disk.kind, disk.id, !disk.autoDelete); err != nil {
					return err
				}
			}
		} else {
			for _, resource := range resources {
				if resource.stateful {
					return groupDenied("dataproc_foreign_stateful_policy")
				}
				if resource.kind == "compute.googleapis.com/Disk" || resource.kind == "compute.googleapis.com/RegionDisk" {
					if err := addDisk(id, resource.kind, resource.id, !resource.delete); err != nil {
						return err
					}
					continue
				}
				live, err := c.nativeGet(ctx, resource.kind, resource.id)
				if err != nil {
					return err
				}
				if text(live["id"]) == "" || c.canonicalName(text(live["selfLink"])) != resource.id {
					return groupDenied("dataproc_ip_identity_invalid")
				}
				if err = add(dataprocMember{kind: resource.kind, id: resource.id, parent: id, data: live, retain: !resource.delete}); err != nil {
					return err
				}
			}
		}
		return nil
	}
	zone := last(text(object(config["gceClusterConfig"])["zoneUri"]))
	for _, group := range groups {
		if group.data == nil {
			return nil, groupDenied("dataproc_node_group_config_missing")
		}
		names := map[string]string{}
		if raw := group.data["instanceNames"]; raw != nil {
			if _, ok := raw.([]any); !ok {
				return nil, groupDenied("dataproc_instance_names_invalid")
			}
		}
		for _, raw := range array(group.data["instanceNames"]) {
			name := text(raw)
			if name == "" || !segmentPattern.MatchString(name) || name == "." || name == ".." {
				return nil, groupDenied("dataproc_instance_name_invalid")
			}
			if _, ok := names[name]; ok {
				return nil, groupDenied("dataproc_instance_duplicate")
			}
			names[name] = ""
		}
		refs := map[string]string{}
		if raw := group.data["instanceReferences"]; raw != nil {
			if _, ok := raw.([]any); !ok {
				return nil, groupDenied("dataproc_instance_references_invalid")
			}
		}
		for _, raw := range array(group.data["instanceReferences"]) {
			ref := object(raw)
			name := text(ref["instanceName"])
			uid := text(ref["instanceId"])
			if name == "" || uid == "" || refs[name] != "" {
				return nil, groupDenied("dataproc_instance_reference_invalid")
			}
			refs[name] = uid
		}
		if len(refs) > 0 {
			hadNames := len(names) > 0
			if len(names) > 0 && len(names) != len(refs) {
				return nil, groupDenied("dataproc_instance_references_changed")
			}
			for name, uid := range refs {
				if hadNames {
					if _, ok := names[name]; !ok {
						return nil, groupDenied("dataproc_instance_references_changed")
					}
				}
				names[name] = uid
			}
		}
		managed := object(group.data["managedGroupConfig"])
		if len(managed) > 0 {
			source := text(managed["instanceGroupManagerUri"])
			if source == "" {
				name := text(managed["instanceGroupManagerName"])
				if name == "" || zone == "" {
					return nil, groupDenied("dataproc_manager_scope_missing")
				}
				source = "projects/" + c.project + "/zones/" + zone + "/instanceGroupManagers/" + name
			}
			id, err := c.computeID(source, managerType)
			if err != nil {
				return nil, err
			}
			live, err := c.nativeGet(ctx, managerType, id)
			if err != nil {
				return nil, err
			}
			if c.canonicalName(text(live["selfLink"])) != id || text(live["id"]) == "" {
				return nil, groupDenied("dataproc_manager_identity_invalid")
			}
			manager, err := c.loadManagedGroup(ctx, id, live)
			if err != nil {
				return nil, err
			}
			if len(object(live["statefulPolicy"])) > 0 {
				return nil, groupDenied("dataproc_foreign_stateful_policy")
			}
			for _, node := range manager.nodes {
				if len(object(node.config["preservedState"])) > 0 {
					return nil, groupDenied("dataproc_foreign_stateful_policy")
				}
			}
			if manager.autoscaler != "" {
				return nil, groupDenied("dataproc_foreign_compute_autoscaler")
			}
			if err = add(dataprocMember{kind: managerType, id: id, parent: group.parent, data: live}); err != nil {
				return nil, err
			}
			managedConfig[id] = dataprocComputeConfiguration(managerType, live)
			managedSets[id] = map[string]string{}
			ig, err := c.nativeGet(ctx, instanceGroupType, manager.instanceGroup)
			if err != nil {
				return nil, err
			}
			if c.canonicalName(text(ig["selfLink"])) != manager.instanceGroup || text(ig["id"]) == "" {
				return nil, groupDenied("dataproc_instance_group_identity_invalid")
			}
			if err = add(dataprocMember{kind: instanceGroupType, id: manager.instanceGroup, parent: id, data: ig}); err != nil {
				return nil, err
			}
			templates := map[string]bool{}
			if value := text(live["instanceTemplate"]); value != "" {
				templates[value] = true
			}
			for _, raw := range array(live["versions"]) {
				if value := text(object(raw)["instanceTemplate"]); value != "" {
					templates[value] = true
				}
			}
			expectedTemplate := text(managed["instanceTemplateName"])
			matchedTemplate := expectedTemplate == ""
			for source := range templates {
				templateID, err := c.computeID(source, "compute.googleapis.com/InstanceTemplate")
				if err != nil {
					return nil, err
				}
				if strings.Contains(expectedTemplate, "/") {
					expectedID, err := c.computeID(expectedTemplate, "compute.googleapis.com/InstanceTemplate")
					if err != nil {
						return nil, err
					}
					matchedTemplate = matchedTemplate || templateID == expectedID
				} else {
					matchedTemplate = matchedTemplate || last(templateID) == expectedTemplate
				}
				template, err := c.nativeGet(ctx, "compute.googleapis.com/InstanceTemplate", templateID)
				if err != nil {
					return nil, err
				}
				hints := dataprocMetadata(object(template["properties"]))
				if text(template["id"]) == "" || c.canonicalName(text(template["selfLink"])) != templateID || hints["dataproc-cluster-uuid"] != text(cluster["clusterUuid"]) || hints["dataproc-cluster-name"] != text(cluster["clusterName"]) || hints["dataproc-region"] != dataprocRegion(root.Identity.NativeID) {
					return nil, groupDenied("dataproc_template_controller_changed")
				}
				if err = add(dataprocMember{kind: "compute.googleapis.com/InstanceTemplate", id: templateID, parent: root.Identity.NativeID, data: template}); err != nil {
					return nil, err
				}
			}
			if !matchedTemplate || len(templates) == 0 {
				return nil, groupDenied("dataproc_template_missing")
			}
			declaredNames := len(names) > 0
			for _, node := range manager.nodes {
				if declaredNames {
					uid, ok := names[last(node.id)]
					if !ok || uid != "" && uid != text(node.data["id"]) {
						return nil, groupDenied("dataproc_managed_members_changed")
					}
					delete(names, last(node.id))
				}
				managedSets[id][node.id] = text(node.data["id"])
				if err = addVM(id, node.id, node.data, node.resources); err != nil {
					return nil, err
				}
			}
			if len(names) > 0 {
				return nil, groupDenied("dataproc_managed_members_changed")
			}
		} else {
			for name, uid := range names {
				if zone == "" {
					return nil, groupDenied("dataproc_cluster_zone_missing")
				}
				id, err := c.computeID("projects/"+c.project+"/zones/"+zone+"/instances/"+name, instanceType)
				if err != nil {
					return nil, err
				}
				vm, err := c.nativeGet(ctx, instanceType, id)
				if isNotFound(err) {
					continue
				}
				if err != nil {
					return nil, err
				}
				if uid != "" && text(vm["id"]) != uid {
					return nil, groupDenied("dataproc_instance_recreated")
				}
				if machine := text(group.data["machineTypeUri"]); machine != "" && last(text(vm["machineType"])) != last(machine) && len(object(group.data["instanceFlexibilityPolicy"])) == 0 {
					return nil, groupDenied("dataproc_vm_machine_type_changed")
				}
				if err = addVM(group.parent, id, vm, nil); err != nil {
					return nil, err
				}
			}
		}
	}
	// Native lists may still expose resources left behind during failed creation
	// or provider teardown. Correlation never authorizes an independent Compute write.
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].kind == instanceType && candidates[j].kind != instanceType })
	for _, candidate := range candidates {
		if seen[candidate.id] {
			continue
		}
		live, err := c.nativeGet(ctx, candidate.kind, candidate.id)
		if err != nil {
			return nil, err
		}
		if c.canonicalName(text(live["selfLink"])) != candidate.id || text(live["id"]) == "" || dataprocComputeConfiguration(candidate.kind, live) != generation[candidate.id] {
			return nil, groupDenied("dataproc_orphan_identity_changed")
		}
		if candidate.kind == instanceType {
			if err = addVM(root.Identity.NativeID, candidate.id, live, nil); err != nil {
				return nil, err
			}
		} else {
			if users := live["users"]; users != nil {
				if _, valid := users.([]any); !valid {
					return nil, groupDenied("dataproc_disk_users_invalid")
				}
			}
			if len(array(live["users"])) > 0 {
				return nil, groupDenied("dataproc_orphan_disk_shared")
			}
			if err = add(dataprocMember{kind: candidate.kind, id: candidate.id, parent: root.Identity.NativeID, data: live}); err != nil {
				return nil, err
			}
		}
	}
	again, err := c.dataprocCompute(ctx, text(cluster["clusterUuid"]))
	if err != nil {
		return nil, err
	}
	if len(again) != len(generation) {
		return nil, groupDenied("dataproc_compute_membership_changed")
	}
	for _, member := range again {
		if generation[member.id] != dataprocComputeConfiguration(member.kind, member.data) {
			return nil, groupDenied("dataproc_compute_membership_changed")
		}
	}
	for id, expected := range managedSets {
		current, err := c.groupList(ctx, id, "listManagedInstances", "managedInstances")
		if err != nil {
			return nil, err
		}
		if len(current) != len(expected) {
			return nil, groupDenied("dataproc_managed_members_changed")
		}
		seen := map[string]bool{}
		for _, record := range current {
			vm, err := c.computeID(text(record["instance"]), instanceType)
			if err != nil || seen[vm] || expected[vm] == "" || expected[vm] != text(record["id"]) || record["currentAction"] != "NONE" {
				return nil, groupDenied("dataproc_managed_members_changed")
			}
			seen[vm] = true
		}
		live, err := c.nativeGet(ctx, managerType, id)
		if err != nil {
			return nil, err
		}
		if dataprocComputeConfiguration(managerType, live) != managedConfig[id] {
			return nil, groupDenied("dataproc_manager_changed")
		}
	}
	jobs, err := c.dataprocJobs(ctx, root, cluster)
	if err != nil {
		return nil, err
	}
	expectedJobs := map[string]string{}
	for _, member := range result {
		if member.kind == dataprocJobType {
			expectedJobs[member.id] = dataprocConfiguration(member.data)
		}
	}
	if len(jobs) != len(expectedJobs) {
		return nil, groupDenied("dataproc_job_membership_changed")
	}
	for _, job := range jobs {
		if expectedJobs[job.id] != dataprocConfiguration(job.data) {
			return nil, groupDenied("dataproc_job_membership_changed")
		}
	}
	live, err := c.nativeGet(ctx, dataprocClusterType, root.Identity.NativeID)
	if err != nil {
		return nil, err
	}
	if err = c.dataprocIdentity(dataprocClusterType, root.Identity.NativeID, live); err != nil {
		return nil, err
	}
	if err = dataprocSameResource(dataprocClusterType, cluster, live); err != nil {
		return nil, err
	}
	return result, nil
}

func (h *computeGroups) contributeDataproc(ctx context.Context, assets []asset.Asset) (governance.Contribution, map[string]bool, error) {
	result := governance.Contribution{}
	owned := map[string]bool{}
	for _, root := range assets {
		if root.Identity.Provider != asset.ProviderGCP || root.Identity.NativeType != dataprocClusterType {
			continue
		}
		live, err := h.client.nativeGet(ctx, dataprocClusterType, root.Identity.NativeID)
		if err != nil {
			return result, owned, err
		}
		members, err := h.client.dataprocMembers(ctx, root, live)
		if err != nil {
			return result, owned, err
		}
		controllers := map[string]asset.Asset{root.Identity.NativeID: root}
		for _, member := range members {
			controller, ok := controllers[member.parent]
			if !ok {
				continue
			}
			managed, found, err := findManagedAsset(assets, controller, member.kind, member.id)
			if err != nil {
				return result, owned, err
			}
			evidence := map[string]any{"resource_type": member.kind, "instance_id": member.id, "lifecycle_kind": "dataproc", "native_cluster_uuid": live["clusterUuid"], "delete_by_default": !member.retain, "retention_supported": member.retain, "native_dataproc_cleanup_only": true}
			if !found {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: root.Identity.Provider, ConnectionID: root.Identity.ConnectionID, NativeType: member.kind, NativeID: member.id, ControllerID: controller.ID, Relationship: graph.RelationshipMemberOf, Evidence: evidence})
				continue
			}
			if err = dataprocSameMember(member, managed.Normalized); err != nil {
				return result, owned, err
			}
			ownership, policy := graph.OwnershipExclusive, graph.CleanupDelegate
			if member.kind == dataprocJobType {
				policy = graph.CleanupDirect
			} else if member.retain {
				ownership, policy = graph.OwnershipReferenced, graph.CleanupRetain
			} else {
				evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
				evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] = true
				owned[member.id] = true
			}
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: controller.ID, ManagedAssetID: managed.ID, Authority: graph.AuthorityAuthoritative, Ownership: ownership, CleanupPolicy: policy, DirectCleanupAllowed: member.kind == dataprocJobType, EvidenceSource: dataprocSource, Evidence: evidence, Confidence: 1})
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: managed.ID, TargetAssetID: controller.ID, Type: graph.RelationshipMemberOf, Source: dataprocSource, Evidence: evidence, Confidence: 1})
			controllers[member.id] = managed
		}
	}
	return result, owned, nil
}

// Dataproc controls its Compute resources while the original cluster UUID exists.
// Correlation is sufficient to prevent an independent write, never to authorize one.
func (c *client) dataprocComputeHasCluster(ctx context.Context, kind string, data map[string]any) (bool, error) {
	if kind == managerType {
		sources := []any{data["instanceTemplate"]}
		for _, raw := range array(data["versions"]) {
			sources = append(sources, object(raw)["instanceTemplate"])
		}
		for _, source := range sources {
			if text(source) == "" {
				continue
			}
			id, err := c.computeID(text(source), "compute.googleapis.com/InstanceTemplate")
			if err != nil {
				return false, err
			}
			template, err := c.nativeGet(ctx, "compute.googleapis.com/InstanceTemplate", id)
			if isNotFound(err) {
				continue
			}
			if err != nil {
				return false, err
			}
			if owned, err := c.dataprocComputeHasCluster(ctx, "compute.googleapis.com/InstanceTemplate", template); err != nil || owned {
				return owned, err
			}
		}
	}
	hints := dataprocMetadata(data)
	if kind == "compute.googleapis.com/InstanceTemplate" {
		hints = dataprocMetadata(object(data["properties"]))
	}
	labels := object(data["labels"])
	for label, key := range map[string]string{"goog-dataproc-cluster-uuid": "dataproc-cluster-uuid", "goog-dataproc-cluster-name": "dataproc-cluster-name", "goog-dataproc-location": "dataproc-region"} {
		if hints[key] == "" {
			hints[key] = text(labels[label])
		} else if value := text(labels[label]); value != "" && value != hints[key] {
			return false, groupDenied("dataproc_compute_owner_ambiguous")
		}
	}
	uuid, name, region := hints["dataproc-cluster-uuid"], hints["dataproc-cluster-name"], hints["dataproc-region"]
	if uuid == "" && name == "" && region == "" {
		return false, nil
	}
	if uuid == "" {
		return false, groupDenied("dataproc_compute_owner_incomplete")
	}
	if name != "" && region != "" {
		id := "//dataproc.googleapis.com/projects/" + c.project + "/regions/" + region + "/clusters/" + name
		live, err := c.nativeGet(ctx, dataprocClusterType, id)
		if isNotFound(err) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if err := c.dataprocIdentity(dataprocClusterType, id, live); err != nil {
			return false, err
		}
		return text(live["clusterUuid"]) == uuid, nil
	}
	regions, err := c.batchList(ctx, "compute.regions.list", map[string]any{"project": c.project}, "items")
	if err != nil {
		return false, err
	}
	if len(regions) == 0 {
		return false, groupDenied("dataproc_regions_incomplete")
	}
	for _, location := range regions {
		region := text(location["name"])
		if region == "" {
			return false, groupDenied("dataproc_region_invalid")
		}
		clusters, err := c.batchList(ctx, "dataproc.projects.regions.clusters.list", map[string]any{"projectId": c.project, "region": region, "pageSize": 100}, "clusters")
		if err != nil {
			return false, err
		}
		for _, cluster := range clusters {
			id := "//dataproc.googleapis.com/projects/" + c.project + "/regions/" + region + "/clusters/" + text(cluster["clusterName"])
			if err := c.dataprocIdentity(dataprocClusterType, id, cluster); err != nil {
				return false, err
			}
			if text(cluster["clusterUuid"]) == uuid {
				return true, nil
			}
		}
	}
	return false, nil
}
