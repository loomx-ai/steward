package azure

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Opt-in independent Blob emulator test against pinned Azurite with OAuth.
// Only the ARM account read is synthetic; every Blob request reaches Azurite
// through the account's public endpoint rewritten to its loopback path-style
// account URL. Azurite checks token claims, not signatures, and its HTTPS
// certificate is self-signed.
func TestBlobContainerEmptinessAzurite(t *testing.T) {
	endpoint := os.Getenv("STEWARD_AZURITE_BLOB_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_AZURITE_BLOB_URL to the pinned loopback Azurite Blob origin")
	}
	origin, err := url.Parse(endpoint)
	if err != nil || origin.Scheme != "https" || net.ParseIP(origin.Hostname()) == nil || !net.ParseIP(origin.Hostname()).IsLoopback() || origin.Path != "" {
		t.Fatal("Azurite must use a loopback HTTPS origin")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	claims, _ := json.Marshal(map[string]any{"aud": "https://storage.azure.com", "iss": "https://sts.windows.net/" + testTenant + "/", "tid": testTenant, "oid": "33333333-4444-5555-6666-777777777777", "nbf": time.Now().Add(-time.Minute).Unix(), "iat": time.Now().Add(-time.Minute).Unix(), "exp": time.Now().Add(time.Hour).Unix()})
	token := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + base64.RawURLEncoding.EncodeToString(claims) + ".signature"
	loopback := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, Proxy: nil} // #nosec G402 -- loopback emulator only
	t.Cleanup(loopback.CloseIdleConnections)
	const account = "devstoreaccount1"
	container := "steward" + strconv.FormatInt(time.Now().UnixNano(), 36)
	emulator := func(method, path string, body []byte, headers map[string]string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(ctx, method, endpoint+"/"+account+"/"+container+path, bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("x-ms-version", "2023-11-03")
		for key, value := range headers {
			req.Header.Set(key, value)
		}
		res, err := loopback.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		return res.StatusCode
	}
	if code := emulator("PUT", "?restype=container", nil, nil); code != 201 {
		t.Fatal("create container", code)
	}
	id := strings.ToLower(resourceID(storageType, "teststorage")) + "/blobservices/default/containers/" + container
	blobRequests := 0
	c := directClient(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == "management.azure.com" {
			return jsonResponse(200, map[string]any{"properties": map[string]any{"primaryEndpoints": map[string]any{"blob": "https://teststorage.blob.core.windows.net/"}}}, nil), nil
		}
		if req.URL.Host != "teststorage.blob.core.windows.net" || req.Method != "GET" {
			t.Fatal("unexpected Blob request", req.Method, req.URL)
		}
		blobRequests++
		clone := req.Clone(req.Context())
		clone.URL.Scheme, clone.URL.Host, clone.Host = origin.Scheme, origin.Host, origin.Host
		clone.URL.Path = "/" + account + req.URL.Path
		clone.Header.Set("Authorization", "Bearer "+token)
		return loopback.RoundTrip(clone)
	})
	check := func(want bool, stage string) {
		t.Helper()
		empty, err := c.blobContainerEmpty(ctx, id)
		if err != nil || empty != want {
			t.Fatalf("%s: empty=%v err=%v", stage, empty, err)
		}
	}
	check(true, "new container")
	// An uploaded but uncommitted block leaves a blob that cleanup must see.
	if code := emulator("PUT", "/staged.bin?comp=block&blockid="+url.QueryEscape(base64.StdEncoding.EncodeToString([]byte("block-1"))), []byte("x"), nil); code != 201 {
		t.Fatal("stage block", code)
	}
	check(false, "uncommitted block")
	if code := emulator("DELETE", "?restype=container", nil, nil); code != 202 {
		t.Fatal("reset container", code)
	}
	container += "b"
	id += "b"
	if code := emulator("PUT", "?restype=container", nil, nil); code != 201 {
		t.Fatal("create second container", code)
	}
	if code := emulator("PUT", "/report.csv", []byte("id,value"), map[string]string{"x-ms-blob-type": "BlockBlob"}); code != 201 {
		t.Fatal("upload blob", code)
	}
	check(false, "committed blob")
	if code := emulator("DELETE", "/report.csv", nil, nil); code != 202 {
		t.Fatal("delete blob", code)
	}
	check(true, "blob deleted")
	emulator("DELETE", "?restype=container", nil, nil)
	if blobRequests != 4 {
		t.Fatal("emptiness checks did not reach Azurite", blobRequests)
	}
}
