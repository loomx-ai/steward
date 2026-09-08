package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

// The cursor binds a service page to its connection, scope, resource rule and
// ordered fanout targets. A changed parent set must restart the scan; continuing
// by numeric parent index could silently omit children.
type productCursor struct {
	Fingerprint string   `json:"fingerprint"`
	Target      int      `json:"target"`
	Token       string   `json:"token,omitempty"`
	Seen        []string `json:"seen,omitempty"`
}
type productTarget struct {
	API        spec.ProductAPISpec `json:"api"`
	Parameters map[string]any      `json:"parameters"`
	ParentType string              `json:"parent_type,omitempty"`
	ParentID   string              `json:"parent_id,omitempty"`
}
type productRecord struct {
	Data     map[string]any
	Location string
}

func (r *Runtime) productDefinition(nativeType string) (spec.ResourceKindSpec, bool) {
	for _, compiled := range r.bundle.Specs {
		if compiled.ResourceKind.NativeType == nativeType {
			return compiled.Definition, compiled.Definition.Discovery.Source == productInventorySource
		}
	}
	return spec.ResourceKindSpec{}, false
}
func (r *Runtime) usesProductSource(nativeType string) bool {
	_, ok := r.productDefinition(nativeType)
	return ok
}

func (r *Runtime) listProduct(ctx context.Context, c *client, request contracts.InventoryRequest, ancestors []string) (contracts.InventoryBatch, error) {
	if request.ResourceKind == nil {
		return contracts.InventoryBatch{}, fmt.Errorf("GCP product inventory requires a resource kind")
	}
	nativeType := request.ResourceKind.NativeType
	definition, ok := r.productDefinition(nativeType)
	if !ok || definition.Discovery.List == nil {
		return contracts.InventoryBatch{}, fmt.Errorf("GCP resource %q has no product discovery rule", nativeType)
	}
	if slices.Contains(ancestors, nativeType) {
		return contracts.InventoryBatch{}, fmt.Errorf("GCP parent discovery contains a cycle")
	}
	ancestors = append(slices.Clone(ancestors), nativeType)
	targets, err := r.productTargets(ctx, c, request, definition, ancestors)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	bound, _ := json.Marshal(struct {
		Connection                                        asset.ConnectionID
		Project, ScopeKind, ScopeID, NativeType, Revision string
		Network                                           *asset.ScanTarget
		Targets                                           []productTarget
	}{request.ConnectionID, c.project, string(request.Scope.Kind), request.Scope.NativeID, nativeType, r.bundle.Revision, request.NetworkTarget, targets})
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(bound))
	cursor := productCursor{Fingerprint: fingerprint}
	if request.Cursor != "" {
		if len(request.Cursor) > 128*1024 {
			return contracts.InventoryBatch{}, fmt.Errorf("GCP product cursor exceeds size limit")
		}
		decoded, decodeErr := base64.RawURLEncoding.DecodeString(request.Cursor)
		if decodeErr != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Fingerprint != fingerprint || cursor.Target < 0 || cursor.Target >= len(targets) {
			return contracts.InventoryBatch{}, fmt.Errorf("GCP product cursor does not match the scan or its current parents")
		}
	}
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{}, Complete: true}
	if len(targets) == 0 {
		return batch, nil
	}
	target := targets[cursor.Target]
	parameters := cloneParameters(target.Parameters)
	if pagination := target.API.Pagination; pagination != nil {
		if cursor.Token != "" {
			parameters[pagination.TokenParameter] = cursor.Token
		}
		if pagination.PageSizeParameter != "" {
			limit := request.Limit
			if limit <= 0 {
				limit = 500
			}
			if pagination.MaxPageSize > 0 && limit > pagination.MaxPageSize {
				limit = pagination.MaxPageSize
			}
			parameters[pagination.PageSizeParameter] = limit
		}
	}
	result, err := r.Invoke(ctx, contracts.Invocation{ConnectionID: request.ConnectionID, Operation: target.API.Operation, Parameters: parameters})
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	if err = checkListCompleteness(result.Data); err != nil {
		return contracts.InventoryBatch{}, fmt.Errorf("%s: %w", target.API.Operation, err)
	}
	records, err := productRecords(result.Data, target.API.ItemsPath)
	if err != nil {
		return contracts.InventoryBatch{}, fmt.Errorf("%s: %w", target.API.Operation, err)
	}
	batch.RequestID = result.RequestID
	kind, _ := findType(nativeType)
	metadata, _ := providerData()
	operation, _ := metadata.catalog.Operation(target.API.Operation)
	seenIDs := map[string]bool{}
	for _, record := range records {
		id, err := c.productIdentity(kind, operation, parameters, target.API.IdentityPath, record)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		// Aggregate Compute collections can contain kinds with separate global and
		// regional bindings. Route those records to their canonical kind's shard.
		if _, err := c.resourceURL(kind, id); err != nil {
			if strings.HasPrefix(nativeType, "compute.googleapis.com/") && c.otherProductKind(kind, id) {
				continue
			}
			return contracts.InventoryBatch{}, err
		}
		if seenIDs[id] {
			return contracts.InventoryBatch{}, fmt.Errorf("GCP list returned duplicate resource %q", id)
		}
		seenIDs[id] = true
		location := record.Location
		if location == "" {
			location = last(text(record.Data["location"]))
		}
		raw := map[string]any{"name": id, "assetType": nativeType, "resource": map[string]any{"data": record.Data, "location": location}}
		item, err := r.inventoryItem(c, raw)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		if !productScopeMatches(request, item) {
			continue
		}
		item.Normalized["_inventory_source"] = productInventorySource
		if target.ParentID != "" {
			item.Normalized[referenceKey(target.ParentType)] = []string{target.ParentID}
		}
		batch.Items = append(batch.Items, item)
	}
	next := ""
	if pagination := target.API.Pagination; pagination != nil {
		value := productValue(result.Data, pagination.TokenPath)
		if value != nil {
			var valid bool
			next, valid = value.(string)
			if !valid {
				return contracts.InventoryBatch{}, fmt.Errorf("GCP next page token is not a string")
			}
		}
	} else if text(result.Data["nextPageToken"]) != "" {
		return contracts.InventoryBatch{}, fmt.Errorf("GCP paginated response has no pagination rule")
	}
	if next != "" {
		tokenHash := fmt.Sprintf("%x", sha256.Sum256([]byte(next)))
		if next == cursor.Token || slices.Contains(cursor.Seen, tokenHash) {
			return contracts.InventoryBatch{}, fmt.Errorf("GCP product pagination did not advance")
		}
		cursor.Token = next
		cursor.Seen = append(cursor.Seen, tokenHash)
	} else {
		cursor.Target++
		cursor.Token = ""
		cursor.Seen = nil
	}
	if cursor.Target < len(targets) {
		encoded, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
		batch.Complete = false
	}
	return batch, nil
}

func productScopeMatches(request contracts.InventoryRequest, item contracts.InventoryItem) bool {
	switch request.Scope.Kind {
	case asset.ScopeProject:
		return true
	case asset.ScopeGlobal:
		return item.Scope.Kind == asset.ScopeGlobal
	case asset.ScopeRegion:
		return item.Scope.NativeID == request.Scope.NativeID || (request.NetworkTarget != nil && item.Scope.Kind == asset.ScopeGlobal)
	default:
		return false
	}
}

func (r *Runtime) productTargets(ctx context.Context, c *client, request contracts.InventoryRequest, definition spec.ResourceKindSpec, ancestors []string) ([]productTarget, error) {
	kind, _ := findType(definition.Metadata.NativeType)
	metadata, _ := providerData()
	locations := []string{request.Scope.NativeID}
	if request.Scope.Kind == asset.ScopeGlobal {
		locations = []string{"global"}
	}
	if request.Scope.Kind != asset.ScopeProject && request.Scope.Kind != asset.ScopeRegion && request.Scope.Kind != asset.ScopeGlobal {
		return nil, fmt.Errorf("unsupported GCP product inventory scope")
	}
	if request.Scope.Kind == asset.ScopeRegion && !segmentPattern.MatchString(request.Scope.NativeID) {
		return nil, fmt.Errorf("invalid GCP region")
	}
	// Only APIs that require a concrete region need region fanout for a project
	// request. Project-wide and aggregate methods make one service request.
	var regions []contracts.DiscoveredRegion
	regionLookup := false
	var parents []contracts.InventoryItem
	if parent := definition.Discovery.Parent; parent != nil {
		if parent.Source != productInventorySource || parent.NativeType == "" {
			return nil, fmt.Errorf("GCP parent discovery requires a product resource kind")
		}
		parentKind := r.resourceKind(parent.NativeType)
		parentRequest := request
		parentRequest.ResourceKind = &parentKind
		parentRequest.Cursor = ""
		for {
			page, err := r.listProduct(ctx, c, parentRequest, ancestors)
			if err != nil {
				return nil, err
			}
			parents = append(parents, page.Items...)
			if page.Complete {
				break
			}
			parentRequest.Cursor = page.NextCursor
		}
		sort.Slice(parents, func(i, j int) bool { return parents[i].NativeID < parents[j].NativeID })
		for i := 1; i < len(parents); i++ {
			if parents[i-1].NativeID == parents[i].NativeID {
				return nil, fmt.Errorf("GCP parent inventory returned duplicate identities")
			}
		}
	} else {
		parents = []contracts.InventoryItem{{}}
	}
	targets := []productTarget{}
	for _, id := range kind.ListOperations {
		operation, _ := metadata.catalog.Operation(id)
		api := *definition.Discovery.List
		api.Operation = id
		parameters := cloneParameters(api.Parameters)
		properties := object(operation.InputSchema["properties"])
		// Variants retain their own native required parent pattern (for example
		// global versus regional Secret Manager), instead of assuming one endpoint.
		if id != definition.Discovery.List.Operation {
			for name, raw := range properties {
				property := object(raw)
				if property["required"] != true {
					continue
				}
				switch name {
				case "project", "projectId":
					parameters[name] = "scope.project"
					if slices.Contains(operation.Call.RawPathParameters, name) {
						parameters[name] = "scope.projectPath"
					}
				case "parent":
					parameters[name] = "scope.projectPath"
					if strings.Contains(text(property["pattern"]), "/locations/") {
						parameters[name] = "scope.locationParent"
					}
				case "region":
					parameters[name] = "scope.location"
				}
			}
		}
		regional := false
		for _, value := range parameters {
			if value == "scope.location" || value == "scope.locationParent" {
				regional = true
			}
		}
		onlyGlobal := len(kind.Scopes) == 1 && kind.Scopes[0] == asset.ScopeGlobal
		// A global Secret Manager list must not be repeated by every regional scan.
		if len(kind.ListOperations) > 1 && !regional && request.Scope.Kind == asset.ScopeRegion && request.NetworkTarget == nil {
			continue
		}
		if regional && request.Scope.Kind == asset.ScopeGlobal && (!slices.Contains(kind.Scopes, asset.ScopeGlobal) || len(kind.ListOperations) > 1) {
			continue
		}
		if onlyGlobal && request.Scope.Kind == asset.ScopeRegion && request.NetworkTarget == nil {
			continue
		}
		targetLocations := locations
		if request.Scope.Kind == asset.ScopeProject {
			targetLocations = []string{"global"}
			if regional {
				if !regionLookup {
					var err error
					regions, err = r.DiscoverRegions(ctx, request.ConnectionID)
					if err != nil {
						return nil, err
					}
					regionLookup = true
				}
				targetLocations = nil
				for _, region := range regions {
					targetLocations = append(targetLocations, region.RegionID)
				}
			}
		}
		for _, location := range targetLocations {
			for _, parent := range parents {
				resolved, err := productParameters(parameters, c, location, parent)
				if err != nil {
					return nil, err
				}
				if _, err = catalog.BindREST(operation, resolved); err != nil {
					return nil, err
				}
				targets = append(targets, productTarget{API: api, Parameters: resolved, ParentType: parent.NativeType, ParentID: parent.NativeID})
			}
		}
	}
	return targets, nil
}

func cloneParameters(input map[string]any) map[string]any {
	result := map[string]any{}
	for key, value := range input {
		result[key] = value
	}
	return result
}
func productParameters(input map[string]any, c *client, location string, parent contracts.InventoryItem) (map[string]any, error) {
	result := cloneParameters(input)
	for key, raw := range result {
		value, ok := raw.(string)
		if !ok {
			continue
		}
		switch value {
		case "scope.project":
			result[key] = c.project
		case "scope.projectPath":
			result[key] = "projects/" + c.project
		case "scope.location":
			result[key] = location
		case "scope.locationParent":
			result[key] = "projects/" + c.project + "/locations/" + location
		case "scope.allLocationsParent":
			result[key] = "projects/" + c.project + "/locations/-"
		case "parent.nativeId":
			result[key] = strings.TrimPrefix(parent.NativeID, "//"+strings.Split(parent.NativeType, "/")[0]+"/")
		default:
			if strings.HasPrefix(value, "parent.normalized.") {
				result[key] = productValue(parent.Normalized, strings.TrimPrefix(value, "parent.normalized."))
			}
			if strings.HasPrefix(value, "scope.") {
				return nil, fmt.Errorf("unsupported GCP scope expression %q", value)
			}
		}
		if result[key] == nil || result[key] == "" {
			return nil, fmt.Errorf("GCP product parameter %q is missing", key)
		}
	}
	return result, nil
}
func productValue(value any, path string) any {
	if path == "$" || path == "" {
		return value
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "$."), ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[part]
	}
	return value
}
func productRecords(data map[string]any, path string) ([]productRecord, error) {
	result := []productRecord{}
	var collect func(any, []string, string) error
	collect = func(value any, parts []string, location string) error {
		if value == nil {
			return nil
		} // Google JSON omits empty repeated fields.
		if len(parts) == 0 {
			items, ok := value.([]any)
			if !ok {
				return fmt.Errorf("GCP list response path %q is not an array", path)
			}
			for _, item := range items {
				data, ok := item.(map[string]any)
				if !ok || len(data) == 0 {
					return fmt.Errorf("GCP list contains an invalid resource")
				}
				result = append(result, productRecord{Data: data, Location: location})
			}
			return nil
		}
		object, ok := value.(map[string]any)
		if !ok {
			return fmt.Errorf("GCP list response path %q has invalid structure", path)
		}
		if parts[0] == "*" {
			keys := make([]string, 0, len(object))
			for key := range object {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			for _, key := range keys {
				if err := collect(object[key], parts[1:], key); err != nil {
					return err
				}
			}
			return nil
		}
		return collect(object[parts[0]], parts[1:], location)
	}
	err := collect(data, strings.Split(strings.TrimPrefix(path, "$."), "."), "")
	return result, err
}
func checkListCompleteness(data map[string]any) error {
	for _, key := range []string{"unreachable", "unreachables", "missingZones"} {
		if value, present := data[key]; present && value != nil {
			values, ok := value.([]any)
			if !ok || len(values) > 0 {
				return fmt.Errorf("GCP list is incomplete (%s)", key)
			}
		}
	}
	for _, key := range []string{"warning", "warnings"} {
		value := data[key]
		if value == nil {
			continue
		}
		warnings := []any{value}
		if list, ok := value.([]any); ok {
			warnings = list
		}
		for _, warning := range warnings {
			w, ok := warning.(map[string]any)
			if !ok || text(w["code"]) != "NO_RESULTS_ON_PAGE" {
				return fmt.Errorf("GCP list returned a partial-result warning")
			}
		}
	}
	// Compute aggregated lists place warnings at the per-scope level too.
	if groups, ok := data["items"].(map[string]any); ok {
		for _, value := range groups {
			group, ok := value.(map[string]any)
			if !ok {
				return fmt.Errorf("GCP aggregate scope is not an object")
			}
			if err := checkListCompleteness(group); err != nil {
				return err
			}
		}
	}
	return nil
}

func (c *client) productIdentity(kind resourceType, operation catalog.Operation, parameters map[string]any, identityPath string, record productRecord) (string, error) {
	name := text(productValue(record.Data, identityPath))
	if name == "" {
		name = text(record.Data["name"])
	}
	if name == "" {
		return "", fmt.Errorf("GCP list resource has no identity at %q", identityPath)
	}
	host := strings.Split(kind.NativeType, "/")[0]
	if strings.HasPrefix(name, "//") {
		return c.canonicalName(name), nil
	}
	if strings.HasPrefix(name, "https://") {
		u, err := url.Parse(name)
		if err != nil {
			return "", fmt.Errorf("invalid GCP resource URL")
		}
		origin, _ := url.Parse(operation.Call.Endpoint)
		if u.Host != origin.Host && u.Host != "www.googleapis.com" {
			return "", fmt.Errorf("GCP resource URL has a foreign host")
		}
		if i := strings.Index(u.Path, "/projects/"); i >= 0 {
			name = u.Path[i+1:]
		} else if host == "storage.googleapis.com" {
			name = text(record.Data["name"])
		} else {
			return "", fmt.Errorf("GCP resource URL has no project")
		}
	}
	if strings.HasPrefix(name, "projects/") {
		return c.canonicalName("//" + host + "/" + name), nil
	}
	if !segmentPattern.MatchString(name) || name == "." || name == ".." {
		return "", fmt.Errorf("invalid GCP resource name")
	}
	if host == "storage.googleapis.com" {
		return "//" + host + "/" + name, nil
	}
	bound, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return "", err
	}
	u, _ := url.Parse(bound.URL)
	path := u.Path
	if strings.Contains(path, "/aggregated/") {
		if !strings.HasPrefix(record.Location, "zones/") && !strings.HasPrefix(record.Location, "regions/") && record.Location != "global" {
			return "", fmt.Errorf("GCP aggregate resource has no valid scope")
		}
		path = strings.Replace(path, "/aggregated/", "/"+record.Location+"/", 1)
	}
	if strings.Contains(path, "/locations/-/") {
		location := last(text(record.Data["location"]))
		if location == "" {
			location = last(text(record.Data["zone"]))
		}
		if location == "" {
			return "", fmt.Errorf("GCP wildcard list resource has no location")
		}
		path = strings.Replace(path, "/locations/-/", "/locations/"+location+"/", 1)
	}
	index := strings.Index(path, "/projects/")
	if index < 0 {
		return "", fmt.Errorf("GCP list URL has no project path")
	}
	return c.canonicalName("//" + host + path[index:] + "/" + name), nil
}
func (c *client) otherProductKind(kind resourceType, id string) bool {
	for _, other := range allTypes() {
		if other.NativeType == kind.NativeType || other.Collection != kind.Collection {
			continue
		}
		if _, err := c.resourceURL(other, id); err == nil {
			return true
		}
	}
	return false
}
