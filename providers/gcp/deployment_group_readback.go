package gcp

import (
	"context"
	"encoding/json"
	"slices"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *action) infraGroupReadback(ctx context.Context, request contracts.ActionRequest) (read contracts.ReadbackResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	_, read, err = a.infraGroupObserve(ctx, request, true)
	return read, err
}

// The same native observations gate the metadata DELETE and final completion.
// Before DELETE, existing group/revision metadata is expected; deployments and
// their reviewed physical effects must already have reached the chosen outcome.
func (a *action) infraGroupObserve(ctx context.Context, request contracts.ActionRequest, includeMetadata bool) (map[string]any, contracts.ReadbackResult, error) {
	read := contracts.ReadbackResult{State: "group_delete"}
	members, policy, err := a.infraGroupActionIdentity(request)
	if err != nil {
		return nil, read, err
	}
	root, rootErr := a.client.infraRead(ctx, infraGroup, a.identity.NativeID)
	if rootErr != nil && !isNotFound(rootErr) {
		return nil, read, rootErr
	}
	if rootErr == nil {
		if _, _, err := infraGroupStates(root); err != nil {
			return nil, read, err
		}
		if err := a.client.infraGroupSame(request.Asset.Normalized, root, policy != "DETACH"); err != nil {
			return nil, read, err
		}
		read.Exists, read.State = includeMetadata, text(root["state"])
	}
	for _, member := range members {
		if member.Kind == infraGroupRevision || member.Absent {
			live, err := a.client.infraRead(ctx, member.Kind, member.ID)
			if isNotFound(err) {
				continue
			}
			if err != nil {
				return nil, read, err
			}
			if member.Absent || infraConfiguration(live) != member.Proof {
				return nil, read, groupDenied("infra_group_member_recreated_or_changed")
			}
			read.Exists = read.Exists || includeMetadata
		}
	}
	for _, impact := range request.LifecycleImpacts {
		if impact.ControllerID != request.Asset.ID || impact.Asset.Identity.NativeType != infraDeployment {
			continue
		}
		driver, err := a.infraChildDriver(impact.Asset)
		if err != nil {
			return nil, read, err
		}
		child := infraGroupChildRequest(request, impact.Asset)
		if policy == "DETACH" {
			live, err := a.client.infraRead(ctx, infraDeployment, impact.Asset.Identity.NativeID)
			if err != nil {
				return nil, read, contracts.DependencyReadError(err)
			}
			if err := infraSame(impact.Asset.Normalized, live); err != nil {
				return nil, read, err
			}
			current, err := a.client.infraSnapshot(ctx, infraDeployment, impact.Asset.Identity.NativeID, live)
			if err != nil {
				return nil, read, err
			}
			encoded, _ := json.Marshal(current)
			if infraManifestHash(infraConfiguration(live), string(encoded)) != impact.Asset.Normalized[infraSnapshotKey] {
				return nil, read, groupDenied("infra_group_retained_deployment_changed")
			}
			if err := driver.infraRetainedDescendants(ctx, child); err != nil {
				return nil, read, err
			}
		} else {
			childRead, err := driver.Readback(ctx, child)
			if err != nil {
				return nil, read, err
			}
			read.Exists = read.Exists || childRead.Exists
		}
	}
	revisions, err := a.infraGroupObservedRevisions(ctx, request, members, policy != "DETACH")
	if err != nil {
		return nil, read, err
	}
	read.Exists = read.Exists || includeMetadata && revisions
	again, err := a.client.infraRead(ctx, infraGroup, a.identity.NativeID)
	if err != nil && !isNotFound(err) {
		return nil, read, err
	}
	if err == nil {
		if _, _, err := infraGroupStates(again); err != nil {
			return nil, read, err
		}
		if rootErr != nil {
			return nil, read, groupDenied("infra_group_root_reappeared")
		}
		if err := a.client.infraGroupSame(request.Asset.Normalized, again, policy != "DETACH"); err != nil {
			return nil, read, err
		}
		read.Exists, read.State = read.Exists || includeMetadata, text(again["state"])
		root = again
	} else {
		root = nil
	}
	return root, read, nil
}

func (a *action) infraGroupObservedRevisions(ctx context.Context, request contracts.ActionRequest, members []infraMember, deprovision bool) (bool, error) {
	records, err := a.client.infraRecords(ctx, infraGroup, a.identity.NativeID, true)
	if isNotFound(err) {
		// A collection 404 is expected only once its containing group is absent.
		_, rootErr := a.client.infraRead(ctx, infraGroup, a.identity.NativeID)
		if isNotFound(rootErr) {
			return false, nil
		}
		if rootErr != nil {
			return false, rootErr
		}
		return false, groupDenied("infra_group_revision_collection_missing")
	}
	if err != nil {
		return false, err
	}
	var newRevisions int
	for _, record := range records {
		index := slices.IndexFunc(members, func(member infraMember) bool { return member.Kind == infraGroupRevision && member.ID == record.id })
		if index >= 0 {
			if infraConfiguration(record.data) != members[index].Proof {
				return false, groupDenied("infra_group_revision_changed")
			}
			continue
		}
		// One successful deprovision produces a new metadata revision. It must
		// describe this exact group, with the reviewed DAG and no deployments.
		newRevisions++
		if !deprovision || newRevisions > 1 {
			return false, groupDenied("infra_group_unreviewed_revision")
		}
		snapshot := object(record.data["snapshot"])
		if snapshot["provisioningState"] != "DEPROVISIONED" || snapshot["state"] != "ACTIVE" {
			return false, groupDenied("infra_group_unexpected_revision_outcome")
		}
		if err := a.client.infraGroupSame(request.Asset.Normalized, snapshot, true); err != nil {
			return false, err
		}
		created, _ := time.Parse(time.RFC3339Nano, text(record.data["createTime"]))
		rootCreated, _ := time.Parse(time.RFC3339Nano, text(request.Asset.Normalized["createTime"]))
		if !created.After(rootCreated) {
			return false, groupDenied("infra_group_revision_time_invalid")
		}
		for _, impact := range request.LifecycleImpacts {
			if impact.Asset.Identity.NativeType == infraGroupRevision {
				previous, _ := time.Parse(time.RFC3339Nano, text(impact.Asset.Normalized["createTime"]))
				if !created.After(previous) {
					return false, groupDenied("infra_group_revision_not_new")
				}
			}
		}
		units, err := a.client.infraGroupUnits(snapshot)
		if err != nil {
			return false, err
		}
		for _, unit := range units {
			if unit.Deployment != "" {
				return false, groupDenied("infra_group_completed_revision_has_deployments")
			}
		}
	}
	again, err := a.client.infraRecords(ctx, infraGroup, a.identity.NativeID, false)
	if isNotFound(err) {
		_, rootErr := a.client.infraRead(ctx, infraGroup, a.identity.NativeID)
		if isNotFound(rootErr) {
			// The group can disappear between complete reads while its DELETE
			// finishes. Keep the first observation pending until the next poll.
			return len(records) != 0, nil
		}
		if rootErr != nil {
			return false, rootErr
		}
		return false, groupDenied("infra_group_revision_collection_missing")
	}
	if err != nil {
		return false, err
	}
	if len(records) != len(again) {
		return false, groupDenied("infra_group_revisions_changed")
	}
	for i, record := range records {
		if record.id != again[i].id || infraConfiguration(record.data) != infraConfiguration(again[i].data) {
			return false, groupDenied("infra_group_revisions_changed")
		}
	}
	return len(records) != 0, nil
}
