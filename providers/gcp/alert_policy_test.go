package gcp

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/inventory"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const alertPolicyName = "projects/sample-project/alertPolicies/1234"
const alertPolicyID = "//monitoring.googleapis.com/" + alertPolicyName

func alertPolicyFixture() map[string]any {
	return map[string]any{"name": alertPolicyName, "displayName": "Uptime failure", "enabled": true, "combiner": "OR", "severity": "ERROR", "userLabels": map[string]any{"environment": "test"}, "documentation": map[string]any{"content": "PRIVATE_ALERT_DOCUMENT", "mimeType": "text/markdown", "links": []any{map[string]any{"displayName": "runbook", "url": "https://example.com?token=PRIVATE_ALERT_URL"}}}, "conditions": []any{map[string]any{"name": alertPolicyName + "/conditions/5678", "displayName": "Check failed", "conditionThreshold": map[string]any{"filter": `metric.type="monitoring.googleapis.com/uptime_check/check_passed" AND metric.label.check_id="PRIVATE_ALERT_CHECK"`, "comparison": "COMPARISON_GT", "duration": "60s", "thresholdValue": 1}}}, "notificationChannels": []any{"projects/sample-project/notificationChannels/9876"}, "creationRecord": map[string]any{"mutateTime": "2026-09-01T00:00:00Z", "mutatedBy": "tester@example.com"}, "mutationRecord": map[string]any{"mutateTime": "2026-09-02T00:00:00Z", "mutatedBy": "tester@example.com"}, "futureNativeField": map[string]any{"value": "bound"}}
}
func TestAlertPolicyNativeInventoryAndFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"normal", "list-empty", "list-paged", "list-denied", "list-null", "list-token", "list-partial", "list-duplicate", "get-denied", "get-partial", "gone", "detail-drift"} {
		t.Run(mode, func(t *testing.T) {
			r, request, _, state, _ := monitoringScenario(t, alertPolicyType)
			*state = mode
			query := productRequest(r, alertPolicyType, "global")
			batch, err := r.List(t.Context(), query)
			if mode == "list-paged" {
				if err != nil || batch.Complete || batch.NextCursor == "" || len(batch.Items) != 0 {
					t.Fatal(batch, err)
				}
				query.Cursor = batch.NextCursor
				batch, err = r.List(t.Context(), query)
			}
			success := mode == "normal" || mode == "list-paged" || mode == "list-empty"
			if !success {
				if err == nil || len(batch.Items) != 0 {
					t.Fatal(batch, err)
				}
				return
			}
			want := 1
			if mode == "list-empty" {
				want = 0
			}
			if err != nil || !batch.Complete || len(batch.Items) != want {
				t.Fatal(batch, err)
			}
			if want == 0 {
				return
			}
			item := batch.Items[0]
			b, _ := json.Marshal(item)
			if strings.Contains(string(b), "PRIVATE_ALERT") || item.Normalized[alertPolicyReview] == "" || item.Tags["environment"] != "test" {
				t.Fatal(string(b))
			}
			if len(item.NetworkReferences) != 0 || len(inventory.FilterNetworkClosure(asset.ScanTarget{Kind: asset.ScanTargetVPC, NativeID: "PRIVATE_ALERT_CHECK"}, []contracts.InventoryItem{item})) != 0 {
				t.Fatal("unparsed condition became a network dependency")
			}
			request.Asset.Normalized = item.Normalized
			assertGCPPropertyQuery(t, r, []asset.Asset{request.Asset}, alertPolicyType, "1234", `properties.enabled = true AND properties.severity = "ERROR"`)
			result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: "monitoring.projects.alertPolicies.get", Parameters: map[string]any{"name": alertPolicyName}})
			b, _ = json.Marshal(result)
			if err != nil || strings.Contains(string(b), "PRIVATE_ALERT") {
				t.Fatal(string(b), err)
			}
		})
	}
}
func TestAlertPolicyDeleteReviewRestartAndSettlement(t *testing.T) {
	for _, mode := range []string{"normal", "gone", "get-denied", "delete-denied", "delete-missing", "delete-error", "delete-operation", "race", "changed-query", "changed-document", "changed-mutation", "changed-unknown", "protected", "missing-review", "wrong-identity"} {
		t.Run(mode, func(t *testing.T) {
			r, request, data, state, deletes := monitoringScenario(t, alertPolicyType)
			*state = mode
			switch mode {
			case "changed-query":
				object(object(array((*data)["conditions"])[0])["conditionThreshold"])["filter"] = "changed"
			case "changed-document":
				object((*data)["documentation"])["content"] = "changed"
			case "changed-mutation":
				object((*data)["mutationRecord"])["mutateTime"] = "2026-09-03T00:00:00Z"
			case "changed-unknown":
				(*data)["futureNativeField"] = false
			case "protected":
				(*data)["userLabels"] = map[string]any{"steward_protected": "true"}
				request.Asset.Normalized[alertPolicyReview] = monitoringConfiguration(alertPolicyType, alertPolicyID, *data)
			case "missing-review":
				delete(request.Asset.Normalized, alertPolicyReview)
			}
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if mode == "wrong-identity" {
				request.Asset.Identity.ConnectionID = "foreign"
			}
			result, err := driver.Execute(t.Context(), *request)
			if mode != "normal" && mode != "gone" {
				if err == nil {
					t.Fatal("unreviewed deletion accepted", result)
				}
				if !strings.HasPrefix(mode, "delete-") && *deletes != 0 {
					t.Fatal("write before verified review")
				}
				return
			}
			if err != nil || result.ProviderOperationID != "" {
				t.Fatal(result, err)
			}
			if mode == "gone" {
				if *deletes != 0 {
					t.Fatal("absent policy deleted")
				}
				return
			}
			if result.Data["phase"] != "alert_policy_delete" || *deletes != 1 {
				t.Fatal(result, *deletes)
			}
			bytes, _ := json.Marshal(result)
			var restored contracts.ActionResult
			if err := json.Unmarshal(bytes, &restored); err != nil {
				t.Fatal(err)
			}
			driver, err = r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			wait, err := driver.Wait(t.Context(), *request, restored)
			if err != nil || wait.Done {
				t.Fatal("live policy reported gone", wait, err)
			}
			*state = "gone"
			settled, err := driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), *request, restored)
			if err != nil || !settled.Settled || !strings.HasPrefix(settled.Operation, "monitoring-synchronous-response:") {
				t.Fatal(settled, err)
			}
			wait, err = driver.Wait(t.Context(), *request, restored)
			if err != nil || !wait.Done || *deletes != 1 {
				t.Fatal(wait, err, *deletes)
			}
			for _, changed := range []string{"key", "phase", "operation", "review"} {
				copy := restored
				copy.Data = cloneParameters(restored.Data)
				q := *request
				switch changed {
				case "key":
					q.IdempotencyKey = "other"
				case "phase":
					copy.Data["phase"] = "uptime_delete"
				case "operation":
					copy.ProviderOperationID = "operations/fake"
				case "review":
					copy.Data["review"] = "wrong"
				}
				if _, err := driver.Wait(t.Context(), q, copy); err == nil {
					t.Fatal("tampered receipt accepted", changed)
				}
			}
			settled, err = driver.(contracts.MutationSettlementReader).MutationSettled(t.Context(), *request, contracts.ActionResult{})
			if err != nil || settled.Settled {
				t.Fatal("lost response released project mutation scope", settled, err)
			}
		})
	}
}
func TestAlertPolicyConditionShapesAndIdentity(t *testing.T) {
	c := &client{project: "sample-project", number: "123456"}
	for _, mode := range []string{"threshold", "absent", "log", "mql", "promql", "sql", "alias", "disabled", "invalid-diagnostic", "enabled-missing", "condition-missing", "condition-null", "condition-duplicate", "condition-foreign", "condition-union", "condition-unknown", "query-type", "channels-null", "channels-foreign-host", "labels-type", "foreign-policy", "object-null"} {
		t.Run(mode, func(t *testing.T) {
			data := alertPolicyFixture()
			condition := object(array(data["conditions"])[0])
			valid := false
			switch mode {
			case "threshold":
				valid = true
			case "absent", "log", "mql", "promql", "sql":
				delete(condition, "conditionThreshold")
				key := map[string]string{"absent": "conditionAbsent", "log": "conditionMatchedLog", "mql": "conditionMonitoringQueryLanguage", "promql": "conditionPrometheusQueryLanguage", "sql": "conditionSql"}[mode]
				body := map[string]any{"filter": "PRIVATE_ALERT_FILTER"}
				if mode == "mql" || mode == "promql" || mode == "sql" {
					body = map[string]any{"query": "PRIVATE_ALERT_QUERY"}
				}
				condition[key] = body
				valid = true
			case "alias":
				data["name"] = strings.Replace(alertPolicyName, "sample-project", "123456", 1)
				condition["name"] = strings.Replace(text(condition["name"]), "sample-project", "123456", 1)
				valid = true
			case "disabled":
				data["enabled"] = false
				valid = true
			case "invalid-diagnostic":
				data["validity"] = map[string]any{"code": 3, "message": "PRIVATE_ALERT_QUERY_DIAGNOSTIC", "details": []any{map[string]any{"query": "PRIVATE_ALERT_DETAIL"}}}
				valid = true
			case "enabled-missing":
				delete(data, "enabled")
			case "condition-missing":
				delete(data, "conditions")
			case "condition-null":
				data["conditions"] = nil
			case "condition-duplicate":
				data["conditions"] = []any{condition, condition}
			case "condition-foreign":
				condition["name"] = "projects/other/alertPolicies/1234/conditions/5678"
			case "condition-union":
				condition["conditionAbsent"] = map[string]any{"filter": "other"}
			case "condition-unknown":
				delete(condition, "conditionThreshold")
				condition["conditionFuture"] = map[string]any{}
			case "query-type":
				object(condition["conditionThreshold"])["filter"] = false
			case "channels-null":
				data["notificationChannels"] = nil
			case "channels-foreign-host":
				data["notificationChannels"] = []any{"https://evil.example/projects/p/notificationChannels/x"}
			case "labels-type":
				data["userLabels"] = map[string]any{"environment": 1}
			case "foreign-policy":
				data["name"] = "projects/other/alertPolicies/1234"
			case "object-null":
				data["documentation"] = nil
			}
			err := c.alertPolicyData(alertPolicyID, data)
			if (err == nil) != valid {
				t.Fatal(valid, err)
			}
			if valid {
				before := firewallDigest(data)
				proof := monitoringConfiguration(alertPolicyType, alertPolicyID, data)
				if before != firewallDigest(data) {
					t.Fatal("hash mutated native data")
				}
				if (mode == "alias" || mode == "invalid-diagnostic") && proof != monitoringConfiguration(alertPolicyType, alertPolicyID, alertPolicyFixture()) {
					t.Fatal("native alias/diagnostic changed configuration proof")
				}
				encoded, _ := json.Marshal(safePayload(data))
				if strings.Contains(string(encoded), "PRIVATE_ALERT") {
					t.Fatal("private policy payload escaped", string(encoded))
				}
			}
		})
	}
}

func TestAlertPolicyRegionAndProjectBinding(t *testing.T) {
	r, request, _, _, _ := monitoringScenario(t, alertPolicyType)
	if _, err := r.ResolveAction(t.Context(), "other", request.Asset); err == nil {
		t.Fatal("connection mismatch accepted")
	}
	for _, scope := range []string{"us-central1", "europe-west1"} {
		batch, err := r.List(t.Context(), productRequest(r, alertPolicyType, scope))
		if err != nil || len(batch.Items) != 0 {
			t.Fatal(batch, err)
		}
	}
}

func TestAlertPolicyPinnedNativeSchema(t *testing.T) {
	raw, err := os.ReadFile("catalog/source/discovery.json")
	if err != nil {
		t.Fatal(err)
	}
	var source map[string]any
	if json.Unmarshal(raw, &source) != nil {
		t.Fatal("invalid source")
	}
	var schemas map[string]any
	for _, raw := range array(source["documents"]) {
		candidate := object(object(object(raw)["document"])["schemas"])
		if candidate["AlertPolicy"] != nil {
			schemas = candidate
			break
		}
	}
	if schemas == nil {
		t.Fatal("missing AlertPolicy native schema")
	}
	compiler := discoveryFixtureSchemaCompiler(t, schemas)
	schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/AlertPolicy")
	if err != nil {
		t.Fatal(err)
	}
	data := alertPolicyFixture()
	delete(data, "futureNativeField")
	if err := schema.Validate(roundTripDataformJSON(t, data)); err != nil {
		t.Fatal(err)
	}
}
