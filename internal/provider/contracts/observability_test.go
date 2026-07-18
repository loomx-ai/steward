package contracts_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestCloudRawPayloadPreservesOriginalShapeWithoutTruncation(t *testing.T) {
	resources := make([]any, 40)
	for index := range resources {
		resources[index] = map[string]any{
			"ResourceId": strings.Repeat("resource-", 400),
			"Properties": map[string]any{
				"Name":          "worker",
				"SecurityToken": "must-not-appear",
			},
		}
	}
	payload, err := contracts.CloudRawPayload(map[string]any{
		"RequestId":     "req-1",
		"NextToken":     "page-2",
		"Authorization": "must-not-appear",
		"Resources":     resources,
	})
	if err != nil {
		t.Fatal(err)
	}
	if payload["RequestId"] != "req-1" || payload["NextToken"] != "page-2" {
		t.Fatalf("original fields changed: %#v", payload)
	}
	included, ok := payload["Resources"].([]any)
	if !ok || len(included) != len(resources) {
		t.Fatalf("resources truncated: %#v", payload["Resources"])
	}
	first := included[0].(map[string]any)
	if len(first["ResourceId"].(string)) <= 2048 {
		t.Fatalf("resource id was truncated: %d", len(first["ResourceId"].(string)))
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"must-not-appear", "Authorization", "SecurityToken"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("raw payload contains %q: %s", forbidden, encoded)
		}
	}
}

func TestCloudObservabilityRedactsOAuthSecretsAtEveryMapDepth(t *testing.T) {
	payload, err := contracts.CloudRawPayload(map[string]any{
		"oauth_access_token":  "oauth-access-secret",
		"OAuth-Refresh-Token": "oauth-refresh-secret",
		"authorization code":  "authorization-code-secret",
		"CodeVerifier":        "code-verifier-secret",
		"error_message":       "safe summary",
		"nested": map[string]any{
			"access_key_secret": "access-key-secret",
			"items": []any{
				map[string]any{"security_token": "security-token-secret"},
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"oauth-access-secret",
		"oauth-refresh-secret",
		"authorization-code-secret",
		"code-verifier-secret",
		"access-key-secret",
		"security-token-secret",
	} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("observability payload retained %q: %s", secret, encoded)
		}
	}
	if payload["error_message"] != "safe summary" {
		t.Fatalf("error summary = %#v", payload)
	}
}
