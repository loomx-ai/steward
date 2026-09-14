package gcp

import (
	"net/http"
	"slices"
	"testing"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func securitySubscriptionData() map[string]any {
	return map[string]any{"name": "organizations/123/subscription", "tier": "PREMIUM", "details": map[string]any{"type": "TRIAL", "startTime": "2026-09-01T00:00:00Z", "endTime": "2026-10-01T00:00:00Z"}}
}

func securitySubscriptionRequest(r *Runtime) contracts.InventoryRequest {
	request := organizationRequest(r)
	kind := r.resourceKind(securitySubscriptionType)
	request.ResourceKind = &kind
	return request
}

func subscriptionScenario(t *testing.T, data map[string]any) (*organizationScenario, *Runtime) {
	t.Helper()
	s := newOrganizationScenario()
	s.hook = func(req *http.Request) (*http.Response, bool) {
		if req.URL.Host != securitySubscriptionHost {
			return nil, false
		}
		if req.URL.Path != "/v1beta2/organizations/123/subscription" {
			t.Fatalf("foreign subscription request: %s", req.URL)
		}
		response := dataformResponse(req, 200, data)
		response.Header.Set("X-Goog-Request-Id", "subscription-request")
		return response, true
	}
	return s, s.runtime(t)
}

func TestSecuritySubscriptionNativeContractAndInventory(t *testing.T) {
	compiler := infraFixtureSchemas(t, "fixtures/security-subscription/provenance.json", "20260828", "490c89a11880b86749392ab72fab8ec2cdc55f80848d67b1da684c2977f2ab3c")
	schema, err := compiler.Compile("https://fixture.test/infra-manager.json#/definitions/Subscription")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"trial", "historical", "none", "pay_as_you_go", "enterprise", "future"} {
		t.Run(mode, func(t *testing.T) {
			data := securitySubscriptionData()
			switch mode {
			case "historical":
				data["tier"] = "STANDARD"
				object(data["details"])["endTime"] = "2026-09-02T00:00:00Z"
			case "none":
				delete(data, "details")
			case "pay_as_you_go":
				object(data["details"])["type"] = "PAY_AS_YOU_GO"
			case "enterprise":
				data["tier"] = "ENTERPRISE_MC"
				object(data["details"])["type"] = "SUB_BASE_OVERAGE"
			case "future":
				data["tier"] = "FUTURE_TIER"
				object(data["details"])["type"] = "FUTURE_TYPE"
			}
			if err := schema.Validate(data); err != nil && mode != "future" {
				t.Fatal(err)
			}
			_, r := subscriptionScenario(t, data)
			batch, err := r.List(t.Context(), securitySubscriptionRequest(r))
			if err != nil || !batch.Complete || len(batch.Items) != 1 || batch.RequestID != "subscription-request" {
				t.Fatalf("native subscription: %+v %v", batch, err)
			}
			item := batch.Items[0]
			if item.NativeID != "//securitycenter.googleapis.com/organizations/123/subscription" || item.Normalized["tier"] != data["tier"] || item.State != "" || item.Actionable == nil || *item.Actionable {
				t.Fatal("incorrect subscription", item)
			}
			if _, ok := item.Normalized["project_id"]; ok {
				t.Fatal("organization subscription assigned project ownership")
			}
			if item.Normalized["subscriptionType"] != object(data["details"])["type"] || item.Normalized["endTime"] != object(data["details"])["endTime"] {
				t.Fatal("native subscription details lost", item.Normalized)
			}
			value := asset.Asset{ID: "subscription", Identity: asset.Identity{Provider: asset.ProviderGCP, ConnectionID: "connection", NativeType: securitySubscriptionType, NativeID: item.NativeID}, Normalized: item.Normalized}
			if _, err := r.ResolveAction(t.Context(), "connection", value); err == nil {
				t.Fatal("subscription gained delete")
			}
			result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: securitySubscriptionGet, Parameters: map[string]any{"name": data["name"]}})
			if err != nil || result.Data["tier"] != data["tier"] {
				t.Fatal("native invocation", result, err)
			}
		})
	}
}

func TestSecuritySubscriptionScopeAndFailureBoundaries(t *testing.T) {
	for _, mode := range []string{"denied", "missing", "identity", "tier", "details", "type", "start", "end", "moved", "lost_ancestor"} {
		t.Run(mode, func(t *testing.T) {
			data := securitySubscriptionData()
			s, r := subscriptionScenario(t, data)
			native := s.hook
			s.hook = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Host != securitySubscriptionHost {
					return native(req)
				}
				switch mode {
				case "denied":
					return dataformResponse(req, 403, map[string]any{}), true
				case "missing":
					return dataformResponse(req, 404, map[string]any{}), true
				case "identity":
					data["name"] = "organizations/999/subscription"
				case "tier":
					data["tier"] = true
				case "details":
					data["details"] = []any{}
				case "type":
					object(data["details"])["type"] = 1
				case "start":
					object(data["details"])["startTime"] = "not-a-time"
				case "end":
					object(data["details"])["endTime"] = map[string]any{}
				case "moved":
					delete(s.project, "parent")
				case "lost_ancestor":
					s.hook = func(req *http.Request) (*http.Response, bool) {
						return dataformResponse(req, 403, map[string]any{}), true
					}
				}
				return native(req)
			}
			if batch, err := r.List(t.Context(), securitySubscriptionRequest(r)); err == nil || len(batch.Items) != 0 {
				t.Fatal("failed subscription returned inventory", batch, err)
			}
		})
	}
	for _, name := range []any{"organizations/999/subscription", "organizations/0123/subscription", "projects/sample-project/subscription", "organizations/123/subscription/../subscription", "organizations/123/subscription?x=y", nil, map[string]any{}} {
		s, r := subscriptionScenario(t, securitySubscriptionData())
		if _, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: securitySubscriptionGet, Parameters: map[string]any{"name": name}}); err == nil {
			t.Fatal("foreign subscription invocation accepted", name)
		}
		if slices.Contains(s.calls, "/v1beta2/organizations/123/subscription") {
			t.Fatal("invalid request reached subscription")
		}
	}
	s, r := subscriptionScenario(t, securitySubscriptionData())
	delete(s.project, "parent")
	batch, err := r.List(t.Context(), securitySubscriptionRequest(r))
	if err != nil || !batch.Complete || len(batch.Items) != 0 || slices.Contains(s.calls, "/v1beta2/organizations/123/subscription") {
		t.Fatal("unowned project invented subscription", batch, err)
	}
	for _, scope := range []asset.Scope{{Kind: asset.ScopeGlobal, NativeID: "other/global"}, {Kind: asset.ScopeProject, NativeID: "other"}, {Kind: asset.ScopeRegion, NativeID: "us-central1"}} {
		request := securitySubscriptionRequest(r)
		request.Scope = scope
		if _, err := r.List(t.Context(), request); err == nil {
			t.Fatal("invalid scan scope accepted", scope)
		}
	}
	request := securitySubscriptionRequest(r)
	request.Cursor = "stale"
	if _, err := r.List(t.Context(), request); err == nil {
		t.Fatal("singleton cursor accepted")
	}
	request = securitySubscriptionRequest(r)
	request.NetworkTarget = &asset.ScanTarget{Kind: asset.ScanTargetVPC}
	if _, err := r.List(t.Context(), request); err == nil {
		t.Fatal("subscription network scope accepted")
	}
}

func TestSecuritySubscriptionInvokeRechecksAncestryAndParameters(t *testing.T) {
	for _, mode := range []string{"moved", "unknown_parameter", "changed_identity", "invalid_details"} {
		t.Run(mode, func(t *testing.T) {
			data := securitySubscriptionData()
			s, r := subscriptionScenario(t, data)
			native := s.hook
			s.hook = func(req *http.Request) (*http.Response, bool) {
				if req.URL.Host == securitySubscriptionHost {
					if mode == "moved" {
						delete(s.project, "parent")
					}
					if mode == "changed_identity" {
						data["name"] = "organizations/999/subscription"
					}
					if mode == "invalid_details" {
						data["details"] = "invalid"
					}
				}
				return native(req)
			}
			parameters := map[string]any{"name": "organizations/123/subscription"}
			if mode == "unknown_parameter" {
				parameters["project"] = "other"
			}
			if result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: "connection", Operation: securitySubscriptionGet, Parameters: parameters}); err == nil || len(result.Data) != 0 {
				t.Fatal("invalid native observation escaped", result, err)
			}
			if mode == "unknown_parameter" && slices.Contains(s.calls, "/v1beta2/organizations/123/subscription") {
				t.Fatal("unknown parameter reached subscription")
			}
		})
	}
}
