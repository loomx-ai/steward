package credential

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/workspace"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/workloadidentity"
)

// EnvelopeVersion 1 is sealed with the master key itself and serves only the
// default workspace, which is all a self-hosted server has. Version 2 is
// sealed with a key derived for the record's workspace and binds the
// workspace into the associated data, so a ciphertext cannot move between
// workspaces of a shared pool.
const (
	EnvelopeVersion          = 1
	WorkspaceEnvelopeVersion = 2
)

var ErrUnavailable = errors.New("connection credential is unavailable")

type Vault struct {
	WorkloadIdentity *workloadidentity.Broker
	repository       persistence.CredentialRepository
	masterKey        []byte
	aead             cipher.AEAD
	strict           bool
}

func NewVault(encodedKey string, repository persistence.CredentialRepository) (*Vault, error) {
	if repository == nil {
		return nil, fmt.Errorf("credential repository is required")
	}
	key, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encodedKey))
	if err != nil {
		return nil, fmt.Errorf("decode credential master key: %w", err)
	}
	if len(key) != 32 {
		return nil, fmt.Errorf("credential master key must decode to exactly 32 bytes")
	}
	aead, err := newAEAD(key)
	if err != nil {
		return nil, err
	}
	return &Vault{repository: repository, masterKey: key, aead: aead}, nil
}

// RequireWorkspace makes sealing and opening without a workspace in context
// fail instead of using the default workspace's key.
func (v *Vault) RequireWorkspace() { v.strict = true }

func newAEAD(key []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create credential cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create credential AEAD: %w", err)
	}
	return aead, nil
}

// keyFor returns the cipher, envelope version and workspace for ctx.
func (v *Vault) keyFor(ctx context.Context) (cipher.AEAD, int, workspace.ID, error) {
	id, ok := workspace.From(ctx)
	if !ok {
		if v.strict {
			return nil, 0, "", fmt.Errorf("%w: no workspace", ErrUnavailable)
		}
		id = workspace.Default
	}
	if id == workspace.Default {
		return v.aead, EnvelopeVersion, id, nil
	}
	key, err := hkdf.Key(sha256.New, v.masterKey, nil, "steward workspace key\x00"+string(id), 32)
	if err != nil {
		return nil, 0, "", fmt.Errorf("derive workspace key: %w", err)
	}
	aead, err := newAEAD(key)
	return aead, WorkspaceEnvelopeVersion, id, err
}

func (v *Vault) Seal(ctx context.Context, connectionID asset.ConnectionID, provider asset.Provider, value contracts.Credential, now time.Time) (asset.ConnectionCredential, error) {
	if value.Type == asset.CredentialOIDC {
		if v.WorkloadIdentity == nil {
			return asset.ConnectionCredential{}, contracts.NewCredentialValidationError("oidc_unavailable", "Workload identity is not configured on this server.", nil)
		}
		if err := workloadidentity.ValidateConfig(provider, value); err != nil {
			return asset.ConnectionCredential{}, err
		}
	}
	if connectionID == "" || provider == "" || value.Type == "" || len(value.Values) == 0 {
		return asset.ConnectionCredential{}, fmt.Errorf("connection, provider, credential type, and credential values are required")
	}
	payload, err := json.Marshal(value.Values)
	if err != nil {
		return asset.ConnectionCredential{}, fmt.Errorf("encode credential values: %w", err)
	}
	aead, version, id, err := v.keyFor(ctx)
	if err != nil {
		return asset.ConnectionCredential{}, err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return asset.ConnectionCredential{}, fmt.Errorf("generate credential nonce: %w", err)
	}
	ciphertext := aead.Seal(nil, nonce, payload, associatedData(version, id, connectionID, provider, value.Type))
	return asset.ConnectionCredential{
		ConnectionID: connectionID, Provider: provider, Type: value.Type, EnvelopeVersion: version,
		Nonce: base64.StdEncoding.EncodeToString(nonce), Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
		ExpiresAt: value.ExpiresAt, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (v *Vault) Resolve(ctx context.Context, connectionID asset.ConnectionID) (contracts.Credential, error) {
	record, err := v.repository.GetCredential(ctx, connectionID)
	if err != nil {
		return contracts.Credential{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return v.open(ctx, record)
}

// open decrypts a sealed record and, for OIDC, binds it to workload identity.
func (v *Vault) open(ctx context.Context, record asset.ConnectionCredential) (contracts.Credential, error) {
	aead, version, id, err := v.keyFor(ctx)
	if err != nil {
		return contracts.Credential{}, err
	}
	if record.EnvelopeVersion != version {
		return contracts.Credential{}, fmt.Errorf("%w: unsupported envelope version %d", ErrUnavailable, record.EnvelopeVersion)
	}
	nonce, err := base64.StdEncoding.DecodeString(record.Nonce)
	if err != nil {
		return contracts.Credential{}, fmt.Errorf("%w: invalid nonce", ErrUnavailable)
	}
	ciphertext, err := base64.StdEncoding.DecodeString(record.Ciphertext)
	if err != nil {
		return contracts.Credential{}, fmt.Errorf("%w: invalid ciphertext", ErrUnavailable)
	}
	payload, err := aead.Open(nil, nonce, ciphertext, associatedData(version, id, record.ConnectionID, record.Provider, record.Type))
	if err != nil {
		return contracts.Credential{}, fmt.Errorf("%w: decrypt failed", ErrUnavailable)
	}
	values := map[string]string{}
	if err := json.Unmarshal(payload, &values); err != nil {
		return contracts.Credential{}, fmt.Errorf("%w: invalid plaintext", ErrUnavailable)
	}
	if record.ExpiresAt != nil && !record.ExpiresAt.After(time.Now()) {
		return contracts.Credential{}, fmt.Errorf("%w: credential expired", ErrUnavailable)
	}
	value := contracts.Credential{Type: record.Type, Values: values, ExpiresAt: record.ExpiresAt}
	if record.Type == asset.CredentialOIDC {
		if v.WorkloadIdentity == nil {
			return contracts.Credential{}, contracts.NewCredentialValidationError("oidc_unavailable", "Workload identity is not configured on this server.", nil)
		}
		return v.WorkloadIdentity.Bind(ctx, record, value)
	}
	return value, nil
}

func (v *Vault) OIDCTrust(ctx context.Context, id asset.ConnectionID) (workloadidentity.Trust, error) {
	record, err := v.repository.GetCredential(ctx, id)
	if err != nil {
		return workloadidentity.Trust{}, err
	}
	if record.Type != asset.CredentialOIDC {
		return workloadidentity.Trust{}, contracts.NewCredentialValidationError("credential_type_unsupported", "This connection does not use OIDC.", nil)
	}
	value, err := v.Resolve(workloadidentity.ForValidation(ctx), id)
	if err != nil {
		return workloadidentity.Trust{}, err
	}
	return v.WorkloadIdentity.Trust(ctx, id, record.Provider, value)
}

// associatedData keeps version 1's exact bytes, which existing self-hosted
// records were sealed with.
func associatedData(version int, id workspace.ID, connectionID asset.ConnectionID, provider asset.Provider, credentialType asset.CredentialType) []byte {
	if version == EnvelopeVersion {
		return []byte(fmt.Sprintf("steward\x00credential\x00%d\x00%s\x00%s\x00%s", version, connectionID, provider, credentialType))
	}
	return []byte(fmt.Sprintf("steward\x00credential\x00%d\x00%s\x00%s\x00%s\x00%s", version, id, connectionID, provider, credentialType))
}

// SealSecret encrypts a workspace secret that is not a cloud credential, such
// as a notification webhook address. The purpose binds the ciphertext to the
// record that owns it, so it cannot be moved to another record.
func (v *Vault) SealSecret(ctx context.Context, purpose string, plaintext string) (string, error) {
	aead, version, id, err := v.keyFor(ctx)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("generate secret nonce: %w", err)
	}
	sealed := aead.Seal(nonce, nonce, []byte(plaintext), secretAssociatedData(version, id, purpose))
	return base64.StdEncoding.EncodeToString(sealed), nil
}

func (v *Vault) OpenSecret(ctx context.Context, purpose string, sealed string) (string, error) {
	aead, version, id, err := v.keyFor(ctx)
	if err != nil {
		return "", err
	}
	raw, err := base64.StdEncoding.DecodeString(sealed)
	if err != nil || len(raw) < aead.NonceSize() {
		return "", fmt.Errorf("%w: invalid secret envelope", ErrUnavailable)
	}
	plaintext, err := aead.Open(nil, raw[:aead.NonceSize()], raw[aead.NonceSize():], secretAssociatedData(version, id, purpose))
	if err != nil {
		return "", fmt.Errorf("%w: decrypt failed", ErrUnavailable)
	}
	return string(plaintext), nil
}

func secretAssociatedData(version int, id workspace.ID, purpose string) []byte {
	if version == EnvelopeVersion {
		return []byte(fmt.Sprintf("steward\x00secret\x00%d\x00%s", version, purpose))
	}
	return []byte(fmt.Sprintf("steward\x00secret\x00%d\x00%s\x00%s", version, id, purpose))
}
