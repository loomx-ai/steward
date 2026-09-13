package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *azureLocalAction) rootRequest(request contracts.ActionRequest) error {
	if len(request.LifecycleImpacts) != 0 || azureLocalImage(a.planned.Identity.NativeType) && len(request.PrerequisiteDeletions) != 0 {
		return serviceDenied("azure_local_root_has_no_cascade")
	}
	seen := map[asset.AssetID]bool{}
	native := map[string]bool{}
	for _, prerequisite := range request.PrerequisiteDeletions {
		vm := prerequisite.Asset
		if !prerequisite.Delete || prerequisite.ControllerID != a.planned.ID || seen[vm.ID] || native[vm.Identity.NativeID] || vm.ID == a.planned.ID || vm.Identity.ConnectionID != a.planned.Identity.ConnectionID || vm.Identity.Partition != a.planned.Identity.Partition {
			return serviceDenied("invalid_azure_local_root_prerequisite")
		}
		if err := a.client.azureLocalVMRecord(vm); err != nil {
			return err
		}
		refs, err := a.client.azureLocalRecordedReferences(vm)
		if err != nil || !slices.Contains(refs[a.planned.Identity.NativeType], a.planned.Identity.NativeID) || vm.Normalized["cleanup_protected"] != false {
			return serviceDenied("azure_local_root_prerequisite_changed")
		}
		seen[vm.ID], native[vm.Identity.NativeID] = true, true
	}
	return nil
}

func (a *azureLocalAction) rootObserve(ctx context.Context) (map[string]any, []string, error) {
	value := a.planned
	res, err := a.client.azureLocalRead(ctx, value.Identity.NativeID, value.Identity.NativeType)
	if err != nil && !isNotFound(err) {
		return nil, nil, err
	}
	var raw map[string]any
	if err == nil {
		raw = res.data
		if resourceRegion(raw) != value.Location || a.client.privateConfiguration(azureLocalCleanupSnapshot(raw)) != object(value.Normalized[azureLocalCleanup])["resource"] {
			return raw, nil, serviceDenied("azure_local_root_configuration_changed")
		}
	}
	// VM creation copies the source image. Deleting an image does not require
	// deleting deployed VMs or reading their Arc registrations (Azure Local FAQ).
	if azureLocalImage(value.Identity.NativeType) {
		return raw, nil, nil
	}
	consumers, err := a.client.azureLocalVMConsumers(ctx, value.Identity.NativeID, value.Identity.NativeType, stringValues(object(value.Normalized[azureLocalCleanup])["vms"]))
	return raw, consumers, err
}

func (a *azureLocalAction) rootPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	for range 2 {
		raw, consumers, err := a.rootObserve(ctx)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if raw == nil || strings.EqualFold(text(object(raw["properties"])["provisioningState"]), "Deleting") {
			return contracts.PreflightResult{Allowed: true, Absent: raw == nil && len(consumers) == 0, Evidence: map[string]any{"azure_local_wait": true}}, nil
		}
		if len(consumers) != 0 {
			return contracts.PreflightResult{}, serviceDenied("azure_local_resource_still_in_use")
		}
		if reason := protectionReason(resourceType{NativeType: a.planned.Identity.NativeType}, raw); reason != "" {
			return contracts.PreflightResult{}, serviceDenied(reason)
		}
		if len(request.PrerequisiteDeletions) == 0 && a.client.privateConfiguration(map[string]any{"etag": raw["etag"], "eTag": raw["eTag"]}) != object(request.Asset.Normalized[azureLocalCleanup])["etag"] {
			return contracts.PreflightResult{}, serviceDenied("azure_local_root_etag_changed")
		}
		if err := a.resourceProtection(ctx, nil); err != nil {
			return contracts.PreflightResult{}, err
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (s *serviceCascades) contributeAzureLocalRoots(ctx context.Context, values []asset.Asset, result *governance.Contribution) error {
	selected := map[string]asset.Asset{}
	seen := map[asset.AssetID]bool{}
	for _, value := range values {
		if value.Identity.Provider != asset.ProviderAzure || (!azureLocalIndependent(value.Identity.NativeType) && value.Identity.NativeType != azureLocalVMType) {
			continue
		}
		if value.Identity.ConnectionID != s.connectionID || selected[value.Identity.NativeID].ID != "" || seen[value.ID] {
			return serviceDenied("ambiguous_azure_local_root_graph")
		}
		selected[value.Identity.NativeID], seen[value.ID] = value, true
	}
	for _, value := range values {
		if value.Identity.Provider != asset.ProviderAzure || !azureLocalIndependent(value.Identity.NativeType) {
			continue
		}
		// A legacy disk record remains readable as a VM-managed impact, but cannot
		// authorize independent deletion without the new native consumer inventory.
		if value.Identity.NativeType == azureLocalDiskType && len(object(value.Normalized[azureLocalCleanup])) == 5 {
			continue
		}
		if err := s.client.azureLocalRootRecord(value); err != nil {
			return err
		}
		a := &azureLocalAction{client: s.client, planned: value}
		before := ""
		var consumers []string
		for range 2 {
			raw, current, err := a.rootObserve(ctx)
			if err != nil {
				return err
			}
			fingerprint := s.client.privateConfiguration(map[string]any{"present": raw != nil, "consumers": current})
			if before != "" && before != fingerprint {
				return serviceDenied("azure_local_root_graph_changed")
			}
			before, consumers = fingerprint, current
		}
		for _, id := range consumers {
			vm, found := selected[id]
			evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "resource_type": azureLocalVMType, "instance_id": id}
			if !found {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: value.Identity.Provider, ConnectionID: value.Identity.ConnectionID, NativeType: azureLocalVMType, NativeID: id, ControllerID: value.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
				continue
			}
			if err := s.client.azureLocalVMRecord(vm); err != nil {
				return err
			}
			refs, err := s.client.azureLocalRecordedReferences(vm)
			if err != nil || !slices.Contains(refs[value.Identity.NativeType], value.Identity.NativeID) {
				return serviceDenied("azure_local_root_consumer_changed")
			}
			if value.Identity.NativeType == azureLocalDiskType && object(vm.Normalized[azureLocalCleanup])["os_disk"] == value.Identity.NativeID {
				continue
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: value.ID, TargetAssetID: vm.ID, Type: graph.RelationshipDependsOn, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
		}
	}
	return nil
}
