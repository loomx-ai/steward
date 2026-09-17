package azure

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const (
	graphTestUserA  = "6e7b768e-07e2-4810-8459-485f84f8f204"
	graphTestUserB  = "87d349ed-44d7-43e1-9a83-5f2406dee5bd"
	graphTestGroup  = "02bd9fd6-8f93-4758-87c3-1fb73740a315"
	graphTestNested = "45b7d2e7-b882-4a80-ba97-10b7a63b8fa4"
)

type graphFixture struct {
	runtime  *Runtime
	users    map[string]map[string]any
	groups   map[string]map[string]any
	members  map[string][]any
	omitted  map[string]bool
	override func(*http.Request) (*http.Response, bool)
	mu       sync.Mutex
	scopes   map[string]bool
	queries  []url.Values
}

func newGraphFixture(t *testing.T) *graphFixture {
	t.Helper()
	f := &graphFixture{users: map[string]map[string]any{}, groups: map[string]map[string]any{}, members: map[string][]any{}, omitted: map[string]bool{}, scopes: map[string]bool{}}
	// Response shapes follow the Microsoft Graph v1.0 "List users", "List
	// groups" and "List group members" documentation.
	f.users[graphTestUserA] = map[string]any{"id": graphTestUserA, "displayName": "Adele Vance", "userPrincipalName": "AdeleV@contoso.com", "accountEnabled": true, "userType": "Member", "mail": "private@contoso.com", "mobilePhone": "+1 425 555 0109"}
	f.users[graphTestUserB] = map[string]any{"id": graphTestUserB, "displayName": "Guest", "userPrincipalName": "guest_fabrikam.com#EXT#@contoso.com", "accountEnabled": false, "userType": "Guest"}
	f.groups[graphTestGroup] = map[string]any{"id": graphTestGroup, "displayName": "Operators", "securityEnabled": true, "mailEnabled": false, "groupTypes": []any{}, "isAssignableToRole": false}
	f.groups[graphTestNested] = map[string]any{"id": graphTestNested, "displayName": "Nested", "securityEnabled": true, "mailEnabled": false, "groupTypes": []any{}}
	f.members[graphTestGroup] = []any{
		map[string]any{"@odata.type": "#microsoft.graph.user", "id": graphTestUserA},
		map[string]any{"@odata.type": "#microsoft.graph.group", "id": graphTestNested},
		map[string]any{"@odata.type": "#microsoft.graph.device", "id": "b1c5b3c4-2f5e-4a4c-9d1f-6a0f8a2c7e11"},
	}
	f.runtime = protocolRuntime(t, func(q *http.Request) (*http.Response, error) {
		if q.Method != "GET" {
			t.Fatal("directory inventory issued a mutation", q.Method, q.URL)
		}
		if f.override != nil {
			if res, ok := f.override(q); ok {
				return res, nil
			}
		}
		if q.URL.Host != "graph.microsoft.com" {
			t.Log("unrouted", q.URL.String())
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
		}
		f.mu.Lock()
		f.queries = append(f.queries, q.URL.Query())
		f.mu.Unlock()
		parts := strings.Split(strings.TrimPrefix(q.URL.Path, "/v1.0/"), "/")
		objects := map[string]map[string]map[string]any{"users": f.users, "groups": f.groups}[parts[0]]
		notFound := jsonResponse(404, map[string]any{"error": map[string]any{"code": "Request_ResourceNotFound", "message": "Resource does not exist."}}, nil)
		switch {
		case len(parts) == 1:
			values := []any{}
			for id, raw := range objects {
				if !f.omitted[id] {
					values = append(values, raw)
				}
			}
			// Graph pages with an absolute @odata.nextLink carrying $skiptoken.
			if q.URL.Query().Get("$skiptoken") == "" {
				return jsonResponse(200, map[string]any{"@odata.context": "https://graph.microsoft.com/v1.0/$metadata#" + parts[0], "value": []any{}, "@odata.nextLink": "https://graph.microsoft.com/v1.0/" + parts[0] + "?$select=" + url.QueryEscape(q.URL.Query().Get("$select")) + "&$top=999&$skiptoken=RFNwdAIAAQAAAB8"}, nil), nil
			}
			return jsonResponse(200, map[string]any{"value": values}, nil), nil
		case len(parts) == 2:
			if raw := objects[parts[1]]; raw != nil {
				return jsonResponse(200, raw, nil), nil
			}
			return notFound, nil
		case len(parts) == 3 && parts[0] == "groups" && parts[2] == "members":
			members := f.members[parts[1]]
			if members == nil {
				members = []any{}
			}
			return jsonResponse(200, map[string]any{"value": members}, nil), nil
		}
		return notFound, nil
	})
	product := f.runtime.transport
	f.runtime.transport = roundTripFunc(func(q *http.Request) (*http.Response, error) {
		if q.URL.Host == "login.microsoftonline.com" {
			body, _ := q.GetBody()
			form := make([]byte, 4096)
			n, _ := body.Read(form)
			values, _ := url.ParseQuery(string(form[:n]))
			f.mu.Lock()
			f.scopes[values.Get("scope")] = true
			f.mu.Unlock()
		}
		return product.RoundTrip(q)
	})
	return f
}

func graphRequest(f *graphFixture, nativeType string, known ...string) contracts.InventoryRequest {
	kind := f.runtime.resourceKind(nativeType)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: graphDirectorySource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeGlobal, NativeID: testSubscription + "/global"}, KnownNativeIDs: known}
}

func TestGraphDirectoryInventory(t *testing.T) {
	f := newGraphFixture(t)
	users, err := f.runtime.List(t.Context(), graphRequest(f, graphUserType))
	if err != nil || !users.Complete || len(users.Items) != 2 {
		t.Fatal(err, users)
	}
	var adele contracts.InventoryItem
	for _, item := range users.Items {
		if item.Normalized["object_id"] == graphTestUserA {
			adele = item
		}
	}
	if adele.NativeID != rbacPrincipalSelector(testTenant, graphTestUserA) || adele.NativeType != graphUserType || adele.State != "enabled" || adele.Name != "Adele Vance" || adele.Actionable == nil || *adele.Actionable || adele.Normalized["cleanup_protected"] != true || adele.Normalized["userPrincipalName"] != "AdeleV@contoso.com" {
		t.Fatal("invalid user authority", adele)
	}
	wire, _ := json.Marshal(users)
	if strings.Contains(string(wire), "private@contoso.com") || strings.Contains(string(wire), "425 555") {
		t.Fatal("unselected personal attributes entered inventory")
	}
	for _, query := range f.queries {
		if query.Get("$select") == "" || query.Has("$filter") || query.Has("$search") {
			t.Fatal("directory read was not a fixed projection", query)
		}
	}
	groups, err := f.runtime.List(t.Context(), graphRequest(f, graphGroupType))
	if err != nil || len(groups.Items) != 2 {
		t.Fatal(err, groups)
	}
	for _, item := range groups.Items {
		if item.Normalized["object_id"] != graphTestGroup {
			continue
		}
		userRefs, _ := item.Normalized[referenceKey(graphUserType)].([]string)
		groupRefs, _ := item.Normalized[referenceKey(graphGroupType)].([]string)
		if len(userRefs) != 1 || userRefs[0] != rbacPrincipalSelector(testTenant, graphTestUserA) || len(groupRefs) != 1 || groupRefs[0] != rbacPrincipalSelector(testTenant, graphTestNested) {
			t.Fatal("group membership lost", item.Normalized)
		}
	}
	if !f.scopes["https://graph.microsoft.com/.default"] {
		t.Fatal("Graph token audience not separated", f.scopes)
	}
	if _, err := f.runtime.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.Graph.users.user.GetUser", Parameters: map[string]any{"userId": graphTestUserA}}); err == nil {
		t.Fatal("generic invocation reached Microsoft Graph")
	}
}

func TestGraphDirectoryReconciliationAndBoundaries(t *testing.T) {
	known := rbacPrincipalSelector(testTenant, graphTestUserB)
	for _, mode := range []string{"known-omitted-live", "known-deleted", "forbidden", "foreign-next-link", "filtered-next-link", "wrong-source", "foreign-tenant-known", "invalid-object-id"} {
		t.Run(mode, func(t *testing.T) {
			f := newGraphFixture(t)
			req := graphRequest(f, graphUserType, known)
			switch mode {
			case "known-omitted-live":
				f.omitted[graphTestUserB] = true
			case "known-deleted":
				delete(f.users, graphTestUserB)
			case "forbidden":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if q.URL.Host == "graph.microsoft.com" {
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "Authorization_RequestDenied"}}, nil), true
					}
					return nil, false
				}
			case "foreign-next-link", "filtered-next-link":
				f.override = func(q *http.Request) (*http.Response, bool) {
					if q.URL.Host == "graph.microsoft.com" && q.URL.Path == "/v1.0/users" {
						next := "https://graph.evil.invalid/v1.0/users?$skiptoken=x"
						if mode == "filtered-next-link" {
							next = "https://graph.microsoft.com/v1.0/users?$filter=accountEnabled%20eq%20true"
						}
						return jsonResponse(200, map[string]any{"value": []any{}, "@odata.nextLink": next}, nil), true
					}
					return nil, false
				}
			case "wrong-source":
				req.Source = inventorySource
			case "foreign-tenant-known":
				req.KnownNativeIDs = []string{rbacPrincipalSelector("99999999-8888-4777-8666-555555555555", graphTestUserB)}
			case "invalid-object-id":
				f.users[graphTestUserA]["id"] = "../me"
			}
			batch, err := f.runtime.List(t.Context(), req)
			switch mode {
			case "known-omitted-live":
				if err != nil || len(batch.Items) != 2 || len(batch.AbsentNativeIDs) != 0 {
					t.Fatal("list omission retired a live user", err, batch)
				}
			case "known-deleted":
				if err != nil || len(batch.AbsentNativeIDs) != 1 || batch.AbsentNativeIDs[0] != known {
					t.Fatal("own absence not reconciled", err, batch)
				}
			default:
				if err == nil || len(batch.AbsentNativeIDs) != 0 {
					t.Fatal("unsafe directory inventory accepted", batch)
				}
			}
		})
	}
}
