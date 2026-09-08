package azure

import (
	"context"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (r *Runtime) List(ctx context.Context, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	if request.Source != "" && request.Source != inventorySource {
		return contracts.InventoryBatch{}, fmt.Errorf("unsupported Azure inventory source")
	}
	c, err := r.resolve(ctx, request.ConnectionID)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	path := c.root() + "/resources"
	endpoint := apiURL(path, resourcesVersion)
	if request.Cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if err != nil || len(decoded) > 16<<10 {
			return contracts.InventoryBatch{}, fmt.Errorf("invalid Azure inventory cursor")
		}
		endpoint = string(decoded)
	}
	values, next, err := c.listPage(ctx, endpoint, path)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	groups, err := c.listAll(ctx, c.root()+"/resourcegroups", resourcesVersion)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	locks, err := c.listAll(ctx, c.root()+"/providers/Microsoft.Authorization/locks", locksVersion)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	groupOwners := map[string]string{}
	for _, value := range groups {
		group := object(value)
		id, _, err := parseID(text(group["id"]))
		if err != nil || !strings.HasPrefix(id, c.root()+"/") {
			return contracts.InventoryBatch{}, fmt.Errorf("invalid Azure resource group")
		}
		groupOwners[id] = text(group["managedBy"])
	}
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{}, Complete: next == ""}
	if next != "" {
		batch.NextCursor = base64.RawURLEncoding.EncodeToString([]byte(next))
	}
	region := strings.ToLower(request.Scope.NativeID)
	if request.Scope.Kind == asset.ScopeGlobal {
		region = "global"
	}
	seen := map[string]bool{}
	appendItem := func(raw map[string]any) error {
		item, err := r.inventoryItem(ctx, c, raw, groupOwners, locks)
		if err != nil {
			return err
		}
		if request.ResourceKind != nil && !strings.EqualFold(item.NativeType, request.ResourceKind.NativeType) {
			return nil
		}
		if !seen[item.NativeID] {
			batch.Items = append(batch.Items, item)
			seen[item.NativeID] = true
		}
		return nil
	}
	if region == "global" && request.Cursor == "" {
		for _, value := range groups {
			raw := object(value)
			raw["type"] = groupType
			if err := appendItem(raw); err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
	}
	for _, value := range values {
		raw := object(value)
		if raw == nil {
			return contracts.InventoryBatch{}, fmt.Errorf("invalid Azure resource list item")
		}
		if resourceRegion(raw) != region {
			continue
		}
		kind, known := findType(text(raw["type"]))
		if known {
			resourceURL, err := c.resourceURL(kind, text(raw["id"]))
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			detail, err := c.request(ctx, "GET", resourceURL)
			if isNotFound(err) {
				continue
			} // Resource removed after the list snapshot.
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			if !strings.EqualFold(text(detail.data["id"]), text(raw["id"])) || !strings.EqualFold(text(detail.data["type"]), kind.NativeType) {
				return contracts.InventoryBatch{}, fmt.Errorf("Azure resource detail identity mismatch")
			}
			raw = detail.data
			if text(raw["location"]) == "" {
				raw["location"] = region
			}
		}
		if err := appendItem(raw); err != nil {
			return contracts.InventoryBatch{}, err
		}
		// ARM's subscription list omits some child resources. Enumerate the
		// children whose lifecycle and dependencies Steward models explicitly.
		children, err := c.children(ctx, kind, raw)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		for _, child := range children {
			if err := appendItem(child); err != nil {
				return contracts.InventoryBatch{}, err
			}
		}
	}
	return batch, nil
}
func resourceRegion(raw map[string]any) string {
	if strings.EqualFold(text(raw["type"]), groupType) {
		return "global"
	}
	location := strings.ToLower(text(raw["location"]))
	if location == "" {
		return "global"
	}
	return location
}
func (c *client) children(ctx context.Context, kind resourceType, raw map[string]any) ([]map[string]any, error) {
	id, _, err := parseID(text(raw["id"]))
	if err != nil {
		return nil, err
	}
	var values []any
	switch kind.NativeType {
	case vnetType:
		values, err = c.listAll(ctx, id+"/subnets", kind.Version)
	case storageType:
		values, err = c.listAll(ctx, id+"/blobServices/default/containers", kind.Version)
	case "Microsoft.Sql/servers":
		for _, child := range []string{"databases", "elasticPools"} {
			items, callErr := c.listAll(ctx, id+"/"+child, kind.Version)
			if callErr != nil {
				return nil, callErr
			}
			values = append(values, items...)
		}
	}
	if err != nil {
		return nil, err
	}
	result := []map[string]any{}
	for _, value := range values {
		child := object(value)
		childID, childType, err := parseID(text(child["id"]))
		if err != nil || !strings.HasPrefix(childID, id+"/") {
			return nil, fmt.Errorf("Azure child belongs to another parent")
		}
		known, ok := findType(childType)
		if !ok {
			return nil, fmt.Errorf("unexpected Azure child resource type")
		}
		child["type"] = known.NativeType
		if text(child["location"]) == "" {
			child["location"] = resourceRegion(raw)
		}
		result = append(result, child)
	}
	return result, nil
}
func (r *Runtime) inventoryItem(ctx context.Context, c *client, raw map[string]any, groupOwners map[string]string, locks []any) (contracts.InventoryItem, error) {
	id, parsedType, err := parseID(text(raw["id"]))
	if err != nil || !strings.HasPrefix(id, c.root()+"/") || !strings.EqualFold(parsedType, text(raw["type"])) {
		return contracts.InventoryItem{}, fmt.Errorf("Azure inventory identity mismatch")
	}
	nativeType := parsedType
	kind, known := findType(nativeType)
	if known {
		nativeType = kind.NativeType
	}
	region := resourceRegion(raw)
	scope := contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: region, Name: region, Location: region}
	if region == "global" {
		scope = contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: c.subscription + "/global", Name: "Global", Location: "global"}
	}
	safe := object(safeResource(raw))
	normalized := map[string]any{}
	for key, value := range object(safe["properties"]) {
		normalized[key] = value
	}
	for _, key := range []string{"sku", "kind", "zones", "managedBy"} {
		if value, ok := safe[key]; ok {
			normalized[key] = value
		}
	}
	parts := strings.Split(id, "/")
	groupID := strings.Join(parts[:5], "/")
	normalized["subscription_id"] = c.subscription
	normalized["resource_group"] = parts[4]
	normalized["_inventory_source"] = inventorySource
	if zones := array(raw["zones"]); len(zones) > 0 {
		normalized["zone_id"] = fmt.Sprint(zones[0])
	}
	reason := protectionReason(kind, raw)
	if groupOwners[groupID] != "" {
		reason = "azure_managed_resource_group"
	}
	if locked(id, locks) {
		reason = "azure_management_lock"
	}
	if reason != "" {
		normalized["cleanup_protected"] = true
		normalized["cleanup_protection_reason"] = reason
	}
	refs := references(nativeType, id, raw)
	if nativeType == vmType {
		// VM placement is carried by its NICs, not by the VM ARM document.
		nicKind, _ := findType(nicType)
		for _, nicID := range refs[nicType] {
			endpoint, err := c.resourceURL(nicKind, nicID)
			if err != nil {
				return contracts.InventoryItem{}, err
			}
			nic, err := c.request(ctx, "GET", endpoint)
			if err != nil {
				return contracts.InventoryItem{}, err
			}
			if !strings.EqualFold(text(nic.data["id"]), nicID) {
				return contracts.InventoryItem{}, fmt.Errorf("Azure NIC identity mismatch")
			}
			for target, ids := range references(nicType, nicID, nic.data) {
				if target == vnetType || target == subnetType {
					for _, ref := range ids {
						addReference(refs, target, ref)
					}
				}
			}
		}
	}
	networkRefs := []string{}
	for target, ids := range refs {
		sort.Strings(ids)
		normalized[referenceKey(target)] = ids
		networkRefs = append(networkRefs, ids...)
	}
	sort.Strings(networkRefs)
	if ids := refs[vnetType]; len(ids) == 1 {
		normalized["vpc_id"] = ids[0]
	}
	if ids := refs[subnetType]; len(ids) > 0 {
		normalized["subnet_ids"] = ids
		if len(ids) == 1 {
			normalized["vswitch_id"] = ids[0]
		}
	}
	if nativeType == vnetType {
		normalized["vpc_id"] = id
	}
	if nativeType == subnetType {
		normalized["vswitch_id"] = id
	}
	tags := map[string]string{}
	for key, value := range object(raw["tags"]) {
		if s, ok := value.(string); ok {
			tags[key] = s
		}
	}
	actionable := known && !kind.ReadOnly
	return contracts.InventoryItem{NativeID: id, NativeType: nativeType, ResourceKind: r.resourceKind(nativeType), Actionable: &actionable, Scope: scope,
		Name: text(raw["name"]), Location: region, State: text(object(raw["properties"])["provisioningState"]), Tags: tags, Normalized: normalized, Raw: safe,
		NativeAliases: []string{text(raw["id"]), id}, NetworkReferences: networkRefs}, nil
}

func addReference(refs map[string][]string, target, id string) {
	for _, existing := range refs[target] {
		if existing == id {
			return
		}
	}
	refs[target] = append(refs[target], id)
}
func references(nativeType, self string, raw map[string]any) map[string][]string {
	result := map[string][]string{}
	add := func(value string) {
		id, target, err := parseID(value)
		if err != nil || id == self {
			return
		}
		// Backend pools and similar embedded subresources resolve to their
		// modeled ARM parent. Reverse child lists are excluded below.
		for {
			if kind, known := findType(target); known {
				addReference(result, kind.NativeType, id)
				break
			}
			parts := strings.Split(id, "/")
			if len(parts) <= 9 {
				break
			}
			id, target, err = parseID(strings.Join(parts[:len(parts)-2], "/"))
			if err != nil {
				break
			}
		}
	}
	fields := map[string]bool{"subnet": true, "virtualnetwork": true, "networksecuritygroup": true, "routetable": true, "natgateway": true,
		"publicipaddress": true, "publicipaddresses": true, "publicipprefix": true, "publicipprefixes": true,
		"networkinterfaces": true, "manageddisk": true, "availabilityset": true, "diskencryptionset": true,
		"loadbalancerbackendaddresspools": true, "applicationgatewaybackendaddresspools": true,
		"serverfarmid": true, "virtualnetworksubnetid": true, "subnetresourceid": true, "managedenvironmentid": true, "environmentid": true,
		"elasticpoolid": true, "vnetsubnetid": true, "delegatedsubnetresourceid": true, "keyvaultid": true}
	var visit func(any, string)
	visit = func(value any, parent string) {
		switch typed := value.(type) {
		case map[string]any:
			if fields[strings.ToLower(parent)] {
				add(text(typed["id"]))
			}
			for key, value := range typed {
				switch key {
				case "subnets", "virtualMachines", "backendIPConfigurations", "privateEndpointConnections", "source", "creationData", "imageReference":
					continue
				case "ipConfigurations":
					if strings.EqualFold(nativeType, subnetType) {
						continue
					}
				}
				visit(value, key)
			}
		case []any:
			for _, value := range typed {
				visit(value, parent)
			}
		case string:
			if fields[strings.ToLower(parent)] {
				add(typed)
			}
		}
	}
	visit(object(raw["properties"]), "")
	for identity := range object(object(raw["identity"])["userAssignedIdentities"]) {
		add(identity)
	}
	// Explicit child -> parent edges order child deletion before its parent.
	parts := strings.Split(self, "/")
	if len(parts) > 9 {
		parent := strings.Join(parts[:len(parts)-2], "/")
		if strings.EqualFold(nativeType, containerType) {
			parent = strings.Join(parts[:9], "/")
		}
		add(parent)
	}
	if strings.EqualFold(nativeType, subnetType) {
		add(strings.Join(parts[:len(parts)-2], "/"))
	}
	for _, id := range result[subnetType] {
		parts := strings.Split(id, "/")
		addReference(result, vnetType, strings.Join(parts[:len(parts)-2], "/"))
	}
	return result
}

func locked(id string, locks []any) bool {
	for _, value := range locks {
		lock := object(value)
		level := strings.ToLower(text(object(lock["properties"])["level"]))
		if level != "cannotdelete" && level != "readonly" {
			continue
		}
		lockID := strings.ToLower(text(lock["id"]))
		index := strings.LastIndex(lockID, "/providers/microsoft.authorization/locks/")
		if index < 0 {
			continue
		}
		scope := lockID[:index]
		if id == scope || strings.HasPrefix(id, scope+"/") || strings.HasPrefix(scope, id+"/") {
			return true
		}
	}
	return false
}

// Environment and configuration values can hold application secrets even when
// their keys are innocuous. Do not persist them in inventory or diagnostics.
func safeResource(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		result := map[string]any{}
		for key, value := range typed {
			switch strings.ToLower(strings.ReplaceAll(key, "_", "")) {
			case "password", "adminpassword", "secret", "secrets", "clientsecret", "accesskey", "connectionstring", "connectionstrings",
				"appsettings", "env", "environmentvariables", "customdata", "userdata", "protectedsettings", "protectedsettingsfromkeyvault":
				continue
			}
			result[key] = safeResource(value)
		}
		return result
	case []any:
		result := make([]any, len(typed))
		for i, item := range typed {
			result[i] = safeResource(item)
		}
		return result
	default:
		return value
	}
}
func (r *Runtime) SearchNetworkTargets(ctx context.Context, query contracts.NetworkTargetQuery) (contracts.NetworkTargetPage, error) {
	nativeType := vnetType
	if query.Kind == asset.ScanTargetVSwitch {
		nativeType = subnetType
	} else if query.Kind != asset.ScanTargetVPC {
		return contracts.NetworkTargetPage{}, fmt.Errorf("unsupported Azure network target")
	}
	kind := r.resourceKind(nativeType)
	batch, err := r.List(ctx, contracts.InventoryRequest{ConnectionID: query.ConnectionID, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: query.RegionID},
		Source: inventorySource, ResourceKind: &kind, Cursor: query.Cursor, Limit: query.Limit})
	if err != nil {
		return contracts.NetworkTargetPage{}, err
	}
	page := contracts.NetworkTargetPage{Items: []contracts.NetworkTargetOption{}, NextCursor: batch.NextCursor}
	for _, item := range batch.Items {
		parent := text(item.Normalized["vpc_id"])
		if query.ParentNativeID != "" && !strings.EqualFold(parent, query.ParentNativeID) {
			continue
		}
		if query.Query != "" && !strings.Contains(strings.ToLower(item.Name+" "+item.NativeID), strings.ToLower(query.Query)) {
			continue
		}
		page.Items = append(page.Items, contracts.NetworkTargetOption{Kind: query.Kind, RegionID: query.RegionID, NativeID: item.NativeID, Name: item.Name, ParentNativeID: parent})
	}
	return page, nil
}
