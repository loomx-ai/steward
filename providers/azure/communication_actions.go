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

type communicationAction struct {
	client                    *client
	kind                      resourceType
	id, root, endpoint, proof string
	assetID                   asset.AssetID
	identity                  asset.Identity
	deletion                  catalog.RESTRequest
}

func newCommunicationAction(c *client, connection asset.ConnectionID, value asset.Asset, kind resourceType) (*communicationAction, error) {
	if err := c.communicationAsset(value); err != nil {
		return nil, err
	}
	if value.Identity.ConnectionID != connection || kind.NativeType != value.Identity.NativeType {
		return nil, serviceDenied("communication_action_connection_changed")
	}
	metadata := asset.ResourceKind{NativeType: kind.NativeType}
	if _, _, err := c.communicationKnown(contracts.InventoryRequest{ConnectionID: connection, ResourceKind: &metadata, KnownNativeIDs: []string{value.Identity.NativeID}, KnownNativeMetadata: map[string]map[string]any{value.Identity.NativeID: value.Normalized}}); err != nil {
		return nil, err
	}
	op, parameters, err := c.resourceOperation(kind, value.Identity.NativeID, "DELETE")
	if err != nil {
		return nil, err
	}
	deletion, err := bindAzureREST(op, parameters)
	if err != nil {
		return nil, err
	}
	return &communicationAction{client: c, kind: kind, id: value.Identity.NativeID, root: text(value.Normalized["_communication_root"]), endpoint: text(value.Normalized["_communication_endpoint"]), proof: text(value.Normalized[communicationProof]), assetID: value.ID, identity: value.Identity, deletion: deletion}, nil
}

func (a *communicationAction) DeletionCheckTimeout() time.Duration {
	if a.kind.NativeType == communicationType || a.kind.NativeType == communicationPhoneType {
		// Released numbers can remain visible through the billing cycle. This
		// bounds verification; it does not assert when billing or retention ends.
		return 40 * 24 * time.Hour
	}
	return time.Hour
}

func (a *communicationAction) requestIdentity(request contracts.ActionRequest) error {
	value := request.Asset
	if request.Action != "delete" || value.ID != a.assetID || value.Identity != a.identity || value.Normalized[communicationProof] != a.proof || len(request.Parameters) != 0 {
		return serviceDenied("communication_action_request_changed")
	}
	if err := a.client.communicationAsset(value); err != nil {
		return err
	}
	members := object(value.Normalized[communicationMembers])
	impacts, prerequisites := map[string]any{}, map[string]any{}
	for id, raw := range members {
		entry := object(raw)
		if entry["parent"] != a.id {
			continue
		}
		if entry["kind"] == communicationPhoneType {
			impacts[id] = entry
		} else {
			prerequisites[id] = entry
		}
	}
	if a.kind.NativeType == communicationDomainType {
		for id, configuration := range object(object(value.Normalized["_communication_incoming"])[a.id]) {
			prerequisites[id] = map[string]any{"kind": communicationType, "configuration": configuration, "shared": true}
		}
	}
	seenIDs, seenAssets := map[string]bool{a.id: true}, map[asset.AssetID]bool{a.assetID: true}
	verify := func(actual []contracts.ActionImpact, expected map[string]any) error {
		if len(actual) != len(expected) {
			return serviceDenied("communication_review_members_changed")
		}
		for _, impact := range actual {
			member := impact.Asset
			id := member.Identity.NativeID
			entry := object(expected[id])
			if !impact.Delete || impact.ControllerID != a.assetID || seenIDs[id] || seenAssets[member.ID] || entry == nil || entry["kind"] != member.Identity.NativeType || entry["configuration"] != member.Normalized[communicationConfiguration] || member.Identity.ConnectionID != a.identity.ConnectionID || member.Identity.Partition != a.identity.Partition {
				return serviceDenied("invalid_communication_review_member")
			}
			if err := a.client.communicationAsset(member); err != nil {
				return err
			}
			if entry["shared"] != true && (member.Normalized["_communication_root"] != a.root || member.Normalized["_communication_parent"] != a.id || member.Normalized["_communication_endpoint"] != a.endpoint || member.Normalized["_communication_group"] != value.Normalized["_communication_group"] || object(member.Normalized["_communication_ancestors"])[a.id] != value.Normalized[communicationConfiguration]) {
				return serviceDenied("communication_review_parent_changed")
			}
			seenIDs[id], seenAssets[member.ID] = true, true
		}
		return nil
	}
	if err := verify(request.LifecycleImpacts, impacts); err != nil {
		return err
	}
	if err := verify(request.PrerequisiteDeletions, prerequisites); err != nil {
		return err
	}
	if request.ExecutionResult != nil {
		return a.verifyReceipt(request, *request.ExecutionResult)
	}
	return nil
}

type communicationObservation struct {
	account        communicationAccountContext
	root, selected map[string]any
	ancestors      map[string]map[string]any
	missingParent  bool
}

func (a *communicationAction) read(ctx context.Context, account communicationAccountContext, id, kind string) (map[string]any, error) {
	if isCommunicationDataType(kind) {
		return a.client.communicationObservedData(ctx, account, id, kind)
	}
	return a.client.communicationARMRead(ctx, id, kind)
}

// A recorded endpoint permits only named reads after ARM account deletion.
// Every mutation still requires the current account and all recorded ancestors.
func (a *communicationAction) observe(ctx context.Context, request contracts.ActionRequest) (communicationObservation, error) {
	state := communicationObservation{ancestors: map[string]map[string]any{}}
	rootKind := communicationRootKind(a.kind.NativeType)
	root, err := a.client.communicationARMRead(ctx, a.root, rootKind)
	if err != nil && !isNotFound(err) {
		return state, err
	}
	state.root = root
	plannedAncestors := object(request.Asset.Normalized["_communication_ancestors"])
	rootConfiguration := plannedAncestors[a.root]
	if a.id == a.root {
		rootConfiguration = request.Asset.Normalized[communicationConfiguration]
	}
	if root != nil && rootConfiguration != a.client.privateConfiguration(communicationSnapshot(rootKind, root)) {
		return state, serviceDenied("communication_action_root_changed")
	}
	if rootKind == communicationType {
		state.account = communicationAccountContext{id: a.root, endpoint: a.endpoint, raw: root}
		if root == nil {
			state.account.historical = true
			state.account.raw = map[string]any{"id": a.root, "properties": map[string]any{"hostName": strings.TrimPrefix(a.endpoint, "https://")}}
		} else if endpoint, err := communicationAccountEndpoint(root); err != nil || endpoint != a.endpoint {
			return state, serviceDenied("communication_action_account_endpoint_changed")
		}
	}
	for _, id := range slices.Sorted(maps.Keys(plannedAncestors)) {
		_, typ, _ := parseID(id)
		kind := communicationKind(typ)
		current := root
		if id != a.root {
			current, err = a.client.communicationARMRead(ctx, id, kind)
			if err != nil && !isNotFound(err) {
				return state, err
			}
		}
		if current == nil {
			state.missingParent = true
			continue
		}
		if plannedAncestors[id] != a.client.privateConfiguration(communicationSnapshot(kind, current)) {
			return state, serviceDenied("communication_action_ancestor_changed")
		}
		state.ancestors[id] = current
	}
	state.selected = root
	if a.id != a.root {
		state.selected, err = a.read(ctx, state.account, a.id, a.kind.NativeType)
		if err != nil && !isNotFound(err) {
			return state, err
		}
	}
	if state.selected != nil && request.Asset.Normalized[communicationConfiguration] != a.client.privateConfiguration(communicationSnapshot(a.kind.NativeType, state.selected)) {
		return state, serviceDenied("communication_action_configuration_changed")
	}
	return state, nil
}

func (a *communicationAction) residuals(ctx context.Context, request contracts.ActionRequest, observation communicationObservation, beforeDelete bool) (bool, error) {
	exists := false
	for _, id := range slices.Sorted(maps.Keys(object(request.Asset.Normalized[communicationMembers]))) {
		entry := object(object(request.Asset.Normalized[communicationMembers])[id])
		kind := text(entry["kind"])
		raw, err := a.read(ctx, observation.account, id, kind)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return false, err
		}
		if entry["configuration"] != a.client.privateConfiguration(communicationSnapshot(kind, raw)) {
			return false, serviceDenied("communication_residual_configuration_changed")
		}
		if beforeDelete && kind != communicationPhoneType {
			return false, serviceDenied("communication_child_requires_prior_deletion")
		}
		if beforeDelete && communicationProtection(kind, raw) != "" {
			return false, serviceDenied("communication_managed_child_protected")
		}
		exists = true
	}
	return exists, nil
}

func (a *communicationAction) protection(ctx context.Context, request contracts.ActionRequest, observed communicationObservation) error {
	groupID := strings.Join(strings.Split(a.root, "/")[:5], "/")
	group, err := a.client.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return err
	}
	if !insightsARMReadValid(group, groupID, groupType) || request.Asset.Normalized["_communication_group"] != a.client.privateConfiguration(insightsWorkspaceResourceSnapshot(group.data)) {
		return serviceDenied("communication_action_group_changed")
	}
	if text(group.data["managedBy"]) != "" {
		return serviceDenied("azure_managed_resource_group")
	}
	if protectedAzureTags(object(group.data["tags"])) {
		return serviceDenied("azure_protected_tag")
	}
	if reason := communicationProtection(a.kind.NativeType, observed.selected); reason != "" {
		return serviceDenied(reason)
	}
	for id, raw := range observed.ancestors {
		_, typ, _ := parseID(id)
		if reason := communicationProtection(communicationKind(typ), raw); reason != "" {
			return serviceDenied(reason)
		}
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return err
	}
	id := a.id
	if isCommunicationDataType(a.kind.NativeType) {
		id = a.root
	}
	if locked(id, locks) {
		return serviceDenied("azure_management_lock")
	}
	return nil
}

func (a *communicationAction) currentTree(ctx context.Context, request contracts.ActionRequest, observation communicationObservation, beforeDelete bool) (communicationTree, error) {
	kind := asset.ResourceKind{NativeType: a.kind.NativeType}
	hints, _, err := a.client.communicationKnown(contracts.InventoryRequest{ConnectionID: a.identity.ConnectionID, ResourceKind: &kind, KnownNativeIDs: []string{a.id}, KnownNativeMetadata: map[string]map[string]any{a.id: request.Asset.Normalized}})
	if err != nil {
		return communicationTree{}, err
	}
	tree, err := a.client.communicationTree(ctx, observation.root, hints)
	if err != nil {
		return tree, err
	}
	if member := tree.members[a.id]; member.id == "" || request.Asset.Normalized[communicationConfiguration] != a.client.privateConfiguration(communicationSnapshot(member.kind, member.raw)) {
		return tree, serviceDenied("communication_action_resource_changed_during_walk")
	}
	for id, expected := range object(request.Asset.Normalized["_communication_ancestors"]) {
		parent := tree.members[id]
		if parent.id == "" || expected != a.client.privateConfiguration(communicationSnapshot(parent.kind, parent.raw)) {
			return tree, serviceDenied("communication_action_parent_changed_during_walk")
		}
	}
	for id, entry := range tree.descendants(a.id) {
		planned := object(object(request.Asset.Normalized[communicationMembers])[id])
		if beforeDelete && object(entry)["kind"] != communicationPhoneType || planned == nil || planned["configuration"] != a.client.privateConfiguration(object(object(entry)["configuration"])) {
			return tree, serviceDenied("communication_action_membership_changed")
		}
	}
	return tree, nil
}

func (a *communicationAction) preflight(ctx context.Context, request contracts.ActionRequest) (check contracts.PreflightResult, observation communicationObservation, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.requestIdentity(request); err != nil {
		return check, observation, err
	}
	if request.ExecutionResult != nil {
		read, err := a.Readback(ctx, request)
		return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists, Evidence: map[string]any{"communication_wait": true}}, observation, err
	}
	for range 2 {
		observation, err = a.observe(ctx, request)
		if err != nil {
			return check, observation, err
		}
		waiting := observation.selected == nil || observation.missingParent || text(object(observation.selected["properties"])["provisioningState"]) == "Deleting"
		if waiting {
			read, err := a.Readback(ctx, request)
			return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists, Evidence: map[string]any{"communication_wait": true}}, observation, err
		}
		if _, err := a.residuals(ctx, request, observation, true); err != nil {
			return check, observation, err
		}
		for _, prerequisite := range request.PrerequisiteDeletions {
			_, err := a.read(ctx, observation.account, prerequisite.Asset.Identity.NativeID, prerequisite.Asset.Identity.NativeType)
			if !isNotFound(err) {
				if err == nil {
					err = serviceDenied("communication_prerequisite_still_exists")
				}
				return check, observation, err
			}
		}
		tree, err := a.currentTree(ctx, request, observation, true)
		if err != nil {
			return check, observation, err
		}
		for id := range observation.ancestors {
			observation.ancestors[id] = tree.members[id].raw
		}
		observation.selected = tree.members[a.id].raw
		if a.kind.NativeType == communicationDomainType {
			incoming, _, err := a.client.communicationIncoming(ctx, map[string]bool{a.id: true}, object(request.Asset.Normalized["_communication_incoming"]))
			if err != nil {
				return check, observation, err
			}
			if len(object(incoming[a.id])) != 0 {
				return check, observation, serviceDenied("communication_domain_still_connected")
			}
		}
		if err := a.protection(ctx, request, observation); err != nil {
			return check, observation, err
		}
	}
	return contracts.PreflightResult{Allowed: true}, observation, nil
}

func (a *communicationAction) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	check, _, err := a.preflight(ctx, request)
	return check, err
}

func (a *communicationAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.requestIdentity(request); err != nil {
		return contracts.ActionResult{}, err
	}
	if request.ExecutionResult != nil {
		return *request.ExecutionResult, nil
	}
	check, observed, err := a.preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !check.Allowed {
		return contracts.ActionResult{}, serviceDenied(check.Reason)
	}
	if check.Absent || check.Evidence["communication_wait"] == true {
		return a.phaseResult(request, "", "", response{}), nil
	}
	deletion := a.deletion
	deletion.Headers = maps.Clone(deletion.Headers)
	if deletion.Headers == nil {
		deletion.Headers = map[string]string{}
	}
	if request.IdempotencyKey != "" {
		deletion.Headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey)
	}
	var res response
	if isCommunicationDataType(a.kind.NativeType) {
		res, err = a.client.communicationRequest(ctx, observed.account, deletion)
	} else {
		res, err = a.client.requestBody(ctx, deletion.Method, deletion.URL, deletion.Body, deletion.Headers)
	}
	if isNotFound(err) {
		return a.phaseResult(request, "", "", response{}), nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operation, polling, err := communicationDeleteReceipt(a.client.subscription, a.id, a.kind.NativeType, a.endpoint, res)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return a.phaseResult(request, operation, polling, res), nil
}

func (a *communicationAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.requestIdentity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	observed, err := a.observe(ctx, request)
	if err != nil {
		return contracts.ReadbackResult{}, contracts.DependencyReadError(err)
	}
	remaining, err := a.residuals(ctx, request, observed, false)
	if err != nil {
		return contracts.ReadbackResult{}, contracts.DependencyReadError(err)
	}
	if observed.selected != nil && !observed.missingParent {
		if _, err := a.currentTree(ctx, request, observed, false); err != nil {
			return contracts.ReadbackResult{}, contracts.DependencyReadError(err)
		}
	}
	return contracts.ReadbackResult{Exists: observed.selected != nil || remaining, State: "communication_deleting"}, nil
}

var _ contracts.ActionDriver = (*communicationAction)(nil)
