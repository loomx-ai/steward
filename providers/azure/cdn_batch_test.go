package azure

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/core/plan"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// Bind the independent rule example into the documented BatchRuleProperties
// shape. These reference/failure scenarios are synthetic; the unchanged CLI
// batch-mode responses are exercised in TestCDNRecordedNativeDeletesAndSignedOperationRecovery.
func cdnBatchScenario(t *testing.T) (*dnsScenario, *Runtime, []asset.Asset) {
	t.Helper()
	s, r, assets := cdnScenario(t)
	rule := cdnAsset(t, assets, afdRuleType)
	set := cdnAsset(t, assets, afdRuleSetType)
	payload, _ := json.Marshal(s.records[rule.Identity.NativeID]["properties"])
	var batchRule map[string]any
	json.Unmarshal(payload, &batchRule)
	batchRule["ruleName"], batchRule["ruleSetName"] = last(rule.Identity.NativeID), last(set.Identity.NativeID)
	batchRule["conditions"] = []any{map[string]any{"name": "RequestHeader", "parameters": map[string]any{"typeName": "DeliveryRuleRequestHeaderConditionParameters", "selector": "X-Innocent", "operator": "Equal", "matchValues": []any{"secret-batch-condition"}}}}
	properties := object(s.records[set.Identity.NativeID]["properties"])
	properties["batchMode"], properties["rules"] = true, []any{batchRule}
	delete(s.records, rule.Identity.NativeID)
	delete(s.lists, set.Identity.NativeID+"/rules")
	assets = slices.DeleteFunc(assets, func(value asset.Asset) bool { return value.Identity.NativeType == afdRuleType })
	for i := range assets {
		if assets[i].ID == set.ID {
			assets[i] = dnsAsset(t, r, s.records[set.Identity.NativeID])
		}
	}
	return s, r, assets
}

func TestCDNBatchRulesUseNativeRuleSetDetailsAndAtomicLifecycle(t *testing.T) {
	for _, kind := range []string{afdOriginGroupType, afdRuleSetType, cdnProfileType} {
		t.Run(kind, func(t *testing.T) {
			s, r, assets := cdnBatchScenario(t)
			set := cdnAsset(t, assets, afdRuleSetType)
			group := cdnAsset(t, assets, afdOriginGroupType)
			if !slices.Contains(cdnDependencies(set), group.Identity.NativeID) || set.Normalized["batchMode"] != true || len(array(set.Normalized["rules"])) != 1 {
				t.Fatal("batch rule-set configuration/reference missing")
			}
			// Native LIST summaries omit rules, while detail GET includes them.
			s.lists[cdnProfileID(set.Identity.NativeID)+"/rulesets"] = []any{map[string]any{"id": set.Identity.NativeID, "properties": map[string]any{"batchMode": true}}}
			for _, scanKind := range []string{afdRuleSetType, afdRuleType} {
				batch, err := r.List(context.Background(), productRequest(r, scanKind))
				want := 1
				if scanKind == afdRuleType {
					want = 0
				}
				if err != nil || !batch.Complete || len(batch.Items) != want {
					t.Fatalf("batch inventory %s: %+v %v", scanKind, batch, err)
				}
				payload, _ := json.Marshal(batch)
				if strings.Contains(string(payload), "secret-batch-condition") || strings.Contains(string(payload), "secret-rule-value") {
					t.Fatal("embedded batch secrets persisted")
				}
			}
			target := cdnAsset(t, assets, kind)
			request, input := dnsRequest(t, r, assets, target)
			solved, err := plan.Solve(input)
			if err != nil {
				t.Fatal(err)
			}
			if kind == cdnProfileType {
				if len(solved.Steps) != 1 || len(request.LifecycleImpacts) != 8 {
					t.Fatalf("batch profile cascade invented an independent rule: %+v", solved)
				}
			} else {
				if len(request.PrerequisiteDeletions) == 0 {
					t.Fatal("batch dependencies absent from review")
				}
				driver, _ := r.ResolveAction(context.Background(), "connection", target)
				if _, err := driver.Execute(context.Background(), request); err == nil || len(s.deletes) != 0 {
					t.Fatal("batch reference did not prevent premature deletion")
				}
			}
			input.RequestOptions = map[asset.AssetID]map[string]any{target.ID: {"retain_resources": []string{set.Identity.NativeID}}}
			if kind == afdRuleSetType {
				input.RequestOptions[target.ID]["retain_resources"] = []string{cdnAsset(t, assets, afdRouteType).Identity.NativeID}
			}
			retained, err := plan.Solve(input)
			if err != nil || len(retained.Blockers) == 0 {
				t.Fatal("retained batch prerequisite/child was deleted", err)
			}
			for _, step := range solved.Steps {
				selected := assets[slices.IndexFunc(assets, func(value asset.Asset) bool { return value.ID == step.AssetID })]
				stepRequest := servicePlanRequest(solved, assets, selected)
				driver, _ := r.ResolveAction(context.Background(), "connection", selected)
				result, err := driver.Execute(context.Background(), stepRequest)
				if err != nil {
					t.Fatalf("batch prerequisite %s: %v", selected.Identity.NativeType, err)
				}
				payload, _ := json.Marshal(stepRequest)
				json.Unmarshal(payload, &stepRequest)
				payload, _ = json.Marshal(result)
				json.Unmarshal(payload, &result)
				driver, _ = r.ResolveAction(context.Background(), "connection", stepRequest.Asset)
				if len(stepRequest.LifecycleImpacts) != 0 {
					wait, err := driver.Wait(context.Background(), stepRequest, result)
					if err != nil || wait.Done {
						t.Fatal("batch cascade closed before native children disappeared", err)
					}
				}
				for _, impact := range stepRequest.LifecycleImpacts {
					s.gone[impact.Asset.Identity.NativeID] = true
				}
				wait, err := driver.Wait(context.Background(), stepRequest, result)
				if err != nil || !wait.Done {
					t.Fatal("batch lifecycle readback", err)
				}
			}
			if len(s.deletes) != len(solved.Steps) || s.deletes[len(s.deletes)-1] != target.Identity.NativeID {
				t.Fatal("unreviewed batch writes", s.deletes)
			}
		})
	}
}

func TestCDNBatchMalformedOrChangedConfigurationPreventsDeletion(t *testing.T) {
	for _, mode := range []string{"mode-type", "mode-null", "missing-array", "null-array", "object-array", "invalid-member", "empty-name", "bad-name", "duplicate-name", "foreign-parent", "invalid-actions", "invalid-condition", "cross-profile-reference", "changed-name", "changed-order", "changed-action", "changed-condition", "changed-mode", "new-rule", "denied-detail", "missing-detail", "wrong-detail", "runtime"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cdnBatchScenario(t)
			root := cdnAsset(t, assets, cdnProfileType)
			request, _ := dnsRequest(t, r, assets, root)
			set := cdnAsset(t, assets, afdRuleSetType)
			raw := s.records[set.Identity.NativeID]
			s.lists[cdnProfileID(set.Identity.NativeID)+"/rulesets"] = []any{map[string]any{"id": set.Identity.NativeID, "properties": map[string]any{"batchMode": true}}}
			props := object(raw["properties"])
			rule := object(array(props["rules"])[0])
			switch mode {
			case "mode-type":
				props["batchMode"] = "true"
			case "mode-null":
				props["batchMode"] = nil
			case "missing-array":
				delete(props, "rules")
			case "null-array":
				props["rules"] = nil
			case "object-array":
				props["rules"] = map[string]any{}
			case "invalid-member":
				props["rules"] = []any{nil}
			case "empty-name":
				delete(rule, "ruleName")
			case "bad-name":
				rule["ruleName"] = "bad/name"
			case "duplicate-name":
				props["rules"] = append(array(props["rules"]), map[string]any{"ruleName": strings.ToUpper(text(rule["ruleName"]))})
			case "foreign-parent":
				rule["ruleSetName"] = "foreign"
			case "invalid-actions":
				rule["actions"] = "not-an-array"
			case "invalid-condition":
				rule["conditions"] = []any{map[string]any{"name": "RequestHeader", "parameters": false}}
			case "cross-profile-reference":
				object(object(object(array(rule["actions"])[0])["parameters"])["originGroupOverride"])["originGroup"] = map[string]any{"id": strings.Replace(cdnAsset(t, assets, afdOriginGroupType).Identity.NativeID, "/profiles/afd/", "/profiles/foreign/", 1)}
			case "changed-name":
				rule["ruleName"] = "different"
			case "changed-order":
				rule["order"] = 5
			case "changed-action":
				object(object(array(rule["actions"])[1])["parameters"])["value"] = "changed-private-action"
			case "changed-condition":
				object(object(array(rule["conditions"])[0])["parameters"])["matchValues"] = []any{"changed-private-condition"}
			case "changed-mode":
				props["batchMode"] = false
			case "new-rule":
				props["rules"] = append(array(props["rules"]), map[string]any{"ruleName": "newRule", "order": 2})
			case "denied-detail":
				s.status[set.Identity.NativeID] = 403
			case "missing-detail":
				s.status[set.Identity.NativeID] = 404
			case "wrong-detail":
				raw["id"] = set.Identity.NativeID + "foreign"
			case "runtime":
				props["deploymentStatus"], rule["deploymentStatus"] = "Succeeded", "Succeeded"
				props["provisioningState"], rule["provisioningState"] = "Updating", nil
			}
			if slices.Contains([]string{"mode-type", "mode-null", "missing-array", "null-array", "object-array", "invalid-member", "empty-name", "bad-name", "duplicate-name", "foreign-parent", "invalid-actions", "invalid-condition", "cross-profile-reference", "denied-detail", "wrong-detail"}, mode) {
				if _, err := r.List(context.Background(), productRequest(r, afdRuleSetType)); err == nil {
					t.Fatal("incomplete or malformed batch detail accepted as inventory")
				}
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", root)
			_, err := driver.Execute(context.Background(), request)
			if mode == "runtime" {
				if err != nil || len(s.deletes) != 1 {
					t.Fatal("runtime state changed the batch configuration", err)
				}
			} else if err == nil || len(s.deletes) != 0 {
				t.Fatal("invalid/changed batch configuration authorized deletion", mode, err)
			}
		})
	}
}

func TestCDNBatchReferenceChangesAndUnresolvedSetsBlockPlanning(t *testing.T) {
	for _, mode := range []string{"unresolved", "changed-private", "changed-reference"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cdnBatchScenario(t)
			set := cdnAsset(t, assets, afdRuleSetType)
			group := cdnAsset(t, assets, afdOriginGroupType)
			if mode == "unresolved" {
				assets = slices.DeleteFunc(assets, func(value asset.Asset) bool { return value.ID == set.ID })
				contributor, _ := r.ServiceLifecycle(context.Background(), "connection")
				contribution, err := contributor.Contribute(context.Background(), "scope", assets)
				if err != nil || len(contribution.Unresolved) == 0 {
					t.Fatal("unobserved batch prerequisite was ignored", err)
				}
				return
			}
			reads := 0
			s.handle = func(req *http.Request) (*http.Response, bool) {
				if strings.EqualFold(req.URL.Path, set.Identity.NativeID) {
					reads++
					if reads == 2 {
						rule := object(array(object(s.records[set.Identity.NativeID]["properties"])["rules"])[0])
						if mode == "changed-private" {
							object(object(array(rule["actions"])[1])["parameters"])["value"] = "changed-during-review"
						} else {
							object(object(object(array(rule["actions"])[0])["parameters"])["originGroupOverride"])["originGroup"] = map[string]any{"id": group.Identity.NativeID + "different"}
						}
					}
				}
				return nil, false
			}
			c, _ := r.resolve(context.Background(), "connection")
			profile, err := c.cdnProfile(context.Background(), group.Identity.NativeID)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := c.cdnIncoming(context.Background(), group.Identity, profile); err == nil || len(s.deletes) != 0 || reads != 2 {
				t.Fatal("batch referring configuration changed without invalidating review", err, reads)
			}
		})
	}
}

func TestCDNClassicRuleRejectsRecreatedBatchParent(t *testing.T) {
	for _, mode := range []string{"classic", "batch", "parent-creation", "missing-binding", "denied-parent", "missing-parent", "foreign-parent"} {
		t.Run(mode, func(t *testing.T) {
			s, r, assets := cdnScenario(t)
			rule := cdnAsset(t, assets, afdRuleType)
			set := cdnAsset(t, assets, afdRuleSetType)
			raw := s.records[set.Identity.NativeID]
			switch mode {
			case "batch":
				object(raw["properties"])["batchMode"] = true
				object(raw["properties"])["rules"] = []any{map[string]any{"ruleName": "rule1"}}
			case "parent-creation":
				raw["systemData"] = map[string]any{"createdAt": "2026-09-10T00:00:00Z"}
			case "missing-binding":
				delete(rule.Normalized, "_cdn_rule_set_configuration")
			case "denied-parent":
				s.status[set.Identity.NativeID] = 403
			case "missing-parent":
				s.status[set.Identity.NativeID] = 404
			case "foreign-parent":
				raw["id"] = set.Identity.NativeID + "foreign"
			}
			driver, _ := r.ResolveAction(context.Background(), "connection", rule)
			_, err := driver.Execute(context.Background(), contracts.ActionRequest{Asset: rule, Action: "delete"})
			if mode == "classic" {
				if err != nil || len(s.deletes) != 1 {
					t.Fatal("classic individual rule deletion changed", err)
				}
			} else if err == nil || len(s.deletes) != 0 {
				t.Fatal("stale independent rule deletion authorized", mode, err)
			}
		})
	}
}

func TestCDNBatchEmptyRulesAndSanitizedDiagnostics(t *testing.T) {
	s, r, assets := cdnBatchScenario(t)
	set := cdnAsset(t, assets, afdRuleSetType)
	var logs []execution.JobLogEntry
	ctx := execution.WithJobLogSink(context.Background(), execution.JobLogSinkFunc(func(_ context.Context, entry execution.JobLogEntry) { logs = append(logs, entry) }))
	c, _ := r.resolve(ctx, "connection")
	kind, _ := findType(afdRuleSetType)
	endpoint, _ := c.resourceURL(kind, set.Identity.NativeID)
	if _, err := c.request(ctx, "GET", endpoint); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(logs)
	if len(logs) != 2 || strings.Contains(string(payload), "secret-batch-condition") || strings.Contains(string(payload), "secret-rule-value") {
		t.Fatal("batch secrets escaped API logs")
	}
	object(s.records[set.Identity.NativeID]["properties"])["rules"] = []any{}
	batch, err := r.List(context.Background(), productRequest(r, afdRuleSetType))
	if err != nil || len(batch.Items) != 1 || len(array(batch.Items[0].Normalized["rules"])) != 0 {
		t.Fatal("native empty batch rules not discovered", err)
	}
	if mode, err := cdnBatchMode(s.records[set.Identity.NativeID]); !mode || err != nil {
		t.Fatal("empty batch rule-set rejected", err)
	}
	for _, name := range []string{"a", strings.Repeat("a", 60), "A1"} {
		object(s.records[set.Identity.NativeID]["properties"])["rules"] = []any{map[string]any{"ruleName": name}}
		if mode, err := cdnBatchMode(s.records[set.Identity.NativeID]); !mode || err != nil {
			t.Fatal("native rule-name boundary", err)
		}
	}
	for _, name := range []string{"1start", strings.Repeat("a", 61), "a-b", "a_b"} {
		object(s.records[set.Identity.NativeID]["properties"])["rules"] = []any{map[string]any{"ruleName": name}}
		if _, err := cdnBatchMode(s.records[set.Identity.NativeID]); err == nil {
			t.Fatalf("invalid native rule-name %q", name)
		}
	}
}
