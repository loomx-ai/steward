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
	// ponytail: cross-page duplicate hashes share the 128 KiB cursor bound;
	// use server-side scan state if larger collections need continuation.
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
	for _, compiled := range r.bundle.Specs {
		if strings.EqualFold(compiled.ResourceKind.NativeType, nativeType) {
			return compiled.Definition, compiled.Definition.Discovery.Source == insightsInventorySource(nativeType)
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
		if isAPIMAssociation(nativeType) {
			raw, err = apimAssociationRow(target.ParentID, nativeType, raw)
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
		wireID := responseID(kind.NativeType, text(raw["id"]))
		id, parsedType, err := parseID(wireID)
		if err != nil || !strings.EqualFold(parsedType, kind.NativeType) || !validResponseType(kind.NativeType, text(raw["type"])) || seen[id] {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure product list returned an invalid or duplicate identity")
		}
		seen[id] = true
		hash := fmt.Sprintf("%x", sha256.Sum256([]byte(id)))
		if slices.Contains(cursor.Resources, hash) {
			return contracts.InventoryBatch{}, fmt.Errorf("Azure product list returned a duplicate resource across pages")
		}
		cursor.Resources = append(cursor.Resources, hash)
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
		readURL, err := c.resourceURL(kind, wireID)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		// ARM list responses can omit lifecycle fields. Enrich from the native
		// detail API before declaring the resource actionable.
		detail, err := c.readResource(ctx, readURL)
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
		cursor.Next = next
	} else {
		cursor.Target++
		cursor.Next = ""
		cursor.Seen = nil
		cursor.Resources = nil
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

func (r *Runtime) productTargets(ctx context.Context, c *client, request contracts.InventoryRequest, definition spec.ResourceKindSpec, ancestors []string) ([]productTarget, error) {
	parents := []contracts.InventoryItem{{}}
	if parent := definition.Discovery.Parent; parent != nil {
		if parent.Source != productInventorySource && !(parent.Source == synapseSource && synapseKind(parent.NativeType) == synapseType) {
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
