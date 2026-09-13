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

func (c *client) elasticSanDeleteOwner(id, location string) error {
	_, typ, err := parseID(id)
	canonical, identityErr := c.elasticSanIdentity(id, elasticSanKind(typ))
	if err != nil || identityErr != nil || canonical != id || location != strings.ToLower(location) || !cosmosOperationRegion.MatchString(location) {
		return serviceDenied("invalid_elastic_san_delete_owner")
	}
	return nil
}

// The official CLI recording uses regional ProviderHub asyncoperations with
// monitor=true. Signed query values stay opaque and never become log fields.
func (c *client) elasticSanPollURL(id, location, endpoint string) (string, error) {
	if c.elasticSanDeleteOwner(id, location) != nil || c.validateURL(endpoint) != nil || len(endpoint) > 32<<10 || endpoint != strings.TrimSpace(endpoint) {
		return "", serviceDenied("invalid_elastic_san_operation_url")
	}
	u, _ := url.Parse(endpoint)
	parts := strings.Split(strings.ToLower(u.Path), "/")
	if u.RawPath != "" || u.ForceQuery || len(parts) != 9 || strings.Join(parts[:6], "/") != c.root()+"/providers/microsoft.elasticsan/locations" || parts[6] != location || parts[7] != "asyncoperations" || !uuidPattern.MatchString(parts[8]) {
		return "", serviceDenied("elastic_san_operation_scope_changed")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != elasticSanVersion || len(query["monitor"]) != 1 || query.Get("monitor") != "true" || len(query) != 2 && len(query) != 6 {
		return "", serviceDenied("invalid_elastic_san_operation_query")
	}
	if len(query) == 6 {
		for _, key := range []string{"t", "c", "s", "h"} {
			if len(query[key]) != 1 || query.Get(key) == "" || strings.ContainsAny(query.Get(key), "\x00\r\n\t ") {
				return "", serviceDenied("invalid_elastic_san_operation_signature")
			}
		}
	}
	return parts[6] + "/" + parts[8], nil
}

func (c *client) elasticSanOperationLocation(id, location string, header http.Header) (string, error) {
	// The reviewed native contract and recorded DELETE use Location. Do not
	// reinterpret an unreviewed async/status header as the same protocol.
	if len(header.Values("Operation-Location"))+len(header.Values("Azure-AsyncOperation")) != 0 || len(header.Values("Location")) > 1 {
		return "", serviceDenied("ambiguous_elastic_san_operation_headers")
	}
	endpoint := header.Get("Location")
	if endpoint == "" {
		if len(header.Values("Location")) != 0 {
			return "", serviceDenied("empty_elastic_san_operation_header")
		}
		return "", nil
	}
	_, err := c.elasticSanPollURL(id, location, endpoint)
	return endpoint, err
}

func (c *client) elasticSanSignReceipt(id, location string, receipt map[string]any) map[string]any {
	result := maps.Clone(receipt)
	if result == nil {
		result = map[string]any{}
	}
	delete(result, "binding")
	result["binding"] = c.privateConfiguration(map[string]any{"protocol": "elastic-san-delete-1", "id": id, "location": location, "receipt": result})
	return result
}

// Optional resource bodies are observed in real CLI DELETE recordings, even
// though the generated REST examples have no body. They must identify the owner.
func (c *client) elasticSanDeleteReceipt(id, location string, res response) (map[string]any, error) {
	if err := c.elasticSanDeleteOwner(id, location); err != nil {
		return nil, err
	}
	if err := operationError(res); err != nil {
		return nil, err
	}
	if !slices.Contains([]int{200, 202, 204}, res.status) || res.data["error"] != nil || res.data["nextLink"] != nil {
		return nil, serviceDenied("invalid_elastic_san_delete_response")
	}
	if len(res.data) != 0 {
		_, typ, _ := parseID(id)
		actual, err := c.elasticSanRecord(res.data, elasticSanKind(typ))
		if err != nil || actual != id || res.status == 204 || text(res.data["location"]) != "" && !strings.EqualFold(text(res.data["location"]), location) {
			return nil, serviceDenied("elastic_san_delete_body_changed_owner")
		}
	}
	// 204 is terminal. Never persist or follow diagnostic callback headers.
	if res.status == 204 {
		return c.elasticSanSignReceipt(id, location, nil), nil
	}
	endpoint, err := c.elasticSanOperationLocation(id, location, res.header)
	if err != nil {
		return nil, err
	}
	if endpoint == "" {
		if res.status == 202 {
			return nil, serviceDenied("missing_elastic_san_delete_location")
		}
		return c.elasticSanSignReceipt(id, location, nil), nil
	}
	return c.elasticSanSignReceipt(id, location, map[string]any{"url": endpoint}), nil
}

func (c *client) elasticSanVerifyReceipt(id, location string, receipt map[string]any) error {
	if err := c.elasticSanDeleteOwner(id, location); err != nil {
		return err
	}
	if receipt["binding"] != c.elasticSanSignReceipt(id, location, receipt)["binding"] {
		return serviceDenied("elastic_san_saved_receipt_changed")
	}
	for key, value := range receipt {
		switch key {
		case "binding":
		case "complete":
			if value != true {
				return serviceDenied("invalid_elastic_san_saved_phase")
			}
		case "url":
			endpoint, ok := value.(string)
			if !ok || endpoint == "" {
				return serviceDenied("invalid_elastic_san_saved_url")
			}
			if _, err := c.elasticSanPollURL(id, location, endpoint); err != nil {
				return err
			}
		default:
			return serviceDenied("unknown_elastic_san_saved_field")
		}
	}
	return nil
}

// Done means transport completion only. The cleanup driver must separately
// establish the reviewed active/retained resource outcome; callback 404 is never
// resource absence. Completed receipts are validated before skipping polling.
func (c *client) elasticSanPoll(ctx context.Context, id, location string, receipt map[string]any) (contracts.WaitResult, error) {
	if err := c.elasticSanVerifyReceipt(id, location, receipt); err != nil {
		return contracts.WaitResult{}, err
	}
	current := maps.Clone(receipt)
	if current["complete"] == true || current["url"] == nil {
		return contracts.WaitResult{Done: true, Data: current}, nil
	}
	endpoint := text(current["url"])
	validate := func(candidate string) error {
		if candidate != endpoint {
			return serviceDenied("elastic_san_poll_url_changed")
		}
		_, err := c.elasticSanPollURL(id, location, candidate)
		return err
	}
	res, err := c.requestUsing(ctx, "GET", endpoint, nil, nil, validate, c.http, true)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if err := operationError(res); err != nil {
		return contracts.WaitResult{}, err
	}
	if !slices.Contains([]int{200, 202, 204}, res.status) || res.data["error"] != nil || res.data["code"] != nil || res.data["nextLink"] != nil || res.status == 204 && len(res.data) != 0 {
		return contracts.WaitResult{}, serviceDenied("invalid_elastic_san_poll_response")
	}
	state, done := "", len(res.data) == 0 && res.status != 202
	if len(res.data) != 0 {
		u, _ := url.Parse(endpoint)
		for key, expected := range map[string]string{"id": u.Path, "name": last(u.Path), "resourceId": id} {
			if value, present := res.data[key]; present && (!strings.EqualFold(text(value), expected) || value != text(value)) {
				return contracts.WaitResult{}, serviceDenied("elastic_san_poll_identity_changed")
			}
		}
		state = text(res.data["status"])
		if res.data["status"] != state || !slices.Contains([]string{"Accepted", "Queued", "InProgress", "Running", "Succeeded"}, state) || state == "Succeeded" && res.status != 200 || res.data["properties"] != nil {
			return contracts.WaitResult{}, serviceDenied("elastic_san_poll_state_unverified")
		}
		done = state == "Succeeded"
	}
	next, err := c.elasticSanOperationLocation(id, location, res.header)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	if next != "" {
		oldURL, _ := url.Parse(endpoint)
		newURL, _ := url.Parse(next)
		if !strings.EqualFold(oldURL.Path, newURL.Path) {
			return contracts.WaitResult{}, serviceDenied("elastic_san_poll_rotation_changed_operation")
		}
		current["url"] = next
	}
	if done {
		current["complete"] = true
	}
	return contracts.WaitResult{Done: done, State: state, RetryAfter: retryAfter(res.header), Data: c.elasticSanSignReceipt(id, location, current)}, nil
}
