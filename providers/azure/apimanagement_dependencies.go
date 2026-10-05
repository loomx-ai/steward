package azure

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Product/user deletion can remove subscriptions, and API deletion can remove
// revisions. Keep those native cascade flags false and review separate deletes.
// Other references use the same prerequisite path without claiming ownership.
func apimIncomingKinds(target string) []string {
	if !isAPIMType(target) || isAPIMAssociation(target) {
		return nil
	}
	var suffixes []string
	switch strings.ToLower(last(target)) {
	case "apis":
		suffixes = []string{"/apis", "/subscriptions", "/products/apiLinks", "/tags/apiLinks", "/products/apis", "/gateways/apis"}
	case "operations":
		suffixes = []string{"/tags/operationLinks"}
	case "apiversionsets":
		suffixes = []string{"/apis"}
	case "products":
		suffixes = []string{"/subscriptions", "/tags/productLinks"}
	case "users":
		suffixes = []string{"/subscriptions", "/groups/users", "/notifications/recipientUsers"}
	case "groups":
		suffixes = []string{"/products/groupLinks", "/products/groups"}
	case "tags":
		suffixes = []string{"/apis/tags", "/apis/operations/tags", "/products/tags"}
	case "loggers":
		suffixes = []string{"/diagnostics", "/apis/diagnostics"}
	case "certificates":
		suffixes = []string{"/backends", "/gateways/certificateAuthorities", "/gateways/hostnameConfigurations"}
	case "authorizationservers", "openidconnectproviders":
		suffixes = []string{"/apis"}
	}
	if slices.Contains([]string{"backends", "namedvalues", "policyfragments", "certificates", "loggers", "schemas", "authorizationproviders", "authorizations"}, strings.ToLower(last(target))) {
		suffixes = append(suffixes, "/policies", "/policyFragments", "/products/policies", "/apis/policies", "/apis/operations/policies", "/apis/resolvers/policies")
	}
	if strings.EqualFold(last(target), "namedValues") {
		suffixes = append(suffixes, "/backends", "/loggers")
	}
	if strings.EqualFold(last(target), "backends") {
		suffixes = append(suffixes, "/backends", "/apis")
	}
	var kinds []string
	for _, namespace := range []string{apimServiceType, apimWorkspaceType} {
		for _, suffix := range suffixes {
			if kind, exists := findType(namespace + suffix); exists {
				kinds = append(kinds, kind.NativeType)
			}
		}
	}
	if strings.EqualFold(target, apimServiceType) || strings.EqualFold(target, apimWorkspaceType) {
		kinds = append(kinds, apimGatewayConnectionType)
	}
	return kinds
}

func apimPrerequisite(target, referrer asset.Asset) bool {
	return isAPIMType(target.Identity.NativeType) && isAPIMType(referrer.Identity.NativeType) &&
		(apimRootID(target.Identity.NativeID) == apimRootID(referrer.Identity.NativeID) || apimGatewayPrerequisite(target, referrer)) &&
		slices.Contains(apimIncomingKinds(target.Identity.NativeType), referrer.Identity.NativeType) &&
		slices.Contains(stringValues(referrer.Normalized["_apim_references"]), strings.ToLower(target.Identity.NativeID)) &&
		!strings.HasPrefix(referrer.Identity.NativeID, target.Identity.NativeID+"/")
}

// apimIncomingIndex indexes the service's live references to target's kind.
func (c *client) apimIncomingIndex(ctx context.Context, target asset.Identity) (map[string][]serviceChild, error) {
	return c.apimIncomingIndexFor(ctx, apimRootID(target.NativeID), apimIncomingKinds(target.NativeType))
}

// apimIncomingIndexFor indexes the references the root's resources of kinds
// make. Concurrent delete checks of one service and kind set share one walk
// that starts after they arrive (liveShared), so the index stays live. The
// returned index is shared and must not be modified.
func (c *client) apimIncomingIndexFor(ctx context.Context, rootID string, kinds []string) (map[string][]serviceChild, error) {
	if len(kinds) == 0 {
		return nil, nil
	}
	kinds = slices.Compact(slices.Sorted(slices.Values(kinds)))
	return liveShared(ctx, c, "apim-incoming:"+rootID+"|"+strings.Join(kinds, ","), func() (map[string][]serviceChild, error) {
		return c.apimIncomingWalk(ctx, rootID, kinds)
	})
}

// Walk only native branches that can refer to these kinds. Scan the whole
// service so an ARM reference from another workspace cannot disappear from the
// review. The service's direct children (each API with its operations and
// policies) are walked concurrently; deeper levels stay serial.
func (c *client) apimIncomingWalk(ctx context.Context, rootID string, kinds []string) (map[string][]serviceChild, error) {
	resolved := map[string][]string{}
	indexes := map[string][]serviceChild{}
	var mu sync.Mutex // Guards resolved and the indexes cache across subtrees.
	var collect func(asset.Identity, map[string]any, bool) ([]serviceChild, error)
	collect = func(parent asset.Identity, raw map[string]any, concurrent bool) ([]serviceChild, error) {
		if err := apimReady(parent.NativeType, raw); err != nil {
			return nil, err
		}
		var branches []string
		apis := false
		for _, kind := range apimOwnedKinds(parent.NativeType) {
			if !slices.ContainsFunc(kinds, func(candidate string) bool { return candidate == kind || strings.HasPrefix(candidate, kind+"/") }) {
				continue
			}
			if isAPIMAPI(kind) {
				apis = true
			} else {
				branches = append(branches, kind)
			}
		}
		children, err := c.nativeServiceChildren(ctx, parent, raw, branches)
		if err != nil {
			return nil, err
		}
		if apis {
			values, err := c.apimAPIs(ctx, parent.NativeID)
			if err != nil {
				return nil, err
			}
			children = append(children, values...)
		}
		var result, nested []serviceChild
		for _, child := range children {
			if err := apimReady(child.kind, child.data); err != nil {
				return nil, err
			}
			if slices.Contains(kinds, child.kind) {
				mu.Lock()
				refs, err := c.apimResolvedReferences(ctx, child.kind, child.id, child.data, indexes, nil)
				if err == nil {
					resolved[child.id] = refs
				}
				mu.Unlock()
				if err != nil {
					return nil, err
				}
				result = append(result, child)
			}
			if slices.ContainsFunc(kinds, func(kind string) bool { return strings.HasPrefix(kind, child.kind+"/") }) {
				nested = append(nested, child)
			}
		}
		read := func(i int) ([]serviceChild, error) {
			return collect(asset.Identity{NativeID: nested[i].id, NativeType: nested[i].kind}, nested[i].data, false)
		}
		subtrees, errs := make([][]serviceChild, len(nested)), make([]error, len(nested))
		if concurrent {
			subtrees, errs = readConcurrently(len(nested), read)
		} else {
			for i := range nested {
				if subtrees[i], errs[i] = read(i); errs[i] != nil {
					break
				}
			}
		}
		for i := range nested {
			if errs[i] != nil {
				return nil, errs[i] // First failure in child order, as a serial walk.
			}
			result = append(result, subtrees[i]...)
		}
		after, err := c.apimResource(ctx, parent.NativeID)
		if err != nil {
			return nil, err
		}
		if c.privateConfiguration(apimSnapshot(parent.NativeType, raw)) != c.privateConfiguration(apimSnapshot(parent.NativeType, after)) {
			return nil, serviceDenied("apim_reference_parent_changed")
		}
		slices.SortFunc(result, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
		return result, nil
	}
	root, err := c.apimResource(ctx, rootID)
	if err != nil {
		return nil, err
	}
	parent := asset.Identity{NativeID: rootID, NativeType: apimServiceType}
	linkConfiguration := ""
	collectAll := func() ([]serviceChild, error) {
		children, err := collect(parent, root, true)
		if err != nil || !slices.Contains(kinds, apimGatewayConnectionType) {
			return children, err
		}
		gateways, err := c.subscriptionReferenceIndex(ctx, apimGatewayType)
		if err != nil {
			return nil, err
		}
		for _, gateway := range gateways {
			gatewayID, _, _ := parseID(text(gateway["id"]))
			live, err := c.apimResource(ctx, gatewayID)
			if err != nil {
				return nil, err
			}
			if err := apimListedIncarnation(apimGatewayType, gateway, live); err != nil {
				return nil, err
			}
			connections, err := collect(asset.Identity{NativeID: gatewayID, NativeType: apimGatewayType}, live, false)
			if err != nil {
				return nil, err
			}
			children = append(children, connections...)
		}
		linkConfiguration, err = c.apimWorkspaceLinks(ctx, rootID, children)
		if err != nil {
			return nil, err
		}
		slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
		return children, nil
	}
	first, err := collectAll()
	if err != nil {
		return nil, err
	}
	firstReferences := c.privateConfiguration(map[string]any{"references": resolved})
	firstLinks := linkConfiguration
	resolved, indexes = map[string][]string{}, map[string][]serviceChild{}
	second, err := collectAll()
	if err != nil {
		return nil, err
	}
	if firstLinks != linkConfiguration || firstReferences != c.privateConfiguration(map[string]any{"references": resolved}) || !slices.EqualFunc(first, second, func(a, b serviceChild) bool {
		return a.id == b.id && a.kind == b.kind && apimETag(a.kind, a.data) == apimETag(b.kind, b.data) && c.privateConfiguration(apimSnapshot(a.kind, a.data)) == c.privateConfiguration(apimSnapshot(b.kind, b.data))
	}) {
		return nil, serviceDenied("apim_references_changed")
	}
	index := map[string][]serviceChild{}
	for _, child := range second {
		for _, id := range resolved[child.id] {
			if !strings.HasPrefix(child.id, id+"/") {
				index[id] = append(index[id], child)
			}
		}
	}
	return index, nil
}

// Each service is walked once for the union of its targets' kinds; a target
// consumes only the referrers of its own kinds, as its own walk would find.
func (s *serviceCascades) contributeAPIMReferences(ctx context.Context, assets []asset.Asset, result *governance.Contribution) error {
	rootKinds := map[string][]string{}
	for _, target := range assets {
		if target.Identity.Provider == asset.ProviderAzure {
			root := apimRootID(target.Identity.NativeID)
			rootKinds[root] = append(rootKinds[root], apimIncomingKinds(target.Identity.NativeType)...)
		}
	}
	indexes := map[string]map[string][]serviceChild{}
	for _, target := range assets {
		kinds := apimIncomingKinds(target.Identity.NativeType)
		if target.Identity.Provider != asset.ProviderAzure || len(kinds) == 0 {
			continue
		}
		root := apimRootID(target.Identity.NativeID)
		index, exists := indexes[root]
		if !exists {
			var err error
			index, err = s.client.apimIncomingIndexFor(ctx, root, rootKinds[root])
			if err != nil {
				return err
			}
			indexes[root] = index
		}
		for _, child := range index[strings.ToLower(target.Identity.NativeID)] {
			if !slices.Contains(kinds, child.kind) {
				continue
			}
			evidence := map[string]any{graph.RelationshipEvidenceRequiredDeletion: true, graph.RelationshipEvidenceAuthority: graph.AuthorityAuthoritative, graph.RelationshipEvidenceDeletionOrder: graph.DeletionOrderTargetBeforeSource, "resource_type": child.kind, "instance_id": child.id}
			controllers := map[string]any{}
			var referrer *asset.Asset
			for i := range assets {
				candidate := &assets[i]
				if candidate.Identity.Provider != target.Identity.Provider || candidate.Identity.ConnectionID != target.Identity.ConnectionID || candidate.Identity.Partition != target.Identity.Partition {
					continue
				}
				if isAPIMType(candidate.Identity.NativeType) && strings.HasPrefix(target.Identity.NativeID, candidate.Identity.NativeID+"/") && strings.HasPrefix(child.id, candidate.Identity.NativeID+"/") {
					controllers[string(candidate.ID)] = true
				}
				if strings.EqualFold(candidate.Identity.NativeType, child.kind) && strings.EqualFold(candidate.Identity.NativeID, child.id) {
					if referrer != nil {
						return serviceDenied("ambiguous_apim_reference")
					}
					referrer = candidate
				}
			}
			evidence[graph.RelationshipEvidenceDeletionCascadeControllers] = controllers
			if referrer == nil {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: target.Identity.Provider, ConnectionID: target.Identity.ConnectionID, NativeType: child.kind, NativeID: child.id, ControllerID: target.ID, Relationship: graph.RelationshipDependsOn, Evidence: evidence})
				continue
			}
			if !apimPrerequisite(target, *referrer) {
				return serviceDenied("apim_reference_changed")
			}
			if err := serviceIncarnation(*referrer, child.data); err != nil {
				return err
			}
			if err := s.client.servicePrivateIncarnation(*referrer, child.data); err != nil {
				return err
			}
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: referrer.ID, Type: graph.RelationshipDependsOn, Source: "azure:apim-reference", Evidence: evidence, Confidence: 1})
		}
	}
	return nil
}

func (a *action) apimReferencesAbsent(ctx context.Context, request contracts.ActionRequest) error {
	if !isAPIMType(a.kind.NativeType) {
		return nil
	}
	if err := a.servicePrerequisitesAbsent(ctx, request); err != nil {
		return err
	}
	index, err := a.client.apimIncomingIndex(ctx, request.Asset.Identity)
	if err != nil {
		return err
	}
	if len(index[a.id]) != 0 {
		return serviceDenied("apim_reference_requires_prior_deletion")
	}
	return nil
}
