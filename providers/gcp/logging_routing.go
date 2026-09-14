package gcp

import (
	"context"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const loggingHost = "logging.googleapis.com"

var loggingSinkName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,99}$`)

type loggingRouting struct {
	Ancestry organizationAncestry
	Sinks    map[string]map[string]any
	Projects map[string][]map[string]any
}

// Project routing is one hop. Direct bucket/storage/topic destinations do not
// run the destination project's router or extend its alert-policy scope.
func loggingDestinationProject(destination string) (string, error) {
	u, err := url.Parse("https://" + destination)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.RawPath != "" || u.Port() != "" {
		return "", groupDenied("logging_destination_invalid")
	}
	parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
	for _, part := range parts {
		if part == "" || part == "." || part == ".." || strings.ContainsAny(part, "\\% \t\n\r") {
			return "", groupDenied("logging_destination_invalid")
		}
	}
	switch u.Host {
	case loggingHost:
		if len(parts) >= 2 {
			project := parts[0] == "projects" && (projectPattern.MatchString(parts[1]) || firewallNumericID(parts[1]))
			if len(parts) == 2 && project {
				return parts[1], nil
			}
			// System sinks in folders/organizations use their own native log buckets.
			bucketParent := project || firewallContainerName(strings.Join(parts[:2], "/")) || parts[0] == "billingAccounts"
			if len(parts) == 6 && bucketParent && parts[2] == "locations" && parts[4] == "buckets" {
				return "", nil
			}
		}
	case "storage.googleapis.com":
		if len(parts) == 1 {
			return "", nil
		}
	case "bigquery.googleapis.com", "pubsub.googleapis.com":
		collection := "datasets"
		if u.Host == "pubsub.googleapis.com" {
			collection = "topics"
		}
		if len(parts) == 4 && parts[0] == "projects" && parts[2] == collection && (projectPattern.MatchString(parts[1]) || firewallNumericID(parts[1])) {
			return "", nil
		}
	}
	return "", groupDenied("logging_destination_invalid")
}

func (c *client) loggingSinkData(parent string, data map[string]any) (string, error) {
	name := text(data["name"])
	if data["name"] != name || !loggingSinkName.MatchString(name) && name != "_Required" && name != "_Default" {
		return "", groupDenied("logging_sink_identity_invalid")
	}
	id := parent + "/sinks/" + name
	if value, present := data["resourceName"]; present {
		canonical := text(value)
		if strings.HasPrefix(parent, "projects/") {
			canonical = strings.Replace(canonical, "projects/"+c.number+"/", "projects/"+c.project+"/", 1)
		}
		if canonical != id {
			return "", groupDenied("logging_sink_parent_changed")
		}
	}
	if err := cloudNatScalars(data, []string{"name", "destination", "filter", "resourceName", "description", "writerIdentity", "createTime", "updateTime"}, []string{"disabled", "includeChildren", "interceptChildren"}, nil, nil); err != nil {
		return "", err
	}
	destinationProject, err := loggingDestinationProject(text(data["destination"]))
	if err != nil {
		return "", err
	}
	if data["interceptChildren"] == true && (data["includeChildren"] != true || destinationProject == "" || strings.HasPrefix(parent, "projects/")) {
		return "", groupDenied("logging_intercept_configuration_invalid")
	}
	exclusions, err := cloudNatObjects(data, "exclusions")
	if err != nil {
		return "", err
	}
	seen := map[string]bool{}
	for _, exclusion := range exclusions {
		if err := cloudNatScalars(exclusion, []string{"name", "filter", "description", "createTime", "updateTime"}, []string{"disabled"}, nil, nil); err != nil {
			return "", err
		}
		name := text(exclusion["name"])
		if !loggingSinkName.MatchString(name) || seen[name] || text(exclusion["filter"]) == "" {
			return "", groupDenied("logging_exclusion_invalid")
		}
		seen[name] = true
	}
	return id, nil
}

func (c *client) loggingSinkList(ctx context.Context, parent string) (map[string]map[string]any, error) {
	collection := strings.Split(parent, "/")[0]
	if parent != "projects/"+c.project && !firewallContainerName(parent) {
		return nil, groupDenied("logging_sink_scope_invalid")
	}
	rows, err := c.batchList(ctx, "logging."+collection+".sinks.list", map[string]any{"parent": parent, "pageSize": 1000, "filter": `in_scope("DEFAULT")`}, "sinks")
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	result := map[string]map[string]any{}
	for _, row := range rows {
		id, err := c.loggingSinkData(parent, row)
		if err != nil {
			return nil, err
		}
		if result[id] != nil {
			return nil, groupDenied("logging_sink_duplicate")
		}
		result[id] = row
	}
	return result, nil
}
func (c *client) loggingSinkRead(ctx context.Context, parent, id string) (map[string]any, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	op, ok := metadata.catalog.Operation("logging." + strings.Split(parent, "/")[0] + ".sinks.get")
	if !ok {
		return nil, groupDenied("logging_sink_operation_missing")
	}
	bound, err := catalog.BindREST(op, map[string]any{"sinkName": id})
	if err != nil {
		return nil, err
	}
	response, err := c.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if err := checkListCompleteness(response.Data); err != nil {
		return nil, err
	}
	actual, err := c.loggingSinkData(parent, response.Data)
	if err != nil {
		return nil, err
	}
	if actual != id {
		return nil, groupDenied("logging_sink_identity_changed")
	}
	return response.Data, nil
}

// DEFAULT on each actual ancestor covers both intercepting and non-intercepting
// aggregates. ALL on the project would omit the latter. Keep disabled sinks and
// potentially intercepted child routes as configuration dependencies; do not
// derive absence from current delivery/IAM state or attempt to simulate routing.
func (c *client) loggingRouting(ctx context.Context) (loggingRouting, error) {
	result := loggingRouting{Sinks: map[string]map[string]any{}, Projects: map[string][]map[string]any{}}
	ancestry, err := c.organizationAncestry(ctx)
	if err != nil {
		return result, contracts.DependencyReadError(err)
	}
	result.Ancestry = ancestry
	parents := []string{"projects/" + c.project}
	for _, folder := range ancestry.Folders {
		parents = append(parents, folder.Name)
	}
	if ancestry.Organization != nil {
		parents = append(parents, text(ancestry.Organization["name"]))
	}
	for _, parent := range parents {
		listed, err := c.loggingSinkList(ctx, parent)
		if err != nil {
			return result, err
		}
		ids := make([]string, 0, len(listed))
		for id := range listed {
			ids = append(ids, id)
		}
		slices.Sort(ids)
		for _, id := range ids {
			live, err := c.loggingSinkRead(ctx, parent, id)
			if err != nil {
				return result, err
			}
			if firewallDigest(live) != firewallDigest(listed[id]) {
				return result, groupDenied("logging_sink_changed")
			}
			result.Sinks[id] = live
			if parent != parents[0] && live["includeChildren"] != true {
				continue
			}
			project, err := loggingDestinationProject(text(live["destination"]))
			if err != nil {
				return result, err
			}
			if project != "" {
				result.Projects[project] = append(result.Projects[project], live)
			}
		}
		again, err := c.loggingSinkList(ctx, parent)
		if err != nil {
			return result, err
		}
		if firewallDigest(listed) != firewallDigest(again) {
			return result, groupDenied("logging_sink_set_changed")
		}
	}
	return result, nil
}

func loggingRouteReference(sink map[string]any, check string) monitoringReference {
	evaluate := func(filter string) monitoringReference {
		expression, ok := parseLoggingFilter(filter)
		if !ok {
			return monitoringUnresolvedReference
		}
		return expression.match(check) // Preserve unknown through exclusion NOT.
	}
	result := monitoringHasReference
	if filter := text(sink["filter"]); filter != "" {
		result = evaluate(filter)
	}
	for _, raw := range array(sink["exclusions"]) {
		exclusion := object(raw)
		if exclusion["disabled"] != true {
			result = monitoringAnd(result, loggingNot(evaluate(text(exclusion["filter"]))))
		}
	}
	return result
}
