package gcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type pageCursor struct {
	Token    string `json:"token"`
	ReadTime string `json:"read_time"`
}

func (c *client) assetPage(ctx context.Context, cursor, nativeType string, limit int) (map[string]any, error) {
	result, err := c.assetPageResult(ctx, cursor, nativeType, limit)
	return result.Data, err
}
func (c *client) assetPageResult(ctx context.Context, cursor, nativeType string, limit int) (contracts.InvocationResult, error) {
	if limit < 1 || limit > 1000 {
		limit = 500
	}
	query := url.Values{"contentType": {"RESOURCE"}, "pageSize": {strconv.Itoa(limit)}}
	if nativeType != "" {
		query.Set("assetTypes", nativeType)
	}
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return contracts.InvocationResult{}, fmt.Errorf("invalid GCP inventory cursor")
		}
		var page pageCursor
		if json.Unmarshal(decoded, &page) != nil || page.Token == "" {
			return contracts.InvocationResult{}, fmt.Errorf("invalid GCP inventory cursor")
		}
		query.Set("pageToken", page.Token)
		if page.ReadTime != "" {
			query.Set("readTime", page.ReadTime)
		}
	}
	result, err := c.requestResult(ctx, "GET", "https://cloudasset.googleapis.com/v1/projects/"+c.project+"/assets", query, nil)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	data := result.Data
	if requested := query.Get("readTime"); requested != "" {
		previous, previousErr := time.Parse(time.RFC3339Nano, requested)
		current, currentErr := time.Parse(time.RFC3339Nano, text(data["readTime"]))
		if previousErr != nil || currentErr != nil || !previous.Equal(current) {
			return contracts.InvocationResult{}, fmt.Errorf("Google asset snapshot changed during pagination")
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, text(data["readTime"])); err != nil {
		return contracts.InvocationResult{}, fmt.Errorf("Google asset response has no valid snapshot time")
	}
	if assets, present := data["assets"]; present && assets != nil {
		if _, ok := assets.([]any); !ok {
			return contracts.InvocationResult{}, fmt.Errorf("Google asset response has an invalid asset list")
		}
	}
	if token := text(data["nextPageToken"]); token != "" {
		if token == query.Get("pageToken") {
			return contracts.InvocationResult{}, fmt.Errorf("Google asset pagination did not advance")
		}
		readTime := text(data["readTime"])
		if readTime == "" {
			readTime = query.Get("readTime")
		}
		encoded, _ := json.Marshal(pageCursor{Token: token, ReadTime: readTime})
		data["nextPageToken"] = base64.RawURLEncoding.EncodeToString(encoded)
	}
	result.NextToken = text(data["nextPageToken"])
	return result, nil
}
func (r *Runtime) List(ctx context.Context, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	if request.Source != "" && request.Source != inventorySource {
		return contracts.InventoryBatch{}, fmt.Errorf("unsupported GCP inventory source")
	}
	c, err := r.resolve(ctx, request.ConnectionID)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	if request.Scope.Kind == asset.ScopeProject && request.Scope.NativeID != c.project && request.Scope.NativeID != c.number {
		return contracts.InventoryBatch{}, fmt.Errorf("GCP inventory belongs to another project")
	}
	nativeType := ""
	if request.ResourceKind != nil {
		nativeType = request.ResourceKind.NativeType
	}
	result, err := c.assetPageResult(ctx, request.Cursor, nativeType, request.Limit)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	data := result.Data
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{}, NextCursor: text(data["nextPageToken"]), RequestID: result.RequestID}
	batch.Complete = batch.NextCursor == ""
	region := request.Scope.NativeID
	if request.Scope.Kind == asset.ScopeGlobal {
		region = "global"
	}
	for _, value := range array(data["assets"]) {
		raw := object(value)
		_, location := assetLocation(raw)
		if request.Scope.Kind != asset.ScopeProject && region != location && !(request.NetworkTarget != nil && location == "global") {
			continue
		}
		item, err := r.inventoryItem(c, raw)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		batch.Items = append(batch.Items, item)
	}
	return batch, nil
}

func assetLocation(raw map[string]any) (string, string) {
	resource := object(raw["resource"])
	data := object(resource["data"])
	location := text(resource["location"])
	for _, key := range []string{"zone", "region", "location"} {
		if location == "" {
			location = last(text(data[key]))
		}
	}
	name := text(raw["name"])
	parts := strings.Split(name, "/")
	for i, part := range parts {
		if (part == "zones" || part == "regions" || part == "locations") && i+1 < len(parts) {
			location = parts[i+1]
			break
		}
	}
	if location == "" {
		location = "global"
	}
	kind, known := findType(text(raw["assetType"]))
	if known && len(kind.Scopes) == 1 && kind.Scopes[0] == asset.ScopeGlobal {
		return location, "global"
	}
	if text(raw["assetType"]) == "secretmanager.googleapis.com/Secret" && !strings.Contains(name, "/locations/") {
		return location, "global"
	}
	return location, regionOf(location)
}

func (r *Runtime) inventoryItem(c *client, raw map[string]any) (contracts.InventoryItem, error) {
	nativeType := text(raw["assetType"])
	nativeID := c.canonicalName(text(raw["name"]))
	if nativeType == "" || nativeID == "" {
		return contracts.InventoryItem{}, fmt.Errorf("Google asset identity is incomplete")
	}
	data := object(object(raw["resource"])["data"])
	kind, known := findType(nativeType)
	if known {
		if data == nil {
			return contracts.InventoryItem{}, fmt.Errorf("Google asset resource data is missing")
		}
		if _, err := c.resourceURL(kind, nativeID); err != nil {
			return contracts.InventoryItem{}, err
		}
	}
	location, region := assetLocation(raw)
	scope := contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: region, Name: region, Location: region}
	if region == "global" {
		scope = contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: c.project + "/global", Name: "Global", Location: "global"}
	}
	normalized := map[string]any{}
	for key, value := range data {
		normalized[key] = value
	}
	normalized["_inventory_source"] = inventorySource
	normalized["project_id"] = c.project
	if reason := protectionReason(nativeType, data); reason != "" {
		normalized["cleanup_protected"] = true
		normalized["cleanup_protection_reason"] = reason
	}
	refs := references(c, data)
	networkIDs := refs["compute.googleapis.com/Network"]
	subnetIDs := refs["compute.googleapis.com/Subnetwork"]
	if len(networkIDs) == 1 {
		normalized["vpc_id"] = networkIDs[0]
	}
	if len(subnetIDs) > 0 {
		normalized["subnet_ids"] = subnetIDs
		if len(subnetIDs) == 1 {
			normalized["vswitch_id"] = subnetIDs[0]
		}
	}
	if nativeType == "compute.googleapis.com/Network" {
		normalized["vpc_id"] = nativeID
	}
	if nativeType == "compute.googleapis.com/Subnetwork" {
		normalized["vswitch_id"] = nativeID
	}
	if zone := text(data["zone"]); zone != "" {
		normalized["zone_id"] = last(zone)
	}
	networkRefs := []string{}
	for target, values := range refs {
		normalized[referenceKey(target)] = values
		networkRefs = append(networkRefs, values...)
	}
	sort.Strings(networkRefs)
	tags := map[string]string{}
	for key, value := range object(data["labels"]) {
		if s, ok := value.(string); ok {
			tags[key] = s
		}
	}
	name := text(data["displayName"])
	if name == "" {
		name = last(text(data["name"]))
	}
	if name == "" {
		name = last(nativeID)
	}
	state := text(data["status"])
	if state == "" {
		state = text(data["state"])
	}
	actionable := known && len(kind.DeleteOperations) > 0
	return contracts.InventoryItem{NativeType: nativeType, NativeID: nativeID, ResourceKind: r.resourceKind(nativeType), Actionable: &actionable, Scope: scope, Name: name, State: state, Location: location, Tags: tags, Normalized: safePayload(normalized), Raw: safePayload(raw), NativeAliases: []string{text(data["selfLink"]), nativeID}, NetworkReferences: networkRefs}, nil
}

func (c *client) canonicalName(value string) string {
	value = canonicalName(value)
	if c.number != "" {
		value = strings.Replace(value, "/projects/"+c.number+"/", "/projects/"+c.project+"/", 1)
	}
	return value
}

func canonicalName(value string) string {
	value = strings.TrimSpace(value)
	if strings.HasPrefix(value, "https://www.googleapis.com/compute/v1/") {
		value = "//compute.googleapis.com/" + strings.TrimPrefix(value, "https://www.googleapis.com/compute/v1/")
	}
	if strings.HasPrefix(value, "https://compute.googleapis.com/compute/v1/") {
		value = "//compute.googleapis.com/" + strings.TrimPrefix(value, "https://compute.googleapis.com/compute/v1/")
	}
	value = strings.Replace(value, "//cloudsql.googleapis.com/", "//sqladmin.googleapis.com/", 1)
	if strings.HasPrefix(value, "//container.googleapis.com/") {
		value = strings.Replace(value, "/zones/", "/locations/", 1)
	}
	return value
}

func references(c *client, data map[string]any) map[string][]string {
	result := map[string][]string{}
	// Restrict references to live dependencies. Creation history (sourceImage,
	// sourceSnapshot) and reverse children lists are not deletion dependencies.
	fields := map[string]string{"network": "compute.googleapis.com/Network", "networkURL": "compute.googleapis.com/Network", "privateNetwork": "compute.googleapis.com/Network", "subnetwork": "compute.googleapis.com/Subnetwork", "subnetworkURL": "compute.googleapis.com/Subnetwork", "topic": "pubsub.googleapis.com/Topic", "deadLetterTopic": "pubsub.googleapis.com/Topic", "healthChecks": "compute.googleapis.com/HealthCheck", "urlMap": "compute.googleapis.com/UrlMap", "sslCertificates": "compute.googleapis.com/SslCertificate", "backendService": "compute.googleapis.com/BackendService", "defaultService": "compute.googleapis.com/BackendService", "service": "compute.googleapis.com/BackendService", "nextHopInstance": "compute.googleapis.com/Instance", "target": "", "source": "compute.googleapis.com/Disk"}
	var visit func(any, string)
	visit = func(value any, key string) {
		switch typed := value.(type) {
		case map[string]any:
			for child, v := range typed {
				visit(v, child)
			}
		case []any:
			for _, v := range typed {
				visit(v, key)
			}
		case string:
			target, ok := fields[key]
			if !ok {
				return
			}
			ref := c.canonicalName(typed)
			if target == "compute.googleapis.com/Network" || target == "compute.googleapis.com/Subnetwork" {
				if strings.HasPrefix(ref, "projects/") {
					ref = "//compute.googleapis.com/" + ref
				}
				if !strings.Contains(ref, "/") && target == "compute.googleapis.com/Network" {
					ref = "//compute.googleapis.com/projects/" + c.project + "/global/networks/" + ref
				}
			}
			if target == "pubsub.googleapis.com/Topic" && strings.HasPrefix(ref, "projects/") {
				ref = "//pubsub.googleapis.com/" + ref
			}
			if target == "" {
				for _, kind := range allTypes() {
					if strings.HasPrefix(ref, "//compute.googleapis.com/") && strings.Contains(ref, "/"+kind.Collection+"/") {
						target = kind.NativeType
						break
					}
				}
			}
			if target == "compute.googleapis.com/Disk" && strings.Contains(ref, "/regions/") {
				target = "compute.googleapis.com/RegionDisk"
			}
			if target == "compute.googleapis.com/BackendService" && strings.Contains(ref, "/regions/") {
				target = "compute.googleapis.com/RegionBackendService"
			}
			kind, known := findType(target)
			if !known {
				return
			}
			if _, err := c.resourceURL(kind, ref); err != nil {
				return
			}
			for _, existing := range result[target] {
				if existing == ref {
					return
				}
			}
			result[target] = append(result[target], ref)
		}
	}
	visit(data, "")
	for _, values := range result {
		sort.Strings(values)
	}
	return result
}

func (r *Runtime) SearchNetworkTargets(ctx context.Context, query contracts.NetworkTargetQuery) (contracts.NetworkTargetPage, error) {
	c, err := r.resolve(ctx, query.ConnectionID)
	if err != nil {
		return contracts.NetworkTargetPage{}, err
	}
	nativeType := "compute.googleapis.com/Network"
	if query.Kind == asset.ScanTargetVSwitch {
		nativeType = "compute.googleapis.com/Subnetwork"
	} else if query.Kind != asset.ScanTargetVPC {
		return contracts.NetworkTargetPage{}, fmt.Errorf("unsupported GCP network target")
	}
	data, err := c.assetPage(ctx, query.Cursor, nativeType, query.Limit)
	if err != nil {
		return contracts.NetworkTargetPage{}, err
	}
	page := contracts.NetworkTargetPage{Items: []contracts.NetworkTargetOption{}, NextCursor: text(data["nextPageToken"])}
	for _, raw := range array(data["assets"]) {
		item, err := r.inventoryItem(c, object(raw))
		if err != nil {
			return contracts.NetworkTargetPage{}, err
		}
		if query.Kind == asset.ScanTargetVSwitch && item.Scope.NativeID != query.RegionID {
			continue
		}
		parent := text(item.Normalized["vpc_id"])
		if query.ParentNativeID != "" && parent != query.ParentNativeID {
			continue
		}
		if query.Query != "" && !strings.Contains(strings.ToLower(item.Name+" "+item.NativeID), strings.ToLower(query.Query)) {
			continue
		}
		page.Items = append(page.Items, contracts.NetworkTargetOption{Kind: query.Kind, RegionID: query.RegionID, NativeID: item.NativeID, Name: item.Name, ParentNativeID: parent})
	}
	return page, nil
}
