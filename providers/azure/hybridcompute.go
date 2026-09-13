package azure

import (
	"context"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strings"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const hybridComputeSource = "hybrid-compute"
const hybridComputeVersion = "2025-01-13"
const hybridMachineType = "Microsoft.HybridCompute/machines"
const hybridExtensionType = hybridMachineType + "/extensions"
const hybridCommandType = hybridMachineType + "/runCommands"
const hybridProfileType = hybridMachineType + "/licenseProfiles"
const hybridLicenseType = "Microsoft.HybridCompute/licenses"

func hybridComputeKind(value string) string {
	for _, kind := range []string{hybridMachineType, hybridExtensionType, hybridCommandType, hybridProfileType, hybridLicenseType} {
		if strings.EqualFold(value, kind) {
			return kind
		}
	}
	return ""
}

func hybridComputeParent(id, kind string) string {
	if kind == hybridMachineType || kind == hybridLicenseType {
		return ""
	}
	return id[:strings.LastIndex(id[:strings.LastIndex(id, "/")], "/")]
}

func (c *client) hybridComputeIdentity(wire, kind string) (string, error) {
	id, typ, err := parseID(wire)
	if err != nil || hybridComputeKind(typ) != kind || kind == "" || !strings.HasPrefix(id, c.root()+"/resourcegroups/") {
		return "", serviceDenied("invalid_hybrid_compute_identity")
	}
	return id, nil
}

func (c *client) hybridComputeRead(ctx context.Context, id, kind string) (response, error) {
	canonical, err := c.hybridComputeIdentity(id, kind)
	if err != nil || canonical != id {
		return response{}, serviceDenied("invalid_hybrid_compute_read_identity")
	}
	mapping, ok := findType(kind)
	if !ok {
		return response{}, serviceDenied("hybrid_compute_mapping_missing")
	}
	endpoint, err := c.resourceURL(mapping, id)
	if err != nil {
		return response{}, err
	}
	res, err := c.request(ctx, "GET", endpoint)
	if err != nil {
		return res, err
	}
	actual, identityErr := c.hybridComputeIdentity(text(res.data["id"]), kind)
	if res.status != 200 || operationLocation(res.header) != "" || identityErr != nil || actual != id || !strings.EqualFold(text(res.data["type"]), kind) || !strings.EqualFold(text(res.data["name"]), last(id)) || object(res.data["properties"]) == nil || text(res.data["location"]) == "" {
		return res, serviceDenied("invalid_hybrid_compute_native_response")
	}
	for _, key := range []string{"etag", "eTag", "managedBy", "kind"} {
		if value := res.data[key]; value != nil {
			if _, ok := value.(string); !ok {
				return res, serviceDenied("invalid_hybrid_compute_metadata_type")
			}
		}
	}
	if value := res.data["tags"]; value != nil {
		tags, ok := value.(map[string]any)
		if !ok {
			return res, serviceDenied("invalid_hybrid_compute_tags")
		}
		for _, value := range tags {
			if _, ok := value.(string); !ok {
				return res, serviceDenied("invalid_hybrid_compute_tag_value")
			}
		}
	}
	// Native examples contain null connection/OS fields. Preserve their absence
	// without inventing a Connected state; reject malformed present scalar fields.
	for _, key := range []string{"provisioningState", "status", "vmId", "agentVersion", "osType", "osName", "osVersion"} {
		if value := object(res.data["properties"])[key]; value != nil {
			if _, ok := value.(string); !ok {
				return res, serviceDenied("invalid_hybrid_compute_property_type")
			}
		}
	}
	return res, nil
}

func (c *client) hybridComputeIndex(ctx context.Context, kind, parent string) ([]any, error) {
	path := c.root() + "/providers/" + kind
	if parent != "" {
		if id, err := c.hybridComputeIdentity(parent, hybridMachineType); err != nil || id != parent || kind == hybridMachineType || kind == hybridLicenseType {
			return nil, serviceDenied("invalid_hybrid_compute_parent")
		}
		path = parent + "/" + last(kind)
	}
	rows, seen := []any{}, map[string]bool{}
	for next := apiURL(path, hybridComputeVersion); next != ""; {
		u, err := url.Parse(next)
		if err != nil || seen[next] {
			return nil, serviceDenied("invalid_hybrid_compute_cursor")
		}
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil || query.Get("api-version") != hybridComputeVersion {
			return nil, serviceDenied("hybrid_compute_version_changed")
		}
		for key, values := range query {
			if len(values) != 1 || values[0] == "" || !slices.Contains([]string{"api-version", "$skiptoken", "$skipToken", "skipToken", "continuationToken"}, key) {
				return nil, serviceDenied("filtered_hybrid_compute_index")
			}
		}
		seen[next] = true
		page, cursor, err := c.listPage(ctx, next, path)
		if err != nil {
			return nil, err
		}
		rows, next = append(rows, page...), cursor
	}
	return rows, nil
}

// Scripts, extension settings, output, agent proxy configuration, keys and
// future fields stay private. Only typed operational metadata is projected.
func hybridComputeSafeValue(value any) any {
	switch value := value.(type) {
	case []any:
		result := make([]any, len(value))
		for i, entry := range value {
			result[i] = hybridComputeSafeValue(entry)
		}
		return result
	case map[string]any:
		result := map[string]any{}
		for _, key := range []string{"id", "name", "type", "location", "kind", "managedBy", "request_id"} {
			if v, ok := value[key].(string); ok {
				result[key] = v
			}
		}
		switch v := value["status_code"].(type) {
		case int, float64, json.Number:
			result["status_code"] = v
		}
		if tags := object(value["tags"]); tags != nil {
			result["tags"] = map[string]any{}
			for key, v := range tags {
				if text, ok := v.(string); ok {
					object(result["tags"])[key] = text
				}
			}
		}
		if props := object(value["properties"]); props != nil {
			public := map[string]any{}
			for _, key := range []string{"provisioningState", "status", "vmId", "agentVersion", "osName", "osType", "osVersion", "osSku", "osEdition", "displayName", "lastStatusChange", "publisher", "type", "typeHandlerVersion", "licenseType"} {
				if v, ok := props[key].(string); ok {
					public[key] = v
				}
			}
			for _, key := range []string{"autoUpgradeMinorVersion", "enableAutomaticUpgrade", "asyncExecution"} {
				if v, ok := props[key].(bool); ok {
					public[key] = v
				}
			}
			for section, keys := range map[string][]string{
				"instanceView":   {"executionState", "startTime", "endTime"},
				"esuProfile":     {"assignedLicense", "esuEligibility", "serverType", "esuKeyState"},
				"productProfile": {"subscriptionStatus", "productType", "enrollmentDate", "disenrollmentDate", "billingStartDate", "billingEndDate"},
				"licenseDetails": {"state", "target", "edition", "type"},
			} {
				if values := object(props[section]); values != nil {
					public[section] = map[string]any{}
					for _, key := range keys {
						if v, ok := values[key].(string); ok {
							object(public[section])[key] = v
						}
					}
				}
			}
			if details := object(props["licenseDetails"]); details != nil {
				for _, key := range []string{"processors", "assignedLicenses"} {
					if value, err := batchInteger(details[key], 32); err == nil && value >= 0 {
						object(public["licenseDetails"])[key] = value
					}
				}
			}
			result["properties"] = public
		}
		for _, key := range []string{"body", "value"} {
			if v, ok := value[key]; ok {
				result[key] = hybridComputeSafeValue(v)
			}
		}
		return result
	default:
		return nil
	}
}

func hybridComputeReferences(id, kind string, raw map[string]any) (map[string][]string, error) {
	refs := map[string][]string{}
	if parent := hybridComputeParent(id, kind); parent != "" {
		refs[hybridMachineType] = []string{parent}
	}
	props := object(raw["properties"])
	values := []struct {
		value any
		kind  string
	}{
		{props["privateLinkScopeResourceId"], "Microsoft.HybridCompute/privateLinkScopes"},
		{props["parentClusterResourceId"], "Microsoft.AzureStackHCI/clusters"},
		{raw["managedBy"], ""},
	}
	if kind == hybridProfileType {
		values = append(values, struct {
			value any
			kind  string
		}{object(props["esuProfile"])["assignedLicense"], hybridLicenseType})
	}
	for _, reference := range values {
		value := reference.value
		if value == nil || value == "" {
			continue
		}
		target, typ, err := parseID(text(value))
		if err != nil || target == id || reference.kind != "" && !strings.EqualFold(typ, reference.kind) {
			return nil, serviceDenied("invalid_hybrid_compute_reference")
		}
		if mapping, ok := findType(typ); ok {
			typ = mapping.NativeType
		}
		refs[typ] = append(refs[typ], target)
	}
	for kind, ids := range refs {
		slices.Sort(ids)
		refs[kind] = slices.Compact(ids)
	}
	return refs, nil
}

func (c *client) hybridComputeRecordedReferences(value asset.Asset) (map[string][]string, error) {
	id, err := c.hybridComputeIdentity(value.Identity.NativeID, hybridComputeKind(value.Identity.NativeType))
	refs := map[string][]string{}
	for kind, ids := range object(value.Normalized["_hybrid_compute_references"]) {
		refs[kind] = stringValues(ids)
	}
	expected := c.privateConfiguration(map[string]any{"id": id, "connection": value.Identity.ConnectionID, "configuration": value.Normalized["_hybrid_compute_configuration"], "references": refs})
	if err != nil || id != value.Identity.NativeID || value.Identity.Provider != asset.ProviderAzure || value.Identity.ConnectionID == "" || value.Identity.Partition != "azure" || value.Normalized["_inventory_source"] != hybridComputeSource || text(value.Normalized["_hybrid_compute_configuration"]) == "" || value.Normalized["_hybrid_compute_reference_binding"] != expected {
		return nil, serviceDenied("invalid_hybrid_compute_recorded_references")
	}
	return maps.Clone(refs), nil
}
