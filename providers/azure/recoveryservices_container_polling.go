package azure

import (
	"context"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Validate every returned capability before choosing a result endpoint. The
// legacy container callbacks are consumed as the official CLI does:
// extract its validated operation ID and bind the original container's native
// operationResults route. No foreign address or arbitrary query is followed.
func (c *client) recoveryContainerCallback(id, endpoint, role string) (token string, legacy bool, err error) {
	owner, e := c.recoveryServicesIdentity(id, recoveryServicesContainer)
	if e != nil || owner != id || endpoint != strings.TrimSpace(endpoint) || len(endpoint) > 32<<10 || c.validateURL(endpoint) != nil {
		return "", false, serviceDenied("invalid_recovery_container_callback_owner")
	}
	u, e := url.Parse(endpoint)
	if e != nil || u.ForceQuery || strings.Contains(strings.ToLower(u.EscapedPath()), "%2f") || strings.Contains(strings.ToLower(u.EscapedPath()), "%5c") || strings.Contains(u.Path, "%") {
		return "", false, serviceDenied("invalid_recovery_container_callback_path")
	}
	split := strings.LastIndex(u.Path, "/")
	if split < 0 {
		return "", false, serviceDenied("invalid_recovery_container_operation")
	}
	token = u.Path[split+1:]
	if token == "" || token == "." || token == ".." || len(token) > 1024 || strings.ContainsAny(token, "%?#/\\\x00\r\n\t ") {
		return "", false, serviceDenied("invalid_recovery_container_operation")
	}
	parent := strings.ToLower(u.Path[:split])
	vault := recoveryServicesVaultID(id)
	fabric := strings.Split(id, "/")[10]
	switch role {
	case "result":
		if parent != id+"/operationresults" && parent != vault+"/backupoperationresults" {
			return "", false, serviceDenied("recovery_container_callback_scope_changed")
		}
	case "status":
		if !slices.Contains([]string{id + "/operationsstatus", vault + "/backupfabrics/" + fabric + "/operationsstatus", vault + "/backupoperations"}, parent) {
			return "", false, serviceDenied("recovery_container_callback_scope_changed")
		}
	default:
		return "", false, serviceDenied("invalid_recovery_container_callback_role")
	}
	q, e := url.ParseQuery(u.RawQuery)
	if e != nil {
		return "", false, serviceDenied("invalid_recovery_container_callback_query")
	}
	// These versions occur in the pinned REST examples and CLI recording. Older
	// unsigned callbacks supply an operation identity only; bind the selected
	// catalog version rather than sending a request to an obsolete endpoint.
	historical := func(version string) bool {
		return slices.Contains([]string{"2017-07-01", "2019-05-13-preview", "2023-04-01"}, version)
	}
	if role == "result" && parent == vault+"/backupoperationresults" && len(q) == 1 && len(q["fabricName"]) == 1 {
		value := q.Get("fabricName")
		name, version, suffix := strings.Cut(value, "?api-version=")
		// The CLI recording contains a second '?' in Location. Consume only this
		// exact legacy form; never follow it or accept additional query fields.
		if strings.EqualFold(name, fabric) && (!suffix || historical(version) || version == recoveryServicesBackupVersion) {
			return token, true, nil
		}
	}
	if len(q["api-version"]) != 1 {
		return "", false, serviceDenied("recovery_container_callback_version_changed")
	}
	if q.Get("api-version") != recoveryServicesBackupVersion {
		if len(q) != 1 || !historical(q.Get("api-version")) {
			return "", false, serviceDenied("recovery_container_callback_version_changed")
		}
		return token, true, nil
	}
	signed := 0
	for key, values := range q {
		if len(values) != 1 || values[0] == "" || strings.ContainsAny(values[0], "\x00\r\n\t ") {
			return "", false, serviceDenied("invalid_recovery_container_callback_query")
		}
		if key == "api-version" {
			continue
		}
		if !slices.Contains([]string{"t", "c", "s", "h"}, key) {
			return "", false, serviceDenied("unknown_recovery_container_callback_query")
		}
		signed++
	}
	if signed != 0 && signed != 4 {
		return "", false, serviceDenied("incomplete_recovery_container_signature")
	}
	return token, false, nil
}

func (c *client) recoveryContainerPollHeaders(id string, headers http.Header) (string, error) {
	if len(headers.Values("Operation-Location")) != 0 || len(headers.Values("Location")) > 1 || len(headers.Values("Azure-AsyncOperation")) > 1 {
		return "", serviceDenied("ambiguous_recovery_container_callbacks")
	}
	result := headers.Get("Location")
	if result == "" {
		return "", serviceDenied("missing_recovery_container_result")
	}
	token, legacy, err := c.recoveryContainerCallback(id, result, "result")
	if err != nil {
		return "", err
	}
	if len(headers.Values("Azure-AsyncOperation")) != 0 {
		other, _, err := c.recoveryContainerCallback(id, headers.Get("Azure-AsyncOperation"), "status")
		if err != nil {
			return "", err
		}
		if other != token {
			return "", serviceDenied("recovery_container_callbacks_disagree")
		}
	}
	if legacy {
		return apiURL(id+"/operationResults/"+token, recoveryServicesBackupVersion), nil
	}
	return result, nil
}

func (c *client) recoveryContainerReceipt(id string, data map[string]any) map[string]any {
	receipt := maps.Clone(data)
	if receipt == nil {
		receipt = map[string]any{}
	}
	delete(receipt, "binding")
	receipt["binding"] = c.privateConfiguration(map[string]any{"protocol": "recovery-container-operation-1", "resource": id, "receipt": receipt})
	return receipt
}

func (c *client) recoveryContainerDeleteReceipt(id string, res response, empty bool) (map[string]any, error) {
	if !empty || len(res.data) != 0 || !slices.Contains([]int{200, 202, 204}, res.status) {
		return nil, serviceDenied("invalid_recovery_container_acknowledgement")
	}
	data := map[string]any{"operation_done": res.status != 202}
	if res.status == 202 {
		endpoint, err := c.recoveryContainerPollHeaders(id, res.header)
		if err != nil {
			return nil, err
		}
		data["poll_url"] = endpoint
	} else if len(res.header.Values("Location"))+len(res.header.Values("Azure-AsyncOperation"))+len(res.header.Values("Operation-Location")) != 0 {
		return nil, serviceDenied("unexpected_recovery_container_async_header")
	}
	return c.recoveryContainerReceipt(id, data), nil
}

func (c *client) recoveryContainerPoll(ctx context.Context, id string, receipt map[string]any) (map[string]any, time.Duration, error) {
	owner, err := c.recoveryServicesIdentity(id, recoveryServicesContainer)
	if err != nil || owner != id || receipt["binding"] != c.recoveryContainerReceipt(id, receipt)["binding"] {
		return nil, 0, serviceDenied("recovery_container_receipt_changed")
	}
	done, ok := receipt["operation_done"].(bool)
	if !ok {
		return nil, 0, serviceDenied("invalid_recovery_container_saved_phase")
	}
	for key := range receipt {
		if !slices.Contains([]string{"binding", "operation_done", "poll_url"}, key) {
			return nil, 0, serviceDenied("unknown_recovery_container_receipt_field")
		}
	}
	endpoint := text(receipt["poll_url"])
	if endpoint != "" {
		_, legacy, err := c.recoveryContainerCallback(id, endpoint, "result")
		if err != nil || legacy {
			return nil, 0, serviceDenied("invalid_recovery_container_saved_poll")
		}
	} else if !done {
		return nil, 0, serviceDenied("missing_recovery_container_saved_poll")
	}
	if done {
		return maps.Clone(receipt), 0, nil
	}
	transport := *c.http
	base := transport.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	ack := &synapseRestoreAckTransport{base: base}
	transport.Transport = ack
	res, err := c.requestUsing(ctx, "GET", endpoint, nil, nil, c.validateURL, &transport, true)
	if err != nil {
		return nil, 0, contracts.DependencyReadError(err)
	}
	if err = operationError(res); err != nil {
		return nil, 0, err
	}
	if len(res.header.Values("Location"))+len(res.header.Values("Azure-AsyncOperation"))+len(res.header.Values("Operation-Location")) != 0 {
		token, _, _ := c.recoveryContainerCallback(id, endpoint, "result")
		next, err := c.recoveryContainerPollHeaders(id, res.header)
		if err != nil {
			return nil, 0, err
		}
		nextToken, _, err := c.recoveryContainerCallback(id, next, "result")
		if err != nil || nextToken != token || next != endpoint {
			return nil, 0, serviceDenied("recovery_container_poll_redirected")
		}
	}
	switch res.status {
	case 202:
		// Recorded container operation results return {} while pending. An empty
		// decoded payload cannot establish completion; only the HTTP status does.
		if len(res.data) != 0 {
			return nil, 0, serviceDenied("invalid_recovery_container_pending_body")
		}
		return maps.Clone(receipt), retryAfter(res.header), nil
	case 200:
		if len(res.data) == 0 && !ack.empty {
			return nil, 0, serviceDenied("invalid_recovery_container_empty_result")
		}
		// A completed result may carry the native container representation. It is
		// not an own GET and never establishes absence by itself.
		if len(res.data) != 0 {
			if err = c.recoveryServicesMetadata(res.data, id, recoveryServicesContainer); err != nil {
				return nil, 0, err
			}
		}
	case 204:
		if len(res.data) != 0 || !ack.empty {
			return nil, 0, serviceDenied("invalid_recovery_container_completed_body")
		}
	default:
		return nil, 0, serviceDenied("invalid_recovery_container_poll_status")
	}
	result := maps.Clone(receipt)
	result["operation_done"] = true
	return c.recoveryContainerReceipt(id, result), 0, nil
}
