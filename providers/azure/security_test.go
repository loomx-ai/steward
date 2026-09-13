package azure

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

func defenderExample(t *testing.T, name string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("fixtures/security/" + name + ".json")
	var result map[string]any
	if err != nil || json.Unmarshal(raw, &result) != nil {
		t.Fatal(name, err)
	}
	return result
}

func TestDefenderOfficialContracts(t *testing.T) {
	raw, err := os.ReadFile("fixtures/security/sources.json")
	var sources []map[string]string
	if err != nil || json.Unmarshal(raw, &sources) != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile("catalog/source/swagger.json")
	var documents catalog.RESTDocumentSet
	if err != nil || json.Unmarshal(raw, &documents) != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	for _, doc := range documents.Documents {
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc.Document))
		if err != nil {
			t.Fatal(err)
		}
		if err := compiler.AddResource(doc.SourceURI, value); err != nil {
			t.Fatal(err)
		}
	}
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	responses, bodies := 0, 0
	for _, source := range sources {
		t.Run(source["file"], func(t *testing.T) {
			payload, err := os.ReadFile("fixtures/security/" + source["file"])
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != source["source_sha256"] {
				t.Fatal("official example changed", err)
			}
			op, ok := metadata.catalog.Operation("Azure.Microsoft.Security." + source["operation"])
			if !ok || !strings.Contains(op.SourceURI, "/c20bf553ad64f20c6d5e3f56080380c086cb1fde/") {
				t.Fatal("native pricing operation missing")
			}
			example := defenderExample(t, strings.TrimSuffix(source["file"], ".json"))
			parameters := object(example["parameters"])
			if source["file"] == "ListPricingsWithPlanFilter_example.json" {
				if parameters["$Filter"] == nil {
					t.Fatal("native filter typo changed")
				}
				if _, err := catalog.BindREST(op, parameters); err == nil {
					t.Fatal("wrong-case filter accepted")
				}
				parameters["$filter"] = parameters["$Filter"]
				delete(parameters, "$Filter")
			}
			if _, err := catalog.BindREST(op, parameters); err != nil {
				t.Error("native request", err)
			}
			for status, response := range object(example["responses"]) {
				responses++
				body := object(response)["body"]
				if body == nil {
					continue
				}
				pointer := "#/paths/" + strings.ReplaceAll(op.Call.Path, "/", "~1") + "/" + strings.ToLower(op.Call.Method) + "/responses/" + status + "/schema"
				schema, err := compiler.Compile(op.SourceURI + pointer)
				if err != nil {
					t.Fatal(err)
				}
				var nulls []map[string]any
				if source["file"] == "ListResourcePricings_example.json" {
					nulls = append(nulls, object(object(array(object(body)["value"])[1])["properties"]))
				}
				if slices.Contains([]string{"GetResourcePricingByNameContainers_example.json", "PutResourcePricingByNameContainersACR_example.json", "PutResourcePricingByNameContainers_example.json", "PutResourcePricingByNameVirtualMachines_example.json"}, source["file"]) {
					nulls = append(nulls, object(object(body)["properties"]))
				}
				if len(nulls) != 0 {
					if schema.Validate(body) == nil {
						t.Fatal("native non-nullable inheritedFrom discrepancy changed")
					}
					for _, props := range nulls {
						value, present := props["inheritedFrom"]
						if !present || value != nil || props["inherited"] != "False" {
							t.Fatal("native null inheritance changed")
						}
						delete(props, "inheritedFrom")
					}
				}
				if err := schema.Validate(body); err != nil {
					t.Error("native response", err)
				}
				bodies++
			}
		})
	}
	if len(sources) != 18 || bodies == 0 {
		t.Fatal("incomplete native evidence", len(sources), responses, bodies)
	}
	t.Log("native responses", responses, "schema-validated bodies", bodies)
}

type defenderFixture struct {
	runtime        *Runtime
	client         *client
	plans, parents map[string]map[string]any
	omitted        map[string]bool
	override       func(*http.Request) (*http.Response, bool)
}

func newDefenderFixture(t *testing.T) *defenderFixture {
	t.Helper()
	f := &defenderFixture{plans: map[string]map[string]any{}, parents: map[string]map[string]any{}, omitted: map[string]bool{}}
	root := "/subscriptions/" + testSubscription
	for _, name := range []string{"VirtualMachines", "Containers"} {
		example := defenderExample(t, "GetPricingByName"+name+"_example")
		raw := object(object(object(example["responses"])["200"])["body"])
		id := root + "/providers/microsoft.security/pricings/" + strings.ToLower(name)
		raw["id"], raw["type"], raw["name"] = id, defenderPricingType, name
		raw["privateFutureSetting"] = "private-defender-input"
		f.plans[id] = raw
	}
	for _, kind := range defenderScopeKinds {
		parent := strings.ToLower(resourceID(kind, "protected"))
		f.parents[parent] = map[string]any{"id": parent, "type": kind, "name": "protected", "location": "westus", "properties": map[string]any{"provisioningState": "Succeeded", "resourceGuid": "native-generation"}}
		plan := "VirtualMachines"
		if kind == aksType || kind == "Microsoft.ContainerRegistry/registries" {
			plan = "Containers"
		}
		id := parent + "/providers/microsoft.security/pricings/" + strings.ToLower(plan)
		raw := batchClone(f.plans[root+"/providers/microsoft.security/pricings/"+strings.ToLower(plan)])
		raw["id"] = id
		props := object(raw["properties"])
		delete(props, "enforce")
		delete(props, "resourcesCoverageStatus")
		props["inherited"], props["inheritedFrom"] = "True", root
		f.plans[id] = raw
	}
	f.runtime = protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if f.override != nil {
			if res, ok := f.override(req); ok {
				return res, nil
			}
		}
		if req.Method != "GET" || req.URL.Host != "management.azure.com" || len(req.URL.Query()) != 1 {
			t.Fatal("unexpected Defender request", req.Method, req.URL)
		}
		path := strings.ToLower(req.URL.Path)
		if path == root {
			if req.URL.Query().Get("api-version") != "2022-12-01" {
				t.Fatal(req.URL)
			}
			return jsonResponse(200, map[string]any{"subscriptionId": testSubscription, "id": root, "state": "Enabled", "tenantId": testTenant}, nil), nil
		}
		if armPathProvider(path) == "microsoft.security" {
			if req.URL.Query().Get("api-version") != defenderVersion {
				t.Fatal(req.URL)
			}
			if strings.HasSuffix(path, "/providers/microsoft.security/pricings") {
				parent := strings.TrimSuffix(path, "/providers/microsoft.security/pricings")
				_, kind, _ := parseID(parent)
				if strings.EqualFold(kind, aksType) || strings.EqualFold(kind, "Microsoft.ContainerRegistry/registries") {
					t.Fatal("native container scope has no proven pricing LIST contract")
				}
				rows := []any{}
				for _, id := range slices.Sorted(maps.Keys(f.plans)) {
					if id[:strings.LastIndex(id, "/")] == path && !f.omitted[id] {
						rows = append(rows, f.plans[id])
					}
				}
				return jsonResponse(200, map[string]any{"value": rows}, http.Header{"X-Ms-Request-Id": {"defender-native-request"}}), nil
			}
			if raw := f.plans[path]; raw != nil {
				return jsonResponse(200, raw, nil), nil
			}
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
		}
		for _, kind := range defenderScopeKinds {
			mapping := defenderParentKind(kind)
			collection := root + "/providers/" + strings.ToLower(kind)
			if path == collection {
				if req.URL.Query().Get("api-version") != mapping.Version {
					t.Fatal("parent version", req.URL)
				}
				rows := []any{}
				for _, id := range slices.Sorted(maps.Keys(f.parents)) {
					if f.parents[id]["type"] == kind && !f.omitted[id] {
						rows = append(rows, f.parents[id])
					}
				}
				return jsonResponse(200, map[string]any{"value": rows}, nil), nil
			}
			if raw := f.parents[path]; raw != nil && raw["type"] == kind {
				if req.URL.Query().Get("api-version") != mapping.Version {
					t.Fatal("parent read version", req.URL)
				}
				return jsonResponse(200, raw, nil), nil
			}
		}
		return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), nil
	})
	var err error
	f.client, err = f.runtime.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *defenderFixture) request() contracts.InventoryRequest {
	kind := f.runtime.resourceKind(defenderPricingType)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: defenderInventorySource, ResourceKind: &kind, Scope: asset.Scope{Kind: asset.ScopeGlobal, NativeID: testSubscription + "/global"}}
}

func TestDefenderNativeInventoryAndPaging(t *testing.T) {
	f := newDefenderFixture(t)
	request := f.request()
	request.Limit = 2
	seen := map[string]bool{}
	for {
		batch, err := f.runtime.List(t.Context(), request)
		if err != nil {
			t.Fatal(err)
		}
		if batch.RequestID != "defender-native-request" {
			t.Fatal("lost request provenance")
		}
		for _, item := range batch.Items {
			if seen[item.NativeID] || item.Actionable == nil || *item.Actionable || item.Location != "global" || item.Normalized["state"] != "Standard" {
				t.Fatal("invalid native plan", item)
			}
			seen[item.NativeID] = true
			value := asset.Asset{ID: asset.AssetID(item.NativeID), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: defenderPricingType, NativeID: item.NativeID}, Normalized: item.Normalized}
			if _, err := f.client.defenderRecordedReferences(value); err != nil {
				t.Fatal("native reference binding required a serialization round trip", err)
			}
			_, scope, _ := f.client.defenderIdentity(item.NativeID)
			if scope != f.client.root() && !slices.Contains(item.NetworkReferences, scope) {
				t.Fatal("lost native resource scope")
			}
			encoded, _ := json.Marshal(item)
			if strings.Contains(string(encoded), "private-defender-input") || strings.Contains(string(encoded), "ExclusionTags") {
				t.Fatal("private plan input leaked")
			}
		}
		if batch.Complete {
			break
		}
		request.Cursor = batch.NextCursor
	}
	if len(seen) != 7 {
		t.Fatal("missing native scopes", len(seen))
	}
}

func TestDefenderKnownAndFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"omitted-plan", "omitted-parent", "own-absence", "parent-absence", "forbidden", "bad-state", "foreign-inheritance", "filtered-request", "private-change"} {
		t.Run(mode, func(t *testing.T) {
			f := newDefenderFixture(t)
			id := strings.ToLower(resourceID(vmType, "protected")) + "/providers/microsoft.security/pricings/virtualmachines"
			_, parent, _ := f.client.defenderIdentity(id)
			request := f.request()
			request.KnownNativeIDs = []string{id}
			switch mode {
			case "omitted-plan":
				f.omitted[id] = true
			case "omitted-parent":
				f.omitted[parent] = true
			case "own-absence":
				delete(f.plans, id)
			case "parent-absence":
				delete(f.parents, parent)
			case "forbidden":
				f.override = func(req *http.Request) (*http.Response, bool) {
					if strings.EqualFold(req.URL.Path, id) {
						return jsonResponse(403, nil, nil), true
					}
					return nil, false
				}
			case "bad-state":
				object(f.plans[id]["properties"])["pricingTier"] = "Unknown"
			case "foreign-inheritance":
				object(f.plans[id]["properties"])["inheritedFrom"] = "/subscriptions/00000000-1111-2222-3333-444444444444"
			case "filtered-request":
				request.Options = map[string]any{"$filter": "name in (VirtualMachines)"}
			case "private-change":
				request.Limit = 1
				page, err := f.runtime.List(t.Context(), request)
				if err != nil {
					t.Fatal(err)
				}
				request.Cursor = page.NextCursor
				f.plans[id]["privateFutureSetting"] = "changed"
			}
			batch, err := f.runtime.List(t.Context(), request)
			switch mode {
			case "omitted-plan", "omitted-parent":
				if err != nil || len(batch.Items) != 7 || len(batch.AbsentNativeIDs) != 0 {
					t.Fatal("known native plan lost", batch, err)
				}
			case "own-absence":
				if err != nil || !slices.Equal(batch.AbsentNativeIDs, []string{id}) {
					t.Fatal("own absence not reconciled", err)
				}
			default:
				if err == nil || isNotFound(err) {
					t.Fatal("unsafe native observation accepted", err)
				}
			}
		})
	}
}

func TestDefenderRegisteredGlobalScanAndGraph(t *testing.T) {
	f := newDefenderFixture(t)
	repository, registry, _ := azureNativeWorkerRepository(t, f.runtime)
	creator, err := inventory.NewCreator(repository, registry)
	if err != nil {
		t.Fatal(err)
	}
	scan := func(fail bool) []asset.Asset {
		created, err := creator.Create(t.Context(), inventory.ScanCreationRequest{ConnectionID: "connection", RequestedBy: "defender-native-worker", RegionMode: inventory.RegionModeSelected, RegionIDs: []string{"global"}, ResourceKindIDs: []asset.ResourceKindID{f.runtime.resourceKind(defenderPricingType).ID}})
		if err != nil || len(created.Shards) != 1 || created.Shards[0].Source != defenderInventorySource || created.Shards[0].Authoritative {
			t.Fatal("global native shard", created.Shards, err)
		}
		legacy := created.Shards[0]
		legacy.Authoritative = true
		if err := repository.PutScanShard(t.Context(), legacy); err != nil {
			t.Fatal(err)
		}
		handler := inventory.NewScanHandler(repository, registry, inventory.NewService(repository))
		for _, job := range created.Jobs {
			if err := handler.Handle(t.Context(), job); err != nil && !fail {
				t.Fatal("registered pricing scan", err)
			}
		}
		shard, err := repository.GetScanShard(t.Context(), legacy.ID)
		if err != nil || shard.Authoritative || shard.Coverage.Authoritative || (shard.Status == asset.ShardSucceeded) == fail {
			t.Fatal("unsafe pricing scan coverage", shard.Status, err)
		}
		if !fail {
			jobs, err := repository.ListJobsByAggregate(t.Context(), "scan_task", string(created.ScanRun.ID))
			if err != nil {
				t.Fatal(err)
			}
			graphs := 0
			for _, job := range jobs {
				if job.Type != execution.JobGraph {
					continue
				}
				graphs++
				if err := governance.NewGraphHandler(repository, registry, fleetHubGraphContributors{f.runtime}).Handle(t.Context(), job); err != nil {
					t.Fatal("registered pricing graph", err)
				}
			}
			if graphs != 1 {
				t.Fatal("missing graph reconciliation")
			}
		}
		values, err := repository.ListActiveAssetsByConnection(t.Context(), "connection", "")
		if err != nil {
			t.Fatal(err)
		}
		return values
	}
	values := scan(false)
	if len(values) != 7 {
		t.Fatal("native plan inventory incomplete", len(values))
	}
	for _, value := range values {
		if string(value.ID) == value.Identity.NativeID || value.Capabilities.Has(asset.CapabilityActionable) {
			t.Fatal("pricing acquired cleanup or lost generated identity")
		}
		if _, err := f.runtime.ResolveAction(t.Context(), "connection", value); err == nil {
			t.Fatal("service-state plan acquired a deletion driver")
		}
	}
	relations, err := repository.ListRelationshipsByConnection(t.Context(), "connection")
	if err != nil || len(relations) != 5 {
		t.Fatal("native inheritance edges lost", len(relations), err)
	}
	for _, relation := range relations {
		if relation.Type != graph.RelationshipUses {
			t.Fatal("pricing inherited resource ownership", relation)
		}
	}
	id := strings.ToLower(resourceID(vmType, "protected")) + "/providers/microsoft.security/pricings/virtualmachines"
	f.omitted[id] = true
	if len(scan(false)) != 7 {
		t.Fatal("native list omission erased a plan")
	}
	previous := f.override
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, id) {
			return jsonResponse(403, nil, nil), true
		}
		if previous != nil {
			return previous(req)
		}
		return nil, false
	}
	if len(scan(true)) != 7 {
		t.Fatal("failed scan erased known plans")
	}
	f.override = previous
	delete(f.plans, id)
	if len(scan(false)) != 6 {
		t.Fatal("own plan absence was not reconciled")
	}
	for _, value := range values {
		if value.Identity.NativeID != id {
			continue
		}
		value.Normalized["_defender_references"] = map[string]any{}
		if _, err := f.client.defenderRecordedReferences(value); err == nil {
			t.Fatal("forged native graph reference accepted")
		}
	}
}

func TestDefenderInheritedPlanOmissionAndPrivateProjection(t *testing.T) {
	f := newDefenderFixture(t)
	parent := f.client.root() + "/providers/microsoft.security/pricings/virtualmachines"
	f.omitted[parent] = true
	batch, err := f.runtime.List(t.Context(), f.request())
	if err != nil || len(batch.Items) != 7 {
		t.Fatal("explicit inherited plan reference was not recovered", err)
	}
	for _, item := range batch.Items {
		if item.NativeID != parent {
			continue
		}
		if item.Normalized["resourcesCoverageStatus"] == nil || item.Normalized["enforce"] == nil {
			t.Fatal("subscription service-state semantics lost")
		}
	}
	raw := map[string]any{"properties": map[string]any{"pricingTier": "Free", "subPlan": map[string]any{"hidden": "private-defender-input"}, "extensions": []any{map[string]any{"name": "AgentlessVmScanning", "isEnabled": "False", "additionalExtensionProperties": map[string]any{"ExclusionTags": "private-defender-input"}, "operationStatus": map[string]any{"code": "Failed", "message": "private-defender-input"}}}}}
	encoded, _ := json.Marshal(safeAPIPayload(raw, apiURL(parent, defenderVersion)))
	if strings.Contains(string(encoded), "private-defender-input") || !strings.Contains(string(encoded), "Failed") {
		t.Fatal("native extension diagnostics lost privacy or status", string(encoded))
	}
}

func TestDefenderNativeParentPagination(t *testing.T) {
	for _, mode := range []string{"native", "filtered", "version", "foreign", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			f := newDefenderFixture(t)
			kind := defenderParentKind(defenderArcType)
			path := f.client.root() + "/providers/" + strings.ToLower(defenderArcType)
			id := strings.ToLower(resourceID(defenderArcType, "protected"))
			f.override = func(req *http.Request) (*http.Response, bool) {
				if !strings.EqualFold(req.URL.Path, path) {
					return nil, false
				}
				if req.URL.Query().Get("$skiptoken") == "second" {
					if len(req.URL.Query()) != 2 {
						t.Fatal("unsafe continuation was sent", req.URL)
					}
					return jsonResponse(200, map[string]any{"value": []any{f.parents[id]}}, nil), true
				}
				next := apiURL(path, kind.Version) + "&%24skiptoken=second"
				rows := []any{}
				switch mode {
				case "filtered":
					next += "&%24filter=hidden"
				case "version":
					next = strings.Replace(next, kind.Version, "2020-01-01", 1)
				case "foreign":
					next = strings.Replace(next, "management.azure.com", "other.example", 1)
				case "duplicate":
					rows = append(rows, f.parents[id])
				}
				return jsonResponse(200, map[string]any{"value": rows, "nextLink": next}, nil), true
			}
			batch, err := f.runtime.List(t.Context(), f.request())
			if mode == "native" {
				if err != nil || len(batch.Items) != 7 {
					t.Fatal("native parent pagination failed", err)
				}
			} else if err == nil {
				t.Fatal("unsafe parent pagination accepted", mode)
			}
		})
	}
}

func TestDefenderEffectiveStateAndTrialCountdown(t *testing.T) {
	f := newDefenderFixture(t)
	root := f.client.root() + "/providers/microsoft.security/pricings/virtualmachines"
	id := strings.ToLower(resourceID(vmType, "protected")) + "/providers/microsoft.security/pricings/virtualmachines"
	object(f.plans[root]["properties"])["resourcesCoverageStatus"] = "PartiallyCovered"
	props := object(f.plans[id]["properties"])
	props["pricingTier"], props["inherited"], props["inheritedFrom"] = "Free", "False", nil
	reads := 0
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, root) {
			reads++
			object(f.plans[root]["properties"])["freeTrialRemainingTime"] = fmt.Sprintf("PT%dS", 1000-reads)
		}
		return nil, false
	}
	batch, err := f.runtime.List(t.Context(), f.request())
	if err != nil {
		t.Fatal("counting-down native trial invalidated inventory", err)
	}
	found := 0
	for _, item := range batch.Items {
		if item.NativeID == root {
			found++
			if item.Normalized["state"] != "Standard" || item.Normalized["resourcesCoverageStatus"] != "PartiallyCovered" {
				t.Fatal("subscription tier became full resource coverage")
			}
		}
		if item.NativeID == id {
			found++
			if item.Normalized["state"] != "Free" || item.Normalized["inherited"] != "False" || item.Normalized[referenceKey(defenderPricingType)] != nil {
				t.Fatal("native override was confused with inherited state")
			}
		}
	}
	if found != 2 {
		t.Fatal("effective resource state missing")
	}
}

func TestDefenderPlansFollowNativeNetworkScope(t *testing.T) {
	f := newDefenderFixture(t)
	subnet := strings.ToLower(resourceID(vnetType, "network") + "/subnets/default")
	vm := strings.ToLower(resourceID(vmType, "protected"))
	request := f.request()
	request.Scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: "westus"}
	request.NetworkTarget = &asset.ScanTarget{Kind: asset.ScanTargetVSwitch, NativeID: subnet}
	batch, err := f.runtime.List(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	items := append(batch.Items, contracts.InventoryItem{NativeID: vm, NativeAliases: []string{vm}, NetworkReferences: []string{subnet}})
	selected := inventory.FilterNetworkClosure(*request.NetworkTarget, items)
	if len(selected) != 2 || !slices.ContainsFunc(selected, func(item contracts.InventoryItem) bool {
		return item.NativeID == vm+"/providers/microsoft.security/pricings/virtualmachines"
	}) {
		t.Fatal("network scope lost its plan or included unrelated subscription plans", selected)
	}
}
