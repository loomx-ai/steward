package gcp

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func securityServiceData(name string) map[string]any {
	return map[string]any{"name": name, "intendedEnablementState": "INHERITED", "effectiveEnablementState": "INGEST_ONLY", "updateTime": "2026-08-31T10:00:00Z", "modules": map[string]any{"SSH_BRUTE_FORCE": map[string]any{"intendedEnablementState": "INHERITED", "effectiveEnablementState": "DISABLED"}}, "serviceConfig": map[string]any{"ordinary": "private-service-configuration"}}
}

func TestSecurityServicesNativeInventoryAndReadOnly(t *testing.T) {
	for _, scope := range []string{"project", "global", "eu"} {
		t.Run(scope, func(t *testing.T) {
			var reads []string
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.URL.Host != securityServiceHost {
					t.Fatalf("unexpected API %s %s", req.Method, req.URL)
				}
				if req.URL.Path == "/v1/projects/sample-project/locations" {
					if req.URL.Query().Get("pageToken") == "" {
						return apiResponse(req, 200, `{"locations":[{"name":"projects/123456/locations/global"}],"nextPageToken":"second"}`), nil
					}
					return apiResponse(req, 200, `{"locations":[{"name":"projects/sample-project/locations/eu","locationId":"eu"}]}`), nil
				}
				if strings.HasSuffix(req.URL.Path, "/securityCenterServices") {
					if req.URL.Query().Get("pageSize") != "100" {
						t.Fatal("native page size missing")
					}
					service := "event-threat-detection"
					if req.URL.Query().Get("pageToken") == "second-service" {
						service = "security-health-analytics"
					}
					name := strings.TrimPrefix(req.URL.Path, "/v1/") + "/" + service
					name = strings.Replace(name, "projects/sample-project/", "projects/123456/", 1)
					body := map[string]any{"securityCenterServices": []any{map[string]any{"name": name}}}
					if service == "event-threat-detection" {
						body["nextPageToken"] = "second-service"
					}
					if req.URL.Query().Has("showEligibleModulesOnly") {
						t.Fatal("filtered native modules")
					}
					return dataformResponse(req, 200, body), nil
				}
				name := strings.TrimPrefix(req.URL.Path, "/v1/")
				reads = append(reads, name)
				return dataformResponse(req, 200, securityServiceData(name)), nil
			})
			request := productRequest(r, securityServiceType, scope)
			var locations []string
			for {
				page, err := r.List(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range page.Items {
					locations = append(locations, item.Location)
					if item.State != "INGEST_ONLY" || item.Normalized["state"] != "INGEST_ONLY" || item.Normalized["intendedEnablementState"] != "INHERITED" || len(object(item.Normalized["modules"])) != 1 || item.Actionable == nil || *item.Actionable {
						t.Fatal("service settings lost", item)
					}
					encoded, _ := json.Marshal(item)
					if strings.Contains(string(encoded), "private-service-configuration") {
						t.Fatal("private service configuration escaped")
					}
					value := asset.Asset{ID: "service", Identity: asset.Identity{ConnectionID: "connection", Provider: asset.ProviderGCP, NativeID: item.NativeID, NativeType: item.NativeType}, Normalized: item.Normalized}
					if _, err := r.ResolveAction(t.Context(), "connection", value); err == nil {
						t.Fatal("service settings became a deletable resource")
					}
				}
				if page.Complete {
					break
				}
				request.Cursor = page.NextCursor
			}
			want := []string{scope, scope}
			if scope == "project" {
				want = []string{"eu", "eu", "global", "global"}
			}
			if !slices.Equal(locations, want) || len(reads) != len(want) {
				t.Fatal("native location/detail coverage", locations, reads)
			}
		})
	}
}

func TestSecurityServicesFailedReadsDoNotCompleteInventory(t *testing.T) {
	for _, mode := range []string{"foreign-project", "foreign-location", "duplicate", "unreachable", "list-denied", "detail-denied", "detail-missing", "detail-identity", "state-type", "module-type", "modules-type", "cursor-cycle", "locations-changed"} {
		t.Run(mode, func(t *testing.T) {
			lists, locations := 0, 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.URL.Host != securityServiceHost {
					t.Fatalf("unexpected API %s", req.URL)
				}
				if req.URL.Path == "/v1/projects/sample-project/locations" {
					locations++
					location := "global"
					if mode == "locations-changed" && locations > 1 {
						location = "eu"
					}
					return dataformResponse(req, 200, map[string]any{"locations": []any{map[string]any{"name": "projects/sample-project/locations/" + location}}}), nil
				}
				name := "projects/sample-project/locations/global/securityCenterServices/event-threat-detection"
				if strings.HasSuffix(req.URL.Path, "/securityCenterServices") {
					lists++
					if mode == "list-denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					if mode == "foreign-project" {
						name = strings.Replace(name, "sample-project", "foreign-project", 1)
					}
					if mode == "foreign-location" {
						name = strings.Replace(name, "global", "eu", 1)
					}
					rows := []any{map[string]any{"name": name}}
					if mode == "duplicate" {
						rows = append(rows, rows[0])
					}
					body := map[string]any{"securityCenterServices": rows}
					if mode == "unreachable" {
						body["unreachable"] = []any{"eu"}
					}
					if mode == "cursor-cycle" || mode == "locations-changed" {
						body["nextPageToken"] = "repeat"
					}
					return dataformResponse(req, 200, body), nil
				}
				if mode == "detail-denied" {
					return apiResponse(req, 403, `{}`), nil
				}
				if mode == "detail-missing" {
					return apiResponse(req, 404, `{}`), nil
				}
				data := securityServiceData(name)
				switch mode {
				case "detail-identity":
					data["name"] = name + "-other"
				case "state-type":
					data["effectiveEnablementState"] = true
				case "module-type":
					data["modules"] = map[string]any{"SSH": false}
				case "modules-type":
					data["modules"] = []any{}
				}
				return dataformResponse(req, 200, data), nil
			})
			request := productRequest(r, securityServiceType, "project")
			page, err := r.List(t.Context(), request)
			if mode == "cursor-cycle" || mode == "locations-changed" {
				if err != nil || page.NextCursor == "" {
					t.Fatal("missing first page", err)
				}
				request.Cursor = page.NextCursor
				page, err = r.List(t.Context(), request)
			}
			if err == nil || page.Complete || len(page.Items) > 0 {
				t.Fatal("uncertain scan succeeded", page, err)
			}
			if mode == "locations-changed" && lists != 1 {
				t.Fatal("changed location cursor issued another list", lists)
			}
		})
	}
}
