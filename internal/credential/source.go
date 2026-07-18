package credential

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

var ErrReadOnly = errors.New("credential source is read-only")

type ValidatedSource struct {
	connections persistence.ConnectionRepository
	credentials persistence.CredentialRepository
	source      contracts.CredentialSource
	vault       *Vault
}

func NewValidatedSource(
	connections persistence.ConnectionRepository,
	credentials persistence.CredentialRepository,
	source contracts.CredentialSource,
) (*ValidatedSource, error) {
	if connections == nil || credentials == nil || source == nil {
		return nil, fmt.Errorf("validated credential source requires connections, credentials, and credential source")
	}
	vault, _ := source.(*Vault)
	return &ValidatedSource{connections: connections, credentials: credentials, source: source, vault: vault}, nil
}

func (s *ValidatedSource) Resolve(ctx context.Context, connectionID asset.ConnectionID) (contracts.Credential, error) {
	before, err := s.connections.GetConnection(ctx, connectionID)
	if err != nil {
		return contracts.Credential{}, err
	}
	if before.Status != asset.ConnectionActive {
		return contracts.Credential{}, fmt.Errorf("%w: connection %q status is %q", asset.ErrConnectionNotValidated, before.ID, before.Status)
	}
	beforeCredential, err := s.credentials.GetCredential(ctx, connectionID)
	if err != nil {
		return contracts.Credential{}, err
	}
	resolved, err := s.source.Resolve(ctx, connectionID)
	if err != nil {
		return contracts.Credential{}, err
	}
	after, err := s.connections.GetConnection(ctx, connectionID)
	if err != nil {
		return contracts.Credential{}, err
	}
	afterCredential, err := s.credentials.GetCredential(ctx, connectionID)
	if err != nil {
		return contracts.Credential{}, err
	}
	if after.Status != asset.ConnectionActive || !sameCredentialVersion(beforeCredential, afterCredential) {
		return contracts.Credential{}, fmt.Errorf("%w: connection %q changed while resolving its credential", asset.ErrConnectionNotValidated, before.ID)
	}
	resolved.ConnectionID = before.ID
	resolved.Site = before.Site
	resolved.Version = SnapshotVersion(before, beforeCredential)
	return resolved, nil
}

func (s *ValidatedSource) CompareAndSwap(
	ctx context.Context,
	expected contracts.Credential,
	replacement contracts.Credential,
) (contracts.Credential, error) {
	if s.vault == nil {
		return contracts.Credential{}, ErrReadOnly
	}
	if expected.ConnectionID == "" || expected.Version == "" {
		return contracts.Credential{}, fmt.Errorf("%w: credential snapshot identity and version are required", persistence.ErrConflict)
	}
	if replacement.Type != expected.Type {
		return contracts.Credential{}, fmt.Errorf("%w: credential type cannot change during refresh", persistence.ErrConflict)
	}
	if replacement.ConnectionID != "" && replacement.ConnectionID != expected.ConnectionID {
		return contracts.Credential{}, fmt.Errorf("%w: credential connection cannot change during refresh", persistence.ErrConflict)
	}
	if replacement.Site != "" && replacement.Site != expected.Site {
		return contracts.Credential{}, fmt.Errorf("%w: credential site cannot change during refresh", persistence.ErrConflict)
	}
	connection, err := s.connections.GetConnection(ctx, expected.ConnectionID)
	if err != nil {
		return contracts.Credential{}, err
	}
	sealed, err := s.credentials.GetCredential(ctx, expected.ConnectionID)
	if err != nil {
		return contracts.Credential{}, err
	}
	if SnapshotVersion(connection, sealed) != expected.Version {
		return contracts.Credential{}, persistence.ErrConflict
	}
	now := time.Now().UTC()
	if !now.After(sealed.UpdatedAt) {
		now = sealed.UpdatedAt.Add(time.Nanosecond)
	}
	replacement.ConnectionID = asset.ConnectionID("")
	replacement.Site = ""
	replacement.Version = ""
	next, err := s.vault.Seal(expected.ConnectionID, connection.Provider, replacement, now)
	if err != nil {
		return contracts.Credential{}, err
	}
	next.CreatedAt = sealed.CreatedAt
	if err := s.credentials.PutCredentialIfConnectionUnchanged(ctx, next, connection.UpdatedAt, sealed); err != nil {
		return contracts.Credential{}, err
	}
	replacement.ConnectionID = connection.ID
	replacement.Site = connection.Site
	replacement.Version = SnapshotVersion(connection, next)
	return replacement, nil
}

func SnapshotVersion(connection asset.CloudConnection, sealed asset.ConnectionCredential) string {
	payload, err := json.Marshal(struct {
		ConnectionID asset.ConnectionID
		Status       asset.ConnectionStatus
		Site         asset.ConnectionSite
		UpdatedAt    time.Time
		Credential   asset.ConnectionCredential
	}{
		ConnectionID: connection.ID,
		Status:       connection.Status,
		Site:         connection.Site,
		UpdatedAt:    connection.UpdatedAt,
		Credential:   sealed,
	})
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func sameCredentialVersion(left, right asset.ConnectionCredential) bool {
	if left.ConnectionID != right.ConnectionID ||
		left.Provider != right.Provider ||
		left.Type != right.Type ||
		left.EnvelopeVersion != right.EnvelopeVersion ||
		left.Nonce != right.Nonce ||
		left.Ciphertext != right.Ciphertext ||
		!left.CreatedAt.Equal(right.CreatedAt) ||
		!left.UpdatedAt.Equal(right.UpdatedAt) {
		return false
	}
	switch {
	case left.ExpiresAt == nil && right.ExpiresAt == nil:
		return true
	case left.ExpiresAt == nil || right.ExpiresAt == nil:
		return false
	default:
		return left.ExpiresAt.Equal(*right.ExpiresAt)
	}
}
