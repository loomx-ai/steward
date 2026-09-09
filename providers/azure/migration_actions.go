package azure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Active with zero pending copies means ready to commit, not unpaired. Revert
// stops copying and preserves the target namespace and all copied entities.
// https://learn.microsoft.com/azure/service-bus-messaging/service-bus-migrate-standard-premium
func migrationConfiguration(raw map[string]any) string {
	properties := object(safePayload(raw)["properties"])
	for _, field := range []string{"targetNamespace", "migrationState", "provisioningState", "pendingReplicationOperationsCount"} {
		delete(properties, field)
	}
	payload, _ := json.Marshal(properties)
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

func (c *client) migrationNamespace(ctx context.Context, id string) (out response, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	canonical, nativeType, err := parseID(id)
	if err != nil || !strings.EqualFold(nativeType, serviceBusNamespaceType) || !strings.HasPrefix(canonical, c.root()+"/") {
		return response{}, serviceDenied("invalid_migration_namespace")
	}
	kind, _ := findType(serviceBusNamespaceType)
	endpoint, err := c.resourceURL(kind, canonical)
	if err != nil {
		return response{}, err
	}
	live, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return response{}, err
	}
	if !validResourceResponse(live, canonical, serviceBusNamespaceType) {
		return response{}, fmt.Errorf("Azure migration namespace identity mismatch")
	}
	return live, nil
}

func (c *client) migrationInventory(ctx context.Context, id string, raw, normalized map[string]any, refs map[string][]string) error {
	sourceID := strings.Join(strings.Split(id, "/")[:9], "/")
	source, err := c.migrationNamespace(ctx, sourceID)
	if err != nil {
		return err
	}
	normalized["_migration_source_creation"] = creationGeneration(source.data)
	normalized["_migration_configuration"] = migrationConfiguration(raw)
	targetID := text(object(raw["properties"])["targetNamespace"])
	if targetID == "" {
		return nil
	}
	canonicalTarget, _, err := parseID(targetID)
	if err != nil || sourceID == canonicalTarget {
		return serviceDenied("invalid_migration_namespace")
	}
	target, err := c.migrationNamespace(ctx, targetID)
	if err != nil {
		return err
	}
	normalized["_migration_target_creation"] = creationGeneration(target.data)
	addReference(refs, serviceBusNamespaceType, strings.ToLower(targetID))
	return nil
}

func (a *action) verifyMigrationTarget(ctx context.Context, planned asset.Asset) error {
	target := text(planned.Normalized["targetNamespace"])
	if target == "" {
		return nil
	}
	live, err := a.client.migrationNamespace(ctx, target)
	if err != nil {
		return err
	}
	if expected := text(planned.Normalized["_migration_target_creation"]); expected == "" || expected != creationGeneration(live.data) {
		return serviceDenied("migration_target_recreated")
	}
	return nil
}

func (a *action) migrationPreflight(ctx context.Context, planned asset.Asset, raw map[string]any, locks []any) error {
	if planned.Identity.NativeType != serviceBusMigrationType || !strings.EqualFold(planned.Identity.NativeID, a.id) || planned.Identity.Provider != asset.ProviderAzure {
		return serviceDenied("invalid_migration_identity")
	}
	if expected := text(planned.Normalized["_migration_configuration"]); expected == "" || expected != migrationConfiguration(raw) {
		return serviceDenied("migration_configuration_changed")
	}
	properties := object(raw["properties"])
	plannedTarget := text(planned.Normalized["targetNamespace"])
	if liveTarget := text(properties["targetNamespace"]); liveTarget != "" && !strings.EqualFold(plannedTarget, liveTarget) {
		return serviceDenied("migration_target_changed")
	}
	sourceID := strings.Join(strings.Split(a.id, "/")[:9], "/")
	if canonical, _, err := parseID(plannedTarget); plannedTarget != "" && (err != nil || canonical == sourceID) {
		return serviceDenied("invalid_migration_namespace")
	}
	for _, item := range []struct{ id, marker string }{{sourceID, "_migration_source_creation"}, {plannedTarget, "_migration_target_creation"}} {
		if item.id == "" {
			continue
		}
		live, err := a.client.migrationNamespace(ctx, item.id)
		if err != nil {
			return err
		}
		if expected := text(planned.Normalized[item.marker]); expected == "" || expected != creationGeneration(live.data) {
			return serviceDenied("migration_namespace_recreated")
		}
		kind, _ := findType(serviceBusNamespaceType)
		if reason := protectionReason(kind, live.data); reason != "" {
			return serviceDenied(reason)
		}
		if locked(strings.ToLower(item.id), locks) {
			return serviceDenied("azure_management_lock")
		}
		groupID := strings.Join(strings.Split(strings.ToLower(item.id), "/")[:5], "/")
		group, err := a.client.request(ctx, "GET", apiURL(groupID, resourcesVersion))
		if err != nil {
			return err
		}
		if !validResourceResponse(group, groupID, groupType) {
			return fmt.Errorf("Azure migration resource group identity mismatch")
		}
		if text(group.data["managedBy"]) != "" {
			return serviceDenied("azure_managed_resource_group")
		}
	}
	return nil
}

func migrationReady(properties map[string]any) bool {
	return strings.EqualFold(text(properties["provisioningState"]), "Succeeded") && strings.EqualFold(text(properties["migrationState"]), "Active") && replicationIdle(properties)
}

func (a *action) prepareMigration(ctx context.Context, request contracts.ActionRequest) (out contracts.ActionResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	live, err := a.client.request(ctx, "GET", a.endpoint)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !validResourceResponse(live, a.id, serviceBusMigrationType) {
		return contracts.ActionResult{}, fmt.Errorf("Azure migration preparation identity mismatch")
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if err := a.migrationPreflight(ctx, request.Asset, live.data, locks); err != nil {
		return contracts.ActionResult{}, err
	}
	if reason := protectionReason(a.kind, live.data); reason != "" {
		return contracts.ActionResult{}, serviceDenied(reason)
	}
	if locked(a.id, locks) {
		return contracts.ActionResult{}, serviceDenied("azure_management_lock")
	}
	properties := object(live.data["properties"])
	if !migrationReady(properties) {
		return contracts.ActionResult{Data: map[string]any{"phase": "await_migration", "target": a.id}, RetryAfter: 2 * time.Second}, nil
	}
	if text(properties["targetNamespace"]) == "" {
		return a.delete(ctx, request)
	}
	metadata, err := providerData()
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operation, ok := metadata.catalog.Operation("Azure.Microsoft.ServiceBus.MigrationConfigs_Revert")
	if !ok {
		return contracts.ActionResult{}, fmt.Errorf("Azure migration Revert operation is unavailable")
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
		headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey + ":revert-migration")
	}
	res, err := a.client.requestBody(ctx, bound.Method, bound.URL, bound.Body, headers)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if res.status != 200 {
		return contracts.ActionResult{}, fmt.Errorf("unexpected Azure migration Revert response")
	}
	if err := operationError(res); err != nil {
		return contracts.ActionResult{}, err
	}
	result, err := a.operationResult(res)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	result.Data["phase"], result.Data["target"], result.Data["operation"] = "revert_migration", a.id, result.ProviderOperationID
	return result, nil
}

func (a *action) waitMigrationPreparation(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (out contracts.WaitResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if a.kind.NativeType != serviceBusMigrationType || text(result.Data["target"]) != a.id || (text(result.Data["phase"]) == "revert_migration" && text(request.Asset.Normalized["targetNamespace"]) == "") {
		return contracts.WaitResult{}, fmt.Errorf("invalid Azure migration preparation phase")
	}
	result.ProviderOperationID = text(result.Data["operation"])
	poll, err := a.poll(ctx, result)
	if err != nil || !poll.Done {
		return poll, err
	}
	live, err := a.client.request(ctx, "GET", a.endpoint)
	if err != nil {
		// Revert keeps the configuration. A disappearing configuration during
		// preparation can mean an external commit; do not continue deleting.
		return contracts.WaitResult{}, err
	}
	if !validResourceResponse(live, a.id, serviceBusMigrationType) {
		return contracts.WaitResult{}, fmt.Errorf("Azure migration readback identity mismatch")
	}
	if err := operationError(live); err != nil {
		return contracts.WaitResult{}, err
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if err := a.migrationPreflight(ctx, request.Asset, live.data, locks); err != nil {
		return contracts.WaitResult{}, err
	}
	properties := object(live.data["properties"])
	if reason := protectionReason(a.kind, live.data); reason != "" {
		return contracts.WaitResult{}, serviceDenied(reason)
	}
	if !migrationReady(properties) || (text(result.Data["phase"]) == "revert_migration" && text(properties["targetNamespace"]) != "") {
		return contracts.WaitResult{State: "reverting_migration", RetryAfter: 2 * time.Second}, nil
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
