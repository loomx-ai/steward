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
		{"bigquery.googleapis.com/Dataset", "bigquery/v2", p + "datasets/warehouse", p + "datasets", "datasets", "global", "", `{"name":"","datasetReference":{"projectId":"sample-project","datasetId":"warehouse"},"location":"US"}`},
		{"bigquery.googleapis.com/Table", "bigquery/v2", p + "datasets/warehouse/tables/events", p + "datasets/warehouse/tables", "tables", "global", "", `{"name":"","tableReference":{"projectId":"sample-project","datasetId":"warehouse","tableId":"events"},"location":"EU"}`},
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
	} {
		t.Run(test.kind+"/"+test.region, func(t *testing.T) {
			host := strings.Split(test.kind, "/")[0]
			data := map[string]any{}
			if err := json.Unmarshal([]byte(test.data), &data); err != nil {
				t.Fatal(err)
			}
			if _, exists := data["name"]; !exists {
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
				switch {
				case r.URL.Path == listPath && r.Method == "GET":
					response = map[string]any{test.items: []any{data}}
					if test.kind == "bigtableadmin.googleapis.com/Table" && r.URL.Query().Get("view") != "REPLICATION_VIEW" {
						t.Fatalf("invalid table view: %s", r.URL)
					}
					if test.kind == "bigquery.googleapis.com/Dataset" && r.URL.Query().Get("all") != "true" {
						t.Fatalf("hidden datasets omitted: %s", r.URL)
					}
				case r.URL.Path == targetPath && r.Method == "DELETE":
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
					if deleted {
						reads++
						if reads > 1 {
							return apiResponse(r, 404, `{}`), nil
						}
					}
				default:
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
			if item.NativeID != "//"+host+"/"+test.name || item.Actionable == nil || !*item.Actionable {
				t.Fatalf("invalid identity/action: %+v", item)
			}
			if test.region == "global" && item.Scope.Kind != asset.ScopeGlobal {
				t.Fatalf("logical global resource lost: %+v", item.Scope)
			}
			request := contracts.ActionRequest{Action: "delete", IdempotencyKey: "service-delete", Asset: asset.Asset{Identity: asset.Identity{NativeType: test.kind, NativeID: item.NativeID}, Normalized: item.Normalized}}
			driver := protocolAction(t, test.kind, test.name, transport)
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
		{"bigtableadmin.googleapis.com/Table", "projects/sample-project/instances/main/tables/app", `{"deletionProtection":true}`, "deletion_protection_enabled"},
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
