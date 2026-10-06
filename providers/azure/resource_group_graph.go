package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const resourceGroupCascadeSource = "azure:resource-group-native-delete"

// Group effects are activated only by selecting the group. They do not replace
// exclusive product ownership or turn independent member selection into group deletion.
//
// managed is managedGroupMembers(assets): the service/cluster contributors
// validate these native controller hints; managed groups must keep their
// owning product's complete lifecycle.
func (c *client) contributeResourceGroups(ctx context.Context, connection asset.ConnectionID, assets []asset.Asset, managed map[managedGroupMemberKey]bool) (out governance.Contribution, err error) {
	defer func() {
		if err != nil {
			out = governance.Contribution{}
			err = contracts.DependencyReadError(err)
		}
	}()
	type members struct {
		known     map[string]asset.Asset
		duplicate bool
	}
	groups := map[string][]int{} // lowercase group ID -> eligible groups
	buckets := map[int]*members{}
	for i, group := range assets {
		if group.Identity.ConnectionID != connection || group.Identity.Provider != asset.ProviderAzure || group.Identity.NativeType != groupType {
			continue
		}
		if managed[managedGroupKey(group.Identity, group.Identity.NativeID)] || text(group.Normalized["_resource_group_configuration"]) == "" || text(group.Normalized["_managed_group_owner"]) != "" {
			continue
		}
		id := strings.ToLower(group.Identity.NativeID)
		groups[id] = append(groups[id], i)
		buckets[i] = &members{known: map[string]asset.Asset{}}
	}
	// One pass over assets: inResourceGroup(id, g) holds exactly when the
	// lowercase g is the lowercase id or one of its "/"-delimited prefixes.
	for _, member := range assets {
		id := strings.ToLower(member.Identity.NativeID)
		for end := len(id); end >= 0; end = strings.LastIndexByte(id[:end], '/') {
			for _, g := range groups[id[:end]] {
				group, bucket := assets[g], buckets[g]
				if member.Identity.Provider != group.Identity.Provider || member.Identity.ConnectionID != group.Identity.ConnectionID || member.Identity.Partition != group.Identity.Partition || member.ID == group.ID {
					continue
				}
				if _, exists := bucket.known[id]; exists {
					bucket.duplicate = true
				}
				if !bucket.duplicate {
					bucket.known[id] = member
				}
			}
		}
	}
	for g, group := range assets {
		bucket := buckets[g]
		if bucket == nil {
			continue
		}
		if bucket.duplicate {
			return out, serviceDenied("resource_group_graph_duplicate_asset")
		}
		known := bucket.known
		var previous map[string]map[string]any
		for pass := 0; pass < 2; pass++ {
			current, err := c.resourceGroupGraphMembers(withPassMemo(ctx), group, known)
			if err != nil {
				return out, err
			}
			if pass == 1 && c.privateConfiguration(map[string]any{"members": previous}) != c.privateConfiguration(map[string]any{"members": current}) {
				return out, serviceDenied("resource_group_graph_members_changed")
			}
			previous = current
		}
		for _, id := range slices.Sorted(maps.Keys(previous)) {
			raw := previous[id]
			_, kind, _ := deploymentStackMemberID(id)
			if mapping, ok := findType(kind); ok {
				kind = mapping.NativeType
			}
			evidence := map[string]any{"resource_type": kind, "instance_id": id, "delete_by_default": true, "retention_supported": false, graph.LifecycleEvidenceNativeDeleteEffect: true, graph.LifecycleEvidenceControllerDeleteGuaranteed: true, graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
			member, found := known[id]
			_, registered := findType(kind)
			if !found || !registered || !strings.EqualFold(member.Identity.NativeType, kind) {
				out.Unresolved = append(out.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: group.Identity.Provider, ConnectionID: group.Identity.ConnectionID, NativeID: id, NativeType: kind, ControllerID: group.ID, Relationship: graph.RelationshipMemberOf, Evidence: evidence})
				continue
			}
			if err := c.deploymentStackPreparedMember(member, raw, nil); err != nil {
				return out, err
			}
			// Independent extension prerequisites must retain their own product execution.
			if kind == diagnosticSettingsType || rbacResourceKind(kind) != "" {
				out.Relationships = append(out.Relationships, graph.Relationship{SourceAssetID: group.ID, TargetAssetID: member.ID, Type: graph.RelationshipDependsOn, Source: resourceGroupCascadeSource, Confidence: 1, Evidence: map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAutomaticSelection: true, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource}})
				continue
			}
			out.Bindings = append(out.Bindings, graph.LifecycleBinding{ControllerAssetID: group.ID, ManagedAssetID: member.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipReferenced, CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: true, EvidenceSource: resourceGroupCascadeSource, Evidence: evidence, Confidence: 1})
			out.Relationships = append(out.Relationships, graph.Relationship{SourceAssetID: member.ID, TargetAssetID: group.ID, Type: graph.RelationshipMemberOf, Source: resourceGroupCascadeSource, Evidence: evidence, Confidence: 1})
		}
	}
	return out, nil
}

// Preserve unknown members as unresolved graph references. Known resources use
// their own native reads, and Monitor's product indexes supplement ARM omissions.
func (c *client) resourceGroupGraphMembers(ctx context.Context, group asset.Asset, known map[string]asset.Asset) (map[string]map[string]any, error) {
	readGroup := func() error {
		live, err := c.deploymentStackMemberRead(ctx, group)
		if err != nil {
			return err
		}
		if text(group.Normalized["_resource_group_configuration"]) != c.resourceGroupConfiguration(live.data) || text(live.data["managedBy"]) != "" {
			return serviceDenied("resource_group_graph_review_changed")
		}
		return nil
	}
	if err := readGroup(); err != nil {
		return nil, err
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	operation, ok := metadata.catalog.Operation("Azure.ResourceManagementClient.Resources_ListByResourceGroup")
	if !ok {
		return nil, serviceDenied("resource_group_graph_operation_missing")
	}
	bound, err := catalog.BindREST(operation, map[string]any{"subscriptionId": c.subscription, "resourceGroupName": last(group.Identity.NativeID)})
	if err != nil {
		return nil, err
	}
	collection, _ := url.Parse(bound.URL)
	values := map[string]map[string]any{}
	pages := map[string]bool{}
	for next := bound.URL; next != ""; {
		if err := rbacListQuery(next, bound.URL); err != nil {
			return nil, err
		}
		page, _ := url.Parse(next)
		key := strings.ToLower(page.Scheme+"://"+page.Host+page.Path) + "?" + page.Query().Encode()
		if pages[key] || len(pages) >= 10000 {
			return nil, serviceDenied("resource_group_graph_repeated_page")
		}
		pages[key] = true
		rows, following, res, err := c.listPageResult(ctx, next, collection.Path)
		if err != nil {
			return nil, err
		}
		if operationLocation(res.header) != "" {
			return nil, serviceDenied("resource_group_graph_incomplete_index")
		}
		// Member reads run concurrently per page. A row's own rejection is
		// reported only after earlier rows' reads, as a serial loop would.
		var reads []func() (map[string]any, error)
		var readIDs []string
		flush := func(rejection error) error {
			results, err := readsInOrder(reads)
			if err != nil {
				return err
			}
			for i, raw := range results {
				values[readIDs[i]] = raw
			}
			reads, readIDs = nil, nil
			return rejection
		}
		for _, value := range rows {
			raw := object(value)
			id, kind, err := deploymentStackMemberID(text(raw["id"]))
			if err != nil || !inResourceGroup(id, group.Identity.NativeID) || id == strings.ToLower(group.Identity.NativeID) || !validResponseType(kind, text(raw["type"])) || values[id] != nil {
				return nil, flush(serviceDenied("resource_group_graph_invalid_member"))
			}
			_, registered := findType(kind)
			if member, found := known[id]; found && registered {
				if !strings.EqualFold(member.Identity.NativeType, kind) {
					return nil, flush(serviceDenied("resource_group_graph_kind_changed"))
				}
				listed := raw
				reads = append(reads, func() (map[string]any, error) {
					live, err := c.deploymentStackMemberRead(ctx, member)
					if err != nil {
						return nil, err
					}
					if err := serviceListedIncarnation(listed, live.data); err != nil {
						return nil, err
					}
					return live.data, nil
				})
				readIDs = append(readIDs, id)
			} else if monitorKind := monitorResourceKind(kind); monitorKind != "" {
				reads = append(reads, func() (map[string]any, error) {
					live, err := c.monitorResourceRead(ctx, monitorKind, id)
					return live.data, err
				})
				readIDs = append(readIDs, id)
			}
			values[id] = raw // listed row marks the ID until its read replaces it
		}
		if err := flush(nil); err != nil {
			return nil, err
		}
		next = following
	}
	extra, err := c.monitorManagedGroupMembers(ctx, strings.ToLower(group.Identity.NativeID), values)
	if err != nil {
		return nil, err
	}
	// IDs are checked in order before any read; rows at or after the first
	// invalid one are not read, and its error follows earlier read results.
	var invalid error
	var ids, kinds []string
	for _, raw := range extra {
		id := strings.ToLower(text(raw["id"]))
		_, _, kind, err := monitorResourceID(id)
		if err != nil {
			invalid = err
			break
		}
		ids, kinds = append(ids, id), append(kinds, kind)
	}
	reads, errs := readConcurrently(len(ids), func(i int) (response, error) { return c.monitorResourceRead(ctx, kinds[i], ids[i]) })
	for i, id := range ids {
		live, err := reads[i], errs[i]
		if err != nil {
			return nil, err
		}
		if c.privateConfiguration(monitorResourceSnapshot(kinds[i], extra[i])) != c.privateConfiguration(monitorResourceSnapshot(kinds[i], live.data)) {
			return nil, serviceDenied("resource_group_graph_monitor_changed")
		}
		values[id] = live.data
	}
	if invalid != nil {
		return nil, invalid
	}
	for id := range known {
		if len(strings.Split(strings.Trim(id, "/"), "/")) == 8 && values[id] == nil {
			return nil, serviceDenied("resource_group_graph_index_omitted_member")
		}
	}
	if err := readGroup(); err != nil {
		return nil, err
	}
	return values, nil
}

// readsInOrder runs reads at most eight at a time and, like the serial loop it
// replaces, reports the earliest failure in input order.
func readsInOrder(reads []func() (map[string]any, error)) ([]map[string]any, error) {
	results := make([]map[string]any, len(reads))
	errs := make([]error, len(reads))
	slots := make(chan struct{}, 8)
	var wg sync.WaitGroup
	for i, read := range reads {
		slots <- struct{}{}
		wg.Go(func() {
			defer func() { <-slots }()
			results[i], errs[i] = read()
		})
	}
	wg.Wait()
	for _, err := range errs {
		if err != nil {
			return nil, err
		}
	}
	return results, nil
}
