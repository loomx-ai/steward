package azure

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

func synapseDataResponse(workspace synapseWorkspace, op catalog.Operation, params map[string]any, res response) (string, error) {
	if res.status != 200 || res.data["error"] != nil || res.data["code"] != nil || operationLocation(res.header) != "" {
		return "", serviceDenied("invalid_synapse_data_response")
	}
	name := strings.TrimPrefix(op.ID, synapseDataOperationPrefix)
	switch name {
	case "SparkBatch_GetSparkBatchJob", "SparkSession_GetSparkSession":
		key, kind := "batchId", "SparkBatch"
		if name == "SparkSession_GetSparkSession" {
			key, kind = "sessionId", "SparkSession"
		}
		expected, err := batchInteger(params[key], 32)
		if err != nil {
			return "", err
		}
		return "", synapseSparkItem(workspace, params, kind, expected, res.data)
	case "SparkBatch_GetSparkBatchJobs", "SparkSession_GetSparkSessions":
		kind := "SparkBatch"
		if name == "SparkSession_GetSparkSessions" {
			kind = "SparkSession"
		}
		from, size := int64(0), int64(20)
		if v, ok := params["from"]; ok {
			from, _ = batchInteger(v, 32)
		}
		if v, ok := params["size"]; ok {
			size, _ = batchInteger(v, 32)
		}
		start, e1 := batchInteger(res.data["from"], 32)
		total, e2 := batchInteger(res.data["total"], 32)
		rows, ok := res.data["sessions"].([]any)
		count := int64(len(rows))
		if e1 != nil || e2 != nil || !ok || start != from || total < 0 || count > size || from < total && (count == 0 || count > total-from) || from >= total && count != 0 {
			return "", serviceDenied("incomplete_synapse_spark_page")
		}
		seen := map[int64]bool{}
		for _, row := range rows {
			raw := object(row)
			id, err := batchInteger(raw["id"], 32)
			if err != nil || id < 0 || seen[id] {
				return "", serviceDenied("invalid_synapse_spark_list_identity")
			}
			seen[id] = true
			if err = synapseSparkItem(workspace, params, kind, id, raw); err != nil {
				return "", err
			}
		}
		if from+count < total {
			return strconv.FormatInt(from+count, 10), nil
		}
		return "", nil
	case "Notebook_GetNotebook", "Notebook_GetNotebooksByWorkspace", "SparkJobDefinition_GetSparkJobDefinition", "SparkJobDefinition_GetSparkJobDefinitionsByWorkspace":
		collection, key := "notebooks", "notebookName"
		if strings.HasPrefix(name, "SparkJobDefinition_") {
			collection, key = "sparkjobdefinitions", "sparkJobDefinitionName"
		}
		if strings.HasSuffix(name, "ByWorkspace") {
			rows, ok := res.data["value"].([]any)
			if !ok {
				return "", serviceDenied("invalid_synapse_artifact_list")
			}
			seen := map[string]bool{}
			for _, row := range rows {
				raw := object(row)
				name := text(raw["name"])
				if err := synapseArtifact(workspace, collection, name, raw); err != nil {
					return "", err
				}
				id := strings.ToLower(text(raw["id"]))
				if seen[id] {
					return "", serviceDenied("duplicate_synapse_artifact")
				}
				seen[id] = true
			}
			next := ""
			if v, ok := res.data["nextLink"]; ok {
				var valid bool
				next, valid = v.(string)
				if !valid {
					return "", serviceDenied("invalid_synapse_artifact_continuation")
				}
			}
			if next != "" {
				u, err := url.Parse(next)
				if err != nil || u.Scheme+"://"+u.Host != workspace.endpoint || !strings.EqualFold(u.Path, op.Call.Path) || u.RawPath != "" || u.User != nil || u.Port() != "" || u.Fragment != "" {
					return "", serviceDenied("synapse_artifact_collection_changed")
				}
				query, err := url.ParseQuery(u.RawQuery)
				if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != synapseDataVersion {
					return "", serviceDenied("synapse_artifact_version_changed")
				}
				for _, values := range query {
					if len(values) != 1 || values[0] == "" {
						return "", serviceDenied("invalid_synapse_artifact_continuation")
					}
				}
			}
			return next, nil
		}
		if err := synapseArtifact(workspace, collection, text(params[key]), res.data); err != nil {
			return "", err
		}
		if etag := res.header.Get("ETag"); etag != "" && res.data["etag"] != nil && etag != text(res.data["etag"]) {
			return "", serviceDenied("synapse_artifact_etag_disagrees")
		}
		return "", nil
	default:
		return "", serviceDenied("unsupported_synapse_data_read")
	}
}

func synapseSparkItem(workspace synapseWorkspace, params map[string]any, kind string, id int64, raw map[string]any) error {
	actual, err := batchInteger(raw["id"], 32)
	state, ok := raw["state"].(string)
	if err != nil || actual < 0 || actual != id || !ok || strings.TrimSpace(state) == "" {
		return serviceDenied("invalid_synapse_spark_identity")
	}
	detailed := params["detailed"] == true
	for key, expected := range map[string]string{"workspaceName": last(workspace.id), "sparkPoolName": text(params["sparkPoolName"]), "jobType": kind} {
		value, present := raw[key]
		if detailed || present {
			if value, ok := value.(string); !ok || !strings.EqualFold(value, expected) {
				return serviceDenied("synapse_spark_owner_changed")
			}
		}
	}
	// Optional detailed fields are often null in the official CLI recordings.
	// Preserve future state strings as observations; cleanup must classify them
	// explicitly before treating a job as terminal.
	for _, key := range []string{"livyInfo", "schedulerInfo", "pluginInfo", "tags", "appInfo"} {
		if v, present := raw[key]; present && v != nil {
			if _, ok := v.(map[string]any); !ok {
				return serviceDenied("invalid_synapse_spark_metadata")
			}
		}
	}
	if v, ok := raw["result"]; ok && v != nil {
		if _, ok := v.(string); !ok {
			return serviceDenied("invalid_synapse_spark_result")
		}
	}
	return nil
}

func synapseArtifact(workspace synapseWorkspace, collection, name string, raw map[string]any) error {
	id, kind, err := parseID(text(raw["id"]))
	if name == "" || strings.ContainsAny(name, "/\\%\x00\r\n") || err != nil || !strings.EqualFold(kind, synapseType+"/"+collection) || !strings.EqualFold(text(raw["type"]), synapseType+"/"+collection) || !strings.EqualFold(id, workspace.id+"/"+collection+"/"+name) || !strings.EqualFold(text(raw["name"]), name) {
		return serviceDenied("invalid_synapse_artifact_identity")
	}
	if _, ok := raw["properties"].(map[string]any); !ok {
		return serviceDenied("synapse_artifact_properties_missing")
	}
	if v, ok := raw["etag"]; ok && v != nil {
		if _, ok := v.(string); !ok {
			return serviceDenied("invalid_synapse_artifact_etag")
		}
	}
	return nil
}

// Code cells, arbitrary job configuration, output/logs, tags and future opaque
// fields can contain credentials. Keep full values private to the read and
// expose only identity, state and pool references in results and diagnostics.
func synapseDataSafeValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, v := range value {
			switch strings.ToLower(key) {
			case "id", "name", "type", "etag", "properties", "bigdatapool", "targetbigdatapool", "referencename", "workspacename", "sparkpoolname", "jobtype", "result", "state", "currentstate", "livyinfo", "schedulerinfo", "plugininfo", "from", "total", "sessions", "value", "request_id", "status_code", "body", "method", "path", "query":
				result[key] = synapseDataSafeValue(v)
			}
		}
		return result
	case []any:
		result := make([]any, len(value))
		for i, v := range value {
			result[i] = synapseDataSafeValue(v)
		}
		return result
	default:
		return value
	}
}
