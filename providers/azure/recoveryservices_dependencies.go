package azure

import (
	"context"
	"errors"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const recoveryGuardProxy = recoveryServicesVault + "/backupResourceGuardProxies"
const recoveryBackupPolicy = recoveryServicesVault + "/backupPolicies"
const recoveryDeleteProtection = recoveryServicesItem + "/delete"

// These are read-only action dependencies, not additional inventory kinds.
func (c *client) recoveryDependencyIdentity(value, kind string) (string, error) {
	id, typ, err := parseID(value)
	if err != nil || value != strings.TrimSpace(value) || !slices.Contains([]string{recoveryGuardProxy, recoveryBackupPolicy}, kind) || typ != strings.ToLower(kind) || len(strings.Split(id, "/")) != 11 {
		return "", serviceDenied("invalid_recovery_dependency_identity")
	}
	if _, err = c.recoveryServicesIdentity(redisParentID(id), recoveryServicesVault); err != nil {
		return "", err
	}
	return id, nil
}

// Some official guard responses use an internal /backupmanagement identity.
// Bind that one documented representation to the already-known vault and name;
// never send a request to the internal ID or use it to select a different vault.
func (c *client) recoveryGuardMetadata(raw map[string]any, vault string) (string, error) {
	if canonical, err := c.recoveryServicesIdentity(vault, recoveryServicesVault); err != nil || canonical != vault {
		return "", serviceDenied("invalid_recovery_guard_vault")
	}
	name := text(raw["name"])
	id, err := c.recoveryDependencyIdentity(vault+"/backupResourceGuardProxies/"+name, recoveryGuardProxy)
	p := object(raw["properties"])
	internal := "/backupmanagement/resources/" + last(vault) + "/backupResourceGuardProxies/" + name
	if err != nil || !strings.EqualFold(text(raw["type"]), recoveryGuardProxy) || p == nil || !(strings.EqualFold(text(raw["id"]), id) || strings.EqualFold(text(raw["id"]), internal)) {
		return "", serviceDenied("invalid_recovery_guard_metadata")
	}
	guard, kind, err := parseID(text(p["resourceGuardResourceId"]))
	if err != nil || kind != "microsoft.dataprotection/resourceguards" || len(strings.Split(guard, "/")) != 9 {
		return "", serviceDenied("invalid_recovery_guard_reference")
	}
	details, ok := p["resourceGuardOperationDetails"].([]any)
	if !ok || len(details) > 1000 {
		return "", serviceDenied("invalid_recovery_guard_operations")
	}
	seen := map[string]bool{}
	for _, value := range details {
		detail := object(value)
		operation := text(detail["vaultCriticalOperation"])
		request := text(detail["defaultResourceRequest"])
		if operation == "" || operation != strings.TrimSpace(operation) || seen[strings.ToLower(operation)] || strings.ContainsAny(operation, "\x00\r\n\t") {
			return "", serviceDenied("invalid_recovery_guard_operation")
		}
		seen[strings.ToLower(operation)] = true
		requestID, _, err := parseID(request)
		if err != nil || len(strings.Split(requestID, "/")) != 11 {
			return "", serviceDenied("invalid_recovery_guard_operation_request")
		}
		if strings.EqualFold(operation, recoveryDeleteProtection) && redisParentID(requestID) != guard {
			return "", serviceDenied("recovery_guard_delete_request_scope_changed")
		}
	}
	return id, nil
}

func (c *client) recoveryGuardProxies(ctx context.Context, vault string, known map[string]any) (map[string]any, error) {
	owner, err := c.recoveryServicesIdentity(vault, recoveryServicesVault)
	if err != nil || owner != vault {
		return nil, serviceDenied("invalid_recovery_guard_vault")
	}
	ids := map[string]bool{}
	for id := range known {
		canonical, err := c.recoveryDependencyIdentity(id, recoveryGuardProxy)
		if err != nil || canonical != id || redisParentID(id) != vault {
			return nil, serviceDenied("invalid_recovery_guard_hint")
		}
		ids[id] = true
	}
	path := vault + "/backupResourceGuardProxies"
	next := apiURL(path, recoveryServicesBackupVersion)
	seen := map[string]bool{}
	listed := map[string]map[string]any{}
	for next != "" {
		u, err := url.Parse(next)
		if err != nil || seen[next] {
			return nil, serviceDenied("invalid_recovery_guard_page")
		}
		q, err := url.ParseQuery(u.RawQuery)
		if err != nil || q.Get("api-version") != recoveryServicesBackupVersion {
			return nil, serviceDenied("invalid_recovery_guard_page_version")
		}
		for key, values := range q {
			if len(values) != 1 || !slices.Contains([]string{"api-version", "$skiptoken", "$skipToken", "skipToken"}, key) {
				return nil, serviceDenied("filtered_recovery_guard_page")
			}
		}
		seen[next] = true
		values, following, res, err := c.listPageResult(ctx, next, path)
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if len(res.header.Values("Location"))+len(res.header.Values("Azure-AsyncOperation"))+len(res.header.Values("Operation-Location")) != 0 {
			return nil, serviceDenied("incomplete_recovery_guard_page")
		}
		for _, value := range values {
			raw := object(value)
			id, err := c.recoveryGuardMetadata(raw, vault)
			if err != nil {
				return nil, err
			}
			if listed[id] != nil {
				return nil, serviceDenied("duplicate_recovery_guard")
			}
			listed[id], ids[id] = raw, true
		}
		next = following
	}
	result := map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(ids)) {
		res, err := c.request(ctx, "GET", apiURL(id, recoveryServicesBackupVersion))
		if isNotFound(err) && listed[id] == nil {
			var call *contracts.ProviderCallError
			if errors.As(err, &call) && slices.Contains([]string{"ParentResourceNotFound", "ResourceGroupNotFound", "SubscriptionNotFound"}, call.Provider.Code) {
				return nil, contracts.DependencyReadError(err)
			}
			continue
		}
		if err != nil {
			return nil, contracts.DependencyReadError(err)
		}
		if res.status != 200 || res.data["error"] != nil || len(res.header.Values("Location"))+len(res.header.Values("Azure-AsyncOperation"))+len(res.header.Values("Operation-Location")) != 0 {
			return nil, serviceDenied("incomplete_recovery_guard_read")
		}
		actual, err := c.recoveryGuardMetadata(res.data, vault)
		if err != nil || actual != id {
			return nil, serviceDenied("recovery_guard_read_identity_changed")
		}
		if listed[id] != nil {
			before, after := maps.Clone(listed[id]), maps.Clone(res.data)
			before["id"], after["id"] = id, id
			if !nativeConfigurationContains(before, after) {
				return nil, serviceDenied("recovery_guard_changed_during_read")
			}
		}
		requiresAuthorization := false
		for _, value := range array(object(res.data["properties"])["resourceGuardOperationDetails"]) {
			if strings.EqualFold(text(object(value)["vaultCriticalOperation"]), recoveryDeleteProtection) {
				requiresAuthorization = true
			}
		}
		result[id] = map[string]any{"configuration": c.privateConfiguration(hybridComputeChildSnapshot(res.data)), "delete_requires_authorization": requiresAuthorization}
	}
	return result, nil
}

func (c *client) recoveryPolicyRead(ctx context.Context, vault, id string) (map[string]any, error) {
	canonical, err := c.recoveryDependencyIdentity(id, recoveryBackupPolicy)
	if err != nil || id != canonical || redisParentID(id) != vault {
		return nil, serviceDenied("invalid_recovery_policy_scope")
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	operation, ok := metadata.catalog.Operation("Azure.Microsoft.RecoveryServices.ProtectionPolicies_Get")
	if !ok {
		return nil, serviceDenied("missing_recovery_policy_operation")
	}
	parts := strings.Split(id, "/")
	bound, err := bindAzureREST(operation, map[string]any{"subscriptionId": c.subscription, "resourceGroupName": parts[4], "vaultName": parts[8], "policyName": parts[10]})
	if err != nil {
		return nil, err
	}
	res, err := c.request(ctx, bound.Method, bound.URL)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if res.status != 200 || res.data["error"] != nil || len(res.header.Values("Location"))+len(res.header.Values("Azure-AsyncOperation"))+len(res.header.Values("Operation-Location")) != 0 {
		return nil, serviceDenied("incomplete_recovery_policy_read")
	}
	actual, err := c.recoveryDependencyIdentity(text(res.data["id"]), recoveryBackupPolicy)
	if err != nil || actual != id || !strings.EqualFold(text(res.data["name"]), last(id)) || !strings.EqualFold(text(res.data["type"]), recoveryBackupPolicy) || text(object(res.data["properties"])["backupManagementType"]) == "" {
		return nil, serviceDenied("invalid_recovery_policy_metadata")
	}
	return res.data, nil
}
