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

func (c *client) netappSignUpdate(id, region, uid string, receipt map[string]any) map[string]any {
	out := maps.Clone(receipt)
	if out == nil {
		out = map[string]any{}
	}
	delete(out, "binding")
	out["binding"] = c.privateConfiguration(map[string]any{"protocol": "netapp-volume-patch-1", "resource": id, "region": region, "uid": uid, "receipt": out})
	return out
}
func (c *client) netappUpdateBody(id, region, uid string, raw map[string]any) bool {
	return netappMetadata(raw, id, netappVolumeType) && resourceRegion(raw) == region && object(raw["properties"])["fileSystemId"] == uid
}
func (c *client) netappUpdateReceipt(id, region, uid string, res response) (map[string]any, error) {
	if c.netappIdentity(id, netappVolumeType) != nil || !uuidPattern.MatchString(uid) || region == "" || region != strings.ToLower(strings.TrimSpace(region)) {
		return nil, serviceDenied("invalid_netapp_update_owner")
	}
	if err := operationError(res); err != nil {
		return nil, err
	}
	if res.status != 200 && res.status != 202 || res.status == 200 && !c.netappUpdateBody(id, region, uid, res.data) || res.status == 202 && len(res.data) != 0 && !c.netappUpdateBody(id, region, uid, res.data) {
		return nil, serviceDenied("invalid_netapp_update_response")
	}
	headers, err := c.netappOperationHeaders(id, region, res.header)
	if err != nil {
		return nil, err
	}
	if res.status == 202 && len(headers) == 0 {
		return nil, serviceDenied("incomplete_netapp_update_receipt")
	}
	return c.netappSignUpdate(id, region, uid, headers), nil
}
func (c *client) netappPollUpdate(ctx context.Context, id, region, uid string, receipt map[string]any) (out contracts.WaitResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if c.netappIdentity(id, netappVolumeType) != nil || !uuidPattern.MatchString(uid) || receipt["binding"] != c.netappSignUpdate(id, region, uid, receipt)["binding"] {
		return out, serviceDenied("netapp_update_receipt_changed")
	}
	// Reuse structural/header validation without accepting a deletion signature.
	structural := c.netappSignReceipt(id, region, receipt)
	if err := c.netappVerifyReceipt(id, region, structural); err != nil {
		return out, err
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
	validate := func(next string) error {
		if next != endpoint {
			return serviceDenied("netapp_update_poll_url_changed")
		}
		_, err := c.netappPollURL(id, region, next, role)
		return err
	}
	res, err := c.requestUsing(ctx, "GET", endpoint, nil, nil, validate, c.http, role == "result_url")
	if err != nil {
		return out, err
	}
	if err := operationError(res); err != nil {
		return out, err
	}
	if res.data["error"] != nil || res.data["code"] != nil || res.data["nextLink"] != nil {
		return out, serviceDenied("invalid_netapp_update_poll_body")
	}
	headers, err := c.netappOperationHeaders(id, region, res.header)
	if err != nil {
		return out, err
	}
	for key, value := range headers {
		if current[key] != value {
			return out, serviceDenied("netapp_update_continuation_changed")
		}
	}
	done := false
	state := ""
	if role == "result_url" {
		if !slices.Contains([]int{200, 202, 204}, res.status) || len(res.data) != 0 && !c.netappUpdateBody(id, region, uid, res.data) {
			return out, serviceDenied("invalid_netapp_update_result")
		}
		done = res.status != 202
	} else {
		operation, _ := c.netappPollURL(id, region, endpoint, role)
		u, _ := url.Parse(endpoint)
		p := object(res.data["properties"])
		if res.status != http.StatusOK || !strings.EqualFold(text(res.data["id"]), u.Path) || !strings.EqualFold(text(res.data["name"]), operation) || !strings.EqualFold(text(p["resourceName"]), id) || p["action"] != "PATCH" {
			return out, serviceDenied("netapp_update_poll_target_changed")
		}
		state = text(res.data["status"])
		if !slices.Contains([]string{"Accepted", "InProgress", "Updating", "Succeeded"}, state) {
			return out, serviceDenied("netapp_update_poll_state_unverified")
		}
		done = state == "Succeeded"
	}
	if done && role == "status_url" && current["result_url"] != nil {
		current["status_done"] = true
		done = false
	}
	if done {
		current["complete"] = true
	}
	return contracts.WaitResult{Done: done, State: state, RetryAfter: retryAfter(res.header), Data: c.netappSignUpdate(id, region, uid, current)}, nil
}
