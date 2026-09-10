package azure

import (
	"context"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
)

func TestStreamAnalyticsManagedGroupCannotOwnExternalJob(t *testing.T) {
	s, r, assets := streamAnalyticsScenario(t, true)
	cluster, job := cdnAsset(t, assets, streamAnalyticsClusterType), cdnAsset(t, assets, streamAnalyticsJobType)
	if aksExternalRelation(cluster, job) {
		t.Fatal("group falsely owns an independent associated job")
	}
	group := strings.Join(strings.Split(cluster.Identity.NativeID, "/")[:5], "/")
	s.lists[group+"/resources"] = []any{s.records[cluster.Identity.NativeID]}
	c, _ := r.resolve(context.Background(), "connection")
	controller := "/subscriptions/" + testSubscription + "/resourcegroups/management/providers/Microsoft.ContainerService/managedClusters/owner"
	if _, err := c.managedGroupResources(context.Background(), controller, group); err == nil {
		t.Fatal("group deletion absorbed an external independent job")
	}
}

func TestStreamAnalyticsManagedGroupChecksExternalTarget(t *testing.T) {
	for _, mode := range []string{"normal", "target-protected", "target-lock", "target-unreadable", "target-private", "cluster-pending"} {
		t.Run(mode, func(t *testing.T) {
			s, r, original := streamAnalyticsScenario(t, false)
			cluster, endpoint := cdnAsset(t, original, streamAnalyticsClusterType), cdnAsset(t, original, streamAnalyticsEndpointType)
			groupID := strings.Join(strings.Split(cluster.Identity.NativeID, "/")[:5], "/")
			controllerID := "/subscriptions/" + testSubscription + "/resourcegroups/management/providers/Microsoft.ContainerService/managedClusters/owner"
			controller := map[string]any{"id": controllerID, "name": "owner", "type": aksType, "location": "westus", "properties": map[string]any{"nodeResourceGroup": "cluster-rg", "resourceUID": "owner-incarnation"}}
			group := map[string]any{"id": groupID, "type": groupType, "managedBy": controllerID}
			s.add(controller, "2024-02-01")
			s.add(group, resourcesVersion)
			s.lists[groupID+"/resources"] = []any{s.records[cluster.Identity.NativeID]}
			targets, _ := streamAnalyticsEndpointTargets(s.records[endpoint.Identity.NativeID])
			targetID := targets[0]
			storage := s.records[targetID]
			s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{group, map[string]any{"id": "/subscriptions/" + testSubscription + "/resourcegroups/data-rg", "type": groupType}, map[string]any{"id": "/subscriptions/" + testSubscription + "/resourcegroups/management", "type": groupType}}
			assets := []asset.Asset{dnsAsset(t, r, controller), dnsAsset(t, r, group), dnsAsset(t, r, s.records[cluster.Identity.NativeID]), dnsAsset(t, r, s.records[endpoint.Identity.NativeID])}
			request, _ := nestedAKSRequest(t, r, assets)
			if len(request.LifecycleImpacts) != 3 {
				t.Fatal("wrong group membership", len(request.LifecycleImpacts))
			}
			switch mode {
			case "target-protected":
				storage["tags"] = map[string]any{"steward:protected": "true"}
			case "target-lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": targetID + "/providers/Microsoft.Authorization/locks/keep", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "target-unreadable":
				s.status[targetID] = 403
			case "target-private":
				object(storage["properties"])["password"] = "rotated-secret"
			case "cluster-pending":
				object(s.records[cluster.Identity.NativeID]["properties"])["provisioningState"] = "Updating"
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", assets[0])
			result, err := driver.Execute(context.Background(), request)
			if mode != "normal" {
				if err == nil || len(s.deletes) != 0 {
					t.Fatal("managed group bypassed Stream Analytics target checks", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if waited, err := driver.Wait(context.Background(), request, result); err != nil || waited.Done {
				t.Fatal("managed group skipped owned-resource absence", waited, err)
			}
			for _, impact := range request.LifecycleImpacts {
				s.gone[impact.Asset.Identity.NativeID] = true
			}
			if waited, err := driver.Wait(context.Background(), request, result); err != nil || !waited.Done {
				t.Fatal("managed group never completed", waited, err)
			}
			if s.gone[targetID] {
				t.Fatal("managed group deleted external data storage")
			}
		})
	}
}
