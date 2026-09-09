package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const infraManifestKey = "_infra_members"
const infraSnapshotKey = "_infra_snapshot"
const infraLifecycleSource = "gcp:infrastructure-manager"

func infraManifestHash(configuration, members string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(configuration+"\n"+members)))
}

type infraMember struct {
	Kind         string `json:"kind"`
	ID           string `json:"id"`
	Proof        string `json:"proof"`
	VisibleProof string `json:"visible_proof,omitempty"`
	Incarnation  string `json:"incarnation,omitempty"`
	Absent       bool   `json:"absent,omitempty"`
	Unmapped     bool   `json:"unmapped,omitempty"`
}

type infraRecord struct {
	kind, id string
	data     map[string]any
}

func infraChildKinds(kind string) []string {
	switch kind {
	case infraDeployment:
		return []string{infraRevision}
	case infraRevision:
		return []string{infraResource}
	case infraPreview:
		return []string{infraChange, infraDrift}
	}
	return nil
}

func (c *client) infraList(ctx context.Context, kind, parent string, details bool) ([]infraRecord, error) {
	collections := infraCollections[kind]
	operation := "config.projects.locations." + strings.Join(collections, ".") + ".list"
	rows, err := c.batchList(ctx, operation, map[string]any{"parent": strings.TrimPrefix(parent, "//"+infraHost+"/")}, collections[len(collections)-1])
	if err != nil {
		return nil, err
	}
	var records []infraRecord
	seen := map[string]bool{}
	for _, row := range rows {
		id, err := c.infraID(kind, text(row["name"]))
		if err != nil || !strings.HasPrefix(id, parent+"/") || seen[id] {
			return nil, groupDenied("infra_child_list_invalid")
		}
		seen[id] = true
		if err := c.infraIdentity(kind, id, row); err != nil {
			return nil, err
		}
		if details {
			live, err := c.infraRead(ctx, kind, id)
			if err != nil {
				return nil, err
			}
			if err := infraSame(row, live); err != nil {
				return nil, err
			}
			row = live
		}
		records = append(records, infraRecord{kind, id, row})
	}
	slices.SortFunc(records, func(a, b infraRecord) int { return strings.Compare(a.id, b.id) })
	return records, nil
}

func (c *client) infraRecords(ctx context.Context, kind, parent string, details bool) ([]infraRecord, error) {
	var result []infraRecord
	for _, child := range infraChildKinds(kind) {
		records, err := c.infraList(ctx, child, parent, details)
		if err != nil {
			return nil, err
		}
		result = append(result, records...)
		for _, record := range records {
			children, err := c.infraRecords(ctx, record.kind, record.id, details)
			if err != nil {
				return nil, err
			}
			result = append(result, children...)
		}
	}
	slices.SortFunc(result, func(a, b infraRecord) int { return strings.Compare(a.id, b.id) })
	return result, nil
}

func (c *client) infraSnapshot(ctx context.Context, kind, id string, data map[string]any) ([]infraMember, error) {
	records, err := c.infraRecords(ctx, kind, id, true)
	if err != nil {
		return nil, err
	}
	latest := ""
	if kind == infraDeployment && text(data["latestRevision"]) != "" {
		latest, err = c.infraID(infraRevision, text(data["latestRevision"]))
		if err != nil {
			return nil, err
		}
	}
	var members []infraMember
	seen := map[string]bool{}
	latestFound := latest == ""
	for _, record := range records {
		member := infraMember{Kind: record.kind, ID: record.id, Proof: infraConfiguration(record.data)}
		latestFound = latestFound || record.id == latest
		if record.id == latest {
			// Native destruction uses the latest revision's service account and
			// source. Keep those dependencies even if the root omits them.
			for kind, refs := range c.infraReferences(infraDeployment, id, record.data) {
				data[referenceKey(kind)] = refs
			}
		}
		if record.kind == infraResource && strings.HasPrefix(record.id, latest+"/resources/") && latest != "" {
			physical, supported, err := c.infraPhysicalMember(ctx, record.data)
			if err != nil {
				return nil, err
			}
			member.Unmapped = !supported
			if supported && physical.ID != "" {
				if seen[physical.ID] {
					return nil, groupDenied("infra_duplicate_physical_owner")
				}
				seen[physical.ID] = true
				members = append(members, physical)
			}
		}
		members = append(members, member)
	}
	if !latestFound || kind == infraDeployment && latest == "" && len(records) != 0 {
		return nil, groupDenied("infra_latest_revision_missing")
	}
	// Reconcile all collections after the native physical reads. A new revision
	// or resource that appears during a detail read must restart discovery.
	again, err := c.infraRecords(ctx, kind, id, false)
	if err != nil {
		return nil, err
	}
	if len(records) != len(again) {
		return nil, groupDenied("infra_membership_changed")
	}
	for i, record := range records {
		if record.kind != again[i].kind || record.id != again[i].id || infraConfiguration(record.data) != infraConfiguration(again[i].data) {
			return nil, groupDenied("infra_membership_changed")
		}
	}
	live, err := c.infraRead(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	if err := infraSame(data, live); err != nil {
		return nil, err
	}
	slices.SortFunc(members, func(a, b infraMember) int { return strings.Compare(a.ID, b.ID) })
	return members, nil
}

func (c *client) infraSavedMembers(root asset.Asset) ([]infraMember, error) {
	var members []infraMember
	value := text(root.Normalized[infraManifestKey])
	if value == "" || json.Unmarshal([]byte(value), &members) != nil || text(root.Normalized[infraSnapshotKey]) != infraManifestHash(text(root.Normalized[infraProof]), value) {
		return nil, groupDenied("infra_plan_manifest_missing")
	}
	seen := map[string]bool{}
	for _, member := range members {
		if seen[member.ID] || member.ID == root.Identity.NativeID || member.Proof == "" && !member.Absent {
			return nil, groupDenied("infra_plan_manifest_invalid")
		}
		seen[member.ID] = true
		if isInfra(member.Kind) {
			if !strings.HasPrefix(member.ID, root.Identity.NativeID+"/") || member.Absent || member.VisibleProof != "" || member.Incarnation != "" || (member.Unmapped && member.Kind != infraResource) {
				return nil, groupDenied("infra_metadata_manifest_invalid")
			}
			if _, err := c.infraName(member.Kind, member.ID); err != nil {
				return nil, err
			}
		} else {
			kind, ok := findType(member.Kind)
			if !ok || root.Identity.NativeType != infraDeployment || member.Unmapped || !member.Absent && member.VisibleProof == "" {
				return nil, groupDenied("infra_physical_manifest_invalid")
			}
			if _, err := c.resourceURL(kind, member.ID); err != nil {
				return nil, err
			}
		}
	}
	return members, nil
}

func (s *serviceCascades) contributeInfra(ctx context.Context, root asset.Asset, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	planned, err := s.client.infraSavedMembers(root)
	if err != nil {
		return result, err
	}
	live, err := s.client.infraRead(ctx, root.Identity.NativeType, root.Identity.NativeID)
	if err != nil {
		return result, err
	}
	if err := infraSame(root.Normalized, live); err != nil {
		return result, err
	}
	members, err := s.client.infraSnapshot(ctx, root.Identity.NativeType, root.Identity.NativeID, live)
	if err != nil {
		return result, err
	}
	if !slices.Equal(planned, members) {
		return result, groupDenied("infra_plan_members_changed")
	}
	for _, member := range members {
		if member.Absent {
			continue
		}
		metadata := isInfra(member.Kind)
		evidence := map[string]any{"resource_type": member.Kind, "instance_id": member.ID, "delete_by_default": true, "retention_supported": !metadata, graph.LifecycleEvidenceControllerDeleteGuaranteed: true, graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
		if metadata {
			evidence[graph.LifecycleEvidenceControllerMetadata] = true
		}
		managed, found, err := findManagedAsset(assets, root, member.Kind, member.ID)
		if err != nil {
			return result, err
		}
		if !found {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{Provider: root.Identity.Provider, ConnectionID: root.Identity.ConnectionID, NativeType: member.Kind, NativeID: member.ID, ControllerID: root.ID, Relationship: graph.RelationshipMemberOf, Evidence: evidence})
			continue
		}
		if metadata {
			if text(managed.Normalized[infraProof]) != member.Proof || text(managed.Normalized[infraRootProof]) != text(root.Normalized[infraProof]) {
				return result, groupDenied("infra_metadata_asset_changed")
			}
		} else if infraPhysicalVisible(member.Kind, managed.Normalized) != member.VisibleProof {
			return result, groupDenied("infra_physical_asset_changed")
		}
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: root.ID, ManagedAssetID: managed.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, EvidenceSource: infraLifecycleSource, Evidence: evidence, Confidence: 1})
		result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: managed.ID, TargetAssetID: root.ID, Type: graph.RelationshipMemberOf, Source: infraLifecycleSource, Evidence: evidence, Confidence: 1})
	}
	return result, nil
}
