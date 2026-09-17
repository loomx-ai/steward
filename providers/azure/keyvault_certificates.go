package azure

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Key Vault certificates exist only in the vault data plane. They are
// inventoried read-only with a separate vault.azure.net token: deletion is a
// data-plane soft delete that also removes the managed key and secret, and it is
// not offered. Only a certificate's own 404, or its vault's own 404, closes it.
const keyVaultCertificateType = "Microsoft.KeyVault/vaults/certificates"
const keyVaultCertificateSource = "keyvault-certificates"
const keyVaultType = "Microsoft.KeyVault/vaults"
const keyVaultCertificateList = "Azure.Microsoft.KeyVault.DataPlane.GetCertificates"
const keyVaultCertificateGet = "Azure.Microsoft.KeyVault.DataPlane.GetCertificate"

var keyVaultObjectName = regexp.MustCompile(`^[0-9A-Za-z-]{1,127}$`)
var keyVaultObjectVersion = regexp.MustCompile(`^[0-9a-f]{32}$`)

type keyVaultContext struct {
	id, endpoint, location string
}

// keyVaultCertificateIdentity maps the ARM-style certificate identity to its vault.
func (c *client) keyVaultCertificateIdentity(value string) (string, string, string, error) {
	id, typ, err := parseID(value)
	parts := strings.Split(id, "/")
	if err != nil || value != strings.TrimSpace(value) || typ != strings.ToLower(keyVaultCertificateType) || len(parts) != 11 || !strings.HasPrefix(id, c.root()+"/") || !keyVaultObjectName.MatchString(parts[10]) {
		return "", "", "", serviceDenied("invalid_keyvault_certificate_identity")
	}
	return id, strings.Join(parts[:9], "/"), parts[10], nil
}

func keyVaultOperation(id string) (catalog.Operation, error) {
	metadata, err := providerData()
	if err != nil {
		return catalog.Operation{}, err
	}
	operation, ok := metadata.catalog.Operation(id)
	if !ok || operation.Call == nil {
		return catalog.Operation{}, serviceDenied("keyvault_native_contract_missing")
	}
	return operation, nil
}

func (c *client) keyVaultRead(ctx context.Context, id string) (keyVaultContext, error) {
	canonical, typ, err := parseID(id)
	if err != nil || typ != strings.ToLower(keyVaultType) || len(strings.Split(canonical, "/")) != 9 || !strings.HasPrefix(canonical, c.root()+"/") {
		return keyVaultContext{}, serviceDenied("invalid_keyvault_identity")
	}
	operation, err := keyVaultOperation("Azure.Microsoft.KeyVault.Vaults_Get")
	if err != nil {
		return keyVaultContext{}, err
	}
	parts := strings.Split(canonical, "/")
	request, err := bindAzureREST(operation, map[string]any{"subscriptionId": c.subscription, "resourceGroupName": parts[4], "vaultName": parts[8]})
	if err != nil {
		return keyVaultContext{}, err
	}
	res, err := c.request(ctx, "GET", request.URL)
	if err != nil {
		return keyVaultContext{}, err
	}
	if res.status != 200 || !strings.EqualFold(text(res.data["id"]), canonical) || !strings.EqualFold(text(res.data["type"]), keyVaultType) {
		return keyVaultContext{}, serviceDenied("keyvault_identity_changed")
	}
	// The vault URI is the data-plane origin. Accept only the public-cloud
	// endpoint named by the vault itself, never a redirected or foreign host.
	endpoint := strings.TrimSuffix(strings.ToLower(text(object(res.data["properties"])["vaultUri"])), "/")
	location := resourceRegion(res.data)
	if endpoint != "https://"+parts[8]+".vault.azure.net" || !cosmosOperationRegion.MatchString(location) {
		return keyVaultContext{}, serviceDenied("invalid_keyvault_endpoint")
	}
	return keyVaultContext{id: canonical, endpoint: endpoint, location: location}, nil
}

// keyVaultURL admits only certificate list and current-version reads on the
// vault's own origin. Native next links carry an explicit :443 port.
func keyVaultURL(vault keyVaultContext, version string) func(string) error {
	return func(value string) error {
		u, err := url.Parse(value)
		if err != nil || u.Scheme != "https" || "https://"+u.Hostname() != vault.endpoint || u.Port() != "" && u.Port() != "443" || u.User != nil || u.Fragment != "" || u.RawPath != "" {
			return serviceDenied("keyvault_request_changed_vault")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != version {
			return serviceDenied("keyvault_request_changed_version")
		}
		for key, values := range query {
			if len(values) != 1 || key != "api-version" && key != "$skiptoken" && key != "maxresults" {
				return serviceDenied("keyvault_request_filtered")
			}
		}
		parts := strings.Split(u.Path, "/")
		if u.Path != "/certificates" && !(len(parts) == 4 && parts[1] == "certificates" && keyVaultObjectName.MatchString(parts[2]) && parts[3] == "") {
			return serviceDenied("keyvault_request_changed_path")
		}
		return nil
	}
}

// keyVaultObjectID parses a data-plane object identifier on the vault origin.
func keyVaultObjectID(vault keyVaultContext, value, collection string, versioned bool) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Scheme != "https" || "https://"+strings.ToLower(u.Hostname()) != vault.endpoint || u.Port() != "" && u.Port() != "443" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" {
		return "", serviceDenied("invalid_keyvault_object_origin")
	}
	parts := strings.Split(u.Path, "/")
	size := 3
	if versioned {
		size = 4
	}
	if len(parts) != size || parts[0] != "" || parts[1] != collection || !keyVaultObjectName.MatchString(parts[2]) || versioned && !keyVaultObjectVersion.MatchString(parts[3]) {
		return "", serviceDenied("invalid_keyvault_object_identity")
	}
	return strings.ToLower(parts[2]), nil
}

func (c *client) keyVaultCertificates(ctx context.Context, vault keyVaultContext) (map[string]bool, error) {
	operation, err := keyVaultOperation(keyVaultCertificateList)
	if err != nil {
		return nil, err
	}
	request, err := bindAzureREST(operation, map[string]any{"vaultBaseUrl": vault.endpoint})
	if err != nil {
		return nil, err
	}
	names := map[string]bool{}
	seen := map[string]bool{}
	for next := request.URL; next != ""; {
		if seen[next] || len(seen) > 10000 {
			return nil, serviceDenied("keyvault_pagination_repeated")
		}
		seen[next] = true
		res, err := c.requestUsing(ctx, "GET", next, nil, nil, keyVaultURL(vault, operation.Call.Version), c.keyVaultHTTP, false)
		if err != nil {
			return nil, err
		}
		values, ok := res.data["value"].([]any)
		if res.status != 200 || !ok {
			return nil, serviceDenied("keyvault_certificate_list_incomplete")
		}
		for _, value := range values {
			name, err := keyVaultObjectID(vault, text(object(value)["id"]), "certificates", false)
			if err != nil {
				return nil, err
			}
			if names[name] {
				return nil, serviceDenied("duplicate_keyvault_certificate")
			}
			names[name] = true
		}
		next = text(res.data["nextLink"])
		if next != "" {
			if u, err := url.Parse(next); err != nil || u.Path != "/certificates" {
				return nil, serviceDenied("keyvault_pagination_changed_collection")
			}
		}
	}
	return names, nil
}

func (c *client) keyVaultCertificateRead(ctx context.Context, vault keyVaultContext, name string) (response, error) {
	operation, err := keyVaultOperation(keyVaultCertificateGet)
	if err != nil {
		return response{}, err
	}
	request, err := bindAzureREST(operation, map[string]any{"vaultBaseUrl": vault.endpoint, "certificateName": name})
	if err != nil {
		return response{}, err
	}
	res, err := c.requestUsing(ctx, "GET", request.URL, nil, nil, keyVaultURL(vault, operation.Call.Version), c.keyVaultHTTP, false)
	if err != nil {
		return res, err
	}
	actual, err := keyVaultObjectID(vault, text(res.data["id"]), "certificates", true)
	if res.status != 200 || err != nil || actual != name || object(res.data["attributes"]) == nil {
		return response{}, serviceDenied("keyvault_certificate_response_invalid")
	}
	return res, nil
}

func keyVaultTime(value any) any {
	number, ok := value.(json.Number)
	if !ok {
		return nil
	}
	seconds, err := strconv.ParseInt(number.String(), 10, 64)
	if err != nil {
		return nil
	}
	return time.Unix(seconds, 0).UTC().Format(time.RFC3339)
}

func (r *Runtime) keyVaultCertificateItem(c *client, vault keyVaultContext, name string, data map[string]any) contracts.InventoryItem {
	id := vault.id + "/certificates/" + name
	attributes := object(data["attributes"])
	state := "disabled"
	if attributes["enabled"] == true {
		state = "enabled"
	}
	parts := strings.Split(id, "/")
	normalized := map[string]any{
		"_inventory_source": keyVaultCertificateSource, "name": parts[10], "state": state, "subscriptionId": c.subscription, "resourceGroup": parts[4],
		"vaultId": vault.id, referenceKey(keyVaultType): []string{vault.id},
		"enabled": attributes["enabled"] == true, "notBefore": keyVaultTime(attributes["nbf"]), "expires": keyVaultTime(attributes["exp"]),
		"recoveryLevel": text(attributes["recoveryLevel"]), "thumbprint": text(data["x5t"]),
		"cleanup_protected": true, "cleanup_protection_reason": "keyvault_certificate_data_plane_read_only",
	}
	// A certificate owns a managed key of the same name, which ARM lists as a key.
	if text(data["kid"]) != "" {
		normalized[referenceKey("Microsoft.KeyVault/vaults/keys")] = []string{vault.id + "/keys/" + name}
	}
	safeAttributes := map[string]any{}
	for _, key := range []string{"enabled", "nbf", "exp", "created", "updated", "recoverableDays", "recoveryLevel"} {
		if value, ok := attributes[key]; ok {
			safeAttributes[key] = value
		}
	}
	raw := map[string]any{"id": id, "type": keyVaultCertificateType, "name": name, "attributes": safeAttributes, "x5t": text(data["x5t"]), "contentType": text(data["contentType"])}
	if issuer := text(object(object(data["policy"])["issuer"])["name"]); issuer != "" {
		raw["issuer"] = issuer
		normalized["issuer"] = issuer
	}
	actionable := false
	return contracts.InventoryItem{
		NativeID: id, NativeType: keyVaultCertificateType, ResourceKind: r.resourceKind(keyVaultCertificateType), Name: name, State: state, Location: vault.location,
		Scope:      contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: vault.location, Name: vault.location, Location: vault.location},
		Normalized: normalized, Raw: raw, NativeAliases: []string{id}, Actionable: &actionable,
	}
}

func (r *Runtime) listKeyVaultCertificates(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.Source != keyVaultCertificateSource || request.ResourceKind == nil || !strings.EqualFold(request.ResourceKind.NativeType, keyVaultCertificateType) || request.Cursor != "" || request.NetworkTarget != nil || len(request.Options) != 0 ||
		!(request.Scope.Kind == asset.ScopeSubscription && strings.EqualFold(request.Scope.NativeID, c.subscription) || request.Scope.Kind == asset.ScopeRegion && cosmosOperationRegion.MatchString(request.Scope.NativeID)) {
		return batch, serviceDenied("invalid_keyvault_certificate_inventory_request")
	}
	known := map[string]map[string]bool{}
	for _, value := range request.KnownNativeIDs {
		id, vault, name, err := c.keyVaultCertificateIdentity(value)
		if err != nil || known[vault][name] || id != vault+"/certificates/"+name {
			return batch, serviceDenied("invalid_keyvault_certificate_known_identity")
		}
		if known[vault] == nil {
			known[vault] = map[string]bool{}
		}
		known[vault][name] = true
	}
	operation, err := keyVaultOperation("Azure.Microsoft.KeyVault.Vaults_ListBySubscription")
	if err != nil {
		return batch, err
	}
	collection := c.root() + "/providers/Microsoft.KeyVault/vaults"
	vaults := map[string]bool{}
	pages := map[string]bool{}
	for next := apiURL(collection, operation.Call.Version); next != ""; {
		if pages[next] {
			return batch, serviceDenied("keyvault_pagination_repeated")
		}
		pages[next] = true
		values, following, _, err := c.listPageResult(ctx, next, collection)
		if err != nil {
			return batch, err
		}
		for _, value := range values {
			id, typ, err := parseID(text(object(value)["id"]))
			if err != nil || typ != strings.ToLower(keyVaultType) || vaults[id] {
				return batch, serviceDenied("keyvault_list_invalid")
			}
			vaults[id] = true
		}
		next = following
	}
	for vault := range known {
		vaults[vault] = true
	}
	batch = contracts.InventoryBatch{Items: []contracts.InventoryItem{}, Complete: true}
	for _, id := range slices.Sorted(maps.Keys(vaults)) {
		vault, err := c.keyVaultRead(ctx, id)
		if isNotFound(err) && known[id] != nil && keyVaultOwnAbsence(err) {
			// Deleting a vault removes its objects from the live data plane.
			for _, name := range slices.Sorted(maps.Keys(known[id])) {
				batch.AbsentNativeIDs = append(batch.AbsentNativeIDs, id+"/certificates/"+name)
			}
			continue
		}
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		if request.Scope.Kind == asset.ScopeRegion && !strings.EqualFold(vault.location, request.Scope.NativeID) {
			continue
		}
		names, err := c.keyVaultCertificates(ctx, vault)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		for name := range known[id] {
			names[name] = names[name] || false
		}
		for _, name := range slices.Sorted(maps.Keys(names)) {
			res, err := c.keyVaultCertificateRead(ctx, vault, name)
			if isNotFound(err) && known[id][name] && !names[name] {
				batch.AbsentNativeIDs = append(batch.AbsentNativeIDs, id+"/certificates/"+name)
				continue
			}
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			batch.Items = append(batch.Items, r.keyVaultCertificateItem(c, vault, name, res.data))
		}
	}
	return batch, nil
}

func keyVaultOwnAbsence(err error) bool {
	var call *contracts.ProviderCallError
	return errors.As(err, &call) && call.Provider.Code == "ResourceNotFound"
}
