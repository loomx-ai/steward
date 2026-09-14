package contracts

import "context"

// MutationSettlement reports that a previously invoked mutation can no longer
// write, independently of whether its requested resource deletion succeeded.
type MutationSettlement struct {
	Settled   bool
	Operation string
}

// MutationSettlementReader must perform only reads. Missing resources or expired
// operation records alone must not establish settlement. It never retries a write
// and must honor the supplied context deadline.
type MutationSettlementReader interface {
	MutationSettled(context.Context, ActionRequest, ActionResult) (MutationSettlement, error)
}
