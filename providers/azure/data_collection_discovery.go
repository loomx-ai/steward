package azure

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const dataCollectionGraphQuery = "insightsresources | where type =~ 'microsoft.insights/datacollectionruleassociations' | project id, type, location | order by id asc"

// Resource Graph is a supplementary discovery index. It may lag native state;
// its entries never prove existence or authorize deletion without a native GET.
func (c *client) dataCollectionGraph(ctx context.Context) ([]any, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	operation, ok := metadata.catalog.Operation("Azure.Microsoft.ResourceGraph.Resources")
	if !ok {
		return nil, fmt.Errorf("missing Azure Resource Graph operation")
	}
	endpoint := apiURL("/providers/Microsoft.ResourceGraph/resources", "2024-04-01")
	options := map[string]any{"resultFormat": "objectArray", "allowPartialScopes": false}
	rows := []any{}
	seen, identities := map[string]bool{}, map[string]bool{}
	var total int64 = -1
	for {
		bound, err := catalog.BindREST(operation, map[string]any{"query": map[string]any{
			"subscriptions": []string{c.subscription}, "query": dataCollectionGraphQuery, "options": options,
		}})
		if err != nil {
			return nil, err
		}
		// This one fixed read query is subscription-bound in its request body.
		// The ordinary transport and generic Invoke still reject global URLs.
		if bound.Method != http.MethodPost {
			return nil, fmt.Errorf("invalid Azure Resource Graph method")
		}
		result, err := c.requestAt(ctx, bound.Method, bound.URL, bound.Body, bound.Headers, func(value string) error {
			if value != endpoint {
				return fmt.Errorf("invalid Azure Resource Graph endpoint")
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
		page, valid := result.data["data"].([]any)
		count, countOK := result.data["count"].(json.Number)
		number, countErr := count.Int64()
		records, totalOK := result.data["totalRecords"].(json.Number)
		currentTotal, totalErr := records.Int64()
		truncated, truncatedOK := result.data["resultTruncated"].(string)
		if result.status != 200 || !valid || !countOK || countErr != nil || number != int64(len(page)) || !totalOK || totalErr != nil || currentTotal < number || currentTotal > 100000 ||
			!truncatedOK || (truncated != "true" && truncated != "false") || (total >= 0 && total != currentTotal) {
			return nil, serviceDenied("invalid_data_collection_query_response")
		}
		total = currentTotal
		for _, value := range page {
			raw := object(value)
			id, kind, err := parseID(text(raw["id"]))
			if err != nil || !strings.HasPrefix(id, c.root()+"/") || !strings.EqualFold(kind, dataCollectionAssociationType) || !strings.EqualFold(text(raw["type"]), kind) || identities[id] {
				return nil, serviceDenied("invalid_data_collection_query_identity")
			}
			identities[id] = true
			rows = append(rows, raw)
		}
		token, tokenOK := result.data["$skipToken"].(string)
		if result.data["$skipToken"] != nil && !tokenOK {
			return nil, serviceDenied("invalid_data_collection_query_pagination")
		}
		if token == "" {
			if truncated != "false" || int64(len(rows)) != total {
				return nil, serviceDenied("incomplete_data_collection_query")
			}
			return rows, nil
		}
		if len(page) == 0 || len(token) > 128*1024 || seen[token] || int64(len(rows)) >= total {
			return nil, serviceDenied("invalid_data_collection_query_pagination")
		}
		seen[token] = true
		options["$skipToken"] = token
	}
}

func (c *client) dataCollectionOrphanTargets(ctx context.Context, targets []productTarget) ([]productTarget, error) {
	rows, err := c.dataCollectionGraph(ctx)
	if err != nil {
		return nil, err
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	operation, _ := metadata.catalog.Operation("Azure.Microsoft.Insights.DataCollectionRuleAssociations_ListByResource")
	kind, _ := findType(dataCollectionAssociationType)
	orphans, generations := map[string]productTarget{}, map[string][]string{}
	for _, value := range rows {
		raw := object(value)
		id := strings.ToLower(text(raw["id"]))
		endpoint, err := c.resourceURL(kind, id)
		if err != nil {
			return nil, err
		}
		live, err := c.request(ctx, "GET", endpoint)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !validResourceResponse(live, id, dataCollectionAssociationType) || len(dataCollectionReferences(live.data)) == 0 {
			return nil, serviceDenied("invalid_data_collection_association")
		}
		canonical, err := c.dataCollectionCanonicalTarget(ctx, live.data, targets)
		if err != nil {
			return nil, err
		}
		if canonical.ParentID != "" {
			indexed, err := c.dataCollectionAssociations(ctx, asset.Identity{NativeID: canonical.ParentID, NativeType: canonical.ParentType})
			if err != nil {
				return nil, err
			}
			if !slices.ContainsFunc(indexed, func(child serviceChild) bool {
				return child.id == id && productGeneration(child.data) == productGeneration(live.data)
			}) {
				return nil, serviceDenied("data_collection_reverse_indexes_disagree")
			}
			continue
		}
		monitored, err := dataCollectionMonitoredResource(id)
		if err != nil {
			return nil, err
		}
		bound, err := catalog.BindREST(operation, map[string]any{"resourceUri": strings.TrimPrefix(monitored, "/")})
		if err != nil {
			return nil, err
		}
		location := orphans[monitored].Location
		if location == "" {
			location = text(raw["location"])
		}
		orphans[monitored] = productTarget{Endpoint: bound.URL, MonitoredResource: monitored, Location: location}
		generations[monitored] = append(generations[monitored], id+":"+productGeneration(live.data))
	}
	keys := []string{}
	for key := range orphans {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		target := orphans[key]
		u, _ := url.Parse(target.Endpoint)
		listed, err := c.listAllURL(ctx, target.Endpoint, u.Path)
		if err != nil {
			return nil, err
		}
		// A native GET still found each seed. An empty or partial native list
		// must not erase those resources from this scan.
		for _, expected := range generations[key] {
			id := expected[:strings.LastIndex(expected, ":")]
			if !slices.ContainsFunc(listed, func(value any) bool { return strings.EqualFold(text(object(value)["id"]), id) }) {
				return nil, serviceDenied("data_collection_resource_index_incomplete")
			}
		}
		slices.Sort(generations[key])
		target.Generation = fmt.Sprintf("%x", sha256.Sum256([]byte(strings.Join(generations[key], "\n"))))
		targets = append(targets, target)
	}
	return targets, nil
}

func (c *client) dataCollectionCanonicalTarget(ctx context.Context, raw map[string]any, targets []productTarget) (productTarget, error) {
	canonical := productTarget{}
	for _, ref := range dataCollectionReferences(raw) {
		index := slices.IndexFunc(targets, func(target productTarget) bool { return target.ParentID == ref })
		if index >= 0 {
			if canonical.ParentID == "" {
				canonical = targets[index]
			}
			continue
		}
		if !strings.HasPrefix(ref, c.root()+"/") {
			continue
		}
		_, nativeType, _ := parseID(ref)
		kind, _ := findType(nativeType)
		endpoint, err := c.resourceURL(kind, ref)
		if err != nil {
			return productTarget{}, err
		}
		_, err = c.request(ctx, "GET", endpoint)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return productTarget{}, err
		}
		return productTarget{}, serviceDenied("data_collection_target_missing_from_inventory")
	}
	return canonical, nil
}

func dataCollectionTargetMembership(raw map[string]any, target productTarget) error {
	if target.MonitoredResource == "" {
		return dataCollectionAssociationMembership(raw, target.ParentID)
	}
	monitored, err := dataCollectionMonitoredResource(text(raw["id"]))
	if err != nil || monitored != target.MonitoredResource || len(dataCollectionReferences(raw)) == 0 {
		return serviceDenied("data_collection_association_changed")
	}
	return nil
}
