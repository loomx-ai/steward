package azure

import (
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const monitorReceiverTargetProof = "_monitor_receiver_target_identity"

func monitorARMTarget(value asset.Asset) bool {
	// Defender plan records expose service state and have no cleanup driver.
	// Their graph must not require unrelated incoming-deletion API permissions.
	if value.Identity.NativeType == defenderPricingType {
		return false
	}
	// Legacy component records retain case-sensitive opaque selectors. They are
	// not ARM scope resources, even though their service URL includes a component.
	if rbacResourceKind(value.Identity.NativeType) != "" {
		return false
	}
	// A Data Factory node is a self-hosted runtime registration. Its native
	// API returns nodeName, without an ARM resource or managed identity.
	if value.Identity.NativeType == dataFactoryNodeType {
		return false
	}
	if value.Identity.Provider != asset.ProviderAzure || insightsLegacyKind(value.Identity.NativeType).kind != "" || value.Identity.NativeType == insightsAnnotationType {
		return false
	}
	_, kind, err := parseID(value.Identity.NativeID)
	if monitorBudgetPath(value.Identity.NativeID) {
		_, _, kind, err = monitorResourceID(value.Identity.NativeID)
	}
	if value.Identity.NativeType == diagnosticSettingsType {
		_, _, kind, err = diagnosticResourceID(value.Identity.NativeID)
	}
	return err == nil && strings.EqualFold(kind, value.Identity.NativeType)
}

func (c *client) monitorControllerGroup(controller asset.Asset) (string, bool) {
	kind := controller.Identity.NativeType
	if kind != aksType && kind != monitorWorkspaceType && kind != applicationInsightsType {
		return "", false
	}
	group, err := controllerResourceGroup(c.subscription, kind, controller.Normalized)
	if kind == applicationInsightsType {
		group = text(object(controller.Normalized["_insights_workspace"])["managed_group"])
		err = nil
	}
	id, typ, identityErr := parseID(group)
	return id, err == nil && identityErr == nil && strings.EqualFold(typ, groupType) && strings.HasPrefix(id, c.root()+"/") && !inResourceGroup(controller.Identity.NativeID, id)
}

// A workspace's ARM name does not identify an ITSM receiver. Authenticate its
// native customer GUID with the scanned configuration so it remains usable
// after the workspace itself is absent, including after JSON recovery.
func (c *client) monitorReceiverTargetBinding(id, kind, location, customer, configuration string) string {
	return c.privateConfiguration(map[string]any{"id": id, "kind": kind, "location": location, "customerId": customer, "configuration": configuration})
}

func (c *client) monitorReceiverTargetCustomer(target asset.Asset) (string, error) {
	customer := strings.ToLower(text(target.Normalized["customerId"]))
	configuration := text(target.Normalized["_monitor_private_link_target_configuration"])
	if !uuidPattern.MatchString(customer) || target.Location == "" || configuration == "" || text(target.Normalized[monitorReceiverTargetProof]) != c.monitorReceiverTargetBinding(target.Identity.NativeID, target.Identity.NativeType, target.Location, customer, configuration) {
		return "", serviceDenied("monitor_receiver_target_identity_changed")
	}
	return customer, nil
}

func (c *client) monitorReferenceMatches(target asset.Asset, kind, reference string) (bool, error) {
	if kind == rbacPrincipalType {
		return c.rbacPrincipalMatches(target, reference)
	}
	if !strings.EqualFold(kind, target.Identity.NativeType) {
		return false, nil
	}
	if reference == target.Identity.NativeID {
		return true, nil
	}
	if !monitorReceiverSelector(kind, reference) {
		return false, nil // Other full ARM identities cannot denote this target.
	}
	if strings.EqualFold(kind, insightsWorkspaceType) {
		subscription, customer, err := monitorITSMWorkspace(strings.TrimPrefix(reference, "workspace-id:"))
		if err != nil || subscription != "" && subscription != c.subscription {
			return false, err
		}
		expected, err := c.monitorReceiverTargetCustomer(target)
		return expected == customer, err
	}
	parts := strings.Split(strings.SplitN(reference, ":", 2)[1], "/")
	scope := strings.Split(parts[0], "@")
	if scope[len(scope)-1] != c.subscription || len(scope) == 2 && scope[0] != strings.ToLower(c.tenant) {
		return false, nil
	}
	// ARM target identity supplies the namespace/hub names even when its native
	// namespace index is now empty. Matching the opaque name is conservative
	// across resource-group moves; it never invents a new destination ARM ID.
	id := strings.Split(target.Identity.NativeID, "/")
	if len(id) < 9 || id[8] != parts[1] {
		return false, nil
	}
	return len(parts) == 2 && len(id) == 9 || len(parts) == 3 && len(id) == 11 && id[10] == parts[2], nil
}
