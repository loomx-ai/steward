package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type fleetFixture struct {
	runtime   *Runtime
	resources map[string]map[string]any
	group     map[string]any
	locks     []any
	calls     map[string]int
	omitted   map[string]bool
	override  func(*http.Request) (*http.Response, bool)
}

func newFleetFixture(t *testing.T) *fleetFixture {
	t.Helper()
	f := &fleetFixture{resources: map[string]map[string]any{}, calls: map[string]int{}, omitted: map[string]bool{}, locks: []any{}}
	root := "/subscriptions/" + testSubscription
	group := root + "/resourcegroups/test"
	f.group = map[string]any{"id": group, "type": groupType, "name": "test", "location": "westus", "properties": map[string]any{"provisioningState": "Succeeded"}}
	for _, kind := range fleetTestKinds {
		name := "resource1"
		switch kind {
		case fleetType:
			name = "fleet1"
		case fleetMemberType:
			name = "member1"
		case fleetRunType:
			name = "run1"
		case fleetStrategyType:
			name = "strategy1"
		case fleetProfileType:
			name = "profile1"
		case fleetGateType:
			name = rbacTestRoleName
		}
		raw := fleetTestBody(t, kind, name)
		object(raw["properties"])["futurePrivateSetting"] = map[string]any{"value": "fleet-private-configuration"}
		if kind == fleetType {
			raw["location"] = "westus"
		}
		if kind == fleetNamespaceType {
			raw["location"] = "eastus"
		}
		f.resources[text(raw["id"])] = raw
	}
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		path := strings.ToLower(req.URL.Path)
		f.calls[req.Method+" "+path]++
		if f.override != nil {
			if response, ok := f.override(req); ok {
				return response, nil
			}
		}
		if req.Method != "GET" || req.URL.Host != "management.azure.com" || !strings.HasPrefix(path, root+"/") {
			t.Fatal("unrequested Fleet operation", req.Method, req.URL)
		}
		if path == root+"/resourcegroups" || path == group || path == root+"/providers/microsoft.authorization/locks" {
			if req.URL.Query().Get("api-version") != resourcesVersion && path != root+"/providers/microsoft.authorization/locks" {
				t.Fatal("changed group API version")
			}
			body := map[string]any{"value": []any{f.group}}
			if path == group {
				body = f.group
			}
			if strings.HasSuffix(path, "/locks") {
				body = map[string]any{"value": f.locks}
			}
			return jsonResponse(200, body, nil), nil
		}
		version := fleetVersion
		if strings.Contains(path, "/clustermeshprofiles") || strings.Contains(path, "/members") {
			version = fleetMeshVersion
		}
		if !fleetPath(path) || req.URL.Query().Get("api-version") != version || len(req.URL.Query()) != 1 {
			t.Fatal("Fleet read escaped native route", req.URL)
		}
		if _, _, err := fleetIdentity(path); err == nil {
			if raw := f.resources[path]; raw != nil {
				return jsonResponse(200, raw, nil), nil
			}
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
		}
		values := []any{}
		for _, id := range slices.Sorted(maps.Keys(f.resources)) {
			raw := f.resources[id]
			collection := strings.ToLower(fleetParent(id, text(raw["type"])) + "/" + last(text(raw["type"])))
			if raw["type"] == fleetType {
				collection = root + "/providers/" + strings.ToLower(fleetType)
			}
			if collection == path && !f.omitted[id] {
				values = append(values, raw)
			}
		}
		return jsonResponse(200, map[string]any{"value": values}, http.Header{"X-Ms-Request-Id": {"fleet-native-index"}}), nil
	})
	return f
}

func (f *fleetFixture) request(kind string) contracts.InventoryRequest {
	resourceKind := f.runtime.resourceKind(kind)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: fleetInventorySource, ResourceKind: &resourceKind, Scope: asset.Scope{Kind: asset.ScopeSubscription, NativeID: testSubscription}}
}

func TestFleetRegisteredInventoryKeepsNativeContextAndReferences(t *testing.T) {
	for _, kind := range fleetTestKinds {
		t.Run(last(kind), func(t *testing.T) {
			f := newFleetFixture(t)
			batch, err := f.runtime.List(t.Context(), f.request(kind))
			if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.RequestID != "fleet-native-index" {
				t.Fatal("native Fleet scan failed", batch, err)
			}
			item := batch.Items[0]
			location := "westus"
			if kind == fleetNamespaceType {
				location = "eastus"
			}
			actionable := kind != fleetType && kind != fleetGateType
			if item.Location != location || item.Scope.Kind != asset.ScopeRegion || item.Actionable == nil || *item.Actionable != actionable || item.Normalized["cleanup_protected"] != (kind == fleetType) || kind == fleetGateType && item.Normalized["cleanup_controller_only"] != true {
				t.Fatal("Fleet native context lost", item)
			}
			for _, field := range []string{fleetConfigurationProof, fleetContextProof, fleetReferencesProof} {
				if text(item.Normalized[field]) == "" {
					t.Fatal("missing private proof", field)
				}
			}
			if item.Normalized["_inventory_source"] != fleetInventorySource || object(item.Normalized["arm_parameters"])["fleetName"] != "fleet1" {
				t.Fatal("Fleet registered parent binding lost")
			}
			if f.calls["GET "+item.NativeID] < 2 {
				t.Fatal("Fleet scan did not independently reread each resource")
			}
			if kind != fleetType && !slices.Contains(item.NetworkReferences, strings.ToLower(resourceID(fleetType, "fleet1"))) {
				t.Fatal("native Fleet parent reference missing")
			}
			switch kind {
			case fleetMemberType:
				if !slices.Contains(item.NetworkReferences, strings.ToLower(resourceID(aksType, "cluster1"))) {
					t.Fatal("shared AKS reference lost")
				}
			case fleetRunType:
				if len(item.NetworkReferences) != 1 {
					t.Fatal("strategy snapshot provenance became a live dependency", item.NetworkReferences)
				}
			case fleetGateType:
				if !slices.Contains(item.NetworkReferences, strings.ToLower(resourceID(fleetType, "fleet1"))+"/updateruns/run1") {
					t.Fatal("Gate's lifecycle target lost")
				}
			case fleetNamespaceType:
				if item.Normalized["placement_dynamic"] != true {
					t.Fatal("dynamic namespace placement became an empty fixed set")
				}
			}
			payload, _ := json.Marshal(batch)
			if strings.Contains(string(payload), "fleet-private-configuration") || strings.Contains(string(payload), "futurePrivateSetting") {
				t.Fatal("private Fleet configuration persisted")
			}
			request := f.request(kind)
			request.Source = inventorySource
			if batch, err := f.runtime.List(t.Context(), request); err != nil || len(batch.Items) != 0 {
				t.Fatal("Fleet scanned twice through ARM", err)
			}
		})
	}
}

func TestFleetKnownIdentityReconciliationRequiresOwnAbsence(t *testing.T) {
	for _, mode := range []string{"child-omitted", "parent-omitted", "parent-and-child-gone", "parent-gone-child-live", "child-forbidden", "child-list-missing", "child-reappears"} {
		t.Run(mode, func(t *testing.T) {
			f := newFleetFixture(t)
			parent := strings.ToLower(resourceID(fleetType, "fleet1"))
			child := parent + "/members/member1"
			request := f.request(fleetMemberType)
			request.KnownNativeIDs = []string{child}
			f.omitted[child] = true
			if mode == "parent-omitted" {
				f.omitted[parent] = true
			}
			if strings.HasPrefix(mode, "parent-") && mode != "parent-omitted" {
				delete(f.resources, parent)
			}
			if mode == "parent-and-child-gone" {
				delete(f.resources, child)
			}
			f.override = func(req *http.Request) (*http.Response, bool) {
				path := strings.ToLower(req.URL.Path)
				if mode == "child-forbidden" && path == child {
					return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
				}
				if mode == "child-list-missing" && path == parent+"/members" {
					return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
				}
				if mode == "child-reappears" && path == child && f.calls["GET "+child] == 1 {
					return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
				}
				return nil, false
			}
			batch, err := f.runtime.List(t.Context(), request)
			want := slices.Contains([]string{"child-omitted", "parent-omitted", "parent-and-child-gone"}, mode)
			if (err == nil) != want {
				t.Fatal("Fleet reconciliation boundary changed", mode, batch, err)
			}
			if !want {
				if isNotFound(err) || len(batch.AbsentNativeIDs) != 0 {
					t.Fatal("incomplete Fleet observation became absence")
				}
				return
			}
			if mode == "parent-and-child-gone" {
				if len(batch.Items) != 0 || !slices.Equal(batch.AbsentNativeIDs, []string{child}) || f.calls["GET "+child] != 2 {
					t.Fatal("child was closed through parent absence", batch, f.calls)
				}
			} else if len(batch.Items) != 1 || len(batch.AbsentNativeIDs) != 0 {
				t.Fatal("omitted live child disappeared", batch)
			}
		})
	}
}

func TestFleetInventoryCursorAndTwoPassDrift(t *testing.T) {
	for _, mode := range []string{"stable", "private", "parent", "group", "lock", "request", "credential", "during-scan"} {
		t.Run(mode, func(t *testing.T) {
			f := newFleetFixture(t)
			parent := strings.ToLower(resourceID(fleetType, "fleet1"))
			second := fleetTestBody(t, fleetMemberType, "member2")
			f.resources[text(second["id"])] = second
			request := f.request(fleetMemberType)
			request.Limit = 1
			if mode == "during-scan" {
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, parent) && f.calls["GET "+parent] == 2 {
						object(f.resources[parent]["properties"])["futureField"] = "modified"
					}
					return nil, false
				}
			}
			first, err := f.runtime.List(t.Context(), request)
			if mode == "during-scan" {
				if err == nil {
					t.Fatal("two different snapshots accepted")
				}
				return
			}
			if err != nil || first.Complete || first.NextCursor == "" || len(first.Items) != 1 {
				t.Fatal("Fleet pagination failed", first, err)
			}
			request.Cursor, request.Limit = first.NextCursor, 10
			switch mode {
			case "private":
				object(second["properties"])["annotations"] = map[string]any{"opaque": "changed"}
			case "parent":
				object(f.resources[parent]["properties"])["futureField"] = "changed"
			case "group":
				f.group["tags"] = map[string]any{"changed": "yes"}
			case "lock":
				f.locks = []any{map[string]any{"id": parent + "/providers/Microsoft.Authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "request":
				request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westus"}
			case "credential":
				f.runtime.credentials = credentialFunc(func(context.Context, asset.ConnectionID) (contracts.Credential, error) {
					credential := testCredential()
					credential.Values["client_secret"] = "rotated-secret"
					return credential, nil
				})
			}
			last, err := f.runtime.List(t.Context(), request)
			if mode == "stable" {
				if err != nil || !last.Complete || len(last.Items) != 1 || last.Items[0].NativeID == first.Items[0].NativeID {
					t.Fatal("Fleet resume failed", last, err)
				}
			} else if err == nil {
				t.Fatal("changed Fleet cursor accepted", mode)
			}
		})
	}
}

func TestFleetPrivatePayloadsStayOutOfInvocationsAndLogs(t *testing.T) {
	f := newFleetFixture(t)
	var entries []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { entries = append(entries, entry) }))
	for _, kind := range fleetTestKinds {
		for id, raw := range f.resources {
			if raw["type"] != kind {
				continue
			}
			row := fleetKind(kind)
			parameters := map[string]any{"resourceGroupName": "test", "fleetName": "fleet1"}
			if kind != fleetType {
				parameters[row.selector] = last(id)
			}
			result, err := f.runtime.Invoke(ctx, contracts.Invocation{ConnectionID: "connection", Operation: "Azure.Microsoft.ContainerService." + row.prefix + "_Get", Parameters: parameters})
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(result)
			if strings.Contains(string(payload), "fleet-private-configuration") {
				t.Fatal("Invoke leaked Fleet authored configuration")
			}
		}
	}
	payload, _ := json.Marshal(entries)
	if len(entries) == 0 || strings.Contains(string(payload), "fleet-private-configuration") || strings.Contains(string(payload), "futurePrivateSetting") {
		t.Fatal("Fleet diagnostic boundary leaked", string(payload))
	}
}

func TestFleetInventoryRejectsForeignSourcesAndKnownIDs(t *testing.T) {
	for _, mode := range []string{"foreign-source", "missing-kind", "wrong-kind", "foreign-id", "wrong-id-kind", "duplicate-id", "padded-id", "metadata", "options", "foreign-scope", "malformed-cursor"} {
		t.Run(mode, func(t *testing.T) {
			f := newFleetFixture(t)
			request := f.request(fleetMemberType)
			id := strings.ToLower(resourceID(fleetType, "fleet1")) + "/members/member1"
			request.KnownNativeIDs = []string{id}
			switch mode {
			case "foreign-source":
				request.Source = productInventorySource
			case "missing-kind":
				request.ResourceKind = nil
			case "wrong-kind":
				kind := f.runtime.resourceKind(aksType)
				request.ResourceKind = &kind
			case "foreign-id":
				request.KnownNativeIDs[0] = strings.Replace(id, testSubscription, rbacOtherSubscription, 1)
			case "wrong-id-kind":
				request.KnownNativeIDs[0] = strings.Replace(id, "/members/", "/updateruns/", 1)
			case "duplicate-id":
				request.KnownNativeIDs = append(request.KnownNativeIDs, id)
			case "padded-id":
				request.KnownNativeIDs[0] += " "
			case "metadata":
				request.KnownNativeMetadata = map[string]map[string]any{id + "-other": {"location": "westus"}}
			case "options":
				request.Options = map[string]any{"$filter": "one-group"}
			case "foreign-scope":
				request.Scope.NativeID = rbacOtherSubscription
			case "malformed-cursor":
				request.Cursor = "invalid"
			}
			if _, err := f.runtime.List(t.Context(), request); err == nil || len(f.calls) != 0 {
				t.Fatal("invalid Fleet scan reached resource APIs", err, f.calls)
			}
		})
	}
}

func TestFleetGenericARMObservationUsesNativeMetadata(t *testing.T) {
	for _, kind := range fleetTestKinds {
		t.Run(last(kind), func(t *testing.T) {
			f := newFleetFixture(t)
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			for _, raw := range f.resources {
				if raw["type"] != kind {
					continue
				}
				current, err := c.fleetRead(t.Context(), kind, text(raw["id"]))
				if err != nil {
					t.Fatal(err)
				}
				raw = current.data // Exercise the same lossless JSON numbers as the ARM walker.
				listed := maps.Clone(raw)
				if kind != fleetType && kind != fleetNamespaceType {
					listed["location"] = "westus"
				}
				owners := map[string]string{strings.ToLower(text(f.group["id"])): ""}
				item, err := f.runtime.inventoryItem(t.Context(), c, listed, owners, nil)
				if err != nil || text(item.Normalized[fleetConfigurationProof]) == "" {
					t.Fatal("ARM observation lost Fleet proof", err)
				}
				listed["properties"] = maps.Clone(object(raw["properties"]))
				object(listed["properties"])["futurePrivateSetting"] = "changed"
				if _, err := f.runtime.inventoryItem(t.Context(), c, listed, owners, nil); err == nil {
					t.Fatal("stale ARM Fleet snapshot accepted")
				}
			}
		})
	}
}
