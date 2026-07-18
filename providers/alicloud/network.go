package alicloud

import (
	"context"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/loomx-ai/steward/internal/provider/spec"
)

func (r *Runtime) SearchNetworkTargets(ctx context.Context, query contracts.NetworkTargetQuery) (contracts.NetworkTargetPage, error) {
	if query.Kind != asset.ScanTargetVPC && query.Kind != asset.ScanTargetVSwitch {
		return contracts.NetworkTargetPage{}, fmt.Errorf("Alibaba Cloud network target kind %q is unsupported", query.Kind)
	}
	region := strings.TrimSpace(query.RegionID)
	if region == "" {
		return contracts.NetworkTargetPage{}, fmt.Errorf("Alibaba Cloud network target region is required")
	}
	nativeType := vpcNativeType
	if query.Kind == asset.ScanTargetVSwitch {
		nativeType = vSwitchNativeType
	}
	kind := r.resourceKind(nativeType)
	if kind.ID == "" {
		return contracts.NetworkTargetPage{}, fmt.Errorf("Alibaba Cloud network target type %q is not registered", nativeType)
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	options := map[string]any{}
	if expression := strings.TrimSpace(query.Query); expression != "" {
		options["search_expression"] = expression
	}
	if query.Kind == asset.ScanTargetVSwitch {
		if parent := strings.TrimSpace(query.ParentNativeID); parent != "" {
			options["vpc_id"] = parent
		}
	}
	batch, err := r.List(ctx, contracts.InventoryRequest{
		ConnectionID: query.ConnectionID,
		Scope: asset.Scope{
			Kind: asset.ScopeRegion, NativeID: region, Name: region, Location: region,
		},
		Source: "resource-center", ResourceKind: &kind, Cursor: query.Cursor, Limit: limit,
		Options: options,
	})
	if err != nil {
		return contracts.NetworkTargetPage{}, err
	}
	needle := strings.ToLower(strings.TrimSpace(query.Query))
	page := contracts.NetworkTargetPage{RequestID: batch.RequestID, NextCursor: batch.NextCursor}
	for _, item := range batch.Items {
		nativeID := strings.TrimSpace(item.NativeID)
		name := strings.TrimSpace(item.Name)
		if nativeID == "" {
			continue
		}
		if !networkOptionMatches(needle, nativeID, name) {
			continue
		}
		parentNativeID := ""
		if query.Kind == asset.ScanTargetVSwitch {
			for _, key := range []string{"vpc_id", "vpcId"} {
				if parentNativeID = strings.TrimSpace(stringValue(item.Normalized[key])); parentNativeID != "" {
					break
				}
			}
		}
		page.Items = append(page.Items, contracts.NetworkTargetOption{
			Kind:           query.Kind,
			RegionID:       region,
			NativeID:       nativeID,
			Name:           name,
			ParentNativeID: parentNativeID,
		})
	}
	return page, nil
}

func (r *Runtime) networkTargetSpec(kind asset.ScanTargetKind) (spec.CompiledSpec, error) {
	nativeType := vpcNativeType
	if kind == asset.ScanTargetVSwitch {
		nativeType = vSwitchNativeType
	}
	for _, compiled := range r.bundle.Specs {
		if compiled.ResourceKind.NativeType != nativeType {
			continue
		}
		if compiled.Definition.Discovery.List == nil {
			return spec.CompiledSpec{}, fmt.Errorf(
				"Alibaba Cloud network target type %q has no product API list spec",
				nativeType,
			)
		}
		return compiled, nil
	}
	return spec.CompiledSpec{}, fmt.Errorf(
		"Alibaba Cloud network target type %q has no resource spec",
		nativeType,
	)
}

func networkOptionMatches(needle string, values ...string) bool {
	if needle == "" {
		return true
	}
	for _, value := range values {
		if strings.Contains(strings.ToLower(value), needle) {
			return true
		}
	}
	return false
}
