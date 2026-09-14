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
	API                  spec.ProductAPISpec `json:"api"`
	Parameters           map[string]any      `json:"parameters"`
	ParentType           string              `json:"parent_type,omitempty"`
	ParentID             string              `json:"parent_id,omitempty"`
	ParentUID            string              `json:"parent_uid,omitempty"`
	ParentConfiguration  string              `json:"parent_configuration,omitempty"`
	ParentContainerChain string              `json:"parent_container_chain,omitempty"`
}
type productRecord struct {
	Data     map[string]any
	Location string
}

func (r *Runtime) productDefinition(nativeType string) (spec.ResourceKindSpec, bool) {
	for _, compiled := range r.bundle.Specs {
		if compiled.ResourceKind.NativeType == nativeType {
			return compiled.Definition, compiled.Definition.Discovery.Source == productInventorySource || compiled.Definition.Discovery.Source == securityBillingSource || compiled.Definition.Discovery.Source == securityServiceSource
		}
	}
	return spec.ResourceKindSpec{}, false
}
func (r *Runtime) usesProductSource(nativeType string) bool {
	if isDataformFolder(nativeType) || isFirewall(nativeType) || nativeType == organizationType || nativeType == securitySubscriptionType {
		return true
	}
	_, ok := r.productDefinition(nativeType)
	return ok
}

func (r *Runtime) listProduct(ctx context.Context, c *client, request contracts.InventoryRequest, ancestors []string) (contracts.InventoryBatch, error) {
	if request.ResourceKind == nil {
		return contracts.InventoryBatch{}, fmt.Errorf("GCP product inventory requires a resource kind")
	}
	nativeType := request.ResourceKind.NativeType
	if nativeType == securityServiceType && request.Source != securityServiceSource {
		return contracts.InventoryBatch{}, groupDenied("security_services_source_invalid")
	}
	if nativeType == securityBillingType && request.Source != securityBillingSource {
		return contracts.InventoryBatch{}, groupDenied("security_billing_source_invalid")
	}
	if isFirewall(nativeType) {
		return r.listFirewall(ctx, c, request)
	}
	if isMetricsScope(nativeType) {
		return r.listMetricsScope(ctx, c, request)
	}
	definition, ok := r.productDefinition(nativeType)
	if !ok || definition.Discovery.List == nil {
		return contracts.InventoryBatch{}, fmt.Errorf("GCP resource %q has no product discovery rule", nativeType)
	}
	if slices.Contains(ancestors, nativeType) {
		return contracts.InventoryBatch{}, fmt.Errorf("GCP parent discovery contains a cycle")
	}
	ancestors = append(slices.Clone(ancestors), nativeType)
	var serviceAncestry *organizationAncestry
	if nativeType == securityServiceType || nativeType == securityBillingType {
		chain, err := c.organizationAncestry(ctx)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		serviceAncestry = &chain
	}
	targets, err := r.productTargets(ctx, c, request, definition, ancestors)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	if serviceAncestry != nil {
		targets = securityServiceTargets(targets, *serviceAncestry)
	}
	bound, _ := json.Marshal(struct {
		Connection                                        asset.ConnectionID
		Project, ScopeKind, ScopeID, NativeType, Revision string
		Network                                           *asset.ScanTarget
		Ancestry                                          *organizationAncestry `json:"ancestry,omitempty"`
		Targets                                           []productTarget
	}{request.ConnectionID, c.project, string(request.Scope.Kind), request.Scope.NativeID, nativeType, r.bundle.Revision, request.NetworkTarget, serviceAncestry, targets})
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
		if err := c.verifySecurityServiceAncestry(ctx, serviceAncestry); err != nil {
			return contracts.InventoryBatch{}, err
		}
		return batch, nil
	}
	target := targets[cursor.Target]
	if err := c.verifyInfraParent(ctx, target); err != nil {
		return contracts.InventoryBatch{}, err
	}
	if err := c.verifyFusionParent(ctx, target); err != nil {
		return contracts.InventoryBatch{}, err
	}
	if err := c.verifyDiscoveryParent(ctx, target); err != nil {
		return contracts.InventoryBatch{}, err
	}
	if err := c.verifyDataprocParent(ctx, target); err != nil {
		return contracts.InventoryBatch{}, err
	}
	if err := c.verifyBatchParent(ctx, target); err != nil {
		return contracts.InventoryBatch{}, err
	}
	if err := c.verifyDataformParent(ctx, target); err != nil {
		return contracts.InventoryBatch{}, err
	}
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
	var result contracts.InvocationResult
	if nativeType == securityBillingType || nativeType == securityServiceType || nativeType == monitoringGroupType || isMonitoringConfig(nativeType) || nativeType == cloudNatType || nativeType == storagePoolType || isDataform(nativeType) || isBatch(nativeType) || isDataproc(nativeType) || isDiscovery(nativeType) || isTPU(nativeType) || isFusion(nativeType) || isInfra(nativeType) {
		// Keep native secret references inside the provider until configuration
		// proofs and dependency IDs have been derived. inventoryItem sanitizes all
		// payloads before they leave this boundary.
		metadata, _ := providerData()
		operation, _ := metadata.catalog.Operation(target.API.Operation)
		bound, bindErr := catalog.BindREST(operation, parameters)
		if bindErr != nil {
			return contracts.InventoryBatch{}, bindErr
		}
		result, err = c.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	} else {
		result, err = r.Invoke(ctx, contracts.Invocation{ConnectionID: request.ConnectionID, Operation: target.API.Operation, Parameters: parameters})
	}
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	if nativeType == securityBillingType {
		if err := c.securityBillingMetadata(result.Data, text(parameters["name"])); err != nil {
			return contracts.InventoryBatch{}, err
		}
	}
	if err = checkListCompleteness(result.Data); err != nil {
		return contracts.InventoryBatch{}, fmt.Errorf("%s: %w", target.API.Operation, err)
	}
	if isInfra(nativeType) {
		if err := infraListShape(result.Data, target.API.ItemsPath); err != nil {
			return contracts.InventoryBatch{}, err
		}
	}
	if nativeType == monitoringGroupType || isMonitoringConfig(nativeType) {
		collection := "uptimeCheckConfigs"
		if nativeType == monitoringDashboardType {
			collection = "dashboards"
		}
		if nativeType == monitoringGroupType {
			collection = "group"
		}
		if nativeType == notificationChannelType {
			collection = "notificationChannels"
		}
		if nativeType == alertPolicyType {
			collection = "alertPolicies"
		}
		if _, err := cloudNatObjects(result.Data, collection); err != nil {
			return contracts.InventoryBatch{}, err
		}
		if err := cloudNatScalars(result.Data, []string{"nextPageToken"}, nil, nil, nil); err != nil {
			return contracts.InventoryBatch{}, err
		}
	}
	if nativeType == cloudNatType {
		if _, err := c.cloudNatRouter(result.Data, target.ParentID, target.ParentUID); err != nil {
			return contracts.InventoryBatch{}, err
		}
	}
	var records []productRecord
	if nativeType != discoverySiteType && nativeType != securityBillingType {
		records, err = productRecords(result.Data, target.API.ItemsPath)
	}
	if err != nil {
		return contracts.InventoryBatch{}, fmt.Errorf("%s: %w", target.API.Operation, err)
	}
	if (nativeType == discoverySiteType || nativeType == securityBillingType) && err == nil {
		records = []productRecord{{Data: result.Data}}
	}
	identityPath := target.API.IdentityPath
	if nativeType == dataprocNodeGroupType {
		groups, err := c.dataprocNodeGroups(target.ParentID, result.Data)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		records = nil
		for _, group := range groups {
			records = append(records, productRecord{Data: group})
		}
		identityPath = "name"
	}
	batch.RequestID = result.RequestID
	kind, _ := findType(nativeType)
	metadata, _ := providerData()
	operation, _ := metadata.catalog.Operation(target.API.Operation)
	seenIDs := map[string]bool{}
	for _, record := range records {
		if resourceSoftDeleted(nativeType, record.Data) {
			continue
		}
		id, err := c.productIdentity(kind, operation, parameters, identityPath, record)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		if nativeType == securityServiceType {
			parent := c.canonicalName("//" + securityServiceHost + "/" + text(parameters["parent"]))
			if !strings.HasPrefix(id, parent+"/securityCenterServices/") {
				return contracts.InventoryBatch{}, groupDenied("security_service_list_parent_changed")
			}
		}
		if nativeType == storagePoolType {
			if err := c.storagePoolIdentity(id, record.Data, record.Location); err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
		if isInfra(nativeType) {
			if err := c.infraIdentity(nativeType, id, record.Data); err != nil {
				return contracts.InventoryBatch{}, err
			}
			if !strings.HasPrefix(id, c.canonicalName("//"+infraHost+"/"+text(parameters["parent"]))+"/") {
				return contracts.InventoryBatch{}, groupDenied("infra_list_scope_changed")
			}
		}
		if isFusion(nativeType) {
			if err := c.fusionIdentity(nativeType, id, record.Data); err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
		if isTPU(nativeType) {
			if err := c.tpuIdentity(nativeType, id, record.Data); err != nil {
				return contracts.InventoryBatch{}, err
			}
			if !strings.HasPrefix(id, c.canonicalName("//"+tpuHost+"/"+text(parameters["parent"]))+"/"+kind.Collection+"/") {
				return contracts.InventoryBatch{}, groupDenied("tpu_list_parent_changed")
			}
		}
		if isDiscovery(nativeType) {
			if err := c.discoveryIdentity(nativeType, id, record.Data); err != nil {
				return contracts.InventoryBatch{}, err
			}
			parent := text(parameters["parent"])
			if parent != "" && !strings.HasPrefix(id, c.canonicalName("//"+discoveryHost+"/"+parent)+"/"+kind.Collection+"/") {
				return contracts.InventoryBatch{}, groupDenied("discoveryengine_list_parent_changed")
			}
		}
		if isBatch(nativeType) {
			if err := c.batchChildIdentity(nativeType, id, text(parameters["parent"])); err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
		if isDataproc(nativeType) {
			if err := c.dataprocIdentity(nativeType, id, record.Data); err != nil {
				return contracts.InventoryBatch{}, err
			}
			region := text(parameters["region"])
			if region == "" {
				region = last(text(parameters["parent"]))
			}
			if region != dataprocRegion(id) {
				return contracts.InventoryBatch{}, groupDenied("dataproc_list_scope_changed")
			}
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
		// Parent enumeration only needs LIST identities for the child source.
		// Persisted Router observations require their own complete native GET.
		if nativeType == routerType && len(ancestors) == 1 {
			live, err := c.routerInventoryData(ctx, id, record.Data)
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			record.Data = live
		}
		if nativeType == monitoringDashboardType {
			live, err := c.monitoringDashboardInventory(ctx, id, record.Data)
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			record.Data = live
		}
		if nativeType == monitoringGroupType {
			live, err := c.monitoringGroupInventory(ctx, id, record.Data)
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			record.Data = live
		}
		if nativeType == notificationChannelType {
			live, err := c.notificationChannelInventory(ctx, id, record.Data)
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			record.Data = live
		}
		if nativeType == alertPolicyType {
			live, err := c.alertPolicyInventory(ctx, id, record.Data)
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			record.Data = live
		}
		if nativeType == uptimeType {
			live, err := c.uptimeInventory(ctx, id, record.Data)
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			record.Data = live
		}
		bigquery := nativeType == "bigquery.googleapis.com/Dataset" || nativeType == "bigquery.googleapis.com/Table"
		bigtable := nativeType == "bigtableadmin.googleapis.com/Table"
		if bigtable && !strings.HasPrefix(id, target.ParentID+"/tables/") {
			return contracts.InventoryBatch{}, groupDenied("bigtable_list_parent_changed")
		}
		if isDataform(nativeType) || isBatch(nativeType) || isDataproc(nativeType) || isDiscovery(nativeType) || isTPU(nativeType) || isFusion(nativeType) || isInfra(nativeType) || bigquery || bigtable || nativeType == securityServiceType || isRouterComponent(nativeType) {
			endpoint, err := c.resourceURL(kind, id)
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			var live map[string]any
			if nativeType == cloudNatType {
				live = record.Data // routers.get already returned the complete native NAT object.
			} else if isRouterComponent(nativeType) {
				live, err = c.routerComponentRead(ctx, nativeType, id)
			} else if isInfra(nativeType) {
				live, err = c.infraRead(ctx, nativeType, id)
			} else if isFusion(nativeType) {
				live, err = c.fusionRead(ctx, nativeType, id)
			} else if isTPU(nativeType) {
				live, err = c.tpuRead(ctx, nativeType, id)
			} else if isDiscovery(nativeType) {
				live, err = c.discoveryRead(ctx, nativeType, id)
			} else {
				live, err = c.request(ctx, "GET", endpoint, nil)
			}
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			if bigquery {
				liveID, identityErr := c.productIdentity(kind, operation, parameters, identityPath, productRecord{Data: live})
				if identityErr != nil || liveID != id {
					return contracts.InventoryBatch{}, groupDenied("bigquery_identity_changed")
				}
			} else if isRouterComponent(nativeType) {
				// The component reader verifies the native wrapper and router-local name.
			} else if bigtable {
				if c.canonicalName("//bigtableadmin.googleapis.com/"+text(live["name"])) != id {
					return contracts.InventoryBatch{}, groupDenied("bigtable_identity_changed")
				}
			} else if nativeType == securityServiceType {
				if c.canonicalName("//"+securityServiceHost+"/"+text(live["name"])) != id {
					return contracts.InventoryBatch{}, groupDenied("security_service_identity_changed")
				}
			} else if isDataproc(nativeType) {
				if err := c.dataprocIdentity(nativeType, id, live); err != nil {
					return contracts.InventoryBatch{}, err
				}
			} else if !isFusion(nativeType) && !isDiscovery(nativeType) && !isTPU(nativeType) && c.canonicalName("//"+strings.Split(nativeType, "/")[0]+"/"+text(live["name"])) != id {
				return contracts.InventoryBatch{}, groupDenied("dataform_identity_changed")
			}
			if isDiscovery(nativeType) {
				if err := serviceIncarnation(record.Data, live); err != nil {
					return contracts.InventoryBatch{}, err
				}
				if nativeType != discoveryCollectionType && nativeType != discoverySiteType {
					if err := discoverySameResource(nativeType, record.Data, live); err != nil {
						return contracts.InventoryBatch{}, err
					}
				}
			}
			if isInfra(nativeType) {
				if err := infraSame(record.Data, live); err != nil {
					return contracts.InventoryBatch{}, err
				}
			}
			if err := fusionSameResource(nativeType, record.Data, live); err != nil {
				return contracts.InventoryBatch{}, err
			}
			if err := dataformSameResource(nativeType, record.Data, live); err != nil {
				return contracts.InventoryBatch{}, err
			}
			if err := batchSameResource(nativeType, record.Data, live); err != nil {
				return contracts.InventoryBatch{}, err
			}
			if err := dataprocSameResource(nativeType, record.Data, live); err != nil {
				return contracts.InventoryBatch{}, err
			}
			if nativeType == batchJobType {
				if _, err := c.batchTaskGroups(id, live); err != nil {
					return contracts.InventoryBatch{}, err
				}
			}
			if isTPU(nativeType) {
				if err := tpuSameResource(nativeType, record.Data, live); err != nil {
					return contracts.InventoryBatch{}, err
				}
				live, err = c.tpuInventoryData(ctx, nativeType, id, live)
				if err != nil {
					return contracts.InventoryBatch{}, err
				}
			}

			if isInfraController(nativeType) {
				members, err := c.infraSnapshot(ctx, nativeType, id, live)
				if err != nil {
					return contracts.InventoryBatch{}, err
				}
				encoded, _ := json.Marshal(members)
				live[infraManifestKey] = string(encoded)
				proof := infraConfiguration(live)
				if nativeType == infraGroup {
					proof += "\n" + text(live[infraGroupStructure])
				}
				live[infraSnapshotKey] = infraManifestHash(proof, string(encoded))
			}
			record.Data = live
		}
		location := record.Location
		if location == "" {
			location = last(text(record.Data["location"]))
		}
		raw := map[string]any{"name": id, "assetType": nativeType, "resource": map[string]any{"data": record.Data, "location": location}}
		item, err := r.inventoryItem(c, raw)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		if nativeType == routerType && len(ancestors) == 1 {
			item.Normalized[routerReview] = routerConfiguration(record.Data, false)
			item.Normalized[routerBaseReview] = routerConfiguration(record.Data, true)
		}
		if !productScopeMatches(request, item) {
			continue
		}
		if nativeType == monitoringGroupType {
			if err := c.enrichMonitoringGroup(ctx, &item, record.Data); err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
		if nativeType == storagePoolType {
			if err := c.enrichStoragePool(ctx, &item, record.Data); err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
		item.Normalized["_inventory_source"] = productInventorySource
		if nativeType == securityBillingType || nativeType == securityServiceType {
			item.Normalized["_inventory_source"] = request.Source
		}
		if err := c.enrichDataformContainer(ctx, &item, record.Data); err != nil {
			return contracts.InventoryBatch{}, err
		}
		if target.ParentID != "" {
			if nativeType == cloudNatType {
				item.Normalized[cloudNatRouterID] = result.Data["id"]
				networks := references(c, map[string]any{"network": result.Data["network"]})["compute.googleapis.com/Network"]
				item.Normalized[referenceKey("compute.googleapis.com/Network")] = networks
				item.NetworkReferences = append(item.NetworkReferences, networks...)
				if len(networks) == 1 {
					item.Normalized["vpc_id"] = networks[0]
				}
				if err := c.enrichCloudNatHubs(ctx, &item, record.Data, result.Data, target.ParentID); err != nil {
					return contracts.InventoryBatch{}, err
				}
			}
			if nativeType == namedSetType {
				item.Normalized[namedSetRouterID] = target.ParentUID
				actionable := firewallNumericID(target.ParentUID) && text(item.Normalized["fingerprint"]) != ""
				item.Actionable = &actionable
			}
			if nativeType == routePolicyType {
				item.Normalized[routePolicyRouterID] = target.ParentUID
				peers, err := routePolicyBGPFromCursor(target.ParentConfiguration)
				if err != nil {
					return contracts.InventoryBatch{}, err
				}
				item.Normalized[routePolicyPeers] = peers
				references := []any{}
				for _, value := range peers {
					peer := object(value)
					for _, direction := range []string{"importPolicies", "exportPolicies"} {
						for _, policy := range array(peer[direction]) {
							if policy == last(id) {
								references = append(references, map[string]any{"peer": peer["name"], "direction": direction})
							}
						}
					}
				}
				item.Normalized["bgpReferences"] = references
				sets, err := routePolicySetReferences(item.Normalized)
				if err != nil {
					return contracts.InventoryBatch{}, err
				}
				setIDs := make([]string, 0, len(sets))
				for _, name := range sets {
					setIDs = append(setIDs, target.ParentID+"/namedSets/"+name)
				}
				item.Normalized[referenceKey(namedSetType)] = setIDs
				item.NetworkReferences = append(item.NetworkReferences, setIDs...)

				actionable := firewallNumericID(target.ParentUID) && text(item.Normalized["fingerprint"]) != ""
				item.Actionable = &actionable
			}
			if isInfra(nativeType) {
				item.Normalized[infraParentProof] = target.ParentConfiguration
				if target.ParentType == infraRevision {
					item.Normalized[infraRootProof] = target.ParentContainerChain
				} else {
					item.Normalized[infraRootProof] = target.ParentConfiguration
				}
			}
			item.Normalized[referenceKey(target.ParentType)] = []string{target.ParentID}
			item.NetworkReferences = append(item.NetworkReferences, target.ParentID)
			if isFusion(nativeType) {
				item.Normalized[fusionParentProof] = target.ParentConfiguration
			}
			if isDiscovery(nativeType) {
				item.Normalized[discoveryParentProof] = target.ParentConfiguration
				item.Normalized["_discoveryengine_ancestors"] = target.ParentContainerChain
				item.Normalized["_discoveryengine_parent_type"] = target.ParentType
				item.Normalized["_discoveryengine_parent_id"] = target.ParentID
			}
			if nativeType == batchTaskType {
				item.Normalized[batchParentProof] = target.ParentConfiguration
				item.Normalized["_batch_job_uid"] = target.ParentUID
			}
			if nativeType == dataprocNodeGroupType {
				item.Normalized[dataprocParentProof] = target.ParentConfiguration
				item.Normalized["_dataproc_cluster_uuid"] = target.ParentUID
			}
			if nativeType == nodePoolType {
				item.Normalized["_gke_cluster_uid"] = target.ParentUID
			}
			if isDataform(nativeType) {
				item.Normalized["_dataform_parent_configuration"] = target.ParentConfiguration
				item.Normalized[dataformContainerChain] = target.ParentContainerChain
			}
		}
		batch.Items = append(batch.Items, item)
	}
	if err := c.verifyInfraParent(ctx, target); err != nil {
		return contracts.InventoryBatch{}, err
	}
	if err := c.verifyFusionParent(ctx, target); err != nil {
		return contracts.InventoryBatch{}, err
	}
	if err := c.verifyDiscoveryParent(ctx, target); err != nil {
		return contracts.InventoryBatch{}, err
	}
	if err := c.verifyDataprocParent(ctx, target); err != nil {
		return contracts.InventoryBatch{}, err
	}
	if err := c.verifyBatchParent(ctx, target); err != nil {
		return contracts.InventoryBatch{}, err
	}
	if err := c.verifyDataformParent(ctx, target); err != nil {
		return contracts.InventoryBatch{}, err
	}
	if err := c.verifySecurityServiceAncestry(ctx, serviceAncestry); err != nil {
		return contracts.InventoryBatch{}, err
	}
	next := ""
	if isDiscovery(nativeType) && result.Data["nextPageToken"] != nil {
		if _, ok := result.Data["nextPageToken"].(string); !ok {
			return contracts.InventoryBatch{}, groupDenied("discoveryengine_page_token_invalid")
		}
	}
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
	if isDiscovery(definition.Metadata.NativeType) {
		return r.discoveryTargets(ctx, c, request, definition, ancestors)
	}
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
		if parentType, ok := findType(parent.NativeType); ok && len(parentType.Scopes) == 1 && parentType.Scopes[0] == asset.ScopeGlobal && request.Scope.Kind == asset.ScopeRegion {
			parentRequest.Scope = asset.Scope{Kind: asset.ScopeProject, NativeID: c.project}
		}
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
		if request.ResourceKind.NativeType == batchTaskType {
			var expanded []contracts.InventoryItem
			for _, parent := range parents {
				groups, err := c.batchTaskGroups(parent.NativeID, parent.Normalized)
				if err != nil {
					return nil, err
				}
				for _, group := range groups {
					copy := parent
					copy.Normalized = cloneParameters(parent.Normalized)
					copy.Normalized["_batch_task_group"] = group
					expanded = append(expanded, copy)
				}
			}
			parents = expanded
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
					if slices.Contains(operation.Call.RawPathParameters, name) && strings.HasPrefix(text(property["pattern"]), "^projects/") {
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
			if value == "scope.location" || value == "scope.locationParent" || value == "scope.regionParent" || value == "scope.iapTunnelLocationParent" || value == "scope.securityBillingName" {
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
		if onlyGlobal {
			targetLocations = []string{"global"}
		}
		var serviceLocations []string
		serviceLocationList := false
		if regional && !onlyGlobal && request.Scope.Kind != asset.ScopeGlobal {
			var err error
			serviceLocations, serviceLocationList, err = c.productLocations(ctx, operation)
			if err != nil {
				return nil, err
			}
		}
		if request.Scope.Kind == asset.ScopeProject {
			targetLocations = []string{"global"}
			if regional && !onlyGlobal && !serviceLocationList {
				if !regionLookup {
					var err error
					if isDataproc(request.ResourceKind.NativeType) {
						var native []map[string]any
						native, err = c.batchList(ctx, "compute.regions.list", map[string]any{"project": c.project}, "items")
						for _, item := range native {
							name := text(item["name"])
							if name == "" || !segmentPattern.MatchString(name) || name == "." || name == ".." {
								return nil, groupDenied("dataproc_region_invalid")
							}
							if item["status"] != "DOWN" {
								regions = append(regions, contracts.DiscoveredRegion{RegionID: name, Name: name})
							}
						}
						if err == nil && len(regions) == 0 {
							return nil, groupDenied("dataproc_regions_incomplete")
						}
					} else {
						regions, err = r.DiscoverRegions(ctx, request.ConnectionID)
					}
					if err != nil {
						return nil, err
					}
					regionLookup = true
				}
				targetLocations = nil
				for _, region := range regions {
					targetLocations = append(targetLocations, region.RegionID)
				}
				if slices.Contains(kind.Scopes, asset.ScopeGlobal) && !slices.Contains(targetLocations, "global") {
					targetLocations = append(targetLocations, "global")
				}
			}
		}
		if serviceLocationList {
			targetLocations = nil
			for _, location := range serviceLocations {
				if location == "global" && !slices.Contains(kind.Scopes, asset.ScopeGlobal) {
					continue
				}
				if request.Scope.Kind == asset.ScopeProject || regionOf(location) == request.Scope.NativeID {
					targetLocations = append(targetLocations, location)
				}
			}
		}
		if request.Scope.Kind == asset.ScopeRegion && request.NetworkTarget != nil && regional && slices.Contains(kind.Scopes, asset.ScopeGlobal) && !slices.Contains(targetLocations, "global") {
			targetLocations = append(targetLocations, "global")
		}
		for _, location := range targetLocations {
			if regional && !serviceLocationList && !productSupportsMultiRegion(operation.Call.Product, location) {
				continue
			}
			for _, parent := range parents {
				if isRouterComponent(kind.NativeType) && parent.Scope.NativeID != location {
					continue
				}
				resolved, err := productParameters(parameters, c, location, parent)
				if isRouterComponent(kind.NativeType) && err == nil && resolved["router"] != last(parent.NativeID) {
					return nil, groupDenied("route_policy_router_identity_changed")
				}
				if err != nil {
					return nil, err
				}
				if _, err = catalog.BindREST(operation, resolved); err != nil {
					return nil, err
				}
				parentUID := ""
				for _, field := range []string{"uid", "uniqueId", "id", "clusterUuid"} {
					if parentUID = text(parent.Normalized[field]); parentUID != "" {
						break
					}
				}
				configuration := text(parent.Normalized[dataformProof])
				if kind.NativeType == routePolicyType {
					peers, err := routePolicyBGPPeers(parent.Normalized)
					if err != nil {
						return nil, err
					}
					encoded, err := json.Marshal(map[string]any{"bgpPeers": peers})
					if err != nil {
						return nil, err
					}
					configuration = string(encoded)
				}
				if isInfra(parent.NativeType) {
					configuration = text(parent.Normalized[infraProof])
				}
				if parent.NativeType == fusionInstanceType {
					configuration = text(parent.Normalized[fusionProof])
				}
				if parent.NativeType == batchJobType {
					configuration = text(parent.Normalized[batchProof])
				}
				if parent.NativeType == dataprocClusterType {
					configuration = text(parent.Normalized[dataprocProof])
				}
				chain := text(parent.Normalized[dataformContainerChain])
				if isInfra(parent.NativeType) {
					chain = text(parent.Normalized[infraRootProof])
				}
				targets = append(targets, productTarget{API: api, Parameters: resolved, ParentType: parent.NativeType, ParentID: parent.NativeID, ParentUID: parentUID, ParentConfiguration: configuration, ParentContainerChain: chain})
			}
		}
	}
	return targets, nil
}

// Product location lists include zonal and multi-region service locations that
// Compute cannot enumerate. An unreadable/partial list is never an empty shard.
func (c *client) productLocations(ctx context.Context, resource catalog.Operation) ([]string, bool, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, false, err
	}
	version := resource.Call.Version
	if resource.Call.Product == "tpu" {
		version = "v2"
	}
	for _, operation := range metadata.catalog.Operations {
		if operation.Call == nil || operation.ID != resource.Call.Product+".projects.locations.list" || operation.Call.Version != version {
			continue
		}
		records, err := c.nativeList(ctx, operation, map[string]any{"name": "projects/" + c.project}, "locations")
		if err != nil {
			return nil, true, err
		}
		var locations []string
		seen := map[string]bool{}
		for _, record := range records {
			name := text(record["name"])
			location := text(record["locationId"])
			if location == "" {
				location = last(name)
			}
			if !segmentPattern.MatchString(location) || location == "." || location == ".." || strings.Contains(location, "/") || seen[location] || (name != "projects/"+c.project+"/locations/"+location && name != "projects/"+c.number+"/locations/"+location) {
				return nil, true, fmt.Errorf("invalid or duplicate GCP product location")
			}
			if resource.Call.Product == "tpu" && regionOf(location) == location {
				return nil, true, groupDenied("tpu_location_not_zone")
			}
			seen[location] = true
			locations = append(locations, location)
		}
		sort.Strings(locations)
		return locations, true, nil
	}
	return nil, false, nil
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
		case "scope.metricsScopeName":
			result[key] = "locations/global/metricsScopes/" + c.number
		case "scope.location":
			result[key] = location
		case "scope.locationParent":
			result[key] = "projects/" + c.project + "/locations/" + location
		case "scope.securityBillingName":
			result[key] = "projects/" + c.project + "/locations/" + location + "/billingMetadata"
		case "scope.regionParent":
			result[key] = "projects/" + c.project + "/regions/" + location
		case "scope.allLocationsParent":
			result[key] = "projects/" + c.project + "/locations/-"
		case "scope.iapTunnelLocationParent":
			result[key] = "projects/" + c.number + "/iap_tunnel/locations/" + location
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
	if _, present := data["error"]; present {
		return fmt.Errorf("GCP list returned an error payload")
	}
	for _, key := range []string{"unreachable", "unreachables", "unreachableLocations", "failedLocations", "missingZones"} {
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
	if isRouterComponent(kind.NativeType) {
		name, ok := record.Data["name"].(string)
		if !ok {
			return "", groupDenied("route_policy_name_invalid")
		}
		collection := "routePolicies"
		if kind.NativeType == namedSetType {
			collection = "namedSets"
		} else if kind.NativeType == cloudNatType {
			collection = "nats"
		}
		id := "//compute.googleapis.com/projects/" + text(parameters["project"]) + "/regions/" + text(parameters["region"]) + "/routers/" + text(parameters["router"]) + "/" + collection + "/" + name
		if _, _, err := c.routerComponentOperation(kind.NativeType, id, "GET"); err != nil {
			return "", err
		}
		return c.canonicalName(id), nil
	}
	if isFusion(kind.NativeType) {
		return c.fusionID(kind.NativeType, text(productValue(record.Data, identityPath)), text(parameters["parent"]))
	}
	if isTPU(kind.NativeType) {
		return c.tpuID(kind.NativeType, text(productValue(record.Data, identityPath)))
	}
	if isDiscovery(kind.NativeType) {
		return c.discoveryID(kind.NativeType, text(productValue(record.Data, identityPath)))
	}
	if kind.NativeType == "bigquery.googleapis.com/Dataset" || kind.NativeType == "bigquery.googleapis.com/Table" {
		reference := object(record.Data["datasetReference"])
		if kind.NativeType == "bigquery.googleapis.com/Table" {
			reference = object(record.Data["tableReference"])
			if reference["datasetId"] != parameters["datasetId"] {
				return "", fmt.Errorf("BigQuery table belongs to another dataset")
			}
		}
		if reference["projectId"] != c.project && reference["projectId"] != c.number {
			return "", fmt.Errorf("BigQuery resource belongs to another project")
		}
		if text(productValue(record.Data, identityPath)) == "" {
			return "", fmt.Errorf("BigQuery resource reference has no native identity")
		}
	}
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
	if strings.HasPrefix(name, "projects/") || (kind.NativeType == securityServiceType || kind.NativeType == securityBillingType) && (strings.HasPrefix(name, "folders/") || strings.HasPrefix(name, "organizations/")) {
		return c.canonicalName("//" + host + "/" + name), nil
	}
	if (!segmentPattern.MatchString(name) && !(kind.NativeType == dnsRecordSetType && dnsRecordName(name))) || name == "." || name == ".." {
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
	id := "//" + host + path[index:] + "/" + name
	// Cloud DNS addresses a record set by both its fully qualified name and
	// record type; A and AAAA records at the same name are distinct resources.
	if kind.NativeType == dnsRecordSetType {
		recordType := text(record.Data["type"])
		if !dnsRecordName(name) || !segmentPattern.MatchString(recordType) || recordType == "." || recordType == ".." {
			return "", fmt.Errorf("invalid Cloud DNS record identity")
		}
		id += "/" + recordType
	}
	return c.canonicalName(id), nil
}

const dnsRecordSetType = "dns.googleapis.com/ResourceRecordSet"

func dnsRecordName(name string) bool {
	return len(name) <= 255 && strings.HasSuffix(name, ".") && name != "." && segmentPattern.MatchString(strings.TrimPrefix(name, "*."))
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

// These products use ordinary regions (or zones), not Discovery Engine's US/EU
// locations. An all-regions scan must not call Compute-region endpoints with a
// multi-region name. Products with Locations.list use that authoritative list;
// project-wide/aggregate APIs retain their ordinary scope filtering.
func productSupportsMultiRegion(product, location string) bool {
	if location != "us" && location != "eu" {
		return true
	}
	switch product {
	case "compute", "file", "dataproc", "servicedirectory", "managedkafka", "redis", "iap", "alloydb", "apigateway", "gkehub", "run", "cloudtasks", "cloudfunctions", "certificatemanager":
		return false
	case "artifactregistry":
		return location == "us" // Artifact Registry calls its European multi-region "europe".
	default:
		return true // DLP and Logging explicitly support US/EU processing/storage.
	}
}
