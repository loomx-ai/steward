package alicloud

import (
	"context"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

const (
	resourceDirectoryFolderNativeType = "ACS::ResourceManager::Folder"
	getResourceDirectoryOperation     = "AlibabaCloud.ResourceManager.GetResourceDirectory"
	resourceDirectoryFolderPageLimit  = 1000
)

// listResourceDirectoryFolders walks the folder tree breadth-first from the
// root folder. ListFoldersForParent returns only one level of children, and
// the root folder itself is part of the resource directory, not a folder
// that can be managed on its own.
func (r *Runtime) listResourceDirectoryFolders(
	ctx context.Context,
	request contracts.InventoryRequest,
	compiled spec.CompiledSpec,
) (contracts.InventoryBatch, error) {
	region, err := productAPIRegion(request)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	directory, err := r.Invoke(ctx, contracts.Invocation{
		ConnectionID: request.ConnectionID, Operation: getResourceDirectoryOperation,
		Scope: map[string]string{"region": region},
	})
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	directoryID := strings.TrimSpace(stringValue(valueAtPath(directory.Data, "ResourceDirectory.ResourceDirectoryId")))
	rootID := strings.TrimSpace(stringValue(valueAtPath(directory.Data, "ResourceDirectory.RootFolderId")))
	if directoryID == "" || rootID == "" {
		return contracts.InventoryBatch{}, fmt.Errorf("Alibaba Cloud GetResourceDirectory returned no directory or root folder ID")
	}
	list := *compiled.Definition.Discovery.List
	var records []any
	queue := []string{rootID}
	for pages := 0; len(queue) > 0; {
		parentID := queue[0]
		queue = queue[1:]
		api := list
		api.Parameters = map[string]any{"ParentFolderId": parentID}
		for cursor := ""; ; pages++ {
			if pages >= resourceDirectoryFolderPageLimit {
				return contracts.InventoryBatch{}, fmt.Errorf("Alibaba Cloud resource directory folders exceeded %d pages", resourceDirectoryFolderPageLimit)
			}
			page, err := r.invokeProductAPIPage(ctx, request, api, specParameterContext{region: region}, cursor, list.Pagination.MaxPageSize)
			if err != nil {
				return contracts.InventoryBatch{}, err
			}
			for _, raw := range page.items {
				folder, ok := productAPIResourceMap(raw)
				if !ok {
					continue
				}
				folder["_resourceDirectoryId"] = directoryID
				if parentID != rootID {
					folder["_parentFolderId"] = parentID
				}
				if id := strings.TrimSpace(stringValue(folder["FolderId"])); id != "" {
					queue = append(queue, id)
				}
				records = append(records, folder)
			}
			if page.next == "" {
				break
			}
			cursor = page.next
		}
	}
	items, err := inventoryItemsFromProductAPI(records, compiled, request, region, "", nil)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	return contracts.InventoryBatch{Items: items, RequestID: directory.RequestID, Complete: true}, nil
}
