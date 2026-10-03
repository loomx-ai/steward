package graph

import (
	"errors"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

var (
	ErrLifecycleCycle      = errors.New("lifecycle authority contains a cycle")
	ErrLifecycleConflict   = errors.New("lifecycle authority has conflicting controllers")
	ErrLifecycleConfidence = errors.New("lifecycle authority confidence is too low")
	ErrLifecycleAuthority  = errors.New("lifecycle authority is not authoritative")
)

const ExecutableConfidence = 0.9

func ResolveAuthority(assetID asset.AssetID, bindings []LifecycleBinding) (AuthorityResolution, error) {
	return resolveController(assetID, indexDelegatingParents(bindings, false), false)
}

// ResolveExecutionController accepts only the operation edges activated by the
// planner's selected controllers. It preserves ownership in the returned chain.
func ResolveExecutionController(assetID asset.AssetID, bindings []LifecycleBinding) (AuthorityResolution, error) {
	return NewExecutionControllers(bindings).Resolve(assetID)
}

// ExecutionControllers resolves execution controllers for many assets against
// one binding set without rescanning the bindings for every asset.
type ExecutionControllers struct {
	parents map[asset.AssetID][]LifecycleBinding
}

func NewExecutionControllers(bindings []LifecycleBinding) ExecutionControllers {
	return ExecutionControllers{parents: indexDelegatingParents(bindings, true)}
}

// Resolve is ResolveExecutionController over the indexed bindings.
func (c ExecutionControllers) Resolve(assetID asset.AssetID) (AuthorityResolution, error) {
	return resolveController(assetID, c.parents, true)
}

func NativeDeleteEffect(binding LifecycleBinding) bool {
	return binding.ControllerAssetID != "" && binding.ManagedAssetID != "" && binding.ControllerAssetID != binding.ManagedAssetID && strings.TrimSpace(binding.EvidenceSource) != "" &&
		binding.Evidence[LifecycleEvidenceNativeDeleteEffect] == true &&
		binding.ClosedAt == nil && binding.Authority == AuthorityAuthoritative &&
		binding.Confidence >= ExecutableConfidence && binding.Confidence <= 1 &&
		binding.CleanupPolicy == CleanupDelegate &&
		(binding.Ownership == OwnershipShared || binding.Ownership == OwnershipReferenced) &&
		binding.Evidence[LifecycleEvidenceControllerDeleteGuaranteed] == true &&
		binding.Evidence[LifecycleEvidenceControllerVerifiesManagedAbsence] == true
}

func resolveController(assetID asset.AssetID, parents map[asset.AssetID][]LifecycleBinding, effects bool) (AuthorityResolution, error) {
	result := AuthorityResolution{
		RequestedAssetID:  assetID,
		ControllerAssetID: assetID,
	}
	seen := map[asset.AssetID]struct{}{assetID: {}}
	current := assetID

	for {
		candidates := parents[current]
		if len(candidates) == 0 {
			return result, nil
		}

		byController := make(map[asset.AssetID]LifecycleBinding, len(candidates))
		for _, binding := range candidates {
			if effects && binding.Evidence[LifecycleEvidenceNativeDeleteEffect] == true && !NativeDeleteEffect(binding) {
				return AuthorityResolution{}, fmt.Errorf("%w: native deletion effect for %s", ErrLifecycleAuthority, current)
			}
			if binding.Authority != AuthorityAuthoritative {
				return AuthorityResolution{}, fmt.Errorf("%w: managed asset %s", ErrLifecycleAuthority, current)
			}
			if binding.Confidence < ExecutableConfidence {
				return AuthorityResolution{}, fmt.Errorf("%w: managed asset %s has confidence %.2f", ErrLifecycleConfidence, current, binding.Confidence)
			}
			if existing, ok := byController[binding.ControllerAssetID]; !ok || binding.Confidence > existing.Confidence {
				byController[binding.ControllerAssetID] = binding
			}
		}
		if len(byController) != 1 {
			return AuthorityResolution{}, fmt.Errorf("%w: managed asset %s has %d controllers", ErrLifecycleConflict, current, len(byController))
		}

		var selected LifecycleBinding
		for _, binding := range byController {
			selected = binding
		}
		if _, ok := seen[selected.ControllerAssetID]; ok {
			return AuthorityResolution{}, fmt.Errorf("%w: controller %s", ErrLifecycleCycle, selected.ControllerAssetID)
		}

		result.Chain = append(result.Chain, selected)
		result.ControllerAssetID = selected.ControllerAssetID
		seen[selected.ControllerAssetID] = struct{}{}
		current = selected.ControllerAssetID
	}
}

// indexDelegatingParents groups the active delegating bindings by managed
// asset, keeping their input order.
func indexDelegatingParents(bindings []LifecycleBinding, effects bool) map[asset.AssetID][]LifecycleBinding {
	parents := make(map[asset.AssetID][]LifecycleBinding)
	for _, binding := range bindings {
		if binding.ClosedAt != nil {
			continue
		}
		if !(effects && binding.Evidence[LifecycleEvidenceNativeDeleteEffect] == true) && (binding.Ownership != OwnershipExclusive || binding.CleanupPolicy != CleanupDelegate) {
			continue
		}
		parents[binding.ManagedAssetID] = append(parents[binding.ManagedAssetID], binding)
	}
	return parents
}
