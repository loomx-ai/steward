package credential_test

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/workspace"
	"github.com/loomx-ai/steward/internal/credential"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const testMasterKey = "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="

func TestVaultEncryptsCredentialsAtRestAndBindsThemToConnection(t *testing.T) {
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "vault.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault(testMasterKey, repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	sealed, err := vault.Seal(context.Background(), "connection-a", asset.ProviderAliCloud, contracts.Credential{
		Type:   asset.CredentialAliCloudAccessKey,
		Values: map[string]string{"access_key_id": "ak-id", "access_key_secret": "super-secret"},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(sealed.Ciphertext, "ak-id") || strings.Contains(sealed.Ciphertext, "super-secret") {
		t.Fatalf("sealed credential contains plaintext: %+v", sealed)
	}
	if err := repositories.Credentials().PutCredential(context.Background(), sealed); err != nil {
		t.Fatal(err)
	}
	resolved, err := vault.Resolve(context.Background(), "connection-a")
	if err != nil || resolved.Values["access_key_id"] != "ak-id" || resolved.Values["access_key_secret"] != "super-secret" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}

	sealed.ConnectionID = "connection-b"
	if err := repositories.Credentials().PutCredential(context.Background(), sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Resolve(context.Background(), "connection-b"); !errors.Is(err, credential.ErrUnavailable) {
		t.Fatalf("credential ciphertext was not bound to its connection: %v", err)
	}
}

func TestVaultRejectsInvalidMasterKeysAndExpiredCredentials(t *testing.T) {
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "vault.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"", "not-base64", "YQ=="} {
		if _, err := credential.NewVault(key, repositories.Credentials()); err == nil {
			t.Fatalf("invalid key %q was accepted", key)
		}
	}
	vault, err := credential.NewVault(testMasterKey, repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	expired := time.Now().Add(-time.Minute)
	sealed, err := vault.Seal(context.Background(), "connection-a", asset.ProviderAWS, contracts.Credential{
		Type: asset.CredentialAWSSession, Values: map[string]string{"access_key_id": "id", "secret_access_key": "secret", "session_token": "token"}, ExpiresAt: &expired,
	}, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Credentials().PutCredential(context.Background(), sealed); err != nil {
		t.Fatal(err)
	}
	if _, err := vault.Resolve(context.Background(), "connection-a"); !errors.Is(err, credential.ErrUnavailable) || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("expired credential resolved: %v", err)
	}
}

func TestVaultKeysCredentialsToTheirWorkspace(t *testing.T) {
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "vault.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault(testMasterKey, repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	first := workspace.With(context.Background(), "ws_first")
	second := workspace.With(context.Background(), "ws_second")
	value := contracts.Credential{Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{"access_key_id": "ak", "access_key_secret": "secret"}}
	sealed, err := vault.Seal(first, "connection-a", asset.ProviderAliCloud, value, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if sealed.EnvelopeVersion != credential.WorkspaceEnvelopeVersion {
		t.Fatalf("envelope version = %d", sealed.EnvelopeVersion)
	}
	for _, ctx := range []context.Context{first, second} {
		if err := repositories.Credentials().PutCredential(ctx, sealed); err != nil {
			t.Fatal(err)
		}
	}
	if resolved, err := vault.Resolve(first, "connection-a"); err != nil || resolved.Values["access_key_secret"] != "secret" {
		t.Fatalf("first workspace resolve = %#v, %v", resolved, err)
	}
	if _, err := vault.Resolve(second, "connection-a"); !errors.Is(err, credential.ErrUnavailable) {
		t.Fatalf("a record copied to another workspace opened: %v", err)
	}
	if _, err := vault.Resolve(context.Background(), "connection-a"); err == nil {
		t.Fatal("the default workspace opened another workspace's record")
	}

	secret, err := vault.SealSecret(first, "notification-url:ntf-1", "https://example.com/hook")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := vault.OpenSecret(second, "notification-url:ntf-1", secret); err == nil {
		t.Fatal("another workspace opened the secret")
	}
	vault.RequireWorkspace()
	if _, err := vault.SealSecret(context.Background(), "notification-url:ntf-1", "x"); err == nil {
		t.Fatal("sealed without a workspace")
	}
}
