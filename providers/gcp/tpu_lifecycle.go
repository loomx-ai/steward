package gcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) tpuQueueNodes(ctx context.Context, id string, data map[string]any) ([]serviceChild, error) {
	specs, err := c.tpuRequestedNodes(id, data)
	if err != nil {
		return nil, err
	}
	parents, expected := map[string]bool{}, map[string]bool{}
	for _, spec := range specs {
		parents[spec.Parent] = true
		if spec.ID != "" {
			expected[spec.ID] = true
		}
	}
	var children []serviceChild
	seen := map[string]bool{}
	for parent := range parents {
		records, err := c.batchList(ctx, "tpu.projects.locations.nodes.list", map[string]any{"parent": strings.TrimPrefix(parent, "//"+tpuHost+"/")}, "nodes")
		if err != nil {
			return nil, err
		}
		for _, record := range records {
			nodeID, err := c.tpuID(tpuNodeType, text(record["name"]))
			if err != nil || !strings.HasPrefix(nodeID, parent+"/nodes/") || seen[nodeID] {
				return nil, groupDenied("tpu_node_list_invalid")
			}
			seen[nodeID] = true
			if err := c.tpuIdentity(tpuNodeType, nodeID, record); err != nil {
				return nil, err
			}
			queue := ""
			if value := text(record["queuedResource"]); value != "" {
				queue, err = c.tpuID(tpuQueueType, value)
				if err != nil {
					return nil, err
				}
			}
			if queue != id {
				if expected[nodeID] {
					return nil, groupDenied("tpu_requested_node_replaced")
				}
				continue
			}
			live, err := c.tpuRead(ctx, tpuNodeType, nodeID)
			if err != nil {
				return nil, err
			}
			if err := tpuSameResource(tpuNodeType, record, live); err != nil {
				return nil, err
			}
			if err := c.tpuQueueRelation(id, data, nodeID, live); err != nil {
				return nil, err
			}
			children = append(children, serviceChild{kind: tpuNodeType, id: nodeID, data: live, direct: true})
		}
	}
	used := map[int]bool{}
	for _, child := range children {
		matched := -1
		for i, spec := range specs {
			if !used[i] && spec.ID == child.id {
				matched = i
				break
			}
		}
		if matched < 0 {
			for i, spec := range specs {
				if !used[i] && spec.ID == "" && strings.HasPrefix(child.id, spec.Parent+"/nodes/") {
					matched = i
					break
				}
			}
		}
		if matched < 0 {
			return nil, groupDenied("tpu_node_count_changed")
		}
		used[matched] = true
	}
	slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
	return children, nil
}

type tpuNodeProof struct {
	ID            string `json:"id"`
	UID           string `json:"uid"`
	Configuration string `json:"configuration"`
	Disks         string `json:"disks"`
}

func (c *client) tpuNodeSnapshot(ctx context.Context, id string, data map[string]any) ([]serviceChild, []tpuNodeProof, error) {
	nodes, err := c.tpuQueueNodes(ctx, id, data)
	if err != nil {
		return nil, nil, err
	}
	var proofs []tpuNodeProof
	for _, node := range nodes {
		_, disks, err := c.tpuDisks(ctx, node.data)
		if err != nil {
			return nil, nil, err
		}
		encoded, _ := json.Marshal(disks)
		proofs = append(proofs, tpuNodeProof{node.id, text(node.data["id"]), tpuConfiguration(tpuNodeType, node.data, false), string(encoded)})
	}
	return nodes, proofs, nil
}

func (c *client) tpuPlannedNodes(data map[string]any) ([]tpuNodeProof, error) {
	var proofs []tpuNodeProof
	if json.Unmarshal([]byte(text(data[tpuNodeProofs])), &proofs) != nil {
		return nil, groupDenied("tpu_node_proofs_invalid")
	}
	seen := map[string]bool{}
	for _, proof := range proofs {
		if _, err := c.tpuName(tpuNodeType, proof.ID); err != nil {
			return nil, err
		}
		if proof.UID == "" || proof.Configuration == "" || proof.Disks == "" || seen[proof.ID] {
			return nil, groupDenied("tpu_node_proofs_invalid")
		}
		seen[proof.ID] = true
	}
	return proofs, nil
}

func (c *client) tpuChildren(ctx context.Context, parent asset.Identity, planned map[string]any) ([]serviceChild, error) {
	live, err := c.tpuRead(ctx, parent.NativeType, parent.NativeID)
	if err != nil {
		return nil, err
	}
	if err := tpuSameResource(parent.NativeType, planned, live); err != nil {
		return nil, err
	}
	var children []serviceChild
	if parent.NativeType == tpuNodeType {
		children, _, err = c.tpuDisks(ctx, live)
	} else if parent.NativeType == tpuQueueType {
		var proofs []tpuNodeProof
		children, proofs, err = c.tpuNodeSnapshot(ctx, parent.NativeID, live)
		if err == nil {
			plannedProofs, readErr := c.tpuPlannedNodes(planned)
			if readErr != nil {
				return nil, readErr
			}
			_, again, readErr := c.tpuNodeSnapshot(ctx, parent.NativeID, live)
			if readErr != nil {
				return nil, readErr
			}
			if !slices.Equal(proofs, again) || !slices.Equal(proofs, plannedProofs) {
				return nil, groupDenied("tpu_nodes_changed")
			}
		}
	}
	if err != nil {
		return nil, err
	}
	again, err := c.tpuRead(ctx, parent.NativeType, parent.NativeID)
	if err != nil {
		return nil, err
	}
	if err := tpuSameResource(parent.NativeType, live, again); err != nil {
		return nil, err
	}
	return children, nil
}

func (c *client) tpuPlannedDisks(planned map[string]any) ([]tpuDiskProof, error) {
	attachments, err := c.tpuAttachments(planned)
	if err != nil {
		return nil, err
	}
	var proofs []tpuDiskProof
	if json.Unmarshal([]byte(text(planned[tpuDiskProofs])), &proofs) != nil || len(proofs) != len(attachments) {
		return nil, groupDenied("tpu_disk_proofs_invalid")
	}
	seen := map[string]bool{}
	for _, proof := range proofs {
		if proof.UID == "" || proof.Configuration == "" || seen[proof.ID] {
			return nil, groupDenied("tpu_disk_proofs_invalid")
		}
		seen[proof.ID] = true
		found := false
		for _, disk := range attachments {
			found = found || disk.Kind == proof.Kind && disk.ID == proof.ID
		}
		if !found {
			return nil, groupDenied("tpu_disk_proof_scope_changed")
		}
	}
	return proofs, nil
}

func (c *client) tpuVerifyDisks(ctx context.Context, planned map[string]any) error {
	proofs, err := c.tpuPlannedDisks(planned)
	if err != nil {
		return err
	}
	for _, proof := range proofs {
		live, err := c.nativeGet(ctx, proof.Kind, proof.ID)
		if err != nil {
			return err
		}
		if c.canonicalName(text(live["selfLink"])) != proof.ID || text(live["id"]) != proof.UID || batchComputeConfiguration(proof.Kind, live) != proof.Configuration {
			return groupDenied("tpu_retained_disk_changed")
		}
	}
	return nil
}

func (c *client) tpuVerifyQueue(ctx context.Context, id string, planned map[string]any) error {
	name := text(planned["queuedResource"])
	if name == "" {
		if text(planned[tpuQueueProof]) != "" {
			return groupDenied("tpu_queue_proof_invalid")
		}
		return nil
	}
	queueID, err := c.tpuID(tpuQueueType, name)
	if err != nil {
		return err
	}
	proof := text(planned[tpuQueueProof])
	if proof == "" {
		return groupDenied("tpu_queue_proof_missing")
	}
	live, err := c.tpuRead(ctx, tpuQueueType, queueID)
	if proof == "absent" && isNotFound(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if proof != tpuConfiguration(tpuQueueType, live, false) {
		return groupDenied("tpu_queue_configuration_changed")
	}
	return c.tpuQueueRelation(queueID, live, id, planned)
}

func (a *action) tpuActionIdentity(request contracts.ActionRequest) error {
	if request.Action != "delete" || request.Asset.ID == "" || request.Asset.Identity != a.identity || a.identity.Provider != asset.ProviderGCP || text(request.Asset.Normalized[tpuProof]) == "" || text(request.Asset.Normalized[tpuBaseProof]) == "" {
		return groupDenied("tpu_action_identity_changed")
	}
	if err := a.client.tpuIdentity(a.kind.NativeType, a.identity.NativeID, request.Asset.Normalized); err != nil {
		return err
	}
	endpoint, err := a.client.resourceURL(a.kind, a.identity.NativeID)
	if err != nil || endpoint != a.endpoint {
		return groupDenied("tpu_action_endpoint_changed")
	}
	if _, err := groupImpacts(request); err != nil {
		return err
	}
	if a.kind.NativeType == tpuQueueType {
		if len(request.LifecycleImpacts) != 0 {
			return groupDenied("tpu_queue_impacts_invalid")
		}
		if _, err := a.client.tpuRequestedNodes(a.identity.NativeID, request.Asset.Normalized); err != nil {
			return err
		}
		proofs, err := a.client.tpuPlannedNodes(request.Asset.Normalized)
		if err != nil {
			return err
		}
		if len(proofs) != len(request.PrerequisiteDeletions) {
			return groupDenied("tpu_prerequisites_changed")
		}
		for _, prerequisite := range request.PrerequisiteDeletions {
			found := false
			for _, proof := range proofs {
				found = found || proof.ID == prerequisite.Asset.Identity.NativeID && proof.UID == text(prerequisite.Asset.Normalized["id"]) && proof.Configuration == text(prerequisite.Asset.Normalized[tpuProof]) && proof.Disks == text(prerequisite.Asset.Normalized[tpuDiskProofs])
			}
			if !found {
				return groupDenied("tpu_prerequisite_configuration_changed")
			}
		}
		return nil
	}
	if a.kind.NativeType != tpuNodeType || len(request.PrerequisiteDeletions) != 0 {
		return groupDenied("tpu_node_plan_invalid")
	}
	proofs, err := a.client.tpuPlannedDisks(request.Asset.Normalized)
	if err != nil {
		return err
	}
	if len(request.LifecycleImpacts) != len(proofs) {
		return groupDenied("tpu_retained_disk_plan_changed")
	}
	seen := map[asset.AssetID]bool{request.Asset.ID: true}
	for _, impact := range request.LifecycleImpacts {
		if impact.Delete || impact.ControllerID != request.Asset.ID || seen[impact.Asset.ID] {
			return groupDenied("tpu_retained_disk_plan_invalid")
		}
		seen[impact.Asset.ID] = true
		found := false
		for _, proof := range proofs {
			found = found || proof.ID == impact.Asset.Identity.NativeID && proof.Kind == impact.Asset.Identity.NativeType && proof.UID == text(impact.Asset.Normalized["id"]) && proof.Configuration == batchComputeConfiguration(proof.Kind, impact.Asset.Normalized)
		}
		if !found {
			return groupDenied("tpu_retained_disk_plan_changed")
		}
	}
	return nil
}

// A retry may see the reviewed disks already detached. It must still match the
// same Node incarnation and every setting outside dataDisks, with no new disk.
func (a *action) tpuSameNode(request contracts.ActionRequest, live map[string]any) error {
	if err := a.client.tpuIdentity(tpuNodeType, a.identity.NativeID, live); err != nil {
		return err
	}
	if text(request.Asset.Normalized[tpuBaseProof]) != tpuConfiguration(tpuNodeType, live, true) {
		return groupDenied("tpu_node_configuration_changed")
	}
	planned, err := a.client.tpuAttachments(request.Asset.Normalized)
	if err != nil {
		return err
	}
	attachments, err := a.client.tpuAttachments(live)
	if err != nil {
		return err
	}
	for _, disk := range attachments {
		if !slices.Contains(planned, disk) {
			return groupDenied("tpu_unreviewed_disk")
		}
	}
	return nil
}

func (a *action) tpuValidateLive(ctx context.Context, request contracts.ActionRequest, live map[string]any) (err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if a.kind.NativeType == tpuNodeType {
		if live != nil {
			if err := a.tpuSameNode(request, live); err != nil {
				return err
			}
		}
		if err := a.client.tpuVerifyQueue(ctx, a.identity.NativeID, request.Asset.Normalized); err != nil {
			return err
		}
		return a.client.tpuVerifyDisks(ctx, request.Asset.Normalized)
	}
	if live != nil {
		if err := tpuSameResource(tpuQueueType, request.Asset.Normalized, live); err != nil {
			return err
		}
	}
	if err := a.servicePrerequisitesAbsent(ctx, request); err != nil {
		return err
	}
	for _, prerequisite := range request.PrerequisiteDeletions {
		if err := a.client.tpuVerifyDisks(ctx, prerequisite.Asset.Normalized); err != nil {
			return err
		}
	}
	data := live
	if data == nil {
		data = request.Asset.Normalized
	}
	nodes, err := a.client.tpuQueueNodes(ctx, a.identity.NativeID, data)
	if err != nil {
		return err
	}
	if len(nodes) != 0 {
		return groupDenied("tpu_queue_nodes_still_exist")
	}
	return nil
}

func (a *action) tpuPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	if err := a.tpuActionIdentity(request); err != nil {
		return contracts.PreflightResult{}, err
	}
	live, err := a.readResource(ctx)
	if err != nil && !isNotFound(err) {
		return contracts.PreflightResult{}, err
	}
	if isNotFound(err) {
		read, err := a.tpuReadback(ctx, request)
		return contracts.PreflightResult{Allowed: err == nil, Absent: err == nil && !read.Exists}, err
	}
	if err := a.tpuValidateLive(ctx, request, live); err != nil {
		return contracts.PreflightResult{}, err
	}
	if _, err := tpuStage(a.kind.NativeType, live); err != nil {
		return contracts.PreflightResult{}, err
	}
	if protectedComputeLabels(live) {
		return contracts.PreflightResult{Reason: "protected_labels"}, nil
	}
	if reason := protectionReason(a.kind.NativeType, live); reason != "" {
		return contracts.PreflightResult{Reason: reason}, nil
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *action) tpuReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.tpuActionIdentity(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	live, err := a.readResource(ctx)
	if err != nil && !isNotFound(err) {
		return contracts.ReadbackResult{}, err
	}
	if err := a.tpuValidateLive(ctx, request, live); err != nil {
		return contracts.ReadbackResult{}, err
	}
	// Checking retained disks or queue membership can take time. Recheck the Node
	// or request last so a same-name replacement cannot hide behind an early 404.
	live, err = a.readResource(ctx)
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: false}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if a.kind.NativeType == tpuNodeType {
		err = a.tpuSameNode(request, live)
	} else {
		err = tpuSameResource(a.kind.NativeType, request.Asset.Normalized, live)
	}
	return contracts.ReadbackResult{Exists: true, State: resourceState(live)}, err
}
