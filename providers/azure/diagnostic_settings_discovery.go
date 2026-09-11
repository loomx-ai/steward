package azure

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Resource LIST does not enumerate settings on every resource. Discover exact
// resource scopes, expand the existing native child adapters, and reconcile
// persisted setting IDs separately. No source/group disappearance proves a
// setting absent, and Resource Graph has no supported diagnostic-settings index.
func (c *client) diagnosticSourceCandidates(ctx context.Context) (map[string]map[string]any, error) {
	rows, err := c.insightsARMIndex(ctx, c.root()+"/resources")
	if err != nil {
		return nil, err
	}
	sources := map[string]map[string]any{}
	var visit func(map[string]any) error
	visit = func(raw map[string]any) error {
		id, kind, err := parseID(text(raw["id"]))
		if err != nil || !strings.HasPrefix(id, c.root()+"/") || text(raw["type"]) != "" && !validResponseType(kind, text(raw["type"])) || diagnosticSourceMetadata(raw) != nil {
			return serviceDenied("invalid_diagnostic_source_index_identity")
		}
		if rbacResourceKind(kind) != "" {
			return rbacListedIdentity(raw)
		}
		if previous := sources[id]; previous != nil {
			wire, wireErr := diagnosticSourceWire(text(raw["id"]))
			previousWire, previousErr := diagnosticSourceWire(text(previous["id"]))
			if wireErr != nil || previousErr != nil || wire != previousWire || serviceListedIncarnation(raw, previous) != nil {
				return serviceDenied("diagnostic_source_indexes_disagree")
			}
			return nil
		}
		mapping, known := findType(kind)
		if known && kind != strings.ToLower(diagnosticSettingsType) {
			endpoint, err := c.resourceURL(mapping, responseID(mapping.NativeType, text(raw["id"])))
			if err != nil {
				return err
			}
			current, err := c.request(ctx, "GET", endpoint)
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
			raw = maps.Clone(current.data)
			raw["id"] = responseID(mapping.NativeType, text(current.data["id"]))
		}
		sources[id] = raw
		if !known || strings.EqualFold(kind, diagnosticSettingsType) {
			return nil
		}
		children, err := c.children(ctx, mapping, raw)
		if err != nil {
			return err
		}
		for _, child := range children {
			if err := visit(child); err != nil {
				return err
			}
		}
		if strings.EqualFold(kind, storageType) {
			// Storage's documented default service scopes are distinct sources.
			// Only native primaryEndpoints establish which services are available.
			for service, endpoint := range object(object(raw["properties"])["primaryEndpoints"]) {
				if slices.Contains([]string{"blob", "file", "queue", "table"}, service) && endpoint != nil && endpoint != "" {
					if _, ok := endpoint.(string); !ok {
						return serviceDenied("invalid_diagnostic_storage_service_endpoint")
					}
					scope := id + "/" + service + "services/default"
					sources[scope] = map[string]any{"id": scope, "type": storageType + "/" + service + "Services", "_diagnostic_storage_parent": raw}
				}
			}
		}
		return nil
	}
	seen := map[string]bool{}
	for _, value := range rows {
		raw := object(value)
		id, _, err := parseID(text(raw["id"]))
		if err != nil || seen[id] {
			return nil, serviceDenied("duplicate_diagnostic_source_index_identity")
		}
		seen[id] = true
		if err := visit(raw); err != nil {
			return nil, err
		}
	}
	return sources, nil
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
	seeds := map[string]map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(hints)) {
		current, err := c.diagnosticRead(ctx, hints[id], diagnosticSettingsType)
		if isNotFound(err) {
			continue // Only this persisted setting's own native GET proves absence.
		}
		if err != nil {
			return nil, nil, "", err
		}
		if err := addScope(diagnosticWireScope(hints[id])); err != nil {
			return nil, nil, "", err
		}
		seeds[id] = current.data
	}
	settings = map[string]map[string]any{}
	for _, id := range slices.Sorted(maps.Keys(scopes)) {
		scope := scopes[id]
		values, provenance, err := c.diagnosticIndex(ctx, scope, diagnosticSettingsType)
		if err != nil {
			return nil, nil, "", err
		}
		maps.Copy(settings, values)
		if provenance != "" {
			requestID = provenance
		}
	}
	for id, raw := range seeds {
		if current := settings[id]; current == nil || c.privateConfiguration(diagnosticSnapshot(raw)) != c.privateConfiguration(diagnosticSnapshot(current)) {
			return nil, nil, "", serviceDenied("diagnostic_hint_missing_from_native_index")
		}
	}
	return settings, sources, requestID, nil
}
