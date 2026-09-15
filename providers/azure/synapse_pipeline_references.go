package azure

import (
	"context"
	"slices"
	"strings"
)

// Walk activity containers, never user parameters, code, or arbitrary JSON.
// Names represented by expressions cannot establish a static graph edge.
func synapsePipelineActivityReferences(workspace string, activities any) (map[string][]string, bool) {
	refs := map[string][]string{}
	unresolved := false
	var walk func(any)
	add := func(value any, typ, kind string) {
		row, ok := value.(map[string]any)
		name, static := row["referenceName"].(string)
		if !ok || row["type"] != typ || !static || name == "" || name != strings.TrimSpace(name) || strings.ContainsAny(name, "@{}%/\\\x00\r\n") || name == "." || name == ".." {
			unresolved = true
			return
		}
		addReference(refs, kind, workspace+"/"+last(kind)+"/"+name)
	}
	walk = func(value any) {
		rows, ok := value.([]any)
		if !ok {
			unresolved = true
			return
		}
		for _, value := range rows {
			row, ok := value.(map[string]any)
			if !ok {
				unresolved = true
				continue
			}
			props := object(row["typeProperties"])
			switch row["type"] {
			case "SynapseNotebook":
				add(props["notebook"], "NotebookReference", synapseNotebookType)
				if value, present := props["sparkPool"]; present {
					add(value, "BigDataPoolReference", synapseSparkType)
				}
			case "SparkJob":
				add(props["sparkJob"], "SparkJobDefinitionReference", synapseJobDefinitionType)
				if value, present := props["sparkPool"]; present {
					add(value, "BigDataPoolReference", synapseSparkType)
				}
			case "ExecutePipeline":
				add(props["pipeline"], "PipelineReference", synapsePipelineType)
			case "ForEach", "Until":
				walk(props["activities"])
			case "IfCondition":
				for _, key := range []string{"ifTrueActivities", "ifFalseActivities"} {
					if value, present := props[key]; present {
						walk(value)
					}
				}
			case "Switch":
				if value, present := props["defaultActivities"]; present {
					walk(value)
				}
				if value, present := props["cases"]; present {
					cases, ok := value.([]any)
					if !ok {
						unresolved = true
					}
					for _, value := range cases {
						walk(object(value)["activities"])
					}
				}
			}
		}
	}
	if activities != nil {
		walk(activities)
	}
	return refs, unresolved
}

func (c *synapseDataClient) synapsePipelineReferences(ctx context.Context, target synapseDataTarget, raw map[string]any, refs map[string][]string, normalized map[string]any) error {
	references, unresolved := synapsePipelineActivityReferences(target.workspace.id, object(raw["properties"])["activities"])
	configurations := map[string]any{}
	missing := []string{}
	for pass := 0; pass < 2; pass++ {
		for kind, ids := range references {
			for _, id := range ids {
				addReference(refs, kind, strings.ToLower(id))
				var res response
				var err error
				if kind == synapseSparkType {
					native, _ := findType(kind)
					var endpoint string
					endpoint, err = c.arm.resourceURL(native, id)
					if err == nil {
						res, err = c.arm.request(ctx, "GET", endpoint)
					}
					if err == nil {
						err = c.arm.synapseReadResponse(res, id, kind)
					}
				} else {
					d := synapseDataKind(kind)
					_, params, e := c.arm.synapseDataIdentity(id, kind)
					err = e
					if err == nil {
						res, err = c.synapseReadData(ctx, target, d, params)
					}
				}
				configuration := "absent"
				if isNotFound(err) {
					if pass == 0 {
						missing = append(missing, strings.ToLower(id))
					}
				} else if err != nil {
					return err
				} else {
					configuration = c.arm.privateConfiguration(synapseDataSnapshot(synapseDataKind(kind), res.data))
				}
				if pass == 1 && configurations[id] != configuration {
					return serviceDenied("synapse_pipeline_dependency_changed")
				}
				configurations[id] = configuration
			}
		}
	}
	slices.Sort(missing)
	normalized["_synapse_dynamic_references"] = unresolved
	normalized["_synapse_missing_references"] = missing
	normalized["_synapse_reference_configurations"] = configurations
	return nil
}
