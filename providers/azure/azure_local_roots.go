package azure

import (
	"context"
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
		if err := a.client.azureLocalRootConsumerRecord(vm, a.planned.Identity.NativeType); err != nil {
			return err
		}
		matched, err := a.client.azureLocalRootConsumerReference(a.planned, vm)
		if err != nil || !matched || vm.Normalized["cleanup_protected"] != false {
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
	if value.Identity.NativeType == azureLocalNetworkType {
		consumers, err := a.client.azureLocalNetworkConsumers(ctx, value, raw)
		return raw, consumers, err
	}
	consumers, err := a.client.azureLocalVMConsumers(ctx, value.Identity.NativeID, value.Identity.NativeType, stringValues(object(value.Normalized[azureLocalCleanup])["vms"]))
	if err == nil && value.Identity.NativeType == azureLocalStorageType {
		var roots []string
		roots, err = a.client.azureLocalStorageConsumers(ctx, value.Identity.NativeID, object(value.Normalized[azureLocalCleanup])["resources"])
		consumers = append(consumers, roots...)
	}
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
		reason := protectionReason(resourceType{NativeType: a.planned.Identity.NativeType}, raw)
		if a.planned.Identity.NativeType == azureLocalNetworkType {
			reason = azureLocalNetworkProtection(raw)
		}
		if reason != "" {
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
		if value.Identity.NativeType == azureLocalNetworkType && value.Normalized["cleanup_protected"] == true {
			continue
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
			_, typ, _ := parseID(id)
			kind := azureLocalKind(typ)
			if strings.EqualFold(typ, azureLocalAKSType) {
				kind = azureLocalAKSType
			}
			evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "resource_type": kind, "instance_id": id}
			if !found {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: value.Identity.Provider, ConnectionID: value.Identity.ConnectionID, NativeType: kind, NativeID: id, ControllerID: value.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
				continue
			}
			if err := s.client.azureLocalRootConsumerRecord(vm, value.Identity.NativeType); err != nil {
				return err
			}
			matched, err := s.client.azureLocalRootConsumerReference(value, vm)
			if err != nil || !matched {
				return serviceDenied("azure_local_root_consumer_changed")
			}
			refs, err := s.client.azureLocalRecordedReferences(vm)
			if err != nil {
				return err
			}
			if value.Identity.NativeType == azureLocalStorageType && len(refs[azureLocalStorageType]) == 0 {
				evidence["storage_placement_unverified"] = true
			}
			if value.Identity.NativeType == azureLocalDiskType && object(vm.Normalized[azureLocalCleanup])["os_disk"] == value.Identity.NativeID {
				continue
			}
			if value.Identity.NativeType == azureLocalStorageType && kind == azureLocalDiskType {
				controllers := map[string]any{}
				for _, candidate := range selected {
					if candidate.Identity.NativeType == azureLocalVMType && object(candidate.Normalized[azureLocalCleanup])["os_disk"] == id {
						if err := s.client.azureLocalVMRecord(candidate); err != nil {
							return err
						}
						controllers[string(candidate.ID)] = true
					}
				}
				if len(controllers) != 0 {
					evidence[graph.RelationshipEvidenceDeletionCascadeControllers] = controllers
				}
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: value.ID, TargetAssetID: vm.ID, Type: graph.RelationshipDependsOn, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
		}
	}
	return nil
}
