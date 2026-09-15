package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Pipelines are independent artifacts. Reading them here grants no ownership
// or permission to delete them as a side effect of Spark pool cleanup.
var synapsePipelineDefinition = synapseDataDefinition{kind: synapseType + "/pipelines", collection: "pipelines", list: "Pipeline_GetPipelinesByWorkspace", read: "Pipeline_GetPipeline", parameter: "pipelineName"}

type synapseSparkWork struct {
	manifest map[string]any
	raw      map[string]map[string]any
}

func synapseSparkWorkDefinitions() []synapseDataDefinition {
	return []synapseDataDefinition{synapseDataKind(synapseBatchType), synapseDataKind(synapseSessionType), synapseDataKind(synapseNotebookType), synapseDataKind(synapseJobDefinitionType), synapsePipelineDefinition}
}

// A typed reference is an authored dependency, even inside nested activities.
// Expressions and malformed selectors cannot establish independence. Missing
// optional pool references are permitted; raw notebook code is never parsed.
func synapsePoolReferences(value any, pool string) (references, unresolved bool) {
	switch value := value.(type) {
	case []any:
		for _, child := range value {
			r, u := synapsePoolReferences(child, pool)
			references, unresolved = references || r, unresolved || u
		}
	case map[string]any:
		for key, child := range value {
			if key == "bigDataPool" || key == "targetBigDataPool" || key == "sparkPool" {
				r, u := synapsePoolReference(child, pool)
				references, unresolved = references || r, unresolved || u
				continue
			}
			r, u := synapsePoolReferences(child, pool)
			references, unresolved = references || r, unresolved || u
		}
		if value["type"] == "BigDataPoolReference" {
			r, u := synapsePoolReference(value, pool)
			references, unresolved = references || r, unresolved || u
		}
	}
	return
}

func synapsePoolReference(value any, pool string) (bool, bool) {
	ref := object(value)
	name, ok := ref["referenceName"].(string)
	if !ok || ref["type"] != "BigDataPoolReference" || name == "" || strings.TrimSpace(name) != name || strings.ContainsAny(name, "@{}%/\\\x00\r\n") {
		return false, true
	}
	return strings.EqualFold(name, pool), false
}

// LIST discovers work; it never proves a reviewed record absent. Every known
// record omitted from LIST gets its own read. Two complete indexes and fresh
// detail reads reject changes while the snapshot is assembled. Only private
// hashes and native selectors leave this collector, never code or arguments.
func (c *synapseDataClient) synapseSparkWork(ctx context.Context, target synapseDataTarget, known map[string]any) (out synapseSparkWork, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	out = synapseSparkWork{manifest: map[string]any{}, raw: map[string]map[string]any{}}
	remaining := maps.Clone(known)
	for _, d := range synapseSparkWorkDefinitions() {
		rows, _, index, readErr := c.synapseListData(ctx, target, d, true)
		if readErr != nil {
			return out, readErr
		}
		seen := map[string]bool{}
		add := func(raw map[string]any) error {
			id, params, err := synapseObservedID(target, d, raw)
			if err != nil {
				return err
			}
			if d.spark {
				if err := synapseSparkIncarnation(raw); err != nil {
					return err
				}
			}
			entry := map[string]any{"kind": d.kind, "parameters": params, "configuration": c.arm.privateConfiguration(synapseDataSnapshot(d, raw))}
			if !d.spark {
				entry["references"], entry["unresolved"] = synapsePoolReferences(object(raw["properties"]), text(target.pool["name"]))
			}
			out.manifest[id], out.raw[id], seen[id] = entry, raw, true
			return nil
		}
		for _, raw := range rows {
			if err := add(raw); err != nil {
				return out, err
			}
		}
		for _, id := range slices.Sorted(maps.Keys(known)) {
			entry := object(known[id])
			if entry["kind"] != d.kind {
				continue
			}
			delete(remaining, id)
			params := object(entry["parameters"])
			// Verify even listed selectors, before allowing them to influence a
			// later mutation. Native read identity must equal the manifest key.
			if params["endpoint"] != target.workspace.endpoint || d.spark && params["sparkPoolName"] != target.pool["name"] {
				return out, serviceDenied("synapse_work_scope_changed")
			}
			metadata, err := providerData()
			if err != nil {
				return out, err
			}
			op, _ := metadata.catalog.Operation(synapseDataOperationPrefix + d.read)
			bound, err := bindAzureREST(op, params)
			if err != nil {
				return out, err
			}
			endpoint, err := url.Parse(bound.URL)
			if err != nil {
				return out, err
			}
			expected := strings.ToLower(target.workspace.id + endpoint.Path)
			if d.spark {
				expected = target.workspace.endpoint + endpoint.EscapedPath()
			}
			if id != expected {
				return out, serviceDenied("synapse_work_identity_changed")
			}
			if seen[id] {
				if c.arm.privateConfiguration(params) != c.arm.privateConfiguration(object(object(out.manifest[id])["parameters"])) {
					return out, serviceDenied("synapse_work_selector_changed")
				}
				continue
			}
			res, readErr := c.synapseReadData(ctx, target, d, params)
			if isNotFound(readErr) {
				continue
			}
			if readErr != nil {
				return out, readErr
			}
			actual, _, readErr := synapseObservedID(target, d, res.data)
			if readErr != nil || actual != id {
				return out, serviceDenied("synapse_work_identity_changed")
			}
			if err := add(res.data); err != nil {
				return out, err
			}
		}
		_, _, after, readErr := c.synapseListData(ctx, target, d, false)
		if readErr != nil {
			return out, readErr
		}
		if index != after {
			return out, serviceDenied("synapse_work_index_changed")
		}
		for _, id := range slices.Sorted(maps.Keys(seen)) {
			entry := object(out.manifest[id])
			res, readErr := c.synapseReadData(ctx, target, d, object(entry["parameters"]))
			if readErr != nil {
				return out, readErr
			}
			if entry["configuration"] != c.arm.privateConfiguration(synapseDataSnapshot(d, res.data)) {
				return out, serviceDenied("synapse_work_configuration_changed")
			}
			out.raw[id] = res.data
		}
	}
	if len(remaining) != 0 {
		return out, serviceDenied("unknown_synapse_work_kind")
	}
	return out, c.verifySynapseDataTarget(ctx, target)
}
