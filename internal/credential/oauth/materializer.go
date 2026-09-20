package oauth

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/persistence"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Materializer turns a stored OAuth credential into material a cloud client can
// use. It owns everything that is easy to get wrong and identical for every
// cloud: reading the current snapshot rather than trusting a stale one,
// serializing renewals per connection, and writing the result back under a
// compare-and-swap so two concurrent renewals cannot lose each other's rotated
// refresh token.
type Materializer struct {
	label     string
	refresher Refresher
	source    contracts.CredentialSource
	updater   contracts.CredentialUpdater
	now       func() time.Time
	locks     sync.Map
}

// NewMaterializer builds a materializer. The label names the cloud in operator
// facing errors ("Alibaba Cloud", "Microsoft Azure"). The updater may be nil,
// in which case a renewal that needs to persist is reported as unavailable
// rather than silently kept in memory.
func NewMaterializer(
	label string,
	refresher Refresher,
	source contracts.CredentialSource,
	updater contracts.CredentialUpdater,
	now func() time.Time,
) *Materializer {
	if now == nil {
		now = time.Now
	}
	return &Materializer{label: label, refresher: refresher, source: source, updater: updater, now: now}
}

// Materialize returns the credential a cloud client should use. A credential of
// any other type passes through untouched, so a provider can call this on every
// credential it resolves.
func (m *Materializer) Materialize(
	ctx context.Context,
	expected contracts.Credential,
) (contracts.Credential, error) {
	if m.refresher == nil || expected.Type != m.refresher.CredentialType() {
		return expected, nil
	}
	now := m.now().UTC()
	usable, ok, err := m.refresher.Usable(expected, now)
	if err != nil || ok {
		return usable, err
	}
	// Without a connection identity there is nothing to lock on and nothing to
	// write back to, so the renewal can only be used for this one call.
	if expected.ConnectionID == "" {
		return m.renew(ctx, expected, now)
	}
	lockValue, _ := m.locks.LoadOrStore(expected.ConnectionID, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	current, err := m.currentCredential(ctx, expected)
	if err != nil {
		return contracts.Credential{}, err
	}
	now = m.now().UTC()
	usable, ok, err = m.refresher.Usable(current, now)
	if err != nil || ok {
		return usable, err
	}
	return m.renew(ctx, current, now)
}

func (m *Materializer) currentCredential(
	ctx context.Context,
	expected contracts.Credential,
) (contracts.Credential, error) {
	if m.source == nil {
		return contracts.Credential{}, m.refreshUnavailable(nil)
	}
	current, err := m.source.Resolve(ctx, expected.ConnectionID)
	if err != nil {
		return contracts.Credential{}, m.refreshUnavailable(err)
	}
	if current.Type != m.refresher.CredentialType() {
		return contracts.Credential{}, m.refreshConflict(nil)
	}
	return current, nil
}

func (m *Materializer) renew(
	ctx context.Context,
	expected contracts.Credential,
	now time.Time,
) (contracts.Credential, error) {
	renewal, err := m.refresher.Renew(ctx, expected, now)
	if err != nil {
		// A renewal that lost a race against another worker is not a failure:
		// that worker's result is already stored and still valid.
		if winner, ok := m.concurrentCredentialAfterFailure(ctx, expected.ConnectionID, now); ok {
			return winner, nil
		}
		return contracts.Credential{}, err
	}
	replacement := contracts.Credential{Type: expected.Type, Values: renewal.Values}
	if expected.ConnectionID == "" {
		return renewal.Usable(replacement), nil
	}
	if m.updater == nil {
		return contracts.Credential{}, m.refreshUnavailable(nil)
	}
	updated, err := m.updater.CompareAndSwap(ctx, expected, replacement)
	if err == nil {
		return renewal.Usable(updated), nil
	}
	if !errors.Is(err, persistence.ErrConflict) {
		return contracts.Credential{}, m.refreshUnavailable(err)
	}
	return m.concurrentWinner(ctx, expected.ConnectionID, now)
}

func (m *Materializer) concurrentCredentialAfterFailure(
	ctx context.Context,
	connectionID asset.ConnectionID,
	now time.Time,
) (contracts.Credential, bool) {
	if connectionID == "" {
		return contracts.Credential{}, false
	}
	winner, err := m.concurrentWinner(ctx, connectionID, now)
	return winner, err == nil
}

func (m *Materializer) concurrentWinner(
	ctx context.Context,
	connectionID asset.ConnectionID,
	now time.Time,
) (contracts.Credential, error) {
	if m.source == nil {
		return contracts.Credential{}, m.refreshConflict(nil)
	}
	winner, err := m.source.Resolve(ctx, connectionID)
	if err != nil || winner.Type != m.refresher.CredentialType() {
		return contracts.Credential{}, m.refreshConflict(err)
	}
	usable, ok, err := m.refresher.Usable(winner, now)
	if err != nil || !ok {
		return contracts.Credential{}, m.refreshConflict(err)
	}
	return usable, nil
}

func (m *Materializer) refreshUnavailable(cause error) error {
	return contracts.NewCredentialValidationError(
		"credential_refresh_unavailable",
		"The "+m.label+" OAuth credential could not be refreshed.",
		cause,
	)
}

func (m *Materializer) refreshConflict(cause error) error {
	return contracts.NewCredentialValidationError(
		"credential_refresh_conflict",
		"The "+m.label+" credential changed while OAuth was refreshing.",
		cause,
	)
}

// ReauthenticationRequired is the error a refresher returns when no stored
// material can renew the authorization and the operator has to authorize again.
func ReauthenticationRequired(label string, cause error) error {
	return contracts.NewCredentialValidationError(
		"oauth_reauthentication_required",
		"The "+label+" OAuth authorization must be completed again.",
		cause,
	)
}

// RefreshFailed is the error a refresher returns when the provider rejected a
// renewal that was otherwise well formed.
func RefreshFailed(label string, cause error) error {
	return contracts.NewCredentialValidationError(
		"oauth_token_refresh_failed",
		label+" OAuth could not refresh the access token.",
		cause,
	)
}

// InvalidCredential is the error a refresher returns when stored values are
// missing or malformed.
func InvalidCredential(label string, cause error) error {
	return contracts.NewCredentialValidationError(
		"credential_fields_invalid",
		"The "+label+" OAuth credential fields are incomplete or invalid.",
		cause,
	)
}
