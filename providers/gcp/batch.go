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
	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const (
	batchJobType     = "batch.googleapis.com/Job"
	batchTaskType    = "batch.googleapis.com/Task"
	batchProof       = "_batch_configuration"
	batchParentProof = "_batch_parent_configuration"
)

func isBatch(kind string) bool { return kind == batchJobType || kind == batchTaskType }

func (c *client) batchChildIdentity(kind, id, parent string) error {
	collection := "jobs"
	if kind == batchTaskType {
		collection = "tasks"
	}
	prefix := c.canonicalName("//batch.googleapis.com/"+parent) + "/" + collection + "/"
	if !strings.HasPrefix(id, prefix) || strings.Contains(strings.TrimPrefix(id, prefix), "/") || last(id) == "." || last(id) == ".." {
		return groupDenied("batch_list_parent_changed")
	}
	return nil
}

func (c *client) batchDependencyData(data map[string]any) map[string]any {
	result := map[string]any{}
	var secrets, buckets, disks, networks, subnets, templates, keys []any
	addEnvironment := func(raw any) {
		env := object(raw)
		for _, ref := range object(env["secretVariables"]) {
			secrets = append(secrets, ref)
		}
		if key := object(env["encryptedVariables"])["keyName"]; key != nil {
			keys = append(keys, key)
		}
	}
	for _, raw := range array(data["taskGroups"]) {
		group := object(raw)
		task := object(group["taskSpec"])
		addEnvironment(task["environment"])
		for _, env := range array(group["taskEnvironments"]) {
			addEnvironment(env)
		}
		for _, runnable := range array(task["runnables"]) {
			addEnvironment(object(runnable)["environment"])
		}
		for _, volume := range array(task["volumes"]) {
			if path := text(object(object(volume)["gcs"])["remotePath"]); path != "" {
				buckets = append(buckets, strings.Split(path, "/")[0])
			}
		}
	}
	policy := object(data["allocationPolicy"])
	expand := func(raw any) any {
		ref := text(raw)
		if strings.HasPrefix(ref, "global/") || strings.HasPrefix(ref, "regions/") {
			return "projects/" + c.project + "/" + ref
		}
		return raw
	}
	for _, raw := range array(policy["instances"]) {
		instance := object(raw)
		if ref := instance["instanceTemplate"]; ref != nil {
			templates = append(templates, expand(ref))
		}
		for _, disk := range array(object(instance["policy"])["disks"]) {
			if ref := object(disk)["existingDisk"]; ref != nil {
				disks = append(disks, ref)
			}
		}
	}
	for _, raw := range array(object(policy["network"])["networkInterfaces"]) {
		nic := object(raw)
		if ref := nic["network"]; ref != nil {
			networks = append(networks, expand(ref))
		}
		if ref := nic["subnetwork"]; ref != nil {
			subnets = append(subnets, expand(ref))
		}
	}
	result["secretVersion"], result["bucketName"], result["source"] = secrets, buckets, disks
	result["network"], result["subnetwork"], result["instanceTemplate"] = networks, subnets, templates
	result["serviceAccount"] = object(policy["serviceAccount"])["email"]
	result["kmsKeyName"] = keys
	result["notifications"] = data["notifications"]
	return result
}

// Job configuration is immutable. Hash before sanitizing runnable commands and
// environments; only provider-owned progress may advance during cleanup.
func batchConfiguration(raw map[string]any) string {
	value := cloneParameters(raw)
	for key := range value {
		if key == "name" || key == "status" || key == "updateTime" || strings.HasPrefix(key, "_") || key == "project_id" || key == "project_number" || strings.HasPrefix(key, "refs_") {
			delete(value, key)
		}
	}
	payload, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

func batchSameResource(kind string, planned, live map[string]any) error {
	if kind != batchJobType {
		return nil
	}
	expected := text(planned[batchProof])
	if expected == "" {
		expected = batchConfiguration(planned)
	}
	_, createdErr := time.Parse(time.RFC3339Nano, text(live["createTime"]))
	if text(live["uid"]) == "" || createdErr != nil || expected != batchConfiguration(live) {
		return groupDenied("batch_job_changed")
	}
	return nil
}

// TaskGroups are embedded Job configuration, not independently addressable
// resources. Fan out only actual names returned by Batch, never a guessed /0.
func (c *client) batchTaskGroups(id string, data map[string]any) ([]string, error) {
	groups, ok := data["taskGroups"].([]any)
	if !ok || len(groups) == 0 {
		return nil, groupDenied("batch_task_groups_missing")
	}
	var result []string
	seen := map[string]bool{}
	for _, raw := range groups {
		name := c.canonicalName("//batch.googleapis.com/" + text(object(raw)["name"]))
		parent := id + "/taskGroups/"
		if !strings.HasPrefix(name, parent) || strings.Contains(strings.TrimPrefix(name, parent), "/") || !segmentPattern.MatchString(strings.TrimPrefix(name, parent)) || last(name) == "." || last(name) == ".." || seen[name] {
			return nil, groupDenied("batch_task_group_identity_invalid")
		}
		seen[name] = true
		result = append(result, strings.TrimPrefix(name, "//batch.googleapis.com/"))
	}
	sort.Strings(result)
	return result, nil
}

func (c *client) verifyBatchParent(ctx context.Context, target productTarget) error {
	if target.ParentType != batchJobType {
		return nil
	}
	if target.ParentConfiguration == "" || target.ParentUID == "" {
		return groupDenied("batch_parent_proof_missing")
	}
	live, err := c.nativeGet(ctx, batchJobType, target.ParentID)
	if err != nil {
		return err
	}
	if c.canonicalName("//batch.googleapis.com/"+text(live["name"])) != target.ParentID || text(live["uid"]) != target.ParentUID || batchConfiguration(live) != target.ParentConfiguration {
		return groupDenied("batch_parent_changed")
	}
	return nil
}

func (c *client) batchTasks(ctx context.Context, parent asset.Identity, data map[string]any) ([]serviceChild, error) {
	target := productTarget{ParentType: batchJobType, ParentID: parent.NativeID, ParentUID: text(data["uid"]), ParentConfiguration: text(data[batchProof])}
	if target.ParentConfiguration == "" {
		target.ParentConfiguration = batchConfiguration(data)
	}
	if err := c.verifyBatchParent(ctx, target); err != nil {
		return nil, err
	}
	groups, err := c.batchTaskGroups(parent.NativeID, data)
	if err != nil {
		return nil, err
	}
	metadata, _ := providerData()
	op, _ := metadata.catalog.Operation("batch.projects.locations.jobs.taskGroups.tasks.list")
	kind, _ := findType(batchTaskType)
	var result []serviceChild
	for _, group := range groups {
		params := map[string]any{"parent": group, "pageSize": 100}
		records, err := c.nativeList(ctx, op, params, "tasks")
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, record := range records {
			id, err := c.productIdentity(kind, op, params, "name", productRecord{Data: record})
			if err != nil || seen[id] {
				return nil, groupDenied("batch_task_identity_invalid")
			}
			if err := c.batchChildIdentity(batchTaskType, id, group); err != nil {
				return nil, err
			}
			seen[id] = true
			live, err := c.nativeGet(ctx, batchTaskType, id)
			if err != nil {
				return nil, err
			}
			if c.canonicalName("//batch.googleapis.com/"+text(live["name"])) != id {
				return nil, groupDenied("batch_task_identity_changed")
			}
			live[batchParentProof] = target.ParentConfiguration
			live["_batch_job_uid"] = target.ParentUID
			result = append(result, serviceChild{kind: batchTaskType, id: id, data: live})
		}
		// Tasks can materialize while a queued job starts. Reconcile the complete
		// set after detail reads, including empty pages with continuation tokens.
		again, err := c.nativeList(ctx, op, map[string]any{"parent": group, "pageSize": 100}, "tasks")
		if err != nil {
			return nil, err
		}
		for _, record := range again {
			id, err := c.productIdentity(kind, op, map[string]any{"parent": group}, "name", productRecord{Data: record})
			if err != nil || !seen[id] {
				return nil, groupDenied("batch_task_membership_changed")
			}
			delete(seen, id)
		}
		if len(seen) != 0 {
			return nil, groupDenied("batch_task_membership_changed")
		}
	}
	if err := c.verifyBatchParent(ctx, target); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *client) batchList(ctx context.Context, operation string, parameters map[string]any, path string) ([]map[string]any, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	op, ok := metadata.catalog.Operation(operation)
	if !ok || op.Call == nil {
		return nil, fmt.Errorf("missing Batch dependency operation")
	}
	if _, err := catalog.BindREST(op, parameters); err != nil {
		return nil, err
	}
	return c.nativeList(ctx, op, parameters, path)
}

// VM labels have no job location. Check its named job in every native Batch
// location before allowing an explicitly selected orphan VM to use ordinary
// Compute cleanup. A same-name job with a new UID is a different incarnation.
func (c *client) batchVMHasJob(ctx context.Context, vm map[string]any) (bool, error) {
	uid := batchUID(vm)
	if uid == "" {
		return false, nil
	}
	name := text(object(vm["labels"])["batch-job-id"])
	if name == "" || name == "." || name == ".." || !segmentPattern.MatchString(name) {
		return false, groupDenied("batch_vm_job_name_invalid")
	}
	metadata, _ := providerData()
	operation, _ := metadata.catalog.Operation("batch.projects.locations.jobs.list")
	locations, supported, err := c.productLocations(ctx, operation)
	if err != nil {
		return false, err
	}
	if !supported {
		return false, groupDenied("batch_job_locations_unavailable")
	}
	for _, location := range locations {
		if location == "global" {
			continue
		}
		id := "//batch.googleapis.com/projects/" + c.project + "/locations/" + location + "/jobs/" + name
		job, err := c.nativeGet(ctx, batchJobType, id)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if c.canonicalName("//batch.googleapis.com/"+text(job["name"])) != id || text(job["uid"]) == "" {
			return false, groupDenied("batch_vm_job_identity_invalid")
		}
		if text(job["uid"]) == uid {
			return true, nil
		}
	}
	return false, nil
}
