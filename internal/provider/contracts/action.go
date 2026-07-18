package contracts

import (
	"context"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
)

type ActionRequest struct {
	Asset          asset.Asset    `json:"asset"`
	Action         string         `json:"action"`
	Parameters     map[string]any `json:"parameters,omitempty"`
	IdempotencyKey string         `json:"idempotency_key"`
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
