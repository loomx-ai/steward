package azure

import (
	"net/url"
	"strconv"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const synapseDataInventorySource = "synapse-data"
const synapseBatchType = synapseSparkType + "/batches"
const synapseSessionType = synapseSparkType + "/sessions"
const synapseNotebookType = synapseType + "/notebooks"
const synapseJobDefinitionType = synapseType + "/sparkJobDefinitions"

type synapseDataDefinition struct {
	kind, collection, list, read, parameter string
	spark                                   bool
}

func synapseDataKind(kind string) synapseDataDefinition {
	for _, d := range []synapseDataDefinition{
		{synapseBatchType, "batches", "SparkBatch_GetSparkBatchJobs", "SparkBatch_GetSparkBatchJob", "batchId", true},
		{synapseSessionType, "sessions", "SparkSession_GetSparkSessions", "SparkSession_GetSparkSession", "sessionId", true},
		{synapseNotebookType, "notebooks", "Notebook_GetNotebooksByWorkspace", "Notebook_GetNotebook", "notebookName", false},
		{synapseJobDefinitionType, "sparkJobDefinitions", "SparkJobDefinition_GetSparkJobDefinitionsByWorkspace", "SparkJobDefinition_GetSparkJobDefinition", "sparkJobDefinitionName", false},
	} {
		if strings.EqualFold(kind, d.kind) {
			return d
		}
	}
	return synapseDataDefinition{}
}

// Spark jobs/sessions are native data-plane URLs, not ARM child resources.
// The kind groups them under their ARM pool for inventory and graph purposes.
func (c *client) synapseDataIdentity(id, kind string) (string, map[string]any, error) {
	d := synapseDataKind(kind)
	if d.kind == "" {
		return "", nil, serviceDenied("unknown_synapse_data_kind")
	}
	params := map[string]any{}
	if !d.spark {
		parsed, typ, err := parseID(id)
		parts := strings.Split(parsed, "/")
		if err != nil || !strings.EqualFold(typ, d.kind) || !strings.HasPrefix(parsed, c.root()+"/") || len(parts) != 11 {
			return "", nil, serviceDenied("invalid_synapse_artifact_id")
		}
		params["endpoint"] = "https://" + parts[8] + ".dev.azuresynapse.net"
		params[d.parameter] = last(strings.TrimSuffix(id, "/"))
		return parsed, params, nil
	}
	u, err := url.Parse(id)
	if err != nil || !synapseEndpointPattern.MatchString(u.Scheme+"://"+u.Host) || u.RawPath != "" || u.User != nil || u.Port() != "" || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery {
		return "", nil, serviceDenied("invalid_synapse_spark_url")
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) != 8 || parts[1] != "livyApi" || parts[2] != "versions" || parts[3] != synapseDataVersion || parts[4] != "sparkPools" || parts[6] != d.collection {
		return "", nil, serviceDenied("invalid_synapse_spark_path")
	}
	number, err := strconv.ParseInt(parts[7], 10, 32)
	if err != nil || number < 0 || strconv.FormatInt(number, 10) != parts[7] || parts[5] == "" || parts[5] != strings.TrimSpace(parts[5]) || strings.ContainsAny(parts[5], "%\\\x00\r\n") || parts[5] == "." || parts[5] == ".." {
		return "", nil, serviceDenied("invalid_synapse_spark_selector")
	}
	params["endpoint"] = u.Scheme + "://" + u.Host
	params["sparkPoolName"] = parts[5]
	params[d.parameter] = number
	params["detailed"] = true
	u.Path = strings.Join(parts, "/")
	return u.String(), params, nil
}

func (c *client) synapseDataOperation(kind resourceType, id, method string) (catalog.Operation, map[string]any, error) {
	if method != "GET" || len(kind.ReadOperations) != 1 {
		return catalog.Operation{}, nil, serviceDenied("synapse_data_mutation_not_implemented")
	}
	_, params, err := c.synapseDataIdentity(id, kind.NativeType)
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	op, ok := metadata.catalog.Operation(kind.ReadOperations[0])
	if !ok || op.Call == nil || op.Call.Style != "azure-synapse-rest" {
		return catalog.Operation{}, nil, serviceDenied("invalid_synapse_data_binding")
	}
	if _, err = bindAzureREST(op, params); err != nil {
		return catalog.Operation{}, nil, err
	}
	return op, params, nil
}
