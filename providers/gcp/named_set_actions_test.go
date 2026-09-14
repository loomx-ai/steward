package gcp

import (
	"testing"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func TestNamedSetNativePolicyReferenceGuards(t *testing.T) {
	for _, mode := range []string{"list-denied", "list-missing", "list-partial", "list-invalid", "list-token", "list-cycle", "reference-denied", "reference-missing", "referenced", "community-reference", "unresolved", "list-pages", "duplicate", "invalid-name"} {
		t.Run(mode, func(t *testing.T) {
			r, request, fixture := routerComponentActionRuntime(t, namedSetType)
			fixture.mode = mode
			policy := routePolicyFixture("not-attached")
			expression := "destination.inAnyRange(prefixSets('other-set'))"
			switch mode {
			case "referenced", "list-pages":
				expression = "destination.inAnyRange(prefixSets('set-a'))"
			case "community-reference":
				expression = "communities.matchesEvery(communitySets('set-a'))"
			case "unresolved":
				expression = "prefixSets(name)"
			case "invalid-name":
				policy["name"] = "../foreign"
			}
			object(array(policy["terms"])[0])["match"] = map[string]any{"expression": expression}
			fixture.policies = []map[string]any{policy}
			if mode == "duplicate" {
				fixture.policies = append(fixture.policies, policy)
			}
			driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = driver.Execute(t.Context(), request); err == nil || fixture.deletes != 0 || isNotFound(err) {
				t.Fatal("unverified policy references reached deletion or became absence", err, fixture.deletes)
			}
			if mode == "list-pages" && fixture.policyLists != 2 {
				t.Fatal("did not reach second policy page", fixture.policyLists)
			}
		})
	}
}

func TestNamedSetIgnoresNonReferencesAndRejectsReintroducedReference(t *testing.T) {
	r, request, fixture := routerComponentActionRuntime(t, namedSetType)
	policy := routePolicyFixture("not-attached")
	object(array(policy["terms"])[0])["match"] = map[string]any{"expression": `"prefixSets('set-a')" == "text" || prefixSets('other-set') == []`}
	fixture.policies = []map[string]any{policy}
	fixture.mode = "list-pages"
	driver, err := r.ResolveAction(t.Context(), "connection", request.Asset)
	if err != nil {
		t.Fatal(err)
	}
	result, err := driver.Execute(t.Context(), request)
	if err != nil || fixture.deletes != 1 {
		t.Fatal(result, err)
	}
	object(array(policy["terms"])[0])["match"] = map[string]any{"expression": `prefixSets('set-a')`}
	fixture.status = "DONE"
	fixture.exists = false
	if wait, err := driver.Wait(t.Context(), request, result); err == nil || wait.Done {
		t.Fatal("dangling reference became successful cleanup", wait, err)
	}
	fixture.policies = nil
	if wait, err := driver.Wait(t.Context(), request, result); err != nil || !wait.Done {
		t.Fatal(wait, err)
	}
	for _, phase := range []string{routePolicyDetach, "route_policy_delete"} {
		changed := result
		changed.Data = cloneParameters(result.Data)
		changed.Data["phase"] = phase
		before := fixture.reads
		if _, err := driver.Wait(t.Context(), request, changed); err == nil || fixture.reads != before {
			t.Fatal("policy phase accepted for named set", err)
		}
	}
	// Normalized inventory-only fields do not change native configuration; elements do.
	a := driver.(*action)
	changed := cloneParameters(request.Asset.Normalized)
	changed["project_id"] = "ignored"
	if a.routerComponentConfiguration(changed) != a.routerComponentConfiguration(request.Asset.Normalized) {
		t.Fatal("inventory fields changed review")
	}
	changed["elements"] = []any{map[string]any{"expression": "'different'"}}
	if a.routerComponentConfiguration(changed) == a.routerComponentConfiguration(request.Asset.Normalized) {
		t.Fatal("element edit lost from review")
	}
}

func TestNamedSetDeleteInvocationBoundaries(t *testing.T) {
	r, request, fixture := routerComponentActionRuntime(t, namedSetType)
	for _, parameters := range []map[string]any{
		{"project": "foreign", "region": "us-central1", "router": "router-a", "namedSet": "set-a"},
		{"region": "../foreign", "router": "router-a", "namedSet": "set-a"},
		{"region": "us-central1", "router": "router-a", "namedSet": "../other"},
		{"region": "us-central1", "router": "router-a"},
	} {
		_, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: request.Asset.Identity.ConnectionID, Operation: namedSetDelete, Parameters: parameters})
		if err == nil || fixture.deletes != 0 {
			t.Fatal("invalid invocation reached cloud", err)
		}
	}
	result, err := r.Invoke(t.Context(), contracts.Invocation{ConnectionID: request.Asset.Identity.ConnectionID, Operation: namedSetDelete, Parameters: map[string]any{"project": "123456", "region": "us-central1", "router": "router-a", "namedSet": "set-a", "requestId": "10000000-0000-4000-8000-000000000001"}})
	if err != nil || fixture.deletes != 1 || result.RequestID == "" {
		t.Fatal(result, err)
	}
}
