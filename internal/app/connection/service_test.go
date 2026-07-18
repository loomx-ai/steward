package connection

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/credential"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/persistence/sqlite"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type validatorStub struct{}

type validatorSpy struct {
	calls      int
	credential contracts.Credential
	identity   contracts.ConnectionIdentity
	err        error
}

type blockingValidator struct {
	started  chan struct{}
	release  chan struct{}
	identity contracts.ConnectionIdentity
	err      error
}

type refreshingValidator struct {
	updater  contracts.CredentialUpdater
	identity contracts.ConnectionIdentity
	err      error
}

type blockingCredentialRepository struct {
	persistence.CredentialRepository
	started chan struct{}
	release chan struct{}
}

func (r *blockingCredentialRepository) GetCredential(ctx context.Context, id asset.ConnectionID) (asset.ConnectionCredential, error) {
	value, err := r.CredentialRepository.GetCredential(ctx, id)
	close(r.started)
	<-r.release
	return value, err
}

type credentialBlockingRepositories struct {
	persistence.Repositories
	credentials persistence.CredentialRepository
}

func (r *credentialBlockingRepositories) Credentials() persistence.CredentialRepository {
	return r.credentials
}

func (s *validatorSpy) ValidateConnection(_ context.Context, _ asset.Provider, value contracts.Credential) (contracts.ConnectionIdentity, error) {
	s.calls++
	s.credential = value
	return s.identity, s.err
}

func (*validatorSpy) ValidateConnectionSite(provider asset.Provider, site asset.ConnectionSite) error {
	return validateTestConnectionSite(provider, site)
}

func (v *blockingValidator) ValidateConnection(_ context.Context, _ asset.Provider, _ contracts.Credential) (contracts.ConnectionIdentity, error) {
	close(v.started)
	<-v.release
	return v.identity, v.err
}

func (*blockingValidator) ValidateConnectionSite(provider asset.Provider, site asset.ConnectionSite) error {
	return validateTestConnectionSite(provider, site)
}

func (v *refreshingValidator) ValidateConnection(ctx context.Context, _ asset.Provider, expected contracts.Credential) (contracts.ConnectionIdentity, error) {
	updated, err := v.updater.CompareAndSwap(ctx, expected, contracts.Credential{
		Type: expected.Type,
		Values: map[string]string{
			"access_key_id": "refreshed", "access_key_secret": "refreshed-secret",
		},
	})
	if err != nil {
		return contracts.ConnectionIdentity{}, err
	}
	identity := v.identity
	identity.CredentialVersion = updated.Version
	return identity, v.err
}

func (*refreshingValidator) ValidateConnectionSite(provider asset.Provider, site asset.ConnectionSite) error {
	return validateTestConnectionSite(provider, site)
}

type refreshQueueStub struct {
	calls []asset.ConnectionID
	err   error
}

func (q *refreshQueueStub) Enqueue(_ context.Context, connectionID asset.ConnectionID) (execution.Job, error) {
	q.calls = append(q.calls, connectionID)
	if q.err != nil {
		return execution.Job{}, q.err
	}
	return execution.Job{ID: execution.JobID("refresh-" + string(connectionID)), ConnectionID: connectionID, Type: execution.JobRegionRefresh, Status: execution.JobPending}, nil
}

func (validatorStub) ValidateConnection(_ context.Context, provider asset.Provider, value contracts.Credential) (contracts.ConnectionIdentity, error) {
	if provider != asset.ProviderAliCloud || value.Values["access_key_secret"] == "invalid" {
		return contracts.ConnectionIdentity{}, errors.New("credential rejected")
	}
	principal := value.Values["principal"]
	if principal == "" {
		principal = "1234567890123456"
	}
	return contracts.ConnectionIdentity{
		Partition: "public", TenantID: principal, Principal: principal,
		RootScopes: []contracts.RootScopeCandidate{{Kind: asset.ScopeAccount, NativeID: principal, Name: "account " + principal}},
	}, nil
}

func (validatorStub) ValidateConnectionSite(provider asset.Provider, site asset.ConnectionSite) error {
	return validateTestConnectionSite(provider, site)
}

func validateTestConnectionSite(provider asset.Provider, site asset.ConnectionSite) error {
	switch provider {
	case asset.ProviderAliCloud:
		if site == asset.ConnectionSiteCN || site == asset.ConnectionSiteINTL {
			return nil
		}
	case asset.ProviderAWS:
		if site == "" {
			return nil
		}
	}
	return errors.New("site rejected")
}

func TestCreateEnforcesProviderSite(t *testing.T) {
	tests := []struct {
		name     string
		provider asset.Provider
		site     asset.ConnectionSite
		wantErr  bool
	}{
		{"alicloud cn", asset.ProviderAliCloud, asset.ConnectionSiteCN, false},
		{"alicloud intl", asset.ProviderAliCloud, asset.ConnectionSiteINTL, false},
		{"alicloud missing", asset.ProviderAliCloud, "", true},
		{"alicloud unknown", asset.ProviderAliCloud, "moon", true},
		{"aws rejects site", asset.ProviderAWS, asset.ConnectionSiteCN, true},
		{"aws without site", asset.ProviderAWS, "", false},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ctx := context.Background()
			repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"), filepath.Join("..", "..", "..", "migrations"))
			if err != nil {
				t.Fatal(err)
			}
			vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
			if err != nil {
				t.Fatal(err)
			}
			service, err := NewService(repositories, vault, validatorStub{}, &refreshQueueStub{})
			if err != nil {
				t.Fatal(err)
			}

			created, err := service.Create(ctx, CreateRequest{
				Name: "site", Provider: test.provider, Site: test.site, Actor: "admin",
				Credential: contracts.Credential{
					Type: asset.CredentialAliCloudAccessKey,
					Values: map[string]string{
						"access_key_id": "ak-id", "access_key_secret": "super-secret",
					},
				},
			})
			if test.wantErr {
				if !errors.Is(err, ErrInvalidSite) {
					t.Fatalf("Create() error = %v, want ErrInvalidSite", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("Create() error = %v", err)
			}
			if created.Site != test.site {
				t.Fatalf("created.Site = %q, want %q", created.Site, test.site)
			}
			stored, err := repositories.Connections().GetConnection(ctx, created.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stored.Site != test.site {
				t.Fatalf("stored.Site = %q, want %q", stored.Site, test.site)
			}
		})
	}
}

func TestCreateWithoutProviderValidation(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	validator := &validatorSpy{err: errors.New("provider must not be called")}
	refreshQueue := &refreshQueueStub{}
	service, err := NewService(repositories, vault, validator, refreshQueue)
	if err != nil {
		t.Fatal(err)
	}

	created, err := service.Create(ctx, CreateRequest{
		Name: "offline", Provider: asset.ProviderAliCloud, Site: asset.ConnectionSiteCN, Actor: "admin",
		Credential: contracts.Credential{Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{
			"access_key_id": "ak-id", "access_key_secret": "super-secret",
		}},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if validator.calls != 0 {
		t.Fatalf("provider validator calls = %d, want 0", validator.calls)
	}
	if created.Status != asset.ConnectionStatus("unverified") || created.Partition != "" || created.Principal != "" {
		t.Fatalf("created connection = %+v", created.CloudConnection)
	}
	if len(refreshQueue.calls) != 0 || created.RegionRefresh != nil {
		t.Fatalf("region refresh = %#v, calls = %#v", created.RegionRefresh, refreshQueue.calls)
	}
	scopes, err := repositories.Inventory().ListScopesByConnection(ctx, created.ID)
	if err != nil || len(scopes) != 0 {
		t.Fatalf("scopes = %#v, err = %v", scopes, err)
	}
	stored, err := repositories.Credentials().GetCredential(ctx, created.ID)
	if err != nil || strings.Contains(stored.Ciphertext, "super-secret") {
		t.Fatalf("stored credential = %+v, err = %v", stored, err)
	}
}

func TestReplaceCredentialRequiresExplicitValidation(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-active", Name: "production", Provider: asset.ProviderAliCloud,
		Partition: "public", Principal: "acs:ram::1234567890123456:user/alice",
		Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	old, err := vault.Seal(connection.ID, connection.Provider, contracts.Credential{
		Type: asset.CredentialAliCloudAccessKey,
		Values: map[string]string{
			"access_key_id": "old-id", "access_key_secret": "old-secret",
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Credentials().PutCredential(ctx, old); err != nil {
		t.Fatal(err)
	}
	validator := &validatorSpy{err: errors.New("provider must not be called")}
	refreshQueue := &refreshQueueStub{}
	service, err := NewService(repositories, vault, validator, refreshQueue)
	if err != nil {
		t.Fatal(err)
	}
	service.now = func() time.Time { return now.Add(time.Minute) }

	replaced, err := service.ReplaceCredential(ctx, connection.ID, contracts.Credential{
		Type: asset.CredentialAliCloudAccessKey,
		Values: map[string]string{
			"access_key_id": "new-id", "access_key_secret": "new-secret",
		},
	}, "admin")
	if err != nil {
		t.Fatalf("ReplaceCredential() error = %v", err)
	}
	if validator.calls != 0 {
		t.Fatalf("provider validator calls = %d, want 0", validator.calls)
	}
	if replaced.Status != asset.ConnectionStatus("unverified") || replaced.Partition != connection.Partition || replaced.Principal != connection.Principal {
		t.Fatalf("replaced connection = %+v", replaced.CloudConnection)
	}
	if len(refreshQueue.calls) != 0 || replaced.RegionRefresh != nil {
		t.Fatalf("region refresh = %#v, calls = %#v", replaced.RegionRefresh, refreshQueue.calls)
	}
	resolved, err := vault.Resolve(ctx, connection.ID)
	if err != nil || resolved.Values["access_key_id"] != "new-id" {
		t.Fatalf("resolved credential = %#v, err = %v", resolved, err)
	}
}

func TestServiceBlocksDeletionWhileConnectionHasActiveJobs(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	service, err := NewService(repositories, vault, validatorStub{}, &refreshQueueStub{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, CreateRequest{
		Name: "busy", Provider: asset.ProviderAliCloud, Site: asset.ConnectionSiteCN, Actor: "admin",
		Credential: contracts.Credential{Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if err := repositories.Jobs().Enqueue(ctx, execution.Job{
		ID: "job-active", ConnectionID: created.ID, Type: execution.JobGraph, Status: execution.JobPending,
		RunAt: now, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := service.Delete(ctx, created.ID, created.Name, "admin"); !errors.Is(err, ErrConnectionBusy) {
		t.Fatalf("active job did not block connection deletion: %v", err)
	}
	if _, err := service.ReplaceCredential(ctx, created.ID, contracts.Credential{
		Type: asset.CredentialAliCloudAccessKey,
		Values: map[string]string{
			"access_key_id": "replacement", "access_key_secret": "replacement-secret",
		},
	}, "admin"); !errors.Is(err, ErrConnectionBusy) {
		t.Fatalf("active job did not block credential replacement: %v", err)
	}
}

func TestServiceManagesOneEncryptedCredentialPerConnection(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	refreshQueue := &refreshQueueStub{}
	service, err := NewService(repositories, vault, validatorStub{}, refreshQueue)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 14, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	created, err := service.Create(ctx, CreateRequest{
		Name: "production", Provider: asset.ProviderAliCloud, Site: asset.ConnectionSiteCN, Actor: "admin",
		Credential: contracts.Credential{Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{
			"access_key_id": "ak-id", "access_key_secret": "super-secret",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if created.Name != "production" || created.Status != asset.ConnectionUnverified || created.Principal != "" || created.Credential.Type != asset.CredentialAliCloudAccessKey {
		t.Fatalf("created=%+v", created)
	}
	if created.RegionRefresh != nil || len(refreshQueue.calls) != 0 {
		t.Fatalf("created refresh = %#v, calls = %#v", created.RegionRefresh, refreshQueue.calls)
	}
	stored, err := repositories.Credentials().GetCredential(ctx, created.ID)
	if err != nil || strings.Contains(stored.Ciphertext, "super-secret") {
		t.Fatalf("stored=%+v err=%v", stored, err)
	}
	scopes, err := repositories.Inventory().ListScopesByConnection(ctx, created.ID)
	if err != nil || len(scopes) != 0 {
		t.Fatalf("scopes=%+v err=%v", scopes, err)
	}
	for _, region := range []asset.ConnectionRegion{
		{ID: "region-active", ConnectionID: created.ID, RegionID: "cn-hangzhou", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionActive, CreatedAt: now, UpdatedAt: now},
		{ID: "region-retired", ConnectionID: created.ID, RegionID: "cn-qingdao", Origin: asset.RegionOriginAPI, Lifecycle: asset.RegionRetired, CreatedAt: now, UpdatedAt: now},
		{ID: "region-excluded", ConnectionID: created.ID, RegionID: "cn-beijing", Origin: asset.RegionOriginManual, Lifecycle: asset.RegionExcluded, CreatedAt: now, UpdatedAt: now},
	} {
		if err := repositories.Regions().PutRegion(ctx, region); err != nil {
			t.Fatal(err)
		}
	}
	if err := repositories.Jobs().Enqueue(ctx, execution.Job{ID: "completed-refresh", ConnectionID: created.ID, Type: execution.JobRegionRefresh, Status: execution.JobSucceeded, RunAt: now, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	listed, err := service.List(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(listed.Items) != 1 || listed.Items[0].ActiveRegionCount != 1 || listed.Items[0].RetiredRegionCount != 1 || listed.Items[0].ExcludedRegionCount != 1 || listed.Items[0].LastRegionRefreshStatus != execution.JobSucceeded || listed.Items[0].LastRegionRefreshAt == nil {
		t.Fatalf("connection list summary = %#v, err = %v", listed, err)
	}

	if _, err := service.Rename(ctx, created.ID, "renamed", "admin"); err != nil {
		t.Fatal(err)
	}
	replaced, err := service.ReplaceCredential(ctx, created.ID, contracts.Credential{Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{
		"access_key_id": "rotated", "access_key_secret": "new-secret",
	}}, "admin")
	if err != nil || replaced.Status != asset.ConnectionUnverified {
		t.Fatalf("replacement = %#v, %v", replaced, err)
	}
	if len(refreshQueue.calls) != 0 {
		t.Fatalf("credential replacement refresh calls = %#v", refreshQueue.calls)
	}
	replaced, err = service.ReplaceCredential(ctx, created.ID, contracts.Credential{Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{
		"access_key_id": "rotated-again", "access_key_secret": "latest-secret",
	}}, "admin")
	if err != nil || replaced.RegionRefresh != nil {
		t.Fatalf("replacement = %#v, %v", replaced, err)
	}
	resolved, err := vault.Resolve(ctx, created.ID)
	if err != nil || resolved.Values["access_key_id"] != "rotated-again" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}

	if err := service.Delete(ctx, created.ID, "wrong", "admin"); !errors.Is(err, ErrConfirmationMismatch) {
		t.Fatalf("wrong confirmation was accepted: %v", err)
	}
	if err := service.Delete(ctx, created.ID, "renamed", "admin"); err != nil {
		t.Fatal(err)
	}
	if _, err := repositories.Credentials().GetCredential(ctx, created.ID); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("credential survived deletion: %v", err)
	}
	page, err := service.List(ctx, persistence.ListOptions{Limit: 10})
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("deleted connection remains active: %+v err=%v", page, err)
	}
	deleted, err := repositories.Connections().GetConnection(ctx, created.ID)
	if err != nil || deleted.Status != asset.ConnectionDeleted || deleted.DeletedAt == nil {
		t.Fatalf("deleted=%+v err=%v", deleted, err)
	}
	audits, err := repositories.Audits().ListAuditEvents(ctx, persistence.ListOptions{Limit: 20})
	encoded, _ := json.Marshal(audits.Items)
	if err != nil || len(audits.Items) != 5 || strings.Contains(string(encoded), "super-secret") || strings.Contains(string(encoded), "new-secret") || strings.Contains(string(encoded), "latest-secret") {
		t.Fatalf("audits=%s err=%v", encoded, err)
	}
}

func TestCreateDoesNotEnqueueRegionRefresh(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	queue := &refreshQueueStub{err: errors.New("queue unavailable")}
	service, err := NewService(repositories, vault, validatorStub{}, queue)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, CreateRequest{
		Name: "saved", Provider: asset.ProviderAliCloud, Site: asset.ConnectionSiteCN, Actor: "admin",
		Credential: contracts.Credential{Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{"access_key_id": "id", "access_key_secret": "secret"}},
	})
	if err != nil || created.Status != asset.ConnectionUnverified || created.RegionRefresh != nil || len(queue.calls) != 0 {
		t.Fatalf("Create() = %#v, %v", created, err)
	}
	if _, err := repositories.Connections().GetConnection(ctx, created.ID); err != nil {
		t.Fatalf("saved connection missing: %v", err)
	}
	if _, err := repositories.Credentials().GetCredential(ctx, created.ID); err != nil {
		t.Fatalf("saved credential missing: %v", err)
	}
}

func TestExplicitValidationActivatesConnectionAndReusesRootScope(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	validator := &validatorSpy{identity: contracts.ConnectionIdentity{
		Partition: "public", TenantID: "1234567890123456",
		Principal: "acs:ram::1234567890123456:user/alice",
		RootScopes: []contracts.RootScopeCandidate{{
			Kind: asset.ScopeAccount, NativeID: "1234567890123456", Name: "production account",
		}},
	}}
	queue := &refreshQueueStub{}
	service, err := NewService(repositories, vault, validator, queue)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	created, err := service.Create(ctx, CreateRequest{
		Name: "production", Provider: asset.ProviderAliCloud, Site: asset.ConnectionSiteCN, Actor: "admin",
		Credential: contracts.Credential{Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{
			"access_key_id": "ak-id", "access_key_secret": "super-secret",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	validated, err := service.Validate(ctx, created.ID, "admin")
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if validated.Status != asset.ConnectionActive || validated.Partition != "public" || validated.TenantID != validator.identity.TenantID || validated.Principal != validator.identity.Principal {
		t.Fatalf("validated connection = %+v", validated.CloudConnection)
	}
	if validator.credential.Site != asset.ConnectionSiteCN {
		t.Fatalf("validated credential site = %q, want %q", validator.credential.Site, asset.ConnectionSiteCN)
	}
	if validated.RegionRefresh == nil || validated.RegionRefresh.JobID == "" || len(queue.calls) != 1 || queue.calls[0] != created.ID {
		t.Fatalf("region refresh = %#v, calls = %#v", validated.RegionRefresh, queue.calls)
	}
	scopes, err := repositories.Inventory().ListScopesByConnection(ctx, created.ID)
	if err != nil || len(scopes) != 1 || scopes[0].NativeID != "1234567890123456" {
		t.Fatalf("scopes = %#v, err = %v", scopes, err)
	}

	validatedAgain, err := service.Validate(ctx, created.ID, "admin")
	if err != nil || validatedAgain.Status != asset.ConnectionActive || len(queue.calls) != 2 {
		t.Fatalf("second validation = %#v, err = %v, calls = %#v", validatedAgain, err, queue.calls)
	}
	scopes, err = repositories.Inventory().ListScopesByConnection(ctx, created.ID)
	if err != nil || len(scopes) != 1 {
		t.Fatalf("repeated validation scopes = %#v, err = %v", scopes, err)
	}
	audits, err := repositories.Audits().ListAuditEvents(ctx, persistence.ListOptions{ConnectionID: created.ID, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	validateCount := 0
	for _, audit := range audits.Items {
		if audit.Action == "connection.validate" && audit.Result == "validated" {
			validateCount++
		}
	}
	if validateCount != 2 {
		t.Fatalf("validation audits = %#v", audits.Items)
	}
}

func TestExplicitValidationAcceptsProviderRefreshedCredentialVersion(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	updater, err := credential.NewValidatedSource(repositories.Connections(), repositories.Credentials(), vault)
	if err != nil {
		t.Fatal(err)
	}
	validator := &refreshingValidator{
		updater: updater,
		identity: contracts.ConnectionIdentity{
			Partition: "public", TenantID: "1234567890123456",
			Principal:  "acs:ram::1234567890123456:user/alice",
			RootScopes: []contracts.RootScopeCandidate{{Kind: asset.ScopeAccount, NativeID: "1234567890123456"}},
		},
	}
	service, err := NewService(repositories, vault, validator, &refreshQueueStub{})
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, CreateRequest{
		Name: "oauth", Provider: asset.ProviderAliCloud, Site: asset.ConnectionSiteINTL, Actor: "admin",
		Credential: contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "original", "access_key_secret": "original-secret",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	validated, err := service.Validate(ctx, created.ID, "admin")
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if validated.Status != asset.ConnectionActive || validated.Site != asset.ConnectionSiteINTL {
		t.Fatalf("validated connection = %#v", validated.CloudConnection)
	}
	resolved, err := vault.Resolve(ctx, created.ID)
	if err != nil || resolved.Values["access_key_id"] != "refreshed" {
		t.Fatalf("resolved credential = %#v, err = %v", resolved, err)
	}
}

func TestExplicitValidationPersistsFailureAfterProviderCredentialRefresh(t *testing.T) {
	providerErr := &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category: execution.ErrorInvalidRequest,
		Code:     "InvalidAccessKeyId.NotFound",
		Message:  "The specified access key is not found.",
	}}
	for _, test := range []struct {
		name        string
		established bool
		identity    contracts.ConnectionIdentity
		validation  error
	}{
		{name: "provider error", validation: providerErr},
		{
			name:        "identity mismatch",
			established: true,
			identity: contracts.ConnectionIdentity{
				Partition: "public", TenantID: "2222222222222222", Principal: "other",
				RootScopes: []contracts.RootScopeCandidate{{Kind: asset.ScopeAccount, NativeID: "2222222222222222"}},
			},
			validation: ErrIdentityMismatch,
		},
	} {
		test := test
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"), filepath.Join("..", "..", "..", "migrations"))
			if err != nil {
				t.Fatal(err)
			}
			vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
			if err != nil {
				t.Fatal(err)
			}
			updater, err := credential.NewValidatedSource(repositories.Connections(), repositories.Credentials(), vault)
			if err != nil {
				t.Fatal(err)
			}
			validator := &refreshingValidator{updater: updater, identity: test.identity}
			if test.validation == providerErr {
				validator.err = providerErr
			}
			service, err := NewService(repositories, vault, validator, &refreshQueueStub{})
			if err != nil {
				t.Fatal(err)
			}
			created, err := service.Create(ctx, CreateRequest{
				Name: "oauth", Provider: asset.ProviderAliCloud, Site: asset.ConnectionSiteCN, Actor: "admin",
				Credential: contracts.Credential{
					Type: asset.CredentialAliCloudAccessKey,
					Values: map[string]string{
						"access_key_id": "original", "access_key_secret": "original-secret",
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.established {
				connection, err := repositories.Connections().GetConnection(ctx, created.ID)
				if err != nil {
					t.Fatal(err)
				}
				connection.Partition = "public"
				connection.TenantID = "1111111111111111"
				connection.Principal = "original"
				connection.UpdatedAt = connection.UpdatedAt.Add(time.Second)
				if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
					t.Fatal(err)
				}
			}

			_, err = service.Validate(ctx, created.ID, "admin")
			if !errors.Is(err, test.validation) {
				t.Fatalf("Validate() error = %v, want %v", err, test.validation)
			}
			stored, getErr := repositories.Connections().GetConnection(ctx, created.ID)
			if getErr != nil || stored.Status != asset.ConnectionInvalid {
				t.Fatalf("stored connection = %#v, err = %v", stored, getErr)
			}
			resolved, resolveErr := vault.Resolve(ctx, created.ID)
			if resolveErr != nil || resolved.Values["access_key_id"] != "refreshed" {
				t.Fatalf("resolved credential = %#v, err = %v", resolved, resolveErr)
			}
			audits, auditErr := repositories.Audits().ListAuditEvents(ctx, persistence.ListOptions{ConnectionID: created.ID, Limit: 10})
			failedAudit := false
			for _, audit := range audits.Items {
				failedAudit = failedAudit || (audit.Action == "connection.validate" && audit.Result == "failed")
			}
			if auditErr != nil || !failedAudit {
				t.Fatalf("validation audits = %#v, err = %v", audits.Items, auditErr)
			}
		})
	}
}

func TestExplicitValidationFailurePersistsInvalidStatusAndSafeProviderEvidence(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	providerErr := &contracts.ProviderCallError{Provider: execution.ProviderError{
		Category: execution.ErrorInvalidRequest,
		Code:     "InvalidAccessKeyId.NotFound", Message: "The specified access key is not found.",
		RequestID: "provider-request",
	}}
	validator := &validatorSpy{err: providerErr}
	queue := &refreshQueueStub{}
	service, err := NewService(repositories, vault, validator, queue)
	if err != nil {
		t.Fatal(err)
	}
	created, err := service.Create(ctx, CreateRequest{
		Name: "invalid", Provider: asset.ProviderAliCloud, Site: asset.ConnectionSiteCN, Actor: "admin",
		Credential: contracts.Credential{Type: asset.CredentialAliCloudAccessKey, Values: map[string]string{
			"access_key_id": "ak-id", "access_key_secret": "super-secret",
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Validate(ctx, created.ID, "admin"); !errors.Is(err, providerErr) {
		t.Fatalf("Validate() error = %v", err)
	}
	stored, err := repositories.Connections().GetConnection(ctx, created.ID)
	if err != nil || stored.Status != asset.ConnectionInvalid {
		t.Fatalf("stored connection = %+v, err = %v", stored, err)
	}
	if len(queue.calls) != 0 {
		t.Fatalf("region refresh calls = %#v", queue.calls)
	}
	audits, err := repositories.Audits().ListAuditEvents(ctx, persistence.ListOptions{ConnectionID: created.ID, Limit: 10})
	encoded, _ := json.Marshal(audits.Items)
	if err != nil || !strings.Contains(string(encoded), `"error_code":"InvalidAccessKeyId.NotFound"`) ||
		!strings.Contains(string(encoded), `"provider_request_id":"provider-request"`) ||
		strings.Contains(string(encoded), "super-secret") {
		t.Fatalf("validation audits = %s, err = %v", encoded, err)
	}
}

func TestExplicitValidationRejectsChangedEstablishedIdentity(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-established", Name: "production", Provider: asset.ProviderAliCloud,
		Partition: "public", TenantID: "1234567890123456", Principal: "acs:ram::1234567890123456:user/alice",
		Status: asset.ConnectionUnverified, CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	sealed, err := vault.Seal(connection.ID, connection.Provider, contracts.Credential{
		Type: asset.CredentialAliCloudAccessKey,
		Values: map[string]string{
			"access_key_id": "ak-id", "access_key_secret": "super-secret",
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Credentials().PutCredential(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	validator := &validatorSpy{identity: contracts.ConnectionIdentity{
		Partition: "public", TenantID: "9999999999999999", Principal: "acs:ram::9999999999999999:user/bob",
		RootScopes: []contracts.RootScopeCandidate{{Kind: asset.ScopeAccount, NativeID: "9999999999999999"}},
	}}
	queue := &refreshQueueStub{}
	service, err := NewService(repositories, vault, validator, queue)
	if err != nil {
		t.Fatal(err)
	}

	if _, err := service.Validate(ctx, connection.ID, "admin"); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("Validate() error = %v", err)
	}
	stored, err := repositories.Connections().GetConnection(ctx, connection.ID)
	if err != nil || stored.Status != asset.ConnectionInvalid || stored.Principal != connection.Principal || stored.Partition != connection.Partition {
		t.Fatalf("stored connection = %+v, err = %v", stored, err)
	}
	if len(queue.calls) != 0 {
		t.Fatalf("region refresh calls = %#v", queue.calls)
	}
}

func TestExplicitValidationAllowsPrincipalRotationWithinEstablishedTenant(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	const accountID = "1234567890123456"
	connection := asset.CloudConnection{
		ID: "connection-established", Name: "production", Provider: asset.ProviderAliCloud,
		Partition: "public", Principal: "acs:ram::1234567890123456:user/alice",
		Status: asset.ConnectionUnverified, CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	sealed, err := vault.Seal(connection.ID, connection.Provider, contracts.Credential{
		Type: asset.CredentialAliCloudAccessKey,
		Values: map[string]string{
			"access_key_id": "ak-id", "access_key_secret": "super-secret",
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Credentials().PutCredential(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	if err := repositories.Inventory().PutScope(ctx, asset.Scope{
		ID: "legacy-account-root", ConnectionID: connection.ID,
		Kind: asset.ScopeAccount, NativeID: accountID, Name: accountID,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	validator := &validatorSpy{identity: contracts.ConnectionIdentity{
		Partition: "public", TenantID: accountID, Principal: "acs:ram::1234567890123456:role/deployer",
		RootScopes: []contracts.RootScopeCandidate{{Kind: asset.ScopeAccount, NativeID: accountID}},
	}}
	service, err := NewService(repositories, vault, validator, &refreshQueueStub{})
	if err != nil {
		t.Fatal(err)
	}

	validated, err := service.Validate(ctx, connection.ID, "admin")
	if err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if validated.Status != asset.ConnectionActive || validated.TenantID != accountID || validated.Principal != validator.identity.Principal {
		t.Fatalf("validated connection = %+v", validated.CloudConnection)
	}
}

func TestExplicitValidationDoesNotCommitAfterCredentialOrConnectionChanges(t *testing.T) {
	for _, test := range []struct {
		name          string
		validationErr error
		mutate        func(context.Context, *Service, asset.CloudConnection) error
		wantStatus    asset.ConnectionStatus
		wantDeleted   bool
	}{
		{
			name: "credential replaced after successful provider call",
			mutate: func(ctx context.Context, service *Service, connection asset.CloudConnection) error {
				_, err := service.ReplaceCredential(ctx, connection.ID, contracts.Credential{
					Type: asset.CredentialAliCloudAccessKey,
					Values: map[string]string{
						"access_key_id": "replacement", "access_key_secret": "replacement-secret",
					},
				}, "admin")
				return err
			},
			wantStatus: asset.ConnectionUnverified,
		},
		{
			name:          "credential replaced after failed provider call",
			validationErr: errors.New("old credential rejected"),
			mutate: func(ctx context.Context, service *Service, connection asset.CloudConnection) error {
				_, err := service.ReplaceCredential(ctx, connection.ID, contracts.Credential{
					Type: asset.CredentialAliCloudAccessKey,
					Values: map[string]string{
						"access_key_id": "replacement", "access_key_secret": "replacement-secret",
					},
				}, "admin")
				return err
			},
			wantStatus: asset.ConnectionUnverified,
		},
		{
			name: "connection deleted after successful provider call",
			mutate: func(ctx context.Context, service *Service, connection asset.CloudConnection) error {
				return service.Delete(ctx, connection.ID, connection.Name, "admin")
			},
			wantStatus:  asset.ConnectionDeleted,
			wantDeleted: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"), filepath.Join("..", "..", "..", "migrations"))
			if err != nil {
				t.Fatal(err)
			}
			vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
			if err != nil {
				t.Fatal(err)
			}
			validator := &blockingValidator{
				started: make(chan struct{}), release: make(chan struct{}), err: test.validationErr,
				identity: contracts.ConnectionIdentity{
					Partition: "public", TenantID: "1234567890123456",
					Principal:  "acs:ram::1234567890123456:user/alice",
					RootScopes: []contracts.RootScopeCandidate{{Kind: asset.ScopeAccount, NativeID: "1234567890123456"}},
				},
			}
			queue := &refreshQueueStub{}
			service, err := NewService(repositories, vault, validator, queue)
			if err != nil {
				t.Fatal(err)
			}
			created, err := service.Create(ctx, CreateRequest{
				Name: "production", Provider: asset.ProviderAliCloud, Site: asset.ConnectionSiteCN, Actor: "admin",
				Credential: contracts.Credential{
					Type: asset.CredentialAliCloudAccessKey,
					Values: map[string]string{
						"access_key_id": "old", "access_key_secret": "old-secret",
					},
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				_, validateErr := service.Validate(ctx, created.ID, "admin")
				done <- validateErr
			}()
			<-validator.started
			if err := test.mutate(ctx, service, created.CloudConnection); err != nil {
				t.Fatal(err)
			}
			close(validator.release)
			if err := <-done; !errors.Is(err, ErrValidationStale) {
				t.Fatalf("Validate() error = %v, want ErrValidationStale", err)
			}
			stored, err := repositories.Connections().GetConnection(ctx, created.ID)
			if err != nil || stored.Status != test.wantStatus || (stored.DeletedAt != nil) != test.wantDeleted {
				t.Fatalf("stored connection = %+v, err = %v", stored, err)
			}
			if len(queue.calls) != 0 {
				t.Fatalf("region refresh calls = %#v", queue.calls)
			}
		})
	}
}

func TestCredentialReplacementCannotResurrectDeletedConnection(t *testing.T) {
	ctx := context.Background()
	repositories, err := sqlite.Open(filepath.Join(t.TempDir(), "connections.db"), filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	vault, err := credential.NewVault("AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE=", repositories.Credentials())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	connection := asset.CloudConnection{
		ID: "connection-race", Name: "production", Provider: asset.ProviderAliCloud,
		Partition: "public", TenantID: "1234567890123456", Principal: "cloud-user",
		Status: asset.ConnectionActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := repositories.Connections().PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	sealed, err := vault.Seal(connection.ID, connection.Provider, contracts.Credential{
		Type: asset.CredentialAliCloudAccessKey,
		Values: map[string]string{
			"access_key_id": "old", "access_key_secret": "old-secret",
		},
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := repositories.Credentials().PutCredential(ctx, sealed); err != nil {
		t.Fatal(err)
	}
	blocker := &blockingCredentialRepository{
		CredentialRepository: repositories.Credentials(),
		started:              make(chan struct{}),
		release:              make(chan struct{}),
	}
	replaceService, err := NewService(
		&credentialBlockingRepositories{Repositories: repositories, credentials: blocker},
		vault,
		validatorStub{},
		&refreshQueueStub{},
	)
	if err != nil {
		t.Fatal(err)
	}
	deleteService, err := NewService(repositories, vault, validatorStub{}, &refreshQueueStub{})
	if err != nil {
		t.Fatal(err)
	}
	replaceDone := make(chan error, 1)
	go func() {
		_, replaceErr := replaceService.ReplaceCredential(ctx, connection.ID, contracts.Credential{
			Type: asset.CredentialAliCloudAccessKey,
			Values: map[string]string{
				"access_key_id": "replacement", "access_key_secret": "replacement-secret",
			},
		}, "admin")
		replaceDone <- replaceErr
	}()
	<-blocker.started
	if err := deleteService.Delete(ctx, connection.ID, connection.Name, "admin"); err != nil {
		t.Fatal(err)
	}
	close(blocker.release)
	if err := <-replaceDone; !errors.Is(err, ErrConnectionChanged) {
		t.Fatalf("ReplaceCredential() error = %v, want ErrConnectionChanged", err)
	}
	stored, err := repositories.Connections().GetConnection(ctx, connection.ID)
	if err != nil || stored.Status != asset.ConnectionDeleted || stored.DeletedAt == nil {
		t.Fatalf("stored connection = %+v, err = %v", stored, err)
	}
	if _, err := repositories.Credentials().GetCredential(ctx, connection.ID); !errors.Is(err, persistence.ErrNotFound) {
		t.Fatalf("deleted credential was recreated: %v", err)
	}
}
