package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func newRBACCaseFixture(t *testing.T, alias bool) (*rbacFixture, string) {
	t.Helper()
	f := newRBACFixture(t)
	wire := "/subscriptions/" + testSubscription + "/resourcegroups/test/providers/microsoft.documentdb/databaseaccounts/account-one/sqldatabases/Sales/containers/Orders/storedprocedures/CreateOrder"
	id := strings.ToLower(wire)
	delete(f.scopes, strings.ToLower(resourceID("Microsoft.KeyVault/vaults", "scope-vault")))
	f.scopes[id] = map[string]any{"id": wire, "name": "CreateOrder", "type": cosmosStoredProcedureType, "properties": map[string]any{"resource": map[string]any{"id": "CreateOrder", "_rid": "native-incarnation", "body": "function () {}"}}}
	for key, raw := range f.resources {
		if raw["type"] == rbacAssignmentType {
			delete(f.resources, key)
		}
	}
	assignment := rbacTestBody(t, rbacAssignmentType, wire, rbacTestAssignmentName)
	assignmentID := strings.ToLower(text(assignment["id"]))
	f.resources[assignmentID] = assignment
	f.override = func(req *http.Request) (*http.Response, bool) {
		path := strings.ToLower(req.URL.Path)
		if path != id && !strings.HasPrefix(path, id+"/") {
			return nil, false
		}
		if cosmosWireSignature(req.URL.Path[:len(wire)]) != wire {
			t.Fatal("RBAC scope names were lowercased", req.Method, req.URL)
		}
		if path == id && req.Method == "GET" {
			if raw := f.scopes[id]; raw != nil {
				copy := maps.Clone(raw)
				if alias {
					copy["id"] = strings.Replace(strings.Replace(wire, "/containers/", "/sqlContainers/", 1), "/storedprocedures/", "/sqlStoredProcedures/", 1)
				}
				return jsonResponse(200, copy, nil), true
			}
			return jsonResponse(404, map[string]any{"error": map[string]any{"code": "ResourceNotFound"}}, nil), true
		}
		return nil, false
	}
	return f, assignmentID
}

func TestRBACNativeScopeSelectorsAndOrphanRecovery(t *testing.T) {
	for _, mode := range []string{"native", "source-alias", "orphan", "lowercase-forgery"} {
		t.Run(mode, func(t *testing.T) {
			f, id := newRBACCaseFixture(t, mode == "source-alias")
			if mode == "orphan" {
				clear(f.scopes)
			}
			value := f.asset(t, rbacAssignmentType, id)
			if !strings.Contains(text(value.Normalized[rbacWireSelector]), "/Sales/containers/Orders/storedprocedures/CreateOrder/") {
				t.Fatal("RBAC inventory lost native scope names")
			}
			driver := f.action(t, value)
			if mode == "lowercase-forgery" {
				value.Normalized[rbacWireSelector] = value.Identity.NativeID
				f.override = func(req *http.Request) (*http.Response, bool) {
					t.Fatal("forged RBAC scope reached transport", req.URL)
					return nil, false
				}
				if _, err := driver.Execute(t.Context(), contracts.ActionRequest{Asset: value, Action: "delete"}); err == nil {
					t.Fatal("lowercased RBAC scope authorized mutation")
				}
				return
			}
			request := contracts.ActionRequest{Asset: value, Action: "delete"}
			f.hold = true
			result, err := driver.Execute(t.Context(), request)
			if err != nil {
				t.Fatal("native RBAC scope deletion failed", err)
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
				t.Fatal("invalid native RBAC persisted state")
			}
			f.runtime.clients = map[asset.ConnectionID]*client{}
			driver = f.action(t, restored.Request.Asset)
			if wait, err := driver.Wait(t.Context(), restored.Request, restored.Result); err != nil || wait.Done {
				t.Fatal("native RBAC scope acknowledgement proved absence", wait, err)
			}
			delete(f.resources, id)
			if wait, err := driver.Wait(t.Context(), restored.Request, restored.Result); err != nil || !wait.Done {
				t.Fatal("native RBAC scope absence failed after recovery", wait, err)
			}
		})
	}
}

func TestRBACPrivatePayloadsStayOutOfInvocationsAndLogs(t *testing.T) {
	for _, kind := range []string{rbacRoleType, rbacAssignmentType, rbacEligibilityType, rbacScheduleType} {
		t.Run(kind, func(t *testing.T) {
			entries := []execution.JobLogEntry{}
			ctx := execution.WithJobLogSink(t.Context(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { entries = append(entries, entry) }))
			raw := rbacTestBody(t, kind, "/subscriptions/"+testSubscription, rbacTestAssignmentName)
			private := map[string]any{"condition": "PRIVATE_RBAC_CONDITION", "permissions": []any{map[string]any{"actions": []any{"PRIVATE_RBAC_ACTION"}}}, "futureAuthoredField": "PRIVATE_RBAC_FUTURE", "description": "PRIVATE_RBAC_DESCRIPTION"}
			for key, value := range private {
				object(raw["properties"])[key] = value
			}
			before, _ := json.Marshal(raw)
			r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
				if req.Method != "GET" || !strings.EqualFold(req.URL.Path, "/subscriptions/"+testSubscription+"/providers/"+kind) || req.URL.Query().Get("api-version") != rbacVersion(kind) {
					t.Fatal("RBAC invocation changed native collection", req.URL)
				}
				return jsonResponse(200, map[string]any{"value": []any{raw}}, nil), nil
			})
			c, _ := r.resolve(ctx, "connection")
			op, params, err := c.rbacOperation(kind, c.root(), "", "GET")
			if err != nil {
				t.Fatal(err)
			}
			result, err := r.Invoke(ctx, contracts.Invocation{ConnectionID: "connection", Operation: op.ID, Parameters: params})
			if err != nil || len(array(result.Data["value"])) != 1 {
				t.Fatal("native RBAC invocation failed", result, err)
			}
			for _, value := range []any{entries, result, safePayload(raw), safePayload(map[string]any{"type": strings.ToUpper(kind), "properties": private}), safePayload(map[string]any{"id": text(raw["id"]), "properties": private}), safeAPIPayload(map[string]any{"body": private}, apiURL(text(raw["id"]), rbacVersion(kind)))} {
				encoded, _ := json.Marshal(value)
				if strings.Contains(string(encoded), "PRIVATE_RBAC_") {
					t.Fatal("private RBAC payload escaped", string(encoded))
				}
			}
			after, _ := json.Marshal(raw)
			if string(before) != string(after) {
				t.Fatal("RBAC redaction changed the private comparison source")
			}
		})
	}
}
