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
	var redact func(any)
	redact = func(value any) {
		switch object := value.(type) {
		case map[string]any:
			for key, child := range object {
				switch strings.ToLower(key) {
				case "error":
					if detail, ok := child.(map[string]any); ok {
						object[key] = map[string]any{"code": detail["code"], "status": detail["status"]}
					} else {
						object[key] = "[REDACTED]"
					}
				case "env", "environmentvariables", "secretenvironmentvariables", "secretvolumes", "user-data", "userdata", "startup-script", "startup-script-url", "sshkeys", "ssh-keys", "connectionstring", "connectionstrings", "clientsecret", "client_secret":
					object[key] = "[REDACTED]"
				case "metadata":
					if metadata, ok := child.(map[string]any); ok {
						for _, raw := range array(metadata["items"]) {
							if item, ok := raw.(map[string]any); ok {
								switch text(item["key"]) {
								case "created-by", "instance-template", "cluster-name", "cluster-uid":
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
