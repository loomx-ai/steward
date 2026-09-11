package azure

import (
	"context"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"
)

type dataFactoryWork struct {
	runs, debug map[string]map[string]any
	verified    bool
}

func dataFactoryRunActive(raw map[string]any) bool {
	return slices.Contains([]string{"Queued", "InProgress", "Canceling"}, text(raw["status"]))
}

func dataFactoryRunMetadata(root, runID string, raw map[string]any) error {
	name, ok := raw["pipelineName"].(string)
	id, kind, err := parseID(root + "/pipelines/" + name)
	if raw == nil || raw["error"] != nil || !uuidPattern.MatchString(runID) || raw["runId"] != runID || !ok || name == "" || name != strings.TrimSpace(name) || err != nil || !strings.EqualFold(kind, dataFactoryPipelineType) || dataFactoryParent(id, dataFactoryPipelineType) != root {
		return serviceDenied("invalid_datafactory_pipeline_run_identity")
	}
	if value, exists := raw["id"]; exists && !strings.EqualFold(text(value), root+"/pipelineruns/"+runID) {
		return serviceDenied("datafactory_pipeline_run_scope_changed")
	}
	if !slices.Contains([]string{"Queued", "InProgress", "Succeeded", "Failed", "Canceling", "Cancelled"}, text(raw["status"])) {
		return serviceDenied("unknown_datafactory_pipeline_run_state")
	}
	if _, err := time.Parse(time.RFC3339Nano, text(raw["runStart"])); err != nil {
		return serviceDenied("datafactory_pipeline_run_creation_missing")
	}
	return nil
}

func dataFactoryWorkSnapshot(raw map[string]any, debug bool) map[string]any {
	result := batchClone(raw)
	if debug {
		delete(result, "lastActivityTime")
		return result
	}
	for _, key := range []string{"status", "lastUpdated", "runEnd", "durationInMs", "message", "isLatest"} {
		delete(result, key)
	}
	if id, ok := result["id"].(string); ok {
		result["id"] = strings.ToLower(id)
	}
	return result
}

func (c *client) dataFactoryRun(ctx context.Context, root, runID string) (map[string]any, error) {
	if !uuidPattern.MatchString(runID) {
		return nil, serviceDenied("invalid_datafactory_pipeline_run_id")
	}
	request, err := c.dataFactoryOperation(root, dataFactoryType, "PipelineRuns_Get", map[string]any{"runId": runID})
	if err != nil {
		return nil, err
	}
	res, err := c.request(ctx, request.Method, request.URL)
	if err != nil {
		return nil, err
	}
	if res.status != 200 || operationLocation(res.header) != "" {
		return nil, serviceDenied("incomplete_datafactory_pipeline_run")
	}
	if err := dataFactoryRunMetadata(root, runID, res.data); err != nil {
		return nil, err
	}
	return res.data, nil
}

func dataFactoryWorkPage(res response, continuation string) ([]any, string, error) {
	rows, ok := res.data["value"].([]any)
	if res.status != 200 || operationLocation(res.header) != "" || res.data["error"] != nil || !ok {
		return nil, "", serviceDenied("incomplete_datafactory_work_index")
	}
	other := "nextLink"
	if continuation == other {
		other = "continuationToken"
	}
	if res.data[other] != nil {
		return nil, "", serviceDenied("invalid_datafactory_work_pagination")
	}
	next := ""
	if value := res.data[continuation]; value != nil {
		next, ok = value.(string)
		if !ok || next != strings.TrimSpace(next) || len(next) > 128<<10 {
			return nil, "", serviceDenied("invalid_datafactory_work_continuation")
		}
	}
	return rows, next, nil
}

// The native run query has a body continuationToken, not a nextLink. Search
// from the factory's creation time; the service retains history for 45 days.
// A previously observed run omitted by that query is read by its own run ID.
func (c *client) dataFactoryRuns(ctx context.Context, root dataFactoryMember, known map[string]any) (map[string]map[string]any, error) {
	start, err := time.Parse(time.RFC3339Nano, text(object(root.raw["properties"])["createTime"]))
	end := time.Now().UTC()
	if err != nil || start.After(end) {
		return nil, serviceDenied("datafactory_work_window_unverified")
	}
	result := map[string]map[string]any{}
	seen, tokens := map[string]bool{}, map[string]bool{}
	token := ""
	for {
		if tokens[token] || len(tokens) >= 10000 {
			return nil, serviceDenied("datafactory_work_pagination_repeated")
		}
		tokens[token] = true
		filter := map[string]any{"lastUpdatedAfter": start.Format(time.RFC3339Nano), "lastUpdatedBefore": end.Format(time.RFC3339Nano)}
		if token != "" {
			filter["continuationToken"] = token
		}
		request, err := c.dataFactoryOperation(root.id, dataFactoryType, "PipelineRuns_QueryByFactory", map[string]any{"filterParameters": filter})
		if err != nil {
			return nil, err
		}
		res, err := c.requestBody(ctx, request.Method, request.URL, request.Body, request.Headers)
		if err != nil {
			return nil, err
		}
		rows, next, err := dataFactoryWorkPage(res, "continuationToken")
		if err != nil {
			return nil, err
		}
		for _, value := range rows {
			row := object(value)
			runID := text(row["runId"])
			if err := dataFactoryRunMetadata(root.id, runID, row); err != nil {
				return nil, err
			}
			if seen[runID] {
				return nil, serviceDenied("duplicate_datafactory_pipeline_run")
			}
			seen[runID] = true
			// Historical results do not authorize mutations. For active work,
			// require the named GET and bind that response's private definition.
			if !dataFactoryRunActive(row) && known[runID] == nil {
				continue
			}
			live, err := c.dataFactoryRun(ctx, root.id, runID)
			if err != nil {
				return nil, err
			}
			if expected := object(known[runID]); expected != nil && expected["configuration"] != c.privateConfiguration(dataFactoryWorkSnapshot(live, false)) {
				return nil, serviceDenied("datafactory_known_pipeline_run_changed")
			}
			for _, key := range []string{"pipelineName", "runStart", "parameters", "invokedBy", "runGroupId"} {
				if row[key] != nil && !nativeConfigurationContains(map[string]any{key: row[key]}, map[string]any{key: live[key]}) {
					return nil, serviceDenied("datafactory_listed_pipeline_run_changed")
				}
			}
			if dataFactoryRunActive(live) {
				result[runID] = live
			}
		}
		if next == "" {
			break
		}
		token = next
	}
	for _, runID := range slices.Sorted(maps.Keys(known)) {
		if seen[runID] {
			continue
		}
		live, err := c.dataFactoryRun(ctx, root.id, runID)
		// A missing historical run is not evidence that cancellation finished.
		if err != nil {
			return nil, err
		}
		if object(known[runID])["configuration"] != c.privateConfiguration(dataFactoryWorkSnapshot(live, false)) {
			return nil, serviceDenied("datafactory_known_pipeline_run_changed")
		}
		if dataFactoryRunActive(live) {
			result[runID] = live
		}
	}
	return result, nil
}

func dataFactoryDebugMetadata(raw map[string]any) error {
	if raw == nil || raw["error"] != nil || !uuidPattern.MatchString(text(raw["sessionId"])) || raw["sessionId"] != text(raw["sessionId"]) {
		return serviceDenied("invalid_datafactory_debug_session")
	}
	if _, err := time.Parse(time.RFC3339Nano, text(raw["startTime"])); err != nil {
		return serviceDenied("datafactory_debug_session_creation_missing")
	}
	return nil
}

// Azure's generated SDK sends POST for the first debug query and GET for
// nextLink pages. Every continuation remains on this exact factory endpoint.
func (c *client) dataFactoryDebug(ctx context.Context, root string) (map[string]map[string]any, error) {
	request, err := c.dataFactoryOperation(root, dataFactoryType, "DataFlowDebugSession_QueryByFactory", nil)
	if err != nil {
		return nil, err
	}
	initial, _ := url.Parse(request.URL)
	result, seen := map[string]map[string]any{}, map[string]bool{}
	for {
		if seen[request.URL] || len(seen) >= 10000 {
			return nil, serviceDenied("datafactory_debug_pagination_repeated")
		}
		seen[request.URL] = true
		res, err := c.requestBody(ctx, request.Method, request.URL, request.Body, request.Headers)
		if err != nil {
			return nil, err
		}
		rows, next, err := dataFactoryWorkPage(res, "nextLink")
		if err != nil {
			return nil, err
		}
		for _, value := range rows {
			row := object(value)
			if err := dataFactoryDebugMetadata(row); err != nil {
				return nil, err
			}
			id := text(row["sessionId"])
			if result[id] != nil {
				return nil, serviceDenied("duplicate_datafactory_debug_session")
			}
			result[id] = row
		}
		if next == "" {
			return result, nil
		}
		if err := c.validateURL(next); err != nil {
			return nil, err
		}
		u, _ := url.Parse(next)
		if err := dataFactoryListQuery(u); err != nil {
			return nil, err
		}
		if !strings.EqualFold(u.Path, initial.Path) {
			return nil, serviceDenied("datafactory_debug_pagination_scope_changed")
		}
		request.Method, request.URL = "GET", next
	}
}

func (c *client) dataFactoryWork(ctx context.Context, tree dataFactoryTree, known map[string]any) (dataFactoryWork, error) {
	work := dataFactoryWork{}
	// Keep factories without an immutable creation timestamp visible and
	// protected. They cannot authorize a work window or a cleanup action.
	if _, err := time.Parse(time.RFC3339Nano, text(object(tree.members[tree.root].raw["properties"])["createTime"])); err != nil {
		return work, nil
	}
	var err error
	work.runs, err = c.dataFactoryRuns(ctx, tree.members[tree.root], object(known["runs"]))
	if err != nil {
		return work, err
	}
	work.debug, err = c.dataFactoryDebug(ctx, tree.root)
	if err != nil {
		return work, err
	}
	for id, raw := range work.debug {
		if expected := object(object(known["debug"])[id]); expected != nil && expected["configuration"] != c.privateConfiguration(dataFactoryWorkSnapshot(raw, true)) {
			return work, serviceDenied("datafactory_known_debug_session_changed")
		}
	}
	work.verified = true
	return work, err
}

func (c *client) dataFactoryWorkManifest(work dataFactoryWork) map[string]any {
	result := map[string]any{"verified": work.verified, "runs": map[string]any{}, "debug": map[string]any{}}
	for _, part := range []struct {
		name string
		rows map[string]map[string]any
	}{{"runs", work.runs}, {"debug", work.debug}} {
		for id, raw := range part.rows {
			entry := map[string]any{"configuration": c.privateConfiguration(dataFactoryWorkSnapshot(raw, part.name == "debug"))}
			if part.name == "runs" {
				entry["pipeline"] = strings.ToLower(text(raw["pipelineName"]))
			}
			object(result[part.name])[id] = entry
		}
	}
	return result
}
