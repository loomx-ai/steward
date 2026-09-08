package azure

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const privateEndpointType = "Microsoft.Network/privateEndpoints"

func privateEndpointNICs(properties map[string]any) ([]string, error) {
	values, ok := properties["networkInterfaces"].([]any)
	if !ok {
		return nil, fmt.Errorf("private endpoint omitted its managed NIC collection")
	}
	ids := []string{}
	for _, value := range values {
		id, kind, err := parseID(text(object(value)["id"]))
		if err != nil || !strings.EqualFold(kind, nicType) || slices.Contains(ids, id) {
			return nil, fmt.Errorf("invalid or duplicate private endpoint NIC identity")
		}
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids, nil
}

// The native Network provider reports this read-only NIC collection and the
// NIC's reciprocal privateEndpoint identity. These NICs share the endpoint's
// lifetime; ordinary NICs and manually created DNS records do not.
// https://learn.microsoft.com/azure/private-link/private-endpoint-overview
// https://learn.microsoft.com/azure/private-link/private-endpoint-dns-integration
func (c *client) privateEndpointChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	ids, err := privateEndpointNICs(object(raw["properties"]))
	if err != nil {
		return nil, err
	}
	children, err := c.nativeServiceChildren(ctx, parent, raw, []string{privateDNSZoneGroupType})
	if err != nil {
		return nil, err
	}
	kind, _ := findType(nicType)
	for _, id := range ids {
		endpoint, err := c.resourceURL(kind, id)
		if err != nil {
			return nil, err
		}
		current, err := c.request(ctx, "GET", endpoint)
		if err != nil {
			return nil, err
		}
		properties := object(current.data["properties"])
		if !validResourceResponse(current, id, nicType) || !strings.EqualFold(text(object(properties["privateEndpoint"])["id"]), parent.NativeID) || text(object(properties["virtualMachine"])["id"]) != "" {
			return nil, fmt.Errorf("private endpoint NIC ownership mismatch")
		}
		if _, ok := properties["ipConfigurations"].([]any); !ok {
			return nil, fmt.Errorf("private endpoint NIC omitted its IP configurations")
		}
		attachments, err := resourceAttachments(c.subscription, nicType, properties)
		if err != nil {
			return nil, err
		}
		if len(attachments) != 0 {
			return nil, fmt.Errorf("private endpoint NIC has an unexpected public IP attachment")
		}
		children = append(children, serviceChild{id: id, kind: nicType, data: current.data})
	}
	return children, nil
}

func privateEndpointNICRelation(parent, child asset.Asset) bool {
	ids, err := privateEndpointNICs(parent.Normalized)
	return err == nil && slices.Contains(ids, strings.ToLower(child.Identity.NativeID)) &&
		strings.EqualFold(text(object(child.Normalized["privateEndpoint"])["id"]), parent.Identity.NativeID)
}
