package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type stackRuntimeFixture struct {
	runtime *Runtime
	fault   string
	reads   int
	hidden  bool
	members map[string][]any
}

func newStackRuntimeFixture(t *testing.T) *stackRuntimeFixture {
	f := &stackRuntimeFixture{}
	root := "/subscriptions/" + testSubscription
	group := root + "/resourcegroups/group"
	f.runtime = protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		path := strings.ToLower(q.URL.Path)
		if q.Method != "GET" {
			t.Fatal("unexpected mutation", q.Method)
		}
		if path == root+"/resourcegroups" {
			if f.fault == "group_index" {
				return jsonResponse(403, map[string]any{}, nil), nil
			}
			rows := []any{map[string]any{"id": group, "type": groupType}}
			if f.hidden {
				rows = []any{}
			}
			return jsonResponse(200, map[string]any{"value": rows}, nil), nil
		}
		if path == group {
			if f.fault == "group_missing" {
				return jsonResponse(404, map[string]any{}, nil), nil
			}
			return jsonResponse(200, map[string]any{"id": group, "type": groupType}, nil), nil
		}
		if strings.HasSuffix(path, "/providers/microsoft.resources/deploymentstacks") {
			if f.fault == "stack_index" {
				return jsonResponse(403, map[string]any{}, nil), nil
			}
			rows := []any{map[string]any{"id": path + "/stack", "type": deploymentStackType}}
			return jsonResponse(200, map[string]any{"value": rows}, nil), nil
		}
		if strings.Contains(path, "/deploymentstacks/") {
			if strings.HasSuffix(path, "/gone") {
				return jsonResponse(404, map[string]any{}, nil), nil
			}
			f.reads++
			state := "succeeded"
			secret := "runtime-private-canary"
			if f.fault == "drift" && f.reads > 2 {
				secret = "changed-private-canary"
			}
			members := f.members[path]
			if members == nil {
				members = []any{}
			}
			return jsonResponse(200, map[string]any{"id": path, "type": deploymentStackType, "name": "stack", "properties": map[string]any{"provisioningState": state, "resources": members, "parameters": map[string]any{"value": secret}}}, nil), nil
		}
		t.Fatalf("unexpected path %s", path)
		return nil, nil
	})
	return f
}
func stackRuntimeRequest(r *Runtime) contracts.InventoryRequest {
	kind := r.resourceKind(deploymentStackType)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: deploymentStackSource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}}
}
func TestDeploymentStackRuntimeInventory(t *testing.T) {
	f := newStackRuntimeFixture(t)
	req := stackRuntimeRequest(f.runtime)
	root := "/subscriptions/" + testSubscription
	req.KnownNativeIDs = []string{root + "/providers/Microsoft.Resources/deploymentStacks/gone"}
	req.Limit = 1
	first, err := f.runtime.List(context.Background(), req)
	if err != nil || first.Complete || first.NextCursor == "" || len(first.Items) != 1 || len(first.AbsentNativeIDs) != 0 {
		t.Fatal(first, err)
	}
	req.Cursor = first.NextCursor
	second, err := f.runtime.List(context.Background(), req)
	if err != nil || !second.Complete || len(second.Items) != 1 || len(second.AbsentNativeIDs) != 1 {
		t.Fatal(second, err)
	}
	for _, item := range append(first.Items, second.Items...) {
		if item.Actionable == nil || *item.Actionable || item.Normalized["cleanup_protected"] != true || object(item.Normalized["_deployment_stack_review"])["arm_members_complete"] != true {
			t.Fatal(item)
		}
		wire, _ := json.Marshal(item)
		if strings.Contains(string(wire), "private-canary") || strings.Contains(string(wire), "parameters") {
			t.Fatal("private raw payload persisted", string(wire))
		}
	}
	f.fault = "drift"
	if _, err := f.runtime.List(context.Background(), req); err == nil {
		t.Fatal("cursor survived configuration drift")
	}
}
func TestDeploymentStackRuntimeFailuresAndRouting(t *testing.T) {
	for _, fault := range []string{"group_index", "group_missing", "stack_index", "drift"} {
		t.Run(fault, func(t *testing.T) {
			f := newStackRuntimeFixture(t)
			f.fault = fault
			batch, err := f.runtime.List(context.Background(), stackRuntimeRequest(f.runtime))
			if err == nil || batch.Complete || len(batch.Items) != 0 || len(batch.AbsentNativeIDs) != 0 {
				t.Fatal(batch, err)
			}
		})
	}
	f := newStackRuntimeFixture(t)
	req := stackRuntimeRequest(f.runtime)
	req.Source = inventorySource
	batch, err := f.runtime.List(context.Background(), req)
	if err != nil || !batch.Complete || len(batch.Items) != 0 || f.reads != 0 {
		t.Fatal("generic source duplicated native stack", batch, err)
	}
	req.Source = deploymentStackSource
	req.Scope = asset.Scope{Kind: asset.ScopeGlobal, NativeID: "global"}
	if batch, err = f.runtime.List(context.Background(), req); err != nil || !batch.Complete || len(batch.Items) != 2 {
		t.Fatal(batch, err)
	}
	for _, change := range []func(*contracts.InventoryRequest){func(q *contracts.InventoryRequest) { q.Source = productInventorySource }, func(q *contracts.InventoryRequest) { q.ResourceKind = nil }, func(q *contracts.InventoryRequest) { q.Scope.NativeID = "foreign" }, func(q *contracts.InventoryRequest) { q.Cursor = "bad" }, func(q *contracts.InventoryRequest) { q.Options = map[string]any{"filter": "x"} }} {
		q := stackRuntimeRequest(f.runtime)
		change(&q)
		if _, err := f.runtime.List(context.Background(), q); err == nil {
			t.Fatal("invalid request accepted", q)
		}
	}
}
func TestDeploymentStackRuntimeKnownHiddenGroup(t *testing.T) {
	f := newStackRuntimeFixture(t)
	f.hidden = true
	req := stackRuntimeRequest(f.runtime)
	req.KnownNativeIDs = []string{"/subscriptions/" + testSubscription + "/resourceGroups/group/providers/Microsoft.Resources/deploymentStacks/stack"}
	batch, err := f.runtime.List(context.Background(), req)
	if err != nil || !batch.Complete || len(batch.Items) != 2 {
		t.Fatal(batch, err)
	}
	f.fault = "group_missing"
	if _, err := f.runtime.List(context.Background(), req); err == nil {
		t.Fatal("live stack with missing parent accepted")
	}
	req.KnownNativeIDs = []string{"/subscriptions/" + testSubscription + "/resourceGroups/group/providers/Microsoft.Resources/deploymentStacks/gone"}
	batch, err = f.runtime.List(context.Background(), req)
	if err != nil || !batch.Complete || len(batch.AbsentNativeIDs) != 1 {
		t.Fatal(batch, err)
	}
}

func TestDeploymentStackGroupPaginationBoundary(t *testing.T) {
	for _, fault := range []string{"filter", "duplicate", "async", "valid"} {
		t.Run(fault, func(t *testing.T) {
			calls := 0
			c := directClient(func(q *http.Request) (*http.Response, error) {
				calls++
				if calls > 2 {
					t.Fatal("unexpected extra page")
				}
				headers := http.Header{}
				body := map[string]any{"value": []any{}}
				if calls == 1 {
					next := apiURL("/subscriptions/"+testSubscription+"/resourcegroups", resourcesVersion) + "&$skiptoken=next"
					if fault == "filter" {
						next += "&$filter=tagName"
					}
					if fault == "duplicate" {
						next += "&api-version=" + resourcesVersion
					}
					if fault == "async" {
						headers.Set("Azure-AsyncOperation", "https://management.azure.com/operation")
					} else {
						body["nextLink"] = next
					}
				}
				return jsonResponse(200, body, headers), nil
			})
			_, err := c.deploymentStackGroups(context.Background())
			if fault == "valid" {
				if err != nil || calls != 2 {
					t.Fatal(calls, err)
				}
			} else if err == nil || calls != 1 {
				t.Fatal("incomplete index accepted", calls, err)
			}
		})
	}
}
func TestDeploymentStackResourceOperationScopes(t *testing.T) {
	c := directClient(nil)
	kind, ok := findType(deploymentStackType)
	if !ok {
		t.Fatal("missing registered stack")
	}
	for _, suffix := range []string{"", "/resourceGroups/group"} {
		id := c.root() + suffix + "/providers/Microsoft.Resources/deploymentStacks/stack"
		if _, err := c.resourceURL(kind, id); err != nil {
			t.Fatal(err)
		}
		if _, _, err := c.resourceOperation(kind, id, "DELETE"); err == nil {
			t.Fatal("unimplemented deletion enabled")
		}
	}
	for _, scope := range []string{"/providers/Microsoft.Management/managementGroups/group", "/subscriptions/00000000-0000-0000-0000-000000000000"} {
		if _, err := c.resourceURL(kind, scope+"/providers/Microsoft.Resources/deploymentStacks/stack"); err == nil {
			t.Fatal("unapproved scope accepted")
		}
	}
}
