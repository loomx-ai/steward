package gcp

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestSecurityServiceListInvokeValidation(t *testing.T) {
	for _, root := range []string{"projects/sample-project", "folders/456", "organizations/123"} {
		for _, mode := range []string{"valid", "empty", "empty-page", "aliases", "foreign", "location", "cluster", "missing-name", "traversal", "duplicate", "duplicate-alias", "null-list", "object-list", "null-record", "state", "modules", "partial", "record-partial", "token", "denied", "missing", "private-extra"} {
			t.Run(root+"/"+mode, func(t *testing.T) {
				parent := root + "/locations/global"
				name := parent + "/securityCenterServices/event-threat-detection"
				calls := 0
				handler := roundTripFunc(func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != "GET" || req.URL.Path != "/v1/"+parent+"/securityCenterServices" || req.URL.Query().Get("pageToken") != "opaque+/=" || req.URL.Query().Get("pageSize") != "7" || req.URL.Query().Get("showEligibleModulesOnly") != "true" {
						t.Fatal(req.Method, req.URL)
					}
					if mode == "denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					if mode == "missing" {
						return apiResponse(req, 404, `{}`), nil
					}
					record := securityServiceData(name)
					body := map[string]any{"securityCenterServices": []any{record}, "nextPageToken": "next+/="}
					switch mode {
					case "empty":
						body = map[string]any{}
					case "empty-page":
						delete(body, "securityCenterServices")
					case "aliases":
						record["name"] = strings.Replace(name, "projects/sample-project/", "projects/123456/", 1)
					case "foreign":
						record["name"] = "projects/other-project/locations/global/securityCenterServices/event-threat-detection"
					case "location":
						record["name"] = strings.Replace(name, "/global/", "/eu/", 1)
					case "cluster":
						record["name"] = parent + "/clusters/cluster/securityCenterServices/event-threat-detection"
					case "missing-name":
						delete(record, "name")
					case "traversal":
						record["name"] = parent + "/securityCenterServices/.."
					case "duplicate":
						body["securityCenterServices"] = []any{record, record}
					case "duplicate-alias":
						other := securityServiceData(strings.Replace(name, "projects/sample-project/", "projects/123456/", 1))
						body["securityCenterServices"] = []any{record, other}
					case "null-list":
						body["securityCenterServices"] = nil
					case "object-list":
						body["securityCenterServices"] = map[string]any{}
					case "null-record":
						body["securityCenterServices"] = []any{record, nil}
					case "state":
						record["effectiveEnablementState"] = true
					case "modules":
						record["modules"] = []any{}
					case "partial":
						body["unreachable"] = []any{"global"}
					case "record-partial":
						record["unreachable"] = []any{"global"}
					case "token":
						body["nextPageToken"] = 12
					case "private-extra":
						body["extension"] = map[string]any{"serviceConfig": map[string]any{"ordinary": "private-service-configuration"}}
					}
					return dataformResponse(req, 200, body), nil
				})
				var r *Runtime
				if strings.HasPrefix(root, "projects/") {
					r = protocolRuntime(t, handler)
				} else {
					r = securityAncestorRuntime(t, newOrganizationScenario(), handler)
				}
				result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "securitycentermanagement." + strings.Split(root, "/")[0] + ".locations.securityCenterServices.list", Parameters: map[string]any{"parent": parent, "pageToken": "opaque+/=", "pageSize": 7, "showEligibleModulesOnly": true}})
				switch mode {
				case "valid", "empty", "empty-page", "aliases", "private-extra":
					if err != nil || result.RequestID != "request-123" {
						t.Fatal(result, err)
					}
					want := "next+/="
					if mode == "empty" {
						want = ""
					}
					if result.NextToken != want {
						t.Fatal("native token lost", result)
					}
					raw, _ := json.Marshal(result)
					if strings.Contains(string(raw), "private-service-configuration") {
						t.Fatal("private list configuration escaped")
					}
				default:
					if err == nil || len(result.Data) != 0 {
						t.Fatal("invalid list accepted", mode, result, err)
					}
				}
				if calls != 1 {
					t.Fatal("Invoke must return one native page", calls)
				}
			})
		}
	}
}

func TestSecurityServiceProjectListInputBoundary(t *testing.T) {
	for _, parent := range []string{"projects/foreign-project/locations/global", "folders/456/locations/global", "projects/sample-project/locations/..", "projects/sample-project/locations/global/clusters/cluster", "projects/sample-project/locations/", "projects/sample-project/locations/-"} {
		t.Run(parent, func(t *testing.T) {
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				t.Fatal("invalid parent reached service", req.URL)
				return nil, nil
			})
			if _, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "securitycentermanagement.projects.locations.securityCenterServices.list", Parameters: map[string]any{"parent": parent}}); err == nil {
				t.Fatal("invalid parent accepted")
			}
		})
	}
}
