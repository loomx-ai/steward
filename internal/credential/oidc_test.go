package credential_test

import (
	"context"
	"encoding/json"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/credential"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/workloadidentity"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOIDCVaultPersistsOnlyConfigurationAndBindsOnRead(t *testing.T) {
	repos, err := sqlite.Open(filepath.Join(t.TempDir(), "oidc.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault(testMasterKey, repos.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	c := contracts.Credential{Type: asset.CredentialOIDC, Values: map[string]string{"role_arn": "arn:aws:iam::123456789012:role/steward"}}
	if _, err = vault.Seal("con_test", asset.ProviderAWS, c, time.Now()); err == nil {
		t.Fatal("OIDC saved on unconfigured server")
	}
	vault.WorkloadIdentity = workloadidentity.NewBroker(&workloadidentity.Issuer{URL: "https://issuer.example", WorkspaceID: "one"}, repos)
	record, err := vault.Seal("con_test", asset.ProviderAWS, c, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err = repos.Credentials().PutCredential(context.Background(), record); err != nil {
		t.Fatal(err)
	}
	resolved, err := vault.Resolve(context.Background(), "con_test")
	if err != nil || resolved.Dynamic == nil {
		t.Fatal("missing runtime binding", err)
	}
	data, err := json.Marshal(resolved)
	if err != nil || strings.Contains(string(data), "Dynamic") || strings.Contains(string(data), "Resolve") {
		t.Fatal("runtime credential serialized", err)
	}
	trust, err := vault.OIDCTrust(context.Background(), "con_test")
	if err != nil || trust.ReadSubject != "workspace:one:connection:con_test:run_phase:read" {
		t.Fatalf("wrong trust: %+v %v", trust, err)
	}
	c.Values["subject_token"] = "client-supplied-token"
	if _, err = vault.Seal("con_test", asset.ProviderAWS, c, time.Now()); err == nil {
		t.Fatal("accepted client-supplied assertion")
	}
}
