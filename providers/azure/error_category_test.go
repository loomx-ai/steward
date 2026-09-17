package azure

import (
	"errors"
	"net/http"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestAPIErrorClassifiesDeletionOutcomes(t *testing.T) {
	for _, tc := range []struct {
		status int
		code   string
		want   execution.ErrorCategory
	}{
		{400, "InUseSubnetCannotBeDeleted", execution.ErrorDependencyViolation},
		{400, "InUseNetworkSecurityGroupCannotBeDeleted", execution.ErrorDependencyViolation},
		{400, "PublicIPAddressInUse", execution.ErrorDependencyViolation},
		{409, "AnotherOperationInProgress", execution.ErrorRetryable},
		{409, "ScopeLocked", execution.ErrorProtected},
		{404, "ResourceNotFound", execution.ErrorNotFound},
		{409, "Conflict", execution.ErrorConflict},
		{400, "InvalidParameter", execution.ErrorInvalidRequest},
	} {
		var call *contracts.ProviderCallError
		if err := apiError(tc.status, tc.code, http.Header{}); !errors.As(err, &call) || call.Provider.Category != tc.want {
			t.Fatal(tc.code, err, tc.want)
		}
	}
}
