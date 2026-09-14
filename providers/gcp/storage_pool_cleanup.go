package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const poolConfigurationKey = "_storage_pool_configuration"
const poolMembersKey = "_storage_pool_members"
const poolSnapshotKey = "_storage_pool_snapshot"
const poolLifecycleSource = "gcp:storage-pool"

type poolMember struct {
	ID      string `json:"id"`
	Created string `json:"created"`
}

func storagePoolConfiguration(data map[string]any) string {
	value := cloneParameters(data)
	for key := range value {
		if strings.HasPrefix(key, "_") || strings.HasPrefix(key, "refs_") || slices.Contains([]string{"state", "status", "resourceStatus", "labelFingerprint", "pool_usage", "storage_pool_disks", "project_id", "project_number", "zone_id"}, key) {
			delete(value, key)
		}
	}
	raw, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func (c *client) storagePoolMembers(id string, rows []map[string]any) ([]poolMember, error) {
	zone := strings.Split(strings.TrimPrefix(id, "//compute.googleapis.com/"), "/")[3]
	members := make([]poolMember, 0, len(rows))
	for _, row := range rows {
		disk, err := c.storagePoolDiskID(text(row["disk"]), zone)
		if err != nil {
			return nil, err
		}
		created := text(row["creationTimestamp"])
		if _, err := time.Parse(time.RFC3339Nano, created); err != nil {
			return nil, groupDenied("storage_pool_member_creation_missing")
		}
		members = append(members, poolMember{disk, created})
	}
	slices.SortFunc(members, func(a, b poolMember) int { return strings.Compare(a.ID, b.ID) })
	return members, nil
}

func (c *client) storagePoolProtection(data map[string]any, members []poolMember) string {
	poolType := text(data["storagePoolType"])
	name := last(poolType)
	if name != "hyperdisk-balanced" && name != "hyperdisk-throughput" || data["exapoolProvisionedCapacityGb"] != nil {
		return "storage_pool_type_requires_external_management"
	}
	if poolType != name {
		if strings.HasPrefix(poolType, "projects/") {
			poolType = "//compute.googleapis.com/" + poolType
		}
		expected := "//compute.googleapis.com/projects/" + c.project + "/zones/" + last(text(data["zone"])) + "/storagePoolTypes/" + name
		if c.canonicalName(poolType) != expected {
			return "storage_pool_type_identity_changed"
		}
	}
	for _, member := range members {
		if !strings.HasPrefix(member.ID, "//compute.googleapis.com/projects/"+c.project+"/") {
			return "storage_pool_shared_members_require_external_cleanup"
		}
	}
	return ""
}

func (c *client) storagePoolSaved(root asset.Asset) ([]poolMember, error) {
	if err := c.storagePoolIdentity(root.Identity.NativeID, root.Normalized, "zones/"+last(text(root.Normalized["zone"]))); err != nil {
		return nil, err
	}
	if text(root.Normalized["id"]) == "" || text(root.Normalized["creationTimestamp"]) == "" {
		return nil, groupDenied("storage_pool_creation_missing")
	}
	encoded, proof := text(root.Normalized[poolMembersKey]), text(root.Normalized[poolConfigurationKey])
	var members []poolMember
	if encoded == "" || proof == "" || json.Unmarshal([]byte(encoded), &members) != nil || text(root.Normalized[poolSnapshotKey]) != infraManifestHash(proof, encoded) {
		return nil, groupDenied("storage_pool_review_missing")
	}
	seen := map[string]bool{}
	for _, member := range members {
		id, err := c.storagePoolDiskID(member.ID, last(text(root.Normalized["zone"])))
		if err != nil || id != member.ID || seen[id] {
			return nil, groupDenied("storage_pool_review_invalid")
		}
		if _, err := time.Parse(time.RFC3339Nano, member.Created); err != nil {
			return nil, groupDenied("storage_pool_review_invalid")
		}
		seen[id] = true
	}
	return members, nil
}

func (c *client) storagePoolSame(root asset.Asset, live map[string]any) error {
	if err := c.storagePoolIdentity(root.Identity.NativeID, live, "zones/"+last(text(root.Normalized["zone"]))); err != nil {
		return err
	}
	if text(root.Normalized[poolConfigurationKey]) != storagePoolConfiguration(live) {
		return groupDenied("storage_pool_configuration_changed")
	}
	return nil
}

func (h *computeGroups) contributeStoragePools(ctx context.Context, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	for _, root := range assets {
		if root.Identity.Provider != asset.ProviderGCP || root.Identity.NativeType != storagePoolType {
			continue
		}
		block := func(id, kind, reason string) {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: root.Identity.Provider, ConnectionID: root.Identity.ConnectionID, ControllerID: root.ID, NativeType: kind, NativeID: id, Relationship: graph.RelationshipDependsOn, Evidence: map[string]any{"source": poolLifecycleSource, "reason": reason}})
		}
		planned, err := h.client.storagePoolSaved(root)
		if err != nil {
			block(root.Identity.NativeID, storagePoolType, "storage_pool_refresh_required")
			continue
		}
		if reason := h.client.storagePoolProtection(root.Normalized, planned); reason != "" {
			block(root.Identity.NativeID, storagePoolType, reason)
			continue
		}
		live, err := h.client.nativeGet(ctx, storagePoolType, root.Identity.NativeID)
		if isNotFound(err) {
			block(root.Identity.NativeID, storagePoolType, "storage_pool_refresh_required")
			continue
		}
		if err != nil {
			return result, err
		}
		if err = h.client.storagePoolSame(root, live); err != nil {
			block(root.Identity.NativeID, storagePoolType, "storage_pool_refresh_required")
			continue
		}
		rows, err := h.client.storagePoolDisks(ctx, root.Identity.NativeID)
		if err != nil {
			return result, err
		}
		members, err := h.client.storagePoolMembers(root.Identity.NativeID, rows)
		if err != nil {
			return result, err
		}
		if !slices.Equal(members, planned) {
			block(root.Identity.NativeID, storagePoolType, "storage_pool_members_refresh_required")
			continue
		}
		again, err := h.client.storagePoolDisks(ctx, root.Identity.NativeID)
		if err != nil {
			return result, err
		}
		repeated, err := h.client.storagePoolMembers(root.Identity.NativeID, again)
		if err != nil {
			return result, err
		}
		live, err = h.client.nativeGet(ctx, storagePoolType, root.Identity.NativeID)
		if err != nil {
			return result, err
		}
		if !slices.Equal(members, repeated) || h.client.storagePoolSame(root, live) != nil {
			block(root.Identity.NativeID, storagePoolType, "storage_pool_members_refresh_required")
			continue
		}
		for _, member := range members {
			disk, found, err := findManagedAsset(assets, root, "compute.googleapis.com/Disk", member.ID)
			if err != nil {
				return result, err
			}
			if !found || text(disk.Normalized["creationTimestamp"]) != member.Created || !slices.Contains(references(h.client, disk.Normalized)[storagePoolType], root.Identity.NativeID) {
				block(member.ID, "compute.googleapis.com/Disk", "storage_pool_disk_refresh_required")
				continue
			}
			// Disk deletion must be separately selected; this prerequisite does not
			// transfer ownership from a VM or authorize a pool cascade.
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: root.ID, TargetAssetID: disk.ID, Type: graph.RelationshipDependsOn, Source: poolLifecycleSource, Confidence: 1, Evidence: map[string]any{
				"native_pool": root.Identity.NativeID,
				graph.RelationshipEvidenceRequiredDeletion:   true,
				graph.RelationshipEvidenceAutomaticSelection: false,
				graph.RelationshipEvidenceAuthority:          string(graph.AuthorityAuthoritative),
				graph.RelationshipEvidenceDeletionOrder:      graph.DeletionOrderTargetBeforeSource,
			}})
		}
	}
	return result, nil
}

func (a *action) storagePoolActionIdentity(request contracts.ActionRequest) ([]poolMember, error) {
	if request.Action != "delete" || request.Asset.ID == "" || request.Asset.Identity != a.identity || !gcpPartition(a.identity.Partition) || len(request.Parameters) != 0 || len(request.LifecycleImpacts) != 0 {
		return nil, groupDenied("storage_pool_action_changed")
	}
	members, err := a.client.storagePoolSaved(request.Asset)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, p := range request.PrerequisiteDeletions {
		found := false
		for _, m := range members {
			found = found || m.ID == p.Asset.Identity.NativeID && m.Created == text(p.Asset.Normalized["creationTimestamp"])
		}
		if !found || seen[p.Asset.Identity.NativeID] || !p.Delete || p.ControllerID != request.Asset.ID || p.Asset.Identity.Provider != a.identity.Provider || p.Asset.Identity.Partition != a.identity.Partition || p.Asset.Identity.ConnectionID != a.identity.ConnectionID || p.Asset.Identity.NativeType != "compute.googleapis.com/Disk" {
			return nil, groupDenied("storage_pool_prerequisite_changed")
		}
		seen[p.Asset.Identity.NativeID] = true
	}
	return members, nil
}

// Every previously reviewed local disk is read independently even if the pool or
// its list endpoint disappeared. Parent absence cannot hide a surviving disk.
func (a *action) storagePoolReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	members, err := a.storagePoolActionIdentity(request)
	if receipt := request.ExecutionResult; receipt != nil && (len(receipt.Data) != 0 || receipt.ProviderOperationID != "") {
		if receipt.Data["storage_pool_receipt"] != storagePoolReceipt(request, receipt.ProviderOperationID) || receipt.Data["operation"] != receipt.ProviderOperationID {
			return contracts.ReadbackResult{}, groupDenied("storage_pool_receipt_changed")
		}
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if reason := a.client.storagePoolProtection(request.Asset.Normalized, members); reason != "" {
		return contracts.ReadbackResult{}, groupDenied(reason)
	}
	exists := false
	for _, member := range members {
		_, err := a.client.nativeGet(ctx, "compute.googleapis.com/Disk", member.ID)
		if err != nil && !isNotFound(err) {
			return contracts.ReadbackResult{}, err
		}
		exists = exists || err == nil
	}
	live, err := a.readResource(ctx)
	if isNotFound(err) {
		return contracts.ReadbackResult{Exists: exists, State: "verifying_storage_pool_disks"}, nil
	}
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err = a.client.storagePoolSame(request.Asset, live); err != nil {
		return contracts.ReadbackResult{}, err
	}
	return contracts.ReadbackResult{Exists: true, State: text(live["state"])}, nil
}

func (a *action) storagePoolPreflight(ctx context.Context, request contracts.ActionRequest) (contracts.PreflightResult, error) {
	members, err := a.storagePoolActionIdentity(request)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if reason := a.client.storagePoolProtection(request.Asset.Normalized, members); reason != "" {
		return contracts.PreflightResult{Reason: reason}, nil
	}
	live, err := a.readResource(ctx)
	if isNotFound(err) {
		read, e := a.storagePoolReadback(ctx, request)
		return contracts.PreflightResult{Allowed: e == nil && !read.Exists, Absent: e == nil && !read.Exists, Reason: "storage_pool_disks_still_exist"}, e
	}
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if err = a.client.storagePoolSame(request.Asset, live); err != nil {
		return contracts.PreflightResult{}, err
	}
	if text(live["state"]) != "READY" || protectedComputeLabels(live) {
		return contracts.PreflightResult{Reason: "storage_pool_not_ready_or_protected"}, nil
	}
	for pass := 0; pass < 2; pass++ {
		rows, err := a.client.storagePoolDisks(ctx, a.identity.NativeID)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if len(rows) != 0 {
			return contracts.PreflightResult{Reason: "storage_pool_disks_still_exist"}, nil
		}
		for _, member := range members {
			_, err := a.client.nativeGet(ctx, "compute.googleapis.com/Disk", member.ID)
			if err == nil {
				return contracts.PreflightResult{Reason: "storage_pool_disks_still_exist"}, nil
			}
			if !isNotFound(err) {
				return contracts.PreflightResult{}, err
			}
		}
	}
	again, err := a.readResource(ctx)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if err = a.client.storagePoolSame(request.Asset, again); err != nil {
		return contracts.PreflightResult{}, err
	}
	if text(again["state"]) != "READY" {
		return contracts.PreflightResult{Reason: "storage_pool_not_ready_or_protected"}, nil
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func storagePoolReceipt(request contracts.ActionRequest, operation string) string {
	raw, _ := json.Marshal([]any{request.Asset.Identity, request.Asset.Normalized[poolConfigurationKey], request.Asset.Normalized[poolMembersKey], operation})
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func (a *action) storagePoolOperation(data map[string]any, request contracts.ActionRequest) (string, error) {
	if _, present := data["error"]; present {
		return "", groupDenied("storage_pool_operation_error_invalid")
	}
	operation, err := a.operationURL(data)
	if err != nil || operation == "" || data["kind"] != "compute#operation" || !slices.Contains([]string{"PENDING", "RUNNING", "DONE"}, text(data["status"])) {
		return "", groupDenied("storage_pool_operation_invalid")
	}
	if target, present := data["targetLink"]; present && a.client.canonicalName(text(target)) != a.identity.NativeID {
		return "", groupDenied("storage_pool_operation_target_changed")
	}
	if target, present := data["targetId"]; present && text(target) != text(request.Asset.Normalized["id"]) {
		return "", groupDenied("storage_pool_operation_target_changed")
	}
	if kind, present := data["operationType"]; present && kind != "delete" {
		return "", groupDenied("storage_pool_operation_type_changed")
	}
	if zone, present := data["zone"]; present && a.client.canonicalName(text(zone)) != a.client.canonicalName(text(request.Asset.Normalized["zone"])) {
		return "", groupDenied("storage_pool_operation_zone_changed")
	}
	if link, present := data["selfLink"]; present && a.client.canonicalName(text(link)) != a.client.canonicalName(operation) {
		return "", groupDenied("storage_pool_operation_identity_changed")
	}
	return operation, nil
}

func (a *action) storagePoolWait(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if _, err := a.storagePoolActionIdentity(request); err != nil {
		return contracts.WaitResult{}, err
	}
	operation := result.ProviderOperationID
	if len(result.Data) != 0 || operation != "" {
		if result.Data["storage_pool_receipt"] != storagePoolReceipt(request, operation) || result.Data["operation"] != operation {
			return contracts.WaitResult{}, groupDenied("storage_pool_receipt_changed")
		}
	}
	if operation != "" {
		expected, err := a.operationURL(map[string]any{"name": last(operation)})
		if err != nil || operation != expected {
			return contracts.WaitResult{}, groupDenied("storage_pool_operation_identity_changed")
		}
		response, err := a.client.requestResult(ctx, "GET", operation, nil, nil)
		if err != nil && !isNotFound(err) {
			return contracts.WaitResult{}, err
		}
		if err == nil {
			if failure := operationError(response.Data, response.RequestID); failure != nil {
				return contracts.WaitResult{}, failure
			}
			actual, err := a.storagePoolOperation(response.Data, request)
			if err != nil || actual != operation {
				return contracts.WaitResult{}, groupDenied("storage_pool_operation_identity_changed")
			}
			if response.Data["status"] != "DONE" {
				return contracts.WaitResult{State: text(response.Data["status"]), RetryAfter: 2 * time.Second}, nil
			}
		}
	}
	read, err := a.storagePoolReadback(ctx, request)
	return contracts.WaitResult{Done: err == nil && !read.Exists, State: read.State, RetryAfter: 2 * time.Second}, err
}
