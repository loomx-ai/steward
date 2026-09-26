package gcp

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

// Triggers that Cloud Run functions and other services create are removed
// through their owner; user triggers stay deletable.
func TestEventarcServiceManagedTriggersAreProtected(t *testing.T) {
	managed := map[string]any{"labels": map[string]any{"goog-managed-by": "cloudfunctions"}}
	if reason := protectionReason(eventarcTriggerType, managed); reason != "trigger_managed_by_service" {
		t.Fatalf("managed trigger reason = %q", reason)
	}
	if reason := protectionReason(eventarcTriggerType, map[string]any{"labels": map[string]any{"team": "a"}}); reason != "" {
		t.Fatalf("user trigger reason = %q", reason)
	}
}

// An enrollment depends on its message bus and the pipeline it delivers to.
func TestEventarcEnrollmentReferencesBusAndPipeline(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	const location = "projects/sample-project/locations/us-central1/"
	refs := references(c, map[string]any{
		"name": location + "enrollments/paid", "messageBus": location + "messageBuses/orders", "destination": location + "pipelines/fulfil",
	})
	if !slices.Equal(refs["eventarc.googleapis.com/MessageBus"], []string{"//eventarc.googleapis.com/" + location + "messageBuses/orders"}) ||
		!slices.Equal(refs["eventarc.googleapis.com/Pipeline"], []string{"//eventarc.googleapis.com/" + location + "pipelines/fulfil"}) {
		t.Fatalf("references = %v", refs)
	}
}

// A deleted API key remains readable with deleteTime until it is purged; that
// state is deletion, not a live key.
func TestDeletedAPIKeysAreSoftDeleted(t *testing.T) {
	if !resourceSoftDeleted(apiKeyType, map[string]any{"deleteTime": "2026-09-26T00:00:00Z"}) || resourceSoftDeleted(apiKeyType, map[string]any{"uid": "key"}) {
		t.Fatal("API key soft-delete state is not recognized")
	}
}

// Read-only types list through their native APIs: Dataflow jobs by job ID,
// Cloud Deploy releases and certificate authorities under their parent.
func TestReadOnlyServiceResourcesList(t *testing.T) {
	const regional = "projects/sample-project/locations/us-central1/"
	for _, test := range []struct{ kind, host, list, items, name string }{
		{"dataflow.googleapis.com/Job", "dataflow.googleapis.com", "/v1b3/" + regional + "jobs", "jobs", regional + "jobs/2026-09-26_01"},
		{"clouddeploy.googleapis.com/Release", "clouddeploy.googleapis.com", "/v1/" + regional + "deliveryPipelines/app/releases", "releases", regional + "deliveryPipelines/app/releases/r1"},
		{certificateAuthorityType, "privateca.googleapis.com", "/v1/" + regional + "caPools/internal/certificateAuthorities", "certificateAuthorities", regional + "caPools/internal/certificateAuthorities/root"},
	} {
		t.Run(test.kind, func(t *testing.T) {
			record := map[string]any{"name": test.name, "state": "ENABLED"}
			if test.kind == "dataflow.googleapis.com/Job" {
				record = map[string]any{"id": "2026-09-26_01", "name": "etl", "projectId": "sample-project", "location": "us-central1", "currentState": "JOB_STATE_RUNNING"}
			}
			parents := map[string]string{
				"/v1/" + regional + "deliveryPipelines": `{"deliveryPipelines":[{"name":"` + regional + `deliveryPipelines/app"}]}`,
				"/v1/" + regional + "caPools":           `{"caPools":[{"name":"` + regional + `caPools/internal"}]}`,
			}
			runtime := protocolRuntime(t, func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != test.host || r.Method != "GET" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
				}
				switch {
				case r.URL.Path == test.list:
					encoded, _ := json.Marshal(map[string]any{test.items: []any{record}})
					return apiResponse(r, 200, string(encoded)), nil
				case strings.HasSuffix(r.URL.Path, "/locations"):
					return apiResponse(r, 200, `{"locations":[{"name":"projects/sample-project/locations/us-central1","locationId":"us-central1"}]}`), nil
				case parents[r.URL.Path] != "":
					return apiResponse(r, 200, parents[r.URL.Path]), nil
				case strings.HasSuffix(r.URL.Path, "/"+last(test.name)):
					encoded, _ := json.Marshal(record)
					return apiResponse(r, 200, string(encoded)), nil
				}
				t.Fatalf("unexpected request: %s", r.URL)
				return nil, nil
			})
			batch, err := runtime.List(context.Background(), productRequest(runtime, test.kind, "us-central1"))
			if err != nil || !batch.Complete || len(batch.Items) != 1 {
				t.Fatalf("list=%+v error=%v", batch, err)
			}
			if item := batch.Items[0]; item.NativeID != "//"+test.host+"/"+test.name || item.Actionable == nil || *item.Actionable {
				t.Fatalf("identity or action = %s %v", item.NativeID, item.Actionable)
			}
		})
	}
}

// Deletes that need an undeploy, cancel or replication change first are
// protected instead of being attempted.
func TestServiceResourcesNeedingStateChangesAreProtected(t *testing.T) {
	for _, test := range []struct {
		kind string
		data map[string]any
		want string
	}{
		{indexEndpointType, map[string]any{"deployedIndexes": []any{map[string]any{"id": "live"}}}, "index_endpoint_has_deployed_indexes"},
		{indexEndpointType, map[string]any{}, ""},
		{trainingPipelineType, map[string]any{"state": "PIPELINE_STATE_RUNNING"}, "training_pipeline_not_finished"},
		{trainingPipelineType, map[string]any{"state": "PIPELINE_STATE_FAILED"}, ""},
		{workbenchInstanceType, map[string]any{"enableDeletionProtection": true}, "deletion_protection_enabled"},
		{netappVolumeType, map[string]any{"hasReplication": true}, "volume_has_replication"},
		{netappVolumeType, map[string]any{"hasReplication": false}, ""},
	} {
		if got := protectionReason(test.kind, test.data); got != test.want {
			t.Fatalf("%s %v: reason = %q, want %q", test.kind, test.data, got, test.want)
		}
	}
}

func TestBatchEReferences(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	const location = "projects/sample-project/locations/us-central1/"
	for _, test := range []struct {
		data         map[string]any
		target, want string
	}{
		{map[string]any{"name": location + "services/api", "template": map[string]any{"vpcAccess": map[string]any{"connector": location + "connectors/serverless"}}}, vpcConnectorType, "//vpcaccess.googleapis.com/" + location + "connectors/serverless"},
		{map[string]any{"name": location + "functions/fn", "serviceConfig": map[string]any{"vpcConnector": location + "connectors/serverless"}}, vpcConnectorType, "//vpcaccess.googleapis.com/" + location + "connectors/serverless"},
		{map[string]any{"name": location + "jobs/nightly", "pubsubTarget": map[string]any{"topicName": "projects/sample-project/topics/events"}}, "pubsub.googleapis.com/Topic", "//pubsub.googleapis.com/projects/sample-project/topics/events"},
		{map[string]any{"name": location + "environments/airflow", "config": map[string]any{"dagGcsPrefix": "gs://us-central1-airflow-bucket/dags"}}, "storage.googleapis.com/Bucket", "//storage.googleapis.com/us-central1-airflow-bucket"},
		{map[string]any{"name": location + "deliveryPipelines/app", "serialPipeline": map[string]any{"stages": []any{map[string]any{"targetId": "prod"}}}}, "clouddeploy.googleapis.com/Target", "//clouddeploy.googleapis.com/" + location + "targets/prod"},
		{map[string]any{"name": location + "volumes/data", "storagePool": "pool"}, netappStoragePoolType, "//netapp.googleapis.com/" + location + "storagePools/pool"},
	} {
		if refs := references(c, test.data); !slices.Equal(refs[test.target], []string{test.want}) {
			t.Fatalf("%v: references = %v", test.data["name"], refs)
		}
	}
}

// Composer and Workbench delete the cluster and VM they run on; a CA pool and
// a NetApp storage pool need their authorities and volumes deleted first.
func TestServiceOwnersBindManagedResourcesAndRequiredChildren(t *testing.T) {
	gcpAsset := func(id, kind, native string, normalized map[string]any) asset.Asset {
		if normalized == nil {
			normalized = map[string]any{}
		}
		normalized["project_id"] = "sample-project"
		return asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "c", Partition: "gcp", NativeType: kind, NativeID: native}, Normalized: normalized}
	}
	const location = "projects/sample-project/locations/us-central1/"
	assets := []asset.Asset{
		gcpAsset("env", composerEnvironmentType, "//composer.googleapis.com/"+location+"environments/airflow", map[string]any{"config": map[string]any{"gkeCluster": location + "clusters/airflow-gke"}}),
		gcpAsset("cluster", clusterType, "//container.googleapis.com/"+location+"clusters/airflow-gke", nil),
		gcpAsset("notebook", workbenchInstanceType, "//notebooks.googleapis.com/projects/sample-project/locations/us-central1-a/instances/lab", nil),
		gcpAsset("vm", instanceType, "//compute.googleapis.com/projects/sample-project/zones/us-central1-a/instances/lab", nil),
		gcpAsset("pool", caPoolType, "//privateca.googleapis.com/"+location+"caPools/internal", nil),
		gcpAsset("ca", certificateAuthorityType, "//privateca.googleapis.com/"+location+"caPools/internal/certificateAuthorities/root", nil),
		gcpAsset("storage", netappStoragePoolType, "//netapp.googleapis.com/"+location+"storagePools/pool", nil),
		gcpAsset("volume", netappVolumeType, "//netapp.googleapis.com/"+location+"volumes/data", map[string]any{"storagePool": "pool"}),
		gcpAsset("other", netappVolumeType, "//netapp.googleapis.com/"+location+"volumes/other", map[string]any{"storagePool": "elsewhere"}),
	}
	result, err := NewServiceOwners().Contribute(context.Background(), "", assets)
	if err != nil {
		t.Fatal(err)
	}
	bindings := map[string]string{}
	for _, binding := range result.Bindings {
		if binding.CleanupPolicy != graph.CleanupDelegate || binding.DirectCleanupAllowed || binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] != true {
			t.Fatalf("binding = %+v", binding)
		}
		bindings[string(binding.ControllerAssetID)] = string(binding.ManagedAssetID)
	}
	if len(bindings) != 2 || bindings["env"] != "cluster" || bindings["notebook"] != "vm" {
		t.Fatalf("bindings = %v", bindings)
	}
	required := map[string]string{}
	for _, relationship := range result.Relationships {
		if relationship.Evidence[graph.RelationshipEvidenceRequiredDeletion] == true {
			if relationship.Evidence[graph.RelationshipEvidenceAutomaticSelection] != false || relationship.Evidence[graph.RelationshipEvidenceDeletionOrder] != graph.DeletionOrderTargetBeforeSource {
				t.Fatalf("relationship = %+v", relationship)
			}
			required[string(relationship.SourceAssetID)] = string(relationship.TargetAssetID)
		}
	}
	if len(required) != 2 || required["pool"] != "ca" || required["storage"] != "volume" {
		t.Fatalf("required = %v", required)
	}
}
