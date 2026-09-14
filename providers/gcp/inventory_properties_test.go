package gcp

import (
	"encoding/json"
	"net/http"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/resourcequery"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func assertGCPPropertyQuery(t *testing.T, r *Runtime, values []asset.Asset, kind, id, query string) {
	t.Helper()
	expression, err := resourcequery.Parse(query)
	if err != nil {
		t.Fatal(err)
	}
	if err := expression.Validate([]asset.ResourceKind{r.resourceKind(kind)}); err != nil {
		t.Fatal("public property query rejected", query, err)
	}
	found := false
	for _, value := range values {
		if value.Identity.NativeType != kind || !strings.HasSuffix(value.Identity.NativeID, id) {
			continue
		}
		found = true
		if !expression.Match(value) {
			t.Fatal("declared query did not match native inventory", query, value.Normalized)
		}
	}
	if !found {
		t.Fatal("query fixture missing", kind, id)
	}
}

func TestGCPPropertyQueriesPreserveNativeIdentityAndState(t *testing.T) {
	t.Run("dataproc", func(t *testing.T) {
		s := newDataprocScenario(t)
		r := protocolRuntime(t, s.transport(t))
		values := s.inventory(t, r)
		assertGCPPropertyQuery(t, r, values, dataprocPolicyType, "/autoscalingPolicies/elastic", `properties.resourceId = "elastic" AND properties.name = "projects/sample-project/regions/us-central1/autoscalingPolicies/elastic" AND properties.projectId = "sample-project"`)
		assertGCPPropertyQuery(t, r, values, dataprocTemplateType, "/workflowTemplates/hourly", `properties.resourceId = "hourly" AND properties.name = "projects/sample-project/regions/us-central1/workflowTemplates/hourly"`)
		assertGCPPropertyQuery(t, r, values, dataprocJobType, "/jobs/spark-1", `properties.name = "spark-1" AND properties.state = "RUNNING" AND properties.projectId = "sample-project"`)
		c := &client{project: "sample-project", number: "123456"}
		for _, value := range values {
			if !isDataproc(value.Identity.NativeType) {
				continue
			}
			if err := c.dataprocIdentity(value.Identity.NativeType, value.Identity.NativeID, value.Normalized); err != nil {
				t.Fatal("property projection changed native action identity", err)
			}
			original := s.resources[strings.TrimPrefix(value.Identity.NativeID, "//dataproc.googleapis.com/")]
			if err := dataprocSameResource(value.Identity.NativeType, value.Normalized, original); err != nil {
				t.Fatal("property projection changed reviewed configuration", err)
			}
		}
	})
	t.Run("tpu", func(t *testing.T) {
		s := newTPUScenario(t)
		r := protocolRuntime(t, s.transport(t))
		values := s.inventory(t, r)
		assertGCPPropertyQuery(t, r, values, tpuQueueType, "/queuedResources/training", `properties.lifecycleState = "ACTIVE" AND properties.projectId = "sample-project"`)
		for _, value := range values {
			if value.Identity.NativeType != tpuQueueType {
				continue
			}
			original := s.resources[tpuTestQueue]
			if object(value.Normalized["state"])["state"] != "ACTIVE" {
				t.Fatal("native queued state object overwritten", value.Normalized)
			}
			if err := tpuSameResource(tpuQueueType, value.Normalized, original); err != nil {
				t.Fatal("queue proof changed", err)
			}
		}
	})
	t.Run("firewall", func(t *testing.T) {
		s := newFirewallScenario()
		r := s.runtime(t)
		values := s.inventory(t, r)
		assertGCPPropertyQuery(t, r, values, firewallPolicyType, "/firewallPolicies/1001", `properties.name = "1001" AND properties.shortName = "hierarchical-policy" AND properties.resourceId = "1001"`)
		for _, value := range values {
			if firewallParentType(value.Identity.NativeType) == firewallPolicyType && value.Normalized["projectId"] != nil {
				t.Fatal("organization policy acquired project ownership", value)
			}
			if isFirewallPolicy(value.Identity.NativeType) && firewallConfiguration(value.Normalized, false) != value.Normalized[firewallProof] {
				t.Fatal("display aliases invalidated native firewall review", value)
			}
		}
	})
}

func TestGCPNativePropertiesAreNotOverwritten(t *testing.T) {
	data := map[string]any{"name": "vm", "id": "1", "selfLink": "https://compute.googleapis.com/compute/v1/projects/sample-project/zones/us-central1-a/instances/vm", "zone": "https://compute.googleapis.com/compute/v1/projects/sample-project/zones/us-central1-a", "status": "RUNNING", "projectId": "native-project-value", "labels": map[string]any{"team": "test"}, "metadata": map[string]any{"items": []any{map[string]any{"key": "startup-script", "value": "PRIVATE_STARTUP"}}}}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" || req.URL.Path != "/compute/v1/projects/sample-project/aggregated/instances" {
			t.Fatalf("unexpected native request %s", req.URL)
		}
		return dataformResponse(req, 200, map[string]any{"items": map[string]any{"zones/us-central1-a": map[string]any{"instances": []any{data}}}}), nil
	})
	batch, err := r.List(t.Context(), productRequest(r, instanceType, "us-central1"))
	if err != nil || len(batch.Items) != 1 {
		t.Fatal(batch, err)
	}
	item := batch.Items[0]
	if item.Normalized["projectId"] != "native-project-value" || item.Normalized["zoneId"] != "us-central1-a" || item.Normalized["state"] != "RUNNING" || item.Normalized["status"] != "RUNNING" {
		t.Fatal("native field replaced or alias missing", item)
	}
	raw, _ := json.Marshal(item)
	if strings.Contains(string(raw), "PRIVATE_STARTUP") {
		t.Fatal("alias projection bypassed sanitization")
	}
	if object(object(item.Raw["resource"])["data"])["state"] != nil {
		t.Fatal("projection mutated native raw observation")
	}
}

func TestGCPChangedPropertyPathsMatchPinnedNativeSchemas(t *testing.T) {
	raw, err := os.ReadFile("catalog/source/discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var source struct {
		Documents []struct {
			Document struct {
				Methods map[string]struct {
					Response struct {
						Ref string `json:"$ref"`
					} `json:"response"`
				} `json:"methods"`
				Schemas map[string]any `json:"schemas"`
			} `json:"document"`
		} `json:"documents"`
	}
	if err := json.Unmarshal(raw, &source); err != nil {
		t.Fatal(err)
	}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		t.Fatalf("unexpected API %s", req.URL)
		return nil, nil
	})
	for kind, fields := range map[string][]string{
		"run.googleapis.com/Service": {"state"}, dataformInvocationType: {"invocationTiming"},
		"pubsub.googleapis.com/Subscription": {"state"}, "pubsub.googleapis.com/Topic": {"state"}, "sqladmin.googleapis.com/Instance": {"state", "labels"}, "compute.googleapis.com/Subnetwork": {"state"},
		"iam.googleapis.com/ServiceAccount": {"projectId"}, dataprocClusterType: {"projectId"}, dataprocPolicyType: {"name", "resourceId"}, dataprocTemplateType: {"name", "resourceId"}, firewallPolicyType: {"name", "shortName"}, tpuQueueType: {"state", "lifecycleState"},
	} {
		definition, _ := r.productDefinition(kind)
		found := false
		for _, doc := range source.Documents {
			method, ok := doc.Document.Methods[definition.Discovery.Detail.Operation]
			if !ok {
				continue
			}
			found = true
			for _, field := range fields {
				property := definition.Fields[field]
				node := object(doc.Document.Schemas[method.Response.Ref])
				for _, segment := range strings.Split(property.Path, ".") {
					if ref := text(node["$ref"]); ref != "" {
						node = object(doc.Document.Schemas[ref])
					}
					node = object(object(node["properties"])[segment])
				}
				if ref := text(node["$ref"]); ref != "" {
					node = object(doc.Document.Schemas[ref])
				}
				if text(node["type"]) != string(property.Type) || node == nil {
					t.Fatal("property differs from native schema", kind, field, property, node)
				}
			}
			break
		}
		if !found {
			t.Fatal("native source operation missing", kind)
		}
	}
}

// An empty/unknown kind has no declared aliases and keeps its observed payload.
func TestGCPUnknownPropertiesRemainNative(t *testing.T) {
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) { t.Fatal("unexpected API"); return nil, nil })
	item := contracts.InventoryItem{NativeType: "unknown.googleapis.com/Resource", Normalized: map[string]any{"name": "native", "status": map[string]any{"state": "ACTIVE"}}}
	before := roundTripDataformJSON(t, item.Normalized)
	r.projectProperties(&item)
	if !reflect.DeepEqual(before, item.Normalized) {
		t.Fatal("invented unknown properties", item)
	}
}

func TestGCPPhysicalProofDistinguishesDerivedAndNativeProperties(t *testing.T) {
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) { t.Fatal("unexpected API"); return nil, nil })
	original := map[string]any{"name": "vm", "id": "42", "status": "RUNNING", "machineType": "n2-standard-2", "project_id": "sample-project", "zone_id": "us-central1-a"}
	item := contracts.InventoryItem{NativeType: instanceType, Normalized: roundTripDataformJSON(t, original)}
	r.projectProperties(&item)
	expected := infraPhysicalVisible(instanceType, original)
	if expected != infraPhysicalVisible(instanceType, item.Normalized) {
		t.Fatal("query aliases changed native physical proof")
	}
	item.Normalized["projectId"] = "changed-display-alias"
	if expected == infraPhysicalVisible(instanceType, item.Normalized) {
		t.Fatal("inconsistent alias silently discarded")
	}
	item.Normalized["projectId"] = "sample-project"
	item.Normalized["machineType"] = "n2-standard-4"
	if expected == infraPhysicalVisible(instanceType, item.Normalized) {
		t.Fatal("native configuration change hidden by alias projection")
	}
	account := map[string]any{"name": "projects/sample-project/serviceAccounts/user@sample-project.iam.gserviceaccount.com", "projectId": "sample-project", "uniqueId": "42"}
	expected = infraPhysicalVisible("iam.googleapis.com/ServiceAccount", account)
	account["projectId"] = "other-project"
	if expected == infraPhysicalVisible("iam.googleapis.com/ServiceAccount", account) {
		t.Fatal("native project identity treated as a display alias")
	}
}

// Native API payloads have no synthetic top-level state/labels/creation time.
func TestGCPNestedNativeInventoryFields(t *testing.T) {
	for _, state := range []string{"CONDITION_PENDING", "CONDITION_RECONCILING", "CONDITION_FAILED", "CONDITION_SUCCEEDED", "STATE_UNSPECIFIED", ""} {
		t.Run("run/"+state, func(t *testing.T) {
			data := map[string]any{"name": "projects/sample-project/locations/us-central1/services/web", "reconciling": true}
			if state != "" {
				data["terminalCondition"] = map[string]any{"type": "Ready", "state": state}
			}
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.URL.Host != "run.googleapis.com" || req.URL.Path != "/v2/projects/sample-project/locations/us-central1/services" {
					t.Fatalf("unexpected native request %s", req.URL)
				}
				return dataformResponse(req, 200, map[string]any{"services": []any{data}}), nil
			})
			batch, err := r.List(t.Context(), productRequest(r, "run.googleapis.com/Service", "us-central1"))
			if err != nil || len(batch.Items) != 1 {
				t.Fatal(batch, err)
			}
			item := batch.Items[0]
			if item.State != state || text(item.Normalized["state"]) != state || !reflect.DeepEqual(item.Normalized["terminalCondition"], data["terminalCondition"]) {
				t.Fatal("Cloud Run condition lost or fabricated", item)
			}
			if state == "" {
				if _, exists := item.Normalized["state"]; exists {
					t.Fatal("missing condition acquired a state", item)
				}
			} else {
				value := asset.Asset{Identity: asset.Identity{NativeType: item.NativeType, NativeID: item.NativeID}, State: item.State, Normalized: item.Normalized}
				assertGCPPropertyQuery(t, r, []asset.Asset{value}, item.NativeType, "/services/web", `state = "`+state+`" AND properties.state = "`+state+`"`)
			}
			if object(object(item.Raw["resource"])["data"])["state"] != nil {
				t.Fatal("native observation was changed", item.Raw)
			}
		})
	}
	t.Run("sql labels", func(t *testing.T) {
		data := map[string]any{"name": "db", "region": "us-central1", "state": "RUNNABLE", "settings": map[string]any{"userLabels": map[string]any{"team": "analytics"}}}
		r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
			if req.Method != "GET" || req.URL.Host != "sqladmin.googleapis.com" || req.URL.Path != "/sql/v1beta4/projects/sample-project/instances" {
				t.Fatalf("unexpected native request %s", req.URL)
			}
			return dataformResponse(req, 200, map[string]any{"items": []any{data}}), nil
		})
		batch, err := r.List(t.Context(), productRequest(r, "sqladmin.googleapis.com/Instance", "us-central1"))
		if err != nil || len(batch.Items) != 1 {
			t.Fatal(batch, err)
		}
		item := batch.Items[0]
		if item.Tags["team"] != "analytics" || object(item.Normalized["labels"])["team"] != "analytics" || !reflect.DeepEqual(item.Normalized["settings"], data["settings"]) {
			t.Fatal("SQL labels missing or native settings changed", item)
		}
		value := asset.Asset{Identity: asset.Identity{NativeType: item.NativeType, NativeID: item.NativeID}, State: item.State, Tags: item.Tags, Normalized: item.Normalized}
		assertGCPPropertyQuery(t, r, []asset.Asset{value}, item.NativeType, "/instances/db", `state = "RUNNABLE" AND tags.team = "analytics"`)
		proof := infraPhysicalVisible(item.NativeType, data)
		if proof != infraPhysicalVisible(item.NativeType, item.Normalized) {
			t.Fatal("derived SQL labels changed the native physical proof")
		}
		changed := roundTripDataformJSON(t, item.Normalized)
		object(object(changed["settings"])["userLabels"])["team"] = "changed"
		if proof == infraPhysicalVisible(item.NativeType, changed) {
			t.Fatal("native SQL label change bypassed the physical proof")
		}
		if object(object(item.Raw["resource"])["data"])["labels"] != nil {
			t.Fatal("native observation was changed", item.Raw)
		}
	})
	t.Run("dataform invocation timing", func(t *testing.T) {
		s := newDataformScenario(t)
		r := protocolRuntime(t, s.transport(t))
		count := 0
		for _, value := range s.inventory(t, r, "us-central1") {
			if value.Identity.NativeType != dataformInvocationType {
				continue
			}
			count++
			original := s.resources[strings.TrimPrefix(value.Identity.NativeID, "//dataform.googleapis.com/")]
			if value.Normalized["createTime"] != nil || !reflect.DeepEqual(value.Normalized["invocationTiming"], original["invocationTiming"]) {
				t.Fatal("invocation timing became a resource creation time", value.Normalized)
			}
			if err := dataformSameResource(dataformInvocationType, value.Normalized, original); err != nil {
				t.Fatal("native invocation proof changed", err)
			}
		}
		if count == 0 {
			t.Fatal("missing invocation fixture")
		}
		expression, err := resourcequery.Parse(`properties.createTime = "2025-01-01T00:00:00Z"`)
		if err != nil {
			t.Fatal(err)
		}
		if err := expression.Validate([]asset.ResourceKind{r.resourceKind(dataformInvocationType)}); err == nil {
			t.Fatal("nonexistent creation time still advertised as queryable")
		}
	})
}

func TestGCPNestedFieldsPreserveNativePrecedence(t *testing.T) {
	if got := resourceState(map[string]any{"state": "ACTIVE", "terminalCondition": map[string]any{"state": "CONDITION_FAILED"}}); got != "ACTIVE" {
		t.Fatal("native lifecycle state replaced", got)
	}
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) { t.Fatal("unexpected API"); return nil, nil })
	item := contracts.InventoryItem{NativeType: "sqladmin.googleapis.com/Instance", Tags: map[string]string{"team": "native"}, Normalized: map[string]any{"settings": map[string]any{"userLabels": map[string]any{"team": "derived", "cost": "", "invalid": true}}}}
	r.projectProperties(&item)
	if !reflect.DeepEqual(item.Tags, map[string]string{"team": "native", "cost": ""}) {
		t.Fatal("existing tags changed or non-string label became a tag", item.Tags)
	}
}
