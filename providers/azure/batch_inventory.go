package azure

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func batchARMAncestors(id, kind string) []string {
	var result []string
	for kind != batchAccountType && isBatchType(kind) && !isBatchDataType(kind) {
		id = redisParentID(id)
		_, kind, _ = parseID(id)
		kind = batchKind(kind)
		result = append(result, id)
	}
	return result
}

func (c *client) batchInventory(ctx context.Context, id, kind string, raw, normalized map[string]any) error {
	if !isBatchType(kind) || isBatchDataType(kind) {
		return nil
	}
	var account batchAccountContext
	var err error
	if kind == batchAccountType {
		endpoint, e := batchAccountEndpoint(raw)
		if e != nil {
			return e
		}
		account = batchAccountContext{id: id, endpoint: endpoint, location: resourceRegion(raw), raw: raw}
	} else {
		account, err = c.batchAccount(ctx, batchAccountID(id))
		if err != nil {
			return err
		}
	}
	normalized["_batch_configuration"] = batchConfiguration(kind, raw)
	normalized["_batch_private_configuration"] = c.privateConfiguration(batchSnapshot(kind, raw))
	normalized["_batch_account"], normalized["_batch_endpoint"], normalized["_batch_location"] = account.id, account.endpoint, account.location
	normalized["_batch_account_binding"] = batchAccountBinding(c, account)
	ancestors := map[string]any{}
	defaults := map[string]any{}
	for _, parent := range batchARMAncestors(id, kind) {
		current := account.raw
		if parent != account.id {
			current, err = c.linkedResource(ctx, parent)
			if err != nil {
				return err
			}
		}
		_, parentKind, _ := parseID(parent)
		ancestors[parent] = c.privateConfiguration(batchSnapshot(batchKind(parentKind), current))
		if batchKind(parentKind) == batchApplicationType {
			defaults[parent] = object(current["properties"])["defaultVersion"]
		}
	}
	normalized["_batch_ancestors"] = ancestors
	normalized["_batch_ancestor_defaults"] = defaults
	normalized["_batch_parameters"] = map[string]any{}
	if kind == batchPoolType {
		normalized["_batch_parameters"] = map[string]any{"poolId": last(id)}
	}
	refs, err := c.batchReferences(ctx, account, id, kind, raw)
	if err != nil {
		return err
	}
	normalized["_batch_references"] = refs
	return nil
}

func (c *client) batchReferences(ctx context.Context, account batchAccountContext, id, kind string, raw map[string]any) (map[string][]string, error) {
	result := map[string][]string{}
	if !isBatchType(kind) {
		return result, nil
	}
	if isBatchDataType(kind) {
		addReference(result, batchAccountType, account.id)
	} else {
		if kind != batchAccountType {
			parent := redisParentID(id)
			_, typ, _ := parseID(parent)
			addReference(result, batchKind(typ), parent)
		}
	}
	addARM := func(value any, expected string) error {
		if value == nil || value == "" {
			return nil
		}
		native, typ, err := parseID(text(value))
		if err != nil || !strings.EqualFold(typ, expected) {
			return serviceDenied("invalid_batch_reference")
		}
		if mapping, ok := findType(typ); ok {
			addReference(result, mapping.NativeType, native)
		}
		return nil
	}
	// Only documented ARM IDs establish these references. URLs in resource files
	// and mount settings never supply authority to fetch an arbitrary endpoint.
	var walk func(any) error
	walk = func(value any) error {
		switch value := value.(type) {
		case map[string]any:
			for key, entry := range value {
				if batchOpaqueReferenceField(key) {
					continue
				}
				expected := map[string]string{"storageAccountId": storageType, "subnetId": subnetType, "diskEncryptionSetId": "Microsoft.Compute/diskEncryptionSets", "diskEncryptionSetResourceId": "Microsoft.Compute/diskEncryptionSets", "scaleSetVmResourceId": scaleSetVMType}[key]
				if expected != "" {
					if err := addARM(entry, expected); err != nil {
						return err
					}
				}
				if key == "userAssignedIdentities" {
					for native := range object(entry) {
						if err := addARM(native, "Microsoft.ManagedIdentity/userAssignedIdentities"); err != nil {
							return err
						}
					}
				}
				if key == "identityReference" {
					ref, ok := entry.(map[string]any)
					if entry != nil && !ok {
						return serviceDenied("invalid_batch_identity_reference")
					}
					if err := addARM(ref["resourceId"], "Microsoft.ManagedIdentity/userAssignedIdentities"); err != nil {
						return err
					}
				}
				if key == "publicIPAddresses" {
					for _, native := range array(entry) {
						if err := addARM(native, "Microsoft.Network/publicIPAddresses"); err != nil {
							return err
						}
					}
				}
				if err := walk(entry); err != nil {
					return err
				}
			}
		case []any:
			for _, entry := range value {
				if err := walk(entry); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := walk(raw); err != nil {
		return nil, err
	}
	props := object(raw["properties"])
	if kind == batchAccountType {
		if err := addARM(object(props["keyVaultReference"])["id"], "Microsoft.KeyVault/vaults"); err != nil {
			return nil, err
		}
	}
	if kind == batchPECType {
		if err := addARM(object(props["privateEndpoint"])["id"], privateEndpointType); err != nil {
			return nil, err
		}
	}
	if isBatchDataType(kind) {
		props = raw
	}
	if kind == batchJobType || kind == batchScheduleType {
		job := props
		if kind == batchScheduleType {
			job = object(props["jobSpecification"])
		}
		poolInfo := object(job["poolInfo"])
		pool := text(poolInfo["poolId"])
		actual := text(object(job["executionInfo"])["poolId"])
		if pool != "" && actual != "" && !strings.EqualFold(pool, actual) {
			return nil, serviceDenied("batch_job_pool_disagrees")
		}
		if pool == "" {
			pool = actual
		}
		if pool != "" {
			if !batchNamePattern.MatchString(pool) {
				return nil, serviceDenied("invalid_batch_pool_reference")
			}
			addReference(result, batchPoolType, account.id+"/pools/"+strings.ToLower(pool))
		}
	}
	if kind == batchTaskType {
		if raw["nodeInfo"] != nil {
			info, ok := raw["nodeInfo"].(map[string]any)
			if !ok {
				return nil, serviceDenied("invalid_batch_task_node_info")
			}
			node, pool, err := batchTaskNode(account, info)
			if err != nil {
				return nil, err
			}
			if node != "" {
				addReference(result, batchNodeType, node)
			}
			if pool != "" {
				addReference(result, batchPoolType, pool)
			}
		}
		_, _, _, params, err := batchDataIdentity(id)
		if err != nil {
			return nil, err
		}
		addReference(result, batchJobType, account.endpoint+"/jobs/"+strings.ToLower(text(params["jobId"])))
		dependencies, ok := raw["dependsOn"].(map[string]any)
		if raw["dependsOn"] != nil && !ok {
			return nil, serviceDenied("invalid_batch_task_dependencies")
		}
		ids := dependencies["taskIds"]
		if ids != nil {
			values, ok := ids.([]any)
			if !ok {
				return nil, serviceDenied("invalid_batch_task_dependencies")
			}
			for _, value := range values {
				name, err := cognitiveExactString(value)
				if err != nil || !batchNamePattern.MatchString(name) {
					return nil, serviceDenied("invalid_batch_task_dependency_id")
				}
				addReference(result, batchTaskType, account.endpoint+"/jobs/"+strings.ToLower(text(params["jobId"]))+"/tasks/"+strings.ToLower(text(value)))
			}
		}
		if dependencies["taskIdRanges"] != nil {
			ranges, ok := dependencies["taskIdRanges"].([]any)
			if !ok {
				return nil, serviceDenied("invalid_batch_task_dependency_ranges")
			}
			siblings, _, err := c.batchListedData(ctx, account, batchTaskType, "Tasks_ListTasks", map[string]any{"jobId": params["jobId"]})
			if err != nil {
				return nil, err
			}
			for _, value := range ranges {
				rangeValue := object(value)
				start, startErr := batchInteger(rangeValue["start"], 32)
				end, endErr := batchInteger(rangeValue["end"], 32)
				if startErr != nil || endErr != nil || end < start {
					return nil, serviceDenied("invalid_batch_task_dependency_range")
				}
				// Native ranges include leading-zero IDs (4, 04 and 004).
				// Iterate actual tasks, never expand a potentially enormous range.
				for _, sibling := range siblings {
					name := text(sibling["id"])
					n, err := strconv.ParseInt(name, 10, 32)
					if err == nil && n >= start && n <= end {
						addReference(result, batchTaskType, strings.ToLower(text(sibling["url"])))
					}
				}
			}
		}
	}
	if kind == batchNodeType {
		_, _, _, params, err := batchDataIdentity(id)
		if err != nil {
			return nil, err
		}
		addReference(result, batchPoolType, account.id+"/pools/"+strings.ToLower(text(params["poolId"])))
	}
	// ARM package references contain an application or version ARM ID; the data
	// plane uses an application ID/name and an optional version. Resolve omitted
	// versions from the actual application, never from a guessed default.
	var packages func(any) error
	packages = func(value any) error {
		switch value := value.(type) {
		case map[string]any:
			for key, entry := range value {
				if batchOpaqueReferenceField(key) {
					continue
				}
				if key == "applicationPackages" || key == "applicationPackageReferences" {
					records, ok := entry.([]any)
					if !ok && entry != nil {
						return serviceDenied("invalid_batch_package_references")
					}
					for _, v := range records {
						ref := object(v)
						name := text(ref["id"])
						if key == "applicationPackageReferences" {
							name = text(ref["applicationId"])
						}
						appID := name
						version := text(ref["version"])
						if !strings.HasPrefix(name, "/") {
							if key == "applicationPackages" || !batchNamePattern.MatchString(name) {
								return serviceDenied("invalid_batch_application_reference")
							}
							appID = account.id + "/applications/" + strings.ToLower(name)
						}
						parsed, typ, err := parseID(appID)
						if err != nil || batchAccountID(parsed) != account.id || (batchKind(typ) != batchApplicationType && batchKind(typ) != batchPackageType) {
							return serviceDenied("invalid_batch_application_reference")
						}
						appID = parsed
						if batchKind(typ) == batchPackageType {
							if version != "" && !strings.EqualFold(version, last(appID)) {
								return serviceDenied("batch_package_version_disagrees")
							}
							version = last(appID)
							appID = redisParentID(appID)
						}
						addReference(result, batchApplicationType, appID)
						if version == "" {
							app, err := c.batchRead(ctx, account, appID, batchApplicationType)
							if err != nil {
								return err
							}
							version = text(object(app.data["properties"])["defaultVersion"])
						}
						if version != "" {
							packageID, err := cognitiveNameID(appID, "versions", version)
							if err != nil {
								return err
							}
							addReference(result, batchPackageType, packageID)
						}
					}
				} else if err := packages(entry); err != nil {
					return err
				}
			}
		case []any:
			for _, entry := range value {
				if err := packages(entry); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := packages(raw); err != nil {
		return nil, err
	}
	if err := c.batchExternalReferences(ctx, account, raw, result); err != nil {
		return nil, err
	}
	for typ, values := range result {
		sort.Strings(values)
		result[typ] = slices.Compact(values)
	}
	return result, nil
}

// nodeInfo records where a task ran, including after the node was removed.
// Its documented URL or account-scoped pool/node IDs establish placement only.
func batchTaskNode(account batchAccountContext, info map[string]any) (string, string, error) {
	pool, node, endpoint := text(info["poolId"]), text(info["nodeId"]), text(info["nodeUrl"])
	for _, key := range []string{"poolId", "nodeId", "nodeUrl"} {
		if value := info[key]; value != nil {
			if s, ok := value.(string); !ok || s == "" || s != strings.TrimSpace(s) {
				return "", "", serviceDenied("invalid_batch_task_node_info")
			}
		}
	}
	if endpoint != "" {
		id, kind, origin, params, err := batchDataIdentity(endpoint)
		if err != nil || kind != batchNodeType || origin != account.endpoint || (pool != "" && !strings.EqualFold(pool, text(params["poolId"]))) || (node != "" && !strings.EqualFold(node, text(params["nodeId"]))) {
			return "", "", serviceDenied("invalid_batch_task_node_info")
		}
		return id, account.id + "/pools/" + strings.ToLower(text(params["poolId"])), nil
	}
	if (pool != "" && !batchNamePattern.MatchString(pool)) || (node != "" && (!batchNamePattern.MatchString(node) || pool == "")) {
		return "", "", serviceDenied("invalid_batch_task_node_info")
	}
	if pool == "" {
		return "", "", nil
	}
	if node != "" {
		endpoint = account.endpoint + "/pools/" + strings.ToLower(pool) + "/nodes/" + strings.ToLower(node)
	}
	return endpoint, account.id + "/pools/" + strings.ToLower(pool), nil
}

func (c *client) batchListedData(ctx context.Context, account batchAccountContext, kind, operation string, params map[string]any) ([]map[string]any, string, error) {
	values, requestID, err := c.batchDataList(ctx, account, operation, params)
	if err != nil {
		return nil, requestID, err
	}
	seen := map[string]bool{}
	for i, raw := range values {
		id, typ, origin, parameters, err := batchDataIdentity(text(raw["url"]))
		if err != nil || typ != kind || origin != account.endpoint || seen[id] || !strings.EqualFold(text(raw["id"]), last(id)) {
			return nil, requestID, serviceDenied("invalid_batch_list_identity")
		}
		if job := text(params["jobId"]); job != "" && !strings.EqualFold(job, text(parameters["jobId"])) {
			return nil, requestID, serviceDenied("batch_task_changed_job")
		}
		if pool := text(params["poolId"]); pool != "" && !strings.EqualFold(pool, text(parameters["poolId"])) {
			return nil, requestID, serviceDenied("batch_node_changed_pool")
		}
		seen[id] = true
		current, err := c.batchRead(ctx, account, id, kind)
		if err != nil {
			return nil, requestID, err
		}
		if !nativeConfigurationContains(batchSnapshot(kind, raw), batchSnapshot(kind, current.data)) {
			return nil, requestID, serviceDenied("batch_listed_configuration_changed")
		}
		values[i] = current.data
	}
	slices.SortFunc(values, func(a, b map[string]any) int {
		return strings.Compare(strings.ToLower(text(a["url"])), strings.ToLower(text(b["url"])))
	})
	return values, requestID, nil
}

func (r *Runtime) batchDataItem(ctx context.Context, c *client, account batchAccountContext, kind string, raw map[string]any, owners map[string]string, locks []any) (contracts.InventoryItem, error) {
	id, typ, origin, parameters, err := batchDataIdentity(text(raw["url"]))
	if err != nil || typ != kind || origin != account.endpoint || !strings.EqualFold(last(id), text(raw["id"])) {
		return contracts.InventoryItem{}, serviceDenied("invalid_batch_inventory_identity")
	}
	safe := safePayload(raw)
	normalized := maps.Clone(safe)
	normalized["name"], normalized["subscription_id"], normalized["resource_group"] = raw["id"], c.subscription, strings.Split(account.id, "/")[4]
	normalized["_batch_account"], normalized["_batch_endpoint"], normalized["_batch_location"] = account.id, account.endpoint, account.location
	normalized["_batch_account_binding"] = batchAccountBinding(c, account)
	normalized["_batch_configuration"], normalized["_batch_private_configuration"] = batchConfiguration(kind, raw), c.privateConfiguration(batchSnapshot(kind, raw))
	delete(parameters, "endpoint")
	normalized["_batch_parameters"] = parameters
	normalized["_inventory_source"] = productInventorySource
	if kind == batchNodeType {
		members, err := c.batchVMTree(ctx, account, raw)
		if err != nil {
			return contracts.InventoryItem{}, err
		}
		resources := c.batchVMResources(members)
		normalized["_batch_vm_resources"], normalized["_batch_vm_binding"] = resources, c.batchNodeBinding(raw, resources)
	}
	refs, err := c.batchReferences(ctx, account, id, kind, raw)
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	normalized["_batch_references"] = refs
	parents := map[string]any{}
	if kind == batchTaskType || kind == batchNodeType {
		parentID, parentKind := account.endpoint+"/jobs/"+strings.ToLower(text(parameters["jobId"])), batchJobType
		if kind == batchNodeType {
			parentID, parentKind = account.id+"/pools/"+strings.ToLower(text(parameters["poolId"])), batchPoolType
		}
		parent, err := c.batchRead(ctx, account, parentID, parentKind)
		if err != nil {
			return contracts.InventoryItem{}, err
		}
		snapshot := batchSnapshot(parentKind, parent.data)
		if kind == batchNodeType {
			snapshot = batchNodePoolSnapshot(parent.data)
		}
		parents[parentID] = c.privateConfiguration(snapshot)
	}
	normalized["_batch_ancestors"] = parents
	groupID := strings.Join(strings.Split(account.id, "/")[:5], "/")
	reason := ""
	if protectedAzureTags(object(account.raw["tags"])) {
		reason = "azure_protected_tag"
	}
	for _, entry := range array(raw["metadata"]) {
		pair := object(entry)
		if protectedAzureTags(map[string]any{text(pair["name"]): pair["value"]}) {
			reason = "azure_protected_tag"
		}
	}
	if owners[groupID] != "" {
		reason = "azure_managed_resource_group"
	}
	if locked(account.id, locks) {
		reason = "azure_management_lock"
	}
	if reason != "" {
		normalized["cleanup_protection_reason"] = reason
		if controllerOnlyReason(reason) {
			normalized["cleanup_controller_only"] = true
		} else {
			normalized["cleanup_protected"] = true
		}
	}
	network := []string{}
	for typ, ids := range refs {
		normalized[referenceKey(typ)] = ids
		network = append(network, ids...)
	}
	sort.Strings(network)
	actionable := true
	return contracts.InventoryItem{NativeID: id, NativeType: kind, ResourceKind: r.resourceKind(kind), Actionable: &actionable, Scope: contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: account.location, Name: account.location, Location: account.location}, Name: text(raw["id"]), Location: account.location, State: text(raw["state"]), Tags: map[string]string{}, Normalized: normalized, Raw: safe, NativeAliases: []string{text(raw["url"]), id}, NetworkReferences: network}, nil
}

// Read every current parent before applying a client-side cursor. The cursor
// binds the complete account/parent/resource set and private configurations;
// changed membership fails the shard instead of skipping resources. This uses
// the existing provider cursor size limit and no server-side scan cache.
func (r *Runtime) listBatchData(ctx context.Context, c *client, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	kind := batchKind(request.ResourceKind.NativeType)
	accountKind := r.resourceKind(batchAccountType)
	accountRequest := request
	accountRequest.ResourceKind = &accountKind
	accountRequest.Cursor = ""
	var accounts []contracts.InventoryItem
	provenance := ""
	seenPages := map[string]bool{}
	for {
		page, err := r.listProduct(ctx, c, accountRequest, nil)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		accounts = append(accounts, page.Items...)
		provenance = page.RequestID
		if page.Complete {
			break
		}
		if page.NextCursor == "" || seenPages[page.NextCursor] {
			return contracts.InventoryBatch{}, serviceDenied("batch_accounts_pagination_failed")
		}
		seenPages[page.NextCursor] = true
		accountRequest.Cursor = page.NextCursor
	}
	owners, locks, err := c.inventoryProtection(ctx)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	var items []contracts.InventoryItem
	bindings := map[string]any{}
	for _, parent := range accounts {
		account, err := c.batchAccount(ctx, parent.NativeID)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		if batchAccountBinding(c, account) != text(parent.Normalized["_batch_account_binding"]) {
			return contracts.InventoryBatch{}, serviceDenied("batch_account_changed_during_scan")
		}
		bindings[account.id] = batchAccountBinding(c, account)
		var values []map[string]any
		switch kind {
		case batchJobType:
			values, provenance, err = c.batchListedData(ctx, account, kind, "Jobs_ListJobs", nil)
		case batchScheduleType:
			values, provenance, err = c.batchListedData(ctx, account, kind, "JobSchedules_ListJobSchedules", nil)
		case batchTaskType:
			var jobs []map[string]any
			jobs, provenance, err = c.batchListedData(ctx, account, batchJobType, "Jobs_ListJobs", nil)
			if err == nil {
				for _, job := range jobs {
					var tasks []map[string]any
					tasks, provenance, err = c.batchListedData(ctx, account, kind, "Tasks_ListTasks", map[string]any{"jobId": text(job["id"])})
					if err != nil {
						break
					}
					values = append(values, tasks...)
					bindings[text(job["url"])] = c.privateConfiguration(batchSnapshot(batchJobType, job))
					var current response
					current, err = c.batchRead(ctx, account, text(job["url"]), batchJobType)
					if err != nil {
						break
					}
					if c.privateConfiguration(batchSnapshot(batchJobType, current.data)) != bindings[text(job["url"])] {
						err = serviceDenied("batch_job_changed_during_scan")
						break
					}
				}
			}
		case batchNodeType:
			var pools []serviceChild
			pools, provenance, err = c.batchPools(ctx, account)
			if err == nil {
				for _, pool := range pools {
					var nodes []map[string]any
					nodes, provenance, err = c.batchListedData(ctx, account, kind, "Nodes_ListNodes", map[string]any{"poolId": last(pool.id)})
					if err != nil {
						break
					}
					values = append(values, nodes...)
					bindings[pool.id] = c.privateConfiguration(batchSnapshot(batchPoolType, pool.data))
					var current response
					current, err = c.batchRead(ctx, account, pool.id, batchPoolType)
					if err != nil {
						break
					}
					if c.privateConfiguration(batchSnapshot(batchPoolType, current.data)) != bindings[pool.id] {
						err = serviceDenied("batch_pool_changed_during_scan")
						break
					}
				}
			}
		default:
			return contracts.InventoryBatch{}, serviceDenied("unknown_batch_data_type")
		}
		if err != nil {
			return contracts.InventoryBatch{}, contracts.DependencyReadError(err)
		}
		for _, raw := range values {
			item, err := r.batchDataItem(ctx, c, account, kind, raw, owners, locks)
			if err != nil {
				return contracts.InventoryBatch{}, contracts.DependencyReadError(err)
			}
			items = append(items, item)
		}
		current, err := c.batchAccount(ctx, account.id)
		if err != nil {
			return contracts.InventoryBatch{}, contracts.DependencyReadError(err)
		}
		if batchAccountBinding(c, current) != bindings[account.id] {
			return contracts.InventoryBatch{}, serviceDenied("batch_account_changed_during_scan")
		}
	}
	slices.SortFunc(items, func(a, b contracts.InventoryItem) int { return strings.Compare(a.NativeID, b.NativeID) })
	for _, item := range items {
		if _, duplicate := bindings[item.NativeID]; duplicate {
			return contracts.InventoryBatch{}, serviceDenied("duplicate_batch_inventory_resource")
		}
		bindings[item.NativeID] = item.Normalized["_batch_private_configuration"]
	}
	payload, _ := json.Marshal(struct {
		Connection     asset.ConnectionID
		Scope          asset.Scope
		Kind, Revision string
		Bindings       map[string]any
		Network        *asset.ScanTarget
	}{request.ConnectionID, request.Scope, kind, r.bundle.Revision, bindings, request.NetworkTarget})
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(payload))
	cursor := productCursor{Fingerprint: fingerprint}
	if request.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || len(request.Cursor) > 128<<10 || json.Unmarshal(raw, &cursor) != nil || cursor.Fingerprint != fingerprint || cursor.Target < 0 || cursor.Target >= len(items) || cursor.Next != "" || len(cursor.Seen) != 0 || len(cursor.Resources) != 0 {
			return contracts.InventoryBatch{}, serviceDenied("batch_inventory_cursor_changed")
		}
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := min(cursor.Target+limit, len(items))
	batch := contracts.InventoryBatch{Items: items[cursor.Target:end], Complete: end == len(items), RequestID: provenance}
	if !batch.Complete {
		cursor.Target = end
		raw, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return batch, nil
}

func batchAddReferences(normalized map[string]any, refs map[string][]string) {
	encoded, _ := json.Marshal(normalized["_batch_references"])
	var values map[string][]string
	json.Unmarshal(encoded, &values)
	for typ, ids := range values {
		for _, id := range ids {
			addReference(refs, typ, id)
		}
	}
}
