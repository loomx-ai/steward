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

type dataFactoryAction struct {
	client   *client
	planned  asset.Asset
	kind     resourceType
	id, root string
	deletion catalog.RESTRequest
}

func newDataFactoryAction(c *client, connection asset.ConnectionID, value asset.Asset, kind resourceType) (*dataFactoryAction, error) {
	if err := c.dataFactoryAsset(value); err != nil {
		return nil, err
	}
	if value.Identity.ConnectionID != connection || kind.NativeType != value.Identity.NativeType || kind.ReadOnly {
		return nil, serviceDenied("datafactory_action_connection_changed")
	}
	wire, err := dataFactoryWireID(value.Identity.NativeID, kind.NativeType, text(value.Normalized["_datafactory_node_name"]))
	if err != nil {
		return nil, err
	}
	op, parameters, err := c.resourceOperation(kind, wire, "DELETE")
	if err != nil {
		return nil, err
	}
	deletion, err := bindAzureREST(op, parameters)
	if err != nil {
		return nil, err
	}
	return &dataFactoryAction{client: c, planned: value, kind: kind, id: value.Identity.NativeID, root: dataFactoryRoot(value.Identity.NativeID), deletion: deletion}, nil
}

func (a *dataFactoryAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }

// Compute the same controller closure as the reviewed lifecycle graph. A
// direct child's descendants belong to that child's separate execution step.
func (a *dataFactoryAction) reviewMembers(request contracts.ActionRequest) (map[string]any, map[string]any) {
	members := object(request.Asset.Normalized[dataFactoryMembers])
	covered := map[string]bool{a.id: true}
	impacts, prerequisites := map[string]any{}, map[string]any{}
	for changed := true; changed; {
		changed = false
		for _, id := range slices.Sorted(maps.Keys(members)) {
			entry := object(members[id])
			if covered[id] || !covered[text(entry["controller"])] {
				continue
			}
			if entry["direct"] == true {
				prerequisites[id] = entry
				continue
			}
			impacts[id], covered[id], changed = entry, true, true
		}
	}
	for id := range covered {
		for source, entry := range object(object(request.Asset.Normalized["_datafactory_incoming"])[id]) {
			if !covered[source] {
				prerequisites[source] = entry
			}
		}
	}
	return impacts, prerequisites
}

func (a *dataFactoryAction) identity(request contracts.ActionRequest) error {
	value := request.Asset
	if request.Action != "delete" || value.ID != a.planned.ID || value.Identity != a.planned.Identity || value.Normalized[dataFactoryProof] != a.planned.Normalized[dataFactoryProof] || len(request.Parameters) != 0 {
		return serviceDenied("datafactory_action_request_changed")
	}
	if err := a.client.dataFactoryAsset(value); err != nil {
		return err
	}
	impacts, prerequisites := a.reviewMembers(request)
	assets := map[string]asset.Asset{a.id: value}
	seen := map[asset.AssetID]bool{value.ID: true}
	for _, part := range []struct {
		actual   []contracts.ActionImpact
		expected map[string]any
		direct   bool
	}{{request.LifecycleImpacts, impacts, false}, {request.PrerequisiteDeletions, prerequisites, true}} {
		if len(part.actual) != len(part.expected) {
			return serviceDenied("datafactory_review_members_changed")
		}
		for _, impact := range part.actual {
			child := impact.Asset
			id := child.Identity.NativeID
			entry := object(part.expected[id])
			if !impact.Delete || seen[child.ID] || assets[id].ID != "" || entry == nil || entry["kind"] != child.Identity.NativeType || entry["configuration"] != child.Normalized[dataFactoryConfiguration] || child.Identity.ConnectionID != value.Identity.ConnectionID || child.Identity.Partition != value.Identity.Partition || part.direct && impact.ControllerID != value.ID {
				return serviceDenied("invalid_datafactory_review_member")
			}
			if err := a.client.dataFactoryAsset(child); err != nil {
				return err
			}
			if !part.direct && (child.Normalized["_datafactory_root"] != a.root || child.Normalized["_datafactory_group"] != value.Normalized["_datafactory_group"] || child.Location != value.Location || object(child.Normalized["_datafactory_ancestors"])[a.id] != value.Normalized[dataFactoryConfiguration]) {
				return serviceDenied("datafactory_review_member_scope_changed")
			}
			seen[child.ID], assets[id] = true, child
		}
	}
	for _, impact := range request.LifecycleImpacts {
		controller := text(object(impacts[impact.Asset.Identity.NativeID])["controller"])
		if assets[controller].ID == "" || impact.ControllerID != assets[controller].ID {
			return serviceDenied("datafactory_review_controller_changed")
		}
	}
	if request.ExecutionResult != nil {
		return a.verifyPhase(request, *request.ExecutionResult)
	}
	return nil
}

func (a *dataFactoryAction) phaseBinding(request contracts.ActionRequest, result contracts.ActionResult) string {
	request.IdempotencyKey, request.ExecutionResult = "", nil
	data := maps.Clone(result.Data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "datafactory-cleanup-1", "request": request, "origin": result.ProviderOperationID, "data": data})
}

func (a *dataFactoryAction) verifyPhase(request contracts.ActionRequest, result contracts.ActionResult) error {
	if !slices.Contains([]string{"wait", "delete", "stop-runtime", "stop-trigger", "stop-cdc", "unsubscribe", "cancel-run", "delete-debug", "remove-links"}, text(result.Data["phase"])) || result.Data["binding"] != a.phaseBinding(request, result) {
		return serviceDenied("datafactory_cleanup_receipt_changed")
	}
	return nil
}

type dataFactoryObservation struct {
	members       map[string]dataFactoryMember
	missingParent bool
}

func (a *dataFactoryAction) readMember(ctx context.Context, id, kind, nodeName string) (dataFactoryMember, error) {
	raw, err := a.client.dataFactoryRead(ctx, id, kind, nodeName)
	if err != nil {
		return dataFactoryMember{}, err
	}
	member, err := a.client.dataFactoryObserved(ctx, dataFactoryMember{id: id, kind: kind, parent: dataFactoryParent(id, kind), root: dataFactoryRoot(id), nodeName: nodeName, raw: raw})
	return member, contracts.DependencyReadError(err)
}

func (a *dataFactoryAction) configuration(member dataFactoryMember, expected any) error {
	snapshot, err := dataFactoryMemberSnapshot(member)
	if err != nil {
		return err
	}
	if expected != a.client.privateConfiguration(snapshot) {
		return serviceDenied("datafactory_action_configuration_changed")
	}
	return nil
}

// Named reads remain necessary after a controller disappears. Collection
// omission, a missing ancestor, and GetStatus 404 never prove child absence.
func (a *dataFactoryAction) observe(ctx context.Context, request contracts.ActionRequest) (dataFactoryObservation, error) {
	out := dataFactoryObservation{members: map[string]dataFactoryMember{}}
	expected := maps.Clone(object(request.Asset.Normalized[dataFactoryMembers]))
	for id, configuration := range object(request.Asset.Normalized["_datafactory_ancestors"]) {
		_, typ, _ := parseID(id)
		expected[id] = map[string]any{"kind": dataFactoryKind(typ), "configuration": configuration}
	}
	for _, prerequisite := range request.PrerequisiteDeletions {
		value := prerequisite.Asset
		expected[value.Identity.NativeID] = map[string]any{"kind": value.Identity.NativeType, "nodeName": value.Normalized["_datafactory_node_name"], "configuration": value.Normalized[dataFactoryConfiguration]}
	}
	expected[a.id] = map[string]any{"kind": a.kind.NativeType, "nodeName": request.Asset.Normalized["_datafactory_node_name"], "configuration": request.Asset.Normalized[dataFactoryConfiguration]}
	for _, id := range slices.Sorted(maps.Keys(expected)) {
		entry := object(expected[id])
		member, err := a.readMember(ctx, id, text(entry["kind"]), text(entry["nodeName"]))
		if isNotFound(err) {
			if object(request.Asset.Normalized["_datafactory_ancestors"])[id] != nil {
				out.missingParent = true
			}
			continue
		}
		if err != nil {
			return out, err
		}
		if err := a.configuration(member, entry["configuration"]); err != nil {
			return out, err
		}
		out.members[id] = member
	}
	return out, nil
}

func (a *dataFactoryAction) prerequisites(ctx context.Context, request contracts.ActionRequest, observed dataFactoryObservation) error {
	impacts, _ := a.reviewMembers(request)
	for id := range object(request.Asset.Normalized[dataFactoryMembers]) {
		if impacts[id] == nil && observed.members[id].id != "" {
			return serviceDenied("datafactory_direct_child_still_exists")
		}
	}
	for _, prerequisite := range request.PrerequisiteDeletions {
		value := prerequisite.Asset
		_, err := a.client.dataFactoryRead(ctx, value.Identity.NativeID, value.Identity.NativeType, text(value.Normalized["_datafactory_node_name"]))
		if !isNotFound(err) {
			if err == nil {
				err = serviceDenied("datafactory_consumer_still_exists")
			}
			return err
		}
	}
	return nil
}

func (a *dataFactoryAction) current(ctx context.Context, request contracts.ActionRequest) (dataFactoryTree, error) {
	kind := asset.ResourceKind{NativeType: a.kind.NativeType}
	metadata := map[string]map[string]any{a.id: request.Asset.Normalized}
	hints, err := a.client.dataFactoryKnown(contracts.InventoryRequest{ConnectionID: request.Asset.Identity.ConnectionID, ResourceKind: &kind, KnownNativeIDs: []string{a.id}, KnownNativeMetadata: metadata})
	if err != nil {
		return dataFactoryTree{}, err
	}
	trees, _, err := a.client.dataFactoryContext(ctx, hints, metadata)
	if err != nil {
		return dataFactoryTree{}, err
	}
	data, err := providerData()
	if err != nil {
		return dataFactoryTree{}, err
	}
	items, _, err := (&Runtime{bundle: data.bundle}).dataFactoryItems(ctx, a.client, request.Asset.Identity.ConnectionID, trees)
	if err != nil {
		return dataFactoryTree{}, err
	}
	tree := trees[a.root]
	selected := items[a.id]
	if selected.NativeID == "" {
		return tree, serviceDenied("datafactory_action_disappeared_during_walk")
	}
	for _, key := range []string{dataFactoryConfiguration, "_datafactory_group", "_datafactory_location", "_datafactory_ancestors"} {
		if a.client.privateConfiguration(map[string]any{"value": selected.Normalized[key]}) != a.client.privateConfiguration(map[string]any{"value": request.Asset.Normalized[key]}) {
			return tree, serviceDenied("datafactory_action_context_changed")
		}
	}
	impacts, _ := a.reviewMembers(request)
	for id, current := range object(selected.Normalized[dataFactoryMembers]) {
		expected := object(object(request.Asset.Normalized[dataFactoryMembers])[id])
		if impacts[id] == nil {
			return tree, serviceDenied("datafactory_direct_child_reappeared")
		}
		if expected == nil || a.client.privateConfiguration(object(current)) != a.client.privateConfiguration(expected) {
			return tree, serviceDenied("datafactory_action_membership_changed")
		}
	}
	covered := map[string]asset.Asset{a.id: request.Asset}
	for _, impact := range request.LifecycleImpacts {
		covered[impact.Asset.Identity.NativeID] = impact.Asset
	}
	for id, planned := range covered {
		item := items[id]
		if item.NativeID == "" {
			continue
		}
		if item.Normalized["cleanup_protected"] != false {
			return tree, serviceDenied(text(item.Normalized["cleanup_protection_reason"]))
		}
		if item.Normalized[dataFactoryConfiguration] != planned.Normalized[dataFactoryConfiguration] {
			return tree, serviceDenied("datafactory_managed_configuration_changed")
		}
		// Native DELETE has no If-Match contract. Check the observed stamp
		// before preparation; accepted operations may update ancestor stamps.
		touched := map[string]any{}
		if request.ExecutionResult != nil {
			touched = object(request.ExecutionResult.Data["touched"])
		}
		stampMayChange := touched[id] == true
		for _, prior := range request.PrerequisiteDeletions {
			if strings.HasPrefix(prior.Asset.Identity.NativeID, id+"/") {
				stampMayChange = true
			}
		}
		if !stampMayChange && item.Normalized["arm_etag"] != planned.Normalized["arm_etag"] {
			return tree, serviceDenied("datafactory_action_etag_changed")
		}
		for source, entry := range object(tree.incoming[id]) {
			expected := object(object(request.Asset.Normalized["_datafactory_incoming"])[id])[source]
			if expected == nil || a.client.privateConfiguration(object(entry)) != a.client.privateConfiguration(object(expected)) {
				return tree, serviceDenied("datafactory_action_consumers_changed")
			}
			if covered[source].ID == "" {
				return tree, serviceDenied("datafactory_consumer_still_exists")
			}
		}
	}
	if !tree.work.verified {
		return tree, serviceDenied("datafactory_action_work_unverified")
	}
	work := a.client.dataFactoryWorkManifest(tree.work)
	for _, part := range []string{"runs", "debug"} {
		for id, entry := range object(work[part]) {
			expected := object(object(request.Asset.Normalized["_datafactory_work"])[part])[id]
			if expected == nil || a.client.privateConfiguration(object(entry)) != a.client.privateConfiguration(object(expected)) {
				return tree, serviceDenied("datafactory_action_new_work")
			}
		}
	}
	for owner, rows := range tree.links {
		if covered[owner].ID == "" {
			continue
		}
		for key, value := range object(rows) {
			entry := object(value)
			expected := object(object(object(request.Asset.Normalized["_datafactory_links"])[owner])[key])
			if expected == nil || entry["configuration"] != expected["configuration"] || entry["source"] != expected["source"] || entry["unresolved"] == true || entry["absent"] != true {
				return tree, serviceDenied("datafactory_action_sharing_changed")
			}
		}
	}
	return tree, nil
}

func (a *dataFactoryAction) preflight(ctx context.Context, request contracts.ActionRequest) (check contracts.PreflightResult, tree dataFactoryTree, err error) {
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
		if selected.id == "" || observed.missingParent || selected.kind == dataFactoryType && selected.state == "Deleting" {
			read, readErr := a.readbackObserved(request, observed)
			return contracts.PreflightResult{Allowed: readErr == nil, Absent: readErr == nil && !read.Exists, Evidence: map[string]any{"datafactory_wait": true}}, tree, readErr
		}
		if err = a.prerequisites(ctx, request, observed); err != nil {
			return
		}
		tree, err = a.current(ctx, request)
		if err != nil {
			return
		}
	}
	return contracts.PreflightResult{Allowed: true}, tree, nil
}

func (a *dataFactoryAction) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	check, _, err := a.preflight(ctx, request)
	return check, err
}

func (a *dataFactoryAction) readbackObserved(request contracts.ActionRequest, observed dataFactoryObservation) (contracts.ReadbackResult, error) {
	exists := observed.members[a.id].id != ""
	for id := range object(request.Asset.Normalized[dataFactoryMembers]) {
		exists = exists || observed.members[id].id != ""
	}
	for _, prerequisite := range request.PrerequisiteDeletions {
		exists = exists || observed.members[prerequisite.Asset.Identity.NativeID].id != ""
	}
	return contracts.ReadbackResult{Exists: exists, State: "datafactory_deleting"}, nil
}

func (a *dataFactoryAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	observed, err := a.observe(ctx, request)
	if err != nil {
		return contracts.ReadbackResult{}, contracts.DependencyReadError(err)
	}
	return a.readbackObserved(request, observed)
}

type dataFactoryPreparation struct{ phase, target, item string }

func (a *dataFactoryAction) next(tree dataFactoryTree) dataFactoryPreparation {
	member := tree.members[a.id]
	props := object(member.raw["properties"])
	step := dataFactoryPreparation{phase: "delete", target: a.id}
	switch member.kind {
	case dataFactoryTriggerType:
		if member.state == "Started" {
			step.phase = "stop-trigger"
			return step
		}
		if member.eventState == "Provisioning" || member.eventState == "Deprovisioning" {
			step.phase = "wait"
			return step
		}
		if member.eventState == "Enabled" {
			step.phase = "unsubscribe"
			return step
		}
		return step
	case dataFactoryCDCType:
		if member.state == "Running" {
			step.phase = "stop-cdc"
		}
		return step
	case dataFactoryIRType:
		if props["type"] == "Managed" && object(object(props["typeProperties"])["ssisProperties"]) != nil {
			if member.state == "Starting" || member.state == "Stopping" {
				step.phase = "wait"
				return step
			}
			if member.state == "Started" {
				step.phase = "stop-runtime"
				return step
			}
		}
	}
	for _, id := range slices.Sorted(maps.Keys(tree.work.runs)) {
		run := tree.work.runs[id]
		if member.kind == dataFactoryPipelineType && !strings.EqualFold(text(run["pipelineName"]), last(a.id)) {
			continue
		}
		step.phase = "wait"
		if (member.kind == dataFactoryType || member.kind == dataFactoryPipelineType) && run["status"] != "Canceling" {
			step.phase, step.target, step.item = "cancel-run", a.root, id
		}
		return step
	}
	if len(tree.work.debug) != 0 && member.kind != dataFactoryPipelineType {
		step.phase = "wait"
		if member.kind == dataFactoryType {
			step.phase, step.target, step.item = "delete-debug", a.root, slices.Sorted(maps.Keys(tree.work.debug))[0]
		}
		return step
	}
	for _, owner := range slices.Sorted(maps.Keys(tree.links)) {
		if member.kind != dataFactoryType && owner != a.id {
			continue
		}
		if len(object(tree.links[owner])) != 0 {
			row := object(object(tree.links[owner])[slices.Sorted(maps.Keys(object(tree.links[owner])))[0]])
			return dataFactoryPreparation{phase: "remove-links", target: owner, item: text(row["factoryName"])}
		}
	}
	return step
}

func (a *dataFactoryAction) phaseResult(request contracts.ActionRequest, step dataFactoryPreparation, res response, operation map[string]any) contracts.ActionResult {
	data := map[string]any{"touched": map[string]any{}, "accepted": map[string]any{}}
	result := contracts.ActionResult{ProviderRequestID: res.header.Get("x-ms-request-id"), ProviderOperationID: operationLocation(res.header), RetryAfter: retryAfter(res.header)}
	if request.ExecutionResult != nil {
		data = batchClone(request.ExecutionResult.Data)
		result.ProviderOperationID = request.ExecutionResult.ProviderOperationID
	}
	data["phase"], data["target"], data["item"], data["operation"] = step.phase, step.target, step.item, operation
	// The worker merges phase data on recovery, so clear fields explicitly.
	data["operation_done"] = false
	if step.phase != "wait" {
		object(data["accepted"])[step.phase+"/"+step.target+"/"+step.item] = true
		for id := step.target; id != ""; {
			object(data["touched"])[id] = true
			_, typ, _ := parseID(id)
			id = dataFactoryParent(id, dataFactoryKind(typ))
		}
	}
	result.Data = data
	result.Data["binding"] = a.phaseBinding(request, result)
	return result
}

func (a *dataFactoryAction) mutate(ctx context.Context, request contracts.ActionRequest, step dataFactoryPreparation) (contracts.ActionResult, error) {
	if step.phase == "wait" {
		return a.phaseResult(request, step, response{}, nil), nil
	}
	if request.ExecutionResult != nil && object(request.ExecutionResult.Data["accepted"])[step.phase+"/"+step.target+"/"+step.item] == true {
		return contracts.ActionResult{}, serviceDenied("datafactory_preparation_reappeared")
	}
	op := a.deletion
	var err error
	operationName, kind := "", a.kind.NativeType
	extra := map[string]any{}
	switch step.phase {
	case "stop-runtime":
		operationName, kind = "IntegrationRuntimes_Stop", dataFactoryIRType
	case "stop-trigger":
		operationName = "Triggers_Stop"
	case "stop-cdc":
		operationName = "ChangeDataCapture_Stop"
	case "unsubscribe":
		operationName = "Triggers_UnsubscribeFromEvents"
	case "cancel-run":
		operationName, kind = "PipelineRuns_Cancel", dataFactoryType
		extra = map[string]any{"runId": step.item, "isRecursive": false}
	case "delete-debug":
		operationName, kind = "DataFlowDebugSession_Delete", dataFactoryType
		extra = map[string]any{"request": map[string]any{"sessionId": step.item}}
	case "remove-links":
		operationName, kind = "IntegrationRuntimes_RemoveLinks", dataFactoryIRType
		extra = map[string]any{"linkedIntegrationRuntimeRequest": map[string]any{"factoryName": step.item}}
	}
	if operationName != "" {
		op, err = a.client.dataFactoryOperation(step.target, kind, operationName, extra)
		if err != nil {
			return contracts.ActionResult{}, err
		}
	}
	headers := maps.Clone(op.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	if request.IdempotencyKey != "" {
		headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey + ":" + step.phase + ":" + step.target + ":" + step.item)
	}
	res, err := a.client.requestBody(ctx, op.Method, op.URL, op.Body, headers)
	if isNotFound(err) && step.phase == "delete" {
		return a.phaseResult(request, step, response{}, nil), nil
	}
	if err != nil {
		return contracts.ActionResult{}, contracts.DependencyReadError(err)
	}
	operation := map[string]any{}
	switch step.phase {
	case "stop-runtime":
		operation, err = a.client.dataFactoryStopReceipt(step.target, res)
	case "unsubscribe":
		_, err = a.client.dataFactoryUnsubscribeReceipt(step.target, res)
	default:
		err = dataFactorySynchronousReceipt(res, step.phase == "delete")
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return a.phaseResult(request, step, res, operation), nil
}

func (a *dataFactoryAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.ActionResult{}, err
	}
	if request.ExecutionResult != nil {
		return *request.ExecutionResult, nil
	}
	check, tree, err := a.preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !check.Allowed {
		return contracts.ActionResult{}, serviceDenied(check.Reason)
	}
	if check.Absent || check.Evidence["datafactory_wait"] == true {
		return a.phaseResult(request, dataFactoryPreparation{phase: "delete", target: a.id}, response{}, nil), nil
	}
	return a.mutate(ctx, request, a.next(tree))
}

func (a *dataFactoryAction) phaseDone(tree dataFactoryTree, result contracts.ActionResult) bool {
	id, item := text(result.Data["target"]), text(result.Data["item"])
	member := tree.members[id]
	if member.id == "" {
		return true
	}
	switch result.Data["phase"] {
	case "stop-runtime":
		return member.state == "Stopped"
	case "stop-trigger":
		return member.state == "Stopped" || member.state == "Disabled"
	case "stop-cdc":
		return member.state == "Stopped"
	case "unsubscribe":
		return member.eventState == "Disabled"
	case "cancel-run":
		return tree.work.runs[item] == nil
	case "delete-debug":
		return tree.work.debug[item] == nil
	case "remove-links":
		for _, value := range object(tree.links[id]) {
			if strings.EqualFold(text(object(value)["factoryName"]), item) {
				return false
			}
		}
	}
	return true
}

func (a *dataFactoryAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	request.ExecutionResult = &result
	if err := a.identity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	out := contracts.WaitResult{RetryAfter: 2 * time.Second, State: text(result.Data["phase"]), Data: batchClone(result.Data)}
	if result.Data["phase"] == "delete" {
		read, err := a.Readback(ctx, request)
		out.Done, out.State = err == nil && !read.Exists, read.State
		return out, err
	}
	if result.Data["phase"] == "stop-runtime" && result.Data["operation_done"] != true {
		poll, err := a.client.dataFactoryPollStop(ctx, text(result.Data["target"]), object(result.Data["operation"]))
		if err != nil {
			return out, contracts.DependencyReadError(err)
		}
		out.Data["operation"] = poll.Data
		if poll.Done {
			out.Data["operation_done"] = true
		}
		updated := result
		updated.Data = out.Data
		out.Data["binding"] = a.phaseBinding(request, updated)
		result.Data = out.Data
		if poll.RetryAfter > 0 {
			out.RetryAfter = poll.RetryAfter
		}
		if !poll.Done {
			return out, nil
		}
	}
	check, tree, err := a.preflight(ctx, request)
	if err != nil {
		return out, err
	}
	if check.Absent {
		out.Done = true
		return out, nil
	}
	if check.Evidence["datafactory_wait"] == true || !a.phaseDone(tree, result) {
		return out, nil
	}
	next, err := a.mutate(ctx, request, a.next(tree))
	if err != nil {
		return out, err
	}
	out.Data, out.State = next.Data, text(next.Data["phase"])
	if next.RetryAfter > 0 {
		out.RetryAfter = next.RetryAfter
	}
	return out, nil
}

var _ contracts.ActionDriver = (*dataFactoryAction)(nil)
