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

func dataMigrationAsyncPhase(id, kind, phase string) bool {
	if phase == "cancel" {
		_, targetKind := dataMigrationTarget(id)
		return kind == dataMigrationType && targetKind != "" && targetKind != "MongoToCosmosDbMongo"
	}
	return phase == "delete" && slices.Contains([]string{dataMigrationServiceType, dataMigrationMongoServiceType, dataMigrationSQLServiceType, dataMigrationType}, kind)
}

// Native operation regions can differ from resource regions. Bind returned
// URLs to the subscription, operation family, phase and UUID, then retain the
// complete receipt privately. Classic CLI recordings use signed 2021 URLs.
func (c *client) dataMigrationPollURL(id, kind, phase, endpoint string) (string, error) {
	if c.dataMigrationIdentity(id, kind) != nil || !dataMigrationAsyncPhase(id, kind, phase) || c.validateURL(endpoint) != nil || len(endpoint) > 32<<10 || endpoint != strings.TrimSpace(endpoint) {
		return "", serviceDenied("invalid_datamigration_operation_owner")
	}
	u, _ := url.Parse(endpoint)
	if u.RawPath != "" || u.ForceQuery {
		return "", serviceDenied("invalid_datamigration_operation_path")
	}
	parts := strings.Split(strings.ToLower(u.Path), "/")
	if len(parts) != 9 && len(parts) != 11 || strings.Join(parts[:7], "/") != c.root()+"/providers/microsoft.datamigration/locations/"+parts[6] || !cosmosOperationRegion.MatchString(parts[6]) || !uuidPattern.MatchString(parts[len(parts)-1]) {
		return "", serviceDenied("datamigration_operation_scope_changed")
	}
	if len(parts) == 9 {
		allowed := slices.Contains([]string{"operationstatuses", "operationresults"}, parts[7])
		allowed = allowed || phase == "delete" && (kind == dataMigrationSQLServiceType && parts[7] == "sqlmigrationserviceoperationresults" || kind == dataMigrationMongoServiceType && parts[7] == "migrationserviceoperationresults")
		if !allowed {
			return "", serviceDenied("datamigration_operation_family_changed")
		}
	} else {
		_, targetKind := dataMigrationTarget(id)
		expected := ""
		if kind == dataMigrationSQLServiceType && phase == "delete" {
			expected = "deletesqlmigrationservice"
		}
		if kind == dataMigrationType {
			if phase == "cancel" {
				expected = "cancel" + strings.ToLower(targetKind) + "migration"
			} else if targetKind == "MongoToCosmosDbMongo" {
				expected = "dropcosmosdbmongomigration"
			} else {
				expected = "drop" + strings.ToLower(targetKind) + "migration"
			}
		}
		if parts[7] != "operationtypes" || expected == "" || parts[8] != expected || parts[9] != "operationresults" {
			return "", serviceDenied("datamigration_operation_phase_changed")
		}
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 || len(query) != 1 && len(query) != 5 {
		return "", serviceDenied("invalid_datamigration_operation_query")
	}
	version := query.Get("api-version")
	if version != dataMigrationVersion && !(version == "2021-06-30" && kind == dataMigrationServiceType && len(parts) == 9 && len(query) == 5) {
		return "", serviceDenied("datamigration_operation_version_changed")
	}
	if len(query) == 5 {
		for _, key := range []string{"t", "c", "s", "h"} {
			if len(query[key]) != 1 || query.Get(key) == "" || strings.ContainsAny(query.Get(key), "\x00\r\n\t ") {
				return "", serviceDenied("invalid_datamigration_operation_signature")
			}
		}
	}
	return parts[6] + "/" + parts[len(parts)-1] + "/" + version, nil
}

func (c *client) dataMigrationOperationHeaders(id, kind, phase string, header http.Header) (map[string]any, error) {
	if len(header.Values("Operation-Location")) != 0 || len(header.Values("Azure-AsyncOperation")) > 1 || len(header.Values("Location")) > 1 {
		return nil, serviceDenied("ambiguous_datamigration_operation_headers")
	}
	operation, signature := map[string]any{}, ""
	for _, entry := range []struct{ name, key string }{{"Azure-AsyncOperation", "status_url"}, {"Location", "result_url"}} {
		endpoint := header.Get(entry.name)
		if endpoint == "" {
			if len(header.Values(entry.name)) != 0 {
				return nil, serviceDenied("empty_datamigration_operation_header")
			}
			continue
		}
		identity, err := c.dataMigrationPollURL(id, kind, phase, endpoint)
		if err != nil {
			return nil, err
		}
		u, _ := url.Parse(endpoint)
		if signature != "" && signature != identity || entry.key == "result_url" && strings.EqualFold(strings.Split(u.Path, "/")[7], "operationStatuses") {
			return nil, serviceDenied("datamigration_operation_headers_disagree")
		}
		signature, operation[entry.key] = identity, endpoint
	}
	return operation, nil
}

func (c *client) dataMigrationReceipt(id, kind, phase string, res response) (map[string]any, error) {
	if err := c.dataMigrationIdentity(id, kind); err != nil {
		return nil, err
	}
	if err := operationError(res); err != nil {
		return nil, err
	}
	valid := false
	switch phase {
	case "delete":
		valid = res.status == 200 || res.status == 204 || res.status == 202 && dataMigrationAsyncPhase(id, kind, phase)
		if kind == dataMigrationMongoServiceType || kind == dataMigrationType && !dataMigrationAsyncPhase(id, kind, "cancel") {
			valid = res.status == 202 || res.status == 204
		}
	case "cancel":
		valid = res.status == 200 && (kind == dataMigrationTaskType || kind == dataMigrationServiceTaskType) || dataMigrationAsyncPhase(id, kind, phase) && (res.status == 200 || res.status == 202)
	}
	if !valid {
		return nil, serviceDenied("invalid_datamigration_mutation_status")
	}
	if len(res.data) != 0 {
		if res.status != 200 || phase == "delete" && kind != dataMigrationType {
			return nil, serviceDenied("unexpected_datamigration_mutation_body")
		}
		if err := dataMigrationMetadata(id, kind, res.data); err != nil {
			return nil, err
		}
	} else if phase == "cancel" && kind != dataMigrationType {
		return nil, serviceDenied("datamigration_cancel_task_receipt_missing")
	}
	operation, err := c.dataMigrationOperationHeaders(id, kind, phase, res.header)
	if err != nil {
		return nil, err
	}
	if res.status == 202 && len(operation) == 0 || res.status == 204 && len(operation) != 0 {
		return nil, serviceDenied("invalid_datamigration_async_receipt")
	}
	return operation, nil
}

func dataMigrationPollResponse(id, endpoint string, final bool, res response) (bool, error) {
	if err := operationError(res); err != nil {
		return false, err
	}
	if res.status != 200 && res.status != 202 && !(final && res.status == 204) || res.data["error"] != nil || res.data["code"] != nil || res.data["nextLink"] != nil {
		return false, serviceDenied("invalid_datamigration_operation_response")
	}
	u, _ := url.Parse(endpoint)
	for key, expected := range map[string]string{"resourceId": id, "id": u.Path, "name": last(u.Path)} {
		if value, present := res.data[key]; present && !strings.EqualFold(text(value), expected) {
			return false, serviceDenied("datamigration_operation_identity_changed")
		}
	}
	if value := res.data["properties"]; value != nil && object(value) == nil {
		return false, serviceDenied("invalid_datamigration_operation_properties")
	}
	if len(res.data) == 0 && final {
		return res.status == 200 || res.status == 204, nil
	}
	state := text(res.data["status"])
	if state == "" || res.data["status"] != state || !slices.Contains([]string{"Accepted", "InProgress", "Running", "Succeeded"}, state) || state == "Succeeded" && res.status != 200 {
		return false, serviceDenied("datamigration_operation_state_unverified")
	}
	return state == "Succeeded", nil
}

// HTTP 200 may still carry an async receipt. Poll accepted operations without
// replaying mutations; success still requires each resource's own readback.
// ARM and the native DMS CLI prefer Azure-AsyncOperation over Location.
// https://learn.microsoft.com/azure/azure-resource-manager/management/async-operations
func (c *client) dataMigrationPoll(ctx context.Context, id, kind, phase string, operation map[string]any) (contracts.WaitResult, error) {
	current := maps.Clone(operation)
	if len(current) == 0 {
		return contracts.WaitResult{Done: true, Data: current}, nil
	}
	header := http.Header{}
	for key, value := range current {
		switch key {
		case "status_url", "result_url":
			endpoint, ok := value.(string)
			if !ok || endpoint == "" {
				return contracts.WaitResult{}, serviceDenied("invalid_datamigration_saved_operation_url")
			}
			name := "Location"
			if key == "status_url" {
				name = "Azure-AsyncOperation"
			}
			header.Set(name, endpoint)
		default:
			return contracts.WaitResult{}, serviceDenied("invalid_datamigration_saved_operation")
		}
	}
	if _, err := c.dataMigrationOperationHeaders(id, kind, phase, header); err != nil {
		return contracts.WaitResult{}, err
	}
	result := contracts.WaitResult{Data: current}
	key := "status_url"
	if current[key] == nil {
		key = "result_url"
	}
	endpoint := text(current[key])
	validate := func(candidate string) error {
		if candidate != endpoint {
			return serviceDenied("datamigration_poll_url_changed")
		}
		_, err := c.dataMigrationPollURL(id, kind, phase, candidate)
		return err
	}
	res, err := c.requestUsing(ctx, "GET", endpoint, nil, nil, validate, c.http, key == "result_url")
	if err != nil {
		return result, err
	}
	result.RetryAfter = retryAfter(res.header)
	if operationLocation(res.header) != "" || len(res.header.Values("Azure-AsyncOperation"))+len(res.header.Values("Location"))+len(res.header.Values("Operation-Location")) != 0 {
		next, err := c.dataMigrationOperationHeaders(id, kind, phase, res.header)
		if err != nil {
			return result, err
		}
		for field, endpoint := range next {
			if current[field] != endpoint {
				return result, serviceDenied("datamigration_poll_receipt_changed")
			}
		}
	}
	result.Done, err = dataMigrationPollResponse(id, endpoint, key == "result_url", res)
	result.State = text(res.data["status"])
	return result, err
}
