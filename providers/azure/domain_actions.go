package azure

import (
	"context"
	"maps"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type domainAction struct {
	action
	planned asset.Asset
}

func (a *domainAction) identity(request contracts.ActionRequest) error {
	value := request.Asset
	if request.Action != "delete" || value.ID != a.planned.ID || value.Identity != a.planned.Identity || value.Location != "global" || text(value.Normalized[domainProof]) != text(a.planned.Normalized[domainProof]) || len(request.Parameters) != 0 || len(request.LifecycleImpacts) != 0 {
		return serviceDenied("domain_action_request_changed")
	}
	if _, err := a.client.plannedResourceID(value); err != nil {
		return err
	}
	dependencies, err := a.client.domainPlan(value)
	if err != nil {
		return err
	}
	if len(request.PrerequisiteDeletions) != len(dependencies) {
		return serviceDenied("domain_review_prerequisites_changed")
	}
	seen := map[string]bool{}
	assets := map[asset.AssetID]bool{value.ID: true}
	for _, prerequisite := range request.PrerequisiteDeletions {
		member := prerequisite.Asset
		id := member.Identity.NativeID
		entry := object(dependencies[id])
		configuration := text(member.Normalized[domainConfiguration])
		if isAppBinding(member.Identity.NativeType) {
			configuration = text(member.Normalized["_app_service_private_configuration"])
		}
		if member.ID == "" || assets[member.ID] || seen[id] || !prerequisite.Delete || prerequisite.ControllerID != value.ID || member.Identity.Provider != value.Identity.Provider || member.Identity.ConnectionID != value.Identity.ConnectionID || member.Identity.Partition != value.Identity.Partition || entry == nil || entry["kind"] != member.Identity.NativeType || configuration == "" || entry["configuration"] != configuration || !serviceChildRelation(value, member) {
			return serviceDenied("invalid_domain_review_prerequisite")
		}
		seen[id], assets[member.ID] = true, true
		if _, err := a.client.plannedResourceID(member); err != nil {
			return err
		}
		if isDomainType(member.Identity.NativeType) {
			if _, err := a.client.domainPlan(member); err != nil {
				return err
			}
		}
	}
	if request.ExecutionResult != nil {
		return a.verifyReceipt(request, *request.ExecutionResult)
	}
	return nil
}

func (a *domainAction) receipt(request contracts.ActionRequest, phase string) string {
	request.ExecutionResult, request.IdempotencyKey = nil, ""
	return a.client.privateConfiguration(map[string]any{"request": request, "phase": phase, "protocol": "domain-delete-after-24h-2"})
}

func (a *domainAction) verifyReceipt(request contracts.ActionRequest, result contracts.ActionResult) error {
	phase := text(result.Data["domain_phase"])
	if result.ProviderOperationID != "" || len(result.Data) != 2 || phase != "delete" && (phase != "hostnames" || a.kind.NativeType != domainType) || text(result.Data["domain_delete_binding"]) != a.receipt(request, phase) {
		return serviceDenied("domain_delete_receipt_changed")
	}
	return nil
}

// Domains_Delete defaults to a 24-hour delay. A persisted waiter spans that
// native window and only the named GET's 404 completes registration cleanup.
func (a *domainAction) DeletionCheckTimeout() time.Duration {
	if a.kind.NativeType == domainType {
		return 48 * time.Hour
	}
	return time.Hour
}

func (a *domainAction) preflight(ctx context.Context, request contracts.ActionRequest) (check contracts.PreflightResult, err error) {
	ctx = context.WithValue(ctx, domainReadContextKey{}, true)
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := a.identity(request); err != nil {
		return check, err
	}
	current, err := a.client.readResource(ctx, a.endpoint)
	if isNotFound(err) {
		read, err := a.residualReadback(ctx, request)
		return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists, Evidence: map[string]any{"domain_absent": true}}, err
	}
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if !insightsARMReadValid(current, a.id, a.kind.NativeType) {
		return contracts.PreflightResult{}, serviceDenied("invalid_domain_preflight_response")
	}
	if err := a.client.domainIncarnation(request.Asset, current.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if a.kind.NativeType == domainType && object(current.data["properties"])["provisioningState"] == "Deleting" {
		_, err := a.residualReadback(ctx, request)
		return contracts.PreflightResult{Allowed: err == nil, Evidence: map[string]any{"domain_deleting": true}}, err
	}
	check, err = a.action.Preflight(ctx, request)
	if err != nil || !check.Allowed {
		return check, err
	}
	// Recheck assignments after the prerequisite walk and protection reads.
	current, err = a.client.readResource(ctx, a.endpoint)
	if isNotFound(err) {
		read, err := a.residualReadback(ctx, request)
		return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists, Evidence: map[string]any{"domain_absent": true}}, err
	}
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if !insightsARMReadValid(current, a.id, a.kind.NativeType) {
		return contracts.PreflightResult{}, serviceDenied("invalid_domain_preflight_response")
	}
	if err := a.client.domainIncarnation(request.Asset, current.data); err != nil {
		return contracts.PreflightResult{}, err
	}
	if a.kind.NativeType == domainOwnershipType {
		parent, err := a.client.domainParent(ctx, a.id)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if text(request.Asset.Normalized["_domain_parent_configuration"]) != a.client.privateConfiguration(domainSnapshot(domainType, parent)) {
			return contracts.PreflightResult{}, serviceDenied("domain_ownership_parent_changed")
		}
		if reason := protectionReason(resourceType{NativeType: domainType}, parent); reason != "" {
			return contracts.PreflightResult{Reason: reason}, nil
		}
		if state := text(object(parent["properties"])["provisioningState"]); state == "InProgress" || state == "Deleting" {
			return contracts.PreflightResult{Reason: "domain_parent_busy"}, nil
		}
		return contracts.PreflightResult{Allowed: true}, nil
	}
	state := text(object(current.data["properties"])["provisioningState"])
	if state == "InProgress" {
		return contracts.PreflightResult{Reason: "domain_provisioning_in_progress"}, nil
	}
	if hostnames := array(object(current.data["properties"])["managedHostNames"]); len(hostnames) != 0 {
		for _, hostname := range hostnames {
			entry := object(hostname)
			known := false
			for _, prerequisite := range request.PrerequisiteDeletions {
				id := prerequisite.Asset.Identity
				if isAppBinding(id.NativeType) && strings.EqualFold(text(entry["name"]), last(id.NativeID)) {
					known = true
				}
			}
			if entry["azureResourceType"] != "Website" || !known {
				return contracts.PreflightResult{Reason: "domain_has_managed_hostnames"}, nil
			}
		}
		// Every reviewed binding's own GET is already 404. Wait for the
		// read-only index to settle; no deletion is authorized by this state.
		return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{"domain_hostnames_pending": true}}, nil
	}
	return contracts.PreflightResult{Allowed: true, Evidence: map[string]any{"domain_deleting": state == "Deleting"}}, nil
}

func (a *domainAction) Preflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	return a.preflight(ctx, request)
}

func (a *domainAction) Execute(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.ActionResult{}, err
	}
	if request.ExecutionResult != nil && request.ExecutionResult.Data["domain_phase"] == "delete" {
		return *request.ExecutionResult, nil
	}
	check, err := a.preflight(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !check.Allowed {
		return contracts.ActionResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProtected, Code: check.Reason, Message: contracts.SafeProviderValidationMessage}}
	}
	phase := "delete"
	if check.Evidence["domain_hostnames_pending"] == true {
		phase = "hostnames"
	}
	result := contracts.ActionResult{Data: map[string]any{"domain_phase": phase, "domain_delete_binding": a.receipt(request, phase)}}
	if phase == "hostnames" {
		result.RetryAfter = time.Minute
		return result, nil
	}
	if check.Absent || check.Evidence["domain_absent"] == true || check.Evidence["domain_deleting"] == true {
		return result, nil
	}
	headers := maps.Clone(a.deletion.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	if request.IdempotencyKey != "" {
		headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey)
	}
	res, err := a.client.requestBody(ctx, a.deletion.Method, a.deletion.URL, a.deletion.Body, headers)
	if isNotFound(err) {
		return result, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	// The pinned API declares synchronous 200/204 receipts, followed by the
	// delayed resource deletion. It defines no operation-status endpoint.
	if res.status != 200 && res.status != 204 || operationLocation(res.header) != "" {
		return contracts.ActionResult{}, serviceDenied("invalid_domain_delete_receipt")
	}
	if err := operationError(res); err != nil {
		return contracts.ActionResult{}, err
	}
	result.ProviderRequestID, result.RetryAfter = res.requestID, retryAfter(res.header)
	return result, nil
}

func (a *domainAction) Wait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if err := a.identity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if err := a.verifyReceipt(request, result); err != nil {
		return contracts.WaitResult{}, err
	}
	if result.Data["domain_phase"] == "hostnames" {
		next, err := a.Execute(ctx, request)
		return contracts.WaitResult{State: "domain_" + text(next.Data["domain_phase"]), Data: next.Data, RetryAfter: time.Minute}, err
	}
	read, err := a.Readback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, State: read.State, RetryAfter: time.Minute}, err
}

func (a *domainAction) residualReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	dependencies, err := a.client.domainPlan(request.Asset)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	exists := false
	for id, value := range dependencies {
		entry := object(value)
		kind, _ := findType(text(entry["kind"]))
		endpoint, err := a.client.resourceURL(kind, id)
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		current, err := a.client.readResource(ctx, endpoint)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		if !insightsARMReadValid(current, id, kind.NativeType) || entry["configuration"] != a.client.privateConfiguration(domainChildSnapshot(kind.NativeType, current.data)) {
			return contracts.ReadbackResult{}, serviceDenied("domain_residual_dependency_changed")
		}
		exists = true
	}
	return contracts.ReadbackResult{Exists: exists, State: "domain_dependencies_deleting"}, nil
}

func (a *domainAction) Readback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	ctx = context.WithValue(ctx, domainReadContextKey{}, true)
	if err := a.identity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	current, err := a.client.readResource(ctx, a.endpoint)
	if isNotFound(err) {
		return a.residualReadback(ctx, request)
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if !insightsARMReadValid(current, a.id, a.kind.NativeType) {
		return contracts.ReadbackResult{}, serviceDenied("invalid_domain_readback_response")
	}
	if err := a.client.domainIncarnation(request.Asset, current.data); err != nil {
		return contracts.ReadbackResult{}, err
	}
	return contracts.ReadbackResult{Exists: true, State: text(object(current.data["properties"])["provisioningState"])}, nil
}

func newDomainAction(driver *action, value asset.Asset) (*domainAction, error) {
	if value.ID == "" || value.Identity.ConnectionID != driver.connectionID || driver.connectionID == "" || value.Identity.Partition == "" || value.Identity.NativeType != driver.kind.NativeType || value.Identity.NativeID != driver.id || value.Location != "global" || !strings.HasPrefix(driver.id, driver.client.root()+"/") {
		return nil, serviceDenied("invalid_domain_action_identity")
	}
	if _, err := driver.client.domainPlan(value); err != nil {
		return nil, err
	}
	return &domainAction{action: *driver, planned: value}, nil
}
