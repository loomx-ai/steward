package gcp

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) dataprocJobPlacement(root asset.Asset, job asset.Asset) error {
	placement := object(job.Normalized["placement"])
	if job.Identity.NativeType != dataprocJobType || dataprocRegion(job.Identity.NativeID) != dataprocRegion(root.Identity.NativeID) || placement["clusterName"] != root.Normalized["clusterName"] || placement["clusterUuid"] != root.Normalized["clusterUuid"] || text(job.Normalized[dataprocProof]) == "" {
		return groupDenied("dataproc_job_scope_changed")
	}
	return c.dataprocIdentity(dataprocJobType, job.Identity.NativeID, job.Normalized)
}

func (c *client) dataprocManagerID(config, cluster map[string]any) (string, error) {
	managed := object(config["managedGroupConfig"])
	source := text(managed["instanceGroupManagerUri"])
	if source == "" {
		name := text(managed["instanceGroupManagerName"])
		zone := last(text(object(object(cluster["config"])["gceClusterConfig"])["zoneUri"]))
		if name == "" || zone == "" {
			return "", groupDenied("dataproc_manager_scope_missing")
		}
		source = "projects/" + c.project + "/zones/" + zone + "/instanceGroupManagers/" + name
	}
	return c.computeID(source, managerType)
}

// Validate reviewed native ancestry independently of live GETs. A missing parent
// must not turn a foreign or retained resource's 404 into successful cleanup.
func (a *action) dataprocPlan(request contracts.ActionRequest) (map[groupImpactKey]contracts.ActionImpact, error) {
	root := request.Asset
	endpoint, err := a.client.resourceURL(a.kind, root.Identity.NativeID)
	if err != nil || endpoint != a.endpoint || root.ID == "" || root.Identity.Provider != asset.ProviderGCP || root.Identity.NativeType != dataprocClusterType || text(root.Normalized[dataprocProof]) == "" {
		return nil, groupDenied("dataproc_plan_invalid")
	}
	if err := a.client.dataprocIdentity(dataprocClusterType, root.Identity.NativeID, root.Normalized); err != nil {
		return nil, err
	}
	impacts, err := groupImpacts(request)
	if err != nil {
		return nil, err
	}
	byID := map[asset.AssetID]asset.Asset{root.ID: root}
	for _, impact := range impacts {
		if prior, ok := byID[impact.Asset.ID]; ok && (prior.Identity != impact.Asset.Identity || dataprocComputeConfiguration(prior.Identity.NativeType, prior.Normalized) != dataprocComputeConfiguration(impact.Asset.Identity.NativeType, impact.Asset.Normalized)) {
			return nil, groupDenied("dataproc_impact_identity_conflict")
		}
		if impact.Asset.ID == root.ID {
			return nil, groupDenied("dataproc_impact_cycle")
		}
		byID[impact.Asset.ID] = impact.Asset
	}
	for _, impact := range impacts {
		parent, ok := byID[impact.ControllerID]
		if !ok {
			return nil, groupDenied("dataproc_impact_parent_missing")
		}
		child := impact.Asset
		kind := child.Identity.NativeType
		rule, found := findType(kind)
		if !found {
			return nil, groupDenied("dataproc_impact_kind_invalid")
		}
		if _, err := a.client.resourceURL(rule, child.Identity.NativeID); err != nil {
			return nil, err
		}
		if !isDataproc(kind) && (text(child.Normalized["id"]) == "" || a.client.canonicalName(text(child.Normalized["selfLink"])) != child.Identity.NativeID) {
			return nil, groupDenied("dataproc_compute_identity_invalid")
		}
		valid := false
		switch kind {
		case dataprocJobType:
			valid = parent.ID == root.ID && !impact.Delete && a.client.dataprocJobPlacement(root, child) == nil
		case dataprocNodeGroupType:
			nodes, err := a.client.dataprocNodeGroups(root.Identity.NativeID, root.Normalized)
			if err != nil {
				return nil, err
			}
			for _, node := range nodes {
				valid = valid || a.client.canonicalName("//dataproc.googleapis.com/"+text(node["name"])) == child.Identity.NativeID
			}
			valid = valid && parent.ID == root.ID && impact.Delete && child.Normalized[dataprocParentProof] == root.Normalized[dataprocProof] && child.Normalized["_dataproc_cluster_uuid"] == root.Normalized["clusterUuid"]
		case managerType:
			var configs []map[string]any
			if parent.ID == root.ID {
				for _, key := range []string{"masterConfig", "workerConfig", "secondaryWorkerConfig"} {
					configs = append(configs, object(object(root.Normalized["config"])[key]))
				}
			}
			if parent.Identity.NativeType == dataprocNodeGroupType {
				configs = append(configs, object(parent.Normalized["nodeGroupConfig"]))
			}
			for _, config := range configs {
				id, err := a.client.dataprocManagerID(config, root.Normalized)
				valid = valid || err == nil && id == child.Identity.NativeID
			}
			valid = valid && impact.Delete
		case instanceGroupType:
			id, err := a.client.computeID(text(parent.Normalized["instanceGroup"]), instanceGroupType)
			valid = parent.Identity.NativeType == managerType && err == nil && id == child.Identity.NativeID && impact.Delete
		case "compute.googleapis.com/InstanceTemplate":
			hints := dataprocMetadata(object(child.Normalized["properties"]))
			if parent.ID == root.ID && impact.Delete && hints["dataproc-cluster-uuid"] == text(root.Normalized["clusterUuid"]) && hints["dataproc-cluster-name"] == text(root.Normalized["clusterName"]) && hints["dataproc-region"] == dataprocRegion(root.Identity.NativeID) {
				for _, manager := range byID {
					if manager.Identity.NativeType == managerType {
						sources := []any{manager.Normalized["instanceTemplate"]}
						for _, raw := range array(manager.Normalized["versions"]) {
							sources = append(sources, object(raw)["instanceTemplate"])
						}
						for _, source := range sources {
							id, err := a.client.computeID(text(source), kind)
							valid = valid || err == nil && id == child.Identity.NativeID
						}
					}
				}
			}
		case instanceType:
			valid = impact.Delete && a.client.dataprocVMIdentity(root, root.Normalized, child.Normalized, child.Identity.NativeID) == nil && (parent.ID == root.ID || parent.Identity.NativeType == managerType || parent.Identity.NativeType == dataprocNodeGroupType)
		case "compute.googleapis.com/Disk", "compute.googleapis.com/RegionDisk":
			if parent.Identity.NativeType == instanceType {
				disks, err := instanceDisks(a.client, parent.Normalized)
				if err != nil {
					return nil, err
				}
				for _, disk := range disks {
					valid = valid || disk.id == child.Identity.NativeID && disk.kind == kind && disk.autoDelete == impact.Delete
				}
			} else if parent.ID == root.ID {
				valid = impact.Delete && object(child.Normalized["labels"])["goog-dataproc-cluster-uuid"] == root.Normalized["clusterUuid"] && len(array(child.Normalized["users"])) == 0
			}
		}
		if !valid {
			return nil, groupDenied("dataproc_impact_scope_or_policy_changed")
		}
		visited := map[asset.AssetID]bool{child.ID: true}
		current := impact.ControllerID
		for current != root.ID {
			if visited[current] {
				return nil, groupDenied("dataproc_impact_cycle")
			}
			visited[current] = true
			next := asset.AssetID("")
			for _, ancestor := range impacts {
				if ancestor.Asset.ID == current {
					if next != "" && next != ancestor.ControllerID {
						return nil, groupDenied("dataproc_impact_ambiguous_parent")
					}
					next = ancestor.ControllerID
				}
			}
			if next == "" {
				return nil, groupDenied("dataproc_impact_parent_missing")
			}
			current = next
		}
	}
	seen := map[string]bool{}
	for _, prior := range request.PrerequisiteDeletions {
		child := prior.Asset
		if !prior.Delete || prior.ControllerID != root.ID || child.ID == "" || byID[child.ID].ID != "" || seen[child.Identity.NativeID] || child.Identity.Provider != root.Identity.Provider || child.Identity.ConnectionID != root.Identity.ConnectionID || child.Identity.Partition != root.Identity.Partition {
			return nil, groupDenied("dataproc_prerequisite_invalid")
		}
		if err := a.client.dataprocJobPlacement(root, child); err != nil {
			return nil, err
		}
		seen[child.Identity.NativeID] = true
	}
	return impacts, nil
}

func (a *action) dataprocPrerequisitesAbsent(ctx context.Context, request contracts.ActionRequest) error {
	for _, prior := range request.PrerequisiteDeletions {
		if _, err := a.client.nativeGet(ctx, dataprocJobType, prior.Asset.Identity.NativeID); !isNotFound(err) {
			if err != nil {
				return err
			}
			return groupDenied("dataproc_prerequisite_still_exists")
		}
	}
	return nil
}

func (a *action) dataprocClusterPreflight(ctx context.Context, request contracts.ActionRequest, live map[string]any) error {
	impacts, err := a.dataprocPlan(request)
	if err != nil {
		return err
	}
	if err := a.dataprocPrerequisitesAbsent(ctx, request); err != nil {
		return err
	}
	if object(live["status"])["state"] == "DELETING" {
		_, err := a.dataprocReadback(ctx, request)
		return err
	}
	members, err := a.client.dataprocMembers(ctx, request.Asset, live)
	if err != nil {
		return err
	}
	controllers := map[string]asset.AssetID{request.Asset.Identity.NativeID: request.Asset.ID}
	for _, impact := range impacts {
		controllers[impact.Asset.Identity.NativeID] = impact.Asset.ID
	}
	present := map[groupImpactKey]bool{}
	for _, member := range members {
		key := groupImpactKey{controllers[member.parent], member.id}
		impact, found := impacts[key]
		if !found && member.parent == request.Asset.Identity.NativeID && !member.retain {
			// A deleted VM may leave its reviewed disk behind. Preserve the
			// original plan ancestry after proving the old controller absent.
			for frozenKey, frozen := range impacts {
				if frozenKey.id != member.id || !frozen.Delete || frozen.ControllerID == request.Asset.ID {
					continue
				}
				for _, ancestor := range impacts {
					if ancestor.Asset.ID != frozen.ControllerID {
						continue
					}
					if _, err := a.client.nativeGet(ctx, ancestor.Asset.Identity.NativeType, ancestor.Asset.Identity.NativeID); isNotFound(err) {
						key, impact, found = frozenKey, frozen, true
					} else if err != nil {
						return err
					}
				}
			}
		}
		if !found || present[key] || impact.Asset.Identity.NativeType != member.kind || impact.Delete == member.retain {
			return groupDenied("dataproc_member_missing_or_policy_changed")
		}
		if err := dataprocSameMember(member, impact.Asset.Normalized); err != nil {
			return err
		}
		if impact.Delete && (protectedComputeLabels(member.data) || protectionReason(member.kind, member.data) != "" || member.data["deletionProtection"] == true) {
			return groupDenied("dataproc_member_protected")
		}
		present[key] = true
	}
	for key, impact := range impacts {
		if present[key] {
			continue
		}
		live, err := a.client.nativeGet(ctx, impact.Asset.Identity.NativeType, impact.Asset.Identity.NativeID)
		if isNotFound(err) && impact.Delete {
			continue
		}
		if err != nil {
			return contracts.DependencyReadError(err)
		}
		if !impact.Delete {
			if err := dataprocSameMember(dataprocMember{kind: impact.Asset.Identity.NativeType, data: live}, impact.Asset.Normalized); err != nil {
				return err
			}
			continue
		}
		return groupDenied("dataproc_member_membership_changed")
	}
	return a.dataprocPrerequisitesAbsent(ctx, request)
}

func (a *action) dataprocReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	impacts, err := a.dataprocPlan(request)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.dataprocPrerequisitesAbsent(ctx, request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	pending := false
	kept := map[string]contracts.ActionImpact{}
	jobs := map[string]contracts.ActionImpact{}
	for _, impact := range impacts {
		kind, id := impact.Asset.Identity.NativeType, impact.Asset.Identity.NativeID
		live, err := a.client.nativeGet(ctx, kind, id)
		if isNotFound(err) && impact.Delete {
			continue
		}
		if err != nil {
			return contracts.ReadbackResult{}, contracts.DependencyReadError(err)
		}
		if isDataproc(kind) {
			if err := a.client.dataprocIdentity(kind, id, live); err != nil {
				return contracts.ReadbackResult{}, err
			}
			if err := dataprocSameResource(kind, impact.Asset.Normalized, live); err != nil {
				return contracts.ReadbackResult{}, err
			}
		} else if text(live["id"]) != text(impact.Asset.Normalized["id"]) || a.client.canonicalName(text(live["selfLink"])) != id {
			return contracts.ReadbackResult{}, groupDenied("dataproc_member_recreated")
		}
		if impact.Delete {
			pending = true
		} else {
			kept[id] = impact
			if kind == dataprocJobType {
				jobs[id] = impact
				terminal, err := dataprocTerminalJob(live)
				if err != nil {
					return contracts.ReadbackResult{}, err
				}
				pending = pending || !terminal
			} else if err := dataprocSameMember(dataprocMember{kind: kind, data: live}, impact.Asset.Normalized); err != nil {
				return contracts.ReadbackResult{}, err
			}
		}
	}
	currentJobs, err := a.client.dataprocJobs(ctx, request.Asset, request.Asset.Normalized)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	for _, job := range currentJobs {
		if _, found := jobs[job.id]; !found {
			return contracts.ReadbackResult{}, groupDenied("dataproc_unreviewed_job")
		}
	}
	// Virtual-cluster GKE resources are dependencies retained by the native API.
	if len(object(request.Asset.Normalized["virtualClusterConfig"])) == 0 && len(object(object(request.Asset.Normalized["config"])["gkeClusterConfig"])) == 0 {
		current, err := a.client.dataprocCompute(ctx, text(request.Asset.Normalized["clusterUuid"]))
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		for _, member := range current {
			if keep, ok := kept[member.id]; !ok || text(keep.Asset.Normalized["id"]) != text(member.data["id"]) {
				pending = true
			}
		}
	}
	live, err := a.client.request(ctx, "GET", a.endpoint, nil)
	if err == nil {
		if err := a.dataprocPreflight(request, live); err != nil {
			return contracts.ReadbackResult{}, err
		}
		pending = true
	} else if !isNotFound(err) {
		return contracts.ReadbackResult{}, err
	}
	return contracts.ReadbackResult{Exists: pending, State: "dataproc_resources_deleting"}, nil
}

func dataprocClusterPhase(request contracts.ActionRequest, operation string) map[string]any {
	return map[string]any{"phase": "dataproc_cluster_delete", "resource": request.Asset.Identity.NativeID, "uuid": request.Asset.Normalized["clusterUuid"], "configuration": request.Asset.Normalized[dataprocProof], "operation": operation}
}

func (a *action) dataprocOperation(data map[string]any, request contracts.ActionRequest) (string, error) {
	if done, found := data["done"]; found {
		if _, ok := done.(bool); !ok {
			return "", groupDenied("dataproc_operation_status_invalid")
		}
	}
	name := text(data["name"])
	expectedParent := strings.TrimSuffix(strings.TrimPrefix(request.Asset.Identity.NativeID, "//dataproc.googleapis.com/"), "/clusters/"+last(request.Asset.Identity.NativeID)) + "/operations/"
	if !strings.HasPrefix(name, expectedParent) || !segmentPattern.MatchString(strings.TrimPrefix(name, expectedParent)) || last(name) == "." || last(name) == ".." {
		return "", groupDenied("dataproc_operation_scope_changed")
	}
	metadata := object(data["metadata"])
	if raw := data["metadata"]; raw != nil && metadata == nil {
		return "", groupDenied("dataproc_operation_metadata_invalid")
	}
	for _, key := range []string{"clusterName", "clusterUuid", "operationType"} {
		if value, found := metadata[key]; found {
			if _, ok := value.(string); !ok {
				return "", groupDenied("dataproc_operation_metadata_invalid")
			}
		}
	}
	if name := text(metadata["clusterName"]); name != "" && name != text(request.Asset.Normalized["clusterName"]) {
		return "", groupDenied("dataproc_operation_cluster_changed")
	}
	if uuid := text(metadata["clusterUuid"]); uuid != "" && uuid != text(request.Asset.Normalized["clusterUuid"]) {
		return "", groupDenied("dataproc_operation_uuid_changed")
	}
	return a.operationURL(data)
}

func (a *action) waitDataprocCluster(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if text(result.Data["phase"]) != "dataproc_cluster_delete" || result.Data["resource"] != request.Asset.Identity.NativeID || text(result.Data["uuid"]) == "" || result.Data["uuid"] != request.Asset.Normalized["clusterUuid"] || result.Data["configuration"] != request.Asset.Normalized[dataprocProof] || text(result.Data["operation"]) != result.ProviderOperationID {
		return contracts.WaitResult{}, groupDenied("dataproc_cluster_phase_invalid")
	}
	if _, err := a.dataprocPlan(request); err != nil {
		return contracts.WaitResult{}, err
	}
	if operation := result.ProviderOperationID; operation != "" {
		parsed, err := url.Parse(operation)
		if err != nil {
			return contracts.WaitResult{}, err
		}
		expected, err := a.dataprocOperation(map[string]any{"name": strings.TrimPrefix(parsed.Path, "/v1/")}, request)
		if err != nil || expected != operation {
			return contracts.WaitResult{}, groupDenied("dataproc_operation_scope_changed")
		}
		response, err := a.client.requestResult(ctx, "GET", expected, nil, nil)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			actual, err := a.dataprocOperation(response.Data, request)
			if err != nil {
				return contracts.WaitResult{}, err
			}
			if actual != expected {
				return contracts.WaitResult{}, groupDenied("dataproc_operation_identity_changed")
			}
			if err := operationError(response.Data, response.RequestID); err != nil {
				return contracts.WaitResult{}, err
			}
			if response.Data["done"] != true {
				return contracts.WaitResult{RetryAfter: 2 * time.Second, State: "dataproc_cluster_delete"}, nil
			}
		}
	}
	read, err := a.dataprocReadback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, RetryAfter: 2 * time.Second, State: read.State}, err
}
