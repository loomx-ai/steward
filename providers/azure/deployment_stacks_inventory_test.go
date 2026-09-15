package azure

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestDeploymentStackInventoryReconcilesOwnReads(t *testing.T) {
	for _, suffix := range []string{"", "/resourceGroups/group"} {
		t.Run(suffix, func(t *testing.T) {
			scope := "/subscriptions/" + testSubscription + suffix
			path := scope + "/providers/Microsoft.Resources/deploymentStacks"
			calls := []string{}
			c := &client{subscription: testSubscription, http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls = append(calls, r.URL.Path)
				if r.Method != "GET" || r.URL.Query().Get("api-version") != deploymentStackVersion {
					t.Fatal("unexpected operation", r.Method, r.URL)
				}
				if r.URL.Path == path {
					name := "a"
					next := any(apiURL(path, deploymentStackVersion) + "&$skiptoken=second")
					if r.URL.Query().Get("$skiptoken") == "second" {
						name, next = "b", nil
					}
					return jsonResponse(200, map[string]any{"value": []any{map[string]any{"id": path + "/" + name, "type": deploymentStackType}}, "nextLink": next}, nil), nil
				}
				if strings.HasSuffix(r.URL.Path, "/gone") {
					return jsonResponse(404, map[string]any{}, nil), nil
				}
				return jsonResponse(200, map[string]any{"id": r.URL.Path, "type": deploymentStackType, "properties": map[string]any{"provisioningState": "futureState", "parameters": map[string]any{"secret": "private"}}}, nil), nil
			})}}
			rows, absent, err := c.deploymentStackInventory(context.Background(), scope, []string{path + "/hidden", path + "/gone", path + "/a"})
			if err != nil || len(rows) != 3 || len(absent) != 1 || absent[0] != strings.ToLower(path+"/gone") || len(calls) != 6 {
				t.Fatalf("rows=%d absent=%v calls=%v err=%v", len(rows), absent, calls, err)
			}
			if text(object(rows[0]["properties"])["provisioningState"]) != "futureState" {
				t.Fatal("inventory must preserve unknown states for subsequent review")
			}
		})
	}
}

func TestDeploymentStackInventoryRejectsIncompleteEvidence(t *testing.T) {
	for _, fault := range []string{"duplicate", "foreign", "wrong_type", "malformed_row", "list_202", "list_async", "bad_value", "bad_cursor", "cycle", "filter", "version", "duplicate_query", "foreign_cursor", "wrong_collection", "own_404", "own_403", "own_mismatch", "own_type", "own_async", "own_error", "own_properties"} {
		t.Run(fault, func(t *testing.T) {
			scope := "/subscriptions/" + testSubscription
			path := scope + "/providers/Microsoft.Resources/deploymentStacks"
			id := path + "/stack"
			calls := 0
			c := &client{subscription: testSubscription, http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if calls > 3 {
					t.Fatal("unbounded pagination")
				}
				status := 200
				headers := http.Header{}
				raw := map[string]any{"id": id, "type": deploymentStackType, "properties": map[string]any{}}
				if r.URL.Path == path {
					body := map[string]any{"value": []any{raw}}
					switch fault {
					case "duplicate":
						body["value"] = []any{raw, raw}
					case "foreign":
						raw["id"] = "/subscriptions/00000000-0000-0000-0000-000000000000/providers/Microsoft.Resources/deploymentStacks/stack"
					case "wrong_type":
						raw["type"] = "Microsoft.Resources/deployments"
					case "malformed_row":
						body["value"] = []any{42}
					case "list_202":
						status = 202
					case "list_async":
						headers.Set("Azure-AsyncOperation", "https://management.azure.com/operation")
					case "bad_value":
						body["value"] = nil
					case "bad_cursor":
						body["nextLink"] = 42
					case "cycle":
						body["nextLink"] = apiURL(path, deploymentStackVersion)
					case "filter":
						body["nextLink"] = apiURL(path, deploymentStackVersion) + "&$filter=name"
					case "version":
						body["nextLink"] = apiURL(path, "2024-03-01")
					case "duplicate_query":
						body["nextLink"] = apiURL(path, deploymentStackVersion) + "&api-version=" + deploymentStackVersion
					case "foreign_cursor":
						body["nextLink"] = "https://example.com" + path + "?api-version=" + deploymentStackVersion
					case "wrong_collection":
						body["nextLink"] = apiURL(path+"/other", deploymentStackVersion)
					}
					return jsonResponse(status, body, headers), nil
				}
				switch fault {
				case "own_404":
					status = 404
				case "own_403":
					status = 403
				case "own_mismatch":
					raw["id"] = path + "/other"
				case "own_type":
					raw["type"] = "Microsoft.Resources/deployments"
				case "own_async":
					headers.Set("Location", "https://management.azure.com/operation")
				case "own_error":
					raw["error"] = map[string]any{"code": "failure"}
				case "own_properties":
					raw["properties"] = nil
				}
				return jsonResponse(status, raw, headers), nil
			})}}
			rows, absent, err := c.deploymentStackInventory(context.Background(), scope, nil)
			if err == nil || rows != nil || absent != nil {
				t.Fatalf("accepted incomplete evidence: rows=%v absent=%v err=%v", rows, absent, err)
			}
		})
	}
}

func TestDeploymentStackInventoryScopeRejectedBeforeNetwork(t *testing.T) {
	c := &client{subscription: testSubscription, http: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) { t.Fatal("unauthorized request", r.URL); return nil, nil })}}
	for _, scope := range []string{"/providers/Microsoft.Management/managementGroups/group", "/subscriptions/00000000-0000-0000-0000-000000000000", "/subscriptions/" + testSubscription + "/resourceGroups/group/", "/subscriptions/" + testSubscription + "?query"} {
		if _, _, err := c.deploymentStackInventory(context.Background(), scope, nil); err == nil {
			t.Fatal("scope accepted", scope)
		}
		if _, err := c.deploymentStackRead(context.Background(), scope+"/providers/Microsoft.Resources/deploymentStacks/stack"); err == nil {
			t.Fatal("read accepted", scope)
		}
	}
	scope := "/subscriptions/" + testSubscription
	if _, _, err := c.deploymentStackInventory(context.Background(), scope, []string{scope + "/resourceGroups/other/providers/Microsoft.Resources/deploymentStacks/stack"}); err == nil {
		t.Fatal("foreign collection hint accepted")
	}
}
