package gcp

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func protocolAction(t *testing.T, nativeType, name string, transport roundTripFunc) *action {
	t.Helper()
	c := &client{project: "sample-project", number: "123456", http: &http.Client{Transport: transport}}
	kind, ok := findType(nativeType)
	if !ok {
		t.Fatal("missing kind")
	}
	nativeID := "//" + strings.Split(nativeType, "/")[0] + "/" + name
	endpoint, err := c.resourceURL(kind, nativeID)
	if err != nil {
		t.Fatal(err)
	}
	op, params, err := c.resourceOperation(kind, nativeID, "DELETE")
	if err != nil {
		t.Fatal(err)
	}
	return &action{client: c, kind: kind, endpoint: endpoint, deleteOperation: op, deleteParameters: params}
}

func TestDeletionTracksNativeOperationsAndConfirmsAbsence(t *testing.T) {
	for _, test := range []struct{ kind, name, operation string }{
		{"compute.googleapis.com/Instance", "projects/sample-project/zones/us-central1-a/instances/web", "operation-1"},
		{"sqladmin.googleapis.com/Instance", "projects/sample-project/instances/db", "operation-1"},
		{"run.googleapis.com/Service", "projects/sample-project/locations/us-central1/services/web", "projects/sample-project/locations/us-central1/operations/operation-1"},
		{"artifactregistry.googleapis.com/Repository", "projects/sample-project/locations/us-central1/repositories/artifacts", "projects/sample-project/locations/us-central1/operations/operation-1"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			deleted, finished := false, false
			deletes, polls, reads := 0, 0, 0
			a := protocolAction(t, test.kind, test.name, func(request *http.Request) (*http.Response, error) {
				if request.Method == "DELETE" {
					deletes++
					deleted = true
					if strings.HasPrefix(test.kind, "compute.") && request.URL.Query().Get("requestId") != googleRequestID("cleanup-job") {
						t.Fatal("provider idempotency key lost")
					}
					return apiResponse(request, 200, `{"name":"`+test.operation+`"}`), nil
				}
				if strings.Contains(request.URL.Path, "/operations/") {
					if !deleted {
						t.Fatal("waiter ran before deletion")
					}
					polls++
					if polls == 1 {
						return apiResponse(request, 200, `{"done":false,"status":"RUNNING"}`), nil
					}
					finished = true
					return apiResponse(request, 200, `{"done":true,"status":"DONE"}`), nil
				}
				reads++
				if finished {
					return apiResponse(request, 404, `{"error":{"status":"NOT_FOUND"}}`), nil
				}
				return apiResponse(request, 200, `{"name":"web","status":"RUNNING"}`), nil
			})
			request := contracts.ActionRequest{Action: "delete", IdempotencyKey: "cleanup-job"}
			result, err := a.Execute(context.Background(), request)
			if err != nil || result.ProviderOperationID == "" || result.ProviderRequestID != "request-123" {
				t.Fatalf("execute=%+v err=%v", result, err)
			}
			wait, err := a.Wait(context.Background(), request, result)
			if err != nil || wait.Done {
				t.Fatalf("unfinished operation treated as complete: %+v %v", wait, err)
			}
			wait, err = a.Wait(context.Background(), request, result)
			expectedReads := 2
			if test.kind == instanceType {
				expectedReads = 3
			}
			if err != nil || !wait.Done || deletes != 1 || polls != 2 || reads != expectedReads {
				t.Fatalf("missing final readback: wait=%+v deletes=%d polls=%d reads=%d err=%v", wait, deletes, polls, reads, err)
			}
		})
	}
}

func TestDeletionRechecksProtectionAndBucketOwnership(t *testing.T) {
	for _, test := range []struct{ kind, name, resource, objects, reason string }{
		{"sqladmin.googleapis.com/Instance", "projects/sample-project/instances/db", `{"settings":{"deletionProtectionEnabled":true}}`, "", "deletion_protection_enabled"},
		{"storage.googleapis.com/Bucket", "sample-bucket", `{"projectNumber":"999999"}`, `{}`, "bucket_project_not_verified"},
		{"storage.googleapis.com/Bucket", "sample-bucket", `{"projectNumber":"123456"}`, `{"items":[{"name":"file","generation":"1"}]}`, "bucket_not_empty"},
	} {
		t.Run(test.reason, func(t *testing.T) {
			var deletes int
			a := protocolAction(t, test.kind, test.name, func(request *http.Request) (*http.Response, error) {
				if request.Method == "DELETE" {
					deletes++
				}
				body := test.resource
				if strings.HasSuffix(request.URL.Path, "/o") {
					if request.URL.Query().Get("versions") != "true" {
						t.Fatal("bucket check omitted old object versions")
					}
					body = test.objects
				}
				return apiResponse(request, 200, body), nil
			})
			_, err := a.Execute(context.Background(), contracts.ActionRequest{Action: "delete"})
			var call *contracts.ProviderCallError
			if !errors.As(err, &call) || call.Provider.Category != execution.ErrorProtected || call.Provider.Code != test.reason || deletes != 0 {
				t.Fatalf("protected resource deleted: %d %v", deletes, err)
			}
		})
	}
}

func TestBucketChildCollectionNotFoundDoesNotProveBucketAbsence(t *testing.T) {
	a := protocolAction(t, "storage.googleapis.com/Bucket", "sample-bucket", func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/o") {
			return apiResponse(request, 404, `{"error":{"code":404,"status":"NOT_FOUND"}}`), nil
		}
		return apiResponse(request, 200, `{"projectNumber":"123456"}`), nil
	})
	check, err := a.Preflight(context.Background(), contracts.ActionRequest{Action: "delete"})
	var call *contracts.ProviderCallError
	if !errors.As(err, &call) || call.Provider.Category != execution.ErrorDependencyViolation || check.Absent {
		t.Fatalf("missing object collection mistaken for bucket absence: %+v %v", check, err)
	}
}

func TestWaiterRejectsPersistedForeignOperationsAndProviderFailure(t *testing.T) {
	var calls int
	a := protocolAction(t, "run.googleapis.com/Service", "projects/sample-project/locations/us-central1/services/web", func(request *http.Request) (*http.Response, error) {
		calls++
		return apiResponse(request, 200, `{"done":true,"error":{"code":7,"message":"PRIVATE_VALUE"}}`), nil
	})
	for _, endpoint := range []string{
		"https://untrusted.example/v2/projects/sample-project/locations/us-central1/operations/op",
		"https://run.googleapis.com/v2/projects/foreign-project/locations/us-central1/operations/op",
		"https://run.googleapis.com/v2/projects/sample-project/locations/us-east1/operations/op",
		"https://run.googleapis.com/v2/projects/sample-project/locations/us-central1/operations/../op",
	} {
		if _, err := a.Wait(context.Background(), contracts.ActionRequest{Action: "delete"}, contracts.ActionResult{ProviderOperationID: endpoint}); err == nil {
			t.Fatalf("accepted foreign waiter %s", endpoint)
		}
	}
	if calls != 0 {
		t.Fatal("invalid waiter reached transport")
	}
	_, err := a.Wait(context.Background(), contracts.ActionRequest{Action: "delete"}, contracts.ActionResult{ProviderOperationID: "https://run.googleapis.com/v2/projects/sample-project/locations/us-central1/operations/op"})
	var call *contracts.ProviderCallError
	if !errors.As(err, &call) || call.Provider.Category != execution.ErrorProviderFailure || call.Provider.RequestID != "request-123" || strings.Contains(err.Error(), "PRIVATE_VALUE") {
		t.Fatalf("operation failure lost or leaked: %v", err)
	}
}

func TestWaiterNeverReportsCompletionWhenResourceReadbackFails(t *testing.T) {
	a := protocolAction(t, "run.googleapis.com/Service", "projects/sample-project/locations/us-central1/services/web", func(request *http.Request) (*http.Response, error) {
		if request.Method != "GET" || !strings.HasSuffix(request.URL.Path, "/services/web") {
			t.Fatalf("unexpected readback %s %s", request.Method, request.URL)
		}
		return apiResponse(request, 403, `{"error":{"code":403,"status":"PERMISSION_DENIED"}}`), nil
	})
	wait, err := a.Wait(context.Background(), contracts.ActionRequest{Action: "delete"}, contracts.ActionResult{})
	if err == nil || wait.Done {
		t.Fatalf("failed readback reported completion: %+v %v", wait, err)
	}
}
