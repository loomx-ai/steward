package credential_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/credential"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type changingCredentialSource struct {
	resolve func()
}

type afterCASCredentialRepository struct {
	persistence.CredentialRepository
	after func()
}

func (r *afterCASCredentialRepository) PutCredentialIfConnectionUnchanged(
	ctx context.Context,
	replacement asset.ConnectionCredential,
	expectedConnectionUpdatedAt time.Time,
	expected asset.ConnectionCredential,
) error {
	if err := r.CredentialRepository.PutCredentialIfConnectionUnchanged(
		ctx,
		replacement,
		expectedConnectionUpdatedAt,
		expected,
	); err != nil {
		return err
	}
	r.after()
	return nil
}

func (s changingCredentialSource) Resolve(context.Context, asset.ConnectionID) (contracts.Credential, error) {
	s.resolve()
	return contracts.Credential{
		Type: asset.CredentialAliCloudAccessKey,
		Values: map[string]string{
			"access_key_id": "replacement", "access_key_secret": "replacement-secret",
		},
	}, nil
}

func TestValidatedSourceProjectsConnectionSiteWithoutPersistingItInCredentialValues(t *testing.T) {
	for _, test := range []struct {
		name     string
		provider asset.Provider
		site     asset.ConnectionSite
		wantSite asset.ConnectionSite
	}{
		{name: "alicloud cn", provider: asset.ProviderAliCloud, site: asset.ConnectionSiteCN, wantSite: asset.ConnectionSiteCN},
		{name: "alicloud intl", provider: asset.ProviderAliCloud, site: asset.ConnectionSiteINTL, wantSite: asset.ConnectionSiteINTL},
		{name: "aws", provider: asset.ProviderAWS},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "credentials.db"), filepath.Join("..", "..", "migrations"))
			if err != nil {
				t.Fatal(err)
			}
			now := time.Date(2026, 7, 27, 11, 0, 0, 0, time.UTC)
			connection := asset.CloudConnection{
				ID: asset.ConnectionID("connection-" + test.name), Name: test.name, Provider: test.provider, Site: test.site,
				Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
			}
			if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
				t.Fatal(err)
			}
			vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := vault.Seal(connection.ID, connection.Provider, contracts.Credential{
				Type: asset.CredentialAliCloudAccessKey,
				Values: map[string]string{
					"access_key_id": "id", "access_key_secret": "secret",
				},
			}, now)
			if err != nil {
				t.Fatal(err)
			}
			if err := repositories.Credentials().PutCredential(ctx, sealed); err != nil {
				t.Fatal(err)
			}
			source, err := credential.NewValidatedSource(repositories.Connections(), repositories.Credentials(), vault)
			if err != nil {
				t.Fatal(err)
			}

			resolved, err := source.Resolve(ctx, connection.ID)
			if err != nil {
				t.Fatal(err)
			}
			if resolved.Site != test.wantSite {
				t.Fatalf("resolved.Site = %q, want %q", resolved.Site, test.wantSite)
			}
			if _, exists := resolved.Values["site"]; exists {
				t.Fatalf("credential values persisted site: %#v", resolved.Values)
			}
		})
	}
}

func TestValidatedSourceSnapshotsAndAtomicallyUpdatesCredential(t *testing.T) {
	setup := func(t *testing.T) (
		context.Context,
		asset.CloudConnection,
		persistence.Repositories,
		*credential.Vault,
		*credential.ValidatedSource,
	) {
		t.Helper()
		ctx := context.Background()
		repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "credentials.db"), filepath.Join("..", "..", "migrations"))
		if err != nil {
			t.Fatal(err)
		}
		now := time.Date(2026, 7, 27, 15, 0, 0, 0, time.UTC)
		connection := asset.CloudConnection{
			ID: "connection-snapshot", Name: "snapshot", Provider: asset.ProviderAliCloud, Site: asset.ConnectionSiteINTL,
			Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
		}
		if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
			t.Fatal(err)
		}
		vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
		if err != nil {
			t.Fatal(err)
		}
		sealed, err := vault.Seal(connection.ID, connection.Provider, contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "original", "access_key_secret": "original-secret",
			},
		}, now)
		if err != nil {
			t.Fatal(err)
		}
		if err := repositories.Credentials().PutCredential(ctx, sealed); err != nil {
			t.Fatal(err)
		}
		source, err := credential.NewValidatedSource(repositories.Connections(), repositories.Credentials(), vault)
		if err != nil {
			t.Fatal(err)
		}
		return ctx, connection, repositories, vault, source
	}

	t.Run("snapshot and successful update", func(t *testing.T) {
		ctx, connection, _, vault, source := setup(t)
		expected, err := source.Resolve(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		if expected.ConnectionID != connection.ID || expected.Site != connection.Site || expected.Version == "" {
			t.Fatalf("credential snapshot = %#v", expected)
		}
		updated, err := source.CompareAndSwap(ctx, expected, contracts.Credential{
			Type: expected.Type,
			Values: map[string]string{
				"access_key_id": "refreshed", "access_key_secret": "refreshed-secret",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if updated.ConnectionID != connection.ID || updated.Site != connection.Site || updated.Version == "" || updated.Version == expected.Version {
			t.Fatalf("updated snapshot = %#v", updated)
		}
		resolved, err := vault.Resolve(ctx, connection.ID)
		if err != nil || resolved.Values["access_key_id"] != "refreshed" {
			t.Fatalf("resolved credential = %#v, err = %v", resolved, err)
		}
	})

	t.Run("user replacement wins stale update", func(t *testing.T) {
		ctx, connection, repositories, vault, source := setup(t)
		expected, err := source.Resolve(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		current, err := repositories.Credentials().GetCredential(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		userReplacement, err := vault.Seal(connection.ID, connection.Provider, contracts.Credential{
			Type: expected.Type,
			Values: map[string]string{
				"access_key_id": "user", "access_key_secret": "user-secret",
			},
		}, current.UpdatedAt.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		userReplacement.CreatedAt = current.CreatedAt
		if err := repositories.Credentials().PutCredential(ctx, userReplacement); err != nil {
			t.Fatal(err)
		}

		if _, err := source.CompareAndSwap(ctx, expected, contracts.Credential{
			Type: expected.Type,
			Values: map[string]string{
				"access_key_id": "stale", "access_key_secret": "stale-secret",
			},
		}); !errors.Is(err, persistence.ErrConflict) {
			t.Fatalf("CompareAndSwap() error = %v, want conflict", err)
		}
		resolved, err := vault.Resolve(ctx, connection.ID)
		if err != nil || resolved.Values["access_key_id"] != "user" {
			t.Fatalf("resolved credential after conflict = %#v, err = %v", resolved, err)
		}
	})

	t.Run("connection change rejects update", func(t *testing.T) {
		ctx, connection, repositories, _, source := setup(t)
		expected, err := source.Resolve(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		connection.Status = asset.ConnectionUnverified
		connection.UpdatedAt = connection.UpdatedAt.Add(time.Minute)
		if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
			t.Fatal(err)
		}
		if _, err := source.CompareAndSwap(ctx, expected, contracts.Credential{
			Type: expected.Type,
			Values: map[string]string{
				"access_key_id": "stale", "access_key_secret": "stale-secret",
			},
		}); !errors.Is(err, persistence.ErrConflict) {
			t.Fatalf("CompareAndSwap() error = %v, want conflict", err)
		}
	})

	t.Run("successful update returns its own linearized snapshot", func(t *testing.T) {
		ctx, connection, repositories, vault, source := setup(t)
		expected, err := source.Resolve(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		current, err := repositories.Credentials().GetCredential(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		concurrent, err := vault.Seal(connection.ID, connection.Provider, contracts.Credential{
			Type: expected.Type,
			Values: map[string]string{
				"access_key_id": "concurrent", "access_key_secret": "concurrent-secret",
			},
		}, current.UpdatedAt.Add(time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		concurrent.CreatedAt = current.CreatedAt
		interleaved := &afterCASCredentialRepository{
			CredentialRepository: repositories.Credentials(),
			after: func() {
				if err := repositories.Credentials().PutCredential(ctx, concurrent); err != nil {
					t.Fatal(err)
				}
			},
		}
		linearSource, err := credential.NewValidatedSource(repositories.Connections(), interleaved, vault)
		if err != nil {
			t.Fatal(err)
		}

		updated, err := linearSource.CompareAndSwap(ctx, expected, contracts.Credential{
			Type: expected.Type,
			Values: map[string]string{
				"access_key_id": "refreshed", "access_key_secret": "refreshed-secret",
			},
		})
		if err != nil {
			t.Fatal(err)
		}
		if updated.Values["access_key_id"] != "refreshed" ||
			updated.ConnectionID != connection.ID ||
			updated.Site != connection.Site ||
			updated.Version == expected.Version {
			t.Fatalf("linearized update snapshot = %#v", updated)
		}
		actual, err := vault.Resolve(ctx, connection.ID)
		if err != nil || actual.Values["access_key_id"] != "concurrent" {
			t.Fatalf("concurrent winner = %#v, err = %v", actual, err)
		}
	})
}

func TestValidatedSourceRejectsUpdatesWhenCredentialSourceIsReadOnly(t *testing.T) {
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "credentials.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	source, err := credential.NewValidatedSource(
		repositories.Connections(),
		repositories.Credentials(),
		changingCredentialSource{resolve: func() {}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := source.CompareAndSwap(context.Background(), contracts.Credential{}, contracts.Credential{}); !errors.Is(err, credential.ErrReadOnly) {
		t.Fatalf("CompareAndSwap() error = %v, want ErrReadOnly", err)
	}
}

func TestValidatedSourceRejectsCredentialResolvedAcrossStatusChange(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "credentials.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-active", Name: "active", Provider: asset.ProviderAliCloud,
		Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	sealed := asset.ConnectionCredential{
		ConnectionID: connection.ID, Provider: connection.Provider, Type: asset.CredentialAliCloudAccessKey,
		EnvelopeVersion: 1, Nonce: "nonce", Ciphertext: "ciphertext", CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Credentials().PutCredential(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	source, err := credential.NewValidatedSource(
		repositories.Connections(),
		repositories.Credentials(),
		changingCredentialSource{resolve: func() {
			connection.Status = asset.ConnectionUnverified
			connection.UpdatedAt = now.Add(time.Minute)
			if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
				t.Fatal(err)
			}
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := source.Resolve(ctx, connection.ID); !errors.Is(err, asset.ErrConnectionNotValidated) {
		t.Fatalf("Resolve() error = %v, want ErrConnectionNotValidated", err)
	}
}

func TestValidatedSourceAllowsUnrelatedConnectionVersionChange(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "credentials.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 12, 30, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-active", Name: "active", Provider: asset.ProviderAliCloud,
		Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	sealed := asset.ConnectionCredential{
		ConnectionID: connection.ID, Provider: connection.Provider, Type: asset.CredentialAliCloudAccessKey,
		EnvelopeVersion: 1, Nonce: "nonce", Ciphertext: "ciphertext", CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Credentials().PutCredential(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	source, err := credential.NewValidatedSource(
		repositories.Connections(),
		repositories.Credentials(),
		changingCredentialSource{resolve: func() {
			connection.UpdatedAt = now.Add(time.Minute)
			if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
				t.Fatal(err)
			}
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := source.Resolve(ctx, connection.ID); err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
}

func TestValidatedSourceRejectsCredentialSnapshotChangeWhileConnectionRemainsActive(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "credentials.db"), filepath.Join("..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 13, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-active", Name: "active", Provider: asset.ProviderAliCloud,
		Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	sealed := asset.ConnectionCredential{
		ConnectionID: connection.ID, Provider: connection.Provider, Type: asset.CredentialAliCloudAccessKey,
		EnvelopeVersion: 1, Nonce: "nonce", Ciphertext: "ciphertext", CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Credentials().PutCredential(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	source, err := credential.NewValidatedSource(
		repositories.Connections(),
		repositories.Credentials(),
		changingCredentialSource{resolve: func() {
			sealed.Nonce = "replacement-nonce"
			sealed.Ciphertext = "replacement-ciphertext"
			sealed.UpdatedAt = now.Add(time.Minute)
			if err := repositories.Credentials().PutCredential(ctx, sealed); err != nil {
				t.Fatal(err)
			}
		}},
	)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := source.Resolve(ctx, connection.ID); !errors.Is(err, asset.ErrConnectionNotValidated) {
		t.Fatalf("Resolve() error = %v, want ErrConnectionNotValidated", err)
	}
}
