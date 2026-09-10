package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestAPIMOperationBoundaries(t *testing.T) {
	id := resourceID(apimServiceType, "service") + "/apis/api"
	root := apimRootID(id)
	valid := []string{
		apiURL(root+"/tenant/operationResults/0123456789abcdef01234567", apimVersion),
		apiURL(root+"/operationResults/b3BlcmF0aW9u==", apimVersion),
		apiURL(id, apimVersion) + "&asyncId=0123456789abcdef01234567&asyncCode=204",
		apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.ApiManagement/locations/westus/operationStatuses/operation-one", apimVersion),
		apiURL(root+"/operationResults/b3BlcmF0aW9u==", apimVersion) + "&asyncResponse&t=1234&c=certificate&s=signature&h=hash",
	}
	for _, endpoint := range valid {
		if err := validateAPIMOperationURL(testSubscription, id, "westus", apimVersion, endpoint); err != nil {
			t.Fatal("native polling URL rejected", endpoint, err)
		}
	}
	invalid := []string{
		strings.Replace(valid[0], testSubscription, testTenant, 1),
		strings.Replace(valid[0], root, root+"-other", 1),
		strings.Replace(valid[0], "management.azure.com", "evil.example", 1),
		strings.Replace(valid[0], "https://", "http://", 1),
		valid[0] + "&api-version=" + apimVersion,
		valid[0] + "&filter=hidden",
		valid[0] + "#fragment",
		strings.Replace(valid[3], "/westus/", "/eastus/", 1),
		strings.Replace(valid[2], "asyncCode=204", "asyncCode=200", 1),
		strings.Replace(valid[2], "asyncId=0123456789abcdef01234567", "asyncId=bad", 1),
		strings.Replace(valid[4], "&h=hash", "&unrecognized=hash", 1),
		strings.Replace(valid[4], "asyncResponse&", "asyncResponse=true&", 1),
		strings.Replace(valid[1], "b3BlcmF0aW9u==", "encoded%2Fescape", 1),
		apiURL(root+"/apis/another", apimVersion),
	}
	for _, endpoint := range invalid {
		if err := validateAPIMOperationURL(testSubscription, id, "westus", apimVersion, endpoint); err == nil {
			t.Fatal("invalid polling URL accepted", endpoint)
		}
	}
}

func TestAPIMAsyncReceiptAndFinalAbsence(t *testing.T) {
	for _, mode := range []string{"status", "location", "regional-location", "rotating", "operation-404", "failed", "missing-status", "unknown-status", "partial", "forbidden", "wrong-resource", "changed-receipt", "conflicting-headers"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := apimScenario(t)
			target := cdnAsset(t, assets, apimAPIType)
			if mode == "location" || mode == "rotating" {
				target = cdnAsset(t, assets, apimServiceType)
			}
			request, _ := dnsRequest(t, r, assets, target)
			endpoint := apiURL(apimRootID(target.Identity.NativeID)+"/tenant/operationResults/0123456789abcdef01234567", apimVersion)
			header := "Azure-Asyncoperation"
			if mode == "location" || mode == "rotating" {
				endpoint = apiURL(target.Identity.NativeID+"/operationResults/b3BlcmF0aW9u==", apimVersion)
				header = "Location"
				if mode == "rotating" {
					endpoint += "&t=1&c=certificate&s=signature&h=hash"
				}
			}
			if mode == "regional-location" {
				metadata, _ := providerData()
				op, _ := metadata.catalog.Operation("Azure.Microsoft.ApiManagement.OperationsResults_Get")
				bound, err := bindAzureREST(op, map[string]any{"subscriptionId": testSubscription, "location": "westus", "operationId": "01234567-89ab-cdef-0123-456789abcdef"})
				if err != nil {
					t.Fatal(err)
				}
				endpoint, header = bound.URL, "Location"
			}
			original := s.handle
			polls := 0
			finished := false
			sent := endpoint
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					original(req)
					s.deletes = append(s.deletes, strings.ToLower(req.URL.Path))
					headers := http.Header{header: {endpoint}, "Retry-After": {"1"}}
					if mode == "conflicting-headers" {
						headers.Set("Location", apiURL(target.Identity.NativeID+"-other", apimVersion)+"&asyncId=0123456789abcdef01234567&asyncCode=204")
					}
					return jsonResponse(202, nil, headers), true
				}
				if req.URL.String() == sent {
					polls++
					if mode == "regional-location" {
						// Native OperationsResults_Get returns an empty 200;
						// 202 carries the same Location while still in progress.
						if finished {
							return &http.Response{StatusCode: 200, Body: http.NoBody, Header: http.Header{}}, true
						}
						return &http.Response{StatusCode: 202, Body: http.NoBody, Header: http.Header{"Location": {sent}}}, true
					}
					if mode == "operation-404" {
						return jsonResponse(404, map[string]any{}, nil), true
					}
					if mode == "forbidden" {
						return jsonResponse(403, map[string]any{}, nil), true
					}
					if mode == "partial" {
						return jsonResponse(206, map[string]any{"status": "Succeeded"}, nil), true
					}
					state := "InProgress"
					if finished {
						state = "Succeeded"
					}
					if mode == "failed" {
						state = "Failed"
					}
					if mode == "unknown-status" {
						state = "Unrecognized"
					}
					body := map[string]any{"status": state}
					if mode == "missing-status" {
						body = map[string]any{"name": "0123456789abcdef01234567"}
					}
					if mode == "wrong-resource" {
						body["resourceId"] = target.Identity.NativeID + "-other"
					}
					headers := http.Header{"Retry-After": {"1"}}
					if mode == "location" && finished {
						return jsonResponse(204, nil, headers), true
					}
					if mode == "rotating" && !finished {
						u, _ := url.Parse(sent)
						q := u.Query()
						q.Set("t", fmt.Sprint(polls+1))
						u.RawQuery = q.Encode()
						sent = u.String()
						headers.Set("Location", sent)
					}
					return jsonResponse(200, body, headers), true
				}
				return original(req)
			}
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			receipt, err := driver.Execute(context.Background(), request)
			if mode == "conflicting-headers" {
				if err == nil || polls != 0 {
					t.Fatal("conflicting native headers accepted", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if mode == "changed-receipt" {
				receipt.Data["apim_operation_binding"] = "changed"
			}
			waited, err := driver.Wait(context.Background(), request, receipt)
			switch mode {
			case "failed", "missing-status", "unknown-status", "partial", "forbidden", "wrong-resource", "changed-receipt":
				if err == nil {
					t.Fatal("invalid operation response accepted", waited)
				}
				return
			}
			if err != nil || waited.Done {
				t.Fatal("operation completed before native absence", waited, err)
			}
			if mode == "rotating" {
				if text(waited.Data["apim_poll_operation"]) != sent {
					t.Fatal("rotated poll receipt not persisted", waited.Data)
				}
				receipt.Data = waited.Data
			}
			encoded, _ := json.Marshal(receipt)
			json.Unmarshal(encoded, &receipt)
			encoded, _ = json.Marshal(request)
			json.Unmarshal(encoded, &request)
			driver, err = r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			finished = true
			waited, err = driver.Wait(context.Background(), request, receipt)
			if err != nil || waited.Done {
				t.Fatal("successful operation hid live resource", waited, err)
			}
			s.gone[target.Identity.NativeID] = true
			streamAnalyticsAfterDelete(s)
			waited, err = driver.Wait(context.Background(), request, receipt)
			if err != nil || !waited.Done || len(s.deletes) != 1 {
				t.Fatal("resumed native absence failed", waited, err, s.deletes)
			}
		})
	}
}

func TestAPIMHeaderETagCannotBeReplacedByWildcardOrBodyETag(t *testing.T) {
	for _, mode := range []string{"header-changed", "body-only", "wildcard", "duplicate", "empty"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := apimScenario(t)
			target := cdnAsset(t, assets, apimServiceType+"/namedValues")
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			original := s.handle
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "GET" && strings.EqualFold(req.URL.Path, target.Identity.NativeID) {
					headers := http.Header{"Etag": {`"another-etag"`}}
					switch mode {
					case "body-only":
						headers = nil
					case "wildcard":
						headers.Set("Etag", "*")
					case "duplicate":
						headers["Etag"] = []string{`"one"`, `"two"`}
					case "empty":
						headers.Set("Etag", "")
					}
					raw := s.records[target.Identity.NativeID]
					copy := map[string]any{}
					for k, v := range raw {
						if k != "_apim_header_etag" {
							copy[k] = v
						}
					}
					copy["etag"] = text(target.Normalized["arm_etag"])
					return jsonResponse(200, copy, headers), true
				}
				return original(req)
			}
			_, err = driver.Execute(context.Background(), contracts.ActionRequest{Action: "delete", Asset: target})
			if err == nil || len(s.deletes) != 0 {
				t.Fatal("invalid conditional ETag authorized deletion", err, s.deletes)
			}
		})
	}
}

func TestAPIMOptionalServiceCreationUsesETagFallback(t *testing.T) {
	s, _, assets := apimScenario(t)
	target := cdnAsset(t, assets, apimServiceType)
	raw := s.records[target.Identity.NativeID]
	delete(object(raw["properties"]), "createdAtUtc")
	if err := validateAPIM(apimServiceType, raw); err != nil {
		t.Fatal("native optional creation date rejected", err)
	}
	before := productGeneration(raw)
	raw["_apim_header_etag"] = `"recreated"`
	if productGeneration(raw) == before {
		t.Fatal("parent cursor lost fallback service generation")
	}
	delete(raw, "_apim_header_etag")
	raw["etag"] = "body-service-generation"
	if err := validateAPIM(apimServiceType, raw); err != nil {
		t.Fatal("native service body ETag rejected", err)
	}
	before = productGeneration(raw)
	raw["etag"] = "new-body-service-generation"
	if productGeneration(raw) == before {
		t.Fatal("body ETag did not bind service incarnation")
	}
	delete(raw, "etag")
	if err := validateAPIM(apimServiceType, raw); err == nil {
		t.Fatal("service has no incarnation evidence")
	}
	object(raw["properties"])["createdAtUtc"] = "malformed-date"
	if err := validateAPIM(apimServiceType, raw); err == nil {
		t.Fatal("malformed service creation time accepted")
	}
}
