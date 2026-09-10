package azure

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type batchTaskFile struct {
	TaskID      string `json:"task_id"`
	NodeID      string `json:"node_id"`
	Directory   string `json:"directory"`
	NodeBinding string `json:"node_binding"`
}

func batchFileDirectory(value string) error {
	if value == "" || len(value) > 4096 || strings.ContainsAny(value, "\\%\x00\r\n") {
		return serviceDenied("invalid_batch_task_directory")
	}
	for _, part := range strings.Split(strings.TrimPrefix(value, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return serviceDenied("invalid_batch_task_directory")
		}
	}
	return nil
}

// Only the native file HEAD route accepts an encoded directory parameter.
// Ordinary Batch resource identities continue to reject encoded paths.
func batchFileURL(u *url.URL) error {
	parts := strings.SplitN(u.Path, "/files/", 2)
	if len(parts) != 2 || batchFileDirectory(parts[1]) != nil {
		return serviceDenied("invalid_batch_file_endpoint")
	}
	_, kind, _, _, err := batchDataIdentity(u.Scheme + "://" + u.Host + parts[0])
	if err != nil || kind != batchNodeType || u.EscapedPath() != parts[0]+"/files/"+url.PathEscape(parts[1]) {
		return serviceDenied("invalid_batch_file_endpoint")
	}
	return nil
}

func batchMPITasks(request contracts.ActionRequest) []asset.Asset {
	values := []asset.Asset{request.Asset}
	for _, impact := range request.LifecycleImpacts {
		values = append(values, impact.Asset)
	}
	values = slices.DeleteFunc(values, func(value asset.Asset) bool {
		return value.Identity.NativeType != batchTaskType || value.Normalized["multiInstanceSettings"] == nil
	})
	slices.SortFunc(values, func(a, b asset.Asset) int { return strings.Compare(a.Identity.NativeID, b.Identity.NativeID) })
	return values
}

func (c *client) batchTaskLocation(ctx context.Context, account batchAccountContext, taskID string, info map[string]any) (batchTaskFile, error) {
	nodeID, _, err := batchTaskNode(account, info)
	directory := strings.ReplaceAll(text(info["taskRootDirectory"]), "\\", "/")
	u, parseErr := url.Parse(text(info["taskRootDirectoryUrl"]))
	if err != nil || nodeID == "" || batchFileDirectory(directory) != nil || parseErr != nil || u.Scheme+"://"+u.Host != account.endpoint || u.User != nil || u.Fragment != "" || u.RawQuery != "" || u.Port() != "" {
		return batchTaskFile{}, serviceDenied("invalid_batch_task_node_info")
	}
	prefix := strings.TrimPrefix(nodeID, account.endpoint) + "/files/"
	if len(u.Path) <= len(prefix) || !strings.EqualFold(u.Path[:len(prefix)], prefix) || u.Path[len(prefix):] != directory {
		return batchTaskFile{}, serviceDenied("batch_task_directory_url_disagrees")
	}
	current, err := c.batchRead(ctx, account, nodeID, batchNodeType)
	if err != nil && !isNotFound(err) {
		return batchTaskFile{}, err
	}
	binding := "absent"
	if err == nil {
		binding = c.privateConfiguration(batchSnapshot(batchNodeType, current.data))
	}
	return batchTaskFile{TaskID: taskID, NodeID: nodeID, Directory: directory, NodeBinding: binding}, nil
}

func (c *client) batchTaskFiles(ctx context.Context, account batchAccountContext, task asset.Asset, live map[string]any) ([]batchTaskFile, bool, error) {
	n, err := batchInteger(object(live["multiInstanceSettings"])["numberOfInstances"], 32)
	if err != nil || n < 1 {
		return nil, false, serviceDenied("invalid_batch_multi_instance_count")
	}
	_, _, _, params, _ := batchDataIdentity(task.Identity.NativeID)
	delete(params, "endpoint")
	subtasks, _, err := c.batchDataList(ctx, account, "Tasks_ListSubTasks", params)
	if err != nil {
		return nil, false, err
	}
	if int64(len(subtasks)) > n-1 {
		return nil, false, serviceDenied("batch_subtask_count_disagrees")
	}
	files := []batchTaskFile{}
	pending := live["state"] != "completed"
	nodes := map[string]bool{}
	add := func(info map[string]any) error {
		file, err := c.batchTaskLocation(ctx, account, strings.ToLower(task.Identity.NativeID), info)
		if err == nil {
			if nodes[file.NodeID] {
				return serviceDenied("duplicate_batch_subtask_node")
			}
			nodes[file.NodeID] = true
			files = append(files, file)
		}
		return err
	}
	if info := object(live["nodeInfo"]); len(info) > 0 {
		if int64(len(subtasks)) != n-1 {
			return nil, false, serviceDenied("batch_subtask_membership_incomplete")
		}
		if err := add(info); err != nil {
			return nil, false, err
		}
	} else if len(subtasks) != 0 || text(object(live["executionInfo"])["startTime"]) != "" {
		return nil, false, serviceDenied("batch_primary_node_info_missing")
	}
	seen := map[int64]bool{}
	for _, subtask := range subtasks {
		id, err := batchInteger(subtask["id"], 32)
		if err != nil || id < 1 || id >= n || seen[id] || !slices.Contains([]string{"preparing", "running", "completed"}, text(subtask["state"])) {
			return nil, false, serviceDenied("invalid_batch_subtask_identity_or_state")
		}
		seen[id] = true
		pending = pending || subtask["state"] != "completed"
		if err := add(object(subtask["nodeInfo"])); err != nil {
			return nil, false, err
		}
	}
	slices.SortFunc(files, func(a, b batchTaskFile) int {
		return strings.Compare(a.NodeID+"/"+a.Directory, b.NodeID+"/"+b.Directory)
	})
	return files, pending, nil
}

func (a *batchAction) taskBinding(request contracts.ActionRequest, result contracts.ActionResult) string {
	return a.client.privateConfiguration(batchClone(map[string]any{"operation": a.operationBinding(result.ProviderOperationID, request), "phase": result.Data["batch_task_phase"], "files": result.Data["batch_task_files"]}))
}

func (a *batchAction) taskReceipt(request contracts.ActionRequest) ([]batchTaskFile, error) {
	if request.ExecutionResult == nil {
		return nil, nil
	}
	result := *request.ExecutionResult
	if result.Data["batch_task_phase"] != "terminating" && result.Data["batch_task_phase"] != "deleting" || result.Data["batch_task_binding"] != a.taskBinding(request, result) || result.Data["batch_operation_binding"] != a.operationBinding(result.ProviderOperationID, request) {
		return nil, serviceDenied("batch_task_cleanup_receipt_changed")
	}
	payload, err := json.Marshal(result.Data["batch_task_files"])
	var files []batchTaskFile
	if err != nil || json.Unmarshal(payload, &files) != nil || files == nil {
		return nil, serviceDenied("invalid_batch_task_cleanup_receipt")
	}
	return files, nil
}

func (a *batchAction) collectTaskFiles(ctx context.Context, request contracts.ActionRequest, account batchAccountContext) ([]batchTaskFile, bool, error) {
	files := []batchTaskFile{}
	tasks := batchMPITasks(request)
	if len(tasks) == 0 {
		return files, false, nil
	}
	previous, err := a.taskReceipt(request)
	if err != nil {
		return nil, false, err
	}
	files = append(files, previous...)
	pending := false
	for _, task := range tasks {
		first, err := a.read(ctx, account, task)
		if isNotFound(err) && request.ExecutionResult != nil {
			continue
		}
		if err != nil {
			return nil, false, err
		}
		if err := batchIncarnation(a.client, task, first.data); err != nil {
			return nil, false, err
		}
		before, running, err := a.client.batchTaskFiles(ctx, account, task, first.data)
		if err != nil {
			return nil, false, err
		}
		second, err := a.read(ctx, account, task)
		if err != nil {
			return nil, false, err
		}
		if err := batchIncarnation(a.client, task, second.data); err != nil {
			return nil, false, err
		}
		after, stillRunning, err := a.client.batchTaskFiles(ctx, account, task, second.data)
		if err != nil {
			return nil, false, err
		}
		if !slices.Equal(before, after) {
			return nil, false, serviceDenied("batch_task_locations_changed_during_walk")
		}
		files = append(files, after...)
		pending = pending || running || stillRunning
	}
	slices.SortFunc(files, func(a, b batchTaskFile) int {
		return strings.Compare(a.TaskID+a.NodeID+a.Directory+a.NodeBinding, b.TaskID+b.NodeID+b.Directory+b.NodeBinding)
	})
	return slices.Compact(files), pending, nil
}

// Termination stops task recovery from assigning another node while DELETE is
// prepared. Native termination completes the primary synchronously, but every
// subtask must reach completed before the final directory snapshot and DELETE.
func (a *batchAction) terminateTasks(ctx context.Context, request contracts.ActionRequest, account batchAccountContext) error {
	metadata, err := providerData()
	if err != nil {
		return err
	}
	op, _ := metadata.catalog.Operation(batchDataPrefix + "Tasks_TerminateTask")
	for _, task := range batchMPITasks(request) {
		current, err := a.read(ctx, account, task)
		if err != nil {
			return err
		}
		if err := batchIncarnation(a.client, task, current.data); err != nil {
			return err
		}
		if current.data["state"] == "completed" {
			continue
		}
		etag, err := batchETag(current)
		if err != nil {
			return err
		}
		_, _, _, params, _ := batchDataIdentity(task.Identity.NativeID)
		params["If-Match"], params["client-request-id"] = etag, azureRequestID(request.IdempotencyKey+task.Identity.NativeID)
		bound, err := bindAzureREST(op, params)
		if err != nil {
			return err
		}
		res, err := a.client.batchRequest(ctx, account, bound)
		if err != nil {
			return err
		}
		if res.status != 204 || len(res.data) != 0 || operationLocation(res.header) != "" {
			return serviceDenied("invalid_batch_task_termination_response")
		}
	}
	return nil
}

func (a *batchAction) taskOperationResult(request contracts.ActionRequest, res response, files []batchTaskFile, phase string) (contracts.ActionResult, error) {
	result, err := a.operationResult(request, res)
	if err != nil || len(batchMPITasks(request)) == 0 {
		return result, err
	}
	if files == nil {
		files, err = a.taskReceipt(request)
		if err != nil || files == nil {
			return contracts.ActionResult{}, serviceDenied("batch_task_absence_requires_cleanup_receipt")
		}
	}
	result.Data["batch_task_phase"], result.Data["batch_task_files"] = phase, files
	result.Data["batch_task_binding"] = a.taskBinding(request, result)
	return result, nil
}

func (a *batchAction) taskFilesAbsent(ctx context.Context, request contracts.ActionRequest, account batchAccountContext) (contracts.ReadbackResult, error) {
	if len(batchMPITasks(request)) == 0 {
		return contracts.ReadbackResult{}, nil
	}
	files, err := a.taskReceipt(request)
	if err != nil || files == nil {
		return contracts.ReadbackResult{}, serviceDenied("batch_task_absence_requires_cleanup_receipt")
	}
	metadata, err := providerData()
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	op, _ := metadata.catalog.Operation(batchDataPrefix + "Nodes_GetNodeFileProperties")
	for _, file := range files {
		current, err := a.client.batchRead(ctx, account, file.NodeID, batchNodeType)
		if err != nil && !isNotFound(err) {
			return contracts.ReadbackResult{}, err
		}
		if err == nil && file.NodeBinding != a.client.privateConfiguration(batchSnapshot(batchNodeType, current.data)) {
			return contracts.ReadbackResult{}, serviceDenied("batch_task_node_recreated")
		}
		_, _, _, params, err := batchDataIdentity(file.NodeID)
		if err != nil || batchFileDirectory(file.Directory) != nil {
			return contracts.ReadbackResult{}, serviceDenied("invalid_batch_task_file_receipt")
		}
		params["filePath"] = file.Directory
		bound, err := bindAzureREST(op, params)
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		res, err := a.client.batchRequest(ctx, account, bound)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		if res.status != 200 || len(res.data) != 0 || operationLocation(res.header) != "" || !strings.EqualFold(res.header.Get("ocp-batch-file-isdirectory"), "true") {
			return contracts.ReadbackResult{}, serviceDenied("invalid_batch_task_directory_response")
		}
		return contracts.ReadbackResult{Exists: true, State: "batch_subtask_files_deleting"}, nil
	}
	return contracts.ReadbackResult{}, nil
}
