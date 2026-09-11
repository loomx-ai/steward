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
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
	"github.com/santhosh-tekuri/jsonschema/v6"
)

const rbacTestClientID = "aaaaaaaa-1111-2222-3333-bbbbbbbbbbbb"

// The identity lives outside the assignment's ARM scope. Only the native
// principal GUID, never path ancestry or clientId, can join these resources.
func rbacPrincipalTarget(t *testing.T, kind string) (*rbacFixture, asset.Asset, asset.Asset, *[]string) {
	t.Helper()
	f := newRBACFixture(t)
	for id, raw := range f.resources {
		if raw["type"] == rbacAssignmentType && id != rbacTestAssignmentID() {
			delete(f.resources, id)
		}
	}
	props := map[string]any{"provisioningState": "Succeeded"}
	if kind == rbacUserIdentityType {
		// Retain the native example's properties; substitute only identities
		// needed to compose it with this subscription's role-assignment fixture.
		body := object(object(object(rbacExample(t, "identities/IdentityGet.json")["responses"])["200"])["body"])
		props = object(body["properties"])
		props["principalId"], props["tenantId"], props["clientId"] = rbacTestPrincipal, testTenant, rbacTestClientID
	}
	raw := nativeResource(kind, "identitytarget", "westus", props)
	raw["id"] = strings.Replace(text(raw["id"]), "/resourceGroups/test/", "/resourceGroups/identities/", 1)
	if kind == storageType {
		raw["kind"] = "StorageV2"
		raw["identity"] = map[string]any{"type": "SystemAssigned", "principalId": rbacTestPrincipal, "tenantId": testTenant}
	}
	id := strings.ToLower(text(raw["id"]))
	group := "/subscriptions/" + testSubscription + "/resourcegroups/identities"
	f.scopes[group] = map[string]any{"id": group, "type": groupType, "name": "identities", "location": "westus", "properties": map[string]any{}}
	f.scopes[id] = raw
	deleted := []string{}
	f.override = func(req *http.Request) (*http.Response, bool) {
		if strings.EqualFold(req.URL.Path, id) {
			if kind == rbacUserIdentityType && req.URL.Query().Get("api-version") != "2023-01-31" {
				t.Fatal("identity lost its native API version", req.URL)
			}
			if f.scopes[id] == nil {
				return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
			}
			if req.Method == "DELETE" {
				deleted = append(deleted, id)
				delete(f.scopes, id)
				return jsonResponse(204, nil, nil), true
			}
		}
		if strings.EqualFold(req.URL.Path, "/subscriptions/"+testSubscription+"/providers/"+rbacUserIdentityType) {
			if req.Method != "GET" || req.URL.Query().Get("api-version") != "2023-01-31" {
				t.Fatal("identity inventory lost its native collection", req.Method, req.URL)
			}
			values := []any{}
			if kind == rbacUserIdentityType && f.scopes[id] != nil {
				values = append(values, f.scopes[id])
			}
			return jsonResponse(200, map[string]any{"value": values}, nil), true
		}
		for _, collection := range []string{"blobservices/default/containers", "fileservices/default/shares", "queueservices/default/queues", "tableservices/default/tables"} {
			if kind == storageType && req.Method == "GET" && strings.EqualFold(req.URL.Path, id+"/"+collection) {
				return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
			}
		}
		return nil, false
	}
	var target asset.Asset
	if kind == rbacUserIdentityType {
		batch, err := f.runtime.List(t.Context(), productRequest(f.runtime, kind))
		if err != nil || len(batch.Items) != 1 {
			t.Fatal("native user identity inventory failed", len(batch.Items), err)
		}
		item := batch.Items[0]
		target = asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: kind, NativeID: id}, Location: item.Location, Normalized: item.Normalized, Capabilities: item.ResourceKind.Capabilities}
	} else {
		target = dnsAsset(t, f.runtime, raw)
	}
	return f, f.asset(t, rbacAssignmentType, rbacTestAssignmentID()), target, &deleted
}

func TestRBACPrincipalNativeIdentitySources(t *testing.T) {
	const prefix = "https://raw.githubusercontent.com/Azure/azure-rest-api-specs/5da82d5c3687ac3cc845330aaf0f13d3a40ce47e/specification/msi/resource-manager/Microsoft.ManagedIdentity/ManagedIdentity/stable/2023-01-31/"
	const sourceDigest = "e5d776b4b7c62740fa4793bccbde62ab5a5fda40be27de2a2c815b711aa429c9"
	metadata, err := providerData()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("catalog/source/swagger.json")
	var documents catalog.RESTDocumentSet
	if err != nil || json.Unmarshal(raw, &documents) != nil {
		t.Fatal("invalid native identity catalog", err)
	}
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft4)
	compiler.UseLoader(offlineSchemaLoader{})
	digests := map[string]string{}
	for _, doc := range documents.Documents {
		digests[doc.SourceURI] = doc.SourceSHA256
		value, err := jsonschema.UnmarshalJSON(bytes.NewReader(doc.Document))
		if err != nil || compiler.AddResource(doc.SourceURI, applicationInsightsSwaggerNullability(value)) != nil {
			t.Fatal("invalid native identity schema", err)
		}
	}
	for _, entry := range []struct{ file, operation, digest string }{
		{"IdentityGet.json", "UserAssignedIdentities_Get", "16ac6490fa970ee58da23962c0dbdfb356f4b2be0991ab662ec277ae538851b7"},
		{"IdentityListBySubscription.json", "UserAssignedIdentities_ListBySubscription", "619f904f84ea5689aebce122f26e9ede9452c8e99ba7c948898f7030777bf3bb"},
	} {
		t.Run(entry.file, func(t *testing.T) {
			payload, err := os.ReadFile("fixtures/rbac/identities/" + entry.file)
			if err != nil || fmt.Sprintf("%x", sha256.Sum256(payload)) != entry.digest {
				t.Fatal("native identity example changed", prefix+"examples/"+entry.file, err)
			}
			example := rbacExample(t, "identities/"+entry.file)
			op, ok := metadata.catalog.Operation("Azure.Microsoft.ManagedIdentity." + entry.operation)
			if !ok || op.Call == nil || op.Call.Method != "GET" || op.Call.Version != "2023-01-31" || digests[op.SourceURI] != sourceDigest {
				t.Fatal("native identity contract differs from pinned Microsoft document", op.ID)
			}
			if _, err := bindAzureREST(op, object(example["parameters"])); err != nil {
				t.Fatal("native identity request does not bind", err)
			}
			body := object(object(object(example["responses"])["200"])["body"])
			pointer := "#/paths/" + strings.NewReplacer("~", "~0", "/", "~1").Replace(op.Call.Path) + "/get/responses/200/schema"
			schema, err := compiler.Compile(op.SourceURI + pointer)
			if err != nil || schema.Validate(body) != nil {
				t.Fatal("native identity response does not match selected schema", err)
			}
			values, listed := body["value"].([]any)
			if !listed {
				values = []any{body}
			}
			for _, value := range values {
				raw := object(value)
				principal, _, err := rbacNativePrincipal(rbacUserIdentityType, raw)
				props := object(raw["properties"])
				if err != nil || principal != rbacPrincipalSelector(text(props["tenantId"]), text(props["principalId"])) || principal == rbacPrincipalSelector(text(props["tenantId"]), text(props["clientId"])) {
					t.Fatal("native principal was confused with its application ID", err)
				}
			}
		})
	}
}

func TestRBACPrincipalRejectsMalformedNativeIdentity(t *testing.T) {
	for _, mode := range []string{"identity-array", "identity-case", "principal-number", "principal-array", "principal-whitespace", "principal-case", "tenant-missing", "tenant-case", "unknown-type", "type-array", "type-none-combined", "type-duplicate", "missing-type", "uami-no-principal", "uami-client-id-only"} {
		t.Run(mode, func(t *testing.T) {
			kind := storageType
			raw := nativeResource(kind, "principaltest", "westus", map[string]any{})
			identity := map[string]any{"type": "SystemAssigned", "principalId": rbacTestPrincipal, "tenantId": testTenant}
			raw["identity"] = identity
			switch mode {
			case "identity-array":
				raw["identity"] = []any{}
			case "identity-case":
				raw["Identity"] = identity
				delete(raw, "identity")
			case "principal-number":
				identity["principalId"] = 123
			case "principal-array":
				identity["principalId"] = []any{rbacTestPrincipal}
			case "principal-whitespace":
				identity["principalId"] = " " + rbacTestPrincipal
			case "principal-case":
				identity["PrincipalId"] = rbacTestPrincipal
				delete(identity, "principalId")
			case "tenant-missing":
				delete(identity, "tenantId")
			case "tenant-case":
				identity["TenantId"] = testTenant
				delete(identity, "tenantId")
			case "unknown-type":
				identity["type"] = "FutureAssigned"
			case "type-array":
				identity["type"] = []any{"SystemAssigned"}
			case "type-none-combined":
				identity["type"] = "SystemAssigned, None"
			case "type-duplicate":
				identity["type"] = "SystemAssigned, SystemAssigned"
			case "missing-type":
				delete(identity, "type")
			case "uami-no-principal", "uami-client-id-only":
				kind = rbacUserIdentityType
				delete(raw, "identity")
				raw["properties"] = map[string]any{"clientId": rbacTestPrincipal, "tenantId": testTenant}
				if mode == "uami-no-principal" {
					delete(object(raw["properties"]), "clientId")
				}
			}
			if _, _, err := rbacNativePrincipal(kind, raw); err == nil {
				t.Fatal("malformed identity became an empty principal", mode)
			}
		})
	}
}

func TestRBACPrincipalMonitorUsesNativePrerequisiteAndRecovery(t *testing.T) {
	f := newRBACFixture(t)
	m := newMonitorInventoryFixture(t, monitorScheduledRuleType)
	id := slices.Sorted(maps.Keys(m.objects))[0]
	for other := range m.objects {
		if other != id {
			delete(m.objects, other)
		}
	}
	m.objects[id]["identity"] = map[string]any{"type": "SystemAssigned", "principalId": rbacTestPrincipal, "tenantId": testTenant}
	base := m.runtime.transport
	m.runtime.transport = roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if rbacPath(req.URL.Path) {
			return f.runtime.transport.RoundTrip(req)
		}
		return base.RoundTrip(req)
	})
	target := m.asset(t, id)
	assignment := f.asset(t, rbacAssignmentType, rbacTestAssignmentID())
	request := contracts.ActionRequest{Asset: target, Action: "delete", PrerequisiteDeletions: []contracts.ActionImpact{{Asset: assignment, ControllerID: target.ID, Delete: true}}}
	driver, err := m.runtime.ResolveAction(t.Context(), "connection", target)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := driver.Execute(t.Context(), request); err == nil || len(m.deletes) != 0 {
		t.Fatal("monitor system identity ignored its role assignment", err)
	}
	// Leave only the reviewed assignment: a second assignment to this principal
	// must independently block cleanup even if its scope is elsewhere.
	for other, raw := range f.resources {
		if raw["type"] == rbacAssignmentType && other != assignment.Identity.NativeID {
			delete(f.resources, other)
		}
	}
	if _, err := f.action(t, assignment).Execute(t.Context(), contracts.ActionRequest{Asset: assignment, Action: "delete"}); err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), request)
	if err != nil || len(m.deletes) != 1 {
		t.Fatal("monitor did not accept a verified principal prerequisite", err)
	}
	wire, _ := json.Marshal(request)
	if json.Unmarshal(wire, &request) != nil {
		t.Fatal("invalid persisted monitor principal request")
	}
	m.runtime.clients = map[asset.ConnectionID]*client{}
	driver, err = m.runtime.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
		t.Fatal("monitor principal recovery failed", wait, err)
	}
}

func TestRBACPrincipalNativeGraphPlanAndIndependentRecovery(t *testing.T) {
	for _, kind := range []string{rbacUserIdentityType, storageType} {
		t.Run(kind, func(t *testing.T) {
			f, assignment, target, deleted := rbacPrincipalTarget(t, kind)
			assets := []asset.Asset{target, assignment}
			service, _ := f.runtime.ServiceLifecycle(t.Context(), "connection")
			store := batchReferenceGraph{assets: assets}
			built, err := governance.NewService(store, store).RebuildGraph(t.Context(), "scope", "connection", "rbac-principal", f.runtime.bundle, []governance.Contributor{service})
			if err != nil || len(built.Bindings) != 0 {
				t.Fatal("principal graph failed or acquired ownership", err)
			}
			uses, reverse := false, false
			for _, edge := range built.Relationships {
				uses = uses || edge.SourceAssetID == assignment.ID && edge.TargetAssetID == target.ID && edge.Type == graph.RelationshipUses
				if edge.SourceAssetID == target.ID && edge.TargetAssetID == assignment.ID && edge.Type == graph.RelationshipDependsOn {
					reverse = edge.Evidence[graph.RelationshipEvidenceRequiredDeletion] == true && edge.Evidence[graph.RelationshipEvidenceAutomaticSelection] == false
				}
			}
			if !uses || !reverse {
				t.Fatal("principal GUID did not become an independent prerequisite", built.Relationships)
			}
			alone, err := plan.Solve(plan.Input{Assets: assets, Relationships: built.Relationships, ResolvedAssetIDs: []asset.AssetID{target.ID}})
			if err != nil || len(alone.Blockers) == 0 {
				t.Fatal("retained role assignment did not block its identity", alone, err)
			}
			selected, err := plan.Solve(plan.Input{Assets: assets, Relationships: built.Relationships, ResolvedAssetIDs: []asset.AssetID{target.ID, assignment.ID}})
			if err != nil || len(selected.Blockers)+len(selected.ImpactItems) != 0 || len(selected.Steps) != 2 || selected.Steps[0].AssetID != assignment.ID || !slices.Contains(selected.Steps[1].DependsOn, selected.Steps[0].ID) {
				t.Fatal("principal cleanup was not ordered independently", selected, err)
			}
			request := servicePlanRequest(selected, assets, target)
			driver := f.action(t, target)
			if _, err := driver.Execute(t.Context(), request); err == nil || len(*deleted) != 0 {
				t.Fatal("live principal assignment allowed identity deletion", err)
			}
			if _, err := f.action(t, assignment).Execute(t.Context(), servicePlanRequest(selected, assets, assignment)); err != nil || len(*deleted) != 0 || len(f.deleted) != 1 {
				t.Fatal("independent assignment deletion touched its identity", err)
			}
			result, err := driver.Execute(t.Context(), request)
			if err != nil || len(*deleted) != 1 {
				t.Fatal("verified principal assignment absence did not authorize deletion", err)
			}
			wire, _ := json.Marshal(struct {
				Request contracts.ActionRequest
				Result  contracts.ActionResult
			}{request, result})
			var restored struct {
				Request contracts.ActionRequest
				Result  contracts.ActionResult
			}
			if json.Unmarshal(wire, &restored) != nil {
				t.Fatal("invalid durable principal proof")
			}
			f.runtime.clients = map[asset.ConnectionID]*client{}
			driver = f.action(t, restored.Request.Asset)
			if wait, err := driver.Wait(t.Context(), restored.Request, restored.Result); err != nil || !wait.Done {
				t.Fatal("native principal recovery failed", wait, err)
			}
			object(restored.Request.Asset.Normalized[rbacIdentityMetadata])["principal"] = rbacPrincipalSelector(testTenant, rbacTestClientID)
			if wait, err := driver.Wait(t.Context(), restored.Request, restored.Result); err == nil || wait.Done || len(*deleted) != 1 {
				t.Fatal("changed recovered principal proof accepted", wait, err)
			}
		})
	}
}

func TestRBACPrincipalUnindexedLateAndChangedIdentity(t *testing.T) {
	for _, mode := range []string{"unindexed", "target-gone", "late-assignment", "changed-principal", "changed-tenant", "enabled-after-review", "identity-get-403", "identity-get-async", "identity-get-wrong-resource", "missing-proof", "forged-principal", "forged-wire", "known-list-omission"} {
		t.Run(mode, func(t *testing.T) {
			f, assignment, target, deleted := rbacPrincipalTarget(t, storageType)
			c, _ := f.runtime.resolve(t.Context(), "connection")
			raw := f.scopes[target.Identity.NativeID]
			source := f.resources[assignment.Identity.NativeID]
			request := contracts.ActionRequest{Asset: target, Action: "delete"}
			driver := f.action(t, target)
			switch mode {
			case "late-assignment":
				delete(f.resources, assignment.Identity.NativeID)
				result, err := driver.Execute(t.Context(), request)
				if err != nil || len(*deleted) != 1 {
					t.Fatal("empty assignment index did not permit identity cleanup", err)
				}
				f.resources[assignment.Identity.NativeID] = source
				f.runtime.clients = map[asset.ConnectionID]*client{}
				if wait, err := f.action(t, target).Wait(t.Context(), request, result); err == nil || isNotFound(err) || wait.Done {
					t.Fatal("late assignment was erased with its principal", wait, err)
				}
				return
			case "target-gone":
				delete(f.scopes, target.Identity.NativeID)
			case "changed-principal":
				object(raw["identity"])["principalId"] = rbacTestClientID
			case "changed-tenant":
				object(raw["identity"])["tenantId"] = rbacOtherSubscription
			case "enabled-after-review":
				identity := raw["identity"]
				delete(raw, "identity")
				target = dnsAsset(t, f.runtime, raw)
				request.Asset = target
				driver = f.action(t, target)
				raw["identity"] = identity
			case "missing-proof":
				delete(target.Normalized, rbacIdentityProof)
			case "forged-principal":
				object(target.Normalized[rbacIdentityMetadata])["principal"] = ""
			case "forged-wire":
				object(target.Normalized[rbacIdentityMetadata])["wire_id"] = strings.Replace(target.Identity.NativeID, "identitytarget", "another", 1)
			case "known-list-omission":
				request.PrerequisiteDeletions = []contracts.ActionImpact{{Asset: assignment, ControllerID: target.ID, Delete: true}}
			}
			base := f.override
			f.override = func(req *http.Request) (*http.Response, bool) {
				if req.Method == "GET" && strings.EqualFold(req.URL.Path, target.Identity.NativeID) {
					switch mode {
					case "identity-get-403":
						return jsonResponse(403, map[string]any{"error": map[string]any{"code": "AuthorizationFailed"}}, nil), true
					case "identity-get-async":
						return jsonResponse(200, raw, http.Header{"Azure-Asyncoperation": {"https://management.azure.com/operation"}}), true
					case "identity-get-wrong-resource":
						wrong := maps.Clone(raw)
						wrong["id"] = resourceID(storageType, "another")
						return jsonResponse(200, wrong, nil), true
					}
				}
				if mode == "known-list-omission" && req.Method == "GET" && strings.EqualFold(req.URL.Path, c.root()+"/providers/"+rbacAssignmentType) {
					return jsonResponse(200, map[string]any{"value": []any{}}, nil), true
				}
				return base(req)
			}
			if _, err := driver.Execute(t.Context(), request); err == nil || isNotFound(err) || len(*deleted) != 0 {
				t.Fatal("unverified principal dependency allowed deletion", mode, err)
			}
			if mode == "unindexed" || mode == "target-gone" {
				contribution, err := c.contributeMonitorIncoming(t.Context(), []asset.Asset{target}, []asset.Asset{target})
				if err != nil || len(contribution.Unresolved) != 1 || contribution.Unresolved[0].NativeID != assignment.Identity.NativeID || contribution.Unresolved[0].Relationship != graph.RelationshipDependsOn {
					t.Fatal("unindexed principal assignment was not a graph blocker", contribution, err)
				}
			}
		})
	}
}

func TestRBACPrincipalDoesNotConfuseClientSharedOrForeignIdentity(t *testing.T) {
	for _, mode := range []string{"client-id", "shared-user-identity", "user-assigned-root-fields", "foreign-tenant", "user-principal", "group-principal"} {
		t.Run(mode, func(t *testing.T) {
			f, _, target, deleted := rbacPrincipalTarget(t, storageType)
			raw := f.scopes[target.Identity.NativeID]
			props := object(f.resources[rbacTestAssignmentID()]["properties"])
			switch mode {
			case "client-id":
				object(raw["identity"])["principalId"] = rbacTestClientID
				object(raw["identity"])["clientId"] = rbacTestPrincipal
			case "shared-user-identity":
				raw["identity"] = map[string]any{"type": "UserAssigned", "userAssignedIdentities": map[string]any{resourceID(rbacUserIdentityType, "shared"): map[string]any{"principalId": rbacTestPrincipal, "clientId": rbacTestClientID}}}
			case "user-assigned-root-fields":
				object(raw["identity"])["type"] = "UserAssigned"
			case "foreign-tenant":
				object(raw["identity"])["tenantId"] = rbacOtherSubscription
			case "user-principal":
				props["principalType"] = "User"
			case "group-principal":
				props["principalType"] = "Group"
			}
			target = dnsAsset(t, f.runtime, raw)
			assignment := f.asset(t, rbacAssignmentType, rbacTestAssignmentID())
			c, _ := f.runtime.resolve(t.Context(), "connection")
			contribution, err := c.contributeRBACReferences(t.Context(), assignment, []asset.Asset{assignment, target})
			if err != nil {
				t.Fatal(err)
			}
			for _, edge := range contribution.Relationships {
				if edge.TargetAssetID == target.ID || edge.SourceAssetID == target.ID {
					t.Fatal("unrelated principal acquired a target dependency", mode, edge)
				}
			}
			if _, err := f.action(t, target).Execute(t.Context(), contracts.ActionRequest{Asset: target, Action: "delete"}); err != nil || len(*deleted) != 1 || len(f.deleted) != 0 {
				t.Fatal("unrelated/shared identity prevented host cleanup or was deleted", mode, err)
			}
		})
	}
}
