package azure

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// These subscription collections and API versions are the native ARM
// contracts of the pinned Event Grid, Desktop Virtualization and HDInsight
// specifications.
func TestCoverageKindsListNativeSubscriptionCollections(t *testing.T) {
	for nativeType, version := range map[string]string{
		eventGridTopicType:               "2025-02-15",
		eventGridSystemTopicType:         "2025-02-15",
		"Microsoft.EventGrid/namespaces": "2025-02-15",
		avdHostPoolType:                  "2024-04-03",
		avdApplicationGroupType:          "2024-04-03",
		avdWorkspaceType:                 "2024-04-03",
		hdinsightClusterType:             "2021-06-01",
	} {
		t.Run(nativeType, func(t *testing.T) {
			root := "/subscriptions/" + testSubscription
			collection := root + "/providers/" + strings.ToLower(nativeType)
			item := nativeResource(nativeType, "sample", "eastus", map[string]any{"provisioningState": "Succeeded"})
			listed := false
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				path := strings.ToLower(req.URL.Path)
				switch {
				case req.Method != "GET":
					t.Fatalf("mutation during inventory %s %s", req.Method, req.URL)
				case path == collection:
					if req.URL.Query().Get("api-version") != version {
						t.Fatalf("wrong API version %s", req.URL)
					}
					listed = true
					return jsonResponse(200, map[string]any{"value": []any{item}}, nil), nil
				case path == strings.ToLower(text(item["id"])):
					return jsonResponse(200, item, nil), nil
				case path == root+"/resourcegroups" || path == root+"/providers/microsoft.authorization/locks":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				t.Fatalf("unexpected request %s", req.URL)
				return nil, nil
			})
			batch, err := r.List(context.Background(), productRequest(r, nativeType))
			if err != nil || !listed || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].NativeID != strings.ToLower(text(item["id"])) {
				t.Fatalf("batch=%+v listed=%v err=%v", batch, listed, err)
			}
		})
	}
}

// Built-in Azure Monitor tables belong to the workspace and are not listed;
// custom tables are, and are deletable.
func TestLogAnalyticsTablesListOnlyUserTables(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	workspace := nativeResource("Microsoft.OperationalInsights/workspaces", "logs", "eastus", map[string]any{"provisioningState": "Succeeded", "customerId": "0d6b6c3a-7d1e-4b5f-9a3c-2f1e8d7c6b5a"})
	table := func(name, creator string) map[string]any {
		return map[string]any{"id": text(workspace["id"]) + "/tables/" + name, "name": name, "properties": map[string]any{"provisioningState": "Succeeded", "schema": map[string]any{"name": name, "tableType": creator}}}
	}
	custom, builtin := table("Orders_CL", "CustomLog"), table("Heartbeat", "Microsoft")
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		switch path := strings.ToLower(req.URL.Path); path {
		case root + "/providers/microsoft.operationalinsights/workspaces":
			return jsonResponse(200, map[string]any{"value": []any{workspace}}, nil), nil
		case strings.ToLower(text(workspace["id"])):
			return jsonResponse(200, workspace, nil), nil
		case strings.ToLower(text(workspace["id"]) + "/tables"):
			return jsonResponse(200, map[string]any{"value": []any{custom, builtin}}, nil), nil
		case strings.ToLower(text(custom["id"])):
			return jsonResponse(200, custom, nil), nil
		case root + "/resourcegroups", root + "/providers/microsoft.authorization/locks":
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		t.Fatalf("unexpected request %s", req.URL)
		return nil, nil
	})
	batch, err := r.List(context.Background(), productRequest(r, logAnalyticsTableType))
	if err != nil || len(batch.Items) != 1 || batch.Items[0].NativeID != strings.ToLower(text(custom["id"])) {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	kind, _ := findType(logAnalyticsTableType)
	if reason := protectionReason(kind, builtin); reason != "azure_log_analytics_builtin_table" {
		t.Fatalf("built-in table reason = %q", reason)
	}
	if reason := protectionReason(kind, custom); reason != "" {
		t.Fatalf("custom table reason = %q", reason)
	}
}

// A host pool with application groups cannot be deleted; the groups are
// selected as prerequisites of the host pool that their hostPoolArmPath names.
func TestDesktopVirtualizationApplicationGroupsPrecedeTheirHostPool(t *testing.T) {
	pool := actionAsset(avdHostPoolType, "pool")
	group := actionAsset(avdApplicationGroupType, "desktops")
	group.Normalized = map[string]any{"properties": map[string]any{"hostPoolArmPath": resourceID(avdHostPoolType, "pool")}}
	other := actionAsset(avdApplicationGroupType, "elsewhere")
	other.Normalized = map[string]any{"properties": map[string]any{"hostPoolArmPath": resourceID(avdHostPoolType, "other")}}
	result := governance.Contribution{}
	contributeDesktopVirtualization([]asset.Asset{pool, group, other}, &result)
	if len(result.Relationships) != 1 {
		t.Fatalf("relationships = %+v", result.Relationships)
	}
	relationship := result.Relationships[0]
	if relationship.SourceAssetID != pool.ID || relationship.TargetAssetID != group.ID || relationship.Type != graph.RelationshipDependsOn ||
		relationship.Evidence[graph.RelationshipEvidenceRequiredDeletion] != true || relationship.Evidence[graph.RelationshipEvidenceDeletionOrder] != graph.DeletionOrderTargetBeforeSource {
		t.Fatalf("relationship = %+v", relationship)
	}
	if !servicePrerequisiteKind(avdHostPoolType, avdSessionHostType) || !HasServiceCascade(eventGridTopicType) || !HasServiceCascade(eventGridSystemTopicType) {
		t.Fatal("host pool prerequisites or Event Grid cascades are not registered")
	}
}

func TestCoverageKindsDeleteWithOperationAndFinalAbsence(t *testing.T) {
	for _, kind := range []string{hdinsightClusterType, eventGridTopicType, avdWorkspaceType} {
		t.Run(kind, func(t *testing.T) {
			value := actionAsset(kind, "sample")
			id := value.Identity.NativeID
			operation := apiURL("/subscriptions/"+testSubscription+"/providers/"+strings.Split(kind, "/")[0]+"/locations/eastus/operationResults/op-1", "2025-01-01")
			deleted, deletes := false, 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if response, handled := emptyMonitorIndexResponse(t, req); handled {
					return response, nil
				}
				if response, handled := emptyDiagnosticSourceIndexResponse(t, req); handled {
					return response, nil
				}
				if strings.EqualFold(req.URL.String(), operation) {
					deleted = true
					return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				switch path := strings.ToLower(req.URL.Path); path {
				case id + "/eventsubscriptions":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				case id:
					if req.Method == "DELETE" {
						deletes++
						return jsonResponse(202, map[string]any{}, http.Header{"Location": {operation}, "X-Ms-Request-Id": {"delete-request"}, "Retry-After": {"1"}}), nil
					}
					if deleted {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
					}
					raw := nativeResource(kind, "sample", "eastus", map[string]any{"provisioningState": "Succeeded"})
					raw["id"] = id
					return jsonResponse(200, raw, nil), nil
				case "/subscriptions/" + testSubscription + "/resourcegroups/test":
					return jsonResponse(200, map[string]any{"id": path}, nil), nil
				case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				t.Fatalf("unexpected %s %s", req.Method, req.URL)
				return nil, nil
			})
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "delete-key"}
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			done := false
			for i := 0; i < 5 && !done; i++ {
				wait, err := driver.Wait(context.Background(), request, result)
				if err != nil {
					t.Fatal(err)
				}
				done = wait.Done
			}
			readback, err := driver.Readback(context.Background(), request)
			if !done || err != nil || readback.Exists || deletes != 1 {
				t.Fatalf("done=%v readback=%+v deletes=%d err=%v", done, readback, deletes, err)
			}
		})
	}
}

// Backend pools are listed through their load balancer; a pool that rules or
// interfaces use is protected, and interface references still point at the
// load balancer.
func TestLoadBalancerBackendPoolsAreListedAndProtectedWhileInUse(t *testing.T) {
	root := "/subscriptions/" + testSubscription
	lb := nativeResource(lbType, "web", "eastus", map[string]any{"provisioningState": "Succeeded"})
	pool := func(name string, props map[string]any) map[string]any {
		props["provisioningState"] = "Succeeded"
		return map[string]any{"id": text(lb["id"]) + "/backendAddressPools/" + name, "name": name, "properties": props}
	}
	idle := pool("idle", map[string]any{})
	used := pool("used", map[string]any{"loadBalancingRules": []any{map[string]any{"id": text(lb["id"]) + "/loadBalancingRules/http"}}})
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		switch path := strings.ToLower(req.URL.Path); path {
		case root + "/providers/microsoft.network/loadbalancers":
			return jsonResponse(200, map[string]any{"value": []any{lb}}, nil), nil
		case strings.ToLower(text(lb["id"])):
			return jsonResponse(200, lb, nil), nil
		case strings.ToLower(text(lb["id"]) + "/backendAddressPools"):
			return jsonResponse(200, map[string]any{"value": []any{idle, used}}, nil), nil
		case strings.ToLower(text(idle["id"])):
			return jsonResponse(200, idle, nil), nil
		case strings.ToLower(text(used["id"])):
			return jsonResponse(200, used, nil), nil
		case root + "/resourcegroups", root + "/providers/microsoft.authorization/locks":
			return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
		}
		t.Fatalf("unexpected request %s", req.URL)
		return nil, nil
	})
	batch, err := r.List(context.Background(), productRequest(r, lbBackendPoolType))
	if err != nil || len(batch.Items) != 2 {
		t.Fatalf("batch=%+v err=%v", batch, err)
	}
	kind, _ := findType(lbBackendPoolType)
	if protectionReason(kind, idle) != "" || protectionReason(kind, used) != "azure_lb_backend_pool_in_use" {
		t.Fatal("backend pool use is not protected")
	}
	nic := references(nicType, resourceID(nicType, "nic"), map[string]any{"properties": map[string]any{"ipConfigurations": []any{map[string]any{"properties": map[string]any{"loadBalancerBackendAddressPools": []any{map[string]any{"id": text(idle["id"])}}}}}}})
	if len(nic[lbType]) != 1 || len(nic[lbBackendPoolType]) != 0 {
		t.Fatalf("interface references = %v", nic)
	}
	if !HasServiceCascade(lbType) {
		t.Fatal("load balancer backend pool cascade is not registered")
	}
}

// Subscription collections and API versions of the pinned batch E
// specifications.
func TestCommonProductKindsListNativeSubscriptionCollections(t *testing.T) {
	for nativeType, version := range map[string]string{
		"Microsoft.Logic/workflows":                      "2019-05-01",
		"Microsoft.Automation/automationAccounts":        "2024-10-23",
		"Microsoft.Devices/IotHubs":                      "2023-06-30",
		"Microsoft.SignalRService/signalR":               "2024-03-01",
		"Microsoft.SignalRService/webPubSub":             "2024-03-01",
		"Microsoft.AppConfiguration/configurationStores": "2024-06-01",
		"Microsoft.Web/staticSites":                      "2025-05-01",
		dnsResolverType:                                  "2025-05-01",
		dnsForwardingRulesetType:                         "2025-05-01",
		relayNamespaceType:                               "2026-01-01",
		notificationNamespaceType:                        "2023-09-01",
		"Microsoft.Databricks/workspaces":                "2026-01-01",
	} {
		t.Run(nativeType, func(t *testing.T) {
			root := "/subscriptions/" + testSubscription
			collection := root + "/providers/" + strings.ToLower(nativeType)
			item := nativeResource(nativeType, "sample", "eastus", map[string]any{"provisioningState": "Succeeded"})
			listed := false
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				path := strings.ToLower(req.URL.Path)
				switch {
				case req.Method != "GET":
					t.Fatalf("mutation during inventory %s %s", req.Method, req.URL)
				case path == collection:
					if req.URL.Query().Get("api-version") != version {
						t.Fatalf("wrong API version %s", req.URL)
					}
					listed = true
					return jsonResponse(200, map[string]any{"value": []any{item}}, nil), nil
				case path == strings.ToLower(text(item["id"])):
					return jsonResponse(200, item, nil), nil
				case path == root+"/resourcegroups" || path == root+"/providers/microsoft.authorization/locks":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				t.Fatalf("unexpected request %s", req.URL)
				return nil, nil
			})
			batch, err := r.List(context.Background(), productRequest(r, nativeType))
			if err != nil || !listed || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].NativeID != strings.ToLower(text(item["id"])) {
				t.Fatalf("batch=%+v listed=%v err=%v", batch, listed, err)
			}
			if deletable := batch.Items[0].Actionable != nil && *batch.Items[0].Actionable; deletable == (nativeType == "Microsoft.Databricks/workspaces") {
				t.Fatalf("actionable = %v", deletable)
			}
		})
	}
}

func TestCommonProductKindsDeleteWithOperationAndFinalAbsence(t *testing.T) {
	for _, kind := range []string{"Microsoft.Logic/workflows", "Microsoft.Automation/automationAccounts", "Microsoft.Devices/IotHubs", "Microsoft.SignalRService/signalR", "Microsoft.SignalRService/webPubSub", "Microsoft.AppConfiguration/configurationStores", "Microsoft.Web/staticSites", dnsResolverType, dnsForwardingRulesetType, relayNamespaceType, notificationNamespaceType} {
		t.Run(kind, func(t *testing.T) {
			value := actionAsset(kind, "sample")
			id := value.Identity.NativeID
			operation := apiURL("/subscriptions/"+testSubscription+"/providers/"+strings.Split(kind, "/")[0]+"/locations/eastus/operationResults/op-1", "2025-01-01")
			deleted, deletes := false, 0
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if response, handled := emptyMonitorIndexResponse(t, req); handled {
					return response, nil
				}
				if response, handled := emptyDiagnosticSourceIndexResponse(t, req); handled {
					return response, nil
				}
				if strings.EqualFold(req.URL.String(), operation) {
					deleted = true
					return &http.Response{StatusCode: 204, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(""))}, nil
				}
				path := strings.ToLower(req.URL.Path)
				// Native child collections of the cascading kinds are empty.
				if req.Method == "GET" && strings.HasPrefix(path, id+"/") && !strings.Contains(strings.TrimPrefix(path, id+"/"), "/") {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				switch path {
				case id:
					if req.Method == "DELETE" {
						deletes++
						return jsonResponse(202, map[string]any{}, http.Header{"Location": {operation}, "X-Ms-Request-Id": {"delete-request"}, "Retry-After": {"1"}}), nil
					}
					if deleted {
						return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
					}
					raw := nativeResource(kind, "sample", "eastus", map[string]any{"provisioningState": "Succeeded"})
					raw["id"] = id
					return jsonResponse(200, raw, nil), nil
				case "/subscriptions/" + testSubscription + "/resourcegroups/test":
					return jsonResponse(200, map[string]any{"id": path}, nil), nil
				case "/subscriptions/" + testSubscription + "/providers/microsoft.authorization/locks":
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), nil
				}
				t.Fatalf("unexpected %s %s", req.Method, req.URL)
				return nil, nil
			})
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			request := contracts.ActionRequest{Asset: value, Action: "delete", IdempotencyKey: "delete-key"}
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			done := false
			for i := 0; i < 5 && !done; i++ {
				wait, err := driver.Wait(context.Background(), request, result)
				if err != nil {
					t.Fatal(err)
				}
				done = wait.Done
			}
			readback, err := driver.Readback(context.Background(), request)
			if !done || err != nil || readback.Exists || deletes != 1 {
				t.Fatalf("done=%v readback=%+v deletes=%d err=%v", done, readback, deletes, err)
			}
		})
	}
}

// A resolver's endpoints and a ruleset's virtual network links are deleted
// first; forwarding rules, relays and notification hubs go with their parent.
// A ruleset depends on the outbound endpoints it forwards through.
func TestDNSResolverRelayAndNotificationHubRules(t *testing.T) {
	for _, child := range []string{dnsResolverType + "/inboundEndpoints", dnsResolverType + "/outboundEndpoints"} {
		if !servicePrerequisiteKind(dnsResolverType, child) {
			t.Fatalf("%s is not a resolver prerequisite", child)
		}
	}
	if !servicePrerequisiteKind(dnsForwardingRulesetType, dnsForwardingRulesetType+"/virtualNetworkLinks") || servicePrerequisiteKind(dnsForwardingRulesetType, dnsForwardingRulesetType+"/forwardingRules") {
		t.Fatal("ruleset prerequisites are wrong")
	}
	for _, parent := range []string{dnsForwardingRulesetType, relayNamespaceType, notificationNamespaceType} {
		if !HasServiceCascade(parent) {
			t.Fatalf("%s cascade is not registered", parent)
		}
	}
	endpoint := resourceID(dnsResolverType, "resolver") + "/outboundEndpoints/egress"
	refs := references(dnsForwardingRulesetType, resourceID(dnsForwardingRulesetType, "rules"), map[string]any{"properties": map[string]any{"dnsResolverOutboundEndpoints": []any{map[string]any{"id": endpoint}}}})
	if len(refs[dnsResolverType+"/outboundEndpoints"]) != 1 {
		t.Fatalf("ruleset references = %v", refs)
	}
}
