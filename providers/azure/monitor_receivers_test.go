package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const monitorReceiverCustomerID = "55dfd1f8-7e59-4f89-bf56-4c82f5ace23c"

func monitorReceiverExample(t *testing.T, file string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/monitorreceivers/" + file)
	if err != nil {
		t.Fatal(err)
	}
	var example map[string]any
	if json.Unmarshal(payload, &example) != nil {
		t.Fatal("invalid native receiver example")
	}
	body := object(object(example["responses"])["200"])["body"]
	if file == "workspace-get.json" {
		// The unchanged published GET incorrectly returns an array. Compose a
		// single native row here; source tests retain and reject that mismatch.
		return object(array(body)[0])
	}
	return object(body)
}

type monitorReceiverFixture struct {
	*monitorInventoryFixture
	receivers                          map[string]map[string]any
	fault                              func(*http.Request) (*http.Response, bool)
	sourceID, namespaceID, workspaceID string
}

func newMonitorReceiverFixture(t *testing.T) *monitorReceiverFixture {
	t.Helper()
	f := &monitorReceiverFixture{monitorInventoryFixture: newMonitorInventoryFixture(t, monitorActionGroupType), receivers: map[string]map[string]any{}}
	f.sourceID = slices.Sorted(maps.Keys(f.objects))[0]
	root := "/subscriptions/" + testSubscription
	f.namespaceID = root + "/resourcegroups/receiver-group/providers/microsoft.eventhub/namespaces/receiver-namespace"
	f.workspaceID = root + "/resourcegroups/another-group/providers/microsoft.operationalinsights/workspaces/receiver-workspace"
	// Native List/Get properties remain intact. Names/scopes and workspace GUID
	// are composed to link the independent official Action Group example.
	namespace := maps.Clone(object(array(monitorReceiverExample(t, "eventhub-namespace-list.json")["value"])[0]))
	namespace["id"], namespace["name"], namespace["type"], namespace["location"] = f.namespaceID, last(f.namespaceID), eventHubNamespaceType, "eastus"
	workspace := monitorReceiverExample(t, "workspace-get.json")
	workspace["id"], workspace["name"], workspace["type"], workspace["location"] = f.workspaceID, last(f.workspaceID), insightsWorkspaceType, "westcentralus"
	object(workspace["properties"])["customerId"] = monitorReceiverCustomerID
	f.receivers[f.namespaceID], f.receivers[f.workspaceID] = namespace, workspace
	source := monitorReceiverExample(t, "actiongroup-create.json")
	props := object(f.objects[f.sourceID]["properties"])
	for _, key := range []string{"eventHubReceivers", "itsmReceivers"} {
		props[key] = object(source["properties"])[key]
	}
	event := object(array(props["eventHubReceivers"])[0])
	event["subscriptionId"], event["tenantId"], event["eventHubNameSpace"], event["eventHubName"] = testSubscription, testTenant, last(f.namespaceID), "notifications"
	object(array(props["itsmReceivers"])[0])["workspaceId"] = testSubscription + "|" + monitorReceiverCustomerID
	f.monitorInventoryFixture.override = func(req *http.Request) (*http.Response, bool) {
		if f.fault != nil {
			if response, ok := f.fault(req); ok {
				return response, true
			}
		}
		for kind, version := range map[string]string{eventHubNamespaceType: "2024-01-01", insightsWorkspaceType: "2022-10-01"} {
			collection := root + "/providers/" + strings.ToLower(kind)
			path := strings.ToLower(req.URL.Path)
			_, actual, _ := parseID(path)
			if path != collection && !strings.EqualFold(actual, kind) {
				continue
			}
			if req.Method != "GET" || req.URL.Query().Get("api-version") != version {
				t.Fatal("receiver index used a non-native operation", req.Method, req.URL)
			}
			if path == collection {
				values := []any{}
				for _, id := range slices.Sorted(maps.Keys(f.receivers)) {
					_, actual, _ := parseID(id)
					if strings.EqualFold(actual, kind) {
						values = append(values, f.receivers[id])
					}
				}
				body := map[string]any{"value": values}
				if len(values) > 1 {
					if req.URL.Query().Get("$skiptoken") == "" {
						body["value"], body["nextLink"] = values[:1], apiURL(collection, version)+"&$skiptoken=receiver-page"
					} else {
						body["value"] = values[1:]
					}
				}
				return jsonResponse(200, body, nil), true
			}
			if raw := f.receivers[path]; raw != nil {
				return jsonResponse(200, raw, nil), true
			}
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
		}
		return nil, false
	}
	return f
}

func TestMonitorReceiverNativeResolutionAndProjection(t *testing.T) {
	f := newMonitorReceiverFixture(t)
	parent := f.asset(t, f.sourceID)
	c, err := f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	refs, err := c.monitorRecordedReferences(parent)
	if err != nil || len(refs) != 3 {
		t.Fatal("native receiver references missing", refs, err)
	}
	for kind, id := range map[string]string{eventHubNamespaceType: f.namespaceID, eventHubType: f.namespaceID + "/eventhubs/notifications", insightsWorkspaceType: f.workspaceID} {
		if !slices.Equal(stringValues(refs[kind]), []string{id}) || !slices.Equal(stringValues(parent.Normalized[referenceKey(kind)]), []string{id}) {
			t.Fatal("receiver was assigned the Action Group's resource group", kind, refs)
		}
		target := asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: parent.Identity.ConnectionID, Partition: parent.Identity.Partition, NativeID: id, NativeType: kind}}
		contribution, err := c.contributeMonitorReferences(t.Context(), parent, []asset.Asset{parent, target})
		if err != nil || len(contribution.Relationships) != 2 || len(contribution.Bindings) != 0 {
			t.Fatal("receiver shared relationship missing", kind, contribution, err)
		}
		for _, rel := range contribution.Relationships {
			if rel.Type == graph.RelationshipDependsOn && (rel.SourceAssetID != target.ID || rel.TargetAssetID != parent.ID || rel.Evidence[graph.RelationshipEvidenceRequiredDeletion] != true || rel.Evidence[graph.RelationshipEvidenceAutomaticSelection] != false) {
				t.Fatal("receiver acquired automatic cleanup", rel)
			}
		}
		incoming, err := c.monitorIncoming(t.Context(), target.Identity)
		if err != nil || len(incoming) != 1 || incoming[0].id != parent.Identity.NativeID {
			t.Fatal("native receiver incoming index missing", incoming, err)
		}
	}
	encoded, _ := json.Marshal(parent)
	for _, private := range []string{"ticketConfiguration", "WorkItemData", "connectionId", "itsmReceivers", "eventHubReceivers"} {
		if strings.Contains(string(encoded), private) {
			t.Fatal("private receiver configuration escaped projection", private)
		}
	}
	var recovered asset.Asset
	if json.Unmarshal(encoded, &recovered) != nil {
		t.Fatal("invalid recovered receiver asset")
	}
	if _, err := c.monitorRecordedReferences(recovered); err != nil {
		t.Fatal("receiver references lost after JSON recovery", err)
	}
}

func TestMonitorReceiverUnresolvedAndForeignBoundaries(t *testing.T) {
	for _, mode := range []string{"missing-namespace", "missing-workspace", "foreign-subscription", "foreign-tenant", "foreign-workspace", "bare-workspace-guid"} {
		t.Run(mode, func(t *testing.T) {
			f := newMonitorReceiverFixture(t)
			props := object(f.objects[f.sourceID]["properties"])
			event, itsm := object(array(props["eventHubReceivers"])[0]), object(array(props["itsmReceivers"])[0])
			switch mode {
			case "missing-namespace":
				delete(f.receivers, f.namespaceID)
			case "missing-workspace":
				delete(f.receivers, f.workspaceID)
			case "foreign-subscription":
				event["subscriptionId"] = "00000000-0000-0000-0000-000000000000"
			case "foreign-tenant":
				event["tenantId"] = "00000000-0000-0000-0000-000000000000"
			case "foreign-workspace":
				itsm["workspaceId"] = "00000000-0000-0000-0000-000000000000|" + monitorReceiverCustomerID
			case "bare-workspace-guid":
				itsm["workspaceId"] = monitorReceiverCustomerID
			}
			parent := f.asset(t, f.sourceID)
			c, _ := f.runtime.resolve(t.Context(), "connection")
			refs, err := c.monitorRecordedReferences(parent)
			if err != nil {
				t.Fatal("unresolved receiver was silently discarded", err)
			}
			if mode != "bare-workspace-guid" {
				kind := eventHubType
				if strings.Contains(mode, "workspace") {
					kind = insightsWorkspaceType
				}
				if len(stringValues(refs[kind])) != 1 || !monitorReceiverSelector(kind, stringValues(refs[kind])[0]) {
					t.Fatal("unresolved receiver acquired a fabricated ARM ID", refs)
				}
			} else if !slices.Equal(stringValues(refs[insightsWorkspaceType]), []string{f.workspaceID}) {
				t.Fatal("unqualified native customer ID did not resolve")
			}
			contribution, err := c.contributeMonitorReferences(t.Context(), parent, []asset.Asset{parent})
			if err != nil || len(contribution.Unresolved) != 3 || len(contribution.Relationships)+len(contribution.Bindings) != 0 {
				t.Fatal("unresolved receiver acquired ownership", contribution, err)
			}
			if strings.HasPrefix(mode, "foreign-") {
				kind := eventHubNamespaceType
				if mode == "foreign-workspace" {
					kind = insightsWorkspaceType
				}
				if f.calls["GET /subscriptions/"+testSubscription+"/providers/"+strings.ToLower(kind)] != 0 {
					t.Fatal("foreign receiver borrowed the local index")
				}
			}
		})
	}
}

func TestMonitorReceiverResolutionDrift(t *testing.T) {
	for _, mode := range []string{"namespace-moved", "workspace-recreated", "previously-missing-namespace", "previously-missing-workspace"} {
		t.Run(mode, func(t *testing.T) {
			f := newMonitorReceiverFixture(t)
			namespace, workspace := f.receivers[f.namespaceID], f.receivers[f.workspaceID]
			if mode == "previously-missing-namespace" {
				delete(f.receivers, f.namespaceID)
			}
			if mode == "previously-missing-workspace" {
				delete(f.receivers, f.workspaceID)
			}
			parent := f.asset(t, f.sourceID)
			request := f.request()
			request.Limit = 1
			first, err := f.runtime.List(t.Context(), request)
			if err != nil || first.NextCursor == "" {
				t.Fatal("missing receiver cursor", err)
			}
			driver, err := f.runtime.ResolveAction(t.Context(), "connection", parent)
			if err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "namespace-moved":
				delete(f.receivers, f.namespaceID)
				id := strings.Replace(f.namespaceID, "receiver-group", "moved-group", 1)
				namespace["id"], f.receivers[id] = id, namespace
			case "workspace-recreated":
				object(workspace["properties"])["customerId"] = testTenant
			case "previously-missing-namespace":
				f.receivers[f.namespaceID] = namespace
			case "previously-missing-workspace":
				f.receivers[f.workspaceID] = workspace
			}
			request.Cursor = first.NextCursor
			batch, err := f.runtime.List(t.Context(), request)
			if err == nil || len(batch.Items) != 0 || batch.Complete {
				t.Fatal("receiver remapping did not invalidate source cursor", batch, err)
			}
			c, _ := f.runtime.resolve(t.Context(), "connection")
			if _, err := c.contributeMonitorReferences(t.Context(), parent, []asset.Asset{parent}); err == nil {
				t.Fatal("receiver remapping changed the reviewed graph")
			}
			_, err = driver.Execute(t.Context(), contracts.ActionRequest{Action: "delete", Asset: parent})
			if err == nil || len(f.deletes) != 0 {
				t.Fatal("receiver remapping survived action preflight", err)
			}
		})
	}
}

func TestMonitorReceiverIndexBoundaries(t *testing.T) {
	for _, kind := range []string{eventHubNamespaceType, insightsWorkspaceType} {
		for _, mode := range []string{"paged", "duplicate-name-or-guid", "duplicate-id", "foreign-id", "missing-location", "id-alias", "get-name", "get-location", "list-403", "list-404", "list-202", "get-403", "get-404", "get-202", "get-lro", "get-alias", "filtered-page", "foreign-page", "page-version", "nextlink-alias", "second-index-change", "customer-id-change", "cancelled"} {
			t.Run(kind+"/"+mode, func(t *testing.T) {
				if mode == "customer-id-change" && kind != insightsWorkspaceType {
					t.Skip("customerId belongs to the workspace API")
				}
				f := newMonitorReceiverFixture(t)
				id, version := f.namespaceID, "2024-01-01"
				if kind == insightsWorkspaceType {
					id, version = f.workspaceID, "2022-10-01"
				}
				collection := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(kind)
				row := f.receivers[id]
				if mode == "paged" || mode == "duplicate-name-or-guid" {
					other := maps.Clone(row)
					otherID := strings.Replace(id, "/resourcegroups/", "/resourcegroups/other-", 1)
					other["id"] = otherID
					if mode == "paged" {
						otherID += "-unrelated"
						other["id"], other["name"] = otherID, last(otherID)
						other["properties"] = maps.Clone(object(row["properties"]))
						if kind == insightsWorkspaceType {
							object(other["properties"])["customerId"] = testTenant
						}
					}
					f.receivers[otherID] = other
				}
				f.fault = func(req *http.Request) (*http.Response, bool) {
					path := strings.ToLower(req.URL.Path)
					if path == collection {
						switch mode {
						case "list-403", "list-404", "list-202":
							status := map[string]int{"list-403": 403, "list-404": 404, "list-202": 202}[mode]
							return jsonResponse(status, map[string]any{"value": []any{}}, nil), true
						case "duplicate-id":
							return jsonResponse(200, map[string]any{"value": []any{row, row}}, nil), true
						case "foreign-id", "missing-location", "id-alias":
							copy := maps.Clone(row)
							if mode == "foreign-id" {
								copy["id"] = strings.Replace(id, testSubscription, testTenant, 1)
							} else if mode == "missing-location" {
								delete(copy, "location")
							} else {
								copy["ID"] = copy["id"]
							}
							return jsonResponse(200, map[string]any{"value": []any{copy}}, nil), true
						case "filtered-page", "foreign-page", "page-version", "nextlink-alias":
							next := apiURL(collection, version) + "&$skiptoken=next"
							key := "nextLink"
							switch mode {
							case "filtered-page":
								next += "&$filter=name%20eq%20hidden"
							case "foreign-page":
								next = strings.Replace(next, testSubscription, testTenant, 1)
							case "page-version":
								next = strings.Replace(next, version, "2000-01-01", 1)
							case "nextlink-alias":
								key = "NextLink"
							}
							return jsonResponse(200, map[string]any{"value": []any{}, key: next}, nil), true
						case "second-index-change":
							if f.calls["GET "+collection] > 1 {
								return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
							}
						}
					}
					if path == id {
						copy := maps.Clone(row)
						switch mode {
						case "get-403", "get-404", "get-202":
							status := map[string]int{"get-403": 403, "get-404": 404, "get-202": 202}[mode]
							return jsonResponse(status, copy, nil), true
						case "get-lro":
							return jsonResponse(200, copy, http.Header{"Azure-Asyncoperation": []string{apiURL(id, version)}}), true
						case "get-name":
							copy["name"] = "replacement"
						case "get-location":
							copy["location"] = "westus"
						case "get-alias":
							copy["ID"] = copy["id"]
						case "customer-id-change":
							copy["properties"] = maps.Clone(object(row["properties"]))
							object(copy["properties"])["customerId"] = testTenant
						default:
							return nil, false
						}
						return jsonResponse(200, copy, nil), true
					}
					return nil, false
				}
				c, _ := f.runtime.resolve(t.Context(), "connection")
				ctx := t.Context()
				if mode == "cancelled" {
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
				refs, err := c.monitorReferences(ctx, monitorActionGroupType, f.sourceID, f.objects[f.sourceID])
				if mode == "paged" {
					if err != nil || !slices.Equal(refs[kind], []string{id}) || f.calls["GET "+collection] != 4 {
						t.Fatal("complete native pages did not resolve", refs, err, f.calls)
					}
				} else if err == nil || isNotFound(err) {
					t.Fatal("incomplete receiver index authorized a relationship", mode, refs, err)
				}
			})
		}
	}
}

func TestMonitorReceiverSelectorBoundaries(t *testing.T) {
	for _, mode := range []string{"event-null", "event-entry", "event-alias", "namespace-alias", "namespace-path", "hub-blank", "hub-type", "subscription-type", "subscription-path", "tenant-null", "tenant-blank", "tenant-omitted", "itsm-null", "itsm-entry", "itsm-alias", "workspace-alias", "workspace-blank", "workspace-path", "workspace-extra", "workspace-subscription", "workspace-customer", "workspace-type", "region-missing", "region-mismatch", "function-child-path", "function-child-missing", "runbook-global", "runbook-name-missing", "runbook-global-type"} {
		t.Run(mode, func(t *testing.T) {
			f := newMonitorReceiverFixture(t)
			props := object(f.objects[f.sourceID]["properties"])
			event, itsm := object(array(props["eventHubReceivers"])[0]), object(array(props["itsmReceivers"])[0])
			switch mode {
			case "event-null":
				props["eventHubReceivers"] = nil
			case "event-entry":
				props["eventHubReceivers"] = []any{"invalid"}
			case "event-alias":
				props["EventHubReceivers"] = props["eventHubReceivers"]
			case "namespace-alias":
				event["eventHubNamespace"] = event["eventHubNameSpace"]
			case "namespace-path":
				event["eventHubNameSpace"] = "namespace/escape"
			case "hub-blank":
				event["eventHubName"] = ""
			case "hub-type":
				event["eventHubName"] = 1
			case "subscription-type":
				event["subscriptionId"] = 1
			case "subscription-path":
				event["subscriptionId"] = "/subscriptions/" + testSubscription
			case "tenant-null":
				event["tenantId"] = nil
			case "tenant-blank":
				event["tenantId"] = ""
			case "tenant-omitted":
				delete(event, "tenantId")
			case "itsm-null":
				props["itsmReceivers"] = nil
			case "itsm-entry":
				props["itsmReceivers"] = []any{1}
			case "itsm-alias":
				props["ItsmReceivers"] = props["itsmReceivers"]
			case "workspace-alias":
				itsm["WorkspaceId"] = itsm["workspaceId"]
			case "workspace-blank":
				itsm["workspaceId"] = ""
			case "workspace-path":
				itsm["workspaceId"] = f.workspaceID
			case "workspace-extra":
				itsm["workspaceId"] = testSubscription + "|" + monitorReceiverCustomerID + "|" + testTenant
			case "workspace-subscription":
				itsm["workspaceId"] = "subscription|" + monitorReceiverCustomerID
			case "workspace-customer":
				itsm["workspaceId"] = testSubscription + "|workspace"
			case "workspace-type":
				itsm["workspaceId"] = 1
			case "region-missing":
				delete(itsm, "region")
			case "region-mismatch":
				itsm["region"] = "eastus"
			case "function-child-path", "function-child-missing":
				name := "escape/child"
				if mode == "function-child-missing" {
					name = ""
				}
				props["azureFunctionReceivers"] = []any{map[string]any{"functionAppResourceId": resourceID(appSiteType, "function"), "functionName": name}}
			case "runbook-global", "runbook-name-missing", "runbook-global-type":
				account := "/subscriptions/" + testSubscription + "/resourcegroups/shared/providers/Microsoft.Automation/automationAccounts/automation"
				row := map[string]any{"automationAccountId": account, "webhookResourceId": account + "/webhooks/webhook", "runbookName": "Global friendly name", "isGlobalRunbook": true}
				if mode == "runbook-name-missing" {
					delete(row, "runbookName")
					row["isGlobalRunbook"] = false
				}
				if mode == "runbook-global-type" {
					row["isGlobalRunbook"] = "true"
				}
				props["automationRunbookReceivers"] = []any{row}
			}
			c, _ := f.runtime.resolve(t.Context(), "connection")
			refs, err := c.monitorReferences(t.Context(), monitorActionGroupType, f.sourceID, f.objects[f.sourceID])
			if mode == "runbook-global" || mode == "tenant-omitted" {
				if err != nil {
					t.Fatal("native optional selector rejected", err)
				}
				for kind := range refs {
					if strings.HasSuffix(strings.ToLower(kind), "/runbooks") {
						t.Fatal("global display name became a native runbook ID")
					}
				}
			} else if err == nil || isNotFound(err) {
				t.Fatal("invalid native selector was accepted", mode, refs, err)
			}
		})
	}
}
