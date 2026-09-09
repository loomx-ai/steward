package gcp

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

const (
	firewallPolicyType             = "compute.googleapis.com/FirewallPolicy"
	networkFirewallPolicyType      = "compute.googleapis.com/NetworkFirewallPolicy"
	firewallAssociationType        = "compute.googleapis.com/FirewallPolicyAssociation"
	networkFirewallAssociationType = "compute.googleapis.com/NetworkFirewallPolicyAssociation"
	firewallBaseProof              = "_firewall_base"
	firewallProof                  = "_firewall_configuration"
	firewallOwnerName              = "_firewall_owner"
	firewallOwnerProof             = "_firewall_owner_chain"
	firewallScope                  = "_firewall_scope"
	firewallContainingPolicy       = "_firewall_parent"
	firewallParentFullProof        = "_firewall_parent_configuration"
	firewallParentProof            = "_firewall_parent_base"
	firewallMembersKey             = "_firewall_members"
	firewallTargetProof            = "_firewall_target"
	firewallSnapshotKey            = "_firewall_snapshot"
)

type firewallMember struct {
	ID, Proof, TargetProof string
}

func isFirewallPolicy(kind string) bool {
	return kind == firewallPolicyType || kind == networkFirewallPolicyType
}

func isFirewall(kind string) bool {
	return isFirewallPolicy(kind) || kind == firewallAssociationType || kind == networkFirewallAssociationType
}

func firewallParentType(kind string) string {
	if kind == firewallPolicyType || kind == firewallAssociationType {
		return firewallPolicyType
	}
	return networkFirewallPolicyType
}

func firewallChildType(kind string) string {
	if kind == firewallPolicyType {
		return firewallAssociationType
	}
	return networkFirewallAssociationType
}

func firewallAssociationName(name string) bool {
	return name != "" && name != "." && name != ".." && len(name) <= 1024 && utf8.ValidString(name) && strings.IndexFunc(name, unicode.IsControl) < 0
}

// Associations have native names and GET/removeAssociation methods, but no REST
// collection path. Escape the name into a composite identity; send it as a query
// parameter at the actual method, including Google's default names with spaces.
func firewallAssociationID(parent, name string) string {
	return parent + "/associations/" + url.PathEscape(name)
}

func (c *client) firewallIdentityParts(kind, id string) (string, string, error) {
	if !isFirewall(kind) || !strings.HasPrefix(id, "//compute.googleapis.com/") {
		return "", "", groupDenied("firewall_identity_invalid")
	}
	parent, association := id, ""
	if !isFirewallPolicy(kind) {
		index := strings.LastIndex(id, "/associations/")
		if index < 0 {
			return "", "", groupDenied("firewall_association_identity_invalid")
		}
		encoded := id[index+len("/associations/"):]
		var err error
		association, err = url.PathUnescape(encoded)
		if err != nil || !firewallAssociationName(association) || url.PathEscape(association) != encoded {
			return "", "", groupDenied("firewall_association_name_invalid")
		}
		parent = id[:index]
	}
	name := strings.TrimPrefix(parent, "//compute.googleapis.com/")
	p := strings.Split(name, "/")
	if firewallParentType(kind) == firewallPolicyType {
		if !firewallContainerName(c.firewallParent) || len(p) != 4 || p[0] != "locations" || p[1] != "global" || p[2] != "firewallPolicies" || !firewallNumericID(p[3]) {
			return "", "", groupDenied("firewall_hierarchy_identity_invalid")
		}
	} else {
		if len(p) != 5 && len(p) != 6 || p[0] != "projects" || p[1] != c.project && p[1] != c.number || p[len(p)-2] != "firewallPolicies" {
			return "", "", groupDenied("firewall_network_identity_invalid")
		}
		if len(p) == 5 && p[2] != "global" || len(p) == 6 && (p[2] != "regions" || !segmentPattern.MatchString(p[3]) || p[3] == "." || p[3] == "..") {
			return "", "", groupDenied("firewall_network_scope_invalid")
		}
		if !segmentPattern.MatchString(last(name)) || last(name) == "." || last(name) == ".." {
			return "", "", groupDenied("firewall_network_name_invalid")
		}
	}
	return parent, association, nil
}

func (c *client) firewallResourceOperation(kind, id, method string) (catalog.Operation, map[string]any, error) {
	parent, association, err := c.firewallIdentityParts(kind, id)
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	p := strings.Split(strings.TrimPrefix(parent, "//compute.googleapis.com/"), "/")
	product := "compute.firewallPolicies."
	parameters := map[string]any{"firewallPolicy": last(parent)}
	if firewallParentType(kind) == networkFirewallPolicyType {
		parameters["project"] = p[1]
		product = "compute.networkFirewallPolicies."
		if len(p) == 6 {
			product, parameters["region"] = "compute.regionNetworkFirewallPolicies.", p[3]
		}
	}
	action := "get"
	if method == "DELETE" {
		action = "delete"
	} else if method != "GET" {
		return catalog.Operation{}, nil, groupDenied("firewall_method_invalid")
	}
	if association != "" {
		parameters["name"] = association
		action = "getAssociation"
		if method == "DELETE" {
			action = "removeAssociation"
		}
	}
	metadata, err := providerData()
	if err != nil {
		return catalog.Operation{}, nil, err
	}
	op, found := metadata.catalog.Operation(product + action)
	if !found {
		return catalog.Operation{}, nil, groupDenied("firewall_method_missing")
	}
	return op, parameters, nil
}

func firewallConfiguration(data map[string]any, base bool) string {
	value := cloneParameters(data)
	for key := range value {
		if strings.HasPrefix(key, "_") || strings.HasPrefix(key, "refs_") || slices.Contains([]string{"project_id", "project_number", "vpc_id", "subnet_ids", "vswitch_id", "cleanup_protected", "cleanup_protection_reason"}, key) || base && (key == "associations" || key == "fingerprint") {
			delete(value, key)
		}
	}
	// These arrays are native sets: rule evaluation uses priority, not position.
	for _, field := range []string{"rules", "packetMirroringRules", "associations"} {
		if list, ok := value[field].([]any); ok {
			copy := slices.Clone(list)
			slices.SortFunc(copy, func(a, b any) int { return strings.Compare(firewallDigest(a), firewallDigest(b)) })
			value[field] = copy
		}
	}
	return firewallDigest(value)
}

func firewallManifestDigest(data map[string]any) string {
	bound := map[string]any{}
	for _, key := range []string{firewallBaseProof, firewallProof, firewallContainingPolicy, firewallParentProof, firewallParentFullProof, firewallMembersKey, firewallOwnerName, firewallOwnerProof, firewallScope, firewallTargetProof} {
		bound[key] = text(data[key])
	}
	return firewallDigest(bound)
}

func (c *client) firewallPolicyIdentity(kind, id string, data map[string]any) error {
	parent, _, err := c.firewallIdentityParts(kind, id)
	if err != nil || !isFirewallPolicy(kind) || parent != id || c.canonicalName(text(data["selfLink"])) != id || text(data["name"]) != last(id) || !firewallNumericID(text(data["id"])) {
		return groupDenied("firewall_policy_identity_changed")
	}
	if kind == firewallPolicyType && (text(data["name"]) != text(data["id"]) || !firewallContainerName(text(data["parent"]))) {
		return groupDenied("firewall_policy_owner_invalid")
	}
	if kind == networkFirewallPolicyType {
		if parent, present := data["parent"]; present && parent != "" {
			return groupDenied("firewall_network_has_hierarchy_parent")
		}
		p := strings.Split(id, "/")
		if value, present := data["region"]; present && c.canonicalName(text(value)) != strings.Join(p[:len(p)-2], "/") {
			return groupDenied("firewall_region_changed")
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, text(data["creationTimestamp"])); err != nil {
		return groupDenied("firewall_policy_creation_missing")
	}
	if fingerprint, ok := data["fingerprint"].(string); !ok || fingerprint == "" {
		return groupDenied("firewall_policy_fingerprint_missing")
	}
	if value, present := data["kind"]; present && value != "compute#firewallPolicy" {
		return groupDenied("firewall_policy_type_invalid")
	}
	if value, present := data["policyType"]; present && !slices.Contains([]string{"VPC_POLICY", "RDMA_ROCE_POLICY", "ULL_POLICY"}, text(value)) {
		return groupDenied("firewall_policy_type_unknown")
	}
	for _, field := range []string{"rules", "packetMirroringRules", "associations"} {
		if value, present := data[field]; present {
			rows, ok := value.([]any)
			if !ok {
				return groupDenied("firewall_policy_list_invalid")
			}
			for _, row := range rows {
				if _, ok := row.(map[string]any); !ok {
					return groupDenied("firewall_policy_member_invalid")
				}
			}
		}
	}
	return nil
}

func (c *client) firewallReadPolicy(ctx context.Context, kind, id string) (map[string]any, error) {
	op, parameters, err := c.firewallResourceOperation(kind, id, "GET")
	if err != nil {
		return nil, err
	}
	bound, err := catalog.BindREST(op, parameters)
	if err != nil {
		return nil, err
	}
	data, err := c.request(ctx, bound.Method, bound.URL, nil)
	if err != nil {
		return nil, err
	}
	if err := c.firewallPolicyIdentity(kind, id, data); err != nil {
		return nil, err
	}
	data[firewallScope] = "projects/" + c.project
	if kind == firewallPolicyType {
		chain, err := c.firewallContainerChain(ctx, text(data["parent"]))
		if err != nil {
			return nil, err
		}
		data[firewallScope], data[firewallOwnerProof], data[firewallOwnerName] = c.firewallParent, chain, data["parent"]
	}
	data[firewallBaseProof], data[firewallProof] = firewallConfiguration(data, true), firewallConfiguration(data, false)
	return data, nil
}

func (c *client) firewallAssociationValue(kind, parent string, policy, data map[string]any) (string, error) {
	name := text(data["name"])
	if !firewallAssociationName(name) || data["firewallPolicyId"] != policy["id"] {
		return "", groupDenied("firewall_association_identity_changed")
	}
	target := text(data["attachmentTarget"])
	if firewallParentType(kind) == firewallPolicyType {
		if !firewallContainerName(target) {
			return "", groupDenied("firewall_association_target_invalid")
		}
	} else {
		target = c.canonicalName(target)
		network, _ := findType("compute.googleapis.com/Network")
		if _, err := c.resourceURL(network, target); err != nil {
			return "", groupDenied("firewall_association_network_invalid")
		}
	}
	return firewallAssociationID(parent, name), nil
}

func (c *client) firewallTarget(ctx context.Context, kind string, data map[string]any) (string, error) {
	target := text(data["attachmentTarget"])
	if firewallParentType(kind) == firewallPolicyType {
		return c.firewallContainerChain(ctx, target)
	}
	target = c.canonicalName(target)
	network, _ := findType("compute.googleapis.com/Network")
	endpoint, err := c.resourceURL(network, target)
	if err != nil {
		return "", err
	}
	live, err := c.request(ctx, "GET", endpoint, nil)
	if isNotFound(err) {
		return "absent", nil
	}
	if err != nil {
		return "", err
	}
	if !firewallNumericID(text(live["id"])) || text(live["name"]) != last(target) || c.canonicalName(text(live["selfLink"])) != target {
		return "", groupDenied("firewall_target_network_changed")
	}
	return firewallDigest(map[string]any{"id": live["id"], "creationTimestamp": live["creationTimestamp"], "name": target}), nil
}

func (c *client) firewallAssociationRows(kind, parent string, policy map[string]any) (map[string]map[string]any, error) {
	result := map[string]map[string]any{}
	targets := map[string]bool{}
	for _, value := range array(policy["associations"]) {
		row := object(value)
		id, err := c.firewallAssociationValue(kind, parent, policy, row)
		if err != nil {
			return nil, err
		}
		target := c.canonicalName(text(row["attachmentTarget"]))
		if result[id] != nil || targets[target] {
			return nil, groupDenied("firewall_association_duplicate")
		}
		result[id], targets[target] = row, true
	}
	return result, nil
}

func (c *client) firewallAssociationGET(ctx context.Context, kind, id string) (map[string]any, error) {
	op, parameters, err := c.firewallResourceOperation(kind, id, "GET")
	if err != nil {
		return nil, err
	}
	bound, err := catalog.BindREST(op, parameters)
	if err != nil {
		return nil, err
	}
	return c.request(ctx, bound.Method, bound.URL, nil)
}

// Read every association independently, bind its target incarnation, then read
// the policy again. Native removeAssociation has no fingerprint precondition.
func (c *client) firewallSnapshot(ctx context.Context, kind, id string, policy map[string]any) (map[string]map[string]any, error) {
	rows, err := c.firewallAssociationRows(firewallChildType(kind), id, policy)
	if err != nil {
		return nil, err
	}
	var members []firewallMember
	for childID, listed := range rows {
		live, err := c.firewallAssociationGET(ctx, firewallChildType(kind), childID)
		if err != nil {
			return nil, err
		}
		actual, err := c.firewallAssociationValue(firewallChildType(kind), id, policy, live)
		if err != nil || actual != childID || firewallConfiguration(listed, false) != firewallConfiguration(live, false) {
			return nil, groupDenied("firewall_association_list_changed")
		}
		targetProof, err := c.firewallTarget(ctx, firewallChildType(kind), live)
		if err != nil {
			return nil, err
		}
		live[firewallContainingPolicy], live[firewallParentProof], live[firewallParentFullProof] = id, policy[firewallBaseProof], policy[firewallProof]
		live[firewallScope], live[firewallOwnerProof], live[firewallOwnerName] = policy[firewallScope], policy[firewallOwnerProof], policy[firewallOwnerName]
		live[firewallTargetProof] = targetProof
		live[firewallProof] = firewallConfiguration(live, false)
		rows[childID] = live
		members = append(members, firewallMember{ID: childID, Proof: text(live[firewallProof]), TargetProof: targetProof})
	}
	again, err := c.firewallReadPolicy(ctx, kind, id)
	if err != nil {
		return nil, err
	}
	if policy[firewallProof] != again[firewallProof] || policy[firewallOwnerProof] != again[firewallOwnerProof] {
		return nil, groupDenied("firewall_policy_changed_during_snapshot")
	}
	slices.SortFunc(members, func(a, b firewallMember) int { return strings.Compare(a.ID, b.ID) })
	encoded, _ := json.Marshal(members)
	policy[firewallMembersKey] = string(encoded)
	policy[firewallSnapshotKey] = firewallManifestDigest(policy)
	for _, child := range rows {
		child[firewallMembersKey] = string(encoded)
		child[firewallSnapshotKey] = firewallManifestDigest(child)
	}
	return rows, nil
}
