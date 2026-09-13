package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const defenderPricingType = "Microsoft.Security/pricings"
const defenderInventorySource = "defender-plans"
const defenderVersion = "2024-01-01"
const defenderArcType = "Microsoft.HybridCompute/machines"

func (c *client) defenderRecordedReferences(value asset.Asset) (map[string][]string, error) {
	id, _, err := c.defenderIdentity(value.Identity.NativeID)
	refs := map[string][]string{}
	for kind, ids := range object(value.Normalized["_defender_references"]) {
		refs[kind] = stringValues(ids)
	}
	expected := c.privateConfiguration(map[string]any{"id": id, "connection": value.Identity.ConnectionID, "configuration": value.Normalized["_defender_configuration"], "references": refs})
	if err != nil || id != value.Identity.NativeID || value.Identity.Provider != asset.ProviderAzure || value.Identity.NativeType != defenderPricingType || value.Identity.ConnectionID == "" || value.Identity.Partition != "azure" || value.Normalized["_inventory_source"] != defenderInventorySource || text(value.Normalized["_defender_configuration"]) == "" || value.Normalized["_defender_reference_binding"] != expected {
		return nil, serviceDenied("invalid_defender_recorded_references")
	}
	return refs, nil
}

var defenderScopeKinds = []string{vmType, scaleSetType, defenderArcType, aksType, "Microsoft.ContainerRegistry/registries"}

func (c *client) defenderScope(scope string) error {
	if scope == c.root() {
		return nil
	}
	id, kind, err := parseID(scope)
	if err != nil || id != scope || !strings.HasPrefix(id, c.root()+"/resourcegroups/") || len(strings.Split(id, "/")) != 9 {
		return serviceDenied("invalid_defender_scope")
	}
	for _, allowed := range defenderScopeKinds {
		if strings.EqualFold(kind, allowed) {
			return nil
		}
	}
	return serviceDenied("unsupported_defender_scope")
}

func (c *client) defenderIdentity(wire string) (string, string, error) {
	id := strings.ToLower(wire)
	marker := "/providers/microsoft.security/pricings/"
	index := strings.LastIndex(id, marker)
	if wire != strings.TrimSpace(wire) || index < 0 || strings.Contains(id[index+len(marker):], "/") || last(id) == "" {
		return "", "", serviceDenied("invalid_defender_identity")
	}
	if _, _, err := parseID(c.root() + "/resourcegroups/identity/providers/microsoft.security/pricings/" + last(id)); err != nil {
		return "", "", err
	}
	scope := id[:index]
	if err := c.defenderScope(scope); err != nil {
		return "", "", err
	}
	return id, scope, nil
}

func (c *client) defenderOperation(scope, name, method string) (catalog.Operation, map[string]any, error) {
	if err := c.defenderScope(scope); err != nil || method != "GET" {
		return catalog.Operation{}, nil, serviceDenied("invalid_defender_operation")
	}
	operationID := "Azure.Microsoft.Security.Pricings_List"
	parameters := map[string]any{"scopeId": strings.TrimPrefix(scope, "/")}
	if name != "" {
		if _, _, err := c.defenderIdentity(scope + "/providers/" + defenderPricingType + "/" + name); err != nil {
			return catalog.Operation{}, nil, err
		}
		operationID = "Azure.Microsoft.Security.Pricings_Get"
		parameters["pricingName"] = name
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	operation, ok := metadata.catalog.Operation(operationID)
	if !ok || operation.Call == nil || operation.Call.Version != defenderVersion {
		return catalog.Operation{}, nil, serviceDenied("defender_native_contract_missing")
	}
	return operation, parameters, nil
}

func defenderProperties(raw map[string]any) error {
	props, ok := raw["properties"].(map[string]any)
	if !ok || !slices.Contains([]string{"Free", "Standard"}, text(props["pricingTier"])) {
		return serviceDenied("invalid_defender_plan_state")
	}
	for _, key := range []string{"subPlan", "freeTrialRemainingTime", "enablementTime", "inheritedFrom"} {
		if value, exists := props[key]; exists && value != nil {
			if _, ok := value.(string); !ok {
				return serviceDenied("invalid_defender_property_type")
			}
		}
	}
	if value, exists := props["deprecated"]; exists {
		if _, ok := value.(bool); !ok {
			return serviceDenied("invalid_defender_property_type")
		}
	}
	if value, exists := props["replacedBy"]; exists {
		values, ok := value.([]any)
		if !ok {
			return serviceDenied("invalid_defender_replacement_plans")
		}
		for _, value := range values {
			if _, ok := value.(string); !ok {
				return serviceDenied("invalid_defender_replacement_plans")
			}
		}
	}
	for _, key := range []string{"inherited", "enforce"} {
		if value, exists := props[key]; exists && !slices.Contains([]string{"True", "False"}, text(value)) {
			return serviceDenied("invalid_defender_plan_inheritance")
		}
	}
	if value, exists := props["resourcesCoverageStatus"]; exists && !slices.Contains([]string{"FullyCovered", "PartiallyCovered", "NotCovered"}, text(value)) {
		return serviceDenied("invalid_defender_plan_coverage")
	}
	if value, exists := props["extensions"]; exists {
		entries, ok := value.([]any)
		if !ok {
			return serviceDenied("invalid_defender_extensions")
		}
		seen := map[string]bool{}
		for _, value := range entries {
			entry := object(value)
			name := text(entry["name"])
			if name == "" || seen[name] || !slices.Contains([]string{"True", "False"}, text(entry["isEnabled"])) {
				return serviceDenied("invalid_defender_extension")
			}
			seen[name] = true
		}
	}
	return nil
}

// Extension parameters and operation messages can contain user-authored values.
// Keep their private configuration digest, but expose only service-state fields.
func defenderSafeValue(value any) any {
	switch value := value.(type) {
	case []any:
		result := make([]any, len(value))
		for i, v := range value {
			result[i] = defenderSafeValue(v)
		}
		return result
	case map[string]any:
		result := map[string]any{}
		for _, key := range []string{"id", "name", "type", "request_id", "status_code"} {
			if v, ok := value[key]; ok {
				result[key] = v
			}
		}
		if props, ok := value["properties"].(map[string]any); ok {
			public := map[string]any{}
			for _, key := range []string{"pricingTier", "subPlan", "inherited", "inheritedFrom", "enforce", "resourcesCoverageStatus", "freeTrialRemainingTime", "enablementTime"} {
				if v, ok := props[key].(string); ok {
					public[key] = v
				}
			}
			if v, ok := props["deprecated"].(bool); ok {
				public["deprecated"] = v
			}
			if values, ok := props["replacedBy"].([]any); ok {
				plans := []string{}
				for _, value := range values {
					if name, ok := value.(string); ok {
						plans = append(plans, name)
					}
				}
				public["replacedBy"] = plans
			}
			if entries, ok := props["extensions"].([]any); ok {
				extensions := []any{}
				for _, v := range entries {
					entry := object(v)
					extensions = append(extensions, map[string]any{"name": text(entry["name"]), "isEnabled": text(entry["isEnabled"]), "operationCode": text(object(entry["operationStatus"])["code"])})
				}
				public["extensions"] = extensions
			}
			result["properties"] = public
		}
		for _, key := range []string{"body", "value"} {
			if v, ok := value[key]; ok {
				result[key] = defenderSafeValue(v)
			}
		}
		return result
	default:
		return nil
	}
}

func defenderSnapshot(raw map[string]any) map[string]any {
	result := maps.Clone(raw)
	result["properties"] = maps.Clone(object(raw["properties"]))
	delete(object(result["properties"]), "freeTrialRemainingTime") // The live duration counts down between reads.
	return result
}

func (c *client) defenderRead(ctx context.Context, wire string) (response, error) {
	id, scope, err := c.defenderIdentity(wire)
	if err != nil {
		return response{}, err
	}
	op, parameters, err := c.defenderOperation(scope, last(wire), "GET")
	if err != nil {
		return response{}, err
	}
	request, err := bindAzureREST(op, parameters)
	if err != nil {
		return response{}, err
	}
	result, err := c.requestBody(ctx, request.Method, request.URL, request.Body, request.Headers)
	if err != nil {
		return result, err
	}
	actual, _, identityErr := c.defenderIdentity(text(result.data["id"]))
	if result.status != 200 || operationLocation(result.header) != "" || identityErr != nil || actual != id || !strings.EqualFold(text(result.data["type"]), defenderPricingType) || !strings.EqualFold(text(result.data["name"]), last(id)) || defenderProperties(result.data) != nil {
		return result, serviceDenied("invalid_defender_native_response")
	}
	return result, nil
}
