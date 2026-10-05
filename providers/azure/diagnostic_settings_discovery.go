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

// diagnosticSourceConcurrency bounds one walk's in-flight source reads.
const diagnosticSourceConcurrency = 16

// diagnosticSourceAhead bounds how many index rows' subtrees run ahead of the
// ordered merge, so a large subscription does not park a goroutine per row.
const diagnosticSourceAhead = 4 * diagnosticSourceConcurrency

// diagnosticWalk is one walk's shared state. Only HTTP reads hold a slot;
// waiting on a shared read or on children holds none, so the walk cannot
// deadlock on its own bound.
type diagnosticWalk struct {
	ctx   context.Context
	reads sync.Map
	slots chan struct{}
	wg    sync.WaitGroup
}

// diagnosticSourceNode holds one visit's reads, made before the ordered merge
// that decides, as the serial visit did, which listing of a source is kept.
// done closes once the node's own reads are made and its children started.
type diagnosticSourceNode struct {
	raw, stored map[string]any
	id, kind    string
	early       bool // err precedes the duplicate check
	err         error
	children    []*diagnosticSourceNode
	done        chan struct{}
}

// Every node, at any depth, is read concurrently within one per-walk bound and
// merged in visit order, waiting on each node as the merge reaches it. The
// merge is the serial visit over those reads, so the same listing wins and the
// first error in visit order is returned; a node read ahead past that error is
// only discarded. Reads of one endpoint are shared within the walk, so a source
// listed both in the index and as a child is read once, as the serial visit
// did. Once the merge decides, outstanding reads are canceled.
func (c *client) diagnosticSourceWalk(ctx context.Context) (map[string]map[string]any, error) {
	rows, err := c.insightsARMIndex(ctx, c.root()+"/resources")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(ctx)
	w := &diagnosticWalk{ctx: ctx, slots: make(chan struct{}, diagnosticSourceConcurrency)}
	defer func() { cancel(); w.wg.Wait() }()
	sources := map[string]map[string]any{}
	var merge func(*diagnosticSourceNode) error
	merge = func(n *diagnosticSourceNode) error {
		<-n.done
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
	nodes := make([]*diagnosticSourceNode, len(rows))
	seen := map[string]bool{}
	for i, value := range rows {
		for j := i; j < min(i+diagnosticSourceAhead, len(rows)); j++ {
			if nodes[j] == nil {
				nodes[j] = c.diagnosticSourceExpand(w, object(rows[j]))
			}
		}
		raw := object(value)
		id, _, err := parseID(text(raw["id"]))
		if err != nil || seen[id] {
			return nil, serviceDenied("duplicate_diagnostic_source_index_identity")
		}
		seen[id] = true
		if err := merge(nodes[i]); err != nil {
			return nil, err
		}
	}
	return sources, nil
}

// diagnosticSourceExpand starts one node's reads and, once they succeed, its
// children's. The returned node is complete when its done channel closes.
func (c *client) diagnosticSourceExpand(w *diagnosticWalk, raw map[string]any) *diagnosticSourceNode {
	n := &diagnosticSourceNode{raw: raw, early: true, done: make(chan struct{})}
	w.wg.Add(1)
	go func() {
		defer w.wg.Done()
		defer close(n.done)
		n.err = c.diagnosticSourceVisit(w, n)
	}()
	return n
}

func (c *client) diagnosticSourceVisit(w *diagnosticWalk, n *diagnosticSourceNode) error {
	raw := n.raw
	id, kind, err := parseID(text(raw["id"]))
	if err != nil || !strings.HasPrefix(id, c.root()+"/") || text(raw["type"]) != "" && !validResponseType(kind, text(raw["type"])) || diagnosticSourceMetadata(raw) != nil {
		return serviceDenied("invalid_diagnostic_source_index_identity")
	}
	if rbacResourceKind(kind) != "" {
		return rbacListedIdentity(raw)
	}
	n.id, n.kind, n.early, n.stored = id, kind, false, raw
	mapping, known := findType(kind)
	var live map[string]any
	endpoint := ""
	if known && kind != strings.ToLower(diagnosticSettingsType) {
		if endpoint, err = c.resourceURL(mapping, responseID(mapping.NativeType, text(raw["id"]))); err != nil {
			return err
		}
		current, err := walkShared(w, "GET "+endpoint, func() (response, error) { return c.request(w.ctx, "GET", endpoint) })
		if err != nil {
			return err
		}
		if !insightsARMReadValid(current, id, kind) || diagnosticSourceMetadata(current.data) != nil || serviceListedIncarnation(raw, current.data) != nil {
			return serviceDenied("diagnostic_source_index_changed")
		}
		if isCosmosType(kind) && cosmosListedIncarnation(kind, raw, current.data) != nil {
			return serviceDenied("diagnostic_source_index_changed")
		}
		wire, wireErr := diagnosticSourceWire(text(raw["id"]))
		currentWire, currentErr := diagnosticSourceWire(responseID(mapping.NativeType, text(current.data["id"])))
		if wireErr != nil || currentErr != nil || wire != currentWire {
			return serviceDenied("diagnostic_source_index_name_changed")
		}
		if HasServiceCascade(mapping.NativeType) {
			live = batchClone(current.data)
		}
		n.stored = maps.Clone(current.data)
		n.stored["id"] = responseID(mapping.NativeType, text(current.data["id"]))
	}
	if !known || strings.EqualFold(kind, diagnosticSettingsType) {
		return nil
	}
	children, err := walkShared(w, "children "+endpoint, func() ([]map[string]any, error) { return c.childrenOf(w.ctx, mapping, n.stored, live) })
	if err != nil {
		return err
	}
	for _, child := range children {
		n.children = append(n.children, c.diagnosticSourceExpand(w, child))
	}
	return nil
}

// walkShared runs read once per key within one walk, holding one of the walk's
// slots; later callers wait for and share its result without a slot. read
// must not itself wait on another key.
func walkShared[T any](w *diagnosticWalk, key string, read func() (T, error)) (T, error) {
	value, _ := w.reads.LoadOrStore(key, &walkRead{})
	shared := value.(*walkRead)
	shared.once.Do(func() {
		select {
		case w.slots <- struct{}{}:
			defer func() { <-w.slots }()
		case <-w.ctx.Done():
		}
		if shared.err = w.ctx.Err(); shared.err == nil { // A free slot may race the cancel.
			shared.value, shared.err = read()
		}
	})
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
