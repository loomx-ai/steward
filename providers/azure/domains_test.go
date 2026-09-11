package azure

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const domainVersion = "2024-11-01"

func domainExample(t *testing.T, operation string) map[string]any {
	t.Helper()
	payload, err := os.ReadFile("fixtures/domains/" + operation + ".json")
	var value map[string]any
	if err != nil || json.Unmarshal(payload, &value) != nil {
		t.Fatal("invalid DomainRegistration example", err)
	}
	return value
}

func domainScenario(t *testing.T, withBindings bool) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s := newDNSScenario()
	var assets []asset.Asset
	if withBindings {
		s, _, assets = appServiceScenario(t)
	}
	root := "/subscriptions/" + testSubscription
	if !withBindings {
		s.lists[root+"/providers/microsoft.web/sites"] = []any{}
		s.version[root+"/providers/microsoft.web/sites"] = appServiceVersion
	}
	var raws []map[string]any
	for _, operation := range []string{"Domains_Get", "Domains_GetOwnershipIdentifier"} {
		raw := object(object(object(domainExample(t, operation)["responses"])["200"])["body"])
		payload, _ := json.Marshal(raw)
		payload = bytes.ReplaceAll(payload, []byte("34adfa4f-cedf-4dc0-ba29-b6d1a69ab345"), []byte(testSubscription))
		json.Unmarshal(payload, &raw)
		id, kind, _ := parseID(text(raw["id"]))
		metadata, _ := findType(kind)
		raw["id"], raw["type"] = id, metadata.NativeType
		raw["etag"] = "domain-inventory-generation"
		if metadata.NativeType == domainType {
			object(raw["properties"])["futureAuthoredSetting"] = "domain-private-future-value"
		} else {
			object(raw["properties"])["ownershipId"] = "domain-private-ownership-token"
		}
		s.add(raw, domainVersion)
		raws = append(raws, raw)
		collection := id[:strings.LastIndex(id, "/")]
		if metadata.NativeType == domainType {
			collection = root + "/providers/microsoft.domainregistration/domains"
		}
		s.lists[collection], s.version[collection] = []any{raw}, domainVersion
	}
	domain := raws[0]
	zone := map[string]any{"id": resourceID(publicDNSZoneType, "example.com"), "name": "example.com", "type": publicDNSZoneType, "location": "global", "etag": "retained-dns-zone", "properties": map[string]any{"zoneType": "Public"}}
	s.add(zone, "2018-05-01")
	for _, kind := range dnsChildTypes(publicDNSZoneType) {
		path := strings.ToLower(text(zone["id"])) + "/" + strings.ToLower(last(kind))
		s.lists[path], s.version[path] = []any{}, "2018-05-01"
	}
	object(domain["properties"])["dnsZoneId"] = zone["id"]
	object(domain["properties"])["dnsType"] = "AzureDns"
	if withBindings {
		for _, value := range assets {
			if !isAppBinding(value.Identity.NativeType) || !strings.HasPrefix(last(value.Identity.NativeID), "custom-") {
				continue
			}
			binding := s.records[value.Identity.NativeID]
			object(binding["properties"])["domainId"] = domain["id"]
			object(domain["properties"])["managedHostNames"] = append(array(object(domain["properties"])["managedHostNames"]), map[string]any{"name": last(value.Identity.NativeID), "azureResourceType": "Website", "siteNames": []any{"sitef6141"}})
		}
	}
	groups := map[string]bool{}
	for id := range s.records {
		parts := strings.Split(id, "/")
		if len(parts) >= 5 && parts[3] == "resourcegroups" {
			groups[strings.Join(parts[:5], "/")] = true
		}
	}
	s.lists[root+"/resourcegroups"] = nil
	for group := range groups {
		row := map[string]any{"id": group, "name": last(group), "type": groupType, "location": "westus", "properties": map[string]any{"provisioningState": "Succeeded"}}
		s.add(row, resourcesVersion)
		s.lists[root+"/resourcegroups"] = append(s.lists[root+"/resourcegroups"], row)
		collection := group + "/providers/microsoft.network/dnszones"
		s.lists[collection], s.version[collection] = []any{}, "2018-05-01"
		if strings.HasPrefix(strings.ToLower(text(zone["id"])), group+"/") {
			s.lists[collection] = []any{zone}
		}
	}
	r := s.runtime(t)
	// Re-normalize the native bindings after adding their DomainRegistration ID.
	for i := range assets {
		assets[i] = dnsAsset(t, r, s.records[assets[i].Identity.NativeID])
	}
	for _, raw := range append(raws, zone) {
		assets = append(assets, dnsAsset(t, r, raw))
	}
	return s, r, assets
}

func TestDomainOfficialContracts(t *testing.T) {
	payload, err := os.ReadFile("fixtures/domains/sources.json")
	var sources []map[string]string
	if err != nil || json.Unmarshal(payload, &sources) != nil || len(sources) != 7 {
		t.Fatal("invalid domain fixture provenance", err)
	}
	payload, _ = os.ReadFile("catalog/source/swagger.json")
	var documents catalog.RESTDocumentSet
	if err := json.Unmarshal(payload, &documents); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	for _, doc := range documents.Documents {
		value, _ := jsonschema.UnmarshalJSON(bytes.NewReader(doc.Document))
		if err := compiler.AddResource(doc.SourceURI, value); err != nil {
			t.Fatal(err)
		}
	}
	responses, bodies := 0, 0
	for _, source := range sources {
		payload, err := os.ReadFile("fixtures/domains/" + source["file"])
		if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != source["source_sha256"] || !strings.Contains(source["source_uri"], "/c20bf553ad64f20c6d5e3f56080380c086cb1fde/") {
			t.Fatal("domain native example changed", source, err)
		}
		example := domainExample(t, source["operation"])
		metadata, err := providerData()
		if err != nil {
			t.Fatal(err)
		}
		op, ok := metadata.catalog.Operation("Azure.Microsoft.DomainRegistration." + source["operation"])
		if !ok || op.Call.Version != domainVersion || op.Call.Method != source["method"] || op.Call.Path != source["path"] {
			t.Fatal("domain native operation was changed", op)
		}
		parameters := object(example["parameters"])
		delete(parameters, "getOnlyIfReadyForDnsManagement") // Extra upstream example parameter, absent from the contract.
		bound, err := catalog.BindREST(op, parameters)
		if err != nil || !strings.Contains(bound.URL, "api-version="+domainVersion) {
			t.Fatal("native domain request did not bind", err)
		}
		for status, response := range object(example["responses"]) {
			responses++
			body := object(response)["body"]
			if body == nil {
				continue
			}
			pointer := "#/paths/" + strings.ReplaceAll(source["path"], "/", "~1") + "/" + strings.ToLower(source["method"]) + "/responses/" + status + "/schema"
			schema, err := compiler.Compile(source["source_document"] + pointer)
			if err != nil || schema.Validate(body) != nil {
				t.Fatal("domain example failed its native schema", source["operation"], err)
			}
			bodies++
		}
	}
	if responses != 9 || bodies != 5 {
		t.Fatal("missing native domain response coverage", responses, bodies)
	}
}

func TestDomainNativeInventoryAndPrivacy(t *testing.T) {
	s, r, assets := domainScenario(t, true)
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	for _, kind := range []string{domainType, domainOwnershipType} {
		request := productRequest(r, kind)
		request.Scope = asset.Scope{Kind: asset.ScopeGlobal, NativeID: testSubscription + "/global"}
		batch, err := r.List(ctx, request)
		if err != nil || !batch.Complete || len(batch.Items) != 1 {
			t.Fatal("native global domain discovery", kind, batch, err)
		}
		item := batch.Items[0]
		if item.NativeType != kind || item.Location != "global" || item.Scope.Kind != asset.ScopeGlobal || text(item.Normalized[domainProof]) == "" {
			t.Fatal("domain lost its native identity/scope/proof", item)
		}
		if kind == domainType && len(object(item.Normalized[domainDependencies])) != 3 {
			t.Fatal("domain scan lost ownership and native bindings")
		}
		for _, value := range []any{batch, item.Raw, item.Normalized, logs} {
			payload, _ := json.Marshal(value)
			for _, private := range []string{"exampleAuthCode", "contactRegistrant", "admin@email.com", "3400 State St", "agreementKey1", "domain-private-ownership-token", "domain-private-future-value"} {
				if bytes.Contains(payload, []byte(private)) {
					t.Fatal("domain private data escaped", private)
				}
			}
		}
	}
	if len(logs) == 0 || len(s.deletes) != 0 || len(assets) < 3 {
		t.Fatal("inventory did not exercise read-only native logging")
	}
}

func TestDomainNativeDependencyPlanAndDelayedCleanup(t *testing.T) {
	s, r, assets := domainScenario(t, true)
	target := cdnAsset(t, assets, domainType)
	request, input := dnsRequest(t, r, assets, target)
	if len(request.PrerequisiteDeletions) != 3 || len(request.LifecycleImpacts) != 0 {
		t.Fatal("domain lost independent prerequisite review", request)
	}
	solved, err := plan.Solve(input)
	if err != nil || len(solved.Steps) != 4 || len(solved.ImpactItems) != 0 {
		t.Fatal("domain should schedule three independent deletes before registration", solved, err)
	}
	driver, err := r.ResolveAction(t.Context(), "connection", target)
	if err != nil {
		t.Fatal(err)
	}
	if timeout, ok := driver.(interface{ DeletionCheckTimeout() time.Duration }); !ok || timeout.DeletionCheckTimeout() != 48*time.Hour {
		t.Fatal("domain timeout cannot span the native 24-hour delay")
	}
	if check, err := driver.Preflight(t.Context(), request); err == nil && check.Allowed {
		t.Fatal("live domain prerequisites did not block registration deletion")
	}
	for _, prerequisite := range request.PrerequisiteDeletions {
		child := prerequisite.Asset
		childDriver, err := r.ResolveAction(t.Context(), "connection", child)
		if err != nil {
			t.Fatal(err)
		}
		result, err := childDriver.Execute(t.Context(), contracts.ActionRequest{Asset: child, Action: "delete", IdempotencyKey: "domain-child"})
		if err != nil {
			t.Fatal("native independent domain dependency delete", child.Identity.NativeType, err)
		}
		if waited, err := childDriver.Wait(t.Context(), contracts.ActionRequest{Asset: child, Action: "delete"}, result); err != nil || !waited.Done {
			t.Fatal("dependency did not verify its own absence", waited, err)
		}
	}
	object(s.records[target.Identity.NativeID]["properties"])["managedHostNames"] = []any{}
	request.IdempotencyKey = "domain-root"
	writes := 0
	s.handle = func(req *http.Request) (*http.Response, bool) {
		if req.Method != "DELETE" {
			return nil, false
		}
		writes++
		if !strings.EqualFold(req.URL.Path, target.Identity.NativeID) || req.URL.Query().Get("api-version") != domainVersion || req.URL.Query().Get("forceHardDeleteDomain") != "false" || req.Header.Get("x-ms-client-request-id") != azureRequestID(request.IdempotencyKey) {
			t.Fatal("wrong native domain delete", req.URL, req.Header)
		}
		object(s.records[target.Identity.NativeID]["properties"])["provisioningState"] = "Deleting"
		return jsonResponse(200, nil, http.Header{"X-Ms-Request-Id": {"domain-delete-receipt"}}), true
	}
	result, err := driver.Execute(t.Context(), request)
	if err != nil || writes != 1 {
		t.Fatal("native delayed domain delete failed", result, writes, err)
	}
	result, request = roundTripDomainAction(t, result, request)
	driver, err = s.runtime(t).ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if waited, err := driver.Wait(t.Context(), request, result); err != nil || waited.Done || writes != 1 {
		t.Fatal("receipt or restart incorrectly completed/repeated delayed delete", waited, err)
	}
	s.gone[target.Identity.NativeID] = true
	// Parent absence cannot hide a recreated/list-omitted dependency still alive.
	child := request.PrerequisiteDeletions[0].Asset
	s.gone[child.Identity.NativeID] = false
	if waited, err := driver.Wait(t.Context(), request, result); err != nil || waited.Done {
		t.Fatal("parent absence hid a surviving dependency", waited, err)
	}
	s.gone[child.Identity.NativeID] = true
	if waited, err := driver.Wait(t.Context(), request, result); err != nil || !waited.Done {
		t.Fatal("domain and dependencies did not reach native absence", waited, err)
	}
	zone := cdnAsset(t, assets, publicDNSZoneType)
	if s.gone[zone.Identity.NativeID] || slices.Contains(s.deletes, zone.Identity.NativeID) || writes != 1 {
		t.Fatal("domain cleanup changed independent DNS lifetime")
	}
}

func TestDomainBindingBatchPreservesAppConfiguration(t *testing.T) {
	s, r, assets := domainScenario(t, true)
	target := cdnAsset(t, assets, domainType)
	request, _ := dnsRequest(t, r, assets, target)
	bindings := slices.Clone(request.PrerequisiteDeletions)
	slices.SortFunc(bindings, func(a, b contracts.ActionImpact) int {
		return strings.Compare(a.Asset.Identity.NativeType, b.Asset.Identity.NativeType)
	})
	s.handle = func(req *http.Request) (*http.Response, bool) {
		id := strings.ToLower(req.URL.Path)
		_, kind, _ := parseID(id)
		if req.Method != "DELETE" || !strings.Contains(kind, "/hostnamebindings") {
			return nil, false
		}
		props := object(s.records[redisParentID(id)]["properties"])
		for _, field := range []string{"hostNames", "enabledHostNames"} {
			values := array(props[field])
			props[field] = slices.DeleteFunc(slices.Clone(values), func(value any) bool { return strings.EqualFold(text(value), last(id)) })
		}
		props["hostNameSslStates"] = slices.DeleteFunc(slices.Clone(array(props["hostNameSslStates"])), func(value any) bool { return strings.EqualFold(text(object(value)["name"]), last(id)) })
		s.gone[id] = true
		return jsonResponse(204, nil, nil), true
	}
	for _, prerequisite := range bindings {
		value := prerequisite.Asset
		if !isAppBinding(value.Identity.NativeType) {
			continue
		}
		driver, err := r.ResolveAction(t.Context(), "connection", value)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := driver.Execute(t.Context(), contracts.ActionRequest{Asset: value, Action: "delete"}); err != nil {
			t.Fatal("a prior reviewed binding removal prevented the next native delete", value.Identity.NativeType, err)
		}
	}
}

func roundTripDomainAction(t *testing.T, result contracts.ActionResult, request contracts.ActionRequest) (contracts.ActionResult, contracts.ActionRequest) {
	t.Helper()
	payload, err := json.Marshal(result)
	if err != nil || json.Unmarshal(payload, &result) != nil {
		t.Fatal("domain result is not durable", err)
	}
	payload, err = json.Marshal(request)
	if err != nil || json.Unmarshal(payload, &request) != nil {
		t.Fatal("domain request is not durable", err)
	}
	return result, request
}
