package gcp

import (
	"context"
	"net/url"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const routerPriorDelete = "router_prior_delete"

// The former generic Router driver issued one DELETE using this UUID scheme.
// Its weak receipt is not an execution cursor: only full native operation echoes
// can prove that old write terminal. This path never retries or verifies deletion.
func (a *action) routerPriorMutationSettled(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.MutationSettlement, error) {
	if request.Action != "delete" || request.Asset.ID == "" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || !gcpPartition(a.identity.Partition) || request.IdempotencyKey == "" || len(request.Parameters) != 0 || !firewallNumericID(text(request.Asset.Normalized["id"])) || request.Asset.Normalized["name"] != last(a.identity.NativeID) {
		return contracts.MutationSettlement{}, groupDenied("router_prior_identity_invalid")
	}
	if _, err := a.client.resourceURL(a.kind, a.identity.NativeID); err != nil {
		return contracts.MutationSettlement{}, err
	}
	operation, ok := result.Data["operation"].(string)
	if !ok || operation == "" || result.ProviderOperationID != operation {
		return contracts.MutationSettlement{}, groupDenied("router_prior_receipt_invalid")
	}
	parsed, err := url.Parse(operation)
	if err != nil {
		return contracts.MutationSettlement{}, groupDenied("router_prior_receipt_invalid")
	}
	expected, err := a.routerComponentOperationURL(last(parsed.Path))
	if err != nil || expected != operation {
		return contracts.MutationSettlement{}, groupDenied("router_prior_receipt_invalid")
	}
	return a.routerMutationStageSettled(ctx, request, routerPriorDelete, operation, "delete")
}
