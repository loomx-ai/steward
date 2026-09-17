package workloadidentity

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/requestmeta"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func testIssuer(t *testing.T) *Issuer {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "signing.pem")
	if err = os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)}), 0600); err != nil {
		t.Fatal(err)
	}
	i, err := Load(Config{IssuerURL: "https://identity.example/oidc/workspaces/test", WorkspaceID: "test", SigningKeyFile: path})
	if err != nil {
		t.Fatal(err)
	}
	return i
}

func claims(t *testing.T, i *Issuer, token string) map[string]any {
	t.Helper()
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		t.Fatal("missing JWT")
	}
	hash := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	if rsa.VerifyPKCS1v15(&i.key.PublicKey, crypto.SHA256, hash[:], sig) != nil {
		t.Fatal("invalid signature")
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var c map[string]any
	if json.Unmarshal(raw, &c) != nil {
		t.Fatal("invalid claims")
	}
	return c
}

func TestIssuerMetadataClaimsAndRotation(t *testing.T) {
	i := testIssuer(t)
	i.now = func() time.Time { return time.Unix(2000000000, 0) }
	token, err := i.sign(context.Background(), "con_test", "aws", "sts.amazonaws.com", "write", "job_test")
	if err != nil {
		t.Fatal(err)
	}
	c := claims(t, i, token)
	if c["iss"] != i.URL || c["sub"] != "workspace:test:connection:con_test:run_phase:write" || c["aud"] != "sts.amazonaws.com" || c["steward_run_id"] != "job_test" || c["exp"].(float64)-c["iat"].(float64) != 300 {
		t.Fatalf("unexpected claims: %#v", c)
	}
	other, _ := i.sign(context.Background(), "con_test", "aws", "sts.amazonaws.com", "write", "job_test")
	if claims(t, i, other)["jti"] == c["jti"] {
		t.Fatal("JWT IDs reused")
	}
	for _, name := range []string{"openid-configuration", "jwks"} {
		w := httptest.NewRecorder()
		i.ServeHTTP(w, httptest.NewRequest("GET", i.URL+"/.well-known/"+name, nil))
		if w.Code != 200 || !json.Valid(w.Body.Bytes()) || strings.Contains(w.Body.String(), "PRIVATE") || strings.Contains(w.Body.String(), `"d":`) {
			t.Fatalf("unsafe public metadata: %s", w.Body.String())
		}
	}
	newKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	oldPublic, _ := x509.MarshalPKIXPublicKey(&i.key.PublicKey)
	bundle := append(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(newKey)}), pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: oldPublic})...)
	path := filepath.Join(t.TempDir(), "rotated.pem")
	if err = os.WriteFile(path, bundle, 0600); err != nil {
		t.Fatal(err)
	}
	rotated, err := Load(Config{i.URL, i.WorkspaceID, path})
	if err != nil || len(rotated.keys) != 2 || rotated.kid == i.kid {
		t.Fatalf("rotation failed: %v", err)
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(Config{i.URL, i.WorkspaceID, path}); err == nil {
		t.Fatal("accepted public-readable signing key")
	}
	for _, issuerURL := range []string{"http://identity.example", "https://identity.example/", "https://user@identity.example", "https://identity.example?a=b"} {
		if _, err = Load(Config{issuerURL, "test", path}); err == nil {
			t.Fatalf("accepted issuer %q", issuerURL)
		}
	}
}

type fakeConnections struct {
	persistence.ConnectionRepository
	value asset.CloudConnection
}

func (r *fakeConnections) GetConnection(context.Context, asset.ConnectionID) (asset.CloudConnection, error) {
	return r.value, nil
}

type fakeCredentials struct {
	persistence.CredentialRepository
	value asset.ConnectionCredential
}

func (r *fakeCredentials) GetCredential(context.Context, asset.ConnectionID) (asset.ConnectionCredential, error) {
	return r.value, nil
}

type fakeRepositories struct {
	persistence.Repositories
	c *fakeConnections
	k *fakeCredentials
}

func (r fakeRepositories) Connections() persistence.ConnectionRepository { return r.c }
func (r fakeRepositories) Credentials() persistence.CredentialRepository { return r.k }

type transportFunc func(*http.Request) (*http.Response, error)

func (f transportFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCloudExchangesRefreshIsolationAndRevocation(t *testing.T) {
	i := testIssuer(t)
	for _, provider := range []asset.Provider{asset.ProviderAWS, asset.ProviderAliCloud, asset.ProviderGCP, asset.ProviderAzure} {
		t.Run(string(provider), func(t *testing.T) {
			now := time.Now().UTC()
			i.now = func() time.Time { return now }
			values := map[asset.Provider]map[string]string{
				asset.ProviderAWS:      {"role_arn": "arn:aws:iam::123456789012:role/read", "write_role_arn": "arn:aws:iam::123456789012:role/write"},
				asset.ProviderAliCloud: {"role_arn": "acs:ram::123456789012:role/read", "write_role_arn": "acs:ram::123456789012:role/write", "oidc_provider_arn": "acs:ram::123456789012:oidc-provider/steward"},
				asset.ProviderGCP:      {"project_id": "test-project", "workload_provider": "projects/123/locations/global/workloadIdentityPools/steward/providers/test", "service_account_email": "read@test-project.iam.gserviceaccount.com", "write_service_account_email": "write@test-project.iam.gserviceaccount.com"},
				asset.ProviderAzure:    {"subscription_id": "00000000-0000-0000-0000-000000000001", "tenant_id": "00000000-0000-0000-0000-000000000002", "client_id": "00000000-0000-0000-0000-000000000003", "write_client_id": "00000000-0000-0000-0000-000000000004"},
			}[provider]
			r := fakeRepositories{c: &fakeConnections{value: asset.CloudConnection{ID: "con_test", Provider: provider, Status: asset.ConnectionActive}}, k: &fakeCredentials{value: asset.ConnectionCredential{ConnectionID: "con_test", Provider: provider, Type: asset.CredentialOIDC, Ciphertext: "sealed-configuration"}}}
			b := NewBroker(i, r)
			count := 0
			phase := "read"
			b.http.Transport = transportFunc(func(req *http.Request) (*http.Response, error) {
				count++
				if req.URL.RawQuery != "" {
					t.Fatal("assertion leaked into query string")
				}
				body, _ := io.ReadAll(req.Body)
				response := `{"access_token":"cloud-token","token_type":"Bearer","expires_in":3600}`
				if req.URL.Host == "iamcredentials.googleapis.com" {
					if req.Header.Get("Authorization") != "Bearer cloud-token" || !strings.Contains(req.URL.Path, "/"+phase+"@") {
						t.Fatal("incorrect impersonation identity")
					}
					response = fmt.Sprintf(`{"accessToken":"impersonated-token","expireTime":%q}`, now.Add(time.Hour).Format(time.RFC3339))
				} else {
					if err := req.ParseForm(); err != nil {
						t.Fatal(err)
					} // Body was consumed: restore for form parsing.
					req.Body = io.NopCloser(strings.NewReader(string(body)))
					req.Form = nil
					req.PostForm = nil
					if err := req.ParseForm(); err != nil {
						t.Fatal(err)
					}
					assertion := req.Form.Get("WebIdentityToken")
					if assertion == "" {
						assertion = req.Form.Get("OIDCToken")
					}
					if assertion == "" {
						assertion = req.Form.Get("subject_token")
					}
					if assertion == "" {
						assertion = req.Form.Get("client_assertion")
					}
					c := claims(t, i, assertion)
					if c["sub"] != i.Subject("con_test", phase) || c["aud"] != Audience(provider, values) || c["steward_provider"] != string(provider) {
						t.Fatalf("identity escaped scope: %#v", c)
					}
					switch provider {
					case asset.ProviderAWS:
						if req.URL.Host != "sts.us-east-1.amazonaws.com" || !strings.HasSuffix(req.Form.Get("RoleArn"), "/"+phase) {
							t.Fatal("wrong AWS target")
						}
						response = fmt.Sprintf(`<AssumeRoleWithWebIdentityResponse><AssumeRoleWithWebIdentityResult><Credentials><AccessKeyId>id</AccessKeyId><SecretAccessKey>secret</SecretAccessKey><SessionToken>session</SessionToken><Expiration>%s</Expiration></Credentials></AssumeRoleWithWebIdentityResult></AssumeRoleWithWebIdentityResponse>`, now.Add(time.Hour).Format(time.RFC3339))
					case asset.ProviderAliCloud:
						if req.URL.Host != "sts.aliyuncs.com" || req.Form.Get("Action") != "AssumeRoleWithOIDC" || !strings.HasSuffix(req.Form.Get("RoleArn"), "/"+phase) {
							t.Fatal("wrong Alibaba target")
						}
						response = fmt.Sprintf(`{"Credentials":{"AccessKeyId":"id","AccessKey`+`Secret":"secret","SecurityToken":"session","Expiration":%q}}`, now.Add(time.Hour).Format(time.RFC3339))
					case asset.ProviderGCP:
						if req.URL.Host != "sts.googleapis.com" || req.Form.Get("audience") != "//iam.googleapis.com/"+values["workload_provider"] {
							t.Fatal("wrong GCP audience")
						}
					case asset.ProviderAzure:
						client := values["client_id"]
						if phase == "write" {
							client = values["write_client_id"]
						}
						if req.URL.Host != "login.microsoftonline.com" || req.Form.Get("client_id") != client {
							t.Fatal("wrong Azure identity")
						}
					}
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
			})
			c := contracts.Credential{Type: asset.CredentialOIDC, Values: values}
			read, err := b.Bind(context.Background(), r.k.value, c)
			if err != nil {
				t.Fatal(err)
			}
			scope := ""
			if provider == asset.ProviderAzure {
				scope = "https://management.azure.com/.default"
			}
			var callers sync.WaitGroup
			for range 8 {
				callers.Go(func() {
					if _, err := read.Dynamic.Resolve(context.Background(), scope); err != nil {
						t.Error(err)
					}
				})
			}
			callers.Wait()
			first := count
			want := 1
			if provider == asset.ProviderGCP {
				want = 2
			}
			if first != want {
				t.Fatalf("concurrent requests exchanged %d times, want %d", first, want)
			}
			again, _ := b.Bind(context.Background(), r.k.value, c)
			if _, err = again.Dynamic.Resolve(context.Background(), scope); err != nil || count != first {
				t.Fatalf("cache failed: %v", err)
			}
			now = now.Add(time.Hour)
			if _, err = read.Dynamic.Resolve(context.Background(), scope); err != nil || count != 2*first {
				t.Fatalf("refresh failed: %v", err)
			}
			phase = "write"
			write, err := b.Bind(requestmeta.WithWorkload(context.Background(), "write", "job_test"), r.k.value, c)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = write.Dynamic.Resolve(context.Background(), scope); err != nil || count != 3*first {
				t.Fatalf("phase leaked credentials: %v", err)
			}
			if read.Dynamic.Key == write.Dynamic.Key {
				t.Fatal("read/write cache keys collide")
			}
			r.c.value.Status = asset.ConnectionInvalid
			if _, err = write.Dynamic.Resolve(context.Background(), scope); err == nil {
				t.Fatal("revoked connection reused cache")
			}
			r.c.value.Status = asset.ConnectionActive
			r.k.value.Ciphertext = "replaced"
			if _, err = write.Dynamic.Resolve(context.Background(), scope); err == nil {
				t.Fatal("replaced credential reused cache")
			}
		})
	}
}

func TestConfigAndExchangeBoundaries(t *testing.T) {
	i := testIssuer(t)
	b := NewBroker(i, nil)
	for _, v := range []map[string]string{
		{"role_arn": "arn:aws:iam::123456789012:role/read", "issuer": "https://evil.example"},
		{"role_arn": "arn:aws:iam::123456789012:role/read", "write_role_arn": "arn:aws:iam::999999999999:role/write"},
		{"role_arn": "https://evil.example"},
	} {
		if ValidateConfig(asset.ProviderAWS, contracts.Credential{Type: asset.CredentialOIDC, Values: v}) == nil {
			t.Fatal("accepted unsafe config")
		}
	}
	b.http.Transport = transportFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 403, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("secret-assertion")), Request: r}, nil
	})
	_, err := b.form(context.Background(), "https://sts.amazonaws.com", nil)
	if err == nil || strings.Contains(err.Error(), "secret-assertion") {
		t.Fatal("identity response was not sanitized")
	}
	if _, err = b.exchange(context.Background(), asset.ProviderAzure, map[string]string{}, "secret-assertion", "https://evil.example"); err == nil {
		t.Fatal("accepted arbitrary audience endpoint")
	}
}
