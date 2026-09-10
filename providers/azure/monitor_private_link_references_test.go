package azure

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func monitorPrivateLinkTargetScenario(t *testing.T, kind string) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s, r, values := monitorPrivateLinkScenario(t)
	raw := nativeResource(kind, "linked-target", "eastus", map[string]any{"provisioningState": "Succeeded", "customerId": "00000000-1111-2222-3333-444444444444"})
	if kind == dataCollectionEndpointType {
		raw = object(object(object(dataCollectionFixture(t, "DataCollectionEndpointsGet")["responses"])["200"])["body"])
		raw["id"], raw["type"], raw["name"] = resourceID(kind, "linked-target"), kind, "linked-target"
	}
	object(raw["properties"])["privateLinkScopedResources"] = []any{map[string]any{"resourceId": values[1].Identity.NativeID, "scopeId": "opaque-native-immutable-id"}}
	metadata, _ := findType(kind)
	s.add(raw, metadata.Version)
	s.lists[strings.ToLower(text(raw["id"])+"/associations")] = []any{}
	object(s.records[values[1].Identity.NativeID]["properties"])["linkedResourceId"] = text(raw["id"])
	batch, err := r.List(context.Background(), productRequest(r, monitorScopedResourceType))
	if err != nil || len(batch.Items) != 1 {
		t.Fatalf("rebind native association: %+v %v", batch, err)
	}
	values[1].Normalized = batch.Items[0].Normalized
	return s, r, []asset.Asset{dnsAsset(t, r, raw), values[1]}
}

func TestMonitorPrivateLinkTargetUnlinkLifecycle(t *testing.T) {
	for _, kind := range []string{"Microsoft.OperationalInsights/workspaces", dataCollectionEndpointType} {
		t.Run(kind, func(t *testing.T) {
			s, r, values := monitorPrivateLinkTargetScenario(t, kind)
			request, input := dnsRequest(t, r, values, values[0])
			result, err := plan.Solve(input)
			if err != nil || len(result.Blockers) != 0 || len(result.Steps) != 2 || len(request.PrerequisiteDeletions) != 1 || len(input.LifecycleBindings) != 0 {
				t.Fatalf("shared association became owned or scope selected: %+v %+v %v", result, input.LifecycleBindings, err)
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", values[0])
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("linked target was deleted before unlinking")
			}
			child, _ := r.ResolveAction(context.Background(), "connection", values[1])
			if _, err := child.Execute(context.Background(), contracts.ActionRequest{Action: "delete", Asset: values[1]}); err != nil {
				t.Fatal(err)
			}
			// The association DELETE and target's read-only reverse index converge
			// separately. A stale backlink must still block target deletion.
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 1 {
				t.Fatal("target accepted a surviving reverse reference")
			}
			object(s.records[values[0].Identity.NativeID]["properties"])["privateLinkScopedResources"] = []any{}
			if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != 2 || s.gone[redisParentID(values[1].Identity.NativeID)] {
				t.Fatalf("unlinked target cleanup changed its scope: %v %v", s.deletes, err)
			}
			if read, err := driver.Readback(context.Background(), request); err != nil || read.Exists {
				t.Fatalf("target final GET absence: %+v %v", read, err)
			}
		})
	}
}

func TestMonitorPrivateLinkIncomingIndexesAndMissingAssets(t *testing.T) {
	for _, mode := range []string{"missing-asset", "foreign-subscription", "reverse-omitted", "reverse-empty", "reverse-duplicate", "reverse-casing", "scope-id-as-resource", "list-denied", "list-partial", "scope-omitted", "changed-scope-list", "changed-association-list", "changed-target", "unreviewed-association"} {
		t.Run(mode, func(t *testing.T) {
			s, r, values := monitorPrivateLinkTargetScenario(t, "Microsoft.OperationalInsights/workspaces")
			c, _ := r.resolve(context.Background(), "connection")
			target, link := values[0], values[1]
			raw := s.records[target.Identity.NativeID]
			props := object(raw["properties"])
			root := redisParentID(link.Identity.NativeID)
			index := "/subscriptions/" + testSubscription + "/providers/microsoft.insights/privatelinkscopes"
			switch mode {
			case "missing-asset":
				values = values[:1]
			case "foreign-subscription":
				id := strings.Replace(link.Identity.NativeID, testSubscription, "00000000-1111-2222-3333-444444444444", 1)
				props["privateLinkScopedResources"] = append(array(props["privateLinkScopedResources"]), map[string]any{"resourceId": id, "scopeId": "foreign"})
			case "reverse-omitted":
				delete(props, "privateLinkScopedResources")
			case "reverse-empty":
				props["privateLinkScopedResources"] = []any{}
			case "reverse-duplicate":
				props["privateLinkScopedResources"] = append(array(props["privateLinkScopedResources"]), array(props["privateLinkScopedResources"])[0])
			case "reverse-casing":
				props["PrivateLinkScopedResources"] = props["privateLinkScopedResources"]
			case "scope-id-as-resource":
				object(array(props["privateLinkScopedResources"])[0])["resourceId"] = root
			case "list-denied":
				s.status[index] = 403
			case "list-partial":
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, index) {
						return jsonResponse(200, map[string]any{}, nil), true
					}
					return nil, false
				}
			case "scope-omitted":
				s.lists[index] = []any{}
			case "changed-scope-list", "changed-association-list", "changed-target":
				count := 0
				path := index
				if mode == "changed-association-list" {
					path = root + "/scopedresources"
				} else if mode == "changed-target" {
					path = target.Identity.NativeID
				}
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, path) {
						count++
						if count == 2 {
							if mode == "changed-target" {
								props["privateLinkScopedResources"] = []any{}
							} else {
								return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
							}
						}
					}
					return nil, false
				}
			case "unreviewed-association":
				values[1].Normalized["linkedResourceId"] = resourceID(dataCollectionEndpointType, "wrong")
			}
			contribution := governance.Contribution{}
			err := (&serviceCascades{client: c}).contributeMonitorPrivateLinkReferences(context.Background(), values, &contribution)
			switch mode {
			case "missing-asset", "foreign-subscription":
				if err != nil || len(contribution.Unresolved) != 1 || contribution.Unresolved[0].NativeType != monitorScopedResourceType {
					t.Fatalf("lost unresolved association: %+v %v", contribution, err)
				}
			case "reverse-omitted":
				if err != nil || len(contribution.Relationships) != 1 || len(contribution.Bindings) != 0 {
					t.Fatalf("omitted optional backlink hid native association: %+v %v", contribution, err)
				}
			default:
				if err == nil {
					t.Fatalf("accepted inconsistent incoming index %s: %+v", mode, contribution)
				}
			}
			if len(s.deletes) != 0 {
				t.Fatal("reference discovery wrote to Azure")
			}
		})
	}
}

func TestMonitorPrivateLinkManagedMonitorWorkspace(t *testing.T) {
	s, r, values := monitorWorkspaceScenario(t)
	scope, _, scopeValues := monitorPrivateLinkScenario(t)
	for id, raw := range scope.records {
		s.records[id], s.version[id] = raw, scope.version[id]
	}
	for id, rows := range scope.lists {
		if strings.Contains(id, "privatelinkscopes") {
			s.lists[id], s.version[id] = rows, scope.version[id]
		}
	}
	associationID := scopeValues[1].Identity.NativeID
	object(s.records[associationID]["properties"])["linkedResourceId"] = values[3].Identity.NativeID
	object(s.records[values[3].Identity.NativeID]["properties"])["privateLinkScopedResources"] = []any{map[string]any{"resourceId": associationID, "scopeId": "native-scope-id"}}
	batch, err := r.List(context.Background(), productRequest(r, monitorScopedResourceType))
	if err != nil || len(batch.Items) != 1 {
		t.Fatalf("managed DCE association inventory: %+v %v", batch, err)
	}
	association := scopeValues[1]
	association.Normalized = batch.Items[0].Normalized
	values = append(values, association)
	request, input := dnsRequest(t, r, values, values[0])
	result, err := plan.Solve(input)
	if err != nil || len(result.Steps) != 3 || len(request.LifecycleImpacts) != 4 || len(request.PrerequisiteDeletions) != 2 {
		t.Fatalf("managed DCE lost external AMPLS prerequisite: %+v %+v %v", result, request, err)
	}
	for _, binding := range input.LifecycleBindings {
		if binding.ManagedAssetID == association.ID {
			t.Fatal("AMPLS association became owned by a monitored workspace")
		}
	}
	driver, _ := r.ResolveAction(context.Background(), "connection", values[0])
	for _, i := range []int{5, 6} {
		if _, err := driver.Execute(context.Background(), request); err == nil {
			t.Fatal("workspace ignored a surviving external association")
		}
		child, _ := r.ResolveAction(context.Background(), "connection", values[i])
		if _, err := child.Execute(context.Background(), servicePlanRequest(result, values, values[i])); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 2 {
		t.Fatal("workspace ignored the managed DCE's stale AMPLS backlink")
	}
	object(s.records[values[3].Identity.NativeID]["properties"])["privateLinkScopedResources"] = []any{}
	if _, err := driver.Execute(context.Background(), request); err != nil || len(s.deletes) != 3 || s.deletes[2] != values[0].Identity.NativeID || s.gone[scopeValues[0].Identity.NativeID] {
		t.Fatalf("managed workspace cleanup did not preserve external scope: %v %v", s.deletes, err)
	}
}
