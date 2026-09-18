package gcp

import (
	"context"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// A direct driver's Preflight may allow a preparation phase (for example,
// changing disk autoDelete) before its DELETE. the deployment owns this deletion, so
// that permission alone cannot authorize skipping the preparation. Its current
// native cascade must already match every reviewed descendant policy.
func (a *action) infraNativeDeleteReady(ctx context.Context, request contracts.ActionRequest) error {
	switch a.kind.NativeType {
	case instanceType, managerType, clusterType, tpuNodeType:
	default:
		return nil
	}
	live, err := a.client.infraPhysicalRead(ctx, a.kind.NativeType, a.identity.NativeID)
	if err != nil {
		return err
	}
	switch a.kind.NativeType {
	case instanceType:
		retained, reason, err := a.plannedDisks(request, live)
		if err != nil {
			return err
		}
		if reason != "" || len(retained) != 0 || live["deletionProtection"] == true {
			return groupDenied("infra_vm_preparation_required")
		}
	case managerType:
		group, reason, err := a.plannedGroup(ctx, request, live)
		if err != nil {
			return err
		}
		if reason != "" {
			return groupDenied(reason)
		}
		impacts, err := groupImpacts(request)
		if err != nil {
			return err
		}
		for _, node := range group.nodes {
			vm := impacts[groupImpactKey{request.Asset.ID, node.id}]
			if !vm.Delete || node.data["deletionProtection"] == true {
				return groupDenied("infra_group_preparation_required")
			}
			for _, resource := range node.resources {
				if impacts[groupImpactKey{vm.Asset.ID, resource.id}].Delete != resource.delete {
					return groupDenied("infra_group_preparation_required")
				}
			}
		}
	case clusterType:
		snapshot, err := a.currentGKENetwork(ctx, request, live)
		if err != nil {
			return err
		}
		if len(snapshot.Workloads) != 0 {
			return groupDenied("infra_gke_finalizers_required")
		}
		for _, resource := range snapshot.Resources {
			if resource.Delete && resource.Phase != "cluster" {
				_, err := a.client.nativeGet(ctx, resource.Kind, resource.ID)
				if isNotFound(err) {
					continue
				}
				if err != nil {
					return err
				}
				return groupDenied("infra_gke_finalizers_required")
			}
		}
	case tpuNodeType:
		disks, err := a.client.tpuAttachments(live)
		if err != nil {
			return err
		}
		stage, err := tpuStage(tpuNodeType, live)
		if err != nil {
			return err
		}
		if len(disks) != 0 || stage != "ready" {
			return groupDenied("infra_tpu_preparation_required")
		}
	}
	return nil
}
