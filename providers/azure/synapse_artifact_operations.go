package azure

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func synapseArtifactDelete(operation string) synapseDataDefinition {
	switch operation {
	case synapseDataOperationPrefix + "Notebook_DeleteNotebook":
		return synapseDataKind(synapseNotebookType)
	case synapseDataOperationPrefix + "SparkJobDefinition_DeleteSparkJobDefinition":
		return synapseDataKind(synapseJobDefinitionType)
	case synapseDataOperationPrefix + "Pipeline_DeletePipeline":
		return synapsePipelineDefinition
	}
	return synapseDataDefinition{}
}

func synapseArtifactOperation(operation string) string {
	switch operation {
	case synapseDataOperationPrefix + "NotebookOperationResult_Get":
		return "notebookOperationResults"
	case synapseDataOperationPrefix + "OperationResult_Get":
		return "operationResults"
	case synapseDataOperationPrefix + "OperationStatus_Get":
		return "operationStatuses"
	}
	return ""
}

// Only the workspace-bound native operation collections are polling targets.
// A Location is not an authorization token and never changes the OAuth audience.
func synapseArtifactPollURL(workspace synapseWorkspace, endpoint string) (collection, operation string, err error) {
	u, parseErr := url.Parse(endpoint)
	if parseErr != nil || len(endpoint) > 32768 || u.Scheme+"://"+u.Host != workspace.endpoint || u.User != nil || u.Port() != "" || u.RawPath != "" || u.Fragment != "" || u.ForceQuery {
		return "", "", serviceDenied("invalid_synapse_artifact_poll_origin")
	}
	parts := strings.Split(u.Path, "/")
	query, parseErr := url.ParseQuery(u.RawQuery)
	if len(parts) != 3 || !slices.Contains([]string{"notebookOperationResults", "operationResults", "operationStatuses"}, parts[1]) || parts[2] == "" || strings.TrimSpace(parts[2]) != parts[2] || strings.ContainsAny(parts[2], "%\\\x00\r\n") || parts[2] == "." || parts[2] == ".." || parseErr != nil || len(query) != 1 || len(query["api-version"]) != 1 || query.Get("api-version") != synapseDataVersion {
		return "", "", serviceDenied("invalid_synapse_artifact_poll_path")
	}
	return parts[1], parts[2], nil
}

func (c *synapseDataClient) artifactReceiptBinding(workspace synapseWorkspace, id string, receipt map[string]any) string {
	copy := maps.Clone(receipt)
	delete(copy, "binding")
	return c.arm.privateConfiguration(map[string]any{"protocol": "synapse-artifact-delete-1", "workspace": workspace.id, "endpoint": workspace.endpoint, "id": id, "receipt": copy})
}

func (c *synapseDataClient) artifactOwner(workspace synapseWorkspace, d synapseDataDefinition, id string) error {
	canonical, kind, err := parseID(id)
	if err != nil || canonical != id || !slices.Contains([]string{synapseNotebookType, synapseJobDefinitionType, synapsePipelineDefinition.kind}, d.kind) || !strings.EqualFold(kind, d.kind) || !strings.HasPrefix(id, c.arm.root()+"/") || len(strings.Split(id, "/")) != 11 || strings.Join(strings.Split(id, "/")[:9], "/") != workspace.id || !synapseEndpointPattern.MatchString(workspace.endpoint) || workspace.endpoint != "https://"+last(workspace.id)+".dev.azuresynapse.net" {
		return serviceDenied("invalid_synapse_artifact_owner")
	}
	return nil
}

func (c *synapseDataClient) artifactDeleteReceipt(workspace synapseWorkspace, d synapseDataDefinition, id string, res response) (map[string]any, error) {
	if err := c.artifactOwner(workspace, d, id); err != nil {
		return nil, err
	}
	if !slices.Contains([]int{200, 202, 204}, res.status) || res.header.Get("Operation-Location") != "" || res.data["code"] != nil || operationError(res) != nil {
		return nil, serviceDenied("invalid_synapse_artifact_delete_receipt")
	}
	out := map[string]any{}
	opID := ""
	for header, key := range map[string]string{"Location": "result_url", "Azure-AsyncOperation": "status_url"} {
		values := res.header.Values(header)
		if len(values) == 0 {
			continue
		}
		if len(values) != 1 || values[0] == "" {
			return nil, serviceDenied("ambiguous_synapse_artifact_callback")
		}
		collection, current, err := synapseArtifactPollURL(workspace, values[0])
		expected := "operationResults"
		if d.kind == synapseNotebookType {
			expected = "notebookOperationResults"
		}
		if key == "status_url" {
			expected = "operationStatuses"
		}
		if err != nil || collection != expected || opID != "" && current != opID {
			return nil, serviceDenied("synapse_artifact_callback_changed")
		}
		opID = current
		out[key] = values[0]
	}
	if res.status == 202 && len(out) == 0 || res.status == 204 && (len(out) != 0 || len(res.data) != 0) {
		return nil, serviceDenied("incomplete_synapse_artifact_delete_receipt")
	}
	if len(res.data) != 0 {
		typ := map[string]string{synapseNotebookType: "Notebook", synapseJobDefinitionType: "SparkJobDefinition", synapsePipelineDefinition.kind: "Pipeline"}[d.kind]
		if res.status != 202 || res.data["state"] != "Deleting" || !strings.EqualFold(text(res.data["id"]), id) || res.data["type"] != typ || !strings.EqualFold(text(res.data["name"]), last(id)) || res.data["operationId"] != opID {
			return nil, serviceDenied("synapse_artifact_delete_identity_changed")
		}
		if _, err := batchInteger(res.data["recordId"], 64); err != nil {
			return nil, serviceDenied("invalid_synapse_artifact_record")
		}
	}
	if len(out) == 0 {
		out["complete"] = true
	}
	out["binding"] = c.artifactReceiptBinding(workspace, id, out)
	return out, nil
}

func (c *synapseDataClient) verifyArtifactReceipt(workspace synapseWorkspace, d synapseDataDefinition, id string, receipt map[string]any) error {
	if err := c.artifactOwner(workspace, d, id); err != nil {
		return err
	}
	if receipt == nil || receipt["binding"] != c.artifactReceiptBinding(workspace, id, receipt) {
		return serviceDenied("synapse_artifact_receipt_changed")
	}
	for key, value := range receipt {
		switch key {
		case "binding":
		case "complete", "status_done":
			if value != true {
				return serviceDenied("invalid_synapse_artifact_receipt_flag")
			}
		case "result_url", "status_url":
			collection, _, err := synapseArtifactPollURL(workspace, text(value))
			expected := "operationResults"
			if d.kind == synapseNotebookType {
				expected = "notebookOperationResults"
			}
			if key == "status_url" {
				expected = "operationStatuses"
			}
			if err != nil || collection != expected {
				return serviceDenied("synapse_artifact_receipt_scope_changed")
			}
		default:
			return serviceDenied("unknown_synapse_artifact_receipt_field")
		}
	}
	return nil
}

func synapseArtifactPollResponse(operation string, res response) (bool, error) {
	if !slices.Contains([]int{200, 201, 202, 204}, res.status) {
		return false, serviceDenied("invalid_synapse_artifact_poll_status")
	}
	if len(res.data) == 0 {
		return res.status != 202, nil
	}
	if res.status == 204 || operationError(res) != nil || res.data["code"] != nil {
		return false, serviceDenied("failed_synapse_artifact_operation")
	}
	if id, present := res.data["id"]; present && id != operation {
		return false, serviceDenied("synapse_artifact_operation_identity_changed")
	}
	switch res.data["status"] {
	case "InProgress":
		return false, nil
	case "Succeeded":
		if res.status == 202 {
			return false, serviceDenied("inconsistent_synapse_artifact_operation")
		}
		return true, nil
	}
	return false, serviceDenied("unknown_synapse_artifact_operation_state")
}

func (c *synapseDataClient) artifactPollRequest(ctx context.Context, workspace synapseWorkspace, endpoint string, headers map[string]string) (response, error) {
	if _, _, err := synapseArtifactPollURL(workspace, endpoint); err != nil {
		return response{}, err
	}
	actual, err := synapseWorkspaceEndpoint(workspace.raw)
	if err != nil || actual != workspace.endpoint || c.arm.synapseMetadata(workspace.raw, synapseType) != nil || !strings.EqualFold(text(workspace.raw["id"]), workspace.id) {
		return response{}, serviceDenied("invalid_synapse_workspace_context")
	}
	transport := c.http.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	current := *c.http
	current.Transport = synapseResultTransport{base: transport}
	validate := func(next string) error {
		if next != endpoint {
			return serviceDenied("synapse_artifact_poll_redirect")
		}
		_, _, err := synapseArtifactPollURL(workspace, next)
		return err
	}
	return c.arm.requestUsing(ctx, "GET", endpoint, nil, headers, validate, &current, true)
}

func (c *synapseDataClient) pollArtifact(ctx context.Context, workspace synapseWorkspace, d synapseDataDefinition, id string, receipt map[string]any) (out contracts.WaitResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err = c.verifyArtifactReceipt(workspace, d, id, receipt); err != nil {
		return out, err
	}
	current := maps.Clone(receipt)
	if current["complete"] == true {
		return contracts.WaitResult{Done: true, Data: current}, nil
	}
	role := "status_url"
	if current[role] == nil || current["status_done"] == true {
		role = "result_url"
	}
	endpoint := text(current[role])
	_, operation, _ := synapseArtifactPollURL(workspace, endpoint)
	res, err := c.artifactPollRequest(ctx, workspace, endpoint, nil)
	if err != nil {
		return out, err
	}
	for header, key := range map[string]string{"Location": "result_url", "Azure-AsyncOperation": "status_url"} {
		values := res.header.Values(header)
		if len(values) > 1 || len(values) == 1 && values[0] != current[key] {
			return out, serviceDenied("synapse_artifact_poll_continuation_changed")
		}
	}
	if res.header.Get("Operation-Location") != "" {
		return out, serviceDenied("unexpected_synapse_artifact_poll_header")
	}
	done, err := synapseArtifactPollResponse(operation, res)
	if err != nil {
		return out, err
	}
	if done && role == "status_url" && current["result_url"] != nil {
		current["status_done"] = true
		done = false
	}
	if done {
		current["complete"] = true
	}
	current["binding"] = c.artifactReceiptBinding(workspace, id, current)
	return contracts.WaitResult{Done: done, Data: current, State: text(res.data["status"]), RetryAfter: retryAfter(res.header)}, nil
}

func (c *synapseDataClient) invokeArtifactOperation(ctx context.Context, op catalog.Operation, inv contracts.Invocation) (contracts.InvocationResult, error) {
	bound, err := bindAzureREST(op, inv.Parameters)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	w, err := c.arm.synapseWorkspaceForEndpoint(ctx, text(inv.Parameters["endpoint"]))
	if err != nil {
		return contracts.InvocationResult{}, contracts.DependencyReadError(err)
	}
	headers := maps.Clone(bound.Headers)
	if headers == nil {
		headers = map[string]string{}
	}
	if inv.IdempotencyKey != "" {
		headers["x-ms-client-request-id"] = azureRequestID(inv.IdempotencyKey)
	}
	res, err := c.artifactPollRequest(ctx, w, bound.URL, headers)
	if err != nil {
		return contracts.InvocationResult{}, contracts.DependencyReadError(err)
	}
	for _, header := range []string{"Location", "Azure-AsyncOperation"} {
		values := res.header.Values(header)
		if len(values) > 1 || len(values) == 1 && values[0] != bound.URL {
			return contracts.InvocationResult{}, serviceDenied("synapse_artifact_operation_header_changed")
		}
	}
	if res.header.Get("Operation-Location") != "" {
		return contracts.InvocationResult{}, serviceDenied("unexpected_synapse_artifact_poll_header")
	}
	_, operation, err := synapseArtifactPollURL(w, bound.URL)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	done, err := synapseArtifactPollResponse(operation, res)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	if err = c.verifySynapseDataTarget(ctx, synapseDataTarget{workspace: w}); err != nil {
		return contracts.InvocationResult{}, contracts.DependencyReadError(err)
	}
	return contracts.InvocationResult{RequestID: res.requestID, OperationID: bound.URL, Data: map[string]any{"operation_done": done, "status": res.data["status"]}}, nil
}
