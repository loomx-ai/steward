package aws

import (
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
)

func TestErrorCategoryClassifiesDeletionOutcomes(t *testing.T) {
	for _, tc := range []struct {
		code   string
		status int
		want   execution.ErrorCategory
	}{
		{"NoSuchEntity", 404, execution.ErrorNotFound},
		{"NoSuchEntity", 400, execution.ErrorNotFound},
		{"NoSuchBucket", 404, execution.ErrorNotFound},
		{"InvalidVpcID.NotFound", 400, execution.ErrorNotFound},
		{"DependencyViolation", 400, execution.ErrorDependencyViolation},
		{"DeleteConflict", 409, execution.ErrorDependencyViolation},
		{"ResourceInUseException", 400, execution.ErrorDependencyViolation},
		{"BucketNotEmpty", 409, execution.ErrorDependencyViolation},
		{"HostedZoneNotEmpty", 400, execution.ErrorDependencyViolation},
		{"InvalidNetworkInterface.InUse", 400, execution.ErrorDependencyViolation},
		{"RequestLimitExceeded", 503, execution.ErrorThrottled},
		{"LimitExceededException", 400, execution.ErrorInvalidRequest},
		{"NoSuchBucketPolicy", 404, execution.ErrorNotFound},
		{"InternalError", 500, execution.ErrorRetryable},
	} {
		if got := errorCategory(tc.code, tc.status); got != tc.want {
			t.Fatal(tc.code, tc.status, got, tc.want)
		}
	}
	for _, tc := range []struct {
		code, message string
		want          execution.ErrorCategory
	}{
		{"NotFound", "", execution.ErrorNotFound},
		{"ResourceConflict", "The subnet 'subnet-1' has dependencies and cannot be deleted. (Service: Ec2, Status Code: 400, Error Code: DependencyViolation)", execution.ErrorDependencyViolation},
		{"ResourceConflict", "resource is being modified", execution.ErrorConflict},
		{"AccessDenied", "", execution.ErrorPermissionDenied},
		{"Throttling", "", execution.ErrorProviderFailure},
		{"ServiceInternalError", "", execution.ErrorProviderFailure},
	} {
		if got := cloudControlHandlerCategory(tc.code, tc.message); got != tc.want {
			t.Fatal(tc.code, tc.message, got, tc.want)
		}
	}
}
