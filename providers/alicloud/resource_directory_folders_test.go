package alicloud

import (
	"context"
	"errors"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestResourceDirectoryFoldersAreWalkedFromTheRootFolder(t *testing.T) {
	t.Parallel()

	// ListFoldersForParent returns one level of children (ResourceManager
	// 2020-03-31), so nested folders need their parent listed first.
	children := map[string][]any{
		"r-root": {map[string]any{"FolderId": "fd-a", "FolderName": "prod"}},
		"fd-a":   {map[string]any{"FolderId": "fd-b", "FolderName": "payments"}},
	}
	runtime, _ := encryptionKeyRuntime(t, func(invocation contracts.Invocation) (contracts.InvocationResult, error) {
		switch invocation.Operation {
		case "AlibabaCloud.ResourceManager.GetResourceDirectory":
			return contracts.InvocationResult{Data: map[string]any{"ResourceDirectory": map[string]any{
				"ResourceDirectoryId": "rd-a", "RootFolderId": "r-root",
			}}}, nil
		case "AlibabaCloud.ResourceManager.ListFoldersForParent":
			folders := children[stringValue(invocation.Parameters["ParentFolderId"])]
			return contracts.InvocationResult{Data: map[string]any{"TotalCount": len(folders), "Folders": map[string]any{"Folder": folders}}}, nil
		}
		return contracts.InvocationResult{}, errors.New("unexpected call " + invocation.Operation)
	})
	kind := runtime.resourceKindByNativeType["ACS::ResourceManager::Folder"]
	batch, err := runtime.List(context.Background(), contracts.InventoryRequest{
		ConnectionID: "connection-a", Scope: asset.Scope{Kind: asset.ScopeGlobal, NativeID: "global", Location: "cn-hangzhou"},
		Source: "product-api", ResourceKind: &kind, Limit: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !batch.Complete || len(batch.Items) != 2 ||
		batch.Items[0].NativeID != "fd-a" || batch.Items[0].Normalized["parentFolderId"] != nil ||
		batch.Items[1].NativeID != "fd-b" || batch.Items[1].Normalized["parentFolderId"] != "fd-a" ||
		batch.Items[1].Normalized["resourceDirectoryId"] != "rd-a" {
		t.Fatalf("folders = %+v", batch.Items)
	}
}
