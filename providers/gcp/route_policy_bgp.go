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

func (a *action) routePolicyBGPState(request contracts.ActionRequest, parent map[string]any) (bool, error) {
	before, after, err := routePolicyBGPReview(request.Asset.Normalized, last(a.identity.NativeID))
	if err != nil {
		return false, err
	}
	live, err := routePolicyBGPPeers(parent)
	if err != nil {
		return false, err
	}
	digest := firewallDigest(live)
	if digest == firewallDigest(after) {
		return false, nil
	}
	if digest == firewallDigest(before) {
		return true, nil
	}
	return false, groupDenied("route_policy_bgp_configuration_changed")
}

func (a *action) routePolicyNativeRequestID(request contracts.ActionRequest, stage string) string {
	if stage == routePolicyDetach {
		return googleRequestID(a.routerComponentRequestID(request) + "/bgp")
	}
	return a.routerComponentRequestID(request)
}

func (a *action) detachRoutePolicy(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	_, peers, err := routePolicyBGPReview(request.Asset.Normalized, last(a.identity.NativeID))
	if err != nil {
		return contracts.ActionResult{}, err
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
