package azure

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func hybridComputeDeletableKind(kind string) bool {
	return slices.Contains([]string{hybridMachineType, hybridExtensionType, hybridCommandType, hybridProfileType, hybridLicenseType}, kind)
}

// The returned UUID identifies a native ProviderHub operation. Its signature
// may rotate, but the subscription, region, UUID, version and URL role may not.
func (c *client) hybridComputePollURL(id, endpoint, role string) (string, error) {
	canonical, typ, err := parseID(id)
	if err != nil || canonical != id || !hybridComputeDeletableKind(hybridComputeKind(typ)) || hybridComputeKind(typ) == hybridLicenseType || !strings.HasPrefix(id, c.root()+"/") || c.validateURL(endpoint) != nil || len(endpoint) > 32<<10 || endpoint != strings.TrimSpace(endpoint) {
		return "", serviceDenied("invalid_hybrid_compute_operation_owner")
	}
	u, _ := url.Parse(endpoint)
	parts := strings.Split(strings.ToLower(u.Path), "/")
	expected := "operationstatus"
	if role == "result_url" {
		expected = "operationresults"
	} else if role != "status_url" {
		return "", serviceDenied("invalid_hybrid_compute_operation_role")
	}
	if u.RawPath != "" || u.ForceQuery || len(parts) != 9 || strings.Join(parts[:6], "/") != c.root()+"/providers/microsoft.hybridcompute/locations" || !cosmosOperationRegion.MatchString(parts[6]) || parts[7] != expected || !uuidPattern.MatchString(parts[8]) {
		return "", serviceDenied("hybrid_compute_operation_scope_changed")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != hybridComputeVersion || len(query) != 1 && len(query) != 5 {
		return "", serviceDenied("invalid_hybrid_compute_operation_query")
	}
	if len(query) == 5 {
		for _, key := range []string{"t", "c", "s", "h"} {
			if len(query[key]) != 1 || query.Get(key) == "" || strings.ContainsAny(query.Get(key), "\x00\r\n\t ") {
				return "", serviceDenied("invalid_hybrid_compute_operation_signature")
			}
		}
	}
	return parts[6] + "/" + parts[8], nil
}

func (c *client) hybridComputeOperationHeaders(id string, header http.Header) (map[string]any, error) {
	if len(header.Values("Operation-Location")) != 0 || len(header.Values("Azure-AsyncOperation")) > 1 || len(header.Values("Location")) > 1 {
		return nil, serviceDenied("ambiguous_hybrid_compute_operation_headers")
	}
	result, identity := map[string]any{}, ""
	for _, entry := range []struct{ header, key string }{{"Azure-AsyncOperation", "status_url"}, {"Location", "result_url"}} {
		value := header.Get(entry.header)
		if value == "" {
			if len(header.Values(entry.header)) != 0 {
				return nil, serviceDenied("empty_hybrid_compute_operation_header")
			}
			continue
		}
		actual, err := c.hybridComputePollURL(id, value, entry.key)
		if err != nil {
			return nil, err
		}
		if identity != "" && identity != actual {
			return nil, serviceDenied("hybrid_compute_operation_headers_disagree")
		}
		identity, result[entry.key] = actual, value
	}
	return result, nil
}

func (c *client) hybridComputeSignReceipt(id string, receipt map[string]any) map[string]any {
	result := maps.Clone(receipt)
	if result == nil {
		result = map[string]any{}
	}
	delete(result, "binding")
	result["binding"] = c.privateConfiguration(map[string]any{"protocol": "hybrid-compute-delete-1", "resource": id, "receipt": result})
	return result
}

func (c *client) hybridComputeDeleteReceipt(id string, res response) (map[string]any, error) {
	canonical, typ, err := parseID(id)
	kind := hybridComputeKind(typ)
	if err != nil || canonical != id || !hybridComputeDeletableKind(kind) || !strings.HasPrefix(id, c.root()+"/") {
		return nil, serviceDenied("invalid_hybrid_compute_delete_owner")
	}
	if err := operationError(res); err != nil {
		return nil, err
	}
	if kind == hybridLicenseType {
		if len(res.data) != 0 || (res.status != 200 && res.status != 204) || operationLocation(res.header) != "" {
			return nil, serviceDenied("invalid_hybrid_compute_license_delete_response")
		}
		if _, err := c.hybridComputeOperationHeaders(id, res.header); err != nil {
			return nil, err
		}
		return c.hybridComputeSignReceipt(id, nil), nil
	}
	if len(res.data) != 0 || res.status != 202 && res.status != 204 && !(res.status == 200 && kind == hybridExtensionType) {
		return nil, serviceDenied("invalid_hybrid_compute_delete_response")
	}
	receipt, err := c.hybridComputeOperationHeaders(id, res.header)
	if err != nil {
		return nil, err
	}
	if res.status == 202 && len(receipt) == 0 || res.status == 204 && len(receipt) != 0 || kind == hybridCommandType && res.status == 202 && receipt["result_url"] == nil {
		return nil, serviceDenied("incomplete_hybrid_compute_delete_receipt")
	}
	return c.hybridComputeSignReceipt(id, receipt), nil
}

// Verify before any request, including synchronous or already-completed
// receipts. A caller cannot turn a tampered empty map into successful deletion.
func (c *client) hybridComputeVerifyReceipt(id string, receipt map[string]any) error {
	canonical, typ, err := parseID(id)
	if err != nil || canonical != id || !hybridComputeDeletableKind(hybridComputeKind(typ)) || !strings.HasPrefix(id, c.root()+"/") || receipt["binding"] != c.hybridComputeSignReceipt(id, receipt)["binding"] {
		return serviceDenied("hybrid_compute_saved_receipt_changed")
	}
	header := http.Header{}
	for key, value := range receipt {
		switch key {
		case "binding":
		case "status_done", "complete":
			if value != true {
				return serviceDenied("invalid_hybrid_compute_saved_phase")
			}
		case "status_url", "result_url":
			endpoint, ok := value.(string)
			if !ok || endpoint == "" {
				return serviceDenied("invalid_hybrid_compute_saved_url")
			}
			name := "Location"
			if key == "status_url" {
				name = "Azure-AsyncOperation"
			}
			header.Set(name, endpoint)
		default:
			return serviceDenied("unknown_hybrid_compute_saved_field")
		}
	}
	_, err = c.hybridComputeOperationHeaders(id, header)
	return err
}

// This verifies operation completion only. The cleanup driver must still read
// every reviewed resource itself; neither operation success nor operation 404
// proves resource absence. The caller must persist returned Data for resume.
func (c *client) hybridComputePoll(ctx context.Context, id string, receipt map[string]any) (contracts.WaitResult, error) {
	if err := c.hybridComputeVerifyReceipt(id, receipt); err != nil {
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
	identity, err := c.hybridComputePollURL(id, endpoint, role)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	validate := func(candidate string) error {
		if candidate != endpoint {
			return serviceDenied("hybrid_compute_poll_url_changed")
		}
		_, err := c.hybridComputePollURL(id, candidate, role)
		return err
	}
	res, err := c.requestUsing(ctx, "GET", endpoint, nil, nil, validate, c.http, role == "result_url")
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if err := operationError(res); err != nil {
		return contracts.WaitResult{}, err
	}
	if res.status != 200 && res.status != 202 && !(role == "result_url" && res.status == 204) || res.data["error"] != nil || res.data["code"] != nil || res.data["nextLink"] != nil {
		return contracts.WaitResult{}, serviceDenied("invalid_hybrid_compute_poll_response")
	}
	u, _ := url.Parse(endpoint)
	for key, expected := range map[string]string{"name": last(u.Path), "id": u.Path, "resourceId": id} {
		value, present := res.data[key]
		if (role == "status_url" && key == "name" || present) && !strings.EqualFold(text(value), expected) {
			return contracts.WaitResult{}, serviceDenied("hybrid_compute_poll_identity_changed")
		}
	}
	state := text(res.data["status"])
	complete := role == "result_url" && len(res.data) == 0 && (res.status == 200 || res.status == 204)
	if !complete {
		if !slices.Contains([]string{"Queued", "Accepted", "InProgress", "Running", "Succeeded"}, state) || state == "Succeeded" && res.status != 200 {
			return contracts.WaitResult{}, serviceDenied("hybrid_compute_poll_state_unverified")
		}
		complete = state == "Succeeded"
	}
	next, err := c.hybridComputeOperationHeaders(id, res.header)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	for key, value := range next {
		actual, err := c.hybridComputePollURL(id, text(value), key)
		if err != nil || actual != identity || current[key] == nil {
			return contracts.WaitResult{}, serviceDenied("hybrid_compute_poll_rotation_changed_operation")
		}
		current[key] = value // Preserve the complete, newly returned signed URL.
	}
	if complete && role == "status_url" && current["result_url"] != nil {
		current["status_done"], complete = true, false
	}
	if complete {
		current["complete"] = true
	}
	return contracts.WaitResult{Done: complete, State: state, RetryAfter: retryAfter(res.header), Data: c.hybridComputeSignReceipt(id, current)}, nil
}
