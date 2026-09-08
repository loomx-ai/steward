package gcp

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const kubeClusterID = "//container.googleapis.com/projects/sample-project/locations/us-central1/clusters/test"

type kubernetesFixture struct {
	client        *client
	live          map[string]any
	native        http.HandlerFunc
	credential    contracts.Credential
	tokens, calls atomic.Int32
}

// Native Google metadata, OAuth and the cluster API use real HTTPS connections.
// A custom dialer routes the original hosts to a local server without changing
// HTTP origins, SNI, certificate verification or bearer-token behavior.
func newKubernetesFixture(t *testing.T, handler http.HandlerFunc) *kubernetesFixture {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	certificate := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		DNSNames: []string{"container.googleapis.com", "oauth2.googleapis.com", "compute.googleapis.com"}, IPAddresses: []net.IP{net.ParseIP("192.0.2.5")},
		KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, IsCA: true, BasicConstraintsValid: true}
	der, err := x509.CreateCertificate(rand.Reader, certificate, certificate, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	f := &kubernetesFixture{live: map[string]any{"name": "test", "id": "cluster-uid", "endpoint": "192.0.2.5", "masterAuth": map[string]any{"clusterCaCertificate": base64.StdEncoding.EncodeToString(ca), "clientKey": "PRIVATE_CLIENT_KEY"}}}
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Host == "oauth2.googleapis.com" && r.URL.Path == "/token" {
			f.tokens.Add(1)
			io.WriteString(w, `{"access_token":"selected-account-token","token_type":"Bearer","expires_in":3600}`)
			return
		}
		if r.Header.Get("Authorization") != "Bearer selected-account-token" {
			t.Error("request used another credential")
		}
		if f.native != nil && r.Host != "192.0.2.5" {
			f.native(w, r)
			return
		}
		if r.Host == "container.googleapis.com" && r.URL.Path == "/v1/projects/sample-project/locations/us-central1/clusters/test" {
			json.NewEncoder(w).Encode(f.live)
			return
		}
		f.calls.Add(1)
		if r.Host != "192.0.2.5" {
			t.Errorf("unexpected destination %q", r.Host)
		}
		w.Header().Set("Audit-Id", "kube-audit-id")
		handler(w, r)
	}))
	server.Config.ErrorLog = log.New(io.Discard, "", 0)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}}
	server.StartTLS()
	t.Cleanup(server.Close)
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(ca)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{RootCAs: roots}
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	t.Cleanup(transport.CloseIdleConnections)
	credential, _ := testCredential(t)
	f.credential = credential
	f.client, err = newClient(credential, transport)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *kubernetesFixture) connect(t *testing.T, ctx context.Context) *kubernetesClient {
	t.Helper()
	k, err := f.client.kubernetes(ctx, kubeClusterID, "cluster-uid")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k.http.Transport.(kubernetesTransport).base.CloseIdleConnections)
	return k
}

func kubeService(name string) map[string]any {
	return map[string]any{"apiVersion": "v1", "kind": "Service", "metadata": map[string]any{"namespace": "default", "name": name, "uid": "uid-" + name, "resourceVersion": "101", "annotations": map[string]any{"example.com/private": "PRIVATE_ANNOTATION"}}, "spec": map[string]any{"type": "LoadBalancer"}}
}

func kubeList(collection kubernetesCollection, rv, token string, items ...map[string]any) map[string]any {
	if items == nil {
		items = []map[string]any{}
	}
	return map[string]any{"apiVersion": collection.api, "kind": collection.kind + "List", "metadata": map[string]any{"resourceVersion": rv, "continue": token}, "items": items}
}

func TestKubernetesTLSAuthenticationPagingDeletePreconditionsAndLogs(t *testing.T) {
	fixture, err := os.ReadFile("fixtures/kubernetes-api.json")
	if err != nil {
		t.Fatal(err)
	}
	var official map[string]any
	if err := json.Unmarshal(fixture, &official); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	if err := compiler.AddResource("https://fixture.test/kubernetes.json", official["document"]); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("https://fixture.test/kubernetes.json#/definitions/io.k8s.apimachinery.pkg.apis.meta.v1.DeleteOptions")
	if err != nil {
		t.Fatal(err)
	}
	var deletes int
	f := newKubernetesFixture(t, func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "DELETE" && r.URL.Path == "/api/v1/namespaces/default/services/web":
			deletes++
			var data map[string]any
			if err := json.NewDecoder(r.Body).Decode(&data); err != nil {
				t.Error(err)
			}
			if err := schema.Validate(data); err != nil {
				t.Error(err)
			}
			if len(data) != 3 || object(data["preconditions"])["uid"] != "uid-web" || object(data["preconditions"])["resourceVersion"] != "101" {
				t.Errorf("unsafe deletion options: %v", data)
			}
			io.WriteString(w, `{"apiVersion":"v1","kind":"Status","status":"Success"}`)
		case r.Method == "GET" && r.URL.Path == "/api/v1/services":
			q := r.URL.Query()
			if q.Get("limit") != "500" || q.Has("resourceVersion") {
				t.Errorf("unexpected list query %v", q)
			}
			if q.Get("continue") == "" {
				json.NewEncoder(w).Encode(kubeList(serviceCollection, "100", "opaque+/=", kubeService("web")))
			} else if q.Get("continue") == "opaque+/=" {
				json.NewEncoder(w).Encode(kubeList(serviceCollection, "100", "", kubeService("api")))
			} else {
				t.Errorf("changed cursor %q", q.Get("continue"))
			}
		default:
			t.Errorf("unexpected Kubernetes method %s %s", r.Method, r.URL)
		}
	})
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	k := f.connect(t, ctx)
	items, err := k.list(ctx, serviceCollection)
	if err != nil || len(items) != 2 {
		t.Fatalf("items=%v error=%v", items, err)
	}
	result, err := k.delete(ctx, serviceCollection, items[0])
	if err != nil || result.RequestID != "kube-audit-id" || deletes != 1 || f.tokens.Load() != 1 {
		t.Fatalf("delete=%+v tokens=%d err=%v", result, f.tokens.Load(), err)
	}
	encoded, _ := json.Marshal(logs)
	for _, secret := range []string{"PRIVATE_ANNOTATION", "PRIVATE_CLIENT_KEY", "selected-account-token", "opaque+/="} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("secret leaked into logs: %s", secret)
		}
	}
	if object(object(items[0]["metadata"])["annotations"])["example.com/private"] != "PRIVATE_ANNOTATION" {
		t.Fatal("logging mutated live response")
	}
}

func TestKubernetesRejectsEndpointAndIdentitySubstitution(t *testing.T) {
	f := newKubernetesFixture(t, func(w http.ResponseWriter, r *http.Request) { t.Error("unverified endpoint received credentials") })
	for _, uid := range []string{"", "another-uid"} {
		if _, err := f.client.kubernetes(context.Background(), kubeClusterID, uid); err == nil {
			t.Fatal("cluster identity substitution accepted")
		}
	}
	for _, host := range []string{"https://192.0.2.5", "192.0.2.5:8443", "127.0.0.1", "169.254.169.254", "::1", "0.0.0.0", "foreign.example", "192.0.2.5/path", "192.0.2.5@foreign.example"} {
		f.live["endpoint"] = host
		if _, err := f.client.kubernetes(context.Background(), kubeClusterID, "cluster-uid"); err == nil {
			t.Fatalf("unsafe endpoint %q accepted", host)
		}
	}
	f.live["endpoint"] = "192.0.2.6" // Valid IP, but the TLS certificate belongs to .5.
	k := f.connect(t, context.Background())
	if _, err := k.list(context.Background(), serviceCollection); err == nil || f.calls.Load() != 0 {
		t.Fatal("certificate hostname mismatch transmitted credentials")
	}
	f.live["endpoint"] = "192.0.2.5"
	object(f.live["masterAuth"])["clusterCaCertificate"] = "bad-ca"
	if _, err := f.client.kubernetes(context.Background(), kubeClusterID, "cluster-uid"); err == nil {
		t.Fatal("invalid cluster CA accepted")
	}
}

func TestKubernetesDNSEndpointUsesPublicCAAndNeverFollowsRedirects(t *testing.T) {
	for _, host := range []string{"gke-cluster.example.gke.goog", "foreign.example", "x.gke.goog.evil.example", "x.gke.goog:443", "x.gke.goog/path"} {
		live := map[string]any{"endpoint": "192.0.2.5", "controlPlaneEndpointsConfig": map[string]any{"ipEndpointsConfig": map[string]any{"enabled": false}, "dnsEndpointConfig": map[string]any{"allowExternalTraffic": true, "endpoint": host}}}
		endpoint, config, err := kubernetesEndpoint(live)
		if host == "gke-cluster.example.gke.goog" {
			if err != nil || endpoint != host || config.RootCAs != nil || config.InsecureSkipVerify {
				t.Fatalf("DNS TLS policy: %s %+v %v", endpoint, config, err)
			}
		} else if err == nil {
			t.Fatalf("unsafe DNS endpoint %q", host)
		}
	}
	f := newKubernetesFixture(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://foreign.example/private", http.StatusTemporaryRedirect)
	})
	k := f.connect(t, context.Background())
	if _, err := k.list(context.Background(), serviceCollection); err == nil || f.calls.Load() != 1 {
		t.Fatal("Kubernetes redirect followed")
	}
	req, _ := http.NewRequest("GET", "https://foreign.example/api/v1/services", nil)
	if _, err := k.http.Do(req); err == nil || f.calls.Load() != 1 {
		t.Fatal("transport forwarded a foreign request")
	}
	for _, path := range []string{"/../escape", "//foreign.example/api", "/api/v1/%2e%2e", "/api/v1/services?token=private"} {
		if _, err := k.request(context.Background(), "GET", path, nil, nil); err == nil {
			t.Fatalf("unsafe path %s accepted", path)
		}
	}
}

func TestKubernetesListFailsEntireSnapshot(t *testing.T) {
	for _, fault := range []string{"changed-version", "repeat-token", "duplicate-object", "invalid-items", "bad-identity", "expired", "forbidden"} {
		t.Run(fault, func(t *testing.T) {
			f := newKubernetesFixture(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("continue") == "" {
					json.NewEncoder(w).Encode(kubeList(serviceCollection, "100", "page2", kubeService("web")))
					return
				}
				data := kubeList(serviceCollection, "100", "", kubeService("api"))
				switch fault {
				case "changed-version":
					object(data["metadata"])["resourceVersion"] = "101"
				case "repeat-token":
					object(data["metadata"])["continue"] = "page2"
				case "duplicate-object":
					data["items"] = []map[string]any{kubeService("web")}
				case "invalid-items":
					data["items"] = map[string]any{}
				case "bad-identity":
					data["items"] = []map[string]any{kubeService("../foreign")}
				case "expired", "forbidden":
					status, reason := 410, "Expired"
					if fault == "forbidden" {
						status, reason = 403, "Forbidden"
					}
					w.WriteHeader(status)
					json.NewEncoder(w).Encode(map[string]any{"apiVersion": "v1", "kind": "Status", "reason": reason, "message": "PRIVATE_ERROR"})
					return
				}
				json.NewEncoder(w).Encode(data)
			})
			k := f.connect(t, context.Background())
			items, err := k.list(context.Background(), serviceCollection)
			if err == nil || items != nil || strings.Contains(err.Error(), "PRIVATE_ERROR") {
				t.Fatalf("partial/unsafe snapshot: %v %v", items, err)
			}
			if fault == "forbidden" {
				var call *contracts.ProviderCallError
				if !errors.As(err, &call) || call.Provider.RequestID != "kube-audit-id" || call.Provider.Category != execution.ErrorPermissionDenied {
					t.Fatalf("permission provenance lost: %v", err)
				}
			}
		})
	}
}

func TestKubernetesGatewayAPIDiscoveryAndCancellation(t *testing.T) {
	for _, status := range []int{200, 403, 404} {
		f := newKubernetesFixture(t, func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/apis/gateway.networking.k8s.io" {
				t.Error("invented API discovery path")
			}
			w.WriteHeader(status)
			if status == 200 {
				io.WriteString(w, `{"apiVersion":"v1","kind":"APIGroup","name":"gateway.networking.k8s.io","versions":[{"groupVersion":"gateway.networking.k8s.io/v1beta1","version":"v1beta1"},{"groupVersion":"gateway.networking.k8s.io/v1","version":"v1"}]}`)
			} else {
				io.WriteString(w, `{"apiVersion":"v1","kind":"Status","reason":"Forbidden"}`)
			}
		})
		k := f.connect(t, context.Background())
		collection, exists, err := k.gatewayCollection(context.Background())
		if status == 200 && (err != nil || !exists || collection.api != "gateway.networking.k8s.io/v1") || status == 403 && err == nil || status == 404 && (err != nil || exists) {
			t.Fatalf("discovery %d: %+v %v %v", status, collection, exists, err)
		}
	}
	started := make(chan struct{})
	f := newKubernetesFixture(t, func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() })
	k := f.connect(t, context.Background())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := k.list(ctx, serviceCollection); done <- err }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("request did not start")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancellation lost: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request did not cancel")
	}
}
