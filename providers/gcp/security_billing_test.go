package gcp

import (
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestSecurityBillingNativeLocationInventory(t *testing.T) {
	for _, scope := range []string{"project", "global", "eu"} {
		t.Run(scope, func(t *testing.T) {
			var reads []string
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.URL.Host != securityServiceHost || req.Header.Get("Authorization") != "Bearer token" {
					t.Fatalf("unexpected native request: %s %s", req.Method, req.URL)
				}
				if req.URL.Path == "/v1/projects/sample-project/locations" {
					if req.URL.Query().Get("pageToken") == "" {
						return apiResponse(req, 200, `{"locations":[{"name":"projects/123456/locations/global"}],"nextPageToken":"next"}`), nil
					}
					return apiResponse(req, 200, `{"locations":[{"name":"projects/sample-project/locations/eu","locationId":"eu"}]}`), nil
				}
				if req.URL.RawQuery != "" || (req.Body != nil && req.ContentLength != 0) {
					t.Fatal("singleton GET gained body/query")
				}
				name := strings.TrimPrefix(req.URL.Path, "/v1/")
				reads = append(reads, name)
				tier := "PREMIUM"
				if strings.Contains(name, "/eu/") {
					tier = "STANDARD"
				}
				return dataformResponse(req, 200, map[string]any{"name": strings.Replace(name, "projects/sample-project/", "projects/123456/", 1), "billingTier": tier}), nil
			})
			request := billingRequest(r, scope)
			var locations []string
			for {
				batch, err := r.List(t.Context(), request)
				if err != nil || batch.RequestID != "request-123" {
					t.Fatal("billing inventory", batch, err)
				}
				for _, item := range batch.Items {
					locations = append(locations, item.Location)
					want := "PREMIUM"
					if item.Location == "eu" {
						want = "STANDARD"
					}
					if item.Normalized["_inventory_source"] != securityBillingSource || item.Normalized["billingTier"] != want || item.Normalized["projectId"] != "sample-project" || item.State != "" || item.Actionable == nil || *item.Actionable || item.NativeID != "//"+securityServiceHost+"/projects/sample-project/locations/"+item.Location+"/billingMetadata" {
						t.Fatal("incorrect native billing metadata", item)
					}
					for _, field := range []string{"subscriptionType", "endTime", "startTime", "effectiveEnablementState"} {
						if _, ok := item.Normalized[field]; ok {
							t.Fatal("inferred unsupported billing field", field)
						}
					}
					value := asset.Asset{Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", NativeType: item.NativeType, NativeID: item.NativeID}, Normalized: item.Normalized}
					if _, err := r.ResolveAction(t.Context(), "connection", value); err == nil {
						t.Fatal("billing metadata became deletable")
					}
				}
				if batch.Complete {
					break
				}
				request.Cursor = batch.NextCursor
			}
			want := []string{scope}
			if scope == "project" {
				want = []string{"eu", "global"}
			}
			if !slices.Equal(locations, want) || len(reads) != len(want) {
				t.Fatal("missing/duplicate singleton read", locations, reads)
			}
		})
	}
}

func TestSecurityBillingInvokeNativeTierAndBoundary(t *testing.T) {
	for _, tier := range []any{"STANDARD", "PREMIUM", "ENTERPRISE", "BILLING_TIER_UNSPECIFIED", "FUTURE_TIER", nil, true} {
		r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
			if req.URL.Host != securityServiceHost || req.URL.Path != "/v1/projects/sample-project/locations/global/billingMetadata" || req.Method != "GET" {
				t.Fatal("unexpected billing read", req.URL)
			}
			data := map[string]any{"name": "projects/sample-project/locations/global/billingMetadata"}
			if tier != nil {
				data["billingTier"] = tier
			}
			return dataformResponse(req, 200, data), nil
		})
		result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: securityBillingGet, Parameters: map[string]any{"name": "projects/sample-project/locations/global/billingMetadata"}})
		if tier == true {
			if err == nil || len(result.Data) != 0 {
				t.Fatal("malformed tier escaped", result, err)
			}
		} else if err != nil || result.Data["billingTier"] != tier {
			t.Fatal("native tier lost", tier, result, err)
		}
	}
	for _, name := range []string{"projects/other/locations/global/billingMetadata", "organizations/123/locations/global/billingMetadata", "projects/sample-project/locations/global/billingMetadata/child", "projects/sample-project/locations/../billingMetadata"} {
		r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
			t.Fatal("invalid billing read reached service", req.URL)
			return nil, nil
		})
		if _, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: securityBillingGet, Parameters: map[string]any{"name": name}}); err == nil {
			t.Fatal("invalid billing identity accepted", name)
		}
	}
}

func TestSecurityBillingFailedReadsAndChangingLocations(t *testing.T) {
	for _, mode := range []string{"denied", "missing", "foreign-project", "foreign-location", "name-missing", "tier-shape", "locations-denied", "locations-invalid", "locations-changed", "unexpected-page"} {
		t.Run(mode, func(t *testing.T) {
			locationReads := 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/v1/projects/sample-project/locations" {
					locationReads++
					if mode == "locations-denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					if mode == "locations-invalid" {
						return apiResponse(req, 200, `{"locations":[{"name":"projects/other/locations/eu"}]}`), nil
					}
					if mode == "locations-changed" && locationReads > 1 {
						return apiResponse(req, 200, `{"locations":[{"name":"projects/sample-project/locations/global"}]}`), nil
					}
					return apiResponse(req, 200, `{"locations":[{"name":"projects/sample-project/locations/eu"},{"name":"projects/sample-project/locations/global"}]}`), nil
				}
				if mode == "denied" {
					return apiResponse(req, 403, `{}`), nil
				}
				if mode == "missing" {
					return apiResponse(req, 404, `{}`), nil
				}
				data := map[string]any{"name": strings.TrimPrefix(req.URL.Path, "/v1/"), "billingTier": "PREMIUM"}
				switch mode {
				case "foreign-project":
					data["name"] = "projects/other/locations/global/billingMetadata"
				case "foreign-location":
					data["name"] = "projects/sample-project/locations/us/billingMetadata"
				case "name-missing":
					delete(data, "name")
				case "tier-shape":
					data["billingTier"] = map[string]any{}
				case "unexpected-page":
					data["nextPageToken"] = "invented"
				}
				return dataformResponse(req, 200, data), nil
			})
			request := billingRequest(r, "project")
			batch, err := r.List(t.Context(), request)
			if mode == "locations-changed" {
				if err != nil || batch.Complete || batch.NextCursor == "" {
					t.Fatal("first target", batch, err)
				}
				request.Cursor = batch.NextCursor
				batch, err = r.List(t.Context(), request)
			}
			if err == nil {
				t.Fatal("failed/incomplete metadata accepted", batch)
			}
		})
	}
}

func billingRequest(r *Runtime, scope string) contracts.InventoryRequest {
	request := productRequest(r, securityBillingType, scope)
	request.Source = securityBillingSource
	return request
}

func TestSecurityBillingRejectsAuthoritativeAndNetworkRouting(t *testing.T) {
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		t.Fatal("invalid source reached service", req.URL)
		return nil, nil
	})
	for _, mode := range []string{"source", "kind", "network"} {
		request := billingRequest(r, "global")
		switch mode {
		case "source":
			request.Source = productInventorySource
		case "kind":
			kind := r.resourceKind(securityServiceType)
			request.ResourceKind = &kind
		case "network":
			request.NetworkTarget = &asset.ScanTarget{Kind: asset.ScanTargetVPC}
		}
		if _, err := r.List(t.Context(), request); err == nil {
			t.Fatal("invalid billing source routing accepted", mode)
		}
	}
}
