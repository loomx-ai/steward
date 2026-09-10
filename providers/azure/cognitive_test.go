package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func cognitiveExample(t *testing.T, name string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/cognitive/" + name)
	var raw map[string]any
	if err != nil || json.Unmarshal(payload, &raw) != nil {
		t.Fatal("invalid Cognitive Services example", err)
	}
	return raw
}
func cognitiveScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	root := "/subscriptions/" + testSubscription
	group := root + "/resourcegroups/test-rg"
	s.lists[root+"/resourcegroups"] = []any{map[string]any{"id": group, "type": groupType}}
	payload, _ := os.ReadFile("fixtures/cognitive/sources.json")
	var manifest []map[string]string
	json.Unmarshal(payload, &manifest)
	var raws []map[string]any
	byKind := map[string]map[string]any{}
	pattern := regexp.MustCompile(`\{([^}]+)\}`)
	for _, mapping := range metadata.kinds {
		if !isCognitiveType(mapping.NativeType) {
			continue
		}
		op, _ := metadata.catalog.Operation(mapping.ReadOperations[0])
		var raw map[string]any
		for _, entry := range manifest {
			if strings.HasSuffix(op.ID, "."+entry["operation"]) {
				raw = object(object(object(cognitiveExample(t, entry["file"])["responses"])["200"])["body"])
				break
			}
		}
		if raw == nil {
			t.Fatal("missing native fixture", mapping.NativeType)
		}
		params := map[string]string{"subscriptionId": testSubscription, "resourceGroupName": "test-rg", "accountName": "account-one", "projectName": "project-one", "name": "app-one", "appName": "app-one", "managedNetworkName": "default", "commitmentPlanName": "plan-one", "commitmentPlanAssociationName": "assoc-one", "deploymentName": "deployment-one", "connectionName": "connection-one", "capabilityHostName": "host-one", "raiBlocklistName": "blocklist-one"}
		id := pattern.ReplaceAllStringFunc(op.Call.Path, func(value string) string {
			key := value[1 : len(value)-1]
			if p := params[key]; p != "" {
				return p
			}
			return "item-one"
		})
		id = strings.ToLower(id)
		// Official examples use unrelated resource IDs and a few malformed short
		// types. Keep those files unchanged and explicitly compose one account here.
		raw["id"], raw["name"], raw["type"] = id, last(id), mapping.NativeType
		if mapping.NativeType == cognitiveType || mapping.NativeType == cognitiveSharedPlanType || mapping.NativeType == cognitiveProjectType {
			raw["location"] = "eastus"
		}
		s.add(raw, mapping.Version)
		raws = append(raws, raw)
		byKind[mapping.NativeType] = raw
	}
	for _, raw := range raws {
		kind := text(raw["type"])
		props := object(raw["properties"])
		if kind == cognitiveHostType || kind == cognitiveProjectHostType {
			for _, field := range []string{"aiServicesConnections", "storageConnections", "threadStorageConnections", "vectorStoreConnections"} {
				delete(props, field)
			}
		}
		switch kind {
		case cognitiveType:
			props["privateEndpointConnections"] = []any{map[string]any{"id": byKind[cognitivePECType]["id"]}}
			props["commitmentPlanAssociations"] = []any{map[string]any{"commitmentPlanId": byKind[cognitiveSharedPlanType]["id"]}}
			props["associatedProjects"] = []any{"project-one"}
			props["defaultProject"] = "project-one"
		case cognitiveAssociationType:
			props["accountId"] = byKind[cognitiveType]["id"]
		case cognitivePolicyType:
			props["type"] = "UserManaged"
			props["customBlocklists"] = []any{map[string]any{"blocklistName": "blocklist-one", "source": "Prompt"}}
		case cognitiveDeploymentType:
			props["raiPolicyName"] = last(text(byKind[cognitivePolicyType]["id"]))
			delete(props, "parentDeploymentName")
			delete(props, "spilloverDeploymentName")
			props["provisioningState"] = "Succeeded"
		case cognitiveHostType:
			props["storageConnections"] = []any{byKind[cognitiveConnectionType]["id"]}
		case cognitiveProjectHostType:
			props["storageConnections"] = []any{byKind[cognitiveProjectConnectionType]["id"]}
			props["vectorStoreConnections"] = []any{byKind[cognitiveConnectionType]["id"]}
		case cognitiveNetworkType:
			props["provisioningState"] = "Succeeded"
			object(props["managedNetwork"])["outboundRules"] = map[string]any{last(text(byKind[cognitiveOutboundType]["id"])): byKind[cognitiveOutboundType]["properties"]}
		case cognitiveToolType:
			props["projectScopes"] = []any{map[string]any{"project": "project-one", "labelValues": map[string]any{"label": "low"}}}
		}
	}
	for _, raw := range raws {
		kind := text(raw["type"])
		id := text(raw["id"])
		path := redisParentID(id) + "/" + strings.ToLower(last(kind))
		if kind == cognitiveType || kind == cognitiveSharedPlanType {
			path = root + "/providers/" + strings.ToLower(kind)
		}
		s.lists[path] = append(s.lists[path], raw)
		s.version[path] = "2026-05-01"
	}
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method == "GET" && strings.HasSuffix(strings.ToLower(req.URL.Path), "/connections") && req.URL.Query().Get("includeAll") != "true" {
			t.Fatal("datastore connections were omitted", req.URL)
		}
		return nil, false
	}
	r := s.runtime(t)
	assets := []asset.Asset{}
	for _, raw := range raws {
		value := dnsAsset(t, r, raw)
		value.Location = "eastus"
		mapping, _ := findType(value.Identity.NativeType)
		if mapping.ReadOnly || value.Normalized["cleanup_controller_only"] == true {
			value.Capabilities = nil
		}
		assets = append(assets, value)
	}
	return s, r, assets
}
func TestCognitiveNativeInventory(t *testing.T) {
	s, r, assets := cognitiveScenario(t)
	_ = s
	if len(assets) != 23 {
		t.Fatal("missing cognitive kinds", len(assets))
	}
	for _, value := range assets {
		t.Run(value.Identity.NativeType, func(t *testing.T) {
			batch, err := r.List(context.Background(), productRequest(r, value.Identity.NativeType))
			if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.Items[0].Location != "eastus" {
				t.Fatalf("Cognitive inventory error=%v complete=%v items=%d", err, batch.Complete, len(batch.Items))
			}
			item := batch.Items[0]
			if len(object(item.Normalized["_cognitive_ancestors"])) != len(cognitiveAncestorIDs(item.NativeID)) {
				t.Fatal("missing ancestor bindings")
			}
		})
	}
}
func TestCognitiveReviewedAccountCleanup(t *testing.T) {
	s, r, assets := cognitiveScenario(t)
	target := cdnAsset(t, assets, cognitiveType)
	request, input := dnsRequest(t, r, assets, target)
	solved, err := plan.Solve(input)
	if err != nil || len(solved.Blockers) > 0 {
		t.Fatal("Cognitive plan", err, solved.Blockers)
	}
	driver, _ := r.ResolveAction(context.Background(), "connection", target)
	if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
		t.Fatal("account bypassed prerequisite deletion")
	}
	for _, step := range solved.Steps {
		value := assets[slices.IndexFunc(assets, func(a asset.Asset) bool { return a.ID == step.AssetID })]
		subrequest := servicePlanRequest(solved, assets, value)
		actionDriver, err := r.ResolveAction(context.Background(), "connection", value)
		if err != nil {
			t.Fatal(err)
		}
		result, err := actionDriver.Execute(context.Background(), subrequest)
		if err != nil {
			t.Fatalf("Cognitive deletion %s: %v", value.Identity.NativeType, err)
		}
		for _, impact := range subrequest.LifecycleImpacts {
			s.gone[impact.Asset.Identity.NativeID] = true
		}
		// Native mutations maintain parent indexes; this is an explicit composite
		// protocol scenario, not an independent cloud emulator.
		account := object(s.records[target.Identity.NativeID]["properties"])
		for _, pair := range [][2]string{{"privateEndpointConnections", cognitivePECType}, {"commitmentPlanAssociations", cognitiveAssociationType}, {"associatedProjects", cognitiveProjectType}} {
			child := cdnAsset(t, assets, pair[1])
			if s.gone[child.Identity.NativeID] {
				account[pair[0]] = []any{}
			}
		}
		if s.gone[cdnAsset(t, assets, cognitiveProjectType).Identity.NativeID] {
			account["defaultProject"] = ""
		}
		if s.gone[cdnAsset(t, assets, cognitiveOutboundType).Identity.NativeID] {
			object(object(s.records[cdnAsset(t, assets, cognitiveNetworkType).Identity.NativeID]["properties"])["managedNetwork"])["outboundRules"] = map[string]any{}
		}
		body, _ := json.Marshal(subrequest)
		json.Unmarshal(body, &subrequest)
		body, _ = json.Marshal(result)
		json.Unmarshal(body, &result)
		actionDriver, _ = r.ResolveAction(context.Background(), "connection", subrequest.Asset)
		waited, err := actionDriver.Wait(context.Background(), subrequest, result)
		if err != nil || !waited.Done {
			t.Fatal("Cognitive resumed wait", value.Identity.NativeType, waited, err)
		}
	}
	if !s.gone[target.Identity.NativeID] || s.gone[cdnAsset(t, assets, cognitiveSharedPlanType).Identity.NativeID] {
		t.Fatal("wrong account/shared plan cleanup")
	}
}
func TestCognitiveNativeSchemasAndProvenance(t *testing.T) {
	payload, _ := os.ReadFile("fixtures/cognitive/sources.json")
	var manifest []map[string]string
	if json.Unmarshal(payload, &manifest) != nil || len(manifest) != 67 {
		t.Fatal("invalid cognitive sources")
	}
	payload, _ = os.ReadFile("catalog/source/swagger.json")
	var set catalog.RESTDocumentSet
	json.Unmarshal(payload, &set)
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	for _, doc := range set.Documents {
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(doc.Document))
		if err := compiler.AddResource(doc.SourceURI, value); err != nil {
			t.Fatal(err)
		}
	}
	checked := 0
	for _, entry := range manifest {
		payload, err := os.ReadFile("fixtures/cognitive/" + entry["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry["source_sha256"] {
			t.Fatal("official fixture changed")
		}
		for _, doc := range set.Documents {
			if !strings.Contains(doc.SourceURI, "/specification/cognitiveservices/") {
				continue
			}
			var native map[string]any
			json.Unmarshal(doc.Document, &native)
			for path, item := range object(native["paths"]) {
				op := object(object(item)["get"])
				if text(op["operationId"]) != entry["operation"] {
					continue
				}
				schema, err := compiler.Compile(doc.SourceURI + "#/paths/" + strings.ReplaceAll(path, "/", "~1") + "/get/responses/200/schema")
				if err != nil {
					t.Fatal(err)
				}
				raw := object(object(object(cognitiveExample(t, entry["file"])["responses"])["200"])["body"])
				body, _ := json.Marshal(raw)
				value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(body))
				if err := schema.Validate(value); err != nil {
					t.Errorf("%s: %v", entry["operation"], err)
				}
				checked++
			}
		}
	}
	if checked != 46 {
		t.Fatal("missing Cognitive schema checks", checked)
	}
}

func TestCognitiveSecretsStayOutOfInventoryAndLogs(t *testing.T) {
	s, r, assets := cognitiveScenario(t)
	root := s.records[cdnAsset(t, assets, cognitiveType).Identity.NativeID]
	object(root["properties"])["migrationToken"] = "cognitive-migration-secret"
	object(root["properties"])["apiProperties"] = map[string]any{"qnaAzureSearchEndpointKey": "cognitive-qna-secret"}
	connection := s.records[cdnAsset(t, assets, cognitiveConnectionType).Identity.NativeID]
	object(connection["properties"])["credentials"] = map[string]any{"key": "cognitive-credential-secret", "refreshToken": "cognitive-refresh-secret"}
	object(connection["properties"])["target"] = "https://user:cognitive-url-password@api.example/path?sig=cognitive-target-signature"
	topic := s.records[cdnAsset(t, assets, cognitiveTopicType).Identity.NativeID]
	object(topic["properties"])["sampleBlobUrl"] = "https://account.blob.core.windows.net/container/blob?sig=cognitive-blob-signature"
	logs := []execution.JobLogEntry{}
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, e execution.JobLogEntry) { logs = append(logs, e) }))
	for _, kind := range []string{cognitiveType, cognitiveConnectionType, cognitiveTopicType} {
		batch, err := r.List(ctx, productRequest(r, kind))
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(batch)
		logBytes, _ := json.Marshal(logs)
		payload = append(payload, logBytes...)
		for _, secret := range []string{"cognitive-migration-secret", "cognitive-qna-secret", "cognitive-credential-secret", "cognitive-refresh-secret", "cognitive-url-password", "cognitive-target-signature", "cognitive-blob-signature"} {
			if bytes.Contains(payload, []byte(secret)) {
				t.Fatal("Cognitive secret leaked", secret)
			}
		}
	}
	if object(root["properties"])["migrationToken"] != "cognitive-migration-secret" {
		t.Fatal("native data was mutated by redaction")
	}
}
