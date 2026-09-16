package azure

import (
	"context"
	"maps"

	"github.com/loomx-ai/steward/internal/provider/contracts"
	"net/http"
	"net/url"
	"slices"
	"strings"
)

// Polling addresses are capabilities returned by Azure, not arbitrary ARM URLs.
// Bind every supported native route to the reviewed owner and preserve the
// operation token's case (native tokens can be base64, not just UUIDs).
func (c *client) dataProtectionPollURL(id, region, endpoint, role string) (string, error) {
	_, kind, err := parseID(id)
	kind = dataProtectionKind(kind)
	canonical, identityErr := c.dataProtectionIdentity(id, kind)
	if err != nil || identityErr != nil || canonical != id || !slices.Contains([]string{dataProtectionInstance, dataProtectionVault}, kind) || !cosmosOperationRegion.MatchString(region) || region != strings.ToLower(strings.TrimSpace(region)) || c.validateURL(endpoint) != nil || len(endpoint) > 32<<10 || endpoint != strings.TrimSpace(endpoint) {
		return "", serviceDenied("invalid_data_protection_operation_owner")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.RawPath != "" || u.ForceQuery {
		return "", serviceDenied("invalid_data_protection_operation_path")
	}
	q, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(q["api-version"]) != 1 || q.Get("api-version") != dataProtectionReadVersion(kind) {
		return "", serviceDenied("invalid_data_protection_operation_version")
	}
	signed := 0
	for key, values := range q {
		if len(values) != 1 || values[0] == "" || strings.ContainsAny(values[0], "\x00\r\n\t ") {
			return "", serviceDenied("invalid_data_protection_operation_query")
		}
		if key == "api-version" {
			continue
		}
		if kind != dataProtectionVault || !slices.Contains([]string{"t", "c", "s", "h"}, key) {
			return "", serviceDenied("unknown_data_protection_operation_query")
		}
		signed++
	}
	if signed != 0 && signed != 4 {
		return "", serviceDenied("incomplete_data_protection_operation_signature")
	}
	split := strings.LastIndex(u.Path, "/")
	if split < 0 {
		return "", serviceDenied("invalid_data_protection_operation_token")
	}
	token := u.Path[split+1:]
	if token == "" || token == "." || token == ".." || strings.ContainsAny(token, "\\\x00\r\n\t ") {
		return "", serviceDenied("invalid_data_protection_operation_token")
	}
	vault := id
	if kind == dataProtectionInstance {
		vault = redisParentID(id)
	}
	group := strings.Join(strings.Split(vault, "/")[:5], "/")
	regional := c.root() + "/providers/microsoft.dataprotection/locations/" + region
	var allowed []string
	switch role {
	case "status_url":
		allowed = []string{regional + "/operationstatus", vault + "/operationstatus", group + "/providers/microsoft.dataprotection/operationstatus"}
		if kind == dataProtectionVault && signed == 4 {
			allowed = append(allowed, group+"/providers/microsoft.dataprotection/locations/"+region+"/operationstatus")
		}
	case "result_url":
		allowed = []string{regional + "/operationresults", vault + "/operationresults"}
		if kind == dataProtectionInstance {
			allowed = append(allowed, id+"/operationresults")
		}
	default:
		return "", serviceDenied("invalid_data_protection_operation_role")
	}
	if !slices.Contains(allowed, strings.ToLower(u.Path[:split])) {
		return "", serviceDenied("data_protection_operation_scope_changed")
	}
	return token, nil
}
func (c *client) dataProtectionOperationHeaders(id, region string, h http.Header) (map[string]any, error) {
	if len(h.Values("Operation-Location")) != 0 || len(h.Values("Azure-AsyncOperation")) > 1 || len(h.Values("Location")) > 1 {
		return nil, serviceDenied("ambiguous_data_protection_operation_headers")
	}
	result := map[string]any{}
	token := ""
	for _, entry := range []struct{ header, role string }{{"Azure-AsyncOperation", "status_url"}, {"Location", "result_url"}} {
		endpoint := h.Get(entry.header)
		if endpoint == "" {
			if len(h.Values(entry.header)) != 0 {
				return nil, serviceDenied("empty_data_protection_operation_header")
			}
			continue
		}
		current, err := c.dataProtectionPollURL(id, region, endpoint, entry.role)
		if err != nil {
			return nil, err
		}
		if token != "" && token != current {
			return nil, serviceDenied("data_protection_operation_headers_disagree")
		}
		token = current
		result[entry.role] = endpoint
	}
	return result, nil
}

func (c *client) dataProtectionSignReceipt(id, region string, receipt map[string]any) map[string]any {
	result := maps.Clone(receipt)
	if result == nil {
		result = map[string]any{}
	}
	delete(result, "binding")
	result["binding"] = c.privateConfiguration(map[string]any{"protocol": "data-protection-operation-1", "resource": id, "region": region, "receipt": result})
	return result
}
func (c *client) dataProtectionDeleteReceipt(id, region string, res response, wireEmpty bool) (map[string]any, error) {
	if err := operationError(res); err != nil {
		return nil, err
	}
	if !slices.Contains([]int{200, 202, 204}, res.status) || !wireEmpty || len(res.data) != 0 {
		return nil, serviceDenied("invalid_data_protection_delete_acknowledgement")
	}
	receipt, err := c.dataProtectionOperationHeaders(id, region, res.header)
	if err != nil {
		return nil, err
	}
	if res.status == 202 && len(receipt) == 0 || res.status != 202 && len(receipt) != 0 {
		return nil, serviceDenied("incomplete_data_protection_delete_acknowledgement")
	}
	receipt["asynchronous"] = res.status == 202
	signed := c.dataProtectionSignReceipt(id, region, receipt)
	return signed, c.dataProtectionVerifyReceipt(id, region, signed)
}
func (c *client) dataProtectionVerifyReceipt(id, region string, receipt map[string]any) error {
	_, kind, err := parseID(id)
	kind = dataProtectionKind(kind)
	canonical, identityErr := c.dataProtectionIdentity(id, kind)
	if err != nil || identityErr != nil || canonical != id || !slices.Contains([]string{dataProtectionVault, dataProtectionInstance}, kind) || !cosmosOperationRegion.MatchString(region) || region != strings.ToLower(region) {
		return serviceDenied("invalid_data_protection_saved_owner")
	}
	if receipt["binding"] != c.dataProtectionSignReceipt(id, region, receipt)["binding"] {
		return serviceDenied("data_protection_receipt_changed")
	}
	h := http.Header{}
	async, ok := receipt["asynchronous"].(bool)
	if !ok {
		return serviceDenied("invalid_data_protection_saved_acknowledgement")
	}
	for key, value := range receipt {
		switch key {
		case "binding", "asynchronous":
		case "status_done", "complete":
			if value != true {
				return serviceDenied("invalid_data_protection_saved_phase")
			}
		case "status_url", "result_url":
			endpoint, ok := value.(string)
			if !ok || endpoint == "" {
				return serviceDenied("invalid_data_protection_saved_url")
			}
			header := "Location"
			if key == "status_url" {
				header = "Azure-AsyncOperation"
			}
			h.Set(header, endpoint)
		default:
			return serviceDenied("unknown_data_protection_saved_field")
		}
	}
	headers, err := c.dataProtectionOperationHeaders(id, region, h)
	if err != nil {
		return err
	}
	if async != (len(headers) > 0) || receipt["status_done"] == true && (receipt["status_url"] == nil || receipt["result_url"] == nil) {
		return serviceDenied("invalid_data_protection_saved_phase")
	}
	return nil
}

// A successful operation is not proof of resource absence. The action must
// reconcile the live and retained collections after this state machine finishes.
func (c *client) dataProtectionPoll(ctx context.Context, id, region string, receipt map[string]any) (out contracts.WaitResult, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if err = c.dataProtectionVerifyReceipt(id, region, receipt); err != nil {
		return out, err
	}
	current := maps.Clone(receipt)
	if current["complete"] == true || current["asynchronous"] == false {
		return contracts.WaitResult{Done: true, Data: current}, nil
	}
	role := "status_url"
	if current[role] == nil || current["status_done"] == true {
		role = "result_url"
	}
	endpoint := text(current[role])
	token, _ := c.dataProtectionPollURL(id, region, endpoint, role)
	validate := func(next string) error {
		if next != endpoint {
			return serviceDenied("data_protection_poll_redirected")
		}
		_, err := c.dataProtectionPollURL(id, region, next, role)
		return err
	}
	transport := *c.http
	base := transport.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	ack := &synapseRestoreAckTransport{base: base}
	transport.Transport = ack
	res, err := c.requestUsing(ctx, "GET", endpoint, nil, nil, validate, &transport, role == "result_url")
	if err != nil {
		return out, err
	}
	if err = operationError(res); err != nil {
		return out, err
	}
	if res.data["error"] != nil || res.data["code"] != nil || res.data["nextLink"] != nil {
		return out, serviceDenied("invalid_data_protection_operation_body")
	}
	next, err := c.dataProtectionOperationHeaders(id, region, res.header)
	if err != nil {
		return out, err
	}
	for key, value := range next {
		successor, err := c.dataProtectionPollURL(id, region, text(value), key)
		if err != nil || successor != token {
			return out, serviceDenied("data_protection_operation_successor_changed")
		}
		current[key] = value
	}
	state := ""
	done := false
	if role == "status_url" {
		u, _ := url.Parse(endpoint)
		if res.status != 200 || !strings.EqualFold(text(res.data["id"]), u.Path) || text(res.data["name"]) != token || last(text(res.data["id"])) != token {
			return out, serviceDenied("data_protection_poll_identity_changed")
		}
		state = strings.ToLower(text(res.data["status"]))
		if !slices.Contains([]string{"accepted", "inprogress", "running", "succeeded"}, state) {
			return out, serviceDenied("data_protection_poll_state_unverified")
		}
		done = state == "succeeded"
	} else {
		switch res.status {
		case 202:
			if !ack.empty {
				return out, serviceDenied("invalid_data_protection_pending_result")
			}
		case 200:
			u, _ := url.Parse(endpoint)
			// Paths are case-insensitive, so determine the resource prefix on a folded copy.
			folded := strings.ToLower(u.Path)
			prefix := folded[:strings.LastIndex(folded, "/operationresults/")]
			regional := c.root() + "/providers/microsoft.dataprotection/locations/" + region
			if prefix == regional {
				if res.data["objectType"] != "OperationJobExtendedInfo" {
					return out, serviceDenied("invalid_data_protection_job_result")
				}
			} else {
				kind := dataProtectionVault
				if prefix == id && strings.Contains(id, "/backupinstances/") {
					kind = dataProtectionInstance
				}
				if err = c.dataProtectionMetadata(res.data, prefix, kind); err != nil {
					return out, err
				}
			}
			done = true
		default:
			return out, serviceDenied("invalid_data_protection_result_status")
		}
	}
	if done && role == "status_url" && current["result_url"] != nil {
		current["status_done"] = true
		done = false
	}
	if done {
		current["complete"] = true
	}
	return contracts.WaitResult{Done: done, State: state, RetryAfter: retryAfter(res.header), Data: c.dataProtectionSignReceipt(id, region, current)}, nil
}
