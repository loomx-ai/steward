package contracts_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestMissingDependencyDoesNotProveTargetAbsence(t *testing.T) {
	missing := &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorNotFound, Code: "ResourceNotFound", RequestID: "dependent-get"}}
	err := contracts.DependencyReadError(fmt.Errorf("read related resource: %w", missing))
	var call *contracts.ProviderCallError
	if !errors.As(err, &call) || call.Provider.Category != execution.ErrorDependencyViolation || call.Provider.RequestID != "dependent-get" || missing.Provider.Category != execution.ErrorNotFound {
		t.Fatalf("dependency lost its error boundary: %+v original=%+v", call, missing)
	}
	for _, category := range []execution.ErrorCategory{execution.ErrorPermissionDenied, execution.ErrorThrottled, execution.ErrorProviderFailure} {
		original := &contracts.ProviderCallError{Provider: execution.ProviderError{Category: category}}
		if contracts.DependencyReadError(original) != original {
			t.Errorf("unrelated %s error changed", category)
		}
	}
	if contracts.DependencyReadError(nil) != nil {
		t.Fatal("nil error changed")
	}
}
