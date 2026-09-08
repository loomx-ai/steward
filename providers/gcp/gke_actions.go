package gcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *action) isGKE() bool {
	return a.kind.NativeType == clusterType || a.kind.NativeType == nodePoolType
}

func (c *client) gkeOwnsGroup(ctx context.Context, id string) (bool, error) {
	metadata, err := providerData()
	if err != nil {
		return false, err
	}
	op, _ := metadata.catalog.Operation("container.projects.locations.clusters.list")
	clusters, err := c.nativeList(ctx, op, map[string]any{"parent": "projects/" + c.project + "/locations/-"}, "clusters")
	if err != nil {
		return false, err
	}
	for _, cluster := range clusters {
		pools, err := productRecords(cluster, "nodePools")
		if err != nil {
			return false, err
		}
		for _, pool := range pools {
			urls, ok := pool.Data["instanceGroupUrls"].([]any)
			if pool.Data["instanceGroupUrls"] != nil && !ok {
				return false, fmt.Errorf("invalid GKE managed group list")
			}
			for _, raw := range urls {
				member, err := c.computeID(text(raw), managerType)
				if err != nil {
					return false, err
				}
				if member == id {
					return true, nil
				}
			}
		}
	}
	return false, nil
}

func gkeProtected(data map[string]any) bool {
	return protectedComputeLabels(data) || protectedComputeLabels(map[string]any{"labels": data["resourceLabels"]}) || protectedComputeLabels(map[string]any{"labels": object(data["config"])["resourceLabels"]})
}

func (a *action) plannedGKE(ctx context.Context, request contracts.ActionRequest, live map[string]any) (string, error) {
	if gkeProtected(live) {
		return "gke_controller_protected", nil
	}
	if a.kind.NativeType == clusterType {
		if text(live["id"]) == "" || text(live["id"]) != text(request.Asset.Normalized["id"]) {
			return "gke_cluster_identity_changed", nil
		}
		// The cluster API also cleans up its network resources. Keep this action
		// unavailable until those impacts are represented and independently read.
		return "gke_network_impact_verification_required", nil
	}
	if text(live["name"]) != last(request.Asset.Identity.NativeID) || text(live["etag"]) == "" || text(live["etag"]) != text(request.Asset.Normalized["etag"]) {
		return "gke_node_pool_changed", nil
	}
	cluster, err := a.client.nativeGet(ctx, clusterType, strings.Split(request.Asset.Identity.NativeID, "/nodePools/")[0])
	if err != nil {
		return "", err
	}
	if text(cluster["id"]) == "" || text(cluster["id"]) != text(request.Asset.Normalized["_gke_cluster_uid"]) {
		return "gke_cluster_identity_changed", nil
	}
	members, err := a.client.gkeMembers(ctx, request.Asset, live)
	if err != nil {
		return "", err
	}
	impacts, err := groupImpacts(request)
	if err != nil {
		return "", err
	}
	controllers := map[string]asset.AssetID{request.Asset.Identity.NativeID: request.Asset.ID}
	present := map[groupImpactKey]bool{}
	for _, member := range members {
		key := groupImpactKey{controllers[member.parent], member.id}
		impact, exists := impacts[key]
		if !exists || key.controller == "" || impact.Asset.Identity.NativeType != member.kind {
			return "gke_member_missing_from_plan", nil
		}
		if impact.Delete != member.deletes {
			return "gke_member_deletion_policy_changed", nil
		}
		identityField := "id"
		if member.kind == nodePoolType {
			identityField = "etag"
		}
		if text(member.data[identityField]) == "" || text(member.data[identityField]) != text(impact.Asset.Normalized[identityField]) {
			return "gke_member_identity_changed", nil
		}
		if member.deletes && (gkeProtected(member.data) || protectionReason(member.kind, member.data) != "" || member.data["deletionProtection"] == true) {
			return "gke_member_protected", nil
		}
		controllers[member.id] = impact.Asset.ID
		present[key] = true
	}
	if len(present) != len(impacts) {
		return "gke_members_changed", nil
	}
	return "", nil
}

func (a *action) gkeReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	impacts, err := groupImpacts(request)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	for _, impact := range impacts {
		if !impact.Delete {
			continue
		}
		_, err := a.client.nativeGet(ctx, impact.Asset.Identity.NativeType, impact.Asset.Identity.NativeID)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		return contracts.ReadbackResult{Exists: true, State: "waiting_for_gke_members"}, nil
	}
	return contracts.ReadbackResult{}, nil
}

func (a *action) gkeOperationURL(data map[string]any) (string, error) {
	name := text(data["name"])
	if !segmentPattern.MatchString(name) || name == "." || name == ".." {
		return "", fmt.Errorf("GKE omitted a valid operation name")
	}
	resource := strings.TrimPrefix(a.endpoint, "https://container.googleapis.com/v1/")
	parts := strings.Split(resource, "/")
	if len(parts) < 6 || parts[0] != "projects" || parts[2] != "locations" {
		return "", fmt.Errorf("invalid GKE operation scope")
	}
	if location := text(data["location"]); location != "" && location != parts[3] {
		return "", fmt.Errorf("GKE operation belongs to another location")
	}
	if target := text(data["targetLink"]); target != "" && a.client.canonicalName(target) != a.client.canonicalName(a.endpoint) {
		return "", fmt.Errorf("GKE operation targets another resource")
	}
	metadata, err := providerData()
	if err != nil {
		return "", err
	}
	op, ok := metadata.catalog.Operation("container.projects.locations.operations.get")
	if !ok {
		return "", fmt.Errorf("GKE operation has no native polling method")
	}
	bound, err := catalog.BindREST(op, map[string]any{"name": strings.Join(parts[:4], "/") + "/operations/" + name})
	if err != nil {
		return "", err
	}
	if self := text(data["selfLink"]); self != "" && a.client.canonicalName(self) != a.client.canonicalName(bound.URL) {
		return "", fmt.Errorf("GKE operation identity mismatch")
	}
	return bound.URL, nil
}
