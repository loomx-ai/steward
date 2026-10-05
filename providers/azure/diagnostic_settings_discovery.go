package azure

import (
	"context"
	"maps"
	"slices"
	"strings"
	"sync"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Resource LIST does not enumerate settings on every resource. Discover exact
// resource scopes, expand the existing native child adapters, and reconcile
// persisted setting IDs separately. No source/group disappearance proves a
// setting absent, and Resource Graph has no supported diagnostic-settings index.
// Concurrent delete checks share one walk that starts after they arrive (see
// liveShared); each caller gets its own map of the shared rows.
func (c *client) diagnosticSourceCandidates(ctx context.Context) (map[string]map[string]any, error) {
	sources, err := liveShared(ctx, c, "diagnostic-sources", func() (map[string]map[string]any, error) { return c.diagnosticSourceWalk(ctx) })
	return maps.Clone(sources), err
}

// diagnosticSourceNode holds one visit's reads, made before the ordered merge
// that decides, as the serial visit did, which listing of a source is kept.
type diagnosticSourceNode struct {
	raw, stored map[string]any
	id, kind    string
	expanded    bool // false: not read ahead; the merge reads it if it gets there
	early       bool // err precedes the duplicate check
	err         error
	children    []*diagnosticSourceNode
}

// Each index row's subtree is read concurrently (serially within), then merged
// in index order. The merge is the serial visit over those reads, so the same
// listing wins and the first error in visit order is returned; a subtree the
// read-ahead skipped after a failure is read when the merge reaches it. Reads
// of one endpoint are shared within the walk, so a source listed both in the
// index and as a child is read once, as the serial visit did.
func (c *client) diagnosticSourceWalk(ctx context.Context) (map[string]map[string]any, error) {
	rows, err := c.insightsARMIndex(ctx, c.root()+"/resources")
	if err != nil {
		return nil, err
	}
	reads := &sync.Map{}
	nodes, _ := readConcurrently(len(rows), func(i int) (*diagnosticSourceNode, error) {
		return c.diagnosticSourceExpand(ctx, object(rows[i]), reads)
	})
	sources := map[string]map[string]any{}
	var merge func(*diagnosticSourceNode) error
	merge = func(n *diagnosticSourceNode) error {
		if !n.expanded {
			n, _ = c.diagnosticSourceExpand(ctx, n.raw, reads)
		}
		if n.early {
			return n.err
		}
		if previous := sources[n.id]; previous != nil {
			wire, wireErr := diagnosticSourceWire(text(n.raw["id"]))
			previousWire, previousErr := diagnosticSourceWire(text(previous["id"]))
			if wireErr != nil || previousErr != nil || wire != previousWire || serviceListedIncarnation(n.raw, previous) != nil {
				return serviceDenied("diagnostic_source_indexes_disagree")
			}
			return nil
		}
		if n.err != nil {
			return n.err
		}
		sources[n.id] = n.stored
		for _, child := range n.children {
			if err := merge(child); err != nil {
				return err
			}
		}
		if strings.EqualFold(n.kind, storageType) {
			// Storage's documented default service scopes are distinct sources.
			// Only native primaryEndpoints establish which services are available.
			for service, endpoint := range object(object(n.stored["properties"])["primaryEndpoints"]) {
				if slices.Contains([]string{"blob", "file", "queue", "table"}, service) && endpoint != nil && endpoint != "" {
					if _, ok := endpoint.(string); !ok {
						return serviceDenied("invalid_diagnostic_storage_service_endpoint")
					}
					scope := n.id + "/" + service + "services/default"
					sources[scope] = map[string]any{"id": scope, "type": storageType + "/" + service + "Services", "_diagnostic_storage_parent": n.stored}
				}
			}
		}
		return nil
	}
	seen := map[string]bool{}
	for i, value := range rows {
		raw := object(value)
		id, _, err := parseID(text(raw["id"]))
		if err != nil || seen[id] {
			return nil, serviceDenied("duplicate_diagnostic_source_index_identity")
		}
		seen[id] = true
		node := nodes[i]
		if node == nil { // Not started after an earlier failure.
			node = &diagnosticSourceNode{raw: raw}
		}
		if err := merge(node); err != nil {
			return nil, err
		}
	}
	return sources, nil
}

// diagnosticSourceExpand makes one serial visit's reads for raw and its
// subtree, returning the subtree's first error. After a failing child the
// remaining children are left unread.
func (c *client) diagnosticSourceExpand(ctx context.Context, raw map[string]any, reads *sync.Map) (*diagnosticSourceNode, error) {
	n := &diagnosticSourceNode{raw: raw, expanded: true, early: true}
	id, kind, err := parseID(text(raw["id"]))
	if err != nil || !strings.HasPrefix(id, c.root()+"/") || text(raw["type"]) != "" && !validResponseType(kind, text(raw["type"])) || diagnosticSourceMetadata(raw) != nil {
		n.err = serviceDenied("invalid_diagnostic_source_index_identity")
		return n, n.err
	}
	if rbacResourceKind(kind) != "" {
		n.err = rbacListedIdentity(raw)
		return n, n.err
	}
	n.id, n.kind, n.early, n.stored = id, kind, false, raw
	fail := func(err error) (*diagnosticSourceNode, error) { n.err = err; return n, err }
	mapping, known := findType(kind)
	var live map[string]any
	endpoint := ""
	if known && kind != strings.ToLower(diagnosticSettingsType) {
		if endpoint, err = c.resourceURL(mapping, responseID(mapping.NativeType, text(raw["id"]))); err != nil {
			return fail(err)
		}
		current, err := walkShared(reads, "GET "+endpoint, func() (response, error) { return c.request(ctx, "GET", endpoint) })
		if err != nil {
			return fail(err)
		}
		if !insightsARMReadValid(current, id, kind) || diagnosticSourceMetadata(current.data) != nil || serviceListedIncarnation(raw, current.data) != nil {
			return fail(serviceDenied("diagnostic_source_index_changed"))
		}
		if isCosmosType(kind) && cosmosListedIncarnation(kind, raw, current.data) != nil {
			return fail(serviceDenied("diagnostic_source_index_changed"))
		}
		wire, wireErr := diagnosticSourceWire(text(raw["id"]))
		currentWire, currentErr := diagnosticSourceWire(responseID(mapping.NativeType, text(current.data["id"])))
		if wireErr != nil || currentErr != nil || wire != currentWire {
			return fail(serviceDenied("diagnostic_source_index_name_changed"))
		}
		if HasServiceCascade(mapping.NativeType) {
			live = batchClone(current.data)
		}
		n.stored = maps.Clone(current.data)
		n.stored["id"] = responseID(mapping.NativeType, text(current.data["id"]))
	}
	if !known || strings.EqualFold(kind, diagnosticSettingsType) {
		return n, nil
	}
	children, err := walkShared(reads, "children "+endpoint, func() ([]map[string]any, error) { return c.childrenOf(ctx, mapping, n.stored, live) })
	if err != nil {
		return fail(err)
	}
	for i, child := range children {
		node, err := c.diagnosticSourceExpand(ctx, child, reads)
		n.children = append(n.children, node)
		if err != nil {
			for _, rest := range children[i+1:] {
				n.children = append(n.children, &diagnosticSourceNode{raw: rest})
			}
			return n, err
		}
	}
	return n, nil
}

// walkShared runs read once per key within one walk; later callers wait for
// and share its result. read must not itself wait on another key.
func walkShared[T any](reads *sync.Map, key string, read func() (T, error)) (T, error) {
	value, _ := reads.LoadOrStore(key, &walkRead{})
	shared := value.(*walkRead)
	shared.once.Do(func() { shared.value, shared.err = read() })
	result, _ := shared.value.(T)
	return result, shared.err
}

type walkRead struct {
	once  sync.Once
	value any
	err   error
}

func (c *client) diagnosticCollection(ctx context.Context, known, extraScopes []string) (settings map[string]map[string]any, sources map[string]map[string]any, requestID string, err error) {
	defer func() { err = contracts.DependencyReadError(err) }()
	hints, scopes := map[string]string{}, map[string]string{c.root(): c.root()}
	for _, wire := range known {
		id, _, kind, err := diagnosticResourceID(wire)
		selector, wireErr := diagnosticWireID(wire)
		if err != nil || wireErr != nil || wire != selector || kind != diagnosticSettingsType || !strings.HasPrefix(id, c.root()+"/") || hints[id] != "" {
			return nil, nil, "", serviceDenied("invalid_known_diagnostic_identity")
		}
		hints[id] = wire
	}
	addScope := func(wire string) error {
		id, err := diagnosticScope(wire)
		selector, wireErr := diagnosticSourceWire(wire)
		if err != nil || wireErr != nil || wire != selector || id != c.root() && !strings.HasPrefix(id, c.root()+"/") || scopes[id] != "" && scopes[id] != wire {
			return serviceDenied("invalid_diagnostic_discovery_scope")
		}
		scopes[id] = wire
		return nil
	}
	for _, scope := range extraScopes {
		if err := addScope(scope); err != nil {
			return nil, nil, "", err
		}
	}
	sources, err = c.diagnosticSourceCandidates(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	for id := range sources {
		if canonical, _, kind, err := diagnosticResourceID(id); err == nil {
			if kind == diagnosticSettingsType {
				wire, err := diagnosticWireID(text(sources[id]["id"]))
				if err != nil || hints[canonical] != "" && hints[canonical] != wire {
					return nil, nil, "", serviceDenied("diagnostic_index_native_selector_changed")
				}
				hints[canonical] = wire
			}
			continue
		}
		wire, err := diagnosticSourceWire(text(sources[id]["id"]))
		if err != nil {
			return nil, nil, "", err
		}
		if err := addScope(wire); err != nil {
			return nil, nil, "", err
		}
	}
	// Reads run concurrently; results and the first error are taken in order.
	seeds := map[string]map[string]any{}
	ids := slices.Sorted(maps.Keys(hints))
	reads, errs := readConcurrently(len(ids), func(i int) (response, error) {
		return c.diagnosticRead(ctx, hints[ids[i]], diagnosticSettingsType)
	})
	for i, id := range ids {
		if isNotFound(errs[i]) {
			continue // Only this persisted setting's own native GET proves absence.
		}
		if errs[i] != nil {
			return nil, nil, "", errs[i]
		}
		if err := addScope(diagnosticWireScope(hints[id])); err != nil {
			return nil, nil, "", err
		}
		seeds[id] = reads[i].data
	}
	settings = map[string]map[string]any{}
	ids = slices.Sorted(maps.Keys(scopes))
	type scopeIndex struct {
		values     map[string]map[string]any
		provenance string
	}
	indexes, errs := readConcurrently(len(ids), func(i int) (scopeIndex, error) {
		// Concurrent delete checks coalesce each scope's list; see liveShared.
		// The shared values are only copied out.
		return liveShared(ctx, c, "diagnostic-index:"+scopes[ids[i]], func() (scopeIndex, error) {
			values, provenance, err := c.diagnosticIndex(ctx, scopes[ids[i]], diagnosticSettingsType)
			return scopeIndex{values, provenance}, err
		})
	})
	for i := range ids {
		if errs[i] != nil {
			return nil, nil, "", errs[i]
		}
		maps.Copy(settings, indexes[i].values)
		if indexes[i].provenance != "" {
			requestID = indexes[i].provenance
		}
	}
	for id, raw := range seeds {
		if current := settings[id]; current == nil || c.privateConfiguration(diagnosticSnapshot(raw)) != c.privateConfiguration(diagnosticSnapshot(current)) {
			return nil, nil, "", serviceDenied("diagnostic_hint_missing_from_native_index")
		}
	}
	return settings, sources, requestID, nil
}
