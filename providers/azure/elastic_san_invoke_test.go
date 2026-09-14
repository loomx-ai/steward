package azure

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestElasticSanInvokeReadResponseBoundary(t *testing.T) {
	for _, kind := range []string{elasticSanType, elasticSanGroupType, elasticSanVolumeType, elasticSanSnapshotType, elasticSanEndpointType} {
		for _, listing := range []bool{false, true} {
			for _, mode := range []string{"valid", "case", "future-state", "foreign", "wrong-resource", "type", "state", "accepted", "polling", "error", "denied", "missing", "duplicate", "value", "cursor-host", "cursor-parent", "cursor-version", "cursor-type", "cursor-filter", "cursor-cycle"} {
				if !listing && (mode == "duplicate" || mode == "value" || strings.HasPrefix(mode, "cursor-")) {
					continue
				}
				t.Run(kind+"/"+map[bool]string{true: "list", false: "get"}[listing]+"/"+mode, func(t *testing.T) {
					record := elasticSanTestRecord(kind)
					id := text(record["id"])
					calls := 0
					next := ""
					r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
						calls++
						if req.Method != "GET" || req.URL.Query().Get("api-version") != elasticSanVersion {
							t.Fatal(req.Method, req.URL)
						}
						if listing && (kind == elasticSanGroupType || kind == elasticSanVolumeType) && req.Header.Get("x-ms-access-soft-deleted-resources") != "true" {
							t.Fatal("retained selector lost")
						}
						if mode == "denied" {
							return jsonResponse(403, map[string]any{}, nil), nil
						}
						if mode == "missing" {
							return jsonResponse(404, map[string]any{}, nil), nil
						}
						switch mode {
						case "future-state":
							object(record["properties"])["provisioningState"] = "FutureState"
						case "case":
							record["id"] = strings.ToUpper(id)
						case "foreign":
							record["id"] = strings.Replace(id, testSubscription, testTenant, 1)
						case "wrong-resource":
							if listing {
								record["id"] = strings.Replace(id, "/resourcegroups/test/", "/resourcegroups/other/", 1)
								if kind == elasticSanType {
									record["id"] = strings.Replace(id, testSubscription, testTenant, 1)
								}
							} else {
								record["id"] = id + "-other"
								record["name"] = last(id) + "-other"
							}
						case "type":
							record["type"] = "Microsoft.Network/virtualNetworks"
						case "state":
							object(record["properties"])["provisioningState"] = true
						}
						var body map[string]any = record
						if listing {
							next = req.URL.String() + "&%24skiptoken=opaque%2B%2F%3D"
							body = map[string]any{"value": []any{record}, "nextLink": next}
							switch mode {
							case "duplicate":
								body["value"] = []any{record, record}
							case "value":
								body["value"] = map[string]any{}
							case "cursor-host":
								body["nextLink"] = "https://foreign.example/page"
							case "cursor-parent":
								body["nextLink"] = strings.Replace(next, "/subscriptions/"+testSubscription+"/", "/subscriptions/"+testTenant+"/", 1)
							case "cursor-version":
								body["nextLink"] = strings.Replace(next, elasticSanVersion, "2025-09-01", 1)
							case "cursor-type":
								body["nextLink"] = true
							case "cursor-cycle":
								body["nextLink"] = req.URL.String()
							case "cursor-filter":
								body["nextLink"] = next + "&%24filter=filtered"
							}
						}
						if mode == "error" {
							body["error"] = map[string]any{"code": "Incomplete"}
						}
						status := 200
						if mode == "accepted" {
							status = 202
						}
						header := http.Header{"X-Ms-Request-Id": {"native-read-id"}}
						if mode == "polling" {
							header.Set("Location", req.URL.String())
						}
						return jsonResponse(status, body, header), nil
					})
					c, err := r.resolve(t.Context(), "connection")
					if err != nil {
						t.Fatal(err)
					}
					read, list, parentKind := elasticSanOperations(kind)
					operation := "Azure.Microsoft.ElasticSan." + read
					_, params, err := c.resourceOperation(resourceType{NativeType: kind, ReadOperations: []string{operation}}, id, "GET")
					if err != nil {
						t.Fatal(err)
					}
					if listing {
						operation = "Azure.Microsoft.ElasticSan." + list
						params = map[string]any{}
						parts := strings.Split(id, "/")
						if parentKind != "" {
							params["resourceGroupName"], params["elasticSanName"] = parts[4], parts[8]
						}
						if parentKind == elasticSanGroupType {
							params["volumeGroupName"] = parts[10]
						}
						if kind == elasticSanGroupType || kind == elasticSanVolumeType {
							params["x-ms-access-soft-deleted-resources"] = "true"
						}
					}
					result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: operation, Parameters: params})
					if mode == "valid" || mode == "case" || mode == "future-state" {
						if err != nil || result.RequestID != "native-read-id" || listing && result.NextToken != next {
							t.Fatal(result, err)
						}
						raw, _ := json.Marshal(result.Data)
						if len(raw) == 0 {
							t.Fatal("empty successful response")
						}
					} else if err == nil || len(result.Data) != 0 || result.NextToken != "" {
						t.Fatal("invalid native response accepted", mode, result, err)
					}
					if calls != 1 {
						t.Fatal("Invoke should return one page", calls)
					}
				})
			}
		}
	}
}

func TestElasticSanInvokeResourceGroupListBoundary(t *testing.T) {
	for _, mode := range []string{"valid", "empty", "foreign-group"} {
		t.Run(mode, func(t *testing.T) {
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if !strings.Contains(strings.ToLower(req.URL.Path), "/resourcegroups/test/providers/microsoft.elasticsan/elasticsans") {
					t.Fatal(req.URL)
				}
				record := elasticSanTestRecord(elasticSanType)
				if mode == "foreign-group" {
					record["id"] = strings.Replace(text(record["id"]), "/resourcegroups/test/", "/resourcegroups/other/", 1)
				}
				values := []any{record}
				if mode == "empty" {
					values = []any{}
				}
				return jsonResponse(200, map[string]any{"value": values}, nil), nil
			})
			result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.ElasticSan.ElasticSans_ListByResourceGroup", Parameters: map[string]any{"resourceGroupName": "test"}})
			if mode == "foreign-group" {
				if err == nil || len(result.Data) != 0 {
					t.Fatal("foreign group escaped", result, err)
				}
			} else if err != nil || result.NextToken != "" {
				t.Fatal(result, err)
			}
		})
	}
}

func TestElasticSanInvokeSnapshotFilterCursor(t *testing.T) {
	for _, mode := range []string{"preserve", "changed", "dropped"} {
		t.Run(mode, func(t *testing.T) {
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.URL.Query().Get("$filter") != "volumeName eq 'volume'" {
					t.Fatal("native filter lost", req.URL)
				}
				next := *req.URL
				query := next.Query()
				query.Set("$skiptoken", "next")
				if mode == "changed" {
					query.Set("$filter", "volumeName eq 'other'")
				}
				if mode == "dropped" {
					query.Del("$filter")
				}
				next.RawQuery = query.Encode()
				return jsonResponse(200, map[string]any{"value": []any{elasticSanTestRecord(elasticSanSnapshotType)}, "nextLink": next.String()}, nil), nil
			})
			result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.ElasticSan.VolumeSnapshots_ListByVolumeGroup", Parameters: map[string]any{"resourceGroupName": "test", "elasticSanName": "san", "volumeGroupName": "group", "$filter": "volumeName eq 'volume'"}})
			if mode == "preserve" {
				if err != nil || result.NextToken == "" {
					t.Fatal("valid filtered page rejected", result, err)
				}
			} else if err == nil || len(result.Data) != 0 {
				t.Fatal("changed filter accepted", result, err)
			}
		})
	}
}
