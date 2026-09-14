package gcp

import (
	"context"
	"slices"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

func (a *action) routerComponentIncarnationKey() string {
	if a.kind.NativeType == cloudNatType {
		return cloudNatRouterID
	}
	if a.kind.NativeType == namedSetType {
		return namedSetRouterID
	}
	return routePolicyRouterID
}

func (a *action) routerComponentDeletePhase() string {
	if a.kind.NativeType == cloudNatType {
		return "cloud_nat_delete"
	}
	if a.kind.NativeType == namedSetType {
		return "named_set_delete"
	}
	return "route_policy_delete"
}

func (a *action) routerComponentConfiguration(data map[string]any) string {
	if a.kind.NativeType == cloudNatType {
		return firewallDigest(cloudNatConfiguration(data))
	}
	if a.kind.NativeType == routePolicyType {
		return routePolicyConfiguration(data)
	}
	value := map[string]any{}
	for _, key := range []string{"name", "type", "description", "elements", "fingerprint"} {
		if field, exists := data[key]; exists {
			value[key] = field
		}
	}
	return firewallDigest(value)
}

// Any policy on the router can hold a set in use, including policies not attached
// to a BGP peer or selected in the local inventory. Inspect every native page and
// fresh policy detail; a partial list or unresolved expression cannot prove safety.
func (a *action) namedSetUnreferenced(ctx context.Context) error {
	metadata, err := providerData()
	if err != nil {
		return err
	}
	op, ok := metadata.catalog.Operation(routePolicyList)
	if !ok {
		return groupDenied("named_set_policy_list_missing")
	}
	parameters := cloneParameters(a.deleteParameters)
	delete(parameters, "namedSet")
	parameters["maxResults"] = 500
	seenTokens, seenPolicies := map[string]bool{}, map[string]bool{}
	for {
		bound, err := catalog.BindREST(op, parameters)
		if err != nil {
			return err
		}
		data, err := a.client.request(ctx, bound.Method, bound.URL, nil)
		if err != nil {
			return err
		}
		if err := checkListCompleteness(data); err != nil {
			return err
		}
		if value, present := data["result"]; present {
			policies, ok := value.([]any)
			if !ok {
				return groupDenied("named_set_policy_list_invalid")
			}
			for _, policy := range policies {
				name := text(object(policy)["name"])
				if !routePolicySegment.MatchString(name) || seenPolicies[name] {
					return groupDenied("named_set_policy_list_invalid")
				}
				seenPolicies[name] = true
				live, err := a.client.routePolicyRead(ctx, a.routerComponentParent()+"/routePolicies/"+name)
				if err != nil {
					return err
				}
				refs, err := routePolicySetReferences(live)
				if err != nil {
					return err
				}
				if slices.Contains(refs, last(a.identity.NativeID)) {
					return groupDenied("named_set_referenced_by_policy")
				}
			}
		}
		next, present := data["nextPageToken"]
		if !present || next == "" {
			return nil
		}
		token, ok := next.(string)
		if !ok || seenTokens[token] {
			return groupDenied("named_set_policy_pagination_invalid")
		}
		seenTokens[token] = true
		parameters["pageToken"] = token
	}
}
