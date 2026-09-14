package gcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestSecurityServicesKnownClusterAndProjectGET(t *testing.T) {
	for _, cluster := range []bool{false, true} {
		for _, mode := range []string{"valid", "number-alias", "filtered", "unknown-state", "foreign-project", "wrong-location", "wrong-service", "wrong-cluster", "missing-name", "state-shape", "module-shape", "partial", "denied", "missing", "cancelled"} {
			t.Run(map[bool]string{false: "project", true: "cluster"}[cluster]+"/"+mode, func(t *testing.T) {
				parent := "projects/sample-project/locations/us-central1"
				operation := securityProjectServiceGet
				if cluster {
					parent += "/clusters/native-cluster-reference"
					operation = securityClusterServiceGet
				}
				name := parent + "/securityCenterServices/container-threat-detection"
				calls := 0
				r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
					calls++
					if req.Method != "GET" || req.URL.Host != securityServiceHost || req.URL.Path != "/v1/"+name {
						t.Fatal("unexpected read", req.Method, req.URL)
					}
					wantQuery := ""
					if mode == "filtered" {
						wantQuery = "showEligibleModulesOnly=true"
					}
					if req.URL.RawQuery != wantQuery {
						t.Fatal("unexpected module filter", req.URL)
					}
					if mode == "denied" {
						return apiResponse(req, 403, `{}`), nil
					}
					if mode == "missing" {
						return apiResponse(req, 404, `{}`), nil
					}
					data := securityServiceData(name)
					switch mode {
					case "number-alias":
						data["name"] = strings.Replace(name, "sample-project", "123456", 1)
					case "foreign-project":
						data["name"] = strings.Replace(name, "sample-project", "foreign-project", 1)
					case "wrong-location":
						data["name"] = strings.Replace(name, "us-central1", "europe-west1", 1)
					case "wrong-service":
						data["name"] = name + "-other"
					case "wrong-cluster":
						if cluster {
							data["name"] = strings.Replace(name, "native-cluster-reference", "different-reference", 1)
						} else {
							data["name"] = parent + "/clusters/other/securityCenterServices/container-threat-detection"
						}
					case "missing-name":
						delete(data, "name")
					case "unknown-state":
						data["effectiveEnablementState"] = "FUTURE_STATE"
					case "state-shape":
						data["effectiveEnablementState"] = true
					case "module-shape":
						data["modules"] = []any{}
					case "partial":
						data["unreachable"] = []any{"us-central1"}
					}
					return dataformResponse(req, 200, data), nil
				})
				params := map[string]any{"name": name}
				if mode == "filtered" {
					params["showEligibleModulesOnly"] = true
				}
				ctx, cancel := context.WithCancel(t.Context())
				defer cancel()
				if mode == "cancelled" {
					cancel()
				}
				result, err := r.Invoke(ctx, contracts.Invocation{ConnectionID: "connection", Operation: operation, Parameters: params})
				switch mode {
				case "valid", "number-alias", "filtered", "unknown-state":
					if err != nil || result.RequestID != "request-123" {
						t.Fatal(result, err)
					}
					want := "INGEST_ONLY"
					if mode == "unknown-state" {
						want = "FUTURE_STATE"
					}
					if result.Data["effectiveEnablementState"] != want || len(object(result.Data["modules"])) != 1 {
						t.Fatal("native state lost", result)
					}
					raw, _ := json.Marshal(result)
					if strings.Contains(string(raw), "private-service-configuration") {
						t.Fatal("private configuration escaped")
					}
				default:
					if err == nil || len(result.Data) != 0 {
						t.Fatal("invalid GET response accepted", result, err)
					}
					if mode == "cancelled" && !errors.Is(err, context.Canceled) {
						t.Fatal(err)
					}
				}
				if mode == "cancelled" && calls != 0 {
					t.Fatal("cancelled read reached service", calls)
				}
			})
		}
	}
}

func TestSecurityServicesKnownClusterReadBinding(t *testing.T) {
	name := "projects/sample-project/locations/us-central1/clusters/native-cluster-reference/securityCenterServices/container-threat-detection"
	c := &client{project: "sample-project", number: "123456"}
	kind, _ := findType(securityServiceType)
	op, params, err := c.resourceOperation(kind, "//"+securityServiceHost+"/"+name, "GET")
	if err != nil || op.ID != securityClusterServiceGet {
		t.Fatal(op.ID, err)
	}
	bound, err := catalog.BindREST(op, params)
	if err != nil || bound.URL != "https://"+securityServiceHost+"/v1/"+name || bound.Method != "GET" {
		t.Fatal(bound, err)
	}
	for _, method := range []string{"DELETE", "PATCH", "POST"} {
		if _, _, err := c.resourceOperation(kind, "//"+securityServiceHost+"/"+name, method); err == nil {
			t.Fatal("unreviewed mutation bound", method)
		}
	}
	for _, mode := range []string{"foreign-project", "foreign-root", "parent-traversal", "empty-cluster", "extra-segment", "project-operation", "no-list", "no-patch"} {
		t.Run(mode, func(t *testing.T) {
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				t.Fatal("invalid input reached native API", req.URL)
				return nil, nil
			})
			input, operation := name, securityClusterServiceGet
			switch mode {
			case "foreign-project":
				input = strings.Replace(name, "sample-project", "foreign-project", 1)
			case "foreign-root":
				input = strings.Replace(name, "projects/sample-project", "organizations/123", 1)
			case "parent-traversal":
				input = strings.Replace(name, "native-cluster-reference", "..", 1)
			case "empty-cluster":
				input = strings.Replace(name, "native-cluster-reference", "", 1)
			case "extra-segment":
				input = name + "/extra"
			case "project-operation":
				operation = securityProjectServiceGet
			case "no-list":
				operation = strings.TrimSuffix(operation, "get") + "list"
			case "no-patch":
				operation = strings.TrimSuffix(operation, "get") + "patch"
			}
			if _, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: operation, Parameters: map[string]any{"name": input}}); err == nil {
				t.Fatal("invalid cluster read accepted", mode)
			}
			if _, err := r.ResolveAction(t.Context(), "connection", asset.Asset{Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", NativeType: securityServiceType, NativeID: "//" + securityServiceHost + "/" + name}}); err == nil {
				t.Fatal("service settings became deletable")
			}
		})
	}
}

// Logging precedes native response validation, including identity validation.
func TestSecurityServicesTransportLogsMalformedConfiguration(t *testing.T) {
	for _, name := range []string{"", "invalid", "projects/sample-project/locations/us-central1/functions/function", "projects/sample-project/locations/us-central1/securityCenterServices/container-threat-detection"} {
		t.Run(name, func(t *testing.T) {
			var logs []execution.JobLogEntry
			ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
			data := map[string]any{"serviceConfig": map[string]any{"ordinary": "PRIVATE_SERVICE_CONFIG"}}
			if name != "" {
				data["name"] = name
			}
			c := &client{http: &http.Client{}}
			for _, payload := range []map[string]any{data, {"securityCenterServices": []any{data}}} {
				c.http.Transport = roundTripFunc(func(req *http.Request) (*http.Response, error) { return dataformResponse(req, 200, payload), nil })
				result, err := c.requestResult(ctx, "GET", "https://"+securityServiceHost+"/v1/projects/sample-project/locations/global/securityCenterServices", nil, nil)
				if err != nil {
					t.Fatal(err)
				}
				raw, _ := json.Marshal(result.Data)
				if !strings.Contains(string(raw), "PRIVATE_SERVICE_CONFIG") {
					t.Fatal("log redaction mutated internal response")
				}
			}
			encoded, _ := json.Marshal(logs)
			if len(logs) != 4 || strings.Contains(string(encoded), "PRIVATE_SERVICE_CONFIG") {
				t.Fatal("private configuration escaped into transport logs", string(encoded))
			}
		})
	}
	function := map[string]any{"name": "projects/sample-project/locations/us-central1/functions/function", "serviceConfig": map[string]any{"service": "projects/sample-project/locations/us-central1/services/function", "availableMemory": "256M"}}
	if object(safePayload(function)["serviceConfig"])["availableMemory"] != "256M" {
		t.Fatal("typed Cloud Functions inventory configuration lost")
	}
}
