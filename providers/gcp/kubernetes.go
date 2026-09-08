package gcp

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Kubernetes access is bound to a fresh Container API identity and its native
// endpoint. Cloud inventory fields, kubeconfig and ambient credentials never
// select a destination or an authentication mechanism.
type kubernetesClient struct {
	http   *http.Client
	origin string
}

func (c *client) kubernetes(ctx context.Context, clusterID, uid string) (*kubernetesClient, error) {
	live, err := c.nativeGet(ctx, clusterType, clusterID)
	if err != nil {
		return nil, err
	}
	if uid == "" || text(live["id"]) != uid || text(live["name"]) != last(clusterID) {
		return nil, groupDenied("gke_cluster_identity_changed")
	}
	if link := text(live["selfLink"]); link != "" && c.canonicalName(link) != clusterID {
		return nil, fmt.Errorf("GKE cluster identity mismatch")
	}
	host, tlsConfig, err := kubernetesEndpoint(live)
	if err != nil {
		return nil, err
	}
	source, ok := c.http.Transport.(*tokenTransport)
	if !ok {
		return nil, fmt.Errorf("GKE requires explicit Google OAuth authentication")
	}
	base, ok := source.base.(*http.Transport)
	if !ok {
		base = http.DefaultTransport.(*http.Transport)
	}
	transport := base.Clone()
	transport.TLSClientConfig = tlsConfig
	transport.DialTLSContext, transport.DialTLS = nil, nil
	origin := "https://" + host
	return &kubernetesClient{origin: origin, http: &http.Client{
		Transport: kubernetesTransport{base: transport, source: source, origin: origin},
		Timeout:   60 * time.Second, CheckRedirect: noRedirect,
	}}, nil
}

func kubernetesEndpoint(live map[string]any) (string, *tls.Config, error) {
	endpoints := object(live["controlPlaneEndpointsConfig"])
	ips := object(endpoints["ipEndpointsConfig"])
	host := text(live["endpoint"])
	if ips["enabled"] == false {
		host = ""
	} else if ips["enablePublicEndpoint"] == false {
		host = text(ips["privateEndpoint"])
	} else if object(live["privateClusterConfig"])["enablePrivateEndpoint"] == true {
		host = text(object(live["privateClusterConfig"])["privateEndpoint"])
	}
	config := &tls.Config{MinVersion: tls.VersionTLS12}
	if host != "" {
		ip, err := netip.ParseAddr(host)
		if err != nil || !ip.IsGlobalUnicast() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.Zone() != "" {
			return "", nil, fmt.Errorf("invalid GKE control plane IP endpoint")
		}
		pem, err := base64.StdEncoding.DecodeString(text(object(live["masterAuth"])["clusterCaCertificate"]))
		roots := x509.NewCertPool()
		if err != nil || !roots.AppendCertsFromPEM(pem) {
			return "", nil, fmt.Errorf("GKE control plane certificate is missing or invalid")
		}
		config.RootCAs = roots
		if ip.Is6() {
			host = "[" + ip.String() + "]"
		}
	} else {
		dns := object(endpoints["dnsEndpointConfig"])
		host = text(dns["endpoint"])
		if dns["allowExternalTraffic"] != true || !strings.HasSuffix(host, ".gke.goog") || !kubernetesName(host, 253) {
			return "", nil, fmt.Errorf("GKE has no accessible control plane endpoint")
		}
		// GKE DNS endpoints use Google Front End public certificates, not the
		// cluster's CA. Leave RootCAs nil to verify against the system roots.
	}
	return host, config, nil
}

type kubernetesTransport struct {
	base   *http.Transport
	source *tokenTransport
	origin string
}

func (t kubernetesTransport) CloseIdleConnections() { t.base.CloseIdleConnections() }

func (t kubernetesTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme+"://"+request.URL.Host != t.origin || request.URL.User != nil || request.URL.Fragment != "" {
		return nil, fmt.Errorf("Kubernetes request left its verified cluster")
	}
	token, err := t.source.accessToken(request.Context())
	if err != nil {
		return nil, err
	}
	clone := request.Clone(request.Context())
	token.SetAuthHeader(clone)
	return t.base.RoundTrip(clone)
}

// Network object bodies can contain private hostnames, annotations and arbitrary
// controller settings. Logs retain request provenance and counts only.
func kubernetesLog(value map[string]any) map[string]any {
	result := map[string]any{}
	for _, key := range []string{"method", "path", "request_id", "status_code"} {
		if v, ok := value[key]; ok {
			result[key] = v
		}
	}
	if data, ok := value["body"].(map[string]any); ok {
		result["kind"] = data["kind"]
		result["item_count"] = len(array(data["items"]))
	}
	return result
}

func (k *kubernetesClient) request(ctx context.Context, method, path string, query url.Values, body []byte) (contracts.InvocationResult, error) {
	u, err := url.Parse(k.origin + path)
	if err != nil || !strings.HasPrefix(path, "/") || strings.Contains(path, "//") || strings.ContainsAny(path, "?#%\\") || u.Scheme+"://"+u.Host != k.origin {
		return contracts.InvocationResult{}, fmt.Errorf("invalid Kubernetes API path")
	}
	for _, segment := range strings.Split(path, "/") {
		if segment == "." || segment == ".." {
			return contracts.InvocationResult{}, fmt.Errorf("invalid Kubernetes API path")
		}
	}
	u.RawQuery = query.Encode()
	return requestJSON(ctx, k.http, method, u, body, kubernetesLog)
}

type kubernetesCollection struct{ api, resource, kind string }

var serviceCollection = kubernetesCollection{"v1", "services", "Service"}
var ingressCollection = kubernetesCollection{"networking.k8s.io/v1", "ingresses", "Ingress"}

func (c kubernetesCollection) prefix() string {
	if c.api == "v1" {
		return "/api/v1"
	}
	return "/apis/" + c.api
}

var kubernetesLabel = regexp.MustCompile(`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`)

func kubernetesName(name string, max int) bool {
	if len(name) == 0 || len(name) > max {
		return false
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) > 63 || !kubernetesLabel.MatchString(label) {
			return false
		}
	}
	return true
}

func (c kubernetesCollection) path(data map[string]any) (string, error) {
	meta := object(data["metadata"])
	namespace, name := text(meta["namespace"]), text(meta["name"])
	if !kubernetesName(namespace, 63) || strings.Contains(namespace, ".") || !kubernetesName(name, 253) || text(meta["uid"]) == "" || text(meta["resourceVersion"]) == "" {
		return "", fmt.Errorf("Kubernetes object identity is incomplete")
	}
	if kind := text(data["kind"]); kind != "" && kind != c.kind {
		return "", fmt.Errorf("unexpected Kubernetes object kind")
	}
	if api := text(data["apiVersion"]); api != "" && api != c.api {
		return "", fmt.Errorf("unexpected Kubernetes API version")
	}
	return c.prefix() + "/namespaces/" + namespace + "/" + c.resource + "/" + name, nil
}

// Kubernetes continuation pages share one resourceVersion. An expired (410)
// snapshot, malformed page or repeated token fails the entire collection.
func (k *kubernetesClient) list(ctx context.Context, collection kubernetesCollection) ([]map[string]any, error) {
	query := url.Values{"limit": {"500"}}
	var items []map[string]any
	version := ""
	seen, identities := map[string]bool{}, map[string]bool{}
	for {
		response, err := k.request(ctx, "GET", collection.prefix()+"/"+collection.resource, query, nil)
		if err != nil {
			return nil, err
		}
		data := response.Data
		meta := object(data["metadata"])
		rv := text(meta["resourceVersion"])
		if rv == "" || (version != "" && version != rv) || data["kind"] != collection.kind+"List" || data["apiVersion"] != collection.api {
			return nil, fmt.Errorf("Kubernetes list snapshot is invalid or changed")
		}
		version = rv
		records, ok := data["items"].([]any)
		if !ok {
			return nil, fmt.Errorf("Kubernetes list items are invalid")
		}
		for _, raw := range records {
			item := object(raw)
			path, err := collection.path(item)
			if err != nil {
				return nil, err
			}
			if identities[path] {
				return nil, fmt.Errorf("Kubernetes list repeated an object")
			}
			identities[path] = true
			items = append(items, item)
		}
		token := text(meta["continue"])
		if token == "" {
			return items, nil
		}
		if seen[token] {
			return nil, fmt.Errorf("Kubernetes pagination did not advance")
		}
		seen[token] = true
		query.Set("continue", token)
	}
}

func (k *kubernetesClient) delete(ctx context.Context, collection kubernetesCollection, data map[string]any) (contracts.InvocationResult, error) {
	path, err := collection.path(data)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	meta := object(data["metadata"])
	body, err := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "DeleteOptions",
		"preconditions": map[string]any{"uid": meta["uid"], "resourceVersion": meta["resourceVersion"]},
	})
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	return k.request(ctx, "DELETE", path, nil, body)
}

func (k *kubernetesClient) gatewayCollection(ctx context.Context) (kubernetesCollection, bool, error) {
	response, err := k.request(ctx, "GET", "/apis/gateway.networking.k8s.io", nil, nil)
	if isNotFound(err) {
		return kubernetesCollection{}, false, nil
	}
	if err != nil {
		return kubernetesCollection{}, false, err
	}
	if response.Data["kind"] != "APIGroup" || response.Data["name"] != "gateway.networking.k8s.io" {
		return kubernetesCollection{}, false, fmt.Errorf("invalid Kubernetes Gateway API discovery")
	}
	for _, version := range []string{"v1", "v1beta1"} {
		for _, raw := range array(response.Data["versions"]) {
			if object(raw)["groupVersion"] == "gateway.networking.k8s.io/"+version {
				return kubernetesCollection{"gateway.networking.k8s.io/" + version, "gateways", "Gateway"}, true, nil
			}
		}
	}
	return kubernetesCollection{}, false, fmt.Errorf("Kubernetes Gateway API has no supported version")
}
