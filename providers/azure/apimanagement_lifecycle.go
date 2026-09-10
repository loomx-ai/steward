package azure

import (
	"context"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func (c *client) apimChildren(ctx context.Context, parent asset.Identity, raw map[string]any) ([]serviceChild, error) {
	collect := func() ([]serviceChild, error) {
		kinds := slices.DeleteFunc(apimOwnedKinds(parent.NativeType), func(kind string) bool { return isAPIMAPI(kind) })
		children, err := c.nativeServiceChildren(ctx, parent, raw, kinds)
		if err != nil {
			return nil, err
		}
		if strings.EqualFold(parent.NativeType, apimServiceType) || strings.EqualFold(parent.NativeType, apimWorkspaceType) {
			apis, err := c.apimAPIs(ctx, parent.NativeID)
			if err != nil {
				return nil, err
			}
			children = append(children, apis...)
			if strings.EqualFold(parent.NativeType, apimServiceType) {
				issues, err := c.apimIssues(ctx, parent.NativeID)
				if err != nil {
					return nil, err
				}
				for id := range issues {
					if !slices.ContainsFunc(apis, func(api serviceChild) bool { return api.id == redisParentID(id) }) {
						return nil, serviceDenied("apim_issue_api_missing_from_index")
					}
				}
			}
		}
		current, err := c.apimResource(ctx, parent.NativeID)
		if err != nil {
			return nil, err
		}
		if err := apimReady(parent.NativeType, current); err != nil {
			return nil, err
		}
		if c.privateConfiguration(apimSnapshot(parent.NativeType, raw)) != c.privateConfiguration(apimSnapshot(parent.NativeType, current)) {
			return nil, serviceDenied("apim_parent_changed")
		}
		for _, child := range children {
			if err := apimReady(child.kind, child.data); err != nil {
				return nil, err
			}
		}
		slices.SortFunc(children, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
		return children, nil
	}
	first, err := collect()
	if err != nil {
		return nil, err
	}
	second, err := collect()
	if err != nil {
		return nil, err
	}
	if len(first) != len(second) {
		return nil, serviceDenied("apim_children_changed")
	}
	for i, child := range first {
		if child.id != second[i].id || child.kind != second[i].kind || c.privateConfiguration(apimSnapshot(child.kind, child.data)) != c.privateConfiguration(apimSnapshot(second[i].kind, second[i].data)) {
			return nil, serviceDenied("apim_children_changed")
		}
	}
	return second, nil
}
