package azure

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

// The official SDK uses POST for the initial ListStreamingJobs request and
// GET for nextLink pages. This is a read operation, with no request body.
func (c *client) streamAnalyticsClusterJobs(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	kind, _ := findType(streamAnalyticsClusterType)
	_, parameters, err := c.resourceOperation(kind, parent.NativeID, "GET")
	if err != nil {
		return nil, err
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	op, _ := metadata.catalog.Operation("Azure.Microsoft.StreamAnalytics.Clusters_ListStreamingJobs")
	bound, err := bindAzureREST(op, parameters)
	if err != nil {
		return nil, err
	}
	first, _ := url.Parse(bound.URL)
	next, method := bound.URL, bound.Method
	seenPages, seenJobs := map[string]bool{}, map[string]bool{}
	var children []serviceChild
	for next != "" {
		if seenPages[next] {
			return nil, serviceDenied("stream_analytics_cluster_jobs_repeated_page")
		}
		seenPages[next] = true
		if err := c.validateURL(next); err != nil {
			return nil, err
		}
		u, _ := url.Parse(next)
		if err := streamAnalyticsListQuery(u); err != nil {
			return nil, err
		}
		if !strings.EqualFold(u.Path, first.Path) || u.Query().Get("api-version") != kind.Version {
			return nil, serviceDenied("stream_analytics_cluster_jobs_page_changed")
		}
		page, err := c.request(ctx, method, next)
		if err != nil {
			return nil, err
		}
		values, ok := page.data["value"].([]any)
		if page.status != 200 || page.data["error"] != nil || !ok {
			return nil, serviceDenied("incomplete_stream_analytics_cluster_jobs")
		}
		for _, value := range values {
			record := object(value)
			id, typ, err := parseID(text(record["id"]))
			if err != nil || !strings.HasPrefix(id, c.root()+"/") || streamAnalyticsKind(typ) != streamAnalyticsJobType || seenJobs[id] || !validResponseType(streamAnalyticsJobType, text(record["type"])) {
				return nil, serviceDenied("invalid_stream_analytics_cluster_job")
			}
			seenJobs[id] = true
			job, err := c.streamAnalyticsResource(ctx, id)
			if err != nil {
				return nil, err
			}
			clusterID, err := streamAnalyticsJobCluster(job)
			if err != nil || !strings.EqualFold(clusterID, parent.NativeID) || resourceRegion(job) != resourceRegion(raw) || record["jobState"] != object(job["properties"])["jobState"] {
				return nil, serviceDenied("stream_analytics_cluster_job_membership_changed")
			}
			children = append(children, serviceChild{kind: streamAnalyticsJobType, id: id, data: job})
		}
		next = ""
		if value := page.data["nextLink"]; value != nil {
			var ok bool
			next, ok = value.(string)
			if !ok {
				return nil, serviceDenied("invalid_stream_analytics_cluster_jobs_next_link")
			}
		}
		method = "GET"
	}
	return children, nil
}

func streamAnalyticsJobIndexes(raw map[string]any, children []serviceChild) error {
	props := object(raw["properties"])
	for field, kind := range map[string]string{"inputs": streamAnalyticsInputType, "outputs": streamAnalyticsOutputType, "functions": streamAnalyticsFunctionType, "transformation": streamAnalyticsTransformationType} {
		value, present := props[field]
		if !present {
			continue // The default native job GET does not expand these fields.
		}
		var values []any
		if field == "transformation" {
			if value != nil {
				values = []any{value}
			}
		} else if value != nil {
			var ok bool
			values, ok = value.([]any)
			if !ok {
				return serviceDenied("invalid_stream_analytics_job_index")
			}
		}
		actual := map[string]bool{}
		for _, child := range children {
			if child.kind == kind {
				actual[child.id] = true
			}
		}
		for _, value := range values {
			record := object(value)
			id, typ, err := parseID(text(record["id"]))
			if err != nil || !strings.EqualFold(typ, kind) || !actual[id] || !strings.EqualFold(last(id), text(record["name"])) || !validResponseType(kind, text(record["type"])) {
				return serviceDenied("stream_analytics_job_indexes_disagree")
			}
			delete(actual, id)
		}
		if len(actual) != 0 {
			return serviceDenied("stream_analytics_job_indexes_disagree")
		}
	}
	return nil
}

func (c *client) streamAnalyticsChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	collect := func() ([]serviceChild, error) {
		kinds := streamAnalyticsOwnedKinds(parent.NativeType)
		if parent.NativeType == streamAnalyticsJobType {
			kinds = []string{streamAnalyticsInputType, streamAnalyticsOutputType, streamAnalyticsFunctionType}
		}
		children, err := c.nativeServiceChildren(ctx, parent, raw, kinds)
		if err != nil {
			return nil, err
		}
		if parent.NativeType == streamAnalyticsJobType {
			transformation, err := c.streamAnalyticsTransformation(ctx, parent)
			if err != nil {
				return nil, err
			}
			children = append(children, transformation...)
			if err := streamAnalyticsJobIndexes(raw, children); err != nil {
				return nil, err
			}
		} else if parent.NativeType == streamAnalyticsClusterType {
			jobs, err := c.streamAnalyticsClusterJobs(ctx, parent, raw)
			if err != nil {
				return nil, err
			}
			children = append(children, jobs...)
		}
		current, err := c.streamAnalyticsResource(ctx, parent.NativeID)
		if err != nil {
			return nil, err
		}
		if err := streamAnalyticsReady(parent.NativeType, current); err != nil {
			return nil, err
		}
		if c.privateConfiguration(streamAnalyticsSnapshot(parent.NativeType, raw)) != c.privateConfiguration(streamAnalyticsSnapshot(parent.NativeType, current)) {
			return nil, serviceDenied("stream_analytics_parent_changed")
		}
		if parent.NativeType == streamAnalyticsJobType {
			if err := streamAnalyticsJobIndexes(current, children); err != nil {
				return nil, err
			}
		}
		for _, child := range children {
			if err := streamAnalyticsReady(child.kind, child.data); err != nil {
				return nil, err
			}
		}
		slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
		return children, nil
	}
	first, err := collect()
	if err != nil {
		return nil, err
	}
	second, err := collect()
	if err != nil {
		return nil, err
	}
	if !slices.EqualFunc(first, second, func(a, b serviceChild) bool {
		return a.id == b.id && c.privateConfiguration(streamAnalyticsSnapshot(a.kind, a.data)) == c.privateConfiguration(streamAnalyticsSnapshot(b.kind, b.data))
	}) {
		return nil, serviceDenied("stream_analytics_children_changed")
	}
	return second, nil
}
