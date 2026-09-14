package gcp

import (
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Resource APIs can return secrets in values whose keys are ordinary words
// (env[].value and metadata.items[].value). Redact those containers in addition
// to the shared credential-key filter before logs, inventory, or Invoke escape
// the provider. This copies the response; live protection checks keep their data.
func safePayload(value map[string]any) map[string]any {
	cleaned, err := contracts.CloudRawPayload(value)
	if err != nil {
		return map[string]any{}
	}
	// Transport log envelopes are created locally before response validation.
	// Never trust a response name to identify private serviceConfig in logs;
	// retain typed configuration for validated inventory and internal checks.
	logPayload := value["body"] != nil && (value["status_code"] != nil || value["method"] != nil)
	var redact func(any)
	redact = func(value any) {
		switch object := value.(type) {
		case map[string]any:
			redactBillingBudgetPayload(object)
			redactAlertPolicyPayload(object)
			redactNotificationChannelPayload(object)
			redactMonitoringGroupPayload(object)
			redactMonitoringDashboardPayload(object)
			if check, ok := object["httpCheck"].(map[string]any); ok {
				for _, field := range []string{"authInfo", "headers", "body"} {
					if _, present := check[field]; present {
						check[field] = "[REDACTED]"
					}
				}
			}
			redactDataprocPayload(object)
			// Service-specific SCC configuration is an untyped private payload.
			// Keep declared service/module enablement metadata available.
			if logPayload || strings.Contains(text(object["name"]), "/securityCenterServices/") {
				if _, present := object["serviceConfig"]; present {
					object["serviceConfig"] = "[REDACTED]"
				}
			}
			// BigQuery view definitions can embed private SQL and literal secrets.
			if _, table := object["tableReference"].(map[string]any); table {
				for _, field := range []string{"view", "materializedView"} {
					if definition, ok := object[field].(map[string]any); ok {
						if _, exists := definition["query"]; exists {
							definition["query"] = "[REDACTED]"
						}
					}
				}
			}
			for key, child := range object {
				switch strings.ToLower(key) {
				case "md5authenticationkeys":
					// Native Router keys use the ordinary field name "key". This
					// also covers Invoke bodies and unexpected secret-bearing GETs.
					object[key] = "[REDACTED]"
				case "error":
					if detail, ok := child.(map[string]any); ok {
						object[key] = map[string]any{"code": detail["code"], "status": detail["status"]}
					} else {
						object[key] = "[REDACTED]"
					}
				case "environment", "taskenvironments", "script", "commands", "statusevents":
					// These Batch containers can hold user code, secrets or task logs.
					// Preserve ordinary string labels with the same field names.
					if _, ok := child.(map[string]any); ok {
						object[key] = "[REDACTED]"
					}
					if _, ok := child.([]any); ok {
						object[key] = "[REDACTED]"
					}
				case "entrypoint":
					if object["imageUri"] != nil {
						object[key] = "[REDACTED]"
					}
				case "env", "environmentvariables", "secretenvironmentvariables", "secretvolumes", "user-data", "userdata", "startup-script", "startup-script-url", "sshkeys", "ssh-keys", "connectionstring", "connectionstrings", "clientsecret", "client_secret", "masterauth", "sharedsecret", "sharedsecrethash", "presharedkeys":
					object[key] = "[REDACTED]"
				case "privatekeydata", "headers", "httpheaders", "authstring", "unwrapped", "wrappedkey", "headervalue", "customrequestheaders", "customresponseheaders", "requestheaderstoadd", "responseheaderstoadd":
					object[key] = "[REDACTED]"
				case "preservedstate", "preservedstatefrompolicy", "preservedstatefromconfig":
					if state, ok := child.(map[string]any); ok && state["metadata"] != nil {
						state["metadata"] = "[REDACTED]"
					}
					redact(child)
				case "metadata":
					if metadata, ok := child.(map[string]any); ok {
						for _, raw := range array(metadata["items"]) {
							if item, ok := raw.(map[string]any); ok {
								switch text(item["key"]) {
								case "created-by", "instance-template", "cluster-name", "cluster-uid", "dataproc-cluster-uuid", "dataproc-cluster-name", "dataproc-region":
								default:
									item["value"] = "[REDACTED]"
								}
							}
						}
					}
					redact(child)
				default:
					redact(child)
				}
			}
		case []any:
			for _, child := range object {
				redact(child)
			}
		}
	}
	redact(cleaned)
	return cleaned
}
