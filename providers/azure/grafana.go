package azure

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const grafanaType = "Microsoft.Dashboard/grafana"
const grafanaPrivateEndpointType = grafanaType + "/managedPrivateEndpoints"
const grafanaConnectionType = grafanaType + "/privateEndpointConnections"
const grafanaIntegrationType = grafanaType + "/integrationFabrics"

var grafanaOperationPath = regexp.MustCompile(`(?i)^/providers/Microsoft\.Dashboard/locations/([a-z0-9]+)/operationStatuses/([a-f0-9]{8}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{4}-[a-f0-9]{12}\*[a-f0-9]{64})$`)

func grafanaGlobalOperation(endpoint string) bool {
	u, err := url.Parse(endpoint)
	return err == nil && strings.HasPrefix(strings.ToLower(u.Path), "/providers/")
}

// Microsoft CLI recordings include this signed ProviderHub path rather than
// a subscription-scoped poll URL. Only returned Grafana operations can use it.
func validateGrafanaGlobalOperation(endpoint, location string) error {
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host != "management.azure.com" || u.User != nil || u.Fragment != "" || (u.RawPath != "" && u.RawPath != u.Path) || len(endpoint) > 32*1024 {
		return fmt.Errorf("invalid Grafana polling endpoint")
	}
	match := grafanaOperationPath.FindStringSubmatch(u.Path)
	if match == nil || (location != "" && !strings.EqualFold(match[1], location)) {
		return fmt.Errorf("Grafana polling endpoint belongs to another provider or location")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query) != 5 {
		return fmt.Errorf("invalid Grafana signed polling parameters")
	}
	for _, key := range []string{"api-version", "t", "c", "s", "h"} {
		if len(query[key]) != 1 || query.Get(key) == "" {
			return fmt.Errorf("invalid Grafana signed polling parameter")
		}
	}
	return nil
}

func (a *action) operationBinding(endpoint string) string {
	return fmt.Sprintf("%x", sha256.Sum256([]byte(a.client.subscription+"\x00"+a.client.tenant+"\x00"+a.id+"\x00"+endpoint)))
}

func (a *action) grafanaOperationResponse(endpoint string, res response) error {
	if !isGrafanaType(a.kind.NativeType) {
		return nil
	}
	if res.status != 200 && res.status != 202 && res.status != 204 {
		return fmt.Errorf("incomplete Grafana polling response")
	}
	u, _ := url.Parse(endpoint)
	for field, expected := range map[string]string{"resourceId": a.id, "id": u.Path, "name": last(u.Path)} {
		value, present := res.data[field]
		if (grafanaGlobalOperation(endpoint) && res.status != 204) || present {
			if !strings.EqualFold(text(value), expected) {
				return fmt.Errorf("Grafana polling response belongs to another resource or operation")
			}
		}
	}
	return nil
}

func isGrafanaType(kind string) bool {
	return kind == grafanaType || kind == grafanaPrivateEndpointType || kind == grafanaConnectionType || kind == grafanaIntegrationType
}

// Grafana does not expose an ETag. Freeze its native management configuration,
// excluding provisioning progress and the separately reviewed connection list.
// This detects drift; the native DELETE has no atomic conditional header.
func grafanaConfiguration(kind string, raw map[string]any) string {
	safe := safePayload(raw)
	delete(object(safe["properties"]), "provisioningState")
	if kind == grafanaType {
		delete(object(safe["properties"]), "privateEndpointConnections")
	}
	if kind == grafanaConnectionType {
		delete(safe, "location") // ProxyResource inherits inventory location from its parent.
	}
	return serviceParentConfiguration(kind, safe)
}

func grafanaIncarnation(planned asset.Asset, live map[string]any) error {
	if !isGrafanaType(planned.Identity.NativeType) {
		return nil
	}
	expected := text(planned.Normalized["_grafana_configuration"])
	if expected == "" || expected != grafanaConfiguration(planned.Identity.NativeType, live) {
		return serviceDenied("grafana_configuration_changed")
	}
	return nil
}

func (c *client) grafanaParent(ctx context.Context, id string) (map[string]any, error) {
	parts := strings.Split(id, "/")
	parentID := strings.Join(parts[:len(parts)-2], "/")
	kind, _ := findType(grafanaType)
	endpoint, err := c.resourceURL(kind, parentID)
	if err != nil {
		return nil, err
	}
	result, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return nil, err
	}
	if !validResourceResponse(result, parentID, grafanaType) {
		return nil, serviceDenied("grafana_parent_identity_changed")
	}
	return result.data, nil
}

func (a *action) grafanaParentPreflight(ctx context.Context, planned asset.Asset) error {
	if !isGrafanaType(a.kind.NativeType) || a.kind.NativeType == grafanaType {
		return nil
	}
	parent, err := a.client.grafanaParent(ctx, a.id)
	if err != nil {
		return err
	}
	if expected := text(planned.Normalized["_grafana_parent_generation"]); expected == "" || expected != productGeneration(parent) {
		return serviceDenied("grafana_parent_changed")
	}
	return nil
}

// Reconcile two complete native lists/GETs; an embedded connection list is not
// an authoritative inventory of all three independently deletable child kinds.
func (c *client) grafanaChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	first, err := c.nativeServiceChildren(ctx, parent, raw, serviceChildKinds(grafanaType))
	if err != nil {
		return nil, err
	}
	second, err := c.nativeServiceChildren(ctx, parent, raw, serviceChildKinds(grafanaType))
	if err != nil {
		return nil, err
	}
	if len(first) != len(second) {
		return nil, serviceDenied("grafana_children_changed")
	}
	for i := range first {
		if first[i].id != second[i].id || productGeneration(first[i].data) != productGeneration(second[i].data) {
			return nil, serviceDenied("grafana_children_changed")
		}
	}
	return second, nil
}

func grafanaKind(raw map[string]any) string {
	_, nativeType, err := parseID(text(raw["id"]))
	if err == nil {
		for _, kind := range []string{grafanaType, grafanaPrivateEndpointType, grafanaConnectionType, grafanaIntegrationType} {
			if strings.EqualFold(kind, nativeType) {
				return kind
			}
		}
	}
	return ""
}
