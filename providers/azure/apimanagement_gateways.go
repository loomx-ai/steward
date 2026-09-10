package azure

import (
	"context"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func apimGatewaySourceID(id string, raw map[string]any) (string, error) {
	source, kind, err := parseID(text(object(raw["properties"])["sourceId"]))
	if err != nil || !strings.EqualFold(kind, apimWorkspaceType) || strings.Split(source, "/")[2] != strings.Split(id, "/")[2] {
		return "", serviceDenied("invalid_apim_gateway_source")
	}
	return source, nil
}

func (c *client) apimGatewaySourceConfiguration(ctx context.Context, id string, raw map[string]any) (string, error) {
	sourceID, err := apimGatewaySourceID(id, raw)
	if err != nil {
		return "", err
	}
	source, err := c.apimResource(ctx, sourceID)
	if err != nil {
		return "", err
	}
	if err := apimReady(apimWorkspaceType, source); err != nil {
		return "", err
	}
	service, err := c.apimResource(ctx, apimRootID(sourceID))
	if err != nil {
		return "", err
	}
	if err := apimReady(apimServiceType, service); err != nil {
		return "", err
	}
	gateway, err := c.apimResource(ctx, apimRootID(id))
	if err != nil {
		return "", err
	}
	if err := apimReady(apimGatewayType, gateway); err != nil {
		return "", err
	}
	if resourceRegion(service) != resourceRegion(gateway) {
		return "", serviceDenied("apim_gateway_source_region_changed")
	}
	return c.privateConfiguration(map[string]any{
		"workspace": apimSnapshot(apimWorkspaceType, source), "workspace_etag": apimETag(apimWorkspaceType, source),
		"service": apimSnapshot(apimServiceType, service), "service_etag": apimETag(apimServiceType, service),
	}), nil
}

func apimGatewayPrerequisite(target, referrer asset.Asset) bool {
	return (strings.EqualFold(target.Identity.NativeType, apimServiceType) || strings.EqualFold(target.Identity.NativeType, apimWorkspaceType)) &&
		strings.EqualFold(referrer.Identity.NativeType, apimGatewayConnectionType)
}

func apimGatewaySourceMembership(parent string, children []serviceChild) error {
	if !strings.EqualFold(parent, apimGatewayType) {
		return nil
	}
	service := ""
	for _, child := range children {
		source, err := apimGatewaySourceID(child.id, child.data)
		if err != nil {
			return err
		}
		owner := apimRootID(source)
		if service != "" && owner != service {
			return serviceDenied("apim_gateway_source_service_changed")
		}
		service = owner
	}
	return nil
}
