package azure

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const (
	cognitiveType                  = "Microsoft.CognitiveServices/accounts"
	cognitiveHostType              = cognitiveType + "/capabilityHosts"
	cognitivePlanType              = cognitiveType + "/commitmentPlans"
	cognitiveConnectionType        = cognitiveType + "/connections"
	cognitiveDefenderType          = cognitiveType + "/defenderForAISettings"
	cognitiveDeploymentType        = cognitiveType + "/deployments"
	cognitiveEncryptionType        = cognitiveType + "/encryptionScopes"
	cognitiveNetworkType           = cognitiveType + "/managedNetworks"
	cognitiveOutboundType          = cognitiveNetworkType + "/outboundRules"
	cognitivePerimeterType         = cognitiveType + "/networkSecurityPerimeterConfigurations"
	cognitivePECType               = cognitiveType + "/privateEndpointConnections"
	cognitiveProjectType           = cognitiveType + "/projects"
	cognitiveApplicationType       = cognitiveProjectType + "/applications"
	cognitiveAgentType             = cognitiveApplicationType + "/agentDeployments"
	cognitiveProjectHostType       = cognitiveProjectType + "/capabilityHosts"
	cognitiveProjectConnectionType = cognitiveProjectType + "/connections"
	cognitiveBlocklistType         = cognitiveType + "/raiBlocklists"
	cognitiveBlockitemType         = cognitiveBlocklistType + "/raiBlocklistItems"
	cognitivePolicyType            = cognitiveType + "/raiPolicies"
	cognitiveToolType              = cognitiveType + "/raiToolLabels"
	cognitiveTopicType             = cognitiveType + "/raitopics"
	cognitiveSharedPlanType        = "Microsoft.CognitiveServices/commitmentPlans"
	cognitiveAssociationType       = cognitiveSharedPlanType + "/accountAssociations"
)

func cognitiveKind(kind string) string {
	for _, value := range []string{cognitiveType, cognitiveHostType, cognitivePlanType, cognitiveConnectionType, cognitiveDefenderType, cognitiveDeploymentType, cognitiveEncryptionType, cognitiveNetworkType, cognitiveOutboundType, cognitivePerimeterType, cognitivePECType, cognitiveProjectType, cognitiveApplicationType, cognitiveAgentType, cognitiveProjectHostType, cognitiveProjectConnectionType, cognitiveBlocklistType, cognitiveBlockitemType, cognitivePolicyType, cognitiveToolType, cognitiveTopicType, cognitiveSharedPlanType, cognitiveAssociationType} {
		if strings.EqualFold(value, kind) {
			return value
		}
	}
	return ""
}
func isCognitiveType(kind string) bool { return cognitiveKind(kind) != "" }

// Native child indexes are reconciled separately. Keep configuration, creation
// identifiers, ownership and credentials; omit only transient operation fields.
func cognitiveSnapshot(kind string, raw map[string]any) map[string]any {
	encoded, _ := json.Marshal(raw)
	var result map[string]any
	json.Unmarshal(encoded, &result)
	result["id"] = strings.ToLower(text(raw["id"]))
	result["name"] = last(text(result["id"]))
	delete(result, "type")
	delete(result, "etag")
	for _, field := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(result["systemData"]), field)
	}
	kind = cognitiveKind(kind)
	if kind != cognitiveType && kind != cognitiveSharedPlanType {
		delete(result, "location")
	}
	props := object(result["properties"])
	delete(props, "provisioningState")
	if kind == cognitiveType {
		for _, field := range []string{"privateEndpointConnections", "commitmentPlanAssociations", "associatedProjects", "defaultProject"} {
			delete(props, field)
		}
	}
	if kind == cognitiveNetworkType {
		network := object(props["managedNetwork"])
		for _, field := range []string{"outboundRules", "status", "provisioningState"} {
			delete(network, field)
		}
	}
	if kind == cognitiveOutboundType {
		delete(props, "status")
		delete(props, "errorInformation")
	}
	return result
}
func cognitiveConfiguration(kind string, raw map[string]any) string {
	return serviceParentConfiguration(kind, cognitiveSnapshot(kind, raw))
}

// Proxy resources may omit their native location. Inventory can inherit a
// display location, but only the original GET value identifies the resource.
func cognitiveNativeLocation(raw map[string]any) string {
	return strings.ReplaceAll(strings.ToLower(text(raw["location"])), " ", "")
}
func cognitiveIncarnation(planned asset.Asset, live map[string]any) error {
	if !isCognitiveType(planned.Identity.NativeType) {
		return nil
	}
	if expected, ok := planned.Normalized["_cognitive_native_location"].(string); !ok || expected != cognitiveNativeLocation(live) {
		return serviceDenied("cognitive_location_changed")
	}
	if expected := text(planned.Normalized["_cognitive_configuration"]); expected == "" || expected != cognitiveConfiguration(planned.Identity.NativeType, live) {
		return serviceDenied("cognitive_configuration_changed")
	}
	if planned.Identity.NativeType == cognitiveType {
		return cognitiveAccountIndexIncarnation(planned.Normalized, live)
	}
	return nil
}
func (c *client) cognitiveResource(ctx context.Context, value string) (map[string]any, error) {
	id, kind, err := parseID(value)
	if err != nil || !isCognitiveType(kind) {
		return nil, serviceDenied("invalid_cognitive_resource")
	}
	return c.linkedResource(ctx, id)
}
func cognitiveAncestorIDs(value string) []string {
	id, kind, err := parseID(value)
	if err != nil || !isCognitiveType(kind) {
		return nil
	}
	var result []string
	for kind = cognitiveKind(kind); kind != cognitiveType && kind != cognitiveSharedPlanType; {
		id = redisParentID(id)
		_, kind, _ = parseID(id)
		kind = cognitiveKind(kind)
		if kind == "" {
			return nil
		}
		result = append(result, id)
	}
	return result
}
func (c *client) cognitiveInventory(ctx context.Context, id, kind string, raw, normalized map[string]any) error {
	if !isCognitiveType(kind) {
		return nil
	}
	normalized["_cognitive_configuration"] = cognitiveConfiguration(kind, raw)
	normalized["_cognitive_private_configuration"] = c.privateConfiguration(cognitiveSnapshot(kind, raw))
	normalized["_cognitive_native_location"] = cognitiveNativeLocation(raw)
	ancestors := map[string]any{}
	for _, ancestor := range cognitiveAncestorIDs(id) {
		live, err := c.cognitiveResource(ctx, ancestor)
		if err != nil {
			return err
		}
		_, parentKind, _ := parseID(ancestor)
		ancestors[ancestor] = map[string]any{"configuration": c.privateConfiguration(cognitiveSnapshot(parentKind, live)), "native_location": cognitiveNativeLocation(live), "account_indexes": cognitiveAccountIndexes(parentKind, live)}
	}
	normalized["_cognitive_ancestors"] = ancestors
	normalized["_cognitive_account_indexes"] = cognitiveAccountIndexes(kind, raw)
	refs, err := c.cognitiveReferenceIDs(ctx, id, kind, raw)
	if err != nil {
		return err
	}
	normalized["_cognitive_references"] = refs
	if targetID, err := cognitiveLinkedTarget(kind, raw); err != nil {
		return err
	} else if targetID != "" {
		target, err := c.linkedResource(ctx, targetID)
		if err != nil {
			return err
		}
		normalized["_cognitive_target_configuration"] = c.privateConfiguration(cognitiveTargetSnapshot(target))
	}
	return nil
}
func (c *client) cognitiveAncestors(ctx context.Context, id string, expected map[string]any, protection bool) error {
	ids := cognitiveAncestorIDs(id)
	if len(ids) != len(expected) {
		return serviceDenied("cognitive_ancestor_set_changed")
	}
	for _, ancestor := range ids {
		live, err := c.cognitiveResource(ctx, ancestor)
		if err != nil {
			return err
		}
		_, kind, _ := parseID(ancestor)
		binding := object(expected[ancestor])
		if location, ok := binding["native_location"].(string); !ok || location != cognitiveNativeLocation(live) {
			return serviceDenied("cognitive_ancestor_location_changed")
		}
		if text(binding["configuration"]) == "" || text(binding["configuration"]) != c.privateConfiguration(cognitiveSnapshot(kind, live)) {
			return serviceDenied("cognitive_ancestor_changed")
		}
		if strings.EqualFold(kind, cognitiveType) {
			if err := cognitiveAccountIndexIncarnation(map[string]any{"_cognitive_account_indexes": binding["account_indexes"]}, live); err != nil {
				return err
			}
		}
		if protection {
			if err := cognitiveProvisioningReady(kind, live); err != nil {
				return err
			}
			mapping, _ := findType(kind)
			if reason := protectionReason(mapping, live); reason != "" {
				return serviceDenied(reason)
			}
		}
	}
	return nil
}
func (a *action) cognitivePreflight(ctx context.Context, planned asset.Asset, raw map[string]any) error {
	return a.cognitiveResourcePreflight(ctx, planned, raw, true)
}
func (a *action) cognitiveResourcePreflight(ctx context.Context, planned asset.Asset, raw map[string]any, standalone bool) error {
	kind := a.kind.NativeType
	if !isCognitiveType(kind) {
		return nil
	}
	if err := cognitiveIncarnation(planned, raw); err != nil {
		return err
	}
	if err := a.client.cognitiveAncestors(ctx, a.id, object(planned.Normalized["_cognitive_ancestors"]), true); err != nil {
		return err
	}
	if kind == cognitiveNetworkType && last(a.id) != "default" {
		return serviceDenied("invalid_cognitive_managed_network")
	}
	if err := cognitiveProvisioningReady(kind, raw); err != nil {
		return err
	}
	if standalone {
		if err := a.cognitiveOutboundPreflight(ctx); err != nil {
			return err
		}
	}
	refs, err := a.client.cognitiveReferenceIDs(ctx, a.id, kind, raw)
	if err != nil {
		return err
	}
	if !slices.Equal(refs, stringValues(planned.Normalized["_cognitive_references"])) {
		return serviceDenied("cognitive_references_changed")
	}
	targetID, err := cognitiveLinkedTarget(kind, raw)
	if err != nil {
		return err
	}
	if targetID == "" {
		return nil
	}
	target, err := a.client.linkedResource(ctx, targetID)
	if err != nil {
		return err
	}
	expected := text(planned.Normalized["_cognitive_target_configuration"])
	if expected == "" || expected != a.client.privateConfiguration(cognitiveTargetSnapshot(target)) {
		return serviceDenied("cognitive_link_target_changed")
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return err
	}
	if err := a.client.linkedResourceProtection(ctx, targetID, target, locks); err != nil {
		return err
	}
	current, err := a.client.linkedResource(ctx, targetID)
	if err != nil {
		return err
	}
	if expected != a.client.privateConfiguration(cognitiveTargetSnapshot(current)) {
		return serviceDenied("cognitive_link_target_changed")
	}
	return nil
}
func cognitiveLinkedTarget(kind string, raw map[string]any) (string, error) {
	if kind == cognitiveOutboundType {
		switch object(raw["properties"])["type"] {
		case "PrivateEndpoint", "FQDN", "ServiceTag":
		default:
			return "", serviceDenied("invalid_cognitive_outbound_type")
		}
	}
	value := ""
	props := object(raw["properties"])
	if kind == cognitiveAssociationType {
		var err error
		value, err = cognitiveExactString(props["accountId"])
		if err != nil {
			return "", err
		}
	}
	if kind == cognitiveOutboundType && props["type"] == "PrivateEndpoint" {
		var err error
		value, err = cognitiveExactString(object(props["destination"])["serviceResourceId"])
		if err != nil {
			return "", err
		}
		group, err := cognitiveExactString(object(props["destination"])["subresourceTarget"])
		if err != nil || group == "" || strings.ContainsAny(group, "/\\%?#\x00\r\n") {
			return "", serviceDenied("invalid_cognitive_private_link_group")
		}
	}
	if value == "" {
		if kind == cognitiveAssociationType || (kind == cognitiveOutboundType && props["type"] == "PrivateEndpoint") {
			return "", serviceDenied("invalid_cognitive_link_target")
		}
		return "", nil
	}
	id, targetKind, err := parseID(value)
	if err != nil || kind == cognitiveAssociationType && !strings.EqualFold(targetKind, cognitiveType) {
		return "", serviceDenied("invalid_cognitive_link_target")
	}
	return id, nil
}

func cognitiveProtection(kind string, raw map[string]any) string {
	if isCognitiveType(kind) && protectedAzureTags(object(object(raw["properties"])["tags"])) {
		return "azure_protected_tag"
	}
	switch kind {
	case cognitiveConnectionType, cognitiveProjectConnectionType:
		props := object(raw["properties"])
		for _, field := range []string{"peRequirement", "peStatus"} {
			switch props[field] {
			case nil, "NotApplicable":
			case "NotRequired":
				if field != "peRequirement" {
					return "azure_cognitive_unknown_connection_network_state"
				}
			case "Inactive":
				if field != "peStatus" {
					return "azure_cognitive_unknown_connection_network_state"
				}
			case "Required", "Active":
				return "azure_cognitive_connection_private_endpoint_unmodeled"
			default:
				return "azure_cognitive_unknown_connection_network_state"
			}
		}
	case cognitiveDefenderType, cognitivePerimeterType:
		return "azure_cognitive_managed_configuration"
	case cognitivePolicyType:
		switch object(raw["properties"])["type"] {
		case "SystemManaged":
			return "azure_cognitive_managed_configuration"
		case "UserManaged":
		default:
			return "azure_cognitive_unknown_policy_type"
		}
	case cognitiveOutboundType:
		switch object(raw["properties"])["category"] {
		case "Required", "Dependency":
			return "azure_cognitive_managed_configuration"
		case "UserDefined", "Recommended":
		default:
			return "azure_cognitive_unknown_outbound_category"
		}
	}
	return ""
}

func cognitiveTargetSnapshot(raw map[string]any) map[string]any {
	if _, kind, err := parseID(text(raw["id"])); err == nil && isCognitiveType(kind) {
		return cognitiveSnapshot(kind, raw)
	}
	return searchTargetSnapshot(raw)
}

func cognitiveListQuery(u *url.URL) error {
	if !strings.EqualFold(last(u.Path), "connections") {
		return nil
	}
	_, kind, err := parseID(strings.TrimSuffix(u.Path, "/"+last(u.Path)))
	if err == nil && (strings.EqualFold(kind, cognitiveType) || strings.EqualFold(kind, cognitiveProjectType)) { // Collection parent.
		query := u.Query()
		if values := query["includeAll"]; len(values) != 1 || values[0] != "true" {
			return serviceDenied("cognitive_connections_incomplete_query")
		}
		if len(query["target"]) > 0 || len(query["category"]) > 0 {
			return serviceDenied("cognitive_connections_filtered_query")
		}
	}
	return nil
}
func cognitiveAccountIndexes(kind string, raw map[string]any) map[string]any {
	if !strings.EqualFold(kind, cognitiveType) {
		return nil
	}
	props := object(raw["properties"])
	return map[string]any{"defaultProject": props["defaultProject"], "associatedProjects": props["associatedProjects"]}
}
func cognitiveAccountIndexIncarnation(normalized, live map[string]any) error {
	expected := object(normalized["_cognitive_account_indexes"])
	props := object(live["properties"])
	if props["defaultProject"] != nil {
		current, err := cognitiveExactString(props["defaultProject"])
		if err != nil {
			return err
		}
		if current != "" && !strings.EqualFold(current, text(expected["defaultProject"])) {
			return serviceDenied("cognitive_default_project_changed")
		}
	}
	if value := props["associatedProjects"]; value != nil {
		rows, ok := value.([]any)
		if !ok {
			return serviceDenied("invalid_cognitive_project_index")
		}
		previous := stringValues(expected["associatedProjects"])
		seen := map[string]bool{}
		for _, row := range rows {
			current, err := cognitiveExactString(row)
			if err != nil || current == "" || seen[strings.ToLower(current)] {
				return serviceDenied("invalid_cognitive_project_index")
			}
			seen[strings.ToLower(current)] = true
			if !slices.ContainsFunc(previous, func(value string) bool { return strings.EqualFold(value, current) }) {
				return serviceDenied("cognitive_project_index_changed")
			}
		}
	}
	return nil
}

func cognitiveExactString(value any) (string, error) {
	result, ok := value.(string)
	if !ok || result != strings.TrimSpace(result) {
		return "", serviceDenied("invalid_cognitive_reference")
	}
	return result, nil
}
func cognitiveProvisioningReady(kind string, raw map[string]any) error {
	props := object(raw["properties"])
	if value, present := props["provisioningState"]; present && value != nil {
		state, err := cognitiveExactString(value)
		if err != nil {
			return err
		}
		if state != "Succeeded" && state != "Failed" && state != "Canceled" {
			return serviceDenied("cognitive_resource_not_terminal")
		}
	}
	if kind == cognitiveOutboundType {
		state := props["status"]
		if state != "Active" && state != "Inactive" && state != "Failed" {
			return serviceDenied("cognitive_outbound_rule_not_terminal")
		}
	}
	return nil
}
func (a *action) cognitiveOutboundPreflight(ctx context.Context) error {
	if a.kind.NativeType != cognitiveOutboundType {
		return nil
	}
	parentID := redisParentID(a.id)
	raw, err := a.client.cognitiveResource(ctx, parentID)
	if err != nil {
		return err
	}
	children, err := a.client.nativeServiceChildren(ctx, asset.Identity{NativeID: parentID, NativeType: cognitiveNetworkType}, raw, []string{cognitiveOutboundType})
	if err != nil {
		return err
	}
	for _, child := range children {
		value := object(child.data["properties"])["parentRuleNames"]
		if value == nil {
			continue
		}
		names, ok := value.([]any)
		if !ok {
			return serviceDenied("invalid_cognitive_outbound_parent_rules")
		}
		for _, name := range names {
			value, err := cognitiveExactString(name)
			if err != nil {
				return err
			}
			id, err := cognitiveNameID(parentID, "outboundRules", value)
			if err != nil {
				return err
			}
			if id == a.id {
				return serviceDenied("cognitive_outbound_dependents_require_network_cleanup")
			}
		}
	}
	return nil
}
