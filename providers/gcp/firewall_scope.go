package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/provider/catalog"
)

type firewallContainer struct {
	Name, Parent, Created string
}

func firewallNumericID(value string) bool {
	id, err := strconv.ParseUint(value, 10, 64)
	return err == nil && id != 0 && strconv.FormatUint(id, 10) == value
}

func firewallContainerName(value string) bool {
	p := strings.Split(value, "/")
	return len(p) == 2 && (p[0] == "organizations" || p[0] == "folders") && firewallNumericID(p[1])
}

func firewallContainerValue(name string, data map[string]any) (firewallContainer, error) {
	value := firewallContainer{Name: text(data["name"]), Parent: text(data["parent"]), Created: text(data["createTime"])}
	if !firewallContainerName(name) || value.Name != name || data["state"] != "ACTIVE" {
		return value, groupDenied("firewall_container_identity_or_state_invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, value.Created); err != nil {
		return value, groupDenied("firewall_container_creation_missing")
	}
	if strings.HasPrefix(name, "folders/") && !firewallContainerName(value.Parent) || strings.HasPrefix(name, "organizations/") && value.Parent != "" {
		return value, groupDenied("firewall_container_parent_invalid")
	}
	return value, nil
}

func (c *client) firewallContainer(ctx context.Context, name string) (firewallContainer, error) {
	if !firewallContainerName(name) {
		return firewallContainer{}, groupDenied("firewall_container_name_invalid")
	}
	data, err := c.request(ctx, "GET", "https://cloudresourcemanager.googleapis.com/v3/"+name, nil)
	if err != nil {
		return firewallContainer{}, err
	}
	return firewallContainerValue(name, data)
}

func firewallDigest(value any) string {
	encoded, _ := json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}

// The configured container is an explicit management boundary. Project ancestry
// never grants access to a policy, and folder moves invalidate the reviewed chain.
func (c *client) firewallContainerChain(ctx context.Context, name string) (string, error) {
	if !firewallContainerName(c.firewallParent) {
		return "", groupDenied("firewall_scope_not_configured")
	}
	seen := map[string]bool{}
	var chain []firewallContainer
	for len(chain) < 64 && name != "" && !seen[name] {
		seen[name] = true
		value, err := c.firewallContainer(ctx, name)
		if err != nil {
			return "", err
		}
		chain = append(chain, value)
		if name == c.firewallParent {
			return firewallDigest(chain), nil
		}
		name = value.Parent
	}
	return "", groupDenied("firewall_container_outside_scope")
}

// Folders.list enumerates direct children, so walk the tree explicitly. Every
// list row is reconciled with GET; the caller repeats this walk after policies.
func (c *client) firewallContainers(ctx context.Context) ([]firewallContainer, error) {
	root, err := c.firewallContainer(ctx, c.firewallParent)
	if err != nil {
		return nil, err
	}
	result := []firewallContainer{root}
	seen := map[string]bool{root.Name: true}
	depths := map[string]int{root.Name: 0}
	for index := 0; index < len(result); index++ {
		parent := result[index]
		rows, err := c.firewallList(ctx, "cloudresourcemanager.folders.list", map[string]any{"parent": parent.Name, "pageSize": 1000}, "folders")
		if err != nil {
			return nil, err
		}
		for _, row := range rows {
			value, err := firewallContainerValue(text(row["name"]), row)
			if err != nil || value.Parent != parent.Name || seen[value.Name] || depths[parent.Name] >= 63 {
				return nil, groupDenied("firewall_folder_tree_invalid")
			}
			live, err := c.firewallContainer(ctx, value.Name)
			if err != nil {
				return nil, err
			}
			if value != live {
				return nil, groupDenied("firewall_folder_changed")
			}
			seen[value.Name], depths[value.Name] = true, depths[parent.Name]+1
			result = append(result, value)
		}
	}
	slices.SortFunc(result, func(a, b firewallContainer) int { return strings.Compare(a.Name, b.Name) })
	return result, nil
}

func (c *client) firewallList(ctx context.Context, operation string, parameters map[string]any, field string) ([]map[string]any, error) {
	metadata, err := providerData()
	if err != nil {
		return nil, err
	}
	op, found := metadata.catalog.Operation(operation)
	if !found || op.Call.Method != "GET" {
		return nil, groupDenied("firewall_list_method_missing")
	}
	parameters = cloneParameters(parameters)
	var rows []map[string]any
	seen := map[string]bool{}
	for {
		bound, err := catalog.BindREST(op, parameters)
		if err != nil {
			return nil, err
		}
		response, err := c.requestResult(ctx, bound.Method, bound.URL, nil, bound.Body)
		if err != nil {
			return nil, err
		}
		if err := checkListCompleteness(response.Data); err != nil {
			return nil, err
		}
		rootField := strings.Split(field, ".")[0]
		for _, other := range []string{"items", "folders", "associations", "firewallPolicies"} {
			if _, present := response.Data[other]; present && other != rootField {
				return nil, groupDenied("firewall_list_collection_invalid")
			}
		}
		values, present := response.Data[rootField]
		if field == "items.*.firewallPolicies" {
			groups, ok := values.(map[string]any)
			if present && !ok {
				return nil, groupDenied("firewall_aggregate_invalid")
			}
			for scope, raw := range groups {
				parts := strings.Split(scope, "/")
				if scope != "global" && (len(parts) != 2 || parts[0] != "regions" || !segmentPattern.MatchString(parts[1]) || parts[1] == "." || parts[1] == "..") {
					return nil, groupDenied("firewall_aggregate_scope_invalid")
				}
				group, ok := raw.(map[string]any)
				if !ok {
					return nil, groupDenied("firewall_aggregate_group_invalid")
				}
				if values, present := group["firewallPolicies"]; present {
					if _, ok := values.([]any); !ok {
						return nil, groupDenied("firewall_aggregate_list_invalid")
					}
				}
				for _, other := range []string{"items", "associations", "folders"} {
					if _, present := group[other]; present {
						return nil, groupDenied("firewall_aggregate_collection_invalid")
					}
				}
			}
			records, err := productRecords(response.Data, field)
			if err != nil {
				return nil, err
			}
			for _, record := range records {
				record.Data["_firewall_list_scope"] = record.Location
				rows = append(rows, record.Data)
			}
		} else {
			list, ok := values.([]any)
			if present && !ok {
				return nil, groupDenied("firewall_list_array_invalid")
			}
			for _, value := range list {
				row, ok := value.(map[string]any)
				if !ok {
					return nil, groupDenied("firewall_list_item_invalid")
				}
				rows = append(rows, row)
			}
		}
		token := ""
		if value, present := response.Data["nextPageToken"]; present {
			var ok bool
			token, ok = value.(string)
			if !ok {
				return nil, groupDenied("firewall_list_token_invalid")
			}
		}
		if token == "" {
			return rows, nil
		}
		if operation == "compute.firewallPolicies.listAssociations" || seen[token] {
			return nil, groupDenied("firewall_list_pagination_invalid")
		}
		seen[token], parameters["pageToken"] = true, token
	}
}
