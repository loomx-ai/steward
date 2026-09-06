package server

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"testing"

	httptransport "github.com/loomx-ai/steward/internal/transport/http"
)

func TestAuthenticationModeDefaultsAndNetworkBoundary(t *testing.T) {
	for _, test := range []struct {
		addr, mode string
		token      bool
		want       string
		valid      bool
	}{
		{"127.0.0.1:8585", "", false, "local", true},
		{"0.0.0.0:8585", "", false, "local", false},
		{"127.0.0.1:8585", "", true, "token", true},
		{"0.0.0.0:8585", "token", true, "token", true},
		{"0.0.0.0:8585", "cloud", true, "cloud", true},
		{"0.0.0.0:8585", "cloud", false, "cloud", false},
		{"127.0.0.1:8585", "invalid", false, "invalid", false},
	} {
		config := Config{Addr: test.addr, AuthMode: test.mode}
		if test.token {
			config.AuthTokens = []httptransport.TokenBinding{{Token: "secret", Principal: httptransport.Principal{Subject: "admin"}}}
		}
		_, mode, err := resolveAuthenticator(config)
		if mode != test.want || (err == nil) != test.valid {
			t.Fatalf("%+v: mode=%s err=%v", test, mode, err)
		}
	}
}

func TestLocalKeyPersistsAndNeverReplacesExistingCredentials(t *testing.T) {
	dir := t.TempDir()
	key, err := localCredentialKey(dir, true)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(decoded) != 32 {
		t.Fatalf("invalid key length: %v", err)
	}
	for _, allowCreate := range []bool{true, false} {
		again, err := localCredentialKey(dir, allowCreate)
		if err != nil || again != key {
			t.Fatalf("key changed: %v", err)
		}
	}
	info, err := os.Stat(filepath.Join(dir, "credential-master-key"))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("key permissions: %v %v", info, err)
	}
	if _, err := localCredentialKey(t.TempDir(), false); err == nil {
		t.Fatal("generated a new key for existing connections")
	}
	if err := os.WriteFile(filepath.Join(dir, "credential-master-key"), []byte("corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := localCredentialKey(dir, true); err == nil {
		t.Fatal("replaced a corrupt key")
	}
}

func TestLocalKeyReusesDevelopmentKey(t *testing.T) {
	dir := t.TempDir()
	want := base64.StdEncoding.EncodeToString(make([]byte, 32))
	if err := os.WriteFile(filepath.Join(dir, "dev-credential-master-key"), []byte(want), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := localCredentialKey(dir, false)
	if err != nil || key != want {
		t.Fatalf("development key not reused: %v", err)
	}
}
