package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestAPIMFixedPortalSettingsAndTenantAccessUseDistinctNativeGETs(t *testing.T) {
	for _, scenario := range []struct {
		kind  string
		files []string
	}{
		{apimServiceType + "/portalsettings", []string{"ApiManagementPortalSettingsGetSignIn.json", "ApiManagementPortalSettingsGetSignUp.json", "ApiManagementPortalSettingsGetDelegation.json"}},
		{apimServiceType + "/tenant", []string{"ApiManagementGetTenantAccess.json", "ApiManagementGetTenantGitAccess.json"}},
	} {
		for _, mode := range []string{"native", "paged", "wrong-name", "changed-config", "unknown-setting", "denied"} {
			t.Run(scenario.kind+"/"+mode, func(t *testing.T) {
				if mode == "unknown-setting" && strings.HasSuffix(scenario.kind, "/tenant") {
					return // Access names use the native extensible modelAsString enum.
				}
				s, r, assets := apimScenario(t)
				service := cdnAsset(t, assets, apimServiceType)
				collection := service.Identity.NativeID + "/" + strings.ToLower(last(scenario.kind))
				s.lists[collection] = []any{}
				for _, file := range scenario.files {
					raw := apimExample(t, file)
					id := collection + "/" + strings.ToLower(text(raw["name"]))
					raw["id"], raw["type"] = id, scenario.kind
					raw["_apim_header_etag"] = `"fixed-setting"`
					s.add(raw, apimVersion)
					s.lists[collection] = append(s.lists[collection], raw)
				}
				first := object(s.lists[collection][0])
				id := text(first["id"])
				base := s.handle
				reads := map[string]bool{}
				switch mode {
				case "wrong-name":
					first["name"] = "ambiguous"
				case "changed-config":
					copy := maps.Clone(first)
					copy["properties"] = map[string]any{"enabled": "changed"}
					s.lists[collection][0] = copy
				case "unknown-setting":
					copy := maps.Clone(first)
					copy["id"], copy["name"] = collection+"/unselected", "unselected"
					s.lists[collection] = append(s.lists[collection], copy)
				case "denied":
					s.status[id] = 403
				}
				s.handle = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					if req.Method == "GET" && redisParentID(path) == service.Identity.NativeID && strings.HasPrefix(path, collection+"/") {
						reads[path] = true
					}
					if mode == "paged" && req.Method == "GET" && path == collection {
						if req.URL.Query().Get("$skip") == "" {
							return jsonResponse(200, map[string]any{"value": s.lists[collection][:1], "nextLink": "https://management.azure.com" + collection + "?api-version=" + apimVersion + "&$skip=1"}, nil), true
						}
						return jsonResponse(200, map[string]any{"value": s.lists[collection][1:]}, nil), true
					}
					return base(req)
				}
				var items []contracts.InventoryItem
				request := productRequest(r, scenario.kind)
				for {
					batch, err := r.List(context.Background(), request)
					if mode != "native" && mode != "paged" {
						if err == nil {
							t.Fatal("invalid fixed configuration accepted", mode)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					items = append(items, batch.Items...)
					if batch.Complete {
						break
					}
					request.Cursor = batch.NextCursor
				}
				if len(items) != len(scenario.files) || len(reads) != len(scenario.files) {
					t.Fatal("fixed configuration endpoints conflated", len(items), reads)
				}
				for _, item := range items {
					if item.Actionable == nil || *item.Actionable || item.Location != "westus" {
						t.Fatal("fixed config actionability or region")
					}
				}
			})
		}
	}
}

func TestAPIMReadonlyConfigurationIsReviewedAndSecretSafe(t *testing.T) {
	reviews := apimReviewCache{}
	for _, kind := range []string{apimServiceType + "/portalconfigs", apimServiceType + "/portalRevisions", apimServiceType + "/portalsettings", apimServiceType + "/settings", apimServiceType + "/tenant"} {
		for _, mode := range []string{"inventory", "cascade", "private-drift", "retained", "surviving", "readonly-plan"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				s, r, assets := apimScenario(t)
				service := cdnAsset(t, assets, apimServiceType)
				target := cdnAsset(t, assets, kind)
				raw := s.records[target.Identity.NativeID]
				object(raw["properties"])["opaqueCustomConfiguration"] = map[string]any{"content": "private-portal-marker"}
				for i := range assets {
					if assets[i].ID == target.ID {
						assets[i] = dnsAsset(t, r, raw)
						assets[i].Capabilities = nil
						target = assets[i]
					}
				}
				if mode == "inventory" {
					batch, err := r.List(context.Background(), productRequest(r, kind))
					if err != nil || !batch.Complete || len(batch.Items) != 1 {
						t.Fatal("readonly configuration inventory", err)
					}
					encoded, _ := json.Marshal(batch)
					if strings.Contains(string(encoded), "private-portal-marker") {
						t.Fatal("opaque portal configuration exported")
					}
					if _, err := r.ResolveAction(context.Background(), "connection", target); err == nil {
						t.Fatal("invented fixed configuration DELETE")
					}
					return
				}
				request, input := reviews.request(t, r, assets, service)
				if mode == "readonly-plan" {
					input.ResolvedAssetIDs = []asset.AssetID{target.ID}
					solved, err := plan.Solve(input)
					if err != nil {
						t.Fatal(err)
					}
					if slices.ContainsFunc(solved.Steps, func(step plan.CleanupTaskStep) bool { return step.AssetID == target.ID && step.Action == "delete" }) {
						t.Fatal("fixed configuration scheduled for direct deletion")
					}
					return
				}
				if !slices.ContainsFunc(request.LifecycleImpacts, func(p contracts.ActionImpact) bool { return p.Asset.ID == target.ID }) {
					t.Fatal("fixed configuration omitted from service review")
				}
				if mode == "retained" {
					input.RequestOptions = map[asset.AssetID]map[string]any{service.ID: {"retain_resources": []string{target.Identity.NativeID}}}
					solved, err := plan.Solve(input)
					if err != nil || len(solved.Blockers) == 0 {
						t.Fatal("retained configuration did not block service", err)
					}
					return
				}
				if mode == "private-drift" {
					object(object(raw["properties"])["opaqueCustomConfiguration"])["content"] = "changed-private-marker"
				}
				driver, err := r.ResolveAction(context.Background(), "connection", service)
				if err != nil {
					t.Fatal(err)
				}
				result, err := driver.Execute(context.Background(), request)
				if mode == "private-drift" {
					if err == nil || len(s.deletes) != 0 {
						t.Fatal("configuration drift ignored")
					}
					return
				}
				if err != nil || len(s.deletes) != 1 || s.deletes[0] != service.Identity.NativeID {
					t.Fatal("configuration cascade", err, s.deletes)
				}
				streamAnalyticsAfterDelete(s)
				if mode == "surviving" {
					s.gone[target.Identity.NativeID] = false
				}
				waited, err := driver.Wait(context.Background(), request, result)
				if err != nil || waited.Done == (mode == "surviving") {
					t.Fatal("configuration absence", waited, err)
				}
			})
		}
	}
}

func TestAPIMPortalPublishingPreventsServiceDeletion(t *testing.T) {
	for _, state := range []string{"pending", "publishing", "", "unrecognized", "completed", "failed"} {
		t.Run(state, func(t *testing.T) {
			s, r, assets := apimScenario(t)
			service := cdnAsset(t, assets, apimServiceType)
			target := cdnAsset(t, assets, apimServiceType+"/portalRevisions")
			raw := s.records[target.Identity.NativeID]
			object(raw["properties"])["status"] = state
			if state == "completed" || state == "failed" {
				if err := apimReady(target.Identity.NativeType, raw); err != nil {
					t.Fatal("native terminal portal status rejected", err)
				}
				return
			}
			contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
			if _, err := contributor.Contribute(context.Background(), "scope", assets); err == nil {
				t.Fatal("publishing portal omitted from review gate")
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", service)
			if _, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: service, Action: "delete"}); err == nil || len(s.deletes) != 0 {
				t.Fatal("publishing portal service deleted")
			}
		})
	}
}
