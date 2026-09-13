package azure

import (
	"context"
	"slices"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

type batchMember struct {
	id, kind, parent string
	raw              map[string]any
	direct           bool
}

type batchTopology struct {
	account batchAccountContext
	members map[string]batchMember
}

func (t batchTopology) fingerprint(c *client) string {
	values := map[string]any{"account": batchAccountBinding(c, t.account)}
	for id, member := range t.members {
		values[id] = map[string]any{"kind": member.kind, "parent": member.parent, "direct": member.direct, "configuration": batchSnapshot(member.kind, member.raw)}
	}
	return c.privateConfiguration(values)
}

// A schedule's ListJobsFromSchedule is the authority for membership. Job names
// and the optional recentJob URL are not ownership proofs. Read the complete
// account twice so a new task, job or auto pool cannot silently enter a plan.
func (c *client) batchTopology(ctx context.Context, account batchAccountContext) (batchTopology, error) {
	collect := func() (batchTopology, error) {
		result := batchTopology{account: account, members: map[string]batchMember{}}
		add := func(id, kind, parent string, raw map[string]any, direct bool) error {
			id = strings.ToLower(id)
			if _, exists := result.members[id]; exists {
				return serviceDenied("duplicate_batch_topology_member")
			}
			result.members[id] = batchMember{id: id, kind: kind, parent: parent, raw: raw, direct: direct}
			return nil
		}
		add(account.id, batchAccountType, "", account.raw, false)
		children, err := c.nativeServiceChildren(ctx, asset.Identity{NativeID: account.id, NativeType: batchAccountType}, account.raw, []string{batchApplicationType, batchPECType, batchPerimeterType})
		if err != nil {
			return result, err
		}
		pools, _, err := c.batchPools(ctx, account)
		if err != nil {
			return result, err
		}
		children = append(children, pools...)
		for _, child := range children {
			if err := add(child.id, child.kind, account.id, child.data, child.kind != batchPerimeterType); err != nil {
				return result, err
			}
			if child.kind == batchApplicationType {
				packages, err := c.nativeServiceChildren(ctx, asset.Identity{NativeID: child.id, NativeType: child.kind}, child.data, []string{batchPackageType})
				if err != nil {
					return result, err
				}
				for _, pkg := range packages {
					if err := add(pkg.id, pkg.kind, child.id, pkg.data, true); err != nil {
						return result, err
					}
				}
			}
			if child.kind == batchPoolType {
				nodes, _, err := c.batchListedData(ctx, account, batchNodeType, "Nodes_ListNodes", map[string]any{"poolId": last(child.id)})
				if err != nil {
					return result, err
				}
				for _, node := range nodes {
					if err := add(text(node["url"]), batchNodeType, child.id, node, false); err != nil {
						return result, err
					}
					members, err := c.batchVMTree(ctx, account, node)
					if err != nil {
						return result, err
					}
					for _, member := range members {
						if err := add(member.id, member.kind, member.parent, member.raw, false); err != nil {
							return result, err
						}
					}
				}
			}
		}
		schedules, _, err := c.batchListedData(ctx, account, batchScheduleType, "JobSchedules_ListJobSchedules", nil)
		if err != nil {
			return result, err
		}
		for _, raw := range schedules {
			if err := add(text(raw["url"]), batchScheduleType, account.id, raw, true); err != nil {
				return result, err
			}
		}
		jobs, _, err := c.batchListedData(ctx, account, batchJobType, "Jobs_ListJobs", nil)
		if err != nil {
			return result, err
		}
		for _, raw := range jobs {
			id := strings.ToLower(text(raw["url"]))
			if err := add(id, batchJobType, account.id, raw, true); err != nil {
				return result, err
			}
			tasks, _, err := c.batchListedData(ctx, account, batchTaskType, "Tasks_ListTasks", map[string]any{"jobId": text(raw["id"])})
			if err != nil {
				return result, err
			}
			for _, task := range tasks {
				if err := add(text(task["url"]), batchTaskType, id, task, false); err != nil {
					return result, err
				}
			}
		}
		for _, schedule := range schedules {
			owned, _, err := c.batchListedData(ctx, account, batchJobType, "Jobs_ListJobsFromSchedule", map[string]any{"jobScheduleId": text(schedule["id"])})
			if err != nil {
				return result, err
			}
			for _, raw := range owned {
				id := strings.ToLower(text(raw["url"]))
				job, exists := result.members[id]
				if !exists || job.kind != batchJobType || job.parent != account.id || c.privateConfiguration(batchSnapshot(batchJobType, raw)) != c.privateConfiguration(batchSnapshot(batchJobType, job.raw)) {
					return result, serviceDenied("batch_schedule_job_indexes_disagree")
				}
				job.parent, job.direct = strings.ToLower(text(schedule["url"])), false
				result.members[id] = job
			}
		}
		if err := result.autoPools(); err != nil {
			return result, err
		}
		current, err := c.batchAccount(ctx, account.id)
		if err != nil {
			return result, err
		}
		if batchAccountBinding(c, current) != batchAccountBinding(c, account) {
			return result, serviceDenied("batch_account_changed_during_walk")
		}
		return result, nil
	}
	first, err := collect()
	if err != nil {
		return first, err
	}
	second, err := collect()
	if err != nil {
		return second, err
	}
	if first.fingerprint(c) != second.fingerprint(c) {
		return second, serviceDenied("batch_membership_changed_during_walk")
	}
	return second, nil
}

func (t batchTopology) autoPools() error {
	for _, job := range t.members {
		if job.kind != batchJobType {
			continue
		}
		poolInfo := object(job.raw["poolInfo"])
		auto := object(poolInfo["autoPoolSpecification"])
		schedule := t.members[job.parent]
		if len(auto) == 0 && schedule.kind == batchScheduleType {
			scheduleAuto := object(object(object(schedule.raw["jobSpecification"])["poolInfo"])["autoPoolSpecification"])
			if len(scheduleAuto) != 0 && scheduleAuto["keepAlive"] != true {
				// A job can be redirected to an independent pool. The schedule's
				// specification alone does not prove the actual pool's lifetime.
				return serviceDenied("batch_auto_pool_ownership_unproven")
			}
		}
		if len(auto) == 0 {
			continue
		}
		if value := auto["keepAlive"]; value != nil {
			keep, ok := value.(bool)
			if !ok {
				return serviceDenied("invalid_batch_auto_pool_retention")
			}
			if keep {
				continue
			}
		}
		owner := job.id
		switch auto["poolLifetimeOption"] {
		case "job":
		case "jobschedule":
			if schedule.kind != batchScheduleType {
				return serviceDenied("batch_auto_pool_schedule_missing")
			}
			owner = schedule.id
		default:
			return serviceDenied("invalid_batch_auto_pool_lifetime")
		}
		poolID := text(object(job.raw["executionInfo"])["poolId"])
		if poolID == "" {
			// No executionInfo means there is no proven allocated auto pool.
			// A poolId prefix must never claim an existing pool by coincidence.
			continue
		}
		if !batchNamePattern.MatchString(poolID) {
			return serviceDenied("invalid_batch_auto_pool_id")
		}
		id := t.account.id + "/pools/" + strings.ToLower(poolID)
		pool, exists := t.members[id]
		if !exists {
			continue // An expired auto pool can be absent from the native list.
		}
		if pool.kind != batchPoolType || (pool.parent != t.account.id && pool.parent != owner) {
			return serviceDenied("ambiguous_batch_auto_pool_owner")
		}
		pool.parent, pool.direct = owner, false
		t.members[id] = pool
	}
	return nil
}

func (t batchTopology) children(id string) []batchMember {
	var children []batchMember
	for _, member := range t.members {
		if member.parent == id {
			children = append(children, member)
		}
	}
	slices.SortFunc(children, func(a, b batchMember) int { return strings.Compare(a.id, b.id) })
	return children
}

func (s *serviceCascades) contributeBatch(ctx context.Context, assets []asset.Asset, result *governance.Contribution) error {
	accounts := map[string]batchAccountContext{}
	selected := map[string]asset.Asset{}
	for _, value := range assets {
		if value.Identity.Provider != asset.ProviderAzure || (!isBatchType(value.Identity.NativeType) && !batchVMKind(value.Identity.NativeType)) {
			continue
		}
		id := strings.ToLower(value.Identity.NativeID)
		if _, exists := selected[id]; exists {
			return serviceDenied("ambiguous_batch_asset")
		}
		selected[id] = value
		if !isBatchType(value.Identity.NativeType) {
			continue
		}
		accountID := text(value.Normalized["_batch_account"])
		if _, exists := accounts[accountID]; !exists {
			account, err := s.client.batchAccount(ctx, accountID)
			if err != nil {
				return err
			}
			accounts[accountID] = account
		}
		if err := batchAssetAccount(s.client, value, accounts[accountID]); err != nil {
			return err
		}
	}
	accountIDs := make([]string, 0, len(accounts))
	for id := range accounts {
		accountIDs = append(accountIDs, id)
	}
	sort.Strings(accountIDs)
	for _, accountID := range accountIDs {
		topology, err := s.client.batchTopology(ctx, accounts[accountID])
		if err != nil {
			return err
		}
		for _, value := range assets {
			if value.Identity.Provider != asset.ProviderAzure {
				continue
			}
			id := strings.ToLower(value.Identity.NativeID)
			member, exists := topology.members[id]
			if !isBatchType(value.Identity.NativeType) && !exists || isBatchType(value.Identity.NativeType) && text(value.Normalized["_batch_account"]) != accountID {
				continue
			}
			if !exists || member.kind != value.Identity.NativeType {
				return serviceDenied("batch_inventory_membership_changed")
			}
			if err := topology.verifyAsset(s.client, value, member); err != nil {
				return err
			}
			for _, child := range topology.children(id) {
				evidence := map[string]any{"resource_type": child.kind, "instance_id": child.id, "delete_by_default": true, "retention_supported": false, graph.LifecycleEvidenceControllerDeleteGuaranteed: true, graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
				policy := graph.CleanupDelegate
				if child.direct {
					policy = graph.CleanupDirect
					delete(evidence, graph.LifecycleEvidenceControllerDeleteGuaranteed)
					delete(evidence, graph.LifecycleEvidenceControllerVerifiesManagedAbsence)
				}
				target, found := selected[child.id]
				if !found {
					result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: value.Identity.Provider, ConnectionID: value.Identity.ConnectionID, NativeType: child.kind, NativeID: child.id, ControllerID: value.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
					continue
				}
				if target.Identity.ConnectionID != value.Identity.ConnectionID || target.Identity.Partition != value.Identity.Partition || target.Identity.NativeType != child.kind {
					return serviceDenied("batch_child_identity_changed")
				}
				if err := topology.verifyAsset(s.client, target, child); err != nil {
					return err
				}
				mapping, _ := findType(child.kind)
				directAllowed := isBatchType(child.kind) && !mapping.ReadOnly && batchProtection(child.kind, child.raw) == ""
				if child.kind == batchPoolType && (member.kind == batchJobType || member.kind == batchScheduleType) {
					directAllowed = false // An expiring auto pool follows its lifetime controller.
				}
				result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: value.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: policy, DirectCleanupAllowed: directAllowed, EvidenceSource: serviceCascadeSource, Evidence: evidence, Confidence: 1})
				result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: value.ID, Type: graph.RelationshipAttachedTo, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
			}
		}
		if err := s.contributeBatchReferences(ctx, topology, selected, result); err != nil {
			return err
		}
	}
	return nil
}

func (t batchTopology) descendant(id, parent string) bool {
	seen := map[string]bool{}
	for id != "" && !seen[id] {
		if id == parent {
			return true
		}
		seen[id] = true
		id = t.members[id].parent
	}
	return false
}

// A retained job or task can still need a pool or an application version.
// These are deletion prerequisites, with an explicit selection choice; they
// are not resources owned by the pool or by the package.
func (s *serviceCascades) contributeBatchReferences(ctx context.Context, topology batchTopology, selected map[string]asset.Asset, result *governance.Contribution) error {
	ids := make([]string, 0, len(topology.members))
	for id := range topology.members {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		member := topology.members[id]
		refs, err := s.client.batchReferences(ctx, topology.account, id, member.kind, member.raw)
		if err != nil {
			return err
		}
		for _, kind := range []string{batchPoolType, batchApplicationType, batchPackageType, batchTaskType} {
			for _, targetID := range refs[kind] {
				target, found := selected[targetID]
				if !found || topology.descendant(id, targetID) || topology.descendant(targetID, id) {
					continue
				}
				evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: false, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource}
				source, exists := selected[id]
				if !exists {
					result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: target.Identity.Provider, ConnectionID: target.Identity.ConnectionID, NativeType: member.kind, NativeID: id, ControllerID: target.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
					continue
				}
				result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: source.ID, Type: graph.RelationshipDependsOn, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
			}
		}
	}
	return nil
}

func batchAssetAccount(c *client, value asset.Asset, account batchAccountContext) error {
	identity := value.Identity
	if identity.Provider != asset.ProviderAzure || !isBatchType(identity.NativeType) || text(value.Normalized["_batch_account"]) != account.id || text(value.Normalized["_batch_endpoint"]) != account.endpoint || text(value.Normalized["_batch_location"]) != account.location || text(value.Normalized["_batch_account_binding"]) != batchAccountBinding(c, account) || !strings.EqualFold(value.Location, account.location) {
		return serviceDenied("batch_account_binding_changed")
	}
	if isBatchDataType(identity.NativeType) {
		id, kind, endpoint, _, err := batchDataIdentity(identity.NativeID)
		if err != nil || id != strings.ToLower(identity.NativeID) || kind != identity.NativeType || endpoint != account.endpoint {
			return serviceDenied("batch_data_identity_changed")
		}
	} else {
		_, kind, err := parseID(identity.NativeID)
		if err != nil || batchKind(kind) != identity.NativeType || batchAccountID(identity.NativeID) != account.id {
			return serviceDenied("batch_arm_identity_changed")
		}
	}
	return nil
}

func batchProtection(kind string, raw map[string]any) string {
	if protectedAzureTags(object(raw["tags"])) {
		return "azure_protected_tag"
	}
	for _, entry := range array(raw["metadata"]) {
		pair := object(entry)
		if protectedAzureTags(map[string]any{text(pair["name"]): pair["value"]}) {
			return "azure_protected_tag"
		}
	}
	if kind == batchPerimeterType {
		return "azure_batch_managed_configuration"
	}
	return ""
}
