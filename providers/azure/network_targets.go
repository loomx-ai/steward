package azure

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type azureNetworkCursor struct {
	Kind  string `json:"kind"`
	Page  string `json:"page,omitempty"`
	Proof string `json:"proof"`
}

func (r *Runtime) SearchNetworkTargets(ctx context.Context, query contracts.NetworkTargetQuery) (contracts.NetworkTargetPage, error) {
	page := contracts.NetworkTargetPage{Items: []contracts.NetworkTargetOption{}}
	if query.RegionID == "" || query.RegionID != strings.TrimSpace(query.RegionID) || query.Kind != asset.ScanTargetVPC && query.Kind != asset.ScanTargetVSwitch || query.Kind == asset.ScanTargetVPC && query.ParentNativeID != "" {
		return page, serviceDenied("invalid_azure_network_query")
	}
	c, err := r.resolve(ctx, query.ConnectionID)
	if err != nil {
		return page, err
	}
	kinds := []string{vnetType, azureLocalNetworkType}
	if query.Kind == asset.ScanTargetVSwitch {
		kinds = []string{subnetType}
	}
	if query.ParentNativeID != "" {
		id, typ, err := parseID(query.ParentNativeID)
		if err != nil || !strings.HasPrefix(id, c.root()+"/resourcegroups/") || len(strings.Split(id, "/")) != 9 || typ != strings.ToLower(vnetType) && typ != strings.ToLower(azureLocalNetworkType) {
			return page, serviceDenied("invalid_azure_network_parent")
		}
		if typ == strings.ToLower(azureLocalNetworkType) {
			// Local subnets are configuration inside one logical network, not
			// independently addressable ARM subnet resources.
			if query.Cursor != "" {
				return page, serviceDenied("invalid_azure_network_cursor")
			}
			return page, nil
		}
	}
	exactLocal, exactID := "", ""
	if id, typ, err := parseID(query.Query); err == nil {
		if !strings.HasPrefix(id, c.root()+"/resourcegroups/") {
			return page, serviceDenied("foreign_azure_network_query")
		}
		for _, kind := range kinds {
			if strings.EqualFold(typ, kind) {
				exactID = id
				kinds = []string{kind}
				if kind == azureLocalNetworkType {
					exactLocal, err = c.azureLocalIdentity(id, kind)
					if err != nil {
						return page, err
					}
				}
				break
			}
		}
	}
	boundary := query
	boundary.Cursor, boundary.Limit = "", 0
	proof := func(cursor azureNetworkCursor) string {
		return c.privateConfiguration(map[string]any{"query": boundary, "revision": r.bundle.Revision, "kind": cursor.Kind, "page": cursor.Page})
	}
	cursor := azureNetworkCursor{Kind: kinds[0]}
	if query.Cursor != "" {
		if len(query.Cursor) > 256<<10 {
			return page, serviceDenied("azure_network_cursor_too_large")
		}
		encoded, err := base64.RawURLEncoding.DecodeString(query.Cursor)
		if err != nil || json.Unmarshal(encoded, &cursor) != nil || !slices.Contains(kinds, cursor.Kind) || cursor.Proof != proof(cursor) {
			return page, serviceDenied("invalid_azure_network_cursor")
		}
	}
	seen := map[string]bool{}
	// Empty filtered pages advance automatically, including the transition from
	// Azure VNet to Azure Local. A bound keeps each HTTP request cancellable and
	// finite; further pages remain available through the signed continuation.
	for attempts := 0; attempts < 100; attempts++ {
		key := cursor.Kind + "\x00" + cursor.Page
		if seen[key] {
			return contracts.NetworkTargetPage{}, serviceDenied("repeated_azure_network_cursor")
		}
		seen[key] = true
		kind := r.resourceKind(cursor.Kind)
		request := contracts.InventoryRequest{ConnectionID: query.ConnectionID, Scope: asset.Scope{Kind: asset.ScopeRegion, NativeID: query.RegionID}, Source: insightsInventorySource(cursor.Kind), ResourceKind: &kind, Cursor: cursor.Page, Limit: query.Limit}
		if exactLocal != "" {
			request.KnownNativeIDs = []string{exactLocal}
		}
		batch, err := r.List(ctx, request)
		if err != nil {
			return contracts.NetworkTargetPage{}, err
		}
		page.NextCursor = ""
		if batch.RequestID != "" {
			page.RequestID = batch.RequestID
		}
		for _, item := range batch.Items {
			parent := text(item.Normalized["vpc_id"])
			if query.ParentNativeID != "" && !strings.EqualFold(parent, query.ParentNativeID) {
				continue
			}
			if exactID != "" && !strings.EqualFold(item.NativeID, exactID) || exactID == "" && query.Query != "" && !strings.Contains(strings.ToLower(item.Name+" "+item.NativeID), strings.ToLower(query.Query)) {
				continue
			}
			page.Items = append(page.Items, contracts.NetworkTargetOption{Kind: query.Kind, RegionID: query.RegionID, NativeID: item.NativeID, Name: item.Name, ParentNativeID: parent})
		}
		if exactID != "" && len(page.Items) > 0 {
			return page, nil
		}
		if batch.NextCursor != "" {
			cursor.Page = batch.NextCursor
		} else {
			index := slices.Index(kinds, cursor.Kind) + 1
			if index == len(kinds) {
				return page, nil
			}
			cursor.Kind, cursor.Page = kinds[index], ""
		}
		cursor.Proof = proof(cursor)
		encoded, _ := json.Marshal(cursor)
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
		if len(page.Items) > 0 {
			return page, nil
		}
	}
	return page, nil
}
