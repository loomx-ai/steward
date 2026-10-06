package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func monitorPrivateLinkTarget(kind string) bool {
	return strings.EqualFold(kind, applicationInsightsType) || strings.EqualFold(kind, dataCollectionEndpointType) || strings.EqualFold(kind, "Microsoft.OperationalInsights/workspaces")
}

func monitorPrivateLinkTargetSnapshot(raw map[string]any) map[string]any {
	snapshot := maps.Clone(raw)
	snapshot["id"] = strings.ToLower(text(raw["id"]))
	snapshot["name"] = last(text(snapshot["id"]))
	delete(snapshot, "type")
	delete(snapshot, "etag")
	if raw["systemData"] != nil {
		snapshot["systemData"] = maps.Clone(object(raw["systemData"]))
		for _, field := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
			delete(object(snapshot["systemData"]), field)
		}
	}
	properties := maps.Clone(object(raw["properties"]))
	snapshot["properties"] = properties
	for _, field := range []string{"privateLinkScopedResources", "PrivateLinkScopedResources", "provisioningState", "modifiedDate"} {
		delete(properties, field)
	}
	return snapshot
}

// The target's native read-only reverse references include foreign subscriptions.
// ScopeId is an opaque immutable identifier, never an ARM parent path.
func monitorPrivateLinkReverse(kind string, raw map[string]any) ([]string, bool, error) {
	field, idField, scopeField := "privateLinkScopedResources", "resourceId", "scopeId"
	if strings.EqualFold(kind, applicationInsightsType) {
		field, idField, scopeField = "PrivateLinkScopedResources", "ResourceId", "ScopeId"
	}
	for key := range object(raw["properties"]) {
		if strings.EqualFold(key, field) && key != field {
			return nil, true, serviceDenied("invalid_monitor_private_link_reverse_field")
		}
	}
	value, present := object(raw["properties"])[field]
	if !present {
		return nil, false, nil
	}
	rows, ok := value.([]any)
	if !ok {
		return nil, true, serviceDenied("invalid_monitor_private_link_reverse_index")
	}
	ids := []string{}
	for _, row := range rows {
		entry, ok := row.(map[string]any)
		id, typ, err := parseID(text(entry[idField]))
		if !ok || err != nil || !strings.EqualFold(typ, monitorScopedResourceType) || slices.Contains(ids, id) {
			return nil, true, serviceDenied("invalid_monitor_private_link_reverse_reference")
		}
		if value, present := entry[scopeField]; present && text(value) == "" {
			return nil, true, serviceDenied("invalid_monitor_private_link_scope_identifier")
		}
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, true, nil
}

// Reconcile the complete subscription scope index and every scope's native
// association LIST/GET twice. A target's omitted optional reverse collection
// cannot hide a local association. Foreign references remain explicit blockers.
func (c *client) monitorPrivateLinkIncoming(ctx context.Context, target asset.Identity) ([]serviceChild, error) {
	if !monitorPrivateLinkTarget(target.NativeType) {
		return nil, nil
	}
	kind, known := findType(target.NativeType)
	if !known {
		return nil, serviceDenied("unknown_monitor_private_link_target")
	}
	endpoint, err := c.resourceURL(kind, target.NativeID)
	if err != nil {
		return nil, err
	}
	live, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(live, target.NativeID, target.NativeType) {
		return nil, serviceDenied("monitor_private_link_target_changed")
	}
	reverse, present, err := monitorPrivateLinkReverse(target.NativeType, live.data)
	if err != nil {
		return nil, err
	}
	// The scan is independent of the target: one memo scope reads it once for
	// every target; a delete check (no memo) reads it live.
	scanned, err := memoized(ctx, "ampls-incoming:"+c.root(), func() ([]monitorPrivateLinkAssociation, error) { return c.monitorPrivateLinkAssociations(ctx) })
	if err != nil {
		return nil, err
	}
	incoming := []serviceChild{}
	for _, association := range scanned {
		if strings.EqualFold(association.linked, target.NativeID) {
			child := association.child
			child.direct = true
			incoming = append(incoming, child)
		}
	}
	if present {
		for _, child := range incoming {
			if !slices.Contains(reverse, child.id) {
				return nil, serviceDenied("monitor_private_link_indexes_disagree")
			}
		}
		for _, id := range reverse {
			if strings.HasPrefix(id, c.root()+"/") {
				if !slices.ContainsFunc(incoming, func(child serviceChild) bool { return child.id == id }) {
					return nil, serviceDenied("monitor_private_link_indexes_disagree")
				}
			} else {
				incoming = append(incoming, serviceChild{id: id, kind: monitorScopedResourceType, direct: true})
			}
		}
	}
	latest, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(latest, target.NativeID, target.NativeType) || c.privateConfiguration(live.data) != c.privateConfiguration(latest.data) {
		return nil, serviceDenied("monitor_private_link_target_changed")
	}
	slices.SortFunc(incoming, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
	return incoming, nil
}

type monitorPrivateLinkAssociation struct {
	child  serviceChild
	linked string
}

// monitorPrivateLinkAssociations reads the subscription scope index and every
// scope's association LIST/GET twice, and returns the second pass once both
// agree. Scopes are read concurrently and checked in index order.
func (c *client) monitorPrivateLinkAssociations(ctx context.Context) ([]monitorPrivateLinkAssociation, error) {
	read := func() ([]monitorPrivateLinkAssociation, string, error) {
		scopes, err := c.subscriptionReferenceIndex(ctx, monitorPrivateLinkType)
		if err != nil {
			return nil, "", err
		}
		kind, _ := findType(monitorPrivateLinkType)
		type scopeRead struct {
			configuration map[string]any
			associations  []monitorPrivateLinkAssociation
		}
		reads, errs := readConcurrently(len(scopes), func(i int) (scopeRead, error) {
			listed := scopes[i]
			id := strings.ToLower(text(listed["id"]))
			endpoint, err := c.resourceURL(kind, id)
			if err != nil {
				return scopeRead{}, err
			}
			live, err := c.request(ctx, "GET", endpoint)
			if err != nil {
				return scopeRead{}, err
			}
			if !validResourceResponse(live, id, monitorPrivateLinkType) || monitorPrivateLinkListed(monitorPrivateLinkType, listed, live.data) != nil {
				return scopeRead{}, serviceDenied("monitor_private_link_scope_index_changed")
			}
			read := scopeRead{configuration: map[string]any{id: monitorPrivateLinkSnapshot(monitorPrivateLinkType, live.data)}}
			children, err := c.nativeServiceChildren(ctx, asset.Identity{NativeType: monitorPrivateLinkType, NativeID: id}, live.data, []string{monitorScopedResourceType})
			if err != nil {
				return scopeRead{}, err
			}
			for _, child := range children {
				read.configuration[child.id] = monitorPrivateLinkSnapshot(child.kind, child.data)
				linked, err := monitorPrivateLinkReference(child.data)
				if err != nil {
					return scopeRead{}, err
				}
				read.associations = append(read.associations, monitorPrivateLinkAssociation{child, linked})
			}
			return read, nil
		})
		configuration := map[string]any{}
		associations := []monitorPrivateLinkAssociation{}
		for i, read := range reads {
			if errs[i] != nil {
				return nil, "", errs[i]
			}
			maps.Copy(configuration, read.configuration)
			associations = append(associations, read.associations...)
		}
		slices.SortFunc(associations, func(a, b monitorPrivateLinkAssociation) int { return strings.Compare(a.child.id, b.child.id) })
		return associations, c.privateConfiguration(configuration), nil
	}
	_, first, err := read()
	if err != nil {
		return nil, err
	}
	associations, second, err := read()
	if err != nil {
		return nil, err
	}
	if first != second {
		return nil, serviceDenied("monitor_private_link_incoming_index_changed")
	}
	return associations, nil
}

func monitorPrivateLinkPrerequisite(target, association asset.Asset) bool {
	if !monitorPrivateLinkTarget(target.Identity.NativeType) || association.Identity.NativeType != monitorScopedResourceType {
		return false
	}
	id, err := monitorPrivateLinkReference(map[string]any{"properties": association.Normalized})
	return err == nil && strings.EqualFold(id, target.Identity.NativeID)
}

// Azure requires disconnecting AMPLS before deleting a monitored resource.
// These graph edges request association deletion, without owning its scope.
// https://learn.microsoft.com/azure/azure-monitor/fundamentals/private-link-configure
func (s *serviceCascades) contributeMonitorPrivateLinkReferences(ctx context.Context, assets []asset.Asset, result *governance.Contribution) error {
	var targets []asset.Asset
	for _, target := range assets {
		if target.Identity.Provider == asset.ProviderAzure && monitorPrivateLinkTarget(target.Identity.NativeType) {
			targets = append(targets, target)
		}
	}
	// Targets are read concurrently and checked in order.
	reads, errs := readConcurrently(len(targets), func(i int) ([]serviceChild, error) {
		return s.client.monitorPrivateLinkIncoming(ctx, targets[i].Identity)
	})
	for i, target := range targets {
		incoming, err := reads[i], errs[i]
		if err != nil {
			return err
		}
		for _, child := range incoming {
			evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "resource_type": child.kind, "instance_id": child.id}
			var referrer *asset.Asset
			for i := range assets {
				candidate := &assets[i]
				if candidate.Identity.Provider == target.Identity.Provider && candidate.Identity.ConnectionID == target.Identity.ConnectionID && candidate.Identity.Partition == target.Identity.Partition && strings.EqualFold(candidate.Identity.NativeType, child.kind) && strings.EqualFold(candidate.Identity.NativeID, child.id) {
					if referrer != nil {
						return serviceDenied("ambiguous_monitor_private_link_association")
					}
					referrer = candidate
				}
			}
			if referrer == nil || child.data == nil {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: target.Identity.Provider, ConnectionID: target.Identity.ConnectionID, NativeType: child.kind, NativeID: child.id, ControllerID: target.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
				continue
			}
			if !monitorPrivateLinkPrerequisite(target, *referrer) {
				return serviceDenied("monitor_private_link_association_changed")
			}
			if err := serviceIncarnation(*referrer, child.data); err != nil {
				return err
			}
			if err := s.client.servicePrivateIncarnation(*referrer, child.data); err != nil {
				return err
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: referrer.ID, Type: graph.RelationshipDependsOn, Source: "azure:monitor-private-link-reference", Evidence: evidence, Confidence: 1})
		}
	}
	return nil
}

func (a *action) monitorPrivateLinkReferencesAbsent(ctx context.Context, request contracts.ActionRequest, raw map[string]any) error {
	if !monitorPrivateLinkTarget(a.kind.NativeType) {
		return nil
	}
	if expected := text(request.Asset.Normalized["_monitor_private_link_target_configuration"]); expected == "" || expected != a.client.privateConfiguration(monitorPrivateLinkTargetSnapshot(raw)) {
		return serviceDenied("monitor_private_link_target_configuration_changed")
	}
	if err := a.servicePrerequisitesAbsent(ctx, request); err != nil {
		return err
	}
	children, err := a.client.monitorPrivateLinkIncoming(ctx, request.Asset.Identity)
	if err != nil {
		return err
	}
	if len(children) != 0 {
		return serviceDenied("monitor_private_link_requires_association_unlink")
	}
	return nil
}
