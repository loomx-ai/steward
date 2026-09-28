package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Policy assignments are extension resources at a management group,
// subscription, resource group or resource scope. The subscription list also
// returns assignments inherited from management groups; like inherited role
// assignments they are not owned by the connection and are left out.
const (
	policyAssignmentType     = "Microsoft.Authorization/policyAssignments"
	policyAssignmentVersion  = "2025-01-01"
	policyAssignmentWire     = "_policy_assignment_wire_id"
	policyAssignmentInstance = "instanceId"
	policyAssignmentPath     = "/providers/microsoft.authorization/policyassignments/"
)

func policyAssignmentKind(kind string) bool { return strings.EqualFold(kind, policyAssignmentType) }

// policyAssignmentScope splits an assignment ID into its lower-case scope and
// name. It rejects IDs that are not policy assignments.
func policyAssignmentScope(wire string) (scope, name string, err error) {
	if wire != strings.TrimSpace(wire) || strings.ContainsAny(wire, "%?#\\\x00\r\n\t") {
		return "", "", serviceDenied("invalid_policy_assignment_identity")
	}
	id := strings.ToLower(wire)
	index := strings.LastIndex(id, policyAssignmentPath)
	if index < 0 {
		return "", "", serviceDenied("invalid_policy_assignment_identity")
	}
	name = wire[index+len(policyAssignmentPath):]
	if name == "" || strings.Contains(name, "/") {
		return "", "", serviceDenied("invalid_policy_assignment_name")
	}
	scope = id[:index]
	if scope == "" {
		scope = "/"
	}
	return scope, name, nil
}

// policyAssignmentProtection names why an assignment must not be deleted
// directly, or returns "".
func policyAssignmentProtection(raw map[string]any, locks []any) string {
	props := object(raw["properties"])
	switch strings.ToLower(text(props["assignmentType"])) {
	case "system", "systemhidden":
		// Created and managed by an Azure service.
		return "azure_policy_assignment_system_managed"
	}
	// Microsoft Defender for Cloud assigns its default initiative and
	// re-creates it while the plan is enabled.
	if strings.EqualFold(text(object(props["metadata"])["assignedBy"]), "Security Center") {
		return "azure_policy_assignment_defender_managed"
	}
	if locked(strings.ToLower(text(raw["id"])), locks) {
		return "azure_management_lock"
	}
	return ""
}

func policyAssignmentCatalogOperation(name string) (catalog.Operation, error) {
	metadata, err := providerData()
	if err != nil {
		return catalog.Operation{}, err
	}
	op, ok := metadata.catalog.Operation("Azure.Microsoft.Authorization.PolicyAssignments_" + name)
	if !ok || op.Call == nil || op.Call.Version != policyAssignmentVersion {
		return catalog.Operation{}, serviceDenied("policy_assignment_native_operation_changed")
	}
	return op, nil
}

// policyAssignmentOperation binds a read or delete of one assignment the
// connection owns; the request keeps the assignment's case-sensitive scope.
func (c *client) policyAssignmentOperation(wire, method string) (catalog.Operation, map[string]any, error) {
	scope, name, err := policyAssignmentScope(wire)
	if err != nil || !c.rbacLocalScope(scope) || method != "GET" && method != "DELETE" {
		return catalog.Operation{}, nil, serviceDenied("policy_assignment_outside_connection")
	}
	operation := "Get"
	if method == "DELETE" {
		operation = "Delete"
	}
	op, err := policyAssignmentCatalogOperation(operation)
	index := strings.LastIndex(strings.ToLower(wire), policyAssignmentPath)
	return op, map[string]any{"scope": strings.TrimPrefix(wire[:index], "/"), "policyAssignmentName": name}, err
}

func (c *client) policyAssignmentRequest(wire, method string) (catalog.RESTRequest, error) {
	op, parameters, err := c.policyAssignmentOperation(wire, method)
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	return bindAzureREST(op, parameters)
}

func (c *client) policyAssignmentRead(ctx context.Context, wire string) (response, error) {
	request, err := c.policyAssignmentRequest(wire, "GET")
	if err != nil {
		return response{}, err
	}
	current, err := c.request(ctx, "GET", request.URL)
	if err != nil {
		return current, err
	}
	if !strings.EqualFold(text(current.data["id"]), wire) || !policyAssignmentKind(text(current.data["type"])) {
		return current, serviceDenied("policy_assignment_read_identity_changed")
	}
	return current, nil
}

// policyAssignmentIndex lists the subscription's own assignments: at the
// subscription and at resource groups and resources within it.
func (c *client) policyAssignmentIndex(ctx context.Context) ([]map[string]any, string, error) {
	op, err := policyAssignmentCatalogOperation("List")
	if err != nil {
		return nil, "", err
	}
	request, err := bindAzureREST(op, map[string]any{"subscriptionId": c.subscription})
	if err != nil {
		return nil, "", err
	}
	u, _ := url.Parse(request.URL)
	rows, seen := []map[string]any{}, map[string]bool{}
	values, result, err := c.listAllURLResult(ctx, request.URL, u.Path)
	if err != nil {
		return nil, "", contracts.DependencyReadError(err)
	}
	for _, value := range values {
		raw, ok := value.(map[string]any)
		if !ok {
			return nil, "", serviceDenied("invalid_policy_assignment_list_row")
		}
		scope, _, err := policyAssignmentScope(text(raw["id"]))
		if err != nil || !policyAssignmentKind(text(raw["type"])) {
			return nil, "", serviceDenied("invalid_policy_assignment_list_row")
		}
		if !c.rbacLocalScope(scope) {
			if scope == "/" || strings.HasPrefix(scope, "/providers/microsoft.management/managementgroups/") {
				continue
			}
			return nil, "", serviceDenied("policy_assignment_index_contains_foreign_subscription")
		}
		id := strings.ToLower(text(raw["id"]))
		if seen[id] {
			return nil, "", serviceDenied("duplicate_policy_assignment_identity")
		}
		seen[id] = true
		rows = append(rows, raw)
	}
	return rows, result.requestID, nil
}

func (r *Runtime) policyAssignmentItem(c *client, raw map[string]any, locks []any) contracts.InventoryItem {
	props := object(raw["properties"])
	id, identity := strings.ToLower(text(raw["id"])), object(raw["identity"])
	scope, name, _ := policyAssignmentScope(text(raw["id"]))
	normalized := map[string]any{
		"name": name, "subscription_id": c.subscription, "_inventory_source": productInventorySource,
		policyAssignmentWire: text(raw["id"]), "scope": scope,
		"initiative":    strings.Contains(strings.ToLower(text(props["policyDefinitionId"])), "/policysetdefinitions/"),
		"identity_type": text(identity["type"]), "identity_principal_id": text(identity["principalId"]),
	}
	for _, field := range []string{"displayName", "policyDefinitionId", "enforcementMode", "assignmentType", policyAssignmentInstance, "notScopes"} {
		if value, ok := props[field]; ok {
			normalized[field] = value
		}
	}
	if reason := policyAssignmentProtection(raw, locks); reason != "" {
		normalized["cleanup_protected"], normalized["cleanup_protection_reason"] = true, reason
	}
	displayName := text(props["displayName"])
	if displayName == "" {
		displayName = name
	}
	actionable := true
	return contracts.InventoryItem{NativeID: id, NativeType: policyAssignmentType, ResourceKind: r.resourceKind(policyAssignmentType), Actionable: &actionable, Scope: contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: c.subscription + "/global", Name: "Global", Location: "global"}, Name: displayName, Location: "global", State: "available", Normalized: normalized, Raw: safePayload(raw), NativeAliases: []string{text(raw["id"]), id}}
}

func (r *Runtime) listPolicyAssignments(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.Source != productInventorySource || request.ResourceKind == nil || !policyAssignmentKind(request.ResourceKind.NativeType) || len(request.Options)+len(request.KnownNativeIDs)+len(request.KnownNativeMetadata) != 0 {
		return batch, serviceDenied("invalid_policy_assignment_inventory_source")
	}
	switch request.Scope.Kind {
	case asset.ScopeSubscription:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription) {
			return batch, serviceDenied("policy_assignment_inventory_subscription_changed")
		}
	case asset.ScopeGlobal:
		if request.Scope.NativeID != "global" && !strings.EqualFold(request.Scope.NativeID, c.subscription+"/global") {
			return batch, serviceDenied("policy_assignment_inventory_global_scope_changed")
		}
	case asset.ScopeRegion:
		if request.Scope.NativeID == "" {
			return batch, serviceDenied("policy_assignment_inventory_region_missing")
		}
	default:
		return batch, serviceDenied("invalid_policy_assignment_inventory_scope")
	}
	rows, requestID, err := c.policyAssignmentIndex(ctx)
	if err != nil {
		return batch, err
	}
	locks, err := c.managementLocks(ctx)
	if err != nil {
		return batch, err
	}
	items, bindings := []contracts.InventoryItem{}, map[string]any{}
	for _, raw := range rows {
		item := r.policyAssignmentItem(c, raw, locks)
		bindings[item.NativeID] = item.Normalized[policyAssignmentInstance]
		if productScopeMatches(request, item) {
			items = append(items, item)
		}
	}
	slices.SortFunc(items, func(a, b contracts.InventoryItem) int { return strings.Compare(a.NativeID, b.NativeID) })
	fingerprint := c.privateConfiguration(map[string]any{"revision": r.bundle.Revision, "bindings": bindings, "ids": slices.Sorted(maps.Keys(bindings))})
	cursor := productCursor{}
	if request.Cursor != "" {
		data, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(data, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint != fingerprint || cursor.Target >= len(items) {
			return batch, serviceDenied("policy_assignment_inventory_cursor_changed")
		}
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := cursor.Target + min(limit, len(items)-cursor.Target)
	batch = contracts.InventoryBatch{Items: items[cursor.Target:end], Complete: end == len(items), RequestID: requestID}
	if !batch.Complete {
		cursor.Fingerprint, cursor.Target = fingerprint, end
		data, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(data)
	}
	return batch, nil
}

type policyAssignmentAction struct {
	client           *client
	planned          asset.Asset
	wire, instanceID string
	deletion         catalog.RESTRequest
}

func newPolicyAssignmentAction(c *client, connection asset.ConnectionID, value asset.Asset) (*policyAssignmentAction, error) {
	if value.ID == "" || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID != connection || connection == "" || value.Location != "global" || !policyAssignmentKind(value.Identity.NativeType) {
		return nil, serviceDenied("invalid_policy_assignment_action_identity")
	}
	wire, instanceID := text(value.Normalized[policyAssignmentWire]), text(value.Normalized[policyAssignmentInstance])
	if !strings.EqualFold(wire, value.Identity.NativeID) || instanceID == "" {
		return nil, serviceDenied("invalid_policy_assignment_action_identity")
	}
	deletion, err := c.policyAssignmentRequest(wire, "DELETE")
	if err != nil {
		return nil, err
	}
	return &policyAssignmentAction{client: c, planned: value, wire: wire, instanceID: instanceID, deletion: deletion}, nil
}

func (*policyAssignmentAction) DeletionCheckTimeout() time.Duration { return 10 * time.Minute }

func (a *policyAssignmentAction) identity(request contracts.ActionRequest) error {
	value := request.Asset
	if request.Action != "delete" || len(request.Parameters)+len(request.LifecycleImpacts) != 0 || value.ID != a.planned.ID || value.Identity != a.planned.Identity || text(value.Normalized[policyAssignmentWire]) != a.wire || text(value.Normalized[policyAssignmentInstance]) != a.instanceID {
		return serviceDenied("policy_assignment_action_identity_changed")
	}
	return nil
}

func (a *policyAssignmentAction) Preflight(ctx context.Context, request contracts.ActionRequest) (result contracts.PreflightResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return result, err
	}
	current, err := a.client.policyAssignmentRead(ctx, a.wire)
	if isNotFound(err) {
		return contracts.PreflightResult{Allowed: true, Absent: true}, nil
	}
	if err != nil {
		return result, err
	}
	// instanceId changes only when the assignment is deleted and recreated.
	if text(object(current.data["properties"])[policyAssignmentInstance]) != a.instanceID {
		return contracts.PreflightResult{Reason: "policy_assignment_recreated"}, nil
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return result, err
	}
	if reason := policyAssignmentProtection(current.data, locks); reason != "" {
		return contracts.PreflightResult{Reason: reason}, nil
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *policyAssignmentAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	check, err := a.Preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !check.Allowed {
		return contracts.ActionResult{}, serviceDenied(check.Reason)
	}
	result := contracts.ActionResult{Data: map[string]any{"_policy_assignment_deleted_instance": a.instanceID}}
	if check.Absent {
		return result, nil
	}
	headers := maps.Clone(a.deletion.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	if request.IdempotencyKey != "" {
		headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey)
	}
	deleted, err := a.client.requestAt(ctx, a.deletion.Method, a.deletion.URL, a.deletion.Body, headers, func(endpoint string) error {
		if endpoint != a.deletion.URL {
			return serviceDenied("policy_assignment_delete_endpoint_changed")
		}
		return a.client.validateURL(endpoint)
	})
	if isNotFound(err) {
		return result, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if operationLocation(deleted.header) != "" || deleted.status != 200 && deleted.status != 204 {
		return contracts.ActionResult{}, serviceDenied("invalid_policy_assignment_delete_response")
	}
	result.ProviderRequestID = deleted.requestID
	return result, nil
}

func (a *policyAssignmentAction) Readback(ctx context.Context, request contracts.ActionRequest) (result contracts.ReadbackResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return result, err
	}
	current, err := a.client.policyAssignmentRead(ctx, a.wire)
	if isNotFound(err) {
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if text(object(current.data["properties"])[policyAssignmentInstance]) != a.instanceID {
		return result, serviceDenied("policy_assignment_recreated_after_delete")
	}
	return contracts.ReadbackResult{Exists: true, State: "deleting"}, nil
}

func (a *policyAssignmentAction) Wait(ctx context.Context, request contracts.ActionRequest, _ contracts.ActionResult) (contracts.WaitResult, error) {
	read, err := a.Readback(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	return contracts.WaitResult{Done: !read.Exists, State: read.State, RetryAfter: 2 * time.Second}, nil
}

var _ contracts.ActionDriver = (*policyAssignmentAction)(nil)
