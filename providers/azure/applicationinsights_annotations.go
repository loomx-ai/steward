package azure

import (
	"context"
	"strings"
	"time"
)

// Time-window omissions never close older observations. Only an explicit known
// identity's own native absence can close it through the inventory worker.
const insightsAnnotationSource = "application-insights-annotations"

func insightsInventorySource(kind string) string {
	if strings.EqualFold(kind, defenderPricingType) {
		return defenderInventorySource
	}
	if dataMigrationKind(kind) != "" {
		return dataMigrationInventorySource
	}
	if dataFactoryKind(kind) != "" {
		return dataFactoryInventorySource
	}
	if communicationKind(kind) != "" {
		return communicationInventorySource
	}
	if fleetKind(kind).kind != "" {
		return fleetInventorySource
	}
	if strings.EqualFold(kind, diagnosticSettingsType) {
		return diagnosticInventorySource
	}
	if strings.EqualFold(kind, insightsAnnotationType) {
		return insightsAnnotationSource
	}
	if insightsWorkbookKind(kind) == insightsWorkbookType || insightsWorkbookKind(kind) == insightsMyWorkbookType {
		return insightsWorkbookSource
	}
	return productInventorySource
}

type insightsInventoryCursor struct {
	productCursor
	Window insightsAnnotationWindow `json:"annotation_window,omitempty"`
}

func insightsRecentAnnotationWindow() insightsAnnotationWindow {
	end := time.Now().UTC()
	// Leave an hour inside Azure's rolling 90-day limit for a scan to finish.
	// Every page keeps these exact bounds; an expired cursor requires a rescan.
	return insightsAnnotationWindow{Start: end.Add(-90*24*time.Hour + time.Hour).Format(time.RFC3339Nano), End: end.Format(time.RFC3339Nano)}
}

// A saved ID is only a discovery hint. It must have this subscription's native
// annotation identity, and an omitted parent needs its own native GET.
func (c *client) insightsAnnotationParents(ctx context.Context, components []serviceChild, known []string) ([]string, error) {
	parents, seen := map[string]bool{}, map[string]bool{}
	var absent []string
	for _, component := range components {
		parents[component.id] = true
	}
	for _, value := range known {
		id, parent, kind, _, err := insightsLegacyIdentity(value)
		if err != nil || id != value || kind != insightsAnnotationType || !strings.HasPrefix(parent, c.root()+"/") || seen[id] {
			return nil, serviceDenied("invalid_insights_known_annotation")
		}
		seen[id] = true
		if parents[parent] {
			continue
		}
		if _, err := c.insightsComponent(ctx, parent); !isNotFound(err) {
			if err != nil {
				return nil, err
			}
			return nil, serviceDenied("insights_annotation_parent_missing_from_index")
		}
		mapping, _ := findType(insightsAnnotationType)
		if _, err := c.insightsChildRead(ctx, mapping, id); !isNotFound(err) {
			if err != nil {
				return nil, err
			}
			return nil, serviceDenied("insights_annotation_survived_parent")
		}
		absent = append(absent, id)
	}
	return absent, nil
}

func (c *client) insightsAnnotationInventoryChildren(ctx context.Context, parent string, window insightsAnnotationWindow, known []string) ([]serviceChild, map[string]bool, []string, error) {
	children, err := c.insightsLegacyChildren(ctx, parent, insightsAnnotationType, window)
	if err != nil {
		return nil, nil, nil, err
	}
	var absent []string
	windowIDs := map[string]bool{}
	for _, child := range children {
		windowIDs[child.id] = true
	}
	mapping, _ := findType(insightsAnnotationType)
	for _, id := range known {
		_, owner, _, _, _ := insightsLegacyIdentity(id) // Already validated against this subscription.
		if owner != parent || windowIDs[id] {
			continue
		}
		current, err := c.insightsChildRead(ctx, mapping, id)
		if isNotFound(err) {
			absent = append(absent, id)
			continue // This does not authorize closing any other missing observation.
		}
		if err != nil {
			return nil, nil, nil, err
		}
		children = append(children, serviceChild{id: id, kind: insightsAnnotationType, data: current.data})
	}
	return children, windowIDs, absent, nil
}
