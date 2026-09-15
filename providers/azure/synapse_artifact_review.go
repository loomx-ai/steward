package azure

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const synapseArtifactReviewKey = "_synapse_artifact_review"
const synapseArtifactProofKey = "_synapse_artifact_proof"

func synapseReviewArtifact(kind string) bool {
	return kind == synapseNotebookType || kind == synapseJobDefinitionType
}
func (c *synapseDataClient) artifactReviewProof(id string, connection asset.ConnectionID, review map[string]any) string {
	return c.arm.privateConfiguration(map[string]any{"protocol": "synapse-artifact-review-1", "id": id, "connection": connection, "review": review})
}

// Known selectors are discovery hints. Validate their scope before any GET,
// including when refreshing a review after credentials have rotated.
func (c *synapseDataClient) artifactPoolHints(workspace synapseWorkspace, known map[string]any) ([]synapseKnownData, error) {
	hints := []synapseKnownData{}
	for id, value := range known {
		row := object(value)
		name := text(row["name"])
		canonical, kind, err := parseID(id)
		if err != nil || canonical != id || !strings.EqualFold(kind, synapseSparkType) || id != workspace.id+"/bigdatapools/"+strings.ToLower(name) || name == "" || strings.ContainsAny(name, "/%\\\x00\r\n") {
			return nil, serviceDenied("synapse_artifact_pool_hint_changed")
		}
		hints = append(hints, synapseKnownData{workspace: workspace.id, params: map[string]any{"sparkPoolName": name}})
	}
	return hints, nil
}

// This review does not guess the undocumented mapping of Spark artifactId to
// notebook/job-definition identities. Any non-quiesced workspace Spark work
// blocks deletion; unrelated jobs are never cancelled as a side effect.
func (c *synapseDataClient) artifactReview(ctx context.Context, target synapseDataTarget, d synapseDataDefinition, raw map[string]any, known map[string]any) (review map[string]any, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	id, params, err := synapseObservedID(target, d, raw)
	if err != nil || !synapseReviewArtifact(d.kind) {
		return nil, serviceDenied("invalid_synapse_cleanup_artifact")
	}
	w := target.workspace
	if err = c.artifactOwner(w, d, id); err != nil {
		return nil, err
	}
	groupID := strings.Join(strings.Split(w.id, "/")[:5], "/")
	group, err := c.arm.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(group, groupID, groupType) {
		return nil, serviceDenied("invalid_synapse_artifact_group")
	}
	blocked := text(group.data["managedBy"]) != "" || object(w.raw["properties"])["provisioningState"] != "Succeeded"
	for _, v := range []map[string]any{raw, w.raw, group.data} {
		blocked = blocked || protectedAzureTags(object(v["tags"]))
	}
	locks, err := c.arm.managementLocks(ctx)
	if err != nil {
		return nil, err
	}
	blocked = blocked || locked(id, locks) || locked(w.id, locks)
	pipelines, err := c.synapseWork(ctx, synapseDataTarget{workspace: w}, object(known["pipelines"]), []synapseDataDefinition{synapsePipelineDefinition})
	if err != nil {
		return nil, err
	}
	for _, value := range pipelines.raw {
		refs, unresolved := synapsePipelineActivityReferences(w.id, object(value["properties"])["activities"])
		blocked = blocked || unresolved
		for _, ref := range refs[d.kind] {
			blocked = blocked || strings.EqualFold(ref, id)
		}
	}
	hints, err := c.artifactPoolHints(w, object(known["pools"]))
	if err != nil {
		return nil, err
	}
	targets, err := c.arm.synapseDataTargets(ctx, w, synapseDataKind(synapseBatchType), hints)
	if err != nil {
		return nil, err
	}
	pools := map[string]any{}
	for _, current := range targets {
		poolID := strings.ToLower(text(current.pool["id"]))
		work, err := c.synapseWork(ctx, current, object(object(object(known["pools"])[poolID])["work"]), []synapseDataDefinition{synapseDataKind(synapseBatchType), synapseDataKind(synapseSessionType)})
		if err != nil {
			return nil, err
		}
		for _, value := range work.raw {
			blocked = blocked || !synapseSparkQuiesced(value)
		}
		pools[poolID] = map[string]any{"name": current.pool["name"], "configuration": c.arm.privateConfiguration(synapseSnapshot(current.pool)), "work": work.manifest}
	}
	afterTargets, err := c.arm.synapseDataTargets(ctx, w, synapseDataKind(synapseBatchType), hints)
	if err != nil {
		return nil, err
	}
	if len(afterTargets) != len(pools) {
		return nil, serviceDenied("synapse_artifact_pool_index_changed")
	}
	for _, current := range afterTargets {
		expected := object(pools[strings.ToLower(text(current.pool["id"]))])
		if expected["configuration"] != c.arm.privateConfiguration(synapseSnapshot(current.pool)) {
			return nil, serviceDenied("synapse_artifact_pool_changed")
		}
	}
	after, err := c.synapseReadData(ctx, target, d, params)
	if err != nil {
		return nil, err
	}
	artifactHash := c.arm.privateConfiguration(synapseDataSnapshot(d, raw))
	if artifactHash != c.arm.privateConfiguration(synapseDataSnapshot(d, after.data)) {
		return nil, serviceDenied("synapse_artifact_configuration_changed")
	}
	if err = c.verifySynapseDataTarget(ctx, synapseDataTarget{workspace: w}); err != nil {
		return nil, err
	}
	groupAfter, err := c.arm.request(ctx, "GET", apiURL(groupID, resourcesVersion))
	if err != nil {
		return nil, err
	}
	groupHash := c.arm.privateConfiguration(synapseSnapshot(group.data))
	if !validResourceResponse(groupAfter, groupID, groupType) || groupHash != c.arm.privateConfiguration(synapseSnapshot(groupAfter.data)) {
		return nil, serviceDenied("synapse_artifact_group_changed")
	}
	return map[string]any{"artifact": artifactHash, "workspace": c.arm.privateConfiguration(synapseSnapshot(w.raw)), "group": groupHash, "pools": pools, "pipelines": pipelines.manifest, "blocked": blocked}, nil
}

func (r *Runtime) synapseArtifactInventory(ctx context.Context, c *synapseDataClient, target synapseDataTarget, d synapseDataDefinition, raw map[string]any, req contracts.InventoryRequest, item *contracts.InventoryItem) error {
	if item.Normalized["_synapse_unresolved_pool"] != nil {
		if item.Normalized["cleanup_protection_reason"] == "synapse_cleanup_not_implemented" {
			item.Normalized["cleanup_protection_reason"] = "synapse_artifact_pool_unresolved"
		}
		return nil
	}
	known := object(req.KnownNativeMetadata[item.NativeID][synapseArtifactReviewKey])
	review, err := c.artifactReview(ctx, target, d, raw, known)
	if err != nil {
		return err
	}
	item.Normalized[synapseArtifactReviewKey] = review
	item.Normalized[synapseArtifactProofKey] = c.artifactReviewProof(item.NativeID, req.ConnectionID, review)
	// A complete local reference/Spark review does not establish the absence of
	// retained or externally orchestrated Pipeline runs. Keep cleanup unavailable.
	actionable := false
	item.Actionable = &actionable

	return nil
}
