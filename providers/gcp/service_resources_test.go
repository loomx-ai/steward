package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// These paths, collection names and response fields come from the native
// service contracts, independently of the generated catalog and YAML rules.
func TestServiceResourceWireLifecycles(t *testing.T) {
	const p = "projects/sample-project/"
	const regional = p + "locations/us-central1/"
	const global = p + "locations/global/"
	for _, test := range []struct {
		kind, version, name, list, items, region, operation, data string
	}{
		{"redis.googleapis.com/Instance", "v1", regional + "instances/cache", p + "locations/-/instances", "instances", "us-central1", regional + "operations/delete", `{}`},
		{"redis.googleapis.com/Cluster", "v1", regional + "clusters/cache", p + "locations/-/clusters", "clusters", "us-central1", regional + "operations/delete", `{"uid":"redis-uid"}`},
		{"dns.googleapis.com/ManagedZone", "dns/v1", p + "managedZones/example", p + "managedZones", "managedZones", "global", "", `{"name":"example","dnsName":"example.com."}`},
		{"dns.googleapis.com/Policy", "dns/v1", p + "policies/resolver", p + "policies", "policies", "global", "", `{"name":"resolver"}`},
		{dnsRecordSetType, "dns/v1", p + "managedZones/example/rrsets/*.example.com./A", p + "managedZones/example/rrsets", "rrsets", "global", "", `{"name":"*.example.com.","type":"A","ttl":300,"rrdatas":["192.0.2.1"]}`},
		{"bigquery.googleapis.com/Dataset", "bigquery/v2", p + "datasets/warehouse", p + "datasets", "datasets", "global", "", `{"datasetReference":{"projectId":"sample-project","datasetId":"warehouse"},"location":"US"}`},
		{"bigquery.googleapis.com/Table", "bigquery/v2", p + "datasets/warehouse/tables/events", p + "datasets/warehouse/tables", "tables", "global", "", `{"tableReference":{"projectId":"sample-project","datasetId":"warehouse","tableId":"events"},"location":"EU"}`},
		{"firestore.googleapis.com/Database", "v1", p + "databases/(default)", p + "databases", "databases", "global", p + "databases/(default)/operations/delete", `{"uid":"database-uid","locationId":"nam5","etag":"reviewed-etag"}`},
		{"bigtableadmin.googleapis.com/Instance", "v2", p + "instances/wide", p + "instances", "instances", "global", "", `{}`},
		{"bigtableadmin.googleapis.com/Cluster", "v2", p + "instances/wide/clusters/zone-a", p + "instances/wide/clusters", "clusters", "us-central1", "", `{"location":"projects/sample-project/locations/us-central1-a"}`},
		{"bigtableadmin.googleapis.com/Table", "v2", p + "instances/wide/tables/events", p + "instances/wide/tables", "tables", "global", "", `{}`},
		{"spanner.googleapis.com/Instance", "v1", p + "instances/sql", p + "instances", "instances", "global", "", `{}`},
		{"spanner.googleapis.com/Database", "v1", p + "instances/sql/databases/events", p + "instances/sql/databases", "databases", "global", "", `{}`},
		{"cloudtasks.googleapis.com/Queue", "v2", regional + "queues/work", regional + "queues", "queues", "us-central1", "", `{}`},
		{"cloudfunctions.googleapis.com/CloudFunction", "v2", regional + "functions/handler", p + "locations/-/functions", "functions", "us-central1", regional + "operations/delete", `{}`},
		{"file.googleapis.com/Instance", "v1", p + "locations/us-central1-a/instances/share", p + "locations/-/instances", "instances", "us-central1", p + "locations/us-central1-a/operations/delete", `{}`},
		{"alloydb.googleapis.com/Cluster", "v1", regional + "clusters/pg", regional + "clusters", "clusters", "us-central1", regional + "operations/delete", `{"uid":"alloy-cluster-uid"}`},
		{"alloydb.googleapis.com/Instance", "v1", regional + "clusters/pg/instances/primary", regional + "clusters/pg/instances", "instances", "us-central1", regional + "operations/delete", `{"uid":"alloy-instance-uid"}`},
		{"managedkafka.googleapis.com/Cluster", "v1", regional + "clusters/broker", regional + "clusters", "clusters", "us-central1", regional + "operations/delete", `{}`},
		{"managedkafka.googleapis.com/Topic", "v1", regional + "clusters/broker/topics/events", regional + "clusters/broker/topics", "topics", "us-central1", "", `{}`},
		{"apigateway.googleapis.com/Api", "v1", global + "apis/orders", global + "apis", "apis", "global", global + "operations/delete", `{}`},
		{"apigateway.googleapis.com/ApiConfig", "v1", global + "apis/orders/configs/v1", global + "apis/orders/configs", "apiConfigs", "global", global + "operations/delete", `{}`},
		{"apigateway.googleapis.com/Gateway", "v1", regional + "gateways/public", regional + "gateways", "gateways", "us-central1", regional + "operations/delete", `{}`},
		{"certificatemanager.googleapis.com/Certificate", "v1", global + "certificates/tls", global + "certificates", "certificates", "global", global + "operations/delete", `{}`},
		{"certificatemanager.googleapis.com/Certificate", "v1", regional + "certificates/tls", regional + "certificates", "certificates", "us-central1", regional + "operations/delete", `{}`},
		{"certificatemanager.googleapis.com/CertificateMap", "v1", global + "certificateMaps/tls", global + "certificateMaps", "certificateMaps", "global", global + "operations/delete", `{}`},
		{"certificatemanager.googleapis.com/CertificateMapEntry", "v1", global + "certificateMaps/tls/certificateMapEntries/www", global + "certificateMaps/tls/certificateMapEntries", "certificateMapEntries", "global", global + "operations/delete", `{}`},
		{"iam.googleapis.com/ServiceAccount", "v1", p + "serviceAccounts/worker@sample-project.iam.gserviceaccount.com", p + "serviceAccounts", "accounts", "global", "", `{"uniqueId":"service-uid"}`},
		{"iam.googleapis.com/ServiceAccountKey", "v1", p + "serviceAccounts/worker@sample-project.iam.gserviceaccount.com/keys/key-1", p + "serviceAccounts/worker@sample-project.iam.gserviceaccount.com/keys", "keys", "global", "", `{"keyType":"USER_MANAGED"}`},
		{"iam.googleapis.com/Role", "v1", p + "roles/CustomReader", p + "roles", "roles", "global", "", `{"etag":"reviewed-etag"}`},
		{"gkehub.googleapis.com/Fleet", "v1", global + "fleets/default", global + "fleets", "fleets", "global", global + "operations/delete", `{"uid":"fleet-uid"}`},
		{"gkehub.googleapis.com/Membership", "v1", regional + "memberships/cluster", p + "locations/-/memberships", "resources", "us-central1", regional + "operations/delete", `{"uniqueId":"membership-uid","endpoint":{"gkeCluster":{"resourceLink":"//container.googleapis.com/projects/sample-project/locations/us-central1/clusters/cluster"}}}`},
		{"run.googleapis.com/Job", "v2", regional + "jobs/work", regional + "jobs", "jobs", "us-central1", regional + "operations/delete", `{"uid":"job-uid","etag":"reviewed-etag"}`},
		{"servicedirectory.googleapis.com/Namespace", "v1", regional + "namespaces/apps", regional + "namespaces", "namespaces", "us-central1", "", `{"uid":"namespace-uid"}`},
		{"servicedirectory.googleapis.com/Service", "v1", regional + "namespaces/apps/services/api", regional + "namespaces/apps/services", "services", "us-central1", "", `{"uid":"service-uid"}`},
		{"servicedirectory.googleapis.com/Endpoint", "v1", regional + "namespaces/apps/services/api/endpoints/backend", regional + "namespaces/apps/services/api/endpoints", "endpoints", "us-central1", "", `{"uid":"endpoint-uid"}`},
		{"logging.googleapis.com/LogBucket", "v2", global + "buckets/archive", p + "locations/-/buckets", "buckets", "global", "", `{"lifecycleState":"ACTIVE"}`},
		{"logging.googleapis.com/LogSink", "v2", p + "sinks/audit", p + "sinks", "sinks", "global", "", `{"name":"audit"}`},
		{"monitoring.googleapis.com/Dashboard", "v1", p + "dashboards/overview", p + "dashboards", "dashboards", "global", "", `{}`},
		{"monitoring.googleapis.com/UptimeCheckConfig", "v3", p + "uptimeCheckConfigs/public", p + "uptimeCheckConfigs", "uptimeCheckConfigs", "global", "", `{}`},

		{"aiplatform.googleapis.com/Endpoint", "v1", regional + "endpoints/predict", regional + "endpoints", "endpoints", "us-central1", regional + "operations/delete", `{}`},
		{"apphub.googleapis.com/Application", "v1", global + "applications/shop", global + "applications", "applications", "global", global + "operations/delete", `{"uid":"application-uid"}`},
		{"apphub.googleapis.com/Service", "v1", global + "applications/shop/services/web", global + "applications/shop/services", "services", "global", global + "operations/delete", `{"uid":"service-uid"}`},
		{"apphub.googleapis.com/Workload", "v1", global + "applications/shop/workloads/worker", global + "applications/shop/workloads", "workloads", "global", global + "operations/delete", `{"uid":"workload-uid"}`},
		{"backupdr.googleapis.com/BackupPlan", "v1", regional + "backupPlans/daily", p + "locations/-/backupPlans", "backupPlans", "us-central1", regional + "operations/delete", `{}`},
		{"backupdr.googleapis.com/BackupPlanAssociation", "v1", regional + "backupPlanAssociations/vm", p + "locations/-/backupPlanAssociations", "backupPlanAssociations", "us-central1", regional + "operations/delete", `{}`},
		{"backupdr.googleapis.com/BackupVault", "v1", regional + "backupVaults/vault", p + "locations/-/backupVaults", "backupVaults", "us-central1", regional + "operations/delete", `{"uid":"vault-uid","deletable":true}`},
		{"backupdr.googleapis.com/DataSource", "v1", regional + "backupVaults/vault/dataSources/vm", regional + "backupVaults/vault/dataSources", "dataSources", "us-central1", regional + "operations/delete", `{"dataSourceBackupApplianceApplication":{"applicationName":"vm"}}`},
		{"backupdr.googleapis.com/Backup", "v1", regional + "backupVaults/vault/dataSources/vm/backups/day", regional + "backupVaults/vault/dataSources/vm/backups", "backups", "us-central1", regional + "operations/delete", `{"enforcedRetentionEndTime":"2000-01-01T00:00:00Z"}`},
		{"dataplex.googleapis.com/Lake", "v1", regional + "lakes/data", regional + "lakes", "lakes", "us-central1", regional + "operations/delete", `{"uid":"lake-uid"}`},
		{"dataplex.googleapis.com/Zone", "v1", regional + "lakes/data/zones/raw", regional + "lakes/data/zones", "zones", "us-central1", regional + "operations/delete", `{"uid":"zone-uid"}`},
		{"dataplex.googleapis.com/Asset", "v1", regional + "lakes/data/zones/raw/assets/bucket", regional + "lakes/data/zones/raw/assets", "assets", "us-central1", regional + "operations/delete", `{"uid":"asset-uid"}`},
		{"datastream.googleapis.com/Stream", "v1", regional + "streams/cdc", regional + "streams", "streams", "us-central1", regional + "operations/delete", `{}`},
		{"datastream.googleapis.com/ConnectionProfile", "v1", regional + "connectionProfiles/postgres", regional + "connectionProfiles", "connectionProfiles", "us-central1", regional + "operations/delete", `{}`},
		{"datastream.googleapis.com/PrivateConnection", "v1", regional + "privateConnections/peering", regional + "privateConnections", "privateConnections", "us-central1", regional + "operations/delete", `{}`},
		{"dlp.googleapis.com/DeidentifyTemplate", "v2", global + "deidentifyTemplates/mask", global + "deidentifyTemplates", "deidentifyTemplates", "global", "", `{}`},
		{"dlp.googleapis.com/InspectTemplate", "v2", regional + "inspectTemplates/pii", regional + "inspectTemplates", "inspectTemplates", "us-central1", "", `{}`},
		{"domains.googleapis.com/Registration", "v1", p + "locations/global/registrations/example-com", p + "locations/global/registrations", "registrations", "global", p + "locations/global/operations/delete", `{"state":"EXPIRED"}`},
		{"networkconnectivity.googleapis.com/Hub", "v1", global + "hubs/transit", global + "hubs", "hubs", "global", global + "operations/delete", `{"uniqueId":"hub-uid"}`},
		{"networkconnectivity.googleapis.com/Spoke", "v1", global + "spokes/vpc", p + "locations/-/spokes", "spokes", "global", global + "operations/delete", `{"uniqueId":"spoke-uid"}`},
		{"networkmanagement.googleapis.com/VpcFlowLogsConfig", "v1", global + "vpcFlowLogsConfigs/audit", global + "vpcFlowLogsConfigs", "vpcFlowLogsConfigs", "global", global + "operations/delete", `{}`},
		{"networksecurity.googleapis.com/FirewallEndpoint", "v1", p + "locations/us-central1-a/firewallEndpoints/inspect", p + "locations/-/firewallEndpoints", "firewallEndpoints", "us-central1", p + "locations/us-central1-a/operations/delete", `{}`},
		{"networksecurity.googleapis.com/FirewallEndpointAssociation", "v1", p + "locations/us-central1-a/firewallEndpointAssociations/vpc", p + "locations/-/firewallEndpointAssociations", "firewallEndpointAssociations", "us-central1", p + "locations/us-central1-a/operations/delete", `{}`},
		{"networkservices.googleapis.com/Gateway", "v1", regional + "gateways/web", regional + "gateways", "gateways", "us-central1", regional + "operations/delete", `{}`},
		{"securitycenter.googleapis.com/NotificationConfig", "v1", p + "notificationConfigs/alerts", p + "notificationConfigs", "notificationConfigs", "global", "", `{}`},
		{"storagetransfer.googleapis.com/AgentPool", "v1", p + "agentPools/copy", p + "agentPools", "agentPools", "global", "", `{}`},

		{"networkservices.googleapis.com/EdgeCacheService", "v1", global + "edgeCacheServices/site", global + "edgeCacheServices", "edgeCacheServices", "global", global + "operations/delete", `{}`},
		{"networkservices.googleapis.com/EdgeCacheOrigin", "v1", global + "edgeCacheOrigins/static", global + "edgeCacheOrigins", "edgeCacheOrigins", "global", global + "operations/delete", `{}`},
		{"networkservices.googleapis.com/EdgeCacheKeyset", "v1", global + "edgeCacheKeysets/tokens", global + "edgeCacheKeysets", "edgeCacheKeysets", "global", global + "operations/delete", `{}`},
		{"networkservices.googleapis.com/MulticastDomain", "v1", global + "multicastDomains/market", global + "multicastDomains", "multicastDomains", "global", global + "operations/delete", `{"uniqueId":"domain-uid","state":{"state":"ACTIVE"}}`},
		{"networkservices.googleapis.com/MulticastDomainGroup", "v1", global + "multicastDomainGroups/group", global + "multicastDomainGroups", "multicastDomainGroups", "global", global + "operations/delete", `{"uniqueId":"group-uid"}`},
		{"networkservices.googleapis.com/MulticastGroupRange", "v1", global + "multicastGroupRanges/range", global + "multicastGroupRanges", "multicastGroupRanges", "global", global + "operations/delete", `{"uniqueId":"range-uid"}`},
		{"networkservices.googleapis.com/MulticastDomainActivation", "v1", p + "locations/us-central1-a/multicastDomainActivations/domain", p + "locations/us-central1-a/multicastDomainActivations", "multicastDomainActivations", "us-central1", p + "locations/us-central1-a/operations/delete", `{"uniqueId":"activation-uid"}`},
		{"networkservices.googleapis.com/MulticastGroupRangeActivation", "v1", p + "locations/us-central1-a/multicastGroupRangeActivations/range", p + "locations/us-central1-a/multicastGroupRangeActivations", "multicastGroupRangeActivations", "us-central1", p + "locations/us-central1-a/operations/delete", `{"uniqueId":"activation-uid"}`},
		{"networkservices.googleapis.com/MulticastProducerAssociation", "v1", p + "locations/us-central1-a/multicastProducerAssociations/producer", p + "locations/us-central1-a/multicastProducerAssociations", "multicastProducerAssociations", "us-central1", p + "locations/us-central1-a/operations/delete", `{"uniqueId":"association-uid"}`},
		{"networkservices.googleapis.com/MulticastGroupProducerActivation", "v1", p + "locations/us-central1-a/multicastGroupProducerActivations/producer", p + "locations/us-central1-a/multicastGroupProducerActivations", "multicastGroupProducerActivations", "us-central1", p + "locations/us-central1-a/operations/delete", `{"uniqueId":"activation-uid"}`},
		{"networkservices.googleapis.com/MulticastConsumerAssociation", "v1", p + "locations/us-central1-a/multicastConsumerAssociations/consumer", p + "locations/us-central1-a/multicastConsumerAssociations", "multicastConsumerAssociations", "us-central1", p + "locations/us-central1-a/operations/delete", `{"uniqueId":"association-uid"}`},
		{"networkservices.googleapis.com/MulticastGroupConsumerActivation", "v1", p + "locations/us-central1-a/multicastGroupConsumerActivations/consumer", p + "locations/us-central1-a/multicastGroupConsumerActivations", "multicastGroupConsumerActivations", "us-central1", p + "locations/us-central1-a/operations/delete", `{"uniqueId":"activation-uid"}`},
		{"networkconnectivity.googleapis.com/InternalRange", "v1", global + "internalRanges/multicast", global + "internalRanges", "internalRanges", "global", global + "operations/delete", `{"ipCidrRange":"239.0.0.0/23"}`},
	} {
		t.Run(test.kind+"/"+test.region, func(t *testing.T) {
			host := strings.Split(test.kind, "/")[0]
			data := map[string]any{}
			if err := json.Unmarshal([]byte(test.data), &data); err != nil {
				t.Fatal(err)
			}
			if _, exists := data["name"]; !exists && !strings.HasPrefix(test.kind, "bigquery.googleapis.com/") {
				data["name"] = test.name
			}
			deleted, polls, reads := false, 0, 0
			listPath := "/" + test.version + "/" + test.list
			targetPath := "/" + test.version + "/" + test.name
			transport := func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != host {
					t.Fatalf("foreign native host: %s", r.URL)
				}
				var response any = data
				if r.URL.Path == "/"+test.version+"/projects/sample-project/locations" && r.Method == "GET" {
					location := test.region
					parts := strings.Split(test.name, "/")
					for i, part := range parts {
						if part == "locations" && i+1 < len(parts) {
							location = parts[i+1]
							break
						}
					}
					return apiResponse(r, 200, `{"locations":[{"name":"projects/sample-project/locations/`+location+`","locationId":"`+location+`"}]}`), nil
				}
				switch {
				case r.URL.Path == listPath && r.Method == "GET":
					response = map[string]any{test.items: []any{data}}
					if test.kind == "bigtableadmin.googleapis.com/Table" && r.URL.Query().Get("view") != "REPLICATION_VIEW" {
						t.Fatalf("invalid table view: %s", r.URL)
					}
					if test.kind == "bigquery.googleapis.com/Dataset" && r.URL.Query().Get("all") != "true" {
						t.Fatalf("hidden datasets omitted: %s", r.URL)
					}
				case (r.URL.Path == targetPath && r.Method == "DELETE") || (test.kind == "backupdr.googleapis.com/DataSource" && r.URL.Path == targetPath+":remove" && r.Method == "POST"):
					if test.kind == "backupdr.googleapis.com/DataSource" {
						var body map[string]any
						if json.NewDecoder(r.Body).Decode(&body) != nil || body["requestId"] != googleRequestID("service-delete") {
							t.Fatal("native remove request body missing")
						}
					}
					if deleted {
						t.Fatal("duplicate delete")
					}
					deleted = true
					if test.kind == "alloydb.googleapis.com/Cluster" && r.URL.Query().Get("force") != "true" {
						t.Fatal("reviewed native AlloyDB cascade omitted")
					}
					if test.kind == "firestore.googleapis.com/Database" || test.kind == "iam.googleapis.com/Role" || test.kind == "run.googleapis.com/Job" {
						if r.URL.Query().Get("etag") != "reviewed-etag" {
							t.Fatalf("reviewed etag omitted: %s", r.URL)
						}
					}
					response = map[string]any{}
					if test.operation != "" {
						response = map[string]any{"name": test.operation, "done": false}
					}
				case test.operation != "" && r.URL.Path == "/"+test.version+"/"+test.operation && r.Method == "GET":
					polls++
					response = map[string]any{"name": test.operation, "done": polls > 1}
				case r.URL.Path == targetPath && r.Method == "GET":
					if test.kind == "bigtableadmin.googleapis.com/Table" && r.URL.Query().Get("view") != "FULL" {
						t.Fatalf("incomplete table detail view: %s", r.URL)
					}
					if deleted {
						reads++
						if reads > 1 {
							return apiResponse(r, 404, `{}`), nil
						}
					}
				default:
					if test.kind == "networkconnectivity.googleapis.com/Hub" && (r.URL.Path == targetPath+"/groups" || r.URL.Path == targetPath+"/routeTables") && r.Method == "GET" {
						return apiResponse(r, 200, `{}`), nil
					}
					childCollection := map[string]string{"spanner.googleapis.com/Instance": "databases", "alloydb.googleapis.com/Cluster": "instances", "servicedirectory.googleapis.com/Namespace": "services", "servicedirectory.googleapis.com/Service": "endpoints"}[test.kind]
					if test.kind == "managedkafka.googleapis.com/Cluster" {
						childCollection = "topics"
					}
					if test.kind == "bigtableadmin.googleapis.com/Instance" && (r.URL.Path == targetPath+"/clusters" || r.URL.Path == targetPath+"/tables") && r.Method == "GET" {
						return apiResponse(r, 200, `{}`), nil
					}
					if childCollection != "" && r.URL.Path == targetPath+"/"+childCollection && r.Method == "GET" {
						return apiResponse(r, 200, `{}`), nil
					}
					body, exists := serviceParentFixture(host, r.URL.Path)
					if !exists || r.Method != "GET" {
						t.Fatalf("unexpected native request: %s %s", r.Method, r.URL)
					}
					return apiResponse(r, 200, body), nil
				}
				encoded, _ := json.Marshal(response)
				return apiResponse(r, 200, string(encoded)), nil
			}
			runtime := protocolRuntime(t, transport)
			batch, err := runtime.List(context.Background(), productRequest(runtime, test.kind, test.region))
			if err != nil || !batch.Complete || len(batch.Items) != 1 {
				t.Fatalf("list=%+v error=%v", batch, err)
			}
			item := batch.Items[0]
			if strings.HasPrefix(test.kind, "bigquery.googleapis.com/") {
				value := asset.Asset{Identity: asset.Identity{NativeType: item.NativeType, NativeID: item.NativeID}, Normalized: item.Normalized}
				assertGCPPropertyQuery(t, runtime, []asset.Asset{value}, test.kind, test.name, `properties.name = "`+last(test.name)+`"`)
				if object(object(item.Raw["resource"])["data"])["name"] != nil {
					t.Fatal("BigQuery native response acquired a synthetic name", item.Raw)
				}
			}
			if test.kind == "networkservices.googleapis.com/MulticastDomain" && item.State != "ACTIVE" {
				t.Fatalf("structured multicast state lost: %s", item.State)
			}
			if item.NativeID != "//"+host+"/"+test.name || item.Actionable == nil || !*item.Actionable {
				t.Fatalf("invalid identity/action: %+v", item)
			}
			if test.region == "global" && item.Scope.Kind != asset.ScopeGlobal {
				t.Fatalf("logical global resource lost: %+v", item.Scope)
			}
			request := contracts.ActionRequest{Action: "delete", IdempotencyKey: "service-delete", Asset: asset.Asset{Identity: asset.Identity{NativeType: test.kind, NativeID: item.NativeID}, Normalized: item.Normalized}}
			driver := protocolAction(t, test.kind, test.name, transport)
			driver.identity = request.Asset.Identity
			result, err := driver.Execute(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			wantOperation := "https://" + host + "/" + test.version + "/" + strings.ReplaceAll(test.operation, "(default)", "%28default%29")
			if test.operation != "" && result.ProviderOperationID != wantOperation {
				t.Fatalf("operation scope changed: %+v", result)
			}
			steps := 2
			if test.operation != "" {
				steps = 3
			}
			for step := 0; step < steps; step++ {
				wait, err := driver.Wait(context.Background(), request, result)
				if err != nil || wait.Done != (step == steps-1) {
					t.Fatalf("wait %d: %+v error=%v", step, wait, err)
				}
			}
			if !deleted || reads != 2 {
				t.Fatalf("missing mutation or independent target read: deleted=%v reads=%d", deleted, reads)
			}
		})
	}
}

// Parent discovery uses native list responses, including scalar BigQuery IDs
// and a project-scoped Bigtable parent for a cluster located in a zone.
func serviceParentFixture(host, path string) (string, bool) {
	fixtures := map[string]string{
		"bigquery.googleapis.com/bigquery/v2/projects/sample-project/datasets/warehouse":                          `{"datasetReference":{"projectId":"sample-project","datasetId":"warehouse"},"location":"US"}`,
		"apphub.googleapis.com/v1/projects/sample-project/locations/global/applications":                          `{"applications":[{"name":"projects/sample-project/locations/global/applications/shop"}]}`,
		"backupdr.googleapis.com/v1/projects/sample-project/locations/-/backupVaults":                             `{"backupVaults":[{"name":"projects/sample-project/locations/us-central1/backupVaults/vault"}]}`,
		"backupdr.googleapis.com/v1/projects/sample-project/locations/us-central1/backupVaults/vault/dataSources": `{"dataSources":[{"name":"projects/sample-project/locations/us-central1/backupVaults/vault/dataSources/vm"}]}`,
		"dataplex.googleapis.com/v1/projects/sample-project/locations/us-central1/lakes":                          `{"lakes":[{"name":"projects/sample-project/locations/us-central1/lakes/data"}]}`,
		"dataplex.googleapis.com/v1/projects/sample-project/locations/us-central1/lakes/data/zones":               `{"zones":[{"name":"projects/sample-project/locations/us-central1/lakes/data/zones/raw"}]}`,

		"dns.googleapis.com/dns/v1/projects/sample-project/managedZones":                                            `{"managedZones":[{"name":"example","dnsName":"example.com."}]}`,
		"bigquery.googleapis.com/bigquery/v2/projects/sample-project/datasets":                                      `{"datasets":[{"datasetReference":{"projectId":"sample-project","datasetId":"warehouse"},"location":"US"}]}`,
		"bigtableadmin.googleapis.com/v2/projects/sample-project/instances":                                         `{"instances":[{"name":"projects/sample-project/instances/wide"}]}`,
		"spanner.googleapis.com/v1/projects/sample-project/instances":                                               `{"instances":[{"name":"projects/sample-project/instances/sql"}]}`,
		"alloydb.googleapis.com/v1/projects/sample-project/locations/us-central1/clusters":                          `{"clusters":[{"name":"projects/sample-project/locations/us-central1/clusters/pg"}]}`,
		"managedkafka.googleapis.com/v1/projects/sample-project/locations/us-central1/clusters":                     `{"clusters":[{"name":"projects/sample-project/locations/us-central1/clusters/broker"}]}`,
		"apigateway.googleapis.com/v1/projects/sample-project/locations/global/apis":                                `{"apis":[{"name":"projects/sample-project/locations/global/apis/orders"}]}`,
		"certificatemanager.googleapis.com/v1/projects/sample-project/locations/global/certificateMaps":             `{"certificateMaps":[{"name":"projects/sample-project/locations/global/certificateMaps/tls"}]}`,
		"iam.googleapis.com/v1/projects/sample-project/serviceAccounts":                                             `{"accounts":[{"name":"projects/sample-project/serviceAccounts/worker@sample-project.iam.gserviceaccount.com"}]}`,
		"servicedirectory.googleapis.com/v1/projects/sample-project/locations/us-central1/namespaces":               `{"namespaces":[{"name":"projects/sample-project/locations/us-central1/namespaces/apps"}]}`,
		"servicedirectory.googleapis.com/v1/projects/sample-project/locations/us-central1/namespaces/apps/services": `{"services":[{"name":"projects/sample-project/locations/us-central1/namespaces/apps/services/api"}]}`,
	}
	body, ok := fixtures[host+path]
	return body, ok
}

func TestServiceProtectionAndSoftDeletion(t *testing.T) {
	for _, test := range []struct{ kind, name, body, reason string }{
		{"firestore.googleapis.com/Database", "projects/sample-project/databases/(default)", `{"deleteProtectionState":"DELETE_PROTECTION_ENABLED"}`, "deletion_protection_enabled"},
		{"spanner.googleapis.com/Database", "projects/sample-project/instances/main/databases/app", `{"enableDropProtection":true}`, "deletion_protection_enabled"},
		{"bigtableadmin.googleapis.com/Table", "projects/sample-project/instances/main/tables/app", `{"name":"projects/sample-project/instances/main/tables/app","deletionProtection":true}`, "deletion_protection_enabled"},
		{"redis.googleapis.com/Cluster", "projects/sample-project/locations/us-central1/clusters/app", `{"deletionProtectionEnabled":true}`, "deletion_protection_enabled"},
		{"file.googleapis.com/Instance", "projects/sample-project/locations/us-central1-a/instances/app", `{"deletionProtectionEnabled":true}`, "deletion_protection_enabled"},
		{"logging.googleapis.com/LogBucket", "projects/sample-project/locations/global/buckets/app", `{"locked":true}`, "log_bucket_retention_locked"},
		{"logging.googleapis.com/LogBucket", "projects/sample-project/locations/global/buckets/_Required", `{"name":"projects/sample-project/locations/global/buckets/_Required"}`, "log_bucket_retention_locked"},
		{"iam.googleapis.com/ServiceAccountKey", "projects/sample-project/serviceAccounts/worker@sample-project.iam.gserviceaccount.com/keys/key", `{"keyType":"SYSTEM_MANAGED"}`, "system_managed_service_account_key"},
		{"managedkafka.googleapis.com/Topic", "projects/sample-project/locations/us-central1/clusters/broker/topics/__remote_log_metadata", `{"name":"projects/sample-project/locations/us-central1/clusters/broker/topics/__remote_log_metadata"}`, "internal_topic_requires_cluster_cleanup"},
		{"gkehub.googleapis.com/Membership", "projects/sample-project/locations/us-central1/memberships/external", `{"endpoint":{"onPremCluster":{}}}`, "membership_requires_native_cluster_unregister"},
	} {
		t.Run(test.kind+"/"+test.name, func(t *testing.T) {
			driver := protocolAction(t, test.kind, test.name, func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatalf("protected resource mutated: %s", r.URL)
				}
				return apiResponse(r, 200, test.body), nil
			})
			if test.kind == "bigtableadmin.googleapis.com/Table" {
				driver.identity.NativeID = "//bigtableadmin.googleapis.com/" + test.name
			}
			check, err := driver.Preflight(context.Background(), contracts.ActionRequest{Action: "delete"})
			if err != nil || check.Allowed || check.Reason != test.reason {
				t.Fatalf("check=%+v error=%v", check, err)
			}
			if _, err = driver.Execute(context.Background(), contracts.ActionRequest{Action: "delete"}); err == nil {
				t.Fatal("protected delete allowed")
			}
		})
	}
	for _, test := range []struct{ kind, name, body string }{
		{"iam.googleapis.com/Role", "projects/sample-project/roles/CustomRole", `{"name":"projects/sample-project/roles/CustomRole","deleted":true}`},
		{"logging.googleapis.com/LogBucket", "projects/sample-project/locations/global/buckets/archive", `{"name":"projects/sample-project/locations/global/buckets/archive","lifecycleState":"DELETE_REQUESTED"}`},
	} {
		t.Run(test.kind+"/soft_deleted", func(t *testing.T) {
			runtime := protocolRuntime(t, func(r *http.Request) (*http.Response, error) {
				items := "roles"
				if test.kind == "logging.googleapis.com/LogBucket" {
					items = "buckets"
				}
				return apiResponse(r, 200, `{"`+items+`":[`+test.body+`]}`), nil
			})
			batch, err := runtime.List(context.Background(), productRequest(runtime, test.kind, "global"))
			if err != nil || !batch.Complete || len(batch.Items) != 0 {
				t.Fatalf("deleted resource resurrected: %+v %v", batch, err)
			}
			driver := protocolAction(t, test.kind, test.name, func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatal("soft delete repeated")
				}
				return apiResponse(r, 200, test.body), nil
			})
			check, err := driver.Preflight(context.Background(), contracts.ActionRequest{Action: "delete"})
			if err != nil || !check.Allowed || !check.Absent {
				t.Fatalf("soft delete not recognized: %+v %v", check, err)
			}
			read, err := driver.Readback(context.Background(), contracts.ActionRequest{Action: "delete"})
			if err != nil || read.Exists || read.State != "soft_deleted" {
				t.Fatalf("soft delete misreported: %+v %v", read, err)
			}
		})
	}
}

func TestServiceListsRejectIncompleteAndMismatchedResources(t *testing.T) {
	for _, test := range []struct{ kind, region, body string }{
		{"bigtableadmin.googleapis.com/Instance", "global", `{"instances":[],"failedLocations":["us-central1-a"]}`},
		{"apigateway.googleapis.com/Api", "global", `{"apis":[],"unreachableLocations":["global"]}`},
		{"bigquery.googleapis.com/Dataset", "global", `{"datasets":[{"datasetReference":{"projectId":"foreign-project","datasetId":"same-name"}}]}`},
		{"bigquery.googleapis.com/Dataset", "global", `{"datasets":[{"datasetReference":{"datasetId":"missing-project"}}]}`},
		{"bigquery.googleapis.com/Table", "global", `{"tables":[{"tableReference":{"projectId":"sample-project","datasetId":"foreign-dataset","tableId":"same-name"}}]}`},
	} {
		t.Run(test.kind+test.body, func(t *testing.T) {
			runtime := protocolRuntime(t, func(r *http.Request) (*http.Response, error) {
				if test.kind == "bigquery.googleapis.com/Table" && strings.HasSuffix(r.URL.Path, "/datasets") {
					body, _ := serviceParentFixture(r.URL.Host, r.URL.Path)
					return apiResponse(r, 200, body), nil
				}
				return apiResponse(r, 200, test.body), nil
			})
			if batch, err := runtime.List(context.Background(), productRequest(runtime, test.kind, test.region)); err == nil || batch.Complete {
				t.Fatalf("unsafe scan completed: %+v %v", batch, err)
			}
		})
	}
}

func TestProjectScopeKeepsGlobalServiceLocations(t *testing.T) {
	for _, test := range []struct{ kind, path, body string }{
		{"apigateway.googleapis.com/Api", "/v1/projects/sample-project/locations/global/apis", `{"apis":[{"name":"projects/sample-project/locations/global/apis/example"}]}`},
		{"gkehub.googleapis.com/Fleet", "/v1/projects/sample-project/locations/global/fleets", `{"fleets":[{"name":"projects/sample-project/locations/global/fleets/default"}]}`},
		{"certificatemanager.googleapis.com/CertificateMap", "/v1/projects/sample-project/locations/global/certificateMaps", `{"certificateMaps":[{"name":"projects/sample-project/locations/global/certificateMaps/tls"}]}`},
	} {
		t.Run(test.kind, func(t *testing.T) {
			calls := 0
			runtime := protocolRuntime(t, func(r *http.Request) (*http.Response, error) {
				calls++
				if r.URL.Path != test.path {
					t.Fatalf("global resource fanned out to a region: %s", r.URL)
				}
				return apiResponse(r, 200, test.body), nil
			})
			batch, err := runtime.List(context.Background(), productRequest(runtime, test.kind, "project"))
			if err != nil || !batch.Complete || len(batch.Items) != 1 || calls != 1 {
				t.Fatalf("global project scan: %+v calls=%d error=%v", batch, calls, err)
			}
		})
	}
}

func TestServiceParentGenerationAndNativePagination(t *testing.T) {
	parentUID := "namespace-generation-1"
	calls := 0
	runtime := protocolRuntime(t, func(r *http.Request) (*http.Response, error) {
		if strings.HasSuffix(r.URL.Path, "/namespaces") {
			return apiResponse(r, 200, `{"namespaces":[{"name":"projects/sample-project/locations/us-central1/namespaces/apps","uid":"`+parentUID+`"}]}`), nil
		}
		calls++
		if r.URL.Query().Get("pageSize") != "100" {
			t.Fatalf("missing native page size: %s", r.URL)
		}
		if r.URL.Query().Get("pageToken") == "" {
			return apiResponse(r, 200, `{"services":[{"name":"projects/sample-project/locations/us-central1/namespaces/apps/services/api"}],"nextPageToken":"opaque-service-page"}`), nil
		}
		if r.URL.Query().Get("pageToken") != "opaque-service-page" {
			t.Fatal("native cursor changed")
		}
		return apiResponse(r, 200, `{}`), nil
	})
	request := productRequest(runtime, "servicedirectory.googleapis.com/Service", "us-central1")
	first, err := runtime.List(context.Background(), request)
	if err != nil || first.Complete || first.NextCursor == "" || len(first.Items) != 1 {
		t.Fatalf("first page: %+v %v", first, err)
	}
	request.Cursor = first.NextCursor
	last, err := runtime.List(context.Background(), request)
	if err != nil || !last.Complete || calls != 2 {
		t.Fatalf("second page: %+v %v", last, err)
	}
	parentUID = "namespace-generation-2"
	if _, err := runtime.List(context.Background(), request); err == nil {
		t.Fatal("cursor crossed recreated parent")
	}
	if calls != 2 {
		t.Fatal("child API called for stale parent generation")
	}
}

func TestServiceSecretsAndProjectReferences(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	data := map[string]any{
		"authorizedNetwork":     "projects/sample-project/global/networks/vpc",
		"kmsKeyName":            "projects/sample-project/locations/us-central1/keyRings/ring/cryptoKeys/key",
		"apiConfig":             "projects/sample-project/locations/global/apis/orders/configs/v1",
		"gatewayServiceAccount": "worker@sample-project.iam.gserviceaccount.com",
		"certificates":          []any{"projects/sample-project/locations/global/certificates/tls"},
		"serviceConfig":         map[string]any{"service": "projects/sample-project/locations/us-central1/services/function"},
		"privateKeyData":        "PRIVATE_VALUE_KEY", "authString": "PRIVATE_VALUE_AUTH",
		"httpTarget": map[string]any{"headers": map[string]any{"ordinary": "PRIVATE_VALUE_HEADER"}},
		"httpCheck":  map[string]any{"headers": map[string]any{"X-Key": "PRIVATE_VALUE_MONITOR"}},
	}
	refs := references(c, data)
	for _, kind := range []string{"compute.googleapis.com/Network", "cloudkms.googleapis.com/CryptoKey", "apigateway.googleapis.com/ApiConfig", "iam.googleapis.com/ServiceAccount", "certificatemanager.googleapis.com/Certificate", "run.googleapis.com/Service"} {
		if len(refs[kind]) != 1 {
			t.Fatalf("missing native reference %s: %v", kind, refs)
		}
	}
	clean, _ := json.Marshal(safePayload(data))
	if strings.Contains(string(clean), "PRIVATE_VALUE") {
		t.Fatal("service secret escaped")
	}
	data["apiConfig"] = "projects/foreign-project/locations/global/apis/orders/configs/v1"
	if len(references(c, data)["apigateway.googleapis.com/ApiConfig"]) != 0 {
		t.Fatal("foreign project reference accepted")
	}
}

func TestIAPTunnelUsesProjectNumberAndCanonicalIdentity(t *testing.T) {
	const native = "projects/123456/iap_tunnel/locations/us-central1/destGroups/private"
	const canonical = "projects/sample-project/iap_tunnel/locations/us-central1/destGroups/private"
	deleted := false
	transport := func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != "iap.googleapis.com" {
			t.Fatalf("foreign IAP request: %s", r.URL)
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/projects/123456/iap_tunnel/locations/us-central1/destGroups":
			return apiResponse(r, 200, `{"tunnelDestGroups":[{"name":"`+native+`","cidrs":["10.0.0.0/8"]}]}`), nil
		case r.Method == "GET" && r.URL.Path == "/v1/"+native:
			if deleted {
				return apiResponse(r, 404, `{}`), nil
			}
			return apiResponse(r, 200, `{"name":"`+native+`"}`), nil
		case r.Method == "DELETE" && r.URL.Path == "/v1/"+native:
			deleted = true
			return apiResponse(r, 200, `{}`), nil
		default:
			t.Fatalf("IAP scope changed: %s %s", r.Method, r.URL)
			return nil, nil
		}
	}
	runtime := protocolRuntime(t, transport)
	page, err := runtime.List(context.Background(), productRequest(runtime, "iap.googleapis.com/TunnelDestGroup", "us-central1"))
	if err != nil || len(page.Items) != 1 || page.Items[0].NativeID != "//iap.googleapis.com/"+canonical {
		t.Fatalf("IAP identity: %+v %v", page, err)
	}
	driver := protocolAction(t, "iap.googleapis.com/TunnelDestGroup", canonical, transport)
	request := contracts.ActionRequest{Action: "delete"}
	result, err := driver.Execute(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	wait, err := driver.Wait(context.Background(), request, result)
	if err != nil || !wait.Done || !deleted {
		t.Fatalf("IAP delete %+v %v", wait, err)
	}
}

func TestBackupRetentionLocksAndRegistrationStates(t *testing.T) {
	const backup = "projects/sample-project/locations/us-central1/backupVaults/vault/dataSources/source/backups/day"
	for _, test := range []struct{ name, body, reason string }{
		{"retention", `{"enforcedRetentionEndTime":"2999-01-01T00:00:00Z"}`, "backup_retention_active"},
		{"malformed_retention", `{"enforcedRetentionEndTime":"bad"}`, "backup_retention_active"},
		{"service_lock", `{"serviceLocks":[{"lockUntilTime":"2999-01-01T00:00:00Z"}]}`, "backup_locked"},
		{"appliance_lock", `{"backupApplianceLocks":[{"lockUntilTime":"2999-01-01T00:00:00Z"}]}`, "backup_locked"},
		{"invalid_lock", `{"serviceLocks":[{}]}`, "backup_locked"},
		{"expired", `{"enforcedRetentionEndTime":"2000-01-01T00:00:00Z","serviceLocks":[{"lockUntilTime":"2000-01-01T00:00:00Z"}]}`, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			driver := protocolAction(t, "backupdr.googleapis.com/Backup", backup, func(r *http.Request) (*http.Response, error) {
				if r.Method != "GET" {
					t.Fatal("protected backup mutated")
				}
				return apiResponse(r, 200, test.body), nil
			})
			check, err := driver.Preflight(context.Background(), contracts.ActionRequest{Action: "delete"})
			if err != nil || check.Reason != test.reason || check.Allowed != (test.reason == "") {
				t.Fatalf("backup protection %+v %v", check, err)
			}
		})
	}
	for _, state := range []string{"ACTIVE", "SUSPENDED", "REGISTRATION_PENDING", "EXPORTED", "EXPIRED", "REGISTRATION_FAILED", "TRANSFER_FAILED"} {
		driver := protocolAction(t, "domains.googleapis.com/Registration", "projects/sample-project/locations/global/registrations/example-com", func(r *http.Request) (*http.Response, error) {
			return apiResponse(r, 200, `{"state":"`+state+`"}`), nil
		})
		check, err := driver.Preflight(context.Background(), contracts.ActionRequest{Action: "delete"})
		allowed := state == "EXPORTED" || state == "EXPIRED" || state == "REGISTRATION_FAILED" || state == "TRANSFER_FAILED"
		if err != nil || check.Allowed != allowed {
			t.Fatalf("registration %s %+v %v", state, check, err)
		}
	}
}

func TestManagedServiceDependencyFormatsAndSecrets(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	for _, test := range []struct{ data, kind, id string }{
		{`{"sourceConfig":{"sourceConnectionProfile":"projects/123456/locations/us-central1/connectionProfiles/db"}}`, "datastream.googleapis.com/ConnectionProfile", "projects/sample-project/locations/us-central1/connectionProfiles/db"},
		{`{"destinationConfig":{"destinationConnectionProfile":"projects/sample-project/locations/us-central1/connectionProfiles/warehouse"}}`, "datastream.googleapis.com/ConnectionProfile", "projects/sample-project/locations/us-central1/connectionProfiles/warehouse"},
		{`{"privateConnectivity":{"privateConnection":"projects/sample-project/locations/us-central1/privateConnections/private"}}`, "datastream.googleapis.com/PrivateConnection", "projects/sample-project/locations/us-central1/privateConnections/private"},
		{`{"hub":"projects/sample-project/locations/global/hubs/transit"}`, "networkconnectivity.googleapis.com/Hub", "projects/sample-project/locations/global/hubs/transit"},
		{`{"firewallEndpoint":"projects/sample-project/locations/us-central1-a/firewallEndpoints/inspect"}`, "networksecurity.googleapis.com/FirewallEndpoint", "projects/sample-project/locations/us-central1-a/firewallEndpoints/inspect"},
		{`{"resourceSpec":{"type":"STORAGE_BUCKET","name":"projects/123456/buckets/archive"}}`, "storage.googleapis.com/Bucket", "archive"},
		{`{"resourceSpec":{"type":"BIGQUERY_DATASET","name":"projects/123456/datasets/events"}}`, "bigquery.googleapis.com/Dataset", "projects/sample-project/datasets/events"},
		{`{"destination":"pubsub.googleapis.com/projects/123456/topics/audit"}`, "pubsub.googleapis.com/Topic", "projects/sample-project/topics/audit"},
		{`{"destination":"logging.googleapis.com/projects/sample-project/locations/global/buckets/archive"}`, "logging.googleapis.com/LogBucket", "projects/sample-project/locations/global/buckets/archive"},
		{`{"destination":"bigquery.googleapis.com/projects/sample-project/datasets/archive"}`, "bigquery.googleapis.com/Dataset", "projects/sample-project/datasets/archive"},
		{`{"destination":"storage.googleapis.com/archive"}`, "storage.googleapis.com/Bucket", "archive"},
		{`{"serviceAccount":"123456-compute@developer.gserviceaccount.com"}`, "iam.googleapis.com/ServiceAccount", "projects/sample-project/serviceAccounts/123456-compute@developer.gserviceaccount.com"},
		{`{"resourceSpec":{"type":"STORAGE_BUCKET","name":"projects/foreign-project/buckets/archive"}}`, "storage.googleapis.com/Bucket", ""},
		{`{"sourceConfig":{"sourceConnectionProfile":"projects/foreign-project/locations/us-central1/connectionProfiles/db"}}`, "datastream.googleapis.com/ConnectionProfile", ""},
	} {
		var data map[string]any
		_ = json.Unmarshal([]byte(test.data), &data)
		refs := references(c, data)[test.kind]
		if test.id == "" {
			if len(refs) != 0 {
				t.Fatalf("foreign service dependency: %v", refs)
			}
		} else if len(refs) != 1 || refs[0] != "//"+strings.Split(test.kind, "/")[0]+"/"+test.id {
			t.Fatalf("missing native relationship: %s %v", test.data, refs)
		}
	}
	data := map[string]any{"deidentifyConfig": map[string]any{"cryptoKey": map[string]any{"unwrapped": map[string]any{"key": "SECRET_DLP"}, "kmsWrapped": map[string]any{"wrappedKey": "SECRET_WRAPPED", "cryptoKeyName": "projects/sample-project/locations/global/keyRings/ring/cryptoKeys/kek"}}}}
	encoded, _ := json.Marshal(safePayload(data))
	if strings.Contains(string(encoded), "SECRET_") || !strings.Contains(string(encoded), "cryptoKeyName") {
		t.Fatalf("DLP secret redaction: %s", encoded)
	}
}

func TestMediaCDNAndMulticastNativeDependencies(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	for _, test := range []struct{ data, kind, name string }{
		{`{"adminNetwork":"projects/123456/locations/global/networks/admin"}`, "compute.googleapis.com/Network", "projects/sample-project/global/networks/admin"},
		{`{"connection":{"nccHub":"projects/sample-project/locations/global/hubs/transit"}}`, "networkconnectivity.googleapis.com/Hub", "projects/sample-project/locations/global/hubs/transit"},
		{`{"multicastDomainGroup":"projects/sample-project/locations/global/multicastDomainGroups/group"}`, "networkservices.googleapis.com/MulticastDomainGroup", "projects/sample-project/locations/global/multicastDomainGroups/group"},
		{`{"multicastDomain":"projects/sample-project/locations/global/multicastDomains/market"}`, "networkservices.googleapis.com/MulticastDomain", "projects/sample-project/locations/global/multicastDomains/market"},
		{`{"reservedInternalRange":"projects/sample-project/locations/global/internalRanges/range"}`, "networkconnectivity.googleapis.com/InternalRange", "projects/sample-project/locations/global/internalRanges/range"},
		{`{"multicastDomainActivation":"projects/sample-project/locations/us-central1-a/multicastDomainActivations/domain"}`, "networkservices.googleapis.com/MulticastDomainActivation", "projects/sample-project/locations/us-central1-a/multicastDomainActivations/domain"},
		{`{"multicastGroupRange":"projects/sample-project/locations/global/multicastGroupRanges/range"}`, "networkservices.googleapis.com/MulticastGroupRange", "projects/sample-project/locations/global/multicastGroupRanges/range"},
		{`{"multicastGroupRangeActivation":"projects/sample-project/locations/us-central1-a/multicastGroupRangeActivations/range"}`, "networkservices.googleapis.com/MulticastGroupRangeActivation", "projects/sample-project/locations/us-central1-a/multicastGroupRangeActivations/range"},
		{`{"multicastProducerAssociation":"projects/sample-project/locations/us-central1-a/multicastProducerAssociations/producer"}`, "networkservices.googleapis.com/MulticastProducerAssociation", "projects/sample-project/locations/us-central1-a/multicastProducerAssociations/producer"},
		{`{"multicastConsumerAssociation":"projects/sample-project/locations/us-central1-a/multicastConsumerAssociations/consumer"}`, "networkservices.googleapis.com/MulticastConsumerAssociation", "projects/sample-project/locations/us-central1-a/multicastConsumerAssociations/consumer"},
		{`{"placementPolicy":"projects/sample-project/regions/us-central1/resourcePolicies/placement"}`, "compute.googleapis.com/ResourcePolicy", "projects/sample-project/regions/us-central1/resourcePolicies/placement"},
		{`{"routing":{"pathMatchers":[{"routeRules":[{"origin":"storage"}]}]}}`, "networkservices.googleapis.com/EdgeCacheOrigin", "projects/sample-project/locations/global/edgeCacheOrigins/storage"},
		{`{"failoverOrigin":"secondary"}`, "networkservices.googleapis.com/EdgeCacheOrigin", "projects/sample-project/locations/global/edgeCacheOrigins/secondary"},
		{`{"signedRequestKeyset":"signed"}`, "networkservices.googleapis.com/EdgeCacheKeyset", "projects/sample-project/locations/global/edgeCacheKeysets/signed"},
		{`{"keyset":"projects/123456/locations/global/edgeCacheKeysets/response"}`, "networkservices.googleapis.com/EdgeCacheKeyset", "projects/sample-project/locations/global/edgeCacheKeysets/response"},
		{`{"edgeSslCertificates":["media-cert"]}`, "certificatemanager.googleapis.com/Certificate", "projects/sample-project/locations/global/certificates/media-cert"},
		{`{"edgeSecurityPolicy":"armor"}`, "compute.googleapis.com/SecurityPolicy", "projects/sample-project/global/securityPolicies/armor"},
		{`{"originAddress":"gs://media-bucket"}`, "storage.googleapis.com/Bucket", "media-bucket"},
		{`{"originAddress":"media-bucket.storage.googleapis.com"}`, "storage.googleapis.com/Bucket", "media-bucket"},
		{`{"validationSharedKeys":[{"secretVersion":"projects/123456/secrets/key/versions/1"}]}`, "secretmanager.googleapis.com/Secret", "projects/sample-project/secrets/key"},
		{`{"awsV4Authentication":{"secretAccessKeyVersion":"projects/sample-project/secrets/aws/versions/latest"}}`, "secretmanager.googleapis.com/Secret", "projects/sample-project/secrets/aws"},
		{`{"linkedVpnTunnels":{"uris":["projects/sample-project/regions/us-central1/vpnTunnels/vpn"]}}`, "compute.googleapis.com/VpnTunnel", "projects/sample-project/regions/us-central1/vpnTunnels/vpn"},
		{`{"linkedInterconnectAttachments":{"uris":["projects/sample-project/regions/us-central1/interconnectAttachments/private"]}}`, "compute.googleapis.com/InterconnectAttachment", "projects/sample-project/regions/us-central1/interconnectAttachments/private"},
		{`{"resourceType":"compute.googleapis.com/Instance","resource":"projects/123456/zones/us-central1-a/instances/vm"}`, "compute.googleapis.com/Instance", "projects/sample-project/zones/us-central1-a/instances/vm"},
	} {
		var data map[string]any
		_ = json.Unmarshal([]byte(test.data), &data)
		refs := references(c, data)[test.kind]
		if len(refs) != 1 || refs[0] != "//"+strings.Split(test.kind, "/")[0]+"/"+test.name {
			t.Fatalf("native dependency lost: %s -> %v", test.data, refs)
		}
	}
	encoded, _ := json.Marshal(safePayload(map[string]any{"headerAction": map[string]any{"requestHeadersToAdd": []any{map[string]any{"headerName": "Authorization", "headerValue": "SECRET_AUTH"}}}, "customRequestHeaders": []any{"Authorization: SECRET_VALUE"}}))
	if strings.Contains(string(encoded), "SECRET_") {
		t.Fatalf("custom header secret persisted: %s", encoded)
	}
}
