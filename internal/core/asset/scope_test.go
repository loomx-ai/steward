package asset_test

import (
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestAuthoritativeScopeWalksRegionAndGlobalAncestors(t *testing.T) {
	t.Parallel()

	scopes := map[asset.ScopeID]asset.Scope{
		"region":       {ID: "region", Kind: asset.ScopeRegion, NativeID: "cn-hangzhou"},
		"region-child": {ID: "region-child", ParentID: "region", Kind: asset.ScopeZone},
		"global":       {ID: "global", Kind: asset.ScopeGlobal, NativeID: "global"},
		"global-child": {ID: "global-child", ParentID: "global", Kind: asset.ScopeResourceGroup},
	}
	for _, test := range []struct {
		scopeID asset.ScopeID
		wantID  asset.ScopeID
	}{
		{scopeID: "region-child", wantID: "region"},
		{scopeID: "global-child", wantID: "global"},
	} {
		got, ok := asset.AuthoritativeScope(test.scopeID, scopes)
		if !ok || got.ID != test.wantID {
			t.Fatalf("scope %s resolved to %+v, ok=%v", test.scopeID, got, ok)
		}
	}
}

func TestAuthoritativeScopeRejectsEmptyMissingAndCyclicChains(t *testing.T) {
	t.Parallel()

	scopes := map[asset.ScopeID]asset.Scope{
		"cycle-a": {ID: "cycle-a", ParentID: "cycle-b", Kind: asset.ScopeZone},
		"cycle-b": {ID: "cycle-b", ParentID: "cycle-a", Kind: asset.ScopeResourceGroup},
	}
	for _, scopeID := range []asset.ScopeID{"", "missing", "cycle-a"} {
		if got, ok := asset.AuthoritativeScope(scopeID, scopes); ok {
			t.Fatalf("scope %s unexpectedly resolved to %+v", scopeID, got)
		}
	}
}
