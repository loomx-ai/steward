package azure

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/loomx-ai/steward/internal/core/asset"
	"github.com/loomx-ai/steward/internal/core/execution"
	"github.com/loomx-ai/steward/internal/provider/catalog"
	"github.com/loomx-ai/steward/internal/provider/contracts"
)

func (a *action) prepareAttachments(ctx context.Context, request contracts.ActionRequest) (contracts.ActionResult, error) {
	live, err := a.client.request(ctx, "GET", a.endpoint)
	if isNotFound(err) {
		return contracts.ActionResult{}, nil
	}
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if !validResourceResponse(live, a.id, a.kind.NativeType) {
		return contracts.ActionResult{}, fmt.Errorf("Azure attachment preparation identity mismatch")
	}
	locks, err := a.client.listAll(ctx, a.client.root()+"/providers/Microsoft.Authorization/locks", locksVersion)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	updates, reason, err := a.evaluateAttachments(ctx, request, live.data, locks)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if locked(a.id, locks) {
		reason = "azure_management_lock"
	}
	if protected := protectionReason(a.kind, live.data); protected != "" {
		reason = protected
	}
	if reason != "" {
		return contracts.ActionResult{}, &contracts.ProviderCallError{Provider: execution.ProviderError{Category: execution.ErrorProtected, Code: reason, Message: contracts.SafeProviderValidationMessage}}
	}
	if len(updates) == 0 {
		return a.delete(ctx, request)
	}
	update := updates[0]
	target, err := a.attachmentTarget(request, update.asset.Identity.NativeID)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	body, err := update.body()
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operationID := "Azure.Microsoft.Compute.VirtualMachines_Update"
	if target.kind.NativeType == nicType {
		operationID = "Azure.Microsoft.Network.NetworkInterfaces_CreateOrUpdate"
	}
	metadata, err := providerData()
	if err != nil {
		return contracts.ActionResult{}, err
	}
	operation, ok := metadata.catalog.Operation(operationID)
	if !ok || operation.Call == nil {
		return contracts.ActionResult{}, fmt.Errorf("Azure retention operation is unavailable")
	}
	_, parameters, err := a.client.resourceOperation(target.kind, target.id, "GET")
	if err != nil {
		return contracts.ActionResult{}, err
	}
	parameters["parameters"] = body
	// Compute declares conditional updates. Network returns an ETag but its
	// pinned operation does not declare If-Match; do not assume it enforces it.
	if _, supports := object(operation.InputSchema["properties"])["If-Match"]; supports && text(update.live["etag"]) != "" {
		parameters["If-Match"] = text(update.live["etag"])
	}
	bound, err := catalog.BindREST(operation, parameters)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if bound.Headers == nil {
		bound.Headers = map[string]string{}
	}
	if request.IdempotencyKey != "" {
		bound.Headers["x-ms-client-request-id"] = azureRequestID(request.IdempotencyKey + ":retain:" + target.id)
	}
	response, err := a.client.requestBody(ctx, bound.Method, bound.URL, bound.Body, bound.Headers)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	if err := operationError(response); err != nil {
		return contracts.ActionResult{}, err
	}
	result, err := target.operationResult(response)
	if err != nil {
		return contracts.ActionResult{}, err
	}
	result.Data["phase"], result.Data["target"], result.Data["operation"] = "prepare_attachments", target.id, result.ProviderOperationID
	retained := make([]any, 0, len(update.retain))
	for _, resource := range update.retain {
		retained = append(retained, resource.id)
	}
	result.Data["retain_resources"] = retained
	return result, nil
}

func (a *action) attachmentTarget(request contracts.ActionRequest, id string) (*action, error) {
	value := request.Asset
	if !strings.EqualFold(id, a.id) {
		found := false
		for _, impact := range request.LifecycleImpacts {
			if strings.EqualFold(impact.Asset.Identity.NativeID, id) && impact.Delete && impact.Asset.Identity.NativeType == nicType && impact.Asset.Identity.ConnectionID == request.Asset.Identity.ConnectionID && impact.Asset.Identity.Partition == request.Asset.Identity.Partition && impact.Asset.Identity.Provider == asset.ProviderAzure {
				if found {
					return nil, fmt.Errorf("ambiguous Azure retention target")
				}
				found, value = true, impact.Asset
			}
		}
		if !found {
			return nil, fmt.Errorf("Azure retention target is outside the reviewed cascade")
		}
	}
	kind, ok := findType(value.Identity.NativeType)
	if !ok || (kind.NativeType != vmType && kind.NativeType != nicType) {
		return nil, fmt.Errorf("invalid Azure retention target kind")
	}
	endpoint, err := a.client.resourceURL(kind, id)
	if err != nil {
		return nil, err
	}
	canonical, _, _ := parseID(id)
	return &action{client: a.client, kind: kind, endpoint: endpoint, id: canonical, location: strings.ToLower(value.Location)}, nil
}

func (a *action) waitAttachmentPreparation(ctx context.Context, request contracts.ActionRequest, result contracts.ActionResult) (contracts.WaitResult, error) {
	if a.kind.NativeType != vmType && a.kind.NativeType != nicType {
		return contracts.WaitResult{}, fmt.Errorf("invalid Azure attachment preparation phase")
	}
	target, err := a.attachmentTarget(request, text(result.Data["target"]))
	if err != nil {
		return contracts.WaitResult{}, err
	}
	result.ProviderOperationID = text(result.Data["operation"])
	poll, err := target.poll(ctx, result)
	if err != nil || !poll.Done {
		return poll, err
	}
	// Some ARM providers finish synchronous writes without a polling header.
	// Verify provisioning state before another mutation even in that case.
	live, err := a.client.request(ctx, "GET", target.endpoint)
	if err != nil && !isNotFound(err) {
		return contracts.WaitResult{}, err
	}
	if err == nil {
		if !strings.EqualFold(text(live.data["id"]), target.id) {
			return contracts.WaitResult{}, fmt.Errorf("Azure preparation readback identity mismatch")
		}
		if err := operationError(live); err != nil {
			return contracts.WaitResult{}, err
		}
		state := text(object(live.data["properties"])["provisioningState"])
		if state != "" && !strings.EqualFold(state, "Succeeded") {
			return contracts.WaitResult{State: state, RetryAfter: retryAfter(live.header)}, nil
		}
		attachments, err := resourceAttachments(a.client.subscription, target.kind.NativeType, object(live.data["properties"]))
		if err != nil {
			return contracts.WaitResult{}, err
		}
		retained := array(result.Data["retain_resources"])
		if len(retained) == 0 {
			return contracts.WaitResult{}, fmt.Errorf("Azure preparation has no retention readback targets")
		}
		for _, id := range retained {
			found := false
			for _, attachment := range attachments {
				if attachment.id == text(id) {
					found = true
					if attachment.delete {
						return contracts.WaitResult{State: "retaining_attachments", RetryAfter: 2 * time.Second}, nil
					}
				}
			}
			if !found {
				return contracts.WaitResult{}, fmt.Errorf("Azure preparation attachment disappeared")
			}
		}
	}
	next, err := a.Execute(ctx, request)
	if err != nil {
		return contracts.WaitResult{}, err
	}
	data := next.Data
	if data == nil {
		data = map[string]any{}
	}
	if text(data["phase"]) == "" {
		data["phase"] = "delete"
	}
	data["operation"] = next.ProviderOperationID
	return contracts.WaitResult{Data: data, State: text(data["phase"]), RetryAfter: 2 * time.Second}, nil
}

func (u attachmentUpdate) body() (map[string]any, error) {
	encoded, err := json.Marshal(u.live)
	if err != nil {
		return nil, err
	}
	var raw map[string]any
	if err := json.Unmarshal(encoded, &raw); err != nil {
		return nil, err
	}
	properties := object(raw["properties"])
	retained := map[string]bool{}
	for _, value := range u.retain {
		retained[value.id] = true
	}
	if u.asset.Identity.NativeType == nicType {
		for _, value := range array(properties["ipConfigurations"]) {
			ip := object(object(object(value)["properties"])["publicIPAddress"])
			if retained[strings.ToLower(text(ip["id"]))] {
				object(ip["properties"])["deleteOption"] = "Detach"
			}
		}
		return nicWritableResource(raw)
	}
	patch := map[string]any{}
	storage := object(properties["storageProfile"])
	storagePatch := map[string]any{}
	os := object(storage["osDisk"])
	if retained[strings.ToLower(text(object(os["managedDisk"])["id"]))] {
		os["deleteOption"] = "Detach"
		storagePatch["osDisk"] = os
	}
	for _, value := range array(storage["dataDisks"]) {
		disk := object(value)
		delete(disk, "diskIOPSReadWrite")
		delete(disk, "diskMBpsReadWrite")
		if retained[strings.ToLower(text(object(disk["managedDisk"])["id"]))] {
			disk["deleteOption"] = "Detach"
			storagePatch["dataDisks"] = storage["dataDisks"]
		}
	}
	if len(storagePatch) > 0 {
		patch["storageProfile"] = storagePatch
	}
	network := object(properties["networkProfile"])
	for _, value := range array(network["networkInterfaces"]) {
		nic := object(value)
		if retained[strings.ToLower(text(nic["id"]))] {
			object(nic["properties"])["deleteOption"] = "Detach"
			patch["networkProfile"] = map[string]any{"networkInterfaces": network["networkInterfaces"]}
		}
	}
	return map[string]any{"properties": patch}, nil
}

// NetworkInterfaces_CreateOrUpdate replaces the NIC. Preserve every writable
// NIC/IP/DNS field declared in the pinned 2024-05-01 schema, keep references by
// native ID, and reject unknown fields instead of silently erasing settings.
func nicWritableResource(raw map[string]any) (map[string]any, error) {
	copyFields := func(values map[string]any, writable, readOnly string) (map[string]any, error) {
		result := map[string]any{}
		for key, value := range values {
			if slices.Contains(strings.Fields(writable), key) {
				result[key] = value
			} else if !slices.Contains(strings.Fields(readOnly), key) {
				return nil, fmt.Errorf("Azure NIC contains an unrecognized field %q", key)
			}
		}
		return result, nil
	}
	root, err := copyFields(raw, "location tags extendedLocation", "id name type etag properties")
	if err != nil {
		return nil, err
	}
	properties, err := copyFields(object(raw["properties"]), "auxiliaryMode auxiliarySku disableTcpStateTracking dnsSettings enableAcceleratedNetworking enableIPForwarding ipConfigurations migrationPhase networkSecurityGroup nicType privateLinkService workloadType", "defaultOutboundConnectivityEnabled dscpConfiguration hostedWorkloads macAddress primary privateEndpoint provisioningState resourceGuid tapConfigurations virtualMachine vnetEncryptionSupported")
	if err != nil {
		return nil, err
	}
	ref := func(value any) (any, error) {
		if value == nil {
			return nil, nil
		}
		id := text(object(value)["id"])
		if id == "" {
			return nil, fmt.Errorf("Azure NIC configuration reference has no identity")
		}
		return map[string]any{"id": id}, nil
	}
	for _, key := range []string{"networkSecurityGroup", "privateLinkService"} {
		if value, found := properties[key]; found {
			properties[key], err = ref(value)
			if err != nil {
				return nil, err
			}
		}
	}
	if dns, found := properties["dnsSettings"]; found {
		properties["dnsSettings"], err = copyFields(object(dns), "dnsServers internalDnsNameLabel", "appliedDnsServers internalDomainNameSuffix internalFqdn")
		if err != nil {
			return nil, err
		}
	}
	var configs []any
	for _, value := range array(properties["ipConfigurations"]) {
		configuration, err := copyFields(object(value), "id name type", "etag properties")
		if err != nil {
			return nil, err
		}
		ip, err := copyFields(object(object(value)["properties"]), "applicationGatewayBackendAddressPools applicationSecurityGroups gatewayLoadBalancer loadBalancerBackendAddressPools loadBalancerInboundNatRules primary privateIPAddress privateIPAddressPrefixLength privateIPAddressVersion privateIPAllocationMethod publicIPAddress subnet virtualNetworkTaps", "privateLinkConnectionProperties provisioningState")
		if err != nil {
			return nil, err
		}
		for _, key := range []string{"subnet", "gatewayLoadBalancer"} {
			if value, found := ip[key]; found {
				ip[key], err = ref(value)
				if err != nil {
					return nil, err
				}
			}
		}
		for _, key := range []string{"applicationGatewayBackendAddressPools", "applicationSecurityGroups", "loadBalancerBackendAddressPools", "loadBalancerInboundNatRules", "virtualNetworkTaps"} {
			if values, found := ip[key]; found {
				items, ok := values.([]any)
				if !ok && values != nil {
					return nil, fmt.Errorf("invalid Azure NIC reference array %q", key)
				}
				references := []any{}
				for _, value := range items {
					reference, err := ref(value)
					if err != nil {
						return nil, err
					}
					references = append(references, reference)
				}
				ip[key] = references
			}
		}
		if value, found := ip["publicIPAddress"]; found && value != nil {
			public, err := ref(value)
			if err != nil {
				return nil, err
			}
			if option := object(object(value)["properties"])["deleteOption"]; option != nil {
				object(public)["properties"] = map[string]any{"deleteOption": option}
			}
			ip["publicIPAddress"] = public
		}
		configuration["properties"] = ip
		configs = append(configs, configuration)
	}
	if configs != nil {
		properties["ipConfigurations"] = configs
	}
	root["properties"] = properties
	return root, nil
}
