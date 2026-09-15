package azure

import (
	"net/url"
	"strings"
)

// Validate the native callback contract, separately from permission to operate
// on the stack. Matching a management-group callback does not authorize it.
func deploymentStackPollURL(id, region, endpoint, role string, parameters url.Values) (string, error) {
	scope, params, err := deploymentStackParameters(id)
	if err != nil || region == "" || strings.Trim(region, "abcdefghijklmnopqrstuvwxyz0123456789") != "" || region != strings.ToLower(strings.TrimSpace(region)) || strings.ContainsAny(region, "/%?#\\\x00\r\n\t") || len(endpoint) > 32<<10 || endpoint != strings.TrimSpace(endpoint) {
		return "", serviceDenied("invalid_deployment_stack_poll_owner")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host != "management.azure.com" || u.User != nil || u.Fragment != "" || u.RawPath != "" || u.ForceQuery {
		return "", serviceDenied("invalid_deployment_stack_poll_url")
	}
	prefix := ""
	if scope != "ManagementGroup" {
		prefix = "/subscriptions/" + text(params["subscriptionId"])
	}
	collection := prefix + "/providers/Microsoft.Resources/locations/" + region + "/deploymentStackOperationStatus/"
	version := deploymentStackVersion
	if role == "result_url" {
		if scope == "ManagementGroup" {
			return "", serviceDenied("unverified_deployment_stack_group_result_url")
		}
		collection = prefix + "/operationresults/"
		version = "2018-08-01"
	} else if role != "status_url" {
		return "", serviceDenied("invalid_deployment_stack_poll_role")
	}
	operation := u.Path[strings.LastIndex(u.Path, "/")+1:]
	if !uuidPattern.MatchString(operation) || !strings.EqualFold(u.Path, collection+operation) {
		return "", serviceDenied("deployment_stack_poll_scope_changed")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["api-version"]) != 1 || q.Get("api-version") != version {
		return "", serviceDenied("deployment_stack_poll_version_changed")
	}
	signatures := 0
	for key, values := range q {
		if len(values) != 1 || values[0] == "" {
			return "", serviceDenied("ambiguous_deployment_stack_poll_query")
		}
		switch key {
		case "api-version":
		case "t", "c", "s", "h":
			signatures++
		case "unmanageAction.Resources", "unmanageAction.ResourceGroups", "unmanageAction.ManagementGroups", "unmanageAction.ResourcesWithoutDeleteSupport", "bypassStackOutOfSyncError":
			if role != "status_url" || len(parameters[key]) != 1 || values[0] != parameters.Get(key) {
				return "", serviceDenied("deployment_stack_poll_action_changed")
			}
		default:
			return "", serviceDenied("unknown_deployment_stack_poll_query")
		}
	}
	if signatures != 0 && signatures != 4 || role == "result_url" && signatures != 0 {
		return "", serviceDenied("incomplete_deployment_stack_poll_signature")
	}
	if role == "status_url" {
		for _, key := range []string{"unmanageAction.Resources", "unmanageAction.ResourceGroups", "unmanageAction.ManagementGroups", "bypassStackOutOfSyncError"} {
			if len(parameters[key]) != 1 || q.Get(key) != parameters.Get(key) {
				return "", serviceDenied("missing_deployment_stack_poll_action")
			}
		}
		if parameters.Has("unmanageAction.ResourcesWithoutDeleteSupport") && q.Get("unmanageAction.ResourcesWithoutDeleteSupport") != parameters.Get("unmanageAction.ResourcesWithoutDeleteSupport") {
			return "", serviceDenied("missing_deployment_stack_poll_action")
		}
	}
	return strings.ToLower(operation), nil
}

// Completion of the operation still requires a separate stack/member readback.
func deploymentStackPollState(endpoint string, res response) (bool, error) {
	u, err := url.Parse(endpoint)
	if err != nil || res.status != 200 || res.data["error"] != nil || operationLocation(res.header) != "" || !strings.EqualFold(text(res.data["id"]), u.Path) || !strings.EqualFold(text(res.data["name"]), last(u.Path)) {
		return false, serviceDenied("invalid_deployment_stack_poll_response")
	}
	switch text(res.data["status"]) {
	case "succeeded":
		return true, nil
	case "initializing", "running", "deleting", "deletingResources", "updatingDenyAssignments", "canceling":
		return false, nil
	case "failed", "canceled":
		return false, serviceDenied("deployment_stack_delete_failed")
	default:
		return false, serviceDenied("unknown_deployment_stack_poll_state")
	}
}
