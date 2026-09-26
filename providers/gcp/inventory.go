package gcp

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

type pageCursor struct {
	Token    string `json:"token"`
	ReadTime string `json:"read_time"`
}

func (c *client) assetPage(ctx context.Context, cursor, nativeType string, limit int) (map[string]any, error) {
	result, err := c.assetPageResult(ctx, cursor, nativeType, limit)
	return result.Data, err
}
func (c *client) assetPageResult(ctx context.Context, cursor, nativeType string, limit int) (contracts.InvocationResult, error) {
	if limit < 1 || limit > 1000 {
		limit = 500
	}
	query := url.Values{"contentType": {"RESOURCE"}, "pageSize": {strconv.Itoa(limit)}}
	if nativeType != "" {
		query.Set("assetTypes", nativeType)
	}
	if cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(cursor)
		if err != nil {
			return contracts.InvocationResult{}, fmt.Errorf("invalid GCP inventory cursor")
		}
		var page pageCursor
		if json.Unmarshal(decoded, &page) != nil || page.Token == "" {
			return contracts.InvocationResult{}, fmt.Errorf("invalid GCP inventory cursor")
		}
		query.Set("pageToken", page.Token)
		if page.ReadTime != "" {
			query.Set("readTime", page.ReadTime)
		}
	}
	result, err := c.requestResult(ctx, "GET", "https://cloudasset.googleapis.com/v1/projects/"+c.project+"/assets", query, nil)
	if err != nil {
		return contracts.InvocationResult{}, err
	}
	data := result.Data
	if requested := query.Get("readTime"); requested != "" {
		previous, previousErr := time.Parse(time.RFC3339Nano, requested)
		current, currentErr := time.Parse(time.RFC3339Nano, text(data["readTime"]))
		if previousErr != nil || currentErr != nil || !previous.Equal(current) {
			return contracts.InvocationResult{}, fmt.Errorf("Google asset snapshot changed during pagination")
		}
	}
	if _, err := time.Parse(time.RFC3339Nano, text(data["readTime"])); err != nil {
		return contracts.InvocationResult{}, fmt.Errorf("Google asset response has no valid snapshot time")
	}
	if assets, present := data["assets"]; present && assets != nil {
		if _, ok := assets.([]any); !ok {
			return contracts.InvocationResult{}, fmt.Errorf("Google asset response has an invalid asset list")
		}
	}
	if token := text(data["nextPageToken"]); token != "" {
		if token == query.Get("pageToken") {
			return contracts.InvocationResult{}, fmt.Errorf("Google asset pagination did not advance")
		}
		readTime := text(data["readTime"])
		if readTime == "" {
			readTime = query.Get("readTime")
		}
		encoded, _ := json.Marshal(pageCursor{Token: token, ReadTime: readTime})
		data["nextPageToken"] = base64.RawURLEncoding.EncodeToString(encoded)
	}
	result.NextToken = text(data["nextPageToken"])
	return result, nil
}
func (r *Runtime) List(ctx context.Context, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	batch, err := r.list(ctx, request)
	if err != nil {
		return batch, err
	}
	for i := range batch.Items {
		r.projectProperties(&batch.Items[i])
	}
	return batch, nil
}

// Apply declared aliases after native discovery, proof construction, redaction
// and service enrichment. Native keys remain authoritative for cleanup consumers.
func (r *Runtime) projectProperties(item *contracts.InventoryItem) {
	definition, _ := r.productDefinition(item.NativeType)
	properties := map[string]any{}
	for field, property := range definition.Fields {
		if _, exists := item.Normalized[field]; exists {
			continue
		}
		if value := productValue(item.Normalized, property.Path); value != nil {
			properties[field] = value
		}
	}
	// Resolve every path against the same native observation, not aliases added
	// earlier in this iteration; Go map order must not affect the projection.
	for field, value := range properties {
		item.Normalized[field] = value
	}
	// Native nested labels become both query properties and inventory tags.
	for key, value := range object(properties["labels"]) {
		if label, ok := value.(string); ok {
			if item.Tags == nil {
				item.Tags = map[string]string{}
			}
			if _, exists := item.Tags[key]; !exists {
				item.Tags[key] = label
			}
		}
	}
}

func (r *Runtime) list(ctx context.Context, request contracts.InventoryRequest) (contracts.InventoryBatch, error) {
	if request.Source != "" && request.Source != inventorySource && request.Source != productInventorySource && request.Source != dataformInventorySource && request.Source != firewallInventorySource && request.Source != organizationInventorySource && request.Source != identityInventorySource && request.Source != billingBudgetSource && request.Source != securityBillingSource && request.Source != securityServiceSource && request.Source != osLoginSource {
		return contracts.InventoryBatch{}, fmt.Errorf("unsupported GCP inventory source")
	}
	c, err := r.resolve(ctx, request.ConnectionID)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	if request.Scope.Kind == asset.ScopeProject && request.Scope.NativeID != c.project && request.Scope.NativeID != c.number {
		return contracts.InventoryBatch{}, fmt.Errorf("GCP inventory belongs to another project")
	}
	if request.Source == billingBudgetSource {
		return r.listBillingBudgets(ctx, c, request)
	}
	if request.Source == identityInventorySource {
		return r.listIdentityGroups(ctx, c, request)
	}
	if request.Source == organizationInventorySource {
		return r.listOrganization(ctx, c, request)
	}
	if request.Source == osLoginSource {
		return r.listOSLoginKeys(ctx, c, request)
	}
	if request.Source == firewallInventorySource {
		if request.ResourceKind == nil || firewallParentType(request.ResourceKind.NativeType) != firewallPolicyType {
			return contracts.InventoryBatch{}, groupDenied("firewall_inventory_source_invalid")
		}
		return r.listFirewall(ctx, c, request)
	}
	if request.Source == securityServiceSource && (request.ResourceKind == nil || request.ResourceKind.NativeType != securityServiceType || request.NetworkTarget != nil) {
		return contracts.InventoryBatch{}, groupDenied("security_services_scope_invalid")
	}
	if request.Source == securityBillingSource && (request.ResourceKind == nil || request.ResourceKind.NativeType != securityBillingType || request.NetworkTarget != nil) {
		return contracts.InventoryBatch{}, groupDenied("security_billing_scope_invalid")
	}
	if request.Source == productInventorySource || request.Source == securityBillingSource || request.Source == securityServiceSource {
		return r.listProduct(ctx, c, request, nil)
	}
	if request.Source == dataformInventorySource {
		return r.listDataformFolders(ctx, c, request)
	}
	nativeType := ""
	if request.ResourceKind != nil {
		nativeType = request.ResourceKind.NativeType
	}
	result, err := c.assetPageResult(ctx, request.Cursor, nativeType, request.Limit)
	if err != nil {
		return contracts.InventoryBatch{}, err
	}
	data := result.Data
	batch := contracts.InventoryBatch{Items: []contracts.InventoryItem{}, NextCursor: text(data["nextPageToken"]), RequestID: result.RequestID}
	batch.Complete = batch.NextCursor == ""
	region := request.Scope.NativeID
	if request.Scope.Kind == asset.ScopeGlobal {
		region = "global"
	}
	for _, value := range array(data["assets"]) {
		raw := object(value)
		// A broad CAI scan indexes unknown kinds. Known kinds have a separate
		// native source, so stale CAI data cannot overwrite its observations.
		if request.Source == inventorySource && r.usesProductSource(text(raw["assetType"])) {
			continue
		}
		_, location := assetLocation(raw)
		if request.Scope.Kind != asset.ScopeProject && region != location && !(request.NetworkTarget != nil && location == "global") {
			continue
		}
		item, err := r.inventoryItem(c, raw)
		if err != nil {
			return contracts.InventoryBatch{}, err
		}
		batch.Items = append(batch.Items, item)
	}
	return batch, nil
}

func assetLocation(raw map[string]any) (string, string) {
	resource := object(raw["resource"])
	data := object(resource["data"])
	location := text(resource["location"])
	for _, key := range []string{"zone", "region", "location", "locationId"} {
		if location == "" {
			location = last(text(data[key]))
		}
	}
	name := text(raw["name"])
	parts := strings.Split(name, "/")
	for i, part := range parts {
		if (part == "zones" || part == "regions" || part == "locations") && i+1 < len(parts) {
			location = parts[i+1]
			break
		}
	}
	if location == "" {
		location = "global"
	}
	kind, known := findType(text(raw["assetType"]))
	if known && len(kind.Scopes) == 1 && kind.Scopes[0] == asset.ScopeGlobal {
		return location, "global"
	}
	if text(raw["assetType"]) == "secretmanager.googleapis.com/Secret" && !strings.Contains(name, "/locations/") {
		return location, "global"
	}
	return location, regionOf(location)
}

func (r *Runtime) inventoryItem(c *client, raw map[string]any) (contracts.InventoryItem, error) {
	nativeType := text(raw["assetType"])
	nativeID := c.canonicalName(text(raw["name"]))
	if nativeType == "" || nativeID == "" {
		return contracts.InventoryItem{}, fmt.Errorf("Google asset identity is incomplete")
	}
	data := object(object(raw["resource"])["data"])
	kind, known := findType(nativeType)
	if known {
		if data == nil {
			return contracts.InventoryItem{}, fmt.Errorf("Google asset resource data is missing")
		}
		if _, err := c.resourceURL(kind, nativeID); err != nil {
			return contracts.InventoryItem{}, err
		}
	}
	if isRouterComponent(nativeType) {
		if err := routerComponentData(nativeType, data, last(nativeID)); err != nil {
			return contracts.InventoryItem{}, err
		}
	}
	location, region := assetLocation(raw)
	scope := contracts.InventoryScope{Kind: asset.ScopeRegion, NativeID: region, Name: region, Location: region}
	if region == "global" {
		scope = contracts.InventoryScope{Kind: asset.ScopeGlobal, NativeID: c.project + "/global", Name: "Global", Location: "global"}
	}
	normalized := map[string]any{}
	for key, value := range data {
		normalized[key] = value
	}
	if nativeType == monitoringDashboardType {
		if err := c.monitoringDashboardData(nativeID, data); err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized[monitoringDashboardReview] = monitoringDashboardConfiguration(nativeID, data)
	}
	if nativeType == monitoringGroupType {
		if err := c.monitoringGroupData(nativeID, data); err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized[monitoringGroupReview] = c.monitoringGroupConfiguration(nativeID, data)
	}
	if nativeType == notificationChannelType {
		if err := c.notificationChannelData(nativeID, data); err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized[notificationChannelReview] = notificationChannelConfiguration(nativeID, data)
		normalized["labels"] = data["userLabels"]
	}
	if nativeType == alertPolicyType {
		if err := c.alertPolicyData(nativeID, data); err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized[alertPolicyReview] = monitoringConfiguration(alertPolicyType, nativeID, data)
		normalized["labels"] = data["userLabels"]
	}
	if nativeType == uptimeType {
		if err := c.uptimeData(nativeID, data); err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized[uptimeReview] = uptimeConfiguration(nativeID, data)
		normalized["labels"] = data["userLabels"]
	}
	if nativeType == cloudNatType {
		normalized[cloudNatReview] = cloudNatConfiguration(data)
	}
	if nativeType == clusterType || nativeType == nodePoolType {
		labels := data["resourceLabels"]
		if nativeType == nodePoolType {
			labels = object(data["config"])["resourceLabels"]
		}
		normalized["labels"] = labels
	}
	if nativeType == storagePoolType {
		normalized["pool_usage"] = data["resourceStatus"]
		if normalized["pool_usage"] == nil {
			normalized["pool_usage"] = data["status"]
		}
	}
	normalized["_inventory_source"] = inventorySource
	normalized["project_id"] = c.project
	normalized["project_number"] = c.number
	if nativeType == securityServiceType || nativeType == securityBillingType {
		parts := strings.Split(strings.TrimPrefix(nativeID, "//"+securityServiceHost+"/"), "/")
		if len(parts) >= 2 {
			normalized["configurationParent"] = strings.Join(parts[:2], "/")
			if parts[0] != "projects" {
				delete(normalized, "project_id")
				delete(normalized, "project_number")
			}
		}
	}
	if isInfra(nativeType) {
		if err := c.infraIdentity(nativeType, nativeID, data); err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized[infraProof] = infraConfiguration(data)
	}
	if isMetricsScope(nativeType) {
		if err := c.metricsIdentity(nativeType, nativeID, data); err != nil {
			return contracts.InventoryItem{}, err
		}
	}
	if nativeType == batchJobType {
		normalized[batchProof] = batchConfiguration(data)
	}
	if isDataproc(nativeType) {
		normalized[dataprocProof] = dataprocConfiguration(data)
		normalized["_dataproc_region"] = dataprocRegion(nativeID)
	}
	if isFusion(nativeType) {
		if err := c.fusionIdentity(nativeType, nativeID, data); err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized[fusionProof] = fusionConfiguration(data)
	}
	if isTPU(nativeType) {
		if err := c.tpuIdentity(nativeType, nativeID, data); err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized[tpuProof] = tpuConfiguration(nativeType, data, false)
		normalized[tpuBaseProof] = tpuConfiguration(nativeType, data, true)
	}
	if isDiscovery(nativeType) {
		if err := c.discoveryIdentity(nativeType, nativeID, data); err != nil {
			return contracts.InventoryItem{}, err
		}
		normalized[discoveryProof] = discoveryConfiguration(data)
	}
	if isDataform(nativeType) {
		normalized[dataformProof] = dataformConfiguration(nativeType, data)
	}
	if reason := protectionReason(nativeType, data); reason != "" {
		normalized["cleanup_protected"] = true
		normalized["cleanup_protection_reason"] = reason
	}
	if nativeType == identityGroupType && identityGroupProtected(data) {
		normalized["cleanup_protected"] = true
		normalized["cleanup_protection_reason"] = "identity_group_locked_or_protected"
	}
	refs := references(c, data)
	if nativeType == alertPolicyType || nativeType == notificationChannelType || nativeType == monitoringDashboardType {
		refs = map[string][]string{}
	}
	if nativeType == monitoringGroupType {
		refs = map[string][]string{}
		if parent := text(data["parentName"]); parent != "" {
			refs[monitoringGroupType] = []string{c.canonicalName("//monitoring.googleapis.com/" + parent)}
		}
	}
	if nativeType == uptimeType {
		var err error
		refs, err = c.uptimeReferences(nativeID, data)
		if err != nil {
			return contracts.InventoryItem{}, err
		}
	}
	if nativeType == cloudNatType {
		refs = c.cloudNatReferences(data)
		hubs, _, err := c.cloudNatHubReferences(data)
		if err != nil {
			return contracts.InventoryItem{}, err
		}
		refs[cloudNatHubType] = hubs
	}
	if nativeType == securityServiceType {
		refs = map[string][]string{}
	}
	if isIdentityGroup(nativeType) {
		refs = map[string][]string{}
		if nativeType == identityMemberType {
			refs[identityGroupType] = []string{"//" + identityHost + "/" + identityGroupName(text(data["name"]))}
		}
	}
	if isFirewall(nativeType) {
		if !isFirewallPolicy(nativeType) {
			parent, _, err := c.firewallIdentityParts(nativeType, nativeID)
			if err != nil {
				return contracts.InventoryItem{}, err
			}
			refs[firewallParentType(nativeType)] = []string{parent}
			if nativeType == networkFirewallAssociationType {
				refs["compute.googleapis.com/Network"] = []string{c.canonicalName(text(data["attachmentTarget"]))}
			}
		} else if nativeType == networkFirewallPolicyType {
			for _, value := range array(data["associations"]) {
				refs["compute.googleapis.com/Network"] = append(refs["compute.googleapis.com/Network"], c.canonicalName(text(object(value)["attachmentTarget"])))
			}
		}
	}
	if isInfra(nativeType) {
		refs = c.infraReferences(nativeType, nativeID, data)
	}
	if nativeType == monitoredProjectType {
		refs[metricsScopeType] = []string{strings.Join(strings.Split(nativeID, "/")[:7], "/")}
	}
	if isFusion(nativeType) {
		var err error
		refs, err = c.fusionReferences(nativeType, nativeID, data)
		if err != nil {
			return contracts.InventoryItem{}, err
		}
	}
	if isTPU(nativeType) {
		var err error
		refs, err = c.tpuReferences(nativeType, nativeID, data)
		if err != nil {
			return contracts.InventoryItem{}, err
		}
	}
	if nativeType == batchJobType {
		refs = references(c, c.batchDependencyData(data))
	}
	if isDataproc(nativeType) {
		refs = c.dataprocReferences(nativeType, nativeID, data)
	}
	if isDiscovery(nativeType) {
		var err error
		refs, err = c.discoveryReferences(nativeType, nativeID, data)
		if err != nil {
			return contracts.InventoryItem{}, err
		}
	}
	if nativeType == "dataform.googleapis.com/WorkflowConfig" {
		id := c.canonicalName("//dataform.googleapis.com/" + text(data["releaseConfig"]))
		kind, _ := findType("dataform.googleapis.com/ReleaseConfig")
		if _, err := c.resourceURL(kind, id); err == nil {
			refs[kind.NativeType] = []string{id}
		}
	}
	networkIDs := refs["compute.googleapis.com/Network"]
	subnetIDs := refs["compute.googleapis.com/Subnetwork"]
	if len(networkIDs) == 1 {
		normalized["vpc_id"] = networkIDs[0]
	}
	if len(subnetIDs) > 0 {
		normalized["subnet_ids"] = subnetIDs
		if len(subnetIDs) == 1 {
			normalized["vswitch_id"] = subnetIDs[0]
		}
	}
	if nativeType == "compute.googleapis.com/Network" {
		normalized["vpc_id"] = nativeID
	}
	if nativeType == "compute.googleapis.com/Subnetwork" {
		normalized["vswitch_id"] = nativeID
	}
	if zone := text(data["zone"]); zone != "" {
		normalized["zone_id"] = last(zone)
	}
	networkRefs := []string{}
	for target, values := range refs {
		normalized[referenceKey(target)] = values
		networkRefs = append(networkRefs, values...)
	}
	sort.Strings(networkRefs)
	tags := map[string]string{}
	for key, value := range object(normalized["labels"]) {
		if s, ok := value.(string); ok {
			tags[key] = s
		}
	}
	name := text(data["displayName"])
	if name == "" {
		name = last(text(data["name"]))
	}
	if name == "" {
		name = last(nativeID)
	}
	state := resourceState(data)
	if nativeType == securityServiceType {
		if err := securityServiceSettings(data); err != nil {
			return contracts.InventoryItem{}, err
		}
		state = text(data["effectiveEnablementState"])
	}
	actionable := known && len(kind.DeleteOperations) > 0
	if nativeType == notificationChannelType && data["type"] == "email" {
		actionable = false
	}
	if actionable {
		_, _, err := c.resourceOperation(kind, nativeID, "DELETE")
		actionable = err == nil
	}
	if nativeType == storagePoolType {
		actionable = false
	} // Native member/configuration discovery enables reviewed pool cleanup.
	sanitize := safePayload
	if nativeType == securityServiceType || nativeType == securityBillingType {
		sanitize = safeSecurityServicePayload
	}
	if isInfra(nativeType) {
		sanitize = safeInfraPayload
	}
	if isFusion(nativeType) {
		sanitize = safeFusionPayload
	}
	if isTPU(nativeType) {
		sanitize = safeTPUPayload
	}
	if isDiscovery(nativeType) {
		sanitize = safeDiscoveryPayload
	}
	aliases := []string{text(data["selfLink"]), nativeID}
	if nativeType == instanceType {
		if alias := uptimeInstanceAlias(nativeID, data); alias != "" {
			aliases = append(aliases, alias)
		}
	}
	return contracts.InventoryItem{NativeType: nativeType, NativeID: nativeID, ResourceKind: r.resourceKind(nativeType), Actionable: &actionable, Scope: scope, Name: name, State: state, Location: location, Tags: tags, Normalized: sanitize(normalized), Raw: sanitize(raw), NativeAliases: aliases, NetworkReferences: networkRefs}, nil
}

func resourceState(data map[string]any) string {
	if state := text(object(data["status"])["procurementStatus"]); state != "" {
		return state
	}
	for _, field := range []string{"status", "state", "routeStatus"} {
		if state := text(data[field]); state != "" {
			return state
		}
		if state := text(object(data[field])["state"]); state != "" {
			return state
		}
	}
	return text(object(data["terminalCondition"])["state"])
}

func (c *client) canonicalName(value string) string {
	if strings.HasPrefix(value, "//"+metricsHost+"/locations/global/metricsScopes/") || strings.HasPrefix(value, "https://"+metricsHost+"/v1/locations/global/metricsScopes/") {
		kind := metricsScopeType
		if strings.Contains(value, "/projects/") {
			kind = monitoredProjectType
		}
		if id, err := c.metricsID(kind, value, false); err == nil {
			return id
		}
		return value
	}
	value = canonicalName(value)
	if c.number != "" {
		value = strings.Replace(value, "/projects/"+c.number+"/", "/projects/"+c.project+"/", 1)
	}
	return value
}

func canonicalName(value string) string {
	value = infraCanonical(fusionCanonical(tpuCanonical(discoveryCanonical(strings.TrimSpace(value)))))
	for _, prefix := range []string{"https://container.googleapis.com/v1/", "https://container.googleapis.com/v1beta1/"} {
		if strings.HasPrefix(value, prefix) {
			value = "//container.googleapis.com/" + strings.TrimPrefix(value, prefix)
		}
	}
	if strings.HasPrefix(value, "https://www.googleapis.com/compute/v1/") {
		value = "//compute.googleapis.com/" + strings.TrimPrefix(value, "https://www.googleapis.com/compute/v1/")
	}
	if strings.HasPrefix(value, "https://compute.googleapis.com/compute/v1/") {
		value = "//compute.googleapis.com/" + strings.TrimPrefix(value, "https://compute.googleapis.com/compute/v1/")
	}
	value = strings.Replace(value, "//cloudsql.googleapis.com/", "//sqladmin.googleapis.com/", 1)
	if strings.HasPrefix(value, "//container.googleapis.com/") {
		value = strings.Replace(value, "/zones/", "/locations/", 1)
	}
	if strings.HasPrefix(value, "//dataproc.googleapis.com/") && (strings.Contains(value, "/autoscalingPolicies/") || strings.Contains(value, "/workflowTemplates/")) {
		value = strings.Replace(value, "/locations/", "/regions/", 1)
	}
	return value
}

func references(c *client, data map[string]any) map[string][]string {
	result := map[string][]string{}
	// Restrict references to live dependencies. Creation history (sourceImage,
	// sourceSnapshot) and reverse children lists are not deletion dependencies.
	fields := map[string]string{"network": "compute.googleapis.com/Network", "networkURL": "compute.googleapis.com/Network", "privateNetwork": "compute.googleapis.com/Network", "subnetwork": "compute.googleapis.com/Subnetwork", "subnetworkURL": "compute.googleapis.com/Subnetwork", "topic": "pubsub.googleapis.com/Topic", "deadLetterTopic": "pubsub.googleapis.com/Topic", "healthChecks": "compute.googleapis.com/HealthCheck", "urlMap": "compute.googleapis.com/UrlMap", "sslCertificates": "compute.googleapis.com/SslCertificate", "backendService": "compute.googleapis.com/BackendService", "defaultService": "compute.googleapis.com/BackendService", "service": "compute.googleapis.com/BackendService", "nextHopInstance": "compute.googleapis.com/Instance", "target": "", "source": "compute.googleapis.com/Disk"}
	var visit func(any, string)
	fields["instanceTemplate"] = "compute.googleapis.com/InstanceTemplate"
	fields["instanceGroup"] = instanceGroupType
	fields["healthCheck"] = "compute.googleapis.com/HealthCheck"
	fields["group"] = ""
	fields["instances"] = instanceType
	for key, target := range map[string]string{
		"subnetworks": "Subnetwork", "natSubnets": "Subnetwork", "attachmentTarget": "Network",
		"router": "Router", "vpnGateway": "VpnGateway", "targetVpnGateway": "TargetVpnGateway", "peerExternalGateway": "ExternalVpnGateway",
		"interconnect": "Interconnect", "interconnectAttachment": "InterconnectAttachment", "networkAttachment": "NetworkAttachment", "nodeTemplate": "NodeTemplate", "resourcePolicies": "ResourcePolicy",
		"targetService": "ForwardingRule", "sslPolicy": "SslPolicy", "securityPolicy": "SecurityPolicy", "edgeSecurityPolicy": "SecurityPolicy",
	} {
		fields[key] = "compute.googleapis.com/" + target
	}
	fields["storagePool"] = storagePoolType
	fields["storagePools"] = storagePoolType
	fields["bucketName"] = "storage.googleapis.com/Bucket"
	for key, target := range map[string]string{
		"authorizedNetwork": "compute.googleapis.com/Network", "networkUri": "compute.googleapis.com/Network", "networkUrl": "compute.googleapis.com/Network",
		"kmsKeyName": "cloudkms.googleapis.com/CryptoKey", "kmsKey": "cloudkms.googleapis.com/CryptoKey", "customerManagedKey": "cloudkms.googleapis.com/CryptoKey",
		"apiConfig": "apigateway.googleapis.com/ApiConfig", "certificates": "certificatemanager.googleapis.com/Certificate", "certificateMap": "certificatemanager.googleapis.com/CertificateMap",
		"gkeCluster": "container.googleapis.com/Cluster", "resourceLink": "container.googleapis.com/Cluster",
		"gatewayServiceAccount": "iam.googleapis.com/ServiceAccount", "serviceAccount": "iam.googleapis.com/ServiceAccount", "serviceAccountEmail": "iam.googleapis.com/ServiceAccount",
		"customerManagedEncryptionKey": "cloudkms.googleapis.com/CryptoKey", "cryptoKeyName": "cloudkms.googleapis.com/CryptoKey",
		"sourceConnectionProfile": "datastream.googleapis.com/ConnectionProfile", "destinationConnectionProfile": "datastream.googleapis.com/ConnectionProfile", "privateConnection": "datastream.googleapis.com/PrivateConnection",
		"backupVault": "backupdr.googleapis.com/BackupVault", "backupPlan": "backupdr.googleapis.com/BackupPlan", "dataSource": "backupdr.googleapis.com/DataSource",
		"firewallEndpoint": "networksecurity.googleapis.com/FirewallEndpoint", "hub": "networkconnectivity.googleapis.com/Hub", "vpcNetwork": "compute.googleapis.com/Network", "subnet": "compute.googleapis.com/Subnetwork", "vpnTunnel": "compute.googleapis.com/VpnTunnel",
		"pubsubTopic": "pubsub.googleapis.com/Topic", "virtualMachine": instanceType, "messageBus": "eventarc.googleapis.com/MessageBus",
		"adminNetwork": "compute.googleapis.com/Network", "nccHub": "networkconnectivity.googleapis.com/Hub", "reservedInternalRange": "networkconnectivity.googleapis.com/InternalRange",
		"multicastDomainGroup": "networkservices.googleapis.com/MulticastDomainGroup", "multicastDomain": "networkservices.googleapis.com/MulticastDomain", "multicastDomainActivation": "networkservices.googleapis.com/MulticastDomainActivation",
		"multicastGroupRange": "networkservices.googleapis.com/MulticastGroupRange", "multicastGroupRangeActivation": "networkservices.googleapis.com/MulticastGroupRangeActivation",
		"multicastProducerAssociation": "networkservices.googleapis.com/MulticastProducerAssociation", "multicastConsumerAssociation": "networkservices.googleapis.com/MulticastConsumerAssociation", "placementPolicy": "compute.googleapis.com/ResourcePolicy",
		"origin": "networkservices.googleapis.com/EdgeCacheOrigin", "failoverOrigin": "networkservices.googleapis.com/EdgeCacheOrigin", "keyset": "networkservices.googleapis.com/EdgeCacheKeyset", "signedRequestKeyset": "networkservices.googleapis.com/EdgeCacheKeyset", "edgeSslCertificates": "certificatemanager.googleapis.com/Certificate",
		"secretVersion": "secretmanager.googleapis.com/Secret", "secretAccessKeyVersion": "secretmanager.googleapis.com/Secret",
		"authenticationTokenSecretVersion": "secretmanager.googleapis.com/Secret", "userPrivateKeySecretVersion": "secretmanager.googleapis.com/Secret", "npmrcEnvironmentVariablesSecretVersion": "secretmanager.googleapis.com/Secret",
	} {
		fields[key] = target
	}
	visit = func(value any, key string) {
		switch typed := value.(type) {
		case map[string]any:
			for child, v := range typed {
				// Compute instances and templates list their identities as
				// serviceAccounts[].email alongside granted scopes.
				if child == "serviceAccounts" {
					for _, account := range array(v) {
						visit(object(account)["email"], "serviceAccountEmail")
					}
				}
				if child == "linkedVpnTunnels" || child == "linkedInterconnectAttachments" {
					key := "vpnTunnel"
					if child == "linkedInterconnectAttachments" {
						key = "interconnectAttachment"
					}
					visit(object(v)["uris"], key)
				}
				visit(v, child)
			}
		case []any:
			for _, v := range typed {
				visit(v, key)
			}
		case string:
			target, ok := fields[key]
			if !ok {
				return
			}
			ref := c.canonicalName(typed)
			if key == "edgeSslCertificates" && !strings.Contains(ref, "/") {
				ref = "projects/" + c.project + "/locations/global/certificates/" + ref
			}
			if key == "edgeSecurityPolicy" && !strings.Contains(ref, "/") {
				ref = "projects/" + c.project + "/global/securityPolicies/" + ref
			}
			if target == "networkservices.googleapis.com/EdgeCacheOrigin" || target == "networkservices.googleapis.com/EdgeCacheKeyset" {
				if !strings.Contains(ref, "/") {
					collection := "edgeCacheOrigins"
					if target == "networkservices.googleapis.com/EdgeCacheKeyset" {
						collection = "edgeCacheKeysets"
					}
					ref = "projects/" + c.project + "/locations/global/" + collection + "/" + ref
				}
			}
			if target == "secretmanager.googleapis.com/Secret" {
				parts := strings.Split(ref, "/")
				if len(parts) >= 4 && parts[len(parts)-2] == "versions" {
					ref = strings.Join(parts[:len(parts)-2], "/")
				}
			}
			if key == "service" && strings.Contains(ref, "/locations/") && strings.Contains(ref, "/services/") {
				target = "run.googleapis.com/Service"
			}
			if target == "iam.googleapis.com/ServiceAccount" && (strings.HasSuffix(ref, "@"+c.project+".iam.gserviceaccount.com") || ref == c.number+"-compute@developer.gserviceaccount.com") && !strings.Contains(ref, "/") {
				ref = "projects/" + c.project + "/serviceAccounts/" + ref
			}
			if target == storagePoolType && strings.HasPrefix(ref, "zones/") {
				ref = "projects/" + c.project + "/" + ref
			}
			if strings.HasPrefix(ref, "projects/") && target != "" {
				ref = "//" + strings.Split(target, "/")[0] + "/" + ref
			}
			if target == "compute.googleapis.com/Network" || target == "compute.googleapis.com/Subnetwork" {
				ref = strings.Replace(ref, "/locations/global/networks/", "/global/networks/", 1)
				if strings.HasPrefix(ref, "projects/") {
					ref = "//compute.googleapis.com/" + ref
				}
				if !strings.Contains(ref, "/") && target == "compute.googleapis.com/Network" {
					ref = "//compute.googleapis.com/projects/" + c.project + "/global/networks/" + ref
				}
			}
			if target == "pubsub.googleapis.com/Topic" && strings.HasPrefix(ref, "projects/") {
				ref = "//pubsub.googleapis.com/" + ref
			}
			if target == "storage.googleapis.com/Bucket" && !strings.Contains(ref, "/") {
				ref = "//storage.googleapis.com/" + ref
			}
			if target == "" {
				for _, kind := range allTypes() {
					if strings.HasPrefix(ref, "//compute.googleapis.com/") && strings.Contains(ref, "/"+kind.Collection+"/") {
						target = kind.NativeType
						break
					}
				}
			}
			if target == "compute.googleapis.com/Disk" && strings.Contains(ref, "/regions/") {
				target = "compute.googleapis.com/RegionDisk"
			}
			if target == "compute.googleapis.com/BackendService" && strings.Contains(ref, "/regions/") {
				target = "compute.googleapis.com/RegionBackendService"
			}
			if target == "compute.googleapis.com/BackendService" && strings.Contains(ref, "/backendBuckets/") {
				target = "compute.googleapis.com/BackendBucket"
			}
			if target == "compute.googleapis.com/HealthCheck" {
				if strings.Contains(ref, "/httpHealthChecks/") {
					target = "compute.googleapis.com/HttpHealthCheck"
				}
				if strings.Contains(ref, "/httpsHealthChecks/") {
					target = "compute.googleapis.com/HttpsHealthCheck"
				}
			}
			ref = c.canonicalName(ref)
			kind, known := findType(target)
			if !known {
				return
			}
			if _, err := c.resourceURL(kind, ref); err != nil {
				return
			}
			for _, existing := range result[target] {
				if existing == ref {
					return
				}
			}
			result[target] = append(result[target], ref)
		}
	}
	visit(data, "")
	if kind, ok := findType(text(data["resourceType"])); ok && text(data["resource"]) != "" {
		fields["backupWorkload"] = kind.NativeType
		visit(text(data["resource"]), "backupWorkload")
	}
	if origin := text(data["originAddress"]); strings.HasPrefix(origin, "gs://") {
		visit(strings.TrimPrefix(origin, "gs://"), "bucketName")
	} else if strings.HasSuffix(origin, ".storage.googleapis.com") {
		visit(strings.TrimSuffix(origin, ".storage.googleapis.com"), "bucketName")
	}
	// These services encode a dependency's type in an adjacent field or URI
	// prefix. Only recognized formats enter the same project-bound validator.
	resource := object(data["resourceSpec"])
	resourceParts := strings.Split(text(resource["name"]), "/")
	if len(resourceParts) == 4 && resourceParts[0] == "projects" && (resourceParts[1] == c.project || resourceParts[1] == c.number) {
		if resource["type"] == "STORAGE_BUCKET" && resourceParts[2] == "buckets" {
			visit(resourceParts[3], "bucketName")
		}
		if resource["type"] == "BIGQUERY_DATASET" && resourceParts[2] == "datasets" {
			fields["datasetResource"] = "bigquery.googleapis.com/Dataset"
			visit(text(resource["name"]), "datasetResource")
		}
	}
	for _, target := range []string{"storage.googleapis.com/Bucket", "bigquery.googleapis.com/Dataset", "pubsub.googleapis.com/Topic", "logging.googleapis.com/LogBucket"} {
		host := strings.Split(target, "/")[0]
		if destination := text(data["destination"]); strings.HasPrefix(destination, host+"/") {
			fields["sinkDestination"] = target
			visit("//"+destination, "sinkDestination")
		}
	}
	// An Eventarc enrollment delivers to a pipeline named by its destination.
	if destination := text(data["destination"]); strings.Contains(destination, "/pipelines/") {
		fields["eventarcPipeline"] = "eventarc.googleapis.com/Pipeline"
		visit(destination, "eventarcPipeline")
	}
	// Sole-tenant placement and specific reservation affinity use native names
	// instead of selfLinks. Resolve them within this VM's actual zone only.
	if zone := last(text(data["zone"])); zone != "" && strings.Contains(text(data["selfLink"]), "/instances/") {
		affinities := array(object(data["scheduling"])["nodeAffinities"])
		reservation := object(data["reservationAffinity"])
		if reservation["consumeReservationType"] == "SPECIFIC_RESERVATION" {
			affinities = append(slices.Clone(affinities), reservation)
		}
		for _, raw := range affinities {
			affinity := object(raw)
			target, collection := "", ""
			if affinity["key"] == "compute.googleapis.com/node-group-name" && affinity["operator"] == "IN" {
				target, collection = "compute.googleapis.com/NodeGroup", "nodeGroups"
			} else if affinity["key"] == "compute.googleapis.com/reservation-name" {
				target, collection = "compute.googleapis.com/Reservation", "reservations"
			}
			kind, known := findType(target)
			if !known {
				continue
			}
			for _, raw := range array(affinity["values"]) {
				id := c.canonicalName(text(raw))
				if !strings.Contains(id, "/") {
					id = "//compute.googleapis.com/projects/" + c.project + "/zones/" + zone + "/" + collection + "/" + id
				}
				if strings.HasPrefix(id, "projects/") {
					id = "//compute.googleapis.com/" + id
				}
				if _, err := c.resourceURL(kind, id); err == nil && !slices.Contains(result[target], id) {
					result[target] = append(result[target], id)
				}
			}
		}
	}
	for _, values := range result {
		sort.Strings(values)
	}
	return result
}

type networkCursor struct {
	Filter string `json:"filter"`
	Page   string `json:"page"`
}

func (r *Runtime) SearchNetworkTargets(ctx context.Context, query contracts.NetworkTargetQuery) (contracts.NetworkTargetPage, error) {
	nativeType := "compute.googleapis.com/Network"
	scope := asset.Scope{Kind: asset.ScopeGlobal, NativeID: "global"}
	if query.Kind == asset.ScanTargetVSwitch {
		nativeType = "compute.googleapis.com/Subnetwork"
		if !segmentPattern.MatchString(query.RegionID) {
			return contracts.NetworkTargetPage{}, fmt.Errorf("GCP subnet search requires a region")
		}
		scope = asset.Scope{Kind: asset.ScopeRegion, NativeID: query.RegionID}
	} else if query.Kind != asset.ScanTargetVPC {
		return contracts.NetworkTargetPage{}, fmt.Errorf("unsupported GCP network target")
	}
	kind := r.resourceKind(nativeType)
	filter := query
	filter.Cursor = ""
	filter.Limit = 0
	encoded, _ := json.Marshal(filter)
	fingerprint := fmt.Sprintf("%x", sha256.Sum256(encoded))
	cursor := networkCursor{Filter: fingerprint}
	if query.Cursor != "" {
		decoded, err := base64.RawURLEncoding.DecodeString(query.Cursor)
		if err != nil || json.Unmarshal(decoded, &cursor) != nil || cursor.Filter != fingerprint || cursor.Page == "" {
			return contracts.NetworkTargetPage{}, fmt.Errorf("GCP network cursor does not match this search")
		}
	}
	batch, err := r.List(ctx, contracts.InventoryRequest{ConnectionID: query.ConnectionID, Source: productInventorySource, ResourceKind: &kind, Scope: scope, Cursor: cursor.Page, Limit: query.Limit})
	if err != nil {
		return contracts.NetworkTargetPage{}, err
	}
	page := contracts.NetworkTargetPage{Items: []contracts.NetworkTargetOption{}, RequestID: batch.RequestID}
	if batch.NextCursor != "" {
		encoded, _ := json.Marshal(networkCursor{Filter: fingerprint, Page: batch.NextCursor})
		page.NextCursor = base64.RawURLEncoding.EncodeToString(encoded)
	}
	for _, item := range batch.Items {
		parent := text(item.Normalized["vpc_id"])
		if query.ParentNativeID != "" && parent != query.ParentNativeID {
			continue
		}
		if query.Query != "" && !strings.Contains(strings.ToLower(item.Name+" "+item.NativeID), strings.ToLower(query.Query)) {
			continue
		}
		page.Items = append(page.Items, contracts.NetworkTargetOption{Kind: query.Kind, RegionID: query.RegionID, NativeID: item.NativeID, Name: item.Name, ParentNativeID: parent})
	}
	return page, nil
}
