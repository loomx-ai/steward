package azure

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
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

type productCursor struct {
	Fingerprint string   `json:"fingerprint"`
	Target      int      `json:"target"`
	Next        string   `json:"next,omitempty"`
	Seen        []string `json:"seen,omitempty"`
}
type productTarget struct {
	MonitoredResource string `json:"monitored_resource,omitempty"`
	Endpoint          string `json:"endpoint"`
	ParentID          string `json:"parent_id,omitempty"`
	ParentType        string `json:"parent_type,omitempty"`
	Generation        string `json:"generation,omitempty"`
	Location          string `json:"location,omitempty"`
}

func (r *Runtime) productDefinition(nativeType string) (spec.ResourceKindSpec, bool) {
	for _, compiled := range r.bundle.Specs {
		if strings.EqualFold(compiled.ResourceKind.NativeType, nativeType) {
			return compiled.Definition, compiled.Definition.Discovery.Source == productInventorySource
		}
	}
	return spec.ResourceKindSpec{}, false
}
func (r *Runtime) usesProductSource(nativeType string) bool {
	_, ok := r.productDefinition(nativeType)
	return ok
}

// Product lists are authoritative per kind. A cursor is bound to the entire
// ordered parent set so a changed parent cannot silently skip a child shard.
func (r *Runtime) listProduct(ctx context.Context, c *client, request contracts.InventoryRequest, ancestors []string) (contracts.InventoryBatch, error) {
	if request.ResourceKind == nil {
		return contracts.InventoryBatch{}, fmt.Errorf("Azure product inventory requires a resource kind")
	}
	nativeType := request.ResourceKind.NativeType
	definition, ok := r.productDefinition(nativeType)
	if !ok || definition.Discovery.List == nil {
		return contracts.InventoryBatch{}, fmt.Errorf("Azure resource %q has no product discovery rule", nativeType)
	}
	if slices.Contains(ancestors, strings.ToLower(nativeType)) {
		return contracts.InventoryBatch{}, fmt.Errorf("Azure product parent cycle")
	}
	ancestors = append(slices.Clone(ancestors), strings.ToLower(nativeType))
	switch request.Scope.Kind {
	case asset.ScopeSubscription:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription) {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure inventory belongs to another subscription")
		}
	case asset.ScopeRegion:
		if text(request.Scope.NativeID) == "" {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure region is required")
		}
	case asset.ScopeGlobal:
		if !strings.EqualFold(request.Scope.NativeID, c.subscription+"/global") && request.Scope.NativeID != "global" {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure global scope belongs to another subscription")
		}
	default:
		return contracts.InventoryBatch{}, fmt.Errorf("unsupported Azure product scope")
	}
	targets, err := r.productTargets(ctx, c, request, definition, ancestors)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	bound, _ := json.Marshal(struct {
		Connection                                             asset.ConnectionID
		Subscription, ScopeKind, ScopeID, NativeType, Revision string
		Network                                                *asset.ScanTarget
		Targets                                                []productTarget
	}{request.ConnectionID, c.subscription, string(request.Scope.Kind), request.Scope.NativeID, nativeType, r.bundle.Revision, request.NetworkTarget, targets})
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(bound))
	cursor := productCursor{Fingerprint: fingerprint}
	if request.Cursor != "" {
		if len(request.Cursor) > 128*1024 {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure product cursor exceeds size limit")
		}
		decoded, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Fingerprint != fingerprint || cursor.Target < 0 || cursor.Target >= len(targets) {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure product cursor does not match the scan or its current parents")
		}
	}
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{}, Complete: true}
	if len(targets) == 0 {
		return batch, nil
	}
	target := targets[cursor.Target]
	u, _ := url.Parse(target.Endpoint)
	endpoint := target.Endpoint
	if cursor.Next != "" {
		nextURL, err := url.Parse(cursor.Next)
		if err != nil || nextURL.Query().Get("api-version") != u.Query().Get("api-version") {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure product cursor changed API version")
		}
		endpoint = cursor.Next
	}
	values, next, provenance, err := c.listPageResult(ctx, endpoint, u.Path)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	// Reading a collection which disappeared with its parent cannot prove that
	// all children are absent. In particular, a 403/404 is never an empty shard.
	if err := c.verifyProductParent(ctx, target); err != nil {
		return contracts.InventoryBatch{}, err
	}
	owners, locks, err := c.inventoryProtection(ctx)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	batch.RequestID = provenance.requestID
	kind, _ := findType(nativeType)
	seen := map[string]bool{}
	for _, value := range values {
		raw := object(value)
		id, parsedType, err := parseID(responseID(kind.NativeType, text(raw["id"])))
		if err != nil || !strings.EqualFold(parsedType, kind.NativeType) || !validResponseType(kind.NativeType, text(raw["type"])) || seen[id] {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure product list returned an invalid or duplicate identity")
		}
		seen[id] = true
		if kind.NativeType == dataCollectionAssociationType {
			if err := dataCollectionTargetMembership(raw, target); err != nil {
				return contracts.InventoryBatch{}, err
			}
		} else if target.ParentID != "" && !strings.EqualFold(id, u.Path+"/"+last(id)) {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure product child belongs to another parent")
		}
		readURL, err := c.resourceURL(kind, id)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		// ARM list responses can omit lifecycle fields. Enrich from the native
		// detail API before declaring the resource actionable.
		detail, err := c.request(ctx, "GET", readURL)
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		if !validResourceResponse(detail, id, kind.NativeType) {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure product detail identity mismatch")
		}
		data := detail.data
		if kind.NativeType == dataCollectionAssociationType {
			if err := dataCollectionTargetMembership(data, target); err != nil {
				return contracts.InventoryBatch{}, err
			}
			if err := serviceListedIncarnation(raw, data); err != nil {
				return contracts.InventoryBatch{}, err
			}
			if !dataCollectionSameReferences(raw, data) {
				return contracts.InventoryBatch{}, serviceDenied("data_collection_association_changed")
			}
			// Prefer a live rule, then a live endpoint, then the monitored resource
			// for orphan discovery. A deleted target cannot hide its surviving link.
			canonical, err := c.dataCollectionCanonicalTarget(ctx, data, targets)
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			if canonical.ParentID != "" && canonical.ParentID != target.ParentID {
				indexed, err := c.dataCollectionAssociations(ctx, asset.Identity{NativeID: canonical.ParentID, NativeType: canonical.ParentType})
				if err != nil {
					return contracts.InventoryBatch{}, err
				}
				if !slices.ContainsFunc(indexed, func(child serviceChild) bool {
					return child.id == id && productGeneration(child.data) == productGeneration(data)
				}) {
					return contracts.InventoryBatch{}, serviceDenied("data_collection_reverse_indexes_disagree")
				}
				continue
			}
		}
		data["id"] = id
		data["type"] = kind.NativeType
		if text(data["name"]) == "" {
			data["name"] = last(id)
		}
		if text(data["location"]) == "" {
			if text(raw["location"]) != "" {
				data["location"] = raw["location"]
			} else if target.Location != "" {
				data["location"] = target.Location
			}
		}
		item, err := r.inventoryItem(ctx, c, data, owners, locks)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		if !productScopeMatches(request, item) {
			continue
		}
		item.Normalized["_inventory_source"] = productInventorySource
		if target.ParentID != "" {
			key := referenceKey(target.ParentType)
			references, _ := item.Normalized[key].([]string)
			references = append(references, target.ParentID)
			sort.Strings(references)
			item.Normalized[key] = slices.Compact(references)
			if !slices.Contains(item.NetworkReferences, target.ParentID) {
				item.NetworkReferences = append(item.NetworkReferences, target.ParentID)
			}
			sort.Strings(item.NetworkReferences)
		}
		batch.Items = append(batch.Items, item)
	}
	if next != "" {
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(next)))
		if slices.Contains(cursor.Seen, hash) {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure product pagination repeated a page")
		}
		cursor.Seen = append(cursor.Seen, hash)
		cursor.Next = next
	} else {
		cursor.Target++
		cursor.Next = ""
		cursor.Seen = nil
	}
	if cursor.Target < len(targets) {
		payload, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(payload)
		if len(batch.NextCursor) > 128*1024 {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure product cursor exceeds size limit")
		}
		batch.Complete = false
	}
	return batch, nil
}

func productScopeMatches(request contracts.InventoryRequest, item contracts.InventoryItem) bool {
	return request.Scope.Kind == asset.ScopeSubscription ||
		(request.Scope.Kind == asset.ScopeGlobal && item.Location == "global") ||
		(request.Scope.Kind == asset.ScopeRegion && (strings.EqualFold(request.Scope.NativeID, item.Location) || (request.NetworkTarget != nil && item.Location == "global")))
}

func productGeneration(raw map[string]any) string {
	properties := object(raw["properties"])
	values := []any{object(raw["systemData"])["createdAt"], properties["resourceGuid"], properties["resourceUid"], properties["uniqueId"], properties["vmId"], properties["creationTime"], properties["timeCreated"], properties["creationDate"], properties["databaseId"]}
	// Not every ARM provider exposes a creation identifier. Keep its etag as a
	// conservative change detector where available.
	values = append(values, raw["etag"])
	extra := map[string]any{}
	for _, field := range []string{"hostId", "createdAt", "createdAtUtc", "eTag"} {
		if value := properties[field]; value != nil {
			extra[field] = value
		}
	}
	if len(extra) != 0 {
		values = append(values, extra)
	}
	if kind := grafanaKind(raw); kind != "" {
		values = append(values, grafanaConfiguration(kind, raw))
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && isDataCollectionType(kind) {
		values = append(values, dataCollectionConfiguration(kind, raw), properties["immutableId"])
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && strings.EqualFold(kind, monitorWorkspaceType) {
		values = append(values, monitorWorkspaceConfiguration(raw))
	}
	encoded, _ := json.Marshal(values)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

// Native creation fields survive ordinary configuration and attachment edits.
// Keep this identity check separate from the stricter service generation check.
func creationGeneration(raw map[string]any) string {
	values := map[string]any{}
	if value := object(raw["systemData"])["createdAt"]; value != nil {
		values["systemData.createdAt"] = value
	}
	for _, field := range []string{"resourceGuid", "resourceUid", "uniqueId", "vmId", "creationTime", "timeCreated", "creationDate", "databaseId", "hostId", "createdAt", "createdAtUtc", "immutableId", "accountId"} {
		if value := object(raw["properties"])[field]; value != nil {
			values[field] = value
		}
	}
	if len(values) == 0 {
		return ""
	}
	encoded, _ := json.Marshal(values)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

func serviceCreationIdentity(planned asset.Asset, live map[string]any) error {
	if expected := text(planned.Normalized["_arm_creation_generation"]); expected != "" && expected != creationGeneration(live) {
		return serviceDenied("service_resource_incarnation_changed")
	}
	return nil
}

var errProductParentGenerationChanged = errors.New("Azure product parent changed during child discovery")

func (c *client) verifyProductParent(ctx context.Context, target productTarget) error {
	if target.ParentID == "" {
		return nil
	}
	kind, _ := findType(target.ParentType)
	endpoint, err := c.resourceURL(kind, target.ParentID)
	if err != nil {
		return err
	}
	current, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return err
	}
	if !validResourceResponse(current, target.ParentID, target.ParentType) {
		return fmt.Errorf("Azure product parent identity mismatch during child discovery")
	}
	if productGeneration(current.data) != target.Generation {
		return errProductParentGenerationChanged
	}
	return nil
}

func (r *Runtime) productTargets(ctx context.Context, c *client, request contracts.InventoryRequest, definition spec.ResourceKindSpec, ancestors []string) ([]productTarget, error) {
	parents := []contracts.InventoryItem{{}}
	if parent := definition.Discovery.Parent; parent != nil {
		if parent.Source != productInventorySource {
			return nil, fmt.Errorf("Azure product parent requires an authoritative source")
		}
		parents = nil
		parentTypes := []string{parent.NativeType}
		if definition.Metadata.NativeType == dataCollectionAssociationType {
			parentTypes = append(parentTypes, dataCollectionEndpointType)
		}
		for _, parentType := range parentTypes {
			kind := r.resourceKind(parentType)
			parentRequest := request
			parentRequest.ResourceKind = &kind
			parentRequest.Cursor = ""
			// Child resources can have their own region, and resource groups are
			// globally scoped. Enumerate the native parent set across the subscription.
			parentRequest.Scope = asset.Scope{Kind: asset.ScopeSubscription, NativeID: c.subscription}
			for {
				batch, err := r.listProduct(ctx, c, parentRequest, ancestors)
				if err != nil {
					return nil, err
				}
				parents = append(parents, batch.Items...)
				if batch.Complete {
					break
				}
				parentRequest.Cursor = batch.NextCursor
			}
		}
		sort.Slice(parents, func(i, j int) bool { return parents[i].NativeID < parents[j].NativeID })
		for i := 1; i < len(parents); i++ {
			if parents[i-1].NativeID == parents[i].NativeID {
				return nil, fmt.Errorf("Azure parent list returned duplicate identities")
			}
		}
	}
	api := definition.Discovery.List
	targets := []productTarget{}
	for _, parent := range parents {
		if definition.Metadata.NativeType == scaleSetVMType {
			mode, err := scaleSetMode(parent.Normalized)
			if err != nil {
				return nil, err
			}
			if mode == "Flexible" {
				continue
			}
		}
		var bound catalog.RESTRequest
		var err error
		if definition.Metadata.NativeType == dataCollectionAssociationType {
			bound, err = c.dataCollectionAssociationList(parent.NativeID, parent.NativeType)
		} else {
			bound, err = c.bindProductList(api, request.Scope.NativeID, parent)
		}
		if err != nil {
			return nil, err
		}
		target := productTarget{Endpoint: bound.URL}
		if parent.NativeID != "" {
			target.ParentID, target.ParentType, target.Location = parent.NativeID, parent.NativeType, parent.Location
			target.Generation = productGeneration(parent.Raw)
		}
		targets = append(targets, target)
	}
	if definition.Metadata.NativeType == dataCollectionAssociationType {
		return c.dataCollectionOrphanTargets(ctx, targets)
	}
	return targets, nil
}

func (c *client) inventoryProtection(ctx context.Context) (map[string]string, []any, error) {
	groups, err := c.listAll(ctx, c.root()+"/resourcegroups", resourcesVersion)
	if err != nil {
		return nil, nil, err
	}
	owners := map[string]string{}
	for _, value := range groups {
		raw := object(value)
		id, kind, err := parseID(text(raw["id"]))
		if err != nil || !strings.HasPrefix(id, c.root()+"/") || !strings.EqualFold(kind, groupType) {
			return nil, nil, fmt.Errorf("invalid Azure resource group identity")
		}
		if _, duplicate := owners[id]; duplicate {
			return nil, nil, fmt.Errorf("duplicate Azure resource group identity")
		}
		owners[id] = text(raw["managedBy"])
	}
	locks, err := c.managementLocks(ctx)
	return owners, locks, err
}

func (c *client) bindProductList(api *spec.ProductAPISpec, location string, parent contracts.InventoryItem) (catalog.RESTRequest, error) {
	metadata, err := providerData()
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	operation, ok := metadata.catalog.Operation(api.Operation)
	if !ok || operation.Call.Method != "GET" || api.ItemsPath != "value" || api.IdentityPath != "id" {
		return catalog.RESTRequest{}, fmt.Errorf("invalid Azure native list rule")
	}
	parameters := map[string]any{}
	for key, value := range api.Parameters {
		switch value {
		case "scope.subscription":
			parameters[key] = c.subscription
		case "scope.location":
			parameters[key] = location
		case "parent.nativeId":
			parameters[key] = parent.NativeID
		default:
			if expression, ok := value.(string); ok && strings.HasPrefix(expression, "parent.normalized.") {
				var resolved any = parent.Normalized
				for _, part := range strings.Split(strings.TrimPrefix(expression, "parent.normalized."), ".") {
					resolved = object(resolved)[part]
				}
				parameters[key] = resolved
			} else {
				parameters[key] = value
			}
		}
	}
	bound, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	return bound, nil
}
