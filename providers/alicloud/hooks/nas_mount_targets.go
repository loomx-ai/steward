package hooks

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const (
	nasFileSystemNativeType  = "ACS::NAS::FileSystem"
	nasMountTargetNativeType = "ACS::NAS::MountTarget"
	nasMountTargetEvidence   = "nas:DescribeMountTargets"
)

// NASMountTargets marks mount targets as direct, exclusively owned children
// of their file system so cleanup schedules them before the file system.
type NASMountTargets struct{}

func NewNASMountTargets() *NASMountTargets {
	return &NASMountTargets{}
}

func (*NASMountTargets) Contribute(
	_ context.Context,
	_ asset.ScopeID,
	assets []asset.Asset,
) (governance.Contribution, error) {
	result := governance.Contribution{}
	for _, mountTarget := range assets {
		if mountTarget.Identity.Provider != asset.ProviderAliCloud ||
			mountTarget.Identity.NativeType != nasMountTargetNativeType {
			continue
		}
		fileSystemID := strings.TrimSpace(normalizedScalar(
			mountTarget.Normalized["fileSystemId"],
		))
		if fileSystemID == "" {
			continue
		}
		fileSystem, found := resolveScopedAsset(
			mountTarget,
			nasFileSystemNativeType,
			fileSystemID,
			assets,
		)
		if !found {
			continue
		}
		evidence := map[string]any{
			"source":              nasMountTargetEvidence,
			"lifecycle_kind":      "nas_mount_target",
			"file_system_id":      fileSystemID,
			"mount_target_domain": mountTarget.Identity.NativeID,
		}
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{
			ControllerAssetID: fileSystem.ID,
			ManagedAssetID:    mountTarget.ID,
			Authority:         graph.AuthorityAuthoritative,
			Ownership:         graph.OwnershipExclusive,
			CleanupPolicy:     graph.CleanupDirect,
			EvidenceSource:    nasMountTargetEvidence,
			Evidence:          evidence,
			Confidence:        1,
		})
	}
	return result, nil
}
