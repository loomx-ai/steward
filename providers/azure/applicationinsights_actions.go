package azure

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Components_Delete is synchronous (empty 200/204). Its managed resource group
// can nevertheless outlive that response, so completion includes every reviewed
// group member and cannot be inferred from component absence alone.
type insightsComponentAction struct {
	action
	assetID                                                   asset.AssetID
	configuration, workspaceConfiguration, groupConfiguration string
}

func (a *insightsComponentAction) identity(request contracts.ActionRequest) (map[string]any, error) {
	value := request.Asset
	if request.Action != "delete" || len(request.Parameters) != 0 || value.ID != a.assetID || value.ID == "" || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != a.connectionID || value.Identity.Partition != a.partition || value.Identity.NativeType != applicationInsightsType || value.Identity.NativeID != a.id || value.Location != a.location || a.configuration == "" || text(value.Normalized["_monitor_private_link_target_configuration"]) != a.configuration || a.workspaceConfiguration == "" || text(value.Normalized["_insights_workspace_configuration"]) != a.workspaceConfiguration || a.groupConfiguration == "" || text(value.Normalized["_insights_group_configuration"]) != a.groupConfiguration {
		return nil, serviceDenied("insights_component_action_identity_changed")
	}
	state, err := a.client.insightsWorkspacePlan(value)
	if err != nil {
		return nil, err
	}
	group := text(state["managed_group"])
	if group == "" {
		if len(request.LifecycleImpacts) != 0 {
			return nil, serviceDenied("insights_shared_workspace_is_not_owned")
		}
		return state, nil
	}
	impacts, err := a.managedGroupImpacts(request, group)
	if err != nil {
		return nil, err
	}
	if impacts[group].Asset.ID == "" || impacts[text(state["workspace"])].Asset.ID == "" {
		return nil, serviceDenied("insights_managed_group_missing_from_plan")
	}
	for id, value := range object(state["members"]) {
		impact, found := impacts[id]
		if !found || !strings.EqualFold(impact.Asset.Identity.NativeType, text(object(value)["kind"])) {
			return nil, serviceDenied("insights_managed_member_missing_from_plan")
		}
	}
	return state, nil
}

func (a *insightsComponentAction) prerequisitesAbsent(ctx context.Context, request contracts.ActionRequest, state map[string]any) error {
	seen := map[string]bool{a.id: true}
	assets := map[asset.AssetID]bool{a.assetID: true}
	for _, impact := range request.LifecycleImpacts {
		seen[impact.Asset.Identity.NativeID], assets[impact.Asset.ID] = true, true
	}
	for _, prerequisite := range request.PrerequisiteDeletions {
		value := prerequisite.Asset
		identity := value.Identity
		if !prerequisite.Delete || prerequisite.ControllerID != a.assetID || value.ID == "" || assets[value.ID] || seen[identity.NativeID] || identity.Provider != asset.ProviderAzure || identity.ConnectionID != a.connectionID || identity.Partition != a.partition {
			return serviceDenied("invalid_insights_component_prerequisite")
		}
		seen[identity.NativeID], assets[value.ID] = true, true
		if slices.Contains(insightsComponentChildKinds(), identity.NativeType) {
			id, parent, kind, _, err := insightsChildIdentity(identity.NativeID)
			if err != nil || id != identity.NativeID || parent != a.id || kind != identity.NativeType || value.Location != a.location || text(value.Normalized["_insights_component"]) != a.id || text(value.Normalized["_insights_component_configuration"]) != a.configuration || text(value.Normalized[insightsChildProofKey(kind)]) == "" {
				return serviceDenied("insights_component_child_prerequisite_changed")
			}
			mapping, _ := findType(kind)
			if _, err := a.client.insightsChildRead(ctx, mapping, id); !isNotFound(err) {
				if err != nil {
					return err
				}
				return serviceDenied("insights_component_child_requires_deletion")
			}
			continue
		}
		id, kind, err := parseID(identity.NativeID)
		linked, linkedErr := monitorPrivateLinkReference(map[string]any{"properties": value.Normalized})
		workspaceLink := linked == text(state["workspace"]) && text(state["managed_group"]) != ""
		if err != nil || id != identity.NativeID || !strings.HasPrefix(id, a.client.root()+"/") || !strings.EqualFold(kind, monitorScopedResourceType) || identity.NativeType != monitorScopedResourceType || linkedErr != nil || linked != a.id && !workspaceLink || text(value.Normalized["_monitor_private_link_private_configuration"]) == "" {
			return serviceDenied("invalid_insights_private_link_prerequisite")
		}
		if workspaceLink && text(object(state["incoming"])[id]) != text(value.Normalized["_monitor_private_link_private_configuration"]) {
			return serviceDenied("insights_workspace_prerequisite_configuration_changed")
		}
		mapping, _ := findType(monitorScopedResourceType)
		endpoint, err := a.client.resourceURL(mapping, id)
		if err != nil {
			return err
		}
		if _, err := a.client.request(ctx, "GET", endpoint); !isNotFound(err) {
			if err != nil {
				return err
			}
			return serviceDenied("insights_private_link_requires_unlink")
		}
	}
	return nil
}

func (a *insightsComponentAction) Preflight(ctx context.Context, request contracts.ActionRequest) (check contracts.PreflightResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	state, err := a.identity(request)
	if err != nil {
		return check, err
	}
	if err := a.prerequisitesAbsent(ctx, request, state); err != nil {
		return check, err
	}
	raw, err := a.client.insightsComponent(ctx, a.id)
	if isNotFound(err) {
		read, err := a.residuals(ctx, request, state)
		return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists, Evidence: map[string]any{"insights_component_absent": true}}, err
	}
	if err != nil {
		return check, err
	}
	if err := a.client.insightsComponentIncarnation(request.Asset, raw); err != nil {
		return check, err
	}
	if reason := protectionReason(a.kind, raw); reason != "" {
		return contracts.PreflightResult{Reason: reason}, nil
	}
	parentGroup := strings.Join(strings.Split(a.id, "/")[:5], "/")
	group, err := a.client.request(ctx, "GET", apiURL(parentGroup, resourcesVersion))
	if err != nil {
		return check, err
	}
	owner, err := insightsManagedBy(group.data)
	if err != nil || !insightsARMReadValid(group, parentGroup, groupType) {
		return check, serviceDenied("invalid_insights_component_resource_group")
	}
	if owner != "" {
		return contracts.PreflightResult{Reason: "azure_managed_resource_group"}, nil
	}
	if protectedAzureTags(object(group.data["tags"])) {
		return contracts.PreflightResult{Reason: "azure_protected_tag"}, nil
	}
	if a.client.privateConfiguration(map[string]any{"id": parentGroup, "tags": group.data["tags"], "managedBy": group.data["managedBy"]}) != a.groupConfiguration {
		return check, serviceDenied("insights_component_resource_group_changed")
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return check, err
	}
	deleting := []string{a.id}
	for _, impact := range request.LifecycleImpacts {
		deleting = append(deleting, impact.Asset.Identity.NativeID)
	}
	for _, id := range deleting {
		if locked(id, locks) || slices.ContainsFunc(locks, func(lock any) bool { return inResourceGroup(text(object(lock)["id"]), id) }) {
			return contracts.PreflightResult{Reason: "azure_management_lock"}, nil
		}
	}
	children, err := a.client.insightsComponentChildren(ctx, a.id)
	if err != nil {
		return check, err
	}
	if len(children) != 0 {
		return check, serviceDenied("insights_component_children_require_prior_deletion")
	}
	incoming, err := a.client.monitorPrivateLinkIncoming(ctx, request.Asset.Identity)
	if err != nil {
		return check, err
	}
	if len(incoming) != 0 {
		return check, serviceDenied("insights_component_requires_private_link_unlink")
	}
	for range 2 {
		groups, err := a.client.insightsGroups(ctx)
		if err != nil {
			return check, err
		}
		snapshot, err := a.client.readInsightsWorkspace(ctx, a.id, raw, groups)
		if err != nil {
			return check, err
		}
		if a.client.privateConfiguration(insightsWorkspaceLifecycleState(snapshot.state)) != a.client.privateConfiguration(insightsWorkspaceLifecycleState(state)) {
			return check, serviceDenied("insights_workspace_lifecycle_changed")
		}
		if len(snapshot.incoming) != 0 {
			return check, serviceDenied("insights_workspace_requires_private_link_unlink")
		}
		if managedGroup := text(state["managed_group"]); managedGroup != "" {
			nativeGroup := maps.Clone(snapshot.group)
			nativeGroup["type"] = groupType
			resources := append([]map[string]any{nativeGroup}, snapshot.resources...)
			reason, err := a.managedGroupResourcesPreflight(ctx, request, managedGroup, resources, locks)
			if err != nil {
				return check, err
			}
			if reason != "" {
				return contracts.PreflightResult{Reason: strings.ReplaceAll(reason, "aks_", "insights_")}, nil
			}
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *insightsComponentAction) receipt(request contracts.ActionRequest) string {
	bindings := func(impacts []contracts.ActionImpact) map[string]any {
		values := map[string]any{}
		for _, impact := range impacts {
			value := impact.Asset
			values[value.Identity.NativeID] = map[string]any{"asset": value.ID, "identity": value.Identity, "location": value.Location, "configuration": value.Normalized, "controller": impact.ControllerID, "delete": impact.Delete}
		}
		return values
	}
	return a.client.privateConfiguration(map[string]any{"asset": a.assetID, "resource": a.id, "connection": a.connectionID, "partition": a.partition, "location": a.location, "component": a.configuration, "workspace": a.workspaceConfiguration, "group": a.groupConfiguration, "impacts": bindings(request.LifecycleImpacts), "prerequisites": bindings(request.PrerequisiteDeletions), "version": insightsComponentVersion, "protocol": "component-and-managed-group-absence"})
}

func (a *insightsComponentAction) result(request contracts.ActionRequest, response response) contracts.ActionResult {
	return contracts.ActionResult{ProviderRequestID: response.requestID, RetryAfter: retryAfter(response.header), Data: map[string]any{"_insights_component_receipt": a.receipt(request)}}
}

func (a *insightsComponentAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	check, err := a.Preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !check.Allowed {
		return contracts.ActionResult{}, serviceDenied(check.Reason)
	}
	if check.Absent || check.Evidence["insights_component_absent"] == true {
		return a.result(request, response{}), nil
	}
	headers := maps.Clone(a.deletion.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	if request.IdempotencyKey != "" {
		headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey)
	}
	response, err := a.client.requestBody(ctx, a.deletion.Method, a.deletion.URL, a.deletion.Body, headers)
	if isNotFound(err) {
		return a.result(request, response), nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if response.status != 200 && response.status != 204 || operationLocation(response.header) != "" || len(response.data) != 0 {
		return contracts.ActionResult{}, serviceDenied("invalid_insights_component_delete_response")
	}
	return a.result(request, response), nil
}

func (a *insightsComponentAction) verifyReceipt(request contracts.ActionRequest, result contracts.ActionResult) error {
	if result.ProviderOperationID != "" || text(result.Data["_insights_component_receipt"]) != a.receipt(request) {
		return serviceDenied("insights_component_receipt_changed")
	}
	return nil
}

func (a *insightsComponentAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if _, err := a.identity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if err := a.verifyReceipt(request, result); err != nil {
		return contracts.WaitResult{}, err
	}
	request.ExecutionResult = &result
	read, err := a.Readback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, State: read.State, RetryAfter: 2 * time.Second}, err
}

func (a *insightsComponentAction) residuals(ctx context.Context, request contracts.ActionRequest, state map[string]any) (contracts.ReadbackResult, error) {
	group := text(state["managed_group"])
	for range 2 {
		if group != "" {
			read, err := a.managedGroupResourcesReadback(ctx, request, group)
			if read.Exists {
				read.State = "insights_managed_workspace_deleting"
			}
			if err != nil || read.Exists {
				return read, err
			}
		}
		// Recheck the component after the group and native members: a new
		// incarnation appearing during residual reads cannot count as absent.
		raw, err := a.client.insightsComponent(ctx, a.id)
		if err == nil {
			if err := a.client.insightsComponentIncarnation(request.Asset, raw); err != nil {
				return contracts.ReadbackResult{}, err
			}
			return contracts.ReadbackResult{Exists: true, State: "insights_component_deleting"}, nil
		}
		if !isNotFound(err) {
			return contracts.ReadbackResult{}, err
		}
	}
	return contracts.ReadbackResult{}, nil
}

func (a *insightsComponentAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	state, err := a.identity(request)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if request.ExecutionResult != nil {
		if err := a.verifyReceipt(request, *request.ExecutionResult); err != nil {
			return contracts.ReadbackResult{}, err
		}
	}
	if err := a.prerequisitesAbsent(ctx, request, state); err != nil {
		return contracts.ReadbackResult{}, err
	}
	raw, err := a.client.insightsComponent(ctx, a.id)
	if err == nil {
		if err := a.client.insightsComponentIncarnation(request.Asset, raw); err != nil {
			return contracts.ReadbackResult{}, err
		}
		return contracts.ReadbackResult{Exists: true, State: "insights_component_deleting"}, nil
	}
	if !isNotFound(err) {
		return contracts.ReadbackResult{}, err
	}
	return a.residuals(ctx, request, state)
}

var _ contracts.ActionDriver = (*insightsComponentAction)(nil)
