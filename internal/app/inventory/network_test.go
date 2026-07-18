package inventory_test

import (
	"testing"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestFilterNetworkClosureIncludesDirectAndIndirectDependents(t *testing.T) {
	t.Parallel()

	items := []contracts.InventoryItem{
		{NativeID: "vpc-a", NativeAliases: []string{"vpc-a"}},
		{NativeID: "vsw-a", NativeAliases: []string{"vsw-a"}, NetworkReferences: []string{"vpc-a"}},
		{NativeID: "i-a", NativeAliases: []string{"i-a"}, NetworkReferences: []string{"vsw-a"}},
		{NativeID: "eni-a", NativeAliases: []string{"eni-a"}, NetworkReferences: []string{"i-a"}},
		{NativeID: "db-other", NativeAliases: []string{"db-other"}, NetworkReferences: []string{"vpc-b"}},
	}
	filtered := inventory.FilterNetworkClosure(asset.ScanTarget{Kind: asset.ScanTargetVPC, NativeID: "vpc-a"}, items)
	if got := nativeIDs(filtered); len(got) != 4 || got[0] != "vpc-a" || got[1] != "vsw-a" || got[2] != "i-a" || got[3] != "eni-a" {
		t.Fatalf("closure=%v", got)
	}
}

func TestFilterNetworkClosureForVSwitchDoesNotIncludeItsParentOrSibling(t *testing.T) {
	t.Parallel()

	items := []contracts.InventoryItem{
		{NativeID: "vpc-a", NativeAliases: []string{"vpc-a"}},
		{NativeID: "vsw-a", NativeAliases: []string{"vsw-a"}, NetworkReferences: []string{"vpc-a"}},
		{NativeID: "vsw-b", NativeAliases: []string{"vsw-b"}, NetworkReferences: []string{"vpc-a"}},
		{NativeID: "i-a", NativeAliases: []string{"i-a"}, NetworkReferences: []string{"vsw-a"}},
	}
	filtered := inventory.FilterNetworkClosure(asset.ScanTarget{Kind: asset.ScanTargetVSwitch, NativeID: "vsw-a", ParentNativeID: "vpc-a"}, items)
	if got := nativeIDs(filtered); len(got) != 2 || got[0] != "vsw-a" || got[1] != "i-a" {
		t.Fatalf("closure=%v", got)
	}
}

func nativeIDs(items []contracts.InventoryItem) []string {
	result := make([]string, 0, len(items))
	for _, item := range items {
		result = append(result, item.NativeID)
	}
	return result
}
