package credential

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const EnvelopeVersion = 1

var ErrUnavailable = errors.New("connection credential is unavailable")

type Vault struct {
	repository persistence.CredentialRepository
	aead       cipher.AEAD
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
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("create credential cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create credential AEAD: %w", err)
	}
	return &Vault{repository: repository, aead: aead}, nil
}

func (v *Vault) Seal(connectionID asset.ConnectionID, provider asset.Provider, value contracts.Credential, now time.Time) (asset.ConnectionCredential, error) {
	if connectionID == "" || provider == "" || value.Type == "" || len(value.Values) == 0 {
		return asset.ConnectionCredential{}, fmt.Errorf("connection, provider, credential type, and credential values are required")
	}
	payload, err := json.Marshal(value.Values)
	if err != nil {
		return asset.ConnectionCredential{}, fmt.Errorf("encode credential values: %w", err)
	}
	nonce := make([]byte, v.aead.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return asset.ConnectionCredential{}, fmt.Errorf("generate credential nonce: %w", err)
	}
	ciphertext := v.aead.Seal(nil, nonce, payload, associatedData(connectionID, provider, value.Type))
	return asset.ConnectionCredential{
		ConnectionID: connectionID, Provider: provider, Type: value.Type, EnvelopeVersion: EnvelopeVersion,
		Nonce: base64.StdEncoding.EncodeToString(nonce), Ciphertext: base64.StdEncoding.EncodeToString(ciphertext),
		ExpiresAt: value.ExpiresAt, CreatedAt: now, UpdatedAt: now,
	}, nil
}

func (v *Vault) Resolve(ctx context.Context, connectionID asset.ConnectionID) (contracts.Credential, error) {
	record, err := v.repository.GetCredential(ctx, connectionID)
	if err != nil {
		return contracts.Credential{}, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	if record.EnvelopeVersion != EnvelopeVersion {
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
	payload, err := v.aead.Open(nil, nonce, ciphertext, associatedData(record.ConnectionID, record.Provider, record.Type))
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
	return contracts.Credential{Type: record.Type, Values: values, ExpiresAt: record.ExpiresAt}, nil
}

func associatedData(connectionID asset.ConnectionID, provider asset.Provider, credentialType asset.CredentialType) []byte {
	return []byte(fmt.Sprintf("steward\x00credential\x00%d\x00%s\x00%s\x00%s", EnvelopeVersion, connectionID, provider, credentialType))
}
