package gcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func securityAncestorRuntime(t *testing.T, s *organizationScenario, handler roundTripFunc) *Runtime {
	r := s.runtime(t)
	base := r.transport
	r.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Host == securityServiceHost {
			return handler(req)
		}
		return base.RoundTrip(req)
	})
	return r
}

func TestSecurityServicesAncestorInventory(t *testing.T) {
	for _, scope := range []string{"project", "global", "eu"} {
		t.Run(scope, func(t *testing.T) {
			s := newOrganizationScenario()
			lists, details := 0, 0
			r := securityAncestorRuntime(t, s, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" {
					t.Fatal(req.Method)
				}
				if req.URL.Path == "/v1/projects/sample-project/locations" {
					return apiResponse(req, 200, `{"locations":[{"name":"projects/123456/locations/global"},{"name":"projects/sample-project/locations/eu"}]}`), nil
				}
				name := strings.TrimPrefix(req.URL.Path, "/v1/")
				if strings.HasSuffix(name, "/securityCenterServices") {
					lists++
					if req.URL.Query().Get("pageSize") != "100" || req.URL.Query().Has("showEligibleModulesOnly") {
						t.Fatal(req.URL)
					}
					service := "event-threat-detection"
					body := map[string]any{"nextPageToken": "second"}
					if req.URL.Query().Get("pageToken") == "second" {
						service = "security-health-analytics"
						delete(body, "nextPageToken")
					}
					body["securityCenterServices"] = []any{map[string]any{"name": name + "/" + service}}
					return dataformResponse(req, 200, body), nil
				}
				details++
				return dataformResponse(req, 200, securityServiceData(name)), nil
			})
			request := securityServiceRequest(r, scope)
			counts := map[string]int{}
			for {
				batch, err := r.List(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				for _, item := range batch.Items {
					parent := text(item.Normalized["configurationParent"])
					counts[parent]++
					if parent != "projects/sample-project" && (item.Normalized["project_id"] != nil || item.Normalized["project_number"] != nil) {
						t.Fatal("ancestor labeled as project resource", item)
					}
					if item.Actionable == nil || *item.Actionable || item.State != "INGEST_ONLY" || item.Normalized["_inventory_source"] != securityServiceSource {
						t.Fatal(item)
					}
					raw, _ := json.Marshal(item)
					if strings.Contains(string(raw), "private-service-configuration") {
						t.Fatal("private config leaked")
					}
				}
				if batch.Complete {
					break
				}
				request.Cursor = batch.NextCursor
			}
			want := 2
			if scope == "project" {
				want = 4
			}
			if len(counts) != 3 || lists != want*3 || details != want*3 {
				t.Fatal(counts, lists, details)
			}
			for _, parent := range []string{"projects/sample-project", "folders/456", "organizations/123"} {
				if counts[parent] != want {
					t.Fatal(counts)
				}
			}
		})
	}
}

func TestSecurityServicesAncestorFailuresAndCursor(t *testing.T) {
	for _, mode := range []string{"list-denied", "list-missing", "foreign-parent", "foreign-location", "detail-denied", "detail-missing", "detail-identity", "duplicate", "malformed", "partial", "ancestry-during-read", "ancestry-between-pages", "ancestor-denied"} {
		t.Run(mode, func(t *testing.T) {
			s := newOrganizationScenario()
			lists := 0
			if mode == "ancestor-denied" {
				s.hook = func(req *http.Request) (*http.Response, bool) {
					if req.URL.Path == "/v3/folders/456" {
						return apiResponse(req, 403, `{}`), true
					}
					return nil, false
				}
			}
			r := securityAncestorRuntime(t, s, func(req *http.Request) (*http.Response, error) {
				name := strings.TrimPrefix(req.URL.Path, "/v1/")
				if strings.HasPrefix(name, "projects/") {
					return apiResponse(req, 200, `{}`), nil
				}
				if strings.HasSuffix(name, "/securityCenterServices") {
					lists++
					if mode == "list-denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					if mode == "list-missing" {
						return apiResponse(req, 404, `{}`), nil
					}
					id := name + "/event-threat-detection"
					if mode == "foreign-parent" {
						id = strings.Replace(id, "folders/456", "folders/999", 1)
					}
					if mode == "foreign-location" {
						id = strings.Replace(id, "global", "eu", 1)
					}
					rows := []any{map[string]any{"name": id}}
					if mode == "duplicate" {
						rows = append(rows, rows[0])
					}
					body := map[string]any{"securityCenterServices": rows}
					if mode == "malformed" {
						body["securityCenterServices"] = true
					}
					if mode == "partial" {
						body["unreachable"] = []any{"eu"}
					}
					if mode == "ancestry-during-read" {
						s.project["parent"] = "organizations/123"
					}
					return dataformResponse(req, 200, body), nil
				}
				if mode == "detail-denied" {
					return apiResponse(req, 403, `{}`), nil
				}
				if mode == "detail-missing" {
					return apiResponse(req, 404, `{}`), nil
				}
				if mode == "detail-identity" {
					name += "-other"
				}
				return dataformResponse(req, 200, securityServiceData(name)), nil
			})
			request := securityServiceRequest(r, "global")
			page, err := r.List(t.Context(), request)
			if mode != "ancestor-denied" {
				if err != nil || page.Complete || len(page.Items) != 0 {
					t.Fatal("expected project page", page, err)
				}
				request.Cursor = page.NextCursor
				if mode == "ancestry-between-pages" {
					s.project["parent"] = "organizations/123"
				}
				page, err = r.List(t.Context(), request)
			}
			if err == nil || page.Complete || len(page.Items) > 0 {
				t.Fatal("unsafe ancestor observation", page, err)
			}
			if (mode == "ancestry-between-pages" || mode == "ancestor-denied") && lists != 0 {
				t.Fatal("changed ancestry used saved cursor", lists)
			}
		})
	}
}

func TestSecurityServicesAncestorInvokeBoundary(t *testing.T) {
	for _, parent := range []string{"folders/456", "organizations/123"} {
		for _, method := range []string{"get", "list"} {
			for _, mode := range []string{"valid", "foreign", "moved", "identity", "denied", "malformed"} {
				t.Run(parent+"/"+method+"/"+mode, func(t *testing.T) {
					s := newOrganizationScenario()
					calls := 0
					name := parent + "/locations/global"
					key := "parent"
					if method == "get" {
						name += "/securityCenterServices/event-threat-detection"
						key = "name"
					}
					r := securityAncestorRuntime(t, s, func(req *http.Request) (*http.Response, error) {
						calls++
						if req.Method != "GET" {
							t.Fatal(req.Method)
						}
						if mode == "denied" {
							return apiResponse(req, 403, `{}`), nil
						}
						id := name
						if method == "list" {
							id += "/securityCenterServices/event-threat-detection"
						}
						if mode == "identity" {
							id = strings.Replace(id, parent, "organizations/999", 1)
						}
						data := securityServiceData(id)
						if mode == "malformed" {
							data["modules"] = true
						}
						if mode == "moved" {
							s.project["parent"] = ""
						}
						if method == "list" {
							return dataformResponse(req, 200, map[string]any{"securityCenterServices": []any{data}}), nil
						}
						return dataformResponse(req, 200, data), nil
					})
					input := name
					if mode == "foreign" {
						input = strings.Replace(input, parent, strings.Split(parent, "/")[0]+"/999", 1)
					}
					result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "securitycentermanagement." + strings.Split(parent, "/")[0] + ".locations.securityCenterServices." + method, Parameters: map[string]any{key: input}})
					if mode == "valid" {
						if err != nil {
							t.Fatal(err)
						}
						raw, _ := json.Marshal(result)
						if strings.Contains(string(raw), "private-service-configuration") {
							t.Fatal("Invoke leaked config")
						}
					} else if err == nil {
						t.Fatal("unsafe Invoke", result)
					}
					if mode == "foreign" && calls != 0 {
						t.Fatal("foreign ancestor called", calls)
					}
				})
			}
		}
	}
}
