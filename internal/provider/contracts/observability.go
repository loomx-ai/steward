package contracts

import (
	"encoding/json"
	"fmt"
	"strings"
)

func CloudRawPayload(value any) (map[string]any, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("encode raw cloud log payload: %w", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, fmt.Errorf("decode raw cloud log payload: %w", err)
	}
	return sanitizeRawLogMap(payload), nil
}

// RedactCloudSecrets drops credential and key material, such as VPN
// pre-shared keys, from a raw provider record before it is stored.
func RedactCloudSecrets(input map[string]any) map[string]any {
	if input == nil {
		return nil
	}
	return sanitizeRawLogMap(input)
}

func sanitizeRawLogMap(input map[string]any) map[string]any {
	result := make(map[string]any, len(input))
	for key, value := range input {
		if forbiddenRawLogKey(key) {
			continue
		}
		result[key] = sanitizeRawLogValue(value)
	}
	return result
}

func sanitizeRawLogValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return sanitizeRawLogMap(typed)
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = sanitizeRawLogValue(item)
		}
		return result
	default:
		return typed
	}
}

func forbiddenRawLogKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", " ", "").Replace(key))
	switch normalized {
	case "accesskeyid", "accesskey" + "secret", "secretaccesskey", "sessiontoken",
		"securitytoken", "accesstoken", "identitytoken", "webidentitytoken",
		"oauthaccesstoken", "oauthrefreshtoken", "authorizationcode", "codeverifier",
		"oidctoken", "clientassertion", "subjecttoken", "assertion",
		"authorization", "signature", "cookie", "cookies", "credential",
		"credentials", "password", "privatekey", "psk", "presharedkey":
		return true
	default:
		return false
	}
}
