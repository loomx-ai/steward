package gcp

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const serviceCascadeSource = "gcp:service-cascade"

type serviceCascadeRule struct {
	children       []string
	directChildren []string
	forceParameter string
}

// These are documented native cascades, not an inference from resource nesting.
// New rules must cover the native child set, reviewed impact and final readback.
var serviceCascadeRules = map[string]serviceCascadeRule{
	dataformRepositoryType:                          {children: []string{"dataform.googleapis.com/Workspace", "dataform.googleapis.com/WorkflowConfig", "dataform.googleapis.com/ReleaseConfig", dataformInvocationType, "dataform.googleapis.com/CompilationResult"}, directChildren: []string{"dataform.googleapis.com/Workspace", "dataform.googleapis.com/WorkflowConfig", "dataform.googleapis.com/ReleaseConfig", dataformInvocationType}, forceParameter: "force"},
	"bigtableadmin.googleapis.com/Instance":         {children: []string{"bigtableadmin.googleapis.com/Cluster", "bigtableadmin.googleapis.com/Table"}},
	"managedkafka.googleapis.com/Cluster":           {children: []string{"managedkafka.googleapis.com/Topic"}},
	"spanner.googleapis.com/Instance":               {children: []string{"spanner.googleapis.com/Database"}},
	"alloydb.googleapis.com/Cluster":                {children: []string{"alloydb.googleapis.com/Instance"}, forceParameter: "force"},
	"servicedirectory.googleapis.com/Namespace":     {children: []string{"servicedirectory.googleapis.com/Service"}},
	"servicedirectory.googleapis.com/Service":       {children: []string{"servicedirectory.googleapis.com/Endpoint"}},
	"networkconnectivity.googleapis.com/Hub":        {children: []string{"networkconnectivity.googleapis.com/Group", "networkconnectivity.googleapis.com/RouteTable"}},
	"networkconnectivity.googleapis.com/RouteTable": {children: []string{"networkconnectivity.googleapis.com/Route"}},
}

func HasServiceCascade(nativeType string) bool { _, ok := serviceCascadeRules[nativeType]; return ok }

type serviceCascades struct{ client *client }

func (r *Runtime) ServiceLifecycle(ctx context.Context, id asset.ConnectionID) (governance.Contributor, error) {
	c, err := r.resolve(ctx, id)
	if err != nil {
		return nil, err
	}
	return &serviceCascades{client: c}, nil
}

type serviceChild struct {
	kind, id string
	data     map[string]any
	direct   bool
}

func (c *client) serviceChildren(ctx context.Context, parent asset.Identity, data map[string]any) ([]serviceChild, error) {
	rule, ok := serviceCascadeRules[parent.NativeType]
	if !ok {
		return nil, nil
	}
	parentKind, _ := findType(parent.NativeType)
	parentURL, err := c.resourceURL(parentKind, parent.NativeID)
	if err != nil {
		return nil, err
	}
	verifyParent := func() error {
		if !isDataform(parent.NativeType) {
			return nil
		}
		live, err := c.request(ctx, "GET", parentURL, nil)
		if err != nil {
			return err
		}
		if c.canonicalName("//dataform.googleapis.com/"+text(live["name"])) != parent.NativeID {
			return groupDenied("dataform_parent_changed")
		}
		return dataformSameResource(parent.NativeType, data, live)
	}
	if err := verifyParent(); err != nil {
		return nil, err
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	parentItem := contracts.InventoryItem{NativeType: parent.NativeType, NativeID: parent.NativeID, Normalized: data}
	var result []serviceChild
	var membershipChecks []func() error
	seen := map[string]bool{}
	for _, childType := range rule.children {
		var found bool
		for _, compiled := range metadata.bundle.Specs {
			definition := compiled.Definition
			if compiled.ResourceKind.NativeType != childType {
				continue
			}
			found = true
			if definition.Discovery.Parent == nil || definition.Discovery.Parent.NativeType != parent.NativeType || definition.Discovery.List == nil {
				return nil, fmt.Errorf("service cascade has no explicit parent discovery")
			}
			api := definition.Discovery.List
			operation, ok := metadata.catalog.Operation(api.Operation)
			if !ok {
				return nil, fmt.Errorf("service child has no native list method")
			}
			parameters, err := productParameters(api.Parameters, c, "", parentItem)
			if err != nil {
				return nil, err
			}
			records, err := c.nativeList(ctx, operation, parameters, api.ItemsPath)
			if err != nil {
				return nil, err
			}
			kind, _ := findType(childType)
			generation := map[string]string{}
			for _, record := range records {
				id, err := c.productIdentity(kind, operation, parameters, api.IdentityPath, productRecord{Data: record})
				if err != nil {
					return nil, err
				}
				// Native nesting also binds the parent, so a foreign or sibling child can
				// never be authorized by a list response at this endpoint.
				if !strings.HasPrefix(id, parent.NativeID+"/") || seen[id] {
					return nil, fmt.Errorf("invalid or duplicate service child identity")
				}
				seen[id] = true
				endpoint, err := c.resourceURL(kind, id)
				if err != nil {
					return nil, err
				}
				live, err := c.request(ctx, "GET", endpoint, nil)
				if err != nil {
					return nil, err
				}
				if c.canonicalName("//"+strings.Split(childType, "/")[0]+"/"+text(live["name"])) != id {
					return nil, fmt.Errorf("service child read returned another identity")
				}
				if err := serviceIncarnation(record, live); err != nil {
					return nil, err
				}
				if err := dataformSameResource(childType, record, live); err != nil {
					return nil, err
				}
				if isDataform(childType) {
					generation[id] = dataformConfiguration(childType, live)
				}
				result = append(result, serviceChild{kind: childType, id: id, data: live, direct: slices.Contains(rule.directChildren, childType)})
			}
			if isDataform(childType) {
				// Scheduled work may create members during detail reads. Reconcile a
				// second complete native set after ALL child detail reads, so work
				// created while reading another collection cannot slip through.
				membershipChecks = append(membershipChecks, func() error {
					again, err := c.nativeList(ctx, operation, parameters, api.ItemsPath)
					if err != nil {
						return err
					}
					for _, record := range again {
						id, err := c.productIdentity(kind, operation, parameters, api.IdentityPath, productRecord{Data: record})
						if err != nil {
							return err
						}
						if generation[id] == "" || generation[id] != dataformConfiguration(childType, record) {
							return groupDenied("dataform_membership_changed")
						}
						delete(generation, id)
					}
					if len(generation) != 0 {
						return groupDenied("dataform_membership_changed")
					}
					return nil
				})
			}
		}
		if !found {
			return nil, fmt.Errorf("service cascade child has no resource rule")
		}
	}
	for _, verify := range membershipChecks {
		if err := verify(); err != nil {
			return nil, err
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i].id < result[j].id })
	if err := verifyParent(); err != nil {
		return nil, err
	}
	return result, nil
}

// Creation identity and conditional-write fields are checked whenever the API
// exposes them. Ordinary mutable settings do not become invented resource IDs.
func serviceIncarnation(planned, live map[string]any) error {
	for _, field := range []string{"uid", "uniqueId", "createTime", "creationTime", "etag"} {
		if value := text(planned[field]); value != "" && value != text(live[field]) {
			return groupDenied("service_resource_changed")
		}
	}
	return nil
}

func (s *serviceCascades) Contribute(ctx context.Context, _ asset.ScopeID, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	for _, parent := range assets {
		if parent.Identity.Provider != asset.ProviderGCP || !HasServiceCascade(parent.Identity.NativeType) {
			continue
		}
		children, err := s.client.serviceChildren(ctx, parent.Identity, parent.Normalized)
		if err != nil {
			return result, err
		}
		for _, child := range children {
			evidence := map[string]any{"resource_type": child.kind, "instance_id": child.id, "delete_by_default": true, "retention_supported": false, graph.LifecycleEvidenceControllerDeleteGuaranteed: true, graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
			policy := graph.CleanupDelegate
			if child.direct {
				policy = graph.CleanupDirect
				delete(evidence, graph.LifecycleEvidenceControllerDeleteGuaranteed)
				delete(evidence, graph.LifecycleEvidenceControllerVerifiesManagedAbsence)
			}
			var target *asset.Asset
			for i := range assets {
				candidate := &assets[i]
				if candidate.Identity.Provider == parent.Identity.Provider && candidate.Identity.ConnectionID == parent.Identity.ConnectionID && candidate.Identity.Partition == parent.Identity.Partition && candidate.Identity.NativeType == child.kind && candidate.Identity.NativeID == child.id {
					if target != nil {
						return result, fmt.Errorf("ambiguous service child identity")
					}
					target = candidate
				}
			}
			if target == nil {
				result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: child.kind, NativeID: child.id, ControllerID: parent.ID, Relationship: graph.RelationshipAttachedTo, Evidence: evidence})
				continue
			}
			if err := serviceIncarnation(target.Normalized, child.data); err != nil {
				return result, err
			}
			if err := dataformSameResource(child.kind, target.Normalized, child.data); err != nil {
				return result, err
			}
			result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: parent.ID, ManagedAssetID: target.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: policy, DirectCleanupAllowed: child.direct, EvidenceSource: serviceCascadeSource, Evidence: evidence, Confidence: 1})
			result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: target.ID, TargetAssetID: parent.ID, Type: graph.RelationshipAttachedTo, Source: serviceCascadeSource, Evidence: evidence, Confidence: 1})
		}
	}
	return result, nil
}

func (a *action) serviceCascadePreflight(ctx context.Context, request contracts.ActionRequest, live map[string]any) error {
	if !HasServiceCascade(a.kind.NativeType) {
		return nil
	}
	endpoint, err := a.client.resourceURL(a.kind, request.Asset.Identity.NativeID)
	if err != nil || endpoint != a.endpoint || request.Asset.Identity.NativeType != a.kind.NativeType {
		return groupDenied("service_parent_identity_changed")
	}
	if err := serviceIncarnation(request.Asset.Normalized, live); err != nil {
		return err
	}
	if err := a.servicePrerequisitesAbsent(ctx, request); err != nil {
		return err
	}
	impacts, err := groupImpacts(request)
	if err != nil {
		return err
	}
	visited := map[groupImpactKey]bool{}
	var verify func(asset.Asset, map[string]any) error
	verify = func(parent asset.Asset, data map[string]any) error {
		children, err := a.client.serviceChildren(ctx, parent.Identity, data)
		if err != nil {
			return err
		}
		for _, child := range children {
			if child.direct {
				return groupDenied("service_prerequisite_still_exists")
			}
			key := groupImpactKey{parent.ID, child.id}
			impact, ok := impacts[key]
			if !ok || impact.Asset.Identity.NativeType != child.kind {
				return groupDenied("service_child_missing_from_plan")
			}
			if !impact.Delete {
				return groupDenied("service_child_retention_not_supported")
			}
			visited[key] = true
			if err := serviceIncarnation(impact.Asset.Normalized, child.data); err != nil {
				return err
			}
			if err := dataformSameResource(child.kind, impact.Asset.Normalized, child.data); err != nil {
				return err
			}
			if protectedComputeLabels(child.data) || protectionReason(child.kind, child.data) != "" {
				return groupDenied("service_child_protected")
			}
			if err := verify(impact.Asset, child.data); err != nil {
				return err
			}
		}
		return nil
	}
	if err := verify(request.Asset, live); err != nil {
		return err
	}
	// A removed child may have been deleted independently after planning. Prove
	// its absence; a changed live membership may never silently drop an impact.
	for key, impact := range impacts {
		if visited[key] {
			continue
		}
		if !impact.Delete {
			return groupDenied("service_child_retention_not_supported")
		}
		if !a.serviceImpactDescendant(request, impact) {
			return groupDenied("service_child_scope_changed")
		}
		kind, _ := findType(impact.Asset.Identity.NativeType)
		endpoint, err := a.client.resourceURL(kind, impact.Asset.Identity.NativeID)
		if err != nil {
			return err
		}
		if _, err = a.client.request(ctx, "GET", endpoint, nil); !isNotFound(err) {
			if err != nil {
				return err
			}
			return groupDenied("service_child_membership_changed")
		}
	}
	return nil
}

func (a *action) serviceImpactDescendant(request contracts.ActionRequest, impact contracts.ActionImpact) bool {
	current := impact
	seen := map[asset.AssetID]bool{}
	for {
		if seen[current.Asset.ID] {
			return false
		}
		seen[current.Asset.ID] = true
		parent := request.Asset
		if current.ControllerID != request.Asset.ID {
			found := false
			for _, candidate := range request.LifecycleImpacts {
				if candidate.Asset.ID == current.ControllerID {
					parent = candidate.Asset
					found = true
					break
				}
			}
			if !found {
				return false
			}
		}
		rule, known := serviceCascadeRules[parent.Identity.NativeType]
		if !known || !slices.Contains(rule.children, current.Asset.Identity.NativeType) || slices.Contains(rule.directChildren, current.Asset.Identity.NativeType) || !strings.HasPrefix(current.Asset.Identity.NativeID, parent.Identity.NativeID+"/") {
			return false
		}
		if parent.ID == request.Asset.ID {
			return true
		}
		for _, candidate := range request.LifecycleImpacts {
			if candidate.Asset.ID == parent.ID {
				current = candidate
				break
			}
		}
	}
}

func (a *action) serviceCascadeReadback(ctx context.Context, request contracts.ActionRequest) (contracts.ReadbackResult, error) {
	endpoint, err := a.client.resourceURL(a.kind, request.Asset.Identity.NativeID)
	if err != nil || endpoint != a.endpoint || request.Asset.Identity.NativeType != a.kind.NativeType {
		return contracts.ReadbackResult{}, groupDenied("service_parent_identity_changed")
	}
	if _, err := groupImpacts(request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	if err := a.servicePrerequisitesAbsent(ctx, request); err != nil {
		return contracts.ReadbackResult{}, err
	}
	for _, impact := range request.LifecycleImpacts {
		if !impact.Delete || !a.serviceImpactDescendant(request, impact) {
			return contracts.ReadbackResult{}, groupDenied("service_child_scope_changed")
		}
		kind, _ := findType(impact.Asset.Identity.NativeType)
		endpoint, err := a.client.resourceURL(kind, impact.Asset.Identity.NativeID)
		if err != nil {
			return contracts.ReadbackResult{}, err
		}
		if _, err = a.client.request(ctx, "GET", endpoint, nil); !isNotFound(err) {
			if err != nil {
				return contracts.ReadbackResult{}, err
			}
			return contracts.ReadbackResult{Exists: true, State: "service_children_deleting"}, nil
		}
	}
	return contracts.ReadbackResult{Exists: false}, nil
}
