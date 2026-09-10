package azure

import (
	"encoding/json"
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/loomx-ai/steward/internal/app/governance"
	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/core/plan"
)

func TestMonitorNativeResourceReferences(t *testing.T) {
	for _, file := range monitorRuleGetFiles() {
		t.Run(file, func(t *testing.T) {
			raw := monitorRuleComposedIdentity(t, monitorRuleExample(t, file), file)
			id, _, kind, err := monitorResourceID(text(raw["id"]))
			if err != nil {
				t.Fatal(err)
			}
			refs, err := monitorResourceReferences(kind, id, raw)
			if err != nil {
				t.Fatal("native reference contract failed", err)
			}
			if kind == insightsWebTestType && len(refs[applicationInsightsType]) != 1 {
				t.Fatal("native hidden component link omitted", refs)
			}
			if strings.HasSuffix(file, "getWebTestMetricAlert.json") && (len(refs[applicationInsightsType]) != 1 || len(refs[insightsWebTestType]) != 1) {
				t.Fatal("native web-test criteria omitted", refs)
			}
			if kind == monitorActionGroupType && len(refs) != 0 {
				t.Fatal("opaque notification destinations became dependencies", refs)
			}
			for _, ids := range refs {
				if len(ids) != len(slices.Compact(slices.Sorted(slices.Values(ids)))) {
					t.Fatal("duplicate monitor resource link")
				}
			}
		})
	}
}

func TestMonitorBudgetNativeGraphAndSharedPlan(t *testing.T) {
	for _, kind := range []string{monitorConsumptionBudgetType, monitorCostBudgetType} {
		t.Run(kind, func(t *testing.T) {
			f := newMonitorInventoryFixture(t, kind)
			targetID := "/subscriptions/" + testSubscription + "/resourcegroups/shared/providers/microsoft.insights/actiongroups/notification"
			firstID := slices.Sorted(maps.Keys(f.objects))[0]
			for id := range f.objects {
				if id != firstID {
					delete(f.objects, id)
				}
			}
			destination := monitorRuleExample(t, "actions-2023-01-01/getActionGroup.json")
			destination["id"], destination["name"] = targetID, last(targetID)
			f.addRelated(t, destination)
			for _, raw := range f.objects {
				for _, value := range object(object(raw["properties"])["notifications"]) {
					object(value)["contactGroups"] = []any{targetID}
				}
			}
			parent, target := f.asset(t, firstID), f.asset(t, targetID)
			values := []asset.Asset{parent, target}
			contributor, err := f.runtime.ServiceLifecycle(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			store := batchReferenceGraph{assets: values}
			built, err := governance.NewService(store, store).RebuildGraph(t.Context(), "scope", "connection", "monitor-native", f.runtime.bundle, []governance.Contributor{contributor})
			if err != nil || len(built.Bindings) != 0 || len(built.Unresolved) != 0 || len(built.Relationships) != 2 {
				t.Fatal("native budget acquired ownership or lost shared usage", built, err)
			}
			var ref graph.Relationship
			for _, relationship := range built.Relationships {
				if relationship.Type == graph.RelationshipUses {
					ref = relationship
				}
			}
			if ref.SourceAssetID != parent.ID || ref.TargetAssetID != target.ID || ref.Type != graph.RelationshipUses || !slices.Contains(stringValues(ref.Evidence["evidence_sources"]), "azure:monitor-reference") {
				t.Fatal("budget dependency direction changed", ref)
			}
			for _, selected := range [][]asset.AssetID{{parent.ID}, {target.ID}, {parent.ID, target.ID}} {
				planned, err := plan.Solve(plan.Input{Assets: values, Relationships: built.Relationships, ResolvedAssetIDs: selected})
				if err != nil {
					t.Fatal(err)
				}
				switch {
				case len(selected) == 2:
					if len(planned.Blockers) != 0 || len(planned.Steps) != 2 || planned.Steps[0].AssetID != parent.ID || !slices.Contains(planned.Steps[1].DependsOn, planned.Steps[0].ID) {
						t.Fatal("selected budget did not precede shared action group", planned)
					}
					required, err := plan.RequiredDeletions(planned.Steps[1])
					if err != nil || len(required) != 1 || required[0].AssetID != parent.ID {
						t.Fatal("native prerequisite lost its frozen budget", required, err)
					}
				case selected[0] == parent.ID:
					if len(planned.Blockers) != 0 || len(planned.Steps) != 1 || planned.Steps[0].AssetID != parent.ID {
						t.Fatal("budget deletion selected notification destination", planned)
					}
				default:
					if len(planned.Blockers) == 0 {
						t.Fatal("retained budget failed to protect shared action group", planned)
					}
				}
			}
		})
	}
}

func TestMonitorGraphBoundaries(t *testing.T) {
	for _, mode := range []string{"missing-target", "other-connection", "other-partition", "foreign-subscription", "duplicate-target", "private-change", "group-change", "missing-parent", "missing-proof", "identity-change"} {
		t.Run(mode, func(t *testing.T) {
			f := newMonitorInventoryFixture(t, monitorConsumptionBudgetType)
			id := ""
			for candidate := range f.objects {
				_, scope, _, _ := monitorResourceID(candidate)
				if scope != "/subscriptions/"+testSubscription {
					id = candidate
					break
				}
			}
			targetID := "/subscriptions/" + testSubscription + "/resourcegroups/shared/providers/microsoft.insights/actiongroups/notification"
			if mode == "foreign-subscription" {
				targetID = strings.Replace(targetID, testSubscription, testTenant, 1)
			}
			for _, value := range object(object(f.objects[id]["properties"])["notifications"]) {
				object(value)["contactGroups"] = []any{targetID}
			}
			batch, err := f.runtime.List(t.Context(), f.request())
			if err != nil {
				t.Fatal(err)
			}
			var parent asset.Asset
			for _, item := range batch.Items {
				if item.NativeID == id {
					parent = asset.Asset{ID: asset.AssetID(id), Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: f.kind, NativeID: id}, Location: item.Location, Normalized: item.Normalized}
				}
			}
			target := asset.Asset{ID: "shared-group", Identity: asset.Identity{Provider: asset.ProviderAzure, ConnectionID: "connection", Partition: "azure", NativeType: monitorActionGroupType, NativeID: targetID}}
			values := []asset.Asset{parent, target}
			wantError := false
			switch mode {
			case "missing-target":
				values = values[:1]
			case "other-connection":
				values[1].Identity.ConnectionID = "another-connection"
			case "other-partition":
				values[1].Identity.Partition = "another-partition"
			case "duplicate-target":
				values = append(values, target)
				wantError = true
			case "private-change":
				for _, value := range object(object(f.objects[id]["properties"])["notifications"]) {
					object(value)["contactEmails"] = []any{"PRIVATE_CHANGED_EMAIL"}
				}
				wantError = true
			case "group-change":
				_, scope, _, _ := monitorResourceID(id)
				f.groups[scope]["managedBy"] = "new-owner"
				wantError = true
			case "missing-parent":
				delete(f.objects, id)
				wantError = true
			case "missing-proof":
				delete(parent.Normalized, monitorConfigurationProof)
				wantError = true
			case "identity-change":
				parent.Identity.NativeType = monitorCostBudgetType
				wantError = true
			}
			c, err := f.runtime.resolve(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			contribution, err := c.contributeMonitorReferences(t.Context(), parent, values)
			if wantError {
				if err == nil || isNotFound(err) {
					t.Fatal("changed native graph accepted or treated as absence", mode, contribution, err)
				}
				return
			}
			if err != nil || len(contribution.Unresolved) != 1 || len(contribution.Relationships)+len(contribution.Bindings) != 0 {
				t.Fatal("missing/foreign target was resolved or owned", mode, contribution, err)
			}
			if contribution.Unresolved[0].NativeID != targetID || contribution.Unresolved[0].Relationship != graph.RelationshipUses {
				t.Fatal("unresolved native identity lost", contribution)
			}
		})
	}
}

func TestMonitorExplicitReceiverAndIdentityReferences(t *testing.T) {
	f := newMonitorInventoryFixture(t, monitorActionGroupType)
	id := slices.Sorted(maps.Keys(f.objects))[0]
	raw := f.objects[id]
	group := "/subscriptions/" + testSubscription + "/resourceGroups/shared"
	account := group + "/providers/Microsoft.Automation/automationAccounts/automation"
	function := group + "/providers/Microsoft.Web/sites/function"
	workflow := group + "/providers/Microsoft.Logic/workflows/receiver"
	identity := group + "/providers/Microsoft.ManagedIdentity/userAssignedIdentities/receiver"
	props := object(raw["properties"])
	props["azureFunctionReceivers"] = []any{map[string]any{"name": "function", "functionAppResourceId": function, "functionName": "notify", "httpTriggerUrl": "https://example.invalid/PRIVATE_RECEIVER_URL"}}
	props["logicAppReceivers"] = []any{map[string]any{"name": "workflow", "resourceId": workflow, "callbackUrl": "https://example.invalid/PRIVATE_RECEIVER_URL"}}
	props["automationRunbookReceivers"] = []any{map[string]any{"automationAccountId": account, "runbookName": "notify", "webhookResourceId": account + "/webhooks/notify", "isGlobalRunbook": false}}
	raw["identity"] = map[string]any{"type": "UserAssigned", "userAssignedIdentities": map[string]any{identity: map[string]any{}}}
	refs, err := monitorResourceReferences(monitorActionGroupType, id, raw)
	if err != nil || len(refs) != 6 {
		t.Fatal("explicit receivers missing", refs, err)
	}
	for _, want := range []string{account, function, workflow, account + "/webhooks/notify", account + "/runbooks/notify", function + "/functions/notify"} {
		canonical, typ, _ := parseID(want)
		if mapping, ok := findType(typ); ok {
			typ = mapping.NativeType
		}
		if !slices.Contains(refs[typ], canonical) {
			t.Fatal("receiver reference omitted", want, refs)
		}
	}
	batch, err := f.runtime.List(t.Context(), f.request())
	if err != nil {
		t.Fatal(err)
	}
	wire, _ := json.Marshal(batch)
	if strings.Contains(string(wire), "PRIVATE_RECEIVER_URL") || strings.Contains(string(wire), "httpTriggerUrl") || strings.Contains(string(wire), "automationRunbookReceivers") {
		t.Fatal("private receiver escaped inventory")
	}
	for _, mode := range []string{"function-type", "receiver-alias", "opaque-scopes", "webtest-criteria-type", "webtest-link-type", "webtest-link-value"} {
		t.Run(mode, func(t *testing.T) {
			copy := maps.Clone(raw)
			copy["properties"] = maps.Clone(props)
			kind, self := monitorActionGroupType, id
			wantError := true
			switch mode {
			case "function-type":
				object(copy["properties"])["azureFunctionReceivers"] = []any{map[string]any{"functionAppResourceId": workflow}}
			case "receiver-alias":
				object(copy["properties"])["LogicAppReceivers"] = props["logicAppReceivers"]
			case "opaque-scopes":
				object(copy["properties"])["scopes"] = []any{identity}
				wantError = false
			case "webtest-criteria-type":
				copy = monitorRuleExample(t, "metric-2026-01-01/getWebTestMetricAlert.json")
				kind = monitorMetricAlertType
				self, _, _, _ = monitorResourceID(text(copy["id"]))
				object(object(copy["properties"])["criteria"])["webTestId"] = function
			case "webtest-link-type", "webtest-link-value":
				copy = monitorRuleExample(t, "webtest/WebTestGet.json")
				kind = insightsWebTestType
				self, _, _, _ = monitorResourceID(text(copy["id"]))
				copy["tags"] = map[string]any{"hidden-link:" + function: "Resource"}
				if mode == "webtest-link-value" {
					copy["tags"] = map[string]any{"hidden-link:" + group + "/providers/Microsoft.Insights/components/app": "not-a-resource-link"}
				}
			}
			refs, err := monitorResourceReferences(kind, self, copy)
			if (err != nil) != wantError {
				t.Fatal("native reference boundary differs", mode, err)
			}
			if mode == "opaque-scopes" && len(refs) != 6 {
				t.Fatal("undeclared action-group scopes or identity became dependencies", refs)
			}
		})
	}
}

func TestMonitorNativeAssignedIdentitySlots(t *testing.T) {
	for _, file := range []string{"metric-2026-01-01/getWebTestMetricAlert.json", "scheduled-2026-03-01/getScheduledQueryRule.json"} {
		for _, mode := range []string{"native-slot", "foreign-identity", "identity-shape", "identity-alias", "identity-type", "identity-kind-missing", "identity-entry-shape", "identity-conflict"} {
			t.Run(file+"/"+mode, func(t *testing.T) {
				raw := monitorRuleExample(t, file)
				id, _, kind, err := monitorResourceID(text(raw["id"]))
				if err != nil {
					t.Fatal(err)
				}
				identity := "/subscriptions/" + testSubscription + "/resourcegroups/shared/providers/Microsoft.ManagedIdentity/userAssignedIdentities/reader"
				switch mode {
				case "foreign-identity":
					identity = strings.Replace(identity, testSubscription, testTenant, 1)
				case "identity-type":
					identity = strings.Replace(identity, "Microsoft.ManagedIdentity/userAssignedIdentities", "Microsoft.Web/sites", 1)
				}
				raw["identity"] = map[string]any{"type": "UserAssigned", "userAssignedIdentities": map[string]any{identity: map[string]any{}}}
				if mode == "identity-shape" {
					raw["identity"] = []any{}
				}
				if mode == "identity-alias" {
					raw["identity"] = map[string]any{"UserAssignedIdentities": map[string]any{identity: map[string]any{}}}
				}
				if mode == "identity-kind-missing" {
					delete(object(raw["identity"]), "type")
				}
				if mode == "identity-entry-shape" {
					object(object(raw["identity"])["userAssignedIdentities"])[identity] = []any{}
				}
				if mode == "identity-conflict" {
					object(raw["identity"])["type"] = "SystemAssigned"
				}
				refs, err := monitorResourceReferences(kind, id, raw)
				if mode == "native-slot" || mode == "foreign-identity" {
					if err != nil || !slices.Contains(refs["Microsoft.ManagedIdentity/userAssignedIdentities"], strings.ToLower(identity)) {
						t.Fatal("declared managed-identity slot omitted", refs, err)
					}
				} else if err == nil {
					t.Fatal("malformed native identity became a reference", refs)
				}
			})
		}
	}
}
