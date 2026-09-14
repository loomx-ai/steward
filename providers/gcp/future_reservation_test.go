package gcp

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const futureReservationTestType = "compute.googleapis.com/FutureReservation"

// Synthetic native responses, validated against the pinned official Discovery
// schemas below. They are not recordings of successful cloud provisioning.
func futureReservationFixture(zone, name string) map[string]any {
	root := "https://compute.googleapis.com/compute/v1/projects/sample-project/zones/" + zone
	return map[string]any{
		"kind": "compute#futureReservation", "id": "9007199254740993", "name": name, "selfLink": root + "/futureReservations/" + name, "zone": root,
		"description": "future capacity", "creationTimestamp": "2026-09-01T00:00:00Z", "planningStatus": "SUBMITTED",
		"timeWindow":            map[string]any{"startTime": "2030-01-01T00:00:00Z", "endTime": "2030-02-01T00:00:00Z"},
		"specificSkuProperties": map[string]any{"totalCount": "9007199254740993", "sourceInstanceTemplate": "https://compute.googleapis.com/compute/v1/projects/sample-project/global/instanceTemplates/original"},
		"status": map[string]any{"procurementStatus": "FAILED_PARTIALLY_FULFILLED", "fulfilledCount": "9007199254740992", "lockTime": "2029-12-01T00:00:00Z", "amendmentStatus": "AMENDMENT_DECLINED",
			"autoCreatedReservations":   []any{root + "/reservations/generated", "https://compute.googleapis.com/compute/v1/projects/shared-project/zones/" + zone + "/reservations/shared"},
			"existingMatchingUsageInfo": map[string]any{"count": "2", "timestamp": "2029-12-01T00:00:00Z"},
			"specificSkuProperties":     map[string]any{"sourceInstanceTemplateId": "9007199254740995"},
			"lastKnownGoodState":        map[string]any{"procurementStatus": "APPROVED", "futureReservationSpecs": map[string]any{"specificSkuProperties": map[string]any{"totalCount": "1"}}}},
		"autoDeleteAutoCreatedReservations": true, "autoCreatedReservationsDuration": map[string]any{"seconds": "86400", "nanos": 0},
		"reservationMode": "DEFAULT", "specificReservationRequired": true, "namePrefix": "generated",
		"commitmentInfo": map[string]any{"commitmentPlan": "TWELVE_MONTH", "previousCommitmentTerms": "EXTEND"},
	}
}

func futureReservationStorageFixture() map[string]any {
	data := futureReservationFixture("us-central1-a", "storage-capacity")
	delete(data, "specificSkuProperties")
	status := object(data["status"])
	for _, key := range []string{"fulfilledCount", "specificSkuProperties", "autoCreatedReservations", "lastKnownGoodState", "existingMatchingUsageInfo"} {
		delete(status, key)
	}
	data["storagePoolProperties"] = map[string]any{"storagePoolType": "hyperdisk-balanced", "requestedStoragePoolProvisionedCapacity": map[string]any{"poolProvisionedCapacityGb": "9007199254740993", "poolProvisionedIops": "100000", "poolProvisionedThroughput": "4096"}}
	status["storagePoolProvisionedCapacity"] = map[string]any{"poolProvisionedCapacityGb": "9007199254740992", "poolProvisionedIops": "90000", "poolProvisionedThroughput": "2048"}
	return data
}

func TestFutureReservationProcurementState(t *testing.T) {
	for _, state := range []string{"APPROVED", "CANCELLED", "COMMITTED", "DECLINED", "DRAFTING", "FAILED", "FAILED_PARTIALLY_FULFILLED", "FULFILLED", "PENDING_AMENDMENT_APPROVAL", "PENDING_APPROVAL", "PROCUREMENT_STATUS_UNSPECIFIED", "PROCURING", "PROVISIONING", "FUTURE_NATIVE_STATE"} {
		t.Run(state, func(t *testing.T) {
			data := futureReservationFixture("us-central1-a", "fixture")
			object(data["status"])["procurementStatus"] = state
			transport := func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.URL.Path != "/compute/v1/projects/sample-project/zones/us-central1-a/futureReservations/fixture" {
					t.Fatalf("unexpected native read %s %s", req.Method, req.URL)
				}
				body, _ := json.Marshal(data)
				return apiResponse(req, 200, string(body)), nil
			}
			runtime := protocolRuntime(t, transport)
			c := &client{project: "sample-project", number: "123456"}
			item, err := runtime.inventoryItem(c, map[string]any{"assetType": futureReservationTestType, "name": data["selfLink"], "resource": map[string]any{"data": data}})
			if err != nil {
				t.Fatal(err)
			}
			if item.State != state || item.Normalized["planningStatus"] != "SUBMITTED" || productValue(item.Normalized, "status.lastKnownGoodState.procurementStatus") != "APPROVED" {
				t.Fatal("procurement/planning/amendment history conflated", item)
			}
			driver := protocolAction(t, futureReservationTestType, "projects/sample-project/zones/us-central1-a/futureReservations/fixture", transport)
			read, err := driver.Readback(t.Context(), contracts.ActionRequest{Action: "delete", Asset: asset.Asset{Identity: asset.Identity{NativeType: futureReservationTestType, NativeID: item.NativeID}, Normalized: item.Normalized}})
			if err != nil || !read.Exists || read.State != state {
				t.Fatal("native readback state", read, err)
			}
		})
	}
	for _, test := range []struct {
		data map[string]any
		want string
	}{
		{map[string]any{"status": "RUNNING", "state": "OTHER"}, "RUNNING"},
		{map[string]any{"status": map[string]any{"state": "ACTIVE"}}, "ACTIVE"},
		{map[string]any{"status": map[string]any{"diskCount": "2"}, "state": "READY"}, "READY"},
		{map[string]any{"state": map[string]any{"state": "READY"}}, "READY"},
		{map[string]any{"planningStatus": "DRAFT"}, ""},
	} {
		if got := resourceState(test.data); got != test.want {
			t.Fatalf("existing state semantics = %q, want %q", got, test.want)
		}
	}
}

func TestFutureReservationNativePagingAndProperties(t *testing.T) {
	for _, region := range []string{"project", "us-central1", "europe-west1"} {
		t.Run(region, func(t *testing.T) {
			first, second := futureReservationFixture("us-central1-a", "first"), futureReservationFixture("europe-west1-b", "second")
			calls := 0
			runtime := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				calls++
				if req.Method != "GET" || req.URL.Host != "compute.googleapis.com" || req.URL.Path != "/compute/v1/projects/sample-project/aggregated/futureReservations" || req.URL.Query().Get("includeAllScopes") != "true" || req.URL.Query().Get("maxResults") != "100" {
					t.Fatalf("unexpected native list %s %s", req.Method, req.URL)
				}
				if calls == 1 {
					return apiResponse(req, 200, `{"nextPageToken":"second","warning":{"code":"NO_RESULTS_ON_PAGE"}}`), nil
				}
				if calls != 2 || req.URL.Query().Get("pageToken") != "second" {
					t.Fatal("incorrect pagination", req.URL)
				}
				body, _ := json.Marshal(map[string]any{"items": map[string]any{"zones/us-central1-a": map[string]any{"futureReservations": []any{first}}, "zones/europe-west1-b": map[string]any{"futureReservations": []any{second}}}})
				return apiResponse(req, 200, string(body)), nil
			})
			request := productRequest(runtime, futureReservationTestType, region)
			page, err := runtime.List(t.Context(), request)
			if err != nil || page.Complete || len(page.Items) != 0 || page.NextCursor == "" {
				t.Fatal("empty page lost continuation", page, err)
			}
			request.Cursor = page.NextCursor
			page, err = runtime.List(t.Context(), request)
			expected := 1
			if region == "project" {
				expected = 2
			}
			if err != nil || !page.Complete || len(page.Items) != expected {
				t.Fatal("scope/pagination", page, err)
			}
			for _, item := range page.Items {
				assertFutureReservationProperties(t, item)
				if len(item.NetworkReferences) != 0 {
					t.Fatal("creation history became deletion dependencies", item.NetworkReferences)
				}
			}
			if calls != 2 {
				t.Fatal("generated reservations caused additional reads", calls)
			}
			foreign := productRequest(runtime, futureReservationTestType, "project")
			foreign.Scope.NativeID = "foreign-project"
			if _, err := runtime.List(t.Context(), foreign); err == nil || calls != 2 {
				t.Fatal("foreign project scan accepted", err, calls)
			}
		})
	}
}

func assertFutureReservationProperties(t *testing.T, item contracts.InventoryItem) {
	t.Helper()
	for key, want := range map[string]any{"requestedInstanceCount": "9007199254740993", "fulfilledCount": "9007199254740992", "resourceId": "9007199254740993", "projectId": "sample-project", "state": "FAILED_PARTIALLY_FULFILLED", "planningStatus": "SUBMITTED", "amendmentStatus": "AMENDMENT_DECLINED", "lockTime": "2029-12-01T00:00:00Z", "autoDeleteAutoCreatedReservations": true} {
		if !reflect.DeepEqual(item.Normalized[key], want) {
			t.Fatalf("normalized %s = %#v, want %#v", key, item.Normalized[key], want)
		}
	}
	if len(array(item.Normalized["autoCreatedReservations"])) != 2 || object(item.Normalized["resolvedSkuProperties"])["sourceInstanceTemplateId"] != "9007199254740995" || object(item.Normalized["timeWindow"])["endTime"] != "2030-02-01T00:00:00Z" || object(item.Normalized["commitmentInfo"])["commitmentPlan"] != "TWELVE_MONTH" {
		t.Fatal("native metadata lost", item.Normalized)
	}
	if item.State != "FAILED_PARTIALLY_FULFILLED" || item.Actionable == nil || !*item.Actionable {
		t.Fatal("native state/action lost", item)
	}
	for key, typ := range map[string]string{"state": "string", "requestedInstanceCount": "string", "fulfilledCount": "string", "autoCreatedReservations": "array", "statusDetails": "object", "storagePoolProperties": "object", "lastKnownGoodState": "object", "specificReservationRequired": "boolean"} {
		found := false
		for _, property := range item.ResourceKind.Properties {
			if property.Path == key && property.Type == typ {
				found = true
			}
		}
		if !found {
			t.Fatal("public property missing", key, typ)
		}
	}
}

func TestFutureReservationFixturesMatchOfficialSchemas(t *testing.T) {
	raw, err := os.ReadFile("catalog/source/discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Documents []struct {
			SourceURI string `json:"source_uri"`
			SourceSHA string `json:"source_sha256"`
			Document  struct {
				Schemas map[string]any `json:"schemas"`
			} `json:"document"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	var schemas map[string]any
	for _, doc := range source.Documents {
		if doc.SourceURI == "https://www.googleapis.com/discovery/v1/apis/compute/v1/rest" && doc.SourceSHA == "5cee2d2fedf69f756fbc23aaef9153db01c2a2caffdbd6f63681912b65ca8139" {
			schemas = doc.Document.Schemas
		}
	}
	if schemas == nil {
		t.Fatal("pinned official Compute schema missing")
	}
	compiler := discoveryFixtureSchemaCompiler(t, schemas)
	schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/FutureReservation")
	if err != nil {
		t.Fatal(err)
	}
	for _, data := range []map[string]any{futureReservationFixture("us-central1-a", "fixture"), futureReservationStorageFixture()} {
		// JSON decoding converts synthetic integer nanos into the wire representation.
		data = roundTripDataformJSON(t, data)
		if err := schema.Validate(data); err != nil {
			t.Fatal("native fixture schema", err)
		}
		malformed := roundTripDataformJSON(t, data)
		malformed["status"] = "APPROVED"
		if schema.Validate(malformed) == nil {
			t.Fatal("scalar status accepted by official schema")
		}
		malformed = roundTripDataformJSON(t, data)
		object(malformed["status"])["fulfilledCount"] = 9007199254740992.0
		if schema.Validate(malformed) == nil {
			t.Fatal("numeric int64 accepted by official schema")
		}
	}
	runtime := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Path != "/compute/v1/projects/sample-project/aggregated/futureReservations" {
			t.Fatalf("unexpected API %s", req.URL)
		}
		return dataformResponse(req, 200, map[string]any{"items": map[string]any{"zones/us-central1-a": map[string]any{"futureReservations": []any{futureReservationStorageFixture()}}}}), nil
	})
	batch, err := runtime.List(t.Context(), productRequest(runtime, futureReservationTestType, "us-central1"))
	if err != nil || len(batch.Items) != 1 {
		t.Fatal("native storage reservation list", batch, err)
	}
	item := batch.Items[0]
	if err != nil || productValue(item.Normalized, "storagePoolProperties.requestedStoragePoolProvisionedCapacity.poolProvisionedCapacityGb") != "9007199254740993" || productValue(item.Normalized, "storagePoolProvisionedCapacity.poolProvisionedCapacityGb") != "9007199254740992" || item.Normalized["requestedInstanceCount"] != nil || len(item.NetworkReferences) != 0 {
		t.Fatal("storage capacity created an instance count or pool identity", item, err)
	}
	// Every public field path must exist in the official schema (provider-added
	// project/zone metadata are intentionally outside that schema).
	definition, _ := runtime.productDefinition(futureReservationTestType)
	for field, property := range definition.Fields {
		if property.Path == "project_id" || property.Path == "zone_id" {
			continue
		}
		node := object(schemas["FutureReservation"])
		for _, part := range strings.Split(property.Path, ".") {
			if ref := text(node["$ref"]); ref != "" {
				node = object(schemas[strings.TrimPrefix(ref, "#/definitions/")])
			}
			node = object(object(node["properties"])[part])
		}
		if ref := text(node["$ref"]); ref != "" {
			node = object(schemas[strings.TrimPrefix(ref, "#/definitions/")])
		}
		if node == nil || text(node["type"]) != string(property.Type) {
			t.Fatal("declared field differs from native schema", field, property, node)
		}
	}
}
