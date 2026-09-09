package azure

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func actionAsset(kind, name string) asset.Asset {
	return asset.Asset{Identity: asset.Identity{Provider: asset.ProviderAzure, NativeType: kind, NativeID: resourceID(kind, name)}, Location: "eastus"}
}

func TestDeleteAsyncPollingRequiresFinalAbsence(t *testing.T) {
	for _, protocol := range []string{"Azure-AsyncOperation", "Operation-Location", "Location"} {
		t.Run(protocol, func(t *testing.T) {
			value := actionAsset(vmType, "vm")
			id := strings.ToLower(value.Identity.NativeID)
			operation := apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.Compute/locations/eastus/operations/delete-123", "2024-07-01")
			reads, polls, deletes := 0, 0, 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				path := strings.ToLower(req.URL.Path)
				if strings.EqualFold(req.URL.String(), operation) {
					polls++
					if protocol == "Location" {
						status := 202
						if polls > 1 {
							status = 204
						}
						return &http.Response{StatusCode: status, Header: http.Header{"Retry-After": {"1"}}, Body: io.NopCloser(strings.NewReader(""))}, nil
					}
					state := "Running"
					if polls > 1 {
						state = "Succeeded"
					}
					return jsonResponse(200, map[string]any{"status": state}, http.Header{"X-Ms-Request-Id": {"poll-request"}, "Retry-After": {"1"}}), nil
				}
				switch path {
				case id:
					if req.Method == "DELETE" {
						deletes++
						if req.Header.Get("x-ms-client-request-id") != azureRequestID("delete-key") {
							t.Error("missing correlation key")
						}
						h := http.Header{}
						h.Set(protocol, operation)
						h.Set("x-ms-request-id", "delete-request")
						h.Set("Retry-After", "3")
						return jsonResponse(202, map[string]any{}, h), nil
					}
					reads++
					if deletes > 0 && reads >= 6 {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
					}
					return jsonResponse(200, nativeResource(vmType, "vm", "eastus", map[string]any{"provisioningState": "Succeeded"}), nil), nil
				case id + "/extensions":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				case "/subscriptions/" + testSubscription + "/resourcegroups/test":
					return jsonResponse(200, map[string]any{"id": path}, nil), nil
				case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				return nil, fmt.Errorf("unexpected %s %s", req.Method, req.URL)
			})
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "delete-key"}
			result, err := driver.Execute(context.Background(), request)
			if err != nil || result.ProviderRequestID != "delete-request" || result.RetryAfter != 3*time.Second {
				t.Fatalf("execute=%+v %v", result, err)
			}
			for i := 0; i < 3; i++ {
				wait, err := driver.Wait(context.Background(), request, result)
				if err != nil {
					t.Fatal(err)
				}
				if wait.Done != (i == 2) {
					t.Fatalf("poll %d prematurely completed: %+v", i, wait)
				}
			}
			if deletes != 1 || reads != 6 {
				t.Fatalf("calls deletes=%d reads=%d", deletes, reads)
			}
		})
	}
}

func TestLiveProtectionPreventsMutation(t *testing.T) {
	tests := []struct {
		name, kind string
		properties map[string]any
		managedBy  string
		locks      []any
		reason     string
	}{
		{name: "disk", kind: diskType, managedBy: resourceID("Microsoft.ContainerService/managedClusters", "cluster"), reason: "azure_managed_resource"},
		{name: "vm", kind: vmType, properties: map[string]any{}, locks: []any{map[string]any{"id": "/subscriptions/" + testSubscription + "/providers/Microsoft.Authorization/locks/protect", "properties": map[string]any{"level": "CanNotDelete"}}}, reason: "azure_management_lock"},
	}
	for _, test := range tests {
		t.Run(test.reason, func(t *testing.T) {
			value := actionAsset(test.kind, test.name)
			raw := nativeResource(test.kind, test.name, "eastus", test.properties)
			raw["managedBy"] = test.managedBy
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method == "DELETE" {
					t.Fatal("protected resource deletion reached API")
				}
				switch {
				case strings.EqualFold(req.URL.Path, value.Identity.NativeID):
					return jsonResponse(200, raw, nil), nil
				case strings.HasSuffix(strings.ToLower(req.URL.Path), "/resourcegroups/test"):
					return jsonResponse(200, map[string]any{"id": req.URL.Path}, nil), nil
				case strings.HasSuffix(req.URL.Path, "/locks"):
					locks := test.locks
					if locks == nil {
						locks = []any{}
					}
					return jsonResponse(200, map[string]any{"value": locks}, nil), nil
				}
				return nil, fmt.Errorf("unexpected URL %s", req.URL)
			})
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			check, err := driver.Preflight(context.Background(), contracts.ActionRequest{Action: "delete"})
			if err != nil || check.Allowed || check.Reason != test.reason {
				t.Fatalf("preflight=%+v %v", check, err)
			}
			_, err = driver.Execute(context.Background(), contracts.ActionRequest{Action: "delete"})
			var call *contracts.ProviderCallError
			if !errors.As(err, &call) || call.Provider.Category != execution.ErrorProtected {
				t.Fatalf("execute=%v", err)
			}
		})
	}
}

func TestPreflightDistinguishesMissingTargetFromMissingRelatedGroup(t *testing.T) {
	for _, targetGone := range []bool{true, false} {
		value := actionAsset(diskType, "disk")
		r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
			if strings.EqualFold(req.URL.Path, value.Identity.NativeID) && !targetGone {
				return jsonResponse(200, nativeResource(diskType, "disk", "eastus", nil), nil), nil
			}
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
		})
		driver, err := r.ResolveAction(context.Background(), "connection", value)
		if err != nil {
			t.Fatal(err)
		}
		check, err := driver.Preflight(context.Background(), contracts.ActionRequest{Asset: value, Action: "delete"})
		var call *contracts.ProviderCallError
		if targetGone {
			if err != nil || !check.Absent {
				t.Fatalf("actual absence lost: %+v %v", check, err)
			}
		} else if !errors.As(err, &call) || call.Provider.Category != execution.ErrorDependencyViolation || check.Absent {
			t.Fatalf("related group absence closed live disk: %+v %v", check, err)
		}
	}
}

func TestAppServiceDeletionPreservesItsPlan(t *testing.T) {
	value := actionAsset("Microsoft.Web/sites", "web")
	raw := nativeResource(appSiteType, "web", "eastus", map[string]any{})
	raw["kind"] = "app"
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method == "DELETE" {
			if req.URL.Query().Get("deleteEmptyServerFarm") != "false" {
				t.Error("delete omitted plan retention")
			}
			return jsonResponse(200, map[string]any{}, nil), nil
		}
		if strings.EqualFold(req.URL.Path, value.Identity.NativeID) {
			return jsonResponse(200, raw, nil), nil
		}
		if strings.HasSuffix(req.URL.Path, "/locks") || strings.HasSuffix(req.URL.Path, "/slots") || strings.HasSuffix(req.URL.Path, "/certificates") || strings.HasSuffix(req.URL.Path, "/hostNameBindings") {
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		return jsonResponse(200, map[string]any{"id": req.URL.Path}, nil), nil
	})
	value = dnsAsset(t, r, raw)
	driver, err := r.ResolveAction(context.Background(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: value, Action: "delete"}); err != nil {
		t.Fatal(err)
	}
}

func TestPersistedOperationRejectsForeignOwnership(t *testing.T) {
	value := actionAsset(vmType, "vm")
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		t.Error("foreign operation reached API")
		return nil, fmt.Errorf("unexpected")
	})
	driver, err := r.ResolveAction(context.Background(), "connection", value)
	if err != nil {
		t.Fatal(err)
	}
	base := armOrigin + "/subscriptions/" + testSubscription + "/providers/Microsoft.Compute/locations/eastus/operations/op?api-version=2024-07-01"
	for _, endpoint := range []string{strings.Replace(base, "management.azure.com", "evil.invalid", 1), strings.Replace(base, testSubscription, testTenant, 1), strings.Replace(base, "Microsoft.Compute", "Microsoft.Storage", 1), strings.Replace(base, "eastus", "westus", 1), strings.Replace(base, "/providers/", "/resourceGroups/foreign/providers/", 1)} {
		if _, err := driver.Wait(context.Background(), contracts.ActionRequest{Action: "delete"}, contracts.ActionResult{ProviderOperationID: endpoint, Data: map[string]any{"polling": "status"}}); err == nil {
			t.Errorf("accepted %s", endpoint)
		}
	}
}

func TestOperationFailureRetainsRequestIDWithoutProviderMessage(t *testing.T) {
	failure := operationError(response{requestID: "failure-request", data: map[string]any{"status": "Failed", "error": map[string]any{"code": "DeploymentFailed", "message": "private provider detail"}}})
	var call *contracts.ProviderCallError
	if !errors.As(failure, &call) || call.Provider.RequestID != "failure-request" || strings.Contains(failure.Error(), "private") {
		t.Fatalf("failure=%v", failure)
	}
}
