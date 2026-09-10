package azure

import (
	"context"
	"net/url"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// URL and client-ID references are resolved from an unfiltered native ARM
// index. Matching entries still require a GET before they establish a binding.
func (c *client) subscriptionReferenceIndex(ctx context.Context, kind string) ([]map[string]any, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	runtime := &Runtime{bundle: metadata.bundle}
	definition, ok := runtime.productDefinition(kind)
	if !ok || definition.Discovery.List == nil || definition.Discovery.Parent != nil {
		return nil, serviceDenied("reference_index_unavailable")
	}
	bound, err := c.bindProductList(definition.Discovery.List, "", contracts.InventoryItem{})
	if err != nil {
		return nil, err
	}
	u, _ := url.Parse(bound.URL)
	rows, err := c.listAllURL(ctx, bound.URL, u.Path)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var values []map[string]any
	for _, row := range rows {
		value := object(row)
		id, typ, err := parseID(text(value["id"]))
		if err != nil || !strings.EqualFold(typ, kind) || !validResponseType(kind, text(value["type"])) || !strings.HasPrefix(id, c.root()+"/") || seen[id] || !strings.EqualFold(text(value["name"]), last(id)) {
			return nil, serviceDenied("invalid_reference_index")
		}
		seen[id] = true
		values = append(values, value)
	}
	return values, nil
}
