package requestmeta

import "context"

type workloadKey struct{}
type Workload struct{ Phase, RunID string }

// WithWorkload is called by the durable worker, never from client input.
func WithWorkload(ctx context.Context, phase, runID string) context.Context {
	return context.WithValue(ctx, workloadKey{}, Workload{Phase: phase, RunID: runID})
}

func WorkloadFrom(ctx context.Context) Workload {
	w, ok := ctx.Value(workloadKey{}).(Workload)
	if !ok {
		return Workload{Phase: "read", RunID: RequestID(ctx)}
	}
	return w
}
