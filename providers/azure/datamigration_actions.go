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

type dataMigrationAction struct {
	client   *client
	planned  asset.Asset
	kind     resourceType
	id       string
	deletion catalog.RESTRequest
}

func newDataMigrationAction(c *client, connection asset.ConnectionID, value asset.Asset, kind resourceType) (*dataMigrationAction, error) {
	if err := c.dataMigrationAsset(value); err != nil {
		return nil, err
	}
	if connection != value.Identity.ConnectionID || kind.NativeType != value.Identity.NativeType || kind.ReadOnly {
		return nil, serviceDenied("datamigration_action_connection_changed")
	}
	op, params, err := c.resourceOperation(kind, value.Identity.NativeID, "DELETE")
	if err != nil {
		return nil, err
	}
	if dataMigrationClassic(kind.NativeType) && kind.NativeType != dataMigrationFileType {
		params["deleteRunningTasks"] = false
	}
	if kind.NativeType == dataMigrationType {
		params["force"] = false
	}
	deletion, err := bindAzureREST(op, params)
	if err != nil {
		return nil, err
	}
	return &dataMigrationAction{client: c, planned: value, kind: kind, id: value.Identity.NativeID, deletion: deletion}, nil
}

func (a *dataMigrationAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }

func (a *dataMigrationAction) prerequisites(request contracts.ActionRequest) map[string]any {
	result := maps.Clone(object(request.Asset.Normalized["_datamigration_dependents"]))
	for id, value := range object(request.Asset.Normalized[dataMigrationMembers]) {
		if object(value)["parent"] == a.id {
			result[id] = value
		}
	}
	return result
}

func (a *dataMigrationAction) identity(request contracts.ActionRequest) error {
	value := request.Asset
	if request.Action != "delete" || value.ID != a.planned.ID || value.Identity != a.planned.Identity || value.Normalized[dataMigrationProof] != a.planned.Normalized[dataMigrationProof] || len(request.Parameters)+len(request.LifecycleImpacts) != 0 {
		return serviceDenied("datamigration_action_request_changed")
	}
	if err := a.client.dataMigrationAsset(value); err != nil {
		return err
	}
	if value.Normalized["cleanup_protected"] != false {
		return serviceDenied("datamigration_reviewed_resource_protected")
	}
	expected := a.prerequisites(request)
	if len(request.PrerequisiteDeletions) != len(expected) {
		return serviceDenied("datamigration_review_prerequisites_changed")
	}
	seen, ids := map[asset.AssetID]bool{value.ID: true}, map[string]bool{a.id: true}
	for _, prerequisite := range request.PrerequisiteDeletions {
		child, entry := prerequisite.Asset, object(expected[prerequisite.Asset.Identity.NativeID])
		if !prerequisite.Delete || prerequisite.ControllerID != value.ID || seen[child.ID] || ids[child.Identity.NativeID] || entry == nil || entry["kind"] != child.Identity.NativeType || entry["configuration"] != child.Normalized[dataMigrationConfiguration] || child.Identity.ConnectionID != value.Identity.ConnectionID || child.Identity.Partition != value.Identity.Partition {
			return serviceDenied("invalid_datamigration_review_prerequisite")
		}
		if err := a.client.dataMigrationAsset(child); err != nil {
			return err
		}
		if entry["parent"] == a.id && (child.Location != value.Location || object(child.Normalized["_datamigration_ancestors"])[a.id] != value.Normalized[dataMigrationConfiguration]) {
			return serviceDenied("datamigration_review_child_context_changed")
		}
		seen[child.ID], ids[child.Identity.NativeID] = true, true
	}
	if request.ExecutionResult != nil {
		return a.verifyPhase(request, *request.ExecutionResult)
	}
	return nil
}

func (a *dataMigrationAction) phaseBinding(request contracts.ActionRequest, result contracts.ActionResult) string {
	request.ExecutionResult, request.IdempotencyKey = nil, ""
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "datamigration-cleanup-1", "request": request, "origin": result.ProviderOperationID, "data": data})
}

func (a *dataMigrationAction) verifyPhase(request contracts.ActionRequest, result contracts.ActionResult) error {
	if !slices.Contains([]string{"wait", "cancel", "delete-node", "delete"}, text(result.Data["phase"])) || result.Data["binding"] != a.phaseBinding(request, result) {
		return serviceDenied("datamigration_cleanup_receipt_changed")
	}
	return nil
}

type dataMigrationObservation struct {
	members       map[string]dataMigrationMember
	missingParent bool
}

// Keep every recorded descendant and consumer's own GET after its service,
// project or target disappears. A parent 404 cannot establish child absence.
func (a *dataMigrationAction) observe(ctx context.Context, request contracts.ActionRequest) (dataMigrationObservation, error) {
	out := dataMigrationObservation{members: map[string]dataMigrationMember{}}
	expected := maps.Clone(object(request.Asset.Normalized[dataMigrationMembers]))
	maps.Copy(expected, object(request.Asset.Normalized["_datamigration_dependents"]))
	for id, configuration := range object(request.Asset.Normalized["_datamigration_ancestors"]) {
		_, typ, _ := parseID(id)
		expected[id] = map[string]any{"kind": dataMigrationKind(typ), "configuration": configuration}
	}
	expected[a.id] = map[string]any{"kind": a.kind.NativeType, "configuration": request.Asset.Normalized[dataMigrationConfiguration]}
	for _, id := range slices.Sorted(maps.Keys(expected)) {
		entry := object(expected[id])
		kind := text(entry["kind"])
		raw, err := a.client.dataMigrationRead(ctx, id, kind)
		if isNotFound(err) {
			out.missingParent = out.missingParent || object(request.Asset.Normalized["_datamigration_ancestors"])[id] != nil
			continue
		}
		if err != nil {
			return out, err
		}
		if a.client.privateConfiguration(dataMigrationSnapshot(kind, raw)) != entry["configuration"] {
			return out, serviceDenied("datamigration_action_configuration_changed")
		}
		out.members[id] = dataMigrationMemberFrom(id, kind, raw)
	}
	return out, nil
}

func (a *dataMigrationAction) current(ctx context.Context, request contracts.ActionRequest) (dataMigrationForest, error) {
	kind := asset.ResourceKind{NativeType: a.kind.NativeType}
	hints, err := a.client.dataMigrationKnown(contracts.InventoryRequest{ConnectionID: request.Asset.Identity.ConnectionID, ResourceKind: &kind, KnownNativeIDs: []string{a.id}, KnownNativeMetadata: map[string]map[string]any{a.id: request.Asset.Normalized}})
	if err != nil {
		return dataMigrationForest{}, err
	}
	var forest dataMigrationForest
	if dataMigrationClassic(a.kind.NativeType) {
		forest, err = a.client.dataMigrationClassicForest(ctx, hints)
	} else {
		forest, err = a.client.dataMigrationModernForest(ctx, hints)
	}
	if err != nil {
		return forest, err
	}
	metadata, err := providerData()
	if err != nil {
		return forest, err
	}
	items, _, err := (&Runtime{bundle: metadata.bundle}).dataMigrationItems(ctx, a.client, request.Asset.Identity.ConnectionID, forest)
	if err != nil {
		return forest, err
	}
	selected := items[a.id]
	if selected.NativeID == "" {
		return forest, serviceDenied("datamigration_action_disappeared_during_walk")
	}
	for _, key := range []string{dataMigrationConfiguration, "_datamigration_service", "_datamigration_parent", "_datamigration_ancestors", "_datamigration_groups", "_datamigration_target", "_datamigration_location", "_datamigration_references", "arm_parameters"} {
		if a.client.privateConfiguration(map[string]any{"value": selected.Normalized[key]}) != a.client.privateConfiguration(map[string]any{"value": request.Asset.Normalized[key]}) {
			return forest, serviceDenied("datamigration_action_context_changed")
		}
	}
	if selected.Normalized["cleanup_protected"] != false {
		return forest, serviceDenied(text(selected.Normalized["cleanup_protection_reason"]))
	}
	for _, key := range []string{dataMigrationMembers, "_datamigration_dependents"} {
		if len(object(selected.Normalized[key])) != 0 {
			return forest, serviceDenied("datamigration_prerequisite_still_exists")
		}
	}
	for name, entry := range object(selected.Normalized["_datamigration_nodes"]) {
		expected := object(request.Asset.Normalized["_datamigration_nodes"])[name]
		if expected == nil || a.client.privateConfiguration(object(entry)) != a.client.privateConfiguration(object(expected)) {
			return forest, serviceDenied("datamigration_registered_nodes_changed")
		}
	}
	// Native DMS deletes have no If-Match. Preparation and reviewed child
	// deletion may update an ETag, but all authored fields remain bound above.
	touched := request.ExecutionResult != nil && request.ExecutionResult.Data["touched"] == true
	for _, prior := range request.PrerequisiteDeletions {
		touched = touched || strings.HasPrefix(prior.Asset.Identity.NativeID, a.id+"/") || prior.Asset.Normalized["_datamigration_service"] == a.id
	}
	if !touched && selected.Normalized["arm_etag"] != request.Asset.Normalized["arm_etag"] {
		return forest, serviceDenied("datamigration_action_etag_changed")
	}
	return forest, nil
}

func (a *dataMigrationAction) readbackObserved(request contracts.ActionRequest, observed dataMigrationObservation) contracts.ReadbackResult {
	exists := observed.members[a.id].id != ""
	for _, key := range []string{dataMigrationMembers, "_datamigration_dependents"} {
		for id := range object(request.Asset.Normalized[key]) {
			exists = exists || observed.members[id].id != ""
		}
	}
	return contracts.ReadbackResult{Exists: exists, State: "datamigration_deleting"}
}

func (a *dataMigrationAction) preflight(ctx context.Context, request contracts.ActionRequest) (check contracts.PreflightResult, forest dataMigrationForest, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err = a.identity(request); err != nil {
		return
	}
	for range 2 {
		observed, readErr := a.observe(ctx, request)
		if readErr != nil {
			err = readErr
			return
		}
		selected := observed.members[a.id]
		if selected.id == "" || observed.missingParent || object(selected.raw["properties"])["provisioningState"] == "Deleting" {
			read := a.readbackObserved(request, observed)
			return contracts.PreflightResult{Allowed: true, Absent: !read.Exists, Evidence: map[string]any{"datamigration_wait": true}}, forest, nil
		}
		for _, key := range []string{dataMigrationMembers, "_datamigration_dependents"} {
			for id := range object(request.Asset.Normalized[key]) {
				if observed.members[id].id != "" {
					err = serviceDenied("datamigration_prerequisite_still_exists")
					return
				}
			}
		}
		forest, err = a.current(ctx, request)
		if err != nil {
			return
		}
	}
	return contracts.PreflightResult{Allowed: true}, forest, nil
}

func (a *dataMigrationAction) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	check, _, err := a.preflight(ctx, request)
	return check, err
}

func (a *dataMigrationAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	observed, err := a.observe(ctx, request)
	if err != nil {
		return contracts.ReadbackResult{}, contracts.DependencyReadError(err)
	}
	return a.readbackObserved(request, observed), nil
}

func dataMigrationTaskActive(state string) bool { return state == "Queued" || state == "Running" }

func (a *dataMigrationAction) next(forest dataMigrationForest) (phase, item string) {
	member := forest.members[a.id]
	switch member.kind {
	case dataMigrationTaskType, dataMigrationServiceTaskType:
		if dataMigrationTaskActive(member.state) {
			return "cancel", ""
		}
	case dataMigrationType:
		_, kind := dataMigrationTarget(a.id)
		if kind != "MongoToCosmosDbMongo" {
			if member.state == "Canceling" {
				return "wait", ""
			}
			if member.state == "InProgress" {
				return "cancel", ""
			}
		}
	case dataMigrationSQLServiceType:
		nodes := array(forest.nodes[a.id]["nodes"])
		for _, value := range nodes {
			jobs, err := batchInteger(object(value)["concurrentJobsRunning"], 32)
			if err != nil || jobs != 0 {
				return "wait", ""
			}
		}
		if len(nodes) != 0 {
			names := []string{}
			for _, value := range nodes {
				names = append(names, text(object(value)["nodeName"]))
			}
			slices.Sort(names)
			return "delete-node", names[0]
		}
	}
	return "delete", ""
}

func (a *dataMigrationAction) phaseResult(request contracts.ActionRequest, phase, item string, res response, operation map[string]any) contracts.ActionResult {
	data := map[string]any{"accepted": map[string]any{}, "touched": false}
	result := contracts.ActionResult{ProviderRequestID: res.requestID, ProviderOperationID: operationLocation(res.header), RetryAfter: retryAfter(res.header)}
	if request.ExecutionResult != nil {
		data = batchClone(request.ExecutionResult.Data)
		result.ProviderOperationID = request.ExecutionResult.ProviderOperationID
	}
	data["phase"], data["item"], data["operation"], data["operation_done"] = phase, item, operation, false
	if phase != "wait" {
		object(data["accepted"])[phase+"/"+item], data["touched"] = true, true
	}
	result.Data = data
	result.Data["binding"] = a.phaseBinding(request, result)
	return result
}

func (a *dataMigrationAction) mutate(ctx context.Context, request contracts.ActionRequest, forest dataMigrationForest, phase, item string) (contracts.ActionResult, error) {
	if phase == "wait" {
		return a.phaseResult(request, phase, item, response{}, nil), nil
	}
	if request.ExecutionResult != nil && object(request.ExecutionResult.Data["accepted"])[phase+"/"+item] == true {
		return contracts.ActionResult{}, serviceDenied("datamigration_preparation_reappeared")
	}
	op := a.deletion
	var err error
	switch phase {
	case "cancel":
		name, extra := "Tasks_Cancel", map[string]any{}
		if a.kind.NativeType == dataMigrationServiceTaskType {
			name = "ServiceTasks_Cancel"
		}
		if a.kind.NativeType == dataMigrationType {
			_, kind := dataMigrationTarget(a.id)
			name = "DatabaseMigrations" + kind + "_cancel"
			extra["parameters"] = map[string]any{"migrationOperationId": object(forest.members[a.id].raw["properties"])["migrationOperationId"]}
		}
		op, err = a.client.dataMigrationOperation(a.id, a.kind.NativeType, name, extra)
	case "delete-node":
		op, err = a.client.dataMigrationOperation(a.id, a.kind.NativeType, "SqlMigrationServices_deleteNode", map[string]any{"parameters": map[string]any{"nodeName": item, "integrationRuntimeName": forest.nodes[a.id]["name"]}})
	case "delete":
		if _, kind := dataMigrationTarget(a.id); a.kind.NativeType == dataMigrationType && kind == "MongoToCosmosDbMongo" && forest.members[a.id].state == "InProgress" {
			operation, params, bindErr := a.client.resourceOperation(a.kind, a.id, "DELETE")
			if bindErr != nil {
				return contracts.ActionResult{}, bindErr
			}
			params["force"] = true // Native Mongo has no separate cancel operation.
			op, err = bindAzureREST(operation, params)
		}
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	headers := maps.Clone(op.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	if request.IdempotencyKey != "" {
		headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey + ":" + phase + ":" + item)
	}
	res, err := a.client.requestBody(ctx, op.Method, op.URL, op.Body, headers)
	if isNotFound(err) && phase == "delete" {
		return a.phaseResult(request, phase, item, response{}, nil), nil
	}
	if err != nil {
		return contracts.ActionResult{}, contracts.DependencyReadError(err)
	}
	operation := map[string]any{}
	if phase == "delete-node" {
		if res.status != 200 || operationLocation(res.header) != "" || len(res.data) != 2 || res.data["nodeName"] != item || res.data["integrationRuntimeName"] != forest.nodes[a.id]["name"] {
			return contracts.ActionResult{}, serviceDenied("invalid_datamigration_delete_node_receipt")
		}
	} else {
		operation, err = a.client.dataMigrationReceipt(a.id, a.kind.NativeType, phase, res)
		if err != nil {
			return contracts.ActionResult{}, err
		}
	}
	return a.phaseResult(request, phase, item, res, operation), nil
}

func (a *dataMigrationAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.ActionResult{}, err
	}
	if request.ExecutionResult != nil {
		return *request.ExecutionResult, nil
	}
	check, forest, err := a.preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !check.Allowed {
		return contracts.ActionResult{}, serviceDenied(check.Reason)
	}
	if check.Absent || check.Evidence["datamigration_wait"] == true {
		return a.phaseResult(request, "delete", "", response{}, nil), nil
	}
	phase, item := a.next(forest)
	return a.mutate(ctx, request, forest, phase, item)
}

func (a *dataMigrationAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	request.ExecutionResult = &result
	if err := a.identity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	out := contracts.WaitResult{RetryAfter: 2 * time.Second, State: text(result.Data["phase"]), Data: batchClone(result.Data)}
	if slices.Contains([]string{"cancel", "delete"}, text(result.Data["phase"])) && result.Data["operation_done"] != true {
		poll, err := a.client.dataMigrationPoll(ctx, a.id, a.kind.NativeType, text(result.Data["phase"]), object(result.Data["operation"]))
		if err != nil {
			return out, contracts.DependencyReadError(err)
		}
		out.Data["operation"], out.Data["operation_done"] = poll.Data, poll.Done
		result.Data = out.Data
		out.Data["binding"] = a.phaseBinding(request, result)
		if poll.RetryAfter > 0 {
			out.RetryAfter = poll.RetryAfter
		}
		if !poll.Done {
			return out, nil
		}
	}
	if result.Data["phase"] == "delete" {
		read, err := a.Readback(ctx, request)
		out.Done, out.State = err == nil && !read.Exists, read.State
		return out, err
	}
	check, forest, err := a.preflight(ctx, request)
	if err != nil {
		return out, err
	}
	if check.Absent {
		out.Done = true
		return out, nil
	}
	if check.Evidence["datamigration_wait"] == true {
		return out, nil
	}
	if result.Data["phase"] == "cancel" {
		state := forest.members[a.id].state
		if dataMigrationTaskActive(state) || state == "InProgress" || state == "Canceling" {
			return out, nil
		}
	}
	if result.Data["phase"] == "delete-node" {
		for _, node := range array(forest.nodes[a.id]["nodes"]) {
			if object(node)["nodeName"] == result.Data["item"] {
				return out, nil
			}
		}
	}
	phase, item := a.next(forest)
	next, err := a.mutate(ctx, request, forest, phase, item)
	if err != nil {
		return out, err
	}
	out.Data, out.State = next.Data, text(next.Data["phase"])
	if next.RetryAfter > 0 {
		out.RetryAfter = next.RetryAfter
	}
	return out, nil
}

var _ contracts.ActionDriver = (*dataMigrationAction)(nil)
