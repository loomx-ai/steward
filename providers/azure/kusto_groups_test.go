package azure

import (
	"context"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestKustoGroupCannotOwnExternalFollower(t *testing.T) {
	s, r, assets := kustoScenario(t)
	if aksExternalRelation(assets[0], assets[3]) || aksExternalRelation(assets[2], assets[3]) {
		t.Fatal("external attachment was group-owned")
	}
	group := strings.Join(strings.Split(assets[0].Identity.NativeID, "/")[:5], "/")
	foreign := strings.Replace(assets[3].Identity.NativeID, "/resourcegroups/testgroup/", "/resourcegroups/foreign/", 1)
	raw := s.records[assets[3].Identity.NativeID]
	raw["id"] = foreign
	s.add(raw, kustoVersion)
	object(object(s.lists[assets[0].Identity.NativeID+"/listfollowerdatabases"][0])["properties"])["clusterResourceId"] = kustoClusterID(foreign)
	s.lists[group+"/resources"] = []any{s.records[assets[0].Identity.NativeID]}
	c, _ := r.resolve(context.Background(), "connection")
	controller := "/subscriptions/" + testSubscription + "/resourcegroups/management/providers/Microsoft.ContainerService/managedClusters/owner"
	if _, err := c.managedGroupResources(context.Background(), controller, group); err == nil {
		t.Fatal("group DELETE claimed an external detach")
	}
}

func TestKustoManagedGroupPreservesLinkedTargetProtection(t *testing.T) {
	for _, mode := range []string{"normal", "target-protected", "target-lock", "target-unreadable", "target-private", "pending"} {
		t.Run(mode, func(t *testing.T) {
			s, r, original := kustoScenario(t)
			root, mpe := original[0], original[8]
			groupID := strings.Join(strings.Split(root.Identity.NativeID, "/")[:5], "/")
			controllerID := "/subscriptions/" + testSubscription + "/resourcegroups/management/providers/Microsoft.ContainerService/managedClusters/owner"
			controller := map[string]any{"id": controllerID, "name": "owner", "type": aksType, "location": "westus", "properties": map[string]any{"nodeResourceGroup": "testgroup", "resourceUID": "owner-incarnation"}}
			group := map[string]any{"id": groupID, "type": groupType, "managedBy": controllerID}
			s.add(controller, "2024-02-01")
			s.add(group, resourcesVersion)
			object(s.records[root.Identity.NativeID]["properties"])["privateEndpointConnections"] = []any{}
			for _, kind := range kustoOwnedKinds(kustoType) {
				s.lists[root.Identity.NativeID+"/"+strings.ToLower(last(kind))] = []any{}
			}
			s.lists[root.Identity.NativeID+"/managedprivateendpoints"] = []any{s.records[mpe.Identity.NativeID]}
			s.lists[root.Identity.NativeID+"/listfollowerdatabases"] = []any{}
			s.lists[groupID+"/resources"] = []any{s.records[root.Identity.NativeID]}
			old, _ := kustoEndpointTarget(s.records[mpe.Identity.NativeID])
			target := strings.Replace(old, "/resourcegroups/testgroup/", "/resourcegroups/shared/", 1)
			raw := s.records[old]
			raw["id"] = target
			s.add(raw, s.version[old])
			delete(s.records, old)
			object(s.records[mpe.Identity.NativeID]["properties"])["privateLinkResourceId"] = target
			s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{group, map[string]any{"id": "/subscriptions/" + testSubscription + "/resourcegroups/shared", "type": groupType}, map[string]any{"id": "/subscriptions/" + testSubscription + "/resourcegroups/management", "type": groupType}}
			assets := []asset.Asset{dnsAsset(t, r, controller), dnsAsset(t, r, group), dnsAsset(t, r, s.records[root.Identity.NativeID]), dnsAsset(t, r, s.records[mpe.Identity.NativeID])}
			request, _ := nestedAKSRequest(t, r, assets)
			if len(request.LifecycleImpacts) != 3 {
				t.Fatal("wrong group membership", len(request.LifecycleImpacts))
			}
			switch mode {
			case "target-protected":
				raw["tags"] = map[string]any{"steward:protected": "true"}
			case "target-lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": target + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "target-unreadable":
				s.status[target] = 403
			case "target-private":
				object(raw["properties"])["password"] = "rotated-secret"
			case "pending":
				object(s.records[root.Identity.NativeID]["properties"])["state"] = "Updating"
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", assets[0])
			result, err := driver.Execute(context.Background(), request)
			if mode != "normal" {
				if err == nil || len(s.deletes) != 0 {
					t.Fatal("group cleanup bypassed Kusto protection", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			for _, impact := range request.LifecycleImpacts {
				s.gone[impact.Asset.Identity.NativeID] = true
			}
			if wait, err := driver.Wait(context.Background(), request, result); err != nil || !wait.Done {
				t.Fatal("group completion", wait, err)
			}
			if s.gone[target] {
				t.Fatal("group deleted external data target")
			}
		})
	}
}
