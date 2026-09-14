package gcp

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const monitoringDashboardType = "monitoring.googleapis.com/Dashboard"
const monitoringDashboardReview = "_monitoring_dashboard_configuration"
const monitoringDashboardDelete = "monitoring.projects.dashboards.delete"

func (c *client) monitoringDashboardData(id string, data map[string]any) error {
	if err := checkListCompleteness(data); err != nil {
		return err
	}
	if err := cloudNatScalars(data, []string{"name", "displayName", "etag"}, nil, nil, nil); err != nil {
		return err
	}
	if !strings.HasPrefix(text(data["name"]), "projects/") || c.canonicalName("//monitoring.googleapis.com/"+text(data["name"])) != id {
		return groupDenied("monitoring_dashboard_identity_invalid")
	}
	kind, _ := findType(monitoringDashboardType)
	if _, err := c.resourceURL(kind, id); err != nil {
		return err
	}
	if text(data["displayName"]) == "" || text(data["etag"]) == "" {
		return groupDenied("monitoring_dashboard_configuration_missing")
	}
	if err := uptimeStringMap(data, "labels"); err != nil {
		return err
	}
	layouts := 0
	for _, key := range []string{"gridLayout", "mosaicLayout", "rowLayout", "columnLayout"} {
		if value, ok := data[key]; ok {
			if object(value) == nil {
				return groupDenied("monitoring_dashboard_layout_invalid")
			}
			layouts++
		}
	}
	if layouts != 1 {
		return groupDenied("monitoring_dashboard_layout_invalid")
	}
	if value, ok := data["annotations"]; ok && object(value) == nil {
		return groupDenied("monitoring_dashboard_annotations_invalid")
	}
	_, err := cloudNatObjects(data, "dashboardFilters")
	return err
}
func monitoringDashboardConfiguration(id string, data map[string]any) string {
	value := cloneParameters(data)
	value["name"] = id
	// Include etag and unknown fields; no conditional DELETE exists in this API.
	return firewallDigest(value)
}
func (c *client) monitoringDashboardRead(ctx context.Context, id string) (map[string]any, error) {
	kind, _ := findType(monitoringDashboardType)
	endpoint, err := c.resourceURL(kind, id)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return nil, err
	}
	if err := c.monitoringDashboardData(id, data); err != nil {
		return nil, err
	}
	return data, nil
}
func (c *client) monitoringDashboardInventory(ctx context.Context, id string, listed map[string]any) (map[string]any, error) {
	if err := c.monitoringDashboardData(id, listed); err != nil {
		return nil, err
	}
	live, err := c.monitoringDashboardRead(ctx, id)
	if err != nil {
		return nil, contracts.DependencyReadError(err)
	}
	if monitoringDashboardConfiguration(id, listed) != monitoringDashboardConfiguration(id, live) {
		return nil, groupDenied("monitoring_dashboard_configuration_changed")
	}
	return live, nil
}
func redactMonitoringDashboardPayload(data map[string]any) {
	name := text(data["name"])
	if !(strings.HasPrefix(name, "projects/") && strings.Contains(name, "/dashboards/")) && !strings.HasPrefix(name, "dashboards/") {
		return
	}
	// A new layout, query dialect or widget can carry private text. Keep only
	// declared display metadata and provider-derived review fields, not raw content.
	for key := range data {
		switch key {
		case "name", "displayName", "etag", "labels", monitoringDashboardReview, "_inventory_source", "project_id", "project_number", "cleanup_protected", "cleanup_protection_reason":
		default:
			data[key] = "[REDACTED]"
		}
	}
}
