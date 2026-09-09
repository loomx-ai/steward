package azure

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	appSiteType            = "Microsoft.Web/sites"
	appSlotType            = appSiteType + "/slots"
	appFunctionType        = appSiteType + "/functions"
	appSlotFunctionType    = appSlotType + "/functions"
	appCertificateType     = "Microsoft.Web/certificates"
	appSiteCertificateType = appSiteType + "/certificates"
	appSlotCertificateType = appSlotType + "/certificates"
	appBindingType         = appSiteType + "/hostNameBindings"
	appSlotBindingType     = appSlotType + "/hostNameBindings"
)

func isAppServiceType(kind string) bool {
	return slices.Contains([]string{appSiteType, appSlotType, appFunctionType, appSlotFunctionType, appCertificateType, appSiteCertificateType, appSlotCertificateType, appBindingType, appSlotBindingType}, kind)
}

func isAppFunction(kind string) bool { return kind == appFunctionType || kind == appSlotFunctionType }
func isAppCertificate(kind string) bool {
	return kind == appCertificateType || kind == appSiteCertificateType || kind == appSlotCertificateType
}
func isAppBinding(kind string) bool { return kind == appBindingType || kind == appSlotBindingType }

func appFunctionHost(raw map[string]any) (bool, error) {
	kind := text(raw["kind"])
	if kind == "" {
		return false, serviceDenied("app_service_kind_missing")
	}
	return slices.ContainsFunc(strings.Split(kind, ","), func(value string) bool { return strings.EqualFold(strings.TrimSpace(value), "functionapp") }), nil
}

// Function files/configuration and certificate material remain in the private
// digest. Only shallow, documented site runtime fields are excluded.
func appServiceSnapshot(kind string, raw map[string]any) map[string]any {
	payload, _ := json.Marshal(raw)
	var snapshot map[string]any
	json.Unmarshal(payload, &snapshot)
	snapshot["id"] = strings.ToLower(text(raw["id"]))
	snapshot["name"] = last(text(snapshot["id"]))
	delete(snapshot, "type")
	delete(snapshot, "etag")
	if isAppFunction(kind) || isAppBinding(kind) {
		delete(snapshot, "location")
	}
	for _, field := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(snapshot["systemData"]), field)
	}
	props := object(snapshot["properties"])
	delete(props, "provisioningState")
	if kind == appSiteType || kind == appSlotType {
		for _, field := range []string{"state", "availabilityState", "usageState", "lastModifiedTimeUtc", "outboundIpAddresses", "possibleOutboundIpAddresses", "inProgressOperationId"} {
			delete(props, field)
		}
	}
	if isAppCertificate(kind) {
		delete(props, "keyVaultSecretStatus")
	}
	return snapshot
}

func appServiceConfiguration(kind string, raw map[string]any) string {
	return serviceParentConfiguration(kind, appServiceSnapshot(kind, raw))
}

func appServiceIncarnation(planned asset.Asset, live map[string]any) error {
	if !isAppServiceType(planned.Identity.NativeType) {
		return nil
	}
	if expected := text(planned.Normalized["_app_service_configuration"]); expected == "" || expected != appServiceConfiguration(planned.Identity.NativeType, live) {
		return serviceDenied("app_service_configuration_changed")
	}
	return nil
}

func (c *client) appServiceParent(ctx context.Context, id string) (map[string]any, error) {
	parts := strings.Split(strings.ToLower(id), "/")
	parentID, kind, err := parseID(strings.Join(parts[:len(parts)-2], "/"))
	if err != nil || (kind != strings.ToLower(appSiteType) && kind != strings.ToLower(appSlotType)) {
		return nil, serviceDenied("invalid_app_service_parent")
	}
	parentKind, _ := findType(kind)
	endpoint, err := c.resourceURL(parentKind, parentID)
	if err != nil {
		return nil, err
	}
	live, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(live, parentID, parentKind.NativeType) {
		return nil, serviceDenied("invalid_app_service_parent")
	}
	return live.data, nil
}

func appDefaultBinding(id string, parent map[string]any) bool {
	name := last(id)
	props := object(parent["properties"])
	if strings.EqualFold(name, text(props["defaultHostName"])) {
		return true
	}
	for _, row := range array(props["hostNameSslStates"]) {
		if object(row)["hostType"] == "Repository" && strings.EqualFold(name, text(object(row)["name"])) {
			return true
		}
	}
	return false
}

func (c *client) appServiceInventory(ctx context.Context, id, kind string, raw, normalized map[string]any) error {
	if !isAppServiceType(kind) {
		return nil
	}
	normalized["_app_service_configuration"] = appServiceConfiguration(kind, raw)
	normalized["_app_service_private_configuration"] = c.privateConfiguration(appServiceSnapshot(kind, raw))
	if kind == appSiteType || kind == appCertificateType {
		return nil
	}
	parent, err := c.appServiceParent(ctx, id)
	if err != nil {
		return err
	}
	_, parentType, _ := parseID(text(parent["id"]))
	parentKind, _ := findType(parentType)
	normalized["_app_service_parent_configuration"] = appServiceConfiguration(parentKind.NativeType, parent)
	normalized["_app_service_parent_private_configuration"] = c.privateConfiguration(appServiceSnapshot(parentKind.NativeType, parent))
	if parentKind.NativeType == appSlotType {
		root, err := c.appServiceParent(ctx, text(parent["id"]))
		if err != nil {
			return err
		}
		normalized["_app_service_root_configuration"] = appServiceConfiguration(appSiteType, root)
		normalized["_app_service_root_private_configuration"] = c.privateConfiguration(appServiceSnapshot(appSiteType, root))
	}
	if isAppFunction(kind) {
		functionHost, err := appFunctionHost(parent)
		if err != nil {
			return err
		}
		if !functionHost {
			return serviceDenied("function_requires_function_app")
		}
		if ref := object(raw["properties"])["function_app_id"]; ref != nil && !strings.EqualFold(text(ref), text(parent["id"])) {
			return serviceDenied("function_parent_disagrees")
		}
	}
	if isAppBinding(kind) && appDefaultBinding(id, parent) {
		normalized["cleanup_controller_only"] = true
		normalized["cleanup_protection_reason"] = "azure_app_service_default_hostname"
	}
	return nil
}

func (c *client) appServiceChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	children := slices.Clone(serviceChildKinds(parent.NativeType))
	functionHost, err := appFunctionHost(raw)
	if err != nil {
		return nil, err
	}
	if !functionHost {
		children = slices.DeleteFunc(children, isAppFunction)
	}
	return c.nativeServiceChildren(ctx, parent, raw, children)
}

func (a *action) appServicePreflight(ctx context.Context, planned asset.Asset, live map[string]any) error {
	kind := planned.Identity.NativeType
	if !isAppServiceType(kind) {
		return nil
	}
	if err := appServiceIncarnation(planned, live); err != nil {
		return err
	}
	if kind != appSiteType && kind != appCertificateType {
		parent, err := a.client.appServiceParent(ctx, a.id)
		if err != nil {
			return err
		}
		_, parentType, _ := parseID(text(parent["id"]))
		parentKind, _ := findType(parentType)
		if expected := text(planned.Normalized["_app_service_parent_configuration"]); expected == "" || expected != appServiceConfiguration(parentKind.NativeType, parent) {
			return serviceDenied("app_service_parent_changed")
		}
		if expected := text(planned.Normalized["_app_service_parent_private_configuration"]); expected == "" || expected != a.client.privateConfiguration(appServiceSnapshot(parentKind.NativeType, parent)) {
			return serviceDenied("app_service_parent_changed")
		}
		if parentKind.NativeType == appSlotType {
			root, err := a.client.appServiceParent(ctx, text(parent["id"]))
			if err != nil {
				return err
			}
			if expected := text(planned.Normalized["_app_service_root_configuration"]); expected == "" || expected != appServiceConfiguration(appSiteType, root) {
				return serviceDenied("app_service_root_changed")
			}
			if expected := text(planned.Normalized["_app_service_root_private_configuration"]); expected == "" || expected != a.client.privateConfiguration(appServiceSnapshot(appSiteType, root)) {
				return serviceDenied("app_service_root_changed")
			}
		}
		if isAppBinding(kind) && appDefaultBinding(a.id, parent) {
			return serviceDenied("azure_app_service_default_hostname")
		}
	}
	if isAppCertificate(kind) {
		return a.client.appCertificateUnused(ctx, planned, live)
	}
	return nil
}

// TLS bindings can identify certificates only by thumbprint. A live match
// blocks deletion; it does not authorize deleting potentially unrelated
// hostname bindings or claim their ownership. Explicit unlinking requires a rescan.
func (c *client) appCertificateUnused(ctx context.Context, planned asset.Asset, live map[string]any) error {
	thumbprint := text(object(live["properties"])["thumbprint"])
	if len(thumbprint) != 40 {
		return serviceDenied("app_certificate_thumbprint_missing")
	}
	for _, char := range strings.ToLower(thumbprint) {
		if !strings.ContainsRune("0123456789abcdef", char) {
			return serviceDenied("app_certificate_thumbprint_invalid")
		}
	}
	metadata, err := providerData()
	if err != nil {
		return err
	}
	runtime := &Runtime{bundle: metadata.bundle}
	definition, _ := runtime.productDefinition(appSiteType)
	bound, err := c.bindProductList(definition.Discovery.List, c.subscription, contracts.InventoryItem{})
	if err != nil {
		return err
	}
	u, _ := url.Parse(bound.URL)
	for pass := 0; pass < 2; pass++ {
		sites, err := c.listAllURL(ctx, bound.URL, u.Path)
		if err != nil {
			return err
		}
		seen := map[string]bool{}
		for _, value := range sites {
			raw := object(value)
			id, nativeType, err := parseID(text(raw["id"]))
			if err != nil || !strings.EqualFold(nativeType, appSiteType) || seen[id] || !validResponseType(appSiteType, text(raw["type"])) {
				return serviceDenied("invalid_certificate_referrer")
			}
			seen[id] = true
			kind, _ := findType(appSiteType)
			endpoint, err := c.resourceURL(kind, id)
			if err != nil {
				return err
			}
			current, err := c.request(ctx, "GET", endpoint)
			if err != nil {
				return err
			}
			if !validResourceResponse(current, id, appSiteType) {
				return serviceDenied("invalid_certificate_referrer")
			}
			if err := serviceListedIncarnation(raw, current.data); err != nil {
				return err
			}
			if err := c.appCertificateSiteUnused(ctx, asset.Identity{NativeID: id, NativeType: appSiteType}, current.data, planned.Identity.NativeID, thumbprint); err != nil {
				return err
			}
		}
	}
	kind, _ := findType(planned.Identity.NativeType)
	endpoint, err := c.resourceURL(kind, planned.Identity.NativeID)
	if err != nil {
		return err
	}
	latest, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return err
	}
	if !validResourceResponse(latest, planned.Identity.NativeID, kind.NativeType) {
		return serviceDenied("invalid_app_certificate_identity")
	}
	if err := appServiceIncarnation(planned, latest.data); err != nil {
		return err
	}
	if err := c.servicePrivateIncarnation(planned, latest.data); err != nil {
		return err
	}
	return nil
}

func (c *client) appCertificateSiteUnused(ctx context.Context, parent asset.Identity, raw map[string]any, certificateID, thumbprint string) error {
	states, ok := object(raw["properties"])["hostNameSslStates"].([]any)
	if !ok {
		return serviceDenied("app_certificate_bindings_incomplete")
	}
	check := func(props map[string]any, optionalTLS bool) error {
		if ref := props["certificateResourceId"]; ref != nil {
			id, kind, err := parseID(text(ref))
			typed, known := findType(kind)
			if err != nil || !known || !isAppCertificate(typed.NativeType) {
				return serviceDenied("invalid_app_certificate_reference")
			}
			if strings.EqualFold(id, certificateID) {
				return serviceDenied("app_certificate_still_bound")
			}
		}
		_, statePresent := props["sslState"]
		_, thumbprintPresent := props["thumbprint"]
		// Microsoft CLI records omit both fields on unbound hostname resources.
		// Site hostNameSslStates still supplies explicit Disabled states.
		if optionalTLS && !statePresent && !thumbprintPresent {
			return nil
		}
		state := text(props["sslState"])
		if state != "Disabled" && state != "SniEnabled" && state != "IpBasedEnabled" {
			return serviceDenied("app_certificate_bindings_incomplete")
		}
		if state != "Disabled" && text(props["thumbprint"]) == "" {
			return serviceDenied("app_certificate_bindings_incomplete")
		}
		if strings.EqualFold(text(props["thumbprint"]), thumbprint) {
			return serviceDenied("app_certificate_still_bound")
		}
		return nil
	}
	for _, value := range states {
		if err := check(object(value), false); err != nil {
			return err
		}
	}
	kinds := []string{appBindingType, appSlotType}
	if parent.NativeType == appSlotType {
		kinds = []string{appSlotBindingType}
	}
	children, err := c.nativeServiceChildren(ctx, parent, raw, kinds)
	if err != nil {
		return err
	}
	for _, child := range children {
		if child.kind == appSlotType {
			if err := c.appCertificateSiteUnused(ctx, asset.Identity{NativeID: child.id, NativeType: child.kind}, child.data, certificateID, thumbprint); err != nil {
				return err
			}
		} else if err := check(object(child.data["properties"]), true); err != nil {
			return err
		}
	}
	return nil
}
