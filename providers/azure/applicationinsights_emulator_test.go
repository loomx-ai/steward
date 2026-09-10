package azure

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Topaz v1.10.222-preview supplies its own component control plane. This checks
// catalog transport and final GET absence, not the still-pending inventory and
// cleanup planner. No Insights response is supplied by the transport adapter.
func TestApplicationInsightsIndependentEmulatorTransport(t *testing.T) {
	endpoint := os.Getenv("STEWARD_TOPAZ_EMULATOR_URL")
	if endpoint == "" {
		t.Skip("set STEWARD_TOPAZ_EMULATOR_URL and STEWARD_TOPAZ_EMULATOR_CA for the pinned loopback host")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || net.ParseIP(u.Hostname()) == nil || !net.ParseIP(u.Hostname()).IsLoopback() || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" || u.Port() == "" {
		t.Fatal("Topaz endpoint must be a bare loopback HTTPS origin")
	}
	cert, err := os.ReadFile(os.Getenv("STEWARD_TOPAZ_EMULATOR_CA"))
	roots := x509.NewCertPool()
	if err != nil || !roots.AppendCertsFromPEM(cert) {
		t.Fatal("Topaz fixture certificate is required", err)
	}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: roots, ServerName: "management.topaz.local.dev", MinVersion: tls.VersionTLS12}}
	t.Cleanup(transport.CloseIdleConnections)
	httpClient := &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: noRedirect}

	// Reproduce the emulator's public GenerateCliToken contract; this key and
	// principal are published test credentials, never Azure credentials.
	// https://github.com/TheCloudTheory/Topaz/blob/79e0ff08ab8919daca6eed2a3b22642a3f8ea083/Topaz.Identity/JwtHelper.cs
	encode := func(value any) string {
		payload, _ := json.Marshal(value)
		return base64.RawURLEncoding.EncodeToString(payload)
	}
	now := time.Now().Unix()
	token := encode(map[string]any{"alg": "HS256", "typ": "JWT"}) + "." + encode(map[string]any{"sub": "00000000-0000-0000-0000-000000000000", "oid": "00000000-0000-0000-0000-000000000000", "aud": "https://topaz.local.dev:8899", "iss": "https://topaz.local.dev:8899", "iat": now, "nbf": now - 5, "exp": now + 600})
	signer := hmac.New(sha256.New, []byte("yD1sMV1WcwVjSfNUxxLNfVHn5sbqD056LwOnkXCkIDnWkXcrg95plLQ3T1tvinLAnuNNiRRZrKyUvs6YzZnJ/A=="))
	signer.Write([]byte(token))
	token += "." + base64.RawURLEncoding.EncodeToString(signer.Sum(nil))
	groupName := "steward-insights-" + fmt.Sprint(time.Now().UnixNano())
	group := "/subscriptions/" + testSubscription + "/resourceGroups/" + groupName
	root := group + "/providers/Microsoft.Insights/components/component"
	call := func(method, path, version string, value any) (int, map[string]any) {
		t.Helper()
		var body []byte
		if value != nil {
			body, _ = json.Marshal(value)
		}
		req, _ := http.NewRequest(method, endpoint+path+"?api-version="+version, bytes.NewReader(body))
		req.Host = "management.topaz.local.dev:" + u.Port()
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		res, err := httpClient.Do(req)
		if err != nil {
			t.Fatal("Topaz fixture request failed", err)
		}
		defer res.Body.Close()
		payload, err := io.ReadAll(res.Body)
		if err != nil {
			t.Fatal(err)
		}
		var data map[string]any
		if len(payload) != 0 && json.Unmarshal(payload, &data) != nil {
			t.Fatal("Topaz returned a non-JSON fixture response", res.StatusCode)
		}
		return res.StatusCode, data
	}
	status, _ := call("PUT", group, resourcesVersion, map[string]any{"location": "westeurope"})
	if status != 200 && status != 201 {
		t.Fatal("Topaz resource-group fixture failed", status)
	}
	t.Cleanup(func() {
		status, _ := call("DELETE", root, "2020-02-02", nil)
		if status != 200 && status != 204 && status != 404 {
			t.Errorf("Topaz component cleanup: %d", status)
		}
		status, _ = call("DELETE", group, resourcesVersion, nil)
		if status != 200 && status != 202 && status != 204 && status != 404 {
			t.Errorf("Topaz resource-group cleanup: %d", status)
		}
	})
	status, created := call("PUT", root, "2020-02-02", map[string]any{"location": "westeurope", "kind": "web", "properties": map[string]any{"Application_Type": "web"}, "tags": map[string]any{"steward-test": "isolated"}})
	if status != 200 && status != 201 {
		t.Fatal("Topaz component fixture failed", status)
	}
	privateKey := text(object(created["properties"])["InstrumentationKey"])
	if privateKey == "" {
		t.Fatal("Topaz did not return its component key")
	}
	requests := 0
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.URL.Host != "management.azure.com" || !strings.HasPrefix(strings.ToLower(req.URL.Path), strings.ToLower(group)+"/providers/microsoft.insights/components") {
			t.Fatal("unexpected Topaz adapter request", req.Method, req.URL)
		}
		requests++
		copy := req.Clone(req.Context())
		copy.URL.Scheme, copy.URL.Host = u.Scheme, u.Host
		copy.Host = "management.topaz.local.dev:" + u.Port()
		copy.Header.Set("Authorization", "Bearer "+token)
		return transport.RoundTrip(copy)
	})
	invoke := func(operation string) (contracts.InvocationResult, error) {
		parameters := map[string]any{"resourceGroupName": groupName}
		if operation != "Components_ListByResourceGroup" {
			parameters["resourceName"] = "component"
		}
		return r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.Insights." + operation, Parameters: parameters})
	}
	read, err := invoke("Components_Get")
	if err != nil || !strings.EqualFold(text(read.Data["id"]), root) {
		t.Fatal("independent component GET failed", err)
	}
	encoded, _ := json.Marshal(read)
	if strings.Contains(string(encoded), privateKey) {
		t.Fatal("emulator component key escaped through Invoke")
	}
	props := object(read.Data["properties"])
	if props["applicationType"] != "web" || props["Application_Type"] != nil || props["AppId"] != nil || props["CreationDate"] != nil {
		t.Fatal("pinned Topaz property-shape discrepancy changed")
	}
	listed, err := invoke("Components_ListByResourceGroup")
	if err != nil || len(array(listed.Data["value"])) != 1 || !strings.EqualFold(text(object(array(listed.Data["value"])[0])["id"]), root) {
		t.Fatal("independent component LIST failed", err, listed)
	}
	_, err = invoke("APIKeys_List")
	if !isNotFound(err) {
		t.Fatal("pinned Topaz child-API limitation changed", err)
	}
	if _, err := invoke("Components_Delete"); err != nil {
		t.Fatal("independent component DELETE failed", err)
	}
	if _, err := invoke("Components_Get"); !isNotFound(err) {
		t.Fatal("independent component GET did not prove absence", err)
	}
	if requests != 5 {
		t.Fatal("incomplete independent protocol check", requests)
	}
	t.Log("Topaz component GET/LIST/DELETE/final GET passed; APIKeys LIST is unsupported, and native AppId/CreationDate are absent")
}
