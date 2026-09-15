package azure

import (
	"context"
	"fmt"
	"net/http"
	"testing"
)

// hostedWorkloads is read-only ARM metadata, not proof that a VM or NetApp
// group owns the NIC. Unknown shapes must not silently become an empty list.
func TestAzureHostedWorkloadNICProtection(t *testing.T) {
	for _, test := range []struct {
		name      string
		value     any
		protected bool
	}{
		{"absent", nil, false},
		{"empty", []any{}, false},
		{"volume", []any{resourceID(netappVolumeType, "volume")}, true},
		{"unknown-workload", []any{"workload"}, true},
		{"empty-entry", []any{""}, true},
		{"null-entry", []any{nil}, true},
		{"object-entry", []any{map[string]any{}}, true},
		{"object", map[string]any{}, true},
		{"string", "", true},
		{"number", 0, true},
		{"boolean", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := nativeResource(nicType, "nic", "eastus", map[string]any{"hostedWorkloads": test.value})
			kind, _ := findType(nicType)
			reason := protectionReason(kind, raw)
			if (reason != "") != test.protected {
				t.Fatalf("protection = %q, want protected %v", reason, test.protected)
			}
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("unexpected request %s %s", req.Method, req.URL)
			})
			c, err := r.resolve(context.Background(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			item, err := r.inventoryItem(context.Background(), c, raw, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			if (item.Normalized["cleanup_protected"] == true) != test.protected {
				t.Fatalf("inventory protection = %v", item.Normalized)
			}
			if test.protected && (item.Normalized["cleanup_protection_reason"] != reason || item.Normalized["cleanup_controller_only"] == true) {
				t.Fatalf("unverified workload granted controller authority: %v", item.Normalized)
			}
		})
	}
}
