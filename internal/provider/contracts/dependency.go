package contracts

import (
	"errors"

	"github.com/loomx-ai/steward/internal/core/execution"
)

// DependencyReadError prevents a missing related resource or collection from
// being interpreted by the executor as proof that the deletion target is gone.
// Callers handle the target's own not-found response before using this helper.
func DependencyReadError(err error) error {
	var call *ProviderCallError
	if !errors.As(err, &call) || call.Provider.Category != execution.ErrorNotFound {
		return err
	}
	copy := *call
	copy.Provider.Category = execution.ErrorDependencyViolation
	copy.Provider.Code = "dependent_resource_not_found"
	copy.Provider.Message = SafeProviderValidationMessage
	return &copy
}
