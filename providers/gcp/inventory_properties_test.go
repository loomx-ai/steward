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
		"pubsub.googleapis.com/Subscription": {"state"}, "pubsub.googleapis.com/Topic": {"state"}, "sqladmin.googleapis.com/Instance": {"state"}, "compute.googleapis.com/Subnetwork": {"state"},
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
