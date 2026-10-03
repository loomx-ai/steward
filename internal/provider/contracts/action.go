package contracts

import (
	"context"
	"errors"
	"sync/atomic"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
)

type ActionRequest struct {
	Asset          asset.Asset    `json:"asset"`
	Action         string         `json:"action"`
	Parameters     map[string]any `json:"parameters,omitempty"`
	IdempotencyKey string         `json:"idempotency_key"`
	// LifecycleImpacts are populated from the reviewed plan by the executor.
	// They are not caller-supplied provider parameters.
	LifecycleImpacts []ActionImpact `json:"lifecycle_impacts,omitempty"`
	// PrerequisiteDeletions come from reviewed direct-child steps on which
	// this step depends. Providers must still verify native absence.
	PrerequisiteDeletions []ActionImpact `json:"prerequisite_deletions,omitempty"`
	// ExecutionResult is supplied only for readback, from the persisted provider
	// receipt. It survives worker restart and is never a caller-supplied parameter.
	ExecutionResult *ActionResult `json:"-"`
}

type ActionImpact struct {
	Asset        asset.Asset   `json:"asset"`
	ControllerID asset.AssetID `json:"controller_id"`
	Delete       bool          `json:"delete"`
}

type PreflightResult struct {
	Allowed  bool           `json:"allowed"`
	Absent   bool           `json:"absent,omitempty"`
	Reason   string         `json:"reason,omitempty"`
	Evidence map[string]any `json:"evidence,omitempty"`
}

type ActionResult struct {
	ProviderRequestID   string         `json:"provider_request_id,omitempty"`
	ProviderOperationID string         `json:"provider_operation_id,omitempty"`
	Data                map[string]any `json:"data,omitempty"`
	RetryAfter          time.Duration  `json:"retry_after,omitempty"`
}

type WaitResult struct {
	Done       bool           `json:"done"`
	RetryAfter time.Duration  `json:"retry_after,omitempty"`
	State      string         `json:"state,omitempty"`
	Data       map[string]any `json:"data,omitempty"`
}

type ReadbackResult struct {
	Exists bool           `json:"exists"`
	State  string         `json:"state,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
}

type PreflightHook interface {
	Preflight(ctx context.Context, request ActionRequest) (PreflightResult, error)
}

type ActionHook interface {
	Execute(ctx context.Context, request ActionRequest) (ActionResult, error)
}

type WaiterHook interface {
	Wait(ctx context.Context, request ActionRequest, result ActionResult) (WaitResult, error)
}

type ReadbackHook interface {
	Readback(ctx context.Context, request ActionRequest) (ReadbackResult, error)
}

type ActionDriver interface {
	PreflightHook
	ActionHook
	WaiterHook
	ReadbackHook
}

type DeletionCheckTimeoutProvider interface {
	DeletionCheckTimeout() time.Duration
}

// ActionProvider resolves a provider-owned action driver after the application
// supplies only the connection identity and normalized asset.
type ActionProvider interface {
	Provider
	ResolveAction(context.Context, asset.ConnectionID, asset.Asset) (ActionDriver, error)
}

type LifecycleHook interface {
	ClassifyLifecycle(ctx context.Context, parent asset.Asset, child asset.Asset) (string, float64, error)
}

type NormalizedError = execution.ProviderError

// MarkBeforeMutation flags a provider failure raised before Execute submitted
// its delete. Errors that are not provider call errors are returned unchanged.
func MarkBeforeMutation(err error) error {
	var callError *ProviderCallError
	if errors.As(err, &callError) {
		callError.BeforeMutation = true
	}
	return err
}

// BeforeMutation reports whether err was raised before Execute submitted its
// delete.
func BeforeMutation(err error) bool {
	var callError *ProviderCallError
	return errors.As(err, &callError) && callError.BeforeMutation
}

type writeScopeKey struct{}

// WithWriteScope scopes ctx to one Execute call, so a provider transport can
// tell whether a failed read came before every write of that call.
func WithWriteScope(ctx context.Context) context.Context {
	return context.WithValue(ctx, writeScopeKey{}, new(atomic.Bool))
}

// NoteWrite records that the provider is sending a write in the Execute call.
func NoteWrite(ctx context.Context) {
	if written, ok := ctx.Value(writeScopeKey{}).(*atomic.Bool); ok {
		written.Store(true)
	}
}

// BeforeFirstWrite reports whether ctx belongs to an Execute call that has not
// sent a write yet.
func BeforeFirstWrite(ctx context.Context) bool {
	written, ok := ctx.Value(writeScopeKey{}).(*atomic.Bool)
	return ok && !written.Load()
}
