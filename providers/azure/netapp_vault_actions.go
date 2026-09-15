package azure

import (
	"context"
	"encoding/json"
	"maps"
	"slices"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type netappVaultAction struct {
	*netappPolicyAction
	members map[string]any
}

func newNetappVaultAction(c *client, connection asset.ConnectionID, value asset.Asset) (*netappVaultAction, error) {
	base, err := newNetappPolicyAction(c, connection, value)
	if err != nil {
		return nil, err
	}
	review := object(value.Normalized[netappVaultReview])
	if value.Identity.NativeType != netappVaultType || len(review) != 4 || review["region"] != value.Location || review["configuration"] != value.Normalized["_netapp_configuration"] || review["assignments"] != c.privateConfiguration(base.review) || value.Normalized[netappVaultProof] != c.netappVaultProofFor(value.Identity.NativeID, connection, review) {
		return nil, serviceDenied("invalid_netapp_vault_action_review")
	}
	return &netappVaultAction{base, object(review["members"])}, nil
}

func (a *netappVaultAction) binding(req contracts.ActionRequest, data map[string]any) string {
	req.IdempotencyKey, req.ExecutionResult = "", nil
	data = maps.Clone(data)
	delete(data, "binding")
	return a.client.privateConfiguration(map[string]any{"protocol": "netapp-vault-action-1", "request": req, "data": data})
}

func (a *netappVaultAction) identity(req contracts.ActionRequest) error {
	if _, err := newNetappVaultAction(a.client, a.planned.Identity.ConnectionID, req.Asset); err != nil {
		return err
	}
	if req.Asset.Normalized[netappVaultProof] != a.planned.Normalized[netappVaultProof] {
		return serviceDenied("netapp_vault_request_changed")
	}
	retained := req
	retained.ExecutionResult = nil
	retained.LifecycleImpacts = nil
	seen := map[string]bool{}
	for _, impact := range req.LifecycleImpacts {
		v := impact.Asset
		if v.Identity.NativeType != netappBackupType {
			retained.LifecycleImpacts = append(retained.LifecycleImpacts, impact)
			continue
		}
		entry := object(a.members[v.Identity.NativeID])
		if entry == nil || seen[v.Identity.NativeID] || !impact.Delete || impact.ControllerID != a.planned.ID || v.Identity.ConnectionID != a.planned.Identity.ConnectionID || v.Location != a.planned.Location || entry["configuration"] != v.Normalized["_netapp_configuration"] || entry["ready"] != true || entry["protected"] != false {
			return serviceDenied("netapp_vault_backup_impact_changed")
		}
		review, err := a.client.netappRecoveryRecorded(v)
		if err != nil || review["uid"] != entry["uid"] || review["created"] != entry["created"] || review["protected"] != false {
			return serviceDenied("netapp_vault_backup_review_changed")
		}
		seen[v.Identity.NativeID] = true
	}
	if len(seen) != len(a.members) {
		return serviceDenied("netapp_vault_unreviewed_backup")
	}
	if err := a.netappPolicyAction.identity(retained); err != nil {
		return err
	}
	if req.ExecutionResult != nil && req.ExecutionResult.Data["binding"] != a.binding(req, req.ExecutionResult.Data) {
		return serviceDenied("netapp_vault_receipt_changed")
	}
	return nil
}

func (a *netappVaultAction) current(ctx context.Context) (bool, error) {
	raw, absent, err := a.root(ctx, false)
	if err != nil || absent {
		return absent, err
	}
	if err := a.consumers(ctx, raw); err != nil {
		return false, err
	}
	members, err := a.client.netappVaultMembers(ctx, a.planned.Identity.NativeID, a.planned.Location, a.members)
	if err != nil {
		return false, err
	}
	for id, value := range members {
		if a.members[id] == nil || a.client.privateConfiguration(object(value)) != a.client.privateConfiguration(object(a.members[id])) {
			return false, serviceDenied("netapp_vault_members_changed")
		}
	}
	return false, nil
}

func (a *netappVaultAction) backup(ctx context.Context, req contracts.ActionRequest, id string, allowAssigned bool) (bool, error) {
	own, err := a.client.netappRead(ctx, id, netappBackupType)
	if isNotFound(err) {
		return true, nil
	}
	if err != nil {
		return false, err
	}
	entry := object(a.members[id])
	if a.client.privateConfiguration(own.data) != entry["configuration"] {
		return false, serviceDenied("netapp_vault_backup_changed")
	}
	live, absent, err := a.client.netappRecoveryBoundary(ctx, id, netappBackupType, nil)
	if err != nil {
		return false, err
	}
	if absent {
		return true, nil
	}
	retention := object(live["retention"])
	source := text(retention["source"])
	consumer := object(object(a.review["consumers"])[source])
	if consumer != nil {
		volume, err := a.volume(ctx, source, false)
		if err != nil {
			return false, err
		}
		assignments, _ := netappAssignments(volume)
		if !allowAssigned && assignments[netappBackupPolicyType] != "" {
			return false, serviceDenied("netapp_vault_backup_policy_still_assigned")
		}
	} else {
		for _, impact := range req.LifecycleImpacts {
			if impact.Asset.Identity.NativeID != id {
				continue
			}
			old := object(object(impact.Asset.Normalized[netappRecoveryReview])["retention"])
			if old["source_configuration"] != retention["source_configuration"] || old["source_absent"] != retention["source_absent"] {
				return false, serviceDenied("netapp_vault_backup_source_changed")
			}
		}
	}
	canRelease := allowAssigned && consumer != nil && retention["allowed"] == false && text(retention["policy"]) != "" && retention["policy"] == consumer["policy"] && retention["source_ready"] == true
	if live["resource"] != entry["configuration"] || live["uid"] != entry["uid"] || live["created"] != entry["created"] || live["protected"] != false || live["ready"] != true && !canRelease {
		return false, serviceDenied("netapp_vault_backup_not_ready")
	}
	return false, nil
}

func (a *netappVaultAction) Preflight(ctx context.Context, req contracts.ActionRequest) (contracts.PreflightResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.PreflightResult{}, err
	}
	absent, err := a.current(ctx)
	if err != nil {
		return contracts.PreflightResult{}, err
	}
	if absent {
		read, err := a.Readback(ctx, req)
		return contracts.PreflightResult{Allowed: err == nil, Absent: !read.Exists}, err
	}
	for id := range object(a.review["consumers"]) {
		own, err := a.volume(ctx, id, false)
		if err != nil {
			return contracts.PreflightResult{}, err
		}
		if object(own["properties"])["provisioningState"] != "Succeeded" {
			return contracts.PreflightResult{}, serviceDenied("netapp_vault_volume_not_ready")
		}
	}
	for id := range a.members {
		if _, err := a.backup(ctx, req, id, true); err != nil {
			return contracts.PreflightResult{}, err
		}
	}
	return contracts.PreflightResult{Allowed: true}, nil
}

func (a *netappVaultAction) Execute(ctx context.Context, req contracts.ActionRequest) (contracts.ActionResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ActionResult{}, err
	}
	if req.ExecutionResult != nil {
		return *req.ExecutionResult, nil
	}
	if _, err := a.Preflight(ctx, req); err != nil {
		return contracts.ActionResult{}, err
	}
	data := map[string]any{"phase": "policies", "index": 0, "step": "pause"}
	data["binding"] = a.binding(req, data)
	return contracts.ActionResult{Data: data}, nil
}

func (a *netappVaultAction) Readback(ctx context.Context, req contracts.ActionRequest) (contracts.ReadbackResult, error) {
	if err := a.identity(req); err != nil {
		return contracts.ReadbackResult{}, err
	}
	_, absent, err := a.root(ctx, true)
	if err != nil {
		return contracts.ReadbackResult{}, err
	}
	if !absent {
		return contracts.ReadbackResult{Exists: true}, nil
	}
	for id := range object(a.review["consumers"]) {
		own, err := a.volume(ctx, id, true)
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		assignments, _ := netappAssignments(own)
		if assignments[netappBackupPolicyType] != "" || object(own["properties"])["provisioningState"] != "Succeeded" {
			return contracts.ReadbackResult{}, serviceDenied("netapp_vault_retained_volume_not_ready")
		}
	}
	for id := range a.members {
		_, err := a.client.netappRead(ctx, id, netappBackupType)
		if !isNotFound(err) {
			if err != nil {
				return contracts.ReadbackResult{}, err
			}
			return contracts.ReadbackResult{Exists: true}, nil
		}
	}
	return contracts.ReadbackResult{Exists: false}, nil
}

func (a *netappVaultAction) Wait(ctx context.Context, req contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	req.ExecutionResult = &result
	if err := a.identity(req); err != nil {
		return contracts.WaitResult{}, err
	}
	_, absent, err := a.root(ctx, true)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if absent {
		read, err := a.Readback(ctx, req)
		return contracts.WaitResult{Done: err == nil && !read.Exists, Data: result.Data}, err
	}
	data := batchClone(result.Data)
	out := contracts.WaitResult{Data: data, RetryAfter: 2 * time.Second}
	save := func() { data["binding"] = a.binding(req, data) }
	phase := text(data["phase"])
	ids := slices.Sorted(maps.Keys(object(a.review["consumers"])))
	if phase == "backups" {
		ids = slices.Sorted(maps.Keys(a.members))
	}
	index, err := batchInteger(data["index"], 32)
	if err != nil || index < 0 || index > int64(len(ids)) {
		return out, serviceDenied("invalid_netapp_vault_phase")
	}
	advance := func() {
		data["index"] = index + 1
		delete(data, "operation")
		delete(data, "operation_done")
		data["step"] = "pause"
		save()
	}
	if (phase == "policies" || phase == "vaults") && index < int64(len(ids)) {
		id := ids[index]
		field := "policyEnforced"
		if phase == "vaults" {
			field = "backupVaultId"
		} else if data["step"] == "policy" {
			field = "backupPolicyId"
		} else if data["step"] != "pause" {
			return out, serviceDenied("invalid_netapp_vault_update_step")
		}
		applied := func(raw map[string]any) bool {
			if object(raw["properties"])["provisioningState"] != "Succeeded" {
				return false
			}
			backup := object(object(object(raw["properties"])["dataProtection"])["backup"])
			if field == "policyEnforced" {
				return backup[field] == false
			}
			return backup[field] == nil || backup[field] == ""
		}
		if receipt := object(data["operation"]); receipt != nil {
			poll, err := a.client.netappPollUpdate(ctx, id, a.planned.Location, text(object(object(a.review["consumers"])[id])["uid"]), receipt)
			if err != nil {
				if ctx.Err() != nil {
					return out, err
				}
				own, readErr := a.volume(ctx, id, false)
				if readErr != nil || !applied(own) {
					return out, err
				}
			} else {
				data["operation"] = poll.Data
				if !poll.Done {
					save()
					return out, nil
				}
			}
			own, err := a.volume(ctx, id, false)
			if err != nil {
				return out, err
			}
			if !applied(own) {
				save()
				return out, nil
			}
			if field == "policyEnforced" {
				data["step"] = "policy"
				delete(data, "operation")
				save()
			} else {
				advance()
			}
			return out, nil
		}
		absent, err := a.current(ctx)
		if err != nil {
			return out, err
		}
		if absent {
			return out, serviceDenied("netapp_vault_removed_before_unassignment")
		}
		own, err := a.volume(ctx, id, false)
		if err != nil {
			return out, err
		}
		assignments, _ := netappAssignments(own)
		if object(own["properties"])["provisioningState"] != "Succeeded" {
			return out, serviceDenied("netapp_vault_volume_not_ready")
		}
		if phase == "policies" && assignments[netappBackupPolicyType] == "" || phase == "vaults" && assignments[netappVaultType] == "" {
			advance()
			return out, nil
		}
		if phase == "vaults" {
			if assignments[netappBackupPolicyType] != "" {
				return out, serviceDenied("netapp_vault_policy_resumed")
			}
			live, err := a.client.netappVaultMembers(ctx, a.planned.Identity.NativeID, a.planned.Location, a.members)
			if err != nil {
				return out, err
			}
			if len(live) != 0 {
				return out, serviceDenied("netapp_vault_backups_remain")
			}
		} else {
			if field == "backupPolicyId" && netappBackupEnforced(own) != false {
				return out, serviceDenied("netapp_vault_policy_resumed")
			}
			idle, err := a.client.netappBackupIdle(ctx, id)
			if err != nil {
				return out, err
			}
			if !idle {
				save()
				return out, nil
			}
			if field == "policyEnforced" && netappBackupEnforced(own) == false {
				data["step"] = "policy"
				save()
				return out, nil
			}
		}
		value := any("")
		if field == "policyEnforced" {
			value = false
		}
		wire, _ := json.Marshal(map[string]any{"properties": map[string]any{"dataProtection": map[string]any{"backup": map[string]any{field: value}}}})
		res, err := a.client.requestBody(ctx, "PATCH", apiURL(id, netappVersion), wire, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":" + field + ":" + id)})
		if err != nil {
			return out, err
		}
		receipt, err := a.client.netappUpdateReceipt(id, a.planned.Location, text(object(object(a.review["consumers"])[id])["uid"]), res)
		if err != nil {
			return out, err
		}
		data["operation"] = receipt
		save()
		return out, nil
	}
	if phase == "backups" && index < int64(len(ids)) {
		id := ids[index]
		if receipt := object(data["operation"]); receipt != nil {
			if data["operation_done"] != true {
				poll, err := a.client.netappPoll(ctx, id, a.planned.Location, receipt)
				if err != nil {
					if ctx.Err() == nil {
						_, readErr := a.client.netappRead(ctx, id, netappBackupType)
						if isNotFound(readErr) {
							advance()
							return out, nil
						}
					}
					return out, err
				}
				data["operation"], data["operation_done"] = poll.Data, poll.Done
				save()
				return out, nil
			}
			own, err := a.client.netappRead(ctx, id, netappBackupType)
			if isNotFound(err) {
				advance()
				return out, nil
			}
			if err != nil {
				return out, err
			}
			uid, created := a.client.netappLeafIdentity(netappBackupType, own.data)
			entry := object(a.members[id])
			if uid != entry["uid"] || created != entry["created"] {
				return out, serviceDenied("netapp_vault_backup_recreated")
			}
			save()
			return out, nil
		}
		absent, err := a.current(ctx)
		if err != nil {
			return out, err
		}
		if absent {
			return out, serviceDenied("netapp_vault_removed_before_backups")
		}
		gone, err := a.backup(ctx, req, id, false)
		if err != nil {
			return out, err
		}
		if gone {
			advance()
			return out, nil
		}
		source := text(object(a.members[id])["source"])
		_, err = a.client.netappRead(ctx, source, netappVolumeType)
		if err != nil && !isNotFound(err) {
			return out, err
		}
		if err == nil {
			idle, err := a.client.netappBackupIdle(ctx, source)
			if err != nil {
				return out, err
			}
			if !idle {
				save()
				return out, nil
			}
		}
		res, err := a.client.requestBody(ctx, "DELETE", apiURL(id, netappVersion), nil, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":backup:" + id)})
		if err != nil {
			return out, err
		}
		receipt, err := a.client.netappDeleteReceipt(id, a.planned.Location, res)
		if err != nil {
			return out, err
		}
		data["operation"] = receipt
		save()
		return out, nil
	}
	if phase == "policies" || phase == "backups" {
		if phase == "policies" {
			data["phase"] = "backups"
		} else {
			data["phase"] = "vaults"
		}
		data["index"] = 0
		data["step"] = "pause"
		save()
		return out, nil
	}
	if phase == "vaults" {
		absent, err := a.current(ctx)
		if err != nil {
			return out, err
		}
		for _, id := range ids {
			own, err := a.volume(ctx, id, true)
			if err != nil {
				return out, err
			}
			assignments, _ := netappAssignments(own)
			if assignments[netappBackupPolicyType] != "" || object(own["properties"])["provisioningState"] != "Succeeded" {
				return out, serviceDenied("netapp_vault_volume_still_assigned")
			}
		}
		if !absent {
			members, err := a.client.netappVaultMembers(ctx, a.planned.Identity.NativeID, a.planned.Location, a.members)
			if err != nil {
				return out, err
			}
			if len(members) != 0 {
				return out, serviceDenied("netapp_vault_backups_remain")
			}
			res, err := a.client.requestBody(ctx, "DELETE", apiURL(a.planned.Identity.NativeID, netappVersion), nil, map[string]string{"x-ms-client-request-id": azureRequestID(req.IdempotencyKey + ":vault:" + a.planned.Identity.NativeID)})
			if err != nil {
				return out, err
			}
			receipt, err := a.client.netappDeleteReceipt(a.planned.Identity.NativeID, a.planned.Location, res)
			if err != nil {
				return out, err
			}
			data["operation"] = receipt
		}
		data["phase"] = "deleting"
		data["index"] = 0
		save()
		return out, nil
	}
	if phase != "deleting" {
		return out, serviceDenied("invalid_netapp_vault_phase")
	}
	if receipt := object(data["operation"]); receipt != nil && data["operation_done"] != true {
		poll, err := a.client.netappPoll(ctx, a.planned.Identity.NativeID, a.planned.Location, receipt)
		if err != nil {
			return out, err
		}
		data["operation"], data["operation_done"] = poll.Data, poll.Done
		save()
		return out, nil
	}
	read, err := a.Readback(ctx, req)
	out.Done = err == nil && !read.Exists
	return out, err
}

func (*netappVaultAction) DeletionCheckTimeout() time.Duration { return 24 * time.Hour }
