// Package workloadidentity implements server-issued OIDC workload identities.
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
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"strings"
	"time"
)

const TokenLifetime = 5 * time.Minute

var identifier = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}$`)

type Config struct{ IssuerURL, WorkspaceID, SigningKeyFile string }
type Issuer struct {
	URL, WorkspaceID string
	key              *rsa.PrivateKey
	kid              string
	keys             []map[string]string
	path             string
	now              func() time.Time
}

// Load requires operator-managed key material. A PEM bundle contains one
// signing key and may retain previous public keys during key rotation.
func Load(c Config) (*Issuer, error) {
	if c == (Config{}) {
		return nil, nil
	}
	u, err := url.Parse(c.IssuerURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || strings.ContainsAny(c.IssuerURL, "?#\r\n") || strings.HasSuffix(c.IssuerURL, "/") || (u.Path != "" && path.Clean(u.Path) != u.Path) || !identifier.MatchString(c.WorkspaceID) {
		return nil, fmt.Errorf("OIDC requires a stable HTTPS issuer URL without a trailing slash and a workspace ID")
	}
	info, err := os.Stat(c.SigningKeyFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, fmt.Errorf("OIDC signing key must be a private regular file (chmod 600)")
	}
	data, err := os.ReadFile(c.SigningKeyFile)
	if err != nil {
		return nil, fmt.Errorf("cannot read OIDC signing key")
	}
	i := &Issuer{URL: c.IssuerURL, WorkspaceID: c.WorkspaceID, path: u.Path, now: time.Now}
	seen := map[string]bool{}
	for len(strings.TrimSpace(string(data))) > 0 {
		block, rest := pem.Decode(data)
		if block == nil {
			return nil, fmt.Errorf("invalid OIDC PEM bundle")
		}
		data = rest
		var public *rsa.PublicKey
		switch block.Type {
		case "PRIVATE KEY", "RSA PRIVATE KEY":
			if i.key != nil {
				return nil, fmt.Errorf("OIDC PEM bundle must contain exactly one signing key")
			}
			var parsed any
			if block.Type == "PRIVATE KEY" {
				parsed, err = x509.ParsePKCS8PrivateKey(block.Bytes)
			} else {
				parsed, err = x509.ParsePKCS1PrivateKey(block.Bytes)
			}
			key, ok := parsed.(*rsa.PrivateKey)
			if err != nil || !ok || key.N.BitLen() < 2048 || key.Validate() != nil {
				return nil, fmt.Errorf("OIDC requires a valid RSA signing key of at least 2048 bits")
			}
			i.key, public = key, &key.PublicKey
			i.kid = keyID(public)
		case "PUBLIC KEY":
			parsed, e := x509.ParsePKIXPublicKey(block.Bytes)
			var ok bool
			public, ok = parsed.(*rsa.PublicKey)
			if e != nil || !ok || public.N.BitLen() < 2048 {
				return nil, fmt.Errorf("invalid OIDC verification key")
			}
		default:
			return nil, fmt.Errorf("unsupported OIDC PEM block")
		}
		kid := keyID(public)
		if !seen[kid] {
			i.keys = append(i.keys, map[string]string{"kty": "RSA", "use": "sig", "alg": "RS256", "kid": kid, "n": encode(public.N.Bytes()), "e": encode(big.NewInt(int64(public.E)).Bytes())})
			seen[kid] = true
		}
	}
	if i.key == nil || len(i.keys) > 10 {
		return nil, fmt.Errorf("OIDC requires one signing key and at most ten verification keys")
	}
	return i, nil
}

func keyID(key *rsa.PublicKey) string {
	sum := sha256.Sum256(x509.MarshalPKCS1PublicKey(key))
	return encode(sum[:])
}
func encode(b []byte) string { return base64.RawURLEncoding.EncodeToString(b) }

func (i *Issuer) Subject(connectionID, phase string) string {
	return "workspace:" + i.WorkspaceID + ":connection:" + connectionID + ":run_phase:" + phase
}

func (i *Issuer) sign(ctx context.Context, connectionID, provider, audience, phase, runID string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if !identifier.MatchString(connectionID) || (phase != "read" && phase != "write") || audience == "" {
		return "", fmt.Errorf("invalid workload identity")
	}
	now := i.now()
	jti := make([]byte, 16)
	if _, err := rand.Read(jti); err != nil {
		return "", err
	}
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": i.kid})
	claims, _ := json.Marshal(map[string]any{"iss": i.URL, "sub": i.Subject(connectionID, phase), "aud": audience, "iat": now.Unix(), "nbf": now.Add(-5 * time.Second).Unix(), "exp": now.Add(TokenLifetime).Unix(), "jti": encode(jti), "steward_workspace_id": i.WorkspaceID, "steward_connection_id": connectionID, "steward_provider": provider, "steward_run_phase": phase, "steward_run_id": runID})
	unsigned := encode(header) + "." + encode(claims)
	hash := sha256.Sum256([]byte(unsigned))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, hash[:])
	if err != nil {
		return "", fmt.Errorf("cannot sign workload identity")
	}
	return unsigned + "." + encode(sig), nil
}

func (i *Issuer) Handles(path string) bool {
	return path == i.path+"/.well-known/openid-configuration" || path == i.path+"/.well-known/jwks"
}

func (i *Issuer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !i.Handles(r.URL.Path) {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		w.Header().Set("Allow", "GET, HEAD")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "public, max-age=60")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if r.Method == http.MethodHead {
		return
	}
	if strings.HasSuffix(r.URL.Path, "/jwks") {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": i.keys})
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"issuer": i.URL, "jwks_uri": i.URL + "/.well-known/jwks", "id_token_signing_alg_values_supported": []string{"RS256"}, "response_types_supported": []string{"id_token"}, "subject_types_supported": []string{"public"}, "claims_supported": []string{"iss", "sub", "aud", "iat", "nbf", "exp", "jti"}})
}
