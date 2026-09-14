package gcp

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const instanceType = "compute.googleapis.com/Instance"
const diskAttachmentSource = "gcp:instance-disks"

type instanceDisk struct {
	id, kind, device string
	autoDelete       bool
}

// instanceDisks reads the attachment's authoritative deletion policy, including
// data disks and regional disks. Local SSDs have no independently managed disk.
func instanceDisks(c *client, data map[string]any) ([]instanceDisk, error) {
	var result []instanceDisk
	values, ok := data["disks"].([]any)
	if !ok && data["disks"] != nil {
		return nil, fmt.Errorf("invalid GCP instance disk attachments")
	}
	seen, devices := map[string]bool{}, map[string]bool{}
	for _, value := range values {
		disk := object(value)
		if text(disk["type"]) == "SCRATCH" {
			continue
		}
		id := c.canonicalName(text(disk["source"]))
		kind := "compute.googleapis.com/Disk"
		if strings.Contains(id, "/regions/") {
			kind = "compute.googleapis.com/RegionDisk"
		}
		rule, _ := findType(kind)
		if _, err := c.resourceURL(rule, id); err != nil {
			return nil, fmt.Errorf("invalid GCP attached disk: %w", err)
		}
		device := text(disk["deviceName"])
		if device == "" || !segmentPattern.MatchString(device) || seen[id] || devices[device] {
			return nil, fmt.Errorf("invalid or duplicate GCP disk attachment")
		}
		autoDelete, valid := disk["autoDelete"].(bool)
		if !valid && disk["autoDelete"] != nil {
			return nil, fmt.Errorf("invalid GCP disk autoDelete value")
		}
		seen[id], devices[device] = true, true
		result = append(result, instanceDisk{id: id, kind: kind, device: device, autoDelete: autoDelete})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].id < result[j].id })
	return result, nil
}

type InstanceDisks struct{}

func NewInstanceDisks() *InstanceDisks { return &InstanceDisks{} }

func (*InstanceDisks) Contribute(_ context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	for _, vm := range assets {
		if vm.Identity.Provider != asset.ProviderGCP || vm.Identity.NativeType != instanceType {
			continue
		}
		project := text(vm.Normalized["project_id"])
		c := &client{project: project, number: text(vm.Normalized["project_number"])}
		batchManaged := false
		if uid := batchUID(vm.Normalized); uid != "" {
			for _, job := range assets {
				if job.Identity.Provider == vm.Identity.Provider && job.Identity.ConnectionID == vm.Identity.ConnectionID && job.Identity.Partition == vm.Identity.Partition && job.Identity.NativeType == batchJobType && job.ClosedAt == nil && text(job.Normalized["uid"]) == uid {
					batchManaged = true
					break
				}
			}
		}
		disks, err := instanceDisks(c, vm.Normalized)
		if err != nil {
			return result, err
		}
		for _, attachment := range disks {
			evidence := map[string]any{"resource_type": attachment.kind, "instance_id": attachment.id, "device_name": attachment.device, "auto_delete": attachment.autoDelete, "lifecycle_kind": "gcp_disk_delete_with_instance"}
			var managed *asset.Asset
			for i := range assets {
				candidate := &assets[i]
				if candidate.Identity.Provider == vm.Identity.Provider && candidate.Identity.ConnectionID == vm.Identity.ConnectionID && candidate.Identity.Partition == vm.Identity.Partition && candidate.Identity.NativeType == attachment.kind && candidate.Identity.NativeID == attachment.id {
					if managed != nil {
						return result, fmt.Errorf("ambiguous GCP attached disk identity")
					}
					managed = candidate
				}
			}
			if managed == nil {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: vm.Identity.Provider, ConnectionID: vm.Identity.ConnectionID, NativeType: attachment.kind, NativeID: attachment.id, ControllerID: vm.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
				continue
			}
			if attachment.autoDelete {
				evidence["delete_by_default"] = true
				evidence[graph.LifecycleEvidenceControllerVerifiesManagedAbsence] = true
				if batchManaged {
					// Batch owns the VM lifecycle; only its Job DELETE is issued.
					evidence["retention_supported"] = false
				}
				evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] = true
				result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: vm.ID, ManagedAssetID: managed.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, EvidenceSource: diskAttachmentSource, Evidence: evidence, Confidence: 1})
			} else {
				evidence[graph.RelationshipEvidenceDeletionOrder] = graph.DeletionOrderTargetBeforeSource
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: managed.ID, TargetAssetID: vm.ID, Type: graph.RelationshipAttachedTo, Source: diskAttachmentSource, Evidence: evidence, Confidence: 1})
		}
	}
	return result, nil
}

func (a *action) plannedDisks(request contracts.ActionRequest, live map[string]any) ([]instanceDisk, string, error) {
	current, err := instanceDisks(a.client, live)
	if err != nil {
		return nil, "", err
	}
	planned, err := instanceDisks(a.client, request.Asset.Normalized)
	if err != nil {
		return nil, "", err
	}
	if len(current) != len(planned) {
		return nil, "attached_resources_changed", nil
	}
	var retain []instanceDisk
	for i, disk := range current {
		expected := planned[i]
		if disk.id != expected.id || disk.device != expected.device {
			return nil, "attached_resources_changed", nil
		}
		shouldDelete := expected.autoDelete
		if expected.autoDelete {
			found := false
			for _, impact := range request.LifecycleImpacts {
				identity := impact.Asset.Identity
				if impact.ControllerID == request.Asset.ID && identity.NativeID == disk.id && identity.NativeType == disk.kind && identity.Provider == asset.ProviderGCP && identity.ConnectionID == request.Asset.Identity.ConnectionID && identity.Partition == request.Asset.Identity.Partition {
					if found {
						return nil, "ambiguous_lifecycle_impact", nil
					}
					found, shouldDelete = true, impact.Delete
				}
			}
			if !found {
				return nil, "attached_resource_missing_from_plan", nil
			}
		}
		if disk.autoDelete != expected.autoDelete && (shouldDelete || disk.autoDelete) {
			return nil, "attached_resources_changed", nil
		}
		if disk.autoDelete && !shouldDelete {
			retain = append(retain, disk)
		}
	}
	return retain, "", nil
}

// Verify native disk outcomes even after the instance itself is absent. The
// frozen attachment list remains authoritative for a resumed deletion.
func (a *action) instanceDisksReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if _, reason, err := a.plannedDisks(request, request.Asset.Normalized); err != nil || reason != "" {
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		return contracts.ReadbackResult{}, groupDenied(reason)
	}
	return a.computeMembersReadback(ctx, request, "compute")
}
