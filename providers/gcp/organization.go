package gcp

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

const organizationType = "cloudresourcemanager.googleapis.com/Organization"
const organizationInventorySource = "organization-ancestry"
const resourceManagerHost = "cloudresourcemanager.googleapis.com"

type organizationAncestry struct {
	Project      map[string]any
	Folders      []firewallContainer
	Organization map[string]any
}

// Follow native parent references from the selected project. This is read-only
// ancestry discovery, not authority to mutate the organization or its children.
func (c *client) organizationAncestry(ctx context.Context) (organizationAncestry, error) {
	var result organizationAncestry
	project, err := c.request(ctx, "GET", "https://"+resourceManagerHost+"/v3/projects/"+c.project, nil)
	if err != nil {
		return result, err
	}
	if project["name"] != "projects/"+c.number || project["projectId"] != c.project || project["state"] != "ACTIVE" {
		return result, groupDenied("organization_project_identity_changed")
	}
	parent := ""
	if value, present := project["parent"]; present {
		var ok bool
		parent, ok = value.(string)
		if !ok || parent != "" && !firewallContainerName(parent) {
			return result, groupDenied("organization_project_parent_invalid")
		}
	}
	// Project labels and display names do not change its ancestry. Preserve the
	// immutable creation identity when exposed, and require it for owned projects.
	created := text(project["createTime"])
	if parent != "" || created != "" {
		if _, err := time.Parse(time.RFC3339Nano, created); err != nil {
			return result, groupDenied("organization_project_creation_missing")
		}
	}
	result.Project = map[string]any{"name": project["name"], "projectId": project["projectId"], "createTime": created, "parent": parent}
	seen := map[string]bool{}
	for parent != "" {
		if seen[parent] || len(seen) >= 64 {
			return result, groupDenied("organization_ancestry_cycle_or_depth")
		}
		seen[parent] = true
		if strings.HasPrefix(parent, "folders/") {
			folder, err := c.firewallContainer(ctx, parent)
			if err != nil {
				return result, err
			}
			result.Folders = append(result.Folders, folder)
			parent = folder.Parent
			continue
		}
		data, err := c.request(ctx, "GET", "https://"+resourceManagerHost+"/v3/"+parent, nil)
		if err != nil {
			return result, err
		}
		if err := organizationIdentity(parent, data); err != nil {
			return result, err
		}
		result.Organization = data
		break
	}
	return result, nil
}

func organizationIdentity(name string, data map[string]any) error {
	if !strings.HasPrefix(name, "organizations/") || !firewallContainerName(name) || data["name"] != name || !slices.Contains([]string{"ACTIVE", "DELETE_REQUESTED"}, text(data["state"])) {
		return groupDenied("organization_identity_or_state_invalid")
	}
	if _, err := time.Parse(time.RFC3339Nano, text(data["createTime"])); err != nil {
		return groupDenied("organization_creation_missing")
	}
	for _, field := range []string{"displayName", "directoryCustomerId", "etag"} {
		if value, present := data[field]; present {
			if _, ok := value.(string); !ok {
				return groupDenied("organization_field_invalid")
			}
		}
	}
	for _, field := range []string{"updateTime", "deleteTime"} {
		if value, present := data[field]; present {
			if _, err := time.Parse(time.RFC3339Nano, text(value)); err != nil {
				return groupDenied("organization_time_invalid")
			}
		}
	}
	return nil
}

func (r *Runtime) listOrganization(ctx context.Context, c *client, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{}, Complete: true}
	if request.Source != organizationInventorySource || request.ResourceKind == nil || request.ResourceKind.NativeType != organizationType || request.Cursor != "" || request.NetworkTarget != nil || request.Scope.Kind != asset.ScopeProject && request.Scope.Kind != asset.ScopeGlobal {
		return batch, groupDenied("organization_inventory_scope_invalid")
	}
	if request.Scope.Kind == asset.ScopeGlobal && !slices.Contains([]string{"global", c.project + "/global", c.number + "/global"}, request.Scope.NativeID) {
		return batch, groupDenied("organization_inventory_project_changed")
	}
	first, err := c.organizationAncestry(ctx)
	if err != nil {
		return batch, err
	}
	second, err := c.organizationAncestry(ctx)
	if err != nil {
		return batch, err
	}
	if !reflect.DeepEqual(first, second) {
		return batch, groupDenied("organization_ancestry_changed")
	}
	if first.Organization == nil {
		return batch, nil
	}
	id := "//" + resourceManagerHost + "/" + text(first.Organization["name"])
	item, err := r.inventoryItem(c, map[string]any{"name": id, "assetType": organizationType, "resource": map[string]any{"data": first.Organization, "location": "global"}})
	if err != nil {
		return batch, err
	}
	delete(item.Normalized, "project_id")
	delete(item.Normalized, "project_number")
	item.Normalized["_inventory_source"] = organizationInventorySource
	item.Normalized["_organization_project"] = first.Project["name"]
	ancestors := []string{}
	for _, folder := range first.Folders {
		ancestors = append(ancestors, folder.Name)
	}
	item.Normalized["_organization_folders"] = ancestors
	batch.Items = append(batch.Items, item)
	return batch, nil
}

func organizationOperation(metadata providerMetadata, id, method string) (catalog.Operation, map[string]any, error) {
	name := strings.TrimPrefix(id, "//"+resourceManagerHost+"/")
	if method != "GET" || name == id || !strings.HasPrefix(name, "organizations/") || !firewallContainerName(name) {
		return catalog.Operation{}, nil, groupDenied("organization_read_identity_invalid")
	}
	op, ok := metadata.catalog.Operation("cloudresourcemanager.organizations.get")
	if !ok {
		return catalog.Operation{}, nil, groupDenied("organization_read_method_missing")
	}
	return op, map[string]any{"name": name}, nil
}
