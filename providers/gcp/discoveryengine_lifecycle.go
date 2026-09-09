package gcp

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) discoveryChildren(ctx context.Context, parent asset.Identity, planned map[string]any) ([]serviceChild, error) {
	rule, ok := serviceCascadeRules[parent.NativeType]
	if !ok {
		return nil, nil
	}
	verify := func() error {
		live, err := c.discoveryRead(ctx, parent.NativeType, parent.NativeID)
		if err != nil {
			return err
		}
		return discoverySameResource(parent.NativeType, planned, live)
	}
	if err := verify(); err != nil {
		return nil, err
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	var result []serviceChild
	var rechecks []func() error
	seen := map[string]bool{}
	for _, childType := range rule.children {
		childKind, _ := findType(childType)
		for _, method := range childKind.ListOperations {
			if discoveryParentKind(method) != parent.NativeType {
				continue
			}
			if childType == discoverySiteType {
				if planned["contentConfig"] != "NO_CONTENT" {
					continue
				}
				id := parent.NativeID + "/siteSearchEngine"
				live, err := c.discoveryRead(ctx, childType, id)
				if err != nil {
					return nil, err
				}
				result = append(result, serviceChild{kind: childType, id: id, data: live})
				continue
			}
			operation, _ := metadata.catalog.Operation(method)
			parameters := map[string]any{"parent": strings.TrimPrefix(parent.NativeID, "//"+discoveryHost+"/")}
			records, err := c.nativeList(ctx, operation, parameters, childKind.Collection)
			if err != nil {
				return nil, err
			}
			generation := map[string]string{}
			for _, record := range records {
				id, err := c.discoveryID(childType, text(record["name"]))
				if err != nil {
					return nil, err
				}
				prefix := parent.NativeID + "/" + childKind.Collection + "/"
				if !strings.HasPrefix(id, prefix) || strings.Contains(strings.TrimPrefix(id, prefix), "/") || seen[id] {
					return nil, groupDenied("discoveryengine_child_identity_invalid")
				}
				seen[id] = true
				live, err := c.discoveryRead(ctx, childType, id)
				if err != nil {
					return nil, err
				}
				if err := discoverySameResource(childType, record, live); err != nil {
					return nil, err
				}
				generation[id] = discoveryConfiguration(record)
				result = append(result, serviceChild{kind: childType, id: id, data: live, direct: slices.Contains(rule.directChildren, childType)})
			}
			rechecks = append(rechecks, func() error {
				again, err := c.nativeList(ctx, operation, parameters, childKind.Collection)
				if err != nil {
					return err
				}
				remaining := map[string]string{}
				for id, proof := range generation {
					remaining[id] = proof
				}
				for _, record := range again {
					id, err := c.discoveryID(childType, text(record["name"]))
					if err != nil {
						return err
					}
					if remaining[id] == "" || remaining[id] != discoveryConfiguration(record) {
						return groupDenied("discoveryengine_children_changed")
					}
					delete(remaining, id)
				}
				if len(remaining) > 0 {
					return groupDenied("discoveryengine_children_changed")
				}
				return nil
			})
		}
	}
	for _, check := range rechecks {
		if err := check(); err != nil {
			return nil, err
		}
	}
	if err := verify(); err != nil {
		return nil, err
	}
	slices.SortFunc(result, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
	return result, nil
}

// Applications reference data stores; deleting an application leaves its data
// stores intact. Enforce the native requirement without silently patching an
// application's dataStoreIds or deleting an unselected application.
func (c *client) discoveryDataStoreUnused(ctx context.Context, id string) error {
	parent := id[:strings.LastIndex(id, "/dataStores/")]
	metadata, err := providerData()
	if err != nil {
		return err
	}
	operation, _ := metadata.catalog.Operation("discoveryengine.projects.locations.collections.engines.list")
	engines, err := c.nativeList(ctx, operation, map[string]any{"parent": strings.TrimPrefix(parent, "//"+discoveryHost+"/")}, "engines")
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, data := range engines {
		engine, err := c.discoveryID(discoveryEngineType, text(data["name"]))
		if err != nil {
			return err
		}
		if !strings.HasPrefix(engine, parent+"/engines/") || seen[engine] {
			return groupDenied("discoveryengine_engine_list_invalid")
		}
		seen[engine] = true
		live, err := c.discoveryRead(ctx, discoveryEngineType, engine)
		if err != nil {
			return err
		}
		if err := discoverySameResource(discoveryEngineType, data, live); err != nil {
			return err
		}
		refs, err := c.discoveryReferences(discoveryEngineType, engine, live)
		if err != nil {
			return err
		}
		if slices.Contains(refs[discoveryDataStoreType], id) {
			return groupDenied("discoveryengine_datastore_in_use")
		}
	}
	return nil
}

func (a *action) discoveryActionIdentity(request contracts.ActionRequest) error {
	if !isDiscovery(a.kind.NativeType) {
		return nil
	}
	if request.Asset.Identity != a.identity || request.Asset.Identity.Provider != asset.ProviderGCP || text(request.Asset.Normalized[discoveryProof]) == "" {
		return groupDenied("discoveryengine_action_identity_changed")
	}
	endpoint, err := a.client.resourceURL(a.kind, request.Asset.Identity.NativeID)
	if err != nil || endpoint != a.endpoint {
		return groupDenied("discoveryengine_action_identity_changed")
	}
	if err := a.client.discoveryIdentity(a.kind.NativeType, a.identity.NativeID, request.Asset.Normalized); err != nil {
		return err
	}
	if _, err := groupImpacts(request); err != nil {
		return err
	}
	seenAssets := map[asset.AssetID]bool{request.Asset.ID: true}
	for _, impact := range request.LifecycleImpacts {
		if seenAssets[impact.Asset.ID] {
			return groupDenied("discoveryengine_duplicate_impact")
		}
		seenAssets[impact.Asset.ID] = true
		parentKind, parentID, err := a.client.discoveryParent(impact.Asset.Identity.NativeType, impact.Asset.Identity.NativeID)
		if err != nil {
			return err
		}
		parent := request.Asset
		if impact.ControllerID != parent.ID {
			for _, candidate := range request.LifecycleImpacts {
				if candidate.Asset.ID == impact.ControllerID {
					parent = candidate.Asset
					break
				}
			}
		}
		if parent.Identity.NativeType != parentKind || parent.Identity.NativeID != parentID {
			return groupDenied("discoveryengine_impact_parent_changed")
		}
		if !impact.Delete || !a.serviceImpactDescendant(request, impact) || !isDiscovery(impact.Asset.Identity.NativeType) || text(impact.Asset.Normalized[discoveryProof]) == "" {
			return groupDenied("discoveryengine_impact_invalid")
		}
	}
	return nil
}

func (a *action) discoveryPreflight(ctx context.Context, request contracts.ActionRequest, live map[string]any, checkDependencies bool) error {
	if !isDiscovery(a.kind.NativeType) {
		return nil
	}
	if err := a.discoveryActionIdentity(request); err != nil {
		return err
	}
	if err := a.client.discoveryIdentity(a.kind.NativeType, request.Asset.Identity.NativeID, live); err != nil {
		return err
	}
	if err := discoverySameResource(a.kind.NativeType, request.Asset.Normalized, live); err != nil {
		return err
	}
	if checkDependencies {
		if err := a.servicePrerequisitesAbsent(ctx, request); err != nil {
			return err
		}
		if parent := text(request.Asset.Normalized["_discoveryengine_parent_id"]); parent != "" {
			parentKind := text(request.Asset.Normalized["_discoveryengine_parent_type"])
			expectedKind, expectedParent, err := a.client.discoveryParent(a.kind.NativeType, a.identity.NativeID)
			if err != nil || parentKind != expectedKind || parent != expectedParent {
				return groupDenied("discoveryengine_parent_invalid")
			}
			if err := a.client.verifyDiscoveryParent(ctx, productTarget{ParentType: parentKind, ParentID: parent, ParentConfiguration: text(request.Asset.Normalized[discoveryParentProof]), ParentContainerChain: text(request.Asset.Normalized["_discoveryengine_ancestors"])}); err != nil {
				return err
			}
		} else if a.kind.NativeType != discoveryCollectionType {
			return groupDenied("discoveryengine_parent_proof_missing")
		}
		if a.kind.NativeType == discoveryDataStoreType {
			return a.client.discoveryDataStoreUnused(ctx, request.Asset.Identity.NativeID)
		}
	}
	return nil
}
