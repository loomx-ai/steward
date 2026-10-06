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
	if recoveryServicesKind(kind) != "" {
		return recoveryServicesSource
	}
	if dataProtectionKind(kind) != "" {
		return dataProtectionSource
	}
	if strings.EqualFold(kind, deploymentStackType) {
		return deploymentStackSource
	}
	if netappKind(kind).kind != "" {
		return netappSource
	}
	if synapseBackupKind(kind) != "" {
		return synapseBackupSource
	}
	if synapseDataKind(kind).kind != "" {
		return synapseDataInventorySource
	}
	if synapseKind(kind) != "" {
		return synapseSource
	}
	if elasticSanKind(kind) != "" {
		return elasticSanSource
	}
	if azureLocalKind(kind) != "" {
		return azureLocalSource
	}
	if hybridComputeKind(kind) != "" {
		return hybridComputeSource
	}
	if strings.EqualFold(kind, defenderPricingType) {
		return defenderInventorySource
	}
	if strings.EqualFold(kind, managementGroupType) {
		return managementGroupSource
	}
	if strings.EqualFold(kind, keyVaultCertificateType) {
		return keyVaultCertificateSource
	}
	if _, ok := graphKindOf(kind); ok {
		return graphDirectorySource
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
	// Saved IDs are checked in order before any read. IDs past the first
	// invalid one are not read, and its error follows the earlier reads.
	type orphan struct{ id, parent string }
	var orphans []orphan
	var invalid error
	for _, value := range known {
		id, parent, kind, _, err := insightsLegacyIdentity(value)
		if err != nil || id != value || kind != insightsAnnotationType || !strings.HasPrefix(parent, c.root()+"/") || seen[id] {
			invalid = serviceDenied("invalid_insights_known_annotation")
			break
		}
		seen[id] = true
		if !parents[parent] {
			orphans = append(orphans, orphan{id, parent})
		}
	}
	_, errs := readConcurrently(len(orphans), func(i int) (struct{}, error) {
		if _, err := c.insightsComponent(ctx, orphans[i].parent); !isNotFound(err) {
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, serviceDenied("insights_annotation_parent_missing_from_index")
		}
		mapping, _ := findType(insightsAnnotationType)
		if _, err := c.insightsChildRead(ctx, mapping, orphans[i].id); !isNotFound(err) {
			if err != nil {
				return struct{}{}, err
			}
			return struct{}{}, serviceDenied("insights_annotation_survived_parent")
		}
		return struct{}{}, nil
	})
	for i, value := range orphans {
		if errs[i] != nil {
			return nil, errs[i]
		}
		absent = append(absent, value.id)
	}
	if invalid != nil {
		return nil, invalid
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
	var older []string
	for _, id := range known {
		_, owner, _, _, _ := insightsLegacyIdentity(id) // Already validated against this subscription.
		if owner == parent && !windowIDs[id] {
			older = append(older, id)
		}
	}
	reads, errs := readConcurrently(len(older), func(i int) (response, error) { return c.insightsChildRead(ctx, mapping, older[i]) })
	for i, id := range older {
		if isNotFound(errs[i]) {
			absent = append(absent, id)
			continue // This does not authorize closing any other missing observation.
		}
		if errs[i] != nil {
			return nil, nil, nil, errs[i]
		}
		children = append(children, serviceChild{id: id, kind: insightsAnnotationType, data: reads[i].data})
	}
	return children, windowIDs, absent, nil
}
