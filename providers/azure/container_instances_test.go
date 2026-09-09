package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const aciVersion = "2025-09-01"

func aciExample(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/container-instances/" + name + ".json")
	var result map[string]any
	if err != nil || json.Unmarshal(payload, &result) != nil {
		t.Fatal("invalid ACI fixture", err)
	}
	return result
}

func aciScenario(t *testing.T) (*dnsScenario, *Runtime, asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	raw := object(object(object(aciExample(t, "ContainerGroupsGet_Succeeded")["responses"])["200"])["body"])
	raw["id"], raw["name"], raw["location"] = resourceID(containerGroupType, "group"), "group", "eastus"
	raw["systemData"] = map[string]any{"createdAt": "2026-09-10T00:00:00Z"}
	properties := object(raw["properties"])
	properties["subnetIds"] = []any{map[string]any{"id": resourceID(vnetType, "external") + "/subnets/apps"}}
	raw["identity"] = map[string]any{"type": "UserAssigned", "userAssignedIdentities": map[string]any{resourceID("Microsoft.ManagedIdentity/userAssignedIdentities", "external"): map[string]any{}}}
	properties["diagnostics"] = map[string]any{"logAnalytics": map[string]any{"workspaceId": "workspace-guid", "workspaceKey": "do-not-store-workspace", "workspaceResourceId": resourceID("Microsoft.OperationalInsights/workspaces", "external")}}
	s.add(raw, aciVersion)
	root := "/subscriptions/" + testSubscription
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": root + "/resourcegroups/test", "type": groupType}}
	s.lists[root+"/providers/microsoft.containerinstance/containergroups"] = []any{raw}
	s.version[root+"/providers/microsoft.containerinstance/containergroups"] = aciVersion
	r := s.runtime(t)
	return s, r, dnsAsset(t, r, raw)
}

func TestContainerInstanceNativeInventoryAndSharedLifecycle(t *testing.T) {
	s, r, root := aciScenario(t)
	batch, err := r.List(context.Background(), productRequest(r, containerGroupType))
	if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].NativeID != root.Identity.NativeID || batch.Items[0].Actionable == nil || !*batch.Items[0].Actionable {
		t.Fatalf("native container group discovery: %+v %v", batch, err)
	}
	item := batch.Items[0]
	for _, kind := range []string{subnetType, vnetType, "Microsoft.ManagedIdentity/userAssignedIdentities", "Microsoft.OperationalInsights/workspaces"} {
		if fmt.Sprint(item.Normalized[referenceKey(kind)]) == "<nil>" {
			t.Fatalf("missing native dependency %s", kind)
		}
	}
	request, input := dnsRequest(t, r, []asset.Asset{root}, root)
	solved, err := plan.Solve(input)
	if err != nil || len(solved.Steps) != 1 || len(request.LifecycleImpacts) != 0 || len(request.PrerequisiteDeletions) != 0 || len(input.LifecycleBindings) != 0 {
		t.Fatalf("embedded members/external volumes became separate deletes: %+v %v", solved, err)
	}
	driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	deletes := 0
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method != "DELETE" {
			return nil, false
		}
		if !strings.EqualFold(req.URL.Path, root.Identity.NativeID) || req.URL.Query().Get("api-version") != aciVersion || len(req.URL.Query()) != 1 || req.Header.Get("If-Match") != "" || req.ContentLength > 0 {
			t.Fatalf("unexpected container deletion %s", req.URL)
		}
		deletes++
		// Microsoft's CLI records a 200 DELETE with the complete old resource.
		// Only the subsequent read can establish final absence.
		return jsonResponse(200, s.records[root.Identity.NativeID], http.Header{"X-Ms-Request-Id": {"aci-delete"}}), true
	}
	result, err := driver.Execute(context.Background(), request)
	if err != nil || result.ProviderRequestID != "aci-delete" || deletes != 1 {
		t.Fatalf("native ACI deletion: %+v %v", result, err)
	}
	payload, _ := json.Marshal(result)
	var resumed contracts.ActionResult
	if json.Unmarshal(payload, &resumed) != nil {
		t.Fatal("invalid persisted receipt")
	}
	driver, _ = r.ResolveAction(context.Background(), "connection", request.Asset)
	wait, err := driver.Wait(context.Background(), request, resumed)
	if err != nil || wait.Done {
		t.Fatalf("200 delete hid surviving group: %+v %v", wait, err)
	}
	s.gone[root.Identity.NativeID] = true
	wait, err = driver.Wait(context.Background(), request, resumed)
	if err != nil || !wait.Done || deletes != 1 {
		t.Fatalf("resumed absence: %+v %v", wait, err)
	}
	if _, err = driver.Execute(context.Background(), request); err != nil || deletes != 1 {
		t.Fatal("already absent group replayed deletion", err)
	}
}

func TestContainerInstanceConfigurationProtectionAndRuntimeState(t *testing.T) {
	for _, mode := range []string{"image", "command", "environment", "registry-key", "missing-private-configuration", "new-container", "init-container", "volume", "subnet", "identity", "os", "dns", "creation", "etag", "missing-configuration", "missing-containers", "duplicate-container", "invalid-subnet", "protected", "managed-group", "lock", "denied", "runtime"} {
		t.Run(mode, func(t *testing.T) {
			s, r, root := aciScenario(t)
			raw := s.records[root.Identity.NativeID]
			properties := object(raw["properties"])
			container := object(array(properties["containers"])[0])
			switch mode {
			case "image":
				object(container["properties"])["image"] = "changed:latest"
			case "command":
				object(container["properties"])["command"] = []any{"changed-command"}
			case "environment":
				object(container["properties"])["environmentVariables"] = []any{map[string]any{"name": "INNOCENT", "value": "changed-environment"}}
			case "registry-key":
				properties["imageRegistryCredentials"] = []any{map[string]any{"server": "example.azurecr.io", "password": "changed-key"}}
			case "missing-private-configuration":
				delete(root.Normalized, "_container_group_private_configuration")
			case "new-container":
				properties["containers"] = append(array(properties["containers"]), map[string]any{"name": "new", "properties": map[string]any{"image": "alpine"}})
			case "init-container":
				properties["initContainers"] = []any{map[string]any{"name": "init", "properties": map[string]any{"image": "alpine"}}}
			case "volume":
				object(object(array(properties["volumes"])[0])["azureFile"])["shareName"] = "different-share"
			case "subnet":
				object(array(properties["subnetIds"])[0])["id"] = resourceID(vnetType, "external") + "/subnets/other"
			case "identity":
				raw["identity"] = map[string]any{"type": "SystemAssigned"}
			case "os":
				properties["osType"] = "Windows"
			case "dns":
				object(properties["ipAddress"])["dnsNameLabel"] = "changed"
			case "creation":
				object(raw["systemData"])["createdAt"] = "2026-09-10T01:00:00Z"
			case "etag":
				raw["etag"] = "new-version"
			case "missing-configuration":
				delete(root.Normalized, "_container_group_configuration")
			case "missing-containers":
				delete(properties, "containers")
			case "duplicate-container":
				properties["containers"] = []any{container, container}
			case "invalid-subnet":
				properties["subnetIds"] = []any{map[string]any{"id": resourceID(vnetType, "external")}}
			case "protected":
				raw["tags"] = map[string]any{"steward:protect": "true"}
			case "managed-group":
				group := "/subscriptions/" + testSubscription + "/resourcegroups/test"
				s.records[group] = map[string]any{"id": group, "managedBy": resourceID(aksType, "controller")}
			case "lock":
				s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.authorization/locks"] = []any{map[string]any{"id": root.Identity.NativeID + "/providers/Microsoft.Authorization/locks/hold", "properties": map[string]any{"level": "CanNotDelete"}}}
			case "denied":
				s.status[root.Identity.NativeID] = 403
			case "runtime":
				properties["provisioningState"] = "Succeeded"
				properties["instanceView"] = map[string]any{"state": "Stopped"}
				object(container["properties"])["instanceView"] = map[string]any{"restartCount": 5, "currentState": map[string]any{"state": "Terminated"}}
				object(properties["ipAddress"])["ip"] = "203.0.113.2"
				object(properties["ipAddress"])["fqdn"] = "new-runtime.example"
			}
			request := contracts.ActionRequest{Asset: root, Action: "delete"}
			driver, err := r.ResolveAction(context.Background(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			_, err = driver.Execute(context.Background(), request)
			if mode == "runtime" {
				if err != nil || len(s.deletes) != 1 {
					t.Fatal("runtime state prevented configured group deletion", err)
				}
			} else if err == nil || len(s.deletes) != 0 {
				t.Fatalf("unreviewed %s allowed deletion: %v %v", mode, s.deletes, err)
			}
		})
	}
}

func TestContainerInstanceConfigurationSecretsDoNotReachInventoryOrLogs(t *testing.T) {
	s, r, root := aciScenario(t)
	raw := s.records[root.Identity.NativeID]
	properties := object(raw["properties"])
	container := object(object(array(properties["containers"])[0])["properties"])
	container["command"] = []any{"do-not-store-command"}
	container["environmentVariables"] = []any{map[string]any{"name": "INNOCENT", "value": "do-not-store-env", "secureValue": "do-not-store-secure"}}
	container["configMap"] = map[string]any{"keyValuePairs": map[string]any{"innocent": "do-not-store-config"}}
	container["livenessProbe"] = map[string]any{"httpGet": map[string]any{"port": 80, "httpHeaders": []any{map[string]any{"name": "Authorization", "value": "do-not-store-header"}}}}
	properties["extensions"] = []any{map[string]any{"name": "sidecar", "properties": map[string]any{"extensionType": "Test", "version": "1", "settings": map[string]any{"innocent": "do-not-store-extension"}, "protectedSettings": "do-not-store-protected"}}}
	properties["imageRegistryCredentials"] = []any{map[string]any{"server": "example.azurecr.io", "password": "do-not-store-registry"}}
	object(object(array(properties["volumes"])[0])["azureFile"])["storageAccountKey"] = "do-not-store-storage"
	properties["volumes"] = append(array(properties["volumes"]), map[string]any{"name": "secret", "secret": map[string]any{"key": "do-not-store-volume"}}, map[string]any{"name": "git", "gitRepo": map[string]any{"repository": "https://user:do-not-store-git@github.com/example/repo.git?sig=do-not-store-query#private", "revision": "main"}})
	batch, err := r.List(context.Background(), productRequest(r, containerGroupType))
	if err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(batch)
	if strings.Contains(string(payload), "do-not-store") || !strings.Contains(string(payload), "share1") || !strings.Contains(string(payload), "github.com/example/repo.git") || container["command"] == nil {
		t.Fatalf("sensitive configuration escaped or live payload mutated: %s", payload)
	}
	logs := []execution.JobLogEntry{}
	c, _ := r.resolve(context.Background(), "connection")
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	kind, _ := findType(containerGroupType)
	endpoint, _ := c.resourceURL(kind, root.Identity.NativeID)
	if _, err = c.request(ctx, "GET", endpoint); err != nil {
		t.Fatal(err)
	}
	payload, _ = json.Marshal(logs)
	if len(logs) != 2 || strings.Contains(string(payload), "do-not-store") {
		t.Fatal("ACI secrets escaped API diagnostics")
	}
}

func TestContainerInstanceManagedGroupChecksPrivateConfiguration(t *testing.T) {
	for _, mode := range []string{"unchanged", "planning-drift", "execution-drift", "credential-change"} {
		t.Run(mode, func(t *testing.T) {
			s, r, previous := aciScenario(t)
			raw := s.records[previous.Identity.NativeID]
			delete(s.records, previous.Identity.NativeID)
			groupID := "/subscriptions/" + testSubscription + "/resourcegroups/custom-nodes"
			raw["id"] = groupID + "/providers/Microsoft.ContainerInstance/containerGroups/member"
			s.add(raw, aciVersion)
			cluster := nativeResource(aksType, "cluster", "eastus", map[string]any{"nodeResourceGroup": "custom-nodes", "provisioningState": "Succeeded"})
			group := map[string]any{"id": groupID, "type": groupType, "managedBy": cluster["id"], "location": "eastus"}
			s.add(cluster, "2024-02-01")
			s.add(group, resourcesVersion)
			s.lists[groupID+"/resources"] = []any{raw}
			assets := []asset.Asset{dnsAsset(t, r, cluster), dnsAsset(t, r, group), dnsAsset(t, r, raw)}
			change := func() {
				object(object(array(object(raw["properties"])["containers"])[0])["properties"])["command"] = []any{"changed-sensitive-command"}
			}
			if mode == "planning-drift" {
				change()
				contributor, _ := r.ClusterLifecycle(context.Background(), "connection")
				if _, err := contributor.Contribute(context.Background(), "scope", assets); err == nil {
					t.Fatal("managed-group planning ignored sensitive configuration drift")
				}
				return
			}
			contributor, _ := r.ClusterLifecycle(context.Background(), "connection")
			contribution, err := contributor.Contribute(context.Background(), "scope", assets)
			if err != nil || len(contribution.Unresolved) != 0 {
				t.Fatalf("managed-group contribution: %+v %v", contribution, err)
			}
			result, err := plan.Solve(plan.Input{Assets: assets, ResolvedAssetIDs: []asset.AssetID{assets[0].ID}, LifecycleBindings: contribution.Bindings, Relationships: contribution.Relationships})
			if err != nil || len(result.Blockers) > 0 || len(result.Steps) != 1 {
				t.Fatalf("managed-group plan: %+v %v", result, err)
			}
			request := servicePlanRequest(result, assets, assets[0])
			if mode == "execution-drift" {
				change()
			}
			driver, err := r.ResolveAction(context.Background(), "connection", assets[0])
			if err != nil {
				t.Fatal(err)
			}
			if mode == "credential-change" {
				// A credential rotation invalidates the prior keyed digest and
				// requires a rescan; no secret is exposed to explain the mismatch.
				driver.(*action).client.fingerprint[0] ^= 1
			}
			check, err := driver.Preflight(context.Background(), request)
			if mode == "unchanged" {
				if err != nil || !check.Allowed {
					t.Fatalf("reviewed ACI group impact blocked: %+v %v", check, err)
				}
			} else if err == nil && check.Allowed {
				t.Fatal("managed-group preflight ignored sensitive configuration drift")
			}
			if len(s.deletes) != 0 {
				t.Fatal("preflight mutated resources")
			}
		})
	}
}

func TestContainerInstanceNativeExamplesAndSchemas(t *testing.T) {
	payload, err := os.ReadFile("fixtures/container-instances/sources.json")
	var manifest []map[string]string
	if err != nil || json.Unmarshal(payload, &manifest) != nil || len(manifest) != 5 {
		t.Fatal("invalid original ACI source manifest")
	}
	payload, _ = os.ReadFile("catalog/source/swagger.json")
	var sources catalog.RESTDocumentSet
	json.Unmarshal(payload, &sources)
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	root := ""
	for _, source := range sources.Documents {
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(source.Document))
		if err := compiler.AddResource(source.SourceURI, value); err != nil {
			t.Fatal(err)
		}
		if strings.HasSuffix(source.SourceURI, "/2025-09-01/containerInstance.json") {
			root = source.SourceURI
			if source.SourceSHA256 != "0543d3a66e6382cbe7956942a55d9f17760ef7b0fc108e39bda88c7367c8bcb3" {
				t.Fatal("native ACI source hash changed")
			}
		}
	}
	for _, entry := range manifest {
		payload, err := os.ReadFile("fixtures/container-instances/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] {
			t.Fatal("original ACI example modified")
		}
		var example map[string]any
		json.Unmarshal(payload, &example)
		body := object(object(object(example["responses"])["200"])["body"])
		definition := "ContainerGroup"
		if entry["file"] == "ContainerGroupsList.json" {
			definition = "ContainerGroupListResult"
		}
		schema, err := compiler.Compile(root + "#/definitions/" + definition)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ = json.Marshal(body)
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(payload))
		if err := schema.Validate(value); err != nil {
			t.Fatalf("original %s does not satisfy native schema: %v", entry["file"], err)
		}
	}
}

func TestContainerInstanceNativePagingAndScope(t *testing.T) {
	for _, mode := range []string{"complete", "denied", "partial", "missing-array", "duplicate", "foreign-scope", "cycle", "version", "bad-detail"} {
		t.Run(mode, func(t *testing.T) {
			s, r, root := aciScenario(t)
			first := s.records[root.Identity.NativeID]
			payload, _ := json.Marshal(first)
			var second map[string]any
			json.Unmarshal(payload, &second)
			second["id"], second["name"] = root.Identity.NativeID+"-second", "group-second"
			s.add(second, aciVersion)
			collection := "/subscriptions/" + testSubscription + "/providers/microsoft.containerinstance/containergroups"
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if mode == "bad-detail" && strings.EqualFold(req.URL.Path, text(second["id"])) {
					return jsonResponse(200, first, nil), true
				}
				if !strings.EqualFold(req.URL.Path, collection) {
					return nil, false
				}
				if req.URL.Query().Get("$skiptoken") == "next" {
					switch mode {
					case "denied":
						return jsonResponse(403, nil, nil), true
					case "partial":
						return jsonResponse(206, map[string]any{"value": []any{second}}, nil), true
					case "missing-array":
						return jsonResponse(200, map[string]any{}, nil), true
					case "duplicate":
						return jsonResponse(200, map[string]any{"value": []any{second, second}}, nil), true
					case "foreign-scope":
						second["id"] = strings.Replace(text(second["id"]), testSubscription, testTenant, 1)
					}
					body := map[string]any{"value": []any{second}}
					if mode == "cycle" {
						body["nextLink"] = req.URL.String()
					}
					return jsonResponse(200, body, nil), true
				}
				next := apiURL(collection, aciVersion) + "&%24skiptoken=next"
				if mode == "version" {
					next = strings.Replace(next, aciVersion, "2023-05-01", 1)
				}
				return jsonResponse(200, map[string]any{"value": []any{first}, "nextLink": next}, http.Header{"X-Ms-Request-Id": {"aci-list"}}), true
			}
			request := productRequest(r, containerGroupType)
			batch, err := r.List(context.Background(), request)
			if mode == "version" {
				if err == nil {
					t.Fatal("changed native API pagination accepted")
				}
				return
			}
			if err != nil || batch.Complete || len(batch.Items) != 1 || batch.RequestID != "aci-list" || batch.NextCursor == "" {
				t.Fatalf("first native page: %+v %v", batch, err)
			}
			request.Cursor = batch.NextCursor
			batch, err = r.List(context.Background(), request)
			if mode == "complete" {
				if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].NativeID != text(second["id"]) {
					t.Fatalf("last native page: %+v %v", batch, err)
				}
			} else if err == nil || batch.Complete {
				t.Fatalf("uncertain %s completed: %+v %v", mode, batch, err)
			}
		})
	}
}

func TestContainerInstanceNativeAsyncDeleteReadback(t *testing.T) {
	for _, mode := range []string{"complete", "failed", "expired", "foreign", "survivor"} {
		t.Run(mode, func(t *testing.T) {
			s, r, root := aciScenario(t)
			request := contracts.ActionRequest{Asset: root, Action: "delete", IdempotencyKey: "aci-delete"}
			driver, err := r.ResolveAction(context.Background(), "connection", root)
			if err != nil {
				t.Fatal(err)
			}
			header := text(object(object(object(aciExample(t, "ContainerGroupsDelete")["responses"])["202"])["headers"])["azure-asyncoperation"])
			// The unchanged Swagger example uses placeholder subscription, region,
			// operation and version values. Rebind these for a usable protocol URL.
			header = strings.NewReplacer("subscriptions/subid", "subscriptions/"+testSubscription, "resourceGroups/demo", "resourceGroups/test", "locations/location", "locations/eastus", "/operationId?", "/01234567-89ab-cdef-0123-456789abcdef?", "api-version=apiVersion", "api-version="+aciVersion).Replace(header)
			if mode == "foreign" {
				header = strings.Replace(header, testSubscription, testTenant, 1)
			}
			polls, deletes := 0, 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "DELETE" {
					deletes++
					return jsonResponse(202, nil, http.Header{"Azure-Asyncoperation": {header}, "Retry-After": {"1"}}), true
				}
				if req.URL.String() == header {
					polls++
					if mode == "expired" {
						return jsonResponse(404, nil, nil), true
					}
					state := "InProgress"
					if polls > 1 {
						state = "Succeeded"
						if mode == "failed" {
							state = "Failed"
						}
					}
					return jsonResponse(200, map[string]any{"status": state}, nil), true
				}
				return nil, false
			}
			result, err := driver.Execute(context.Background(), request)
			if mode == "foreign" {
				if err == nil || polls > 0 {
					t.Fatal("foreign ACI polling accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			payload, _ := json.Marshal(result)
			json.Unmarshal(payload, &result)
			driver, _ = r.ResolveAction(context.Background(), "connection", root)
			wait, err := driver.Wait(context.Background(), request, result)
			if mode == "expired" {
				if err == nil && wait.Done {
					t.Fatal("expired poll hid live group")
				}
				return
			}
			if err != nil || wait.Done {
				t.Fatalf("premature ACI completion: %+v %v", wait, err)
			}
			wait, err = driver.Wait(context.Background(), request, result)
			if mode == "failed" {
				if err == nil {
					t.Fatal("failed operation succeeded")
				}
				return
			}
			if err != nil || wait.Done {
				t.Fatal("operation success hid a surviving native group", err)
			}
			if mode == "complete" {
				s.gone[root.Identity.NativeID] = true
				wait, err = driver.Wait(context.Background(), request, result)
				if err != nil || !wait.Done || deletes != 1 {
					t.Fatalf("resumed ACI delete: %+v %v", wait, err)
				}
			}
		})
	}
}

func TestContainerInstanceMicrosoftCLIRecordedResponses(t *testing.T) {
	fixture := aciExample(t, "cli-recordings")
	if text(fixture["source_sha256"]) != "39f741f11a1bc0bab412d517b79002f18459439e5ce892a90eb8ffcc0b760bdd" {
		t.Fatal("CLI source changed")
	}
	values := array(fixture["recordings"])
	if len(values) != 3 {
		t.Fatal("missing CLI native sequence")
	}
	read := object(object(values[1])["body"])
	payload, _ := json.Marshal(read)
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	json.Unmarshal(payload, &read)
	s := newDNSScenario()
	s.add(read, aciVersion)
	group := strings.Join(strings.Split(strings.ToLower(text(read["id"])), "/")[:5], "/")
	s.lists["/subscriptions/"+testSubscription+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
	// The retained LIST was resource-group scoped and omits instanceView.
	// Replay its body through the selected native subscription List contract.
	listed := object(object(values[0])["body"])
	payload, _ = json.Marshal(listed)
	payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
	json.Unmarshal(payload, &listed)
	s.lists["/subscriptions/"+testSubscription+"/providers/microsoft.containerinstance/containergroups"] = array(listed["value"])
	r := s.runtime(t)
	request := productRequest(r, containerGroupType)
	request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westus"}
	batch, err := r.List(context.Background(), request)
	if err != nil || len(batch.Items) != 1 || len(array(batch.Items[0].Normalized["containers"])) != 1 {
		t.Fatalf("recorded ACI inventory: %+v %v", batch, err)
	}
	root := dnsAsset(t, r, read)
	deletes := 0
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method != "DELETE" {
			return nil, false
		}
		deletes++
		body := object(object(values[2])["body"])
		payload, _ := json.Marshal(body)
		payload = bytes.ReplaceAll(payload, []byte("00000000-0000-0000-0000-000000000000"), []byte(testSubscription))
		json.Unmarshal(payload, &body)
		return jsonResponse(int(object(values[2])["status"].(float64)), body, nil), true
	}
	actionRequest := contracts.ActionRequest{Asset: root, Action: "delete"}
	driver, _ := r.ResolveAction(context.Background(), "connection", root)
	result, err := driver.Execute(context.Background(), actionRequest)
	if err != nil || deletes != 1 {
		t.Fatal("recorded native DELETE failed", err)
	}
	wait, err := driver.Wait(context.Background(), actionRequest, result)
	if err != nil || wait.Done {
		t.Fatal("native response body incorrectly established absence", err)
	}
	s.gone[root.Identity.NativeID] = true
	wait, err = driver.Wait(context.Background(), actionRequest, result)
	if err != nil || !wait.Done {
		t.Fatal("native absence failed", err)
	}
}
