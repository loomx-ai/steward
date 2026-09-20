// Package oauth runs the provider-neutral half of a browser authorization: the
// loopback callback, PKCE, flow ownership and expiry, and the refresh loop that
// keeps a stored authorization usable. Everything cloud-specific — endpoints,
// client identity, what a token is exchanged for — belongs to a Driver.
package oauth

import (
	"context"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Request is the authorization request the manager owns. A driver must send
// State and CodeChallenge to the provider unchanged: the manager verifies the
// state on the callback and holds the verifier that proves the exchange.
type Request struct {
	RedirectURI   string
	State         string
	CodeChallenge string
	CodeVerifier  string
}

// Authorization is one prepared browser authorization. Exchange closes over
// whatever per-flow material the driver produced in Authorize — a dynamically
// registered client, a resolved endpoint — so that state never has to travel
// back through the manager.
type Authorization struct {
	URL      string
	Exchange func(ctx context.Context, code string) (Session, error)
}

// Session is a completed authorization that has not yet been bound to a cloud
// scope. It is held in memory only: a session becomes storable material when
// Credential seals it together with the target the operator picked.
type Session interface {
	// Targets lists the cloud scopes this authorization can reach. Returning
	// none means the authorization already names exactly one scope.
	Targets(ctx context.Context) ([]contracts.OAuthTarget, error)
	// Credential seals the session and one target ID into a storable
	// credential. An empty target ID is only valid when Targets returned none.
	Credential(ctx context.Context, targetID string) (contracts.Credential, error)
}

// Callback is the loopback redirect a provider sends the browser back to.
// The clouds do not agree on how much of a loopback URI has to match what
// their client is registered for, so its shape is the driver's to state:
// Entra ID matches the host and the path exactly and lets only the port vary,
// while Google and Alibaba Cloud accept a path.
type Callback struct {
	// Host is advertised in the redirect URI: "localhost" or "127.0.0.1",
	// defaulting to "127.0.0.1". The listener always binds 127.0.0.1 whichever
	// name is advertised, exactly as MSAL does — a browser that resolves
	// "localhost" to ::1 first still reaches it, because it falls back to the
	// IPv4 address.
	Host string
	// Path is advertised as given. Empty advertises no path at all, which is
	// what the Azure CLI's client is registered for.
	Path string
}

// Driver is the cloud-specific half of a browser authorization.
type Driver interface {
	Provider() asset.Provider
	// Callback is the loopback redirect this provider will accept.
	Callback() Callback
	// Authorize validates the operator-supplied parameters and builds the
	// authorization request. A driver that registers a client dynamically does
	// it here, so a registration failure never opens a browser window.
	Authorize(ctx context.Context, params map[string]string, request Request) (Authorization, error)
}

// Refresher renews a stored authorization. The manager-side materializer owns
// the locking, the staleness test and the compare-and-swap; a driver only has
// to turn one credential snapshot into a fresher one.
type Refresher interface {
	// CredentialType is the stored type this refresher owns.
	CredentialType() asset.CredentialType
	// Usable reports the credential to hand a cloud client when the snapshot
	// still holds valid material, so an unexpired credential never triggers a
	// network call. The second result is false when a renewal is required.
	Usable(snapshot contracts.Credential, now time.Time) (contracts.Credential, bool, error)
	// Renew exchanges the snapshot for fresh material. It returns the values to
	// persist and the credential to hand the cloud client, which may differ:
	// Alibaba Cloud and AWS persist OAuth tokens but hand out temporary keys.
	Renew(ctx context.Context, snapshot contracts.Credential, now time.Time) (Renewal, error)
}

// Renewal is one successful refresh. Values replaces the stored credential's
// values; Usable is what the cloud client is given once the store agrees.
type Renewal struct {
	Values map[string]string
	Usable func(stored contracts.Credential) contracts.Credential
}
