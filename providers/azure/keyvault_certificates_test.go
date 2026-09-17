package azure

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func keyVaultExample(t *testing.T, name string) map[string]any {
	t.Helper()
	wire, err := os.ReadFile("fixtures/keyvault/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err = json.Unmarshal(wire, &doc); err != nil {
		t.Fatal(err)
	}
	return object(object(object(doc["responses"])["200"])["body"])
}

func TestKeyVaultCertificateOfficialExamples(t *testing.T) {
	wire, err := os.ReadFile("fixtures/keyvault/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var sources []struct{ File, SHA256 string }
	if err = json.Unmarshal(wire, &sources); err != nil || len(sources) != 2 {
		t.Fatal("missing native evidence", err)
	}
	for _, source := range sources {
		wire, err = os.ReadFile("fixtures/keyvault/" + source.File)
		sum := sha256.Sum256(wire)
		if err != nil || hex.EncodeToString(sum[:]) != source.SHA256 {
			t.Fatal("changed native fixture", source.File, err)
		}
	}
	vault := keyVaultContext{id: strings.ToLower(resourceID(keyVaultType, "myvault")), endpoint: "https://myvault.vault.azure.net", location: "eastus"}
	for _, value := range array(keyVaultExample(t, "GetCertificates-example")["value"]) {
		if _, err := keyVaultObjectID(vault, text(object(value)["id"]), "certificates", false); err != nil {
			t.Fatal(err)
		}
	}
	current := keyVaultExample(t, "GetCertificate-example")
	name, err := keyVaultObjectID(vault, text(current["id"]), "certificates", true)
	if err != nil || name != "selfsignedcert01" {
		t.Fatal(name, err)
	}
	for _, bad := range []string{"https://other.vault.azure.net/certificates/a", "https://myvault.vault.azure.net.evil.invalid/certificates/a", "http://myvault.vault.azure.net/certificates/a", "https://myvault.vault.azure.net:8443/certificates/a", "https://myvault.vault.azure.net/keys/a", "https://myvault.vault.azure.net/certificates/a/b", "https://myvault.vault.azure.net/certificates/a_b", "https://myvault.vault.azure.net/certificates/a?x=1"} {
		if _, err := keyVaultObjectID(vault, bad, "certificates", false); err == nil {
			t.Fatal("accepted foreign certificate identity", bad)
		}
	}
}

type keyVaultFixture struct {
	runtime  *Runtime
	vault    string
	vaultURI string
	certs    map[string]map[string]any
	omitted  map[string]bool
	override func(*http.Request) (*http.Response, bool)
	mu       sync.Mutex
	scopes   map[string]string
}

func newKeyVaultFixture(t *testing.T) *keyVaultFixture {
	t.Helper()
	f := &keyVaultFixture{vault: strings.ToLower(resourceID(keyVaultType, "myvault")), vaultURI: "https://myvault.vault.azure.net/", certs: map[string]map[string]any{}, omitted: map[string]bool{}, scopes: map[string]string{}}
	for _, name := range []string{"listCert01", "listCert02"} {
		body := batchClone(keyVaultExample(t, "GetCertificate-example"))
		body["id"] = "https://myvault.vault.azure.net/certificates/" + name + "/f60f2a4f8ae442cfb41ca2090bd4b769"
		object(body["attributes"])["recoveryLevel"] = "Recoverable+Purgeable"
		f.certs[strings.ToLower(name)] = body
	}
	delete(f.certs["listcert02"], "kid")
	f.runtime = protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		if q.Method != "GET" {
			t.Fatal("inventory issued mutation", q.Method, q.URL)
		}
		if f.override != nil {
			if res, ok := f.override(q); ok {
				return res, nil
			}
		}
		path := strings.ToLower(q.URL.Path)
		switch {
		case q.URL.Host == "management.azure.com" && path == "/subscriptions/"+testSubscription+"/providers/microsoft.keyvault/vaults":
			return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": resourceID(keyVaultType, "myvault"), "type": keyVaultType, "name": "myvault", "location": "eastus"}}}, nil), nil
		case q.URL.Host == "management.azure.com" && path == f.vault:
			return jsonResponse(200, map[string]any{"id": resourceID(keyVaultType, "myvault"), "type": keyVaultType, "name": "myvault", "location": "eastus", "properties": map[string]any{"vaultUri": f.vaultURI}}, nil), nil
		case q.URL.Hostname() == "myvault.vault.azure.net" && path == "/certificates":
			if q.URL.Query().Get("api-version") != "2025-07-01" {
				t.Fatal("wrong data-plane API version")
			}
			values := []any{}
			for name, body := range f.certs {
				if !f.omitted[name] {
					values = append(values, map[string]any{"id": strings.TrimSuffix(text(body["id"]), "/f60f2a4f8ae442cfb41ca2090bd4b769"), "attributes": body["attributes"], "x5t": body["x5t"]})
				}
			}
			// Native pages are split with an absolute next link on port 443.
			if q.URL.Query().Get("$skiptoken") == "" {
				return jsonResponse(200, map[string]any{"value": values[:0], "nextLink": "https://myvault.vault.azure.net:443/certificates?api-version=2025-07-01&$skiptoken=page2&maxresults=25"}, nil), nil
			}
			return jsonResponse(200, map[string]any{"value": values, "nextLink": nil}, nil), nil
		case q.URL.Hostname() == "myvault.vault.azure.net" && strings.HasPrefix(path, "/certificates/") && strings.HasSuffix(path, "/"):
			if body := f.certs[strings.Split(path, "/")[2]]; body != nil {
				return jsonResponse(200, body, nil), nil
			}
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "CertificateNotFound"}}, nil), nil
		}
		t.Log("unrouted", q.URL.String())
		return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
	})
	product := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		if q.URL.Host == "login.microsoftonline.com" {
			wire, _ := q.GetBody()
			form := make([]byte, 4096)
			n, _ := wire.Read(form)
			values, _ := url.ParseQuery(string(form[:n]))
			f.mu.Lock()
			f.scopes[values.Get("scope")] = values.Get("scope")
			f.mu.Unlock()
		}
		return product.RoundTrip(q)
	})
	return f
}

func keyVaultRequest(f *keyVaultFixture) contracts.InventoryRequest {
	k := f.runtime.resourceKind(keyVaultCertificateType)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: keyVaultCertificateSource, ResourceKind: &k, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: "eastus"}}
}

func TestKeyVaultCertificateInventory(t *testing.T) {
	f := newKeyVaultFixture(t)
	batch, err := f.runtime.List(t.Context(), keyVaultRequest(f))
	if err != nil || !batch.Complete || len(batch.Items) != 2 || len(batch.AbsentNativeIDs) != 0 {
		t.Fatal(err, batch)
	}
	first := batch.Items[0]
	n := first.Normalized
	if first.NativeID != f.vault+"/certificates/listcert01" || first.NativeType != keyVaultCertificateType || first.State != "enabled" || first.Location != "eastus" || first.Actionable == nil || *first.Actionable || n["cleanup_protected"] != true {
		t.Fatal("invalid certificate authority", first)
	}
	if n["vaultId"] != f.vault || n["expires"] != "2039-12-31T23:59:59Z" || n["thumbprint"] != "fLi3U52HunIVNXubkEnf8tP6Wbo" || n["recoveryLevel"] != "Recoverable+Purgeable" {
		t.Fatal("certificate metadata lost", n)
	}
	if refs, _ := n[referenceKey("Microsoft.KeyVault/vaults/keys")].([]string); len(refs) != 1 || refs[0] != f.vault+"/keys/listcert01" {
		t.Fatal("managed key reference lost", n)
	}
	if _, ok := batch.Items[1].Normalized[referenceKey("Microsoft.KeyVault/vaults/keys")]; ok {
		t.Fatal("invented managed key reference")
	}
	wire, _ := json.Marshal(batch)
	if strings.Contains(string(wire), "MIICODCCAeag") {
		t.Fatal("certificate body leaked into inventory")
	}
	if f.scopes["https://vault.azure.net/.default"] == "" || f.scopes["https://management.azure.com/.default"] == "" {
		t.Fatal("data-plane token audience not separated", f.scopes)
	}
	req := keyVaultRequest(f)
	req.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westus"}
	if batch, err = f.runtime.List(t.Context(), req); err != nil || len(batch.Items) != 0 {
		t.Fatal("foreign region leaked", err)
	}
	if _, err = f.runtime.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: keyVaultCertificateGet, Parameters: map[string]any{"vaultBaseUrl": "https://myvault.vault.azure.net", "certificateName": "listCert01"}}); err == nil {
		t.Fatal("generic invocation reached the Key Vault data plane")
	}
}

func TestKeyVaultCertificateReconciliationAndBoundaries(t *testing.T) {
	for _, mode := range []string{"known-omitted-live", "known-absent", "vault-deleted", "listed-then-missing", "data-plane-forbidden", "foreign-vault-uri", "foreign-next-link", "filtered-next-link", "wrong-source", "forged-known"} {
		t.Run(mode, func(t *testing.T) {
			f := newKeyVaultFixture(t)
			req := keyVaultRequest(f)
			known := f.vault + "/certificates/listcert01"
			req.KnownNativeIDs = []string{known}
			switch mode {
			case "known-omitted-live":
				f.omitted["listcert01"] = true
			case "known-absent":
				delete(f.certs, "listcert01")
			case "vault-deleted":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, f.vault) {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
					}
					if strings.HasSuffix(strings.ToLower(q.URL.Path), "/microsoft.keyvault/vaults") {
						return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
					}
					return nil, false
				}
			case "listed-then-missing":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if strings.EqualFold(q.URL.Path, "/certificates/listcert02/") {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "CertificateNotFound"}}, nil), true
					}
					return nil, false
				}
			case "data-plane-forbidden":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if q.URL.Hostname() == "myvault.vault.azure.net" {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Forbidden", "innererror": map[string]any{"code": "ForbiddenByFirewall"}}}, nil), true
					}
					return nil, false
				}
			case "foreign-vault-uri":
				f.vaultURI = "https://othervault.vault.azure.net/"
			case "foreign-next-link", "filtered-next-link":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if q.URL.Hostname() == "myvault.vault.azure.net" && q.URL.Path == "/certificates" {
						next := "https://evil.invalid/certificates?api-version=2025-07-01&$skiptoken=x"
						if mode == "filtered-next-link" {
							next = "https://myvault.vault.azure.net/certificates?api-version=2025-07-01&includePending=true"
						}
						return jsonResponse(200, map[string]any{"value": []any{}, "nextLink": next}, nil), true
					}
					return nil, false
				}
			case "wrong-source":
				req.Source = inventorySource
			case "forged-known":
				req.KnownNativeIDs = []string{strings.Replace(known, testSubscription, "00000000-0000-0000-0000-000000000000", 1)}
			}
			batch, err := f.runtime.List(t.Context(), req)
			switch mode {
			case "known-omitted-live":
				if err != nil || len(batch.Items) != 2 || len(batch.AbsentNativeIDs) != 0 {
					t.Fatal("list omission retired a live certificate", err)
				}
			case "known-absent", "vault-deleted":
				if err != nil || len(batch.AbsentNativeIDs) != 1 || batch.AbsentNativeIDs[0] != known {
					t.Fatal("own absence not reconciled", err, batch.AbsentNativeIDs)
				}
			default:
				if err == nil || len(batch.AbsentNativeIDs) != 0 {
					t.Fatal("unsafe inventory accepted", err, batch)
				}
			}
		})
	}
}

func TestKeyVaultCertificateURLBoundaries(t *testing.T) {
	vault := keyVaultContext{id: strings.ToLower(resourceID(keyVaultType, "myvault")), endpoint: "https://myvault.vault.azure.net"}
	validate := keyVaultURL(vault, "2025-07-01")
	for _, good := range []string{"https://myvault.vault.azure.net/certificates?api-version=2025-07-01", "https://myvault.vault.azure.net:443/certificates?api-version=2025-07-01&$skiptoken=a&maxresults=25", "https://myvault.vault.azure.net/certificates/cert-1/?api-version=2025-07-01"} {
		if err := validate(good); err != nil {
			t.Fatal(good, err)
		}
	}
	for _, bad := range []string{"https://myvault.vault.azure.net/certificates", "https://myvault.vault.azure.net/certificates?api-version=7.4", "https://myvault.vault.azure.net/secrets/a/?api-version=2025-07-01", "https://myvault.vault.azure.net/certificates/a/b?api-version=2025-07-01", "https://myvault.vault.azure.net/deletedcertificates?api-version=2025-07-01", "https://user@myvault.vault.azure.net/certificates?api-version=2025-07-01", "https://myvault.vault.azure.net/certificates?api-version=2025-07-01&api-version=2025-07-01"} {
		if err := validate(bad); err == nil {
			t.Fatal("accepted", bad)
		}
	}
}
