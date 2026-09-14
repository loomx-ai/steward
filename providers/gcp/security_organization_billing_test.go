package gcp

import (
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestSecurityBillingOrganizationInventory(t *testing.T) {
	for _, scope := range []string{"project", "global", "eu"} {
		t.Run(scope, func(t *testing.T) {
			s := newOrganizationScenario()
			reads := 0
			r := securityAncestorRuntime(t, s, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" {
					t.Fatal(req.Method)
				}
				if req.URL.Path == "/v1/projects/sample-project/locations" {
					return apiResponse(req, 200, `{"locations":[{"name":"projects/sample-project/locations/global"},{"name":"projects/123456/locations/eu"}]}`), nil
				}
				reads++
				name := strings.TrimPrefix(req.URL.Path, "/v1/")
				if strings.HasPrefix(name, "folders/") || req.URL.RawQuery != "" || !strings.HasSuffix(name, "/billingMetadata") {
					t.Fatal("non-native billing request", req.URL)
				}
				tier := "PREMIUM"
				if strings.HasPrefix(name, "organizations/") {
					tier = "ENTERPRISE"
				}
				return dataformResponse(req, 200, map[string]any{"name": name, "billingTier": tier}), nil
			})
			request := billingRequest(r, scope)
			counts := map[string]int{}
			for {
				batch, err := r.List(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range batch.Items {
					parent := text(item.Normalized["configurationParent"])
					counts[parent]++
					want := "PREMIUM"
					if parent == "organizations/123" {
						want = "ENTERPRISE"
						if item.Normalized["projectId"] != nil || item.Normalized["project_id"] != nil {
							t.Fatal("organization claims project ownership", item)
						}
					}
					if item.Normalized["billingTier"] != want || item.Actionable == nil || *item.Actionable || item.State != "" || item.Normalized["_inventory_source"] != securityBillingSource {
						t.Fatal(item)
					}
				}
				if batch.Complete {
					break
				}
				request.Cursor = batch.NextCursor
			}
			want := 1
			if scope == "project" {
				want = 2
			}
			if len(counts) != 2 || counts["projects/sample-project"] != want || counts["organizations/123"] != want || reads != 2*want {
				t.Fatal(counts, reads)
			}
		})
	}
}

func TestSecurityBillingOrganizationFailures(t *testing.T) {
	for _, mode := range []string{"denied", "missing", "identity", "location", "tier", "pagination", "during-read", "between-pages", "ancestor-denied"} {
		t.Run(mode, func(t *testing.T) {
			s := newOrganizationScenario()
			orgReads := 0
			if mode == "ancestor-denied" {
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v3/organizations/123" {
						return apiResponse(req, 403, `{}`), true
					}
					return nil, false
				}
			}
			r := securityAncestorRuntime(t, s, func(req *http.Request) (*http.Response, error) {
				name := strings.TrimPrefix(req.URL.Path, "/v1/")
				data := map[string]any{"name": name, "billingTier": "PREMIUM"}
				if strings.HasPrefix(name, "organizations/") {
					orgReads++
					switch mode {
					case "denied":
						return apiResponse(req, 403, `{}`), nil
					case "missing":
						return apiResponse(req, 404, `{}`), nil
					case "identity":
						data["name"] = strings.Replace(name, "123", "999", 1)
					case "location":
						data["name"] = strings.Replace(name, "global", "eu", 1)
					case "tier":
						data["billingTier"] = true
					case "pagination":
						data["nextPageToken"] = "unexpected"
					case "during-read":
						s.project["parent"] = ""
					}
				}
				return dataformResponse(req, 200, data), nil
			})
			request := billingRequest(r, "global")
			batch, err := r.List(t.Context(), request)
			if mode != "ancestor-denied" {
				if err != nil || batch.Complete || len(batch.Items) != 1 {
					t.Fatal(batch, err)
				}
				request.Cursor = batch.NextCursor
				if mode == "between-pages" {
					s.project["parent"] = ""
				}
				batch, err = r.List(t.Context(), request)
			}
			if err == nil || batch.Complete || len(batch.Items) != 0 {
				t.Fatal("unsafe billing observation", batch, err)
			}
			if (mode == "between-pages" || mode == "ancestor-denied") && orgReads != 0 {
				t.Fatal("stale ancestry reached organization", orgReads)
			}
		})
	}
}

func TestSecurityBillingOrganizationInvoke(t *testing.T) {
	for _, mode := range []string{"valid", "unknown-tier", "missing-tier", "foreign", "folder", "invalid-location", "identity", "tier", "denied", "moved"} {
		t.Run(mode, func(t *testing.T) {
			s := newOrganizationScenario()
			calls := 0
			name := "organizations/123/locations/global/billingMetadata"
			r := securityAncestorRuntime(t, s, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" || req.URL.Path != "/v1/"+name || req.URL.RawQuery != "" {
					t.Fatal(req.URL)
				}
				data := map[string]any{"name": name, "billingTier": "ENTERPRISE"}
				switch mode {
				case "unknown-tier":
					data["billingTier"] = "FUTURE_TIER"
				case "missing-tier":
					delete(data, "billingTier")
				case "tier":
					data["billingTier"] = true
				case "identity":
					data["name"] = name + "-other"
				case "denied":
					return apiResponse(req, 403, `{}`), nil
				case "moved":
					s.project["parent"] = ""
				}
				return dataformResponse(req, 200, data), nil
			})
			input := name
			switch mode {
			case "foreign":
				input = strings.Replace(name, "123", "999", 1)
			case "folder":
				input = strings.Replace(name, "organizations/123", "folders/456", 1)
			case "invalid-location":
				input = strings.Replace(name, "global", "..", 1)
			}
			result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: securityOrganizationBillingGet, Parameters: map[string]any{"name": input}})
			switch mode {
			case "valid", "unknown-tier", "missing-tier":
				if err != nil || result.Data["name"] != name {
					t.Fatal(result, err)
				}
				want := any("ENTERPRISE")
				if mode == "unknown-tier" {
					want = "FUTURE_TIER"
				}
				if mode == "missing-tier" {
					want = nil
				}
				if result.Data["billingTier"] != want {
					t.Fatal(result)
				}
			default:
				if err == nil {
					t.Fatal("unsafe organization billing Invoke", result)
				}
			}
			if (mode == "foreign" || mode == "folder" || mode == "invalid-location") && calls != 0 {
				t.Fatal("foreign request", calls)
			}
		})
	}
}
