package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func batchUID(data map[string]any) string { return text(object(data["labels"])["batch-job-uid"]) }

func batchComputeConfiguration(kind string, data map[string]any) string {
	fields := []string{"id", "name", "creationTimestamp", "selfLink"}
	if kind == instanceType {
		fields = append(fields, "machineType", "serviceAccounts", "sourceInstanceTemplate", "deletionProtection")
	} else {
		fields = append(fields, "type", "sizeGb")
	}
	value := map[string]any{}
	for _, field := range fields {
		value[field] = data[field]
	}
	labels := object(data["labels"])
	value["labels"] = map[string]any{"batch-job-id": labels["batch-job-id"], "batch-job-uid": labels["batch-job-uid"], "batch-node": labels["batch-node"]}
	if kind == instanceType {
		for field, keys := range map[string][]string{
			"disks":             {"source", "autoDelete", "boot", "deviceName", "type", "mode"},
			"networkInterfaces": {"name", "network", "subnetwork", "nicType", "stackType"},
		} {
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
	payload, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

// These labels are documented Batch correlation fields, but Compute users can
// change them. They authorize no Compute mutation: only the reviewed Batch job
// is deleted, and every correlated resource must be independently read absent.
func (c *client) batchComputeLabel(job asset.Identity, data, member map[string]any, kind string) error {
	labels := object(member["labels"])
	if text(labels["batch-job-uid"]) != text(data["uid"]) || text(labels["batch-job-id"]) != last(job.NativeID) {
		return groupDenied("batch_compute_labels_changed")
	}
	if kind == instanceType {
		if node, exists := labels["batch-node"]; !exists || node != "" {
			return groupDenied("batch_compute_node_label_missing")
		}
	}
	created, err := time.Parse(time.RFC3339Nano, text(member["creationTimestamp"]))
	started, startErr := time.Parse(time.RFC3339Nano, text(data["createTime"]))
	if err != nil || startErr != nil || created.Before(started) || text(member["id"]) == "" {
		return groupDenied("batch_compute_creation_unverified")
	}
	return nil
}

func (c *client) batchCompute(ctx context.Context, uid string) ([]serviceChild, error) {
	// UID is interpolated into a native filter, so restrict it to label values.
	if len(uid) == 0 || len(uid) > 63 || strings.Trim(uid, "abcdefghijklmnopqrstuvwxyz0123456789_-") != "" {
		return nil, groupDenied("batch_job_uid_invalid")
	}
	var result []serviceChild
	for _, collection := range []struct{ operation, path, kind string }{
		{"compute.instances.aggregatedList", "items.*.instances", instanceType},
		{"compute.disks.aggregatedList", "items.*.disks", "compute.googleapis.com/Disk"},
	} {
		records, err := c.batchList(ctx, collection.operation, map[string]any{"project": c.project, "includeAllScopes": true, "filter": "labels.batch-job-uid = \"" + uid + "\""}, collection.path)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, record := range records {
			kind := collection.kind
			if kind == "compute.googleapis.com/Disk" && strings.Contains(text(record["selfLink"]), "/regions/") {
				kind = "compute.googleapis.com/RegionDisk"
			}
			id, err := c.computeID(text(record["selfLink"]), kind)
			if err != nil || seen[id] || batchUID(record) != uid {
				return nil, groupDenied("batch_compute_identity_invalid")
			}
			seen[id] = true
			result = append(result, serviceChild{kind: kind, id: id, data: record})
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].id < result[j].id })
	return result, nil
}

func (c *client) batchDiskUsers(data map[string]any, vm string, exclusive bool) error {
	users, valid := data["users"].([]any)
	if !valid || len(users) == 0 {
		return groupDenied("batch_disk_attachment_unverified")
	}
	found := false
	seen := map[string]bool{}
	for _, raw := range users {
		id, err := c.computeID(text(raw), instanceType)
		if err != nil || seen[id] || (exclusive && id != vm) {
			return groupDenied("batch_disk_shared_or_invalid")
		}
		seen[id] = true
		found = found || id == vm
	}
	if !found {
		return groupDenied("batch_disk_attachment_changed")
	}
	return nil
}

func (c *client) batchAllocationMatches(job, vm map[string]any) error {
	policy := object(job["allocationPolicy"])
	if email := text(object(policy["serviceAccount"])["email"]); email != "" {
		accounts := array(vm["serviceAccounts"])
		if len(accounts) != 1 || text(object(accounts[0])["email"]) != email {
			return groupDenied("batch_vm_service_account_changed")
		}
	}
	nativeRef := func(value string) string {
		if strings.HasPrefix(value, "global/") || strings.HasPrefix(value, "regions/") {
			value = "projects/" + c.project + "/" + value
		}
		if strings.HasPrefix(value, "projects/") {
			value = "//compute.googleapis.com/" + value
		}
		return c.canonicalName(value)
	}
	for _, raw := range array(object(policy["network"])["networkInterfaces"]) {
		expected := object(raw)
		found := false
		for _, raw := range array(vm["networkInterfaces"]) {
			actual := object(raw)
			if network := text(expected["network"]); network != "" && nativeRef(network) != nativeRef(text(actual["network"])) {
				continue
			}
			if subnet := text(expected["subnetwork"]); subnet != "" && nativeRef(subnet) != nativeRef(text(actual["subnetwork"])) {
				continue
			}
			if expected["noExternalIpAddress"] == true && (len(array(actual["accessConfigs"])) > 0 || len(array(actual["ipv6AccessConfigs"])) > 0) {
				continue
			}
			found = true
		}
		if !found {
			return groupDenied("batch_vm_network_changed")
		}
	}
	for _, raw := range array(policy["instances"]) {
		if machine := text(object(object(raw)["policy"])["machineType"]); machine != "" && last(text(vm["machineType"])) != last(machine) {
			return groupDenied("batch_vm_machine_type_changed")
		}
	}
	return nil
}

// All attached disks with autoDelete=false are retained. Existing disks named
// in the job or instance template may never acquire an implicit delete policy.
func (c *client) batchExternalDisks(ctx context.Context, data map[string]any) (map[string]bool, error) {
	result := map[string]bool{}
	for _, raw := range array(object(data["allocationPolicy"])["instances"]) {
		instance := object(raw)
		for _, raw := range array(object(instance["policy"])["disks"]) {
			if source := text(object(raw)["existingDisk"]); source != "" {
				kind := "compute.googleapis.com/Disk"
				if strings.Contains(source, "/regions/") {
					kind = "compute.googleapis.com/RegionDisk"
				}
				id, err := c.computeID(source, kind)
				if err != nil {
					return nil, err
				}
				result[id] = true
			}
		}
		if source := text(instance["instanceTemplate"]); source != "" {
			if strings.HasPrefix(source, "global/") {
				source = "projects/" + c.project + "/" + source
			}
			id, err := c.computeID(source, "compute.googleapis.com/InstanceTemplate")
			if err != nil {
				return nil, err
			}
			template, err := c.nativeGet(ctx, "compute.googleapis.com/InstanceTemplate", id)
			if err != nil {
				return nil, err
			}
			if text(template["id"]) == "" || c.canonicalName(text(template["selfLink"])) != id {
				return nil, groupDenied("batch_template_identity_invalid")
			}
			for _, raw := range array(object(template["properties"])["disks"]) {
				if source := text(object(raw)["source"]); source != "" {
					kind := "compute.googleapis.com/Disk"
					if strings.Contains(source, "/regions/") {
						kind = "compute.googleapis.com/RegionDisk"
					}
					id, err := c.computeID(source, kind)
					if err != nil {
						return nil, err
					}
					result[id] = true
				}
			}
		}
	}
	return result, nil
}

func (c *client) batchChildren(ctx context.Context, parent asset.Identity, data map[string]any) ([]serviceChild, error) {
	result, err := c.batchTasks(ctx, parent, data)
	if err != nil {
		return nil, err
	}
	// Obtain raw immutable configuration even when the caller has a sanitized
	// inventory asset. Native dependencies are never inferred from redacted text.
	live, err := c.nativeGet(ctx, batchJobType, parent.NativeID)
	if err != nil {
		return nil, err
	}
	if err := batchSameResource(batchJobType, data, live); err != nil {
		return nil, err
	}
	external, err := c.batchExternalDisks(ctx, live)
	if err != nil {
		return nil, err
	}
	members, err := c.batchCompute(ctx, text(data["uid"]))
	if err != nil {
		return nil, err
	}
	ownedDisks, attached, retained := map[string]serviceChild{}, map[string]bool{}, map[string]serviceChild{}
	generation := map[string]string{}
	for i := range members {
		member := &members[i]
		live, err := c.nativeGet(ctx, member.kind, member.id)
		if err != nil {
			return nil, err
		}
		if c.canonicalName(text(live["selfLink"])) != member.id || batchComputeConfiguration(member.kind, live) != batchComputeConfiguration(member.kind, member.data) {
			return nil, groupDenied("batch_compute_changed")
		}
		if err := c.batchComputeLabel(parent, data, live, member.kind); err != nil {
			return nil, err
		}
		member.data = live
		generation[member.id] = batchComputeConfiguration(member.kind, live)
		if member.kind != instanceType {
			if users := live["users"]; users != nil {
				if _, valid := users.([]any); !valid {
					return nil, groupDenied("batch_disk_users_invalid")
				}
			}
			ownedDisks[member.id] = *member
		}
	}
	for _, member := range members {
		if member.kind != instanceType {
			continue
		}
		if err := c.batchAllocationMatches(live, member.data); err != nil {
			return nil, err
		}
		result = append(result, member)
		disks, err := instanceDisks(c, member.data)
		if err != nil {
			return nil, err
		}
		for _, disk := range disks {
			live, err := c.nativeGet(ctx, disk.kind, disk.id)
			if err != nil {
				return nil, err
			}
			if c.canonicalName(text(live["selfLink"])) != disk.id || text(live["id"]) == "" {
				return nil, groupDenied("batch_disk_identity_invalid")
			}
			if err := c.batchDiskUsers(live, member.id, disk.autoDelete); err != nil {
				return nil, err
			}
			if disk.autoDelete {
				owned, ok := ownedDisks[disk.id]
				if !ok || external[disk.id] || attached[disk.id] || batchComputeConfiguration(disk.kind, owned.data) != batchComputeConfiguration(disk.kind, live) {
					return nil, groupDenied("batch_disk_delete_policy_unverified")
				}
				attached[disk.id] = true
			} else {
				if _, owned := ownedDisks[disk.id]; owned {
					return nil, groupDenied("batch_created_disk_retention_not_supported")
				}
				retained[disk.id] = serviceChild{kind: disk.kind, id: disk.id, data: live, retain: true}
			}
		}
	}
	for id, disk := range ownedDisks {
		if attached[id] {
			continue // The existing VM attachment contributor binds these disks.
		}
		if _, ok := retained[id]; ok {
			continue
		}
		// A disk can outlive a VM during provider teardown. Keep it as an explicit
		// job impact; native Batch cleanup must prove it absent before success.
		if external[id] || len(array(disk.data["users"])) > 0 {
			return nil, groupDenied("batch_orphan_disk_ownership_unverified")
		}
		result = append(result, disk)
	}
	for _, disk := range retained {
		result = append(result, disk)
	}
	again, err := c.batchCompute(ctx, text(data["uid"]))
	if err != nil {
		return nil, err
	}
	for _, member := range again {
		if generation[member.id] == "" || generation[member.id] != batchComputeConfiguration(member.kind, member.data) {
			return nil, groupDenied("batch_compute_membership_changed")
		}
		delete(generation, member.id)
	}
	if len(generation) != 0 {
		return nil, groupDenied("batch_compute_membership_changed")
	}
	expectedTasks := map[string]bool{}
	for _, member := range result {
		if member.kind == batchTaskType {
			expectedTasks[member.id] = true
		}
	}
	groups, err := c.batchTaskGroups(parent.NativeID, data)
	if err != nil {
		return nil, err
	}
	for _, group := range groups {
		tasks, err := c.batchList(ctx, "batch.projects.locations.jobs.taskGroups.tasks.list", map[string]any{"parent": group, "pageSize": 100}, "tasks")
		if err != nil {
			return nil, err
		}
		for _, task := range tasks {
			id := c.canonicalName("//batch.googleapis.com/" + text(task["name"]))
			if !expectedTasks[id] {
				return nil, groupDenied("batch_task_membership_changed")
			}
			delete(expectedTasks, id)
		}
	}
	if len(expectedTasks) != 0 {
		return nil, groupDenied("batch_task_membership_changed")
	}
	if err := c.verifyBatchParent(ctx, productTarget{ParentType: batchJobType, ParentID: parent.NativeID, ParentUID: text(data["uid"]), ParentConfiguration: batchConfiguration(live)}); err != nil {
		return nil, err
	}
	sort.Slice(result, func(i, j int) bool { return result[i].id < result[j].id })
	return result, nil
}
