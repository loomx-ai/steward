package gcp

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// A reviewed child step is evidence of intent, never evidence of native absence.
func (a *action) servicePrerequisitesAbsent(ctx context.Context, request contracts.ActionRequest) error {
	seen := map[string]bool{}
	assetIDs := map[asset.AssetID]bool{request.Asset.ID: true}
	for _, prerequisite := range request.PrerequisiteDeletions {
		identity := prerequisite.Asset.Identity
		id := identity.NativeID
		kind, known := findType(identity.NativeType)
		if !known || len(kind.DeleteOperations) == 0 || !prerequisite.Delete || prerequisite.Asset.ID == "" || assetIDs[prerequisite.Asset.ID] || prerequisite.ControllerID != request.Asset.ID || seen[id] || identity.Provider != asset.ProviderGCP || identity.ConnectionID != request.Asset.Identity.ConnectionID || identity.Partition != request.Asset.Identity.Partition || !slices.Contains(serviceCascadeRules[a.kind.NativeType].directChildren, identity.NativeType) {
			return groupDenied("invalid_service_prerequisite")
		}
		if a.kind.NativeType == tpuQueueType {
			if err := a.client.tpuIdentity(tpuNodeType, id, prerequisite.Asset.Normalized); err != nil {
				return err
			}
			if text(prerequisite.Asset.Normalized[tpuProof]) == "" || text(prerequisite.Asset.Normalized[tpuQueueProof]) != text(request.Asset.Normalized[tpuProof]) {
				return groupDenied("tpu_prerequisite_proof_changed")
			}
			if err := a.client.tpuQueueRelation(request.Asset.Identity.NativeID, request.Asset.Normalized, id, prerequisite.Asset.Normalized); err != nil {
				return err
			}
		} else if isDataformFolder(a.kind.NativeType) {
			if err := a.client.dataformFolderRelation(request.Asset.Identity, request.Asset.Normalized, identity.NativeType, id, prerequisite.Asset.Normalized); err != nil {
				return err
			}
		} else if !strings.HasPrefix(id, request.Asset.Identity.NativeID+"/") {
			return groupDenied("invalid_service_prerequisite")
		}
		seen[id], assetIDs[prerequisite.Asset.ID] = true, true
		endpoint, err := a.client.resourceURL(kind, id)
		if err != nil {
			return err
		}
		if _, err := a.client.request(ctx, "GET", endpoint, nil); !isNotFound(err) {
			if err != nil {
				return err
			}
			return groupDenied("service_prerequisite_still_exists")
		}
	}
	return nil
}
