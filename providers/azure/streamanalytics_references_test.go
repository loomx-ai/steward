package azure

import (
	"context"
	"net/http"
	"slices"
	"strings"
	"testing"
)

func TestStreamAnalyticsNamedDataSourcesResolveAcrossResourceGroups(t *testing.T) {
	for _, test := range []struct {
		source      string
		settings    map[string]any
		root, child string
	}{
		{"Microsoft.Storage/Blob", map[string]any{"storageAccounts": []any{map[string]any{"accountName": "data"}}, "container": "events"}, storageType, "blobServices/default/containers/events"},
		{"Microsoft.Storage/Table", map[string]any{"accountName": "data"}, storageType, ""},
		{"Microsoft.ServiceBus/EventHub", map[string]any{"serviceBusNamespace": "data", "eventHubName": "events", "consumerGroupName": "cg"}, eventHubNamespaceType, "eventhubs/events/consumergroups/cg"},
		{"Microsoft.EventHub/EventHub", map[string]any{"serviceBusNamespace": "data", "eventHubName": "events"}, eventHubNamespaceType, "eventhubs/events"},
		{"Microsoft.ServiceBus/Queue", map[string]any{"serviceBusNamespace": "data", "queueName": "queue"}, serviceBusNamespaceType, "queues/queue"},
		{"Microsoft.ServiceBus/Topic", map[string]any{"serviceBusNamespace": "data", "topicName": "topic"}, serviceBusNamespaceType, "topics/topic"},
		{"Microsoft.Sql/Server/Database", map[string]any{"server": "data", "database": "db"}, sqlServerType, "databases/db"},
		{"Microsoft.Sql/Server/DataWarehouse", map[string]any{"server": "data", "database": "db"}, sqlServerType, "databases/db"},
		{"Microsoft.Storage/DocumentDB", map[string]any{"accountId": "data", "database": "CaseSensitive", "collectionNamePattern": "Records"}, cosmosType, ""},
		{"Microsoft.AzureFunction", map[string]any{"functionAppName": "data", "functionName": "run"}, appSiteType, "functions/run"},
	} {
		t.Run(test.source, func(t *testing.T) {
			s, r, _ := streamAnalyticsScenario(t, false)
			id := "/subscriptions/" + testSubscription + "/resourcegroups/external-data/providers/" + strings.ToLower(test.root) + "/data"
			path := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(test.root)
			mapping, _ := findType(test.root)
			s.lists[path], s.version[path] = []any{map[string]any{"id": id, "name": "data", "type": test.root}}, mapping.Version
			raw := map[string]any{"properties": map[string]any{"datasource": map[string]any{"type": test.source, "properties": test.settings}}}
			c, _ := r.resolve(context.Background(), "connection")
			for _, kind := range []string{streamAnalyticsInputType, streamAnalyticsOutputType} {
				refs, err := c.streamAnalyticsReferences(context.Background(), kind, raw)
				if err != nil || !slices.Contains(refs, id) || test.child != "" && !slices.Contains(refs, strings.ToLower(id+"/"+test.child)) {
					t.Fatal("named data source lost cross-group identity", test.source, refs, err)
				}
				if test.root == cosmosType && len(refs) != 1 {
					t.Fatal("invented a case-sensitive Cosmos child identity")
				}
			}
			if test.root == storageType {
				refs, err := c.streamAnalyticsReferences(context.Background(), streamAnalyticsJobType, map[string]any{"properties": map[string]any{"jobStorageAccount": map[string]any{"accountName": "data"}}})
				if err != nil || !slices.Equal(refs, []string{id}) {
					t.Fatal("job storage account missing", refs, err)
				}
			}
		})
	}
}

func TestStreamAnalyticsReferenceDiscoveryDoesNotGuessOrHideFailures(t *testing.T) {
	for _, mode := range []string{"absent", "foreign-explicit-id", "duplicate-name", "duplicate-id", "foreign-index", "wrong-type", "wrong-name", "forbidden", "partial", "missing-array", "invalid-name", "invalid-id", "unknown-connector"} {
		t.Run(mode, func(t *testing.T) {
			s, r, _ := streamAnalyticsScenario(t, false)
			c, _ := r.resolve(context.Background(), "connection")
			path := "/subscriptions/" + testSubscription + "/providers/" + strings.ToLower(storageType)
			id := "/subscriptions/" + testSubscription + "/resourcegroups/external/providers/" + strings.ToLower(storageType) + "/data"
			row := map[string]any{"id": id, "name": "data", "type": storageType}
			s.lists[path] = []any{row}
			var value any = "data"
			switch mode {
			case "absent":
				s.lists[path] = []any{}
			case "foreign-explicit-id":
				value = strings.Replace(id, testSubscription, testTenant, 1)
			case "duplicate-name":
				s.lists[path] = append(s.lists[path], map[string]any{"id": strings.Replace(id, "/external/", "/another/", 1), "name": "data", "type": storageType})
			case "duplicate-id":
				s.lists[path] = append(s.lists[path], row)
			case "foreign-index":
				row["id"] = strings.Replace(id, testSubscription, testTenant, 1)
			case "wrong-type":
				row["type"] = cosmosType
			case "wrong-name":
				row["name"] = "other"
			case "forbidden":
				s.status[path] = 403
			case "partial":
				s.status[path] = 206
			case "invalid-name":
				value = "data/other"
			case "invalid-id":
				value = strings.Replace(id, "microsoft.storage/storageaccounts", "microsoft.compute/disks", 1)
			}
			previous := s.handle
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if mode == "foreign-explicit-id" || mode == "unknown-connector" {
					t.Fatal("explicit ID or unknown connector must not guess HTTP endpoints")
				}
				if mode == "missing-array" && strings.EqualFold(req.URL.Path, path) {
					return jsonResponse(200, map[string]any{}, nil), true
				}
				return previous(req)
			}
			if mode == "unknown-connector" {
				refs, err := c.streamAnalyticsReferences(context.Background(), streamAnalyticsOutputType, map[string]any{"properties": map[string]any{"datasource": map[string]any{"type": "Microsoft.Future/NewService", "properties": map[string]any{"endpoint": "https://untrusted.invalid"}}}})
				if err != nil || len(refs) != 0 {
					t.Fatal("unknown connector fabricated an ARM dependency", refs, err)
				}
				return
			}
			resolved, err := c.streamAnalyticsNamedResource(context.Background(), storageType, value)
			switch mode {
			case "absent":
				if err != nil || resolved != "" {
					t.Fatal("missing name became a guessed resource", resolved, err)
				}
			case "foreign-explicit-id":
				if err != nil || resolved != value {
					t.Fatal("explicit foreign identity was lost", resolved, err)
				}
			default:
				if err == nil {
					t.Fatal("invalid or incomplete reference inventory accepted", mode)
				}
			}
		})
	}
}
