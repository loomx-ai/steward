package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func defenderParentKind(kind string) resourceType {
	if kind == defenderArcType {
		return resourceType{NativeType: kind, Version: "2025-01-13", ReadOperations: []string{"Azure.Microsoft.HybridCompute.Machines_Get"}, ListOperations: []string{"Azure.Microsoft.HybridCompute.Machines_ListBySubscription"}}
	}
	mapping, _ := findType(kind)
	return mapping
}

func (c *client) defenderParentIndex(ctx context.Context, kind string) ([]any, error) {
	mapping := defenderParentKind(kind)
	path := c.root() + "/providers/" + kind
	rows, seen := []any{}, map[string]bool{}
	for next := apiURL(path, mapping.Version); next != ""; {
		u, err := url.Parse(next)
		if err != nil || seen[next] {
			return nil, serviceDenied("invalid_defender_parent_cursor")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || query.Get("api-version") != mapping.Version {
			return nil, serviceDenied("defender_parent_version_changed")
		}
		for key, values := range query {
			if len(values) != 1 || values[0] == "" || !slices.Contains([]string{"api-version", "$skiptoken", "$skipToken", "skipToken", "continuationToken"}, key) {
				return nil, serviceDenied("filtered_defender_parent_index")
			}
		}
		seen[next] = true
		page, cursor, err := c.listPage(ctx, next, path)
		if err != nil {
			return nil, err
		}
		rows, next = append(rows, page...), cursor
	}
	return rows, nil
}

func (c *client) defenderParent(ctx context.Context, scope string) (map[string]any, error) {
	if err := c.defenderScope(scope); err != nil {
		return nil, err
	}
	if scope == c.root() {
		return c.subscriptionIdentity(ctx)
	}
	_, kind, _ := parseID(scope)
	for _, candidate := range defenderScopeKinds {
		if strings.EqualFold(kind, candidate) {
			kind = candidate
			break
		}
	}
	mapping := defenderParentKind(kind)
	endpoint, err := c.resourceURL(mapping, scope)
	if err != nil {
		return nil, err
	}
	res, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, err
	}
	if res.status != 200 || !validResourceResponse(res, scope, kind) {
		return nil, serviceDenied("invalid_defender_parent")
	}
	return res.data, nil
}

func (c *client) defenderPricingIndex(ctx context.Context, scope string) (map[string]map[string]any, string, error) {
	_, parentKind, _ := parseID(scope)
	if strings.EqualFold(parentKind, aksType) || strings.EqualFold(parentKind, "Microsoft.ContainerRegistry/registries") {
		// The native LIST contract names VM, VMSS and Arc machine scopes only.
		// Container examples establish this named plan on AKS and ACR instead.
		res, err := c.defenderRead(ctx, scope+"/providers/"+defenderPricingType+"/Containers")
		if isNotFound(err) {
			return map[string]map[string]any{}, res.requestID, nil
		}
		if err != nil {
			return nil, "", err
		}
		id, _, _ := c.defenderIdentity(text(res.data["id"]))
		return map[string]map[string]any{id: res.data}, res.requestID, nil
	}
	op, params, err := c.defenderOperation(scope, "", "GET")
	if err != nil {
		return nil, "", err
	}
	request, err := bindAzureREST(op, params)
	if err != nil {
		return nil, "", err
	}
	res, err := c.requestBody(ctx, request.Method, request.URL, request.Body, request.Headers)
	if err != nil {
		return nil, "", err
	}
	rows, ok := res.data["value"].([]any)
	if res.status != 200 || !ok || res.data["nextLink"] != nil || operationLocation(res.header) != "" {
		return nil, "", serviceDenied("invalid_defender_index")
	}
	values := map[string]map[string]any{}
	// Rows are checked in order before any read; rows at or after the first
	// invalid one are not read, and its error follows earlier read results.
	var invalid error
	var ids []string
	seen := map[string]bool{}
	for _, row := range rows {
		raw := object(row)
		id, parent, err := c.defenderIdentity(text(raw["id"]))
		if err != nil || parent != scope || seen[id] || !strings.EqualFold(text(raw["name"]), last(id)) || !strings.EqualFold(text(raw["type"]), defenderPricingType) || defenderProperties(raw) != nil {
			invalid = serviceDenied("invalid_defender_index_member")
			break
		}
		seen[id] = true
		ids = append(ids, id)
	}
	lives, errs := readConcurrently(len(ids), func(i int) (response, error) { return c.defenderRead(ctx, text(object(rows[i])["id"])) })
	for i, id := range ids {
		live, err := lives[i], errs[i]
		if err != nil {
			return nil, "", err
		}
		if c.privateConfiguration(defenderSnapshot(object(rows[i]))) != c.privateConfiguration(defenderSnapshot(live.data)) {
			return nil, "", serviceDenied("defender_index_member_changed")
		}
		values[id] = live.data
	}
	if invalid != nil {
		return nil, "", invalid
	}
	return values, res.requestID, nil
}

func (r *Runtime) defenderSnapshot(ctx context.Context, c *client, request contracts.InventoryRequest) ([]contracts.InventoryItem, map[string]any, string, error) {
	parents := map[string]map[string]any{}
	subscription, err := c.defenderParent(ctx, c.root())
	if err != nil {
		return nil, nil, "", err
	}
	parents[c.root()] = subscription
	for _, kind := range defenderScopeKinds {
		rows, err := c.defenderParentIndex(ctx, kind)
		if err != nil {
			return nil, nil, "", err
		}
		// Rows are checked in order before any read; rows at or after the
		// first invalid one are not read, and its error follows earlier reads.
		var invalid error
		var ids []string
		seen := map[string]bool{}
		for _, row := range rows {
			raw := object(row)
			id, typ, err := parseID(text(raw["id"]))
			if err != nil || !strings.EqualFold(typ, kind) || !strings.EqualFold(text(raw["type"]), kind) || parents[id] != nil || seen[id] {
				invalid = serviceDenied("invalid_defender_parent_index")
				break
			}
			seen[id] = true
			ids = append(ids, id)
		}
		currents, errs := readConcurrently(len(ids), func(i int) (map[string]any, error) { return c.defenderParent(ctx, ids[i]) })
		for i, id := range ids {
			if errs[i] != nil {
				return nil, nil, "", errs[i]
			}
			if serviceListedIncarnation(object(rows[i]), currents[i]) != nil {
				return nil, nil, "", serviceDenied("defender_parent_changed")
			}
			parents[id] = currents[i]
		}
		if invalid != nil {
			return nil, nil, "", invalid
		}
	}
	known, values := map[string]bool{}, map[string]map[string]any{}
	// Known IDs are checked in order before any read, as in the serial walk.
	var invalid error
	var knownIDs, scopes []string
	for _, id := range request.KnownNativeIDs {
		canonical, scope, err := c.defenderIdentity(id)
		if err != nil || canonical != id || known[id] {
			invalid = serviceDenied("invalid_defender_known_identity")
			break
		}
		known[id] = true
		knownIDs, scopes = append(knownIDs, id), append(scopes, scope)
	}
	lives, errs := readConcurrently(len(knownIDs), func(i int) (response, error) { return c.defenderRead(ctx, knownIDs[i]) })
	for i, id := range knownIDs {
		scope, live, err := scopes[i], lives[i], errs[i]
		if isNotFound(err) {
			continue
		}
		if err != nil {
			return nil, nil, "", err
		}
		values[id] = live.data
		if parents[scope] == nil {
			parent, err := c.defenderParent(ctx, scope)
			if err != nil {
				return nil, nil, "", err
			}
			parents[scope] = parent
		}
	}
	if invalid != nil {
		return nil, nil, "", invalid
	}
	for id := range request.KnownNativeMetadata {
		if !known[id] {
			return nil, nil, "", serviceDenied("unrelated_defender_known_metadata")
		}
	}
	provenance := ""
	type pricingIndex struct {
		values    map[string]map[string]any
		requestID string
	}
	scopes = slices.Sorted(maps.Keys(parents))
	indexes, errs := readConcurrently(len(scopes), func(i int) (pricingIndex, error) {
		index, requestID, err := c.defenderPricingIndex(ctx, scopes[i])
		return pricingIndex{index, requestID}, err
	})
	for i := range scopes {
		index, requestID, err := indexes[i].values, indexes[i].requestID, errs[i]
		if err != nil {
			return nil, nil, "", err
		}
		for id, raw := range index {
			if previous := values[id]; previous != nil && c.privateConfiguration(defenderSnapshot(previous)) != c.privateConfiguration(defenderSnapshot(raw)) {
				return nil, nil, "", serviceDenied("defender_known_plan_changed")
			}
			values[id] = raw
		}
		if requestID != "" {
			provenance = requestID
		}
	}
	// Recover the specifically referenced subscription plan even if its list
	// omitted it. Inheritance is a typed native reference, not guessed coverage.
	for _, id := range slices.Sorted(maps.Keys(values)) {
		raw := values[id]
		_, scope, _ := c.defenderIdentity(id)
		props := object(raw["properties"])
		if inherited, ok := props["inheritedFrom"].(string); ok && inherited != "" {
			if props["inherited"] != "True" || !strings.EqualFold(inherited, c.root()) || scope == c.root() {
				return nil, nil, "", serviceDenied("invalid_defender_inherited_scope")
			}
			parentID := c.root() + "/providers/microsoft.security/pricings/" + last(id)
			if values[parentID] == nil {
				parent, err := c.defenderRead(ctx, parentID)
				if err != nil {
					return nil, nil, "", err
				}
				values[parentID] = parent.data
			}
		}
		if props["inherited"] == "True" && text(props["inheritedFrom"]) == "" {
			return nil, nil, "", serviceDenied("missing_defender_inherited_scope")
		}
	}
	items, bindings := []contracts.InventoryItem{}, map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(values)) {
		raw := values[id]
		_, scope, _ := c.defenderIdentity(id)
		props := object(raw["properties"])
		safe := safePayload(object(defenderSafeValue(raw)))
		normalized := maps.Clone(object(safe["properties"]))
		normalized["name"], normalized["state"], normalized["subscription_id"], normalized["scope_id"] = raw["name"], props["pricingTier"], c.subscription, scope
		normalized["_inventory_source"] = defenderInventorySource
		refs := map[string][]string{}
		if props["inherited"] == "True" {
			normalized[referenceKey(defenderPricingType)] = []string{c.root() + "/providers/microsoft.security/pricings/" + last(id)}
			refs[defenderPricingType] = []string{c.root() + "/providers/microsoft.security/pricings/" + last(id)}
		}
		stamp := map[string]any{"plan": defenderSnapshot(raw), "parent": diagnosticSourceStamp(parents[scope])}
		if scope == c.root() {
			stamp["parent"] = map[string]any{"id": c.root(), "tenantId": parents[scope]["tenantId"], "state": parents[scope]["state"]}
		}
		bindings[id] = c.privateConfiguration(stamp)
		normalized["_defender_configuration"] = bindings[id]
		network := []string{}
		if scope != c.root() {
			_, kind, _ := parseID(scope)
			if mapping, ok := findType(kind); ok {
				kind = mapping.NativeType
			}
			normalized[referenceKey(kind)] = []string{scope}
			refs[kind] = []string{scope}
			network = append(network, scope)
		}
		recorded := map[string]any{}
		for kind, ids := range refs {
			recorded[kind] = ids
		}
		normalized["_defender_references"] = recorded
		normalized["_defender_reference_binding"] = c.privateConfiguration(map[string]any{"id": id, "connection": request.ConnectionID, "configuration": bindings[id], "references": refs})
		// This models service entitlement/state, matching the Alibaba baseline's
		// read-only DescribeVersionConfig. Removing an override is not plan removal.
		actionable := false
		item := contracts.InventoryItem{NativeID: id, NativeType: defenderPricingType, ResourceKind: r.resourceKind(defenderPricingType), Name: text(raw["name"]), Location: "global", Scope: contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: c.subscription + "/global", Name: "Global", Location: "global"}, Actionable: &actionable, Raw: safe, Normalized: normalized, NativeAliases: []string{id, text(raw["id"])}, NetworkReferences: network}
		if productScopeMatches(request, item) {
			items = append(items, item)
		}
	}
	return items, bindings, provenance, nil
}

func (r *Runtime) listDefender(ctx context.Context, c *client, request contracts.InventoryRequest) (batch contracts.InventoryBatch, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	if request.Source != defenderInventorySource || request.ResourceKind == nil || request.ResourceKind.NativeType != defenderPricingType || len(request.Options) != 0 {
		return batch, serviceDenied("invalid_defender_inventory_request")
	}
	if request.Scope.Kind != asset.ScopeSubscription && request.Scope.Kind != asset.ScopeGlobal && request.Scope.Kind != asset.ScopeRegion || request.Scope.Kind == asset.ScopeGlobal && request.Scope.NativeID != "global" && request.Scope.NativeID != c.subscription+"/global" || request.Scope.Kind == asset.ScopeRegion && request.Scope.NativeID == "" {
		return batch, serviceDenied("invalid_defender_inventory_scope")
	}
	cursor := productCursor{}
	if request.Cursor != "" {
		encoded, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if len(request.Cursor) > 128<<10 || err != nil || json.Unmarshal(encoded, &cursor) != nil || cursor.Target < 1 || cursor.Fingerprint == "" || cursor.Next != "" || len(cursor.Seen)+len(cursor.Resources) != 0 {
			return batch, serviceDenied("invalid_defender_cursor")
		}
	}
	return r.inventorySnapshotPage(ctx, c, request, cursor, "defender_cursor_changed", func(request contracts.InventoryRequest) (inventorySnapshot, error) {
		items, before, provenance, err := r.defenderSnapshot(ctx, c, request)
		if err != nil {
			return inventorySnapshot{}, err
		}
		_, after, _, err := r.defenderSnapshot(ctx, c, request)
		if err != nil {
			return inventorySnapshot{}, err
		}
		if c.privateConfiguration(before) != c.privateConfiguration(after) {
			return inventorySnapshot{}, serviceDenied("defender_inventory_changed_during_scan")
		}
		boundary := request
		boundary.Cursor, boundary.Limit = "", 0
		fingerprint := c.privateConfiguration(map[string]any{"request": boundary, "revision": r.bundle.Revision, "bindings": before})
		snapshot := inventorySnapshot{items: items, provenance: provenance, fingerprint: fingerprint}
		for _, id := range request.KnownNativeIDs {
			if before[id] == nil {
				snapshot.absent = append(snapshot.absent, id)
			}
		}
		return snapshot, nil
	})
}
