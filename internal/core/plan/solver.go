package plan

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/idgen"
)

var ErrCleanupTaskInvalidated = errors.New("cleanup task snapshot is no longer current")

type Input struct {
	CleanupTaskID     CleanupTaskID                    `json:"cleanup_task_id,omitempty"`
	Selectors         []CleanupSelector                `json:"selectors,omitempty"`
	ResolvedAssetIDs  []asset.AssetID                  `json:"resolved_asset_ids"`
	Assets            []asset.Asset                    `json:"assets"`
	Relationships     []graph.Relationship             `json:"relationships,omitempty"`
	LifecycleBindings []graph.LifecycleBinding         `json:"lifecycle_bindings,omitempty"`
	Protections       []ProtectionPolicy               `json:"protections,omitempty"`
	Revision          RevisionBinding                  `json:"revision"`
	Coverage          ScanCoverage                     `json:"scan_coverage"`
	RequestOptions    map[asset.AssetID]map[string]any `json:"request_options,omitempty"`
}

type Result struct {
	Steps        []CleanupTaskStep `json:"steps"`
	ImpactItems  []ImpactItem      `json:"impact_items"`
	Blockers     []Blocker         `json:"blockers"`
	Warnings     []Warning         `json:"warnings"`
	SnapshotHash string            `json:"snapshot_hash"`
}

func Solve(input Input) (Result, error) {
	selected := uniqueAssetIDs(input.ResolvedAssetIDs)
	if len(selected) == 0 {
		return Result{}, fmt.Errorf("cleanup task requires at least one selected asset")
	}
	assets, err := indexAssets(input.Assets)
	if err != nil {
		return Result{}, err
	}
	bindings := activeBindings(input.LifecycleBindings)
	relationships := activeRelationships(input.Relationships)
	protections := indexProtections(input.Protections)
	result := Result{}
	ids := newSolveIDs()
	blockers := newBlockerSet()
	selectedSet := make(map[asset.AssetID]struct{}, len(selected))
	for _, id := range selected {
		selectedSet[id] = struct{}{}
	}

	candidates := make(map[asset.AssetID]struct{}, len(selected))
	for _, id := range selected {
		value, ok := assets[id]
		if !ok {
			blockers.add(Blocker{Code: BlockAssetMissing, AssetID: id, Message: "selected asset is not present in the planning snapshot"})
			continue
		}
		if value.ClosedAt != nil {
			blockers.add(Blocker{Code: BlockAssetClosed, AssetID: id, Message: "selected asset is already closed"})
			continue
		}
		resolution, resolveErr := graph.ResolveAuthority(id, bindings)
		if resolveErr != nil {
			blockers.add(lifecycleBlocker(id, resolveErr))
			continue
		}
		if resolution.ControllerAssetID != id {
			if _, selectedController := selectedSet[resolution.ControllerAssetID]; !selectedController {
				if skipWithoutSelectedController(resolution.Chain) {
					result.Warnings = append(result.Warnings, Warning{
						Code: WarningManagedByControllerSkipped, AssetID: id,
						ControllerID: resolution.ControllerAssetID,
						Message:      "resource cleanup is delegated to an unselected lifecycle controller and will be skipped",
						Evidence:     managedControllerEvidence(resolution),
					})
					continue
				}
				if binding, allowed := directCleanupFallback(resolution.Chain); allowed {
					candidates[id] = struct{}{}
					result.Warnings = append(result.Warnings, Warning{
						Code: WarningManagedResourceDirectCleanup, AssetID: id, ControllerID: resolution.ControllerAssetID,
						Message: "resource is managed by a lifecycle controller; direct cleanup is allowed but controller cleanup is recommended",
						Evidence: map[string]any{
							"binding": binding, "recommended_controller_id": resolution.ControllerAssetID,
						},
					})
					continue
				}
				blockers.add(Blocker{
					Code: BlockManagedByController, AssetID: id, ControllerID: resolution.ControllerAssetID,
					Message:  "asset cleanup is delegated to its highest lifecycle controller",
					Evidence: managedControllerEvidence(resolution),
				})
				continue
			}
			candidates[resolution.ControllerAssetID] = struct{}{}
			continue
		}
		candidates[id] = struct{}{}
	}

	byController := bindingsByController(bindings)
	suppressed := make(map[asset.AssetID]struct{})
	directChildren := make(map[asset.AssetID]asset.AssetID)
	controllerRoots := make(map[asset.AssetID]bool)
	impactByKey := make(map[string]ImpactItem)
	candidateIDs := mapKeys(candidates)
	for _, root := range candidateIDs {
		visited := make(map[asset.AssetID]bool)
		var walk func(asset.AssetID, asset.AssetID)
		walk = func(controllerID, effectiveStepOwner asset.AssetID) {
			if visited[controllerID] {
				blockers.add(Blocker{Code: BlockLifecycleCycle, AssetID: controllerID, ControllerID: root, Message: "lifecycle controller graph contains a cycle"})
				return
			}
			visited[controllerID] = true
			children := byController[controllerID]
			if len(children) > 0 {
				controllerRoots[effectiveStepOwner] = true
			}
			for _, binding := range children {
				managedID := binding.ManagedAssetID
				managed, exists := assets[managedID]
				if !exists || managed.ClosedAt != nil {
					blockers.add(Blocker{Code: BlockControllerUnavailable, AssetID: managedID, ControllerID: controllerID, Message: "lifecycle-managed asset is unavailable in the planning snapshot"})
					continue
				}
				if binding.Ownership == graph.OwnershipExclusive && binding.CleanupPolicy == graph.CleanupDelegate {
					resolution, resolveErr := graph.ResolveAuthority(managedID, bindings)
					if resolveErr != nil {
						blockers.add(lifecycleBlocker(managedID, resolveErr))
						continue
					}
					if resolution.ControllerAssetID != effectiveStepOwner {
						continue
					}
				}
				nextStepOwner := effectiveStepOwner
				if binding.CleanupPolicy == graph.CleanupDirect {
					if binding.Authority != graph.AuthorityAuthoritative || binding.Ownership != graph.OwnershipExclusive || binding.Confidence < graph.ExecutableConfidence {
						blockers.add(Blocker{Code: BlockDirectCleanupInvalid, AssetID: managedID, ControllerID: controllerID, Message: "direct child cleanup lacks authoritative exclusive lifecycle evidence"})
						continue
					}
					directChildren[managedID] = effectiveStepOwner
					nextStepOwner = managedID
				} else {
					suppressed[managedID] = struct{}{}
					stepID := ids.step(effectiveStepOwner)
					expected := impactExpectation(binding, managed, input.RequestOptions[effectiveStepOwner])
					impactKey := string(effectiveStepOwner) + "\x00" + string(controllerID) + "\x00" + string(managedID)
					impact := ImpactItem{
						ID: ids.impact(effectiveStepOwner, controllerID, managedID), CleanupTaskID: input.CleanupTaskID,
						AssetID: managedID, ControllerID: controllerID, DelegatedTo: stepID,
						Ownership: binding.Ownership, CleanupPolicy: binding.CleanupPolicy, Expected: expected,
						Evidence: impactEvidence(binding, effectiveStepOwner), MayContinueBilling: expected != ExpectedDelegatedDelete,
					}
					impactByKey[impactKey] = impact
					if expected == ExpectedUnknown {
						blockers.add(Blocker{Code: BlockLifecycleAuthority, AssetID: managedID, ControllerID: controllerID, Message: "lifecycle cleanup outcome is unknown", Evidence: impact.Evidence})
					}
					if expected == ExpectedDelegatedDelete {
						if protection, protected := protections[managedID]; protected && protection.Protected {
							blockers.add(protectionBlocker(protection, effectiveStepOwner))
						}
					}
				}
				walk(managedID, nextStepOwner)
			}
			visited[controllerID] = false
		}
		walk(root, root)
	}

	stepAssets := make(map[asset.AssetID]CleanupTaskStep)
	for _, id := range candidateIDs {
		if _, isImpact := suppressed[id]; isImpact {
			continue
		}
		value, ok := assets[id]
		if !ok {
			continue
		}
		if !value.Capabilities.Has(asset.CapabilityActionable) {
			result.Warnings = append(result.Warnings, Warning{
				Code: WarningNotActionableSkipped, AssetID: id,
				Message: "asset does not declare an actionable capability and will be skipped",
			})
			continue
		}
		if protection, protected := protections[id]; protected && protection.Protected {
			blockers.add(protectionBlocker(protection, id))
			continue
		}
		kind := StepDirect
		if controllerRoots[id] {
			kind = StepController
		}
		stepAssets[id] = CleanupTaskStep{
			ID: ids.step(id), CleanupTaskID: input.CleanupTaskID, AssetID: id, Kind: kind, Action: "delete",
			RequestOptions: cloneMap(input.RequestOptions[id]),
			Evidence:       map[string]any{"inventory_revision": input.Revision.InventoryRevision, "graph_revision": input.Revision.GraphRevision, "spec_bundle_revision": input.Revision.SpecBundleRevision, "spec_hash": input.Revision.SpecHash},
		}
	}
	for childID, controllerID := range directChildren {
		value, ok := assets[childID]
		if !ok || !value.Capabilities.Has(asset.CapabilityActionable) {
			blockers.add(Blocker{Code: BlockNotActionable, AssetID: childID, ControllerID: controllerID, Message: "direct lifecycle cleanup requires an actionable child asset"})
			continue
		}
		if protection, protected := protections[childID]; protected && protection.Protected {
			blockers.add(protectionBlocker(protection, controllerID))
			continue
		}
		kind := StepDirect
		if controllerRoots[childID] {
			kind = StepController
		}
		stepAssets[childID] = CleanupTaskStep{
			ID: ids.step(childID), CleanupTaskID: input.CleanupTaskID, AssetID: childID, Kind: kind, Action: "delete",
			RequestOptions: cloneMap(input.RequestOptions[childID]),
			Evidence:       map[string]any{"lifecycle_controller": controllerID, "cleanup_policy": graph.CleanupDirect},
		}
	}

	impactItems := stableImpacts(impactByKey)
	for _, impact := range impactItems {
		if !requiresManagedAbsenceVerification(impact) {
			continue
		}
		evidence := cloneMap(impact.Evidence)
		evidence["controller_asset_id"] = impact.ControllerID
		stepAssets[impact.AssetID] = CleanupTaskStep{
			ID: ids.step(impact.AssetID), CleanupTaskID: input.CleanupTaskID,
			AssetID: impact.AssetID, Kind: StepVerification,
			Action: ActionVerifyManagedAbsent, Evidence: evidence,
		}
	}
	deletionOwner := make(map[asset.AssetID]asset.AssetID, len(stepAssets)+len(impactItems))
	stepOwnerByID := make(map[StepID]asset.AssetID, len(stepAssets))
	blockingVerifications := make(map[asset.AssetID][]asset.AssetID)
	for id, step := range stepAssets {
		deletionOwner[id] = id
		stepOwnerByID[step.ID] = id
	}
	for _, impact := range impactItems {
		if impact.Expected != ExpectedDelegatedDelete {
			continue
		}
		if verification, exists := stepAssets[impact.AssetID]; exists &&
			verification.Kind == StepVerification &&
			managedVerificationBlocksDependents(impact) {
			deletionOwner[impact.AssetID] = impact.AssetID
			if controllerID, controllerExists := stepOwnerByID[impact.DelegatedTo]; controllerExists {
				blockingVerifications[controllerID] = append(
					blockingVerifications[controllerID],
					impact.AssetID,
				)
			}
		} else if ownerID, exists := stepOwnerByID[impact.DelegatedTo]; exists {
			deletionOwner[impact.AssetID] = ownerID
		}
	}

	dependencies := make(map[asset.AssetID]map[asset.AssetID]struct{})
	for childID, controllerID := range directChildren {
		if _, childExists := stepAssets[childID]; childExists {
			if _, controllerExists := stepAssets[controllerID]; controllerExists {
				addDependency(dependencies, controllerID, childID)
			}
		}
	}
	for _, impact := range impactItems {
		if !requiresManagedAbsenceVerification(impact) {
			continue
		}
		if controllerID, exists := stepOwnerByID[impact.DelegatedTo]; exists {
			addDependency(dependencies, impact.AssetID, controllerID)
		}
	}
	for _, relationship := range relationships {
		if !ordersDeletion(relationship.Type) {
			continue
		}
		if isLifecycleControllerRelationship(relationship, impactItems) {
			continue
		}
		sourceOwner, sourceExists := deletionOwner[relationship.SourceAssetID]
		if !sourceExists {
			continue
		}
		targetOwner, targetExists := deletionOwner[relationship.TargetAssetID]
		if !targetExists {
			continue
		}
		if relationship.Evidence[graph.RelationshipEvidenceDeletionOrder] == graph.DeletionOrderTargetBeforeSource {
			addDependencyWithManagedVerifications(
				dependencies,
				sourceOwner,
				targetOwner,
				blockingVerifications,
			)
			continue
		}
		addDependencyWithManagedVerifications(
			dependencies,
			targetOwner,
			sourceOwner,
			blockingVerifications,
		)
	}
	for id, step := range stepAssets {
		for dependencyID := range dependencies[id] {
			step.DependsOn = append(step.DependsOn, stepAssets[dependencyID].ID)
		}
		sort.Slice(step.DependsOn, func(i, j int) bool { return step.DependsOn[i] < step.DependsOn[j] })
		stepAssets[id] = step
	}
	steps, acyclic := topologicalSteps(stepAssets, dependencies)
	if !acyclic {
		blockers.add(Blocker{Code: BlockDependencyCycle, Message: "resource dependency graph cannot be reduced to a cleanup DAG"})
		steps = stableSteps(stepAssets)
	}
	result.Steps = steps
	result.ImpactItems = impactItems
	result.Blockers = blockers.values()
	sort.Slice(result.Warnings, func(i, j int) bool {
		left := strings.Join([]string{string(result.Warnings[i].Code), string(result.Warnings[i].AssetID), string(result.Warnings[i].ControllerID)}, "\x00")
		right := strings.Join([]string{string(result.Warnings[j].Code), string(result.Warnings[j].AssetID), string(result.Warnings[j].ControllerID)}, "\x00")
		return left < right
	})
	result.SnapshotHash, err = snapshotHash(input, selected, assets, relationships, bindings)
	if err != nil {
		return Result{}, err
	}
	return result, nil
}

func requiresManagedAbsenceVerification(impact ImpactItem) bool {
	return impact.Expected == ExpectedDelegatedDelete &&
		!ControllerDeletionImpliesAbsence(impact.Evidence)
}

func managedVerificationBlocksDependents(impact ImpactItem) bool {
	value, _ := impact.Evidence[graph.LifecycleEvidenceWaitUntilAbsentBeforeDependents].(bool)
	return value
}

func isLifecycleControllerRelationship(
	relationship graph.Relationship,
	impacts []ImpactItem,
) bool {
	for _, impact := range impacts {
		if (relationship.SourceAssetID == impact.AssetID &&
			relationship.TargetAssetID == impact.ControllerID) ||
			(relationship.TargetAssetID == impact.AssetID &&
				relationship.SourceAssetID == impact.ControllerID) {
			return true
		}
	}
	return false
}

func addDependencyWithManagedVerifications(
	dependencies map[asset.AssetID]map[asset.AssetID]struct{},
	dependent asset.AssetID,
	dependency asset.AssetID,
	blockingVerifications map[asset.AssetID][]asset.AssetID,
) {
	addDependency(dependencies, dependent, dependency)
	for _, verification := range blockingVerifications[dependency] {
		if verification != dependent {
			addDependency(dependencies, dependent, verification)
		}
	}
}

func skipWithoutSelectedController(chain []graph.LifecycleBinding) bool {
	for _, binding := range chain {
		action, _ := binding.Evidence[graph.LifecycleEvidenceUnselectedControllerAction].(string)
		if strings.TrimSpace(action) == graph.LifecycleUnselectedControllerSkip {
			return true
		}
	}
	return false
}

func directCleanupFallback(chain []graph.LifecycleBinding) (graph.LifecycleBinding, bool) {
	if len(chain) != 1 {
		return graph.LifecycleBinding{}, false
	}
	binding := chain[0]
	if !binding.DirectCleanupAllowed ||
		binding.Authority != graph.AuthorityAuthoritative ||
		binding.Ownership != graph.OwnershipExclusive ||
		binding.CleanupPolicy != graph.CleanupDelegate ||
		binding.Confidence < graph.ExecutableConfidence {
		return graph.LifecycleBinding{}, false
	}
	return binding, true
}

func managedControllerEvidence(resolution graph.AuthorityResolution) map[string]any {
	evidence := map[string]any{"chain": resolution.Chain}
	if len(resolution.Chain) == 0 {
		return evidence
	}
	immediate := resolution.Chain[0]
	evidence["immediate_controller_id"] = immediate.ControllerAssetID
	evidence["suggested_asset_ids"] = []asset.AssetID{immediate.ControllerAssetID}
	if kind, ok := immediate.Evidence["lifecycle_kind"].(string); ok && strings.TrimSpace(kind) != "" {
		evidence["lifecycle_kind"] = kind
	}
	return evidence
}

func CheckFresh(stored CleanupTask, current RevisionBinding, snapshotHash string) error {
	if stored.Revision != current {
		return fmt.Errorf("%w: bound revision changed", ErrCleanupTaskInvalidated)
	}
	if strings.TrimSpace(stored.SnapshotHash) == "" || stored.SnapshotHash != snapshotHash {
		return fmt.Errorf("%w: lifecycle impact snapshot changed", ErrCleanupTaskInvalidated)
	}
	return nil
}

func lifecycleBlocker(id asset.AssetID, err error) Blocker {
	blocker := Blocker{AssetID: id, Message: err.Error()}
	switch {
	case errors.Is(err, graph.ErrLifecycleConflict):
		blocker.Code = BlockLifecycleConflict
	case errors.Is(err, graph.ErrLifecycleCycle):
		blocker.Code = BlockLifecycleCycle
	case errors.Is(err, graph.ErrLifecycleConfidence):
		blocker.Code = BlockLifecycleConfidence
	case errors.Is(err, graph.ErrLifecycleAuthority):
		blocker.Code = BlockLifecycleAuthority
	default:
		blocker.Code = BlockLifecycleAuthority
	}
	return blocker
}

func impactExpectation(binding graph.LifecycleBinding, managed asset.Asset, options map[string]any) ExpectedOutcome {
	if binding.Ownership == graph.OwnershipShared || binding.Ownership == graph.OwnershipReferenced {
		return ExpectedRetainShared
	}
	if binding.Ownership == graph.OwnershipUnknown || binding.CleanupPolicy == graph.CleanupUnknown {
		return ExpectedUnknown
	}
	if explicitlyRetained(managed, binding, options) {
		return ExpectedRetainExplicit
	}
	if binding.CleanupPolicy == graph.CleanupDelegate {
		if deleteByDefault, exists := binding.Evidence["delete_by_default"].(bool); exists && !deleteByDefault {
			return ExpectedProviderDefaultRetain
		}
		return ExpectedDelegatedDelete
	}
	if binding.CleanupPolicy == graph.CleanupRetain {
		return ExpectedProviderDefaultRetain
	}
	return ExpectedUnknown
}

func explicitlyRetained(managed asset.Asset, binding graph.LifecycleBinding, options map[string]any) bool {
	if retained, ok := options["retain_all_resources"].(bool); ok && retained {
		return true
	}
	for _, id := range stringValues(options["retain_resources"]) {
		if id == managed.Identity.NativeID || id == string(managed.ID) {
			return true
		}
	}
	resourceType, _ := binding.Evidence["resource_type"].(string)
	if resourceType == "" {
		return false
	}
	items, _ := options["delete_options"].([]any)
	for _, item := range items {
		object, _ := item.(map[string]any)
		if object["resource_type"] == resourceType && object["delete_mode"] == "retain" {
			return true
		}
	}
	return false
}

func stringValues(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			if text, ok := item.(string); ok {
				result = append(result, text)
			}
		}
		return result
	default:
		return nil
	}
}

func impactEvidence(binding graph.LifecycleBinding, effectiveController asset.AssetID) map[string]any {
	evidence := cloneMap(binding.Evidence)
	evidence["authority"] = binding.Authority
	evidence["confidence"] = binding.Confidence
	evidence["evidence_source"] = binding.EvidenceSource
	evidence["effective_controller"] = effectiveController
	evidence["graph_revision"] = binding.GraphRevision
	return evidence
}

func protectionBlocker(policy ProtectionPolicy, controller asset.AssetID) Blocker {
	return Blocker{
		Code: BlockProtected, AssetID: policy.AssetID, ControllerID: controller,
		Message:  "cleanup is denied by protection policy: " + policy.Reason,
		Evidence: map[string]any{"source": policy.Source, "policy": policy.Evidence},
	}
}

func indexAssets(values []asset.Asset) (map[asset.AssetID]asset.Asset, error) {
	result := make(map[asset.AssetID]asset.Asset, len(values))
	for _, value := range values {
		if value.ID == "" {
			return nil, fmt.Errorf("planning snapshot contains asset without ID")
		}
		if _, duplicate := result[value.ID]; duplicate {
			return nil, fmt.Errorf("planning snapshot contains duplicate asset %q", value.ID)
		}
		result[value.ID] = value
	}
	return result, nil
}

func activeBindings(values []graph.LifecycleBinding) []graph.LifecycleBinding {
	result := make([]graph.LifecycleBinding, 0, len(values))
	for _, value := range values {
		if value.ClosedAt == nil {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return bindingKey(result[i]) < bindingKey(result[j]) })
	return result
}

func activeRelationships(values []graph.Relationship) []graph.Relationship {
	result := make([]graph.Relationship, 0, len(values))
	for _, value := range values {
		if value.ClosedAt == nil {
			result = append(result, value)
		}
	}
	sort.Slice(result, func(i, j int) bool { return relationshipKey(result[i]) < relationshipKey(result[j]) })
	return result
}

func bindingsByController(values []graph.LifecycleBinding) map[asset.AssetID][]graph.LifecycleBinding {
	result := make(map[asset.AssetID][]graph.LifecycleBinding)
	for _, value := range values {
		result[value.ControllerAssetID] = append(result[value.ControllerAssetID], value)
	}
	return result
}

func indexProtections(values []ProtectionPolicy) map[asset.AssetID]ProtectionPolicy {
	result := make(map[asset.AssetID]ProtectionPolicy, len(values))
	for _, value := range values {
		if value.Protected {
			result[value.AssetID] = value
		}
	}
	return result
}

func uniqueAssetIDs(values []asset.AssetID) []asset.AssetID {
	seen := make(map[asset.AssetID]struct{}, len(values))
	result := make([]asset.AssetID, 0, len(values))
	for _, value := range values {
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

func mapKeys(values map[asset.AssetID]struct{}) []asset.AssetID {
	result := make([]asset.AssetID, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool { return result[i] < result[j] })
	return result
}

type solveIDs struct {
	steps   map[asset.AssetID]StepID
	impacts map[string]ImpactItemID
}

func newSolveIDs() *solveIDs {
	return &solveIDs{steps: make(map[asset.AssetID]StepID), impacts: make(map[string]ImpactItemID)}
}

func (g *solveIDs) step(assetID asset.AssetID) StepID {
	if id := g.steps[assetID]; id != "" {
		return id
	}
	id := StepID(idgen.MustNew("stp"))
	g.steps[assetID] = id
	return id
}

func (g *solveIDs) impact(root, controller, managed asset.AssetID) ImpactItemID {
	key := strings.Join([]string{string(root), string(controller), string(managed)}, "\x00")
	if id := g.impacts[key]; id != "" {
		return id
	}
	id := ImpactItemID(idgen.MustNew("imp"))
	g.impacts[key] = id
	return id
}

func addDependency(values map[asset.AssetID]map[asset.AssetID]struct{}, step, dependency asset.AssetID) {
	if step == dependency {
		return
	}
	if values[step] == nil {
		values[step] = make(map[asset.AssetID]struct{})
	}
	values[step][dependency] = struct{}{}
}

func ordersDeletion(relationshipType graph.RelationshipType) bool {
	switch relationshipType {
	case graph.RelationshipDependsOn, graph.RelationshipAttachedTo, graph.RelationshipMemberOf, graph.RelationshipUses, graph.RelationshipRoutesTo, graph.RelationshipCreatedFrom:
		return true
	default:
		return false
	}
}

func topologicalSteps(steps map[asset.AssetID]CleanupTaskStep, dependencies map[asset.AssetID]map[asset.AssetID]struct{}) ([]CleanupTaskStep, bool) {
	indegree := make(map[asset.AssetID]int, len(steps))
	dependents := make(map[asset.AssetID][]asset.AssetID)
	for id := range steps {
		indegree[id] = 0
	}
	for stepID, values := range dependencies {
		if _, exists := steps[stepID]; !exists {
			continue
		}
		for dependencyID := range values {
			if _, exists := steps[dependencyID]; !exists {
				continue
			}
			indegree[stepID]++
			dependents[dependencyID] = append(dependents[dependencyID], stepID)
		}
	}
	ready := make([]asset.AssetID, 0, len(steps))
	for id, degree := range indegree {
		if degree == 0 {
			ready = append(ready, id)
		}
	}
	sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
	result := make([]CleanupTaskStep, 0, len(steps))
	for len(ready) > 0 {
		id := ready[0]
		ready = ready[1:]
		result = append(result, steps[id])
		for _, dependent := range dependents[id] {
			indegree[dependent]--
			if indegree[dependent] == 0 {
				ready = append(ready, dependent)
				sort.Slice(ready, func(i, j int) bool { return ready[i] < ready[j] })
			}
		}
	}
	return result, len(result) == len(steps)
}

func stableSteps(values map[asset.AssetID]CleanupTaskStep) []CleanupTaskStep {
	ids := make([]asset.AssetID, 0, len(values))
	for id := range values {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	result := make([]CleanupTaskStep, 0, len(ids))
	for _, id := range ids {
		result = append(result, values[id])
	}
	return result
}

func stableImpacts(values map[string]ImpactItem) []ImpactItem {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]ImpactItem, 0, len(keys))
	for _, key := range keys {
		result = append(result, values[key])
	}
	return result
}

func snapshotHash(input Input, selected []asset.AssetID, assets map[asset.AssetID]asset.Asset, relationships []graph.Relationship, bindings []graph.LifecycleBinding) (string, error) {
	assetValues := make([]asset.Asset, 0, len(assets))
	for _, value := range assets {
		// Dirty is an operator quality annotation, not cloud inventory state.
		// It is evaluated immediately before execution and must not invalidate
		// an otherwise unchanged cleanup snapshot.
		value.Dirty = false
		assetValues = append(assetValues, value)
	}
	sort.Slice(assetValues, func(i, j int) bool { return assetValues[i].ID < assetValues[j].ID })
	protections := append([]ProtectionPolicy(nil), input.Protections...)
	sort.Slice(protections, func(i, j int) bool {
		if protections[i].AssetID != protections[j].AssetID {
			return protections[i].AssetID < protections[j].AssetID
		}
		return protections[i].Source < protections[j].Source
	})
	payload, err := json.Marshal(struct {
		Selected      []asset.AssetID                  `json:"selected"`
		Assets        []asset.Asset                    `json:"assets"`
		Relationships []graph.Relationship             `json:"relationships"`
		Bindings      []graph.LifecycleBinding         `json:"bindings"`
		Protections   []ProtectionPolicy               `json:"protections"`
		Revision      RevisionBinding                  `json:"revision"`
		Options       map[asset.AssetID]map[string]any `json:"options"`
		Selectors     []CleanupSelector                `json:"selectors"`
		Coverage      ScanCoverage                     `json:"coverage"`
	}{selected, assetValues, relationships, bindings, protections, input.Revision, input.RequestOptions, input.Selectors, input.Coverage})
	if err != nil {
		return "", fmt.Errorf("hash cleanup planning snapshot: %w", err)
	}
	digest := sha256.Sum256(payload)
	return hex.EncodeToString(digest[:]), nil
}

func bindingKey(value graph.LifecycleBinding) string {
	return strings.Join([]string{string(value.ControllerAssetID), string(value.ManagedAssetID), string(value.Ownership), string(value.CleanupPolicy), value.EvidenceSource, string(value.ID)}, "\x00")
}

func relationshipKey(value graph.Relationship) string {
	return strings.Join([]string{string(value.SourceAssetID), string(value.TargetAssetID), string(value.Type), value.Source, string(value.ID)}, "\x00")
}

func cloneMap(value map[string]any) map[string]any {
	if len(value) == 0 {
		return map[string]any{}
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return map[string]any{}
	}
	var clone map[string]any
	if err := json.Unmarshal(payload, &clone); err != nil {
		return map[string]any{}
	}
	return clone
}

type blockerSet struct {
	valuesByKey map[string]Blocker
}

func newBlockerSet() *blockerSet {
	return &blockerSet{valuesByKey: make(map[string]Blocker)}
}

func (s *blockerSet) add(value Blocker) {
	key := string(value.Code) + "\x00" + string(value.AssetID) + "\x00" + string(value.ControllerID)
	if _, exists := s.valuesByKey[key]; !exists {
		s.valuesByKey[key] = value
	}
}

func (s *blockerSet) values() []Blocker {
	keys := make([]string, 0, len(s.valuesByKey))
	for key := range s.valuesByKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]Blocker, 0, len(keys))
	for _, key := range keys {
		result = append(result, s.valuesByKey[key])
	}
	return result
}
