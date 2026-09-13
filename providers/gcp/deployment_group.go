package gcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

const infraGroupStructure = "_infra_group_structure"

type infraDeploymentUnit struct {
	ID, Deployment string
	Dependencies   []string
}

func infraGroupStates(data map[string]any) (string, string, error) {
	state, ok := data["state"].(string)
	if !ok || !slices.Contains([]string{"CREATING", "ACTIVE", "UPDATING", "DELETING", "FAILED", "SUSPENDED", "DELETED"}, state) {
		return "", "", groupDenied("infra_group_state_unknown")
	}
	provisioning := ""
	if value, present := data["provisioningState"]; present {
		provisioning, ok = value.(string)
		if !ok {
			return "", "", groupDenied("infra_group_provisioning_state_invalid")
		}
	}
	if !slices.Contains([]string{"", "PROVISIONING_STATE_UNSPECIFIED", "PROVISIONING", "PROVISIONED", "FAILED_TO_PROVISION", "DEPROVISIONING", "DEPROVISIONED", "FAILED_TO_DEPROVISION"}, provisioning) {
		return "", "", groupDenied("infra_group_provisioning_state_unknown")
	}
	return state, provisioning, nil
}

func (c *client) infraGroupUnits(data map[string]any) ([]infraDeploymentUnit, error) {
	var result []infraDeploymentUnit
	value, present := data["deploymentUnits"]
	if !present {
		return result, nil
	}
	rows, ok := value.([]any)
	if !ok {
		return nil, groupDenied("infra_group_units_invalid")
	}
	seen, deployments := map[string]bool{}, map[string]bool{}
	for _, value := range rows {
		row, ok := value.(map[string]any)
		id := text(row["id"])
		if !ok || !segmentPattern.MatchString(id) || id == "." || id == ".." || seen[id] {
			return nil, groupDenied("infra_group_unit_identity_invalid")
		}
		unit := infraDeploymentUnit{ID: id}
		if value, present := row["dependencies"]; present {
			if value == nil {
				return nil, groupDenied("infra_group_dependencies_invalid")
			}
			dependencies, err := discoveryStrings(value)
			if err != nil {
				return nil, err
			}
			unique := map[string]bool{}
			for _, dependency := range dependencies {
				if !seen[dependency] || unique[dependency] {
					return nil, groupDenied("infra_group_unit_dag_invalid")
				}
				unique[dependency] = true
			}
			unit.Dependencies = dependencies
		}
		if value, present := row["deployment"]; present {
			name, ok := value.(string)
			if !ok {
				return nil, groupDenied("infra_group_deployment_invalid")
			}
			if name != "" {
				var err error
				unit.Deployment, err = c.infraID(infraDeployment, name)
				if err != nil || deployments[unit.Deployment] {
					return nil, groupDenied("infra_group_deployment_identity_invalid")
				}
				deployments[unit.Deployment] = true
			}
		}
		seen[id] = true
		result = append(result, unit)
	}
	return result, nil
}

func (c *client) infraGroupIdentity(kind, id string, data map[string]any) error {
	group := data
	if kind == infraGroupRevision {
		group = object(data["snapshot"])
		_, parent := infraParent(kind, id)
		actual, err := c.infraID(infraGroup, text(group["name"]))
		if err != nil || actual != parent {
			return groupDenied("infra_group_revision_snapshot_invalid")
		}
		if _, err := time.Parse(time.RFC3339Nano, text(group["createTime"])); err != nil {
			return groupDenied("infra_group_snapshot_creation_missing")
		}
		if value, present := data["alternativeIds"]; present {
			if value == nil {
				return groupDenied("infra_group_revision_aliases_invalid")
			}
			if _, err := discoveryStrings(value); err != nil {
				return err
			}
		}
	}
	_, err := c.infraGroupUnits(group)
	return err
}

// Deprovision clears each deployment reference but preserves its unit and DAG.
// Hash all other fields before redaction so only that native mutation is allowed.
func infraGroupStructuralProof(data map[string]any) string {
	value := cloneParameters(data)
	rows, _ := value["deploymentUnits"].([]any)
	if rows != nil {
		units := make([]any, 0, len(rows))
		for _, row := range rows {
			unit := cloneParameters(object(row))
			delete(unit, "deployment")
			units = append(units, unit)
		}
		value["deploymentUnits"] = units
	}
	return infraConfiguration(value)
}

func (c *client) infraGroupSame(planned, live map[string]any, clearing bool) error {
	if !clearing {
		return infraSame(planned, live)
	}
	if text(planned[infraGroupStructure]) == "" || planned[infraGroupStructure] != infraGroupStructuralProof(live) {
		return groupDenied("infra_group_configuration_changed")
	}
	before, err := c.infraGroupUnits(planned)
	if err != nil {
		return err
	}
	after, err := c.infraGroupUnits(live)
	if err != nil || len(before) != len(after) {
		return groupDenied("infra_group_units_changed")
	}
	for i, unit := range after {
		if unit.ID != before[i].ID || unit.Deployment != "" && unit.Deployment != before[i].Deployment {
			return groupDenied("infra_group_reference_changed")
		}
	}
	return nil
}

func (c *client) infraGroupSnapshot(ctx context.Context, id string, data map[string]any) ([]infraMember, error) {
	records, err := c.infraRecords(ctx, infraGroup, id, true)
	if err != nil {
		return nil, err
	}
	current, err := c.infraGroupUnits(data)
	if err != nil {
		return nil, err
	}
	var members []infraMember
	var successful *infraRecord
	var newest time.Time
	rootCreated, _ := time.Parse(time.RFC3339Nano, text(data["createTime"]))
	for i, record := range records {
		snapshot := object(record.data["snapshot"])
		if snapshot["createTime"] != data["createTime"] {
			return nil, groupDenied("infra_group_revision_incarnation_changed")
		}
		created, err := time.Parse(time.RFC3339Nano, text(record.data["createTime"]))
		if err != nil || created.Before(rootCreated) {
			return nil, groupDenied("infra_group_revision_creation_invalid")
		}
		// Revisions are snapshots created after provision/deprovision completes.
		// Select the latest successful snapshot, not the newest failed attempt.
		state := text(snapshot["provisioningState"])
		if !slices.Contains([]string{"PROVISIONED", "DEPROVISIONED", "FAILED_TO_PROVISION", "FAILED_TO_DEPROVISION"}, state) {
			return nil, groupDenied("infra_group_revision_outcome_unknown")
		}
		if state == "PROVISIONED" || state == "DEPROVISIONED" {
			if created.Equal(newest) {
				return nil, groupDenied("infra_group_latest_revision_ambiguous")
			}
			if created.After(newest) {
				successful, newest = &records[i], created
			}
		}
		members = append(members, infraMember{Kind: infraGroupRevision, ID: record.id, Proof: infraConfiguration(record.data)})
	}
	if successful == nil && data["provisioningState"] == "PROVISIONED" {
		return nil, groupDenied("infra_group_successful_revision_missing")
	}
	if successful != nil {
		previous, err := c.infraGroupUnits(object(successful.data["snapshot"]))
		if err != nil {
			return nil, err
		}
		current = append(current, previous...)
	}
	seen := map[string]bool{}
	for _, unit := range current {
		if unit.Deployment == "" || seen[unit.Deployment] {
			continue
		}
		seen[unit.Deployment] = true
		member := infraMember{Kind: infraDeployment, ID: unit.Deployment}
		live, err := c.infraRead(ctx, infraDeployment, member.ID)
		if isNotFound(err) {
			member.Absent = true
		} else if err != nil {
			return nil, err
		} else {
			children, err := c.infraSnapshot(ctx, infraDeployment, member.ID, live)
			if err != nil {
				return nil, err
			}
			encoded, _ := json.Marshal(children)
			member.Proof = infraConfiguration(live)
			member.Snapshot = infraManifestHash(member.Proof, string(encoded))
		}
		members = append(members, member)
	}
	if err := c.infraStableRecords(ctx, infraGroup, id, data, records); err != nil {
		return nil, err
	}
	data[infraGroupStructure] = infraGroupStructuralProof(data)
	slices.SortFunc(members, func(a, b infraMember) int { return strings.Compare(a.ID, b.ID) })
	return members, nil
}

func (c *client) infraGroupSavedMembers(root asset.Asset) ([]infraMember, error) {
	var members []infraMember
	value := text(root.Normalized[infraManifestKey])
	proof := text(root.Normalized[infraProof]) + "\n" + text(root.Normalized[infraGroupStructure])
	if value == "" || json.Unmarshal([]byte(value), &members) != nil || text(root.Normalized[infraSnapshotKey]) != infraManifestHash(proof, value) || text(root.Normalized[infraGroupStructure]) == "" {
		return nil, groupDenied("infra_group_manifest_missing")
	}
	seen := map[string]bool{}
	for _, member := range members {
		if seen[member.ID] || member.Proof == "" && !member.Absent || member.VisibleProof != "" || member.Incarnation != "" || member.Unmapped {
			return nil, groupDenied("infra_group_manifest_invalid")
		}
		seen[member.ID] = true
		if member.Kind != infraDeployment && member.Kind != infraGroupRevision {
			return nil, groupDenied("infra_group_member_type_invalid")
		}
		if _, err := c.infraName(member.Kind, member.ID); err != nil {
			return nil, err
		}
		if member.Kind == infraGroupRevision && (!strings.HasPrefix(member.ID, root.Identity.NativeID+"/revisions/") || member.Absent || member.Snapshot != "") {
			return nil, groupDenied("infra_group_metadata_invalid")
		}
		if member.Kind == infraDeployment && (member.Absent && (member.Proof != "" || member.Snapshot != "") || !member.Absent && member.Snapshot == "") {
			return nil, groupDenied("infra_group_deployment_proof_missing")
		}
	}
	return members, nil
}

func (s *serviceCascades) contributeInfraGroup(ctx context.Context, root asset.Asset, assets []asset.Asset) (governance.Contribution, error) {
	result := governance.Contribution{}
	planned, err := s.client.infraGroupSavedMembers(root)
	if err != nil {
		return result, err
	}
	live, err := s.client.infraRead(ctx, infraGroup, root.Identity.NativeID)
	if err != nil {
		return result, err
	}
	if err := infraSame(root.Normalized, live); err != nil {
		return result, err
	}
	members, err := s.client.infraGroupSnapshot(ctx, root.Identity.NativeID, live)
	if err != nil || !slices.Equal(planned, members) {
		if err == nil {
			err = groupDenied("infra_group_members_changed")
		}
		return result, err
	}
	for _, member := range members {
		if member.Absent {
			continue
		}
		evidence := map[string]any{"resource_type": member.Kind, "instance_id": member.ID, "delete_by_default": true, "retention_supported": member.Kind == infraDeployment, graph.LifecycleEvidenceControllerMetadata: true, graph.LifecycleEvidenceControllerDeleteGuaranteed: true, graph.LifecycleEvidenceControllerVerifiesManagedAbsence: true}
		managed, found, err := findManagedAsset(assets, root, member.Kind, member.ID)
		if err != nil {
			return result, err
		}
		if !found {
			result.Unresolved = append(result.Unresolved, graph.UnresolvedReference{BlocksCleanup: true, Provider: root.Identity.Provider, ConnectionID: root.Identity.ConnectionID, NativeType: member.Kind, NativeID: member.ID, ControllerID: root.ID, Relationship: graph.RelationshipMemberOf, Evidence: evidence})
			continue
		}
		if text(managed.Normalized[infraProof]) != member.Proof || member.Kind == infraDeployment && managed.Normalized[infraSnapshotKey] != member.Snapshot || member.Kind == infraGroupRevision && managed.Normalized[infraRootProof] != root.Normalized[infraProof] {
			return result, groupDenied("infra_group_member_asset_changed")
		}
		result.Bindings = append(result.Bindings, graph.LifecycleBinding{ControllerAssetID: root.ID, ManagedAssetID: managed.ID, Authority: graph.AuthorityAuthoritative, Ownership: graph.OwnershipExclusive, CleanupPolicy: graph.CleanupDelegate, DirectCleanupAllowed: member.Kind == infraDeployment, EvidenceSource: "gcp:deployment-group", Evidence: evidence, Confidence: 1})
		result.Relationships = append(result.Relationships, graph.Relationship{SourceAssetID: managed.ID, TargetAssetID: root.ID, Type: graph.RelationshipMemberOf, Source: "gcp:deployment-group", Evidence: evidence, Confidence: 1})
	}
	return result, nil
}
