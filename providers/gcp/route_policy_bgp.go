package gcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const routePolicyPeers = "_route_policy_bgp_peers"
const routePolicyDetach = "route_policy_detach"

// Keep the entire peer configuration for the native merge-patch array replacement.
// Authentication key material belongs to Router.md5AuthenticationKeys, not peers.
func routePolicyBGPPeers(data map[string]any) ([]any, error) {
	peers := []any{}
	if raw, exists := data["bgpPeers"]; exists {
		var ok bool
		peers, ok = raw.([]any)
		if !ok {
			return nil, groupDenied("route_policy_bgp_peers_invalid")
		}
	}
	result := make([]any, 0, len(peers))
	seen := map[string]bool{}
	for _, value := range peers {
		peer, ok := value.(map[string]any)
		name, validName := peer["name"].(string)
		if !ok || !validName || !routePolicySegment.MatchString(name) || seen[name] {
			return nil, groupDenied("route_policy_bgp_peer_invalid")
		}
		seen[name] = true
		peer = cloneParameters(peer)
		for _, field := range []string{"importPolicies", "exportPolicies"} {
			policies := []any{}
			if raw, exists := peer[field]; exists {
				var ok bool
				policies, ok = raw.([]any)
				if !ok {
					return nil, groupDenied("route_policy_bgp_policies_invalid")
				}
			}
			for _, value := range policies {
				policy, ok := value.(string)
				if !ok || !routePolicySegment.MatchString(policy) {
					return nil, groupDenied("route_policy_bgp_policy_invalid")
				}
			}
			peer[field] = slices.Clone(policies)
		}
		result = append(result, peer)
	}
	// Peer membership is unordered; each peer's import/export evaluation order is not.
	slices.SortFunc(result, func(a, b any) int { return strings.Compare(text(object(a)["name"]), text(object(b)["name"])) })
	return result, nil
}

func routePolicyBGPReview(data map[string]any, policy string) (before, after []any, err error) {
	raw, exists := data[routePolicyPeers]
	if !exists {
		return nil, nil, groupDenied("route_policy_bgp_review_missing")
	}
	before, err = routePolicyBGPPeers(map[string]any{"bgpPeers": raw})
	if err != nil {
		return nil, nil, err
	}
	after = make([]any, 0, len(before))
	for _, value := range before {
		peer := cloneParameters(object(value))
		for _, field := range []string{"importPolicies", "exportPolicies"} {
			remaining := []any{}
			for _, value := range array(peer[field]) {
				if value != policy {
					remaining = append(remaining, value)
				}
			}
			peer[field] = remaining
		}
		after = append(after, peer)
	}
	return before, after, nil
}

// Preserve successful sibling deletions without accepting new peers, settings,
// references or evaluation order. A removed sibling reference is safe only when
// its native policy is absent and the containing router still matches.
func (a *action) routePolicyBGPMerge(ctx context.Context, request contracts.ActionRequest, parent map[string]any) ([]any, bool, error) {
	before, _, err := routePolicyBGPReview(request.Asset.Normalized, last(a.identity.NativeID))
	if err != nil {
		return nil, false, err
	}
	live, err := routePolicyBGPPeers(parent)
	if err != nil {
		return nil, false, err
	}
	if len(before) != len(live) {
		return nil, false, groupDenied("route_policy_bgp_configuration_changed")
	}
	removed := map[string]bool{}
	for i, value := range before {
		old, current := cloneParameters(object(value)), cloneParameters(object(live[i]))
		for _, direction := range []string{"importPolicies", "exportPolicies"} {
			actual := array(current[direction])
			index := 0
			for _, policy := range array(old[direction]) {
				if index < len(actual) && actual[index] == policy {
					index++
				} else {
					removed[text(policy)] = true
				}
			}
			if index != len(actual) {
				return nil, false, groupDenied("route_policy_bgp_configuration_changed")
			}
			delete(old, direction)
			delete(current, direction)
		}
		if firewallDigest(old) != firewallDigest(current) {
			return nil, false, groupDenied("route_policy_bgp_configuration_changed")
		}
	}
	delete(removed, last(a.identity.NativeID))
	for name := range removed {
		if _, err := a.client.routePolicyRead(ctx, a.routerComponentParent()+"/routePolicies/"+name); !isNotFound(err) {
			if err != nil {
				return nil, false, err
			}
			return nil, false, groupDenied("route_policy_removed_sibling_still_exists")
		}
	}
	if len(removed) != 0 {
		latest, err := a.routerComponentParentData(ctx, request)
		if err != nil {
			return nil, false, err
		}
		peers, err := routePolicyBGPPeers(latest)
		if err != nil {
			return nil, false, err
		}
		if firewallDigest(peers) != firewallDigest(live) {
			return nil, false, groupDenied("route_policy_bgp_configuration_changed")
		}
	}
	_, desired, err := routePolicyBGPReview(map[string]any{routePolicyPeers: live}, last(a.identity.NativeID))
	return desired, firewallDigest(desired) != firewallDigest(live), err
}

func (a *action) routePolicyNativeRequestID(request contracts.ActionRequest, stage string) string {
	if a.kind.NativeType == routerType && stage == routerPriorDelete {
		return googleRequestID(request.IdempotencyKey)
	}
	if stage == routePolicyDetach {
		return googleRequestID(a.routerComponentRequestID(request) + "/bgp")
	}
	return a.routerComponentRequestID(request)
}

func (a *action) detachRoutePolicy(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	parent, err := a.routerComponentParentData(ctx, request)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	peers, attached, err := a.routePolicyBGPMerge(ctx, request, parent)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !attached {
		return a.deleteRouterComponent(ctx, request)
	}
	metadata, err := providerData()
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operation, ok := metadata.catalog.Operation("compute.routers.patch")
	if !ok {
		return contracts.ActionResult{}, groupDenied("route_policy_bgp_method_missing")
	}
	parameters := cloneParameters(a.deleteParameters)
	delete(parameters, "policy")
	parameters["requestId"] = a.routePolicyNativeRequestID(request, routePolicyDetach)
	// managementType is output-only; all writable and forward-compatible peer fields
	// are preserved. Other router fields (NAT, interfaces, BGP, keys) are omitted.
	writable := make([]any, 0, len(peers))
	for _, value := range peers {
		peer := cloneParameters(object(value))
		delete(peer, "managementType")
		writable = append(writable, peer)
	}
	parameters["body"] = map[string]any{"bgpPeers": writable}
	bound, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	response, err := a.client.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	endpoint, err := a.routerComponentOperationResult(request, response.Data, "", response.RequestID, routePolicyDetach)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	return contracts.ActionResult{ProviderRequestID: response.RequestID, ProviderOperationID: endpoint, Data: a.routerComponentStage(request, routePolicyDetach, endpoint, endpoint, text(response.Data["operationType"])), RetryAfter: 2 * time.Second}, nil
}

// Cursor data contains only a snapshot of peers, never the router's secret keys.
func routePolicyBGPFromCursor(encoded string) ([]any, error) {
	var data map[string]any
	if encoded == "" || json.Unmarshal([]byte(encoded), &data) != nil {
		return nil, groupDenied("route_policy_bgp_cursor_invalid")
	}
	return routePolicyBGPPeers(data)
}
