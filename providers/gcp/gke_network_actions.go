package gcp

import (
	"context"
	"fmt"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func validateGKEWorkloads(snapshot gkeNetworkSnapshot, systemUID string, live []gkeWorkload) error {
	if systemUID != snapshot.SystemUID {
		return groupDenied("gke_kubernetes_identity_changed")
	}
	planned := map[string]gkeWorkload{}
	for _, workload := range snapshot.Workloads {
		if _, exists := planned[workload.key()]; exists || workload.UID == "" || workload.Hash == "" {
			return groupDenied("gke_workload_plan_invalid")
		}
		planned[workload.key()] = workload
	}
	for _, workload := range live {
		original, exists := planned[workload.key()]
		if !exists || workload.API != original.API || workload.UID != original.UID || workload.Hash != original.Hash {
			return groupDenied("gke_workload_changed")
		}
		if protectedComputeLabels(map[string]any{"labels": object(workload.data["metadata"])["labels"]}) {
			return groupDenied("gke_workload_protected")
		}
	}
	return nil
}

func (a *action) gkeNetworkPreflight(ctx context.Context, request contracts.ActionRequest, live map[string]any) error {
	_, err := a.currentGKENetwork(ctx, request, live)
	return err
}

func (a *action) currentGKENetwork(ctx context.Context, request contracts.ActionRequest, live map[string]any) (gkeNetworkSnapshot, error) {
	planned, err := plannedGKENetwork(request.Asset)
	if err != nil {
		return gkeNetworkSnapshot{}, err
	}
	nodes, err := a.client.gkeMembers(ctx, request.Asset, live)
	if err != nil {
		return gkeNetworkSnapshot{}, err
	}
	current, err := a.client.gkeNetwork(ctx, request.Asset, live, nodes)
	if err != nil {
		return current, err
	}
	if err := validateGKEWorkloads(planned, current.SystemUID, current.Workloads); err != nil {
		return current, err
	}
	resources := map[string]gkeNetworkResource{}
	for _, resource := range planned.Resources {
		resources[resource.ID] = resource
	}
	for _, resource := range current.Resources {
		original, exists := resources[resource.ID]
		if !exists || original.Kind != resource.Kind || original.UID != resource.UID || original.Delete != resource.Delete || original.Phase != resource.Phase {
			return current, groupDenied("gke_network_resources_changed")
		}
	}
	allPresent := len(current.Workloads) == len(planned.Workloads)
	for _, workload := range current.Workloads {
		allPresent = allPresent && text(object(workload.data["metadata"])["deletionTimestamp"]) == ""
	}
	if allPresent && len(current.Resources) != len(planned.Resources) {
		return current, groupDenied("gke_network_resources_changed")
	}
	return current, nil
}

func gkePhase(phase string) contracts.ActionResult {
	return contracts.ActionResult{Data: map[string]any{"phase": phase}, RetryAfter: 2 * time.Second}
}

func (a *action) prepareGKENetwork(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	snapshot, err := plannedGKENetwork(request.Asset)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	live, err := a.client.nativeGet(ctx, clusterType, request.Asset.Identity.NativeID)
	if isNotFound(err) {
		return gkePhase("gke_delete"), nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if reason, err := a.plannedGKE(ctx, request, live); err != nil {
		return contracts.ActionResult{}, err
	} else if reason != "" {
		return contracts.ActionResult{}, groupDenied(reason)
	}
	current, err := a.currentGKENetwork(ctx, request, live)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	k, err := a.client.kubernetes(ctx, request.Asset.Identity.NativeID, snapshot.ClusterUID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	defer k.http.CloseIdleConnections()
	workloads := current.Workloads
	if len(workloads) > 0 {
		for _, workload := range workloads {
			if workload.Kind != workloads[0].Kind {
				break
			}
			if text(object(workload.data["metadata"])["deletionTimestamp"]) != "" {
				continue
			}
			// The current resourceVersion closes the read/delete race. Normal
			// finalizers are left intact so the live controller performs cleanup.
			response, err := k.delete(ctx, workload.collection(), workload.data)
			if isNotFound(err) {
				return gkePhase("gke_network"), nil
			}
			if err != nil {
				return contracts.ActionResult{}, err
			}
			result := gkePhase("gke_network")
			result.ProviderRequestID = response.RequestID
			result.Data["request_id"] = response.RequestID
			return result, nil
		}
		return gkePhase("gke_network"), nil
	}
	if result, pending, err := a.cleanupGKENetwork(ctx, request, false); err != nil || pending {
		return result, err
	}
	// Recheck the cluster and all node/member protection immediately before its
	// native deletion. Workloads and load-balancer finalizers have now finished.
	live, err = a.client.nativeGet(ctx, clusterType, request.Asset.Identity.NativeID)
	if isNotFound(err) {
		return gkePhase("gke_delete"), nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if reason, err := a.plannedGKE(ctx, request, live); err != nil {
		return contracts.ActionResult{}, err
	} else if reason != "" {
		return contracts.ActionResult{}, groupDenied(reason)
	}
	result, err := a.delete(ctx, request)
	if err != nil {
		return result, err
	}
	if result.Data == nil {
		result.Data = map[string]any{}
	}
	result.Data["phase"] = "gke_delete"
	result.Data["operation"] = result.ProviderOperationID
	return result, nil
}

func (a *action) networkMemberAction(request contracts.ActionRequest, resource gkeNetworkResource) (*action, contracts.ActionRequest, error) {
	impacts, err := groupImpacts(request)
	if err != nil {
		return nil, contracts.ActionRequest{}, err
	}
	impact, exists := impacts[groupImpactKey{request.Asset.ID, resource.ID}]
	if !exists || !impact.Delete || !resource.Delete || impact.Asset.Identity.NativeType != resource.Kind || text(impact.Asset.Normalized["id"]) != resource.UID {
		return nil, contracts.ActionRequest{}, groupDenied("gke_network_cleanup_missing_from_plan")
	}
	kind, ok := findType(resource.Kind)
	if !ok || !gkeNetworkKind(resource.Kind) {
		return nil, contracts.ActionRequest{}, groupDenied("gke_network_cleanup_kind_invalid")
	}
	endpoint, err := a.client.resourceURL(kind, resource.ID)
	if err != nil {
		return nil, contracts.ActionRequest{}, err
	}
	op, params, err := a.client.resourceOperation(kind, resource.ID, "DELETE")
	if err != nil {
		return nil, contracts.ActionRequest{}, err
	}
	return &action{client: a.client, kind: kind, endpoint: endpoint, deleteOperation: op, deleteParameters: params}, contracts.ActionRequest{Asset: impact.Asset, Action: "delete", IdempotencyKey: request.IdempotencyKey + ":gke-network:" + resource.UID}, nil
}

// Controllers can finish their Kubernetes deletion while a Compute resource is
// left behind. Only frozen, explicitly planned members can be recovered here.
// Native references order the cleanup; no prefix search authorizes a mutation.
func (a *action) cleanupGKENetwork(ctx context.Context, request contracts.ActionRequest, afterCluster bool) (contracts.ActionResult, bool, error) {
	snapshot, err := plannedGKENetwork(request.Asset)
	if err != nil {
		return contracts.ActionResult{}, false, err
	}
	live := map[string]gkeNetworkResource{}
	for _, resource := range snapshot.Resources {
		if !resource.Delete || (!afterCluster && resource.Phase != "workload") {
			continue
		}
		data, err := a.client.nativeGet(ctx, resource.Kind, resource.ID)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return contracts.ActionResult{}, false, err
		}
		if text(data["id"]) != resource.UID {
			return contracts.ActionResult{}, false, groupDenied("gke_network_resource_identity_changed")
		}
		if gkeProtected(data) || protectionReason(resource.Kind, data) != "" {
			return contracts.ActionResult{}, false, groupDenied("gke_network_resource_protected")
		}
		resource.data = data
		live[resource.ID] = resource
	}
	if len(live) == 0 {
		return contracts.ActionResult{}, false, nil
	}
	referenced := map[string]bool{}
	for _, resource := range live {
		for _, ids := range references(a.client, resource.data) {
			for _, id := range ids {
				referenced[id] = true
			}
		}
		if resource.Kind == "compute.googleapis.com/Address" || resource.Kind == "compute.googleapis.com/GlobalAddress" {
			for _, raw := range array(resource.data["users"]) {
				if _, exists := live[a.client.canonicalName(text(raw))]; exists {
					referenced[resource.ID] = true
				}
			}
		}
	}
	for _, resource := range snapshot.Resources {
		if _, exists := live[resource.ID]; !exists || referenced[resource.ID] {
			continue
		}
		driver, child, err := a.networkMemberAction(request, resource)
		if err != nil {
			return contracts.ActionResult{}, false, err
		}
		result, err := driver.Execute(ctx, child)
		if err != nil {
			return result, false, err
		}
		if result.Data == nil {
			result.Data = map[string]any{}
		}
		result.Data["phase"] = "gke_network_cleanup"
		result.Data["resource"] = resource.ID
		result.Data["after_cluster"] = afterCluster
		result.Data["operation"] = result.ProviderOperationID
		return result, true, nil
	}
	return contracts.ActionResult{}, false, groupDenied("gke_network_dependency_cycle")
}

func (a *action) waitGKENetwork(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	phase := text(result.Data["phase"])
	afterCluster := false
	switch phase {
	case "gke_network":
	case "gke_network_cleanup":
		snapshot, err := plannedGKENetwork(request.Asset)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		var member *gkeNetworkResource
		for _, resource := range snapshot.Resources {
			if resource.ID == text(result.Data["resource"]) {
				copy := resource
				member = &copy
			}
		}
		if member == nil {
			return contracts.WaitResult{}, groupDenied("gke_network_cleanup_missing_from_plan")
		}
		driver, child, err := a.networkMemberAction(request, *member)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		wait, err := driver.Wait(ctx, child, contracts.ActionResult{ProviderOperationID: text(result.Data["operation"])})
		if err != nil || !wait.Done {
			return wait, err
		}
		afterCluster = result.Data["after_cluster"] == true
	case "gke_delete":
		wait, err := a.waitOperation(ctx, text(result.Data["operation"]))
		if err != nil || !wait.Done {
			return wait, err
		}
		_, err = a.client.nativeGet(ctx, clusterType, request.Asset.Identity.NativeID)
		if err == nil {
			return contracts.WaitResult{State: "deleting_cluster", RetryAfter: 2 * time.Second}, nil
		}
		if !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		afterCluster = true
	default:
		return contracts.WaitResult{}, fmt.Errorf("invalid GKE cleanup phase")
	}
	var next contracts.ActionResult
	var err error
	if afterCluster {
		cluster, readErr := a.client.nativeGet(ctx, clusterType, request.Asset.Identity.NativeID)
		if readErr == nil {
			if text(cluster["id"]) != text(request.Asset.Normalized["id"]) {
				return contracts.WaitResult{}, groupDenied("gke_cluster_identity_changed")
			}
			return contracts.WaitResult{State: "deleting_cluster", RetryAfter: 2 * time.Second}, nil
		}
		if !isNotFound(readErr) {
			return contracts.WaitResult{}, readErr
		}
		// Wait for native node deletion before recovering cluster network rules.
		snapshot, snapshotErr := plannedGKENetwork(request.Asset)
		if snapshotErr != nil {
			return contracts.WaitResult{}, snapshotErr
		}
		networkIDs := map[string]bool{}
		for _, resource := range snapshot.Resources {
			networkIDs[resource.ID] = true
		}
		for _, impact := range request.LifecycleImpacts {
			if !impact.Delete || networkIDs[impact.Asset.Identity.NativeID] {
				continue
			}
			_, readErr := a.client.nativeGet(ctx, impact.Asset.Identity.NativeType, impact.Asset.Identity.NativeID)
			if readErr == nil {
				return contracts.WaitResult{State: "waiting_for_gke_nodes", RetryAfter: 2 * time.Second}, nil
			}
			if !isNotFound(readErr) {
				return contracts.WaitResult{}, readErr
			}
		}
		var pending bool
		next, pending, err = a.cleanupGKENetwork(ctx, request, true)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		if !pending {
			read, err := a.gkeReadback(ctx, request)
			return contracts.WaitResult{Done: err == nil && !read.Exists, State: read.State, RetryAfter: 2 * time.Second}, err
		}
	} else {
		next, err = a.prepareGKENetwork(ctx, request)
	}
	if err != nil {
		return contracts.WaitResult{}, err
	}
	return contracts.WaitResult{Data: next.Data, State: text(next.Data["phase"]), RetryAfter: 2 * time.Second}, nil
}
