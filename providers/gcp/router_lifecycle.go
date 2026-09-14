package gcp

import (
	"context"
	"encoding/hex"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/graph"
)

func (c *client) routerSaved(value asset.Asset) error {
	kind, _ := findType(routerType)
	if _, err := c.resourceURL(kind, value.Identity.NativeID); err != nil {
		return err
	}
	if value.ID == "" || value.Identity.NativeType != routerType || !firewallNumericID(text(value.Normalized["id"])) || value.Normalized["name"] != last(value.Identity.NativeID) {
		return groupDenied("router_review_identity_invalid")
	}
	for _, field := range []string{routerReview, routerBaseReview} {
		digest, err := hex.DecodeString(text(value.Normalized[field]))
		if err != nil || len(digest) != 32 {
			return groupDenied("router_review_missing")
		}
	}
	_, err := routePolicyBGPPeers(value.Normalized)
	return err
}

// These native components do not have ordinary nested REST GET endpoints.
func (c *client) routerLiveChildren(ctx context.Context, parent asset.Identity, data map[string]any) ([]serviceChild, error) {
	nats, err := c.cloudNatRouter(data, parent.NativeID, text(data["id"]))
	if err != nil {
		return nil, err
	}
	var result []serviceChild
	for _, nat := range nats {
		result = append(result, serviceChild{kind: cloudNatType, id: parent.NativeID + "/nats/" + text(nat["name"]), data: nat})
	}
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	parts := strings.Split(strings.TrimPrefix(parent.NativeID, "//compute.googleapis.com/"), "/")
	for _, entry := range []struct{ kind, method, collection string }{{routePolicyType, routePolicyList, "routePolicies"}, {namedSetType, "compute.routers.listNamedSets", "namedSets"}} {
		op, ok := metadata.catalog.Operation(entry.method)
		if !ok {
			return nil, groupDenied("router_child_list_missing")
		}
		records, err := c.nativeList(ctx, op, map[string]any{"project": c.project, "region": parts[3], "router": parts[5]}, "result")
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, record := range records {
			name := text(record["name"])
			if !routePolicySegment.MatchString(name) || seen[name] {
				return nil, groupDenied("router_child_list_invalid")
			}
			seen[name] = true
			id := parent.NativeID + "/" + entry.collection + "/" + name
			live, err := c.routerComponentRead(ctx, entry.kind, id)
			if err != nil {
				return nil, err
			}
			if fingerprint, present := record["fingerprint"]; present && fingerprint != live["fingerprint"] {
				return nil, groupDenied("router_child_changed")
			}
			result = append(result, serviceChild{kind: entry.kind, id: id, data: live, direct: true})
		}
	}
	slices.SortFunc(result, func(a, b serviceChild) int { return strings.Compare(a.id, b.id) })
	return result, nil
}

func routerChildrenReview(children []serviceChild) string {
	values := map[string]string{}
	for _, child := range children {
		a := action{}
		a.kind, _ = findType(child.kind)
		values[child.id] = a.routerComponentConfiguration(child.data)
	}
	return firewallDigest(values)
}

// Bind the complete membership and native configuration to the scanned Router.
func (c *client) routerChildren(ctx context.Context, parent asset.Identity, saved map[string]any) ([]serviceChild, error) {
	kind, _ := findType(routerType)
	endpoint, err := c.resourceURL(kind, parent.NativeID)
	if err != nil {
		return nil, err
	}
	read := func() (map[string]any, error) {
		live, err := c.request(ctx, "GET", endpoint, nil)
		if err != nil {
			return nil, err
		}
		if err := c.routerData(parent.NativeID, text(saved["id"]), live); err != nil {
			return nil, err
		}
		if text(saved[routerReview]) == "" || routerConfiguration(live, false) != text(saved[routerReview]) {
			return nil, groupDenied("router_configuration_changed")
		}
		return live, nil
	}
	live, err := read()
	if err != nil {
		return nil, err
	}
	children, err := c.routerLiveChildren(ctx, parent, live)
	if err != nil {
		return nil, err
	}
	again, err := c.routerLiveChildren(ctx, parent, live)
	if err != nil {
		return nil, err
	}
	if routerChildrenReview(children) != routerChildrenReview(again) {
		return nil, groupDenied("router_children_changed")
	}
	if _, err := read(); err != nil {
		return nil, err
	}
	return children, nil
}

func routerSameChild(parent asset.Asset, target asset.Asset, child serviceChild) error {
	a := action{}
	a.kind, _ = findType(child.kind)
	if text(target.Normalized[a.routerComponentIncarnationKey()]) != text(parent.Normalized["id"]) || a.routerComponentConfiguration(target.Normalized) != a.routerComponentConfiguration(child.data) {
		return groupDenied("router_child_review_changed")
	}
	if child.kind == routePolicyType {
		peers, err := routePolicyBGPPeers(parent.Normalized)
		if err != nil {
			return err
		}
		if firewallDigest(target.Normalized[routePolicyPeers]) != firewallDigest(peers) {
			return groupDenied("router_child_peer_review_changed")
		}
	}
	return nil
}

func (c *client) routerUseReferences(parent asset.Asset) []graph.UnresolvedReference {
	var result []graph.UnresolvedReference
	for _, value := range array(parent.Normalized["interfaces"]) {
		for field, kind := range map[string]string{"linkedVpnTunnel": "compute.googleapis.com/VpnTunnel", "linkedInterconnectAttachment": "compute.googleapis.com/InterconnectAttachment"} {
			if id := text(object(value)[field]); id != "" {
				result = append(result, graph.UnresolvedReference{Provider: parent.Identity.Provider, ConnectionID: parent.Identity.ConnectionID, NativeType: kind, NativeID: c.canonicalName(id), ControllerID: parent.ID, Relationship: graph.RelationshipAttachedTo, BlocksCleanup: true, Evidence: map[string]any{"reason": "router_in_use", "interface": object(value)["name"]}})
			}
		}
	}
	return result
}
