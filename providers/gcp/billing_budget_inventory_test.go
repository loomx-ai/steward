package gcp

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const testBillingBudgetID = "//billingbudgets.googleapis.com/" + testBillingBudget

func billingInventoryRequest(r *Runtime) contracts.InventoryRequest {
	kind := r.resourceKind(billingBudgetType)
	return contracts.InventoryRequest{ConnectionID: "connection", Source: billingBudgetSource, Scope: asset.Scope{Kind: asset.ScopeGlobal, NativeID: "sample-project/global"}, ResourceKind: &kind}
}

// Visibility omissions are successful LIST responses; only exact own GETs can
// reconcile saved budgets. All other account/budget operations stay native.
func billingInventoryScenario(t *testing.T) (*billingBudgetScenario, *Runtime, *string) {
	t.Helper()
	s := budgetScenario(t, nil)
	mode := ""
	r := protocolRuntime(t, func(req *http.Request) (*http.Response, error) {
		if req.Method != "GET" {
			t.Fatal("inventory wrote", req.Method)
		}
		if strings.HasPrefix(mode, "known-") && req.URL.Host == "cloudbilling.googleapis.com" && req.URL.Path == "/v1/billingAccounts" {
			return apiResponse(req, 200, `{}`), nil
		}
		if mode == "known-parent-late-denied" && req.URL.Host == "cloudbilling.googleapis.com" && req.URL.Path == "/v1/"+testBillingAccount && s.calls["account"] > 0 {
			return apiResponse(req, 403, `{}`), nil
		}
		if strings.HasPrefix(mode, "known-account-") && req.URL.Host == "cloudbilling.googleapis.com" && req.URL.Path == "/v1/"+testBillingAccount {
			code := 403
			if mode == "known-account-missing" {
				code = 404
			}
			return apiResponse(req, code, `{}`), nil
		}
		if req.URL.Host == "billingbudgets.googleapis.com" && req.URL.Path == "/v1/"+testBillingBudget {
			if mode == "known-gone" || strings.HasPrefix(mode, "known-parent-") {
				return apiResponse(req, 404, `{}`), nil
			}
			if mode == "known-denied" {
				return apiResponse(req, 403, `{}`), nil
			}
			if mode == "late-budget-drift" && s.calls["budget"] > 0 {
				s.budget["etag"] = "changed-after-index"
			}
		}
		return s.r.transport.RoundTrip(req)
	})
	return s, r, &mode
}

func TestBillingBudgetInventoryAndKnownReconciliation(t *testing.T) {
	for _, mode := range []string{"present", "paged", "empty", "accounts-denied", "budgets-denied", "budget-missing", "known-live", "known-gone", "known-denied", "known-account-denied", "known-account-missing", "known-parent-drift", "known-parent-late-denied", "late-budget-drift"} {
		t.Run(mode, func(t *testing.T) {
			s, r, state := billingInventoryScenario(t)
			*state = mode
			if !strings.HasPrefix(mode, "known-") {
				s.mode = mode
			}
			if mode == "known-parent-drift" {
				s.mode = "account-late"
			}
			object(s.budget["notificationsRule"])["pubsubTopic"] = "projects/PRIVATE_DELIVERY/topics/PRIVATE_DELIVERY"
			object(s.budget["budgetFilter"])["labels"] = map[string]any{"private": "PRIVATE_FILTER"}
			request := billingInventoryRequest(r)
			if strings.HasPrefix(mode, "known-") {
				request.KnownNativeIDs = []string{testBillingBudgetID}
			}
			batch, err := r.List(t.Context(), request)
			good := slices.Contains([]string{"present", "paged", "empty", "known-live", "known-gone"}, mode)
			if (err == nil) != good {
				t.Fatal(batch, err)
			}
			if !good {
				if batch.Complete || len(batch.Items)+len(batch.AbsentNativeIDs) != 0 {
					t.Fatal("failed scan exposed partial authority", batch)
				}
				return
			}
			if !batch.Complete || batch.NextCursor != "" {
				t.Fatal(batch)
			}
			if mode == "empty" || mode == "known-gone" {
				if len(batch.Items) != 0 || (len(batch.AbsentNativeIDs) == 1) != (mode == "known-gone") {
					t.Fatal(batch)
				}
				if mode == "known-gone" && (batch.AbsentNativeIDs[0] != testBillingBudgetID || batch.RequestID != "request-123") {
					t.Fatal(batch)
				}
				return
			}
			if len(batch.Items) != 1 || len(batch.AbsentNativeIDs) != 0 || batch.RequestID != "request-123" {
				t.Fatal(batch)
			}
			item := batch.Items[0]
			if item.NativeID != testBillingBudgetID || item.Normalized["billingAccount"] != testBillingAccount || item.Normalized[billingBudgetReview] != firewallDigest(s.budget) || item.Normalized["project_id"] != nil || item.Scope.NativeID != "sample-project/global" || !slices.Contains(item.ResourceKind.Capabilities, asset.CapabilityActionable) {
				t.Fatal(item)
			}
			payload, _ := json.Marshal(item)
			if strings.Contains(string(payload), "PRIVATE_DELIVERY") || strings.Contains(string(payload), "PRIVATE_FILTER") {
				t.Fatal("private budget settings escaped")
			}
			if _, err := r.ResolveAction(t.Context(), "connection", asset.Asset{Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", Partition: "gcp", NativeType: billingBudgetType, NativeID: item.NativeID}, Normalized: item.Normalized}); err != nil {
				t.Fatal("reviewed budget action unavailable", err)
			}

		})
	}
}

func TestBillingBudgetInventoryScopeAndSource(t *testing.T) {
	for _, mode := range []string{"source", "kind", "region", "foreign-global", "foreign-project", "cursor", "network", "options", "invalid-known", "duplicate-known", "unrelated-metadata"} {
		t.Run(mode, func(t *testing.T) {
			s := budgetScenario(t, nil)
			req := billingInventoryRequest(s.r)
			switch mode {
			case "source":
				req.Source = productInventorySource
			case "kind":
				req.ResourceKind.NativeType = notificationChannelType
			case "region":
				req.Scope.Kind = asset.ScopeRegion
			case "foreign-global":
				req.Scope.NativeID = "foreign/global"
			case "foreign-project":
				req.Scope.Kind = asset.ScopeProject
				req.Scope.NativeID = "foreign"
			case "cursor":
				req.Cursor = "unbound-page"
			case "network":
				req.NetworkTarget = &asset.ScanTarget{}
			case "options":
				req.Options = map[string]any{"scope": "hidden"}
			case "invalid-known":
				req.KnownNativeIDs = []string{testBillingBudgetID + "/extra"}
			case "duplicate-known":
				req.KnownNativeIDs = []string{testBillingBudgetID, testBillingBudgetID}
			case "unrelated-metadata":
				req.KnownNativeMetadata = map[string]map[string]any{testBillingBudgetID: {}}
			}
			batch, err := s.r.List(t.Context(), req)
			if err == nil || batch.Complete || len(batch.Items)+len(batch.AbsentNativeIDs) != 0 || len(s.calls) != 0 {
				t.Fatal(batch, s.calls, err)
			}
		})
	}
	s := budgetScenario(t, nil)
	found := false
	for _, source := range s.r.InventorySources() {
		if source.Name == billingBudgetSource {
			found = true
			if source.AuthoritativeDefault || !source.ReconcileKnownIDs || !source.KindSpecific || source.NetworkClosure {
				t.Fatal(source)
			}
		}
	}
	if !found {
		t.Fatal("budget source not registered")
	}
}

func TestBillingBudgetObservedAssetDependency(t *testing.T) {
	for _, mode := range []string{"current", "stale", "closed", "foreign", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			channel, _, _, _ := channelDependencyFixture(t)
			s := budgetScenario(t, channel.r.transport.RoundTrip)
			batch, err := s.r.List(t.Context(), billingInventoryRequest(s.r))
			if err != nil || len(batch.Items) != 1 {
				t.Fatal(batch, err)
			}
			budget := asset.Asset{ID: "budget", Identity: channel.request.Asset.Identity, Normalized: batch.Items[0].Normalized}
			budget.Identity.NativeType, budget.Identity.NativeID = billingBudgetType, testBillingBudgetID
			if mode == "stale" {
				budget.Normalized[billingBudgetReview] = "stale"
			}
			if mode == "closed" {
				now := time.Now()
				budget.ClosedAt = &now
			}
			if mode == "foreign" {
				budget.Identity.ConnectionID = "foreign"
			}
			values := []asset.Asset{channel.request.Asset, channel.policy, budget}
			if mode == "duplicate" {
				values = append(values, budget)
			}
			h, err := s.r.MonitoringDependencies(t.Context(), "connection")
			if err != nil {
				t.Fatal(err)
			}
			result, err := h.Contribute(t.Context(), "global", values)
			if mode == "duplicate" {
				if err == nil {
					t.Fatal("ambiguous budget identity accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			refs := assertBudgetCoverage(t, result.Unresolved, channel.request.Asset.ID)
			if mode != "current" {
				if len(refs) != 1 || len(result.Relationships) != 1 {
					t.Fatal(result)
				}
				return
			}
			if len(refs) != 0 || len(result.Relationships) != 2 {
				t.Fatal(result)
			}
			found := false
			for _, edge := range result.Relationships {
				if edge.TargetAssetID == budget.ID {
					found = true
					if edge.SourceAssetID != channel.request.Asset.ID || edge.Evidence[graph.RelationshipEvidenceRequiredDeletion] != true || edge.Evidence[graph.RelationshipEvidenceAutomaticSelection] != false || edge.Evidence[graph.RelationshipEvidenceDeletionOrder] != graph.DeletionOrderTargetBeforeSource {
						t.Fatal(edge)
					}
				}
			}
			if !found {
				t.Fatal("fresh budget not linked")
			}
		})
	}
}

func TestBillingBudgetReadBindingAndRedaction(t *testing.T) {
	s := budgetScenario(t, nil)
	c, err := s.r.resolve(t.Context(), "connection")
	if err != nil {
		t.Fatal(err)
	}
	kind, ok := findType(billingBudgetType)
	if !ok {
		t.Fatal("missing budget mapping")
	}
	url, err := c.resourceURL(kind, testBillingBudgetID)
	if err != nil || url != "https://billingbudgets.googleapis.com/v1/"+testBillingBudget {
		t.Fatal(url, err)
	}
	if operation, _, err := c.resourceOperation(kind, testBillingBudgetID, "DELETE"); err != nil || operation.ID != billingBudgetDelete {
		t.Fatal("native delete binding missing", operation, err)
	}
	for _, id := range []string{testBillingBudget, testBillingBudgetID + "/extra", strings.Replace(testBillingBudgetID, "billingbudgets.googleapis.com", "foreign.test", 1)} {
		if _, err := c.resourceURL(kind, id); err == nil {
			t.Fatal("invalid identity bound", id)
		}
	}
	raw := billingBudgetFixture()
	object(raw["notificationsRule"])["pubsubTopic"] = "PRIVATE_DELIVERY"
	object(raw["budgetFilter"])["labels"] = map[string]any{"private": "PRIVATE_FILTER"}
	cleaned := safePayload(map[string]any{"budgets": []any{raw}})
	b, _ := json.Marshal(cleaned)
	if strings.Contains(string(b), "PRIVATE_DELIVERY") || strings.Contains(string(b), "PRIVATE_FILTER") || object(raw["notificationsRule"])["pubsubTopic"] != "PRIVATE_DELIVERY" {
		t.Fatal("redaction leaked or mutated native proof input")
	}
}
