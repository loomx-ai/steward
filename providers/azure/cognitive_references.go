package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func cognitiveNameID(parent, collection, value string) (string, error) {
	if value == "" || strings.TrimSpace(value) != value || strings.ContainsAny(value, "/\\%?#\x00\r\n") {
		return "", serviceDenied("invalid_cognitive_reference")
	}
	id, _, err := parseID(parent + "/" + collection + "/" + value)
	if err != nil {
		return "", serviceDenied("invalid_cognitive_reference")
	}
	return id, nil
}

// Capability hosts accept account connections and, within a project, project
// connections. A bare name which resolves in both scopes is ambiguous.
func (c *client) cognitiveConnectionID(ctx context.Context, id, kind, value string) (string, error) {
	parents := []string{redisRootID(id)}
	if kind == cognitiveProjectHostType {
		parents = append(parents, redisParentID(id))
	}
	var candidates []string
	if strings.HasPrefix(value, "/") {
		canonical, refKind, err := parseID(value)
		if err != nil || (!strings.EqualFold(refKind, cognitiveConnectionType) && !strings.EqualFold(refKind, cognitiveProjectConnectionType)) || !slices.Contains(parents, redisParentID(canonical)) {
			return "", serviceDenied("invalid_cognitive_connection_reference")
		}
		candidates = []string{canonical}
	} else {
		for _, parent := range parents {
			candidate, err := cognitiveNameID(parent, "connections", value)
			if err != nil {
				return "", err
			}
			candidates = append(candidates, candidate)
		}
	}
	var matches []string
	for _, candidate := range candidates {
		if _, err := c.cognitiveResource(ctx, candidate); isNotFound(err) {
			continue
		} else if err != nil {
			return "", err
		}
		matches = append(matches, candidate)
	}
	if len(matches) != 1 {
		return "", serviceDenied("cognitive_connection_reference_ambiguous_or_missing")
	}
	return matches[0], nil
}
func (c *client) cognitiveReferenceIDs(ctx context.Context, id, kind string, raw map[string]any) ([]string, error) {
	props := object(raw["properties"])
	refs := []string{}
	addName := func(parent, collection string, raw any) error {
		value, err := cognitiveExactString(raw)
		if err != nil {
			return err
		}
		ref, err := cognitiveNameID(parent, collection, value)
		if err != nil {
			return err
		}
		refs = append(refs, ref)
		return nil
	}
	switch kind {
	case cognitiveType:
		if err := cognitiveAccountIndexIncarnation(map[string]any{"_cognitive_account_indexes": cognitiveAccountIndexes(kind, raw)}, raw); err != nil {
			return nil, err
		}
	case cognitiveHostType, cognitiveProjectHostType:
		for _, field := range []string{"aiServicesConnections", "storageConnections", "threadStorageConnections", "vectorStoreConnections"} {
			if props[field] == nil {
				continue
			}
			rows, ok := props[field].([]any)
			if !ok {
				return nil, serviceDenied("invalid_cognitive_connection_reference")
			}
			for _, row := range rows {
				value, err := cognitiveExactString(row)
				if err != nil {
					return nil, err
				}
				ref, err := c.cognitiveConnectionID(ctx, id, kind, value)
				if err != nil {
					return nil, err
				}
				refs = append(refs, ref)
			}
		}
	case cognitiveDeploymentType:
		for _, field := range []string{"parentDeploymentName", "spilloverDeploymentName", "raiPolicyName"} {
			if props[field] == nil {
				continue
			}
			value, err := cognitiveExactString(props[field])
			if err != nil {
				return nil, err
			}
			if value == "" || field == "raiPolicyName" && value == "Microsoft.Default" {
				continue
			}
			collection := "deployments"
			if field == "raiPolicyName" {
				collection = "raiPolicies"
			}
			if err := addName(redisParentID(id), collection, value); err != nil {
				return nil, err
			}
		}
	case cognitivePolicyType:
		if value := props["customBlocklists"]; value != nil {
			rows, ok := value.([]any)
			if !ok {
				return nil, serviceDenied("invalid_cognitive_blocklist_reference")
			}
			for _, row := range rows {
				if err := addName(redisParentID(id), "raiBlocklists", object(row)["blocklistName"]); err != nil {
					return nil, err
				}
			}
		}
	case cognitiveToolType:
		if value := props["projectScopes"]; value != nil {
			rows, ok := value.([]any)
			if !ok {
				return nil, serviceDenied("invalid_cognitive_project_reference")
			}
			for _, row := range rows {
				if err := addName(redisParentID(id), "projects", object(row)["project"]); err != nil {
					return nil, err
				}
			}
		}
	}
	slices.Sort(refs)
	return slices.Compact(refs), nil
}

// Required deletion relationships express shared use, never exclusive ownership.
func cognitiveSharedPrerequisite(parent, child asset.Asset) bool {
	target, source := parent.Identity.NativeType, child.Identity.NativeType
	if !isCognitiveType(target) || !isCognitiveType(source) {
		return false
	}
	if target == cognitiveType && source == cognitiveAssociationType {
		return strings.EqualFold(text(child.Normalized["accountId"]), parent.Identity.NativeID)
	}
	if redisRootID(parent.Identity.NativeID) != redisRootID(child.Identity.NativeID) {
		return false
	}
	// A BYO Key Vault stores secrets for every account/project connection.
	// https://learn.microsoft.com/azure/foundry/how-to/connections-add#azure-key-vault-limitations
	if (target == cognitiveConnectionType || target == cognitiveProjectConnectionType) && parent.Normalized["category"] == "AzureKeyVault" {
		if source == cognitiveConnectionType || source == cognitiveProjectConnectionType {
			return !strings.EqualFold(parent.Identity.NativeID, child.Identity.NativeID)
		}
	}
	switch target {
	case cognitiveHostType, cognitiveNetworkType:
		return source == cognitiveProjectType || target == cognitiveNetworkType && source == cognitiveHostType
	case cognitiveProjectHostType:
		return source == cognitiveApplicationType && redisParentID(parent.Identity.NativeID) == redisParentID(child.Identity.NativeID)
	}
	return slices.ContainsFunc(stringValues(child.Normalized["_cognitive_references"]), func(id string) bool { return strings.EqualFold(id, parent.Identity.NativeID) })
}
func cognitiveIncomingKinds(kind string) []string {
	switch kind {
	case cognitiveHostType:
		return []string{cognitiveProjectType}
	case cognitiveNetworkType:
		return []string{cognitiveProjectType, cognitiveHostType}
	case cognitiveProjectHostType:
		return []string{cognitiveApplicationType}
	case cognitiveConnectionType:
		return []string{cognitiveHostType, cognitiveProjectHostType}
	case cognitiveProjectConnectionType:
		return []string{cognitiveProjectHostType}
	case cognitiveDeploymentType, cognitivePolicyType:
		return []string{cognitiveDeploymentType}
	case cognitiveBlocklistType:
		return []string{cognitivePolicyType}
	case cognitiveProjectType:
		return []string{cognitiveToolType}
	}
	return nil
}
func (c *client) cognitiveIncoming(ctx context.Context, parent asset.Identity, parentRaw map[string]any) ([]serviceChild, error) {
	kinds := cognitiveIncomingKinds(parent.NativeType)
	if (parent.NativeType == cognitiveConnectionType || parent.NativeType == cognitiveProjectConnectionType) && object(parentRaw["properties"])["category"] == "AzureKeyVault" {
		kinds = append(kinds, cognitiveConnectionType, cognitiveProjectConnectionType)
	}
	if len(kinds) == 0 {
		return nil, nil
	}
	rootID := redisRootID(parent.NativeID)
	root, err := c.cognitiveResource(ctx, rootID)
	if err != nil {
		return nil, err
	}
	var walk func(asset.Identity, map[string]any) ([]serviceChild, error)
	walk = func(identity asset.Identity, raw map[string]any) ([]serviceChild, error) {
		branches := []string{}
		for _, child := range cognitiveOwnedKinds(identity.NativeType) {
			if slices.ContainsFunc(kinds, func(kind string) bool { return kind == child || strings.HasPrefix(kind, child+"/") }) {
				branches = append(branches, child)
			}
		}
		children, err := c.nativeServiceChildren(ctx, identity, raw, branches)
		if err != nil {
			return nil, err
		}
		result := []serviceChild{}
		for _, child := range children {
			if slices.Contains(kinds, child.kind) && !strings.EqualFold(child.id, parent.NativeID) {
				refs, err := c.cognitiveReferenceIDs(ctx, child.id, child.kind, child.data)
				if err != nil {
					return nil, err
				}
				normalized := map[string]any{"_cognitive_references": refs}
				for k, v := range object(child.data["properties"]) {
					normalized[k] = v
				}
				if cognitiveSharedPrerequisite(asset.Asset{Identity: parent, Normalized: object(parentRaw["properties"])}, asset.Asset{Identity: asset.Identity{NativeID: child.id, NativeType: child.kind}, Normalized: normalized}) {
					child.direct = true
					result = append(result, child)
				}
			}
			if slices.ContainsFunc(kinds, func(kind string) bool { return strings.HasPrefix(kind, child.kind+"/") }) {
				nested, err := walk(asset.Identity{NativeID: child.id, NativeType: child.kind}, child.data)
				if err != nil {
					return nil, err
				}
				result = append(result, nested...)
			}
		}
		return result, nil
	}
	return walk(asset.Identity{NativeID: rootID, NativeType: cognitiveType}, root)
}

func (c *client) cognitiveAccountAssociations(ctx context.Context, account string) ([]serviceChild, error) {
	mapping, _ := findType(cognitiveSharedPlanType)
	records, err := c.listAll(ctx, c.root()+"/providers/Microsoft.CognitiveServices/commitmentPlans", mapping.Version)
	if err != nil {
		return nil, err
	}
	result := []serviceChild{}
	seen := map[string]bool{}
	for _, record := range records {
		listed := object(record)
		id, kind, err := parseID(text(listed["id"]))
		if err != nil || !strings.EqualFold(kind, cognitiveSharedPlanType) || !strings.HasPrefix(id, c.root()+"/") || seen[id] || !validResponseType(cognitiveSharedPlanType, text(listed["type"])) {
			return nil, serviceDenied("invalid_cognitive_shared_plan")
		}
		seen[id] = true
		raw, err := c.cognitiveResource(ctx, id)
		if err != nil {
			return nil, err
		}
		if err := serviceListedIncarnation(listed, raw); err != nil {
			return nil, err
		}
		children, err := c.nativeServiceChildren(ctx, asset.Identity{NativeID: id, NativeType: cognitiveSharedPlanType}, raw, []string{cognitiveAssociationType})
		if err != nil {
			return nil, err
		}
		for _, child := range children {
			target, err := cognitiveLinkedTarget(child.kind, child.data)
			if err != nil {
				return nil, err
			}
			if strings.EqualFold(target, account) {
				child.direct = true
				result = append(result, child)
			}
		}
	}
	return result, nil
}

func cognitiveOwnedKinds(kind string) []string {
	switch kind {
	case cognitiveType:
		return []string{cognitiveHostType, cognitivePlanType, cognitiveConnectionType, cognitiveDefenderType, cognitiveDeploymentType, cognitiveEncryptionType, cognitiveNetworkType, cognitivePerimeterType, cognitivePECType, cognitiveProjectType, cognitiveBlocklistType, cognitivePolicyType, cognitiveToolType, cognitiveTopicType}
	case cognitiveProjectType:
		return []string{cognitiveApplicationType, cognitiveProjectHostType, cognitiveProjectConnectionType}
	case cognitiveApplicationType:
		return []string{cognitiveAgentType}
	case cognitiveNetworkType:
		return []string{cognitiveOutboundType}
	case cognitiveBlocklistType:
		return []string{cognitiveBlockitemType}
	case cognitiveSharedPlanType:
		return []string{cognitiveAssociationType}
	}
	return nil
}
func (c *client) cognitiveChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	collect := func() ([]serviceChild, error) {
		children, err := c.nativeServiceChildren(ctx, parent, raw, cognitiveOwnedKinds(parent.NativeType))
		if err != nil {
			return nil, err
		}
		incoming, err := c.cognitiveIncoming(ctx, parent, raw)
		if err != nil {
			return nil, err
		}
		children = append(children, incoming...)
		if parent.NativeType == cognitiveType {
			associations, err := c.cognitiveAccountAssociations(ctx, parent.NativeID)
			if err != nil {
				return nil, err
			}
			children = append(children, associations...)
		}
		if err := cognitiveChildIndexes(parent, raw, children); err != nil {
			return nil, err
		}
		slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
		for i := 1; i < len(children); i++ {
			if children[i-1].id == children[i].id {
				return nil, serviceDenied("cognitive_duplicate_child")
			}
		}
		return children, nil
	}
	first, err := collect()
	if err != nil {
		return nil, err
	}
	second, err := collect()
	if err != nil {
		return nil, err
	}
	if !slices.EqualFunc(first, second, func(a, b serviceChild) bool {
		return a.id == b.id && cognitiveNativeLocation(a.data) == cognitiveNativeLocation(b.data) && c.privateConfiguration(cognitiveSnapshot(a.kind, a.data)) == c.privateConfiguration(cognitiveSnapshot(b.kind, b.data))
	}) {
		return nil, serviceDenied("cognitive_children_changed")
	}
	return second, nil
}
func cognitiveChildIndexes(parent asset.Identity, raw map[string]any, children []serviceChild) error {
	props := object(raw["properties"])
	indexes := map[string]map[string]bool{}
	for _, child := range children {
		if indexes[child.kind] == nil {
			indexes[child.kind] = map[string]bool{}
		}
		indexes[child.kind][child.id] = true
	}
	compare := func(value any, kind string, resolve func(any) (string, error)) error {
		if value == nil {
			return nil
		}
		rows, ok := value.([]any)
		if !ok {
			return serviceDenied("cognitive_child_indexes_disagree")
		}
		actual := indexes[kind]
		seen := map[string]bool{}
		for _, row := range rows {
			id, err := resolve(row)
			if err != nil || !actual[id] || seen[id] {
				return serviceDenied("cognitive_child_indexes_disagree")
			}
			seen[id] = true
		}
		if len(actual) != len(seen) {
			return serviceDenied("cognitive_child_indexes_disagree")
		}
		return nil
	}
	if parent.NativeType == cognitiveType {
		if err := compare(props["privateEndpointConnections"], cognitivePECType, func(value any) (string, error) { id, _, err := parseID(text(object(value)["id"])); return id, err }); err != nil {
			return err
		}
		if err := compare(props["associatedProjects"], cognitiveProjectType, func(raw any) (string, error) {
			value, err := cognitiveExactString(raw)
			if err != nil {
				return "", err
			}
			return cognitiveNameID(parent.NativeID, "projects", value)
		}); err != nil {
			return err
		}
		if props["defaultProject"] != nil {
			value, err := cognitiveExactString(props["defaultProject"])
			if err != nil {
				return err
			}
			if value != "" {
				id, err := cognitiveNameID(parent.NativeID, "projects", value)
				if err != nil || !indexes[cognitiveProjectType][id] {
					return serviceDenied("cognitive_default_project_missing")
				}
			}
		}
		if value := props["commitmentPlanAssociations"]; value != nil {
			actual := map[string]bool{}
			for _, child := range children {
				if child.kind == cognitiveAssociationType {
					actual[redisParentID(child.id)] = true
				}
			}
			rows, ok := value.([]any)
			if !ok {
				return serviceDenied("cognitive_commitment_indexes_disagree")
			}
			seen := map[string]bool{}
			for _, row := range rows {
				id, kind, err := parseID(text(object(row)["commitmentPlanId"]))
				if err != nil || !strings.EqualFold(kind, cognitiveSharedPlanType) || !actual[id] || seen[id] {
					return serviceDenied("cognitive_commitment_indexes_disagree")
				}
				seen[id] = true
			}
			if len(seen) != len(actual) {
				return serviceDenied("cognitive_commitment_indexes_disagree")
			}
		}
	}
	if parent.NativeType == cognitiveNetworkType {
		if value := object(props["managedNetwork"])["outboundRules"]; value != nil {
			rules, ok := value.(map[string]any)
			if !ok || len(rules) != len(indexes[cognitiveOutboundType]) {
				return serviceDenied("cognitive_outbound_indexes_disagree")
			}
			for name, rule := range rules {
				id, err := cognitiveNameID(parent.NativeID, "outboundRules", name)
				if err != nil || !indexes[cognitiveOutboundType][id] {
					return serviceDenied("cognitive_outbound_indexes_disagree")
				}
				for _, child := range children {
					if child.id == id {
						left := cognitiveSnapshot(cognitiveOutboundType, map[string]any{"id": id, "properties": rule})
						right := cognitiveSnapshot(cognitiveOutboundType, map[string]any{"id": id, "properties": child.data["properties"]})
						if serviceParentConfiguration(cognitiveOutboundType, left) != serviceParentConfiguration(cognitiveOutboundType, right) {
							return serviceDenied("cognitive_outbound_indexes_disagree")
						}
					}
				}
			}
		}
	}
	return nil
}
