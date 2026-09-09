package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const grafanaVersion = "2025-08-01"
const grafanaSpecCommit = "e45039baa985c442877529906e705982a6e0099d"

// Paths are literal native contracts, independently of the generated catalog.
var grafanaContracts = []struct{ kind, operation, suffix, schema string }{
	{grafanaType, "Grafana", "", "ManagedGrafana"},
	{grafanaPrivateEndpointType, "ManagedPrivateEndpoints", "/managedPrivateEndpoints/outbound", "ManagedPrivateEndpointModel"},
	{grafanaConnectionType, "PrivateEndpointConnections", "/privateEndpointConnections/inbound", "PrivateEndpointConnection"},
	{grafanaIntegrationType, "IntegrationFabrics", "/integrationFabrics/integration", "IntegrationFabric"},
}

func grafanaFixture(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/grafana/" + name + ".json")
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	if err := json.Unmarshal(payload, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func grafanaScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	root := "/subscriptions/" + testSubscription
	parentID := root + "/resourceGroups/test/providers/Microsoft.Dashboard/grafana/workspace"
	for _, tc := range grafanaContracts {
		raw := object(object(object(grafanaFixture(t, tc.operation+"_Get")["responses"])["200"])["body"])
		// Official example IDs omit /providers/ and one example type is singular.
		// Preserve those fixtures; use the declared Swagger route/type for calls.
		raw["id"], raw["type"], raw["name"] = parentID+tc.suffix, tc.kind, last(parentID+tc.suffix)
		if tc.kind != grafanaConnectionType {
			raw["location"] = "eastus2"
		}
		if tc.kind == grafanaPrivateEndpointType {
			object(raw["properties"])["privateLinkResourceId"] = resourceID(storageType, "external")
		}
		if tc.kind == grafanaConnectionType {
			object(raw["properties"])["privateEndpoint"] = map[string]any{"id": resourceID(privateEndpointType, "external")}
			object(raw["properties"])["provisioningState"] = "Succeeded" // The upstream Accepted example violates its declared enum.
		}
		if tc.kind == grafanaIntegrationType {
			object(raw["properties"])["targetResourceId"] = resourceID(aksType, "external")
			object(raw["properties"])["dataSourceResourceId"] = resourceID("Microsoft.OperationalInsights/workspaces", "external")
		}
		s.add(raw, grafanaVersion)
		list := ""
		if tc.suffix == "" {
			list = root + "/providers/microsoft.dashboard/grafana"
		} else {
			list = strings.ToLower(parentID + tc.suffix[:strings.LastIndex(tc.suffix, "/")])
		}
		s.lists[list], s.version[list] = []any{raw}, grafanaVersion
	}
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": root + "/resourceGroups/test", "type": groupType}}
	r := s.runtime(t)
	var assets []asset.Asset
	for _, tc := range grafanaContracts {
		batch, err := r.List(context.Background(), productRequest(r, tc.kind))
		if err != nil || len(batch.Items) != 1 || !batch.Complete || batch.Items[0].Location != "eastus2" {
			t.Fatalf("Grafana native inventory %s: %+v %v", tc.kind, batch, err)
		}
		item := batch.Items[0]
		assets = append(assets, asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeID: item.NativeID, NativeType: item.NativeType}, Normalized: item.Normalized, Location: item.Location, Capabilities: asset.CapabilitySet{asset.CapabilityActionable}})
	}
	return s, r, assets
}

func TestGrafanaNativePlanPrerequisitesAndResumedOperations(t *testing.T) {
	for _, header := range []string{"Azure-AsyncOperation", "Operation-Location", "Location"} {
		t.Run(header, func(t *testing.T) {
			s, r, assets := grafanaScenario(t)
			root := assets[0]
			request, input := dnsRequest(t, r, assets, root)
			result, err := plan.Solve(input)
			if err != nil || len(result.Steps) != 4 || len(request.PrerequisiteDeletions) != 3 || len(request.LifecycleImpacts) != 0 {
				t.Fatalf("native prerequisite plan %+v %+v %v", result, request, err)
			}
			for _, binding := range input.LifecycleBindings {
				if binding.CleanupPolicy != graph.CleanupDirect || binding.Evidence[graph.LifecycleEvidenceControllerDeleteGuaranteed] == true {
					t.Fatal("Grafana child was delegated to an unverified native cascade")
				}
			}
			for _, step := range result.Steps {
				if step.AssetID == root.ID && len(step.DependsOn) != 3 {
					t.Fatal("parent did not wait for all three native child steps")
				}
			}
			for _, retained := range []string{string(assets[1].ID), assets[3].Identity.NativeID} {
				retention := input
				retention.RequestOptions = map[asset.AssetID]map[string]any{root.ID: {"retain_resources": []string{retained}}}
				if plan, err := plan.Solve(retention); err != nil || len(plan.Blockers) == 0 {
					t.Fatalf("accepted impossible child retention %+v %v", plan, err)
				}
			}
			parentDriver, _ := r.ResolveAction(context.Background(), "connection", root)
			if _, err := parentDriver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
				t.Fatal("parent deleted before child completion")
			}
			operationURL := apiURL("/subscriptions/"+testSubscription+"/providers/Microsoft.Dashboard/locations/eastus2/operationStatuses/native-delete", grafanaVersion)
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.Contains(req.URL.Path, "/operationStatuses/") {
					return jsonResponse(200, map[string]any{"status": "Succeeded"}, nil), true
				}
				if req.Method == "DELETE" {
					id := strings.ToLower(req.URL.Path)
					if s.records[id] == nil || req.Header.Get("x-ms-client-request-id") != azureRequestID(id) || req.Header.Get("If-Match") != "" || req.ContentLength > 0 {
						t.Fatalf("unexpected Grafana native DELETE %s %v", req.URL, req.Header)
					}
					s.deletes = append(s.deletes, id)
					h := http.Header{"X-Ms-Request-Id": {"grafana-delete"}}
					h.Set(header, operationURL)
					return jsonResponse(202, nil, h), true
				}
				return nil, false
			}
			for _, index := range []int{1, 2, 3, 0} {
				value := assets[index]
				req := servicePlanRequest(result, assets, value)
				req.IdempotencyKey = value.Identity.NativeID
				driver, err := r.ResolveAction(context.Background(), "connection", value)
				if err != nil {
					t.Fatal(err)
				}
				op, err := driver.Execute(context.Background(), req)
				if err != nil || op.ProviderRequestID != "grafana-delete" || op.ProviderOperationID != operationURL {
					t.Fatalf("Grafana native delete %+v %v", op, err)
				}
				encoded, _ := json.Marshal(req)
				json.Unmarshal(encoded, &req)
				encoded, _ = json.Marshal(op)
				json.Unmarshal(encoded, &op)
				driver, _ = s.runtime(t).ResolveAction(context.Background(), "connection", req.Asset)
				if wait, err := driver.Wait(context.Background(), req, op); err != nil || wait.Done {
					t.Fatalf("LRO success hid a surviving resource: %+v %v", wait, err)
				}
				s.gone[value.Identity.NativeID] = true
				if index == 0 {
					s.gone[assets[2].Identity.NativeID] = false
					if wait, err := driver.Wait(context.Background(), req, op); err == nil || wait.Done {
						t.Fatalf("parent absence hid a surviving child: %+v %v", wait, err)
					}
					s.gone[assets[2].Identity.NativeID] = true
				}
				if wait, err := driver.Wait(context.Background(), req, op); err != nil || !wait.Done {
					t.Fatalf("Grafana final native readback: %+v %v", wait, err)
				}
				before := len(s.deletes)
				if _, err := driver.Execute(context.Background(), req); err != nil || len(s.deletes) != before {
					t.Fatalf("resumed Grafana action replayed DELETE: %v", err)
				}
			}
			if len(s.deletes) != 4 || s.deletes[3] != root.Identity.NativeID {
				t.Fatalf("wrong deletion order %v", s.deletes)
			}
		})
	}
}

func TestGrafanaConfigurationAndScopeDriftBlockWrites(t *testing.T) {
	for _, mode := range []string{"root-configuration", "root-api-key-mode", "root-creation", "child-target", "child-connection-state", "child-creation", "parent-before-child", "missing-proof", "child-permission", "parent-permission", "parent-missing", "wrong-parent", "protected-child", "locked-parent", "managed-group", "foreign-prerequisite", "new-child", "list-denied", "partial-list", "duplicate-list", "changing-list", "changing-parent"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := grafanaScenario(t)
			root, child := assets[0], assets[1]
			request, _ := dnsRequest(t, r, assets, root)
			target := root
			parentRaw, childRaw := s.records[root.Identity.NativeID], s.records[child.Identity.NativeID]
			collection := root.Identity.NativeID + "/managedprivateendpoints"
			switch mode {
			case "root-configuration":
				object(parentRaw["properties"])["publicNetworkAccess"] = "Disabled"
			case "root-api-key-mode":
				object(parentRaw["properties"])["apiKey"] = "Disabled"
			case "root-creation":
				object(parentRaw["systemData"])["createdAt"] = "2026-09-09T00:00:00Z"
			case "child-target", "child-connection-state", "child-creation", "parent-before-child", "missing-proof", "child-permission", "parent-permission", "parent-missing", "wrong-parent", "protected-child":
				target = child
				switch mode {
				case "child-target":
					object(childRaw["properties"])["privateLinkResourceId"] = resourceID(storageType, "another")
				case "child-connection-state":
					object(object(childRaw["properties"])["connectionState"])["status"] = "Rejected"
				case "child-creation":
					object(childRaw["systemData"])["createdAt"] = "2026-09-09T00:00:00Z"
				case "parent-before-child":
					object(parentRaw["properties"])["grafanaMajorVersion"] = "12"
				case "missing-proof":
					delete(target.Normalized, "_grafana_configuration")
				case "child-permission":
					s.status[child.Identity.NativeID] = 403
				case "parent-permission":
					s.status[root.Identity.NativeID] = 403
				case "parent-missing":
					s.status[root.Identity.NativeID] = 404
				case "wrong-parent":
					parentRaw["id"] = root.Identity.NativeID + "-another"
				case "protected-child":
					childRaw["tags"] = map[string]any{"steward/protected": "true"}
				}
			case "locked-parent":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": root.Identity.NativeID + "/providers/Microsoft.Authorization/locks/protect", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "managed-group":
				group := "/subscriptions/" + testSubscription + "/resourcegroups/test"
				s.records[group] = map[string]any{"id": group, "managedBy": resourceID(aksType, "owner")}
			case "foreign-prerequisite":
				request.PrerequisiteDeletions[0].Asset.Identity.ConnectionID = "other"
			case "new-child":
				added := map[string]any{"id": collection + "/new", "type": grafanaPrivateEndpointType, "location": "eastus2", "properties": map[string]any{"provisioningState": "Succeeded"}}
				s.add(added, grafanaVersion)
				s.lists[collection] = append(s.lists[collection], added)
			case "list-denied":
				s.status[collection] = 403
			case "partial-list":
				s.status[collection] = 206
			case "duplicate-list":
				s.lists[collection] = append(s.lists[collection], s.lists[collection][0])
			case "changing-list", "changing-parent":
				reads := 0
				s.handle = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, collection) {
						reads++
						if reads == 2 {
							if mode == "changing-list" {
								return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
							}
							object(parentRaw["properties"])["publicNetworkAccess"] = "Disabled"
						}
					}
					return nil, false
				}
			}
			if target.ID != root.ID {
				request = contracts.ActionRequest{Asset: target, Action: "delete"}
			} else if slices.Contains([]string{"root-configuration", "root-api-key-mode", "root-creation", "locked-parent", "managed-group", "foreign-prerequisite"}, mode) {
				for _, value := range assets[1:] {
					s.gone[value.Identity.NativeID] = true
				}
			}
			// List faults must be reached during planning, before any child writes.
			if strings.Contains(mode, "list") || mode == "new-child" || mode == "changing-parent" {
				contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
				contribution, err := contributor.Contribute(context.Background(), "scope", assets)
				if err == nil && len(contribution.Unresolved) == 0 {
					t.Fatalf("accepted incomplete Grafana child set %+v", contribution)
				}
				return
			}
			driver, err := r.ResolveAction(context.Background(), "connection", target)
			if err != nil {
				t.Fatal(err)
			}
			check, err := driver.Preflight(context.Background(), request)
			if (err == nil && (check.Allowed || check.Absent)) || isNotFound(err) {
				t.Fatalf("unsafe Grafana preflight %+v %v", check, err)
			}
			if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) > 0 {
				t.Fatal("Grafana drift reached a native write")
			}
		})
	}
}

func TestGrafanaNativeSchemasProvenanceAndInvalidExampleIdentities(t *testing.T) {
	data, err := os.ReadFile("fixtures/grafana/sources.json")
	if err != nil {
		t.Fatal(err)
	}
	var manifest []map[string]string
	if json.Unmarshal(data, &manifest) != nil || len(manifest) != 12 {
		t.Fatal("incomplete Grafana native example provenance")
	}
	for _, entry := range manifest {
		data, err := os.ReadFile("fixtures/grafana/" + entry["file"])
		if err != nil || entry["source_sha256"] != fmt.Sprintf("%x", sha256.Sum256(data)) || !strings.Contains(entry["source_uri"], "/Azure/azure-rest-api-specs/"+grafanaSpecCommit+"/") {
			t.Fatalf("Grafana fixture provenance mismatch %s %v", entry["file"], err)
		}
	}
	payload, _ := os.ReadFile("catalog/source/swagger.json")
	var sources catalog.RESTDocumentSet
	if json.Unmarshal(payload, &sources) != nil {
		t.Fatal("invalid native catalog source")
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	root := ""
	for _, source := range sources.Documents {
		if !strings.Contains(source.SourceURI, "/"+grafanaSpecCommit+"/") {
			continue
		}
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(source.Document))
		if err := compiler.AddResource(source.SourceURI, value); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(source.SourceURI, "/grafana.json") {
			root = source.SourceURI
		}
	}
	s, _, assets := grafanaScenario(t)
	for i, tc := range grafanaContracts {
		schema, err := compiler.Compile(root + "#/definitions/" + tc.schema)
		if err != nil {
			t.Fatal(err)
		}
		fixture := grafanaFixture(t, tc.operation+"_Get")
		raw := object(object(object(fixture["responses"])["200"])["body"])
		for index, record := range []map[string]any{raw, s.records[assets[i].Identity.NativeID]} {
			payload, _ := json.Marshal(record)
			value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
			err := schema.Validate(value)
			if index == 0 && tc.kind == grafanaConnectionType {
				if err == nil {
					t.Fatal("expected upstream Accepted example to violate the native connection enum")
				}
				continue
			}
			if err != nil {
				t.Fatalf("Grafana native schema %s: %v", tc.kind, err)
			}
		}
		// Schema string types alone cannot validate ARM path ownership. Retain
		// rejection coverage for the actual errors in upstream example files.
		if validResourceResponse(response{status: 200, data: raw}, assets[i].Identity.NativeID, tc.kind) {
			t.Fatal("accepted an official example's foreign/malformed identity")
		}
	}
}

func TestGrafanaDependenciesAndSMTPRedaction(t *testing.T) {
	s, r, assets := grafanaScenario(t)
	for _, tc := range []struct {
		index int
		kind  string
	}{{1, storageType}, {2, privateEndpointType}, {3, aksType}, {3, "Microsoft.OperationalInsights/workspaces"}} {
		refs := assets[tc.index].Normalized[referenceKey(tc.kind)]
		if len(array(refs)) == 0 {
			if values, ok := refs.([]string); !ok || len(values) != 1 {
				t.Fatalf("missing external native %s reference: %v", tc.kind, refs)
			}
		}
	}
	root := s.records[assets[0].Identity.NativeID]
	smtp := object(object(object(root["properties"])["grafanaConfigurations"])["smtp"])
	smtp["password"] = "do-not-store-smtp-password"
	batch, err := r.List(context.Background(), productRequest(r, grafanaType))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(batch)
	if strings.Contains(string(payload), "do-not-store") || smtp["password"] != "do-not-store-smtp-password" {
		t.Fatal("SMTP secret escaped inventory or sanitized live configuration in place")
	}
	if refs := assets[0].Normalized[referenceKey(privateEndpointType)]; refs != nil {
		t.Fatal("parent embedded connections created a reverse ownership edge")
	}
}

func TestGrafanaChildPaginationBindsCurrentParentConfiguration(t *testing.T) {
	for _, mode := range []string{"complete", "parent-configuration", "parent-creation", "parent-removed", "parent-denied", "page-denied", "cycle"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := grafanaScenario(t)
			parent, child := assets[0], assets[1]
			payload, _ := json.Marshal(s.records[child.Identity.NativeID])
			var second map[string]any
			json.Unmarshal(payload, &second)
			second["id"], second["name"] = child.Identity.NativeID+"two", "outboundtwo"
			s.add(second, grafanaVersion)
			collection := parent.Identity.NativeID + "/managedprivateendpoints"
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if !strings.EqualFold(req.URL.Path, collection) {
					return nil, false
				}
				body := map[string]any{"value": []any{s.records[child.Identity.NativeID]}}
				if req.URL.Query().Get("$skiptoken") == "next" {
					if mode == "page-denied" {
						return jsonResponse(403, nil, nil), true
					}
					body["value"] = []any{second}
				} else {
					body["nextLink"] = apiURL(collection, grafanaVersion) + "&%24skiptoken=next"
				}
				if mode == "cycle" {
					body["nextLink"] = apiURL(collection, grafanaVersion) + "&%24skiptoken=next"
				}
				return jsonResponse(200, body, http.Header{"X-Ms-Request-Id": {"grafana-page"}}), true
			}
			request := productRequest(r, grafanaPrivateEndpointType)
			first, err := r.List(context.Background(), request)
			if err != nil || first.Complete || len(first.Items) != 1 || first.NextCursor == "" || first.RequestID != "grafana-page" {
				t.Fatalf("native first page: %+v %v", first, err)
			}
			switch mode {
			case "parent-configuration":
				object(s.records[parent.Identity.NativeID]["properties"])["publicNetworkAccess"] = "Disabled"
			case "parent-creation":
				object(s.records[parent.Identity.NativeID]["systemData"])["createdAt"] = "2026-09-09T00:00:00Z"
			case "parent-removed":
				s.gone[parent.Identity.NativeID] = true
			case "parent-denied":
				s.status[parent.Identity.NativeID] = 403
			}
			request.Cursor = first.NextCursor
			lastPage, err := r.List(context.Background(), request)
			if mode == "complete" {
				if err != nil || !lastPage.Complete || len(lastPage.Items) != 1 || lastPage.Items[0].NativeID != text(second["id"]) {
					t.Fatalf("native final page: %+v %v", lastPage, err)
				}
			} else if err == nil || lastPage.Complete {
				t.Fatalf("uncertain child inventory declared complete: %+v %v", lastPage, err)
			}
		})
	}
}

type grafanaRecordedResponse struct {
	Status  int            `json:"status"`
	Headers http.Header    `json:"headers"`
	Body    map[string]any `json:"body"`
}

func (r grafanaRecordedResponse) response() *http.Response {
	header := http.Header{}
	for key, values := range r.Headers {
		for _, value := range values {
			header.Add(key, value)
		}
	}
	return jsonResponse(r.Status, r.Body, header)
}

func TestGrafanaMicrosoftCLIRecordedDeletionAndSignedPolling(t *testing.T) {
	payload, err := os.ReadFile("fixtures/grafana/cli-delete-recordings.json")
	if err != nil {
		t.Fatal(err)
	}
	// Microsoft already redacted the recorded subscription. Rebind only that
	// placeholder; retain the recorded ARM paths, bodies and operation statuses.
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	var recordings []struct {
		SourceURI    string `json:"source_uri"`
		SourceSHA    string `json:"source_sha256"`
		Version      string `json:"recorded_api_version"`
		ResourceURI  string `json:"resource_uri"`
		Read, Delete grafanaRecordedResponse
		Poll         []grafanaRecordedResponse
		DeleteIndex  int   `json:"delete_index"`
		PollIndices  []int `json:"poll_indices"`
	}
	if json.Unmarshal(payload, &recordings) != nil || len(recordings) != 3 {
		t.Fatal("invalid Microsoft Grafana recordings")
	}
	for _, recording := range recordings {
		t.Run(fmt.Sprint(recording.DeleteIndex), func(t *testing.T) {
			if recording.Version != "2023-09-01" || len(recording.Poll) != len(recording.PollIndices) || len(recording.Poll) < 2 {
				t.Fatal("recording lost its native version or polling sequence")
			}
			if recording.DeleteIndex == 16 {
				if recording.SourceSHA != "722d81efec8e3f9e563650de29e084202ed0c34d59c3a69474f8d6e26fd2a87c" || !strings.Contains(recording.SourceURI, "/1681210b2e6e1cce1624d1c41e7b32c19d51650b/") {
					t.Fatal("changed native workspace recording")
				}
			} else if recording.SourceSHA != "2428b5d42232ae06ad53c5291161d0ffe9d8a744abb169e061698abc5d91da89" || !strings.Contains(recording.SourceURI, "/da13f11483dcc737691f78717dd3d340dd98f8ff/") {
				t.Fatal("changed native connection recording")
			}
			s, r, assets := grafanaScenario(t)
			id, kind, err := parseID(text(recording.Read.Body["id"]))
			if err != nil || !strings.EqualFold(id, strings.Split(recording.ResourceURI, "?")[0][len(armOrigin):]) {
				t.Fatal("native recording did not return its requested ARM identity")
			}
			definition, _ := findType(kind)
			value := assets[0]
			value.Identity.NativeID, value.Identity.NativeType, value.ID = id, definition.NativeType, asset.AssetID(id)
			value.Location = "westcentralus"
			value.Normalized = map[string]any{"_grafana_configuration": grafanaConfiguration(definition.NativeType, recording.Read.Body), "_arm_creation_generation": creationGeneration(recording.Read.Body), "_arm_generation": productGeneration(recording.Read.Body)}
			s.add(recording.Read.Body, grafanaVersion)
			if value.Identity.NativeType == grafanaType {
				for _, collection := range []string{"managedPrivateEndpoints", "privateEndpointConnections", "integrationFabrics"} {
					s.lists[id+"/"+strings.ToLower(collection)] = []any{}
				}
			} else {
				parentID := id[:strings.LastIndex(id[:strings.LastIndex(id, "/")], "/")]
				parent := nativeResource(grafanaType, "workspace", "westcentralus", map[string]any{"provisioningState": "Succeeded"})
				parent["id"], parent["name"] = parentID, last(parentID)
				s.add(parent, grafanaVersion)
				value.Normalized["_grafana_parent_generation"] = productGeneration(parent)
			}
			pollIndex, writes := 0, 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					if !strings.EqualFold(req.URL.Path, id) || req.URL.Query().Get("api-version") != grafanaVersion {
						t.Fatal("recording replay changed the bound native DELETE")
					}
					writes++
					return recording.Delete.response(), true
				}
				if grafanaGlobalOperation(req.URL.String()) {
					if req.Method != "GET" || req.URL.Query().Get("s") != "fixture-s" || req.URL.Query().Get("api-version") != "2023-09-01" {
						t.Fatal("signed polling URL was rewritten or used for a mutation")
					}
					res := recording.Poll[min(pollIndex, len(recording.Poll)-1)].response()
					pollIndex++
					return res, true
				}
				return nil, false
			}
			driver, err := r.ResolveAction(context.Background(), "connection", value)
			if err != nil {
				t.Fatal(err)
			}
			request := contracts.ActionRequest{Asset: value, Action: "delete"}
			operation, err := driver.Execute(context.Background(), request)
			if err != nil || writes != 1 || !grafanaGlobalOperation(operation.ProviderOperationID) {
				t.Fatalf("native recorded DELETE failed (writes %d): %v", writes, err)
			}
			serialized, _ := json.Marshal(operation)
			json.Unmarshal(serialized, &operation)
			driver, _ = s.runtime(t).ResolveAction(context.Background(), "connection", value)
			for pollIndex < len(recording.Poll) {
				if wait, err := driver.Wait(context.Background(), request, operation); err != nil || wait.Done {
					t.Fatalf("native recorded polling bypassed resource readback: %+v %v", wait, err)
				}
			}
			s.gone[id] = true
			if wait, err := driver.Wait(context.Background(), request, operation); err != nil || !wait.Done {
				t.Fatalf("recorded completion with synthetic final GET absence: %+v %v", wait, err)
			}
			// Raw Invoke accepts only this native response's exact Grafana poll
			// shape; ordinary client and product-list access remains restricted.
			c, _ := r.resolve(context.Background(), "connection")
			if err := c.validateURL(operation.ProviderOperationID); err == nil {
				t.Fatal("global polling escaped into ordinary ARM request authority")
			}
			_, params, _ := c.resourceOperation(definition, id, "DELETE")
			invoked, err := r.Invoke(context.Background(), contracts.Invocation{ConnectionID: "connection", Operation: definition.DeleteOperations[0], Parameters: params})
			if err != nil || invoked.OperationID != operation.ProviderOperationID {
				t.Fatalf("native Grafana Invoke rejected its returned operation: %v", err)
			}
		})
	}
}

func TestGrafanaSignedOperationAuthorityFailuresAndLogging(t *testing.T) {
	s, r, assets := grafanaScenario(t)
	value := assets[1]
	driver, _ := r.ResolveAction(context.Background(), "connection", value)
	a := driver.(*action)
	endpoint := armOrigin + "/providers/Microsoft.Dashboard/locations/eastus2/operationStatuses/11111111-1111-1111-1111-111111111111*" + strings.Repeat("A", 64) + "?api-version=2025-08-01&t=fixture-t&c=fixture-c&s=fixture-s&h=fixture-h"
	if err := a.validateOperationURL(endpoint); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(endpoint)
	responseBody := map[string]any{"id": u.Path, "name": last(u.Path), "resourceId": value.Identity.NativeID, "status": "Succeeded"}
	operation := contracts.ActionResult{ProviderOperationID: endpoint, Data: map[string]any{"polling": "status", "grafana_operation_binding": a.operationBinding(endpoint)}}
	for _, mode := range []string{"foreign-host", "foreign-provider", "foreign-region", "encoded-path", "traversal", "missing-signature", "duplicate-query", "unknown-query", "corrupt-receipt", "changed-target", "response-target", "response-id", "response-name", "missing-response-target", "partial", "failed", "canceled", "denied", "expired-live", "expired-absent"} {
		t.Run(mode, func(t *testing.T) {
			payload, _ := json.Marshal(operation)
			var result contracts.ActionResult
			json.Unmarshal(payload, &result)
			payload, _ = json.Marshal(responseBody)
			var body map[string]any
			json.Unmarshal(payload, &body)
			status := 200
			s.gone[value.Identity.NativeID] = false
			switch mode {
			case "foreign-host":
				result.ProviderOperationID = strings.Replace(endpoint, "management.azure.com", "example.com", 1)
			case "foreign-provider":
				result.ProviderOperationID = strings.Replace(endpoint, "Microsoft.Dashboard", "Microsoft.Compute", 1)
			case "foreign-region":
				result.ProviderOperationID = strings.Replace(endpoint, "eastus2", "westus", 1)
			case "encoded-path":
				result.ProviderOperationID = strings.Replace(endpoint, "/providers/", "/%70roviders/", 1)
			case "traversal":
				result.ProviderOperationID = strings.Replace(endpoint, "/locations/", "/../locations/", 1)
			case "missing-signature":
				result.ProviderOperationID = strings.Replace(endpoint, "&s=fixture-s", "", 1)
			case "duplicate-query":
				result.ProviderOperationID += "&s=other"
			case "unknown-query":
				result.ProviderOperationID += "&redirect=other"
			case "corrupt-receipt":
				result.Data["grafana_operation_binding"] = "bad"
			case "changed-target":
				result.ProviderOperationID = strings.Replace(endpoint, strings.Repeat("A", 64), strings.Repeat("B", 64), 1)
			case "response-target":
				body["resourceId"] = resourceID(vmType, "other")
			case "response-id":
				body["id"] = u.Path + "-other"
			case "response-name":
				body["name"] = "other"
			case "missing-response-target":
				delete(body, "resourceId")
			case "partial":
				status = 206
			case "failed":
				body["status"] = "Failed"
			case "canceled":
				body["status"] = "Canceled"
			case "denied":
				status = 403
			case "expired-live", "expired-absent":
				status = 404
				s.gone[value.Identity.NativeID] = mode == "expired-absent"
			}
			calls := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.Contains(req.URL.Path, "/operationStatuses/") {
					calls++
					return jsonResponse(status, body, nil), true
				}
				return nil, false
			}
			wait, err := driver.Wait(context.Background(), contracts.ActionRequest{Asset: value, Action: "delete"}, result)
			if mode == "expired-live" || mode == "expired-absent" {
				if err != nil || wait.Done != (mode == "expired-absent") {
					t.Fatalf("expired operation skipped actual absence: %+v %v", wait, err)
				}
			} else if err == nil || wait.Done {
				t.Fatalf("invalid native Grafana operation accepted: %+v %v", wait, err)
			}
			if strings.HasPrefix(mode, "foreign-") || strings.Contains(mode, "query") || slices.Contains([]string{"encoded-path", "traversal", "missing-signature", "corrupt-receipt", "changed-target"}, mode) {
				if calls != 0 {
					t.Fatal("invalid operation authority reached HTTP")
				}
			}
		})
	}
	var entries []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { entries = append(entries, e) }))
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if strings.Contains(req.URL.Path, "/operationStatuses/") {
			return jsonResponse(200, responseBody, nil), true
		}
		return nil, false
	}
	if _, err := driver.Wait(ctx, contracts.ActionRequest{Asset: value, Action: "delete"}, operation); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(entries)
	if len(entries) == 0 || strings.Contains(string(encoded), "fixture-") || !strings.Contains(string(encoded), "api-version") {
		t.Fatal("signed native polling parameters leaked or API diagnostics were lost")
	}
}
