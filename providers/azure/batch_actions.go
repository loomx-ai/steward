package azure

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type batchAction struct {
	client             *client
	kind               resourceType
	id, accountID      string
	location, endpoint string
	deletion           catalog.RESTRequest
}

func newBatchAction(c *client, value asset.Asset, kind resourceType) (*batchAction, error) {
	accountID := text(value.Normalized["_batch_account"])
	id, typ, err := parseID(accountID)
	if err != nil || id != accountID || batchKind(typ) != batchAccountType || !strings.HasPrefix(id, c.root()+"/") || value.Identity.ConnectionID == "" {
		return nil, serviceDenied("invalid_batch_action_account")
	}
	endpoint := text(value.Normalized["_batch_endpoint"])
	if !batchEndpointPattern.MatchString(endpoint) {
		return nil, serviceDenied("invalid_batch_action_endpoint")
	}
	op, params, err := c.resourceOperation(kind, value.Identity.NativeID, "DELETE")
	if err != nil {
		return nil, err
	}
	if isBatchDataType(kind.NativeType) {
		if text(params["endpoint"]) != endpoint {
			return nil, serviceDenied("batch_action_changed_endpoint")
		}
	} else if batchAccountID(value.Identity.NativeID) != accountID {
		return nil, serviceDenied("batch_action_changed_account")
	}
	deletion, err := bindAzureREST(op, params)
	if err != nil {
		return nil, err
	}
	return &batchAction{client: c, kind: kind, id: strings.ToLower(value.Identity.NativeID), accountID: accountID, endpoint: endpoint, location: strings.ToLower(value.Location), deletion: deletion}, nil
}

func (*batchAction) DeletionCheckTimeout() time.Duration { return time.Hour }

func (a *batchAction) identity(request contracts.ActionRequest) error {
	if request.Action != "delete" || request.Asset.Identity.Provider != asset.ProviderAzure || request.Asset.Identity.NativeType != a.kind.NativeType || strings.ToLower(request.Asset.Identity.NativeID) != a.id || text(request.Asset.Normalized["_batch_account"]) != a.accountID || text(request.Asset.Normalized["_batch_endpoint"]) != a.endpoint || strings.ToLower(request.Asset.Location) != a.location {
		return serviceDenied("batch_action_identity_changed")
	}
	return nil
}

func (a *batchAction) read(ctx context.Context, account batchAccountContext, value asset.Asset) (response, error) {
	if batchVMKind(value.Identity.NativeType) {
		raw, err := a.client.linkedResource(ctx, value.Identity.NativeID)
		return response{status: 200, data: raw}, err
	}
	return a.client.batchRead(ctx, account, value.Identity.NativeID, value.Identity.NativeType)
}

func (a *batchAction) impacts(request contracts.ActionRequest) (map[string]contracts.ActionImpact, error) {
	values := map[string]contracts.ActionImpact{}
	byAssetID := map[asset.AssetID]string{request.Asset.ID: a.id}
	for _, impact := range request.LifecycleImpacts {
		id := strings.ToLower(impact.Asset.Identity.NativeID)
		if impact.Asset.ID == "" || !impact.Delete || id == a.id || values[id].Asset.ID != "" || byAssetID[impact.Asset.ID] != "" || impact.Asset.Identity.ConnectionID != request.Asset.Identity.ConnectionID || impact.Asset.Identity.Partition != request.Asset.Identity.Partition || impact.Asset.Identity.Provider != asset.ProviderAzure {
			return nil, serviceDenied("invalid_batch_lifecycle_impact")
		}
		if batchVMKind(impact.Asset.Identity.NativeType) {
			_, kind, err := parseID(id)
			if err != nil || !strings.HasPrefix(id, a.client.root()+"/") || !strings.EqualFold(kind, impact.Asset.Identity.NativeType) {
				return nil, serviceDenied("invalid_batch_vm_impact")
			}
		} else if !isBatchType(impact.Asset.Identity.NativeType) || text(impact.Asset.Normalized["_batch_account"]) != a.accountID || text(impact.Asset.Normalized["_batch_endpoint"]) != a.endpoint {
			return nil, serviceDenied("invalid_batch_lifecycle_impact")
		} else if isBatchDataType(impact.Asset.Identity.NativeType) {
			_, kind, origin, _, err := batchDataIdentity(id)
			if err != nil || kind != impact.Asset.Identity.NativeType || origin != a.endpoint {
				return nil, serviceDenied("invalid_batch_data_impact")
			}
		} else if batchAccountID(id) != a.accountID {
			return nil, serviceDenied("invalid_batch_arm_impact")
		}
		values[id], byAssetID[impact.Asset.ID] = impact, id
	}
	for id, impact := range values {
		seen := map[string]bool{}
		for current := impact; ; current = values[id] {
			if seen[id] {
				return nil, serviceDenied("cyclic_batch_lifecycle_impact")
			}
			seen[id] = true
			parentID, exists := byAssetID[current.ControllerID]
			if !exists {
				return nil, serviceDenied("batch_impact_controller_missing")
			}
			if parentID == a.id {
				break
			}
			id = parentID
		}
	}
	return values, a.vmImpacts(request, values)
}

func (a *batchAction) protected(ctx context.Context, account batchAccountContext, member batchMember, locks []any) error {
	if batchVMKind(member.kind) {
		return a.protectedVM(ctx, account, member, locks)
	}
	if err := batchReady(member.kind, member.raw); err != nil {
		return err
	}
	if reason := batchProtection(member.kind, member.raw); reason != "" && !(member.kind == batchPerimeterType && member.parent == a.accountID && a.kind.NativeType == batchAccountType) {
		return serviceDenied(reason)
	}
	lockID := member.id
	if isBatchDataType(member.kind) {
		lockID = account.id
		if member.kind == batchNodeType {
			_, _, _, parameters, _ := batchDataIdentity(member.id)
			lockID += "/pools/" + strings.ToLower(text(parameters["poolId"]))
		}
	}
	if locked(lockID, locks) {
		return serviceDenied("azure_management_lock")
	}
	if member.kind == batchPackageType {
		app, err := a.client.batchRead(ctx, account, redisParentID(member.id), batchApplicationType)
		if err != nil {
			return err
		}
		if allowed, ok := object(app.data["properties"])["allowUpdates"].(bool); !ok || !allowed {
			return serviceDenied("batch_application_package_updates_disabled")
		}
	}
	return nil
}

func (a *batchAction) preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, batchAccountContext, response, error) {
	deny := func(err error) (contracts.PreflightResult, batchAccountContext, response, error) {
		return contracts.PreflightResult{}, batchAccountContext{}, response{}, err
	}
	if err := a.identity(request); err != nil {
		return deny(err)
	}
	impacts, err := a.impacts(request)
	if err != nil {
		return deny(err)
	}
	account, err := a.client.batchAccount(ctx, a.accountID)
	if err != nil {
		return deny(err)
	}
	if err := batchAssetAccount(a.client, request.Asset, account); err != nil {
		return deny(err)
	}
	live, err := a.read(ctx, account, request.Asset)
	if isNotFound(err) {
		read, readErr := a.readImpacts(ctx, request, account)
		return contracts.PreflightResult{Allowed: readErr == nil, Absent: readErr == nil && !read.Exists, Evidence: map[string]any{"batch_parent_absent": true}}, account, live, readErr
	}
	if err != nil {
		return deny(err)
	}
	if err := a.incarnation(ctx, account, request, live.data); err != nil {
		return deny(err)
	}
	groupID := strings.Join(strings.Split(a.accountID, "/")[:5], "/")
	group, err := a.client.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return deny(err)
	}
	if !validResourceResponse(group, groupID, groupType) || text(group.data["managedBy"]) != "" {
		return deny(serviceDenied("azure_managed_resource_group"))
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return deny(err)
	}
	if batchProtection(batchAccountType, account.raw) != "" || locked(a.accountID, locks) {
		return deny(serviceDenied("batch_account_protected"))
	}
	expectedAncestors := batchARMAncestors(a.id, a.kind.NativeType)
	if a.kind.NativeType == batchTaskType {
		expectedAncestors = []string{redisParentID(a.id)}
	}
	if a.kind.NativeType == batchNodeType {
		_, _, _, params, _ := batchDataIdentity(a.id)
		expectedAncestors = []string{a.accountID + "/pools/" + strings.ToLower(text(params["poolId"]))}
	}
	plannedAncestors := object(request.Asset.Normalized["_batch_ancestors"])
	if len(plannedAncestors) != len(expectedAncestors) {
		return deny(serviceDenied("batch_ancestor_binding_missing"))
	}
	for _, parentID := range expectedAncestors {
		expected := plannedAncestors[parentID]
		kind := batchJobType
		if !strings.HasPrefix(parentID, "https://") {
			_, kind, err = parseID(parentID)
			kind = batchKind(kind)
		}
		parent, err := a.client.batchRead(ctx, account, parentID, kind)
		if err != nil {
			return deny(err)
		}
		snapshot := batchSnapshot(kind, parent.data)
		if a.kind.NativeType == batchNodeType && kind == batchPoolType {
			snapshot = batchNodePoolSnapshot(parent.data)
		}
		if text(expected) == "" || text(expected) != a.client.privateConfiguration(snapshot) {
			restored, err := a.restoreApplicationDefault(ctx, account, kind, parent.data, text(object(request.Asset.Normalized["_batch_ancestor_defaults"])[parentID]))
			if err != nil || text(expected) == "" || text(expected) != a.client.privateConfiguration(batchSnapshot(kind, restored)) {
				return deny(serviceDenied("batch_ancestor_changed"))
			}
		}
		if err := a.protected(ctx, account, batchMember{id: parentID, kind: kind, raw: parent.data}, locks); err != nil {
			return deny(err)
		}
	}
	if err := a.prerequisitesAbsent(ctx, request, account); err != nil {
		return deny(err)
	}
	topology, err := a.client.batchTopology(ctx, account)
	if err != nil {
		return deny(err)
	}
	root, exists := topology.members[a.id]
	if !exists || root.kind != a.kind.NativeType {
		return deny(serviceDenied("batch_root_membership_changed"))
	}
	if root.kind == batchNodeType {
		if err := topology.verifyAsset(a.client, request.Asset, root); err != nil {
			return deny(err)
		}
	}
	if err := a.protected(ctx, account, root, locks); err != nil {
		return deny(err)
	}
	for parent := root.parent; parent != ""; parent = topology.members[parent].parent {
		if err := a.protected(ctx, account, topology.members[parent], locks); err != nil {
			return deny(err)
		}
	}
	assets := map[string]asset.Asset{a.id: request.Asset}
	for id, impact := range impacts {
		assets[id] = impact.Asset
	}
	seen := map[string]bool{}
	var verify func(string) error
	verify = func(id string) error {
		for _, child := range topology.children(id) {
			if child.direct {
				return serviceDenied("batch_child_requires_prior_deletion")
			}
			impact, exists := impacts[child.id]
			if !exists || impact.ControllerID != assets[id].ID || impact.Asset.Identity.NativeType != child.kind {
				return serviceDenied("batch_child_missing_from_plan")
			}
			seen[child.id] = true
			if isBatchType(child.kind) {
				if err := batchAssetAccount(a.client, impact.Asset, account); err != nil {
					return err
				}
			}
			if err := topology.verifyAsset(a.client, impact.Asset, child); err != nil {
				return err
			}
			if err := a.protected(ctx, account, child, locks); err != nil {
				return err
			}
			if err := verify(child.id); err != nil {
				return err
			}
		}
		return nil
	}
	if err := verify(a.id); err != nil {
		return deny(err)
	}
	for id, impact := range impacts {
		if seen[id] {
			continue
		}
		if _, err := a.read(ctx, account, impact.Asset); !isNotFound(err) {
			if err != nil {
				return deny(err)
			}
			return deny(serviceDenied("batch_impact_membership_changed"))
		}
	}
	for id, member := range topology.members {
		if topology.descendant(id, a.id) {
			continue
		}
		refs, err := a.client.batchReferences(ctx, account, id, member.kind, member.raw)
		if err != nil {
			return deny(err)
		}
		for kind, ids := range refs {
			// Task placement is historical and RemoveNodes requeues running
			// tasks. It does not require deleting their jobs or task records.
			if member.kind == batchTaskType && kind == batchNodeType {
				continue
			}
			for _, target := range ids {
				if topology.descendant(target, a.id) && (target == a.id || seen[target]) {
					return deny(serviceDenied("batch_resource_still_referenced"))
				}
			}
		}
	}
	// Re-read after every dependency check. Data-plane DELETE uses this ETag;
	// ARM's selected DELETE operations do not declare If-Match.
	live, err = a.read(ctx, account, request.Asset)
	if err != nil {
		return deny(err)
	}
	if err := a.incarnation(ctx, account, request, live.data); err != nil {
		return deny(err)
	}
	if _, _, err := a.collectTaskFiles(ctx, request, account); err != nil {
		return deny(err)
	}
	return contracts.PreflightResult{Allowed: true}, account, live, nil
}

func (a *batchAction) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	result, _, _, err := a.preflight(ctx, request)
	return result, contracts.DependencyReadError(err)
}

func (a *batchAction) prerequisitesAbsent(ctx context.Context, request contracts.ActionRequest, account batchAccountContext) error {
	seen := map[string]bool{}
	for _, impact := range request.PrerequisiteDeletions {
		id := strings.ToLower(impact.Asset.Identity.NativeID)
		if seen[id] || id == a.id || !impact.Delete || impact.ControllerID != request.Asset.ID || impact.Asset.Identity.ConnectionID != request.Asset.Identity.ConnectionID || impact.Asset.Identity.Partition != request.Asset.Identity.Partition {
			return serviceDenied("invalid_batch_prerequisite")
		}
		seen[id] = true
		if err := batchAssetAccount(a.client, impact.Asset, account); err != nil {
			return err
		}
		if impact.Asset.Identity.NativeType == batchNodeType {
			if err := a.client.batchNodeAbsent(ctx, account, impact.Asset); err != nil {
				return err
			}
			continue
		}
		if _, err := a.read(ctx, account, impact.Asset); !isNotFound(err) {
			if err != nil {
				return err
			}
			return serviceDenied("batch_prerequisite_still_exists")
		}
	}
	return nil
}

func batchETag(raw response) (string, error) {
	value := raw.header.Get("ETag")
	if value == "" {
		value = text(raw.data["eTag"])
	}
	if value == "" || value == "*" || strings.ContainsAny(value, "\r\n") {
		return "", serviceDenied("batch_conditional_etag_missing")
	}
	return value, nil
}

func (a *batchAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	check, account, live, err := a.preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, contracts.DependencyReadError(err)
	}
	if !check.Allowed {
		return contracts.ActionResult{}, serviceDenied(check.Reason)
	}
	if check.Absent || check.Evidence["batch_parent_absent"] == true {
		return a.taskOperationResult(request, response{status: 204}, nil, "deleting")
	}
	deletion := a.deletion
	deletion.Headers = maps.Clone(deletion.Headers)
	if deletion.Headers == nil {
		deletion.Headers = map[string]string{}
	}
	if a.kind.NativeType == batchNodeType {
		_, _, _, params, _ := batchDataIdentity(a.id)
		pool, err := a.client.batchPoolData(ctx, account, text(params["poolId"]))
		if err != nil {
			return contracts.ActionResult{}, contracts.DependencyReadError(err)
		}
		if pool.data["allocationState"] != "steady" {
			return contracts.ActionResult{}, serviceDenied("batch_pool_not_steady")
		}
		node, err := a.read(ctx, account, request.Asset)
		if err != nil {
			return contracts.ActionResult{}, contracts.DependencyReadError(err)
		}
		if err := a.incarnation(ctx, account, request, node.data); err != nil {
			return contracts.ActionResult{}, err
		}
		live = pool // RemoveNodes conditions the pool, not the individual node.
	}
	if a.kind.NativeType == batchScheduleType && live.data["state"] == "active" {
		etag, err := batchETag(live)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		metadata, err := providerData()
		if err != nil {
			return contracts.ActionResult{}, err
		}
		op, _ := metadata.catalog.Operation(batchDataPrefix + "JobSchedules_DisableJobSchedule")
		_, _, _, params, _ := batchDataIdentity(a.id)
		params["If-Match"] = etag
		bound, err := bindAzureREST(op, params)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		disabled, err := a.client.batchRequest(ctx, account, bound)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if disabled.status != 204 || len(disabled.data) != 0 || operationLocation(disabled.header) != "" {
			return contracts.ActionResult{}, serviceDenied("invalid_batch_schedule_disable_response")
		}
		// Disable is synchronous. Repeat the membership review before DELETE;
		// a job created just before disable must appear in the reviewed plan.
		check, account, live, err = a.preflight(ctx, request)
		if err != nil {
			return contracts.ActionResult{}, contracts.DependencyReadError(err)
		}
		if check.Absent || check.Evidence["batch_parent_absent"] == true {
			return a.taskOperationResult(request, response{status: 204}, nil, "deleting")
		}
		if !check.Allowed || live.data["state"] == "active" {
			return contracts.ActionResult{}, serviceDenied("batch_schedule_still_active")
		}
	}
	files, pendingTasks, err := a.collectTaskFiles(ctx, request, account)
	if err != nil {
		return contracts.ActionResult{}, contracts.DependencyReadError(err)
	}
	if pendingTasks {
		if err := a.terminateTasks(ctx, request, account); err != nil {
			return contracts.ActionResult{}, err
		}
		return a.taskOperationResult(request, response{status: 204}, files, "terminating")
	}
	if len(batchMPITasks(request)) > 0 {
		live, err = a.read(ctx, account, request.Asset)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		if err := a.incarnation(ctx, account, request, live.data); err != nil {
			return contracts.ActionResult{}, err
		}
		if a.kind.NativeType == batchTaskType && live.data["state"] != "completed" {
			return contracts.ActionResult{}, serviceDenied("batch_task_reactivated_before_delete")
		}
	}
	if isBatchDataType(a.kind.NativeType) {
		etag, err := batchETag(live)
		if err != nil {
			return contracts.ActionResult{}, err
		}
		deletion.Headers["If-Match"] = etag
		deletion.Headers["client-request-id"] = azureRequestID(request.IdempotencyKey)
	} else if request.IdempotencyKey != "" {
		deletion.Headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey)
	}
	var result response
	if isBatchDataType(a.kind.NativeType) {
		result, err = a.client.batchRequest(ctx, account, deletion)
	} else {
		result, err = a.client.requestBody(ctx, deletion.Method, deletion.URL, deletion.Body, deletion.Headers)
	}
	if isNotFound(err) {
		return a.taskOperationResult(request, response{status: 204}, files, "deleting")
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if err := operationError(result); err != nil {
		return contracts.ActionResult{}, err
	}
	statuses := []int{200, 202, 204}
	switch a.kind.NativeType {
	case batchTaskType:
		statuses = []int{200}
	case batchJobType, batchScheduleType, batchNodeType:
		statuses = []int{202}
	case batchApplicationType, batchPackageType:
		statuses = []int{200, 204}
	case batchPECType:
		statuses = []int{202, 204}
	}
	if result.data["code"] != nil || !slices.Contains(statuses, result.status) || (isBatchDataType(a.kind.NativeType) && len(result.data) != 0) {
		return contracts.ActionResult{}, serviceDenied("invalid_batch_delete_response")
	}
	return a.taskOperationResult(request, result, files, "deleting")
}

func (a *batchAction) readImpacts(ctx context.Context, request contracts.ActionRequest, account batchAccountContext) (contracts.ReadbackResult, error) {
	impacts, err := a.impacts(request)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.prerequisitesAbsent(ctx, request, account); err != nil {
		return contracts.ReadbackResult{}, err
	}
	for _, impact := range impacts {
		if _, err := a.read(ctx, account, impact.Asset); !isNotFound(err) {
			if err != nil {
				return contracts.ReadbackResult{}, err
			}
			return contracts.ReadbackResult{Exists: true, State: "batch_children_deleting"}, nil
		}
	}
	return a.taskFilesAbsent(ctx, request, account)
}

func (a *batchAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	account, err := a.client.batchAccount(ctx, a.accountID)
	if isNotFound(err) && a.kind.NativeType == batchAccountType {
		return a.absentAccount(ctx, request)
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := batchAssetAccount(a.client, request.Asset, account); err != nil {
		return contracts.ReadbackResult{}, err
	}
	current, err := a.read(ctx, account, request.Asset)
	if isNotFound(err) {
		return a.readImpacts(ctx, request, account)
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.incarnation(ctx, account, request, current.data); err != nil {
		return contracts.ReadbackResult{}, err
	}
	return contracts.ReadbackResult{Exists: true, State: batchState(a.kind.NativeType, current.data)}, nil
}

var _ contracts.ActionDriver = (*batchAction)(nil)

// Native package deletion clears defaultVersion only when that exact package
// was the default. Permit this narrow side effect after confirming its absence;
// every other authored application field remains bound by the private digest.
func (a *batchAction) restoreApplicationDefault(ctx context.Context, account batchAccountContext, kind string, live map[string]any, original string) (map[string]any, error) {
	if kind != batchApplicationType || original == "" || text(object(live["properties"])["defaultVersion"]) != "" {
		return live, nil
	}
	packageID, err := cognitiveNameID(text(live["id"]), "versions", original)
	if err != nil {
		return nil, err
	}
	if _, err := a.client.batchRead(ctx, account, packageID, batchPackageType); !isNotFound(err) {
		if err != nil {
			return nil, err
		}
		return live, nil
	}
	copy := batchClone(live)
	object(copy["properties"])["defaultVersion"] = original
	return copy, nil
}

func (a *batchAction) incarnation(ctx context.Context, account batchAccountContext, request contracts.ActionRequest, live map[string]any) error {
	planned := request.Asset
	if planned.Identity.NativeType == batchNodeType {
		members, err := a.client.batchVMTree(ctx, account, live)
		if err != nil {
			return err
		}
		if text(planned.Normalized["_batch_vm_binding"]) != a.client.batchNodeBinding(live, a.client.batchVMResources(members)) {
			return serviceDenied("batch_node_vm_configuration_changed")
		}
	}
	if err := batchIncarnation(a.client, planned, live); err == nil {
		return nil
	}
	if planned.Identity.NativeType == batchPoolType {
		restored, err := a.restorePoolCapacity(ctx, request, account, live)
		if err != nil {
			return err
		}
		return batchIncarnation(a.client, planned, restored)
	}
	restored, err := a.restoreApplicationDefault(ctx, account, planned.Identity.NativeType, live, text(planned.Normalized["defaultVersion"]))
	if err != nil {
		return err
	}
	return batchIncarnation(a.client, planned, restored)
}

func (a *batchAction) absentAccount(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	impacts, err := a.impacts(request)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	receipt := request.ExecutionResult
	if receipt == nil || text(receipt.Data["batch_operation_binding"]) != a.operationBinding(receipt.ProviderOperationID, request) {
		return contracts.ReadbackResult{}, serviceDenied("batch_account_absence_requires_execution_receipt")
	}
	// Account cleanup independently deletes jobs, schedules and pools before its
	// own ARM DELETE. Its authenticated receipt preserves that verification when
	// ARM removes the endpoint authority. Check remaining ARM impacts directly.
	account := batchAccountContext{id: a.accountID, endpoint: a.endpoint, location: a.location}
	for _, impact := range impacts {
		if isBatchDataType(impact.Asset.Identity.NativeType) {
			return contracts.ReadbackResult{}, serviceDenied("batch_account_has_unverified_data_impact")
		}
		if _, err := a.read(ctx, account, impact.Asset); !isNotFound(err) {
			if err != nil {
				return contracts.ReadbackResult{}, err
			}
			return contracts.ReadbackResult{Exists: true, State: "batch_account_children_deleting"}, nil
		}
	}
	for _, impact := range request.PrerequisiteDeletions {
		if isBatchDataType(impact.Asset.Identity.NativeType) {
			continue
		}
		if _, err := a.read(ctx, account, impact.Asset); !isNotFound(err) {
			if err != nil {
				return contracts.ReadbackResult{}, err
			}
			return contracts.ReadbackResult{Exists: true, State: "batch_account_prerequisite_recreated"}, nil
		}
	}
	return contracts.ReadbackResult{}, nil
}
