package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	dataformFolderType     = "dataform.googleapis.com/Folder"
	dataformTeamFolderType = "dataform.googleapis.com/TeamFolder"
	dataformContainerChain = "_dataform_container_chain"
)

func isDataformFolder(kind string) bool {
	return kind == dataformFolderType || kind == dataformTeamFolderType
}

func dataformLocation(id string) string {
	parts := strings.Split(strings.TrimPrefix(id, "//dataform.googleapis.com/"), "/")
	if len(parts) < 4 {
		return ""
	}
	return strings.Join(parts[:4], "/")
}

func (c *client) dataformFolderContainer(kind, id string, raw map[string]any) (asset.Identity, error) {
	if kind != dataformFolderType && kind != dataformRepositoryType {
		return asset.Identity{}, nil
	}
	name, ok := raw["containingFolder"].(string)
	if raw["containingFolder"] != nil && !ok {
		return asset.Identity{}, groupDenied("invalid_dataform_container")
	}
	team, valid := raw["teamFolderName"].(string)
	if raw["teamFolderName"] != nil && !valid {
		return asset.Identity{}, groupDenied("invalid_dataform_team_folder")
	}
	if team != "" {
		resource, _ := findType(dataformTeamFolderType)
		idTeam := c.canonicalName("//dataform.googleapis.com/" + team)
		if _, err := c.resourceURL(resource, idTeam); err != nil || dataformLocation(idTeam) != dataformLocation(id) || name == "" {
			return asset.Identity{}, groupDenied("invalid_dataform_team_folder")
		}
	}
	if name == "" {
		return asset.Identity{}, nil
	}
	parent := c.canonicalName("//dataform.googleapis.com/" + name)
	for _, nativeType := range []string{dataformFolderType, dataformTeamFolderType} {
		resource, _ := findType(nativeType)
		if _, err := c.resourceURL(resource, parent); err == nil && dataformLocation(parent) == dataformLocation(id) && parent != id {
			return asset.Identity{Provider: asset.ProviderGCP, NativeType: nativeType, NativeID: parent}, nil
		}
	}
	return asset.Identity{}, groupDenied("invalid_dataform_container")
}

func (c *client) dataformFolderRelation(parent asset.Identity, parentData map[string]any, kind, id string, raw map[string]any) error {
	container, err := c.dataformFolderContainer(kind, id, raw)
	if err != nil || container.NativeID != parent.NativeID || container.NativeType != parent.NativeType {
		return groupDenied("dataform_folder_membership_changed")
	}
	team := ""
	if parent.NativeType == dataformTeamFolderType {
		team = strings.TrimPrefix(parent.NativeID, "//dataform.googleapis.com/")
	} else {
		team = text(parentData["teamFolderName"])
	}
	actual, valid := raw["teamFolderName"].(string)
	if raw["teamFolderName"] != nil && !valid {
		return groupDenied("invalid_dataform_team_folder")
	}
	canonical := func(name string) string {
		if name == "" {
			return ""
		}
		return c.canonicalName("//dataform.googleapis.com/" + name)
	}
	if canonical(actual) != canonical(team) {
		return groupDenied("dataform_team_folder_changed")
	}
	return nil
}

func (c *client) dataformRead(ctx context.Context, kind, id string, planned map[string]any) (map[string]any, error) {
	resource, known := findType(kind)
	if !known {
		return nil, groupDenied("invalid_dataform_resource_type")
	}
	endpoint, err := c.resourceURL(resource, id)
	if err != nil {
		return nil, err
	}
	live, err := c.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	if c.canonicalName("//dataform.googleapis.com/"+text(live["name"])) != id {
		return nil, groupDenied("dataform_identity_changed")
	}
	if planned != nil {
		if err := dataformSameResource(kind, planned, live); err != nil {
			return nil, err
		}
	}
	return live, nil
}

func (c *client) dataformEntry(entry map[string]any, location string) (serviceChild, error) {
	if len(entry) != 1 {
		return serviceChild{}, groupDenied("invalid_dataform_contents_entry")
	}
	for field, kind := range map[string]string{"folder": dataformFolderType, "repository": dataformRepositoryType, "teamFolder": dataformTeamFolderType} {
		raw := object(entry[field])
		if raw == nil {
			continue
		}
		id := c.canonicalName("//dataform.googleapis.com/" + text(raw["name"]))
		resource, _ := findType(kind)
		if _, err := c.resourceURL(resource, id); err != nil || dataformLocation(id) != location {
			return serviceChild{}, groupDenied("invalid_dataform_contents_identity")
		}
		return serviceChild{kind: kind, id: id, data: raw, direct: true}, nil
	}
	return serviceChild{}, groupDenied("invalid_dataform_contents_entry")
}

func (c *client) dataformQuery(ctx context.Context, operationID, parameter, name, items string) ([]map[string]any, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	operation, known := metadata.catalog.Operation(operationID)
	if !known {
		return nil, fmt.Errorf("missing Dataform contents operation")
	}
	return c.nativeList(ctx, operation, map[string]any{parameter: name, "pageSize": 100}, items)
}

func (c *client) dataformFolderEntries(ctx context.Context, parent asset.Identity) ([]serviceChild, error) {
	operation, parameter := "dataform.projects.locations.folders.queryFolderContents", "folder"
	if parent.NativeType == dataformTeamFolderType {
		operation, parameter = "dataform.projects.locations.teamFolders.queryContents", "teamFolder"
	}
	entries, err := c.dataformQuery(ctx, operation, parameter, strings.TrimPrefix(parent.NativeID, "//dataform.googleapis.com/"), "entries")
	if err != nil {
		return nil, err
	}
	result := []serviceChild{}
	seen := map[string]bool{}
	for _, entry := range entries {
		child, err := c.dataformEntry(entry, dataformLocation(parent.NativeID))
		if err != nil {
			return nil, err
		}
		if child.kind == dataformTeamFolderType || seen[child.id] {
			return nil, groupDenied("invalid_dataform_contents_entry")
		}
		seen[child.id] = true
		result = append(result, child)
	}
	return result, nil
}

func (c *client) dataformFolderChildren(ctx context.Context, parent asset.Identity, data map[string]any) ([]serviceChild, error) {
	live, err := c.dataformRead(ctx, parent.NativeType, parent.NativeID, data)
	if err != nil {
		return nil, err
	}
	children, err := c.dataformFolderEntries(ctx, parent)
	if err != nil {
		return nil, err
	}
	seen := map[string]string{}
	for i := range children {
		child := &children[i]
		child.data, err = c.dataformRead(ctx, child.kind, child.id, child.data)
		if err != nil {
			return nil, err
		}
		if err := c.dataformFolderRelation(parent, live, child.kind, child.id, child.data); err != nil {
			return nil, err
		}
		seen[child.id] = dataformConfiguration(child.kind, child.data)
	}
	again, err := c.dataformFolderEntries(ctx, parent)
	if err != nil {
		return nil, err
	}
	for _, child := range again {
		if seen[child.id] == "" || seen[child.id] != dataformConfiguration(child.kind, child.data) {
			return nil, groupDenied("dataform_membership_changed")
		}
		delete(seen, child.id)
	}
	if len(seen) != 0 {
		return nil, groupDenied("dataform_membership_changed")
	}
	if _, err := c.dataformRead(ctx, parent.NativeType, parent.NativeID, live); err != nil {
		return nil, err
	}
	sort.Slice(children, func(i, j int) bool { return children[i].id < children[j].id })
	return children, nil
}

type dataformContainerProof struct{ Name, Kind, Configuration string }

// Moving an ancestor changes the reviewed tree without changing repository or
// invocation configuration. Bind every native container before any child write.
func (c *client) dataformContainers(ctx context.Context, kind, id string, raw map[string]any) ([]dataformContainerProof, error) {
	var result []dataformContainerProof
	seen := map[string]bool{id: true}
	for {
		parent, err := c.dataformFolderContainer(kind, id, raw)
		if err != nil {
			return nil, err
		}
		if parent.NativeID == "" {
			return result, nil
		}
		if seen[parent.NativeID] {
			return nil, groupDenied("dataform_folder_cycle")
		}
		seen[parent.NativeID] = true
		live, err := c.dataformRead(ctx, parent.NativeType, parent.NativeID, nil)
		if err != nil {
			return nil, err
		}
		if err := c.dataformFolderRelation(parent, live, kind, id, raw); err != nil {
			return nil, err
		}
		result = append(result, dataformContainerProof{parent.NativeID, parent.NativeType, dataformConfiguration(parent.NativeType, live)})
		kind, id, raw = parent.NativeType, parent.NativeID, live
	}
}

func dataformContainersHash(proofs []dataformContainerProof) string {
	if len(proofs) == 0 {
		return ""
	}
	payload, _ := json.Marshal(proofs)
	return fmt.Sprintf("%x", sha256.Sum256(payload))
}

func (c *client) enrichDataformContainer(ctx context.Context, item *contracts.InventoryItem, raw map[string]any) error {
	proofs, err := c.dataformContainers(ctx, item.NativeType, item.NativeID, raw)
	if err != nil || len(proofs) == 0 {
		return err
	}
	parent := proofs[0]
	item.Normalized["_dataform_container_name"] = parent.Name
	item.Normalized["_dataform_container_configuration"] = parent.Configuration
	item.Normalized[dataformContainerChain] = dataformContainersHash(proofs)
	item.Normalized[referenceKey(parent.Kind)] = []any{parent.Name}
	item.NetworkReferences = append(item.NetworkReferences, parent.Name)
	return nil
}

func (a *action) verifyDataformContainer(ctx context.Context, request contracts.ActionRequest, live map[string]any) error {
	proofs, err := a.client.dataformContainers(ctx, a.kind.NativeType, request.Asset.Identity.NativeID, live)
	if err != nil {
		return contracts.DependencyReadError(err)
	}
	if dataformContainersHash(proofs) != text(request.Asset.Normalized[dataformContainerChain]) {
		return groupDenied("dataform_container_chain_changed")
	}
	if len(proofs) > 0 && (text(request.Asset.Normalized["_dataform_container_configuration"]) != proofs[0].Configuration || text(request.Asset.Normalized["_dataform_container_name"]) != proofs[0].Name) {
		return groupDenied("dataform_container_proof_missing")
	}
	return nil
}

// There is no project-wide Folder.list method. Team-folder searches and user
// root contents are visibility-filtered seeds; physical nesting is independently
// verified from containingFolder and native parent content queries.
func (c *client) dataformFolderForest(ctx context.Context, locations []string, includeUserFolders bool) (map[string]serviceChild, error) {
	nodes := map[string]serviceChild{}
	var queue []string
	var add func(serviceChild) error
	add = func(node serviceChild) error {
		if old, exists := nodes[node.id]; exists {
			if old.kind != node.kind {
				return groupDenied("dataform_resource_type_changed")
			}
			return dataformSameResource(node.kind, old.data, node.data)
		}
		nodes[node.id] = node
		if isDataformFolder(node.kind) {
			queue = append(queue, node.id)
		}
		parent, err := c.dataformFolderContainer(node.kind, node.id, node.data)
		if err != nil {
			return err
		}
		if parent.NativeID != "" {
			raw, err := c.dataformRead(ctx, parent.NativeType, parent.NativeID, nil)
			if err != nil {
				return err
			}
			if err := c.dataformFolderRelation(parent, raw, node.kind, node.id, node.data); err != nil {
				return err
			}
			if err := add(serviceChild{kind: parent.NativeType, id: parent.NativeID, data: raw}); err != nil {
				return err
			}
		}
		return nil
	}
	for _, location := range locations {
		entries, err := c.dataformQuery(ctx, "dataform.projects.locations.teamFolders.search", "location", location, "results")
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, entry := range entries {
			node, err := c.dataformEntry(entry, location)
			if err != nil {
				return nil, err
			}
			if node.kind != dataformTeamFolderType || seen[node.id] {
				return nil, groupDenied("invalid_dataform_team_search")
			}
			seen[node.id] = true
			node.data, err = c.dataformRead(ctx, node.kind, node.id, node.data)
			if err != nil {
				return nil, err
			}
			if err := add(node); err != nil {
				return nil, err
			}
		}
		if !includeUserFolders {
			continue
		}
		entries, err = c.dataformQuery(ctx, "dataform.projects.locations.queryUserRootContents", "location", location, "entries")
		if err != nil {
			return nil, err
		}
		seen = map[string]bool{}
		for _, entry := range entries {
			node, err := c.dataformEntry(entry, location)
			if err != nil {
				return nil, err
			}
			if node.kind == dataformTeamFolderType || seen[node.id] {
				return nil, groupDenied("invalid_dataform_user_root")
			}
			seen[node.id] = true
			node.data, err = c.dataformRead(ctx, node.kind, node.id, node.data)
			if err != nil {
				return nil, err
			}
			if err := add(node); err != nil {
				return nil, err
			}
		}
	}
	if !includeUserFolders {
		return nodes, nil
	}
	for i := 0; i < len(queue); i++ {
		node := nodes[queue[i]]
		parent := asset.Identity{Provider: asset.ProviderGCP, NativeType: node.kind, NativeID: node.id}
		children, err := c.dataformFolderChildren(ctx, parent, node.data)
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			if err := add(child); err != nil {
				return nil, err
			}
		}
	}
	// Nodes can be shared by search and root-content seed lists. Their single
	// physical parent must still form an acyclic tree within this location.
	for _, node := range nodes {
		seen := map[string]bool{}
		for {
			if seen[node.id] {
				return nil, groupDenied("dataform_folder_cycle")
			}
			seen[node.id] = true
			parent, err := c.dataformFolderContainer(node.kind, node.id, node.data)
			if err != nil {
				return nil, err
			}
			if parent.NativeID == "" {
				break
			}
			var exists bool
			node, exists = nodes[parent.NativeID]
			if !exists {
				return nil, groupDenied("dataform_container_missing")
			}
		}
	}
	return nodes, nil
}

func (r *Runtime) listDataformFolders(ctx context.Context, c *client, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	if request.ResourceKind == nil || !isDataformFolder(request.ResourceKind.NativeType) || (request.Scope.Kind != asset.ScopeProject && request.Scope.Kind != asset.ScopeRegion) {
		return contracts.InventoryBatch{}, fmt.Errorf("invalid Dataform folder inventory scope")
	}
	metadata, err := providerData()
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	operation, _ := metadata.catalog.Operation("dataform.projects.locations.repositories.list")
	available, supported, err := c.productLocations(ctx, operation)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	if !supported {
		return contracts.InventoryBatch{}, fmt.Errorf("missing native Dataform location discovery")
	}
	locations := []string{}
	for _, region := range available {
		if region != "global" && (request.Scope.Kind == asset.ScopeProject || regionOf(region) == request.Scope.NativeID) {
			locations = append(locations, "projects/"+c.project+"/locations/"+region)
		}
	}
	nodes, err := c.dataformFolderForest(ctx, locations, request.ResourceKind.NativeType == dataformFolderType)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	items := []contracts.InventoryItem{}
	for _, node := range nodes {
		if node.kind != request.ResourceKind.NativeType {
			continue
		}
		item, err := r.inventoryItem(c, map[string]any{"name": node.id, "assetType": node.kind, "resource": map[string]any{"data": node.data}})
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		item.Normalized["_inventory_source"] = dataformInventorySource
		if err := c.enrichDataformContainer(ctx, &item, node.data); err != nil {
			return contracts.InventoryBatch{}, err
		}
		items = append(items, item)
	}
	sort.Slice(items, func(i, j int) bool { return items[i].NativeID < items[j].NativeID })
	// Query timestamps and internal serving metadata may change between pages.
	// Bind the cursor to stable native identities/configuration of the full tree.
	type nodeProof struct{ ID, Kind, Configuration string }
	proofs := make([]nodeProof, 0, len(nodes))
	for _, node := range nodes {
		proofs = append(proofs, nodeProof{node.id, node.kind, dataformConfiguration(node.kind, node.data)})
	}
	sort.Slice(proofs, func(i, j int) bool { return proofs[i].ID < proofs[j].ID })
	bound, _ := json.Marshal(struct {
		Connection asset.ConnectionID
		Scope      asset.Scope
		Kind       string
		Network    *asset.ScanTarget
		Revision   string
		Nodes      []nodeProof
	}{request.ConnectionID, request.Scope, request.ResourceKind.NativeType, request.NetworkTarget, r.bundle.Revision, proofs})
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(bound))
	cursor := struct {
		Fingerprint string `json:"fingerprint"`
		Offset      int    `json:"offset"`
	}{Fingerprint: fingerprint}
	if request.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(request.Cursor)
		if len(request.Cursor) > 4096 || err != nil || json.Unmarshal(raw, &cursor) != nil || cursor.Fingerprint != fingerprint || cursor.Offset <= 0 || cursor.Offset >= len(items) {
			return contracts.InventoryBatch{}, fmt.Errorf("Dataform folder cursor no longer matches the visible tree")
		}
	}
	limit := request.Limit
	if limit <= 0 || limit > 500 {
		limit = 500
	}
	end := min(cursor.Offset+limit, len(items))
	batch := contracts.InventoryBatch{Items: slices.Clone(items[cursor.Offset:end]), Complete: end == len(items)}
	if !batch.Complete {
		cursor.Offset = end
		raw, _ := json.Marshal(cursor)
		batch.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return batch, nil
}
