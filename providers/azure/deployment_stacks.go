package azure

import (
	"regexp"
	"strings"
)

const deploymentStackType = "Microsoft.Resources/deploymentStacks"
const deploymentStackVersion = "2025-07-01"
const deploymentStackOperation = "Azure.Microsoft.Resources.DeploymentStacks_"

var deploymentStackNamePattern = regexp.MustCompile(`^[-a-zA-Z0-9_.()]{1,90}$`)

// Scope parsing is separate from authorization. In particular, a management
// group ID must never be treated as belonging to a subscription connection.
func deploymentStackParameters(id string) (string, map[string]any, error) {
	if id != strings.TrimSpace(id) || !strings.HasPrefix(id, "/") || strings.ContainsAny(id, "%?#\\\x00\r\n") {
		return "", nil, serviceDenied("invalid_deployment_stack_identity")
	}
	parts := strings.Split(strings.TrimPrefix(id, "/"), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return "", nil, serviceDenied("invalid_deployment_stack_identity")
		}
	}
	lower := strings.ToLower(id)
	p := strings.Split(strings.TrimPrefix(lower, "/"), "/")
	params := map[string]any{}
	scope := ""
	switch {
	case len(p) == 6 && p[0] == "subscriptions" && uuidPattern.MatchString(p[1]) && p[2] == "providers":
		scope = "Subscription"
		params["subscriptionId"] = parts[1]
	case len(p) == 8 && p[0] == "subscriptions" && uuidPattern.MatchString(p[1]) && p[2] == "resourcegroups" && p[4] == "providers":
		scope = "ResourceGroup"
		params["subscriptionId"] = parts[1]
		params["resourceGroupName"] = parts[3]
	case len(p) == 8 && p[0] == "providers" && p[1] == "microsoft.management" && p[2] == "managementgroups" && p[4] == "providers":
		scope = "ManagementGroup"
		params["managementGroupId"] = parts[3]
	default:
		return "", nil, serviceDenied("invalid_deployment_stack_scope")
	}
	if p[len(p)-3] != "microsoft.resources" || p[len(p)-2] != "deploymentstacks" || !deploymentStackNamePattern.MatchString(parts[len(parts)-1]) {
		return "", nil, serviceDenied("invalid_deployment_stack_type")
	}
	params["deploymentStackName"] = parts[len(parts)-1]
	return scope, params, nil
}

func validateDeploymentStackRead(res response, id string) error {
	scope, _, err := deploymentStackParameters(id)
	actual, _, ownErr := deploymentStackParameters(text(res.data["id"]))
	if err != nil || ownErr != nil || scope != actual || !strings.EqualFold(text(res.data["id"]), id) || !strings.EqualFold(text(res.data["type"]), deploymentStackType) || res.status != 200 || res.data["error"] != nil || operationLocation(res.header) != "" || object(res.data["properties"]) == nil {
		return serviceDenied("invalid_deployment_stack_read")
	}
	return nil
}

// Templates, parameters, outputs, external inputs, extension identifiers and
// provider error messages can contain credentials even under innocuous keys.
func deploymentStackSafeValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, item := range value {
			switch key {
			case "id", "type", "name", "location", "status", "provisioningState", "denyStatus", "code", "deploymentScope", "deploymentId", "apiVersion":
				if textValue, ok := item.(string); ok {
					out[key] = textValue
				}
			case "value", "properties", "resources", "detachedResources", "deletedResources", "failedResources", "error", "details":
				switch item.(type) {
				case map[string]any, []any:
					out[key] = deploymentStackSafeValue(item)
				}
			case "actionOnUnmanage":
				modes := map[string]any{}
				for field, mode := range object(item) {
					switch field {
					case "resources", "resourceGroups", "managementGroups":
						if mode == "delete" || mode == "detach" {
							modes[field] = mode
						}
					case "resourcesWithoutDeleteSupport":
						if mode == "detach" || mode == "fail" {
							modes[field] = mode
						}
					}
				}
				out[key] = modes
			case "denySettings":
				settings := map[string]any{}
				p := object(item)
				if mode := text(p["mode"]); mode == "none" || mode == "denyDelete" || mode == "denyWriteAndDelete" {
					settings["mode"] = mode
				}
				if apply, ok := p["applyToChildScopes"].(bool); ok {
					settings["applyToChildScopes"] = apply
				}
				out[key] = settings
			}
		}
		return out
	case []any:
		out := []any{}
		for _, item := range value {
			if _, ok := item.(map[string]any); ok {
				out = append(out, deploymentStackSafeValue(item))
			}
		}
		return out
	default:
		return nil
	}
}
