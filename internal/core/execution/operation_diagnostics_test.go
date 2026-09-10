package execution_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
)

func TestPublicOperationDiagnosticsDoNotChangeResumableReceipts(t *testing.T) {
	original := "https://private-user:private-password@management.azure.com/subscriptions/test/OperationResults/id?api-version=2020-03-01&t=private-time&c=private-cert&s=private-signature&h=private-hash#private-fragment"
	expected := "https://management.azure.com/subscriptions/test/OperationResults/id?api-version=2020-03-01"
	stored := execution.ActionAttempt{ProviderOperationID: original, ProviderResult: map[string]any{"stream_analytics_poll_operation": original, "nested": []any{map[string]any{"operation": original}}, "polling": "location", "count": 1.0}}
	safe := execution.PublicActionAttempt(stored)
	if safe.ProviderOperationID != expected || safe.ProviderResult["stream_analytics_poll_operation"] != expected || safe.ProviderResult["polling"] != "location" || safe.ProviderResult["count"] != 1.0 {
		t.Fatal("operation diagnostics lost identity", safe)
	}
	payload, _ := json.Marshal(safe)
	if strings.Contains(string(payload), "private-") {
		t.Fatal("signed polling credentials exposed")
	}
	if stored.ProviderOperationID != original || stored.ProviderResult["stream_analytics_poll_operation"] != original || stored.ProviderResult["nested"].([]any)[0].(map[string]any)["operation"] != original {
		t.Fatal("public projection changed persisted execution receipt")
	}
	for _, id := range []string{"", "opaque-operation", "projects/project/locations/us/operations/op", "arn:aws:service:us:account:operation/id"} {
		if execution.PublicOperationID(id) != id {
			t.Fatal("opaque provider operation changed", id)
		}
	}
	for _, id := range []string{"https://management.azure.com/path?bad=%zz&s=private-secret", "https://%invalid/path?s=private-secret", "HTTPS://management.azure.com/path?api-version=one&api-version=two&s=private-secret"} {
		if strings.Contains(execution.PublicOperationID(id), "private-secret") {
			t.Fatal("malformed polling URL exposed credentials")
		}
	}
}
