package azure

import (
	"context"
	"net/url"
	"slices"
	"strings"
)

func apimBaseAPI(id string) string { base, _, _ := strings.Cut(id, ";rev="); return base }
func apimRevisionID(base string, value any) (string, error) {
	supplied, ok := value.(string)
	if !ok || supplied != strings.TrimSpace(supplied) {
		return "", serviceDenied("invalid_apim_revision_id")
	}
	if strings.HasPrefix(supplied, "/apis/") {
		supplied = apimNamespaceID(base) + supplied
	}
	id, kind, err := parseID(supplied)
	_, baseKind, _ := parseID(base)
	if err != nil || !strings.EqualFold(kind, baseKind) || apimBaseAPI(id) != base {
		return "", serviceDenied("apim_revision_owner_changed")
	}
	return id, nil
}
func (c *client) apimRevisions(ctx context.Context, base string) ([]map[string]any, error) {
	_, kindName, err := parseID(base)
	if err != nil || !isAPIMAPI(kindName) || base != apimBaseAPI(base) {
		return nil, serviceDenied("invalid_apim_revision_parent")
	}
	kind, _ := findType(kindName)
	_, parameters, err := c.resourceOperation(kind, base, "GET")
	if err != nil {
		return nil, err
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	name := "ApiRevision_ListByService"
	if strings.Contains(strings.ToLower(kindName), "/workspaces/") {
		name = "Workspace" + name
	}
	op, ok := metadata.catalog.Operation("Azure.Microsoft.ApiManagement." + name)
	if !ok {
		return nil, serviceDenied("missing_apim_revision_api")
	}
	bound, err := bindAzureREST(op, parameters)
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(bound.URL)
	values, err := c.listAllURL(ctx, bound.URL, u.Path)
	if err != nil {
		return nil, err
	}
	var result []map[string]any
	seen := map[string]bool{}
	current := 0
	for _, value := range values {
		row := object(value)
		id, err := apimRevisionID(base, row["apiId"])
		revision := text(row["apiRevision"])
		isCurrent, validCurrent := row["isCurrent"].(bool)
		if err != nil || revision == "" || id != base+";rev="+revision || seen[id] || !validCurrent {
			return nil, serviceDenied("invalid_apim_revision_index")
		}
		if row["isOnline"] != nil {
			if _, ok := row["isOnline"].(bool); !ok {
				return nil, serviceDenied("invalid_apim_revision_online_state")
			}
		}
		seen[id] = true
		if isCurrent {
			current++
		}
		result = append(result, row)
	}
	// A deleted current revision may leave explicit non-current revisions.
	// Inventory must still expose them; an alias row without a current entry
	// is rejected separately when reconciling Api_List with the detail reads.
	if len(result) == 0 || current > 1 {
		return nil, serviceDenied("ambiguous_apim_current_revision")
	}
	return result, nil
}

// Api_List exposes logical APIs. Enumerate each revision explicitly; a revision
// list is metadata with apiId, not an ARM resource collection with id/type.
func (c *client) apimAPIs(ctx context.Context, parent string) ([]serviceChild, error) {
	children, _, err := c.apimAPIsResult(ctx, parent)
	return children, err
}

func (c *client) apimAPIsResult(ctx context.Context, parent string) ([]serviceChild, response, error) {
	_, parentType, err := parseID(parent)
	if err != nil || !strings.EqualFold(parentType, apimServiceType) && !strings.EqualFold(parentType, apimWorkspaceType) {
		return nil, response{}, serviceDenied("invalid_apim_api_owner")
	}
	kindName := apimServiceType + "/apis"
	if strings.EqualFold(parentType, apimWorkspaceType) {
		kindName = apimWorkspaceType + "/apis"
	}
	list, provenance, err := c.listAllURLResult(ctx, apiURL(parent+"/apis", apimVersion), parent+"/apis")
	if err != nil {
		return nil, response{}, err
	}
	groups := map[string][]map[string]any{}
	listedIDs := map[string]bool{}
	for _, value := range list {
		raw := object(value)
		id, kind, err := parseID(text(raw["id"]))
		if err != nil || !strings.EqualFold(kind, kindName) || redisParentID(id) != parent || listedIDs[id] || !validResponseType(kindName, text(raw["type"])) {
			return nil, response{}, serviceDenied("invalid_apim_api_list")
		}
		if err := validateAPIM(kindName, raw); err != nil {
			return nil, response{}, err
		}
		listedIDs[id] = true
		groups[apimBaseAPI(id)] = append(groups[apimBaseAPI(id)], raw)
	}
	var bases []string
	for base := range groups {
		bases = append(bases, base)
	}
	slices.Sort(bases)
	// Bases are read concurrently and merged in order, so the first failure
	// is the one a serial walk would return. Each base still re-lists its
	// revisions after its reads.
	reads, errs := readConcurrently(len(bases), func(i int) ([]serviceChild, error) {
		base := bases[i]
		var children []serviceChild
		revisions, err := c.apimRevisions(ctx, base)
		if err != nil {
			return nil, err
		}
		// Revisions are checked in order before any read; revisions past the
		// first invalid one are not read, and its error follows the earlier reads'.
		var ids []string
		var invalid error
		for _, revision := range revisions {
			id, err := apimRevisionID(base, revision["apiId"])
			if err != nil {
				invalid = err
				break
			}
			if revision["isCurrent"] == true {
				id = base
			}
			ids = append(ids, id)
		}
		raws, rawErrs := readConcurrently(len(ids), func(i int) (map[string]any, error) { return c.apimResource(ctx, ids[i]) })
		readByRevision := map[string]map[string]any{}
		for i, id := range ids {
			revision, raw, err := revisions[i], raws[i], rawErrs[i]
			if err != nil {
				return nil, err
			}
			props := object(raw["properties"])
			if props["apiRevision"] != revision["apiRevision"] || (props["isCurrent"] == true) != (revision["isCurrent"] == true) {
				return nil, serviceDenied("apim_revision_changed_during_discovery")
			}
			readByRevision[text(revision["apiRevision"])] = raw
			children = append(children, serviceChild{kind: kindName, id: id, data: raw})
		}
		if invalid != nil {
			return nil, invalid
		}
		for _, listed := range groups[base] {
			live := readByRevision[text(object(listed["properties"])["apiRevision"])]
			if live == nil {
				return nil, serviceDenied("apim_api_revision_indexes_disagree")
			}
			// The current revision may be listed through its explicit ;rev=n alias.
			originalID := text(listed["id"])
			if strings.EqualFold(originalID, base) && !strings.EqualFold(text(live["id"]), base) {
				return nil, serviceDenied("apim_api_alias_changed")
			}
			copy := apimSnapshot(kindName, listed)
			liveID := strings.ToLower(text(live["id"]))
			copy["id"], copy["name"] = liveID, last(liveID)
			if !nativeConfigurationContains(copy, apimSnapshot(kindName, live)) {
				return nil, serviceDenied("apim_listed_revision_changed")
			}
		}
		after, err := c.apimRevisions(ctx, base)
		if err != nil {
			return nil, err
		}
		if c.privateConfiguration(map[string]any{"revisions": revisions}) != c.privateConfiguration(map[string]any{"revisions": after}) {
			return nil, serviceDenied("apim_revision_index_changed")
		}
		return children, nil
	})
	var children []serviceChild
	for i := range bases {
		if errs[i] != nil {
			return nil, response{}, errs[i]
		}
		children = append(children, reads[i]...)
	}
	slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
	return children, provenance, nil
}
func (c *client) apimAPIPage(ctx context.Context, parent string) ([]any, string, response, error) {
	children, provenance, err := c.apimAPIsResult(ctx, parent)
	if err != nil {
		return nil, "", response{}, err
	}
	values := make([]any, 0, len(children))
	for _, child := range children {
		values = append(values, child.data)
	}
	return values, "", provenance, nil
}
