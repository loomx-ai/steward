package gcp

import (
	"errors"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestAPIErrorClassifiesResourceInUse(t *testing.T) {
	inUse := map[string]any{"code": 400, "message": "The network resource 'default' is already being used by 'firewall-1'", "errors": []any{map[string]any{"reason": "resourceInUseByAnotherResource", "domain": "global"}}}
	var call *contracts.ProviderCallError
	if err := apiError(400, "", inUse, ""); !errors.As(err, &call) || call.Provider.Category != execution.ErrorDependencyViolation || call.Provider.Code != "resourceInUseByAnotherResource" {
		t.Fatal(err)
	}
	invalid := map[string]any{"code": 400, "errors": []any{map[string]any{"reason": "invalid"}}}
	if err := apiError(400, "INVALID_ARGUMENT", invalid, ""); !errors.As(err, &call) || call.Provider.Category != execution.ErrorInvalidRequest {
		t.Fatal(err)
	}
}
