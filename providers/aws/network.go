package aws

import (
	"context"
	"fmt"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type NetworkListRequest struct {
	ParentNativeID string
	Cursor         string
	Limit          int
}

type NetworkItem struct {
	NativeID       string
	Name           string
	ParentNativeID string
}

type NetworkPage struct {
	Items     []NetworkItem
	NextToken string
	RequestID string
}

type NetworkClient interface {
	ListVPCs(context.Context, NetworkListRequest) (NetworkPage, error)
	ListVSwitches(context.Context, NetworkListRequest) (NetworkPage, error)
}

func (r *Runtime) SearchNetworkTargets(ctx context.Context, query contracts.NetworkTargetQuery) (contracts.NetworkTargetPage, error) {
	if query.Kind != asset.ScanTargetVPC && query.Kind != asset.ScanTargetVSwitch {
		return contracts.NetworkTargetPage{}, fmt.Errorf("AWS network target kind %q is unsupported", query.Kind)
	}
	region := strings.TrimSpace(query.RegionID)
	if region == "" {
		return contracts.NetworkTargetPage{}, fmt.Errorf("AWS network target region is required")
	}
	limit := query.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	credential, err := r.resolveCredential(ctx, query.ConnectionID)
	if err != nil {
		return contracts.NetworkTargetPage{}, err
	}
	client, err := r.factory.Network(ctx, credential, region)
	if err != nil {
		return contracts.NetworkTargetPage{}, NormalizeError(err)
	}
	request := NetworkListRequest{ParentNativeID: strings.TrimSpace(query.ParentNativeID), Cursor: strings.TrimSpace(query.Cursor), Limit: limit}
	var providerPage NetworkPage
	if query.Kind == asset.ScanTargetVPC {
		providerPage, err = client.ListVPCs(ctx, request)
	} else {
		providerPage, err = client.ListVSwitches(ctx, request)
	}
	if err != nil {
		return contracts.NetworkTargetPage{}, NormalizeError(err)
	}
	needle := strings.ToLower(strings.TrimSpace(query.Query))
	page := contracts.NetworkTargetPage{NextCursor: providerPage.NextToken, RequestID: providerPage.RequestID}
	for _, item := range providerPage.Items {
		if !networkOptionMatches(needle, item.NativeID, item.Name) {
			continue
		}
		page.Items = append(page.Items, contracts.NetworkTargetOption{
			Kind: query.Kind, RegionID: region, NativeID: item.NativeID, Name: item.Name, ParentNativeID: item.ParentNativeID,
		})
	}
	return page, nil
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
