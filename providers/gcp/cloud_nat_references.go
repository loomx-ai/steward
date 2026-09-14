package gcp

import (
	"context"
	"net/url"
	"slices"
	"strings"

	"cel.dev/cel-go/common"
	"cel.dev/cel-go/common/ast"
	"cel.dev/cel-go/parser"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const cloudNatHubType = "networkconnectivity.googleapis.com/Hub"
const cloudNatSpokeType = "networkconnectivity.googleapis.com/Spoke"

// Hub projects may differ from the source VPC project. Validate the native name
// without resourceURL's connection-project restriction; missing targets remain
// unresolved graph references, never local aliases for another project's Hub.
func (c *client) cloudNatHubName(value string) (string, error) {
	const host = "networkconnectivity.googleapis.com"
	if strings.HasPrefix(value, "projects/") {
		value = "//" + host + "/" + value
	}
	u, err := url.Parse(value)
	if err != nil || u.Host != host || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.RawPath != "" || (u.Scheme != "" && u.Scheme != "https") {
		return "", groupDenied("cloud_nat_hub_reference_invalid")
	}
	path := u.Path
	if u.Scheme == "https" {
		if !strings.HasPrefix(path, "/v1/projects/") {
			return "", groupDenied("cloud_nat_hub_reference_invalid")
		}
		path = strings.TrimPrefix(path, "/v1")
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(parts) != 6 || parts[0] != "projects" || !projectPattern.MatchString(parts[1]) || parts[2] != "locations" || parts[3] != "global" || parts[4] != "hubs" || !segmentPattern.MatchString(parts[5]) || parts[5] == "." || parts[5] == ".." {
		return "", groupDenied("cloud_nat_hub_reference_invalid")
	}
	return c.canonicalName("//" + host + path), nil
}

func cloudNatHubSelector(node ast.Expr) bool {
	if node.Kind() == ast.SelectKind {
		sel := node.AsSelect()
		return sel.FieldName() == "hub" && sel.Operand().Kind() == ast.IdentKind && strings.TrimPrefix(sel.Operand().AsIdent(), ".") == "nexthop"
	}
	if node.Kind() == ast.CallKind {
		call := node.AsCall()
		args := call.Args()
		return call.FunctionName() == "_[_]" && len(args) == 2 && args[0].Kind() == ast.IdentKind && strings.TrimPrefix(args[0].AsIdent(), ".") == "nexthop" && args[1].Kind() == ast.LiteralKind && args[1].AsLiteral().Value() == "hub"
	}
	return false
}

// Inspect syntax, never evaluate packet predicates. An unbound selector needs
// the source VPC's native NCC membership to identify its potential Hub targets.
func (c *client) cloudNatHubReferences(data map[string]any) ([]string, bool, error) {
	p, err := parser.NewParser()
	if err != nil {
		return nil, false, err
	}
	var hubs []string
	membership := false
	for _, raw := range array(data["rules"]) {
		expression := text(object(raw)["match"])
		if strings.TrimSpace(expression) == "" {
			continue
		}
		parsed, issues := p.Parse(common.NewTextSource(expression))
		if parsed == nil || len(issues.GetErrors()) != 0 || ast.ExceedsDepth(parsed, 250) {
			return nil, false, groupDenied("cloud_nat_cel_invalid")
		}
		selectors, bound := map[int64]bool{}, map[int64]bool{}
		var invalid error
		ast.PostOrderVisit(parsed.Expr(), ast.NewExprVisitor(func(node ast.Expr) {
			if cloudNatHubSelector(node) {
				selectors[node.ID()] = true
			}
			if node.Kind() != ast.CallKind {
				return
			}
			call := node.AsCall()
			args := call.Args()
			if call.FunctionName() != "_==_" || len(args) != 2 {
				return
			}
			for i := range 2 {
				if !cloudNatHubSelector(args[i]) || args[1-i].Kind() != ast.LiteralKind {
					continue
				}
				value, ok := args[1-i].AsLiteral().Value().(string)
				if !ok {
					continue
				}
				hub, err := c.cloudNatHubName(value)
				if err != nil {
					invalid = err
					continue
				}
				hubs = append(hubs, hub)
				bound[args[i].ID()] = true
			}
		}))
		if invalid != nil {
			return nil, false, invalid
		}
		for id := range selectors {
			if !bound[id] {
				membership = true
			}
		}
	}
	slices.Sort(hubs)
	return slices.Compact(hubs), membership, nil
}

func (c *client) cloudNatVpcHubs(ctx context.Context, network string) ([]string, string, error) {
	metadata, _ := providerData()
	operation, ok := metadata.catalog.Operation("networkconnectivity.projects.locations.spokes.list")
	if !ok {
		return nil, "", groupDenied("cloud_nat_spoke_operation_missing")
	}
	records, err := c.nativeList(ctx, operation, map[string]any{"parent": "projects/" + c.project + "/locations/-"}, "spokes")
	if err != nil {
		return nil, "", contracts.DependencyReadError(err)
	}
	var hubs []string
	proofs := map[string]any{}
	seen := map[string]bool{}
	kind, _ := findType(cloudNatSpokeType)
	for _, listed := range records {
		name := text(listed["name"])
		id := c.canonicalName("//networkconnectivity.googleapis.com/" + name)
		endpoint, err := c.resourceURL(kind, id)
		if err != nil || seen[id] {
			return nil, "", groupDenied("cloud_nat_spoke_identity_invalid")
		}
		seen[id] = true
		linked, present := listed["linkedVpcNetwork"]
		if !present {
			continue
		}
		vpc := object(linked)
		if vpc == nil || text(vpc["uri"]) == "" {
			return nil, "", groupDenied("cloud_nat_spoke_network_invalid")
		}
		source, err := c.computeID(text(vpc["uri"]), "compute.googleapis.com/Network")
		if err != nil {
			return nil, "", err
		}
		if source != network {
			continue
		}
		hub, err := c.cloudNatHubName(text(listed["hub"]))
		if err != nil {
			return nil, "", err
		}
		live, err := c.request(ctx, "GET", endpoint, nil)
		if err != nil {
			return nil, "", contracts.DependencyReadError(err)
		}
		if err := checkListCompleteness(live); err != nil {
			return nil, "", err
		}
		if live["name"] != name || firewallDigest(live) != firewallDigest(listed) {
			return nil, "", groupDenied("cloud_nat_spoke_changed")
		}
		proofs[id] = live
		hubs = append(hubs, hub)
	}
	slices.Sort(hubs)
	return slices.Compact(hubs), firewallDigest(proofs), nil
}

func (c *client) enrichCloudNatHubs(ctx context.Context, item *contracts.InventoryItem, data, parent map[string]any, parentID string) error {
	hubs, membership, err := c.cloudNatHubReferences(data)
	if err != nil || !membership {
		return err
	}
	network := text(item.Normalized["vpc_id"])
	if network == "" {
		return groupDenied("cloud_nat_source_network_missing")
	}
	first, proof, err := c.cloudNatVpcHubs(ctx, network)
	if err != nil {
		return err
	}
	_, second, err := c.cloudNatVpcHubs(ctx, network)
	if err != nil {
		return err
	}
	if proof != second {
		return groupDenied("cloud_nat_spoke_membership_changed")
	}
	kind, _ := findType(routerType)
	endpoint, err := c.resourceURL(kind, parentID)
	if err != nil {
		return err
	}
	live, err := c.request(ctx, "GET", endpoint, nil)
	if err != nil {
		return contracts.DependencyReadError(err)
	}
	if err := c.routerData(parentID, text(parent["id"]), live); err != nil {
		return err
	}
	if routerConfiguration(parent, false) != routerConfiguration(live, false) {
		return groupDenied("cloud_nat_router_changed")
	}
	hubs = append(hubs, first...)
	slices.Sort(hubs)
	hubs = slices.Compact(hubs)
	item.Normalized["_cloud_nat_hub_membership"] = proof
	item.Normalized[referenceKey(cloudNatHubType)] = hubs
	item.NetworkReferences = append(item.NetworkReferences, hubs...)
	slices.Sort(item.NetworkReferences)
	item.NetworkReferences = slices.Compact(item.NetworkReferences)
	return nil
}
