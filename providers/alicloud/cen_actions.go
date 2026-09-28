package alicloud

import (
	"context"
	"errors"
	"sort"
	"strings"

	"github.com/loomx-ai/steward/internal/provider/contracts"
)

// cenPreDeletePhase marks a CEN deletion that is still removing contents the
// provider requires to be gone first: non-default QoS queues of an
// inter-region traffic QoS policy, and the group sources, group members and
// vSwitch associations of a multicast domain.
const cenPreDeletePhase = "cen_pre_delete_cleanup"

const (
	cenDeleteQosQueueOperation          = "AlibabaCloud.CEN.DeleteCenInterRegionTrafficQosQueue"
	cenListMulticastGroupsOperation     = "AlibabaCloud.CEN.ListTransitRouterMulticastGroups"
	cenDeregisterGroupSourcesOperation  = "AlibabaCloud.CEN.DeregisterTransitRouterMulticastGroupSources"
	cenDeregisterGroupMembersOperation  = "AlibabaCloud.CEN.DeregisterTransitRouterMulticastGroupMembers"
	cenListMulticastAssociationsOp      = "AlibabaCloud.CEN.ListTransitRouterMulticastDomainAssociations"
	cenDisassociateMulticastDomainOp    = "AlibabaCloud.CEN.DisassociateTransitRouterMulticastDomain"
	cenDefaultQosQueueUndeletableCode   = "OperationFailed.NotSupportDeleteDefaultQueue"
	cenMulticastListPageLimit           = 100
	cenMulticastListPageSafetyThreshold = 1000
)

func cenPreDeleteCleanupType(nativeType string) bool {
	return nativeType == CENInterRegionTrafficQosPolicyNativeType ||
		nativeType == CENTransitRouterMulticastDomainNativeType
}

// advanceCENPreDeleteCleanup performs one round of the removals a CEN resource
// needs before its own deletion. handled is false once nothing remains, and
// the caller then deletes the resource.
func (h *ResourceAction) advanceCENPreDeleteCleanup(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, bool, error) {
	readback, _, resource, err := h.readbackDetails(ctx, request)
	if err != nil || !readback.Exists {
		return contracts.ActionResult{}, false, err
	}
	if !strings.EqualFold(strings.TrimSpace(readback.State), "Active") {
		return h.cenPreDeleteWaiting(readback.State, ""), true, nil
	}
	if h.nativeType == CENInterRegionTrafficQosPolicyNativeType {
		return h.deleteCENQosQueues(ctx, request, resource)
	}
	return h.clearCENMulticastDomain(ctx, request)
}

func (h *ResourceAction) cenPreDeleteWaiting(state, requestID string) contracts.ActionResult {
	return contracts.ActionResult{
		ProviderRequestID: requestID,
		Data:              map[string]any{"phase": cenPreDeletePhase, "state": state},
		RetryAfter:        h.pollInterval(),
	}
}

// deleteCENQosQueues deletes every queue except the default one.
// DeleteCenInterRegionTrafficQosPolicy fails with
// AssociationExist.TransitQosQueueExist while other queues exist, and the
// provider identifies the default queue only by refusing to delete it.
func (h *ResourceAction) deleteCENQosQueues(
	ctx context.Context,
	request contracts.ActionRequest,
	policy map[string]any,
) (contracts.ActionResult, bool, error) {
	// ponytail: the default queue's refusal is repeated each round; the policy
	// holds at most a handful of queues.
	for _, raw := range anySlice(policy["TrafficQosQueues"]) {
		queue, _ := raw.(map[string]any)
		queueID := strings.TrimSpace(stringValue(queue["QosQueueId"]))
		if queueID == "" {
			continue
		}
		result, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID, Operation: cenDeleteQosQueueOperation,
			Scope:          map[string]string{"region": h.region},
			Parameters:     map[string]any{"QosQueueId": queueID, "DryRun": false},
			IdempotencyKey: request.IdempotencyKey + ":delete-qos-queue:" + queueID,
		})
		if isNotFound(err) || providerErrorCode(err) == cenDefaultQosQueueUndeletableCode {
			continue
		}
		if err != nil {
			return contracts.ActionResult{}, false, err
		}
		waiting := h.cenPreDeleteWaiting("Modifying", result.RequestID)
		waiting.Data["deleted_qos_queue_id"] = queueID
		return waiting, true, nil
	}
	return contracts.ActionResult{}, false, nil
}

// clearCENMulticastDomain removes group sources and members first, since a
// vSwitch cannot leave the domain while it holds them, then the vSwitch
// associations. DeleteTransitRouterMulticastDomain rejects a domain that
// still has either.
func (h *ResourceAction) clearCENMulticastDomain(
	ctx context.Context,
	request contracts.ActionRequest,
) (contracts.ActionResult, bool, error) {
	domainID := request.Asset.Identity.NativeID
	groups, err := h.cenListAll(ctx, cenListMulticastGroupsOperation, domainID, "TransitRouterMulticastGroups")
	if err != nil {
		return contracts.ActionResult{}, false, err
	}
	type registration struct{ sources, members, peers []string }
	byGroup := map[string]*registration{}
	for _, group := range groups {
		status := strings.TrimSpace(stringValue(group["Status"]))
		if !strings.EqualFold(status, "Registered") {
			return h.cenPreDeleteWaiting(status, ""), true, nil
		}
		address := strings.TrimSpace(stringValue(group["GroupIpAddress"]))
		if byGroup[address] == nil {
			byGroup[address] = &registration{}
		}
		current := byGroup[address]
		networkInterface := strings.TrimSpace(stringValue(group["NetworkInterfaceId"]))
		peer := strings.TrimSpace(stringValue(group["PeerTransitRouterMulticastDomainId"]))
		if truthy(group["GroupSource"]) && networkInterface != "" {
			current.sources = append(current.sources, networkInterface)
		}
		if truthy(group["GroupMember"]) {
			if peer != "" {
				current.peers = append(current.peers, peer)
			} else if networkInterface != "" {
				current.members = append(current.members, networkInterface)
			}
		}
	}
	addresses := make([]string, 0, len(byGroup))
	for address := range byGroup {
		addresses = append(addresses, address)
	}
	sort.Strings(addresses)
	requestID := ""
	for _, address := range addresses {
		current := byGroup[address]
		calls := []struct {
			operation, key, parameter string
			values                    []string
		}{
			{cenDeregisterGroupSourcesOperation, "sources", "NetworkInterfaceIds", current.sources},
			{cenDeregisterGroupMembersOperation, "members", "NetworkInterfaceIds", current.members},
			{cenDeregisterGroupMembersOperation, "peers", "PeerTransitRouterMulticastDomains", current.peers},
		}
		for _, call := range calls {
			if len(call.values) == 0 {
				continue
			}
			result, err := h.provider.Invoke(ctx, contracts.Invocation{
				ConnectionID: h.connectionID, Operation: call.operation,
				Scope: map[string]string{"region": h.region},
				Parameters: map[string]any{
					"TransitRouterMulticastDomainId": domainID, "GroupIpAddress": address,
					call.parameter: call.values, "DryRun": false,
				},
				IdempotencyKey: request.IdempotencyKey + ":deregister-" + call.key + ":" + address + ":" + strings.Join(call.values, ","),
			})
			if err != nil && !isNotFound(err) {
				return contracts.ActionResult{}, false, err
			}
			requestID = result.RequestID
		}
	}
	if len(addresses) > 0 {
		return h.cenPreDeleteWaiting("Deregistering", requestID), true, nil
	}

	associations, err := h.cenListAll(ctx, cenListMulticastAssociationsOp, domainID, "TransitRouterMulticastAssociations")
	if err != nil {
		return contracts.ActionResult{}, false, err
	}
	vSwitches := map[string][]string{}
	for _, association := range associations {
		status := strings.TrimSpace(stringValue(association["Status"]))
		if !strings.EqualFold(status, "Associated") {
			return h.cenPreDeleteWaiting(status, ""), true, nil
		}
		attachmentID := strings.TrimSpace(stringValue(association["TransitRouterAttachmentId"]))
		if vSwitchID := strings.TrimSpace(stringValue(association["VSwitchId"])); attachmentID != "" && vSwitchID != "" {
			vSwitches[attachmentID] = append(vSwitches[attachmentID], vSwitchID)
		}
	}
	attachments := make([]string, 0, len(vSwitches))
	for attachmentID := range vSwitches {
		attachments = append(attachments, attachmentID)
	}
	sort.Strings(attachments)
	for _, attachmentID := range attachments {
		result, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID, Operation: cenDisassociateMulticastDomainOp,
			Scope: map[string]string{"region": h.region},
			Parameters: map[string]any{
				"TransitRouterMulticastDomainId": domainID, "TransitRouterAttachmentId": attachmentID,
				"VSwitchIds": vSwitches[attachmentID], "DryRun": false,
			},
			IdempotencyKey: request.IdempotencyKey + ":disassociate:" + attachmentID + ":" + strings.Join(vSwitches[attachmentID], ","),
		})
		if err != nil && !isNotFound(err) {
			return contracts.ActionResult{}, false, err
		}
		requestID = result.RequestID
	}
	if len(attachments) > 0 {
		return h.cenPreDeleteWaiting("Dissociating", requestID), true, nil
	}
	return contracts.ActionResult{}, false, nil
}

func (h *ResourceAction) cenListAll(
	ctx context.Context,
	operation string,
	domainID string,
	itemsPath string,
) ([]map[string]any, error) {
	var records []map[string]any
	token := ""
	for page := 0; page < cenMulticastListPageSafetyThreshold; page++ {
		parameters := map[string]any{
			"TransitRouterMulticastDomainId": domainID, "MaxResults": cenMulticastListPageLimit,
		}
		if token != "" {
			parameters["NextToken"] = token
		}
		result, err := h.provider.Invoke(ctx, contracts.Invocation{
			ConnectionID: h.connectionID, Operation: operation,
			Scope: map[string]string{"region": h.region}, Parameters: parameters,
		})
		if err != nil {
			return nil, err
		}
		for _, raw := range anySlice(valueAtPath(result.Data, itemsPath)) {
			if record, ok := raw.(map[string]any); ok {
				records = append(records, record)
			}
		}
		next := strings.TrimSpace(stringValue(result.Data["NextToken"]))
		if next == "" || next == token {
			return records, nil
		}
		token = next
	}
	return nil, errors.New("Alibaba Cloud CEN multicast listing exceeded 1000 pages")
}

func providerErrorCode(err error) string {
	var providerError *contracts.ProviderCallError
	if errors.As(err, &providerError) {
		return strings.TrimSpace(providerError.Provider.Code)
	}
	return ""
}
