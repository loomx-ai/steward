package azure

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
)

const apimServiceType = "Microsoft.ApiManagement/service"
const apimWorkspaceType = apimServiceType + "/workspaces"
const apimAPIType = apimServiceType + "/apis"
const apimIssueType = apimAPIType + "/issues"
const apimGatewayType = "Microsoft.ApiManagement/gateways"
const apimGatewayAlias = "Microsoft.ApiManagement/gateway"
const apimGatewayConnectionType = apimGatewayType + "/configConnections"
const apimVersion = "2024-05-01"

func isAPIMType(kind string) bool {
	return strings.EqualFold(kind, apimServiceType) || strings.HasPrefix(strings.ToLower(kind), strings.ToLower(apimServiceType)+"/") || isAPIMGateway(kind)
}
func isAPIMGateway(kind string) bool {
	return strings.EqualFold(kind, apimGatewayType) || strings.EqualFold(kind, apimGatewayConnectionType)
}
func isAPIMRoot(kind string) bool {
	return strings.EqualFold(kind, apimServiceType) || strings.EqualFold(kind, apimGatewayType)
}
func apimOwnedKinds(kind string) []string {
	metadata, err := providerData()
	if err != nil {
		return nil
	}
	var result []string
	for _, candidate := range metadata.kinds {
		if isAPIMType(candidate.NativeType) && strings.EqualFold(candidate.NativeType[:strings.LastIndex(candidate.NativeType, "/")], kind) {
			result = append(result, candidate.NativeType)
		}
	}
	slices.Sort(result)
	return result
}
func apimRootID(id string) string {
	id, kind, err := parseID(id)
	if strings.EqualFold(kind, apimGatewayAlias) {
		id, kind, err = parseID(responseID(apimGatewayType, id))
	}
	if err != nil || !isAPIMType(kind) {
		return ""
	}
	return strings.Join(strings.Split(id, "/")[:9], "/")
}
func apimNamespaceID(id string) string {
	root := apimRootID(id)
	parts := strings.Split(id, "/")
	if len(parts) >= 11 && strings.EqualFold(parts[9], "workspaces") {
		return strings.Join(parts[:11], "/")
	}
	return root
}
func apimAncestorIDs(id string) []string {
	if apimRootID(id) == "" {
		return nil
	}
	var result []string
	for !strings.EqualFold(id, apimRootID(id)) {
		id = redisParentID(id)
		result = append(result, id)
	}
	return result
}

// Policy XML, portal documents, credentials and named values stay in a keyed
// digest. Child collections and transient deployment progress are checked apart.
func apimSnapshot(kind string, raw map[string]any) map[string]any {
	encoded, _ := json.Marshal(raw)
	var result map[string]any
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	_ = decoder.Decode(&result)
	result["id"] = strings.ToLower(responseID(kind, text(raw["id"])))
	result["name"] = last(text(result["id"]))
	for _, key := range []string{"type", "etag", "_apim_header_etag"} {
		delete(result, key)
	}
	for _, key := range []string{"lastModifiedAt", "lastModifiedBy", "lastModifiedByType"} {
		delete(object(result["systemData"]), key)
	}
	props := object(result["properties"])
	for _, key := range []string{"provisioningState", "targetProvisioningState"} {
		delete(props, key)
	}
	if isAPIMNotification(kind) {
		delete(props, "recipients") // Verified against native child collections.
	}
	if isAPIMRoot(kind) {
		delete(props, "privateEndpointConnections")
		if text(props["createdAtUtc"]) == "" {
			result["_apim_service_etag"] = apimETag(kind, raw)
		}
	} else {
		delete(result, "location")
	}
	return result
}
func apimConfiguration(kind string, raw map[string]any) string {
	return serviceParentConfiguration(kind, apimSnapshot(kind, raw))
}
func apimListedIncarnation(kind string, listed, live map[string]any) error {
	if !nativeConfigurationContains(apimSnapshot(kind, listed), apimSnapshot(kind, live)) {
		return serviceDenied("apim_listed_configuration_changed")
	}
	return nil
}
func apimIncarnation(planned asset.Asset, live map[string]any) error {
	if isAPIMType(planned.Identity.NativeType) {
		if expected := text(planned.Normalized["arm_etag"]); expected != "" && expected != apimETag(planned.Identity.NativeType, live) {
			return serviceDenied("apim_resource_etag_changed")
		}
		if expected := text(planned.Normalized["_apim_configuration"]); expected == "" || expected != apimConfiguration(planned.Identity.NativeType, live) {
			return serviceDenied("apim_configuration_changed")
		}
	}
	return nil
}
func validateAPIM(kind string, raw map[string]any) error {
	if props, ok := raw["properties"].(map[string]any); !ok || props == nil {
		return serviceDenied("invalid_apim_properties")
	}
	id, actual, err := parseID(responseID(kind, text(raw["id"])))
	if err != nil || !strings.EqualFold(kind, actual) || !strings.EqualFold(last(id), text(raw["name"])) {
		return serviceDenied("invalid_apim_identity")
	}
	if isAPIMRoot(kind) {
		if text(raw["location"]) == "" || text(object(raw["properties"])["provisioningState"]) == "" {
			return serviceDenied("incomplete_apim_service")
		}
		// createdAtUtc is optional in the native contract. Without it,
		// freeze the service ETag as the conservative incarnation guard.
		if created := text(object(raw["properties"])["createdAtUtc"]); created != "" {
			if _, err := time.Parse(time.RFC3339Nano, created); err != nil {
				return serviceDenied("invalid_apim_service_creation_identity")
			}
		} else if apimETag(kind, raw) == "" {
			return serviceDenied("missing_apim_service_incarnation")
		}
	}
	if isAPIMAPI(kind) {
		revision := text(object(raw["properties"])["apiRevision"])
		if revision == "" {
			return serviceDenied("missing_apim_api_revision")
		}
		name := last(id)
		if base, rev, ok := strings.Cut(name, ";rev="); ok && (base == "" || rev == "" || rev != revision || strings.Contains(rev, ";")) {
			return serviceDenied("apim_revision_identity_changed")
		}
	}
	return nil
}
func apimReady(kind string, raw map[string]any) error {
	if err := validateAPIM(kind, raw); err != nil {
		return err
	}
	props := object(raw["properties"])
	if strings.EqualFold(kind, apimServiceType+"/portalRevisions") && !slices.Contains([]string{"completed", "failed"}, text(props["status"])) {
		return serviceDenied("apim_portal_revision_not_terminal")
	}
	for _, key := range []string{"provisioningState", "targetProvisioningState"} {
		state := text(props[key])
		if state != "" && !slices.Contains([]string{"Succeeded", "Failed", "Canceled", "Cancelled", "Stopped"}, state) && !(strings.EqualFold(last(kind), "privateEndpointConnections") && state == "Pending") {
			return serviceDenied("apim_resource_not_terminal")
		}
	}
	return nil
}
func apimProtection(kind string, raw map[string]any) string {
	if !isAPIMType(kind) {
		return ""
	}
	props := object(raw["properties"])
	if strings.EqualFold(last(kind), "templates") {
		return "azure_apim_template_reset_only"
	}
	if (strings.EqualFold(kind, apimServiceType+"/groups") || strings.EqualFold(kind, apimWorkspaceType+"/groups")) && props["builtIn"] == true {
		return "azure_apim_builtin_group"
	}
	if strings.EqualFold(kind, apimServiceType+"/users") && last(strings.ToLower(text(raw["id"]))) == "1" {
		return "azure_apim_administrator"
	}
	if strings.EqualFold(last(kind), "subscriptions") && strings.EqualFold(last(text(raw["id"])), "master") {
		return "azure_apim_builtin_subscription"
	}
	return ""
}
func apimSafeProperties(value any) any {
	result := map[string]any{}
	// Allow known inventory descriptors only. Opaque application bodies may
	// contain credentials under arbitrary keys, including innocent looking ones.
	for _, key := range []string{"displayName", "name", "state", "status", "type", "builtIn", "provisioningState", "targetProvisioningState", "createdAtUtc", "createdDate", "registrationDate", "expirationDate", "thumbprint", "apiRevision", "apiVersion", "isCurrent", "path", "protocols", "apiVersionSetId", "loggerId", "scope", "ownerId", "apiId", "groupId", "productId", "operationId", "certificateId", "resourceId", "virtualNetworkType", "publicNetworkAccess", "subscriptionRequired", "format", "contentType", "versioningScheme", "version", "publisherName", "title", "protocol", "loggerType", "isBuffered", "privateEndpoint", "privateLinkServiceConnectionState", "virtualNetworkConfiguration", "publicIpAddressId"} {
		if v, ok := object(value)[key]; ok {
			result[key] = v
		}
	}
	if secret, ok := object(value)["secret"].(bool); ok {
		result["isSecret"] = secret
	}
	return result
}
func apimRaw(raw map[string]any) bool {
	return isAPIMType(text(raw["type"])) || strings.EqualFold(text(raw["type"]), apimGatewayAlias) || apimRootID(text(raw["id"])) != ""
}
func apimListQuery(u *url.URL) error {
	if armPathProvider(u.Path) != "microsoft.apimanagement" {
		return nil
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != apimVersion {
		return serviceDenied("invalid_apim_list_version")
	}
	for key, values := range query {
		if key == "api-version" {
			continue
		}
		if len(values) != 1 || values[0] == "" || key != "$skip" && key != "$top" && key != "$skiptoken" {
			return serviceDenied("filtered_apim_collection")
		}
		if key == "$skip" || key == "$top" {
			for _, digit := range values[0] {
				if digit < '0' || digit > '9' {
					return serviceDenied("invalid_apim_pagination")
				}
			}
			if key == "$top" && strings.TrimLeft(values[0], "0") == "" {
				return serviceDenied("empty_apim_page_size")
			}
		}
	}
	return nil
}
func (c *client) apimResource(ctx context.Context, id string) (map[string]any, error) {
	_, kind, err := parseID(id)
	if err != nil || !isAPIMType(kind) {
		return nil, serviceDenied("invalid_apim_resource")
	}
	raw, err := c.linkedResource(ctx, id)
	if err != nil {
		return nil, err
	}
	return raw, validateAPIM(kind, raw)
}
func (c *client) apimInventory(ctx context.Context, id, kind string, raw, normalized map[string]any) error {
	if !isAPIMType(kind) {
		return nil
	}
	if err := validateAPIM(kind, raw); err != nil {
		return err
	}
	external := map[string][]string{}
	refs, err := c.apimResolvedReferences(ctx, kind, id, raw, nil, external)
	if err != nil {
		return err
	}
	normalized["_apim_references"] = refs
	normalized["_apim_external_references"] = external
	if kind == apimServiceType+"/privateEndpointConnections" {
		target, err := apimPrivateEndpointID(raw)
		if err != nil {
			return err
		}
		live, err := c.linkedResource(ctx, target)
		if err != nil {
			return err
		}
		normalized["_apim_private_endpoint_configuration"] = c.privateConfiguration(searchTargetSnapshot(live))
	}
	if strings.EqualFold(kind, apimGatewayConnectionType) {
		configuration, err := c.apimGatewaySourceConfiguration(ctx, id, raw)
		if err != nil {
			return err
		}
		normalized["_apim_gateway_source_configuration"] = configuration
	}
	normalized["_apim_configuration"] = apimConfiguration(kind, raw)
	normalized["_apim_private_configuration"] = c.privateConfiguration(apimSnapshot(kind, raw))
	normalized["arm_etag"] = apimETag(kind, raw)
	if flag, ok := object(raw["properties"])["secret"].(bool); ok {
		normalized["secret"] = flag
	}
	ancestors := map[string]any{}
	for _, parentID := range apimAncestorIDs(id) {
		parent, err := c.apimResource(ctx, parentID)
		if err != nil {
			return err
		}
		_, parentKind, _ := parseID(parentID)
		ancestors[parentID] = c.privateConfiguration(apimSnapshot(parentKind, parent))
		if isAPIMRoot(parentKind) {
			normalized["_apim_location"] = resourceRegion(parent)
		}
	}
	normalized["_apim_ancestors"] = ancestors
	return nil
}
func (a *action) apimRequestIdentity(value asset.Asset) error {
	if !isAPIMType(a.kind.NativeType) {
		return nil
	}
	id, kind, err := parseID(value.Identity.NativeID)
	if err != nil || value.Identity.Provider != asset.ProviderAzure || id != a.id || !strings.EqualFold(kind, a.kind.NativeType) || !strings.EqualFold(value.Identity.NativeType, a.kind.NativeType) {
		return serviceDenied("apim_action_identity_changed")
	}
	return nil
}
func (a *action) apimPreflight(ctx context.Context, planned asset.Asset, raw map[string]any) error {
	if !isAPIMType(a.kind.NativeType) {
		return nil
	}
	if err := apimReady(a.kind.NativeType, raw); err != nil {
		return err
	}
	if err := apimIncarnation(planned, raw); err != nil {
		return err
	}
	if _, conditional := a.deletion.Headers["If-Match"]; conditional {
		if etag := apimConditionalETag(a.kind.NativeType, raw); etag == "" || etag != text(planned.Normalized["arm_etag"]) {
			return serviceDenied("apim_resource_etag_changed")
		}
	}
	if expected := text(planned.Normalized["_apim_private_configuration"]); expected == "" || expected != a.client.privateConfiguration(apimSnapshot(a.kind.NativeType, raw)) {
		return serviceDenied("apim_private_configuration_changed")
	}
	parents := apimAncestorIDs(a.id)
	plannedParents := object(planned.Normalized["_apim_ancestors"])
	if len(parents) != len(plannedParents) {
		return serviceDenied("apim_ancestors_changed")
	}
	locks, err := a.client.managementLocks(ctx)
	if err != nil {
		return err
	}
	if err := a.client.apimVerifyTarget(ctx, planned, raw, locks); err != nil {
		return err
	}
	for _, id := range parents {
		live, err := a.client.apimResource(ctx, id)
		if err != nil {
			return err
		}
		_, kind, _ := parseID(id)
		if err := apimReady(kind, live); err != nil {
			return err
		}
		expected := text(plannedParents[id])
		if expected == "" || expected != a.client.privateConfiguration(apimSnapshot(kind, live)) {
			return serviceDenied("apim_ancestor_changed")
		}
		if err := a.client.linkedResourceProtection(ctx, id, live, locks); err != nil {
			return err
		}
	}
	return nil
}

// Retain the exact header separately: the service may also return an unquoted
// body etag. Only the GET header is the conditional-delete contract for proxies.
func apimResponseETag(method string, res *response) error {
	if method != "GET" || !apimRaw(res.data) {
		return nil
	}
	values := res.header.Values("ETag")
	if len(values) > 1 {
		return serviceDenied("ambiguous_apim_etag")
	}
	if len(values) == 1 {
		etag := values[0]
		if etag == "" || etag != strings.TrimSpace(etag) || etag == "*" || strings.ContainsAny(etag, "\r\n\x00,") {
			return serviceDenied("invalid_apim_etag")
		}
		res.data["_apim_header_etag"] = etag
	}
	return nil
}

func apimETag(kind string, raw map[string]any) string {
	if header := text(raw["_apim_header_etag"]); header != "" {
		return header
	}
	if isAPIMRoot(kind) || strings.EqualFold(kind, apimGatewayConnectionType) {
		return text(raw["etag"])
	}
	return ""
}
func apimConditionalETag(kind string, raw map[string]any) string {
	etag := text(raw["_apim_header_etag"])
	if etag == "" && strings.EqualFold(kind, apimGatewayConnectionType) {
		etag = text(raw["etag"]) // This contract exposes the entity tag in its body.
	}
	if etag == "*" || etag != strings.TrimSpace(etag) || strings.ContainsAny(etag, "\r\n\x00,") {
		return ""
	}
	return etag
}
func apimSafeRaw(raw map[string]any) map[string]any {
	result := maps.Clone(raw)
	delete(result, "_apim_header_etag")
	result["properties"] = apimSafeProperties(raw["properties"])
	return result
}
