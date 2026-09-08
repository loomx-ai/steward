package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestKMSParentFanoutPagingDependenciesAndDrift(t *testing.T) {
	const parent = "projects/sample-project/locations/us-central1"
	changed := false
	versionCalls := 0
	r := protocolRuntime(t, func(request *http.Request) (*http.Response, error) {
		var data map[string]any
		switch request.URL.Path {
		case "/v1/" + parent + "/keyRings":
			data = map[string]any{"keyRings": []any{map[string]any{"name": parent + "/keyRings/ring"}}}
			if changed {
				data["keyRings"] = append(data["keyRings"].([]any), map[string]any{"name": parent + "/keyRings/new"})
			}
		case "/v1/" + parent + "/keyRings/ring/cryptoKeys":
			data = map[string]any{"cryptoKeys": []any{map[string]any{"name": parent + "/keyRings/ring/cryptoKeys/key"}}}
		case "/v1/" + parent + "/keyRings/new/cryptoKeys":
			data = map[string]any{}
		case "/v1/" + parent + "/keyRings/ring/cryptoKeys/key/cryptoKeyVersions":
			versionCalls++
			name := parent + "/keyRings/ring/cryptoKeys/key/cryptoKeyVersions/1"
			data = map[string]any{"cryptoKeyVersions": []any{map[string]any{"name": name, "state": "DESTROYED"}}}
			if request.URL.Query().Get("pageToken") == "" {
				data["nextPageToken"] = "second"
			} else {
				data["cryptoKeyVersions"] = []any{map[string]any{"name": strings.TrimSuffix(name, "1") + "2", "state": "DESTROY_SCHEDULED"}}
			}
		default:
			t.Fatalf("unexpected KMS fanout request %s", request.URL)
		}
		raw, _ := json.Marshal(data)
		return apiResponse(request, 200, string(raw)), nil
	})
	request := productRequest(r, "cloudkms.googleapis.com/CryptoKeyVersion", "us-central1")
	first, err := r.List(context.Background(), request)
	if err != nil || first.Complete || len(first.Items) != 1 {
		t.Fatalf("first=%+v err=%v", first, err)
	}
	ref := first.Items[0].Normalized["refs_cloudkms_googleapis_com_CryptoKey"]
	if values, ok := ref.([]string); !ok || len(values) != 1 || values[0] != "//cloudkms.googleapis.com/"+parent+"/keyRings/ring/cryptoKeys/key" {
		t.Fatalf("parent dependency lost: %+v", ref)
	}
	request.Cursor = first.NextCursor
	second, err := r.List(context.Background(), request)
	if err != nil || !second.Complete || len(second.Items) != 1 || second.Items[0].State != "DESTROY_SCHEDULED" {
		t.Fatalf("second=%+v err=%v", second, err)
	}
	// Change an intermediate parent itself, not only its ordering. The root
	// parent set changing without any keys leaves this version target set intact.
	changed = true
	second, err = r.List(context.Background(), request)
	if err != nil || !second.Complete {
		t.Fatalf("unchanged child targets should remain valid: %+v %v", second, err)
	}
	if versionCalls != 3 {
		t.Fatalf("version calls=%d", versionCalls)
	}
}

func TestKMSCursorRejectsChangedChildParentsAndForeignParents(t *testing.T) {
	parent := "projects/sample-project/locations/us-central1/keyRings/ring"
	changed := false
	calls := 0
	r := protocolRuntime(t, func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/keyRings") {
			name := parent
			if changed {
				name = strings.Replace(parent, "/ring", "/other", 1)
			}
			return apiResponse(request, 200, `{"keyRings":[{"name":"`+name+`"}]}`), nil
		}
		calls++
		return apiResponse(request, 200, `{"nextPageToken":"second"}`), nil
	})
	request := productRequest(r, "cloudkms.googleapis.com/CryptoKey", "us-central1")
	page, err := r.List(context.Background(), request)
	if err != nil || page.NextCursor == "" {
		t.Fatalf("page=%+v err=%v", page, err)
	}
	request.Cursor = page.NextCursor
	changed = true
	if _, err = r.List(context.Background(), request); err == nil {
		t.Fatal("changed KMS parent set accepted")
	}
	if calls != 1 {
		t.Fatal("changed cursor reached child API")
	}
	parent = "projects/foreign-project/locations/us-central1/keyRings/ring"
	request.Cursor = ""
	changed = false
	if _, err = r.List(context.Background(), request); err == nil {
		t.Fatal("cross-project parent accepted")
	}
	if calls != 1 {
		t.Fatal("foreign child API invoked")
	}
}

func TestKMSGlobalLocationIsDiscovered(t *testing.T) {
	r := protocolRuntime(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/v1/projects/sample-project/locations/global/keyRings" {
			t.Fatalf("global location omitted: %s", request.URL)
		}
		return apiResponse(request, 200, `{"keyRings":[{"name":"projects/sample-project/locations/global/keyRings/ring"}]}`), nil
	})
	page, err := r.List(context.Background(), productRequest(r, "cloudkms.googleapis.com/KeyRing", "global"))
	if err != nil || len(page.Items) != 1 || !page.Complete {
		t.Fatalf("page=%+v err=%v", page, err)
	}
}

func TestKMSVersionDeletionPreconditionsAndLongRunningReadback(t *testing.T) {
	name := "projects/sample-project/locations/us-central1/keyRings/ring/cryptoKeys/key/cryptoKeyVersions/1"
	for _, body := range []string{`{"state":"ENABLED"}`, `{"state":"DESTROY_SCHEDULED"}`, `{"state":"DESTROYED","importTime":"2026-08-01T00:00:00Z"}`, `{}`} {
		deletes := 0
		a := protocolAction(t, "cloudkms.googleapis.com/CryptoKeyVersion", name, func(request *http.Request) (*http.Response, error) {
			if request.Method == "DELETE" {
				deletes++
			}
			return apiResponse(request, 200, body), nil
		})
		_, err := a.Execute(context.Background(), contracts.ActionRequest{Action: "delete"})
		var failure *contracts.ProviderCallError
		if !errors.As(err, &failure) || failure.Provider.Category != execution.ErrorProtected || deletes != 0 {
			t.Fatalf("invalid key version was deleted: %d %v", deletes, err)
		}
	}
	deleted := false
	polls := 0
	a := protocolAction(t, "cloudkms.googleapis.com/CryptoKeyVersion", name, func(request *http.Request) (*http.Response, error) {
		if request.Method == "DELETE" {
			deleted = true
			return apiResponse(request, 200, `{"name":"projects/sample-project/locations/us-central1/operations/delete-key","done":false}`), nil
		}
		if strings.HasSuffix(request.URL.Path, "/operations/delete-key") {
			polls++
			return apiResponse(request, 200, `{"done":true}`), nil
		}
		if deleted {
			return apiResponse(request, 404, `{"error":{"code":404,"status":"NOT_FOUND"}}`), nil
		}
		return apiResponse(request, 200, `{"state":"DESTROYED"}`), nil
	})
	result, err := a.Execute(context.Background(), contracts.ActionRequest{Action: "delete"})
	if err != nil {
		t.Fatal(err)
	}
	if result.ProviderOperationID != "https://cloudkms.googleapis.com/v1/projects/sample-project/locations/us-central1/operations/delete-key" {
		t.Fatalf("wrong KMS operation: %+v", result)
	}
	wait, err := a.Wait(context.Background(), contracts.ActionRequest{Action: "delete"}, result)
	if err != nil || !wait.Done || polls != 1 {
		t.Fatalf("wait=%+v polls=%d err=%v", wait, polls, err)
	}
	result.ProviderOperationID = "https://cloudkms.googleapis.com/v1/projects/sample-project/locations/us-east1/operations/delete-key"
	if _, err = a.Wait(context.Background(), contracts.ActionRequest{Action: "delete"}, result); err == nil {
		t.Fatal("cross-location KMS waiter accepted")
	}
}

func TestKMSParentDeletionReadsAllChildrenAndRetainsProviderErrors(t *testing.T) {
	ring := "projects/sample-project/locations/us-central1/keyRings/ring"
	for _, test := range []struct {
		kind, name, reason, body string
		status                   int
	}{
		{"CryptoKey", ring + "/cryptoKeys/key", "key_rotation_enabled", `{"rotationPeriod":"86400s"}`, 200},
		{"CryptoKey", ring + "/cryptoKeys/key", "key_versions_not_deleted", `{}`, 200},
		{"KeyRing", ring, "key_ring_contains_unexpired_import_jobs", `{}`, 200},
		{"KeyRing", ring, "", `{}`, 403},
	} {
		t.Run(test.reason, func(t *testing.T) {
			a := protocolAction(t, "cloudkms.googleapis.com/"+test.kind, test.name, func(request *http.Request) (*http.Response, error) {
				if request.URL.Path == "/v1/"+test.name {
					return apiResponse(request, 200, test.body), nil
				}
				if test.status == 403 {
					return apiResponse(request, 403, `{"error":{"status":"PERMISSION_DENIED"}}`), nil
				}
				if strings.HasSuffix(request.URL.Path, "/cryptoKeyVersions") {
					return apiResponse(request, 200, `{"cryptoKeyVersions":[{"name":"version","state":"DESTROYED"}]}`), nil
				}
				if strings.HasSuffix(request.URL.Path, "/importJobs") {
					if request.URL.Query().Get("pageToken") == "" {
						return apiResponse(request, 200, `{"importJobs":[{"state":"EXPIRED"}],"nextPageToken":"second"}`), nil
					}
					return apiResponse(request, 200, `{"importJobs":[{"state":"ACTIVE"}]}`), nil
				}
				return apiResponse(request, 200, `{}`), nil
			})
			check, err := a.Preflight(context.Background(), contracts.ActionRequest{Action: "delete"})
			if test.status == 403 {
				if err == nil {
					t.Fatal("child permission error treated as empty")
				}
				return
			}
			if err != nil || check.Allowed || check.Reason != test.reason {
				t.Fatalf("preflight=%+v err=%v", check, err)
			}
		})
	}
}
