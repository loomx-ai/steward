package azure

import (
	"bufio"
	"context"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (c *client) synapseOperationOwner(id string) (string, error) {
	canonical, kind, err := parseID(id)
	kind = synapseKind(kind)
	if err != nil || canonical != id || kind == "" || !strings.HasPrefix(id, c.root()+"/") || (kind == synapseType && len(strings.Split(id, "/")) != 9) || (kind != synapseType && len(strings.Split(id, "/")) != 11) {
		return "", serviceDenied("invalid_synapse_operation_owner")
	}
	return kind, nil
}

// Operation URLs come from the pinned workspace/SQL-pool operation contracts.
// A hostname suffix or the placeholder URL in DELETE examples grants no authority.
func (c *client) synapsePollURL(id, endpoint, role string) (string, error) {
	kind, err := c.synapseOperationOwner(id)
	if err != nil || c.validateURL(endpoint) != nil || endpoint != strings.TrimSpace(endpoint) || len(endpoint) > 32<<10 {
		return "", serviceDenied("invalid_synapse_poll_url")
	}
	u, _ := url.Parse(endpoint)
	parts := strings.Split(u.Path, "/")
	workspace := strings.Join(strings.Split(id, "/")[:9], "/")
	collection := "operationResults"
	if role == "status_url" {
		collection = "operationStatuses"
	} else if role != "result_url" {
		return "", serviceDenied("invalid_synapse_poll_role")
	}
	parent := strings.Join(parts[:max(0, len(parts)-2)], "/")
	validParent := strings.EqualFold(parent, workspace) || role == "result_url" && kind == synapseSQLType && strings.EqualFold(parent, id)
	name := last(u.Path)
	if u.RawPath != "" || u.ForceQuery || !validParent || len(parts) < 3 || !strings.EqualFold(parts[len(parts)-2], collection) || name == "" || name != parts[len(parts)-1] || name == "." || name == ".." || name != strings.TrimSpace(name) || strings.ContainsAny(name, "%\\\x00\r\n") {
		return "", serviceDenied("synapse_poll_scope_changed")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q) != 1 || len(q["api-version"]) != 1 || q.Get("api-version") != synapseVersion {
		return "", serviceDenied("synapse_poll_version_changed")
	}
	return name, nil
}
func (c *client) synapseOperationHeaders(id string, h http.Header) (map[string]any, error) {
	if len(h.Values("Operation-Location")) != 0 || len(h.Values("Azure-AsyncOperation")) > 1 || len(h.Values("Location")) > 1 {
		return nil, serviceDenied("ambiguous_synapse_operation_headers")
	}
	result := map[string]any{}
	identity := ""
	for _, entry := range []struct{ header, role string }{{"Azure-AsyncOperation", "status_url"}, {"Location", "result_url"}} {
		v := h.Get(entry.header)
		if v == "" {
			if len(h.Values(entry.header)) != 0 {
				return nil, serviceDenied("empty_synapse_operation_header")
			}
			continue
		}
		current, err := c.synapsePollURL(id, v, entry.role)
		if err != nil {
			return nil, err
		}
		if identity != "" && identity != current {
			return nil, serviceDenied("synapse_operation_headers_disagree")
		}
		identity = current
		result[entry.role] = v
	}
	return result, nil
}
func (c *client) synapseSignReceipt(id string, receipt map[string]any) map[string]any {
	result := maps.Clone(receipt)
	if result == nil {
		result = map[string]any{}
	}
	delete(result, "binding")
	result["binding"] = c.privateConfiguration(map[string]any{"protocol": "synapse-arm-delete-1", "resource": id, "receipt": result})
	return result
}
func (c *client) synapseDeleteReceipt(id string, res response) (map[string]any, error) {
	kind, err := c.synapseOperationOwner(id)
	if err != nil {
		return nil, err
	}
	if err = operationError(res); err != nil {
		return nil, err
	}
	if !slices.Contains([]int{200, 202, 204}, res.status) || res.data["error"] != nil || res.data["code"] != nil || res.data["nextLink"] != nil {
		return nil, serviceDenied("invalid_synapse_delete_response")
	}
	if len(res.data) != 0 {
		if res.status == 204 || c.synapseMetadata(res.data, kind) != nil || !strings.EqualFold(text(res.data["id"]), id) {
			return nil, serviceDenied("synapse_delete_body_changed_owner")
		}
	}
	receipt, err := c.synapseOperationHeaders(id, res.header)
	if err != nil {
		return nil, err
	}
	if res.status == 202 && len(receipt) == 0 || res.status == 204 && len(receipt) != 0 {
		return nil, serviceDenied("incomplete_synapse_delete_receipt")
	}
	return c.synapseSignReceipt(id, receipt), nil
}
func (c *client) synapseVerifyReceipt(id string, receipt map[string]any) error {
	if _, err := c.synapseOperationOwner(id); err != nil {
		return err
	}
	if receipt["binding"] != c.synapseSignReceipt(id, receipt)["binding"] {
		return serviceDenied("synapse_saved_receipt_changed")
	}
	h := http.Header{}
	for key, value := range receipt {
		switch key {
		case "binding":
		case "status_done", "complete":
			if value != true {
				return serviceDenied("invalid_synapse_saved_phase")
			}
		case "status_url", "result_url":
			v, ok := value.(string)
			if !ok || v == "" {
				return serviceDenied("invalid_synapse_saved_url")
			}
			header := "Location"
			if key == "status_url" {
				header = "Azure-AsyncOperation"
			}
			h.Set(header, v)
		default:
			return serviceDenied("unknown_synapse_saved_field")
		}
	}
	if receipt["status_done"] == true && (receipt["status_url"] == nil || receipt["result_url"] == nil) {
		return serviceDenied("invalid_synapse_saved_phase")
	}
	_, err := c.synapseOperationHeaders(id, h)
	return err
}

// Operation completion is distinct from resource absence. A caller must still
// read the reviewed resource itself, even when this function reports Done.
func (c *client) synapsePollObservation(id, endpoint, role string, res response) (bool, string, error) {
	identity, err := c.synapsePollURL(id, endpoint, role)
	if err != nil {
		return false, "", err
	}
	if err = operationError(res); err != nil {
		return false, "", err
	}
	if res.data["error"] != nil || res.data["code"] != nil || res.data["nextLink"] != nil {
		return false, "", serviceDenied("invalid_synapse_poll_response")
	}
	if role == "result_url" {
		if !slices.Contains([]int{200, 201, 202, 204}, res.status) {
			return false, "", serviceDenied("invalid_synapse_result_status")
		}
		if len(res.data) == 0 {
			return res.status != 202, "", nil
		}
		kind, _ := c.synapseOperationOwner(id)
		if res.status == 204 || c.synapseMetadata(res.data, kind) != nil || !strings.EqualFold(text(res.data["id"]), id) {
			return false, "", serviceDenied("synapse_result_body_changed_owner")
		}
		state := text(object(res.data["properties"])["provisioningState"])
		if state == "Succeeded" && res.status != 202 {
			return true, state, nil
		}
		if slices.Contains([]string{"Accepted", "Creating", "Updating", "Deleting", "InProgress"}, state) {
			return false, state, nil
		}
		return false, "", serviceDenied("synapse_result_state_unverified")
	}
	if res.status != 200 {
		return false, "", serviceDenied("invalid_synapse_status_response")
	}
	u, _ := url.Parse(endpoint)
	for key, expected := range map[string]string{"name": identity, "resourceId": id} {
		if value, present := res.data[key]; present && (value != text(value) || !strings.EqualFold(text(value), expected)) {
			return false, "", serviceDenied("synapse_operation_identity_changed")
		}
	}
	if value, present := res.data["id"]; present && (value != text(value) || text(value) != identity && !strings.EqualFold(text(value), u.Path)) {
		return false, "", serviceDenied("synapse_operation_identity_changed")
	}
	state, ok := res.data["status"].(string)
	if !ok || state != "InProgress" && state != "Succeeded" {
		return false, "", serviceDenied("synapse_status_unverified")
	}
	return state == "Succeeded", state, nil
}
func (c *client) synapsePoll(ctx context.Context, id string, receipt map[string]any) (out contracts.WaitResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err := c.synapseVerifyReceipt(id, receipt); err != nil {
		return contracts.WaitResult{}, err
	}
	current := maps.Clone(receipt)
	if current["complete"] == true || current["status_url"] == nil && current["result_url"] == nil {
		return contracts.WaitResult{Done: true, Data: current}, nil
	}
	role := "status_url"
	if current[role] == nil || current["status_done"] == true {
		role = "result_url"
	}
	endpoint := text(current[role])
	identity, _ := c.synapsePollURL(id, endpoint, role)
	validate := func(next string) error {
		if next != endpoint {
			return serviceDenied("synapse_poll_url_changed")
		}
		_, err := c.synapsePollURL(id, next, role)
		return err
	}
	res, err := c.requestUsing(ctx, "GET", endpoint, nil, nil, validate, c.synapsePollHTTP(role), role == "result_url")
	if err != nil {
		return contracts.WaitResult{}, err
	}
	done, state, err := c.synapsePollObservation(id, endpoint, role, res)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	next, err := c.synapseOperationHeaders(id, res.header)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	for key, value := range next {
		actual, err := c.synapsePollURL(id, text(value), key)
		// Endpoints may not change collection/owner/operation while resuming.
		if err != nil || actual != identity || current[key] != value {
			return contracts.WaitResult{}, serviceDenied("synapse_poll_continuation_changed")
		}
	}
	if done && role == "status_url" && current["result_url"] != nil {
		current["status_done"] = true
		done = false
	}
	if done {
		current["complete"] = true
	}
	return contracts.WaitResult{Done: done, State: state, RetryAfter: retryAfter(res.header), Data: c.synapseSignReceipt(id, current)}, nil
}

func synapseARMDelete(operation string) bool {
	return slices.Contains([]string{"Azure.Microsoft.Synapse.Workspaces_Delete", "Azure.Microsoft.Synapse.BigDataPools_Delete", "Azure.Microsoft.Synapse.SqlPools_Delete"}, operation)
}
func synapseARMOperationRole(operation string) string {
	switch operation {
	case "Azure.Microsoft.Synapse.Operations_GetAzureAsyncHeaderResult":
		return "status_url"
	case "Azure.Microsoft.Synapse.Operations_GetLocationHeaderResult", "Azure.Microsoft.Synapse.SqlPoolOperationResults_GetLocationHeaderResult":
		return "result_url"
	}
	return ""
}
func (c *client) invokeSynapseOperation(ctx context.Context, operation string, request catalog.RESTRequest) (contracts.InvocationResult, error) {
	role := synapseARMOperationRole(operation)
	u, _ := url.Parse(request.URL)
	parts := strings.Split(u.Path, "/")
	id := strings.ToLower(strings.Join(parts[:len(parts)-2], "/"))
	validate := func(candidate string) error {
		if candidate != request.URL {
			return serviceDenied("synapse_poll_url_changed")
		}
		_, err := c.synapsePollURL(id, candidate, role)
		return err
	}
	res, err := c.requestUsing(ctx, "GET", request.URL, nil, request.Headers, validate, c.synapsePollHTTP(role), role == "result_url")
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	done, _, err := c.synapsePollObservation(id, request.URL, role, res)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	headers, err := c.synapseOperationHeaders(id, res.header)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	expected, _ := c.synapsePollURL(id, request.URL, role)
	for key, value := range headers {
		actual, err := c.synapsePollURL(id, text(value), key)
		if err != nil || actual != expected {
			return contracts.InvocationResult{}, serviceDenied("synapse_poll_continuation_changed")
		}
	}
	data := safePayload(object(synapseOperationSafeValue(res.data)))
	data["_synapse_operation_done"] = done
	return contracts.InvocationResult{Data: data, OperationID: request.URL, RequestID: res.requestID}, nil
}

func synapseOperationSafeValue(value any) any {
	switch value := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for key, v := range value {
			switch strings.ToLower(key) {
			case "id", "name", "type", "location", "status", "provisioningstate", "starttime", "endtime", "percentcomplete", "resourceid", "request_id", "status_code", "body", "method", "path", "query", "api-version":
				out[key] = synapseOperationSafeValue(v)
			}
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, v := range value {
			out[i] = synapseOperationSafeValue(v)
		}
		return out
	default:
		return value
	}
}

// The pinned Location contract includes empty 201 responses. The shared ARM
// reader accepts empty 200/202/204, so normalize only that empty result body to
// its equivalent empty object, preserving status, headers and all nonempty bytes.
type synapseResultTransport struct{ base http.RoundTripper }

func (t synapseResultTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	res, err := t.base.RoundTrip(request)
	if err != nil || res.StatusCode != http.StatusCreated || res.Body == nil {
		return res, err
	}
	original := res.Body
	reader := bufio.NewReader(original)
	if _, err := reader.Peek(1); err == io.EOF {
		original.Close()
		res.Body = io.NopCloser(strings.NewReader("{}"))
	} else {
		res.Body = struct {
			io.Reader
			io.Closer
		}{reader, original}
	}
	return res, nil
}
func (c *client) synapsePollHTTP(role string) *http.Client {
	if role != "result_url" {
		return c.http
	}
	transport := c.http.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	current := *c.http
	current.Transport = synapseResultTransport{base: transport}
	return &current
}
