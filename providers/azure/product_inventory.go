package azure

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

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
	// Walk names this process's record of the identities the target's earlier
	// pages returned; see productScanCache.commit.
	Walk string `json:"walk,omitempty"`
	// Resources is no longer written. Snapshot sources sharing this cursor
	// shape still reject a cursor that carries it.
	Resources []string `json:"resources,omitempty"`
}
type productTarget struct {
	SynapsePrivateConfiguration         string         `json:"synapse_private_configuration,omitempty"`
	DomainPrivateConfiguration          string         `json:"domain_private_configuration,omitempty"`
	APIMPrivateConfiguration            string         `json:"apim_private_configuration,omitempty"`
	BatchPrivateConfiguration           string         `json:"batch_private_configuration,omitempty"`
	StreamAnalyticsPrivateConfiguration string         `json:"stream_analytics_private_configuration,omitempty"`
	KustoAncestors                      map[string]any `json:"kusto_ancestors,omitempty"`
	KustoPrivateConfiguration           string         `json:"kusto_private_configuration,omitempty"`
	MongoClusterPrivateConfiguration    string         `json:"mongocluster_private_configuration,omitempty"`
	CosmosAncestors                     map[string]any `json:"cosmos_ancestors,omitempty"`
	CosmosThroughput                    string         `json:"cosmos_throughput,omitempty"`
	ParentWireID                        string         `json:"parent_wire_id,omitempty"`
	CognitiveAncestors                  map[string]any `json:"cognitive_ancestors,omitempty"`
	CognitiveNativeLocation             string         `json:"cognitive_native_location,omitempty"`
	RedisRootConfiguration              string         `json:"redis_root_configuration,omitempty"`
	AppServiceRootConfiguration         string         `json:"app_service_root_configuration,omitempty"`
	CDNProfileConfiguration             string         `json:"cdn_profile_configuration,omitempty"`
	MonitoredResource                   string         `json:"monitored_resource,omitempty"`
	Endpoint                            string         `json:"endpoint"`
	ParentID                            string         `json:"parent_id,omitempty"`
	ParentType                          string         `json:"parent_type,omitempty"`
	Generation                          string         `json:"generation,omitempty"`
	Location                            string         `json:"location,omitempty"`
}

func (r *Runtime) productDefinition(nativeType string) (spec.ResourceKindSpec, bool) {
	if compiled, ok := r.specIndex(nativeType); ok {
		return compiled.Definition, compiled.Definition.Discovery.Source == insightsInventorySource(nativeType)
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
	// Without a scan, a shard's first page lists parents afresh and its later
	// pages reuse that set. The shards of one scan share it.
	targetKey := productTargetKey{run: request.ScanRunID, connection: request.ConnectionID, credential: c.fingerprint, nativeType: strings.ToLower(nativeType), scopeKind: request.Scope.Kind, scopeID: request.Scope.NativeID}
	targets, targetsDigest, err := r.targetCache.get(targetKey, request.Cursor == "" && request.ScanRunID == "", func() ([]productTarget, error) {
		return r.productTargets(ctx, c, request, definition, ancestors)
	})
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	bound, _ := json.Marshal(struct {
		Connection                                             asset.ConnectionID
		Subscription, ScopeKind, ScopeID, NativeType, Revision string
		Network                                                *asset.ScanTarget
		Targets                                                string
	}{request.ConnectionID, c.subscription, string(request.Scope.Kind), request.Scope.NativeID, nativeType, r.bundle.Revision, request.NetworkTarget, targetsDigest})
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
	// Every region shard lists the same subscription-wide parents. Once one
	// shard of this scan walked a target to its end, skip the targets none of
	// whose children landed in this shard's scope.
	placementKey := func(target productTarget) productScanKey {
		encoded, _ := json.Marshal(target)
		return productScanKey{run: request.ScanRunID, connection: request.ConnectionID, credential: c.fingerprint, name: strings.ToLower(nativeType) + "\x00" + fmt.Sprintf("%x", sha256.Sum256(encoded))}
	}
	var placed map[string]string
	for request.ScanRunID != "" && cursor.Next == "" && cursor.Target < len(targets) {
		var ok bool
		if placed, ok = r.productScan.placement(placementKey(targets[cursor.Target])); !ok || !productPlacedElsewhere(request, placed) {
			break
		}
		cursor.Target++
	}
	if cursor.Target >= len(targets) {
		return batch, nil
	}
	target := targets[cursor.Target]
	if request.ScanRunID != "" && cursor.Next != "" {
		placed, _ = r.productScan.placement(placementKey(target))
	}
	u, _ := url.Parse(target.Endpoint)
	endpoint := target.Endpoint
	if cursor.Next != "" {
		nextURL, err := url.Parse(cursor.Next)
		if err != nil || nextURL.Query().Get("api-version") != u.Query().Get("api-version") {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure product cursor changed API version")
		}
		endpoint = cursor.Next
	}
	var values []any
	var next string
	var provenance response
	if synapseKind(nativeType) != "" {
		values, next, provenance, err = c.synapsePage(ctx, endpoint, u.Path, nativeType)
	} else if isAPIMAPI(nativeType) {
		values, next, provenance, err = c.apimAPIPage(ctx, target.ParentID)
	} else if nativeType == apimIssueType {
		values, next, provenance, err = c.apimIssuePage(ctx, target.ParentID)
	} else if nativeType == streamAnalyticsTransformationType {
		values, next, provenance, err = c.streamAnalyticsTransformationPage(ctx, endpoint, target.ParentID)
	} else if nativeType == batchPoolType {
		// Batch data-plane auto pools must agree with the complete ARM index.
		// Return that checked collection as one client shard.
		var account batchAccountContext
		account, err = c.batchAccount(ctx, target.ParentID)
		if err == nil {
			var pools []serviceChild
			pools, provenance.requestID, err = c.batchPools(ctx, account)
			for _, pool := range pools {
				values = append(values, pool.data)
			}
		}
	} else {
		values, next, provenance, err = c.listPageResult(ctx, endpoint, u.Path)
	}
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	// Reading a collection which disappeared with its parent cannot prove that
	// all children are absent. In particular, a 403/404 is never an empty shard.
	if err := c.verifyProductParent(ctx, target); err != nil {
		// The scan's shared parent listing may be what went stale; a retried
		// shard lists parents afresh.
		r.targetCache.forget(targetKey)
		r.productScan.forgetParents(request.ScanRunID, request.ConnectionID)
		return contracts.InventoryBatch{}, err
	}
	owners, locks, err := c.inventoryProtection(ctx)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	batch.RequestID = provenance.requestID
	kind, _ := findType(nativeType)
	// seen maps every identity this page returned to the region its item
	// landed in, or "" for rows which are no item of this kind.
	seen := map[string]string{}
	type productRow struct {
		raw        map[string]any
		wireID, id string
		readURL    string
	}
	rows := []productRow{}
	for _, value := range values {
		raw := object(value)
		if isAPIMAssociation(nativeType) {
			raw, err = apimAssociationRow(target.ParentID, nativeType, raw)
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
		wireID := responseID(kind.NativeType, text(raw["id"]))
		id, parsedType, err := parseID(wireID)
		if _, duplicate := seen[id]; err != nil || !strings.EqualFold(parsedType, kind.NativeType) || !validResponseType(kind.NativeType, text(raw["type"])) || duplicate {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure product list returned an invalid or duplicate identity")
		}
		seen[id] = ""
		if isCosmosType(kind.NativeType) {
			if target.ParentID != "" && !cosmosSameWireID(cosmosParentID(wireID), target.ParentWireID) {
				return contracts.InventoryBatch{}, fmt.Errorf("Cosmos DB child changed its parent name")
			}
		}
		if kind.NativeType == dataCollectionAssociationType {
			if err := dataCollectionTargetMembership(raw, target); err != nil {
				return contracts.InventoryBatch{}, err
			}
		} else if nativeType == streamAnalyticsTransformationType {
			if !strings.EqualFold(redisParentID(id), target.ParentID) {
				return contracts.InventoryBatch{}, fmt.Errorf("Stream Analytics transformation belongs to another job")
			}
		} else if target.ParentID != "" && !strings.EqualFold(id, u.Path+"/"+last(id)) {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure product child belongs to another parent")
		}
		// Every workspace lists hundreds of built-in Azure Monitor tables. They
		// are part of the workspace, not resources a user creates or deletes.
		if kind.NativeType == logAnalyticsTableType && logAnalyticsTableCreator(raw) == "Microsoft" {
			continue
		}
		// Subscription-wide lists serve every region shard. Skip rows the listed
		// location, or an earlier shard's detail read in this scan, already
		// places in another shard before their detail read.
		if region, ok := productListedRegion(kind.NativeType, raw); ok && !productScopeMatches(request, contracts.InventoryItem{Location: region}) {
			seen[id] = region
			continue
		}
		if region := placed[id]; region != "" && !productScopeMatches(request, contracts.InventoryItem{Location: region}) {
			seen[id] = region
			continue
		}
		readURL, err := c.resourceURL(kind, wireID)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		rows = append(rows, productRow{raw: raw, wireID: wireID, id: id, readURL: readURL})
	}
	// ARM list responses can omit lifecycle fields. Enrich from the native
	// detail API before declaring the resource actionable.
	details, readErrs := readConcurrently(len(rows), func(i int) (response, error) {
		return c.readResource(ctx, rows[i].readURL)
	})
	for i, row := range rows {
		raw, wireID, id := row.raw, row.wireID, row.id
		detail, err := details[i], readErrs[i]
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
		if synapseKind(kind.NativeType) != "" {
			if err := c.synapseReadResponse(detail, id, kind.NativeType); err != nil {
				return contracts.InventoryBatch{}, err
			}
			if !nativeConfigurationContains(synapseSnapshot(raw), synapseSnapshot(data)) {
				return contracts.InventoryBatch{}, serviceDenied("synapse_listed_configuration_changed")
			}
		}
		if isDomainType(kind.NativeType) && (!insightsARMReadValid(detail, id, kind.NativeType) || !nativeConfigurationContains(domainSnapshot(kind.NativeType, raw), domainSnapshot(kind.NativeType, data))) {
			return contracts.InventoryBatch{}, serviceDenied("domain_listed_configuration_changed")
		}
		if err := monitorPrivateLinkListed(kind.NativeType, raw, data); err != nil {
			return contracts.InventoryBatch{}, err
		}
		if isAPIMType(kind.NativeType) {
			if err := apimListedIncarnation(kind.NativeType, raw, data); err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
		if isBatchType(kind.NativeType) {
			if !nativeConfigurationContains(batchSnapshot(kind.NativeType, raw), batchSnapshot(kind.NativeType, data)) {
				return contracts.InventoryBatch{}, serviceDenied("batch_listed_configuration_changed")
			}
		}
		if isStreamAnalyticsType(kind.NativeType) {
			if err := streamAnalyticsListedIncarnation(kind.NativeType, raw, data); err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
		if isCosmosType(kind.NativeType) {
			if err := cosmosListedIncarnation(kind.NativeType, raw, data); err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
		if isCosmosType(kind.NativeType) && !cosmosSameWireID(responseID(kind.NativeType, text(data["id"])), wireID) {
			return contracts.InventoryBatch{}, fmt.Errorf("Cosmos DB detail changed its resource name")
		}
		cognitiveLocation := cognitiveNativeLocation(data)
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
		if isCosmosType(kind.NativeType) {
			data["id"] = wireID
		}
		data["type"] = kind.NativeType
		if text(data["name"]) == "" {
			data["name"] = last(id)
		}
		if text(data["location"]) == "" && !isCosmosType(kind.NativeType) {
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
		if isCognitiveType(kind.NativeType) {
			item.Normalized["_cognitive_native_location"] = cognitiveLocation
		}
		seen[id] = item.Location
		if !productScopeMatches(request, item) {
			continue
		}
		item.Normalized["_inventory_source"] = definition.Discovery.Source
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
	}
	if cursor.Next == "" {
		cursor.Walk = ""
		if next != "" {
			cursor.Walk = rand.Text()
		}
	}
	walked, complete, err := r.productScan.commit(cursor.Walk, cursor.Next == "", next == "", seen)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	if complete && request.ScanRunID != "" {
		r.productScan.place(placementKey(target), walked)
	}
	if next != "" {
		cursor.Next = next
	} else {
		cursor.Target++
		cursor.Next = ""
		cursor.Seen = nil
		cursor.Walk = ""
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

// productListedRegion is the region inventoryItem derives from a list row's
// location. Rows without a location, and kinds whose region comes from a parent
// or a native property, are only placed after their detail read.
func productListedRegion(nativeType string, raw map[string]any) (string, bool) {
	if text(raw["location"]) == "" || isCosmosType(nativeType) || isAPIMType(nativeType) || isBatchType(nativeType) || isStreamAnalyticsType(nativeType) || isKustoType(nativeType) ||
		fleetKind(nativeType).kind != "" || monitorResourceKind(nativeType) != "" {
		return "", false
	}
	return resourceRegion(map[string]any{"type": nativeType, "location": raw["location"]}), true
}

func productGeneration(raw map[string]any) string {
	if strings.EqualFold(text(raw["type"]), apimGatewayAlias) {
		return apimConfiguration(apimGatewayType, raw)
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && isAPIMType(kind) {
		return apimConfiguration(kind, raw)
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && isBatchType(kind) {
		return batchConfiguration(batchKind(kind), raw)
	}
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
	if _, kind, err := parseID(text(raw["id"])); err == nil && monitorPrivateLinkKind(kind) != "" {
		values = append(values, monitorPrivateLinkConfiguration(kind, raw))
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
	if _, kind, err := parseID(text(raw["id"])); err == nil && strings.EqualFold(kind, containerGroupType) {
		values = append(values, containerGroupConfiguration(raw))
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && isCDNType(kind) {
		values = append(values, cdnConfiguration(kind, raw))
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && isStreamAnalyticsType(kind) {
		return streamAnalyticsConfiguration(kind, raw)
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && isKustoType(kind) {
		values = append(values, kustoConfiguration(kind, raw))
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && isMongoClusterType(kind) {
		values = append(values, mongoClusterConfiguration(kind, raw))
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && isCosmosType(kind) {
		values = append(values, cosmosConfiguration(kind, raw), cosmosCreation(raw))
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && isCognitiveType(kind) {
		values = append(values, cognitiveConfiguration(kind, raw))
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && isSearchType(kind) {
		values = append(values, searchConfiguration(kind, raw))
	}
	if _, kind, err := parseID(text(raw["id"])); err == nil && isRedisType(kind) {
		values = append(values, redisConfiguration(kind, raw))
	}
	encoded, _ := json.Marshal(values)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

// Native creation fields survive ordinary configuration and attachment edits.
// Keep this identity check separate from the stricter service generation check.
func creationGeneration(raw map[string]any) string {
	if _, kind, err := parseID(text(raw["id"])); err == nil && isCosmosType(kind) {
		return cosmosCreation(raw)
	}
	values := map[string]any{}
	if value := object(raw["systemData"])["createdAt"]; value != nil {
		values["systemData.createdAt"] = value
	}
	for _, field := range []string{"resourceGuid", "resourceUid", "uniqueId", "vmId", "creationTime", "timeCreated", "creationDate", "databaseId", "hostId", "createdAt", "createdAtUtc", "immutableId", "accountId", "internalId", "dateCreated", "deploymentId", "commitmentPlanGuid", "topicId"} {
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
	parentID := target.ParentID
	if isCosmosType(target.ParentType) {
		id, typ, err := parseID(target.ParentWireID)
		if err != nil || !strings.EqualFold(id, target.ParentID) || !strings.EqualFold(typ, target.ParentType) {
			return fmt.Errorf("Cosmos DB parent is missing its native identity")
		}
		parentID = target.ParentWireID
	}
	endpoint, err := c.resourceURL(kind, parentID)
	if err != nil {
		return err
	}
	current, err := c.readResource(ctx, endpoint)
	if err != nil {
		return err
	}
	if !validResourceResponse(current, target.ParentID, target.ParentType) {
		return fmt.Errorf("Azure product parent identity mismatch during child discovery")
	}
	if ctx.Value(domainReadContextKey{}) == true && !insightsARMReadValid(current, target.ParentID, target.ParentType) {
		return serviceDenied("invalid_domain_dependency_parent_response")
	}
	if isCosmosType(target.ParentType) && !cosmosSameWireID(responseID(target.ParentType, text(current.data["id"])), parentID) {
		return fmt.Errorf("Cosmos DB parent changed its resource name")
	}
	if err := c.verifyCosmosProductParent(ctx, target, current.data); err != nil {
		return err
	}
	if target.SynapsePrivateConfiguration != "" {
		if err := c.synapseReadResponse(current, target.ParentID, target.ParentType); err != nil {
			return err
		}
		if target.SynapsePrivateConfiguration != c.privateConfiguration(synapseSnapshot(current.data)) {
			return errProductParentGenerationChanged
		}
	}
	if target.APIMPrivateConfiguration != "" && target.APIMPrivateConfiguration != c.privateConfiguration(apimSnapshot(target.ParentType, current.data)) {
		return errProductParentGenerationChanged
	}
	if target.DomainPrivateConfiguration != "" && (!insightsARMReadValid(current, target.ParentID, target.ParentType) || target.DomainPrivateConfiguration != c.privateConfiguration(domainSnapshot(target.ParentType, current.data))) {
		return errProductParentGenerationChanged
	}
	if target.StreamAnalyticsPrivateConfiguration != "" && target.StreamAnalyticsPrivateConfiguration != c.privateConfiguration(streamAnalyticsSnapshot(target.ParentType, current.data)) {
		return errProductParentGenerationChanged
	}
	if target.BatchPrivateConfiguration != "" && target.BatchPrivateConfiguration != c.privateConfiguration(batchSnapshot(target.ParentType, current.data)) {
		return errProductParentGenerationChanged
	}
	if target.KustoPrivateConfiguration != "" && target.KustoPrivateConfiguration != c.privateConfiguration(kustoSnapshot(target.ParentType, current.data)) {
		return errProductParentGenerationChanged
	}
	if target.KustoAncestors != nil {
		if err := c.kustoAncestors(ctx, target.ParentID, target.KustoAncestors, false); err != nil {
			return err
		}
	}
	if target.MongoClusterPrivateConfiguration != "" && target.MongoClusterPrivateConfiguration != c.privateConfiguration(mongoClusterSnapshot(target.ParentType, current.data)) {
		return errProductParentGenerationChanged
	}
	if target.CognitiveAncestors != nil {
		if target.CognitiveNativeLocation != cognitiveNativeLocation(current.data) {
			return errProductParentGenerationChanged
		}
		if err := c.cognitiveAncestors(ctx, target.ParentID, target.CognitiveAncestors, false); err != nil {
			return err
		}
	}
	if target.RedisRootConfiguration != "" {
		root, err := c.redisResource(ctx, redisRootID(target.ParentID))
		if err != nil {
			return err
		}
		if redisConfiguration(redisEnterpriseType, root) != target.RedisRootConfiguration {
			return errProductParentGenerationChanged
		}
	}
	if target.AppServiceRootConfiguration != "" {
		root, err := c.appServiceParent(ctx, target.ParentID)
		if err != nil {
			return err
		}
		if appServiceParentConfiguration(appSiteType, root) != target.AppServiceRootConfiguration {
			return errProductParentGenerationChanged
		}
	}
	if target.CDNProfileConfiguration != "" {
		profile, err := c.cdnProfile(ctx, target.ParentID)
		if err != nil {
			return err
		}
		if cdnConfiguration(cdnProfileType, profile) != target.CDNProfileConfiguration {
			return errProductParentGenerationChanged
		}
	}
	if productGeneration(current.data) != target.Generation {
		return errProductParentGenerationChanged
	}
	return nil
}

// productTargetTTL bounds how long a child shard's later pages reuse the parent
// targets its first page listed; without it every page relists every parent.
// The cursor fingerprint still rejects resuming against a different parent set
// once the targets are listed again.
const productTargetTTL = 10 * time.Minute

type productTargetKey struct {
	run        asset.ScanRunID
	connection asset.ConnectionID
	credential [32]byte
	nativeType string
	scopeKind  asset.ScopeKind
	scopeID    string
}

type productTargetCache struct {
	mu      sync.Mutex
	entries map[productTargetKey]cachedProductTargets
}

type cachedProductTargets struct {
	targets []productTarget
	// digest binds cursors to the target set without re-encoding it per page.
	digest  string
	expires time.Time
}

func (c *productTargetCache) get(key productTargetKey, refresh bool, list func() ([]productTarget, error)) ([]productTarget, string, error) {
	now := time.Now()
	c.mu.Lock()
	entry, ok := c.entries[key]
	c.mu.Unlock()
	if ok && !refresh && now.Before(entry.expires) {
		return entry.targets, entry.digest, nil
	}
	targets, err := list()
	if err != nil {
		return nil, "", err
	}
	encoded, _ := json.Marshal(targets)
	entry = cachedProductTargets{targets: targets, digest: fmt.Sprintf("%x", sha256.Sum256(encoded)), expires: now.Add(productTargetTTL)}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.entries == nil {
		c.entries = map[productTargetKey]cachedProductTargets{}
	}
	for cached, entry := range c.entries {
		if !now.Before(entry.expires) {
			delete(c.entries, cached)
		}
	}
	c.entries[key] = entry
	return entry.targets, entry.digest, nil
}

func (c *productTargetCache) forget(key productTargetKey) {
	c.mu.Lock()
	delete(c.entries, key)
	c.mu.Unlock()
}

// productScanKey names state the shards of one scan share. Nothing is shared
// without a scan.
type productScanKey struct {
	run        asset.ScanRunID
	connection asset.ConnectionID
	credential [32]byte
	name       string
}

// productScanCache holds, for at most productTargetTTL after their last use:
//   - parent listings, so child kinds and region shards list each parent kind
//     once per scan; concurrent shards wait for one listing, and a failed
//     listing is dropped rather than read as empty;
//   - placements, the region every child of a completely walked target landed
//     in, so other region shards skip targets and detail reads that cannot
//     yield an item in their scope;
//   - walks, the identities a shard's earlier pages of a target returned, to
//     reject a duplicate across pages without growing the cursor.
type productScanCache struct {
	mu         sync.Mutex
	parents    map[productScanKey]*productParentListing
	placements map[productScanKey]*productPlacement
	walks      map[string]*productWalk
}

type productParentListing struct {
	done    chan struct{}
	items   []contracts.InventoryItem
	err     error
	expires time.Time
}

type productPlacement struct {
	locations map[string]string
	expires   time.Time
}

type productWalk struct {
	ids     map[string]string
	partial bool
	expires time.Time
}

func (s *productScanCache) parentItems(ctx context.Context, key productScanKey, list func() ([]contracts.InventoryItem, error)) ([]contracts.InventoryItem, error) {
	for {
		s.mu.Lock()
		now := time.Now()
		entry := s.parents[key]
		if entry == nil || !entry.expires.IsZero() && !now.Before(entry.expires) {
			if s.parents == nil {
				s.parents = map[productScanKey]*productParentListing{}
			}
			for cached, old := range s.parents {
				if !old.expires.IsZero() && !now.Before(old.expires) {
					delete(s.parents, cached)
				}
			}
			entry = &productParentListing{done: make(chan struct{})}
			s.parents[key] = entry
			s.mu.Unlock()
			func() {
				defer func() {
					s.mu.Lock()
					if entry.err != nil {
						if s.parents[key] == entry {
							delete(s.parents, key)
						}
					} else {
						entry.expires = time.Now().Add(productTargetTTL)
					}
					s.mu.Unlock()
					close(entry.done)
				}()
				entry.err = errors.New("Azure product parent listing did not finish")
				entry.items, entry.err = list()
			}()
			return entry.items, entry.err
		}
		if !entry.expires.IsZero() {
			entry.expires = now.Add(productTargetTTL)
		}
		s.mu.Unlock()
		select {
		case <-entry.done:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		// Another shard's failure, perhaps its own cancellation, is not this
		// shard's: list again.
		if entry.err == nil {
			return entry.items, nil
		}
	}
}

func (s *productScanCache) forgetParents(run asset.ScanRunID, connection asset.ConnectionID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for key, entry := range s.parents {
		if key.run == run && key.connection == connection && !entry.expires.IsZero() {
			delete(s.parents, key)
		}
	}
}

func (s *productScanCache) placement(key productScanKey) (map[string]string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry := s.placements[key]
	if entry == nil || !time.Now().Before(entry.expires) {
		return nil, false
	}
	entry.expires = time.Now().Add(productTargetTTL)
	return entry.locations, true
}

func (s *productScanCache) place(key productScanKey, locations map[string]string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	if s.placements == nil {
		s.placements = map[productScanKey]*productPlacement{}
	}
	for cached, entry := range s.placements {
		if !now.Before(entry.expires) {
			delete(s.placements, cached)
		}
	}
	s.placements[key] = &productPlacement{locations: locations, expires: now.Add(productTargetTTL)}
}

// commit records one page of a target walk and rejects an identity an earlier
// page returned. It returns the whole walk once its last page is committed;
// complete is false when this process no longer held the earlier pages, which
// it then cannot check against.
func (s *productScanCache) commit(walk string, first, last bool, page map[string]string) (map[string]string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	current := &productWalk{ids: page, partial: !first}
	if existing := s.walks[walk]; !first && existing != nil && now.Before(existing.expires) {
		for id := range page {
			if _, duplicate := existing.ids[id]; duplicate {
				return nil, false, fmt.Errorf("Azure product list returned a duplicate resource across pages")
			}
		}
		maps.Copy(existing.ids, page)
		current = existing
	}
	if s.walks == nil {
		s.walks = map[string]*productWalk{}
	}
	for cached, entry := range s.walks {
		if !now.Before(entry.expires) {
			delete(s.walks, cached)
		}
	}
	if last || walk == "" {
		delete(s.walks, walk)
		return current.ids, last && !current.partial, nil
	}
	current.expires = now.Add(productTargetTTL)
	s.walks[walk] = current
	return nil, false, nil
}

// productPlacedElsewhere reports a completely walked target none of whose
// children landed in the request's scope.
func productPlacedElsewhere(request contracts.InventoryRequest, locations map[string]string) bool {
	for _, location := range locations {
		if location != "" && productScopeMatches(request, contracts.InventoryItem{Location: location}) {
			return false
		}
	}
	return true
}

// detailReadConcurrency bounds the detail reads one inventory page runs at once.
const detailReadConcurrency = 8

var errReadNotStarted = errors.New("Azure detail read was not started after an earlier read failed")

// readConcurrently runs read for every index, at most detailReadConcurrency at
// once, and returns the results in index order. After a read fails with
// anything but not-found it starts no further reads; those report
// errReadNotStarted, after the failure in index order.
func readConcurrently[T any](count int, read func(int) (T, error)) ([]T, []error) {
	results, errs := make([]T, count), make([]error, count)
	slots := make(chan struct{}, detailReadConcurrency)
	var wg sync.WaitGroup
	var failed atomic.Bool
	for i := range count {
		slots <- struct{}{}
		if failed.Load() {
			<-slots
			for j := i; j < count; j++ {
				errs[j] = errReadNotStarted
			}
			break
		}
		wg.Add(1)
		go func() {
			defer func() { <-slots; wg.Done() }()
			results[i], errs[i] = read(i)
			if errs[i] != nil && !isNotFound(errs[i]) {
				failed.Store(true)
			}
		}()
	}
	wg.Wait()
	return results, errs
}

func (r *Runtime) productTargets(ctx context.Context, c *client, request contracts.InventoryRequest, definition spec.ResourceKindSpec, ancestors []string) ([]productTarget, error) {
	parents := []contracts.InventoryItem{{}}
	if parent := definition.Discovery.Parent; parent != nil {
		if parent.Source != productInventorySource && !(parent.Source == synapseSource && synapseKind(parent.NativeType) == synapseType) {
			return nil, fmt.Errorf("Azure product parent requires an authoritative source")
		}
		parentTypes := []string{parent.NativeType}
		if definition.Metadata.NativeType == dataCollectionAssociationType {
			parentTypes = append(parentTypes, dataCollectionEndpointType)
		}
		for _, parentType := range parentTypes {
			// Checked before waiting on a shared listing, which a cycle would await.
			if slices.Contains(ancestors, strings.ToLower(parentType)) {
				return nil, fmt.Errorf("Azure product parent cycle")
			}
		}
		list := func() ([]contracts.InventoryItem, error) {
			var parents []contracts.InventoryItem
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
			return parents, nil
		}
		// The scan's shards share one parent listing, read only from here on.
		var err error
		if request.ScanRunID == "" {
			parents, err = list()
		} else {
			parents, err = r.productScan.parentItems(ctx, productScanKey{run: request.ScanRunID, connection: request.ConnectionID, credential: c.fingerprint, name: strings.ToLower(strings.Join(parentTypes, ","))}, list)
		}
		if err != nil {
			return nil, err
		}
	}
	api := definition.Discovery.List
	targets := []productTarget{}
	for _, parent := range parents {
		if parent.NativeType == cosmosType && isCosmosType(definition.Metadata.NativeType) {
			applies, err := cosmosChildApplies(definition.Metadata.NativeType, parent.Raw)
			if err != nil {
				return nil, err
			}
			if !applies {
				continue
			}
		}
		if definition.Metadata.NativeType == redisLinkType {
			premium, err := redisPremium(parent.Raw)
			if err != nil {
				return nil, err
			}
			if !premium {
				continue
			}
		}
		if isAppFunction(definition.Metadata.NativeType) {
			functionHost, err := appFunctionHost(parent.Raw)
			if err != nil {
				return nil, err
			}
			if !functionHost {
				continue
			}
		}
		if parent.NativeType == afdRuleSetType && definition.Metadata.NativeType == afdRuleType {
			batch, err := cdnBatchMode(parent.Raw)
			if err != nil {
				return nil, err
			}
			if batch {
				continue
			}
		}
		if parent.NativeType == cdnProfileType && isCDNType(definition.Metadata.NativeType) {
			applies, err := cdnChildApplies(definition.Metadata.NativeType, parent.Raw)
			if err != nil {
				return nil, err
			}
			if !applies {
				continue
			}
		}
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
			if synapseKind(parent.NativeType) != "" {
				target.SynapsePrivateConfiguration = text(parent.Normalized["_synapse_private_configuration"])
			}
			if isAPIMType(parent.NativeType) {
				target.APIMPrivateConfiguration = text(parent.Normalized["_apim_private_configuration"])
			}
			if parent.NativeType == domainType {
				target.DomainPrivateConfiguration = text(parent.Normalized[domainConfiguration])
				target.Generation = text(parent.Normalized["_arm_generation"])
			}
			if isCosmosType(parent.NativeType) {
				target.ParentWireID = text(parent.Normalized["_cosmos_wire_id"])
				target.CosmosAncestors = object(parent.Normalized["_cosmos_ancestors"])
				target.CosmosThroughput = text(parent.Normalized["_cosmos_throughput_binding"])
			}
			if isStreamAnalyticsType(parent.NativeType) {
				target.StreamAnalyticsPrivateConfiguration = text(parent.Normalized["_stream_analytics_private_configuration"])
			}
			if isBatchType(parent.NativeType) {
				target.BatchPrivateConfiguration = text(parent.Normalized["_batch_private_configuration"])
			}
			if isKustoType(parent.NativeType) {
				target.KustoAncestors = object(parent.Normalized["_kusto_ancestors"])
				target.KustoPrivateConfiguration = text(parent.Normalized["_kusto_private_configuration"])
			}
			if isMongoClusterType(parent.NativeType) {
				target.MongoClusterPrivateConfiguration = text(parent.Normalized["_mongocluster_private_configuration"])
			}
			if isCognitiveType(parent.NativeType) {
				target.CognitiveAncestors = object(parent.Normalized["_cognitive_ancestors"])
				target.CognitiveNativeLocation = text(parent.Normalized["_cognitive_native_location"])
			}
			if parent.NativeType == redisDatabaseType {
				target.RedisRootConfiguration = text(parent.Normalized["_redis_parent_configuration"])
			}
			if parent.NativeType == appSlotType {
				target.AppServiceRootConfiguration = text(parent.Normalized["_app_service_parent_configuration"])
			}
			if isCDNType(parent.NativeType) {
				target.CDNProfileConfiguration = text(parent.Normalized["_cdn_profile_configuration"])
			}
		}
		targets = append(targets, target)
	}
	if definition.Metadata.NativeType == dataCollectionAssociationType {
		return c.dataCollectionOrphanTargets(ctx, targets)
	}
	return targets, nil
}

// inventoryProtectionTTL bounds how long inventory pages of every kind and
// region share one read of the subscription's resource group owners and locks.
// Cleanup and pre-delete checks call managementLocks and always read live.
const inventoryProtectionTTL = 30 * time.Second

type inventoryProtectionCache struct {
	mu           sync.Mutex
	subscription string
	owners       map[string]string
	locks        []any
	expires      time.Time
}

func (c *client) inventoryProtection(ctx context.Context) (map[string]string, []any, error) {
	cache := c.protection
	if cache == nil {
		return c.readInventoryProtection(ctx)
	}
	cache.mu.Lock()
	defer cache.mu.Unlock()
	if cache.subscription == c.subscription && time.Now().Before(cache.expires) {
		return cache.owners, cache.locks, nil
	}
	owners, locks, err := c.readInventoryProtection(ctx)
	if err != nil {
		return nil, nil, err
	}
	cache.subscription, cache.owners, cache.locks, cache.expires = c.subscription, owners, locks, time.Now().Add(inventoryProtectionTTL)
	return owners, locks, nil
}

func (c *client) readInventoryProtection(ctx context.Context) (map[string]string, []any, error) {
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
	singleton := api.Operation == "Azure.Microsoft.StreamAnalytics.StreamingJobs_Get" && api.ItemsPath == "properties.transformation" && api.Parameters["$expand"] == "transformation"
	if !ok || operation.Call.Method != "GET" || (api.ItemsPath != "value" && !singleton) || api.IdentityPath != "id" {
		return catalog.RESTRequest{}, fmt.Errorf("invalid Azure native list rule")
	}
	parameters := map[string]any{}
	for key, value := range api.Parameters {
		switch value {
		case "scope.subscription":
			parameters[key] = c.subscription
		case "scope.subscriptionPath":
			parameters[key] = strings.TrimPrefix(c.root(), "/")
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
	bound, err := bindAzureREST(operation, parameters)
	if err != nil {
		return catalog.RESTRequest{}, err
	}
	return bound, nil
}
