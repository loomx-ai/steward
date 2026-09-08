package gcp

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestProductLocationsFanoutNativeZonesAndMultiRegions(t *testing.T) {
	for _, test := range []struct {
		scope string
		want  []string
	}{
		{"project", []string{"eu", "us-central1-a", "us-central1-b"}},
		{"us-central1", []string{"us-central1-a", "us-central1-b"}},
		{"eu", []string{"eu"}}, {"asia-east1", nil},
	} {
		t.Run(test.scope, func(t *testing.T) {
			var listed []string
			runtime := protocolRuntime(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "aiplatform.googleapis.com" || r.Method != "GET" {
					t.Fatalf("foreign location API: %s %s", r.Method, r.URL)
				}
				if r.URL.Path == "/v1/projects/sample-project/locations" {
					if r.URL.Query().Get("pageToken") == "" {
						return apiResponse(r, 200, `{"locations":[{"name":"projects/sample-project/locations/us-central1-a","locationId":"us-central1-a"}],"nextPageToken":"second"}`), nil
					}
					return apiResponse(r, 200, `{"locations":[{"name":"projects/123456/locations/us-central1-b","locationId":"us-central1-b"},{"name":"projects/sample-project/locations/eu"}]}`), nil
				}
				if !strings.HasSuffix(r.URL.Path, "/endpoints") {
					t.Fatalf("unexpected native endpoint: %s", r.URL)
				}
				parts := strings.Split(r.URL.Path, "/")
				location := parts[5]
				listed = append(listed, location)
				return apiResponse(r, 200, fmt.Sprintf(`{"endpoints":[{"name":"projects/sample-project/locations/%s/endpoints/123"}]}`, location)), nil
			})
			request := productRequest(runtime, "aiplatform.googleapis.com/Endpoint", test.scope)
			count := 0
			for {
				page, err := runtime.List(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				count += len(page.Items)
				if page.Complete {
					break
				}
				request.Cursor = page.NextCursor
			}
			if !slices.Equal(listed, test.want) || count != len(test.want) {
				t.Fatalf("locations=%v count=%d want=%v", listed, count, test.want)
			}
		})
	}
}

func TestProductLocationFailureDoesNotAuthorizeAbsence(t *testing.T) {
	for _, test := range []struct {
		name, body string
		status     int
	}{
		{"foreign", `{"locations":[{"name":"projects/another-project/locations/us-central1"}]}`, 200},
		{"changed_id", `{"locations":[{"name":"projects/sample-project/locations/us-central1","locationId":"eu"}]}`, 200},
		{"duplicate", `{"locations":[{"name":"projects/sample-project/locations/us-central1"},{"name":"projects/sample-project/locations/us-central1"}]}`, 200},
		{"partial", `{"locations":[],"unreachable":["eu"]}`, 200},
		{"cycle", `{"nextPageToken":"repeat"}`, 200},
		{"bad_token", `{"nextPageToken":12}`, 200},
		{"permission", `{}`, 403},
	} {
		t.Run(test.name, func(t *testing.T) {
			runtime := protocolRuntime(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Path != "/v1/projects/sample-project/locations" {
					t.Fatalf("invalid location discovery reached resource list: %s", r.URL)
				}
				return apiResponse(r, test.status, test.body), nil
			})
			if page, err := runtime.List(context.Background(), productRequest(runtime, "aiplatform.googleapis.com/Endpoint", "project")); err == nil || page.Complete {
				t.Fatalf("invalid location list authorized absence: %+v %v", page, err)
			}
		})
	}
}

func TestProductCursorBindsNativeLocationSet(t *testing.T) {
	changed := false
	lists := 0
	runtime := protocolRuntime(t, func(r *http.Request) (*http.Response, error) {
		if r.URL.Path == "/v1/projects/sample-project/locations" {
			location := "eu"
			if changed {
				location = "us"
			}
			return apiResponse(r, 200, `{"locations":[{"name":"projects/sample-project/locations/`+location+`"},{"name":"projects/sample-project/locations/us-central1"}]}`), nil
		}
		lists++
		return apiResponse(r, 200, `{}`), nil
	})
	request := productRequest(runtime, "aiplatform.googleapis.com/Endpoint", "project")
	page, err := runtime.List(context.Background(), request)
	if err != nil || page.NextCursor == "" {
		t.Fatalf("first list %+v %v", page, err)
	}
	request.Cursor = page.NextCursor
	changed = true
	if _, err = runtime.List(context.Background(), request); err == nil || lists != 1 {
		t.Fatalf("changed native location cursor accepted: calls=%d %v", lists, err)
	}
}
