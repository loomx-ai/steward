package alicloud

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

const inventorySourceField = "_inventory_source"

const armsTraceAppNativeType = "ACS::ARMS::TraceApp"
const vpcPeerConnectionNativeType = "ACS::VPC::PeerConnection"

func (r *Runtime) projectResourceCenterConfiguration(item *contracts.InventoryItem) error {
	if item == nil {
		return fmt.Errorf("Resource Center inventory item is required")
	}
	compiled, ok := r.compiledSpec(item.NativeType)
	if !ok {
		return nil
	}
	if item.Normalized == nil {
		item.Normalized = make(map[string]any)
	}
	configuration := normalizedConfiguration(item.Raw["Configuration"])
	item.Normalized["configuration"] = configuration
	item.Normalized[inventorySourceField] = "resource-center"
	projectResourceCenterScope(item, compiled.ResourceKind, configuration)
	for field, property := range compiled.Definition.Fields {
		if value := configurationValueAtPath(configuration, property.Path); value != nil {
			item.Normalized[field] = value
		}
	}
	// Resource Center calls the DTS job kind "Type" and returns lowercase
	// values such as "subscribe". DescribeDtsJobs expects the same discriminator
	// as the uppercase JobType query parameter.
	if item.NativeType == dtsInstanceNativeType &&
		!relationshipValuePresent(item.Normalized["jobType"]) {
		if jobType := strings.ToUpper(strings.TrimSpace(stringValue(
			configurationValueAtPath(configuration, "Type"),
		))); jobType != "" {
			item.Normalized["jobType"] = jobType
		}
	}
	// Resource Center exposes the ARMS trace application identifier as
	// TraceAppId, while the product API and delete operation call it AppId.
	// Preserve the product API field mapping and project the Resource Center
	// alias only when the canonical field is absent.
	if item.NativeType == armsTraceAppNativeType &&
		!relationshipValuePresent(item.Normalized["appId"]) {
		if value := configurationValueAtPath(configuration, "TraceAppId"); value != nil {
			item.Normalized["appId"] = value
		}
	}
	// ListVpcPeerConnections returns requester and accepter VPCs as nested
	// objects, while Resource Center flattens them to VpcId and
	// AcceptingVpcId. Preserve both canonical relationship fields so cleanup
	// can order the peer connection before either VPC.
	if item.NativeType == vpcPeerConnectionNativeType {
		for normalizedKey, configurationKey := range map[string]string{
			"vpcId":          "VpcId",
			"acceptingVpcId": "AcceptingVpcId",
		} {
			if relationshipValuePresent(item.Normalized[normalizedKey]) {
				continue
			}
			if value := configurationValueAtPath(configuration, configurationKey); value != nil {
				item.Normalized[normalizedKey] = value
			}
		}
	}
	for _, relationship := range compiled.Definition.Relationships {
		if relationshipValuePresent(valueAtPath(item.Normalized, relationship.TargetIDPath)) {
			continue
		}
		candidates := relationshipConfigurationKeys(compiled, relationship)
		values := configurationValuesForKeys(configuration, candidates)
		switch len(values) {
		case 0:
			continue
		case 1:
			item.Normalized[relationship.TargetIDPath] = values[0]
		default:
			item.Normalized[relationship.TargetIDPath] = values
		}
	}
	projectCENBandwidthPackageIDs(item, configuration)
	projectCanonicalVPCID(item.Normalized, configuration)
	if item.NativeType == KMSKeyNativeType {
		if creator, managed := serviceManagedKMSCreator(item.Normalized); managed {
			actionable := false
			item.Actionable = &actionable
			item.Normalized[NormalizedServiceManagedField] = true
			item.Normalized["_service_creator"] = creator
		}
	}
	switch item.NativeType {
	case securityGroupNativeType, networkInterfaceNativeType:
		if value := configurationValueAtPath(configuration, "ServiceManaged"); value != nil {
			item.Normalized[NormalizedServiceManagedField] = value
		}
		if value := configurationValueAtPath(configuration, "ServiceID"); value != nil {
			item.Normalized[NormalizedServiceIDField] = value
		}
	}
	if name := strings.TrimSpace(stringValue(item.Normalized["name"])); name != "" {
		item.Name = name
	}
	if state := strings.TrimSpace(stringValue(item.Normalized["state"])); state != "" {
		item.State = state
	}
	item.NetworkReferences = appendUniqueReferences(
		item.NetworkReferences,
		scalarConfigurationReferences(configuration, nil),
	)
	return nil
}

func projectCENBandwidthPackageIDs(
	item *contracts.InventoryItem,
	configuration any,
) {
	if item.NativeType != CENBandwidthPackageNativeType ||
		relationshipValuePresent(item.Normalized["cenIds"]) {
		return
	}
	values := configurationValuesForKeys(
		configuration,
		map[string]struct{}{canonicalConfigurationKey("CenIds"): {}},
	)
	if len(values) > 0 {
		item.Normalized["cenIds"] = values
	}
}

func projectResourceCenterScope(
	item *contracts.InventoryItem,
	kind asset.ResourceKind,
	configuration any,
) {
	regionID := strings.TrimSpace(
		stringValue(configurationValueAtPath(configuration, "RegionId")),
	)
	if regionID == "" {
		regionID = strings.TrimSpace(
			stringValue(configurationValueAtPath(item.Raw, "RegionId")),
		)
	}
	if regionID == "" {
		regionID = strings.TrimSpace(item.Location)
	}
	if regionID == "" || strings.EqualFold(regionID, "global") {
		return
	}
	item.Location = regionID
	if !regionOnlyResourceKind(kind) {
		return
	}
	if item.Scope.Kind == asset.ScopeRegion && strings.TrimSpace(item.Scope.NativeID) == regionID {
		return
	}
	item.Scope = contracts.InventoryScope{
		Kind: asset.ScopeRegion, NativeID: regionID, Name: regionID, Location: regionID,
	}
}

func regionOnlyResourceKind(kind asset.ResourceKind) bool {
	regional := false
	for _, scopeKind := range kind.ScopeKinds {
		switch scopeKind {
		case asset.ScopeGlobal:
			return false
		case asset.ScopeRegion:
			regional = true
		}
	}
	return regional
}

func projectCanonicalVPCID(normalized map[string]any, configuration any) {
	if relationshipValuePresent(normalized["vpc_id"]) {
		return
	}
	values := configurationValuesForKeys(
		configuration,
		map[string]struct{}{canonicalConfigurationKey("vpc_id"): {}},
	)
	if len(values) == 1 {
		normalized["vpc_id"] = values[0]
	}
}

func (r *Runtime) compiledSpec(nativeType string) (spec.CompiledSpec, bool) {
	for _, compiled := range r.bundle.Specs {
		if compiled.ResourceKind.NativeType == nativeType {
			return compiled, true
		}
	}
	return spec.CompiledSpec{}, false
}

func normalizedConfiguration(value any) any {
	switch typed := value.(type) {
	case string:
		text := strings.TrimSpace(typed)
		if strings.HasPrefix(text, "{") || strings.HasPrefix(text, "[") {
			var decoded any
			decoder := json.NewDecoder(strings.NewReader(text))
			decoder.UseNumber()
			if err := decoder.Decode(&decoded); err == nil {
				return normalizedConfiguration(decoded)
			}
		}
		return typed
	case []any:
		result := make([]any, len(typed))
		for index, item := range typed {
			result[index] = normalizedConfiguration(item)
		}
		return result
	case map[string]any:
		result := make(map[string]any, len(typed))
		for key, item := range typed {
			result[key] = normalizedConfiguration(item)
		}
		return result
	default:
		return value
	}
}

func configurationValueAtPath(value any, path string) any {
	current := value
	for _, segment := range strings.Split(strings.TrimSpace(path), ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil
		}
		next, exists := object[segment]
		if !exists {
			want := canonicalConfigurationKey(segment)
			for key, candidate := range object {
				if canonicalConfigurationKey(key) == want {
					next = candidate
					exists = true
					break
				}
			}
		}
		if !exists {
			return nil
		}
		current = next
	}
	return current
}

func relationshipConfigurationKeys(
	compiled spec.CompiledSpec,
	relationship spec.RelationshipSpec,
) map[string]struct{} {
	result := make(map[string]struct{})
	add := func(value string) {
		if key := canonicalConfigurationKey(value); key != "" {
			result[key] = struct{}{}
		}
	}
	add(relationship.TargetIDPath)
	if property, exists := compiled.Definition.Fields[relationship.TargetIDPath]; exists && property.Path != "" {
		segments := strings.Split(property.Path, ".")
		add(segments[len(segments)-1])
	}
	parts := strings.Split(relationship.TargetType, "::")
	if len(parts) > 0 {
		resource := parts[len(parts)-1]
		add(resource + "Id")
		add(resource + "Ids")
		if strings.HasSuffix(resource, "Instance") {
			base := strings.TrimSuffix(resource, "Instance")
			add(base + "Id")
			add(base + "Ids")
		}
	}
	if relationship.TargetType == "ACS::VPC::VSwitch" {
		// NAS and a few older Alibaba APIs use VswId instead of VSwitchId.
		add("VswId")
		add("VswIds")
		if canonicalConfigurationKey(relationship.TargetIDPath) ==
			canonicalConfigurationKey("vSwitchIds") {
			add("BackupVSwitchId")
		}
	}
	return result
}

func configurationValuesForKeys(value any, keys map[string]struct{}) []string {
	seen := make(map[string]struct{})
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if _, matched := keys[canonicalConfigurationKey(key)]; matched {
					for _, candidate := range scalarConfigurationReferences(child, nil) {
						if candidate = strings.TrimSpace(candidate); candidate != "" {
							seen[candidate] = struct{}{}
						}
					}
				}
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(value)
	result := make([]string, 0, len(seen))
	for candidate := range seen {
		result = append(result, candidate)
	}
	sort.Strings(result)
	return result
}

func canonicalConfigurationKey(value string) string {
	var result strings.Builder
	for _, character := range strings.TrimSpace(value) {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			result.WriteRune(unicode.ToLower(character))
		}
	}
	return result.String()
}

func relationshipValuePresent(value any) bool {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed) != ""
	case []string:
		for _, item := range typed {
			if strings.TrimSpace(item) != "" {
				return true
			}
		}
	case []any:
		for _, item := range typed {
			if relationshipValuePresent(item) {
				return true
			}
		}
	}
	return false
}
