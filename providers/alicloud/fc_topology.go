package alicloud

import (
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const fcAliasNativeType = "ACS::FC::Alias"

// enrichFCAliasVersions records every service version an FC 2.0 alias routes
// to, the main version and each additionalVersionWeight key, as version
// native IDs. DeleteServiceVersion fails with VersionAlreadyInUse while an
// alias still references the version, so cleanup must delete the alias first.
func enrichFCAliasVersions(items []contracts.InventoryItem) []contracts.InventoryItem {
	for index := range items {
		if items[index].NativeType != fcAliasNativeType {
			continue
		}
		service := strings.TrimSpace(stringValue(items[index].Normalized["serviceName"]))
		if service == "" {
			continue
		}
		versions := map[string]struct{}{}
		if version := strings.TrimSpace(stringValue(valueAtPath(items[index].Raw, "versionId"))); version != "" {
			versions[version] = struct{}{}
		}
		if weights, ok := valueAtPath(items[index].Raw, "additionalVersionWeight").(map[string]any); ok {
			for version := range weights {
				if version = strings.TrimSpace(version); version != "" {
					versions[version] = struct{}{}
				}
			}
		}
		refs := make([]string, 0, len(versions))
		for version := range versions {
			refs = append(refs, service+"/"+version)
		}
		sort.Strings(refs)
		values := make([]any, len(refs))
		for position, ref := range refs {
			values[position] = ref
		}
		items[index].Normalized["versionRefs"] = values
	}
	return items
}
