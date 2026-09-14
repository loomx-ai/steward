package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strconv"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type synapseDataTarget struct {
	workspace synapseWorkspace
	pool      map[string]any
}
type synapseKnownData struct {
	id, workspace string
	params        map[string]any
}

func synapseDataSnapshot(d synapseDataDefinition, raw map[string]any) map[string]any {
	result := batchClone(raw)
	if !d.spark {
		return result
	}
	for _, key := range []string{"state", "result", "appId", "appInfo", "log", "pluginInfo", "errorInfo", "registeredSources"} {
		delete(result, key)
	}
	result["schedulerInfo"] = map[string]any{"submittedAt": object(raw["schedulerInfo"])["submittedAt"]}
	result["livyInfo"] = map[string]any{"jobCreationRequest": object(raw["livyInfo"])["jobCreationRequest"]}
	return result
}

func (c *client) synapseDataWorkspaces(ctx context.Context, request contracts.InventoryRequest, d synapseDataDefinition) (map[string]synapseWorkspace, []synapseKnownData, error) {
	workspaces := map[string]synapseWorkspace{}
	endpoints := map[string]string{}
	add := func(id string, listed map[string]any) error {
		if _, ok := workspaces[id]; ok {
			return nil
		}
		workspace, err := c.synapseWorkspaceRead(ctx, id)
		if err != nil {
			return err
		}
		if listed != nil && !nativeConfigurationContains(synapseSnapshot(listed), synapseSnapshot(workspace.raw)) {
			return serviceDenied("synapse_workspace_changed")
		}
		if previous := endpoints[workspace.endpoint]; previous != "" && previous != id {
			return serviceDenied("ambiguous_synapse_workspace")
		}
		workspaces[id] = workspace
		endpoints[workspace.endpoint] = id
		return nil
	}
	collection := c.root() + "/providers/Microsoft.Synapse/workspaces"
	next := apiURL(collection, synapseVersion)
	pages, seen := map[string]bool{}, map[string]bool{}
	for next != "" {
		if pages[next] {
			return nil, nil, serviceDenied("synapse_workspace_pagination_cycle")
		}
		pages[next] = true
		rows, following, _, err := c.synapsePage(ctx, next, collection, synapseType)
		if err != nil {
			return nil, nil, err
		}
		for _, row := range rows {
			raw := object(row)
			id := strings.ToLower(text(raw["id"]))
			if seen[id] {
				return nil, nil, serviceDenied("duplicate_synapse_workspace")
			}
			seen[id] = true
			if err = add(id, raw); err != nil {
				return nil, nil, err
			}
		}
		next = following
	}
	known := []synapseKnownData{}
	seen = map[string]bool{}
	for _, id := range request.KnownNativeIDs {
		canonical, params, err := c.synapseDataIdentity(id, d.kind)
		if err != nil || canonical != id || seen[id] {
			return nil, nil, serviceDenied("invalid_synapse_data_known_id")
		}
		seen[id] = true
		normalized := request.KnownNativeMetadata[id]
		workspaceID := ""
		if !d.spark {
			workspaceID = strings.Join(strings.Split(id, "/")[:9], "/")
		}
		if normalized != nil {
			if normalized["_synapse_connection"] != string(request.ConnectionID) || normalized["_synapse_endpoint"] != params["endpoint"] {
				return nil, nil, serviceDenied("synapse_data_known_connection_changed")
			}
			hint := text(normalized["_synapse_workspace"])
			if workspaceID != "" && hint != workspaceID {
				return nil, nil, serviceDenied("synapse_data_known_parent_changed")
			}
			workspaceID = hint
			// Preserve native case-sensitive API selectors while ARM IDs stay canonical.
			recorded := object(normalized["_synapse_parameters"])
			if !d.spark {
				name := text(recorded[d.parameter])
				if name == "" || !strings.EqualFold(name, text(params[d.parameter])) {
					return nil, nil, serviceDenied("synapse_data_known_selector_changed")
				}
				params[d.parameter] = name
			}
		}
		if workspaceID == "" {
			workspaceID = endpoints[text(params["endpoint"])]
		}
		if workspaceID == "" {
			return nil, nil, serviceDenied("synapse_data_known_workspace_unresolved")
		}
		if err := add(workspaceID, nil); err != nil {
			return nil, nil, err
		}
		if workspaces[workspaceID].endpoint != params["endpoint"] {
			return nil, nil, serviceDenied("synapse_data_known_endpoint_changed")
		}
		known = append(known, synapseKnownData{id: id, workspace: workspaceID, params: params})
	}
	for id := range request.KnownNativeMetadata {
		if !seen[id] {
			return nil, nil, serviceDenied("unrelated_synapse_data_known_metadata")
		}
	}
	return workspaces, known, nil
}

func (c *client) synapseDataTargets(ctx context.Context, workspace synapseWorkspace, d synapseDataDefinition, known []synapseKnownData) ([]synapseDataTarget, error) {
	if !d.spark {
		return []synapseDataTarget{{workspace: workspace}}, nil
	}
	pools := map[string]map[string]any{}
	add := func(id string, listed map[string]any) error {
		canonical, typ, err := parseID(id)
		if err != nil || synapseKind(typ) != synapseSparkType || redisParentID(canonical) != workspace.id {
			return serviceDenied("synapse_data_pool_parent_changed")
		}
		if pools[canonical] != nil {
			return nil
		}
		res, err := c.request(ctx, "GET", apiURL(id, synapseVersion))
		if err != nil {
			return err
		}
		if err = c.synapseReadResponse(res, canonical, synapseSparkType); err != nil {
			return err
		}
		if resourceRegion(res.data) != resourceRegion(workspace.raw) || listed != nil && !nativeConfigurationContains(synapseSnapshot(listed), synapseSnapshot(res.data)) {
			return serviceDenied("synapse_data_pool_changed")
		}
		pools[canonical] = res.data
		return nil
	}
	collection := workspace.id + "/bigDataPools"
	next := apiURL(collection, synapseVersion)
	pages, seen := map[string]bool{}, map[string]bool{}
	for next != "" {
		if pages[next] {
			return nil, serviceDenied("synapse_pool_pagination_cycle")
		}
		pages[next] = true
		rows, following, _, err := c.synapsePage(ctx, next, collection, synapseSparkType)
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			raw := object(row)
			id := strings.ToLower(text(raw["id"]))
			if seen[id] {
				return nil, serviceDenied("duplicate_synapse_pool")
			}
			seen[id] = true
			if err = add(id, raw); err != nil {
				return nil, err
			}
		}
		next = following
	}
	for _, hint := range known {
		if hint.workspace == workspace.id {
			if err := add(workspace.id+"/bigDataPools/"+text(hint.params["sparkPoolName"]), nil); err != nil {
				return nil, err
			}
		}
	}
	targets := []synapseDataTarget{}
	ids := slices.Sorted(maps.Keys(pools))
	for _, id := range ids {
		targets = append(targets, synapseDataTarget{workspace: workspace, pool: pools[id]})
	}
	return targets, nil
}

func synapseTargetParams(target synapseDataTarget, d synapseDataDefinition) map[string]any {
	params := map[string]any{"endpoint": target.workspace.endpoint}
	if d.spark {
		params["sparkPoolName"] = text(target.pool["name"])
		params["detailed"] = true
	}
	return params
}

func synapseObservedID(target synapseDataTarget, d synapseDataDefinition, raw map[string]any) (string, map[string]any, error) {
	params := synapseTargetParams(target, d)
	if !d.spark {
		params[d.parameter] = text(raw["name"])
		return strings.ToLower(text(raw["id"])), params, nil
	}
	id, err := batchInteger(raw["id"], 32)
	if err != nil || id < 0 {
		return "", nil, serviceDenied("invalid_synapse_spark_id")
	}
	params[d.parameter] = id
	return target.workspace.endpoint + "/livyApi/versions/" + synapseDataVersion + "/sparkPools/" + url.PathEscape(text(target.pool["name"])) + "/" + d.collection + "/" + strconv.FormatInt(id, 10), params, nil
}

func (c *synapseDataClient) synapseReadData(ctx context.Context, target synapseDataTarget, d synapseDataDefinition, params map[string]any) (response, error) {
	metadata, err := providerData()
	if err != nil {
		return response{}, err
	}
	op, _ := metadata.catalog.Operation(synapseDataOperationPrefix + d.read)
	request, err := bindAzureREST(op, params)
	if err != nil {
		return response{}, err
	}
	res, err := c.request(ctx, target.workspace, request)
	if err != nil {
		return res, err
	}
	_, err = synapseDataResponse(target.workspace, op, params, res)
	return res, err
}

func (c *synapseDataClient) synapseListData(ctx context.Context, target synapseDataTarget, d synapseDataDefinition, details bool) ([]map[string]any, string, string, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, "", "", err
	}
	op, _ := metadata.catalog.Operation(synapseDataOperationPrefix + d.list)
	params := synapseTargetParams(target, d)
	if d.spark {
		params["from"] = 0
		params["size"] = 20
	}
	request, err := bindAzureREST(op, params)
	if err != nil {
		return nil, "", "", err
	}
	result := []map[string]any{}
	index := map[string]any{}
	pages, ids := map[string]bool{}, map[string]bool{}
	total := int64(-1)
	provenance := ""
	for {
		if pages[request.URL] {
			return nil, "", "", serviceDenied("synapse_data_pagination_cycle")
		}
		pages[request.URL] = true
		res, err := c.request(ctx, target.workspace, request)
		if err != nil {
			return nil, "", "", err
		}
		if res.requestID != "" {
			provenance = res.requestID
		}
		next, err := synapseDataResponse(target.workspace, op, params, res)
		if err != nil {
			return nil, "", "", err
		}
		field := "value"
		if d.spark {
			field = "sessions"
			count, _ := batchInteger(res.data["total"], 32)
			if total >= 0 && total != count {
				return nil, "", "", serviceDenied("synapse_spark_total_changed")
			}
			total = count
		}
		for _, row := range array(res.data[field]) {
			raw := object(row)
			id, readParams, err := synapseObservedID(target, d, raw)
			if err != nil {
				return nil, "", "", err
			}
			if ids[id] {
				return nil, "", "", serviceDenied("duplicate_synapse_data_identity")
			}
			ids[id] = true
			index[id] = synapseDataSnapshot(d, raw)
			if !details {
				result = append(result, raw)
				continue
			}
			detail, err := c.synapseReadData(ctx, target, d, readParams)
			if err != nil {
				return nil, "", "", err
			}
			if !nativeConfigurationContains(synapseDataSnapshot(d, raw), synapseDataSnapshot(d, detail.data)) {
				return nil, "", "", serviceDenied("synapse_listed_data_changed")
			}
			result = append(result, detail.data)
		}
		if next == "" {
			break
		}
		if d.spark {
			offset, err := strconv.Atoi(next)
			if err != nil {
				return nil, "", "", err
			}
			params["from"] = offset
			request, err = bindAzureREST(op, params)
			if err != nil {
				return nil, "", "", err
			}
		} else {
			request.URL = next
		}
	}
	return result, provenance, c.arm.privateConfiguration(index), nil
}

func (c *synapseDataClient) verifySynapseDataTarget(ctx context.Context, target synapseDataTarget) error {
	if target.pool != nil {
		id := text(target.pool["id"])
		res, err := c.arm.request(ctx, "GET", apiURL(id, synapseVersion))
		if err != nil {
			return err
		}
		if err = c.arm.synapseReadResponse(res, id, synapseSparkType); err != nil {
			return err
		}
		if c.arm.privateConfiguration(synapseSnapshot(res.data)) != c.arm.privateConfiguration(synapseSnapshot(target.pool)) {
			return serviceDenied("synapse_data_parent_pool_changed")
		}
	}
	after, err := c.arm.synapseWorkspaceRead(ctx, target.workspace.id)
	if err != nil {
		return err
	}
	if c.arm.privateConfiguration(synapseSnapshot(after.raw)) != c.arm.privateConfiguration(synapseSnapshot(target.workspace.raw)) {
		return serviceDenied("synapse_data_parent_workspace_changed")
	}
	return nil
}

func (r *Runtime) synapseDataInventoryItem(ctx context.Context, c *synapseDataClient, target synapseDataTarget, d synapseDataDefinition, raw map[string]any, request contracts.InventoryRequest, owners map[string]string, locks []any) (contracts.InventoryItem, error) {
	id, params, err := synapseObservedID(target, d, raw)
	if err != nil {
		return contracts.InventoryItem{}, err
	}
	canonical, _, err := c.arm.synapseDataIdentity(id, d.kind)
	if err != nil || canonical != id {
		return contracts.InventoryItem{}, serviceDenied("noncanonical_synapse_data_identity")
	}
	safe := safePayload(object(synapseDataSafeValue(raw)))
	normalized := maps.Clone(safe)
	workspace := target.workspace
	region := resourceRegion(workspace.raw)
	poolID := ""
	if target.pool != nil {
		poolID = strings.ToLower(text(target.pool["id"]))
	}
	refs := map[string][]string{}
	addReference(refs, synapseType, workspace.id)
	if poolID != "" {
		addReference(refs, synapseSparkType, poolID)
	}
	if !d.spark {
		key := "bigDataPool"
		if d.kind == synapseJobDefinitionType {
			key = "targetBigDataPool"
		}
		if value, present := object(raw["properties"])[key]; present && value != nil {
			ref, ok := value.(map[string]any)
			name := text(ref["referenceName"])
			if !ok || ref["type"] != "BigDataPoolReference" || name == "" || strings.ContainsAny(name, "/\\%\x00\r\n") || name == "." || name == ".." {
				return contracts.InventoryItem{}, serviceDenied("invalid_synapse_artifact_pool_reference")
			}
			refID := workspace.id + "/bigDataPools/" + name
			kind, _ := findType(synapseSparkType)
			endpoint, err := c.arm.resourceURL(kind, refID)
			if err != nil {
				return contracts.InventoryItem{}, err
			}
			res, err := c.arm.request(ctx, "GET", endpoint)
			if isNotFound(err) {
				normalized["_synapse_unresolved_pool"] = strings.ToLower(refID)
			} else if err != nil {
				return contracts.InventoryItem{}, err
			} else if err = c.arm.synapseReadResponse(res, refID, synapseSparkType); err != nil {
				return contracts.InventoryItem{}, err
			}
			addReference(refs, synapseSparkType, strings.ToLower(refID))
			poolID = strings.ToLower(refID)
		}
	}
	normalized["_synapse_connection"] = string(request.ConnectionID)
	normalized["_synapse_workspace"] = workspace.id
	normalized["_synapse_endpoint"] = workspace.endpoint
	normalized["_synapse_pool"] = poolID
	normalized["_synapse_parameters"] = params
	normalized["_synapse_private_configuration"] = c.arm.privateConfiguration(synapseDataSnapshot(d, raw))
	normalized["_synapse_workspace_configuration"] = c.arm.privateConfiguration(synapseSnapshot(workspace.raw))
	if target.pool != nil {
		normalized["_synapse_pool_configuration"] = c.arm.privateConfiguration(synapseSnapshot(target.pool))
	}
	normalized["_inventory_source"] = synapseDataInventorySource
	normalized["subscription_id"] = c.arm.subscription
	normalized["resource_group"] = strings.Split(workspace.id, "/")[4]
	name := text(raw["name"])
	if name == "" {
		name = last(id)
	}
	state := text(raw["state"])
	if !d.spark {
		state = text(object(raw["properties"])["entityState"])
	}
	normalized["name"], normalized["state"] = name, state
	reason := "synapse_cleanup_not_implemented"
	if protectedAzureTags(object(workspace.raw["tags"])) || protectedAzureTags(object(target.pool["tags"])) || protectedAzureTags(object(raw["tags"])) {
		reason = "azure_protected_tag"
	}
	group := strings.Join(strings.Split(workspace.id, "/")[:5], "/")
	if owners[group] != "" {
		reason = "azure_managed_resource_group"
	}
	if locked(workspace.id, locks) || poolID != "" && locked(poolID, locks) || !d.spark && locked(id, locks) {
		reason = "azure_management_lock"
	}
	normalized["cleanup_protected"] = true
	normalized["cleanup_protection_reason"] = reason
	network := []string{}
	for typ, ids := range refs {
		normalized[referenceKey(typ)] = ids
		network = append(network, ids...)
	}
	slices.Sort(network)
	actionable := false
	return contracts.InventoryItem{NativeID: id, NativeType: d.kind, ResourceKind: r.resourceKind(d.kind), Scope: contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: region, Name: region, Location: region}, Name: name, State: state, Location: region, Tags: map[string]string{}, Raw: safe, Normalized: normalized, NativeAliases: []string{id}, NetworkReferences: network, Actionable: &actionable}, nil
}
func (r *Runtime) listSynapseData(ctx context.Context, c *synapseDataClient, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.ResourceKind == nil || request.Source != synapseDataInventorySource || len(request.Options) != 0 || request.Scope.Kind != asset.ScopeRegion && request.Scope.Kind != asset.ScopeSubscription {
		return batch, serviceDenied("invalid_synapse_data_inventory_request")
	}
	d := synapseDataKind(request.ResourceKind.NativeType)
	if d.kind == "" {
		return batch, serviceDenied("invalid_synapse_data_inventory_kind")
	}
	cursor := productCursor{}
	if request.Cursor != "" {
		encoded, e := base64.RawURLEncoding.DecodeString(request.Cursor)
		if e != nil || len(request.Cursor) > 128<<10 || json.Unmarshal(encoded, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_synapse_data_cursor")
		}
	}
	workspaces, known, err := c.arm.synapseDataWorkspaces(ctx, request, d)
	if err != nil {
		return batch, err
	}
	owners, locks, err := c.arm.inventoryProtection(ctx)
	if err != nil {
		return batch, err
	}
	items := []contracts.InventoryItem{}
	absent := []string{}
	bindings := map[string]any{}
	observed := map[string]bool{}
	provenance := ""
	for _, workspaceID := range slices.Sorted(maps.Keys(workspaces)) {
		workspace := workspaces[workspaceID]
		if request.Scope.Kind == asset.ScopeRegion && !strings.EqualFold(resourceRegion(workspace.raw), request.Scope.NativeID) {
			continue
		}
		bindings[workspaceID] = c.arm.privateConfiguration(synapseSnapshot(workspace.raw))
		targets, err := c.arm.synapseDataTargets(ctx, workspace, d, known)
		if err != nil {
			return batch, err
		}
		for _, target := range targets {
			if target.pool != nil {
				bindings[text(target.pool["id"])] = c.arm.privateConfiguration(synapseSnapshot(target.pool))
			}
			values, requestID, index, err := c.synapseListData(ctx, target, d, true)
			if err != nil {
				return batch, err
			}
			if requestID != "" {
				provenance = requestID
			}
			seen := map[string]bool{}
			for _, raw := range values {
				id, _, err := synapseObservedID(target, d, raw)
				if err != nil {
					return batch, err
				}
				seen[id] = true
			}
			for _, hint := range known {
				if hint.workspace != workspaceID || d.spark && !strings.EqualFold(text(hint.params["sparkPoolName"]), text(target.pool["name"])) || seen[hint.id] {
					continue
				}
				res, err := c.synapseReadData(ctx, target, d, hint.params)
				if isNotFound(err) {
					absent = append(absent, hint.id)
					continue
				}
				if err != nil {
					return batch, err
				}
				values = append(values, res.data)
			}
			for _, raw := range values {
				item, err := r.synapseDataInventoryItem(ctx, c, target, d, raw, request, owners, locks)
				if err != nil {
					return batch, err
				}
				if observed[item.NativeID] {
					return batch, serviceDenied("duplicate_synapse_data_asset")
				}
				observed[item.NativeID] = true
				// Re-read after reference resolution so graph edges never come from a
				// different artifact/job configuration than the saved observation.
				_, params, err := synapseObservedID(target, d, raw)
				if err != nil {
					return batch, err
				}
				after, err := c.synapseReadData(ctx, target, d, params)
				if err != nil {
					return batch, err
				}
				if c.arm.privateConfiguration(synapseDataSnapshot(d, raw)) != c.arm.privateConfiguration(synapseDataSnapshot(d, after.data)) {
					return batch, serviceDenied("synapse_data_configuration_changed")
				}
				items = append(items, item)
			}
			// Offset pagination has no server snapshot. Confirm the full observed
			// membership and authored list state again before returning a complete page.
			_, _, currentIndex, err := c.synapseListData(ctx, target, d, false)
			if err != nil {
				return batch, err
			}
			if currentIndex != index {
				return batch, serviceDenied("synapse_data_index_changed")
			}
			if err = c.verifySynapseDataTarget(ctx, target); err != nil {
				return batch, err
			}
		}
		// Empty pool indexes also require a fresh workspace check.
		if err = c.verifySynapseDataTarget(ctx, synapseDataTarget{workspace: workspace}); err != nil {
			return batch, err
		}
	}
	slices.SortFunc(items, func(a, b contracts.InventoryItem) int { return strings.Compare(a.NativeID, b.NativeID) })
	slices.Sort(absent)
	boundary := request
	boundary.Cursor, boundary.Limit = "", 0
	fingerprint := c.arm.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "items": items, "absent": absent, "parents": bindings})
	if request.Cursor != "" && (cursor.Fingerprint != fingerprint || cursor.Target >= len(items)) {
		return batch, serviceDenied("synapse_data_cursor_changed")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 1000
	}
	end := cursor.Target + min(limit, len(items)-cursor.Target)
	batch = contracts.InventoryBatch{Items: items[cursor.Target:end], Complete: end == len(items), RequestID: provenance}
	if batch.Complete {
		batch.AbsentNativeIDs = absent
	} else {
		cursor.Fingerprint, cursor.Target = fingerprint, end
		encoded, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	return batch, nil
}
