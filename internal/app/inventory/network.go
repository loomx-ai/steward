package inventory

import (
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// FilterNetworkClosure returns the selected VPC/vSwitch and every resource
// that directly or indirectly references a member already inside that target.
// It preserves provider order so projection and logs remain deterministic.
func FilterNetworkClosure(target asset.ScanTarget, items []contracts.InventoryItem) []contracts.InventoryItem {
	seed := strings.TrimSpace(target.NativeID)
	if seed == "" || (target.Kind != asset.ScanTargetVPC && target.Kind != asset.ScanTargetVSwitch) {
		return nil
	}
	members := map[string]struct{}{seed: {}}
	included := make([]bool, len(items))
	changed := true
	for changed {
		changed = false
		for index, item := range items {
			if included[index] || !networkItemTouches(item, members) {
				continue
			}
			included[index] = true
			changed = true
			addNetworkMember(members, item.NativeID)
			for _, alias := range item.NativeAliases {
				addNetworkMember(members, alias)
			}
		}
	}
	result := make([]contracts.InventoryItem, 0)
	for index, item := range items {
		if included[index] {
			result = append(result, item)
		}
	}
	return result
}

func networkItemTouches(item contracts.InventoryItem, members map[string]struct{}) bool {
	if networkMemberExists(members, item.NativeID) {
		return true
	}
	for _, alias := range item.NativeAliases {
		if networkMemberExists(members, alias) {
			return true
		}
	}
	for _, reference := range item.NetworkReferences {
		if networkMemberExists(members, reference) {
			return true
		}
	}
	for _, reference := range scalarReferences(item.Normalized, nil) {
		if networkMemberExists(members, reference) {
			return true
		}
	}
	for _, reference := range scalarReferences(item.Raw, nil) {
		if networkMemberExists(members, reference) {
			return true
		}
	}
	return false
}

func scalarReferences(value any, result []string) []string {
	switch typed := value.(type) {
	case string:
		if text := strings.TrimSpace(typed); text != "" {
			result = append(result, text)
		}
	case []string:
		for _, item := range typed {
			result = scalarReferences(item, result)
		}
	case []any:
		for _, item := range typed {
			result = scalarReferences(item, result)
		}
	case map[string]string:
		for _, item := range typed {
			result = scalarReferences(item, result)
		}
	case map[string]any:
		for _, item := range typed {
			result = scalarReferences(item, result)
		}
	}
	return result
}

func networkMemberExists(members map[string]struct{}, value string) bool {
	_, ok := members[strings.TrimSpace(value)]
	return ok
}

func addNetworkMember(members map[string]struct{}, value string) {
	if value = strings.TrimSpace(value); value != "" {
		members[value] = struct{}{}
	}
}

func networkTargetForTask(task asset.ScanRun, targetKey string) (*asset.ScanTarget, error) {
	for index := range task.Targets {
		if task.Targets[index].Key == targetKey {
			target := task.Targets[index]
			return &target, nil
		}
	}
	return nil, fmt.Errorf("scan task %q has no target %q", task.ID, targetKey)
}
