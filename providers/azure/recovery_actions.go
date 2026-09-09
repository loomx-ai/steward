package azure

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) recoveryPeer(ctx context.Context, planned asset.Asset, raw map[string]any) (out *serviceChild, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	kind, id := planned.Identity.NativeType, strings.ToLower(planned.Identity.NativeID)
	canonical, parsedType, parseErr := parseID(id)
	if parseErr != nil || canonical != id || !strings.EqualFold(parsedType, kind) {
		return nil, serviceDenied("invalid_recovery_identity")
	}
	if !recoveryType(kind) || (!strings.EqualFold(text(planned.Normalized["role"]), "Primary") && !strings.EqualFold(text(planned.Normalized["role"]), "PrimaryNotReplicating")) || text(planned.Normalized["_recovery_configuration"]) == "" || text(planned.Normalized["_recovery_configuration"]) != recoveryConfiguration(raw) {
		return nil, serviceDenied("recovery_configuration_changed")
	}
	defer func() {
		if err == nil {
			err = c.verifyRecoveryAlias(ctx, kind, id, raw)
		}
	}()
	properties := object(raw["properties"])
	if !recoveryUnpaired(properties) && !recoveryPrimary(properties) {
		return nil, serviceDenied("recovery_pair_changed")
	}
	namespaceType := strings.TrimSuffix(kind, "/disasterRecoveryConfigs")
	ownID := strings.Join(strings.Split(id, "/")[:9], "/")
	peerID := text(planned.Normalized["_recovery_partner_namespace"])
	for _, item := range []struct{ id, marker string }{{ownID, "_recovery_namespace_creation"}, {peerID, "_recovery_peer_namespace_creation"}} {
		if item.id == "" {
			continue
		}
		namespace, err := c.recoveryNamespace(ctx, namespaceType, item.id)
		if err != nil {
			return nil, err
		}
		if expected := text(planned.Normalized[item.marker]); expected == "" || expected != creationGeneration(namespace.data) {
			return nil, serviceDenied("recovery_namespace_recreated")
		}
	}
	if peerID == "" {
		if !recoveryUnpaired(properties) || text(planned.Normalized["partnerNamespace"]) != "" || text(planned.Normalized["_recovery_peer_alias"]) != "" {
			return nil, serviceDenied("recovery_pair_changed")
		}
		return nil, nil
	}
	peerAliasID := peerID + "/disasterrecoveryconfigs/" + last(id)
	if peerID == ownID || text(planned.Normalized["_recovery_peer_alias"]) != peerAliasID || !strings.EqualFold(text(planned.Normalized["_recovery_peer_role"]), "Secondary") || text(planned.Normalized["_recovery_peer_configuration"]) == "" || !strings.EqualFold(text(planned.Normalized["role"]), "Primary") {
		return nil, serviceDenied("recovery_pair_changed")
	}
	if !recoveryUnpaired(properties) {
		partner, err := c.recoveryNamespace(ctx, namespaceType, text(properties["partnerNamespace"]))
		if err != nil {
			return nil, err
		}
		if text(partner.data["id"]) != peerID || creationGeneration(partner.data) != text(planned.Normalized["_recovery_peer_namespace_creation"]) {
			return nil, serviceDenied("recovery_pair_changed")
		}
	}
	definition, _ := findType(kind)
	endpoint, err := c.resourceURL(definition, peerAliasID)
	if err != nil {
		return nil, err
	}
	peer, err := c.request(ctx, "GET", endpoint)
	if isNotFound(err) && recoveryUnpaired(properties) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	peerProperties := object(peer.data["properties"])
	if !validResourceResponse(peer, peerAliasID, kind) || recoveryConfiguration(peer.data) != text(planned.Normalized["_recovery_peer_configuration"]) || !strings.EqualFold(text(peerProperties["role"]), "Secondary") || !replicationCountValid(peerProperties) {
		return nil, serviceDenied("recovery_peer_alias_changed")
	}
	backlink, err := c.recoveryNamespace(ctx, namespaceType, text(peerProperties["partnerNamespace"]))
	if err != nil {
		return nil, err
	}
	if text(backlink.data["id"]) != ownID || creationGeneration(backlink.data) != text(planned.Normalized["_recovery_namespace_creation"]) {
		return nil, serviceDenied("recovery_pair_changed")
	}
	return &serviceChild{kind: kind, id: peerAliasID, data: peer.data}, nil
}

// Alias responses need not have an ETag. Re-read the actual pairing after peer
// discovery so a change of partner cannot hide behind a stable ARM generation.
func (c *client) verifyRecoveryAlias(ctx context.Context, kind, id string, raw map[string]any) error {
	definition, _ := findType(kind)
	endpoint, err := c.resourceURL(definition, id)
	if err != nil {
		return err
	}
	live, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return err
	}
	before, after := object(raw["properties"]), object(live.data["properties"])
	if !validResourceResponse(live, id, kind) || recoveryConfiguration(raw) != recoveryConfiguration(live.data) || creationGeneration(raw) != creationGeneration(live.data) || !strings.EqualFold(text(before["role"]), text(after["role"])) || !messagingNamespaceValueValid(after["partnerNamespace"]) || !strings.EqualFold(text(before["partnerNamespace"]), text(after["partnerNamespace"])) || protectionReason(definition, raw) != protectionReason(definition, live.data) {
		return serviceDenied("recovery_pair_changed")
	}
	return nil
}

func (a *action) recoveryPreflight(ctx context.Context, planned asset.Asset, raw map[string]any, locks []any) error {
	if planned.Identity.Provider != asset.ProviderAzure || !strings.EqualFold(planned.Identity.NativeID, a.id) || planned.Identity.NativeType != a.kind.NativeType {
		return serviceDenied("invalid_recovery_identity")
	}
	if _, err := a.client.recoveryPeer(ctx, planned, raw); err != nil {
		return err
	}
	namespaceType := strings.TrimSuffix(a.kind.NativeType, "/disasterRecoveryConfigs")
	for _, id := range []string{strings.Join(strings.Split(a.id, "/")[:9], "/"), text(planned.Normalized["_recovery_partner_namespace"])} {
		if id == "" {
			continue
		}
		live, err := a.client.recoveryNamespace(ctx, namespaceType, id)
		if err != nil {
			return err
		}
		marker := "_recovery_namespace_creation"
		if id == text(planned.Normalized["_recovery_partner_namespace"]) {
			marker = "_recovery_peer_namespace_creation"
		}
		if text(planned.Normalized[marker]) != creationGeneration(live.data) {
			return serviceDenied("recovery_namespace_recreated")
		}
		kind, _ := findType(namespaceType)
		if reason := protectionReason(kind, live.data); reason != "" {
			return serviceDenied(reason)
		}
		if locked(id, locks) {
			return serviceDenied("azure_management_lock")
		}
		groupID := strings.Join(strings.Split(id, "/")[:5], "/")
		group, err := a.client.request(ctx, "GET", apiURL(groupID, resourcesVersion))
		if err != nil {
			return err
		}
		if !validResourceResponse(group, groupID, groupType) {
			return fmt.Errorf("Azure recovery resource group identity mismatch")
		}
		if text(group.data["managedBy"]) != "" {
			return serviceDenied("azure_managed_resource_group")
		}
	}
	return nil
}

func (a *action) prepareRecovery(ctx context.Context, request contracts.ActionRequest) (out contracts.ActionResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	live, err := a.client.request(ctx, "GET", a.endpoint)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !validResourceResponse(live, a.id, a.kind.NativeType) {
		return contracts.ActionResult{}, fmt.Errorf("Azure recovery preparation identity mismatch")
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if err := a.recoveryPreflight(ctx, request.Asset, live.data, locks); err != nil {
		return contracts.ActionResult{}, err
	}
	if reason := protectionReason(a.kind, live.data); reason != "" {
		return contracts.ActionResult{}, serviceDenied(reason)
	}
	if locked(a.id, locks) {
		return contracts.ActionResult{}, serviceDenied("azure_management_lock")
	}
	if err := a.serviceCascadePreflight(ctx, request, live.data, locks); err != nil {
		return contracts.ActionResult{}, err
	}
	properties := object(live.data["properties"])
	if recoveryUnpaired(properties) {
		return a.delete(ctx, request)
	}
	if !strings.EqualFold(text(properties["provisioningState"]), "Succeeded") || !replicationIdle(properties) {
		return contracts.ActionResult{Data: map[string]any{"phase": "await_recovery", "target": a.id}, RetryAfter: 2 * time.Second}, nil
	}
	metadata, err := providerData()
	if err != nil {
		return contracts.ActionResult{}, err
	}
	provider := strings.Split(a.kind.NativeType, "/")[0]
	operation, ok := metadata.catalog.Operation("Azure." + provider + ".DisasterRecoveryConfigs_BreakPairing")
	if !ok {
		return contracts.ActionResult{}, fmt.Errorf("Azure recovery BreakPairing operation is unavailable")
	}
	_, parameters, err := a.client.resourceOperation(a.kind, a.id, "GET")
	if err != nil {
		return contracts.ActionResult{}, err
	}
	bound, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	headers := map[string]string{}
	if request.IdempotencyKey != "" {
		headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey + ":break-recovery")
	}
	res, err := a.client.requestBody(ctx, bound.Method, bound.URL, bound.Body, headers)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if res.status != 200 {
		return contracts.ActionResult{}, fmt.Errorf("unexpected Azure recovery BreakPairing response")
	}
	if err := operationError(res); err != nil {
		return contracts.ActionResult{}, err
	}
	result, err := a.operationResult(res)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	result.Data["phase"], result.Data["target"], result.Data["operation"] = "break_recovery", a.id, result.ProviderOperationID
	return result, nil
}

func (a *action) waitRecoveryPreparation(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (out contracts.WaitResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if !recoveryType(a.kind.NativeType) || text(result.Data["target"]) != a.id || (text(result.Data["phase"]) == "break_recovery" && text(request.Asset.Normalized["_recovery_peer_alias"]) == "") {
		return contracts.WaitResult{}, fmt.Errorf("invalid Azure recovery preparation phase")
	}
	result.ProviderOperationID = text(result.Data["operation"])
	poll, err := a.poll(ctx, result)
	if err != nil || !poll.Done {
		return poll, err
	}
	check, err := a.Preflight(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !check.Allowed || check.Absent || check.Evidence["service_parent_absent"] == true {
		return contracts.WaitResult{}, serviceDenied("recovery_changed_during_preparation")
	}
	live, err := a.client.request(ctx, "GET", a.endpoint)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if !validResourceResponse(live, a.id, a.kind.NativeType) {
		return contracts.WaitResult{}, fmt.Errorf("Azure recovery readback identity mismatch")
	}
	if err := operationError(live); err != nil {
		return contracts.WaitResult{}, err
	}
	properties := object(live.data["properties"])
	if (text(result.Data["phase"]) == "break_recovery" && !recoveryUnpaired(properties)) || (!recoveryUnpaired(properties) && (!strings.EqualFold(text(properties["provisioningState"]), "Succeeded") || !replicationIdle(properties))) {
		return contracts.WaitResult{State: "unpairing_recovery", RetryAfter: 2 * time.Second}, nil
	}
	next, err := a.Execute(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if next.Data == nil {
		next.Data = map[string]any{}
	}
	if text(next.Data["phase"]) == "" {
		next.Data["phase"] = "delete"
	}
	next.Data["operation"] = next.ProviderOperationID
	return contracts.WaitResult{Data: next.Data, State: text(next.Data["phase"]), RetryAfter: 2 * time.Second}, nil
}
