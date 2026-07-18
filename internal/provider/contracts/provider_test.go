package contracts_test

import (
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestProviderSupportsRootScopeUsesDeclaredInventorySources(t *testing.T) {
	t.Parallel()

	descriptors := []contracts.ProviderDescriptor{
		{
			Provider: asset.ProviderAWS,
			InventorySources: []contracts.InventorySource{{
				Name: "resource-explorer", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion, asset.ScopeGlobal},
			}},
		},
		{
			Provider: asset.ProviderAliCloud,
			InventorySources: []contracts.InventorySource{{
				Name: "resource-center", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
			}},
		},
		{
			Provider: "all-scopes",
			InventorySources: []contracts.InventorySource{{
				Name: "broad-index",
			}},
		},
		{
			Provider: "no-sources",
		},
		{
			Provider: "duplicate-no-source",
		},
		{
			Provider: "duplicate-no-source",
			InventorySources: []contracts.InventorySource{{
				Name: "regional-index", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
			}},
		},
		{
			Provider: "duplicate-global",
			InventorySources: []contracts.InventorySource{{
				Name: "regional-index", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion},
			}},
		},
		{
			Provider: "duplicate-global",
			InventorySources: []contracts.InventorySource{{
				Name: "global-index", RootScopeKinds: []asset.ScopeKind{asset.ScopeGlobal},
			}},
		},
		{
			Provider: "multi-source",
			InventorySources: []contracts.InventorySource{
				{Name: "regional-index", RootScopeKinds: []asset.ScopeKind{asset.ScopeRegion}},
				{Name: "global-index", RootScopeKinds: []asset.ScopeKind{asset.ScopeGlobal}},
			},
		},
	}
	tests := []struct {
		name          string
		provider      asset.Provider
		scope         asset.ScopeKind
		noDescriptors bool
		wantSupported bool
		wantProvable  bool
	}{
		{
			name: "AWS global", provider: asset.ProviderAWS, scope: asset.ScopeGlobal,
			wantSupported: true, wantProvable: true,
		},
		{
			name: "AliCloud global", provider: asset.ProviderAliCloud, scope: asset.ScopeGlobal,
			wantProvable: true,
		},
		{
			name: "unspecified source scopes cover all", provider: "all-scopes", scope: asset.ScopeGlobal,
			wantSupported: true, wantProvable: true,
		},
		{name: "descriptor has no sources", provider: "no-sources", scope: asset.ScopeGlobal},
		{name: "provider not found", provider: "missing", scope: asset.ScopeRegion},
		{name: "descriptors missing", provider: asset.ProviderAWS, scope: asset.ScopeGlobal, noDescriptors: true},
		{name: "duplicate no-source then Region", provider: "duplicate-no-source", scope: asset.ScopeGlobal},
		{name: "duplicate Region then Global", provider: "duplicate-global", scope: asset.ScopeGlobal},
		{
			name: "one descriptor merges multiple sources", provider: "multi-source", scope: asset.ScopeGlobal,
			wantSupported: true, wantProvable: true,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			values := descriptors
			if test.noDescriptors {
				values = nil
			}
			supported, provable := contracts.ProviderSupportsRootScope(values, test.provider, test.scope)
			if supported != test.wantSupported || provable != test.wantProvable {
				t.Fatalf(
					"ProviderSupportsRootScope(%q, %q) = (%v, %v), want (%v, %v)",
					test.provider, test.scope, supported, provable, test.wantSupported, test.wantProvable,
				)
			}
		})
	}
}

func TestSanitizeProviderErrorNeverReturnsArbitraryProviderMessage(t *testing.T) {
	for _, message := range []string{
		"The specified access key is not found.",
		"AccessKeyId: sensitive-key",
		"AccessKeyId%3Dsensitive-key",
		"https%3A%2F%2Fsts.example.test%2F%3FAccessKeyId%3Dsensitive-key%26Signature%3Dsensitive-signature",
	} {
		t.Run(message, func(t *testing.T) {
			value := execution.ProviderError{
				Category:  execution.ErrorInvalidRequest,
				Code:      "InvalidAccessKeyId.NotFound",
				Message:   message,
				RequestID: "provider-request",
			}

			sanitized := contracts.SanitizeProviderError(value)

			if sanitized.Message != contracts.SafeProviderValidationMessage {
				t.Fatalf("sanitized message = %q, want %q", sanitized.Message, contracts.SafeProviderValidationMessage)
			}
			encoded := strings.ToLower(sanitized.Message)
			for _, forbidden := range []string{"sensitive", "accesskey", "signature", "sts.example.test"} {
				if strings.Contains(encoded, forbidden) {
					t.Fatalf("sanitized message %q contains %q", sanitized.Message, forbidden)
				}
			}
			if sanitized.Code != value.Code || sanitized.RequestID != value.RequestID || sanitized.Category != value.Category {
				t.Fatalf("structured diagnostics changed: %#v", sanitized)
			}
		})
	}
}

func TestProviderCallErrorIncludesProviderRequestID(t *testing.T) {
	err := &contracts.ProviderCallError{Provider: execution.ProviderError{
		Code:      "ServiceUnavailable",
		Message:   "temporary failure",
		RequestID: "provider-request",
	}}

	if got, want := err.Error(), "ServiceUnavailable: temporary failure; request_id=provider-request"; got != want {
		t.Fatalf("Error() = %q, want %q", got, want)
	}
}
