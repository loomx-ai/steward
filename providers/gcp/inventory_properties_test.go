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
	selected := map[string][]string{}
	// Check every declared field, including future resource additions. Native
	// detail wrappers and explicit adapter-derived fields are handled below.
	for _, compiled := range r.bundle.Specs {
		kind := compiled.ResourceKind.NativeType
		definition, _ := r.productDefinition(kind)
		for field := range definition.Fields {
			selected[kind] = append(selected[kind], field)
		}
	}
	for kind, fields := range selected {
		t.Run(kind, func(t *testing.T) {
			definition, _ := r.productDefinition(kind)
			for _, doc := range source.Documents {
				method, ok := doc.Document.Methods[definition.Discovery.Detail.Operation]
				if !ok {
					continue
				}
				schemas := doc.Document.Schemas
				root := nativePropertySchema(schemas, object(schemas[method.Response.Ref]), definition.Discovery.Detail.ItemsPath)
				// Some native resources have no GET and use a matching member of
				// a list for detail/readback (TPU reservations, Data Fusion children).
				if text(root["type"]) == "array" {
					root = nativePropertySchema(schemas, object(root["items"]), "$")
				}
				for _, field := range fields {
					property := definition.Fields[field]
					path := property.Path
					switch {
					case kind == billingBudgetType && path == "billing_account":
						// The account is the parent encoded in native Budget.name,
						// not an extra field invented in the native response schema.
						nameSchema := nativePropertySchema(schemas, root, "name")
						if property.Type != "string" || field != "billingAccount" || nameSchema["type"] != "string" || object(root["properties"])[path] != nil || object(root["properties"])[field] != nil {
							t.Fatal("invalid derived budget account property", property, nameSchema)
						}
						name, parent, err := billingBudgetName("//billingbudgets.googleapis.com/" + text(billingBudgetFixture()["name"]))
						if err != nil || name != testBillingBudget || parent != text(billingAccountFixture()["name"]) {
							t.Fatal("budget property is not its native account parent", name, parent, err)
						}
						continue
					case kind == securityServiceType && path == "configurationParent":
						// Adapter-derived container, not a native service field.
						if field != "configurationParent" || property.Type != "string" || nativePropertySchema(schemas, root, "name")["type"] != "string" || object(root["properties"])[path] != nil {
							t.Fatal("invalid derived security service parent", field, property)
						}
						continue
					case path == "project_id" || path == "zone_id":
						if property.Type != "string" {
							t.Fatal("derived scope must be a string", field, property)
						}
						continue
					case kind == routePolicyType && path == "bgpReferences":
						// This adapter field projects Router peer names/directions;
						// it is not a field returned by getRoutePolicy.
						if property.Type != "array" || object(root["properties"])[path] != nil {
							t.Fatal("invalid derived policy references", property)
						}
						peers := nativePropertySchema(schemas, object(schemas["Router"]), "bgpPeers")
						peer := nativePropertySchema(schemas, object(peers["items"]), "$")
						if peers["type"] != "array" || nativePropertySchema(schemas, peer, "name")["type"] != "string" {
							t.Fatal("native peer identity schema missing")
						}
						for _, direction := range []string{"importPolicies", "exportPolicies"} {
							policies := nativePropertySchema(schemas, peer, direction)
							if policies["type"] != "array" || object(policies["items"])["type"] != "string" {
								t.Fatal("native policy reference schema missing", direction)
							}
						}
						continue
					case kind == storagePoolType && path == "storage_pool_disks":
						// Native member discovery is covered by the StoragePool
						// inventory/SQLite/restart suites, not its GET schema.
						if property.Type != "array" {
							t.Fatal("derived pool members must be an array", property)
						}
						continue
					case kind == storagePoolType && (path == "pool_usage" || strings.HasPrefix(path, "pool_usage.")):
						path = "resourceStatus" + strings.TrimPrefix(path, "pool_usage")
					}
					if property.Path != field && object(root["properties"])[field] != nil {
						t.Error("alias would collide with an authoritative native field", field, property)
					}
					node := nativePropertySchema(schemas, root, path)
					nativeType := text(node["type"])
					if nativeType == "integer" && property.Type == "number" {
						nativeType = "number"
					}
					if kind == discoveryHost+"/TargetSite" && field == "indexingStatus" {
						for _, state := range array(node["enum"]) {
							if safeDiscoveryPayload(map[string]any{"indexingStatus": state})["indexingStatus"] != state {
								t.Error("native indexing enum was redacted", state)
							}
						}
						fixture := newDiscoveryScenario(t).resources[deUSStore+"/siteSearchEngine/targetSites/site-1"]
						found := false
						for _, state := range array(node["enum"]) {
							found = found || fixture["indexingStatus"] == state
						}
						if !found {
							t.Error("fixture indexing state is not native", fixture["indexingStatus"])
						}
					}
					if nativeType != string(property.Type) || node == nil {
						t.Error("property differs from native schema", field, property, node)
					}
				}
				return
			}
			t.Fatal("native source operation missing", definition.Discovery.Detail.Operation)
		})
	}
}

func nativePropertySchema(schemas map[string]any, node map[string]any, path string) map[string]any {
	for _, segment := range strings.Split(path, ".") {
		if ref := text(node["$ref"]); ref != "" {
			node = object(schemas[ref])
		}
		if segment != "$" && segment != "" {
			node = object(object(node["properties"])[segment])
		}
	}
	if ref := text(node["$ref"]); ref != "" {
		node = object(schemas[ref])
	}
	return node
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

func TestGCPComputeNativeStatusQueries(t *testing.T) {
	cases := []struct {
		kind, path, collection, field, value string
		data                                 map[string]any
	}{
		{"Route", "global/routes", "routes", "state", "ACTIVE", map[string]any{"routeStatus": "ACTIVE"}},
		{"Route", "global/routes", "routes", "state", "DROPPED", map[string]any{"routeStatus": "DROPPED"}},
		{"Route", "global/routes", "routes", "state", "INACTIVE", map[string]any{"routeStatus": "INACTIVE"}},
		{"Route", "global/routes", "routes", "state", "PENDING", map[string]any{"routeStatus": "PENDING"}},
		{"Route", "global/routes", "routes", "", "", map[string]any{}},
		{"Network", "global/networks", "networks", "", "", map[string]any{}},
		{"GlobalForwardingRule", "global/forwardingRules", "forwardingRules", "pscConnectionStatus", "REJECTED", map[string]any{"pscConnectionStatus": "REJECTED"}},
		{"SslCertificate", "aggregated/sslCertificates", "sslCertificates", "managedStatus", "PROVISIONING_FAILED", map[string]any{"managed": map[string]any{"status": "PROVISIONING_FAILED"}}},
	}
	for _, tt := range cases {
		t.Run(tt.kind+"/"+tt.value, func(t *testing.T) {
			data := tt.data
			data["name"] = "fixture"
			data["selfLink"] = "https://compute.googleapis.com/compute/v1/projects/sample-project/global/" + tt.collection + "/fixture"
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || req.URL.Host != "compute.googleapis.com" || req.URL.Path != "/compute/v1/projects/sample-project/"+tt.path {
					t.Fatalf("unexpected native request %s", req.URL)
				}
				var items any = []any{data}
				if tt.kind == "SslCertificate" {
					items = map[string]any{"global": map[string]any{tt.collection: items}}
				}
				return dataformResponse(req, 200, map[string]any{"items": items}), nil
			})
			kind := "compute.googleapis.com/" + tt.kind
			batch, err := r.List(t.Context(), productRequest(r, kind, "global"))
			if err != nil || len(batch.Items) != 1 {
				t.Fatal(batch, err)
			}
			item := batch.Items[0]
			wantState := ""
			if tt.kind == "Route" {
				wantState = tt.value
			}
			if item.State != wantState {
				t.Fatal("native condition misrepresented as resource state", item)
			}
			if tt.field != "" {
				value := asset.Asset{Identity: asset.Identity{NativeType: item.NativeType, NativeID: item.NativeID}, State: item.State, Normalized: item.Normalized}
				query := `properties.` + tt.field + ` = "` + tt.value + `"`
				if tt.kind == "Route" {
					query += ` AND state = "` + tt.value + `"`
				}
				assertGCPPropertyQuery(t, r, []asset.Asset{value}, kind, "/fixture", query)
			}
			if object(object(item.Raw["resource"])["data"])["state"] != nil || item.Normalized["status"] != nil {
				t.Fatal("native payload acquired a fabricated status", item)
			}
			if tt.kind == "Network" {
				for _, field := range []string{"state", "labels"} {
					expression, err := resourcequery.Parse(`properties.` + field + ` = "ACTIVE"`)
					if err != nil {
						t.Fatal(err)
					}
					if err := expression.Validate([]asset.ResourceKind{r.resourceKind(kind)}); err == nil {
						t.Fatal("nonexistent native field advertised", field)
					}
				}
			}
		})
	}
}

func TestGCPRemainingNativePropertyQueries(t *testing.T) {
	t.Run("discovery metadata", func(t *testing.T) {
		s := newDiscoveryScenario(t)
		r := protocolRuntime(t, s.transport(t))
		queries := map[string]string{
			"Document":     `properties.id = "document-1" AND properties.schemaId = "default_schema" AND properties.indexedAt = "2026-08-01T13:00:00Z"`,
			"TargetSite":   `properties.indexingStatus = "PENDING"`,
			"Branch":       `properties.isDefault = true`,
			"Session":      `properties.startTime = "2026-08-01T12:00:00Z"`,
			"Conversation": `properties.startTime = "2026-08-01T12:00:00Z"`,
		}
		seen := map[string]bool{}
		for _, value := range s.inventory(t, r) {
			kind := last(value.Identity.NativeType)
			if query := queries[kind]; query != "" {
				assertGCPPropertyQuery(t, r, []asset.Asset{value}, value.Identity.NativeType, value.Identity.NativeID, query)
				seen[kind] = true
			}
			c, err := r.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			// Read through the adapter to include native connector and sitemap
			// enrichment rather than comparing against incomplete list fixtures.
			live, err := c.discoveryRead(t.Context(), value.Identity.NativeType, value.Identity.NativeID)
			if err != nil {
				t.Fatal(err)
			}
			if err := discoverySameResource(value.Identity.NativeType, value.Normalized, live); err != nil {
				t.Fatal("metadata exposure changed native configuration proof", kind, err)
			}
		}
		if len(seen) != len(queries) {
			t.Fatal("missing native query fixture", seen)
		}
	})
	t.Run("identity names", func(t *testing.T) {
		s := newIdentityScenario()
		r := s.runtime(t)
		values := s.inventory(t, r)
		assertGCPPropertyQuery(t, r, values, identityGroupType, identityTestGroup, `properties.name = "groups/g-primary" AND properties.displayName = "g-primary"`)
		assertGCPPropertyQuery(t, r, values, identityMemberType, "/memberships/m-user", `properties.name = "groups/g-primary/memberships/m-user" AND properties.memberId = "member@example.test"`)
		for _, value := range values {
			if identityConfiguration(value.Normalized) != value.Normalized[identityProof] {
				t.Fatal("identity alias changed reviewed configuration", value)
			}
		}
	})
	t.Run("organization names", func(t *testing.T) {
		s := newOrganizationScenario()
		r := s.runtime(t)
		batch, err := r.List(t.Context(), organizationRequest(r))
		if err != nil || len(batch.Items) != 1 {
			t.Fatal(batch, err)
		}
		item := batch.Items[0]
		value := asset.Asset{Identity: asset.Identity{NativeType: item.NativeType, NativeID: item.NativeID}, Normalized: item.Normalized}
		assertGCPPropertyQuery(t, r, []asset.Asset{value}, organizationType, "organizations/123", `properties.name = "organizations/123" AND properties.displayName = "example.test" AND properties.state = "ACTIVE"`)
	})
	t.Run("infra change intent", func(t *testing.T) {
		s := newInfraScenario(t)
		r := protocolRuntime(t, s.transport(t))
		values := s.inventory(t, r)
		assertGCPPropertyQuery(t, r, values, infraChange, "/resourceChanges/network", `properties.intent = "DELETE" AND properties.terraformType = "google_compute_network"`)
		assertGCPPropertyQuery(t, r, values, infraDrift, "/resourceDrifts/network", `properties.terraformType = "google_compute_network"`)
		for _, value := range values {
			if value.Identity.NativeType == infraChange || value.Identity.NativeType == infraDrift {
				original := s.resources[strings.TrimPrefix(value.Identity.NativeID, "//"+infraHost+"/")]
				if err := infraSame(value.Normalized, original); err != nil {
					t.Fatal("change/drift aliases altered configuration proof", err)
				}
			}
		}
	})
	t.Run("gke labels", func(t *testing.T) {
		f := newGKEFixture(t)
		f.gke[f.cluster.Identity.NativeID]["resourceLabels"] = map[string]any{"team": "cluster"}
		f.gke[f.pool.Identity.NativeID]["config"] = map[string]any{"resourceLabels": map[string]any{"team": "pool"}}
		r := protocolRuntime(t, f.roundTrip)
		for kind, want := range map[string]string{clusterType: "cluster", nodePoolType: "pool"} {
			batch, err := r.List(t.Context(), productRequest(r, kind, "us-central1"))
			if err != nil || len(batch.Items) != 1 {
				t.Fatal(batch, err)
			}
			item := batch.Items[0]
			if item.Tags["team"] != want || object(item.Normalized["labels"])["team"] != want {
				t.Fatal("native GKE labels lost", item)
			}
			if infraPhysicalVisible(kind, f.gke[item.NativeID]) != infraPhysicalVisible(kind, item.Normalized) {
				t.Fatal("native GKE label path changed physical proof", item)
			}
		}
	})
}
