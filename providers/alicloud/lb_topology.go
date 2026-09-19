package alicloud

import (
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	albListenerNativeType = "ACS::ALB::Listener"
	// NormalizedALBServerGroupIDsField lists the server groups an ALB
	// listener's default actions forward to.
	NormalizedALBServerGroupIDsField = "serverGroupIds"
)

// enrichALBListenerServerGroups records the server groups a listener forwards
// to. ListListeners nests them in DefaultActions[].ForwardGroupConfig
// .ServerGroupTuples[], which a field path cannot address.
func enrichALBListenerServerGroups(items []contracts.InventoryItem) []contracts.InventoryItem {
	for index := range items {
		if items[index].NativeType != albListenerNativeType {
			continue
		}
		found := map[string]struct{}{}
		collectServerGroupIDs(valueAtPath(items[index].Raw, "DefaultActions"), found)
		ids := make([]string, 0, len(found))
		for id := range found {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		values := make([]any, len(ids))
		for position, id := range ids {
			values[position] = id
		}
		if items[index].Normalized == nil {
			items[index].Normalized = make(map[string]any)
		}
		items[index].Normalized[NormalizedALBServerGroupIDsField] = values
	}
	return items
}

func collectServerGroupIDs(value any, result map[string]struct{}) {
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "ServerGroupId" {
				if id := strings.TrimSpace(stringValue(child)); id != "" {
					result[id] = struct{}{}
				}
				continue
			}
			collectServerGroupIDs(child, result)
		}
	case []any:
		for _, child := range typed {
			collectServerGroupIDs(child, result)
		}
	}
}
