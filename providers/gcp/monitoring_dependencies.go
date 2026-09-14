package gcp

import (
	"context"
	"encoding/hex"
	"slices"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const monitoringDependencySource = "gcp:monitoring-dependencies"

type monitoringDependencies struct {
	client     *client
	connection asset.ConnectionID
}

func (r *Runtime) MonitoringDependencies(ctx context.Context, connection asset.ConnectionID) (governance.Contributor, error) {
	c, err := r.resolve(ctx, connection)
	if err != nil {
		return nil, err
	}
	return &monitoringDependencies{client: c, connection: connection}, nil
}

// Reverse metrics-scope discovery is essential: a policy in another scoping
// project can query this project's checks. Neither own-project LIST nor an
// inventory snapshot proves those incoming references absent.
func (c *client) monitoringScopingProjects(ctx context.Context) ([]string, error) {
	if !firewallNumericID(c.number) {
		return nil, groupDenied("monitoring_project_number_invalid")
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	op, ok := metadata.catalog.Operation(metricsReverse)
	if !ok {
		return nil, groupDenied("monitoring_scope_operation_missing")
	}
	bound, err := catalog.BindREST(op, map[string]any{"monitoredResourceContainer": "projects/" + c.number})
	if err != nil {
		return nil, err
	}
	response, err := c.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if err := metricsComplete(response.Data); err != nil {
		return nil, err
	}
	values, ok := response.Data["metricsScopes"].([]any)
	if !ok || len(values) == 0 {
		return nil, groupDenied("metrics_scope_reverse_missing")
	}
	result := []string{}
	seen := map[string]bool{}
	for i, raw := range values {
		id, err := c.metricsID(metricsScopeType, text(object(raw)["name"]), false)
		project := last(id)
		if err != nil || !firewallNumericID(project) || seen[project] || i == 0 && project != c.number {
			return nil, groupDenied("metrics_scope_reverse_invalid")
		}
		seen[project] = true
		result = append(result, project)
	}
	slices.Sort(result)
	return result, nil
}

func (c *client) monitoringPolicyList(ctx context.Context) (map[string]map[string]any, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	op, ok := metadata.catalog.Operation("monitoring.projects.alertPolicies.list")
	if !ok {
		return nil, groupDenied("monitoring_policy_operation_missing")
	}
	rows, err := c.nativeList(ctx, op, map[string]any{"name": "projects/" + c.project, "pageSize": 100}, "alertPolicies")
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	result := map[string]map[string]any{}
	for _, row := range rows {
		id := c.canonicalName("//monitoring.googleapis.com/" + text(row["name"]))
		if result[id] != nil {
			return nil, groupDenied("monitoring_policy_duplicate")
		}
		if err := c.alertPolicyData(id, row); err != nil {
			return nil, err
		}
		result[id] = row
	}
	return result, nil
}

func monitoringPolicyReviews(values map[string]map[string]any) string {
	reviews := map[string]any{}
	for id, value := range values {
		reviews[id] = monitoringConfiguration(alertPolicyType, id, value)
	}
	return firewallDigest(reviews)
}
func (c *client) monitoringPolicies(ctx context.Context) (map[string]map[string]any, error) {
	projects, err := c.monitoringScopingProjects(ctx)
	if err != nil {
		return nil, err
	}
	result := map[string]map[string]any{}
	for _, project := range projects {
		reader := c
		if project != c.number {
			copy := *c
			copy.project, copy.number, copy.firewallParent, copy.identityParent = project, "", "", ""
			if _, err := copy.projectIdentity(ctx); err != nil {
				return nil, contracts.DependencyReadError(err)
			}
			reader = &copy
		}
		listed, err := reader.monitoringPolicyList(ctx)
		if err != nil {
			return nil, err
		}
		ids := make([]string, 0, len(listed))
		for id := range listed {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			live, err := reader.alertPolicyInventory(ctx, id, listed[id])
			if err != nil {
				return nil, err
			}
			if result[id] != nil {
				return nil, groupDenied("monitoring_policy_duplicate")
			}
			result[id] = live
		}
		again, err := reader.monitoringPolicyList(ctx)
		if err != nil {
			return nil, err
		}
		if monitoringPolicyReviews(listed) != monitoringPolicyReviews(again) {
			return nil, groupDenied("monitoring_policy_set_changed")
		}
	}
	again, err := c.monitoringScopingProjects(ctx)
	if err != nil {
		return nil, err
	}
	if !slices.Equal(projects, again) {
		return nil, groupDenied("monitoring_scoping_projects_changed")
	}
	return result, nil
}

func (h *monitoringDependencies) Contribute(ctx context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	var checks []asset.Asset
	for _, value := range assets {
		if value.ClosedAt == nil && value.Identity.Provider == asset.ProviderGCP && value.Identity.ConnectionID == h.connection && value.Identity.NativeType == uptimeType {
			checks = append(checks, value)
		}
	}
	if len(checks) == 0 {
		return result, nil
	}
	policies, err := h.client.monitoringPolicies(ctx)
	if err != nil {
		return result, err
	}
	ids := make([]string, 0, len(policies))
	for id := range policies {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, check := range checks {
		if !gcpPartition(check.Identity.Partition) {
			return result, groupDenied("monitoring_check_partition_invalid")
		}
		live, err := h.client.monitoringRead(ctx, uptimeType, check.Identity.NativeID)
		if err != nil {
			return result, contracts.DependencyReadError(err)
		}
		if uptimeConfiguration(check.Identity.NativeID, live) != text(check.Normalized[uptimeReview]) {
			return result, groupDenied("monitoring_configuration_changed")
		}
		for _, id := range ids {
			ref := alertPolicyUptimeReference(policies[id], last(check.Identity.NativeID))
			if ref == monitoringNoReference {
				continue
			}
			block := func(reason string) {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: check.Identity.Provider, ConnectionID: check.Identity.ConnectionID, ControllerID: check.ID, NativeType: alertPolicyType, NativeID: id, Relationship: graph.RelationshipDependsOn, Evidence: map[string]any{"reason": reason, "source": monitoringDependencySource}})
			}
			if ref == monitoringUnresolvedReference {
				block("monitoring_condition_reference_unresolved")
				continue
			}
			kind, _ := findType(alertPolicyType)
			if _, err := h.client.resourceURL(kind, id); err != nil {
				block("monitoring_foreign_policy_requires_own_connection")
				continue
			}
			policy, found, err := findManagedAsset(assets, check, alertPolicyType, id)
			if err != nil {
				return result, err
			}
			if !found || policy.ClosedAt != nil || text(policy.Normalized[alertPolicyReview]) != monitoringConfiguration(alertPolicyType, id, policies[id]) {
				block("monitoring_policy_refresh_required")
				continue
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: check.ID, TargetAssetID: policy.ID, Type: graph.RelationshipDependsOn, Source: monitoringDependencySource, Confidence: 1, Evidence: map[string]any{
				graph.RelationshipEvidenceRequiredDeletion:   true,
				graph.RelationshipEvidenceAutomaticSelection: false,
				graph.RelationshipEvidenceAuthority:          string(graph.AuthorityAuthoritative),
				graph.RelationshipEvidenceDeletionOrder:      graph.DeletionOrderTargetBeforeSource,
				"native_policy":                              id, "configuration": policy.Normalized[alertPolicyReview],
			}})
		}
	}
	return result, nil
}

func (a *action) monitoringPrerequisites(request contracts.ActionRequest) error {
	seen := map[string]bool{}
	for _, prerequisite := range request.PrerequisiteDeletions {
		p := prerequisite.Asset
		proof, err := hex.DecodeString(text(p.Normalized[alertPolicyReview]))
		if a.kind.NativeType != uptimeType || err != nil || len(proof) != 32 || p.ID == "" || p.ID == request.Asset.ID || !prerequisite.Delete || prerequisite.ControllerID != request.Asset.ID || p.Identity.Provider != a.identity.Provider || p.Identity.ConnectionID != a.identity.ConnectionID || p.Identity.Partition != a.identity.Partition || p.Identity.NativeType != alertPolicyType || seen[p.Identity.NativeID] {
			return groupDenied("monitoring_prerequisite_changed")
		}
		kind, _ := findType(alertPolicyType)
		// Foreign-project dependencies cannot be independently deleted through this
		// configured project. The graph retains them as unresolved boundaries.
		if _, err := a.client.resourceURL(kind, p.Identity.NativeID); err != nil {
			return err
		}
		seen[p.Identity.NativeID] = true
	}
	return nil
}
func (a *action) monitoringIncoming(ctx context.Context, request contracts.ActionRequest) error {
	if a.kind.NativeType != uptimeType {
		return nil
	}
	for _, p := range request.PrerequisiteDeletions {
		_, err := a.client.monitoringRead(ctx, alertPolicyType, p.Asset.Identity.NativeID)
		if !isNotFound(err) {
			if err != nil {
				return contracts.DependencyReadError(err)
			}
			return groupDenied("monitoring_prerequisite_still_exists")
		}
	}
	policies, err := a.client.monitoringPolicies(ctx)
	if err != nil {
		return err
	}
	for _, policy := range policies {
		switch alertPolicyUptimeReference(policy, last(a.identity.NativeID)) {
		case monitoringHasReference:
			return groupDenied("uptime_referenced_by_alert_policy")
		case monitoringUnresolvedReference:
			return groupDenied("monitoring_condition_reference_unresolved")
		}
	}
	return nil
}
