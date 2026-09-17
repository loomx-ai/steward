package gcp

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Opt-in independent Cloud Storage emulator test against a pinned
// fake-gcs-server. Bucket and object responses come from the unmodified
// emulator; only the storage.googleapis.com origin is rewritten to loopback.
// The emulator deletes buckets that still hold noncurrent versions, unlike
// Cloud Storage, so the test also shows Steward's own check stops that delete.
func TestCloudStorageBucketEmulator(t *testing.T) {
	endpoint := os.Getenv("STEWARD_GCS_EMULATOR_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_GCS_EMULATOR_URL to the pinned loopback fake-gcs-server")
	}
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Scheme != "http" || origin.Hostname() != "127.0.0.1" || origin.Port() == "" || origin.Path != "" {
		t.Fatal("emulator must use a loopback HTTP origin")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	local := &http.Client{Timeout: 10 * time.Second}
	call := func(method, path, body string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, endpoint+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		res, err := local.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		raw, _ := io.ReadAll(res.Body)
		var data map[string]any
		_ = json.Unmarshal(raw, &data)
		return res.StatusCode, data
	}
	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	versioned, empty := "steward-versions-"+suffix, "steward-empty-"+suffix
	for _, name := range []string{versioned, empty} {
		if code, _ := call("POST", "/storage/v1/b?project=sample-project", `{"name":"`+name+`","versioning":{"enabled":true}}`); code != 200 {
			t.Fatal("create bucket", code)
		}
	}
	t.Cleanup(func() {
		req, _ := http.NewRequest("DELETE", endpoint+"/storage/v1/b/"+versioned, nil)
		if res, err := local.Do(req); err == nil {
			res.Body.Close()
		}
	})
	if code, _ := call("POST", "/upload/storage/v1/b/"+versioned+"/o?uploadType=media&name=report.csv", "id,value"); code != 200 {
		t.Fatal("upload object", code)
	}
	if code, _ := call("DELETE", "/storage/v1/b/"+versioned+"/o/report.csv", ""); code != 200 && code != 204 {
		t.Fatal("delete live object", code)
	}
	if code, listed := call("GET", "/storage/v1/b/"+versioned+"/o?versions=true", ""); code != 200 || len(array(listed["items"])) != 1 {
		t.Fatal("emulator did not keep the noncurrent version", code)
	}
	forwarded := 0
	action := func(bucket string) *action {
		a := protocolAction(t, "storage.googleapis.com/Bucket", bucket, func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != "storage.googleapis.com" {
				t.Fatal("request outside Cloud Storage", req.URL)
			}
			forwarded++
			clone := req.Clone(req.Context())
			clone.URL.Scheme, clone.URL.Host, clone.Host, clone.RequestURI = origin.Scheme, origin.Host, origin.Host, ""
			return http.DefaultTransport.RoundTrip(clone)
		})
		a.client.number = "0" // fake-gcs-server reports project number 0 for every bucket.
		return a
	}
	request := contracts.ActionRequest{Action: "delete"}
	protected := action(versioned)
	check, err := protected.Preflight(ctx, request)
	if err != nil || check.Allowed || check.Reason != "bucket_not_empty" {
		t.Fatalf("bucket with a noncurrent version passed preflight: %+v %v", check, err)
	}
	if _, err := protected.Execute(ctx, request); err == nil {
		t.Fatal("bucket with a noncurrent version was deleted")
	}
	if code, _ := call("GET", "/storage/v1/b/"+versioned, ""); code != 200 {
		t.Fatal("protected bucket disappeared", code)
	}
	cleanup := action(empty)
	if check, err = cleanup.Preflight(ctx, request); err != nil || !check.Allowed {
		t.Fatalf("empty bucket blocked: %+v %v", check, err)
	}
	result, err := cleanup.Execute(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := cleanup.Wait(ctx, request, result)
	if err != nil || !wait.Done {
		t.Fatalf("bucket deletion not confirmed: %+v %v", wait, err)
	}
	if code, _ := call("GET", "/storage/v1/b/"+empty, ""); code != 404 {
		t.Fatal("bucket still exists", code)
	}
	if forwarded == 0 {
		t.Fatal("no request reached the emulator")
	}
}
